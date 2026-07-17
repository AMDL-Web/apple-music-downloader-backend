package config

import (
	"reflect"

	"gopkg.in/yaml.v3"
)

// MutableView returns only the runtime-changeable part of cfg — the shape
// GET/PUT /api/v1/config exchange with clients, which have no use for the
// startup-bound fields the update endpoint refuses to change anyway. It is
// derived from isRuntimeKey, so it always matches what the runtime config
// file holds; the deprecated catalog.media_user_token_priority is normalized
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
// signing, worker pool size, tool paths). Changing them through the runtime
// config API would silently do nothing, so PUT /api/v1/config rejects an
// update whenever this returns a non-empty list. The startup-bound set is
// everything isRuntimeKey does not claim; runtime-managed fields take effect
// immediately for new requests and newly started jobs.
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
