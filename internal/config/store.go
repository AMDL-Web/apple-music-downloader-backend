package config

import (
	"context"
	"fmt"
	"maps"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
)

// SettingsStore persists the database configuration layer: the keys someone
// changed through PUT /api/v1/config, each as a JSON-encoded value. Keys that
// were never set have no row, which is what makes them fall through to the
// config file and then to the built-in defaults.
type SettingsStore interface {
	ConfigSettings(ctx context.Context) (map[string]string, error)
	// ApplyConfigSettings writes one batch atomically: a non-nil value stores
	// the key, a nil value deletes its row.
	ApplyConfigSettings(ctx context.Context, changes map[string]*string) error
}

// snapshot is one consistent view of the merged configuration: the effective
// values, where each key came from, and the database layer they were built on.
type snapshot struct {
	cfg      Config
	sources  map[string]Source
	settings map[string]string
}

// Store holds the live configuration shared by every component that must
// observe updates made through the API after startup. Get returns the current
// snapshot by value; callers must treat slices inside it as read-only, since
// mutating one in place would race with concurrent readers of the same
// snapshot.
//
// A Store built with NewLayeredStore resolves defaults, database, config file,
// and environment on every change. NewStore creates an in-memory Store for
// tests: the config it is given is the bottom layer and updates are applied on
// top of it, but nothing is persisted and no files are read.
type Store struct {
	current  atomic.Pointer[snapshot]
	settings SettingsStore
	// resolver and persistent are guarded by saveMu, which serializes Update
	// against Reload: without it, a reload that read the file just before a
	// concurrent update wrote the database could swap the pre-update values
	// back in and silently drop that update.
	resolver   *Resolver
	persistent bool
	saveMu     sync.Mutex
}

// NewStore returns an in-memory Store whose bottom layer is cfg. Updates merge
// onto it and are visible to Get, but nothing is written and Reload is a no-op.
func NewStore(cfg Config) *Store {
	s := &Store{resolver: staticResolver(cfg), settings: &memorySettings{}}
	s.current.Store(&snapshot{cfg: cfg, sources: defaultSources(), settings: map[string]string{}})
	return s
}

// NewLayeredStore resolves the full layer stack and returns the live Store.
// The settings backend supplies the database layer, the resolver the file and
// environment layers above it.
func NewLayeredStore(ctx context.Context, resolver *Resolver, settings SettingsStore) (*Store, error) {
	stored, err := settings.ConfigSettings(ctx)
	if err != nil {
		return nil, err
	}
	cfg, sources, err := resolver.Resolve(stored)
	if err != nil {
		return nil, err
	}
	s := &Store{resolver: resolver, settings: settings, persistent: true}
	s.current.Store(&snapshot{cfg: cfg, sources: sources, settings: stored})
	return s, nil
}

// Persistent reports whether updates are written to the database and therefore
// survive a restart.
func (s *Store) Persistent() bool { return s.persistent }

func (s *Store) Get() Config { return s.current.Load().cfg }

// Sources returns the layer that supplied each key's effective value.
func (s *Store) Sources() map[string]Source {
	return maps.Clone(s.current.Load().sources)
}

// Set replaces the live snapshot without persisting anything. It exists for
// tests and for components that build a Store around a config they already
// hold; the resolved layer sources are left as they were.
func (s *Store) Set(cfg Config) {
	current := s.current.Load()
	s.current.Store(&snapshot{cfg: cfg, sources: current.sources, settings: current.settings})
}

// Update applies patch to the database layer and swaps in the re-resolved
// configuration. The whole read-modify-write is serialized, so two concurrent
// updates cannot both derive from the same stale snapshot and silently drop
// each other's changes.
//
// A patch is refused as a whole, never partially: startup-bound keys, keys
// pinned by the config file or an AMDL_* variable, and a merged result that
// fails validation all come back as *RejectedError with nothing written. Any
// other error is a persistence failure and likewise leaves the running config
// untouched.
func (s *Store) Update(ctx context.Context, patch Patch) (Config, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	current := s.current.Load()

	var startupBound []string
	for _, key := range patch.Keys() {
		if !isRuntimeKey(key) {
			startupBound = append(startupBound, key)
		}
	}
	if len(startupBound) > 0 {
		return Config{}, &RejectedError{Reason: RejectStartupBound, Keys: startupBound}
	}

	// A key held by a higher layer would accept the write and then keep
	// serving the old value, so refuse it. Only a real change is refused: a
	// client that echoes the whole current config back, or resets a shadowed
	// database row, is asking for nothing it will not get.
	var locked []string
	lockedBy := map[string]Source{}
	for _, key := range patch.Keys() {
		source := current.sources[key]
		if !source.Locked() || patch[key] == nil {
			continue
		}
		effective, err := fieldValueJSON(current.cfg, key)
		if err != nil {
			return Config{}, err
		}
		if *patch[key] != effective {
			locked = append(locked, key)
			lockedBy[key] = source
		}
	}
	if len(locked) > 0 {
		return Config{}, &RejectedError{Reason: RejectLocked, Keys: locked, Sources: lockedBy}
	}

	next := maps.Clone(current.settings)
	if next == nil {
		next = map[string]string{}
	}
	for key, value := range patch {
		if value == nil {
			delete(next, key)
			continue
		}
		next[key] = *value
	}
	cfg, sources, err := s.resolver.Resolve(next)
	if err != nil {
		return Config{}, &RejectedError{Reason: RejectInvalid, Err: err}
	}
	if err := s.settings.ApplyConfigSettings(ctx, patch); err != nil {
		return Config{}, err
	}
	s.current.Store(&snapshot{cfg: cfg, sources: sources, settings: next})
	return cfg, nil
}

// Reload re-reads every layer — config file, environment, and database — and
// applies the runtime-mutable fields to the live snapshot. Startup-bound
// fields keep their in-memory values because the components built from them
// cannot follow a live change; a config-file edit to one of those applies on
// the next restart, and the reported sources describe the file as it now is,
// not the values still in force.
//
// On a read or validation error the snapshot is left unchanged so callers can
// keep serving the last good config. No-op for in-memory stores.
func (s *Store) Reload(ctx context.Context) error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if !s.persistent {
		return nil
	}
	resolver, err := s.resolver.Reread(os.Environ())
	if err != nil {
		return err
	}
	stored, err := s.settings.ConfigSettings(ctx)
	if err != nil {
		return err
	}
	resolved, sources, err := resolver.Resolve(stored)
	if err != nil {
		return err
	}
	cfg := s.current.Load().cfg
	copyRuntimeFields(&cfg, resolved)
	s.resolver = resolver
	s.current.Store(&snapshot{cfg: cfg, sources: sources, settings: stored})
	return nil
}

// fieldValueJSON encodes one key's current value the same way the database
// layer stores it, so a requested value and the effective one compare exactly.
func fieldValueJSON(cfg Config, key string) (string, error) {
	for _, field := range envFields() {
		if field.key == key {
			return encodeSetting(reflect.ValueOf(cfg).FieldByIndex(field.index))
		}
	}
	return "", fmt.Errorf("unknown configuration key %q", key)
}

func defaultSources() map[string]Source {
	sources := map[string]Source{}
	for _, field := range envFields() {
		sources[field.key] = SourceDefault
	}
	return sources
}

// memorySettings is the SettingsStore of an in-memory Store: it keeps the
// database layer for the process lifetime without a database behind it.
type memorySettings struct {
	mu     sync.Mutex
	values map[string]string
}

func (m *memorySettings) ConfigSettings(context.Context) (map[string]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.values), nil
}

func (m *memorySettings) ApplyConfigSettings(_ context.Context, changes map[string]*string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.values == nil {
		m.values = map[string]string{}
	}
	for key, value := range changes {
		if value == nil {
			delete(m.values, key)
			continue
		}
		m.values[key] = *value
	}
	return nil
}
