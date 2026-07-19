package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
	"amdl/internal/hooks"
	"amdl/internal/limits"
)

type recoveryProcessor struct{}

func (recoveryProcessor) ValidateRequest(context.Context, string) (ValidationResult, error) {
	return ValidationResult{Type: "song", Storefront: "cn"}, nil
}

func (recoveryProcessor) ProcessJob(context.Context, domain.Job, Reporter) error {
	return nil
}

type artifactCleanerProcessor struct {
	recoveryProcessor
	mu      sync.Mutex
	cleaned []string
}

func (p *artifactCleanerProcessor) CleanupJobArtifacts(job domain.Job) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleaned = append(p.cleaned, job.ID)
}

func (p *artifactCleanerProcessor) cleanedJobs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.cleaned...)
}

func TestManagerCleansArtifactsForQueuedCancelAndTerminalDelete(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	queued := domain.Job{ID: "job-queued-clean", Input: "queued", Type: "song", Storefront: "cn", Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now}
	failed := domain.Job{ID: "job-failed-clean", Input: "failed", Type: "song", Storefront: "cn", Status: domain.JobFailed, CreatedAt: now, UpdatedAt: now}
	for _, job := range []domain.Job{queued, failed} {
		if err := store.CreateJob(context.Background(), job); err != nil {
			t.Fatal(err)
		}
	}
	processor := &artifactCleanerProcessor{}
	manager := NewManager(store, events.NewHub(), processor, 1, slog.Default())
	if err := manager.Cancel(context.Background(), queued.ID); err != nil {
		t.Fatal(err)
	}
	if err := manager.Delete(context.Background(), failed.ID); err != nil {
		t.Fatal(err)
	}
	if got := processor.cleanedJobs(); len(got) != 2 || got[0] != queued.ID || got[1] != failed.ID {
		t.Fatalf("cleaned jobs = %v, want queued cancel then failed delete", got)
	}
}

func TestManagerDeleteSurvivesCorruptOverridesRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amdl.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	failed := domain.Job{ID: "job-corrupt-overrides", Input: "failed", Type: "song", Storefront: "cn", Status: domain.JobFailed, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateJob(context.Background(), failed); err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE jobs SET overrides='{broken' WHERE id=?`, failed.ID); err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	processor := &artifactCleanerProcessor{}
	manager := NewManager(store, events.NewHub(), processor, 1, slog.Default())
	if err := manager.Delete(context.Background(), failed.ID); err != nil {
		t.Fatalf("Delete with corrupt overrides = %v, want success", err)
	}
	if _, err := store.GetJob(context.Background(), failed.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("job still present after delete: %v", err)
	}
	if got := processor.cleanedJobs(); len(got) != 0 {
		t.Fatalf("cleanup ran without decodable metadata: %v", got)
	}
}

type tokenRecoveryProcessor struct {
	jobs chan domain.Job
}

func (p *tokenRecoveryProcessor) ValidateRequest(context.Context, string) (ValidationResult, error) {
	return ValidationResult{Type: "station", Storefront: "us", ID: "ra.1"}, nil
}

func (p *tokenRecoveryProcessor) ProcessJob(_ context.Context, job domain.Job, _ Reporter) error {
	p.jobs <- job
	return errors.New("keep retryable")
}

type cancelAfterTotalProcessor struct {
	started chan struct{}
	once    sync.Once
}

func (p *cancelAfterTotalProcessor) ValidateRequest(context.Context, string) (ValidationResult, error) {
	return ValidationResult{Type: "artist", Storefront: "cn", ID: "1495777901"}, nil
}

func (p *cancelAfterTotalProcessor) ProcessJob(ctx context.Context, job domain.Job, reporter Reporter) error {
	job.TotalItems = 2
	if err := reporter.SetJob(ctx, &job); err != nil {
		return err
	}
	for i := 1; i <= 2; i++ {
		item := domain.JobItem{
			JobID: job.ID, AdamID: "song", Kind: "song", Index: i, Title: "Song", Status: domain.ItemQueued,
		}
		if err := reporter.AddItem(ctx, &item); err != nil {
			return err
		}
	}
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return ctx.Err()
}

func TestRecoverUnfinishedRequeuesQueuedAndRunningJobs(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	queued := domain.Job{ID: "job-queued", Input: "https://music.apple.com/cn/song/queued/1", Type: "song", Storefront: "cn", CanonicalKey: "song:cn:1", Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now}
	running := domain.Job{ID: "job-running", Input: "https://music.apple.com/cn/song/running/2", Type: "song", Storefront: "cn", CanonicalKey: "song:cn:2", Status: domain.JobRunning, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)}
	completed := domain.Job{ID: "job-completed", Input: "https://music.apple.com/cn/song/completed/3", Type: "song", Storefront: "cn", CanonicalKey: "song:cn:3", Status: domain.JobCompleted, CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second)}
	for _, job := range []domain.Job{queued, running, completed} {
		if err := store.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}

	manager := NewManager(store, events.NewHub(), recoveryProcessor{}, 1, slog.Default())
	recovered, err := manager.RecoverUnfinished(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if recovered != 2 {
		t.Fatalf("recovered = %d, want 2", recovered)
	}
	gotRunning, err := store.GetJob(ctx, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotRunning.Status != domain.JobQueued {
		t.Fatalf("running job status = %s, want %s", gotRunning.Status, domain.JobQueued)
	}
	gotCompleted, err := store.GetJob(ctx, completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotCompleted.Status != domain.JobCompleted {
		t.Fatalf("completed job status = %s, want %s", gotCompleted.Status, domain.JobCompleted)
	}
	manager.recoveryMu.Lock()
	recoveryQueue := append([]string(nil), manager.recoveryQueue...)
	manager.recoveryMu.Unlock()
	if len(recoveryQueue) != 2 {
		t.Fatalf("recovery queue length = %d, want 2", len(recoveryQueue))
	}
	if first, second := recoveryQueue[0], recoveryQueue[1]; first != queued.ID || second != running.ID {
		t.Fatalf("queued ids = [%s, %s], want [%s, %s]", first, second, queued.ID, running.ID)
	}
	events, err := store.ListEventsAfter(ctx, running.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "job_recovered" {
		t.Fatalf("running job recovery events = %+v, want one job_recovered", events)
	}
}

func TestRecoverUnfinishedRestoresMediaUserTokenOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "amdl.db")
	store, err := db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	token := "persisted-token"
	now := time.Now().UTC()
	job := domain.Job{
		ID: "station-recover", Input: "station|us|ra.1", Type: "station", Storefront: "us", CanonicalKey: "station:us:ra.1",
		Overrides: &config.DownloadOverrides{MediaUserToken: &token}, Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateJob(context.Background(), job); err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = db.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	processor := &tokenRecoveryProcessor{jobs: make(chan domain.Job, 1)}
	manager := NewManager(store, events.NewHub(), processor, 1, slog.Default())
	if recovered, err := manager.RecoverUnfinished(context.Background()); err != nil || recovered != 1 {
		t.Fatalf("RecoverUnfinished() = (%d, %v), want (1, nil)", recovered, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.Start(ctx)
	select {
	case recovered := <-processor.jobs:
		if recovered.Overrides == nil || recovered.Overrides.MediaUserToken == nil || *recovered.Overrides.MediaUserToken != token {
			t.Fatalf("recovered token override = %+v, want %q", recovered.Overrides, token)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("recovered job did not run")
	}
	shutdownCtx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

type recoveryBacklogProcessor struct {
	total   int32
	started chan struct{}
	release chan struct{}
	done    chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (p *recoveryBacklogProcessor) ValidateRequest(context.Context, string) (ValidationResult, error) {
	return ValidationResult{Type: "song", Storefront: "cn"}, nil
}

func (p *recoveryBacklogProcessor) ProcessJob(ctx context.Context, _ domain.Job, _ Reporter) error {
	p.started <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
	}
	if p.calls.Add(1) == p.total {
		p.once.Do(func() { close(p.done) })
	}
	return nil
}

func TestRecoverUnfinishedHandlesMoreThanQueueCapacityWithoutLoss(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	const backlog = 300
	for i := 0; i < backlog; i++ {
		id := strconv.Itoa(i)
		job := domain.Job{
			ID: "recover-" + id, Input: "song-" + id, Type: "song", Storefront: "cn",
			CanonicalKey: "song:cn:" + id, Status: domain.JobQueued, CreatedAt: now.Add(time.Duration(i) * time.Microsecond), UpdatedAt: now,
		}
		if err := store.CreateJob(ctx, job); err != nil {
			t.Fatalf("create recoverable job %d: %v", i, err)
		}
	}

	processor := &recoveryBacklogProcessor{
		total: backlog, started: make(chan struct{}, backlog), release: make(chan struct{}), done: make(chan struct{}),
	}
	manager := NewManager(store, events.NewHub(), processor, 8, slog.Default())
	recovered, err := manager.RecoverUnfinished(ctx)
	if err != nil {
		t.Fatalf("RecoverUnfinished: %v", err)
	}
	if recovered != backlog {
		t.Fatalf("recovered = %d, want %d", recovered, backlog)
	}
	manager.recoveryMu.Lock()
	queued := len(manager.recoveryQueue)
	manager.recoveryMu.Unlock()
	if queued != backlog {
		t.Fatalf("recovery queue length = %d, want %d", queued, backlog)
	}
	if len(manager.queue) != 0 {
		t.Fatalf("submission queue length = %d, want 0", len(manager.queue))
	}

	manager.Start(context.Background())
	for i := 0; i < 8; i++ {
		select {
		case <-processor.started:
		case <-time.After(2 * time.Second):
			t.Fatal("workers did not begin draining recovered backlog")
		}
	}
	close(processor.release)
	select {
	case <-processor.done:
	case <-time.After(10 * time.Second):
		t.Fatalf("processed %d/%d recovered jobs", processor.calls.Load(), backlog)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		recoverable, err := store.ListRecoverableJobs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(recoverable) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d recovered jobs remain unfinished", len(recoverable))
		}
		time.Sleep(10 * time.Millisecond)
	}
	shutdownCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := processor.calls.Load(); got != backlog {
		t.Fatalf("processor calls = %d, want %d", got, backlog)
	}
}

func TestCancelledJobPreservesProcessorUpdatedTotalItems(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	processor := &cancelAfterTotalProcessor{started: make(chan struct{})}
	manager := NewManager(store, events.NewHub(), processor, 1, slog.Default())
	manager.Start(ctx)
	resp := manager.SubmitBatch(ctx, []string{"https://music.apple.com/cn/artist/example/1495777901"}, false, nil)
	if resp.Accepted != 1 || len(resp.Results) != 1 || resp.Results[0].Status != domain.SubmitAccepted || resp.Results[0].Job == nil {
		t.Fatalf("unexpected submit result: %+v", resp)
	}
	job := *resp.Results[0].Job

	select {
	case <-processor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not start")
	}
	if err := manager.Cancel(ctx, job.ID); err != nil {
		t.Fatal(err)
	}

	var got domain.Job
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		got, err = store.GetJob(ctx, job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == domain.JobCancelled && got.Error == "cancelled" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Status != domain.JobCancelled {
		t.Fatalf("status = %s, want %s", got.Status, domain.JobCancelled)
	}
	if got.TotalItems != 2 {
		t.Fatalf("total_items = %d, want 2", got.TotalItems)
	}
}

// keyedProcessor derives ValidationResult from a "type|storefront|id" test
// URL so canonical-key dedup can be exercised without real Apple Music
// parsing. URLs prefixed "bad:" are treated as invalid.
type keyedProcessor struct{}

func (keyedProcessor) ValidateRequest(_ context.Context, url string) (ValidationResult, error) {
	if strings.HasPrefix(url, "bad:") {
		return ValidationResult{}, &RequestError{Code: "invalid_url", Message: "bad test url"}
	}
	parts := strings.SplitN(url, "|", 3)
	if len(parts) != 3 {
		return ValidationResult{}, &RequestError{Code: "invalid_url", Message: "malformed test url"}
	}
	return ValidationResult{Type: parts[0], Storefront: parts[1], ID: parts[2]}, nil
}

func (keyedProcessor) ProcessJob(context.Context, domain.Job, Reporter) error {
	return nil
}

type shutdownProcessor struct {
	keyedProcessor
	started chan struct{}
	exited  chan struct{}
	once    sync.Once
}

func (p *shutdownProcessor) ProcessJob(ctx context.Context, _ domain.Job, _ Reporter) error {
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	close(p.exited)
	return ctx.Err()
}

func TestManagerShutdownCancelsAndWaitsForRunningWorker(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	processor := &shutdownProcessor{started: make(chan struct{}), exited: make(chan struct{})}
	manager := NewManager(store, events.NewHub(), processor, 1, slog.Default())
	manager.Start(context.Background())
	resp := manager.SubmitBatch(context.Background(), []string{"song|cn|shutdown"}, false, nil)
	if resp.Accepted != 1 {
		t.Fatalf("submit = %+v", resp)
	}
	select {
	case <-processor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not start")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case <-processor.exited:
	default:
		t.Fatal("Shutdown returned before ProcessJob exited")
	}
	if err := manager.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after Shutdown: %v", err)
	}

	job, err := store.GetJob(context.Background(), resp.Results[0].Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != domain.JobCancelled {
		t.Fatalf("job status = %s, want %s", job.Status, domain.JobCancelled)
	}
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return NewManager(store, events.NewHub(), keyedProcessor{}, 1, slog.Default())
}

func TestSubmitBatchStoresTokenOnlyForJobsThatNeedIt(t *testing.T) {
	manager := newTestManager(t)
	token := "batch-token"
	embedLyrics := false
	overrides := &config.DownloadOverrides{MediaUserToken: &token, EmbedLyrics: &embedLyrics}
	resp := manager.SubmitBatch(context.Background(), []string{
		"station|us|ra.1",
		"playlist|us|pl.u-private",
		"playlist|us|pl.editorial",
		"song|us|1",
	}, false, overrides)
	if resp.Accepted != 4 {
		t.Fatalf("accepted = %d, want 4: %+v", resp.Accepted, resp.Results)
	}
	want := []string{"batch-token", "batch-token", "", ""}
	for i, result := range resp.Results {
		job, err := manager.store.GetJob(context.Background(), result.Job.ID)
		if err != nil {
			t.Fatal(err)
		}
		if job.Overrides == nil || job.Overrides.EmbedLyrics == nil || *job.Overrides.EmbedLyrics {
			t.Fatalf("job %s (%s) lost non-token overrides: %+v", result.Job.ID, result.URL, job.Overrides)
		}
		got := ""
		if job.Overrides.MediaUserToken != nil {
			got = *job.Overrides.MediaUserToken
		}
		if got != want[i] {
			t.Fatalf("job %s (%s) token = %q, want %q", result.Job.ID, result.URL, got, want[i])
		}
		if want[i] == "" && job.Overrides.MediaUserToken != nil {
			t.Fatalf("job %s (%s) retained a token pointer for an unrelated job", result.Job.ID, result.URL)
		}
	}
	if overrides.MediaUserToken == nil || *overrides.MediaUserToken != token {
		t.Fatalf("SubmitBatch mutated the caller's overrides: %+v", overrides)
	}
}

// failingProcessor validates like keyedProcessor but every job fails, so
// tests can observe the failed-terminal state.
type failingProcessor struct{ keyedProcessor }

func (failingProcessor) ProcessJob(context.Context, domain.Job, Reporter) error {
	return errors.New("boom")
}

func TestTerminalJobDropsPersistedTokenUnlessRetryable(t *testing.T) {
	newOverrides := func() *config.DownloadOverrides {
		token := "tok"
		return &config.DownloadOverrides{MediaUserToken: &token}
	}
	waitToken := func(t *testing.T, m *Manager, jobID, want string, wantPresent bool) {
		t.Helper()
		var job domain.Job
		deadline := time.Now().Add(2 * time.Second)
		for {
			var err error
			job, err = m.store.GetJob(context.Background(), jobID)
			if err == nil && job.Status.IsTerminal() && !m.FinalizeInFlight(jobID) {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %s never finalized", jobID)
			}
			time.Sleep(10 * time.Millisecond)
		}
		got := ""
		present := job.Overrides != nil && job.Overrides.MediaUserToken != nil
		if present {
			got = *job.Overrides.MediaUserToken
		}
		if got != want || present != wantPresent {
			t.Fatalf("token after terminal = %q, want %q", got, want)
		}
	}

	t.Run("completed drops the token", func(t *testing.T) {
		manager := newTestManager(t)
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		manager.Start(ctx)
		resp := manager.SubmitBatch(ctx, []string{"station|us|ra.1"}, false, newOverrides())
		if resp.Accepted != 1 {
			t.Fatalf("submit: %+v", resp.Results)
		}
		waitToken(t, manager, resp.Results[0].Job.ID, "", false)
	})

	t.Run("cancelled drops the token", func(t *testing.T) {
		manager := newTestManager(t)
		resp := manager.SubmitBatch(context.Background(), []string{"station|us|ra.1"}, false, newOverrides())
		if resp.Accepted != 1 {
			t.Fatalf("submit: %+v", resp.Results)
		}
		jobID := resp.Results[0].Job.ID
		if err := manager.Cancel(context.Background(), jobID); err != nil {
			t.Fatal(err)
		}
		waitToken(t, manager, jobID, "", false)
	})

	t.Run("failed keeps the token for retry", func(t *testing.T) {
		store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		manager := NewManager(store, events.NewHub(), failingProcessor{}, 1, slog.Default())
		ctx, stop := context.WithCancel(context.Background())
		defer stop()
		manager.Start(ctx)
		resp := manager.SubmitBatch(ctx, []string{"station|us|ra.1"}, false, newOverrides())
		if resp.Accepted != 1 {
			t.Fatalf("submit: %+v", resp.Results)
		}
		waitToken(t, manager, resp.Results[0].Job.ID, "tok", true)
	})
}

func TestSubmitBatchDedupesWithinRequest(t *testing.T) {
	manager := newTestManager(t)
	ctx := context.Background()
	resp := manager.SubmitBatch(ctx, []string{
		"album|us|111",
		"song|us|222",
		"album|us|111", // same canonical key as the first entry
	}, false, nil)
	if len(resp.Results) != 3 {
		t.Fatalf("results = %+v, want 3", resp.Results)
	}
	if resp.Results[0].Status != domain.SubmitAccepted || resp.Results[1].Status != domain.SubmitAccepted {
		t.Fatalf("first two results = %+v, want accepted", resp.Results[:2])
	}
	if resp.Results[2].Status != domain.SubmitDuplicateInRequest {
		t.Fatalf("third result = %+v, want duplicate_in_request", resp.Results[2])
	}
	if resp.Accepted != 2 || resp.Rejected != 1 {
		t.Fatalf("resp = %+v, want 2 accepted / 1 rejected", resp)
	}
}

func TestSubmitBatchRejectsActiveDuplicateButAllowsAfterCompletion(t *testing.T) {
	manager := newTestManager(t)
	ctx := context.Background()

	first := manager.SubmitBatch(ctx, []string{"song|us|222"}, false, nil)
	if first.Accepted != 1 {
		t.Fatalf("first submit = %+v, want 1 accepted", first)
	}
	jobID := first.Results[0].Job.ID

	second := manager.SubmitBatch(ctx, []string{"song|us|222"}, false, nil)
	if second.Results[0].Status != domain.SubmitDuplicateActive || second.Results[0].ExistingJobID != jobID {
		t.Fatalf("second submit = %+v, want duplicate_active for %s", second.Results[0], jobID)
	}

	if err := manager.store.UpdateJobStatus(ctx, jobID, domain.JobCompleted, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	third := manager.SubmitBatch(ctx, []string{"song|us|222"}, false, nil)
	if third.Results[0].Status != domain.SubmitAccepted {
		t.Fatalf("third submit = %+v, want accepted after completion", third.Results[0])
	}
}

func TestSubmitBatchQueueFullMarksRemainingWithoutRollback(t *testing.T) {
	manager := newTestManager(t)
	manager.queue = make(chan string, 1)
	ctx := context.Background()

	resp := manager.SubmitBatch(ctx, []string{"song|us|1", "song|us|2", "song|us|3"}, false, nil)
	if resp.Results[0].Status != domain.SubmitAccepted {
		t.Fatalf("first = %+v, want accepted", resp.Results[0])
	}
	if resp.Results[1].Status != domain.SubmitQueueFull || resp.Results[2].Status != domain.SubmitQueueFull {
		t.Fatalf("remaining = %+v, want queue_full", resp.Results[1:])
	}
	if resp.Accepted != 1 || resp.Rejected != 2 {
		t.Fatalf("resp = %+v, want 1 accepted / 2 rejected", resp)
	}
}

func TestSubmitBatchInvalidURLReportsError(t *testing.T) {
	manager := newTestManager(t)
	resp := manager.SubmitBatch(context.Background(), []string{"bad:not-a-url"}, false, nil)
	if resp.Results[0].Status != domain.SubmitInvalid || resp.Results[0].Error == "" {
		t.Fatalf("result = %+v, want invalid with error message", resp.Results[0])
	}
}

func TestJobCompletionDispatchesHook(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	hooksCfg := hooks.Config{Enabled: true, Entries: []hooks.Entry{
		{Name: "on-finish", Type: "webhook", Events: []string{"job_finished"}, URL: server.URL},
	}}
	manager := NewManager(store, events.NewHub(), keyedProcessor{}, 1, slog.Default())
	dispatcher := hooks.NewDispatcher(hooksCfg, manager.Event, slog.Default())
	manager.SetHooks(dispatcher)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	manager.Start(ctx)

	resp := manager.SubmitBatch(ctx, []string{"song|us|1"}, false, nil)
	if resp.Accepted != 1 {
		t.Fatalf("submit = %+v, want 1 accepted", resp)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&calls) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	// Drain the dispatcher before asserting so the hook goroutine has fully
	// recorded its result and doesn't outlive the test.
	dispatcher.Shutdown(context.Background())
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("webhook calls = %d, want 1 after job completion", got)
	}
}

func TestJobQueuedDispatchesHook(t *testing.T) {
	var calls int32
	var mu sync.Mutex
	var gotEvent, gotStatus string
	var gotTotal, gotItems int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
			Job   struct {
				Status     string `json:"status"`
				TotalItems int    `json:"total_items"`
			} `json:"job"`
			Items []struct{} `json:"items"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		gotEvent = payload.Event
		gotStatus = payload.Job.Status
		gotTotal = payload.Job.TotalItems
		gotItems = len(payload.Items)
		mu.Unlock()
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	hooksCfg := hooks.Config{Enabled: true, Entries: []hooks.Entry{
		{Name: "on-queued", Type: "webhook", Events: []string{"job_queued"}, URL: server.URL},
	}}
	manager := NewManager(store, events.NewHub(), keyedProcessor{}, 1, slog.Default())
	dispatcher := hooks.NewDispatcher(hooksCfg, manager.Event, slog.Default())
	manager.SetHooks(dispatcher)

	// Deliberately do NOT start workers: the creation hook fires from
	// SubmitBatch itself, so the job stays queued and only job_queued can fire.
	resp := manager.SubmitBatch(context.Background(), []string{"song|us|1"}, false, nil)
	if resp.Accepted != 1 {
		t.Fatalf("submit = %+v, want 1 accepted", resp)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&calls) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	dispatcher.Shutdown(context.Background())

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("webhook calls = %d, want 1 after job creation", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if gotEvent != "job_queued" {
		t.Fatalf("event = %q, want job_queued", gotEvent)
	}
	if gotStatus != string(domain.JobQueued) {
		t.Fatalf("job status = %q, want %q", gotStatus, domain.JobQueued)
	}
	if gotTotal != 0 || gotItems != 0 {
		t.Fatalf("creation payload total_items=%d items=%d, want 0 and 0 (no tracks resolved yet)", gotTotal, gotItems)
	}
}

// TestRecoverUnfinishedDoesNotDispatchJobQueuedHook guards the documented
// design decision that recovery re-enqueues jobs without re-firing the
// job_queued creation hook: those jobs were created before the restart, so
// dispatching job_queued again would double-notify external systems.
func TestRecoverUnfinishedDoesNotDispatchJobQueuedHook(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	now := time.Now().UTC()
	queued := domain.Job{ID: "job-recover", Input: "https://music.apple.com/cn/song/q/1", Type: "song", Storefront: "cn", CanonicalKey: "song:cn:1", Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateJob(ctx, queued); err != nil {
		t.Fatal(err)
	}

	hooksCfg := hooks.Config{Enabled: true, Entries: []hooks.Entry{
		{Name: "on-queued", Type: "webhook", Events: []string{"job_queued"}, URL: server.URL},
	}}
	manager := NewManager(store, events.NewHub(), recoveryProcessor{}, 1, slog.Default())
	dispatcher := hooks.NewDispatcher(hooksCfg, manager.Event, slog.Default())
	manager.SetHooks(dispatcher)

	// Do NOT start workers: recovery itself must not fire the creation hook.
	if _, err := manager.RecoverUnfinished(ctx); err != nil {
		t.Fatal(err)
	}
	// Dispatch launches its webhook goroutine synchronously, so any erroneous
	// dispatch during recovery is already counted in the WaitGroup that
	// Shutdown drains — no arbitrary sleep needed.
	dispatcher.Shutdown(context.Background())
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("job_queued hook calls after recovery = %d, want 0", got)
	}
}

func TestCancelQueuedJobDispatchesCancelledHookAndNeverRuns(t *testing.T) {
	var calls int32
	var lastEvent string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		lastEvent = payload.Event
		mu.Unlock()
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	hooksCfg := hooks.Config{Enabled: true, Entries: []hooks.Entry{
		{Name: "on-cancel", Type: "webhook", Events: []string{"job_cancelled"}, URL: server.URL},
	}}
	// A processor that fails the test if ProcessJob is ever invoked: a
	// cancelled-while-queued job must never actually run.
	processor := &neverRunProcessor{t: t}
	manager := NewManager(store, events.NewHub(), processor, 1, slog.Default())
	dispatcher := hooks.NewDispatcher(hooksCfg, manager.Event, slog.Default())
	manager.SetHooks(dispatcher)

	// Deliberately do not call manager.Start(ctx): the submitted job stays in
	// the in-memory queue channel, never dequeued, so Cancel() must take the
	// "not yet running" path.
	ctx := context.Background()
	resp := manager.SubmitBatch(ctx, []string{"song|us|1"}, false, nil)
	if resp.Accepted != 1 {
		t.Fatalf("submit = %+v, want 1 accepted", resp)
	}
	jobID := resp.Results[0].Job.ID

	if err := manager.Cancel(ctx, jobID); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	got, err := store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.JobCancelled {
		t.Fatalf("status = %s, want cancelled", got.Status)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&calls) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	dispatcher.Shutdown(context.Background())
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("webhook calls = %d, want exactly 1 for the queued-cancel path", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if lastEvent != "job_cancelled" {
		t.Fatalf("event = %q, want job_cancelled", lastEvent)
	}

	// Simulate a worker eventually dequeuing this already-cancelled job: it
	// must not resurrect the job into JobRunning or invoke the processor.
	manager.run(ctx, jobID)
	got, err = store.GetJob(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.JobCancelled {
		t.Fatalf("status after late run() = %s, want cancelled (must not resurrect)", got.Status)
	}
}

func TestDeleteRefusesActiveAndFinalizingJobs(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	processor := &cancelAfterTotalProcessor{started: make(chan struct{})}
	manager := NewManager(store, events.NewHub(), processor, 1, slog.Default())

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	manager.Start(ctx)

	resp := manager.SubmitBatch(ctx, []string{"artist|cn|1495777901"}, false, nil)
	if resp.Accepted != 1 {
		t.Fatalf("submit = %+v, want 1 accepted", resp)
	}
	jobID := resp.Results[0].Job.ID
	<-processor.started

	// Running: the job sits in m.cancels until finalize completes.
	if err := manager.Delete(ctx, jobID); !errors.Is(err, db.ErrJobNotTerminal) {
		t.Fatalf("Delete(running) error = %v, want ErrJobNotTerminal", err)
	}

	// Finalize-in-flight (Cancel's queued path): status row already reads
	// terminal but the marker must still refuse deletion.
	manager.mu.Lock()
	manager.finalizing["job_finalizing"] = true
	manager.mu.Unlock()
	if err := manager.Delete(ctx, "job_finalizing"); !errors.Is(err, db.ErrJobNotTerminal) {
		t.Fatalf("Delete(finalizing) error = %v, want ErrJobNotTerminal", err)
	}

	if err := manager.Cancel(ctx, jobID); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	// Delete keeps refusing until run()'s finalize fully completes and clears
	// the m.cancels entry, then succeeds exactly on the row it guarded.
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := manager.Delete(ctx, jobID)
		if err == nil {
			break
		}
		if !errors.Is(err, db.ErrJobNotTerminal) {
			t.Fatalf("Delete() error = %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("Delete() still refused 2s after Cancel")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := store.GetJob(ctx, jobID); err == nil {
		t.Fatal("job row still exists after successful Delete")
	}
}

func TestDeleteRefusesJobWhileHookIsPending(t *testing.T) {
	release := make(chan struct{})
	hookServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer hookServer.Close()

	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	manager := NewManager(store, events.NewHub(), recoveryProcessor{}, 1, slog.Default())
	dispatcher := hooks.NewDispatcher(hooks.Config{Enabled: true, Entries: []hooks.Entry{{
		Name: "blocking", Type: "webhook", Events: []string{"job_finished"}, URL: hookServer.URL,
	}}}, manager.Event, slog.Default())
	manager.SetHooks(dispatcher)

	job := domain.Job{ID: "job-with-hook", Input: "song|us|1", Type: "song", CanonicalKey: "song:us:1", Status: domain.JobCompleted}
	if err := store.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	dispatcher.Dispatch("job_finished", job, nil)
	if err := manager.Delete(context.Background(), job.ID); !errors.Is(err, db.ErrJobNotTerminal) {
		t.Fatalf("Delete() while hook pending error = %v, want ErrJobNotTerminal", err)
	}

	close(release)
	dispatcher.Shutdown(context.Background())
	if err := manager.Delete(context.Background(), job.ID); err != nil {
		t.Fatalf("Delete() after hook completed: %v", err)
	}
}

func TestCancelRunningJobDispatchesCancelledHookExactlyOnce(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	hooksCfg := hooks.Config{Enabled: true, Entries: []hooks.Entry{
		{Name: "on-cancel", Type: "webhook", Events: []string{"job_cancelled"}, URL: server.URL},
	}}
	processor := &cancelAfterTotalProcessor{started: make(chan struct{})}
	manager := NewManager(store, events.NewHub(), processor, 1, slog.Default())
	dispatcher := hooks.NewDispatcher(hooksCfg, manager.Event, slog.Default())
	manager.SetHooks(dispatcher)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	manager.Start(ctx)

	resp := manager.SubmitBatch(ctx, []string{"song|us|1"}, false, nil)
	if resp.Accepted != 1 {
		t.Fatalf("submit = %+v, want 1 accepted", resp)
	}
	jobID := resp.Results[0].Job.ID

	select {
	case <-processor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not start")
	}
	if err := manager.Cancel(ctx, jobID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var got domain.Job
	for time.Now().Before(deadline) {
		got, err = store.GetJob(ctx, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == domain.JobCancelled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Status != domain.JobCancelled {
		t.Fatalf("status = %s, want cancelled", got.Status)
	}

	// Give any (incorrect) duplicate dispatch a chance to land before asserting.
	time.Sleep(200 * time.Millisecond)
	dispatcher.Shutdown(context.Background())
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("webhook calls = %d, want exactly 1 (no duplicate dispatch between Cancel and run)", got)
	}
}

// neverRunProcessor fails the test if ProcessJob is ever called.
type neverRunProcessor struct{ t *testing.T }

func (p *neverRunProcessor) ValidateRequest(context.Context, string) (ValidationResult, error) {
	return ValidationResult{Type: "song", Storefront: "us", ID: "1"}, nil
}

func (p *neverRunProcessor) ProcessJob(context.Context, domain.Job, Reporter) error {
	p.t.Fatal("ProcessJob must not run for a job cancelled while queued")
	return nil
}

func TestNilHooksDispatcherIsNoop(t *testing.T) {
	manager := newTestManager(t)
	manager.Start(context.Background())
	resp := manager.SubmitBatch(context.Background(), []string{"song|us|1"}, false, nil)
	if resp.Accepted != 1 {
		t.Fatalf("submit = %+v, want 1 accepted", resp)
	}
	deadline := time.Now().Add(2 * time.Second)
	var job domain.Job
	for time.Now().Before(deadline) {
		job, _ = manager.store.GetJob(context.Background(), resp.Results[0].Job.ID)
		if job.Status == domain.JobCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.Status != domain.JobCompleted {
		t.Fatalf("job status = %s, want completed (manager.hooks is nil until SetHooks is called)", job.Status)
	}
}

// TestHooksPendingIsNilSafe mirrors SetHooks/Dispatch's existing nil-receiver
// contract: server.go's events/eventsWS handlers call HooksPending on every
// terminal job regardless of whether SetHooks was ever called, and must not
// need a nil *Manager check either.
func TestHooksPendingIsNilSafe(t *testing.T) {
	var m *Manager
	if m.HooksPending("job-1") {
		t.Fatal("nil *Manager HooksPending = true, want false")
	}

	manager := newTestManager(t) // hooks left unset (nil dispatcher)
	if manager.HooksPending("job-1") {
		t.Fatal("HooksPending with unset hooks dispatcher = true, want false")
	}
}

// TestFinalizeInFlightTracksFinalizeMarks pins the contract eventsExhausted
// (internal/api) relies on to close the pre-dispatch gap: a job's terminal
// status becomes visible before hook dispatch increments the dispatcher's
// pending count, and in that window the job is still covered by either its
// m.cancels entry (run's finalize path) or its m.finalizing mark (Cancel's
// queued path). FinalizeInFlight must report true for both.
func TestFinalizeInFlightTracksFinalizeMarks(t *testing.T) {
	var nilManager *Manager
	if nilManager.FinalizeInFlight("job-1") {
		t.Fatal("nil *Manager FinalizeInFlight = true, want false")
	}

	manager := newTestManager(t)
	if manager.FinalizeInFlight("job-1") {
		t.Fatal("FinalizeInFlight with no marks = true, want false")
	}

	manager.mu.Lock()
	manager.cancels["job-1"] = func() {}
	manager.mu.Unlock()
	if !manager.FinalizeInFlight("job-1") {
		t.Fatal("FinalizeInFlight with a cancels entry = false, want true")
	}

	manager.mu.Lock()
	delete(manager.cancels, "job-1")
	manager.finalizing["job-1"] = true
	manager.mu.Unlock()
	if !manager.FinalizeInFlight("job-1") {
		t.Fatal("FinalizeInFlight with a finalizing mark = false, want true")
	}

	manager.mu.Lock()
	delete(manager.finalizing, "job-1")
	manager.mu.Unlock()
	if manager.FinalizeInFlight("job-1") {
		t.Fatal("FinalizeInFlight after marks cleared = true, want false")
	}
}

// cancelThenReturnNilProcessor blocks until its context is cancelled and then
// returns nil, simulating a processor that finishes "successfully" right as a
// cancel arrives (or one that doesn't surface ctx errors).
type cancelThenReturnNilProcessor struct {
	started chan struct{}
	once    sync.Once
}

func (p *cancelThenReturnNilProcessor) ValidateRequest(context.Context, string) (ValidationResult, error) {
	return ValidationResult{Type: "song", Storefront: "us", ID: "1"}, nil
}

func (p *cancelThenReturnNilProcessor) ProcessJob(ctx context.Context, _ domain.Job, _ Reporter) error {
	p.once.Do(func() { close(p.started) })
	<-ctx.Done()
	return nil
}

func TestCancelRunningJobWinsOverNilProcessorReturn(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		seen = append(seen, payload.Event)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	hooksCfg := hooks.Config{Enabled: true, Entries: []hooks.Entry{
		{Name: "terminal", Type: "webhook", Events: []string{"job_finished", "job_cancelled"}, URL: server.URL},
	}}
	processor := &cancelThenReturnNilProcessor{started: make(chan struct{})}
	manager := NewManager(store, events.NewHub(), processor, 1, slog.Default())
	dispatcher := hooks.NewDispatcher(hooksCfg, manager.Event, slog.Default())
	manager.SetHooks(dispatcher)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	manager.Start(ctx)

	resp := manager.SubmitBatch(ctx, []string{"song|us|1"}, false, nil)
	if resp.Accepted != 1 {
		t.Fatalf("submit = %+v, want 1 accepted", resp)
	}
	jobID := resp.Results[0].Job.ID

	select {
	case <-processor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("processor did not start")
	}
	if err := manager.Cancel(ctx, jobID); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var got domain.Job
	for time.Now().Before(deadline) {
		got, err = store.GetJob(ctx, jobID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status == domain.JobCancelled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Even though ProcessJob returned nil, cancellation must win: the job is
	// cancelled and the hook is job_cancelled, never job_finished.
	if got.Status != domain.JobCancelled {
		t.Fatalf("status = %s, want cancelled even though ProcessJob returned nil", got.Status)
	}

	time.Sleep(200 * time.Millisecond)
	dispatcher.Shutdown(context.Background())
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "job_cancelled" {
		t.Fatalf("hook events = %v, want exactly [job_cancelled]", seen)
	}
}

func TestFinalizeJobSkipsHookWhenPersistenceFails(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}

	hooksCfg := hooks.Config{Enabled: true, Entries: []hooks.Entry{
		{Name: "on-cancel", Type: "webhook", Events: []string{"job_cancelled"}, URL: server.URL},
	}}
	manager := NewManager(store, events.NewHub(), keyedProcessor{}, 1, slog.Default())
	dispatcher := hooks.NewDispatcher(hooksCfg, manager.Event, slog.Default())
	manager.SetHooks(dispatcher)

	// Close the store so the terminal-status write inside finalizeJob fails.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	job := domain.Job{ID: "job-x", Type: "song", Status: domain.JobCancelled, Error: "cancelled"}
	if err := manager.finalizeJob(context.Background(), job, "job_cancelled", "job cancelled"); err == nil {
		t.Fatal("finalizeJob returned nil, want a persistence error when the store is closed")
	}

	// The hook must not fire when the terminal status could not be persisted.
	time.Sleep(200 * time.Millisecond)
	dispatcher.Shutdown(context.Background())
	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("webhook calls = %d, want 0 when persistence failed", got)
	}
}

func TestTerminalJobEventPayloadCarriesRESTJobSnapshot(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	embed := false
	now := time.Now().UTC().Add(-time.Minute)
	job := domain.Job{
		ID: "job-terminal-payload", Input: "song|us|1", Type: "song", Storefront: "us",
		CanonicalKey: "song:us:1", Status: domain.JobRunning, TotalItems: 1,
		Overrides: &config.DownloadOverrides{EmbedCover: &embed}, CreatedAt: now, UpdatedAt: now,
	}
	if err := store.CreateJob(context.Background(), job); err != nil {
		t.Fatal(err)
	}

	manager := NewManager(store, events.NewHub(), keyedProcessor{}, 1, slog.Default())
	job.Status = domain.JobCompleted
	job.DoneItems = 1
	if err := manager.persistTerminal(context.Background(), job, "job_finished", "completed"); err != nil {
		t.Fatal(err)
	}

	recorded, err := store.ListEventsAfter(context.Background(), job.ID, 0)
	if err != nil || len(recorded) != 1 {
		t.Fatalf("terminal events = %+v, err=%v, want one", recorded, err)
	}
	var snapshot domain.Job
	if err := json.Unmarshal([]byte(recorded[0].Payload), &snapshot); err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(recorded[0].Payload), &fields); err != nil {
		t.Fatal(err)
	}
	if snapshot.ID != job.ID || snapshot.Status != domain.JobCompleted || snapshot.DoneItems != 1 || snapshot.UpdatedAt.IsZero() || fields["message"] != "completed" {
		t.Fatalf("terminal payload = %s, want complete REST job snapshot plus message", recorded[0].Payload)
	}
}

// raceProcessor either completes quickly or, if cancelled during its short
// work window, returns the context error. This maximizes the variety of
// interleavings between run()'s startup claim and a concurrent Cancel.
type raceProcessor struct{}

func (raceProcessor) ValidateRequest(_ context.Context, url string) (ValidationResult, error) {
	parts := strings.SplitN(url, "|", 3)
	return ValidationResult{Type: parts[0], Storefront: parts[1], ID: parts[2]}, nil
}

func (raceProcessor) ProcessJob(ctx context.Context, _ domain.Job, _ Reporter) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Millisecond):
		return nil
	}
}

// TestCancelRacingStartupDispatchesExactlyOneConsistentHook stresses the exact
// interface race between a worker claiming a queued job (run() marking it
// running) and a concurrent Cancel(). For every job — however the two
// interleave — exactly one terminal hook must fire, and it must match the
// persisted final status. Before the startup claim was serialized under m.mu,
// this window could produce both a job_cancelled hook (from Cancel) and a
// job_finished hook (from a resurrected worker), with the job downloaded
// despite the cancel. Run with -race.
func TestCancelRacingStartupDispatchesExactlyOneConsistentHook(t *testing.T) {
	const jobs = 150

	var mu sync.Mutex
	hookEvents := map[string][]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Event string `json:"event"`
			Job   struct {
				ID string `json:"id"`
			} `json:"job"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		hookEvents[payload.Job.ID] = append(hookEvents[payload.Job.ID], payload.Event)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	hooksCfg := hooks.Config{Enabled: true, MaxConcurrent: 8, Entries: []hooks.Entry{
		{Name: "terminal", Type: "webhook", Events: []string{"job_finished", "job_failed", "job_cancelled"}, URL: server.URL},
	}}
	manager := NewManager(store, events.NewHub(), raceProcessor{}, 4, slog.Default())
	dispatcher := hooks.NewDispatcher(hooksCfg, manager.Event, slog.Default())
	manager.SetHooks(dispatcher)

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	manager.Start(ctx)

	jobIDs := make([]string, 0, jobs)
	var wg sync.WaitGroup
	for i := 0; i < jobs; i++ {
		url := "song|us|" + strconv.Itoa(i)
		resp := manager.SubmitBatch(ctx, []string{url}, false, nil)
		if resp.Accepted != 1 || resp.Results[0].Job == nil {
			t.Fatalf("submit %d = %+v, want 1 accepted", i, resp)
		}
		jobID := resp.Results[0].Job.ID
		jobIDs = append(jobIDs, jobID)
		// Fire the cancel concurrently with the worker picking the job up.
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			_ = manager.Cancel(context.Background(), id)
		}(jobID)
	}
	wg.Wait()

	// Wait for every job to reach a terminal status.
	deadline := time.Now().Add(10 * time.Second)
	for {
		pending := 0
		for _, id := range jobIDs {
			job, err := store.GetJob(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if !job.Status.IsTerminal() {
				pending++
			}
		}
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d jobs did not reach a terminal status in time", pending)
		}
		time.Sleep(20 * time.Millisecond)
	}

	dispatcher.Shutdown(context.Background())

	mu.Lock()
	defer mu.Unlock()
	for _, id := range jobIDs {
		job, err := store.GetJob(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		got := hookEvents[id]
		if len(got) != 1 {
			t.Fatalf("job %s (final status %s): terminal hooks = %v, want exactly one", id, job.Status, got)
		}
		want := hookEventForStatus(job.Status)
		if got[0] != want {
			t.Fatalf("job %s: hook = %q but final status %s maps to %q", id, got[0], job.Status, want)
		}
	}
}

func TestRetryRequeuesFailedJobAndResetsOnlyUnfinishedItems(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	job := domain.Job{ID: "job-failed", Input: "https://music.apple.com/cn/album/example/1", Type: "album", Storefront: "cn", CanonicalKey: "album:cn:1", Status: domain.JobFailed, TotalItems: 2, DoneItems: 1, FailedItems: 1, Error: "boom", CreatedAt: now, UpdatedAt: now}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	completed := domain.JobItem{ID: "item-done", JobID: job.ID, AdamID: "song-1", Kind: "song", Index: 1, Status: domain.ItemCompleted, Progress: 1, Codec: "alac", CreatedAt: now, UpdatedAt: now}
	failed := domain.JobItem{ID: "item-failed", JobID: job.ID, AdamID: "song-2", Kind: "song", Index: 2, Status: domain.ItemFailed, Progress: 0.4, Codec: "alac", RetryKind: "download", Attempt: 3, MaxAttempts: 3, StatusMessage: "ALAC failed", Error: "boom", CreatedAt: now, UpdatedAt: now}
	for _, item := range []domain.JobItem{completed, failed} {
		if err := store.CreateItem(ctx, item); err != nil {
			t.Fatal(err)
		}
	}

	manager := NewManager(store, events.NewHub(), recoveryProcessor{}, 1, slog.Default())
	if err := manager.Retry(ctx, job.ID); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.JobQueued || got.Error != "" {
		t.Fatalf("job after retry = status %s error %q, want queued with empty error", got.Status, got.Error)
	}
	items, err := store.ListItems(ctx, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("items = %d, want 2", len(items))
	}
	if items[0].Status != domain.ItemCompleted || items[0].Progress != 1 || items[0].Codec != "alac" {
		t.Fatalf("completed item was touched by retry: %+v", items[0])
	}
	reset := items[1]
	if reset.Status != domain.ItemQueued || reset.Progress != 0 || reset.Codec != "" || reset.Error != "" ||
		reset.RetryKind != "" || reset.Attempt != 0 || reset.MaxAttempts != 0 || reset.StatusMessage != "" {
		t.Fatalf("failed item was not reset to queued: %+v", reset)
	}
	if len(manager.queue) != 1 || <-manager.queue != job.ID {
		t.Fatal("retry did not enqueue the job")
	}
	events, err := store.ListEventsAfter(ctx, job.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Type != "job_retried" {
		t.Fatalf("events = %+v, want one job_retried", events)
	}
}

func TestRetryRefusesNonFailedMissingAndDuplicateJobs(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	manager := NewManager(store, events.NewHub(), recoveryProcessor{}, 1, slog.Default())

	for i, status := range []domain.JobStatus{domain.JobQueued, domain.JobRunning, domain.JobCompleted, domain.JobCancelled} {
		job := domain.Job{ID: "job-" + string(status), Input: "in", Type: "song", CanonicalKey: "song:cn:" + strconv.Itoa(i), Status: status, CreatedAt: now, UpdatedAt: now}
		if err := store.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
		if err := manager.Retry(ctx, job.ID); !errors.Is(err, ErrJobNotRetryable) {
			t.Fatalf("Retry(%s job) = %v, want ErrJobNotRetryable", status, err)
		}
	}

	if err := manager.Retry(ctx, "no-such-job"); !errors.Is(err, db.ErrJobNotFound) {
		t.Fatalf("Retry(missing job) = %v, want ErrJobNotFound", err)
	}

	// The same canonical key already has an active job (resubmitted by the
	// user): retrying the old failed job must be refused, not trip the
	// partial unique index.
	failed := domain.Job{ID: "job-old-failed", Input: "in", Type: "song", CanonicalKey: "song:cn:dup", Status: domain.JobFailed, CreatedAt: now, UpdatedAt: now}
	active := domain.Job{ID: "job-new-active", Input: "in", Type: "song", CanonicalKey: "song:cn:dup", Status: domain.JobQueued, CreatedAt: now, UpdatedAt: now}
	for _, job := range []domain.Job{failed, active} {
		if err := store.CreateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if err := manager.Retry(ctx, failed.ID); !errors.Is(err, db.ErrDuplicateActive) {
		t.Fatalf("Retry(failed job with active duplicate) = %v, want ErrDuplicateActive", err)
	}
	if len(manager.queue) != 0 {
		t.Fatalf("queue length = %d, want 0 after refused retries", len(manager.queue))
	}
}

// TestRetryRefusesWhilePreviousRunIsFinalizing covers the window where the
// job row already reads failed but the old worker's deferred cleanup has not
// removed its cancels entry yet (or Cancel's queued path is still dispatching
// hooks under a finalizing mark). Retrying inside that window would let the
// old worker's deferred delete remove the new run's cancel registration.
func TestRetryRefusesWhilePreviousRunIsFinalizing(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	job := domain.Job{ID: "job-finalizing", Input: "in", Type: "song", CanonicalKey: "song:cn:fin", Status: domain.JobFailed, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(store, events.NewHub(), recoveryProcessor{}, 1, slog.Default())

	manager.mu.Lock()
	manager.cancels[job.ID] = func() {}
	manager.mu.Unlock()
	if err := manager.Retry(ctx, job.ID); !errors.Is(err, ErrJobFinalizing) {
		t.Fatalf("Retry(with lingering cancels entry) = %v, want ErrJobFinalizing", err)
	}

	manager.mu.Lock()
	delete(manager.cancels, job.ID)
	manager.finalizing[job.ID] = true
	manager.mu.Unlock()
	if err := manager.Retry(ctx, job.ID); !errors.Is(err, ErrJobFinalizing) {
		t.Fatalf("Retry(while finalizing) = %v, want ErrJobFinalizing", err)
	}

	manager.mu.Lock()
	delete(manager.finalizing, job.ID)
	manager.mu.Unlock()
	if err := manager.Retry(ctx, job.ID); err != nil {
		t.Fatalf("Retry(after finalize finished) = %v, want success", err)
	}
	if len(manager.queue) != 1 || <-manager.queue != job.ID {
		t.Fatal("retry after finalize did not enqueue the job")
	}
}

// priorityCaptureProcessor reports the limits priority the manager stamped on
// each job's processing context.
type priorityCaptureProcessor struct {
	keyedProcessor
	priorities chan int64
}

func (p *priorityCaptureProcessor) ProcessJob(ctx context.Context, _ domain.Job, _ Reporter) error {
	priority, ok := limits.PriorityFromContext(ctx)
	if !ok {
		priority = -1
	}
	p.priorities <- priority
	return nil
}

func TestRunStampsPoolPriorityFromJobCreationTime(t *testing.T) {
	store, err := db.Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	processor := &priorityCaptureProcessor{priorities: make(chan int64, 1)}
	manager := NewManager(store, events.NewHub(), processor, 1, slog.Default())
	manager.Start(context.Background())
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = manager.Shutdown(shutdownCtx)
	}()

	resp := manager.SubmitBatch(context.Background(), []string{"song|cn|priority"}, false, nil)
	if resp.Accepted != 1 {
		t.Fatalf("submit = %+v", resp)
	}
	job, err := store.GetJob(context.Background(), resp.Results[0].Job.ID)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-processor.priorities:
		if want := job.CreatedAt.UnixNano(); got != want {
			t.Fatalf("pool priority = %d, want job CreatedAt %d", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job was not processed")
	}
}
