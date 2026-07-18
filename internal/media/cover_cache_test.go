package media

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"amdl/internal/config"
)

type countingCoverCatalog struct {
	fakeDownloaderCatalog

	mu    sync.Mutex
	calls map[coverCacheKey]int
	fetch func(context.Context, string, string, string) ([]byte, error)
}

func (c *countingCoverCatalog) FetchCover(ctx context.Context, artworkURLs []string, format, size string) ([]byte, error) {
	url := ""
	if len(artworkURLs) > 0 {
		url = artworkURLs[0]
	}
	key := coverCacheKey{url: url, format: format, size: size}
	c.mu.Lock()
	c.calls[key]++
	c.mu.Unlock()
	return c.fetch(ctx, url, format, size)
}

func (c *countingCoverCatalog) callCount(key coverCacheKey) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls[key]
}

func TestCoverCacheCoalescesConcurrentFetches(t *testing.T) {
	requestStarted := make(chan struct{}, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	catalog := &countingCoverCatalog{
		calls: make(map[coverCacheKey]int),
		fetch: func(ctx context.Context, _, _, _ string) ([]byte, error) {
			requestStarted <- struct{}{}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return []byte("cover"), nil
			}
		},
	}
	cache := newCoverCache(catalog)
	type result struct {
		data []byte
		err  error
	}
	results := make(chan result, 2)
	fetch := func() {
		data, err := cache.fetch(context.Background(), []string{"album-art"}, "jpg", "5000x5000")
		results <- result{data: data, err: err}
	}

	go fetch()
	<-requestStarted
	go fetch()
	select {
	case <-requestStarted:
		t.Error("second concurrent caller started a duplicate cover request")
	case <-time.After(50 * time.Millisecond):
	}
	unblock()
	for range 2 {
		result := <-results
		if result.err != nil || !bytes.Equal(result.data, []byte("cover")) {
			t.Fatalf("fetch result = %q, %v", result.data, result.err)
		}
	}
	key := coverCacheKey{url: "album-art", format: "jpg", size: "5000x5000"}
	if got := catalog.callCount(key); got != 1 {
		t.Fatalf("cover requests = %d, want 1", got)
	}
}

func TestCoverCacheSharesFallbackCandidate(t *testing.T) {
	catalog := &countingCoverCatalog{
		calls: make(map[coverCacheKey]int),
		fetch: func(_ context.Context, url, _, _ string) ([]byte, error) {
			if url == "track-art" {
				return nil, errors.New("track cover unavailable")
			}
			return []byte("album-cover"), nil
		},
	}
	cache := newCoverCache(catalog)
	data, err := cache.fetch(context.Background(), []string{"track-art", "album-art"}, "jpg", "3000x3000")
	if err != nil || !bytes.Equal(data, []byte("album-cover")) {
		t.Fatalf("fallback fetch = %q, %v", data, err)
	}
	data, err = cache.fetch(context.Background(), []string{"album-art"}, "jpg", "3000x3000")
	if err != nil || !bytes.Equal(data, []byte("album-cover")) {
		t.Fatalf("cached album fetch = %q, %v", data, err)
	}
	if got := catalog.callCount(coverCacheKey{url: "track-art", format: "jpg", size: "3000x3000"}); got != 1 {
		t.Fatalf("track cover requests = %d, want 1", got)
	}
	if got := catalog.callCount(coverCacheKey{url: "album-art", format: "jpg", size: "3000x3000"}); got != 1 {
		t.Fatalf("album cover requests = %d, want 1", got)
	}
}

func TestCoverCacheSeparatesFormatAndSize(t *testing.T) {
	catalog := &countingCoverCatalog{
		calls: make(map[coverCacheKey]int),
		fetch: func(_ context.Context, _, format, size string) ([]byte, error) {
			return []byte(format + ":" + size), nil
		},
	}
	cache := newCoverCache(catalog)
	requests := []coverCacheKey{
		{url: "album-art", format: "jpg", size: "5000x5000"},
		{url: "album-art", format: "png", size: "5000x5000"},
		{url: "album-art", format: "jpg", size: "3000x3000"},
	}
	for _, request := range requests {
		if _, err := cache.fetch(context.Background(), []string{request.url}, request.format, request.size); err != nil {
			t.Fatal(err)
		}
	}
	for _, request := range requests {
		if got := catalog.callCount(request); got != 1 {
			t.Fatalf("requests for %+v = %d, want 1", request, got)
		}
	}
}

func TestCoverCacheDoesNotRetainFailures(t *testing.T) {
	var attempts int
	catalog := &countingCoverCatalog{
		calls: make(map[coverCacheKey]int),
		fetch: func(_ context.Context, _, _, _ string) ([]byte, error) {
			attempts++
			if attempts == 1 {
				return nil, errors.New("temporary failure")
			}
			return []byte("recovered"), nil
		},
	}
	cache := newCoverCache(catalog)
	if _, err := cache.fetch(context.Background(), []string{"album-art"}, "jpg", "5000x5000"); err == nil {
		t.Fatal("first fetch succeeded, want temporary failure")
	}
	data, err := cache.fetch(context.Background(), []string{"album-art"}, "jpg", "5000x5000")
	if err != nil || !bytes.Equal(data, []byte("recovered")) {
		t.Fatalf("retry fetch = %q, %v", data, err)
	}
	key := coverCacheKey{url: "album-art", format: "jpg", size: "5000x5000"}
	if got := catalog.callCount(key); got != 2 {
		t.Fatalf("cover requests = %d, want 2", got)
	}
}

func TestCoverCacheEvictsLeastRecentlyUsedBytes(t *testing.T) {
	catalog := &countingCoverCatalog{
		calls: make(map[coverCacheKey]int),
		fetch: func(_ context.Context, url, _, _ string) ([]byte, error) {
			return []byte(url), nil
		},
	}
	cache := newCoverCacheWithLimit(catalog, int64(len("art1")))
	for _, url := range []string{"art1", "art2", "art1"} {
		if _, err := cache.fetch(context.Background(), []string{url}, "jpg", "5000x5000"); err != nil {
			t.Fatal(err)
		}
	}
	if got := catalog.callCount(coverCacheKey{url: "art1", format: "jpg", size: "5000x5000"}); got != 2 {
		t.Fatalf("art1 requests = %d, want 2 after eviction", got)
	}
	if got := catalog.callCount(coverCacheKey{url: "art2", format: "jpg", size: "5000x5000"}); got != 1 {
		t.Fatalf("art2 requests = %d, want 1", got)
	}
}

func TestStandaloneAndEmbeddedCoversShareJobCache(t *testing.T) {
	catalog := &countingCoverCatalog{
		calls: make(map[coverCacheKey]int),
		fetch: func(context.Context, string, string, string) ([]byte, error) {
			return []byte("shared-cover"), nil
		},
	}
	cfg := config.Default()
	downloader := &Downloader{cfg: cfg, catalog: catalog, covers: newCoverCache(catalog)}
	path := filepath.Join(t.TempDir(), "album", "cover.jpg")
	if err := downloader.ensureStandaloneCover(context.Background(), path, func(context.Context) (string, error) {
		return "album-art", nil
	}); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(written, []byte("shared-cover")) {
		t.Fatalf("standalone cover = %q", written)
	}
	embedded, err := downloader.fetchCover(context.Background(), []string{"album-art"}, cfg.Download.CoverFormat, cfg.Download.CoverSize)
	if err != nil || !bytes.Equal(embedded, []byte("shared-cover")) {
		t.Fatalf("embedded cover = %q, %v", embedded, err)
	}
	key := coverCacheKey{url: "album-art", format: cfg.Download.CoverFormat, size: cfg.Download.CoverSize}
	if got := catalog.callCount(key); got != 1 {
		t.Fatalf("cover requests = %d, want 1 shared request", got)
	}
}

func TestStandaloneCoverFailureIsHandledOncePerJob(t *testing.T) {
	cfg := config.Default()
	cfg.Download.MaxAttempts = 1
	downloader := &Downloader{cfg: cfg, standaloneCoverState: newStandaloneCoverState()}
	path := filepath.Join(t.TempDir(), "artist", "artist.jpg")
	wantErr := errors.New("artist not found")
	var calls int
	resolve := func(context.Context) (string, error) {
		calls++
		return "", wantErr
	}

	if err := downloader.ensureStandaloneCover(context.Background(), path, resolve); !errors.Is(err, wantErr) {
		t.Fatalf("first ensure error=%v, want %v", err, wantErr)
	}
	if err := downloader.ensureStandaloneCover(context.Background(), path, resolve); err != nil {
		t.Fatalf("second ensure error=%v, want suppressed duplicate", err)
	}
	if calls != 1 {
		t.Fatalf("resolver calls=%d, want 1", calls)
	}
}

func TestStandaloneCoversForDifferentPathsProceedConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()

	catalog := &countingCoverCatalog{
		calls: make(map[coverCacheKey]int),
		fetch: func(ctx context.Context, url, _, _ string) ([]byte, error) {
			started <- url
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-release:
				return []byte(url), nil
			}
		},
	}
	cfg := config.Default()
	cfg.Download.MaxAttempts = 1
	downloader := &Downloader{
		cfg: cfg, catalog: catalog, standaloneCoverState: newStandaloneCoverState(),
	}
	dir := t.TempDir()
	type request struct {
		path string
		url  string
	}
	requests := []request{
		{path: filepath.Join(dir, "album-a", "cover.jpg"), url: "art-a"},
		{path: filepath.Join(dir, "album-b", "cover.jpg"), url: "art-b"},
	}
	errs := make(chan error, len(requests))
	for _, req := range requests {
		req := req
		go func() {
			errs <- downloader.ensureStandaloneCover(context.Background(), req.path, func(context.Context) (string, error) {
				return req.url, nil
			})
		}()
	}

	seen := make(map[string]bool)
	for range requests {
		select {
		case url := <-started:
			seen[url] = true
		case <-time.After(time.Second):
			t.Fatal("standalone covers for different paths were serialized")
		}
	}
	unblock()
	for range requests {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for _, req := range requests {
		if !seen[req.url] {
			t.Fatalf("cover fetch %q did not start", req.url)
		}
		data, err := os.ReadFile(req.path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != req.url {
			t.Fatalf("cover %q = %q", req.path, data)
		}
	}
}
