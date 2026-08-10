package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Source names the layer that supplied a key's effective value. The layers are
// ordered environment > config file > database > built-in default: a value set
// in a lower layer is shadowed, not merged, and only the winning layer is
// reported here.
type Source string

const (
	SourceDefault  Source = "default"
	SourceDatabase Source = "db"
	SourceFile     Source = "file"
	SourceEnv      Source = "env"
)

// Locked reports whether a key backed by this layer refuses runtime edits.
// PUT /api/v1/config writes the database layer only, so a key held by the
// config file or an AMDL_* variable would accept the write and then keep
// serving the old value on the next resolve — those writes are refused up
// front instead of silently doing nothing.
func (s Source) Locked() bool { return s == SourceFile || s == SourceEnv }

// Resolver merges the configuration layers that sit above the built-in
// defaults. The file and environment layers are read once, when the process
// starts or when Store.Reload re-reads them; the database layer is passed in
// on every Resolve because it changes while the process runs.
//
// The config file is optional and partial: only the keys it actually spells
// out override anything. A key absent from it falls through to the database,
// then to Default(). Nothing ever writes it back — it is an operator's
// override file, not managed state.
type Resolver struct {
	path      string
	fileRaw   []byte
	fileKeys  map[string]bool
	fileFound bool
	env       map[string]string
	// base is the bottom layer, Default() for a real config file. An
	// in-memory Store substitutes the config it was constructed with so that
	// updates merge onto it instead of onto the shipped defaults.
	base Config
}

// staticResolver returns a resolver with no file or environment layer, whose
// bottom layer is cfg. It backs the in-memory Store used by tests.
func staticResolver(cfg Config) *Resolver {
	return &Resolver{fileKeys: map[string]bool{}, env: map[string]string{}, base: cfg}
}

// NewResolver reads the two layers that do not depend on the database: the
// config file at path (absent is normal and means "no file overrides") and the
// AMDL_* variables in environ. Unknown keys in either layer are an error, so a
// typo fails startup instead of quietly doing nothing.
func NewResolver(path string, environ []string) (*Resolver, error) {
	r := &Resolver{path: path, base: Default()}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		keys, err := fileKeys(raw, path)
		if err != nil {
			return nil, err
		}
		r.fileRaw, r.fileKeys, r.fileFound = raw, keys, true
	case errors.Is(err, fs.ErrNotExist):
		r.fileKeys = map[string]bool{}
	default:
		return nil, err
	}
	if r.env, err = envOverrides(environ); err != nil {
		return nil, err
	}
	return r, nil
}

// Path returns the config file the resolver reads, whether or not it exists.
func (r *Resolver) Path() string { return r.path }

// FileFound reports whether a config file was present at the last read.
func (r *Resolver) FileFound() bool { return r.fileFound }

// Reread re-reads the file and environment layers, returning a fresh resolver.
// The receiver is left untouched so a broken hand edit cannot replace a
// working set of layers.
func (r *Resolver) Reread(environ []string) (*Resolver, error) {
	return NewResolver(r.path, environ)
}

// Resolve stacks the layers onto Default() and returns the effective config
// together with the winning layer for every key. settings is the database
// layer: dotted config keys mapped to JSON-encoded values, as stored by
// PUT /api/v1/config. A nil map resolves without it, which is how startup
// reads database.path before the database it names is open.
func (r *Resolver) Resolve(settings map[string]string) (Config, map[string]Source, error) {
	cfg := r.base
	sources := map[string]Source{}
	for _, field := range envFields() {
		sources[field.key] = SourceDefault
	}

	applied, err := applyDatabaseSettings(&cfg, settings)
	if err != nil {
		return cfg, sources, err
	}
	for _, key := range applied {
		sources[key] = SourceDatabase
	}

	if r.fileRaw != nil {
		decoder := yaml.NewDecoder(bytes.NewReader(r.fileRaw))
		decoder.KnownFields(true)
		if err := decoder.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return cfg, sources, fmt.Errorf("%s: %w", r.path, err)
		}
	}
	for key := range r.fileKeys {
		sources[key] = SourceFile
	}

	if err := applyEnvOverrides(&cfg, r.env); err != nil {
		return cfg, sources, err
	}
	for key := range r.env {
		sources[key] = SourceEnv
	}

	clampLimits(&cfg)
	if err := cfg.Validate(); err != nil {
		return cfg, sources, err
	}
	return cfg, sources, nil
}

// fileKeys returns the dotted keys the config file actually sets. Presence is
// what locks a key, so this counts the keys written in the file rather than
// comparing values: an operator who pins a key to the same value the default
// already has still means it to be pinned.
func fileKeys(raw []byte, path string) (map[string]bool, error) {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	known := knownKeys()
	sections := map[string]bool{}
	for key := range known {
		section, _, _ := strings.Cut(key, ".")
		sections[section] = true
	}
	keys := map[string]bool{}
	for _, section := range slices.Sorted(maps.Keys(doc)) {
		if !sections[section] {
			return nil, fmt.Errorf("%s: unknown configuration section %q", path, section)
		}
		// A section header with nothing under it sets no keys; the YAML decode
		// leaves the whole section at its lower-layer values.
		leaves, ok := doc[section].(map[string]any)
		if !ok {
			continue
		}
		for _, leaf := range slices.Sorted(maps.Keys(leaves)) {
			key := section + "." + leaf
			if !known[key] {
				return nil, fmt.Errorf("%s: unknown configuration key %q", path, key)
			}
			keys[key] = true
		}
	}
	return keys, nil
}

// applyDatabaseSettings overlays the stored runtime settings onto cfg and
// returns the keys it applied, sorted. Values are JSON-encoded per key, which
// round-trips lists and keeps an empty string distinguishable from an unset
// key — the database layer stores only keys someone explicitly set, so an
// absent row means "fall through", not "empty".
//
// Rows that name an unknown or startup-bound key are an error rather than a
// skip: only PUT /api/v1/config writes this table and it never produces
// either, so such a row means the database was hand-edited or written by a
// different version, and quietly ignoring it would hide the mismatch.
func applyDatabaseSettings(cfg *Config, settings map[string]string) ([]string, error) {
	if len(settings) == 0 {
		return nil, nil
	}
	cfgValue := reflect.ValueOf(cfg).Elem()
	byKey := map[string]envField{}
	for _, field := range envFields() {
		byKey[field.key] = field
	}
	applied := make([]string, 0, len(settings))
	for _, key := range slices.Sorted(maps.Keys(settings)) {
		field, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("stored setting %q is not a configuration key", key)
		}
		if !isRuntimeKey(key) {
			return nil, fmt.Errorf("stored setting %q is startup-bound and cannot come from the database", key)
		}
		if err := decodeSetting(cfgValue.FieldByIndex(field.index), settings[key]); err != nil {
			return nil, fmt.Errorf("stored setting %q: %w", key, err)
		}
		applied = append(applied, key)
	}
	return applied, nil
}

// decodeSetting writes one JSON-encoded stored value into a config field,
// rejecting anything that is not exactly that field's type.
func decodeSetting(target reflect.Value, raw string) error {
	return json.Unmarshal([]byte(raw), target.Addr().Interface())
}

// encodeSetting renders a config field as the JSON stored in the database.
func encodeSetting(value reflect.Value) (string, error) {
	raw, err := json.Marshal(value.Interface())
	if err != nil {
		return "", err
	}
	return string(raw), nil
}
