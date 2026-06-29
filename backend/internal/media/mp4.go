package media

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"amdl/backend/internal/applemusic"
	"amdl/backend/internal/config"
	"github.com/zhaarey/go-mp4tag"
)

type sampleInfo struct {
	DescIndex int
	Data      []byte
	Duration  int
}

type songInfo struct {
	Codec         string
	Raw           []byte
	Samples       []sampleInfo
	NHML          string
	DecoderParams []byte
	CreationTime  time.Time
	ModifiedTime  time.Time
}

type MP4Processor struct {
	cfg config.Config
}

func newMP4Processor(cfg config.Config) *MP4Processor {
	return &MP4Processor{cfg: cfg}
}

func (p *MP4Processor) extractSong(ctx context.Context, raw []byte, codec string) (songInfo, error) {
	dir, err := os.MkdirTemp(p.cfg.Download.TempDir, "extract-*")
	if err != nil {
		return songInfo{}, err
	}
	defer os.RemoveAll(dir)
	name := "song"
	rawPath := filepath.Join(dir, name+".mp4")
	nhmlPath := filepath.Join(dir, name+".nhml")
	xmlPath := filepath.Join(dir, name+".xml")
	mediaPath := filepath.Join(dir, name+".media")
	if err := os.WriteFile(rawPath, raw, 0o644); err != nil {
		return songInfo{}, err
	}
	if err := run(ctx, p.cfg.Tools.GPAC, "-i", rawPath, "nhmlw:pckp=true", "-o", nhmlPath); err != nil {
		return songInfo{}, err
	}
	if err := run(ctx, p.cfg.Tools.MP4Box, "-diso", rawPath, "-out", xmlPath); err != nil {
		return songInfo{}, err
	}
	nhmlRaw, err := os.ReadFile(nhmlPath)
	if err != nil {
		return songInfo{}, err
	}
	xmlRaw, err := os.ReadFile(xmlPath)
	if err != nil {
		return songInfo{}, err
	}
	mediaRaw, err := os.ReadFile(mediaPath)
	if err != nil {
		return songInfo{}, err
	}
	var decoder []byte
	switch codec {
	case "alac":
		atomPath := filepath.Join(dir, name+".atom")
		if err := run(ctx, p.cfg.Tools.MP4Extract, "moov/trak/mdia/minf/stbl/stsd/enca[0]/alac", rawPath, atomPath); err != nil {
			return songInfo{}, err
		}
		decoder, _ = os.ReadFile(atomPath)
	case "aac", "aac-downmix", "aac-binaural":
		infoPath := filepath.Join(dir, name+".info")
		decoder, _ = os.ReadFile(infoPath)
	}
	samples, err := parseSamples(string(nhmlRaw), string(xmlRaw), mediaRaw)
	if err != nil {
		return songInfo{}, err
	}
	created, modified := parseMovieTimes(string(xmlRaw))
	return songInfo{Codec: codec, Raw: raw, Samples: samples, NHML: string(nhmlRaw), DecoderParams: decoder, CreationTime: created, ModifiedTime: modified}, nil
}

func (p *MP4Processor) encapsulate(ctx context.Context, info songInfo, decrypted []byte) ([]byte, error) {
	dir, err := os.MkdirTemp(p.cfg.Download.TempDir, "encap-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	name := "song"
	mediaPath := filepath.Join(dir, name+".media")
	if err := os.WriteFile(mediaPath, decrypted, 0o644); err != nil {
		return nil, err
	}
	outPath := filepath.Join(dir, name+".m4a")
	switch info.Codec {
	case "alac":
		nhmlPath := filepath.Join(dir, name+".nhml")
		nhml := replaceNHMLBase(info.NHML, filepath.Base(mediaPath))
		if err := os.WriteFile(nhmlPath, []byte(nhml), 0o644); err != nil {
			return nil, err
		}
		if err := run(ctx, p.cfg.Tools.GPAC, "-i", nhmlPath, "nhmlr", "-o", outPath); err != nil {
			return nil, err
		}
		atomPath := filepath.Join(dir, name+".atom")
		finalPath := filepath.Join(dir, name+"_final.m4a")
		if err := os.WriteFile(atomPath, info.DecoderParams, 0o644); err != nil {
			return nil, err
		}
		if err := run(ctx, p.cfg.Tools.MP4Edit, "--insert", "moov/trak/mdia/minf/stbl/stsd/alac:"+atomPath, outPath, finalPath); err != nil {
			return nil, err
		}
		outPath = finalPath
	case "aac", "aac-downmix", "aac-binaural":
		nhmlPath := filepath.Join(dir, name+".nhml")
		infoPath := filepath.Join(dir, name+".info")
		nhml := replaceNHMLBase(info.NHML, filepath.Base(mediaPath))
		nhml = replaceOrInsertAttr(nhml, "specificInfoFile", filepath.Base(infoPath))
		nhml = replaceOrInsertAttr(nhml, "streamType", "5")
		if err := os.WriteFile(infoPath, info.DecoderParams, 0o644); err != nil {
			return nil, err
		}
		if err := os.WriteFile(nhmlPath, []byte(nhml), 0o644); err != nil {
			return nil, err
		}
		if err := run(ctx, p.cfg.Tools.GPAC, "-i", nhmlPath, "nhmlr", "-o", outPath); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported codec %s", info.Codec)
	}
	_ = run(ctx, p.cfg.Tools.MP4Box, "-brand", "M4A ", "-ab", "M4A ", "-ab", "mp42", outPath)
	return os.ReadFile(outPath)
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

func (p *MP4Processor) fixESDS(ctx context.Context, rawSong, song []byte) ([]byte, error) {
	dir, err := os.MkdirTemp(p.cfg.Download.TempDir, "esds-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	rawPath := filepath.Join(dir, "raw.m4a")
	inPath := filepath.Join(dir, "in.m4a")
	atomPath := filepath.Join(dir, "esds.atom")
	outPath := filepath.Join(dir, "out.m4a")
	_ = os.WriteFile(rawPath, rawSong, 0o644)
	_ = os.WriteFile(inPath, song, 0o644)
	if err := run(ctx, p.cfg.Tools.MP4Extract, "moov/trak/mdia/minf/stbl/stsd/enca[0]/esds", rawPath, atomPath); err != nil {
		return nil, err
	}
	if err := run(ctx, p.cfg.Tools.MP4Edit, "--replace", "moov/trak/mdia/minf/stbl/stsd/mp4a/esds:"+atomPath, inPath, outPath); err != nil {
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

func (p *MP4Processor) writeMetadata(ctx context.Context, path string, song applemusic.Song, lyrics string, cover []byte, info songInfo) error {
	args := []string{"-name", "1=" + song.Name, "-itags", "tool=:" + "artist=AppleMusic"}
	var coverPath string
	if p.cfg.Download.EmbedCover && len(cover) > 0 {
		dir := filepath.Dir(path)
		coverPath = filepath.Join(dir, "cover."+p.cfg.Download.CoverFormat)
		if err := os.WriteFile(coverPath, cover, 0o644); err == nil {
			args[3] += ":cover=" + coverPath
			defer os.Remove(coverPath)
		}
	}
	args = append(args, path)
	_ = run(ctx, p.cfg.Tools.MP4Box, args...)

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
		if id, err := strconv.ParseUint(song.AlbumID, 10, 64); err == nil {
			tags.ItunesAlbumID = int32(id)
		}
	}
	if song.ArtistID != "" {
		if id, err := strconv.ParseUint(song.ArtistID, 10, 64); err == nil {
			tags.ItunesArtistID = int32(id)
		}
	}
	mp4, err := mp4tag.Open(path)
	if err != nil {
		return err
	}
	defer mp4.Close()
	return mp4.Write(tags, []string{})
}

func parseSamples(nhml, xml string, media []byte) ([]sampleInfo, error) {
	nhmlSamples := parseNHMLSamples(nhml)
	descIndexes := parseDescIndexes(xml)
	if len(nhmlSamples) == 0 {
		return nil, fmt.Errorf("no NHML samples extracted")
	}
	if len(descIndexes) == 0 {
		descIndexes = make([]int, len(nhmlSamples))
	}
	reader := bytes.NewReader(media)
	out := make([]sampleInfo, 0, len(nhmlSamples))
	for i, n := range nhmlSamples {
		data := make([]byte, n.length)
		if _, err := reader.Read(data); err != nil {
			return nil, err
		}
		desc := 0
		if i < len(descIndexes) {
			desc = descIndexes[i]
		}
		out = append(out, sampleInfo{DescIndex: desc, Data: data, Duration: n.duration})
	}
	return out, nil
}

type nhmlSample struct {
	length   int
	duration int
}

var nhmlSampleRe = regexp.MustCompile(`<NHNTSample\b[^>]*dataLength="(\d+)"[^>]*duration="(\d+)"`)

func parseNHMLSamples(nhml string) []nhmlSample {
	matches := nhmlSampleRe.FindAllStringSubmatch(nhml, -1)
	out := make([]nhmlSample, 0, len(matches))
	for _, m := range matches {
		out = append(out, nhmlSample{length: atoi(m[1]), duration: atoi(m[2])})
	}
	return out
}

var (
	moofRe = regexp.MustCompile(`(?s)<MovieFragmentBox\b.*?</MovieFragmentBox>`)
	tfhdRe = regexp.MustCompile(`<TrackFragmentHeaderBox\b[^>]*SampleDescriptionIndex="(\d+)"`)
	trunRe = regexp.MustCompile(`<TrackRunBox\b[^>]*SampleCount="(\d+)"`)
	mvhdRe = regexp.MustCompile(`<MovieHeaderBox\b[^>]*CreationTime="(\d+)"[^>]*ModificationTime="(\d+)"`)
)

func parseDescIndexes(xml string) []int {
	var out []int
	for _, moof := range moofRe.FindAllString(xml, -1) {
		desc := 0
		if m := tfhdRe.FindStringSubmatch(moof); len(m) == 2 {
			desc = max(0, atoi(m[1])-1)
		}
		for _, trun := range trunRe.FindAllStringSubmatch(moof, -1) {
			for i := 0; i < atoi(trun[1]); i++ {
				out = append(out, desc)
			}
		}
	}
	return out
}

func parseMovieTimes(xml string) (time.Time, time.Time) {
	if m := mvhdRe.FindStringSubmatch(xml); len(m) == 3 {
		return macTime(atoi(m[1])), macTime(atoi(m[2]))
	}
	return time.Now(), time.Now()
}

func macTime(seconds int) time.Time {
	return time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(seconds) * time.Second)
}

func replaceNHMLBase(nhml, mediaName string) string {
	return replaceOrInsertAttr(nhml, "baseMediaFile", mediaName)
}

func replaceOrInsertAttr(nhml, attr, value string) string {
	re := regexp.MustCompile(attr + `="[^"]*"`)
	if re.MatchString(nhml) {
		return re.ReplaceAllString(nhml, attr+`="`+value+`"`)
	}
	return strings.Replace(nhml, "<NHNTStream", "<NHNTStream "+attr+`="`+value+`"`, 1)
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
