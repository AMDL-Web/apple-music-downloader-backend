package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadRejectsUnknownFields(t *testing.T) {
	path := writeConfig(t, "download:\n  codec: alac\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "field codec not found") {
		t.Fatalf("Load() error = %v, want unknown field error", err)
	}
}

func TestLoadRejectsExplicitEmptyValues(t *testing.T) {
	path := writeConfig(t, "catalog:\n  album_track_url_mode: \"\"\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "album_track_url_mode") {
		t.Fatalf("Load() error = %v, want album_track_url_mode validation error", err)
	}
}

func TestLoadRejectsUnknownCoverFormat(t *testing.T) {
	path := writeConfig(t, "download:\n  cover_format: webp\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "cover_format") {
		t.Fatalf("Load() error = %v, want cover_format validation error", err)
	}
}

func TestLoadRejectsExplicitAACLCInPriority(t *testing.T) {
	path := writeConfig(t, "download:\n  quality_priority: [alac, aac-lc]\n")
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "aac-lc") {
		t.Fatalf("Load() error = %v, want implicit AAC-LC validation error", err)
	}
}
