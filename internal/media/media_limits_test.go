package media

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/limits"
	"amdl/internal/wrapper"
)

type blockingDecryptWrapper struct {
	fakeDownloaderWrapper
	entered chan struct{}
	allow   chan struct{}
}

func (w *blockingDecryptWrapper) NewDecryptSession(ctx context.Context, _ string) (wrapper.DecryptSession, error) {
	w.entered <- struct{}{}
	select {
	case <-w.allow:
		return identityDecryptSession{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestMediaDownloadLimitSharedAcrossJobClones(t *testing.T) {
	entered := make(chan struct{}, 2)
	allow := make(chan struct{}, 2)
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := active.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-allow
		active.Add(-1)
		_, _ = w.Write([]byte("media"))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Download.TempDir = t.TempDir()
	cfg.Download.MaxParallelDownloads = 1
	cfg.Download.MaxParallelDecrypts = 1
	base := (&Downloader{cfg: cfg, http: server.Client()}).withConfig(cfg)
	a, b := base.withConfig(cfg), base.withConfig(cfg)
	if a.downloadLimit != b.downloadLimit || a.decryptLimit != b.decryptLimit || a.inFlightLimit != b.inFlightLimit {
		t.Fatal("withConfig clones did not share process-wide media limiters")
	}

	results := make(chan selectedDownloadMedia, 2)
	for i, d := range []*Downloader{a, b} {
		go func(d *Downloader, i int) {
			// Distinct output paths: the resume checkpoint is keyed by output
			// path, and in production the output lock keeps same-output
			// downloads from running concurrently.
			got, err := d.downloadSelectedEnhancedMedia(context.Background(), selectedDownloadMedia{info: selectedMediaInfo{MediaURI: server.URL}}, "alac", "job-pool", fmt.Sprintf("out-%d.m4a", i), func(domain.ItemStatus, float64, string) {})
			if err != nil {
				t.Errorf("download: %v", err)
			}
			results <- got
		}(d, i)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first media request did not start")
	}
	select {
	case <-entered:
		t.Fatal("second request entered while global download pool was full")
	case <-time.After(100 * time.Millisecond):
	}
	allow <- struct{}{}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("second media request did not start after permit release")
	}
	allow <- struct{}{}
	for range 2 {
		got := <-results
		if got.releaseInFlight != nil {
			got.releaseInFlight()
		}
		if got.rawPath != "" {
			_ = os.Remove(got.rawPath)
		}
	}
	if got := peak.Load(); got != 1 {
		t.Fatalf("peak media requests = %d, want 1", got)
	}
}

func TestMediaInFlightBackpressureAndDownloadFailureRelease(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			http.Error(w, "retry", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("media"))
	}))
	defer server.Close()
	cfg := config.Default()
	cfg.Download.TempDir = t.TempDir()
	d := &Downloader{
		cfg: cfg, http: server.Client(),
		downloadLimit: limits.NewSemaphore(2), decryptLimit: limits.NewSemaphore(1), inFlightLimit: limits.NewSemaphore(3),
	}
	input := selectedDownloadMedia{info: selectedMediaInfo{MediaURI: server.URL}}
	if _, err := d.downloadSelectedEnhancedMedia(context.Background(), input, "alac", "job-backpressure", "out-first.m4a", func(domain.ItemStatus, float64, string) {}); err == nil {
		t.Fatal("first failed media response unexpectedly succeeded")
	}

	results := make(chan selectedDownloadMedia, 4)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := d.downloadSelectedEnhancedMedia(context.Background(), input, "alac", "job-backpressure", fmt.Sprintf("out-%d.m4a", i), func(domain.ItemStatus, float64, string) {})
			if err != nil {
				t.Errorf("download after failure: %v", err)
				return
			}
			results <- got
		}(i)
	}
	held := make([]selectedDownloadMedia, 0, 4)
	for range 3 {
		select {
		case got := <-results:
			held = append(held, got)
		case <-time.After(time.Second):
			t.Fatal("three in-flight downloads did not complete")
		}
	}
	if got := hits.Load(); got != 4 { // one failure + three admitted successes
		t.Fatalf("requests before backpressure release = %d, want 4", got)
	}
	held[0].releaseInFlight()
	select {
	case got := <-results:
		held = append(held, got)
	case <-time.After(time.Second):
		t.Fatal("fourth download did not start after in-flight permit release")
	}
	wg.Wait()
	for _, got := range held {
		got.releaseInFlight()
		_ = os.Remove(got.rawPath)
	}
}

func TestDecryptLimitIsGlobalAndCanceledWaiterDoesNotEnter(t *testing.T) {
	cfg := config.Default()
	cfg.Download.TempDir = t.TempDir()
	rawPath := cfg.Download.TempDir + "/raw.mp4"
	if err := os.WriteFile(rawPath, []byte("invalid but sufficient to reach decrypt session"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &blockingDecryptWrapper{entered: make(chan struct{}, 2), allow: make(chan struct{}, 1)}
	base := (&Downloader{cfg: cfg, wrapper: w, http: http.DefaultClient}).withConfig(cfg)
	base.decryptLimit = limits.NewSemaphore(1)
	selected := selectedDownloadMedia{rawPath: rawPath, info: selectedMediaInfo{}}
	call := func(ctx context.Context, d *Downloader) error {
		return d.downloadEnhancedCodec(ctx, domain.Job{ID: "job"}, &domain.JobItem{ID: "item"}, applemusic.Song{ID: "song"},
			"alac", "", nil, cfg.Download.TempDir+"/out.m4a", selected, &recordingReporter{}, func(domain.ItemStatus, float64, string) {})
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- call(context.Background(), base.withConfig(cfg)) }()
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("first decrypt did not enter")
	}
	ctx, cancel := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() { secondDone <- call(ctx, base.withConfig(cfg)) }()
	cancel()
	select {
	case err := <-secondDone:
		if err == nil {
			t.Fatal("canceled decrypt waiter returned nil")
		}
	case <-time.After(time.Second):
		t.Fatal("canceled decrypt waiter did not exit")
	}
	select {
	case <-w.entered:
		t.Fatal("canceled waiter entered wrapper while global decrypt permit was held")
	default:
	}
	w.allow <- struct{}{}
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first decrypt did not exit")
	}
}
