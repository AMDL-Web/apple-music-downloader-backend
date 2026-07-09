package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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
	"amdl/internal/media"
	"amdl/internal/wrapper"

	"github.com/coder/websocket"
)

// maxBatchSubmitURLs caps the number of URLs accepted in a single batch
// submit request.
const maxBatchSubmitURLs = 100

// urlSplitPattern splits a pasted textarea blob of URLs on newlines,
// whitespace, commas, and semicolons (ASCII and full-width variants).
var urlSplitPattern = regexp.MustCompile(`[\r\n\s,;，；]+`)

type Server struct {
	cfg      config.Config
	store    *db.Store
	hub      *events.Hub
	manager  *jobs.Manager
	wrapper  wrapperService
	quality  qualityService
	devToken developerTokenService
	tools    *media.ToolChecker
	logger   *slog.Logger
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

func NewServer(cfg config.Config, store *db.Store, hub *events.Hub, manager *jobs.Manager, wrapperClient wrapperService, qualityClient qualityService, devToken developerTokenService, tools *media.ToolChecker, logger *slog.Logger) *Server {
	return &Server{cfg: cfg, store: store, hub: hub, manager: manager, wrapper: wrapperClient, quality: qualityClient, devToken: devToken, tools: tools, logger: logger}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /docs", swaggerUI)
	mux.HandleFunc("GET /api/openapi.yaml", openAPI)
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/capabilities", s.capabilities)
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
	mux.HandleFunc("GET /api/v1/downloads/{id}/events", s.events)
	mux.HandleFunc("GET /api/v1/downloads/{id}/events/ws", s.eventsWS)
	return cors(mux)
}

func (s *Server) queryQuality(w http.ResponseWriter, r *http.Request) {
	var req media.QualityRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,DELETE,OPTIONS")
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

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"api":                  "v1",
		"supported_inputs":     []string{"song_url", "album_url", "playlist_url", "artist_url"},
		"unsupported_inputs":   []string{"music_video", "station", "search"},
		"quality_priority":     s.cfg.Download.QualityPriority,
		"codec_alternative":    s.cfg.Download.CodecAlternative,
		"fallback_codec":       "aac-lc",
		"album_track_url_mode": s.cfg.Catalog.AlbumTrackURLMode,
		"tools":                s.tools.Check(r.Context()),
	})
}

// developerToken hands out a freshly signed Apple Music developer token. Only
// local signing mode can serve it: the legacy web-discovered token is
// origin-locked to music.apple.com and would be rejected anywhere else.
func (s *Server) developerToken(w http.ResponseWriter, r *http.Request) {
	if s.devToken == nil || !s.cfg.Catalog.DeveloperTokenSigningEnabled() {
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
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	resp := s.manager.SubmitBatch(r.Context(), urls, req.Force)
	status := http.StatusUnprocessableEntity
	if resp.Accepted > 0 {
		status = http.StatusAccepted
	}
	writeJSON(w, status, resp)
}

func (s *Server) listDownloads(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
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
	jobs, err := s.store.ListJobs(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	// Derive each job's progress counters from its live items, matching
	// getDownload and the overview feed's pushed snapshots — the stored job-row
	// counters are only refreshed at terminal status, so a running job would
	// otherwise report done_items=0 here.
	for i := range jobs {
		items, err := s.store.ListItems(r.Context(), jobs[i].ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		jobs[i].DoneItems, jobs[i].FailedItems = domain.CountItemProgress(items)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"downloads": jobs, "last_event_id": lastEventID,
	})
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

// eventsExhausted reports whether job's event stream will never deliver
// another event. A terminal job.Status alone is not enough: post-download
// hook dispatch is fire-and-forget and can keep recording hook_started/
// hook_succeeded/hook_failed events well after the job itself reached a
// terminal status (see jobs.Manager.HooksPending), so both must hold.
func (s *Server) eventsExhausted(job domain.Job) bool {
	return job.Status.IsTerminal() && !s.manager.HooksPending(job.ID)
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
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	lastID, _ := strconv.ParseInt(r.URL.Query().Get("last_event_id"), 10, 64)

	// Subscribe before the first backlog read so an event published between
	// that read and registering for live updates still wakes a subsequent
	// drain instead of being lost in the gap. Skipped only once eventsExhausted
	// confirms nothing (job event or hook event) will ever be published for
	// this job again (job.Status and its terminal event are written atomically
	// — see Store.FinalizeJob — so observing it here guarantees the terminal
	// event is already visible to the ListEventsAfter call below).
	var ch <-chan domain.Event
	if !s.eventsExhausted(job) {
		var cancel func()
		ch, cancel = s.hub.Subscribe(id)
		defer cancel()
	}

	pending, err := s.store.ListEventsAfter(r.Context(), id, lastID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(pending) == 0 && s.eventsExhausted(job) {
		// Nothing left to replay and the job will never emit another event:
		// refuse the subscription instead of holding a socket open forever.
		writeError(w, http.StatusConflict, fmt.Errorf("job %s is already %s: no further events will be emitted", id, job.Status))
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
	if s.eventsExhausted(job) {
		// Backlog has been delivered in full and nothing more will ever
		// arrive (job event or hook event), so close instead of idling
		// forever.
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
			if s.eventsExhausted(job) {
				// A pending hook (the reason this loop was entered instead of
				// closing right after the initial backlog) has now recorded
				// its terminal event: nothing more will ever arrive.
				return
			}
		case <-ticker.C:
			if err := drain(); err != nil {
				return
			}
			if s.eventsExhausted(job) {
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
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)

	// Subscribe before the first backlog read so an event published between
	// that read and registering for live updates still wakes a subsequent
	// drain instead of being lost in the gap. Skipped only once eventsExhausted
	// confirms nothing (job event or hook event) will ever be published for
	// this job again (job.Status and its terminal event are written atomically
	// — see Store.FinalizeJob — so observing it here guarantees the terminal
	// event is already visible to the ListEventsAfter call below).
	var ch <-chan domain.Event
	if !s.eventsExhausted(job) {
		var cancel func()
		ch, cancel = s.hub.Subscribe(id)
		defer cancel()
	}

	pending, err := s.store.ListEventsAfter(r.Context(), id, lastID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if len(pending) == 0 && s.eventsExhausted(job) {
		// Nothing left to replay and the job will never emit another event:
		// refuse the subscription instead of holding a connection open forever.
		writeError(w, http.StatusConflict, fmt.Errorf("job %s is already %s: no further events will be emitted", id, job.Status))
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
	if s.eventsExhausted(job) {
		// Backlog has been delivered in full and nothing more will ever
		// arrive (job event or hook event), so close instead of idling
		// forever.
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
			if s.eventsExhausted(job) {
				// A pending hook (the reason this loop was entered instead of
				// closing right after the initial backlog) has now recorded
				// its terminal event: nothing more will ever arrive.
				return
			}
		case <-ticker.C:
			drain()
			if s.eventsExhausted(job) {
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
