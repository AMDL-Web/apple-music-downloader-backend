package config

import (
	"os"
	"sync"
	"sync/atomic"
)

// Store holds the live runtime configuration shared by every component that
// must observe config updates made through the API after startup. Set replaces
// the whole snapshot atomically; Get returns the current snapshot by value.
// Callers must treat slices inside a returned Config as read-only — mutating
// them in place would race with concurrent readers of the same snapshot.
//
// A Store created with NewFileStore is backed by the combined config file.
// SetAndSave updates its runtime-managed fields so changes survive a restart.
// NewStore creates an in-memory Store for tests.
type Store struct {
	current atomic.Pointer[Config]
	// path is the backing combined config file; empty for in-memory stores.
	// saveMu serializes SetAndSave so concurrent updates cannot interleave the
	// file write and the snapshot swap.
	path   string
	saveMu sync.Mutex
}

func NewStore(cfg Config) *Store {
	s := &Store{}
	s.Set(cfg)
	return s
}

func NewFileStore(cfg Config, path string) *Store {
	s := NewStore(cfg)
	s.path = path
	return s
}

// Persistent reports whether updates are written back to config.yaml and
// therefore survive a restart.
func (s *Store) Persistent() bool {
	return s.path != ""
}

func (s *Store) Get() Config {
	return *s.current.Load()
}

func (s *Store) Set(cfg Config) {
	s.current.Store(&cfg)
}

// SetAndSave persists cfg to the backing file first and only then swaps the
// in-memory snapshot, so a failed write leaves the running config unchanged.
// For in-memory stores it is equivalent to Set.
func (s *Store) SetAndSave(cfg Config) error {
	_, err := s.UpdateAndSave(func(Config) (Config, error) { return cfg, nil })
	return err
}

// UpdateAndSave atomically applies mutate to the current snapshot, persists
// the result to the backing file, and swaps it in, returning the new config.
// saveMu is held across the whole read-modify-write, so two concurrent
// merge-style updates can never both derive from the same stale snapshot and
// silently drop each other's changes. A mutate error or a failed write
// leaves the running config unchanged.
func (s *Store) UpdateAndSave(mutate func(current Config) (Config, error)) (Config, error) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	updated, err := mutate(s.Get())
	if err != nil {
		return Config{}, err
	}
	if s.path != "" {
		if err := saveRuntimeFields(s.path, updated); err != nil {
			return Config{}, err
		}
	}
	s.Set(updated)
	return updated, nil
}

// Reload re-reads the combined config file, applying only runtime-mutable
// fields. Startup-bound fields keep their in-memory values because components
// built from them cannot follow a live change; those edits apply on restart.
// A deleted runtime key resets to its built-in default through Load, and
// AMDL_* environment overrides are re-applied. On a read or
// validation error the snapshot is left unchanged so callers can keep
// serving the last good config. No-op for in-memory stores. It shares
// saveMu with UpdateAndSave: without it, a reload that read the file just
// before a concurrent update wrote it could swap the pre-update values back
// in and silently drop that update.
func (s *Store) Reload() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.path == "" {
		return nil
	}
	loaded, err := load(s.path, os.Environ())
	if err != nil {
		return err
	}
	cfg := s.Get()
	copyRuntimeFields(&cfg, loaded)
	s.Set(cfg)
	return nil
}
