package media

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
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

func TestDownloadPublishesWaitingUntilGlobalPermitIsGranted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("media"))
	}))
	defer server.Close()

	cfg := config.Default()
	cfg.Download.TempDir = t.TempDir()
	cfg.Download.MaxParallelDownloads = 1
	d := (newTestDownloader(&Downloader{cfg: cfg, http: server.Client()})).withConfig(cfg)
	releaseHeldPermit, err := d.downloadLimit.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	statuses := make(chan domain.ItemStatus, 8)
	type result struct {
		media selectedDownloadMedia
		err   error
	}
	done := make(chan result, 1)
	go func() {
		media, downloadErr := d.downloadSelectedEnhancedMedia(
			context.Background(),
			selectedDownloadMedia{info: selectedMediaInfo{MediaURI: server.URL}},
			"alac",
			"job-waiting",
			"out.m4a",
			func(status domain.ItemStatus, _ string, _ func(*domain.ItemProgress)) {
				statuses <- status
			},
		)
		done <- result{media: media, err: downloadErr}
	}()

	select {
	case status := <-statuses:
		if status != domain.ItemWaitingDownload {
			t.Fatalf("first status = %q, want waiting_download", status)
		}
	case <-time.After(time.Second):
		t.Fatal("waiting_download was not published")
	}
	select {
	case status := <-statuses:
		t.Fatalf("published %q before download permit was granted", status)
	case <-time.After(100 * time.Millisecond):
	}

	releaseHeldPermit()
	select {
	case status := <-statuses:
		if status != domain.ItemDownloading {
			t.Fatalf("status after permit = %q, want downloading", status)
		}
	case <-time.After(time.Second):
		t.Fatal("downloading was not published after permit")
	}

	got := <-done
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.media.releaseInFlight != nil {
		got.media.releaseInFlight()
	}
	if got.media.rawPath != "" {
		_ = os.Remove(got.media.rawPath)
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
	base := newTestDownloader(withDecryptLimit(&Downloader{cfg: cfg, http: server.Client()}, 1)).withConfig(cfg)
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
			got, err := d.downloadSelectedEnhancedMedia(context.Background(), selectedDownloadMedia{info: selectedMediaInfo{MediaURI: server.URL}}, "alac", "job-pool", fmt.Sprintf("out-%d.m4a", i), func(domain.ItemStatus, string, func(*domain.ItemProgress)) {})
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
	d := newTestDownloader(&Downloader{
		cfg: cfg, http: server.Client(),
		downloadLimit: limits.NewSemaphore(2), decryptLimit: limits.NewSemaphore(1), inFlightLimit: limits.NewSemaphore(3),
	})
	input := selectedDownloadMedia{info: selectedMediaInfo{MediaURI: server.URL}}
	if _, err := d.downloadSelectedEnhancedMedia(context.Background(), input, "alac", "job-backpressure", "out-first.m4a", func(domain.ItemStatus, string, func(*domain.ItemProgress)) {}); err == nil {
		t.Fatal("first failed media response unexpectedly succeeded")
	}

	results := make(chan selectedDownloadMedia, 4)
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := d.downloadSelectedEnhancedMedia(context.Background(), input, "alac", "job-backpressure", fmt.Sprintf("out-%d.m4a", i), func(domain.ItemStatus, string, func(*domain.ItemProgress)) {})
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
	base := (newTestDownloader(&Downloader{cfg: cfg, wrapper: w, http: http.DefaultClient})).withConfig(cfg)
	base.decryptLimit = limits.NewSemaphore(1)
	selected := selectedDownloadMedia{encryptedMedia: encryptedMedia{rawPath: rawPath}}
	call := func(ctx context.Context, d *Downloader) error {
		return d.downloadEnhancedCodec(ctx, domain.Job{ID: "job"}, &domain.JobItem{ID: "item"}, applemusic.Song{ID: "song"},
			"alac", trackLyrics{}, nil, cfg.Download.TempDir+"/out.m4a", selected, &recordingReporter{}, func(domain.ItemStatus, string, func(*domain.ItemProgress)) {})
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

// wrapper-lite answers /key one at a time, so a track's keys are fetched while
// its media downloads, and a track whose keys are not in yet waits in
// waiting_decrypt without a decrypt slot instead of showing as decrypting.
func TestKeysAreFetchedDuringTheDownloadAndAwaitedBeforeDecrypting(t *testing.T) {
	releaseMedia, testDone := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-releaseMedia:
		case <-testDone:
			return
		}
		_, _ = w.Write([]byte("media"))
	}))
	defer server.Close()
	defer close(testDone) // a failed test must not leave Close waiting on the handler

	asked := make(chan []string, 4)
	keysReady := make(chan struct{})
	w := fakeDownloaderWrapper{prepareKeys: func(ctx context.Context, songID string, keys []string) error {
		asked <- append([]string{songID}, keys...)
		select {
		case <-keysReady:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	cfg := config.Default()
	cfg.Download.TempDir = t.TempDir()
	d := (newTestDownloader(&Downloader{cfg: cfg, wrapper: w, http: server.Client()})).withConfig(cfg)
	d.decryptLimit = limits.NewSemaphore(1)

	type result struct {
		media selectedDownloadMedia
		err   error
	}
	downloaded := make(chan result, 1)
	go func() {
		media, err := d.downloadSelectedEnhancedMedia(context.Background(),
			selectedDownloadMedia{info: selectedMediaInfo{MediaURI: server.URL, Keys: []string{"skd://k"}}, songID: "song"},
			"alac", "job-keys", "out.m4a", func(domain.ItemStatus, string, func(*domain.ItemProgress)) {})
		downloaded <- result{media, err}
	}()
	select {
	case got := <-asked:
		if fmt.Sprint(got) != "[song skd://k]" {
			t.Fatalf("PrepareKeys(%v), want song and skd://k", got)
		}
	case <-time.After(time.Second):
		t.Fatal("the keys were not asked for while the media downloaded")
	}
	close(releaseMedia)
	var got result
	select {
	case got = <-downloaded:
	case <-time.After(time.Second):
		t.Fatal("download did not finish")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	defer got.media.release()

	var mu sync.Mutex
	var seen []string
	decryptDone := make(chan error, 1)
	go func() {
		decryptDone <- d.downloadEnhancedCodec(context.Background(), domain.Job{ID: "job-keys"}, &domain.JobItem{ID: "item"},
			applemusic.Song{ID: "song"}, "alac", trackLyrics{}, nil, cfg.Download.TempDir+"/out.m4a", got.media, &recordingReporter{},
			func(status domain.ItemStatus, message string, _ func(*domain.ItemProgress)) {
				mu.Lock()
				seen = append(seen, string(status)+": "+message)
				mu.Unlock()
			})
	}()
	select {
	case <-asked:
	case <-time.After(time.Second):
		t.Fatal("the decrypt phase did not wait for the keys")
	}
	mu.Lock()
	before := fmt.Sprint(seen)
	mu.Unlock()
	if before != "[waiting_decrypt: waiting for decryption key]" {
		t.Fatalf("statuses while the keys are missing = %s, want only waiting_decrypt", before)
	}
	slotCtx, cancelSlot := context.WithTimeout(context.Background(), time.Second)
	release, err := d.decryptLimit.Acquire(slotCtx)
	cancelSlot()
	if err != nil {
		t.Fatalf("the decrypt slot is held while the track waits for its keys: %v", err)
	}
	release()
	close(keysReady)
	select {
	case <-decryptDone: // the bytes are not a real stream; reaching decrypt is enough
	case <-time.After(5 * time.Second):
		t.Fatal("decrypt phase did not finish")
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(seen, "decrypting: opening decrypt session") {
		t.Fatalf("statuses = %v, want decrypting once the keys arrived", seen)
	}
}
