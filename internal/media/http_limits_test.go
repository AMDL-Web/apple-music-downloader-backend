package media

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
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
