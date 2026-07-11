package config

import "sync/atomic"

// Store holds the live runtime configuration shared by every component that
// must observe config updates made through the API after startup. Set replaces
// the whole snapshot atomically; Get returns the current snapshot by value.
// Callers must treat slices inside a returned Config as read-only — mutating
// them in place would race with concurrent readers of the same snapshot.
//
// Runtime updates live only in this store: they are never written back to
// configs/config.yaml, so the file's values apply again after a restart.
type Store struct {
	current atomic.Pointer[Config]
}

func NewStore(cfg Config) *Store {
	s := &Store{}
	s.Set(cfg)
	return s
}

func (s *Store) Get() Config {
	return *s.current.Load()
}

func (s *Store) Set(cfg Config) {
	s.current.Store(&cfg)
}
