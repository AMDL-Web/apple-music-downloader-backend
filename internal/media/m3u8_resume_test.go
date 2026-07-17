package media

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

const testResumeOwner = "job-test"

func TestDownloadToFileResumesInterruptedTransfer(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 16*1024)
	cut := 73 * 1024
	var mu sync.Mutex
	var requests []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Header.Clone())
		requestNumber := len(requests)
		mu.Unlock()

		w.Header().Set("ETag", `"track-v1"`)
		if requestNumber == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload[:cut])
			return
		}
		if got, want := r.Header.Get("Range"), fmt.Sprintf("bytes=%d-", cut); got != want {
			t.Errorf("Range = %q, want %q", got, want)
		}
		if got := r.Header.Get("If-Range"); got != `"track-v1"` {
			t.Errorf("If-Range = %q, want track ETag", got)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(payload)-1, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)-cut))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[cut:])
	}))
	defer server.Close()

	dir := t.TempDir()
	key := filepath.Join(dir, "final.m4a")
	if _, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, testResumeOwner, key, nil); err == nil {
		t.Fatal("first truncated transfer succeeded")
	}
	checkpoint, metadataPath := mustResumePaths(t, dir, testResumeOwner, key)
	if info, err := os.Stat(checkpoint); err != nil || info.Size() != int64(cut) {
		t.Fatalf("partial checkpoint size = %v, err=%v; want %d", info, err, cut)
	}
	if _, err := os.Stat(metadataPath); err != nil {
		t.Fatalf("resume metadata missing: %v", err)
	}
	metadataBytes, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(metadataBytes, []byte(server.URL)) {
		t.Fatal("resume metadata persisted the signed source URL instead of its fingerprint")
	}

	var progress []float64
	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, testResumeOwner, key, func(value float64) {
		progress = append(progress, value)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, path, payload)
	committedMetadata, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(committedMetadata, []byte(`"complete":true`)) {
		t.Fatalf("completed transfer metadata was not committed: %s", committedMetadata)
	}
	if len(progress) == 0 || progress[0] != float64(cut)/float64(len(payload)) || progress[len(progress)-1] != 1 {
		t.Fatalf("resume progress = %v, want first=%v last=1", progress, float64(cut)/float64(len(payload)))
	}
	for i := 1; i < len(progress); i++ {
		if progress[i] < progress[i-1] {
			t.Fatalf("progress decreased at %d: %v", i, progress)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2", len(requests))
	}
	for i, header := range requests {
		if got := header.Get("Accept-Encoding"); got != "identity" {
			t.Errorf("request %d Accept-Encoding = %q, want identity", i+1, got)
		}
	}
}

func TestDownloadToFileTruncatesWhenServerIgnoresRange(t *testing.T) {
	payload := bytes.Repeat([]byte("new representation"), 4096)
	dir := t.TempDir()
	key := "stable output key"
	checkpoint, metadataPath := mustResumePaths(t, dir, testResumeOwner, key)
	oldPrefix := []byte("old partial bytes that must not be retained")
	if err := os.WriteFile(checkpoint, oldPrefix, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeResumeMetadata(metadataPath, resumeMetadata{Version: resumeMetadataVersion, SourceHash: sourceFingerprint("placeholder"), ETag: `"v1"`, Total: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}

	var rangeHeader, ifRangeHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader = r.Header.Get("Range")
		ifRangeHeader = r.Header.Get("If-Range")
		w.Header().Set("ETag", `"v2"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload) // 200 means Range/If-Range was not usable.
	}))
	defer server.Close()

	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, testResumeOwner, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rangeHeader != fmt.Sprintf("bytes=%d-", len(oldPrefix)) || ifRangeHeader != `"v1"` {
		t.Fatalf("resume headers Range=%q If-Range=%q", rangeHeader, ifRangeHeader)
	}
	assertFileBytes(t, path, payload)
}

func TestDownloadToFileRejectsMismatchedPartialContent(t *testing.T) {
	payload := bytes.Repeat([]byte("replacement"), 8192)
	dir := t.TempDir()
	key := "output"
	checkpoint, metadataPath := mustResumePaths(t, dir, testResumeOwner, key)
	partial := payload[:4096]
	if err := os.WriteFile(checkpoint, partial, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeResumeMetadata(metadataPath, resumeMetadata{Version: resumeMetadataVersion, SourceHash: sourceFingerprint("old-url"), ETag: `"old"`, Total: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("ETag", `"changed"`)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", len(partial), len(payload)-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[len(partial):])
			return
		}
		if r.Header.Get("Range") != "" {
			t.Errorf("clean restart unexpectedly sent Range %q", r.Header.Get("Range"))
		}
		w.Header().Set("ETag", `"changed"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, testResumeOwner, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want mismatched 206 plus clean GET", requests)
	}
	assertFileBytes(t, path, payload)
}

func TestDownloadToFileAcceptsCompleteCheckpointOn416(t *testing.T) {
	payload := []byte("already complete encrypted media")
	dir := t.TempDir()
	key := "output"
	checkpoint, metadataPath := mustResumePaths(t, dir, testResumeOwner, key)
	if err := os.WriteFile(checkpoint, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	serverRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverRequests++
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(payload)))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer server.Close()
	if err := writeResumeMetadata(metadataPath, resumeMetadata{Version: resumeMetadataVersion, SourceHash: sourceFingerprint(server.URL), ETag: `"complete"`, Complete: true}); err != nil {
		t.Fatal(err)
	}

	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, testResumeOwner, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if serverRequests != 1 {
		t.Fatalf("requests = %d, want 1", serverRequests)
	}
	assertFileBytes(t, path, payload)
}

func TestDownloadToFileValidatesCompletedCheckpointWith416(t *testing.T) {
	payload := []byte("complete encrypted checkpoint")
	dir := t.TempDir()
	key := "output"
	checkpoint, metadataPath := mustResumePaths(t, dir, testResumeOwner, key)
	if err := os.WriteFile(checkpoint, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	serverRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverRequests++
		if got := r.Header.Get("Range"); got != fmt.Sprintf("bytes=%d-", len(payload)) {
			t.Errorf("Range = %q", got)
		}
		if got := r.Header.Get("If-Range"); got != `"complete"` {
			t.Errorf("If-Range = %q", got)
		}
		w.Header().Set("ETag", `"complete"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(payload)))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer server.Close()
	if err := writeResumeMetadata(metadataPath, resumeMetadata{
		Version: resumeMetadataVersion, SourceHash: sourceFingerprint(server.URL), ETag: `"complete"`, Total: int64(len(payload)), Complete: true,
	}); err != nil {
		t.Fatal(err)
	}

	var progress []float64
	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, testResumeOwner, key, func(value float64) {
		progress = append(progress, value)
	})
	if err != nil {
		t.Fatal(err)
	}
	if serverRequests != 1 {
		t.Fatalf("requests = %d, want 1", serverRequests)
	}
	if len(progress) != 1 || progress[0] != 1 {
		t.Fatalf("progress = %v, want [1]", progress)
	}
	assertFileBytes(t, path, payload)
}

func TestDownloadToFileRestartsCompletedCheckpointWhenETagChanged(t *testing.T) {
	oldPayload := []byte("old complete representation")
	newPayload := []byte("new complete representation")
	if len(oldPayload) != len(newPayload) {
		t.Fatal("test representations must have the same size")
	}
	dir := t.TempDir()
	key := "output"
	checkpoint, metadataPath := mustResumePaths(t, dir, testResumeOwner, key)
	if err := os.WriteFile(checkpoint, oldPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("ETag", `"new"`)
		if requests == 1 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(newPayload)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if r.Header.Get("Range") != "" {
			t.Errorf("clean restart sent Range %q", r.Header.Get("Range"))
		}
		_, _ = w.Write(newPayload)
	}))
	defer server.Close()
	if err := writeResumeMetadata(metadataPath, resumeMetadata{
		Version: resumeMetadataVersion, SourceHash: sourceFingerprint("old-signed-url"), ETag: `"old"`, Total: int64(len(oldPayload)), Complete: true,
	}); err != nil {
		t.Fatal(err)
	}

	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, testResumeOwner, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want validator probe plus clean GET", requests)
	}
	assertFileBytes(t, path, newPayload)
}

func TestDownloadToFileRestartsFullLengthCheckpointWithoutCompleteMarker(t *testing.T) {
	payload := []byte("fresh complete representation")
	dir := t.TempDir()
	key := "output"
	checkpoint, metadataPath := mustResumePaths(t, dir, testResumeOwner, key)
	if err := os.WriteFile(checkpoint, bytes.Repeat([]byte("x"), len(payload)), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			t.Errorf("uncommitted full-length checkpoint sent Range %q", r.Header.Get("Range"))
		}
		w.Header().Set("ETag", `"same"`)
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	if err := writeResumeMetadata(metadataPath, resumeMetadata{
		Version: resumeMetadataVersion, SourceHash: sourceFingerprint(server.URL), ETag: `"same"`, Total: int64(len(payload)), Complete: false,
	}); err != nil {
		t.Fatal(err)
	}

	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, testResumeOwner, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, path, payload)
}

func TestDownloadToFileRestartsWhen416TotalDoesNotMatch(t *testing.T) {
	payload := []byte("replacement full object")
	dir := t.TempDir()
	key := "output"
	checkpoint, metadataPath := mustResumePaths(t, dir, testResumeOwner, key)
	if err := os.WriteFile(checkpoint, []byte("oversized stale partial data"), 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if requests == 1 {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(payload)))
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		if r.Header.Get("Range") != "" {
			t.Errorf("clean request sent Range %q", r.Header.Get("Range"))
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	if err := writeResumeMetadata(metadataPath, resumeMetadata{Version: resumeMetadataVersion, SourceHash: sourceFingerprint(server.URL), ETag: `"old"`}); err != nil {
		t.Fatal(err)
	}

	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, testResumeOwner, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	assertFileBytes(t, path, payload)
}

func TestDownloadToFileRestartsWithoutValidator(t *testing.T) {
	payload := []byte("new same-url representation")
	dir := t.TempDir()
	key := "output"
	checkpoint, metadataPath := mustResumePaths(t, dir, testResumeOwner, key)
	if err := os.WriteFile(checkpoint, []byte("old-prefix"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" || r.Header.Get("If-Range") != "" {
			t.Errorf("unvalidated checkpoint sent Range=%q If-Range=%q", r.Header.Get("Range"), r.Header.Get("If-Range"))
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	// Make the persisted source identity match this request; absence of a
	// validator alone must still force a full restart.
	if err := writeResumeMetadata(metadataPath, resumeMetadata{
		Version: resumeMetadataVersion, SourceHash: sourceFingerprint(server.URL), Total: int64(len(payload)),
	}); err != nil {
		t.Fatal(err)
	}

	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, testResumeOwner, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, path, payload)
}

func TestDownloadToFileRejectsInconsistentContentRangeLength(t *testing.T) {
	payload := bytes.Repeat([]byte("range"), 4096)
	dir := t.TempDir()
	key := "output"
	checkpoint, metadataPath := mustResumePaths(t, dir, testResumeOwner, key)
	partial := payload[:1024]
	if err := os.WriteFile(checkpoint, partial, 0o600); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("ETag", `"track"`)
		if requests == 1 {
			// Header claims only 100 bytes while Content-Length/body contain the
			// entire remainder. The client must reject this 206 before appending.
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", len(partial), len(partial)+99, len(payload)))
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)-len(partial)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[len(partial):])
			return
		}
		if r.Header.Get("Range") != "" {
			t.Errorf("clean restart sent Range %q", r.Header.Get("Range"))
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	if err := writeResumeMetadata(metadataPath, resumeMetadata{
		Version: resumeMetadataVersion, SourceHash: sourceFingerprint(server.URL), ETag: `"track"`, Total: int64(len(payload)),
	}); err != nil {
		t.Fatal(err)
	}

	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, testResumeOwner, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want invalid 206 plus clean GET", requests)
	}
	assertFileBytes(t, path, payload)
}

func TestDownloadToFileDiscardsCorruptResumeMetadata(t *testing.T) {
	payload := []byte("fresh complete payload")
	dir := t.TempDir()
	key := "output"
	checkpoint, metadataPath := mustResumePaths(t, dir, testResumeOwner, key)
	if err := os.WriteFile(checkpoint, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") != "" {
			t.Errorf("corrupt state sent Range %q", r.Header.Get("Range"))
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	path, err := downloadToFile(context.Background(), server.Client(), server.URL, dir, testResumeOwner, key, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertFileBytes(t, path, payload)
}

func TestCleanupResumableDownloadRemovesMediaAndMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "resume-test.mp4")
	for _, candidate := range []string{path, path + ".json", path + ".json.tmp"} {
		if err := os.WriteFile(candidate, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cleanupResumableDownload(path)
	for _, candidate := range []string{path, path + ".json", path + ".json.tmp"} {
		if _, err := os.Stat(candidate); !os.IsNotExist(err) {
			t.Errorf("checkpoint artifact %s still exists (err=%v)", candidate, err)
		}
	}
}

func TestCleanupResumeOwnerRemovesAllCodecArtifacts(t *testing.T) {
	dir := t.TempDir()
	owner := "job-with-fallback"
	for _, key := range []string{"alac-output", "aac-output"} {
		path, metadataPath := resumableDownloadPaths(dir, owner, key)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(metadataPath, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ownerDir := resumeOwnerDir(dir, owner)
	cleanupResumeOwner(dir, owner)
	if _, err := os.Stat(ownerDir); !os.IsNotExist(err) {
		t.Fatalf("owner checkpoint directory still exists: %v", err)
	}
}

func assertFileBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("file content differs: got %d bytes, want %d", len(got), len(want))
	}
}

func mustResumePaths(t *testing.T, dir, owner, key string) (string, string) {
	t.Helper()
	path, metadataPath := resumableDownloadPaths(dir, owner, key)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	return path, metadataPath
}
