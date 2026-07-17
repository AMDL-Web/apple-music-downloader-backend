package media

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"github.com/Eyevinn/mp4ff/mp4"
)

// encryptedFixture pairs the raw bytes of a genuinely CENC-encrypted
// fragmented MP4 with the per-sample IVs used to encrypt it (captured at
// build time, before any round trip through our own box parser), so a test
// can perform the same real AES-CTR decryption the production wrapper would.
type encryptedFixture struct {
	raw         []byte
	fragmentIVs [][]mp4.InitializationVector // per fragment, per sample
}

// buildEncryptedFixture takes a plain fragmented MP4 produced by ffmpeg and
// re-encrypts it using mp4ff's own CENC protect helpers (InitProtect +
// EncryptFragment), which is the same library our production decrypt path
// (extractSong/encapsulate) is built on. This produces a fixture with real
// per-fragment senc/saio/saiz/tenc/sinf boxes -- unlike ffmpeg's own
// "-encryption_scheme cenc-aes-ctr" CLI flag, which (as of ffmpeg 6.1) does
// not populate per-fragment senc/saio/saiz for these single-fragment test
// clips, making it unsuitable for exercising the traf-level box-stripping
// logic under test.
func buildEncryptedFixture(t *testing.T, plain []byte, key, iv []byte, kid mp4.UUID) encryptedFixture {
	t.Helper()
	file, err := mp4.DecodeFile(bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("decode plain fixture: %v", err)
	}
	if file.Init == nil || file.Init.Moov == nil {
		t.Fatalf("plain fixture missing init segment")
	}
	ipd, err := mp4.InitProtect(file.Init, key, iv, "cenc", kid, nil)
	if err != nil {
		t.Fatalf("InitProtect: %v", err)
	}
	return encryptAndEncode(t, file, key, iv, ipd)
}

// buildALACEncryptedFixture mirrors buildEncryptedFixture but for ALAC.
// mp4ff has no registered box decoder for a bare "alac" sample entry (only
// the CENC-wrapped "enca" form is registered, since real Apple Music content
// is always DRM-wrapped at rest), so ffmpeg's plain "alac"-named stsd entry
// decodes as an opaque mp4.UnknownBox and mp4.InitProtect's type switch
// (which requires *mp4.AudioSampleEntryBox) rejects it. To model what real
// Apple ALAC content actually looks like on the wire -- an "enca" sample
// entry wrapping sinf/tenc plus the original "alac" decoder-config atom as
// an opaque child -- this rebuilds the sample entry by hand from the raw
// bytes of ffmpeg's unencrypted "alac" box.
func buildALACEncryptedFixture(t *testing.T, plain []byte, key, iv []byte, kid mp4.UUID) encryptedFixture {
	t.Helper()
	file, err := mp4.DecodeFile(bytes.NewReader(plain))
	if err != nil {
		t.Fatalf("decode plain fixture: %v", err)
	}
	if file.Init == nil || file.Init.Moov == nil {
		t.Fatalf("plain fixture missing init segment")
	}
	stsd := file.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd
	if len(stsd.Children) == 0 {
		t.Fatalf("plain ALAC fixture has empty stsd")
	}
	orig, ok := stsd.Children[0].(*mp4.UnknownBox)
	if !ok {
		t.Fatalf("plain ALAC stsd entry decoded as %T, want *mp4.UnknownBox (mp4ff added alac support upstream? update this test)", stsd.Children[0])
	}
	body := orig.Payload()
	// ISO/IEC 14496-12 8.5.2/12.2.3: 6 reserved + 2 data_reference_index +
	// 8 reserved + 2 channelcount + 2 samplesize + 2 pre_defined + 2
	// reserved + 4 samplerate (16.16 fixed point) = 28 bytes, followed by
	// the nested "alac" decoder-config atom.
	if len(body) < 28 {
		t.Fatalf("ALAC sample entry body too short: %d bytes", len(body))
	}
	channelCount := binary.BigEndian.Uint16(body[16:18])
	sampleSize := binary.BigEndian.Uint16(body[18:20])
	sampleRate := uint16(binary.BigEndian.Uint32(body[24:28]) >> 16)
	configAtom := body[28:]
	if len(configAtom) < 8 || string(configAtom[4:8]) != "alac" {
		t.Fatalf("expected nested alac config atom, got %q", configAtom[4:8])
	}
	configBox := mp4.CreateUnknownBox("alac", uint64(len(configAtom)), configAtom[8:])

	enca := mp4.CreateAudioSampleEntryBox("enca", channelCount, sampleSize, sampleRate, configBox)
	tenc := &mp4.TencBox{Version: 0, DefaultIsProtected: 1, DefaultPerSampleIVSize: 16, DefaultKID: kid}
	schi := &mp4.SchiBox{}
	schi.AddChild(tenc)
	sinf := &mp4.SinfBox{}
	sinf.AddChild(&mp4.FrmaBox{DataFormat: "alac"})
	sinf.AddChild(&mp4.SchmBox{SchemeType: "cenc", SchemeVersion: 65536})
	sinf.AddChild(schi)
	enca.AddChild(sinf)

	stsd.Children[0] = enca
	stsd.Enca = enca

	ipd := &mp4.InitProtectData{
		Tenc:     tenc,
		ProtFunc: func(sample []byte, scheme string) ([]mp4.SubSamplePattern, error) { return nil, nil },
		Trex:     file.Init.Moov.Mvex.Trex,
		Scheme:   "cenc",
	}
	return encryptAndEncode(t, file, key, iv, ipd)
}

func encryptAndEncode(t *testing.T, file *mp4.File, key, iv []byte, ipd *mp4.InitProtectData) encryptedFixture {
	t.Helper()
	var allIVs [][]mp4.InitializationVector
	for _, seg := range file.Segments {
		for _, frag := range seg.Fragments {
			if err := mp4.EncryptFragment(frag, key, iv, ipd); err != nil {
				t.Fatalf("EncryptFragment: %v", err)
			}
			senc := frag.Moof.Trafs[0].Senc
			ivs := make([]mp4.InitializationVector, len(senc.IVs))
			copy(ivs, senc.IVs)
			allIVs = append(allIVs, ivs)
		}
	}
	var buf bytes.Buffer
	if err := file.Encode(&buf); err != nil {
		t.Fatalf("encode encrypted fixture: %v", err)
	}
	return encryptedFixture{raw: buf.Bytes(), fragmentIVs: allIVs}
}

func decryptCTR(t *testing.T, key, iv, data []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	fullIV := make([]byte, 16)
	copy(fullIV, iv)
	out := make([]byte, len(data))
	cipher.NewCTR(block, fullIV).XORKeyStream(out, data)
	return out
}

// TestCodecRoundTripThroughRealEncryption exercises the full production
// pipeline (extractSong -> encapsulate -> fixEncapsulate -> checkIntegrity)
// against a genuinely CENC-encrypted fixture built with mp4ff's own protect
// helpers, then genuinely decrypts each sample using the per-sample IVs used
// at encryption time (as the real wrapper's output would look), for every
// codec family the downloader supports end to end through the shared
// mp4ff-based pipeline: AAC (mp4a), ALAC, EC-3, and AC-3.
func TestCodecRoundTripThroughRealEncryption(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	key := make([]byte, 16)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	iv := make([]byte, 8)
	if _, err := rand.Read(iv); err != nil {
		t.Fatal(err)
	}
	kid := mp4.UUID(make([]byte, 16))
	if _, err := rand.Read(kid); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name         string
		codec        string
		encoder      string
		movflags     string
		alac         bool
		duration     string
		fragDuration string // forces multiple moof/mdat fragments per file, like a multi-fragment HLS segment
	}{
		{name: "aac", codec: "aac", encoder: "aac", movflags: "frag_keyframe+empty_moov+default_base_moof", duration: "2"},
		{name: "alac", codec: "alac", encoder: "alac", movflags: "frag_keyframe+empty_moov+default_base_moof", alac: true, duration: "2"},
		{name: "ec3", codec: "ec3", encoder: "eac3", movflags: "frag_keyframe+empty_moov+default_base_moof+delay_moov", duration: "2"},
		{name: "ac3", codec: "ac3", encoder: "ac3", movflags: "frag_keyframe+empty_moov+default_base_moof+delay_moov", duration: "2"},
		{name: "aac-multifrag", codec: "aac", encoder: "aac", movflags: "frag_keyframe+empty_moov+default_base_moof", duration: "5", fragDuration: "1000000"},
		{name: "alac-multifrag", codec: "alac", encoder: "alac", movflags: "frag_keyframe+empty_moov+default_base_moof", alac: true, duration: "5", fragDuration: "1000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			src := filepath.Join(tmp, "plain.m4a")
			args := []string{"-y", "-v", "error",
				"-f", "lavfi", "-i", "sine=frequency=440:duration=" + tc.duration,
				"-c:a", tc.encoder, "-f", "mp4",
				"-movflags", tc.movflags,
			}
			if tc.fragDuration != "" {
				args = append(args, "-frag_duration", tc.fragDuration)
			}
			args = append(args, src)
			gen := exec.Command("ffmpeg", args...)
			if out, err := gen.CombinedOutput(); err != nil {
				t.Fatalf("generate plain input: %v: %s", err, out)
			}
			plain, err := os.ReadFile(src)
			if err != nil {
				t.Fatal(err)
			}

			var fixture encryptedFixture
			if tc.alac {
				fixture = buildALACEncryptedFixture(t, plain, key, iv, kid)
			} else {
				fixture = buildEncryptedFixture(t, plain, key, iv, kid)
			}

			cfg := config.Config{}
			cfg.Download.TempDir = tmp
			cfg.Tools.FFmpeg = "ffmpeg"
			p := newMP4Processor(cfg)
			ctx := context.Background()

			info, err := p.extractSong(ctx, bytes.NewReader(fixture.raw), tc.codec)
			if err != nil {
				t.Fatalf("extractSong: %v", err)
			}
			t.Logf("samples: %d fragments: %d", len(info.Samples), len(info.fragments))
			if len(info.fragments) != len(fixture.fragmentIVs) {
				t.Fatalf("fragment count mismatch: extracted %d, encrypted %d", len(info.fragments), len(fixture.fragmentIVs))
			}

			decrypted := make([][]byte, len(info.Samples))
			sampleIdx := 0
			for fi := range info.fragments {
				ivs := fixture.fragmentIVs[fi]
				if len(ivs) != info.fragSampleCounts[fi] {
					t.Fatalf("fragment %d: %d IVs for %d samples", fi, len(ivs), info.fragSampleCounts[fi])
				}
				for i := range ivs {
					decrypted[sampleIdx] = decryptCTR(t, key, ivs[i], info.Samples[sampleIdx].Data)
					sampleIdx++
				}
			}
			if sampleIdx != len(info.Samples) {
				t.Fatalf("decrypted %d of %d samples", sampleIdx, len(info.Samples))
			}

			out, err := p.encapsulate(ctx, info, decrypted)
			if err != nil {
				t.Fatalf("encapsulate: %v", err)
			}
			t.Logf("encapsulated size: %d bytes", len(out))

			// The production download path (downloadEnhancedCodec) does not use
			// the buffered extractSong+encapsulate pair above; it streams the
			// decrypt fragment by fragment via streamDecryptToFile so peak memory
			// is one fragment rather than a whole track. Drive that path over the
			// same fixture, decrypting each fragment's samples with the IVs used
			// at encryption time, and assert it produces byte-identical output to
			// the buffered path already validated below (flatten + integrity +
			// tagging). This covers the streaming read/key-select/encode loop for
			// both single- and multi-fragment inputs across every codec.
			streamedPath := filepath.Join(tmp, "streamed.mp4")
			fragIdx := 0
			streamErr := p.streamDecryptToFile(ctx, bytes.NewReader(fixture.raw), streamedPath, []string{"unused-key"},
				func(_ string, samples [][]byte) ([][]byte, error) {
					ivs := fixture.fragmentIVs[fragIdx]
					fragIdx++
					got := make([][]byte, len(samples))
					for i := range samples {
						got[i] = decryptCTR(t, key, ivs[i], samples[i])
					}
					return got, nil
				}, nil)
			if streamErr != nil {
				t.Fatalf("streamDecryptToFile: %v", streamErr)
			}
			if fragIdx != len(fixture.fragmentIVs) {
				t.Fatalf("streaming decrypted %d fragments, want %d", fragIdx, len(fixture.fragmentIVs))
			}
			streamed, err := os.ReadFile(streamedPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(streamed, out) {
				t.Fatalf("streamed output (%d bytes) differs from buffered encapsulate (%d bytes)", len(streamed), len(out))
			}

			flat, err := p.fixEncapsulate(ctx, out)
			if err != nil {
				t.Fatalf("fixEncapsulate: %v", err)
			}
			t.Logf("flattened size: %d bytes", len(flat))

			// High-memory production mode keeps the encrypted input in RAM but
			// still decrypts one fragment at a time. Its fragmented output is piped
			// directly into ffmpeg, avoiding both dec-* and fix-*/in.m4a. A
			// non-seekable ffmpeg input can produce different MP4 timing-table bytes
			// than the identical seekable input, so compare decoded audio below rather
			// than requiring byte-identical container metadata.
			highFlatPath := filepath.Join(tmp, "high-flat.m4a")
			fragIdx = 0
			highErr := p.streamDecryptToFlatFile(ctx, bytes.NewReader(fixture.raw), highFlatPath, []string{"unused-key"},
				func(_ string, samples [][]byte) ([][]byte, error) {
					ivs := fixture.fragmentIVs[fragIdx]
					fragIdx++
					got := make([][]byte, len(samples))
					for i := range samples {
						got[i] = decryptCTR(t, key, ivs[i], samples[i])
					}
					return got, nil
				}, nil)
			if highErr != nil {
				t.Fatalf("streamDecryptToFlatFile: %v", highErr)
			}
			if fragIdx != len(fixture.fragmentIVs) {
				t.Fatalf("high-memory path decrypted %d fragments, want %d", fragIdx, len(fixture.fragmentIVs))
			}
			highFlat, err := os.ReadFile(highFlatPath)
			if err != nil {
				t.Fatal(err)
			}
			if len(highFlat) < 12 || string(highFlat[8:12]) != "M4A " {
				got := "<too short>"
				if len(highFlat) >= 12 {
					got = string(highFlat[8:12])
				}
				t.Fatalf("high-memory ftyp major_brand = %q, want \"M4A \"", got)
			}
			if !p.checkIntegrityFile(ctx, highFlatPath) {
				t.Fatal("high-memory flat output failed integrity check")
			}
			if tc.name == "alac-multifrag" && runtime.GOOS != "windows" {
				// A child that exits before consuming stdin must make the producer's
				// next write fail, not leave it blocked forever behind an unread io.Pipe.
				failFFmpeg := filepath.Join(tmp, "fail-ffmpeg")
				if err := os.WriteFile(failFFmpeg, []byte("#!/bin/sh\nexit 17\n"), 0o755); err != nil {
					t.Fatal(err)
				}
				failCfg := cfg
				failCfg.Tools.FFmpeg = failFFmpeg
				failProcessor := newMP4Processor(failCfg)
				done := make(chan error, 1)
				go func() {
					done <- failProcessor.streamDecryptToFlatFile(ctx, bytes.NewReader(fixture.raw), filepath.Join(tmp, "must-fail.m4a"), []string{"unused-key"},
						func(_ string, samples [][]byte) ([][]byte, error) { return samples, nil }, nil)
				}()
				select {
				case err := <-done:
					if err == nil || !bytes.Contains([]byte(err.Error()), []byte("exit status 17")) {
						t.Fatalf("early ffmpeg exit error = %v, want exit status 17", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("streamDecryptToFlatFile blocked after ffmpeg exited")
				}
			}
			decode := func(path string) []byte {
				cmd := exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-i", path, "-map", "0:a:0", "-f", "s16le", "pipe:1")
				pcm, decodeErr := cmd.Output()
				if decodeErr != nil {
					t.Fatalf("decode %s: %v", filepath.Base(path), decodeErr)
				}
				return pcm
			}
			referencePath := filepath.Join(tmp, "reference-flat.m4a")
			if err := os.WriteFile(referencePath, flat, 0o644); err != nil {
				t.Fatal(err)
			}
			if highPCM, referencePCM := decode(highFlatPath), decode(referencePath); !bytes.Equal(highPCM, referencePCM) {
				t.Fatalf("high-memory decoded audio (%d bytes) differs from reference (%d bytes)", len(highPCM), len(referencePCM))
			}

			// downloader.go documents fixEncapsulate as writing an "M4A " ftyp
			// brand; the generic mp4 muxer needed for EC-3 support defaults to
			// "isom" instead, so -brand must be passed explicitly to keep that
			// comment accurate.
			if len(flat) < 12 || string(flat[8:12]) != "M4A " {
				got := "<too short>"
				if len(flat) >= 12 {
					got = string(flat[8:12])
				}
				t.Fatalf("ftyp major_brand = %q, want \"M4A \"", got)
			}

			if !p.checkIntegrity(ctx, flat) {
				dir, _ := os.MkdirTemp(cfg.Download.TempDir, "diag-*")
				inPath := filepath.Join(dir, "song.m4a")
				_ = os.WriteFile(inPath, flat, 0o644)
				cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-v", "error", "-i", inPath, "-c:a", "pcm_s16le", "-f", "null", "/dev/null")
				diag, _ := cmd.CombinedOutput()
				t.Fatalf("integrity check failed on round-tripped output:\n%s", diag)
			}

			// go-mp4tag needs an existing moov.udta.meta.ilst container to write
			// into (see downloader.go's downloadEnhancedCodec comment); confirm
			// the flattened file still has one under the -f mp4 muxer used by
			// fixEncapsulate, and that tagging doesn't corrupt playback.
			outPath := filepath.Join(tmp, "tagged.m4a")
			if err := os.WriteFile(outPath, flat, 0o644); err != nil {
				t.Fatal(err)
			}
			song := applemusic.Song{Name: "Test Track", ArtistName: "Test Artist", AlbumName: "Test Album", TrackNumber: 1}
			if err := p.writeMetadata(ctx, outPath, song, "", nil, info); err != nil {
				t.Fatalf("writeMetadata: %v", err)
			}
			tagged, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatal(err)
			}
			if !p.checkIntegrity(ctx, tagged) {
				t.Fatal("integrity check failed after writeMetadata")
			}
			probe := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_entries", "format_tags=title,artist,album", "-of", "default=nw=1", outPath)
			probeOut, err := probe.CombinedOutput()
			if err != nil {
				t.Fatalf("ffprobe tagged output: %v: %s", err, probeOut)
			}
			for _, want := range []string{"title=Test Track", "artist=Test Artist", "album=Test Album"} {
				if !bytes.Contains(probeOut, []byte(want)) {
					t.Fatalf("tagged output missing %q; ffprobe output:\n%s", want, probeOut)
				}
			}
		})
	}
}
