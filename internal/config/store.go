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
// A Store created with NewFileStore is backed by the live runtime config
// file: SetAndSave writes the runtime-managed subset of the new snapshot back
// to it, so runtime updates survive a restart. Startup-bound fields are never
// written anywhere — their file of record is the owner-edited startup config.
// NewStore creates an in-memory Store for tests.
type Store struct {
	current atomic.Pointer[Config]
	// path is the backing runtime config file; empty for in-memory stores.
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

// Persistent reports whether updates to this store are written back to a
// runtime config file (and therefore survive a restart).
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
		if err := saveRuntime(s.path, updated); err != nil {
			return Config{}, err
		}
	}
	s.Set(updated)
	return updated, nil
}

// Reload re-reads the backing runtime file into the snapshot, picking up
// manual edits made while the backend is running. Startup-bound fields keep
// their in-memory values by construction — the runtime file cannot contain
// them (strict key check), and the components built from them (listener,
// wrapper connection, catalog client, tool checker, worker pool) cannot
// follow a live change anyway; startup-file edits apply on the next restart,
// as documented. A runtime key deleted from the file reads as its built-in
// default, matching what a fresh load would produce, and AMDL_* environment
// overrides are re-applied so overridden values stay pinned. On a read or
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
	cfg := s.Get()
	resetRuntimeToDefaults(&cfg)
	if err := decodeFileStrict(&cfg, s.path, roleRuntime); err != nil {
		return err
	}
	if err := applyEnvOverrides(&cfg, os.Environ()); err != nil {
		return err
	}
	if err := cfg.NormalizeDeprecated(); err != nil {
		return err
	}
	clampDownloadLimits(&cfg.Download)
	if err := cfg.Validate(); err != nil {
		return err
	}
	s.Set(cfg)
	return nil
}
