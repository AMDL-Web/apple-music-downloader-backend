package media

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/jobs"
	"amdl/internal/wrapper"
	"github.com/zhaarey/go-mp4tag"
)

// TestPostDecryptFixturePipeline is an opt-in, offline performance and
// correctness harness for the real Enhanced-HLS post-decrypt pipeline. It is
// intentionally absent from normal test cost: both AMDL_POSTDECRYPT_FIXTURE_DIR
// and AMDL_POSTDECRYPT_MODE (low or high) must be set before it does any work.
//
// The fixture directory must contain encrypted.m4a and fixture.json. The JSON
// has the test-only AES key and one base64 IV per sample, grouped by fragment.
// The key and IVs are never included in logs, errors, output media, or metrics.
// Media is served only by a loopback httptest server behind a transport that
// rejects every other URL, so this test cannot fall through to a CDN.
//
// Optional environment variables:
//
//   - AMDL_POSTDECRYPT_TEMP_DIR: base for a unique, automatically removed
//     scratch directory.
//   - AMDL_POSTDECRYPT_OUTPUT_DIR: base for a separate unique, automatically
//     removed output directory (use a different mount to exercise EXDEV copy).
//   - AMDL_POSTDECRYPT_SYSTEM_TEMP_DIR: base for an isolated operating-system
//     temp directory, preventing metric overlap with scratch or output.
//   - AMDL_POSTDECRYPT_RESULT_PATH: append one JSON object per run as JSONL;
//     this makes go test -count=N preserve every measurement.
//   - AMDL_POSTDECRYPT_FFMPEG: ffmpeg executable (default: ffmpeg).
//   - AMDL_POSTDECRYPT_GOLDEN_PATH: optional playable M4A whose decoded PCM
//     SHA-256 must match the new result.
//   - AMDL_POSTDECRYPT_GOLDEN_PCM_SHA256: optional expected decoded PCM digest.
//   - AMDL_POSTDECRYPT_KEEP_ARTIFACTS=1: retain the unique scratch and output
//     directories after the run for manual diagnosis (the default removes them).
func TestPostDecryptFixturePipeline(t *testing.T) {
	fixtureDir := strings.TrimSpace(os.Getenv("AMDL_POSTDECRYPT_FIXTURE_DIR"))
	if fixtureDir == "" {
		t.Skip("set AMDL_POSTDECRYPT_FIXTURE_DIR and AMDL_POSTDECRYPT_MODE to run the offline fixture harness")
	}
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("AMDL_POSTDECRYPT_MODE")))
	if mode != config.MemoryModeLow && mode != config.MemoryModeHigh {
		t.Fatalf("AMDL_POSTDECRYPT_MODE must be %q or %q", config.MemoryModeLow, config.MemoryModeHigh)
	}

	fixtureDir = postDecryptExistingDir(t, fixtureDir, "fixture")
	encryptedPath := filepath.Join(fixtureDir, "encrypted.m4a")
	fixtureJSONPath := filepath.Join(fixtureDir, "fixture.json")
	inputInfo := postDecryptRegularFile(t, encryptedPath, "encrypted fixture")
	postDecryptRegularFile(t, fixtureJSONPath, "fixture metadata")
	fixture := postDecryptLoadFixture(t, fixtureJSONPath)

	ffmpeg := strings.TrimSpace(os.Getenv("AMDL_POSTDECRYPT_FFMPEG"))
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	if _, err := exec.LookPath(ffmpeg); err != nil {
		t.Fatalf("find ffmpeg %q: %v", ffmpeg, err)
	}

	runRoot := t.TempDir()
	tempDir := postDecryptRunDir(t, os.Getenv("AMDL_POSTDECRYPT_TEMP_DIR"), runRoot, "scratch", fixtureDir, mode)
	outputDir := postDecryptRunDir(t, os.Getenv("AMDL_POSTDECRYPT_OUTPUT_DIR"), runRoot, "output", fixtureDir, mode)
	systemTempDir := postDecryptRunDir(t, os.Getenv("AMDL_POSTDECRYPT_SYSTEM_TEMP_DIR"), runRoot, "system-temp", fixtureDir, mode)
	t.Setenv("TMPDIR", systemTempDir)
	resultPath := strings.TrimSpace(os.Getenv("AMDL_POSTDECRYPT_RESULT_PATH"))
	if resultPath != "" {
		postDecryptRejectFixtureWrite(t, resultPath, fixtureDir, false)
	}
	outputPath := postDecryptUniqueOutputPath(t, outputDir, mode)

	var requestCount atomic.Int64
	var unexpectedRequestCount atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/encrypted.m4a" || r.URL.RawQuery != "" {
			unexpectedRequestCount.Add(1)
			http.Error(w, "unexpected offline fixture request", http.StatusBadRequest)
			return
		}
		input, err := os.Open(encryptedPath)
		if err != nil {
			http.Error(w, "open offline fixture", http.StatusInternalServerError)
			return
		}
		defer input.Close()
		w.Header().Set("Content-Type", "audio/mp4")
		w.Header().Set("ETag", `"amdl-postdecrypt-fixture"`)
		http.ServeContent(w, r, "encrypted.m4a", inputInfo.ModTime(), input)
	}))
	defer server.Close()

	mediaURL := server.URL + "/encrypted.m4a"
	client := server.Client()
	client.Timeout = 10 * time.Minute
	client.Transport = &postDecryptRestrictedTransport{
		base:       client.Transport,
		allowedURL: mediaURL,
		unexpected: &unexpectedRequestCount,
	}

	cfg := config.Default()
	cfg.Download.MemoryMode = mode
	cfg.Download.TempDir = tempDir
	cfg.Download.DownloadsDir = outputDir
	cfg.Download.MaxParallelDownloads = 1
	cfg.Download.MaxParallelDecrypts = 1
	cfg.Download.CheckIntegrity = true
	cfg.Download.EmbedCover = true
	cfg.Download.SaveLyricsFile = false
	cfg.Tools.FFmpeg = ffmpeg

	downloader := &Downloader{
		cfg:     cfg,
		wrapper: postDecryptFixtureWrapper{fixture: fixture},
		http:    client,
		mp4:     newMP4Processor(cfg),
	}
	selected := selectedDownloadMedia{info: selectedMediaInfo{
		MediaURI:   mediaURL,
		Keys:       []string{"offline-fixture-key-slot"},
		CodecID:    "audio-alac-stereo-192000-24",
		BitDepth:   24,
		SampleRate: 192000,
	}}
	song := applemusic.Song{
		ID:            "offline-fixture-track",
		Name:          "Post-decrypt fixture",
		ArtistName:    "AMDL test",
		AlbumName:     "Offline pipeline",
		AlbumArtist:   "AMDL test",
		ComposerName:  "AMDL test",
		GenreNames:    []string{"Test"},
		ReleaseDate:   "2026-01-01",
		AlbumRelease:  "2026-01-01",
		TrackNumber:   1,
		TrackCount:    1,
		DiscNumber:    1,
		DiscCount:     1,
		ISRC:          "TEST00000001",
		RecordLabel:   "AMDL",
		Copyright:     "Offline test fixture",
		ContentRating: "clean",
	}
	cover, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatalf("decode embedded test cover: %v", err)
	}
	const lyrics = "[00:00.00] Offline post-decrypt fixture"
	job := domain.Job{ID: "postdecrypt-fixture-job"}
	item := domain.JobItem{ID: "postdecrypt-fixture-item", JobID: job.ID, AdamID: song.ID}
	phases := &postDecryptPhaseRecorder{}
	reporter := postDecryptReporter{}

	runtime.GC()
	probe := startPostDecryptProbe(tempDir, systemTempDir, outputDir)
	started := time.Now()
	selected, downloadErr := downloader.downloadSelectedEnhancedMedia(
		context.Background(), selected, "alac", job.ID, outputPath, phases.set,
	)
	if selected.rawPath != "" {
		defer cleanupResumableDownload(selected.rawPath)
	}
	var pipelineErr error
	if downloadErr != nil {
		pipelineErr = fmt.Errorf("download encrypted fixture: %w", downloadErr)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		pipelineErr = downloader.downloadEnhancedCodec(
			ctx, job, &item, song, "alac", lyrics, cover, outputPath, selected, reporter, phases.set,
		)
		cancel()
	}
	cleanupResumableDownload(selected.rawPath)
	if selected.releaseInFlight != nil {
		selected.releaseInFlight()
	}
	wall := time.Since(started)
	usage := probe.stop()
	cgroupMemoryPeak := postDecryptCgroupMemoryPeak()

	metrics := postDecryptFixtureMetrics{
		Timestamp:              time.Now().UTC().Format(time.RFC3339Nano),
		Mode:                   mode,
		WallMS:                 float64(wall.Microseconds()) / 1000,
		InputBytes:             inputInfo.Size(),
		FragmentCount:          len(fixture.fragmentIVs),
		GoHeapStartBytes:       usage.goHeapStart,
		GoPeakHeapBytes:        usage.goPeakHeap,
		GoPeakHeapDeltaBytes:   postDecryptNonNegativeDelta(usage.goPeakHeap, usage.goHeapStart),
		RSSStartBytes:          usage.rssStart,
		PeakRSSBytes:           usage.rssPeak,
		PeakRSSDeltaBytes:      postDecryptNonNegativeDelta(usage.rssPeak, usage.rssStart),
		CgroupMemoryPeakBytes:  cgroupMemoryPeak,
		TempPeakBytes:          usage.tempPeak,
		SystemTempPeakBytes:    usage.systemTempPeak,
		OutputPeakBytes:        usage.outputPeak,
		CombinedPeakBytes:      usage.combinedPeak,
		SampleIntervalMS:       20,
		HTTPRequests:           requestCount.Load(),
		UnexpectedHTTPRequests: unexpectedRequestCount.Load(),
		Phases:                 phases.values(),
		OutputPath:             outputPath,
	}
	if pipelineErr != nil {
		metrics.Error = pipelineErr.Error()
		postDecryptEmitMetrics(t, resultPath, metrics)
		t.Fatalf("offline post-decrypt pipeline: %v", pipelineErr)
	}
	if got := unexpectedRequestCount.Load(); got != 0 {
		metrics.Error = fmt.Sprintf("unexpected HTTP requests: %d", got)
		postDecryptEmitMetrics(t, resultPath, metrics)
		t.Fatalf("offline harness attempted %d unexpected HTTP request(s)", got)
	}
	if item.Status != domain.ItemCompleted || item.OutputPath != outputPath {
		metrics.Error = fmt.Sprintf("unexpected completed item state: status=%s", item.Status)
		postDecryptEmitMetrics(t, resultPath, metrics)
		t.Fatalf("completed item = status %q, output %q", item.Status, item.OutputPath)
	}
	postDecryptRequirePhaseOrder(t, metrics.Phases, []string{
		string(domain.ItemDownloading), string(domain.ItemDecrypting), string(domain.ItemRemuxing),
		string(domain.ItemSaving), string(domain.ItemTagging),
	})

	outputInfo, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("stat finalized output: %v", err)
	}
	metrics.OutputBytes = outputInfo.Size()
	postDecryptCheckBrand(t, outputPath)
	postDecryptCheckTags(t, outputPath, song, lyrics)
	pcmHash := postDecryptPCMHash(t, context.Background(), ffmpeg, outputPath)
	metrics.AudioPCMSHA256 = pcmHash
	postDecryptCheckGolden(t, ffmpeg, pcmHash)
	metrics.OutputSHA256 = postDecryptFileSHA256(t, outputPath)

	// The production path already decoded the whole file during its integrity
	// phase. Checking once more after metadata proves the tail-moov rewrite did
	// not invalidate the finalized container.
	if !downloader.mp4.checkIntegrityFile(context.Background(), outputPath) {
		t.Fatal("finalized, tagged output failed the post-tag integrity check")
	}
	postDecryptEmitMetrics(t, resultPath, metrics)
}

type postDecryptFixtureData struct {
	key         []byte
	fragmentIVs [][][]byte
}

type postDecryptFixtureJSON struct {
	Key         string     `json:"key"`
	FragmentIVs [][]string `json:"fragment_ivs"`
}

func postDecryptLoadFixture(t *testing.T, path string) *postDecryptFixtureData {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open fixture metadata: %v", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil {
		t.Fatalf("read fixture metadata: %v", err)
	}
	if len(raw) > 4<<20 {
		t.Fatal("fixture metadata exceeds 4 MiB")
	}
	var encoded postDecryptFixtureJSON
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatalf("parse fixture metadata: %v", err)
	}
	key, err := base64.StdEncoding.DecodeString(encoded.Key)
	if err != nil {
		t.Fatal("fixture key is not valid base64")
	}
	if _, err := aes.NewCipher(key); err != nil {
		t.Fatalf("fixture key has invalid AES length: %v", err)
	}
	if len(encoded.FragmentIVs) == 0 {
		t.Fatal("fixture has no fragment IVs")
	}
	fragmentIVs := make([][][]byte, len(encoded.FragmentIVs))
	for fragmentIndex, encodedIVs := range encoded.FragmentIVs {
		if len(encodedIVs) == 0 {
			t.Fatalf("fixture fragment %d has no sample IVs", fragmentIndex)
		}
		fragmentIVs[fragmentIndex] = make([][]byte, len(encodedIVs))
		for sampleIndex, encodedIV := range encodedIVs {
			iv, err := base64.StdEncoding.DecodeString(encodedIV)
			if err != nil || len(iv) != aes.BlockSize {
				t.Fatalf("fixture fragment %d sample %d has an invalid AES-CTR IV", fragmentIndex, sampleIndex)
			}
			fragmentIVs[fragmentIndex][sampleIndex] = iv
		}
	}
	return &postDecryptFixtureData{key: key, fragmentIVs: fragmentIVs}
}

type postDecryptFixtureWrapper struct {
	fixture *postDecryptFixtureData
}

func (postDecryptFixtureWrapper) Status(context.Context) (wrapper.Status, error) {
	return wrapper.Status{}, errors.New("unexpected wrapper status request in offline fixture test")
}

func (postDecryptFixtureWrapper) M3U8(context.Context, string) (string, error) {
	return "", errors.New("unexpected wrapper manifest request in offline fixture test")
}

func (postDecryptFixtureWrapper) Lyrics(context.Context, string, wrapper.LyricsRequestOptions) (string, error) {
	return "", errors.New("unexpected wrapper lyrics request in offline fixture test")
}

func (postDecryptFixtureWrapper) WebPlayback(context.Context, string) (string, error) {
	return "", errors.New("unexpected wrapper playback request in offline fixture test")
}

func (w postDecryptFixtureWrapper) NewDecryptSession(context.Context, string) (wrapper.DecryptSession, error) {
	if w.fixture == nil {
		return nil, errors.New("offline fixture is not configured")
	}
	block, err := aes.NewCipher(w.fixture.key)
	if err != nil {
		return nil, errors.New("offline fixture AES key is invalid")
	}
	return &postDecryptFixtureSession{block: block, fragmentIVs: w.fixture.fragmentIVs}, nil
}

func (postDecryptFixtureWrapper) License(context.Context, string, string, string) (string, error) {
	return "", errors.New("unexpected wrapper license request in offline fixture test")
}

type postDecryptFixtureSession struct {
	block       cipher.Block
	fragmentIVs [][][]byte
	next        int
	closed      bool
}

func (s *postDecryptFixtureSession) DecryptFragment(_ string, samples [][]byte) ([][]byte, error) {
	if s.closed {
		return nil, errors.New("offline decrypt session is closed")
	}
	if s.next >= len(s.fragmentIVs) {
		return nil, fmt.Errorf("received more encrypted fragments than fixture IV groups: fragment %d", s.next)
	}
	ivs := s.fragmentIVs[s.next]
	fragmentIndex := s.next
	s.next++
	if len(samples) != len(ivs) {
		return nil, fmt.Errorf("fragment %d has %d samples, fixture has %d IVs", fragmentIndex, len(samples), len(ivs))
	}
	decrypted := make([][]byte, len(samples))
	for i, sample := range samples {
		decrypted[i] = make([]byte, len(sample))
		cipher.NewCTR(s.block, ivs[i]).XORKeyStream(decrypted[i], sample)
	}
	return decrypted, nil
}

func (s *postDecryptFixtureSession) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true
	if s.next != len(s.fragmentIVs) {
		return fmt.Errorf("decrypted %d fragments, fixture describes %d", s.next, len(s.fragmentIVs))
	}
	return nil
}

type postDecryptRestrictedTransport struct {
	base       http.RoundTripper
	allowedURL string
	unexpected *atomic.Int64
}

func (t *postDecryptRestrictedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || req.URL.String() != t.allowedURL {
		t.unexpected.Add(1)
		return nil, fmt.Errorf("offline fixture transport rejected unexpected %s request", req.Method)
	}
	return t.base.RoundTrip(req)
}

type postDecryptReporter struct{}

func (postDecryptReporter) SetJob(context.Context, domain.Job) error         { return nil }
func (postDecryptReporter) AddItem(context.Context, domain.JobItem) error    { return nil }
func (postDecryptReporter) UpdateItem(context.Context, domain.JobItem) error { return nil }
func (postDecryptReporter) RemoveItem(context.Context, string) error         { return nil }
func (postDecryptReporter) ListItems(context.Context, string) ([]domain.JobItem, error) {
	return nil, nil
}
func (postDecryptReporter) Event(context.Context, domain.Event) error { return nil }

var _ jobs.Reporter = postDecryptReporter{}

type postDecryptPhaseRecorder struct {
	mu     sync.Mutex
	phases []string
}

func (r *postDecryptPhaseRecorder) set(status domain.ItemStatus, _ float64, _ string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	phase := string(status)
	if len(r.phases) == 0 || r.phases[len(r.phases)-1] != phase {
		r.phases = append(r.phases, phase)
	}
}

func (r *postDecryptPhaseRecorder) values() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.phases...)
}

type postDecryptProbeResult struct {
	goHeapStart    uint64
	goPeakHeap     uint64
	rssStart       uint64
	rssPeak        uint64
	tempPeak       int64
	systemTempPeak int64
	outputPeak     int64
	combinedPeak   int64
}

type postDecryptProbe struct {
	stopSignal chan struct{}
	done       chan postDecryptProbeResult
}

func startPostDecryptProbe(tempDir, systemTempDir, outputDir string) *postDecryptProbe {
	probe := &postDecryptProbe{
		stopSignal: make(chan struct{}),
		done:       make(chan postDecryptProbeResult, 1),
	}
	ready := make(chan struct{})
	go func() {
		var result postDecryptProbeResult
		sample := func() {
			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			if result.goHeapStart == 0 {
				result.goHeapStart = mem.HeapAlloc
			}
			if mem.HeapAlloc > result.goPeakHeap {
				result.goPeakHeap = mem.HeapAlloc
			}
			rss := postDecryptCurrentRSS()
			if result.rssStart == 0 {
				result.rssStart = rss
			}
			if rss > result.rssPeak {
				result.rssPeak = rss
			}
			tempBytes := postDecryptDirBytes(tempDir)
			systemTempBytes := postDecryptDirBytes(systemTempDir)
			outputBytes := postDecryptDirBytes(outputDir)
			if tempBytes > result.tempPeak {
				result.tempPeak = tempBytes
			}
			if outputBytes > result.outputPeak {
				result.outputPeak = outputBytes
			}
			if systemTempBytes > result.systemTempPeak {
				result.systemTempPeak = systemTempBytes
			}
			if combined := tempBytes + systemTempBytes + outputBytes; combined > result.combinedPeak {
				result.combinedPeak = combined
			}
		}
		sample()
		close(ready)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sample()
			case <-probe.stopSignal:
				sample()
				probe.done <- result
				return
			}
		}
	}()
	// Establish the baseline before downloadBytes can allocate high mode's
	// whole-track buffer; otherwise goroutine scheduling could make the first
	// sample look like the starting footprint and hide the allocation delta.
	<-ready
	return probe
}

func (p *postDecryptProbe) stop() postDecryptProbeResult {
	close(p.stopSignal)
	return <-p.done
}

func postDecryptCurrentRSS() uint64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	raw, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := bytes.Fields(raw)
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseUint(string(fields[1]), 10, 64)
	if err != nil {
		return 0
	}
	return pages * uint64(os.Getpagesize())
}

func postDecryptCgroupMemoryPeak() uint64 {
	if runtime.GOOS != "linux" {
		return 0
	}
	raw, err := os.ReadFile("/sys/fs/cgroup/memory.peak")
	if err != nil {
		return 0
	}
	peak, err := strconv.ParseUint(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0
	}
	return peak
}

func postDecryptDirBytes(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

type postDecryptFixtureMetrics struct {
	Timestamp              string   `json:"timestamp"`
	Mode                   string   `json:"mode"`
	WallMS                 float64  `json:"wall_ms"`
	InputBytes             int64    `json:"input_bytes"`
	FragmentCount          int      `json:"fragment_count"`
	GoHeapStartBytes       uint64   `json:"go_heap_start_bytes"`
	GoPeakHeapBytes        uint64   `json:"go_peak_heap_bytes"`
	GoPeakHeapDeltaBytes   uint64   `json:"go_peak_heap_delta_bytes"`
	RSSStartBytes          uint64   `json:"rss_start_bytes,omitempty"`
	PeakRSSBytes           uint64   `json:"peak_rss_bytes,omitempty"`
	PeakRSSDeltaBytes      uint64   `json:"peak_rss_delta_bytes,omitempty"`
	CgroupMemoryPeakBytes  uint64   `json:"cgroup_memory_peak_bytes,omitempty"`
	TempPeakBytes          int64    `json:"temp_peak_bytes"`
	SystemTempPeakBytes    int64    `json:"system_temp_peak_bytes"`
	OutputPeakBytes        int64    `json:"output_peak_bytes"`
	CombinedPeakBytes      int64    `json:"combined_peak_bytes"`
	SampleIntervalMS       int      `json:"sample_interval_ms"`
	HTTPRequests           int64    `json:"http_requests"`
	UnexpectedHTTPRequests int64    `json:"unexpected_http_requests"`
	Phases                 []string `json:"phases"`
	OutputPath             string   `json:"output_path"`
	OutputBytes            int64    `json:"output_bytes,omitempty"`
	OutputSHA256           string   `json:"output_sha256,omitempty"`
	AudioPCMSHA256         string   `json:"audio_pcm_sha256,omitempty"`
	Error                  string   `json:"error,omitempty"`
}

func postDecryptEmitMetrics(t *testing.T, resultPath string, metrics postDecryptFixtureMetrics) {
	t.Helper()
	encoded, err := json.Marshal(metrics)
	if err != nil {
		t.Fatalf("encode post-decrypt metrics: %v", err)
	}
	t.Log(string(encoded))
	if resultPath == "" {
		return
	}
	absPath, err := filepath.Abs(resultPath)
	if err != nil {
		t.Fatalf("resolve AMDL_POSTDECRYPT_RESULT_PATH: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		t.Fatalf("create metrics directory: %v", err)
	}
	file, err := os.OpenFile(absPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatalf("open metrics JSONL: %v", err)
	}
	line := append(encoded, '\n')
	_, writeErr := file.Write(line)
	closeErr := file.Close()
	if writeErr != nil {
		t.Fatalf("append metrics JSONL: %v", writeErr)
	}
	if closeErr != nil {
		t.Fatalf("close metrics JSONL: %v", closeErr)
	}
}

func postDecryptPCMHash(t *testing.T, ctx context.Context, ffmpeg, path string) string {
	t.Helper()
	// Decode to 32-bit PCM so the 24-bit ALAC fixture's low eight bits remain
	// part of the correctness digest. A 16-bit hash could miss corruption that
	// only affects those bits.
	cmd := exec.CommandContext(ctx, ffmpeg, "-v", "error", "-i", path, "-map", "0:a:0", "-c:a", "pcm_s32le", "-f", "s32le", "pipe:1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("open ffmpeg PCM output: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start ffmpeg PCM hash: %v", err)
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil {
		t.Fatalf("hash decoded PCM: %v", copyErr)
	}
	if waitErr != nil {
		t.Fatalf("decode PCM for hash: %v: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func postDecryptCheckGolden(t *testing.T, ffmpeg, got string) {
	t.Helper()
	want := strings.ToLower(strings.TrimSpace(os.Getenv("AMDL_POSTDECRYPT_GOLDEN_PCM_SHA256")))
	if goldenPath := strings.TrimSpace(os.Getenv("AMDL_POSTDECRYPT_GOLDEN_PATH")); goldenPath != "" {
		goldenInfo := postDecryptRegularFile(t, goldenPath, "golden output")
		if goldenInfo.Size() == 0 {
			t.Fatal("golden output is empty")
		}
		goldenHash := postDecryptPCMHash(t, context.Background(), ffmpeg, goldenPath)
		if want != "" && want != goldenHash {
			t.Fatal("AMDL_POSTDECRYPT_GOLDEN_PATH and AMDL_POSTDECRYPT_GOLDEN_PCM_SHA256 disagree")
		}
		want = goldenHash
	}
	if want == "" {
		return
	}
	decoded, err := hex.DecodeString(want)
	if err != nil || len(decoded) != sha256.Size {
		t.Fatal("AMDL_POSTDECRYPT_GOLDEN_PCM_SHA256 must be a 64-character hexadecimal SHA-256")
	}
	if got != want {
		t.Fatalf("decoded PCM SHA-256 = %s, want golden %s", got, want)
	}
}

func postDecryptFileSHA256(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open output for SHA-256: %v", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatalf("hash output file: %v", err)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func postDecryptCheckBrand(t *testing.T, path string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open finalized output: %v", err)
	}
	defer file.Close()
	var header [12]byte
	if _, err := io.ReadFull(file, header[:]); err != nil {
		t.Fatalf("read finalized M4A header: %v", err)
	}
	if string(header[4:8]) != "ftyp" || string(header[8:12]) != "M4A " {
		t.Fatalf("finalized output has ftyp/major_brand %q/%q, want ftyp/M4A", header[4:8], header[8:12])
	}
}

func postDecryptCheckTags(t *testing.T, path string, song applemusic.Song, lyrics string) {
	t.Helper()
	track, err := mp4tag.Open(path)
	if err != nil {
		t.Fatalf("open finalized tags: %v", err)
	}
	defer track.Close()
	tags, err := track.Read()
	if err != nil {
		t.Fatalf("read finalized tags: %v", err)
	}
	if tags.Title != song.Name || tags.Artist != song.ArtistName || tags.Album != song.AlbumName || tags.Lyrics != lyrics {
		t.Fatalf("finalized metadata mismatch: title=%q artist=%q album=%q lyrics_chars=%d", tags.Title, tags.Artist, tags.Album, len(tags.Lyrics))
	}
	if len(tags.Pictures) != 1 || tags.Pictures[0] == nil || len(tags.Pictures[0].Data) == 0 {
		t.Fatalf("finalized cover count = %d, want one non-empty cover", len(tags.Pictures))
	}
}

func postDecryptRequirePhaseOrder(t *testing.T, got, want []string) {
	t.Helper()
	next := 0
	for _, phase := range got {
		if next < len(want) && phase == want[next] {
			next++
		}
	}
	if next != len(want) {
		t.Fatalf("pipeline phases = %v, want ordered subsequence %v", got, want)
	}
}

func postDecryptExistingDir(t *testing.T, path, label string) string {
	t.Helper()
	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve %s directory: %v", label, err)
	}
	resolved, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		t.Fatalf("resolve %s directory symlinks: %v", label, err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		t.Fatalf("%s directory %q is not readable", label, resolved)
	}
	return resolved
}

func postDecryptRegularFile(t *testing.T, path, label string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", label, err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("%s %q is not a regular file", label, path)
	}
	return info
}

func postDecryptRunDir(t *testing.T, configuredBase, runRoot, fallback, fixtureDir, mode string) string {
	t.Helper()
	base := strings.TrimSpace(configuredBase)
	if base == "" {
		base = runRoot
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		t.Fatalf("resolve %s base: %v", fallback, err)
	}
	postDecryptRejectFixtureWrite(t, absBase, fixtureDir, true)
	if err := os.MkdirAll(absBase, 0o755); err != nil {
		t.Fatalf("create %s base: %v", fallback, err)
	}
	dir, err := os.MkdirTemp(absBase, "amdl-postdecrypt-"+mode+"-"+fallback+"-*")
	if err != nil {
		t.Fatalf("create unique %s directory: %v", fallback, err)
	}
	if os.Getenv("AMDL_POSTDECRYPT_KEEP_ARTIFACTS") == "1" {
		t.Logf("keeping %s fixture artifacts in %s", fallback, dir)
	} else {
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
	}
	return dir
}

func postDecryptUniqueOutputPath(t *testing.T, outputDir, mode string) string {
	t.Helper()
	placeholder, err := os.CreateTemp(outputDir, "result-"+mode+"-*.m4a")
	if err != nil {
		t.Fatalf("reserve unique output path: %v", err)
	}
	path := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		t.Fatalf("close output placeholder: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove output placeholder: %v", err)
	}
	return path
}

func postDecryptRejectFixtureWrite(t *testing.T, path, fixtureDir string, directory bool) {
	t.Helper()
	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("resolve writable harness path: %v", err)
	}
	probe := absPath
	suffix := []string{}
	if !directory {
		suffix = append(suffix, filepath.Base(probe))
		probe = filepath.Dir(probe)
	}
	// Resolve the closest existing ancestor, then reattach any missing path
	// components. This also catches a configured path reached through a symlink
	// into the read-only fixture before MkdirAll has a chance to create it.
	for {
		resolved, resolveErr := filepath.EvalSymlinks(probe)
		if resolveErr == nil {
			absPath = resolved
			for i := len(suffix) - 1; i >= 0; i-- {
				absPath = filepath.Join(absPath, suffix[i])
			}
			break
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			break
		}
		suffix = append(suffix, filepath.Base(probe))
		probe = parent
	}
	rel, err := filepath.Rel(fixtureDir, absPath)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		t.Fatalf("refusing to write harness data inside read-only fixture directory %q", fixtureDir)
	}
}

func postDecryptNonNegativeDelta(peak, start uint64) uint64 {
	if peak <= start {
		return 0
	}
	return peak - start
}
