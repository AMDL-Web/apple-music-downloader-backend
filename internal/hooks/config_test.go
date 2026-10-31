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

func TestLoadConfigAcceptsJobQueuedEvent(t *testing.T) {
	path := writeHooksConfig(t, `
enabled: true
entries:
  - name: "on-queued"
    type: "webhook"
    events: ["job_queued"]
    url: "http://example.local/queued"
`)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil for job_queued event", err)
	}
	if len(cfg.Entries) != 1 || !cfg.Entries[0].MatchesEvent("job_queued") {
		t.Fatalf("entry did not match job_queued: %+v", cfg.Entries)
	}
}

func TestLoadConfigRejectsMixingCreationAndTerminalEvents(t *testing.T) {
	path := writeHooksConfig(t, `
entries:
  - name: "mixed"
    type: "webhook"
    events: ["job_queued", "job_finished"]
    url: "http://example.local"
`)
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "cannot be combined with terminal events") {
		t.Fatalf("LoadConfig() error = %v, want creation/terminal mix rejection", err)
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

// A hooks.yaml from before these keys were fixed fails at startup naming each
// removed key and the upgrade guide, not with the decoder's bare "not found".
func TestLoadConfigNamesRemovedKeys(t *testing.T) {
	path := writeHooksConfig(t, `
enabled: true
timeout_seconds: 30
max_concurrent: 2
entries:
  - name: "refresh"
    type: "webhook"
    events: ["job_finished"]
    url: "http://example.local/refresh"
    send_payload: false
    timeout_seconds: 15
  - type: "exec"
    events: ["job_failed"]
    command: "/bin/true"
    timeout_seconds: 5
`)
	_, err := LoadConfig(path)
	if err == nil {
		t.Fatal("LoadConfig() error = nil, want removed keys rejected")
	}
	for _, want := range []string{
		"timeout_seconds", "max_concurrent",
		`entries["refresh"].send_payload`, `entries["refresh"].timeout_seconds`, "entries[1].timeout_seconds",
		"docs/upgrading-to-2.0.md",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

// TestShippedHooksConfigLoads guards the shipped configs/hooks.example.yaml,
// which installs copy to hooks.yaml: with every example commented out,
// `entries:` must parse as an empty list (not error), and hooks must default
// to disabled.
func TestShippedHooksConfigLoads(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join("..", "..", "configs", "hooks.example.yaml"))
	if err != nil {
		t.Fatalf("shipped configs/hooks.example.yaml failed to load: %v", err)
	}
	if cfg.Enabled {
		t.Fatalf("shipped config enabled = true, want false by default")
	}
	if len(cfg.Entries) != 0 {
		t.Fatalf("shipped config entries = %d, want 0 (all examples commented out)", len(cfg.Entries))
	}
}

func TestLoadConfigRejectsInvalidWebhookURL(t *testing.T) {
	for _, raw := range []string{"media-server.local/refresh", "ftp://media-server.local/refresh", "http://", "http//media-server.local", "://x"} {
		t.Run(raw, func(t *testing.T) {
			path := writeHooksConfig(t, `
entries:
  - name: "bad-url"
    type: "webhook"
    events: ["job_finished"]
    url: "`+raw+`"
`)
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), `hooks.entries["bad-url"]: url`) {
				t.Fatalf("LoadConfig() error = %v, want an invalid url error naming the entry", err)
			}
		})
	}
}

func TestLoadConfigChecksAPreset(t *testing.T) {
	for body, want := range map[string]string{
		"entries:\n  - {name: a, type: webhook, events: [job_finished], url: \"https://x.example\", preset: pager}\n": "unknown preset",
		"entries:\n  - {name: a, type: exec, events: [job_finished], command: x, preset: ntfy}\n":                     "for type webhook",
	} {
		if _, err := LoadConfig(writeHooksConfig(t, body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: %v, want an error naming %q", body, err, want)
		}
	}
	if _, err := LoadConfig(writeHooksConfig(t, "entries:\n  - {name: a, type: webhook, events: [job_finished], url: \"https://x.example\", preset: ntfy}\n")); err != nil {
		t.Errorf("a known preset: %v", err)
	}
}
