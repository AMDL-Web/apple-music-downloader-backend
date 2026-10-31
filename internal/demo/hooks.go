package demo

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"time"

	"amdl/internal/client"
	"amdl/internal/hookpreset"
)

// demoHooks are the hooks the demo starts with: a command, which the API
// only lists, and a library refresh.
func demoHooks() client.Hooks {
	return client.Hooks{Enabled: true, Entries: []client.HookEntry{
		{Name: "tag-and-move", Enabled: true, Type: "exec", Events: []string{"job_finished"}, JobTypes: []string{}},
		{Name: "jellyfin", Enabled: true, Type: "webhook", Events: []string{"job_finished"}, JobTypes: []string{"album"},
			Preset: "jellyfin", Method: http.MethodPost, Origin: "http://jellyfin.local:8096", HeaderNames: []string{"Authorization"}, MaxAttempts: 2},
	}}
}

func (b *Backend) listHooks(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	writeJSON(w, http.StatusOK, b.hooks)
}

func (b *Backend) setHooksEnabled(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil || req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "enabled is required")
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.hooks.Enabled = *req.Enabled
	writeJSON(w, http.StatusOK, b.hooks)
}

func (b *Backend) hookPresets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"presets": hookpreset.All})
}

// putHook applies a write as the server does: omitted fields keep their
// values, a preset builds the request, a command hook is refused, a new hook
// needs events and a url.
func (b *Backend) putHook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req client.HookWrite
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	preset, newEvents := "", []string(nil)
	if req.Preset != "" {
		p, ok := hookpreset.Find(req.Preset)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "no preset is named "+req.Preset)
			return
		}
		built, err := p.Build(req.Values)
		if err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_hook", err.Error())
			return
		}
		req.Method, req.URL, req.Headers = built.Method, built.URL, &built.Headers
		preset, newEvents = p.ID, p.Events
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	i := slices.IndexFunc(b.hooks.Entries, func(e client.HookEntry) bool { return e.Name == name })
	e := client.HookEntry{Name: name, Enabled: true, Type: "webhook", JobTypes: []string{}, Method: http.MethodPost, MaxAttempts: 1}
	if i >= 0 {
		e = b.hooks.Entries[i]
		if e.Type != "webhook" {
			writeError(w, http.StatusConflict, "hook_read_only", name+" is a command hook; command hooks are edited in hooks.yaml")
			return
		}
	} else {
		e.Events = newEvents
	}
	switch {
	case preset != "":
		e.Preset = preset
	case req.URL != "" || req.Method != "" || req.Headers != nil || req.Body != nil:
		e.Preset = ""
	}
	if req.Enabled != nil {
		e.Enabled = *req.Enabled
	}
	if len(req.Events) > 0 {
		e.Events = req.Events
	}
	if req.JobTypes != nil {
		e.JobTypes = *req.JobTypes
	}
	if req.MaxAttempts != nil {
		e.MaxAttempts = max(1, *req.MaxAttempts)
	}
	if req.Method != "" {
		e.Method = req.Method
	}
	if req.URL != "" {
		u, err := url.Parse(req.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			writeError(w, http.StatusUnprocessableEntity, "invalid_hook", `hooks.entries["`+name+`"]: url: must be an absolute http or https URL`)
			return
		}
		e.Origin = u.Scheme + "://" + u.Host
	}
	if req.Headers != nil {
		e.HeaderNames = nil
		for k := range *req.Headers {
			e.HeaderNames = append(e.HeaderNames, k)
		}
		sort.Strings(e.HeaderNames)
	}
	if len(e.Events) == 0 || e.Origin == "" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_hook", `hooks.entries["`+name+`"]: a new hook needs events and a url`)
		return
	}
	if i >= 0 {
		b.hooks.Entries[i] = e
	} else {
		b.hooks.Entries = append(b.hooks.Entries, e)
	}
	writeJSON(w, http.StatusOK, e)
}

func (b *Backend) deleteHook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	b.mu.Lock()
	defer b.mu.Unlock()
	i := slices.IndexFunc(b.hooks.Entries, func(e client.HookEntry) bool { return e.Name == name })
	switch {
	case i < 0:
		writeError(w, http.StatusNotFound, "hook_not_found", "no hook is named "+name)
	case b.hooks.Entries[i].Type != "webhook":
		writeError(w, http.StatusConflict, "hook_read_only", name+" is a command hook; command hooks are edited in hooks.yaml")
	default:
		b.hooks.Entries = slices.Delete(b.hooks.Entries, i, i+1)
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}
}

// testHook answers after a short wait, as a request over the network
// would: the demo's hosts are made up, so it never sends one.
func (b *Backend) testHook(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	b.mu.Lock()
	i := slices.IndexFunc(b.hooks.Entries, func(e client.HookEntry) bool { return e.Name == name })
	var e client.HookEntry
	if i >= 0 {
		e = b.hooks.Entries[i]
	}
	b.mu.Unlock()
	switch {
	case i < 0:
		writeError(w, http.StatusNotFound, "hook_not_found", "no hook is named "+name)
		return
	case e.Type != "webhook":
		writeError(w, http.StatusConflict, "hook_read_only", name+" is a command hook; command hooks are edited in hooks.yaml")
		return
	}
	select {
	case <-time.After(600 * time.Millisecond):
	case <-r.Context().Done():
		return
	}
	writeJSON(w, http.StatusOK, client.HookTest{Succeeded: true})
}
