package media

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/zhaarey/go-mp4tag"
)

// BenchmarkWriteMP4TagsIO isolates the metadata rewrite cost from fixture
// creation. The media payload is intentionally much larger than the moov so
// the benchmark catches regressions that accidentally send mdat back through
// go-mp4tag's whole-file temporary-copy path.
func BenchmarkWriteMP4TagsIO(b *testing.B) {
	const mediaBytes = 64 << 20
	payload := bytes.Repeat([]byte{0xa5}, mediaBytes)
	base := metadataTestMP4(payload, false)
	tags := &mp4tag.MP4Tags{
		Title:       "Benchmark Track",
		Artist:      "Benchmark Artist",
		Album:       "Benchmark Album",
		Lyrics:      "benchmark lyrics",
		TrackNumber: 1,
		TrackTotal:  10,
		Custom:      map[string]string{"ISRC": "BENCHMARK123", "LABEL": "Benchmark Label"},
		Pictures: []*mp4tag.MP4Picture{{
			Format: mp4tag.ImageTypeAuto,
			Data:   append([]byte("\xff\xd8\xff\xe0"), bytes.Repeat([]byte{0x5a}, 1<<20)...),
		}},
	}

	benchmarks := []struct {
		name  string
		write func(string, *mp4tag.MP4Tags) error
	}{
		{name: "legacy-whole-file", write: writeMP4TagsLegacy},
		{name: "tail-moov-fast-path", write: func(path string, tags *mp4tag.MP4Tags) error {
			used, err := writeMP4TagsFast(path, tags)
			if err == nil && !used {
				return fmt.Errorf("eligible benchmark fixture did not use fast path")
			}
			return err
		}},
	}
	for _, benchmark := range benchmarks {
		benchmark := benchmark
		b.Run(benchmark.name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "benchmark.m4a")
			b.ReportMetric(float64(mediaBytes)/(1<<20), "media-MiB")
			b.SetBytes(mediaBytes)
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := os.WriteFile(path, base, 0o644); err != nil {
					b.Fatal(err)
				}
				b.StartTimer()
				if err := benchmark.write(path, tags); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestWriteMP4TagsFastMatchesLegacyWithoutTouchingMedia(t *testing.T) {
	payload := bytes.Repeat([]byte("audio-payload-"), 256*1024)
	base := metadataTestMP4(payload, false)
	dir := t.TempDir()
	fastPath := filepath.Join(dir, "fast.m4a")
	legacyPath := filepath.Join(dir, "legacy.m4a")
	if err := os.WriteFile(fastPath, base, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, base, 0o644); err != nil {
		t.Fatal(err)
	}

	// Seed both files with tags that the update does not replace. This checks
	// that the shell path uses go-mp4tag's existing merge behavior rather than
	// merely serializing the incoming struct.
	seed := &mp4tag.MP4Tags{
		BPM:         123,
		Comment:     "preserved comment",
		Description: "preserved description",
		Custom:      map[string]string{"EXISTING": "preserved value"},
		Pictures: []*mp4tag.MP4Picture{{
			Format: mp4tag.ImageTypeAuto,
			Data:   append([]byte("\x89PNG"), []byte("old-cover")...),
		}},
	}
	for _, path := range []string{fastPath, legacyPath} {
		if err := writeMP4TagsLegacy(path, seed); err != nil {
			t.Fatalf("seed %s: %v", filepath.Base(path), err)
		}
	}

	before, err := os.ReadFile(fastPath)
	if err != nil {
		t.Fatal(err)
	}
	beforeBoxes, err := readTopLevelMP4Boxes(bytes.NewReader(before), int64(len(before)))
	if err != nil {
		t.Fatal(err)
	}
	_, beforeMoov, ok := metadataTailLayout(beforeBoxes, int64(len(before)))
	if !ok {
		t.Fatal("test fixture is not eligible for the metadata tail fast path")
	}
	beforeSTCO := append([]byte(nil), findTestMP4Box(before, "moov", "trak", "mdia", "minf", "stbl", "stco")...)
	if len(beforeSTCO) == 0 {
		t.Fatal("test fixture has no stco")
	}

	update := &mp4tag.MP4Tags{
		Album:           "Album",
		AlbumSort:       "Album Sort",
		AlbumArtist:     "Album Artist",
		AlbumArtistSort: "Album Artist Sort",
		Artist:          "Artist",
		ArtistSort:      "Artist Sort",
		Composer:        "Composer",
		ComposerSort:    "Composer Sort",
		Conductor:       "Conductor",
		Copyright:       "Copyright",
		CustomGenre:     "Genre",
		Date:            "2026-07-18T00:00:00Z",
		DiscNumber:      1,
		DiscTotal:       2,
		ItunesAdvisory:  mp4tag.ItunesAdvisoryExplicit,
		ItunesAlbumID:   123456,
		ItunesArtistID:  654321,
		Lyrics:          "line one\nline two",
		Publisher:       "Label",
		Title:           "Title",
		TitleSort:       "Title Sort",
		TrackNumber:     3,
		TrackTotal:      12,
		Custom: map[string]string{
			"PERFORMER":   "Artist",
			"RELEASETIME": "2026-07-18",
			"ISRC":        "TEST12345678",
			"LABEL":       "Label",
			"UPC":         "123456789012",
		},
		Pictures: []*mp4tag.MP4Picture{{
			Format: mp4tag.ImageTypeAuto,
			Data:   append([]byte("\x89PNG"), []byte("new-cover")...),
		}},
	}

	used, err := writeMP4TagsFast(fastPath, update)
	if err != nil {
		t.Fatalf("fast write: %v", err)
	}
	if !used {
		t.Fatal("eligible tail-moov file unexpectedly used legacy fallback")
	}
	if err := writeMP4TagsLegacy(legacyPath, update); err != nil {
		t.Fatalf("legacy write: %v", err)
	}

	after, err := os.ReadFile(fastPath)
	if err != nil {
		t.Fatal(err)
	}
	// The whole prefix, including the multi-megabyte mdat, must remain byte-for-
	// byte untouched. Only the final moov is eligible to change.
	if !bytes.Equal(after[:beforeMoov.start], before[:beforeMoov.start]) {
		t.Fatal("fast metadata rewrite changed bytes before the final moov")
	}
	if afterSTCO := findTestMP4Box(after, "moov", "trak", "mdia", "minf", "stbl", "stco"); !bytes.Equal(afterSTCO, beforeSTCO) {
		t.Fatal("fast metadata rewrite changed chunk offsets for mdat-before-moov layout")
	}

	fastTags := readTestMP4Tags(t, fastPath)
	legacyTags := readTestMP4Tags(t, legacyPath)
	if !reflect.DeepEqual(fastTags, legacyTags) {
		t.Fatalf("fast tags differ from legacy tags:\nfast:   %#v\nlegacy: %#v", fastTags, legacyTags)
	}

	// go-mp4tag currently does not expose every atom it writes through Read
	// (notably the sort fields). Compare a canonicalized multiset of raw ilst
	// children as well. Custom map iteration can reorder sibling ---- atoms, but
	// their bytes and all fixed iTunes atoms must match exactly.
	fastAtoms := canonicalTestILSTAtoms(t, after)
	legacyBytes, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyAtoms := canonicalTestILSTAtoms(t, legacyBytes)
	if !reflect.DeepEqual(fastAtoms, legacyAtoms) {
		t.Fatal("fast ilst atoms differ from go-mp4tag legacy output")
	}
}

func TestWriteMP4TagsFallsBackForMoovBeforeMedia(t *testing.T) {
	payload := []byte("media sample")
	ftyp := mp4Box("ftyp", append([]byte("M4A \x00\x00\x00\x00M4A "), []byte("mp42")...))
	// Build once to learn the stable moov size, then rebuild stco with the actual
	// media offset. The legacy writer must shift this value when ilst grows.
	moov := metadataTestMoov(len(payload), 0)
	mediaOffset := uint32(len(ftyp) + len(moov) + 8)
	moov = metadataTestMoov(len(payload), mediaOffset)
	fixture := append(append(ftyp, moov...), mp4Box("mdat", payload)...)

	path := filepath.Join(t.TempDir(), "faststart.m4a")
	if err := os.WriteFile(path, fixture, 0o644); err != nil {
		t.Fatal(err)
	}
	used, err := writeMP4TagsFast(path, &mp4tag.MP4Tags{Title: "must not be written"})
	if err != nil {
		t.Fatalf("probe fast path: %v", err)
	}
	if used {
		t.Fatal("moov-before-mdat layout must not use the tail fast path")
	}
	if err := writeMP4Tags(path, &mp4tag.MP4Tags{Title: "Fallback Title"}); err != nil {
		t.Fatalf("fallback write: %v", err)
	}
	if got := readTestMP4Tags(t, path).Title; got != "Fallback Title" {
		t.Fatalf("fallback title = %q, want %q", got, "Fallback Title")
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := readTopLevelMP4Boxes(bytes.NewReader(written), int64(len(written)))
	if err != nil {
		t.Fatal(err)
	}
	var mdat topLevelMP4Box
	for _, box := range boxes {
		if box.typ == "mdat" {
			mdat = box
		}
	}
	stco := findTestMP4Box(written, "moov", "trak", "mdia", "minf", "stbl", "stco")
	if len(stco) < 20 {
		t.Fatalf("invalid stco after fallback: %d bytes", len(stco))
	}
	if got, want := binary.BigEndian.Uint32(stco[16:20]), uint32(mdat.start+mdat.headerSize); got != want {
		t.Fatalf("fallback stco offset = %d, want mdat payload offset %d", got, want)
	}
}

func TestWriteMP4TagsFastRejectsTrailingBoxWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trailing-box.m4a")
	before := metadataTestMP4([]byte("media sample"), true)
	if err := os.WriteFile(path, before, 0o644); err != nil {
		t.Fatal(err)
	}

	used, err := writeMP4TagsFast(path, &mp4tag.MP4Tags{Title: "probe"})
	if err != nil {
		t.Fatalf("probe fast path: %v", err)
	}
	if used {
		t.Fatal("moov with a trailing top-level box must not use the fast path")
	}
	afterProbe, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterProbe, before) {
		t.Fatal("ineligible fast-path probe modified the source")
	}

	if err := writeMP4Tags(path, &mp4tag.MP4Tags{Title: "Fallback Title"}); err != nil {
		t.Fatalf("fallback write: %v", err)
	}
	if got := readTestMP4Tags(t, path).Title; got != "Fallback Title" {
		t.Fatalf("fallback title = %q, want %q", got, "Fallback Title")
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	boxes, err := readTopLevelMP4Boxes(bytes.NewReader(written), int64(len(written)))
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) == 0 || boxes[len(boxes)-1].typ != "free" {
		t.Fatal("legacy fallback did not preserve the trailing top-level box")
	}
}

func TestWriteMP4TagsRejectsUnsafeTopLevelSizesWithoutMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{
			name: "size-zero-moov",
			mutate: func(in []byte) []byte {
				out := append([]byte(nil), in...)
				boxes, err := readTopLevelMP4Boxes(bytes.NewReader(out), int64(len(out)))
				if err != nil {
					t.Fatal(err)
				}
				for _, box := range boxes {
					if box.typ == "moov" {
						binary.BigEndian.PutUint32(out[box.start:box.start+4], 0)
						return out
					}
				}
				t.Fatal("fixture has no moov")
				return nil
			},
		},
		{
			name: "extended-size-moov",
			mutate: func(in []byte) []byte {
				boxes, err := readTopLevelMP4Boxes(bytes.NewReader(in), int64(len(in)))
				if err != nil {
					t.Fatal(err)
				}
				for _, box := range boxes {
					if box.typ != "moov" {
						continue
					}
					out := make([]byte, 0, len(in)+8)
					out = append(out, in[:box.start]...)
					var header [16]byte
					binary.BigEndian.PutUint32(header[:4], 1)
					copy(header[4:8], "moov")
					binary.BigEndian.PutUint64(header[8:], uint64(box.size+8))
					out = append(out, header[:]...)
					out = append(out, in[box.start+8:]...)
					return out
				}
				t.Fatal("fixture has no moov")
				return nil
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := tc.mutate(metadataTestMP4([]byte("media sample"), false))
			path := filepath.Join(t.TempDir(), "unsafe.m4a")
			if err := os.WriteFile(path, before, 0o644); err != nil {
				t.Fatal(err)
			}
			err := writeMP4Tags(path, &mp4tag.MP4Tags{Title: "must not be written"})
			if !errors.Is(err, errUnsupportedMP4TagLayout) {
				t.Fatalf("write error = %v, want %v", err, errUnsupportedMP4TagLayout)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(after, before) {
				t.Fatal("unsafe-layout rejection modified the source")
			}
		})
	}
}

func TestWriteMP4TagsFastAcceptsExtendedMediaData(t *testing.T) {
	payload := bytes.Repeat([]byte("audio-payload-"), 1024)
	base := metadataTestMP4(payload, false)
	stco := findTestMP4Box(base, "moov", "trak", "mdia", "minf", "stbl", "stco")
	if len(stco) < 20 {
		t.Fatal("fixture has no usable stco")
	}
	// The larger mdat header moves its payload eight bytes later. Model a
	// valid source file by updating the existing chunk offset before replacing
	// the header; the fast metadata rewrite must leave that value alone.
	binary.BigEndian.PutUint32(stco[16:20], binary.BigEndian.Uint32(stco[16:20])+8)
	boxes, err := readTopLevelMP4Boxes(bytes.NewReader(base), int64(len(base)))
	if err != nil {
		t.Fatal(err)
	}
	var mdat topLevelMP4Box
	for _, box := range boxes {
		if box.typ == "mdat" {
			mdat = box
		}
	}
	if mdat.size == 0 {
		t.Fatal("fixture has no mdat")
	}

	fixture := make([]byte, 0, len(base)+8)
	fixture = append(fixture, base[:mdat.start]...)
	var header [16]byte
	binary.BigEndian.PutUint32(header[:4], 1)
	copy(header[4:8], "mdat")
	binary.BigEndian.PutUint64(header[8:], uint64(mdat.size+8))
	fixture = append(fixture, header[:]...)
	fixture = append(fixture, base[mdat.start+8:]...)

	path := filepath.Join(t.TempDir(), "extended-mdat.m4a")
	if err := os.WriteFile(path, fixture, 0o644); err != nil {
		t.Fatal(err)
	}
	used, err := writeMP4TagsFast(path, &mp4tag.MP4Tags{Title: "Extended mdat"})
	if err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Fatal("tail-moov file with extended mdat did not use fast path")
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written[:mdat.start+16+int64(len(payload))], fixture[:mdat.start+16+int64(len(payload))]) {
		t.Fatal("fast metadata rewrite changed extended mdat bytes")
	}
	writtenBoxes, err := readTopLevelMP4Boxes(bytes.NewReader(written), int64(len(written)))
	if err != nil {
		t.Fatal(err)
	}
	ftyp, moov, ok := metadataTailLayout(writtenBoxes, int64(len(written)))
	if !ok {
		t.Fatal("tagged extended-mdat fixture lost its tail-moov layout")
	}
	// go-mp4tag itself cannot read an extended top-level mdat. Verify the tag
	// through the same normalized metadata shell the fast path intentionally
	// uses, while the prefix assertion above covers the real media bytes.
	shell := make([]byte, 0, ftyp.size+8+moov.size)
	shell = append(shell, written[ftyp.start:ftyp.start+ftyp.size]...)
	shell = append(shell, []byte{0, 0, 0, 8, 'm', 'd', 'a', 't'}...)
	shell = append(shell, written[moov.start:moov.start+moov.size]...)
	shellPath := filepath.Join(t.TempDir(), "extended-mdat-shell.m4a")
	if err := os.WriteFile(shellPath, shell, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readTestMP4Tags(t, shellPath).Title; got != "Extended mdat" {
		t.Fatalf("title = %q, want %q", got, "Extended mdat")
	}
}

func metadataTestMP4(payload []byte, trailingBox bool) []byte {
	ftyp := mp4Box("ftyp", append([]byte("M4A \x00\x00\x00\x00M4A "), []byte("mp42")...))
	mediaOffset := uint32(len(ftyp) + 8)
	mdat := mp4Box("mdat", payload)
	moov := metadataTestMoov(len(payload), mediaOffset)
	out := append(append(ftyp, mdat...), moov...)
	if trailingBox {
		out = append(out, mp4Box("free", []byte("tail"))...)
	}
	return out
}

func metadataTestMoov(sampleSize int, mediaOffset uint32) []byte {
	base := minimalMoov("mp4a", sampleSize, mediaOffset)
	ilst := mp4Box("ilst", nil)
	meta := mp4Box("meta", append(fullBoxHeader(), ilst...))
	udta := mp4Box("udta", meta)
	return mp4Box("moov", append(append([]byte(nil), base[8:]...), udta...))
}

func readTestMP4Tags(t *testing.T, path string) *mp4tag.MP4Tags {
	t.Helper()
	track, err := mp4tag.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer track.Close()
	tags, err := track.Read()
	if err != nil {
		t.Fatal(err)
	}
	return tags
}

func canonicalTestILSTAtoms(t *testing.T, file []byte) [][]byte {
	t.Helper()
	ilst := findTestMP4Box(file, "moov", "udta", "meta", "ilst")
	if len(ilst) < 8 {
		t.Fatal("missing ilst")
	}
	atoms := splitTestMP4Boxes(t, ilst[8:])
	sort.Slice(atoms, func(i, j int) bool { return bytes.Compare(atoms[i], atoms[j]) < 0 })
	return atoms
}

func findTestMP4Box(data []byte, path ...string) []byte {
	if len(path) == 0 {
		return data
	}
	for offset := 0; offset+8 <= len(data); {
		size := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		if size < 8 || offset+size > len(data) {
			return nil
		}
		box := data[offset : offset+size]
		if string(box[4:8]) == path[0] {
			if len(path) == 1 {
				return box
			}
			children := box[8:]
			if path[0] == "meta" {
				if len(children) < 4 {
					return nil
				}
				children = children[4:]
			}
			return findTestMP4Box(children, path[1:]...)
		}
		offset += size
	}
	return nil
}

func splitTestMP4Boxes(t *testing.T, data []byte) [][]byte {
	t.Helper()
	var boxes [][]byte
	for offset := 0; offset < len(data); {
		if offset+8 > len(data) {
			t.Fatalf("truncated box header at ilst offset %d", offset)
		}
		size := int(binary.BigEndian.Uint32(data[offset : offset+4]))
		if size < 8 || offset+size > len(data) {
			t.Fatalf("invalid ilst child size %d at offset %d", size, offset)
		}
		boxes = append(boxes, append([]byte(nil), data[offset:offset+size]...))
		offset += size
	}
	return boxes
}
