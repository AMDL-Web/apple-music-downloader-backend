package media

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// outputLockTable serializes writers targeting the same canonical output
// path. Entries are reference counted (holders plus waiters) and removed once
// unused, so user-controlled output names cannot grow the table forever.
type outputLockTable struct {
	mu      sync.Mutex
	entries map[string]*outputLockEntry
}

type outputLockEntry struct {
	mu   sync.Mutex
	refs int
}

var processOutputLocks outputLockTable

func (t *outputLockTable) acquire(path string) (func(), error) {
	key, err := canonicalOutputPath(path)
	if err != nil {
		return nil, fmt.Errorf("canonicalize output path: %w", err)
	}

	t.mu.Lock()
	if t.entries == nil {
		t.entries = make(map[string]*outputLockEntry)
	}
	entry := t.entries[key]
	if entry == nil {
		entry = &outputLockEntry{}
		t.entries[key] = entry
	}
	entry.refs++
	t.mu.Unlock()

	entry.mu.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			entry.mu.Unlock()
			t.mu.Lock()
			entry.refs--
			if entry.refs == 0 && t.entries[key] == entry {
				delete(t.entries, key)
			}
			t.mu.Unlock()
		})
	}, nil
}

// canonicalOutputPath resolves existing symlinks while still supporting an
// output file (and parent directories) that have not been created yet.
func canonicalOutputPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	// Resolve directory aliases, but deliberately do not follow a symlink at
	// the final filename. Finalization replaces that directory entry; following
	// it would make the lock key change as soon as force cleanup removed the
	// symlink, allowing another writer to bypass the existing waiter queue.
	name := filepath.Base(abs)
	current := filepath.Dir(abs)
	var missing []string
	for {
		resolved, evalErr := filepath.EvalSymlinks(current)
		if evalErr == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Join(filepath.Clean(resolved), name), nil
		}
		if !os.IsNotExist(evalErr) {
			return "", evalErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", evalErr
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
