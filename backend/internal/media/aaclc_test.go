package media

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"
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

func TestSelectCodecFallsBackOnlyWhenEnhancedHLSIsMissing(t *testing.T) {
	if codec, fallback := selectCodec("alac", false); codec != "aac-lc" || !fallback {
		t.Fatalf("missing Enhanced HLS selected codec=%q fallback=%v", codec, fallback)
	}
	if codec, fallback := selectCodec("alac", true); codec != "alac" || fallback {
		t.Fatalf("available Enhanced HLS selected codec=%q fallback=%v", codec, fallback)
	}
	if codec, fallback := selectCodec("aac-lc", false); codec != "aac-lc" || fallback {
		t.Fatalf("explicit AAC-LC selected codec=%q fallback=%v", codec, fallback)
	}
}
