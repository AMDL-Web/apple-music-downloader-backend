package config

import (
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// isRuntimeKey is the single authority on which fields PUT /api/v1/config may
// change and which fields Store.Reload applies without a restart.
func isRuntimeKey(key string) bool {
	switch key {
	case "logging.level", "logging.access_log",
		"catalog.album_track_url_mode", "catalog.media_user_token",
		"catalog.media_user_token_priority", "catalog.signed_mode_hls_source":
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
// derived from isRuntimeKey, so it always matches the hot-reloadable fields
// in config.yaml; the deprecated catalog.media_user_token_priority is normalized
// to empty before this runs and its omitempty tag keeps it out of the view.
func MutableView(cfg Config) map[string]any {
	raw, err := filterConfigYAML(cfg, isRuntimeKey)
	if err != nil {
		return map[string]any{}
	}
	view := map[string]any{}
	_ = yaml.Unmarshal(raw, &view)
	return view
}

// RuntimeLockedChanges returns the dotted keys of fields that differ between
// old and updated but are consumed only at process startup (listen address,
// database path, logging outputs, wrapper connection, catalog client/token
// signing and Apple request limits, worker/resource pool sizes, tool paths).
// Changing them through the runtime config API would silently do nothing, so
// PUT /api/v1/config rejects an update whenever this returns a non-empty
// list. The startup-bound set is everything isRuntimeKey does not claim;
// runtime-managed fields take effect immediately for new requests and newly
// started jobs.
func RuntimeLockedChanges(old, updated Config) []string {
	var changed []string
	oldValue, updatedValue := reflect.ValueOf(old), reflect.ValueOf(updated)
	for _, field := range envFields() {
		if isRuntimeKey(field.key) {
			continue
		}
		if !reflect.DeepEqual(oldValue.FieldByIndex(field.index).Interface(), updatedValue.FieldByIndex(field.index).Interface()) {
			changed = append(changed, field.key)
		}
	}
	return changed
}
