package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/zhaarey/go-mp4tag"
)

type sampleInfo struct {
	DescIndex int
	Data      []byte
	Duration  int
}

// songInfo carries the parsed, still-encrypted media between extractSong and
// encapsulate. The mp4ff init segment and fragments are retained so the
// decrypted samples can be written back into the original container structure
// without re-parsing.
type songInfo struct {
	Codec     string
	Samples   []sampleInfo
	init      *mp4.InitSegment
	fragments []*mp4.Fragment
	// fragSampleCounts[i] is the number of samples in fragments[i]; used to map
	// the flat decrypted sample slice back onto each fragment's mdat.
	fragSampleCounts []int
}

type MP4Processor struct {
	cfg config.Config
}

func newMP4Processor(cfg config.Config) *MP4Processor {
	return &MP4Processor{cfg: cfg}
}

// extractSong parses the encrypted fragmented MP4 downloaded from the CDN and
// pulls out the individual (still-encrypted) audio samples together with the
// per-fragment sample-description index used to select the decryption key.
// This replaces the previous gpac/MP4Box/mp4extract pipeline with pure-Go mp4ff
// box parsing.
func (p *MP4Processor) extractSong(_ context.Context, raw []byte, codec string) (songInfo, error) {
	r := bytes.NewReader(raw)
	var offset uint64
	init, offset, err := readInitSegment(r, offset)
	if err != nil {
		return songInfo{}, fmt.Errorf("read init segment: %w", err)
	}
	if init == nil || init.Moov == nil {
		return songInfo{}, errors.New("no moov box in init segment")
	}

	var trex *mp4.TrexBox
	if init.Moov.Mvex != nil && len(init.Moov.Mvex.Trexs) > 0 {
		trex = init.Moov.Mvex.Trexs[0]
	}
	defaultDescIndex := 1
	if trex != nil && trex.DefaultSampleDescriptionIndex > 0 {
		defaultDescIndex = int(trex.DefaultSampleDescriptionIndex)
	}

	var (
		samples   []sampleInfo
		fragments []*mp4.Fragment
		counts    []int
	)
	for {
		var frag *mp4.Fragment
		frag, offset, err = readNextFragment(r, offset)
		if err != nil {
			return songInfo{}, fmt.Errorf("read fragment: %w", err)
		}
		if frag == nil {
			break
		}
		full, err := frag.GetFullSamples(trex)
		if err != nil {
			return songInfo{}, fmt.Errorf("get fragment samples: %w", err)
		}

		// The sample-description index is signalled per fragment via tfhd; fall
		// back to the trex default when absent. It is 1-based in the container
		// and selects one of the (typically two) decryption keys.
		descIndex := defaultDescIndex
		if len(frag.Moof.Trafs) > 0 {
			tfhd := frag.Moof.Trafs[0].Tfhd
			if tfhd != nil && tfhd.HasSampleDescriptionIndex() {
				descIndex = int(tfhd.SampleDescriptionIndex)
			}
		}
		keyIndex := max(0, descIndex-1)

		for _, s := range full {
			samples = append(samples, sampleInfo{DescIndex: keyIndex, Data: s.Data, Duration: int(s.Dur)})
		}
		fragments = append(fragments, frag)
		counts = append(counts, len(full))
	}
	if len(samples) == 0 {
		return songInfo{}, errors.New("no samples extracted from media")
	}
	return songInfo{Codec: codec, Samples: samples, init: init, fragments: fragments, fragSampleCounts: counts}, nil
}

// encapsulate rebuilds a playable (fragmented) MP4 from the decrypted samples by
// stripping the encryption boxes from the init segment and fragments and writing
// the plaintext sample data back into each mdat. The decoder configuration
// (alac/mp4a/ec-3) is preserved from the original init segment, so no external
// muxer is required. The fragmented output is flattened to a regular MP4 by the
// subsequent ffmpeg copy pass (fixEncapsulate).
func (p *MP4Processor) encapsulate(_ context.Context, info songInfo, decrypted [][]byte) ([]byte, error) {
	if info.init == nil || info.init.Moov == nil {
		return nil, errors.New("missing init segment")
	}
	if len(decrypted) != len(info.Samples) {
		return nil, fmt.Errorf("decrypted sample count mismatch: got %d, want %d", len(decrypted), len(info.Samples))
	}

	// Convert encrypted sample entries (enca -> alac/mp4a/ec-3), drop sinf and
	// moov-level pssh boxes.
	if _, err := mp4.DecryptInit(info.init); err != nil {
		return nil, fmt.Errorf("decrypt init: %w", err)
	}
	// Drop encryption sample groups (seig) left in the sample table.
	for _, trak := range info.init.Moov.Traks {
		if trak.Mdia != nil && trak.Mdia.Minf != nil && trak.Mdia.Minf.Stbl != nil {
			stbl := trak.Mdia.Minf.Stbl
			stbl.Children = filterEncryptionSampleGroups(stbl.Children)
		}
	}
	// Apple Music encrypts with two identical sample entries (two IVs); after
	// decryption they are identical, so collapse to a single entry and point
	// every fragment at it.
	collapseSampleEntries(info.init)

	var buf bytes.Buffer
	if err := info.init.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode init segment: %w", err)
	}

	sampleIdx := 0
	for fi, frag := range info.fragments {
		n := info.fragSampleCounts[fi]
		var mdatData []byte
		for k := 0; k < n; k++ {
			mdatData = append(mdatData, decrypted[sampleIdx]...)
			sampleIdx++
		}
		if frag.Mdat == nil {
			return nil, fmt.Errorf("fragment %d has no mdat box", fi)
		}
		frag.Mdat.Data = mdatData

		// Strip per-fragment encryption boxes (senc/saiz/saio) and pssh, then
		// shrink trun.data_offset by the removed byte count. mp4ff Encode does
		// NOT recompute data_offset when it is already non-zero — leaving the
		// pre-strip value makes ffmpeg flatten read past the real mdat payload
		// (observed as leading zeroed ALAC packets after fixEncapsulate).
		var bytesRemoved uint64
		for _, traf := range frag.Moof.Trafs {
			bytesRemoved += traf.RemoveEncryptionBoxes()
			// Normalise the sample-description index to the single retained entry.
			if traf.Tfhd != nil && traf.Tfhd.HasSampleDescriptionIndex() {
				traf.Tfhd.SampleDescriptionIndex = 1
			}
		}
		_, psshRemoved := frag.Moof.RemovePsshs()
		bytesRemoved += psshRemoved
		if bytesRemoved > math.MaxInt32 {
			return nil, fmt.Errorf("fragment %d removed too many bytes for trun.data_offset adjustment: %d", fi, bytesRemoved)
		}
		removed := int32(bytesRemoved)
		for _, traf := range frag.Moof.Trafs {
			for _, trun := range traf.Truns {
				if trun.HasDataOffset() {
					trun.DataOffset -= removed
				}
			}
		}

		if err := frag.Encode(&buf); err != nil {
			return nil, fmt.Errorf("encode fragment %d: %w", fi, err)
		}
	}
	if sampleIdx != len(decrypted) {
		return nil, fmt.Errorf("consumed %d of %d decrypted samples", sampleIdx, len(decrypted))
	}
	return buf.Bytes(), nil
}

func (p *MP4Processor) fixEncapsulate(ctx context.Context, song []byte) ([]byte, error) {
	dir, err := os.MkdirTemp(p.cfg.Download.TempDir, "fix-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	inPath := filepath.Join(dir, "in.m4a")
	outPath := filepath.Join(dir, "out.m4a")
	if err := os.WriteFile(inPath, song, 0o644); err != nil {
		return nil, err
	}
	if err := run(ctx, p.cfg.Tools.FFmpeg, "-y", "-i", inPath, "-fflags", "+bitexact", "-map_metadata", "0", "-c:a", "copy", "-c:v", "copy", outPath); err != nil {
		return nil, err
	}
	return os.ReadFile(outPath)
}

func (p *MP4Processor) checkIntegrity(ctx context.Context, song []byte) bool {
	dir, err := os.MkdirTemp(p.cfg.Download.TempDir, "check-*")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	inPath := filepath.Join(dir, "song.m4a")
	_ = os.WriteFile(inPath, song, 0o644)
	cmd := exec.CommandContext(ctx, p.cfg.Tools.FFmpeg, "-y", "-v", "error", "-i", inPath, "-c:a", "pcm_s16le", "-f", "null", "/dev/null")
	out, err := cmd.CombinedOutput()
	return err == nil && len(out) == 0
}

func (p *MP4Processor) writeMetadata(_ context.Context, path string, song applemusic.Song, lyrics string, cover []byte, _ songInfo) error {
	tags := &mp4tag.MP4Tags{
		Title:       song.Name,
		Artist:      song.ArtistName,
		Album:       song.AlbumName,
		AlbumArtist: firstNonEmpty(song.AlbumArtist, song.ArtistName),
		Composer:    song.ComposerName,
		CustomGenre: firstGenre(song.GenreNames),
		Lyrics:      lyrics,
		TrackNumber: int16(song.TrackNumber),
		TrackTotal:  int16(song.TrackCount),
		DiscNumber:  int16(song.DiscNumber),
		DiscTotal:   int16(song.DiscCount),
		Date:        firstNonEmpty(song.AlbumRelease, song.ReleaseDate),
		Copyright:   song.Copyright,
		Publisher:   song.RecordLabel,
		Custom: map[string]string{
			"PERFORMER":   song.ArtistName,
			"RELEASETIME": song.ReleaseDate,
			"ISRC":        song.ISRC,
			"LABEL":       song.RecordLabel,
			"UPC":         song.UPC,
		},
	}
	tags.TitleSort = song.Name
	tags.ArtistSort = song.ArtistName
	tags.AlbumSort = song.AlbumName
	tags.AlbumArtistSort = firstNonEmpty(song.AlbumArtist, song.ArtistName)
	tags.ComposerSort = song.ComposerName
	if song.ContentRating == "explicit" {
		tags.ItunesAdvisory = mp4tag.ItunesAdvisoryExplicit
	} else if song.ContentRating == "clean" {
		tags.ItunesAdvisory = mp4tag.ItunesAdvisoryClean
	} else {
		tags.ItunesAdvisory = mp4tag.ItunesAdvisoryNone
	}
	if song.AlbumID != "" {
		if id, err := strconv.ParseInt(song.AlbumID, 10, 32); err == nil {
			tags.ItunesAlbumID = int32(id)
		}
	}
	if song.ArtistID != "" {
		if id, err := strconv.ParseInt(song.ArtistID, 10, 32); err == nil {
			tags.ItunesArtistID = int32(id)
		}
	}
	if p.cfg.Download.EmbedCover && len(cover) > 0 {
		tags.Pictures = []*mp4tag.MP4Picture{{Format: mp4tag.ImageTypeAuto, Data: cover}}
	}
	mp4, err := mp4tag.Open(path)
	if err != nil {
		return err
	}
	defer mp4.Close()
	return mp4.Write(tags, []string{})
}

// readInitSegment reads top-level boxes through the moov box that completes the
// initialization segment, tracking the running byte offset so subsequent
// fragment boxes report the correct absolute positions (required for mdat
// sample extraction).
func readInitSegment(r io.Reader, offset uint64) (*mp4.InitSegment, uint64, error) {
	init := mp4.NewMP4Init()
	for {
		box, err := mp4.DecodeBox(offset, r)
		if err != nil {
			return nil, offset, err
		}
		boxType := box.Type()
		if init.Ftyp == nil && boxType != "ftyp" {
			return nil, offset, fmt.Errorf("unexpected box type %s, expected ftyp", boxType)
		}
		init.AddChild(box)
		offset += box.Size()
		if boxType == "moov" {
			return init, offset, nil
		}
	}
}

// readNextFragment reads the next moof+mdat fragment. It returns (nil, offset,
// nil) at end of stream.
func readNextFragment(r io.Reader, offset uint64) (*mp4.Fragment, uint64, error) {
	frag := mp4.NewFragment()
	for {
		box, err := mp4.DecodeBox(offset, r)
		if errors.Is(err, io.EOF) {
			return nil, offset, nil
		}
		if err != nil {
			return nil, offset, err
		}
		boxType := box.Type()
		offset += box.Size()
		switch boxType {
		case "moof", "emsg", "prft":
			frag.AddChild(box)
		case "mdat":
			frag.AddChild(box)
			if frag.Moof == nil {
				return nil, offset, fmt.Errorf("mdat box without preceding moof (ends @ offset %d)", offset)
			}
			return frag, offset, nil
		default:
			// Ignore unexpected top-level boxes between fragments.
		}
	}
}

// filterEncryptionSampleGroups removes sbgp/sgpd boxes that describe encryption
// key assignment (grouping type seig/seam), leaving other sample groups (e.g.
// roll) untouched.
func filterEncryptionSampleGroups(children []mp4.Box) []mp4.Box {
	out := make([]mp4.Box, 0, len(children))
	for _, child := range children {
		switch box := child.(type) {
		case *mp4.SbgpBox:
			if box.GroupingType == "seig" || box.GroupingType == "seam" {
				continue
			}
		case *mp4.SgpdBox:
			if box.GroupingType == "seig" || box.GroupingType == "seam" {
				continue
			}
		}
		out = append(out, child)
	}
	return out
}

// collapseSampleEntries reduces a sample description table that holds multiple
// (now identical, post-decryption) entries down to a single entry. Apple Music
// ships two entries because two IVs are used to encrypt the track.
func collapseSampleEntries(init *mp4.InitSegment) {
	for _, trak := range init.Moov.Traks {
		if trak.Mdia == nil || trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil {
			continue
		}
		stsd := trak.Mdia.Minf.Stbl.Stsd
		if stsd == nil || len(stsd.Children) <= 1 {
			continue
		}
		stsd.Children = stsd.Children[:1]
		stsd.SampleCount = 1
		if init.Moov.Mvex != nil && trak.Tkhd != nil {
			for _, trex := range init.Moov.Mvex.Trexs {
				if trex.TrackID == trak.Tkhd.TrackID {
					trex.DefaultSampleDescriptionIndex = 1
				}
			}
		}
	}
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func firstGenre(genres []string) string {
	if len(genres) == 0 {
		return ""
	}
	return genres[0]
}
