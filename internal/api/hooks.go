package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"amdl/internal/hookpreset"
	"amdl/internal/hooks"
)

// HooksService writes hooks.yaml (hooks.Editor): the master switch and the
// webhook entries. Command entries are never written or run through it.
type HooksService interface {
	SetEnabled(on bool) (hooks.Listing, error)
	PutWebhook(name string, p hooks.WebhookPatch) (hooks.ListedEntry, error)
	Delete(name string) error
	Test(ctx context.Context, name string) error
}

func (s *Server) listHooks(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.manager.ListHooks())
}

// listHookPresets answers with the presets a webhook can be built from,
// whether or not this process can write hooks.yaml.
func (s *Server) listHookPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": hookpreset.All})
}

// hooksEditor is the editor, or false after answering 503 when this process
// has none.
func (s *Server) hooksEditor(w http.ResponseWriter) (HooksService, bool) {
	if s.hooks == nil {
		writeError(w, http.StatusServiceUnavailable, CodeFeatureUnavailable, "hooks.yaml cannot be edited in this process")
		return nil, false
	}
	return s.hooks, true
}

func (s *Server) setHooksEnabled(w http.ResponseWriter, r *http.Request) {
	ed, ok := s.hooksEditor(w)
	if !ok {
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeJSONBody(w, r, &req, true) {
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "enabled is required")
		return
	}
	listing, err := ed.SetEnabled(*req.Enabled)
	if err != nil {
		writeHookError(w, "", err)
		return
	}
	writeJSON(w, http.StatusOK, listing)
}

// hookWrite is the body of PUT /hooks/{name}: an omitted field keeps the
// entry's value, as PUT /config does, so a client changes the events of a
// hook without sending its address and tokens again. Preset and Values build
// the request from internal/hookpreset in place of URL, Method, Headers and
// Body.
type hookWrite struct {
	Preset      *string            `json:"preset"`
	Values      map[string]string  `json:"values"`
	Type        *string            `json:"type"`
	Enabled     *bool              `json:"enabled"`
	Events      *[]string          `json:"events"`
	JobTypes    *[]string          `json:"job_types"`
	MaxAttempts *int               `json:"max_attempts"`
	Method      *string            `json:"method"`
	URL         *string            `json:"url"`
	Headers     *map[string]string `json:"headers"`
	Body        *string            `json:"body"`
}

func (s *Server) updateHook(w http.ResponseWriter, r *http.Request) {
	ed, ok := s.hooksEditor(w)
	if !ok {
		return
	}
	name, ok := hookName(w, r)
	if !ok {
		return
	}
	var req hookWrite
	if !decodeJSONBody(w, r, &req, true) {
		return
	}
	if req.Type != nil && *req.Type != "webhook" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "only webhook hooks are written through the API; command hooks are edited in hooks.yaml")
		return
	}
	patch := hooks.WebhookPatch{
		Enabled: req.Enabled, Events: req.Events, JobTypes: req.JobTypes, MaxAttempts: req.MaxAttempts,
		Method: req.Method, URL: req.URL, Headers: req.Headers, Body: req.Body,
	}
	raw := req.Method != nil || req.URL != nil || req.Headers != nil || req.Body != nil
	switch {
	case req.Preset != nil && raw:
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "preset and values build url, method, headers and body; send one or the other")
		return
	case req.Preset != nil:
		p, ok := hookpreset.Find(*req.Preset)
		if !ok {
			writeError(w, http.StatusBadRequest, CodeInvalidRequest, "no preset is named "+*req.Preset)
			return
		}
		built, err := p.Build(req.Values)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, CodeInvalidHook, err.Error())
			return
		}
		patch.Method, patch.URL, patch.Headers, patch.Body = &built.Method, &built.URL, &built.Headers, &built.Body
		patch.Preset, patch.NewEvents = &p.ID, p.Events
	case req.Values != nil:
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "values need a preset")
		return
	case raw:
		// A request written by hand is no preset's any more.
		none := ""
		patch.Preset = &none
	}
	entry, err := ed.PutWebhook(name, patch)
	if err != nil {
		writeHookError(w, name, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func (s *Server) deleteHook(w http.ResponseWriter, r *http.Request) {
	ed, ok := s.hooksEditor(w)
	if !ok {
		return
	}
	name, ok := hookName(w, r)
	if !ok {
		return
	}
	if err := ed.Delete(name); err != nil {
		writeHookError(w, name, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// testHook answers 200 whether the webhook took the request or not: the
// test ran, and succeeded says how it went.
func (s *Server) testHook(w http.ResponseWriter, r *http.Request) {
	ed, ok := s.hooksEditor(w)
	if !ok {
		return
	}
	name, ok := hookName(w, r)
	if !ok {
		return
	}
	err := ed.Test(r.Context(), name)
	var fileErr *hooks.FileError
	switch {
	case errors.Is(err, hooks.ErrNotFound), errors.Is(err, hooks.ErrReadOnly), errors.As(err, &fileErr):
		writeHookError(w, name, err)
	case err != nil:
		writeJSON(w, http.StatusOK, map[string]any{"succeeded": false, "error": err.Error()})
	default:
		writeJSON(w, http.StatusOK, map[string]any{"succeeded": true})
	}
}

func hookName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if strings.TrimSpace(name) != name || name == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "a hook name cannot be empty or start or end with a space")
		return "", false
	}
	return name, true
}

func writeHookError(w http.ResponseWriter, name string, err error) {
	var invalid *hooks.InvalidError
	var fileErr *hooks.FileError
	switch {
	case errors.Is(err, hooks.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeHookNotFound, "no hook is named "+name)
	case errors.Is(err, hooks.ErrReadOnly):
		writeError(w, http.StatusConflict, CodeHookReadOnly, name+" is a command hook; command hooks are edited in hooks.yaml")
	case errors.As(err, &invalid):
		writeError(w, http.StatusUnprocessableEntity, CodeInvalidHook, invalid.Error())
	case errors.As(err, &fileErr):
		writeError(w, http.StatusUnprocessableEntity, CodeInvalidHooksFile, fileErr.Error())
	default:
		writeError(w, http.StatusInternalServerError, CodeInternalError, err.Error())
	}
}
