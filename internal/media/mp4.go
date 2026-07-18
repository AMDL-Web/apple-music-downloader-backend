package media

import (
	"bufio"
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
func (p *MP4Processor) extractSong(_ context.Context, raw io.Reader, codec string) (songInfo, error) {
	// Buffer the reader so the many small mp4ff box reads don't each hit the
	// underlying *os.File; a bytes.Reader passed by tests is wrapped harmlessly.
	r := bufio.NewReaderSize(raw, 1<<20)
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
//
// The download path streams fragment-by-fragment via streamDecryptToFile
// instead; encapsulate (with extractSong) is retained as the buffered reference
// the round-trip tests decrypt and encode in one shot, and against which the
// streaming output is asserted byte-for-byte identical. Both share
// prepareDecryptedInit and encodeDecryptedFragment, so they cannot drift.
func (p *MP4Processor) encapsulate(_ context.Context, info songInfo, decrypted [][]byte) ([]byte, error) {
	if info.init == nil || info.init.Moov == nil {
		return nil, errors.New("missing init segment")
	}
	if len(decrypted) != len(info.Samples) {
		return nil, fmt.Errorf("decrypted sample count mismatch: got %d, want %d", len(decrypted), len(info.Samples))
	}

	if err := prepareDecryptedInit(info.init); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := info.init.Encode(&buf); err != nil {
		return nil, fmt.Errorf("encode init segment: %w", err)
	}

	sampleIdx := 0
	for fi, frag := range info.fragments {
		n := info.fragSampleCounts[fi]
		if err := encodeDecryptedFragment(&buf, fi, frag, decrypted[sampleIdx:sampleIdx+n]); err != nil {
			return nil, err
		}
		sampleIdx += n
	}
	if sampleIdx != len(decrypted) {
		return nil, fmt.Errorf("consumed %d of %d decrypted samples", sampleIdx, len(decrypted))
	}
	return buf.Bytes(), nil
}

// prepareDecryptedInit rewrites an init segment for plaintext output: it converts
// the encrypted sample entries (enca -> alac/mp4a/ec-3) and drops sinf/pssh via
// mp4ff's DecryptInit, removes the encryption sample groups (seig) left in the
// sample table, and collapses Apple's two identical (post-decryption) sample
// entries down to one. Shared by the buffered (encapsulate) and streaming
// (streamDecryptToFile) paths so both emit an identical container.
func prepareDecryptedInit(init *mp4.InitSegment) error {
	if _, err := mp4.DecryptInit(init); err != nil {
		return fmt.Errorf("decrypt init: %w", err)
	}
	for _, trak := range init.Moov.Traks {
		if trak.Mdia != nil && trak.Mdia.Minf != nil && trak.Mdia.Minf.Stbl != nil {
			stbl := trak.Mdia.Minf.Stbl
			stbl.Children = filterEncryptionSampleGroups(stbl.Children)
		}
	}
	// Apple Music encrypts with two identical sample entries (two IVs); after
	// decryption they are identical, so collapse to a single entry and point
	// every fragment at it.
	collapseSampleEntries(init)
	return nil
}

// encodeDecryptedFragment writes the fragment's plaintext sample data back into
// its mdat, strips the per-fragment encryption boxes (senc/saiz/saio) and pssh,
// fixes trun.data_offset for the removed bytes, and encodes the fragment to w.
// len(decrypted) must equal the fragment's sample count. fi is used only for
// error messages.
func encodeDecryptedFragment(w io.Writer, fi int, frag *mp4.Fragment, decrypted [][]byte) error {
	if frag.Mdat == nil {
		return fmt.Errorf("fragment %d has no mdat box", fi)
	}
	// Keep the wrapper's per-sample reply buffers as separate mdat parts. mp4ff
	// writes DataParts sequentially, so concatenating them into another
	// fragment-sized []byte only increases the peak heap without changing the
	// encoded container. Clear the encrypted payload first: Data and DataParts
	// are mutually exclusive representations of the same mdat body.
	frag.Mdat.Data = nil
	frag.Mdat.DataParts = decrypted

	// Strip per-fragment encryption boxes (senc/saiz/saio) and pssh, then shrink
	// trun.data_offset by the removed byte count. mp4ff Encode does NOT recompute
	// data_offset when it is already non-zero — leaving the pre-strip value makes
	// the ffmpeg flatten read past the real mdat payload (observed as leading
	// zeroed ALAC packets after flattening).
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
		return fmt.Errorf("fragment %d removed too many bytes for trun.data_offset adjustment: %d", fi, bytesRemoved)
	}
	removed := int32(bytesRemoved)
	for _, traf := range frag.Moof.Trafs {
		for _, trun := range traf.Truns {
			if trun.HasDataOffset() {
				trun.DataOffset -= removed
			}
		}
	}
	if err := frag.Encode(w); err != nil {
		return fmt.Errorf("encode fragment %d: %w", fi, err)
	}
	return nil
}

// streamDecryptToFile reads the encrypted fragmented MP4 from r one fragment at
// a time, decrypts each fragment's samples via decrypt, and writes the plaintext
// fragmented MP4 to outPath. Only a single fragment's samples are held in memory
// at once, so peak memory is independent of track length. All samples in a
// fragment share one key, selected by the fragment's tfhd sample-description
// index (falling back to the trex default). onProgress, if non-nil, is called
// after each fragment with the number of encrypted input bytes consumed so far.
func (p *MP4Processor) streamDecryptToFile(
	ctx context.Context,
	r io.Reader,
	outPath string,
	keys []string,
	decrypt func(key string, samples [][]byte) ([][]byte, error),
	onProgress func(consumed uint64),
) error {
	out, err := os.Create(outPath)
	if err != nil {
		return err
	}
	streamErr := p.streamDecryptToWriter(ctx, r, out, keys, decrypt, onProgress)
	closeErr := out.Close()
	if streamErr != nil {
		return streamErr
	}
	return closeErr
}

// streamDecryptToWriter is the common fragment-at-a-time decrypt/remux loop.
// The low-memory path writes it to a dec-* file; the high-memory path connects
// it directly to ffmpeg's stdin so the decrypted fragmented MP4 never exists
// as either a whole-track []byte or an intermediate file.
func (p *MP4Processor) streamDecryptToWriter(
	ctx context.Context,
	r io.Reader,
	w io.Writer,
	keys []string,
	decrypt func(key string, samples [][]byte) ([][]byte, error),
	onProgress func(consumed uint64),
) error {
	bw := bufio.NewWriterSize(w, 1<<20)
	br := bufio.NewReaderSize(r, 1<<20)

	var offset uint64
	init, offset, err := readInitSegment(br, offset)
	if err != nil {
		return fmt.Errorf("read init segment: %w", err)
	}
	if init == nil || init.Moov == nil {
		return errors.New("no moov box in init segment")
	}

	// Capture trex and the default sample-description index BEFORE
	// prepareDecryptedInit runs: collapseSampleEntries rewrites the trex default
	// index to 1, but the per-fragment key selection below needs the original
	// value so it picks the same key the buffered path would.
	var trex *mp4.TrexBox
	if init.Moov.Mvex != nil && len(init.Moov.Mvex.Trexs) > 0 {
		trex = init.Moov.Mvex.Trexs[0]
	}
	defaultDescIndex := 1
	if trex != nil && trex.DefaultSampleDescriptionIndex > 0 {
		defaultDescIndex = int(trex.DefaultSampleDescriptionIndex)
	}

	if err := prepareDecryptedInit(init); err != nil {
		return err
	}
	if err := init.Encode(bw); err != nil {
		return fmt.Errorf("encode init segment: %w", err)
	}

	fragCount := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var frag *mp4.Fragment
		frag, offset, err = readNextFragment(br, offset)
		if err != nil {
			return fmt.Errorf("read fragment: %w", err)
		}
		if frag == nil {
			break
		}
		full, err := frag.GetFullSamples(trex)
		if err != nil {
			return fmt.Errorf("get fragment samples: %w", err)
		}

		descIndex := defaultDescIndex
		if len(frag.Moof.Trafs) > 0 {
			tfhd := frag.Moof.Trafs[0].Tfhd
			if tfhd != nil && tfhd.HasSampleDescriptionIndex() {
				descIndex = int(tfhd.SampleDescriptionIndex)
			}
		}
		keyIndex := max(0, descIndex-1)
		if keyIndex >= len(keys) {
			keyIndex = 0
		}
		key := ""
		if len(keys) > 0 {
			key = keys[keyIndex]
		}

		sampleData := make([][]byte, len(full))
		for i := range full {
			sampleData[i] = full[i].Data
		}
		decrypted, err := decrypt(key, sampleData)
		if err != nil {
			return err
		}
		if len(decrypted) != len(full) {
			return fmt.Errorf("fragment %d: decrypted %d samples, want %d", fragCount, len(decrypted), len(full))
		}
		if err := encodeDecryptedFragment(bw, fragCount, frag, decrypted); err != nil {
			return err
		}
		fragCount++
		if onProgress != nil {
			onProgress(offset)
		}
	}
	if fragCount == 0 {
		return errors.New("no fragments extracted from media")
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return nil
}

// streamDecryptToFlatFile decrypts and re-encodes one fragment at a time and
// feeds the resulting fragmented MP4 directly to ffmpeg over stdin. The OS
// stdin pipe applies backpressure and reports a broken pipe if ffmpeg exits, so
// the producer cannot remain blocked behind a dead child. The high-memory
// path needs only the encrypted whole-track cache plus the current encrypted
// and decrypted fragments in Go's heap. No dec-* or fix-*/in.m4a copy is made.
func (p *MP4Processor) streamDecryptToFlatFile(
	ctx context.Context,
	r io.Reader,
	outPath string,
	keys []string,
	decrypt func(key string, samples [][]byte) ([][]byte, error),
	onProgress func(consumed uint64),
) error {
	cmd := exec.CommandContext(ctx, p.cfg.Tools.FFmpeg,
		"-y", "-f", "mp4", "-i", "pipe:0",
		"-fflags", "+bitexact", "-map_metadata", "0",
		"-c:a", "copy", "-c:v", "copy", "-f", "mp4", "-brand", "M4A ", outPath)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("open streamed flatten stdin: %w", err)
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start streamed flatten: %w", err)
	}

	streamErr := p.streamDecryptToWriter(ctx, r, stdin, keys, decrypt, onProgress)
	_ = stdin.Close()
	waitErr := cmd.Wait()

	if streamErr != nil {
		if waitErr != nil {
			return fmt.Errorf("stream media: %v; streamed flatten: %w: %s", streamErr, waitErr, strings.TrimSpace(stderr.String()))
		}
		return streamErr
	}
	if waitErr != nil {
		return fmt.Errorf("streamed flatten: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// flattenToFile flattens the fragmented MP4 in `song` into a regular progressive
// MP4 written directly to outPath, without reading the flattened result back
// into memory. The download path uses this so a whole track's flattened bytes
// never exist in RAM; fixEncapsulate keeps the byte-returning form for tests and
// the ALAC-repair fallback.
func (p *MP4Processor) flattenToFile(ctx context.Context, song []byte, outPath string) error {
	dir, err := os.MkdirTemp(p.cfg.Download.TempDir, "fix-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	inPath := filepath.Join(dir, "in.m4a")
	if err := os.WriteFile(inPath, song, 0o644); err != nil {
		return err
	}
	return p.flattenFileToFile(ctx, inPath, outPath)
}

// flattenFileToFile flattens the fragmented MP4 at inPath into a regular
// progressive MP4 at outPath. The streaming decrypt path writes its fragmented
// output to a file and feeds it here directly, so nothing round-trips through a
// []byte.
func (p *MP4Processor) flattenFileToFile(ctx context.Context, inPath, outPath string) error {
	// -f mp4 is required: ffmpeg infers the muxer from the .m4a extension
	// otherwise, which selects the "ipod" muxer. That muxer's codec tag table
	// has no entry for eac3 (EC-3/Dolby Atmos), so it refuses to write the
	// header ("Could not find tag for codec eac3 ... not currently supported
	// in container") and every EC-3 download fails at this step. The generic
	// mp4 muxer supports the same tags plus ec-3; -brand "M4A " keeps the
	// ftyp major_brand the ipod muxer would have written (its default is
	// "isom" otherwise), matching the M4A brand callers document below.
	return run(ctx, p.cfg.Tools.FFmpeg, "-y", "-i", inPath, "-fflags", "+bitexact", "-map_metadata", "0", "-c:a", "copy", "-c:v", "copy", "-f", "mp4", "-brand", "M4A ", outPath)
}

func (p *MP4Processor) fixEncapsulate(ctx context.Context, song []byte) ([]byte, error) {
	dir, err := os.MkdirTemp(p.cfg.Download.TempDir, "fix-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	outPath := filepath.Join(dir, "out.m4a")
	if err := p.flattenToFile(ctx, song, outPath); err != nil {
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
	return p.checkIntegrityFile(ctx, inPath)
}

// checkIntegrityFile runs the same full-decode ffmpeg check as checkIntegrity
// directly against a file on disk, so the download path can verify the flattened
// .part file without first reading it back into memory.
func (p *MP4Processor) checkIntegrityFile(ctx context.Context, path string) bool {
	cmd := exec.CommandContext(ctx, p.cfg.Tools.FFmpeg, "-y", "-v", "error", "-i", path, "-c:a", "pcm_s16le", "-f", "null", "/dev/null")
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
