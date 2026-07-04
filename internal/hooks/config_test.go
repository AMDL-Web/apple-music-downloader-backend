package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHooksConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hooks.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadConfigMissingFileReturnsDisabledDefault(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil for missing file", err)
	}
	if cfg.Enabled {
		t.Fatalf("cfg.Enabled = true, want false for missing file")
	}
}

func TestLoadConfigValidEntry(t *testing.T) {
	path := writeHooksConfig(t, `
enabled: true
entries:
  - name: "emby-refresh"
    type: "webhook"
    events: ["job_finished"]
    url: "http://example.local/refresh"
    send_payload: false
  - name: "post-process"
    enabled: false
    type: "exec"
    events: ["job_finished", "job_failed"]
    command: "/bin/true"
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if len(cfg.Entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(cfg.Entries))
	}
	if !cfg.Entries[0].IsEnabled() {
		t.Fatalf("entries[0].IsEnabled() = false, want true (omitted defaults to enabled)")
	}
	if cfg.Entries[1].IsEnabled() {
		t.Fatalf("entries[1].IsEnabled() = true, want false (explicit disable)")
	}
}

func TestLoadConfigRejectsDuplicateNames(t *testing.T) {
	path := writeHooksConfig(t, `
entries:
  - name: "dup"
    type: "webhook"
    events: ["job_finished"]
    url: "http://example.local"
  - name: "dup"
    type: "exec"
    events: ["job_failed"]
    command: "/bin/true"
`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "duplicate hook name") {
		t.Fatalf("LoadConfig() error = %v, want duplicate hook name error", err)
	}
}

func TestLoadConfigRejectsUnknownType(t *testing.T) {
	path := writeHooksConfig(t, `
entries:
  - name: "bad"
    type: "telegram"
    events: ["job_finished"]
`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "type must be one of") {
		t.Fatalf("LoadConfig() error = %v, want type validation error", err)
	}
}

func TestLoadConfigRejectsUnknownEvent(t *testing.T) {
	path := writeHooksConfig(t, `
entries:
  - name: "bad"
    type: "webhook"
    events: ["item_completed"]
    url: "http://example.local"
`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unsupported event") {
		t.Fatalf("LoadConfig() error = %v, want unsupported event error", err)
	}
}

func TestLoadConfigRejectsWebhookWithoutURL(t *testing.T) {
	path := writeHooksConfig(t, `
entries:
  - name: "bad"
    type: "webhook"
    events: ["job_finished"]
`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("LoadConfig() error = %v, want url required error", err)
	}
}

func TestLoadConfigRejectsExecWithoutCommand(t *testing.T) {
	path := writeHooksConfig(t, `
entries:
  - name: "bad"
    type: "exec"
    events: ["job_finished"]
`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "command is required") {
		t.Fatalf("LoadConfig() error = %v, want command required error", err)
	}
}

func TestLoadConfigRejectsUnknownJobType(t *testing.T) {
	path := writeHooksConfig(t, `
entries:
  - name: "bad"
    type: "webhook"
    events: ["job_finished"]
    job_types: ["single"]
    url: "http://example.local"
`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unsupported job type") {
		t.Fatalf("LoadConfig() error = %v, want unsupported job type error", err)
	}
}

func TestLoadConfigRejectsUnknownField(t *testing.T) {
	path := writeHooksConfig(t, "unknown_field: true\n")
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("LoadConfig() error = %v, want unknown field error", err)
	}
}

// TestShippedHooksConfigLoads guards the shipped configs/hooks.yaml: with every
// example commented out, `entries:` must parse as an empty list (not error),
// and hooks must default to disabled.
func TestShippedHooksConfigLoads(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "configs", "hooks.yaml"))
	if err != nil {
		t.Fatalf("shipped configs/hooks.yaml failed to load: %v", err)
	}
	if cfg.Enabled {
		t.Fatalf("shipped config enabled = true, want false by default")
	}
	if len(cfg.Entries) != 0 {
		t.Fatalf("shipped config entries = %d, want 0 (all examples commented out)", len(cfg.Entries))
	}
}
