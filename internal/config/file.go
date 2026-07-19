package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

const configExampleFileName = "config.example.yaml"

// managedFileHeader tops config.yaml after the runtime config API rewrites it.
// Detailed field documentation remains in config.example.yaml because YAML
// marshaling does not preserve comments.
const managedFileHeader = `# Managed by the amdl backend: PUT /api/v1/config rewrites this file and
# does not preserve comments. Runtime-mutable fields take effect immediately;
# startup-bound fields are read only when the process starts. Complete field
# documentation lives in config.example.yaml.
`

// legacyRuntimeBackupSuffix names the consumed runtime.yaml left behind when
// upgrading a deployment from the former split-file format.
const legacyRuntimeBackupSuffix = ".pre-merge.bak"

// BootstrapResult reports whether startup created config.yaml or merged a
// legacy runtime.yaml into it.
type BootstrapResult struct {
	CreatedConfig     bool
	MergedRuntime     bool
	RuntimeBackupPath string
}

// decodeLegacyRuntimeFile overlays the runtime-only file used by the former
// two-file format. It exists solely for the one-time upgrade into config.yaml.
func decodeLegacyRuntimeFile(cfg *Config, path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := checkLegacyRuntimeKeys(raw, path); err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func checkLegacyRuntimeKeys(raw []byte, path string) error {
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	sections := map[string]bool{}
	for key := range knownKeys() {
		section, _, _ := strings.Cut(key, ".")
		sections[section] = true
	}
	known := knownKeys()
	for _, section := range slices.Sorted(maps.Keys(doc)) {
		if !sections[section] {
			return fmt.Errorf("%s: unknown configuration section %q", path, section)
		}
		leaves, ok := doc[section].(map[string]any)
		if !ok {
			continue
		}
		for _, leaf := range slices.Sorted(maps.Keys(leaves)) {
			key := section + "." + leaf
			if !known[key] {
				return fmt.Errorf("%s: unknown configuration key %q", path, key)
			}
			if !isRuntimeKey(key) {
				return fmt.Errorf("%s: %s is startup-bound and cannot appear in the legacy runtime config", path, key)
			}
		}
	}
	return nil
}

// EnsureFile makes sure config.yaml exists and folds a legacy runtime.yaml
// into it exactly once. Values are read without AMDL_* overlays so environment
// settings are never baked into a file merely by starting the process.
//
// The legacy file is renamed only after the merged config has been written
// atomically. If the process stops between those operations, retrying the same
// merge is harmless because the legacy runtime values are identical.
func EnsureFile(configPath, legacyRuntimePath string) (BootstrapResult, error) {
	var result BootstrapResult
	exists, err := fileExists(configPath)
	if err != nil {
		return result, err
	}
	if !exists {
		if err := bootstrapConfig(configPath); err != nil {
			return result, err
		}
		result.CreatedConfig = true
	}

	if legacyRuntimePath == "" || filepath.Clean(legacyRuntimePath) == filepath.Clean(configPath) {
		return result, nil
	}
	runtimeExists, err := fileExists(legacyRuntimePath)
	if err != nil {
		return result, err
	}
	if !runtimeExists {
		return result, nil
	}

	cfg, err := load(configPath, nil)
	if err != nil {
		return result, fmt.Errorf("load config before merging legacy runtime file: %w", err)
	}
	if err := decodeLegacyRuntimeFile(&cfg, legacyRuntimePath); err != nil {
		return result, fmt.Errorf("merge legacy runtime config %s: %w", legacyRuntimePath, err)
	}
	if err := cfg.NormalizeDeprecated(); err != nil {
		return result, err
	}
	clampCatalogLimits(&cfg.Catalog)
	clampDownloadLimits(&cfg.Download)
	if err := cfg.Validate(); err != nil {
		return result, err
	}
	if err := saveConfig(configPath, cfg); err != nil {
		return result, err
	}

	backupPath, err := availableBackupPath(legacyRuntimePath + legacyRuntimeBackupSuffix)
	if err != nil {
		return result, err
	}
	if err := os.Rename(legacyRuntimePath, backupPath); err != nil {
		return result, fmt.Errorf("archive merged legacy runtime config: %w", err)
	}
	result.MergedRuntime = true
	result.RuntimeBackupPath = backupPath
	return result, nil
}

func bootstrapConfig(configPath string) error {
	examplePath := filepath.Join(filepath.Dir(configPath), configExampleFileName)
	raw, err := os.ReadFile(examplePath)
	if err != nil {
		return fmt.Errorf("config file %s does not exist and bootstrapping from %s failed: %w", configPath, examplePath, err)
	}
	if _, err := load(examplePath, nil); err != nil {
		return fmt.Errorf("config file %s does not exist and bootstrapping from %s failed: %w", configPath, examplePath, err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(configPath, raw, 0o600)
}

// Save writes the complete configuration to path. The live file may contain a
// media-user-token, so every generated replacement is owner-only.
func Save(path string, cfg Config) error {
	return saveConfig(path, cfg)
}

func saveConfig(path string, cfg Config) error {
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(path, append([]byte(managedFileHeader), out...), 0o600)
}

// saveRuntimeFields rewrites the combined file with cfg's runtime values while
// retaining startup-bound values currently on disk. This prevents an API PUT
// from undoing a valid config.yaml edit that is waiting for the next restart.
func saveRuntimeFields(path string, cfg Config) error {
	disk := cfg
	if exists, err := fileExists(path); err != nil {
		return err
	} else if exists {
		loaded, err := load(path, nil)
		if err != nil {
			return fmt.Errorf("reload startup-bound config fields before save: %w", err)
		}
		disk = loaded
		copyRuntimeFields(&disk, cfg)
	}
	return saveConfig(path, disk)
}

func availableBackupPath(base string) (string, error) {
	for i := 0; ; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s.%d", base, i)
		}
		exists, err := fileExists(candidate)
		if err != nil {
			return "", err
		}
		if !exists {
			return candidate, nil
		}
	}
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
