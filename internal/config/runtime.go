package config

import (
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// isRuntimeKey is the single authority on which fields PUT /api/v1/config may
// change, which fields the database layer may hold, and which fields
// Store.Reload applies without a restart. Everything it does not claim is
// startup-bound: consumed once while the process boots, so it can only come
// from the config file, an AMDL_* variable, or the built-in default.
func isRuntimeKey(key string) bool {
	switch key {
	case "logging.level", "logging.access_log",
		"catalog.album_track_url_mode", "catalog.media_user_token",
		"catalog.signed_mode_hls_source",
		// Read per job when the input resolves, so flipping it takes effect on
		// newly started jobs without a restart.
		"catalog.motion_artwork_enabled":
		return true
	}
	section, _, _ := strings.Cut(key, ".")
	switch section {
	case "download":
		switch key {
		case "download.max_running_jobs",
			"download.max_parallel_downloads",
			"download.max_parallel_decrypts",
			"download.max_parallel_wrapper_requests":
			return false
		}
		return true
	case "simulate":
		return true
	// The watcher re-reads both keys every tick, so the whole section is
	// hot-reloadable: toggling it off stops the polling itself, not just the
	// handling of its results.
	case "library_sync":
		return true
	}
	return false
}

// knownKeys returns every dotted configuration key from Config's YAML tags.
func knownKeys() map[string]bool {
	keys := map[string]bool{}
	for _, field := range envFields() {
		keys[field.key] = true
	}
	return keys
}

// filterConfigYAML marshals cfg and retains only keys accepted by keep. It is
// used to produce the runtime-only API representation from the combined file.
func filterConfigYAML(cfg Config, keep func(key string) bool) ([]byte, error) {
	full, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(full, &doc); err != nil {
		return nil, err
	}
	root := doc.Content[0]
	filtered := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i := 0; i+1 < len(root.Content); i += 2 {
		section, body := root.Content[i], root.Content[i+1]
		if body.Kind != yaml.MappingNode {
			continue
		}
		kept := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for j := 0; j+1 < len(body.Content); j += 2 {
			if keep(section.Value + "." + body.Content[j].Value) {
				kept.Content = append(kept.Content, body.Content[j], body.Content[j+1])
			}
		}
		if len(kept.Content) > 0 {
			filtered.Content = append(filtered.Content, section, kept)
		}
	}
	return yaml.Marshal(filtered)
}

// copyRuntimeFields copies only hot-reloadable fields from source to target.
func copyRuntimeFields(target *Config, source Config) {
	sourceValue := reflect.ValueOf(source)
	targetValue := reflect.ValueOf(target).Elem()
	for _, field := range envFields() {
		if isRuntimeKey(field.key) {
			targetValue.FieldByIndex(field.index).Set(sourceValue.FieldByIndex(field.index))
		}
	}
}

// MutableView returns only the runtime-changeable part of cfg — the shape
// GET/PUT /api/v1/config exchange with clients, which have no use for the
// startup-bound fields the update endpoint refuses to change anyway. It is
// derived from isRuntimeKey, so it always matches the set of keys the database
// layer can hold.
func MutableView(cfg Config) map[string]any {
	raw, err := filterConfigYAML(cfg, isRuntimeKey)
	if err != nil {
		return map[string]any{}
	}
	view := map[string]any{}
	_ = yaml.Unmarshal(raw, &view)
	return view
}

// SourcesView reports, for every key MutableView exposes, which layer supplied
// the effective value. Startup-bound keys are left out: their sources describe
// the file as it is now rather than the values the running process was built
// from, which would be misleading rather than useful.
func SourcesView(sources map[string]Source) map[string]string {
	view := map[string]string{}
	for _, field := range envFields() {
		if !isRuntimeKey(field.key) {
			continue
		}
		source := sources[field.key]
		if source == "" {
			source = SourceDefault
		}
		view[field.key] = string(source)
	}
	return view
}

// LockedView lists the runtime keys a client cannot change, sorted. A key is
// locked when the config file or an AMDL_* variable supplies its value, since
// both outrank the database layer PUT /api/v1/config writes.
func LockedView(sources map[string]Source) []string {
	locked := []string{}
	for _, field := range envFields() {
		if isRuntimeKey(field.key) && sources[field.key].Locked() {
			locked = append(locked, field.key)
		}
	}
	slices.Sort(locked)
	return locked
}
