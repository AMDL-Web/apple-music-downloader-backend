package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// configExampleFileName is the tracked template that ships with the backend.
// It is the field documentation, and the live config file starts life as a
// verbatim copy of it.
const configExampleFileName = "config.example.yaml"

// EnsureFile seeds the config file by copying the example next to it, and
// reports whether it created one. Copying verbatim is safe because the example
// leaves every runtime-mutable key commented out: a fresh config.yaml
// therefore sets only startup-bound keys and pins nothing the API owns.
//
// Both files are optional. A missing example is not an error — the config file
// is an override layer, and an install without one runs on stored settings and
// defaults alone — so this quietly does nothing rather than refusing to start.
// The copy is owner-only: the live file is where an operator would put a
// media-user-token, and it is edited by hand from here on.
func EnsureFile(configPath string) (bool, error) {
	exists, err := fileExists(configPath)
	if err != nil || exists {
		return false, err
	}
	examplePath := filepath.Join(filepath.Dir(configPath), configExampleFileName)
	raw, err := os.ReadFile(examplePath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	// Parse the example before planting a copy of it, so a broken template
	// fails here naming itself instead of leaving behind a config.yaml the
	// operator has to work out was never theirs.
	if _, err := NewResolver(examplePath, nil); err != nil {
		return false, err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return false, err
	}
	return true, writeFileAtomic(configPath, raw, 0o600)
}

// writeFileAtomic writes data via a random temporary file and rename, so a
// crash cannot leave a truncated config and a pre-planted fixed-name symlink
// cannot redirect the write.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := file.Name()
	defer os.Remove(tmp)
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func fileExists(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return true, nil
	} else if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	} else {
		return false, err
	}
}
