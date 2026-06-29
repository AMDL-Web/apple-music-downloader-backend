package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"amdl/backend/internal/config"
	"amdl/backend/internal/db"
	"amdl/backend/internal/domain"
	"amdl/backend/internal/events"
	"amdl/backend/internal/jobs"
	"amdl/backend/internal/media"
	"amdl/backend/internal/wrapper"
)

type Server struct {
	cfg     config.Config
	store   *db.Store
	hub     *events.Hub
	manager *jobs.Manager
	wrapper *wrapper.Client
	tools   *media.ToolChecker
	logger  *slog.Logger
}

func NewServer(cfg config.Config, store *db.Store, hub *events.Hub, manager *jobs.Manager, wrapperClient *wrapper.Client, tools *media.ToolChecker, logger *slog.Logger) *Server {
	return &Server{cfg: cfg, store: store, hub: hub, manager: manager, wrapper: wrapperClient, tools: tools, logger: logger}
}

func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/capabilities", s.capabilities)
	mux.HandleFunc("POST /api/v1/downloads", s.createDownload)
	mux.HandleFunc("GET /api/v1/downloads", s.listDownloads)
	mux.HandleFunc("/api/v1/downloads/", s.downloadByID)
	return cors(mux)
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	status, err := s.wrapper.Status(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "ok",
		"wrapper":       status,
		"wrapper_error": errorString(err),
	})
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"api":                "v1",
		"supported_inputs":   []string{"song_url", "album_url", "playlist_url"},
		"unsupported_inputs": []string{"music_video", "artist", "station", "search"},
		"quality_priority":   s.cfg.Download.QualityPriority,
		"tools":              s.tools.Check(r.Context()),
	})
}

func (s *Server) createDownload(w http.ResponseWriter, r *http.Request) {
	var req domain.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, fmt.Errorf("url is required"))
		return
	}
	job, err := s.manager.Submit(r.Context(), req)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) listDownloads(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	jobs, err := s.store.ListJobs(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, jobs)
}

func (s *Server) downloadByID(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/downloads/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeError(w, http.StatusNotFound, fmt.Errorf("not found"))
		return
	}
	id := parts[0]
	if len(parts) == 1 && r.Method == http.MethodGet {
		s.getDownload(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "cancel" && r.Method == http.MethodPost {
		if err := s.manager.Cancel(r.Context(), id); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
		return
	}
	if len(parts) == 2 && parts[1] == "events" && r.Method == http.MethodGet {
		s.events(w, r, id)
		return
	}
	writeError(w, http.StatusNotFound, fmt.Errorf("not found"))
}

func (s *Server) getDownload(w http.ResponseWriter, r *http.Request, id string) {
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
	writeJSON(w, http.StatusOK, map[string]any{"job": job, "items": items})
}

func (s *Server) events(w http.ResponseWriter, r *http.Request, id string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	lastID, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	existing, _ := s.store.ListEventsAfter(r.Context(), id, lastID)
	for _, ev := range existing {
		writeSSE(w, ev)
		lastID = ev.ID
	}
	flusher.Flush()
	ch, cancel := s.hub.Subscribe(id)
	defer cancel()
	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if ev.ID > lastID {
				writeSSE(w, ev)
				flusher.Flush()
			}
		case <-ticker.C:
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

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
