package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/jobs"
)

// urlSplitPattern splits a pasted textarea blob of URLs on newlines,
// whitespace, commas, and semicolons (ASCII and full-width variants).
var urlSplitPattern = regexp.MustCompile(`[\r\n\s,;，；]+`)

// maxBatchSubmitURLs caps the number of URLs accepted in a single batch
// submit request.
const maxBatchSubmitURLs = 100

func (s *Server) createDownload(w http.ResponseWriter, r *http.Request) {
	var req domain.DownloadRequest
	// Unknown fields are rejected rather than ignored: a typo inside
	// overrides would otherwise submit successfully and run the whole batch
	// without the intended settings.
	if !decodeJSONBody(w, r, &req, true) {
		return
	}
	var urls []string
	for _, raw := range req.URLs {
		for _, entry := range urlSplitPattern.Split(raw, -1) {
			entry = strings.TrimSpace(entry)
			if entry != "" {
				urls = append(urls, entry)
			}
		}
	}
	if len(urls) == 0 {
		writeError(w, http.StatusBadRequest, fmt.Errorf("urls is required"))
		return
	}
	if len(urls) > maxBatchSubmitURLs {
		writeError(w, http.StatusBadRequest, fmt.Errorf("too many urls: max %d per request", maxBatchSubmitURLs))
		return
	}
	// Normalize the canonical token override once here so downstream code sees
	// one three-state value: absent inherits the runtime token, present-empty
	// clears it for this job, and a non-empty value is trimmed before storage.
	if req.Overrides != nil && req.Overrides.MediaUserToken != nil {
		token := strings.TrimSpace(*req.Overrides.MediaUserToken)
		req.Overrides.MediaUserToken = &token
	}
	if req.Overrides != nil {
		// Validate the overlay against the same rules the runtime config must
		// satisfy, applied to the config these jobs would actually run under.
		// Strict: fresh input is rejected, not clamped — only overrides already
		// persisted before the limits existed are clamped when jobs run.
		if _, err := req.Overrides.ApplyValidatedStrict(s.currentConfig()); err != nil {
			writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("invalid overrides: %w", err))
			return
		}
		if err := s.manager.ValidateHookSelection(req.Overrides.Hooks); err != nil {
			writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("invalid overrides: %w", err))
			return
		}
	}
	resp := s.manager.SubmitBatch(r.Context(), urls, req.Overrides)
	status := http.StatusUnprocessableEntity
	if resp.Accepted > 0 {
		status = http.StatusAccepted
	}
	writeJSON(w, status, resp)
}

func (s *Server) listDownloads(w http.ResponseWriter, r *http.Request) {
	filter, err := parseJobListFilter(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Read the global cursor before the job snapshot, for the same reason
	// getDownload reads its per-job cursor first: an event committing between
	// this read and the snapshot is already reflected in the snapshot, so the
	// cursor a client resumes the overview feed from never runs ahead of what
	// this response shows.
	lastEventID, err := s.store.LatestGlobalEventID(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// ListJobs already derives done_items/failed_items from live job_items,
	// matching getDownload and the overview feed's pushed snapshots.
	jobs, total, err := s.store.ListJobs(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	counts, err := s.store.CountJobsByStatus(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"downloads":     jobs,
		"status_counts": makeJobStatusCounts(counts, total),
		"total":         total, "limit": filter.Limit, "offset": filter.Offset, "last_event_id": lastEventID,
	})
}

func makeJobStatusCounts(counts map[domain.JobStatus]int, total int) domain.JobStatusCounts {
	return domain.JobStatusCounts{
		Queued:    counts[domain.JobQueued],
		Running:   counts[domain.JobRunning],
		Completed: counts[domain.JobCompleted],
		Failed:    counts[domain.JobFailed],
		Cancelled: counts[domain.JobCancelled],
		Total:     total,
	}
}

var (
	allowedListStatuses = map[domain.JobStatus]struct{}{
		domain.JobQueued: {}, domain.JobRunning: {}, domain.JobCompleted: {}, domain.JobFailed: {}, domain.JobCancelled: {},
	}
	allowedListTypes = map[string]struct{}{
		"song": {}, "album": {}, "playlist": {}, "artist": {}, "station": {},
	}
)

func parseJobListFilter(r *http.Request) (db.JobListFilter, error) {
	q := r.URL.Query()
	filter := db.JobListFilter{
		Storefront: strings.TrimSpace(q.Get("storefront")),
		Query:      strings.TrimSpace(q.Get("q")),
		Sort:       strings.TrimSpace(q.Get("sort")),
		Order:      strings.TrimSpace(strings.ToLower(q.Get("order"))),
	}

	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 200 {
			return filter, fmt.Errorf("limit must be an integer between 1 and 200")
		}
		filter.Limit = limit
	}
	if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return filter, fmt.Errorf("offset must be a non-negative integer")
		}
		filter.Offset = offset
	}

	for _, st := range parseCSVQuery(q, "status") {
		js := domain.JobStatus(st)
		if _, ok := allowedListStatuses[js]; !ok {
			return filter, fmt.Errorf("status %q is not supported; allowed: queued, running, completed, failed, cancelled", st)
		}
		filter.Statuses = append(filter.Statuses, js)
	}

	for _, t := range parseCSVQuery(q, "type") {
		if _, ok := allowedListTypes[t]; !ok {
			return filter, fmt.Errorf("type %q is not supported; allowed: song, album, playlist, artist, station", t)
		}
		filter.Types = append(filter.Types, t)
	}

	if filter.Sort != "" && filter.Sort != db.JobListSortCreatedAt && filter.Sort != db.JobListSortUpdatedAt {
		return filter, fmt.Errorf("sort must be created_at or updated_at")
	}
	if filter.Order != "" && filter.Order != db.JobListOrderAsc && filter.Order != db.JobListOrderDesc {
		return filter, fmt.Errorf("order must be asc or desc")
	}

	var err error
	if filter.CreatedAfter, err = parseOptionalTime(q.Get("created_after"), false); err != nil {
		return filter, fmt.Errorf("created_after: %w", err)
	}
	if filter.CreatedBefore, err = parseOptionalTime(q.Get("created_before"), true); err != nil {
		return filter, fmt.Errorf("created_before: %w", err)
	}
	if filter.UpdatedAfter, err = parseOptionalTime(q.Get("updated_after"), false); err != nil {
		return filter, fmt.Errorf("updated_after: %w", err)
	}
	if filter.UpdatedBefore, err = parseOptionalTime(q.Get("updated_before"), true); err != nil {
		return filter, fmt.Errorf("updated_before: %w", err)
	}
	if filter.CreatedAfter != nil && filter.CreatedBefore != nil && filter.CreatedAfter.After(*filter.CreatedBefore) {
		return filter, fmt.Errorf("created_after must be <= created_before")
	}
	if filter.UpdatedAfter != nil && filter.UpdatedBefore != nil && filter.UpdatedAfter.After(*filter.UpdatedBefore) {
		return filter, fmt.Errorf("updated_after must be <= updated_before")
	}
	filter.Normalize()
	return filter, nil
}

// parseCSVQuery collects values for key from both repeated query params and
// comma-separated entries (status=a&status=b and status=a,b are equivalent).
func parseCSVQuery(q url.Values, key string) []string {
	raw := q[key]
	if len(raw) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0)
	for _, part := range raw {
		for _, item := range strings.Split(part, ",") {
			item = strings.TrimSpace(item)
			if item == "" {
				continue
			}
			if _, ok := seen[item]; ok {
				continue
			}
			seen[item] = struct{}{}
			out = append(out, item)
		}
	}
	return out
}

func parseOptionalTime(raw string, endOfDayIfDateOnly bool) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if t, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		utc := t.UTC()
		return &utc, nil
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		utc := t.UTC()
		return &utc, nil
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		utc := t.UTC()
		if endOfDayIfDateOnly {
			utc = utc.Add(24*time.Hour - time.Nanosecond)
		}
		return &utc, nil
	}
	return nil, fmt.Errorf("must be RFC3339 or YYYY-MM-DD")
}

// jobSnapshot returns a job with progress counters derived from its live
// items, the same shape the GET /downloads list and getDownload return. A
// sql.ErrNoRows error means the job no longer exists (e.g. deleted between a
// milestone event and this read); the overview feed treats any other error as
// transient and retries.
func (s *Server) jobSnapshot(ctx context.Context, id string) (*domain.Job, error) {
	job, err := s.store.GetJob(ctx, id)
	if err != nil {
		return nil, err
	}
	done, failed, err := s.store.CountItemProgress(ctx, id)
	if err != nil {
		return nil, err
	}
	job.DoneItems, job.FailedItems = done, failed
	return &job, nil
}

func (s *Server) cancelDownload(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.Cancel(r.Context(), r.PathValue("id")); err != nil {
		// A missing job is a 404 here as it is on retry and delete. It used to
		// be funnelled into 500 along with everything else, which left a client
		// unable to tell "this job is gone, drop it from the list" from "the
		// server is broken, back off and retry".
		if errors.Is(err, db.ErrJobNotFound) {
			writeError(w, http.StatusNotFound, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// retryDownload re-queues a failed job. Only tracks that did not finish are
// downloaded again: completed/skipped items are preserved as-is, failed ones
// are reset to queued and re-processed.
func (s *Server) retryDownload(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.Retry(r.Context(), r.PathValue("id")); err != nil {
		switch {
		case errors.Is(err, db.ErrJobNotFound):
			writeError(w, http.StatusNotFound, err)
		case errors.Is(err, jobs.ErrJobNotRetryable), errors.Is(err, jobs.ErrJobFinalizing), errors.Is(err, db.ErrDuplicateActive):
			writeError(w, http.StatusConflict, err)
		case errors.Is(err, jobs.ErrQueueFull):
			writeError(w, http.StatusServiceUnavailable, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

func (s *Server) getDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Read the event cursor before the job/items snapshot below. If an event
	// commits concurrently between this read and the snapshot reads, its
	// effect is guaranteed to already be visible in the snapshot — so the
	// cursor never races ahead of what the response actually reflects.
	// Reading it after the snapshot instead would let a concurrent event land
	// in between: the cursor would include it, but the snapshot wouldn't,
	// and events/ws?last_event_id= filters strictly by id, so a client would
	// skip that event and never see its effect until a later one arrives.
	lastEventID, err := s.store.LatestEventID(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	items, err := s.store.ListItems(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Derive the progress counters from the live item list rather than trusting
	// the job row's stored counters, which are only refreshed when the job
	// reaches a terminal status. Without this a running job reports done_items=0
	// while the items array already shows completed items in the same response.
	job.DoneItems, job.FailedItems = domain.CountItemProgress(items)
	// The snapshot and the SSE/WS stream are two access modes of one state, so
	// hook outcomes — which the stream pushes as hook_* events — must be
	// visible here too, derived from those same persisted events.
	hookEvents, err := s.store.ListHookEvents(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	hooks := domain.SummarizeHooks(hookEvents, s.manager.HooksPending(id))
	writeJSON(w, http.StatusOK, map[string]any{
		"job": job, "items": items, "hooks": hooks, "last_event_id": lastEventID,
	})
}

func (s *Server) deleteDownload(w http.ResponseWriter, r *http.Request) {
	// Deletion goes through the manager, not the store, so a job whose
	// finalize sequence (terminal event + hook dispatch) is still in flight
	// is refused even though its status row already reads terminal.
	if err := s.manager.Delete(r.Context(), r.PathValue("id")); err != nil {
		switch {
		case errors.Is(err, db.ErrJobNotFound):
			writeError(w, http.StatusNotFound, err)
		case errors.Is(err, db.ErrJobNotTerminal):
			writeError(w, http.StatusConflict, err)
		default:
			writeError(w, http.StatusInternalServerError, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
