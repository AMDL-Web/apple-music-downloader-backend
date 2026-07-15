package main

import (
	"path/filepath"
	"testing"

	"amdl/internal/config"
	"amdl/internal/domain"
)

func TestRecoveryTempOverrideRejectsLegacyPathEscape(t *testing.T) {
	root := t.TempDir()
	base := config.Default()
	base.Download.DownloadsDir = filepath.Join(root, "downloads")
	base.Download.TempDir = filepath.Join(root, "temp")

	inside := filepath.Join(base.Download.TempDir, "job-1")
	job := domain.Job{Overrides: &config.DownloadOverrides{TempDir: &inside}}
	got, ok, err := recoveryTempOverride(base, job)
	if err != nil || !ok || got != inside {
		t.Fatalf("inside override = (%q, %v, %v), want accepted", got, ok, err)
	}

	escape := filepath.Join(root, "outside")
	job.Overrides.TempDir = &escape
	if _, _, err := recoveryTempOverride(base, job); err == nil {
		t.Fatal("legacy temp_dir escape was accepted for startup cleanup")
	}
}
