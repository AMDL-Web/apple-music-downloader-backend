package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// The two committed, fully commented sample files that live next to the live
// config files and document every key. The live runtime file is
// machine-managed (rewritten by the runtime config API), so the comments'
// home is the example files. startupExampleFileName keeps its pre-split name
// so existing deployments and docs keep pointing at the same file.
const (
	startupExampleFileName = "config.example.yaml"
	runtimeExampleFileName = "runtime.example.yaml"
)

// runtimeFileHeader tops the machine-managed runtime config file, pointing
// readers at the example file since YAML marshaling cannot preserve comments.
const runtimeFileHeader = `# Managed by the amdl backend: rewritten (comments dropped) on every
# PUT /api/v1/config. This file holds only the runtime-mutable keys. Manual
# edits take effect on the next GET /api/v1/config; a PUT issued before that
# overwrites them. Startup-bound keys live in the startup config file
# (config.yaml) and are read only at process start. Key documentation lives
# in ` + runtimeExampleFileName + `.
`

// startupFileHeader tops a startup config file the backend writes itself —
// either the one-time legacy-file migration or a bootstrap from a pre-split
// example. After that write the file is owner-edited only: the backend never
// rewrites it again, so manual comments and formatting are safe.
const startupFileHeader = `# Startup configuration for the amdl backend: every key here is read once at
# process start, so edits take effect on the next restart. The backend never
# rewrites this file. Runtime-mutable keys live in runtime.yaml next to it,
# managed through PUT /api/v1/config. Key documentation lives in
# ` + startupExampleFileName + `.
`

// legacyBackupSuffix names the pre-migration copy of a combined config.yaml
// that EnsureFiles keeps next to the rewritten file.
const legacyBackupSuffix = ".pre-split.bak"

// BootstrapResult reports what EnsureFiles had to create or migrate, so main
// can log it.
type BootstrapResult struct {
	// CreatedStartup: the startup config file was created from the example.
	CreatedStartup bool
	// CreatedRuntime: the runtime config file was created from an example (or
	// built-in defaults when no example is available).
	CreatedRuntime bool
	// MigratedLegacy: a pre-split config.yaml carrying runtime keys was split
	// in place — runtime keys moved to the runtime file, the startup file
	// rewritten with only startup keys.
	MigratedLegacy bool
	// LegacyBackupPath: where the pre-migration combined file was copied,
	// empty unless MigratedLegacy.
	LegacyBackupPath string
}

// EnsureFiles makes sure both halves of the split configuration exist at
// startupPath and runtimePath, creating them from the example files on first
// start and migrating a legacy combined config.yaml (which holds runtime keys)
// by splitting it in place. Values are taken from the files only — AMDL_*
// environment overrides are deliberately not baked in: they overlay every
// load instead, so unsetting a variable restores the file value.
func EnsureFiles(startupPath, runtimePath string) (BootstrapResult, error) {
	var result BootstrapResult
	startupExists, err := fileExists(startupPath)
	if err != nil {
		return result, err
	}
	if !startupExists {
		if err := bootstrapStartup(startupPath); err != nil {
			return result, err
		}
		result.CreatedStartup = true
	}
	runtimeExists, err := fileExists(runtimePath)
	if err != nil {
		return result, err
	}
	if runtimeExists {
		return result, nil
	}
	// The startup file exists but the runtime file does not. A startup file
	// still carrying runtime keys is a pre-split config.yaml: split it. One
	// without runtime keys (fresh bootstrap above, or a hand-written minimal
	// config) just needs the runtime file created from the example defaults.
	raw, err := os.ReadFile(startupPath)
	if err != nil {
		return result, err
	}
	if !result.CreatedStartup && hasRuntimeKeys(raw) {
		backupPath, err := migrateLegacy(startupPath, runtimePath, raw)
		if err != nil {
			return result, err
		}
		result.MigratedLegacy = true
		result.LegacyBackupPath = backupPath
		return result, nil
	}
	if err := bootstrapRuntime(startupPath, runtimePath); err != nil {
		return result, err
	}
	result.CreatedRuntime = true
	return result, nil
}

// bootstrapStartup creates the startup config file from the sibling
// config.example.yaml. A pre-split (startup-only) example is copied verbatim
// so its comments carry over into the now owner-edited live file; a legacy
// combined example has its startup subset extracted instead. A missing
// example next to a missing config is an error the caller surfaces (nothing
// to start from).
func bootstrapStartup(startupPath string) error {
	examplePath := filepath.Join(filepath.Dir(startupPath), startupExampleFileName)
	raw, err := os.ReadFile(examplePath)
	if err != nil {
		return fmt.Errorf("config file %s does not exist and bootstrapping from %s failed: %w", startupPath, examplePath, err)
	}
	// Validate the example's values before writing anything (lenient about
	// which file keys live in — the example may still be a combined one).
	cfg, err := load(examplePath, nil)
	if err != nil {
		return fmt.Errorf("config file %s does not exist and bootstrapping from %s failed: %w", startupPath, examplePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(startupPath), 0o755); err != nil {
		return err
	}
	if checkFileKeys(raw, examplePath, roleStartup) == nil {
		// Startup-only example: keep its comments in the live file verbatim.
		return writeFileAtomic(startupPath, raw, 0o644)
	}
	out, err := filterConfigYAML(cfg, isStartupKey)
	if err != nil {
		return err
	}
	return writeFileAtomic(startupPath, append([]byte(startupFileHeader), out...), 0o644)
}

// bootstrapRuntime creates the runtime config file. Values come from the
// first available source: runtime.example.yaml, the runtime subset of a
// legacy combined config.example.yaml (deployments upgraded in place have no
// runtime example), or the built-in defaults.
func bootstrapRuntime(startupPath, runtimePath string) error {
	dir := filepath.Dir(startupPath)
	cfg := Default()
	if runtimeExamplePath := filepath.Join(dir, runtimeExampleFileName); fileExistsNoErr(runtimeExamplePath) {
		raw, err := os.ReadFile(runtimeExamplePath)
		if err != nil {
			return err
		}
		if err := checkFileKeys(raw, runtimeExamplePath, roleRuntime); err != nil {
			return err
		}
		if cfg, err = load(runtimeExamplePath, nil); err != nil {
			return fmt.Errorf("bootstrapping %s from %s failed: %w", runtimePath, runtimeExamplePath, err)
		}
	} else if startupExamplePath := filepath.Join(dir, startupExampleFileName); fileExistsNoErr(startupExamplePath) {
		var err error
		if cfg, err = load(startupExamplePath, nil); err != nil {
			return fmt.Errorf("bootstrapping %s from %s failed: %w", runtimePath, startupExamplePath, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		return err
	}
	return saveRuntime(runtimePath, cfg)
}

// migrateLegacy splits a pre-split combined config file in place: the
// runtime-managed keys move to a new runtime file and the startup file is
// rewritten with only its startup keys, after copying the original next to
// it as a backup. The runtime file is written first: if the process dies
// between the two writes, the next start fails the strict startup-file key
// check loudly (runtime keys still present alongside an existing runtime
// file) instead of silently rebuilding runtime values from examples.
func migrateLegacy(startupPath, runtimePath string, raw []byte) (string, error) {
	cfg, err := load(startupPath, nil)
	if err != nil {
		return "", fmt.Errorf("migrating legacy config %s: %w", startupPath, err)
	}
	backupPath := startupPath + legacyBackupSuffix
	if exists, err := fileExists(backupPath); err != nil {
		return "", err
	} else if !exists {
		// The combined file may hold catalog.media_user_token — keep the backup
		// owner-only like every runtime-file write.
		if err := writeFileAtomic(backupPath, raw, 0o600); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(filepath.Dir(runtimePath), 0o755); err != nil {
		return "", err
	}
	if err := saveRuntime(runtimePath, cfg); err != nil {
		return "", err
	}
	startupOut, err := filterConfigYAML(cfg, isStartupKey)
	if err != nil {
		return "", err
	}
	if err := writeFileAtomic(startupPath, append([]byte(startupFileHeader), startupOut...), 0o644); err != nil {
		return "", err
	}
	return backupPath, nil
}

// SaveRuntime writes the runtime-managed subset of cfg to the runtime config
// file at path. Startup-bound fields are never written: their file of record
// is the owner-edited startup config.
func SaveRuntime(path string, cfg Config) error {
	return saveRuntime(path, cfg)
}

func saveRuntime(path string, cfg Config) error {
	out, err := filterConfigYAML(cfg, isRuntimeKey)
	if err != nil {
		return err
	}
	// The runtime file may contain catalog.media_user_token: owner-only.
	return writeFileAtomic(path, append([]byte(runtimeFileHeader), out...), 0o600)
}

// writeFileAtomic writes data to path via a temp-file rename, so a crash
// mid-write never leaves a truncated file behind. A random, exclusive temp
// file in the same directory avoids a fixed path+".tmp" that could be
// pre-planted as a symlink and make the write truncate an unrelated file
// before the final rename.
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

func fileExistsNoErr(path string) bool {
	exists, err := fileExists(path)
	return err == nil && exists
}
