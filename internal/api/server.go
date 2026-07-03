package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"amdl/internal/auth"
	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
	"amdl/internal/jobs"
	"amdl/internal/media"
	"amdl/internal/wrapper"
)

// maxBatchSubmitURLs caps the number of URLs accepted in a single batch
// submit request.
const maxBatchSubmitURLs = 100

// urlSplitPattern splits a pasted textarea blob of URLs on newlines,
// whitespace, commas, and semicolons (ASCII and full-width variants).
var urlSplitPattern = regexp.MustCompile(`[\r\n\s,;，；]+`)

type Server struct {
	cfg     config.Config
	store   *db.Store
	hub     *events.Hub
	manager *jobs.Manager
	wrapper wrapperService
	quality qualityService
	tools   *media.ToolChecker
	logger  *slog.Logger
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

func NewServer(cfg config.Config, store *db.Store, hub *events.Hub, manager *jobs.Manager, wrapperClient wrapperService, qualityClient qualityService, tools *media.ToolChecker, logger *slog.Logger) *Server {
	return &Server{cfg: cfg, store: store, hub: hub, manager: manager, wrapper: wrapperClient, quality: qualityClient, tools: tools, logger: logger}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /docs", swaggerUI)
	mux.HandleFunc("GET /api/openapi.yaml", openAPI)
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/capabilities", s.capabilities)
	mux.HandleFunc("GET /api/v1/me", s.me)
	mux.HandleFunc("GET /api/v1/users", auth.RequireAdmin(s.listUsers))
	mux.HandleFunc("POST /api/v1/users", auth.RequireAdmin(s.createUser))
	mux.HandleFunc("GET /api/v1/users/{id}", auth.RequireAdmin(s.getUser))
	mux.HandleFunc("PATCH /api/v1/users/{id}", auth.RequireAdmin(s.updateUser))
	mux.HandleFunc("DELETE /api/v1/users/{id}", auth.RequireAdmin(s.deleteUser))
	mux.HandleFunc("GET /api/v1/wrapper/status", auth.RequireAdmin(s.wrapperStatus))
	mux.HandleFunc("POST /api/v1/wrapper/login", auth.RequireAdmin(s.wrapperLogin))
	mux.HandleFunc("POST /api/v1/wrapper/login/{login_id}/2fa", auth.RequireAdmin(s.wrapperTwoStep))
	mux.HandleFunc("POST /api/v1/wrapper/logout", auth.RequireAdmin(s.wrapperLogout))
	mux.HandleFunc("POST /api/v1/quality", s.queryQuality)
	mux.HandleFunc("POST /api/v1/downloads", s.createDownload)
	mux.HandleFunc("GET /api/v1/downloads", s.listDownloads)
	mux.HandleFunc("GET /api/v1/downloads/{id}", s.getDownload)
	mux.HandleFunc("POST /api/v1/downloads/{id}/cancel", s.cancelDownload)
	mux.HandleFunc("GET /api/v1/downloads/{id}/events", s.events)
	return cors(auth.Middleware(s.store, s.cfg.Auth)(mux))
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
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
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
		"retry_policy": map[string]int{
			"operation_retries":      s.cfg.Download.Retries,
			"first_codec_retries":    s.cfg.Download.Retries,
			"fallback_codec_retries": 0,
		},
		"tools": s.tools.Check(r.Context()),
	})
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
	identity, _ := auth.FromContext(r.Context())
	resp := s.manager.SubmitBatch(r.Context(), urls, req.Force, identity.UserID)
	status := http.StatusUnprocessableEntity
	if resp.Accepted > 0 {
		status = http.StatusAccepted
	}
	writeJSON(w, status, resp)
}

func (s *Server) listDownloads(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.FromContext(r.Context())
	userFilter := ""
	if !identity.IsAdmin() {
		userFilter = identity.UserID
	} else if username := strings.TrimSpace(r.URL.Query().Get("user")); username != "" {
		user, err := s.store.GetUserByUsername(r.Context(), username)
		if err != nil {
			writeJSON(w, http.StatusOK, []domain.Job{})
			return
		}
		userFilter = user.ID
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	jobs, err := s.store.ListJobs(r.Context(), limit, userFilter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

// authorizeJob loads a job and enforces ownership: non-admin callers get 404
// for jobs they do not own, hiding their existence.
func (s *Server) authorizeJob(w http.ResponseWriter, r *http.Request, id string) (domain.Job, bool) {
	job, err := s.store.GetJob(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, fmt.Errorf("not found"))
		return domain.Job{}, false
	}
	identity, _ := auth.FromContext(r.Context())
	if !identity.IsAdmin() && job.UserID != identity.UserID {
		writeError(w, http.StatusNotFound, fmt.Errorf("not found"))
		return domain.Job{}, false
	}
	return job, true
}

func (s *Server) cancelDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.authorizeJob(w, r, id); !ok {
		return
	}
	if err := s.manager.Cancel(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (s *Server) getDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := s.authorizeJob(w, r, id)
	if !ok {
		return
	}
	items, err := s.store.ListItems(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"job": job, "items": items,
		"retry_policy": map[string]int{
			"operation_retries":      s.cfg.Download.Retries,
			"first_codec_retries":    s.cfg.Download.Retries,
			"fallback_codec_retries": 0,
		},
	})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.authorizeJob(w, r, id); !ok {
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
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)

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

	// Subscribe before the first drain so an event published between reading the
	// backlog and registering for live updates still wakes a subsequent drain
	// instead of being lost in the gap.
	ch, cancel := s.hub.Subscribe(id)
	defer cancel()
	drain()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			drain()
		case <-ticker.C:
			drain()
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, ev domain.Event) {
	raw, _ := json.Marshal(ev)
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, raw)
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

func writeSubmitError(w http.ResponseWriter, err error) {
	var requestErr *jobs.RequestError
	if errors.As(err, &requestErr) {
		status := http.StatusUnprocessableEntity
		if requestErr.Code == "invalid_url" {
			status = http.StatusBadRequest
		} else if requestErr.Code == "decryptor_unavailable" {
			status = http.StatusServiceUnavailable
		} else if requestErr.Code == "invalid_configuration" {
			status = http.StatusInternalServerError
		}
		body := map[string]any{"error": requestErr.Code, "message": requestErr.Message}
		if requestErr.Storefront != "" {
			body["storefront"] = requestErr.Storefront
		}
		if requestErr.SupportedStorefronts != nil {
			body["supported_storefronts"] = requestErr.SupportedStorefronts
		}
		writeJSON(w, status, body)
		return
	}
	if errors.Is(err, jobs.ErrQueueFull) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "queue_full", "message": err.Error()})
		return
	}
	writeError(w, http.StatusInternalServerError, err)
}
