package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// The configuration is split across two files: a startup file (config.yaml)
// holding the keys consumed once at process start, and a runtime file
// (runtime.yaml) holding the keys PUT /api/v1/config may change while the
// backend runs. isRuntimeKey is the single authority on that split — the
// strict per-file key checks, the file writers, MutableView, and
// RuntimeLockedChanges are all derived from it, so a field added to Config
// lands on the right side of every one of those by classifying it here once.
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
		// max_running_jobs sizes the worker pool built at startup, and the
		// global download/decrypt/wrapper pools are likewise sized once from
		// the startup snapshot.
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

// fileRole says which side of the startup/runtime split a config file holds,
// so loading can reject keys that ended up in the wrong file instead of
// silently preferring one copy over the other.
type fileRole int

const (
	roleStartup fileRole = iota
	roleRuntime
)

func (r fileRole) String() string {
	if r == roleRuntime {
		return "runtime"
	}
	return "startup"
}

// knownKeys returns every dotted config key, derived from the Config struct
// via the same enumeration the environment overlay uses.
func knownKeys() map[string]bool {
	keys := map[string]bool{}
	for _, field := range envFields() {
		keys[field.key] = true
	}
	return keys
}

func knownSections() map[string]bool {
	sections := map[string]bool{}
	for _, field := range envFields() {
		section, _, _ := strings.Cut(field.key, ".")
		sections[section] = true
	}
	return sections
}

// checkFileKeys verifies that every key in a raw config file is a known
// config key on the correct side of the startup/runtime split for the file's
// role. It reports the misplaced key with the file it belongs in, so a key
// moved to the wrong file fails loudly instead of being shadowed by the other
// file's value.
func checkFileKeys(raw []byte, path string, role fileRole) error {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	known, sections := knownKeys(), knownSections()
	for _, section := range slices.Sorted(maps.Keys(doc)) {
		if !sections[section] {
			return fmt.Errorf("%s: unknown configuration section %q", path, section)
		}
		leaves, ok := doc[section].(map[string]any)
		if !ok {
			// A known section holding a non-mapping value (or nothing): let the
			// typed decode report the precise type error at the real location.
			continue
		}
		for _, leaf := range slices.Sorted(maps.Keys(leaves)) {
			key := section + "." + leaf
			if !known[key] {
				return fmt.Errorf("%s: unknown configuration key %q", path, key)
			}
			if role == roleStartup && isRuntimeKey(key) {
				return fmt.Errorf("%s: %s is runtime-managed and belongs in the runtime config file; remove it from the startup config", path, key)
			}
			if role == roleRuntime && !isRuntimeKey(key) {
				return fmt.Errorf("%s: %s is startup-bound and belongs in the startup config file; remove it from the runtime config", path, key)
			}
		}
	}
	return nil
}

// hasRuntimeKeys reports whether raw contains at least one known
// runtime-managed key. EnsureFiles uses it to recognize a pre-split
// config.yaml that still carries the runtime section values.
func hasRuntimeKeys(raw []byte) bool {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return false
	}
	known := knownKeys()
	for section, body := range doc {
		leaves, ok := body.(map[string]any)
		if !ok {
			continue
		}
		for leaf := range leaves {
			key := section + "." + leaf
			if known[key] && isRuntimeKey(key) {
				return true
			}
		}
	}
	return false
}

// decodeFileStrict overlays the config file at path onto cfg after checking
// every key sits in the right file for role. An empty file is a valid file
// with no overrides. Unknown fields that survive the key check (e.g. inside
// malformed sections) are rejected by the decoder.
func decodeFileStrict(cfg *Config, path string, role fileRole) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := checkFileKeys(raw, path, role); err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// filterConfigYAML marshals cfg and keeps only the keys keep accepts,
// preserving the Config struct's section and field order (a plain map
// marshal would reorder keys alphabetically). Empty sections are dropped.
// Fields marked omitempty that hold their zero value (only the deprecated
// catalog.media_user_token_priority) never appear in the output.
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

func isStartupKey(key string) bool { return !isRuntimeKey(key) }

// resetRuntimeToDefaults sets every runtime-managed field of cfg back to its
// built-in default. Store.Reload uses it so a key deleted from the runtime
// file reads as "back to default", matching how a fresh load would see it.
func resetRuntimeToDefaults(cfg *Config) {
	defaults := reflect.ValueOf(Default())
	target := reflect.ValueOf(cfg).Elem()
	for _, field := range envFields() {
		if isRuntimeKey(field.key) {
			target.FieldByIndex(field.index).Set(defaults.FieldByIndex(field.index))
		}
	}
}
