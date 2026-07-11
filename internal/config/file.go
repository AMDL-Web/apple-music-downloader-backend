package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// exampleFileName is the committed, fully commented sample config that lives
// next to the live config file and documents every key. The live file is
// machine-managed (rewritten by the runtime config API), so the comments'
// home is the example file.
const exampleFileName = "config.example.yaml"

// savedFileHeader tops every config file written by Save, pointing readers at
// the example file since YAML marshaling cannot preserve comments.
const savedFileHeader = `# Managed by the amdl backend: rewritten (comments dropped) on every
# PUT /api/v1/config. Manual edits to runtime-mutable fields (download,
# simulate, catalog.album_track_url_mode) take effect on the next
# GET /api/v1/config; edits to startup-bound fields require a restart. A PUT
# issued before either of those overwrites manual edits. Key documentation
# lives in ` + exampleFileName + `.
`

// BootstrapFromExample creates the live config file at path from the sibling
// config.example.yaml, so a fresh checkout starts with the documented
// defaults. The values are loaded from the example and written back in the
// same machine-managed format every later Save produces — the example's
// comments are not carried over, since the first PUT /api/v1/config would
// drop them anyway. It reports whether it created the file; an existing file
// is left untouched, and a missing example next to a missing config is an
// error the caller surfaces (nothing to start from).
func BootstrapFromExample(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	examplePath := filepath.Join(filepath.Dir(path), exampleFileName)
	cfg, err := Load(examplePath)
	if err != nil {
		return false, fmt.Errorf("config file %s does not exist and bootstrapping from %s failed: %w", path, examplePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := Save(path, cfg); err != nil {
		return false, err
	}
	return true, nil
}

// Save writes cfg to path as YAML via a temp-file rename, so a crash
// mid-write never leaves a truncated config behind.
func Save(path string, cfg Config) error {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(savedFileHeader), raw...), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
