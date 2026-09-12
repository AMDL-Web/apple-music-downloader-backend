package api

import (
	"context"
	"log/slog"
	"net/http"

	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/events"
	"amdl/internal/jobs"
	"amdl/internal/librarysync"
	"amdl/internal/logging"
	"amdl/internal/media"
	"amdl/internal/wrapper"
)

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
	// librarySync is optional; nil means the watcher was not wired.
	librarySync librarySyncService
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

// librarySyncService is the library watcher, wired in after construction via
// SetLibrarySync rather than as another NewServer parameter — the watcher needs
// the job manager, which is itself a NewServer argument, and the many existing
// call sites should not all have to learn about an optional dependency. A nil
// value means the feature is not wired, and its endpoints answer 503.
type librarySyncService interface {
	Status() librarysync.Status
	ResetAnchor(ctx context.Context) (int64, error)
}

func NewServer(cfg *config.Store, store *db.Store, hub *events.Hub, manager *jobs.Manager, wrapperClient wrapperService, qualityClient qualityService, devToken developerTokenService, logger *slog.Logger, logSystem ...*logging.System) *Server {
	s := &Server{cfg: cfg, store: store, hub: hub, manager: manager, wrapper: wrapperClient, quality: qualityClient, devToken: devToken, logger: logger}
	if len(logSystem) > 0 && logSystem[0] != nil {
		s.logSystem = logSystem[0]
		s.logStore = logSystem[0].Store
	}
	return s
}

// SetLibrarySync attaches the library watcher. Optional: without it the
// /api/v1/library-sync endpoints answer 503 and nothing else changes.
func (s *Server) SetLibrarySync(watcher librarySyncService) {
	s.librarySync = watcher
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
	mux.HandleFunc("GET /api/v1/logs/stream/ws", s.streamLogsWS)
	mux.HandleFunc("GET /api/v1/config", s.getConfig)
	mux.HandleFunc("PUT /api/v1/config", s.updateConfig)
	mux.HandleFunc("GET /api/v1/hooks", s.listHooks)
	mux.HandleFunc("GET /api/v1/developer-token", s.developerToken)
	mux.HandleFunc("GET /api/v1/library-sync", s.librarySyncStatus)
	mux.HandleFunc("POST /api/v1/library-sync/reset", s.librarySyncReset)
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
