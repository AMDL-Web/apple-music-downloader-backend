package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
	"amdl/internal/jobs"
	"amdl/internal/logging"
	"amdl/internal/media"
	"amdl/internal/wrapper"

	"github.com/coder/websocket"
)

// maxBatchSubmitURLs caps the number of URLs accepted in a single batch
// submit request.
const maxBatchSubmitURLs = 100

// maxJSONBodyBytes bounds every JSON request body handled by this API. The
// largest legitimate payload is a runtime-config patch or a batch of 100
// URLs; 1 MiB leaves ample room for both while preventing unbounded reads.
const maxJSONBodyBytes int64 = 1 << 20

// urlSplitPattern splits a pasted textarea blob of URLs on newlines,
// whitespace, commas, and semicolons (ASCII and full-width variants).
var urlSplitPattern = regexp.MustCompile(`[\r\n\s,;，；]+`)

type Server struct {
	// cfg is the live runtime config store shared with the download pipeline;
	// GET/PUT /api/v1/config read and replace its snapshot.
	cfg       *config.Store
	store     *db.Store
	hub       *events.Hub
	manager   *jobs.Manager
	wrapper   wrapperService
	quality   qualityService
	devToken  developerTokenService
	logger    *slog.Logger
	logStore  *logging.Store
	logSystem *logging.System
}

type wrapperService interface {
	Status(context.Context) (wrapper.Status, error)
	StartLogin(context.Context, string, string) (wrapper.LoginResult, error)
	SubmitTwoStepCode(context.Context, string, string) (wrapper.LoginResult, error)
	Logout(context.Context, string) error
}

type qualityService interface {
	QueryQuality(context.Context, media.QualityRequest) (media.QualityResult, error)
}

type developerTokenService interface {
	MintDeveloperToken() (string, error)
}

func NewServer(cfg *config.Store, store *db.Store, hub *events.Hub, manager *jobs.Manager, wrapperClient wrapperService, qualityClient qualityService, devToken developerTokenService, logger *slog.Logger, logSystem ...*logging.System) *Server {
	s := &Server{cfg: cfg, store: store, hub: hub, manager: manager, wrapper: wrapperClient, quality: qualityClient, devToken: devToken, logger: logger}
	if len(logSystem) > 0 && logSystem[0] != nil {
		s.logSystem = logSystem[0]
		s.logStore = logSystem[0].Store
	}
	return s
}

// currentConfig returns the live runtime config snapshot; nil-safe for test
// Servers constructed without a config store.
func (s *Server) currentConfig() config.Config {
	if s.cfg == nil {
		return config.Config{}
	}
	return s.cfg.Get()
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /docs", swaggerUI)
	mux.HandleFunc("GET /api/openapi.yaml", openAPI)
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/logs", s.listLogs)
	mux.HandleFunc("GET /api/v1/logs/stream", s.streamLogs)
	mux.HandleFunc("GET /api/v1/config", s.getConfig)
	mux.HandleFunc("PUT /api/v1/config", s.updateConfig)
	mux.HandleFunc("GET /api/v1/developer-token", s.developerToken)
	mux.HandleFunc("GET /api/v1/wrapper/status", s.wrapperStatus)
	mux.HandleFunc("POST /api/v1/wrapper/login", s.wrapperLogin)
	mux.HandleFunc("POST /api/v1/wrapper/login/{login_id}/2fa", s.wrapperTwoStep)
	mux.HandleFunc("POST /api/v1/wrapper/logout", s.wrapperLogout)
	mux.HandleFunc("POST /api/v1/quality", s.queryQuality)
	mux.HandleFunc("POST /api/v1/downloads", s.createDownload)
	mux.HandleFunc("GET /api/v1/downloads", s.listDownloads)
	// The literal "events" segment is more specific than "{id}", so these two
	// overview-feed routes take precedence over GET /api/v1/downloads/{id} for
	// that exact path (a real job id is never "events").
	mux.HandleFunc("GET /api/v1/downloads/events", s.downloadsFeed)
	mux.HandleFunc("GET /api/v1/downloads/events/ws", s.downloadsFeedWS)
	mux.HandleFunc("GET /api/v1/downloads/{id}", s.getDownload)
	mux.HandleFunc("DELETE /api/v1/downloads/{id}", s.deleteDownload)
	mux.HandleFunc("POST /api/v1/downloads/{id}/cancel", s.cancelDownload)
	mux.HandleFunc("POST /api/v1/downloads/{id}/retry", s.retryDownload)
	mux.HandleFunc("GET /api/v1/downloads/{id}/events", s.events)
	mux.HandleFunc("GET /api/v1/downloads/{id}/events/ws", s.eventsWS)
	return s.observeHTTP(cors(mux))
}

func (s *Server) queryQuality(w http.ResponseWriter, r *http.Request) {
	var req media.QualityRequest
	if !decodeJSONBody(w, r, &req, false) {
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("url is required"))
		return
	}
	if s.quality == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("quality service is not configured"))
		return
	}
	result, err := s.quality.QueryQuality(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) wrapperStatus(w http.ResponseWriter, r *http.Request) {
	status, err := s.wrapper.Status(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *Server) wrapperLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeJSONBody(w, r, &req, false) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.Password = strings.TrimSpace(req.Password)
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("username and password are required"))
		return
	}
	result, err := s.wrapper.StartLogin(r.Context(), req.Username, req.Password)
	if err != nil {
		writeWrapperError(w, err)
		return
	}
	status := http.StatusOK
	if result.Status == wrapper.LoginStatusNeedsTwoStep {
		status = http.StatusAccepted
	}
	writeJSON(w, status, result)
}

func (s *Server) wrapperTwoStep(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Code string `json:"two_step_code"`
	}
	if !decodeJSONBody(w, r, &req, false) {
		return
	}
	req.Code = strings.TrimSpace(req.Code)
	if req.Code == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("two_step_code is required"))
		return
	}
	result, err := s.wrapper.SubmitTwoStepCode(r.Context(), r.PathValue("login_id"), req.Code)
	if err != nil {
		writeWrapperError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) wrapperLogout(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
	}
	if !decodeJSONBody(w, r, &req, false) {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("username is required"))
		return
	}
	if err := s.wrapper.Logout(r.Context(), req.Username); err != nil {
		writeWrapperError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out", "username": req.Username})
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-Request-ID")
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if s.store != nil {
		if err := s.store.Ping(r.Context()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status":         "degraded",
				"database_error": err.Error(),
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// getConfig returns the runtime-changeable part of the current config
// (download minus max_running_jobs, logging level/access log, simulate,
// catalog.album_track_url_mode/media_user_token/media_user_token_priority/signed_mode_hls_source).
// Startup-bound fields are omitted: clients cannot change them through this
// API, so they have no reason to see them here.
//
// The backing file is re-read first, so manual edits made while the backend
// is running take effect on the next GET instead of requiring a restart. If
// the file is currently unreadable or invalid (e.g. an edit in progress),
// the last good snapshot is served and reload_error reports why.
func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{"persisted": false}
	if s.cfg != nil {
		resp["persisted"] = s.cfg.Persistent()
		if err := s.cfg.Reload(); err != nil {
			resp["reload_error"] = err.Error()
		}
	}
	s.applyLoggingConfig()
	resp["config"] = config.MutableView(s.currentConfig())
	writeJSON(w, http.StatusOK, resp)
}

// updateConfig merges the request body onto the current runtime config:
// omitted fields keep their current values, present fields (including whole
// sections) are replaced. The merged result must pass full config validation,
// and fields consumed only at startup (server, database, logging outputs,
// wrapper, tools, catalog client/signing, download.max_running_jobs) are
// rejected — changing them at runtime would silently do nothing. Accepted
// changes apply to new requests and newly started jobs immediately and are
// written back to the live config file, so they survive restarts.
func (s *Server) updateConfig(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("runtime config store is not configured"))
		return
	}
	var body json.RawMessage
	if !decodeJSONBody(w, r, &body, false) {
		return
	}
	// Decode, merge, and validate inside the store's atomic update, so two
	// concurrent PUTs can never merge onto the same stale snapshot and
	// silently drop each other's changes. rejectStatus/rejectErr carry the
	// request-level failure out of the closure; any other error is a
	// persistence failure.
	var rejectStatus int
	var rejectErr error
	updated, err := s.cfg.UpdateAndSave(func(current config.Config) (config.Config, error) {
		merged := current
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&merged); err != nil {
			rejectStatus, rejectErr = http.StatusBadRequest, err
			return config.Config{}, err
		}
		if locked := config.RuntimeLockedChanges(current, merged); len(locked) > 0 {
			rejectStatus, rejectErr = http.StatusUnprocessableEntity, fmt.Errorf("fields can only be changed in the config file and require a restart: %s", strings.Join(locked, ", "))
			return config.Config{}, rejectErr
		}
		// A field pinned by an AMDL_* environment override would accept the
		// write but revert to the environment value on the next reload, so
		// reject it up front instead of pretending the change stuck.
		if locked := config.EnvLockedChanges(current, merged, os.LookupEnv); len(locked) > 0 {
			rejectStatus, rejectErr = http.StatusUnprocessableEntity, fmt.Errorf("fields are pinned by environment variables; unset the variable and restart to change them: %s", strings.Join(locked, ", "))
			return config.Config{}, rejectErr
		}
		if err := merged.Validate(); err != nil {
			rejectStatus, rejectErr = http.StatusUnprocessableEntity, err
			return config.Config{}, err
		}
		return merged, nil
	})
	if err != nil {
		if rejectErr != nil {
			writeError(w, rejectStatus, rejectErr)
			return
		}
		writeError(w, http.StatusInternalServerError, fmt.Errorf("persist config: %w", err))
		return
	}
	// Another concurrent update may have committed after this one returned;
	// apply the store's final snapshot so an older request cannot restore its
	// logging level after a newer write.
	s.applyLoggingConfig()
	writeJSON(w, http.StatusOK, map[string]any{"config": config.MutableView(updated), "persisted": s.cfg.Persistent()})
}

func (s *Server) applyLoggingConfig() {
	if s.logSystem == nil {
		return
	}
	for {
		level := s.currentConfig().Logging.Level
		_ = s.logSystem.SetLevel(level)
		if s.currentConfig().Logging.Level == level {
			return
		}
	}
}

// developerToken hands out a freshly signed Apple Music developer token. Only
// local signing mode can serve it: the legacy web-discovered token is
// origin-locked to music.apple.com and would be rejected anywhere else.
func (s *Server) developerToken(w http.ResponseWriter, r *http.Request) {
	if s.devToken == nil || !s.currentConfig().Catalog.DeveloperTokenSigningEnabled() {
		writeError(w, http.StatusConflict, fmt.Errorf("developer token endpoint requires local signing mode (catalog.apple_music_* keys); the web-discovered token is origin-restricted and cannot be shared"))
		return
	}
	token, err := s.devToken.MintDeveloperToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

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
	if req.Overrides != nil {
		// Validate the overlay against the same rules the runtime config must
		// satisfy, applied to the config these jobs would actually run under.
		if err := req.Overrides.Apply(s.currentConfig()).Validate(); err != nil {
			writeError(w, http.StatusUnprocessableEntity, fmt.Errorf("invalid overrides: %w", err))
			return
		}
	}
	mediaUserToken := s.currentConfig().Catalog.EffectiveMediaUserToken(req.MediaUserToken)
	resp := s.manager.SubmitBatch(r.Context(), urls, req.Force, req.Overrides, mediaUserToken)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"downloads": jobs, "total": total, "limit": filter.Limit, "offset": filter.Offset, "last_event_id": lastEventID,
	})
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
// items, the same shape the GET /downloads list and getDownload return. ok is
// false if the job no longer exists (e.g. deleted between a milestone event
// and this read), so the overview feed simply skips pushing it.
func (s *Server) jobSnapshot(ctx context.Context, id string) (*domain.Job, bool) {
	job, err := s.store.GetJob(ctx, id)
	if err != nil {
		return nil, false
	}
	items, err := s.store.ListItems(ctx, id)
	if err != nil {
		return nil, false
	}
	job.DoneItems, job.FailedItems = domain.CountItemProgress(items)
	return &job, true
}

func (s *Server) cancelDownload(w http.ResponseWriter, r *http.Request) {
	if err := s.manager.Cancel(r.Context(), r.PathValue("id")); err != nil {
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

// eventsExhausted reloads jobID and reports whether its event stream will
// never deliver another event. Reloading is essential for connections opened
// while the job was active: retaining their initial Job snapshot would keep
// them open forever after the job transitions to a terminal state. A terminal
// job.Status alone is not enough: post-download
// hook dispatch is fire-and-forget and can keep recording hook_started/
// hook_succeeded/hook_failed events well after the job itself reached a
// terminal status (see jobs.Manager.HooksPending). And HooksPending alone
// still has a gap: the terminal status becomes visible (Store.FinalizeJob)
// before hook dispatch increments the pending count, so FinalizeInFlight
// must be consulted too — and strictly before HooksPending. Dispatch
// increments pending before the manager drops its finalize marks, so
// observing "not finalizing" guarantees pending already reflects any hooks
// that will fire; the reverse order could read pending before the increment
// and the finalize mark after its removal, wrongly concluding exhausted
// while hook events are still coming.
func (s *Server) eventsExhausted(ctx context.Context, jobID string) bool {
	job, err := s.store.GetJob(ctx, jobID)
	if errors.Is(err, sql.ErrNoRows) {
		// A successful DELETE publishes a final job_deleted tombstone after
		// removing the row. Once the backlog drain has consumed that event there
		// can be no future per-job events, so an already-open stream must close.
		return true
	}
	if err != nil || !job.Status.IsTerminal() {
		return false
	}
	return !s.manager.FinalizeInFlight(jobID) && !s.manager.HooksPending(jobID)
}

// eventsWS is the WebSocket twin of events: it streams the same persisted job
// events — one domain.Event encoded as a JSON text message, identical to the
// SSE data payload — over a WebSocket connection. Resume works via the
// ?last_event_id= query parameter (WebSocket has no Last-Event-ID header
// convention); clients pass the id of the last event they saw. Delivery uses
// the same store-drain pattern as events: the hub only wakes the drain sooner,
// and the ticker bounds tail latency for events dropped by a full hub buffer.
// The ticker also pings the peer so half-open mobile connections are detected
// and torn down instead of holding the goroutine forever.
func (s *Server) eventsWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetJob(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	lastID, _ := strconv.ParseInt(r.URL.Query().Get("last_event_id"), 10, 64)

	// Subscribe before inspecting state or reading the backlog so a concurrent
	// retry cannot change a failed job back to queued in the gap and leave us
	// waiting on a nil channel. The store remains the source of truth; the hub
	// only wakes a subsequent drain sooner.
	ch, cancel := s.hub.Subscribe(id)
	defer cancel()
	exhausted := s.eventsExhausted(r.Context(), id)

	pending, err := s.store.ListEventsAfter(r.Context(), id, lastID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(pending) == 0 && exhausted {
		// Nothing left to replay and the job will never emit another event:
		// refuse the subscription instead of holding a socket open forever.
		terminalJob, _ := s.store.GetJob(r.Context(), id)
		writeError(w, http.StatusConflict, fmt.Errorf("job %s is already %s: no further events will be emitted", id, terminalJob.Status))
		return
	}

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return // Accept already wrote the handshake failure response
	}
	defer conn.CloseNow()

	// CloseRead answers control frames (ping/pong/close) in the background and
	// cancels the returned context when the client goes away. The protocol is
	// server-push only, so a client data frame also terminates the connection.
	ctx := conn.CloseRead(r.Context())

	drain := func() error {
		events, err := s.store.ListEventsAfter(ctx, id, lastID)
		if err != nil {
			// Keep the connection on a store read error; the next hub wake or
			// tick retries, mirroring the SSE drain.
			return nil
		}
		for _, ev := range events {
			raw, _ := json.Marshal(ev)
			if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
				return err
			}
			lastID = ev.ID
		}
		return nil
	}

	for _, ev := range pending {
		raw, _ := json.Marshal(ev)
		if err := conn.Write(ctx, websocket.MessageText, raw); err != nil {
			return
		}
		lastID = ev.ID
	}
	if s.eventsExhausted(ctx, id) {
		// Backlog has been delivered in full and nothing more will ever
		// arrive (job event or hook event), so close instead of idling
		// forever. A hook may have recorded its terminal event after the
		// backlog read but before the HooksPending check just above; that
		// event is committed before Pending drops (see
		// hooks.Dispatcher.donePending), so one final drain is guaranteed to
		// deliver it before the connection closes.
		_ = drain()
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			if err := drain(); err != nil {
				return
			}
			if s.eventsExhausted(ctx, id) {
				// A pending hook (the reason this loop was entered instead of
				// closing right after the initial backlog) has now recorded
				// its terminal event: nothing more will ever arrive. That
				// event may have been committed after drain's read, so drain
				// once more before closing.
				_ = drain()
				return
			}
		case <-ticker.C:
			if err := drain(); err != nil {
				return
			}
			if s.eventsExhausted(ctx, id) {
				_ = drain()
				return
			}
			pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
			err := conn.Ping(pingCtx)
			pingCancel()
			if err != nil {
				return
			}
		}
	}
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

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, err := s.store.GetJob(r.Context(), id); err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)

	// Subscribe before inspecting state or reading the backlog so a concurrent
	// retry cannot change a failed job back to queued in the gap and leave us
	// waiting on a nil channel. The store remains the source of truth; the hub
	// only wakes a subsequent drain sooner.
	ch, cancel := s.hub.Subscribe(id)
	defer cancel()
	exhausted := s.eventsExhausted(r.Context(), id)

	pending, err := s.store.ListEventsAfter(r.Context(), id, lastID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(pending) == 0 && exhausted {
		// Nothing left to replay and the job will never emit another event:
		// refuse the subscription instead of holding a connection open forever.
		terminalJob, _ := s.store.GetJob(r.Context(), id)
		writeError(w, http.StatusConflict, fmt.Errorf("job %s is already %s: no further events will be emitted", id, terminalJob.Status))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}

	// drain streams every persisted event newer than lastID directly from the
	// store, so delivery never depends on the in-memory hub channel. The hub
	// only wakes us to drain sooner; any event dropped by a full channel buffer
	// is still picked up on the next drain, and the ticker bounds the tail
	// latency for a dropped final event that has no successor to wake us.
	drain := func() {
		events, err := s.store.ListEventsAfter(r.Context(), id, lastID)
		if err != nil {
			return
		}
		for _, ev := range events {
			writeSSE(w, ev)
			lastID = ev.ID
		}
		flusher.Flush()
	}

	for _, ev := range pending {
		writeSSE(w, ev)
		lastID = ev.ID
	}
	flusher.Flush()
	if s.eventsExhausted(r.Context(), id) {
		// Backlog has been delivered in full and nothing more will ever
		// arrive (job event or hook event), so close instead of idling
		// forever. A hook may have recorded its terminal event after the
		// backlog read but before the HooksPending check just above; that
		// event is committed before Pending drops (see
		// hooks.Dispatcher.donePending), so one final drain is guaranteed to
		// deliver it before the stream closes.
		drain()
		return
	}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			drain()
			if s.eventsExhausted(r.Context(), id) {
				// A pending hook (the reason this loop was entered instead of
				// closing right after the initial backlog) has now recorded
				// its terminal event: nothing more will ever arrive. That
				// event may have been committed after drain's read, so drain
				// once more before closing.
				drain()
				return
			}
		case <-ticker.C:
			drain()
			if s.eventsExhausted(r.Context(), id) {
				drain()
				return
			}
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, ev domain.Event) {
	raw, _ := json.Marshal(ev)
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, raw)
}

// downloadsFeed is the SSE overview feed: it pushes one DownloadFeedMessage
// per change to the GET /downloads list — download_upserted (the affected
// job's latest snapshot) as jobs are queued/started/progress/finish, and
// download_deleted as jobs are removed. Resume via the Last-Event-ID header;
// the client should first GET /downloads for the full list and use that
// response's last_event_id here.
func (s *Server) downloadsFeed(w http.ResponseWriter, r *http.Request) {
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}

	// Subscribe before the first drain so a milestone published between the
	// backlog read and registration still wakes a subsequent drain.
	ch, cancel := s.hub.SubscribeAll()
	defer cancel()

	// Flush the response head right away so the client's EventSource opens even
	// when there's no backlog to send (mirrors the single-job events handler);
	// otherwise the 200 would wait for the first change or the 10s keepalive,
	// and intermediary proxies could time the idle connection out first.
	flusher.Flush()

	write := func(msg domain.DownloadFeedMessage) error {
		raw, _ := json.Marshal(msg)
		fmt.Fprintf(w, "id: %d\n", msg.EventID)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", msg.Type, raw)
		flusher.Flush()
		return nil
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	keepalive := func() error {
		fmt.Fprint(w, ": keepalive\n\n")
		flusher.Flush()
		return nil
	}
	s.runDownloadsFeed(r.Context(), lastID, ch, ticker.C, write, keepalive)
}

// downloadsFeedWS is the WebSocket twin of downloadsFeed. Resume via the
// last_event_id query parameter (WebSocket has no Last-Event-ID header). Each
// DownloadFeedMessage is one JSON text message with event_id as the resume
// cursor.
func (s *Server) downloadsFeedWS(w http.ResponseWriter, r *http.Request) {
	lastID, _ := strconv.ParseInt(r.URL.Query().Get("last_event_id"), 10, 64)
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := conn.CloseRead(r.Context())

	ch, cancel := s.hub.SubscribeAll()
	defer cancel()

	write := func(msg domain.DownloadFeedMessage) error {
		raw, _ := json.Marshal(msg)
		return conn.Write(ctx, websocket.MessageText, raw)
	}
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	keepalive := func() error {
		pingCtx, pingCancel := context.WithTimeout(ctx, 5*time.Second)
		defer pingCancel()
		return conn.Ping(pingCtx)
	}
	s.runDownloadsFeed(ctx, lastID, ch, ticker.C, write, keepalive)
}

// runDownloadsFeed drives the overview feed loop shared by the SSE and WS
// endpoints. It drains milestone events newer than the cursor into one message
// per affected job (deduped to the latest action/snapshot) and calls keepalive
// on each tick. write returning an error (client gone) ends the loop.
func (s *Server) runDownloadsFeed(ctx context.Context, lastID int64, ch <-chan domain.Event, tick <-chan time.Time, write func(domain.DownloadFeedMessage) error, keepalive func() error) {
	drain := func() error {
		events, err := s.store.ListMilestoneEventsAfter(ctx, lastID)
		if err != nil {
			return nil // keep the connection; the next wake or tick retries
		}
		type pendingMessage struct {
			eventID int64
			deleted bool
		}
		// One upsert per job, carrying that job's own highest milestone id in
		// this batch — never the batch-wide max. Emitting a job's message with
		// the batch max would advance the client's resume cursor past other
		// jobs' still-undelivered messages, so a mid-batch disconnect would skip
		// them permanently (ListMilestoneEventsAfter is strict id>afterID).
		latest := map[string]pendingMessage{}
		var order []string
		for _, ev := range events {
			if _, seen := latest[ev.JobID]; !seen {
				order = append(order, ev.JobID)
			}
			if ev.ID > latest[ev.JobID].eventID {
				latest[ev.JobID] = pendingMessage{eventID: ev.ID, deleted: ev.Type == domain.EventDeleted}
			}
		}
		// Send in ascending per-job cursor order so the client's Last-Event-ID
		// only ever moves forward: a disconnect after message k leaves the
		// cursor at k's id, and every later message (higher id) is simply
		// redelivered on reconnect — an idempotent upsert, never a skip.
		sort.Slice(order, func(i, j int) bool { return latest[order[i]].eventID < latest[order[j]].eventID })
		for _, jobID := range order {
			pending := latest[jobID]
			if pending.deleted {
				if err := write(domain.DownloadFeedMessage{Type: "download_deleted", JobID: jobID, EventID: pending.eventID}); err != nil {
					return err
				}
				lastID = pending.eventID
				continue
			}
			snap, ok := s.jobSnapshot(ctx, jobID)
			if !ok {
				continue // job deleted between the milestone and this read
			}
			if err := write(domain.DownloadFeedMessage{Type: "download_upserted", Job: snap, EventID: pending.eventID}); err != nil {
				return err
			}
			lastID = pending.eventID
		}
		return nil
	}

	if err := drain(); err != nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-ch:
			if !ok {
				return
			}
			if err := drain(); err != nil {
				return
			}
		case <-tick:
			if err := drain(); err != nil {
				return
			}
			if keepalive != nil {
				if err := keepalive(); err != nil {
					return
				}
			}
		}
	}
}

// decodeJSONBody applies the same resource and framing rules to every JSON
// endpoint: at most maxJSONBodyBytes, exactly one JSON value, and optionally
// no unknown object fields. It writes the request error response itself so a
// MaxBytesError can consistently map to 413 instead of being flattened into
// a malformed-JSON 400.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any, disallowUnknownFields bool) bool {
	if r.ContentLength > maxJSONBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("JSON request body exceeds %d bytes", maxJSONBodyBytes))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	if disallowUnknownFields {
		decoder.DisallowUnknownFields()
	}
	if err := decoder.Decode(dst); err != nil {
		writeJSONBodyError(w, err)
		return false
	}

	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("request body must contain exactly one JSON value")
		}
		writeJSONBodyError(w, err)
		return false
	}
	return true
}

func writeJSONBodyError(w http.ResponseWriter, err error) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Errorf("JSON request body exceeds %d bytes", tooLarge.Limit))
		return
	}
	writeError(w, http.StatusBadRequest, err)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

func writeWrapperError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, wrapper.ErrAuthenticationFailed):
		status = http.StatusUnauthorized
	case errors.Is(err, wrapper.ErrAlreadyLoggedIn), errors.Is(err, wrapper.ErrLoginSessionBusy):
		status = http.StatusConflict
	case errors.Is(err, wrapper.ErrLoginSessionNotFound), errors.Is(err, wrapper.ErrAccountNotFound):
		status = http.StatusNotFound
	case errors.Is(err, wrapper.ErrLoginTimeout):
		status = http.StatusGatewayTimeout
	}
	writeError(w, status, err)
}
