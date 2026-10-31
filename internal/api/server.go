package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"amdl/internal/config"
	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/events"
	"amdl/internal/jobs"
	"amdl/internal/logging"
	"amdl/internal/usertoken"
)

// Deps are the components the HTTP layer serves. Every field is required
// except Logger, Logging, Tokens and Console; New refuses a nil one, so no handler
// answers 503 for a missing dependency.
type Deps struct {
	// Config is the live runtime config shared with the download pipeline;
	// the /config routes read, reload and replace its snapshot.
	Config  *config.Store
	Store   *db.Store
	Hub     *events.Hub
	Manager *jobs.Manager
	// Logger is the base logger of request logs; nil means slog.Default().
	Logger *slog.Logger
	// Logging backs GET /logs and GET /logs/stream and applies runtime
	// log-level changes. When it is nil, or logging.buffer_size is 0 (no
	// in-memory retention), the two log routes answer 503
	// feature_unavailable.
	Logging     *logging.System
	Wrapper     WrapperService
	Quality     QualityService
	LibrarySync LibrarySyncService
	// Catalog backs POST /lookup, GET /search, GET /artists/{id}/albums and
	// GET /albums/{id}.
	Catalog CatalogService
	// Tokens picks the media-user-token for lookups and reports the
	// wrapper's, masked, on GET /wrapper/status; nil means
	// catalog.media_user_token alone.
	Tokens *usertoken.Provider
	// Console serves the web console under /console/ (apps/console), and GET /
	// redirects to it. Nil leaves both routes out. The binary passes it in so
	// this package does not import a product.
	Console http.Handler
	// Hooks writes hooks.yaml for the hook routes that change it; nil
	// answers them 503 feature_unavailable.
	Hooks HooksService
}

// Server holds the HTTP handlers; build it with New and serve Routes.
type Server struct {
	cfg       *config.Store
	store     *db.Store
	hub       *events.Hub
	manager   *jobs.Manager
	logger    *slog.Logger
	logSystem *logging.System
	logStore  *logging.Store
	// logsOff is set when logging.buffer_size is 0: the log routes answer 503
	// feature_unavailable with that reason.
	logsOff     bool
	wrapper     WrapperService
	quality     QualityService
	librarySync LibrarySyncService
	catalog     CatalogService
	tokens      *usertoken.Provider
	console     http.Handler
	hooks       HooksService
	// now is the clock of GET /system uptime and the GET /stats window and
	// cache; nil means time.Now.
	now        func() time.Time
	statsCache statsCache
	// readEvents reads a page of a job's events for the job event streams;
	// nil reads the store. Tests make reads fail through it.
	readEvents func(ctx context.Context, jobID string, afterID int64, limit int) ([]domain.Event, error)
	// streams is cancelled by EndStreams; every SSE loop also watches it.
	streams    context.Context
	endStreams context.CancelFunc
}

// New returns a Server for deps, or an error naming the first required
// dependency that is nil. It does no I/O.
func New(deps Deps) (*Server, error) {
	for _, dep := range []struct {
		name    string
		missing bool
	}{
		{"Config", deps.Config == nil},
		{"Store", deps.Store == nil},
		{"Hub", deps.Hub == nil},
		{"Manager", deps.Manager == nil},
		{"Wrapper", deps.Wrapper == nil},
		{"Quality", deps.Quality == nil},
		{"LibrarySync", deps.LibrarySync == nil},
		{"Catalog", deps.Catalog == nil},
	} {
		if dep.missing {
			return nil, fmt.Errorf("api: Deps.%s is required", dep.name)
		}
	}
	s := &Server{
		cfg: deps.Config, store: deps.Store, hub: deps.Hub, manager: deps.Manager,
		logger: deps.Logger, logSystem: deps.Logging,
		wrapper: deps.Wrapper, quality: deps.Quality, librarySync: deps.LibrarySync,
		catalog: deps.Catalog, tokens: deps.Tokens, console: deps.Console, hooks: deps.Hooks,
	}
	s.streams, s.endStreams = context.WithCancel(context.Background())
	// logging.buffer_size is a startup key, so the snapshot read here is the
	// size the log store was built with. A store without retention keeps no
	// history for the log routes to serve.
	if deps.Logging != nil {
		if deps.Config.Get().Logging.BufferSize == 0 {
			s.logsOff = true
		} else {
			s.logStore = deps.Logging.Store
		}
	}
	return s, nil
}

// currentConfig returns the live runtime config snapshot; nil-safe for test
// Servers constructed without a config store.
func (s *Server) currentConfig() config.Config {
	if s.cfg == nil {
		return config.Config{}
	}
	return s.cfg.Get()
}

// route is one line of the route table. Every pattern under /api/v1/ is an
// operation in openapi.yaml and the reverse (TestRoutesMatchSpec).
type route struct {
	method  string
	pattern string
	handler http.Handler
}

func (s *Server) routes() []route {
	h := func(f http.HandlerFunc) http.Handler { return f }
	table := []route{
		{http.MethodGet, "/api/v1/health", h(s.health)},
		{http.MethodGet, "/api/v1/system", h(s.systemInfo)},
		{http.MethodGet, "/api/v1/stats", h(s.stats)},
		{http.MethodGet, "/api/v1/logs", h(s.listLogs)},
		{http.MethodGet, "/api/v1/logs/stream", h(s.streamLogs)},
		{http.MethodGet, "/api/v1/config", h(s.getConfig)},
		{http.MethodPut, "/api/v1/config", h(s.updateConfig)},
		{http.MethodPost, "/api/v1/config/reload", h(s.reloadConfig)},
		{http.MethodGet, "/api/v1/hooks", h(s.listHooks)},
		{http.MethodPut, "/api/v1/hooks", h(s.setHooksEnabled)},
		{http.MethodGet, "/api/v1/hooks/presets", h(s.listHookPresets)},
		{http.MethodPut, "/api/v1/hooks/{name}", h(s.updateHook)},
		{http.MethodDelete, "/api/v1/hooks/{name}", h(s.deleteHook)},
		{http.MethodPost, "/api/v1/hooks/{name}/test", h(s.testHook)},
		{http.MethodGet, "/api/v1/library-sync", h(s.librarySyncStatus)},
		{http.MethodPost, "/api/v1/library-sync/reset", h(s.librarySyncReset)},
		{http.MethodGet, "/api/v1/wrapper/status", h(s.wrapperStatus)},
		{http.MethodPost, "/api/v1/wrapper/login", h(s.wrapperLogin)},
		{http.MethodPost, "/api/v1/wrapper/login/{login_id}/2fa", h(s.wrapperTwoStep)},
		{http.MethodPost, "/api/v1/wrapper/logout", h(s.wrapperLogout)},
		{http.MethodPost, "/api/v1/quality", h(s.queryQuality)},
		{http.MethodPost, "/api/v1/lookup", h(s.lookupLink)},
		{http.MethodGet, "/api/v1/search", h(s.search)},
		{http.MethodGet, "/api/v1/artists/{artist_id}/albums", h(s.artistAlbums)},
		{http.MethodGet, "/api/v1/albums/{album_id}", h(s.getAlbum)},
		{http.MethodPost, "/api/v1/downloads", h(s.createDownload)},
		{http.MethodGet, "/api/v1/downloads", h(s.listDownloads)},
		// The literal "events" segment is more specific than "{id}", so the
		// overview feed takes precedence over GET /api/v1/downloads/{id} for
		// that exact path (a real job id is never "events").
		{http.MethodGet, "/api/v1/downloads/events", h(s.downloadsFeed)},
		{http.MethodGet, "/api/v1/downloads/{id}", h(s.getDownload)},
		{http.MethodDelete, "/api/v1/downloads/{id}", h(s.deleteDownload)},
		{http.MethodPost, "/api/v1/downloads/{id}/cancel", h(s.cancelDownload)},
		{http.MethodPost, "/api/v1/downloads/{id}/retry", h(s.retryDownload)},
		{http.MethodGet, "/api/v1/downloads/{id}/events", h(s.jobEvents)},
	}
	if s.console == nil {
		return table
	}
	return append(table,
		route{http.MethodGet, "/{$}", h(redirectToConsole)},
		route{http.MethodGet, "/console/", http.StripPrefix("/console", s.console)},
	)
}

// Routes returns the handler to serve: the route table on one mux, wrapped in
// request observation (X-Request-ID, access log, panic recovery). There are
// no CORS headers: the only client is the console, served from this origin.
func (s *Server) Routes() http.Handler {
	table := s.routes()
	mux := http.NewServeMux()
	for _, rt := range table {
		mux.Handle(rt.method+" "+rt.pattern, rt.handler)
	}
	// http.ServeMux answers an unknown path or a wrong method with its own
	// plain-text 404/405. Under /api/ every miss reaches this catch-all
	// instead, because a method-less pattern matches whatever a method
	// pattern does not, and it answers with the JSON error envelope.
	mux.Handle("/api/", apiFallback(table))
	return s.observeHTTP(mux)
}

// apiFallback answers a request under /api/ that no route matched: 405
// method_not_allowed with an Allow header when some route has the path with
// another method, else 404 route_not_found. Path matching uses a ServeMux per
// pattern so it agrees with the real routing.
func apiFallback(table []route) http.Handler {
	type pathRoutes struct {
		probe   *http.ServeMux
		methods []string
	}
	byPattern := map[string]*pathRoutes{}
	var paths []*pathRoutes
	for _, rt := range table {
		if !strings.HasPrefix(rt.pattern, "/api/") {
			continue
		}
		p, ok := byPattern[rt.pattern]
		if !ok {
			p = &pathRoutes{probe: http.NewServeMux()}
			p.probe.Handle(rt.pattern, http.NotFoundHandler())
			byPattern[rt.pattern] = p
			paths = append(paths, p)
		}
		p.methods = append(p.methods, rt.method)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		allowed := map[string]bool{}
		for _, p := range paths {
			if _, pattern := p.probe.Handler(r); pattern == "" {
				continue
			}
			for _, method := range p.methods {
				allowed[method] = true
				if method == http.MethodGet {
					allowed[http.MethodHead] = true
				}
			}
		}
		if len(allowed) == 0 {
			writeError(w, http.StatusNotFound, CodeRouteNotFound, "no route for "+r.URL.Path)
			return
		}
		methods := make([]string, 0, len(allowed))
		for method := range allowed {
			methods = append(methods, method)
		}
		sort.Strings(methods)
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeError(w, http.StatusMethodNotAllowed, CodeMethodNotAllowed, r.Method+" is not allowed on "+r.URL.Path)
	})
}

// redirectToConsole sends the bare root to the web console.
func redirectToConsole(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/console/", http.StatusFound)
}
