package logging

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amdl/internal/config"
)

func testConfig() config.LoggingConfig {
	cfg := config.Default().Logging
	cfg.Console = false
	cfg.BufferSize = 10
	return cfg
}

func TestSystemCapturesFiltersAndRedacts(t *testing.T) {
	system, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer system.Close()

	logger := system.Logger.With("component", "jobs", "job_id", "job-1")
	logger.Debug("hidden")
	logger.Info("download ready", "password", "plaintext",
		slog.Group("account", "authorization", "Bearer secret", "name", "alice"),
		"payload", map[string]any{"access_token": "nested-secret", "items": []any{map[string]any{"api_key": "nested-key"}}},
		"error", errors.New("failure"),
	)

	page := system.Store.List(Filter{Component: "jobs", JobID: "job-1", Query: "alice", Limit: 20})
	if page.NextCursor != 1 || len(page.Entries) != 1 {
		t.Fatalf("entries/cursor = %d/%d, want 1/1", len(page.Entries), page.NextCursor)
	}
	if page.Entries[0].Attributes["password"] != "[REDACTED]" {
		t.Fatalf("password not redacted: %#v", page.Entries[0].Attributes)
	}
	if page.Entries[0].Attributes["error"] != "failure" {
		t.Fatalf("error attr = %#v", page.Entries[0].Attributes["error"])
	}
	account, ok := page.Entries[0].Attributes["account"].(map[string]any)
	if !ok || account["authorization"] != "[REDACTED]" || account["name"] != "alice" {
		t.Fatalf("group redaction = %#v", page.Entries[0].Attributes["account"])
	}
	payload, ok := page.Entries[0].Attributes["payload"].(map[string]any)
	if !ok || payload["access_token"] != "[REDACTED]" {
		t.Fatalf("nested redaction = %#v", page.Entries[0].Attributes["payload"])
	}
	items, itemsOK := payload["items"].([]any)
	if !itemsOK || len(items) != 1 {
		t.Fatalf("nested items = %#v", payload["items"])
	}
	item, itemOK := items[0].(map[string]any)
	if !itemOK || item["api_key"] != "[REDACTED]" {
		t.Fatalf("nested item redaction = %#v", items[0])
	}
}

func TestHandlerPreservesBoundAttributeGroups(t *testing.T) {
	system, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	logger := system.Logger.With("root", "value").WithGroup("nested").With("child", "value")
	logger.Info("grouped")
	page := system.Store.List(Filter{Limit: 1})
	if page.Entries[0].Attributes["root"] != "value" {
		t.Fatalf("root attr moved into group: %#v", page.Entries[0].Attributes)
	}
	nested, ok := page.Entries[0].Attributes["nested"].(map[string]any)
	if !ok || nested["child"] != "value" {
		t.Fatalf("nested attr missing: %#v", page.Entries[0].Attributes)
	}
}

func TestSystemUpdatesLevel(t *testing.T) {
	system, err := New(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	system.Logger.Debug("before")
	if err := system.SetLevel("debug"); err != nil {
		t.Fatal(err)
	}
	system.Logger.Debug("after")
	page := system.Store.List(Filter{Limit: 10})
	if len(page.Entries) != 1 || page.Entries[0].Message != "after" {
		t.Fatalf("level update entries = %#v", page.Entries)
	}
	if err := system.SetLevel("trace"); err == nil {
		t.Fatal("invalid runtime level must fail")
	}
}

func TestStoreEvictionAndFilteredSubscription(t *testing.T) {
	store := NewStore(2)
	store.append(Entry{Time: time.Now(), Level: "info", Message: "one", Attributes: map[string]any{"component": "api"}})
	store.append(Entry{Time: time.Now(), Level: "warn", Message: "two", Attributes: map[string]any{"component": "jobs"}})
	store.append(Entry{Time: time.Now(), Level: "error", Message: "three", Attributes: map[string]any{"component": "jobs"}})
	page := store.List(Filter{Limit: 10})
	if page.NextCursor != 3 || len(page.Entries) != 2 || page.Entries[0].Message != "two" {
		t.Fatalf("evicted entries = %#v, cursor=%d", page.Entries, page.NextCursor)
	}
	page = store.List(Filter{After: 1, Limit: 1})
	if page.Truncated || page.OldestSequence != 2 || page.NextCursor != 2 || len(page.Entries) != 1 {
		t.Fatalf("cursor page = %+v", page)
	}
	gapStore := NewStore(2)
	for i := 0; i < 4; i++ {
		gapStore.append(Entry{Time: time.Now(), Level: "info", Message: "entry"})
	}
	if gap := gapStore.List(Filter{After: 1, Limit: 10}); !gap.Truncated || gap.OldestSequence != 3 {
		t.Fatalf("eviction gap = %+v", gap)
	}
	backlog, live, stop := store.Subscribe(Filter{After: 2, Levels: []string{"error"}, Limit: 10})
	defer stop()
	if len(backlog) != 1 || backlog[0].Message != "three" {
		t.Fatalf("backlog = %#v", backlog)
	}
	store.append(Entry{Time: time.Now(), Level: "info", Message: "ignored"})
	store.append(Entry{Time: time.Now(), Level: "error", Message: "live"})
	select {
	case entry := <-live:
		if entry.Message != "live" {
			t.Fatalf("live entry = %#v", entry)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for filtered live entry")
	}
}

func TestStoreDisconnectsSlowSubscriberAndMarksDisabledHistory(t *testing.T) {
	store := NewStore(0)
	_, live, stop := store.Subscribe(Filter{})
	defer stop()
	for i := 0; i < 257; i++ {
		store.append(Entry{Time: time.Now(), Level: "info", Message: "entry"})
	}
	for i := 0; i < 256; i++ {
		if _, ok := <-live; !ok {
			t.Fatalf("subscriber closed after %d buffered records", i)
		}
	}
	if _, ok := <-live; ok {
		t.Fatal("slow subscriber must be closed after its buffer fills")
	}
	page := store.List(Filter{After: 1, Limit: 10})
	if !page.Truncated || page.OldestSequence != 0 || page.NextCursor != 257 {
		t.Fatalf("disabled history page = %+v", page)
	}
}

func TestFileOutput(t *testing.T) {
	cfg := testConfig()
	cfg.FileEnabled = true
	cfg.FilePath = filepath.Join(t.TempDir(), "nested", "amdl.log")
	cfg.Format = "json"
	system, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	system.Logger.InfoContext(context.Background(), "written", "component", "test", "payload", map[string]string{"token": "must-not-leak"})
	if err := system.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cfg.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"msg":"written"`) {
		t.Fatalf("file output = %s", raw)
	}
	if strings.Contains(string(raw), "must-not-leak") || !strings.Contains(string(raw), "[REDACTED]") {
		t.Fatalf("file output was not redacted: %s", raw)
	}
}

func TestFileOutputFailsFastForUnwritableTarget(t *testing.T) {
	cfg := testConfig()
	cfg.FileEnabled = true
	cfg.FilePath = t.TempDir()
	if _, err := New(cfg); err == nil || !strings.Contains(err.Error(), "open log file") {
		t.Fatalf("New() error = %v, want log-file probe failure", err)
	}
}
