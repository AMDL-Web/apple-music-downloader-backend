package api

import (
	"fmt"
	"net/http"
)

// librarySyncStatus reports what the watcher is doing: the live config values
// plus what the last pass observed. It answers even while the feature is
// disabled, so a client can render the toggle's current state without guessing.
func (s *Server) librarySyncStatus(w http.ResponseWriter, r *http.Request) {
	if s.librarySync == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("library sync is not wired into this process"))
		return
	}
	writeJSON(w, http.StatusOK, s.librarySync.Status())
}

// librarySyncReset forgets the watcher's watermark.
//
// This deliberately does not re-download anything. The next pass finds no
// anchor, re-anchors to the library as it stands then, and submits nothing — so
// the effect is "stop treating my current library as new", not "fetch it all
// again". Re-downloading one album is a normal POST /api/v1/downloads.
func (s *Server) librarySyncReset(w http.ResponseWriter, r *http.Request) {
	if s.librarySync == nil {
		writeError(w, http.StatusServiceUnavailable, fmt.Errorf("library sync is not wired into this process"))
		return
	}
	cleared, err := s.librarySync.ResetAnchor(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cleared": cleared,
		"status":  s.librarySync.Status(),
	})
}
