package hooks

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"amdl/internal/config"
	"amdl/internal/domain"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError + 100}))
}

type eventCollector struct {
	mu   sync.Mutex
	evs  []domain.Event
	done chan struct{}
	want int
}

type recordingRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *recordingRunner) Run(_ context.Context, entry Entry, _ Payload) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, entry.Name)
	return nil
}

func (r *recordingRunner) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = nil
}

func (r *recordingRunner) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func newEventCollector(want int) *eventCollector {
	return &eventCollector{done: make(chan struct{}), want: want}
}

func (c *eventCollector) record(_ context.Context, ev domain.Event) error {
	c.mu.Lock()
	c.evs = append(c.evs, ev)
	n := len(c.evs)
	c.mu.Unlock()
	if n >= c.want {
		select {
		case <-c.done:
		default:
			close(c.done)
		}
	}
	return nil
}

func (c *eventCollector) wait(t *testing.T) []domain.Event {
	t.Helper()
	select {
	case <-c.done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for hook events")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]domain.Event{}, c.evs...)
}

func boolPtr(b bool) *bool { return &b }

func TestDispatchSkipsWhenGloballyDisabled(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{Enabled: false, Entries: []Entry{
		{Name: "h", Type: "webhook", Events: []string{"job_finished"}, URL: server.URL},
	}}
	d := NewDispatcher(cfg, nil, discardLogger())
	d.Dispatch("job_finished", domain.Job{ID: "j1", Type: "song"}, nil)
	d.Shutdown(context.Background())

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("calls = %d, want 0 when hooks.enabled = false", got)
	}
}

func TestDispatchSkipsDisabledEntryAndNonMatchingEventOrJobType(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{Enabled: true, Entries: []Entry{
		{Name: "disabled", Enabled: boolPtr(false), Type: "webhook", Events: []string{"job_finished"}, URL: server.URL},
		{Name: "wrong-event", Type: "webhook", Events: []string{"job_failed"}, URL: server.URL},
		{Name: "wrong-job-type", Type: "webhook", Events: []string{"job_finished"}, JobTypes: []string{"album"}, URL: server.URL},
	}}
	d := NewDispatcher(cfg, nil, discardLogger())
	d.Dispatch("job_finished", domain.Job{ID: "j1", Type: "song"}, nil)
	d.Shutdown(context.Background())

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("calls = %d, want 0 (all entries should be filtered out)", got)
	}
}

func TestList(t *testing.T) {
	var nilDispatcher *Dispatcher
	if got := nilDispatcher.List(); got.Enabled || got.Entries == nil || len(got.Entries) != 0 {
		t.Fatalf("nil dispatcher listing = %+v, want disabled with empty non-nil entries", got)
	}

	d := NewDispatcher(Config{Enabled: false, Entries: []Entry{
		{Name: "default-enabled", Type: "webhook", Events: []string{"job_finished"}},
		{Name: "explicit-enabled", Enabled: boolPtr(true), Type: "exec", Events: []string{"job_failed"}, JobTypes: []string{"album"}},
		{Name: "disabled", Enabled: boolPtr(false), Type: "webhook", Events: []string{"job_queued"}, JobTypes: []string{}},
	}}, nil, discardLogger())
	want := Listing{Enabled: false, Entries: []ListedEntry{
		{Name: "default-enabled", Enabled: true, Type: "webhook", Events: []string{"job_finished"}, JobTypes: []string{}},
		{Name: "explicit-enabled", Enabled: true, Type: "exec", Events: []string{"job_failed"}, JobTypes: []string{"album"}},
		{Name: "disabled", Enabled: false, Type: "webhook", Events: []string{"job_queued"}, JobTypes: []string{}},
	}}
	if got := d.List(); !reflect.DeepEqual(got, want) {
		t.Fatalf("listing = %+v, want %+v", got, want)
	}
}

func TestDispatchHonorsPerJobHookSelection(t *testing.T) {
	runner := &recordingRunner{}
	const runnerType = "selection-test"
	previous, hadPrevious := runners[runnerType]
	runners[runnerType] = runner
	defer func() {
		if hadPrevious {
			runners[runnerType] = previous
		} else {
			delete(runners, runnerType)
		}
	}()

	cfg := Config{Enabled: true, Entries: []Entry{
		{Name: "first", Type: runnerType, Events: []string{"job_finished"}},
		{Name: "second", Type: runnerType, Events: []string{"job_finished"}},
		{Name: "queued", Type: runnerType, Events: []string{"job_queued"}},
		{Name: "disabled", Enabled: boolPtr(false), Type: runnerType, Events: []string{"job_finished"}},
		{Name: "wrong-event", Type: runnerType, Events: []string{"job_failed"}},
		{Name: "wrong-job-type", Type: runnerType, Events: []string{"job_finished"}, JobTypes: []string{"album"}},
	}}
	tests := []struct {
		name  string
		event string
		hooks *[]string
		want  []string
	}{
		{name: "nil list runs all enabled matches", event: "job_finished", want: []string{"first", "second"}},
		{name: "empty list runs none", event: "job_finished", hooks: stringSlicePtr([]string{}), want: nil},
		{name: "named subset runs once despite duplicate names", event: "job_finished", hooks: stringSlicePtr([]string{"second", "second"}), want: []string{"second"}},
		{name: "selection cannot bypass entry filters", event: "job_finished", hooks: stringSlicePtr([]string{"disabled", "wrong-event", "wrong-job-type"}), want: nil},
		{name: "selection filters job queued", event: "job_queued", hooks: stringSlicePtr([]string{"queued"}), want: []string{"queued"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner.reset()
			job := domain.Job{ID: "job-1", Type: "song"}
			if tt.hooks != nil {
				job.Overrides = &config.DownloadOverrides{Hooks: tt.hooks}
			}
			d := NewDispatcher(cfg, nil, discardLogger())
			d.Dispatch(tt.event, job, nil)
			if err := d.Shutdown(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := runner.snapshot()
			sort.Strings(got)
			sort.Strings(tt.want)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("called hooks = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateSelection(t *testing.T) {
	d := NewDispatcher(Config{Enabled: false, Entries: []Entry{{Name: "known"}}}, nil, discardLogger())
	if err := d.ValidateSelection(stringSlicePtr([]string{"known", "known"})); err != nil {
		t.Fatalf("known duplicate names rejected while hooks globally disabled: %v", err)
	}
	if err := d.ValidateSelection(stringSlicePtr([]string{"missing"})); err == nil || !strings.Contains(err.Error(), `unknown hook "missing"`) {
		t.Fatalf("unknown hook error = %v", err)
	}

	emptyConfig := NewDispatcher(Config{}, nil, discardLogger())
	if err := emptyConfig.ValidateSelection(stringSlicePtr([]string{"cannot-run"})); err != nil {
		t.Fatalf("selection against empty hooks config rejected: %v", err)
	}
}

func stringSlicePtr(values []string) *[]string { return &values }

func TestDispatchRunsMatchingWebhookAndRecordsSuccessEvent(t *testing.T) {
	var gotBody []byte
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotBody, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{Enabled: true, TimeoutSeconds: 5, MaxConcurrent: 2, Entries: []Entry{
		{Name: "emby-refresh", Type: "webhook", Events: []string{"job_finished"}, URL: server.URL},
	}}
	collector := newEventCollector(2) // hook_started + hook_succeeded
	d := NewDispatcher(cfg, collector.record, discardLogger())

	job := domain.Job{ID: "job-1", Type: "album", Status: domain.JobCompleted, Input: "https://music.apple.com/us/album/x/1"}
	items := []domain.JobItem{{ID: "item-1", Title: "Song 1", Status: domain.ItemCompleted, OutputPath: "/data/out/1.m4a"}}
	d.Dispatch("job_finished", job, items)

	evs := collector.wait(t)
	d.Shutdown(context.Background())

	var sawStarted, sawSucceeded bool
	for _, ev := range evs {
		if ev.JobID != "job-1" || ev.Phase != "emby-refresh" {
			t.Fatalf("unexpected event: %+v", ev)
		}
		switch ev.Type {
		case "hook_started":
			sawStarted = true
		case "hook_succeeded":
			sawSucceeded = true
		default:
			t.Fatalf("unexpected event type: %s", ev.Type)
		}
	}
	if !sawStarted || !sawSucceeded {
		t.Fatalf("events = %+v, want hook_started and hook_succeeded", evs)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotBody) == 0 {
		t.Fatal("webhook received empty body, want JSON payload (send_payload defaults to true)")
	}
}

func TestDispatchWebhookNoPayloadSendsEmptyBody(t *testing.T) {
	var gotLen int
	var gotContentType string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotLen = len(body)
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{Enabled: true, Entries: []Entry{
		{Name: "refresh", Type: "webhook", Events: []string{"job_finished"}, URL: server.URL, SendPayload: boolPtr(false)},
	}}
	collector := newEventCollector(2)
	d := NewDispatcher(cfg, collector.record, discardLogger())
	d.Dispatch("job_finished", domain.Job{ID: "job-1", Type: "song"}, nil)
	collector.wait(t)
	d.Shutdown(context.Background())

	if gotLen != 0 {
		t.Fatalf("body length = %d, want 0 when send_payload is false", gotLen)
	}
	if gotContentType != "" {
		t.Fatalf("content-type = %q, want empty when send_payload is false", gotContentType)
	}
}

func TestDispatchWebhookFailureRetriesAndRecordsFailure(t *testing.T) {
	var attempts int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	cfg := Config{Enabled: true, Entries: []Entry{
		{Name: "flaky", Type: "webhook", Events: []string{"job_finished"}, URL: server.URL, MaxAttempts: 3},
	}}
	collector := newEventCollector(2) // hook_started + hook_failed
	d := NewDispatcher(cfg, collector.record, discardLogger())
	d.Dispatch("job_finished", domain.Job{ID: "job-1", Type: "song"}, nil)
	evs := collector.wait(t)
	d.Shutdown(context.Background())

	if got := atomic.LoadInt32(&attempts); got != 3 {
		t.Fatalf("attempts = %d, want 3 (max_attempts = 3)", got)
	}
	var sawFailed bool
	for _, ev := range evs {
		if ev.Type == "hook_failed" {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Fatalf("events = %+v, want a hook_failed event", evs)
	}
}

func TestDispatchExecRunsCommandWithEnvAndStdin(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "out.txt")
	cfg := Config{Enabled: true, Entries: []Entry{
		{
			Name: "post-process", Type: "exec", Events: []string{"job_finished"},
			Command: "cat > " + outPath + " && printf '%s' \"$AMDL_JOB_ID:$AMDL_JOB_STATUS\" >> " + outPath,
		},
	}}
	collector := newEventCollector(2)
	d := NewDispatcher(cfg, collector.record, discardLogger())
	job := domain.Job{ID: "job-42", Type: "song", Status: domain.JobCompleted}
	d.Dispatch("job_finished", job, nil)
	collector.wait(t)
	d.Shutdown(context.Background())

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output file: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("exec hook produced no output; stdin payload or env vars may not be wired")
	}
	if !strings.Contains(string(got), "job-42:completed") {
		t.Fatalf("output = %q, want to contain job-42:completed", got)
	}
}

// TestPendingTracksInFlightHookExecution guards the fix for a PR review
// finding: a job's own terminal event is not the last event its stream will
// ever see, because hook dispatch is fire-and-forget and can keep recording
// hook_started/hook_succeeded/hook_failed well after that. Pending must
// report true for the whole time an execution is in flight and only flip to
// false once its terminal hook event has actually been recorded.
func TestPendingTracksInFlightHookExecution(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{Enabled: true, TimeoutSeconds: 5, Entries: []Entry{
		{Name: "h", Type: "webhook", Events: []string{"job_finished"}, URL: server.URL},
	}}
	collector := newEventCollector(2) // hook_started + hook_succeeded
	d := NewDispatcher(cfg, collector.record, discardLogger())

	if d.Pending("job-1") {
		t.Fatal("Pending before Dispatch = true, want false")
	}
	d.Dispatch("job_finished", domain.Job{ID: "job-1", Type: "song"}, nil)

	deadline := time.After(2 * time.Second)
	for !d.Pending("job-1") {
		select {
		case <-deadline:
			t.Fatal("Pending never became true after Dispatch launched the hook")
		case <-time.After(time.Millisecond):
		}
	}

	close(release) // let the blocked webhook call, and hook_succeeded, proceed
	collector.wait(t)
	d.Shutdown(context.Background())

	if d.Pending("job-1") {
		t.Fatal("Pending after hook completion = true, want false")
	}
}

// TestPendingIsNilSafe mirrors Dispatch's existing nil-receiver contract so
// callers (e.g. jobs.Manager.HooksPending) don't need to branch on whether
// hooks are configured at all.
func TestPendingIsNilSafe(t *testing.T) {
	var d *Dispatcher
	if d.Pending("job-1") {
		t.Fatal("nil *Dispatcher Pending = true, want false")
	}
}

// TestShutdownRejectsDispatchAfterClose exercises the WaitGroup safety fix:
// a Shutdown that has already flipped closed=true must not race a concurrent
// Dispatch's wg.Add against its own wg.Wait. Run under -race to catch a
// regression back to the unguarded version.
func TestShutdownRejectsDispatchAfterClose(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{Enabled: true, Entries: []Entry{
		{Name: "h", Type: "webhook", Events: []string{"job_finished"}, URL: server.URL},
	}}
	d := NewDispatcher(cfg, nil, discardLogger())

	d.Shutdown(context.Background())
	d.Dispatch("job_finished", domain.Job{ID: "job-1", Type: "song"}, nil)
	d.Shutdown(context.Background())

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("calls = %d, want 0: Dispatch after Shutdown must be a no-op", got)
	}
}

func TestShutdownCanBeWaitedAgainAfterTimeout(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	d := NewDispatcher(Config{Enabled: true, Entries: []Entry{{
		Name: "blocking", Type: "webhook", Events: []string{"job_finished"}, URL: server.URL,
	}}}, nil, discardLogger())
	d.Dispatch("job_finished", domain.Job{ID: "job-1", Type: "song"}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := d.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error = %v, want deadline exceeded", err)
	}

	close(release)
	if err := d.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after timed-out Shutdown: %v", err)
	}
}

// TestConcurrentDispatchAndShutdownDoesNotRace fuzzes the exact race the
// mutex guard exists to prevent: a producer goroutine calling Dispatch while
// another goroutine calls Shutdown. Must be run with -race.
func TestConcurrentDispatchAndShutdownDoesNotRace(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	cfg := Config{Enabled: true, Entries: []Entry{
		{Name: "h", Type: "webhook", Events: []string{"job_finished"}, URL: server.URL},
	}}
	d := NewDispatcher(cfg, nil, discardLogger())

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			d.Dispatch("job_finished", domain.Job{ID: "job-1", Type: "song"}, nil)
		}
	}()

	d.Shutdown(context.Background())
	wg.Wait()
}
