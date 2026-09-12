package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"amdl/internal/config"
)

// getConfig returns the runtime-changeable part of the current config
// (mutable download fields, logging level/access log, simulate,
// catalog.album_track_url_mode/media_user_token/signed_mode_hls_source).
// Startup-bound fields are omitted: clients cannot change them through this
// API, so they have no reason to see them here.
//
// The combined config file is re-read first. Runtime-field edits take effect
// on the next GET; startup-field edits remain pending until restart. If the
// file is currently unreadable or invalid (e.g. an edit in progress), the
// last good snapshot is served and reload_error reports why.
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

func (s *Server) listHooks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.manager.ListHooks())
}

// updateConfig merges the request body onto the current runtime config:
// omitted fields keep their current values, present fields (including whole
// sections) are replaced. The merged result must pass full config validation,
// and fields consumed only at startup (server, database, logging outputs,
// wrapper, tools, catalog client/signing/request limits, and process-wide
// download worker/media pools) are rejected — changing them at runtime would
// silently do nothing. Accepted
// changes apply to new requests and newly started jobs immediately and are
// written back to the combined config file, so they survive restarts.
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
		// v1.2 exposed catalog.media_user_token_priority. Keep accepting valid
		// legacy payloads, but normalize the now-redundant field away so it is
		// neither effective nor written back to the managed config.
		if err := merged.NormalizeDeprecated(); err != nil {
			rejectStatus, rejectErr = http.StatusUnprocessableEntity, err
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
