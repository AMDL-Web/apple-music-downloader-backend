package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"amdl/internal/config"
	"github.com/Eyevinn/mp4ff/mp4"
	widevine "github.com/iyear/gowidevine"
	wvpb "github.com/iyear/gowidevine/widevinepb"
)

func TestExtractAACLCMedia(t *testing.T) {
	kid := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES-CTR,URI=\"data:;base64," + kid + "\"\n#EXT-X-MAP:URI=\"audio.m4a\"\n"))
	}))
	defer server.Close()

	got, err := extractAACLCMedia(context.Background(), server.Client(), server.URL+"/track.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	if got.MediaURI != server.URL+"/audio.m4a" || got.KID != kid || got.KeyURI == "" {
		t.Fatalf("unexpected AAC-LC media: %+v", got)
	}
}

func TestMakeWidevinePSSH(t *testing.T) {
	kid := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	if _, err := makeWidevinePSSH(kid); err != nil {
		t.Fatal(err)
	}
}

func TestEmbeddedWidevineDeviceLoads(t *testing.T) {
	kid := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef"))
	if _, _, err := newWidevineSession(kid); err != nil {
		t.Fatal(err)
	}
}

func TestWidevineDecryptStreamsRealCENCToFlatFile(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	tmp := t.TempDir()
	plainPath := filepath.Join(tmp, "plain-fragmented.m4a")
	gen := exec.Command("ffmpeg", "-y", "-v", "error",
		"-f", "lavfi", "-i", "sine=frequency=523:duration=2",
		"-c:a", "aac", "-f", "mp4",
		"-movflags", "frag_keyframe+empty_moov+default_base_moof",
		"-frag_duration", "500000", plainPath)
	if out, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate fragmented AAC: %v: %s", err, out)
	}
	plain, err := os.ReadFile(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef")
	iv := []byte("12345678")
	kid := mp4.UUID([]byte("fedcba9876543210"))
	fixture := buildEncryptedFixture(t, plain, key, iv, kid)
	keys := []*widevine.Key{{
		Type: wvpb.License_KeyContainer_CONTENT,
		ID:   kid,
		Key:  key,
	}}

	// The direct pipe helper must not consult Download.TempDir. Point it at a
	// regular file so the old flattenToFile path (MkdirTemp + fix-*/in.m4a)
	// would fail deterministically instead of creating and quickly removing an
	// intermediate that an after-the-fact directory scan could miss.
	blockedFixDir := filepath.Join(tmp, "must-not-create-fix-input")
	if err := os.WriteFile(blockedFixDir, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{}
	cfg.Download.TempDir = blockedFixDir
	cfg.Tools.FFmpeg = "ffmpeg"
	p := newMP4Processor(cfg)
	flatPath := filepath.Join(tmp, "aaclc-flat.m4a")
	callbackCalled := false
	if err := p.decryptWidevineToFlatFile(context.Background(), bytes.NewReader(fixture.raw), keys, flatPath, func() {
		callbackCalled = true
	}); err != nil {
		t.Fatalf("stream Widevine decrypt to flat file: %v", err)
	}
	if !callbackCalled {
		t.Fatal("post-input remux callback was not called")
	}
	flat, err := os.ReadFile(flatPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(flat) < 12 || string(flat[8:12]) != "M4A " {
		t.Fatalf("flat output has invalid M4A brand: %q", flat[:min(len(flat), 12)])
	}
	if !p.checkIntegrityFile(context.Background(), flatPath) {
		t.Fatal("streamed AAC-LC output failed integrity check")
	}

	decode := func(path string) []byte {
		t.Helper()
		cmd := exec.Command("ffmpeg", "-v", "error", "-i", path, "-map", "0:a:0", "-f", "s16le", "pipe:1")
		pcm, err := cmd.Output()
		if err != nil {
			t.Fatalf("decode %s: %v", filepath.Base(path), err)
		}
		return pcm
	}
	if got, want := decode(flatPath), decode(plainPath); !bytes.Equal(got, want) {
		t.Fatalf("streamed AAC-LC audio differs: got %d PCM bytes, want %d", len(got), len(want))
	}
	if fixDirs, err := filepath.Glob(filepath.Join(tmp, "fix-*")); err != nil {
		t.Fatal(err)
	} else if len(fixDirs) != 0 {
		t.Fatalf("AAC-LC streaming path created fix input intermediates: %v", fixDirs)
	}
}

func TestConfiguredCodecsUsesFallbackChain(t *testing.T) {
	cfg := config.DownloadConfig{
		QualityPriority: []string{"alac", "aac"}, CodecAlternative: true,
	}
	got, err := configuredCodecs(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alac", "aac", "aac-lc"}
	if len(got) != len(want) {
		t.Fatalf("configuredCodecs() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("configuredCodecs() = %#v, want %#v", got, want)
		}
	}
}

func TestConfiguredCodecsCanDisableFallback(t *testing.T) {
	got, err := configuredCodecs(config.DownloadConfig{
		QualityPriority: []string{"alac", "aac-lc"}, CodecAlternative: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "alac" {
		t.Fatalf("configuredCodecs() = %#v, want [alac]", got)
	}
}

func TestAttemptsForCodecAppliesToEveryCodec(t *testing.T) {
	if got := attemptsForCodec(3, 0); got != 3 {
		t.Fatalf("first codec attempts = %d, want 3", got)
	}
	if got := attemptsForCodec(3, 1); got != 3 {
		t.Fatalf("fallback codec attempts = %d, want 3", got)
	}
	if got := attemptsForCodec(3, 2); got != 3 {
		t.Fatalf("final fallback attempts = %d, want 3", got)
	}
}
