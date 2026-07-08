package media

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"amdl/internal/config"
	"github.com/Eyevinn/mp4ff/mp4"
)

// TestEncapsulateRoundTripProducesPlayableFile exercises the full pure-Go MP4
// path end to end on a real fragmented MP4: parse the samples (extractSong),
// re-encapsulate them (encapsulate), flatten with ffmpeg (fixEncapsulate) and
// verify the result decodes cleanly (checkIntegrity).
//
// The input is an unencrypted fragmented AAC file, so the "decryption" step is
// the identity function (samples are passed straight through). This validates
// the mp4ff box parsing and re-muxing without needing the external decrypt
// wrapper or real Apple Music keys.
func TestEncapsulateRoundTripProducesPlayableFile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}

	tmp := t.TempDir()
	src := filepath.Join(tmp, "frag.m4a")
	// 2s sine tone, fragmented so the container has moof/mdat fragments.
	gen := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:a", "aac", "-b:a", "128k",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate fragmented input: %v: %s", err, out)
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{}
	cfg.Download.TempDir = tmp
	cfg.Tools.FFmpeg = "ffmpeg"
	p := newMP4Processor(cfg)
	ctx := context.Background()

	info, err := p.extractSong(ctx, raw, "aac")
	if err != nil {
		t.Fatalf("extractSong: %v", err)
	}
	if len(info.Samples) == 0 {
		t.Fatal("no samples extracted")
	}
	if len(info.fragments) == 0 {
		t.Fatal("no fragments retained")
	}

	// Identity "decryption": pass the extracted (already-plaintext) sample data
	// back through, matching the [][]byte shape the wrapper returns.
	decrypted := make([][]byte, len(info.Samples))
	for i, s := range info.Samples {
		decrypted[i] = s.Data
	}

	out, err := p.encapsulate(ctx, info, decrypted)
	if err != nil {
		t.Fatalf("encapsulate: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("encapsulate produced empty output")
	}

	flat, err := p.fixEncapsulate(ctx, out)
	if err != nil {
		t.Fatalf("fixEncapsulate: %v", err)
	}
	if !p.checkIntegrity(ctx, flat) {
		t.Fatal("integrity check failed on round-tripped output")
	}
}

// TestEncapsulateStripsEncryptionStructure verifies the encryption-specific
// transformation that replaces the old mp4extract/mp4edit/gpac pipeline: on a
// real CENC-encrypted fragmented MP4, encapsulate must convert the protected
// audio sample entry (enca) back to its original format (mp4a), drop the sinf
// and pssh boxes, and remove per-fragment senc boxes. The sample data is fed
// back as-is (still ciphertext) because only the container structure is under
// test here.
func TestEncapsulateStripsEncryptionStructure(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}

	tmp := t.TempDir()
	src := filepath.Join(tmp, "enc.mp4")
	gen := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:a", "aac", "-b:a", "128k",
		"-encryption_scheme", "cenc-aes-ctr",
		"-encryption_key", "76a6c65c5ea762046bd749a2e632ccbb",
		"-encryption_kid", "a7e61c373e219033c21091fa607bf3b8",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		src)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate encrypted input: %v: %s", err, out)
	}
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{}
	cfg.Download.TempDir = tmp
	cfg.Tools.FFmpeg = "ffmpeg"
	p := newMP4Processor(cfg)
	ctx := context.Background()

	// Sanity: the input really is encrypted (enca sample entry present).
	inFile, err := mp4.DecodeFile(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode input: %v", err)
	}
	if got := audioSampleEntryType(t, inFile.Moov); got != "enca" {
		t.Fatalf("input sample entry = %q, want enca (test fixture not encrypted)", got)
	}

	info, err := p.extractSong(ctx, raw, "aac")
	if err != nil {
		t.Fatalf("extractSong: %v", err)
	}
	decrypted := make([][]byte, len(info.Samples))
	for i, s := range info.Samples {
		decrypted[i] = s.Data
	}
	out, err := p.encapsulate(ctx, info, decrypted)
	if err != nil {
		t.Fatalf("encapsulate: %v", err)
	}

	outFile, err := mp4.DecodeFile(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode encapsulated output: %v", err)
	}
	stsd := outFile.Moov.Traks[0].Mdia.Minf.Stbl.Stsd
	if stsd.SampleCount != 1 || len(stsd.Children) != 1 {
		t.Fatalf("stsd not collapsed: SampleCount=%d children=%d", stsd.SampleCount, len(stsd.Children))
	}
	entry, ok := stsd.Children[0].(*mp4.AudioSampleEntryBox)
	if !ok {
		t.Fatalf("stsd child is %T, want *AudioSampleEntryBox", stsd.Children[0])
	}
	if entry.Type() != "mp4a" {
		t.Fatalf("sample entry type = %q, want mp4a (enca not stripped)", entry.Type())
	}
	if entry.Sinf != nil {
		t.Fatal("sinf box still present after decryption")
	}
	if outFile.Moov.Pssh != nil {
		t.Fatal("pssh box still present in moov")
	}
	// Fragments must no longer carry senc boxes.
	for _, seg := range outFile.Segments {
		for _, frag := range seg.Fragments {
			for _, traf := range frag.Moof.Trafs {
				if traf.Senc != nil || traf.UUIDSenc != nil {
					t.Fatal("senc box still present in fragment")
				}
			}
		}
	}
}

func audioSampleEntryType(t *testing.T, moov *mp4.MoovBox) string {
	t.Helper()
	stsd := moov.Traks[0].Mdia.Minf.Stbl.Stsd
	if len(stsd.Children) == 0 {
		t.Fatal("empty stsd")
	}
	return stsd.Children[0].Type()
}
