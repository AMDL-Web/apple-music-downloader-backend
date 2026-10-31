package api

import (
	"context"
	"net/http"
	"strings"

	"amdl/internal/applemusic"
	"amdl/internal/media"
)

// QualityService probes the declared codecs and audio quality of a URL
// without creating a job.
type QualityService interface {
	QueryQuality(context.Context, media.QualityRequest) (media.QualityResult, error)
}

func (s *Server) queryQuality(w http.ResponseWriter, r *http.Request) {
	var req media.QualityRequest
	if !decodeJSONBody(w, r, &req, false) {
		return
	}
	req.URL = strings.TrimSpace(req.URL)
	if req.URL == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "url is required")
		return
	}
	result, err := s.quality.QueryQuality(r.Context(), req)
	if applemusic.IsNotFound(err) {
		writeError(w, http.StatusNotFound, CodeCatalogNotFound, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, CodeUpstreamError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// health answers 503 with the HealthResponse body, not the error envelope:
// monitors and the console read status and database_error from it.
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
