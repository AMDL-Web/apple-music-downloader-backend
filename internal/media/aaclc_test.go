package media

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"amdl/internal/config"
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

func TestRetriesForCodecAppliesToEveryCodec(t *testing.T) {
	if got := retriesForCodec(3, 0); got != 3 {
		t.Fatalf("first codec retries = %d, want 3", got)
	}
	if got := retriesForCodec(3, 1); got != 3 {
		t.Fatalf("fallback codec retries = %d, want 3", got)
	}
	if got := retriesForCodec(3, 2); got != 3 {
		t.Fatalf("final fallback retries = %d, want 3", got)
	}
}
