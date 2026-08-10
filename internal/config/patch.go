package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// Patch is a decoded PUT /api/v1/config body: one entry per key the request
// mentions. A non-nil value is the JSON to store in the database layer; a nil
// value is an explicit JSON null, which resets the key by dropping its row so
// the config file, or failing that the built-in default, takes over again. A
// key the body never mentions is not in the map at all and keeps whatever it
// currently has.
type Patch map[string]*string

// Keys returns the patched keys in a stable order, for messages and diffs.
func (p Patch) Keys() []string { return slices.Sorted(maps.Keys(p)) }

// ParsePatch decodes a config update body into per-key values. Unknown
// sections and keys, values of the wrong type, and non-object bodies are all
// rejected here, so everything downstream can assume well-formed input. Values
// are re-encoded canonically, which is what the database ends up storing.
func ParsePatch(body []byte) (Patch, error) {
	var sections map[string]json.RawMessage
	if err := json.Unmarshal(body, &sections); err != nil {
		return nil, err
	}
	byKey := map[string]envField{}
	for _, field := range envFields() {
		byKey[field.key] = field
	}
	cfgType := reflect.TypeOf(Config{})
	patch := Patch{}
	for _, section := range slices.Sorted(maps.Keys(sections)) {
		var leaves map[string]json.RawMessage
		if err := json.Unmarshal(sections[section], &leaves); err != nil {
			return nil, fmt.Errorf("config section %q must be an object: %w", section, err)
		}
		for _, leaf := range slices.Sorted(maps.Keys(leaves)) {
			key := section + "." + leaf
			field, ok := byKey[key]
			if !ok {
				return nil, fmt.Errorf("unknown configuration key %q", key)
			}
			raw := leaves[leaf]
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
				patch[key] = nil
				continue
			}
			// Decode into a fresh zero value of the field's own type: it type
			// checks the request and normalizes the encoding in one step, so
			// two spellings of the same value cannot look like a change.
			scratch := reflect.New(cfgType.FieldByIndex(field.index).Type).Elem()
			if err := decodeSetting(scratch, string(raw)); err != nil {
				return nil, fmt.Errorf("configuration key %q: %w", key, err)
			}
			encoded, err := encodeSetting(scratch)
			if err != nil {
				return nil, fmt.Errorf("configuration key %q: %w", key, err)
			}
			patch[key] = &encoded
		}
	}
	return patch, nil
}

// RejectReason classifies why an update cannot be applied as written.
type RejectReason string

const (
	// RejectStartupBound: the key is consumed once, when the process starts,
	// so the API cannot change it at all.
	RejectStartupBound RejectReason = "startup_bound"
	// RejectLocked: the key's effective value comes from the config file or an
	// AMDL_* variable, either of which outranks the database layer this API
	// writes.
	RejectLocked RejectReason = "locked"
	// RejectInvalid: the merged configuration fails validation.
	RejectInvalid RejectReason = "invalid"
)

// RejectedError reports an update refused on its merits rather than a
// persistence failure, so the API can answer 422 with the offending keys
// instead of re-deriving them.
type RejectedError struct {
	Reason RejectReason
	// Keys are the offending dotted config keys, sorted. Empty for
	// RejectInvalid, whose Err already names what failed.
	Keys []string
	// Sources maps each locked key to the layer holding it. Set only for
	// RejectLocked.
	Sources map[string]Source
	Err     error
}

func (e *RejectedError) Error() string {
	switch e.Reason {
	case RejectStartupBound:
		return fmt.Sprintf("fields are read once at startup and can only be set in the config file or the environment: %s", strings.Join(e.Keys, ", "))
	case RejectLocked:
		parts := make([]string, 0, len(e.Keys))
		for _, key := range e.Keys {
			switch e.Sources[key] {
			case SourceEnv:
				parts = append(parts, fmt.Sprintf("%s (pinned by %s)", key, EnvVarName(key)))
			default:
				parts = append(parts, fmt.Sprintf("%s (pinned by the config file)", key))
			}
		}
		return fmt.Sprintf("fields are pinned by a higher layer than the one this API writes; remove the override and restart to change them: %s", strings.Join(parts, ", "))
	default:
		return e.Err.Error()
	}
}

func (e *RejectedError) Unwrap() error { return e.Err }
