package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"amdl/internal/limits"
)

func TestReadAllLimitedRejectsOverflow(t *testing.T) {
	if _, err := readAllLimited(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("readAllLimited accepted a response larger than its limit")
	}
}

func TestDownloadBytesRejectsOversizedContentLengthBeforeReading(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(maxInMemoryMediaBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	if _, err := downloadBytes(context.Background(), server.Client(), server.URL, nil); err == nil {
		t.Fatal("downloadBytes accepted an oversized declared response")
	}
}

func TestManifestRequestRetries429ThroughSharedGate(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			http.Error(w, "limited", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("#EXTM3U\n"))
	}))
	defer server.Close()

	gate := limits.NewRequestGate(1, 1000, 1000)
	body, err := downloadText(context.Background(), server.Client(), server.URL, gate)
	if err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 || body != "#EXTM3U\n" {
		t.Fatalf("hits/body = %d/%q, want 2/manifest", hits.Load(), body)
	}
}
