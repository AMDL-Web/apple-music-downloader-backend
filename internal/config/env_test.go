package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestEnvOverrides(t *testing.T) {
	path := writeConfig(t, "wrapper:\n  address: \"10.0.0.1:8080\"\n")
	cfg, err := load(path, []string{
		"AMDL_SERVER_LISTEN=:19090",
		"AMDL_WRAPPER_ADDRESS=wrapper-manager:8080",
		"AMDL_DATABASE_PATH=/srv/amdl/amdl.db",
		"AMDL_LOGGING_LEVEL=debug",
		"AMDL_SIMULATE_ENABLED=true",
		"AMDL_DOWNLOAD_MAX_ATTEMPTS=7",
		"AMDL_DOWNLOAD_QUALITY_PRIORITY=aac, alac,",
		"AMDL_CATALOG_ALLOWED_ORIGINS=",
		"AMDL_CATALOG_MEDIA_USER_TOKEN_PRIORITY=request",
		"UNRELATED=1",
	})
	if err != nil {
		t.Fatalf("load with env overrides: %v", err)
	}
	if cfg.Server.Listen != ":19090" {
		t.Fatalf("listen = %q, want env override", cfg.Server.Listen)
	}
	// The environment wins over an explicit file value.
	if cfg.Wrapper.Address != "wrapper-manager:8080" {
		t.Fatalf("wrapper address = %q, want env override over file value", cfg.Wrapper.Address)
	}
	if cfg.Database.Path != "/srv/amdl/amdl.db" {
		t.Fatalf("database path = %q, want env override", cfg.Database.Path)
	}
	if cfg.Logging.Level != "debug" {
		t.Fatalf("logging level = %q, want debug", cfg.Logging.Level)
	}
	if !cfg.Simulate.Enabled {
		t.Fatal("simulate.enabled not overridden")
	}
	if cfg.Download.MaxAttempts != 7 {
		t.Fatalf("max attempts = %d, want 7", cfg.Download.MaxAttempts)
	}
	if !reflect.DeepEqual(cfg.Download.QualityPriority, []string{"aac", "alac"}) {
		t.Fatalf("quality priority = %#v, want trimmed comma-separated items", cfg.Download.QualityPriority)
	}
	// An empty value overrides a list to empty.
	if !reflect.DeepEqual(cfg.Catalog.AllowedOrigins, []string{}) {
		t.Fatalf("allowed origins = %#v, want empty list", cfg.Catalog.AllowedOrigins)
	}
	if cfg.Catalog.LegacyMediaUserTokenPriority != "" {
		t.Fatalf("legacy media-user-token priority was not normalized: %q", cfg.Catalog.LegacyMediaUserTokenPriority)
	}
}

func TestEnvOverridesIgnoreNonConfigVariables(t *testing.T) {
	path := writeConfig(t, "server:\n  listen: \":18080\"\n")
	if _, err := load(path, []string{
		"AMDL_CONFIG=/etc/amdl/config.yaml",
		"AMDL_HOOKS_CONFIG=/etc/amdl/hooks.yaml",
	}); err != nil {
		t.Fatalf("ignored variables must not fail load: %v", err)
	}
}

func TestEnvOverridesRejectUnknownVariables(t *testing.T) {
	path := writeConfig(t, "server:\n  listen: \":18080\"\n")
	_, err := load(path, []string{"AMDL_DOWNLOADS_DIR=/music", "AMDL_TYPO=1"})
	if err == nil || !strings.Contains(err.Error(), "unknown configuration environment variable") {
		t.Fatalf("load error = %v, want unknown variable error", err)
	}
	if !strings.Contains(err.Error(), "AMDL_DOWNLOADS_DIR") || !strings.Contains(err.Error(), "AMDL_TYPO") {
		t.Fatalf("error %v must name every unknown variable", err)
	}
	// The pre-override container variables are gone, not silently ignored:
	// a deployment still setting them must fail loudly and switch to
	// AMDL_SERVER_LISTEN / AMDL_WRAPPER_ADDRESS.
	for _, legacy := range []string{"AMDL_LISTEN=:18080", "AMDL_WRAPPER_ADDR=host:8080"} {
		name, _, _ := strings.Cut(legacy, "=")
		if _, err := load(path, []string{legacy}); err == nil || !strings.Contains(err.Error(), name) {
			t.Fatalf("load with %s error = %v, want unknown variable error naming it", name, err)
		}
	}
}

func TestEnvOverridesRejectInvalidValues(t *testing.T) {
	path := writeConfig(t, "server:\n  listen: \":18080\"\n")
	if _, err := load(path, []string{"AMDL_SIMULATE_ENABLED=maybe"}); err == nil ||
		!strings.Contains(err.Error(), "AMDL_SIMULATE_ENABLED") || !strings.Contains(err.Error(), "boolean") {
		t.Fatalf("load error = %v, want boolean parse error naming the variable", err)
	}
	if _, err := load(path, []string{"AMDL_DOWNLOAD_MAX_ATTEMPTS=lots"}); err == nil ||
		!strings.Contains(err.Error(), "AMDL_DOWNLOAD_MAX_ATTEMPTS") || !strings.Contains(err.Error(), "integer") {
		t.Fatalf("load error = %v, want integer parse error naming the variable", err)
	}
}

func TestEnvOverridesGoThroughValidation(t *testing.T) {
	path := writeConfig(t, "server:\n  listen: \":18080\"\n")
	if _, err := load(path, []string{"AMDL_DOWNLOAD_COVER_FORMAT=webp"}); err == nil ||
		!strings.Contains(err.Error(), "cover_format") {
		t.Fatalf("load error = %v, want cover_format validation error", err)
	}
}

func TestLoadAppliesProcessEnvironment(t *testing.T) {
	t.Setenv("AMDL_DOWNLOAD_DOWNLOADS_DIR", "/music/from-env")
	path := writeConfig(t, "download:\n  downloads_dir: \"data/downloads\"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Download.DownloadsDir != "/music/from-env" {
		t.Fatalf("downloads dir = %q, want process env override", cfg.Download.DownloadsDir)
	}
}

// Every leaf of Config must map to a unique variable name and a kind
// setFromEnv understands, so adding a field of an unsupported type (or a
// colliding yaml tag) fails here instead of at a user's startup.
func TestEnvFieldsCoverConfig(t *testing.T) {
	fields := envFields()
	cfgValue := reflect.ValueOf(Config{})
	seen := map[string]string{}
	for _, field := range fields {
		if previous, dup := seen[field.name]; dup {
			t.Errorf("variable %s maps to both %s and %s", field.name, previous, field.key)
		}
		seen[field.name] = field.key
		target := cfgValue.FieldByIndex(field.index)
		switch target.Kind() {
		case reflect.String, reflect.Bool, reflect.Int:
		case reflect.Slice:
			if target.Type().Elem().Kind() != reflect.String {
				t.Errorf("%s: slice of %s is not supported by setFromEnv", field.key, target.Type().Elem())
			}
		default:
			t.Errorf("%s: kind %s is not supported by setFromEnv", field.key, target.Kind())
		}
		if _, ok := envIgnored[field.name]; ok {
			t.Errorf("%s collides with an ignored non-config variable", field.name)
		}
	}
}

func TestEnvLockedChanges(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "AMDL_DOWNLOAD_COVER_FORMAT" || name == "AMDL_WRAPPER_ADDRESS" {
			return "x", true
		}
		return "", false
	}
	current := Default()
	merged := Default()
	if locked := EnvLockedChanges(current, merged, lookup); len(locked) != 0 {
		t.Fatalf("no changes must yield no locked fields, got %v", locked)
	}
	merged.Download.CoverFormat = "png"
	merged.Download.EmbedCover = false // changed but not pinned
	locked := EnvLockedChanges(current, merged, lookup)
	want := []string{"download.cover_format (AMDL_DOWNLOAD_COVER_FORMAT)"}
	if !reflect.DeepEqual(locked, want) {
		t.Fatalf("locked = %v, want %v", locked, want)
	}
}

// Bootstrapping must write the example's values, not the environment overlay:
// the overlay is re-applied on every load, and baking it into the file would
// keep the value pinned even after the variable is unset.
func TestBootstrapDoesNotBakeEnvOverrides(t *testing.T) {
	t.Setenv("AMDL_WRAPPER_ADDRESS", "wrapper-manager:8080")
	dir := t.TempDir()
	example := "wrapper:\n  address: \"127.0.0.1:8080\"\n"
	if err := os.WriteFile(filepath.Join(dir, "config.example.yaml"), []byte(example), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.yaml")
	if created, err := BootstrapFromExample(path); err != nil || !created {
		t.Fatalf("bootstrap = (%v, %v), want created", created, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "wrapper-manager:8080") {
		t.Fatalf("env override leaked into the bootstrapped file:\n%s", raw)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Wrapper.Address != "wrapper-manager:8080" {
		t.Fatalf("loaded wrapper address = %q, want env overlay on top of the file", cfg.Wrapper.Address)
	}
}
