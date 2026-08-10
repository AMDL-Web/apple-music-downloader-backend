package db

import (
	"context"
	"path/filepath"
	"testing"
)

func openSettingsStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestConfigSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openSettingsStore(t)

	// A fresh database holds no settings at all: every key falls through to
	// the config file and the built-in defaults.
	settings, err := store.ConfigSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings) != 0 {
		t.Fatalf("fresh database settings = %v, want none", settings)
	}

	format, attempts := `"png"`, `7`
	if err := store.ApplyConfigSettings(ctx, map[string]*string{
		"download.cover_format": &format,
		"download.max_attempts": &attempts,
	}); err != nil {
		t.Fatal(err)
	}
	settings, err = store.ConfigSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings["download.cover_format"] != `"png"` || settings["download.max_attempts"] != "7" {
		t.Fatalf("stored settings = %v", settings)
	}

	// Writing the same key again replaces its value rather than failing on the
	// primary key.
	updated := `"jpeg"`
	if err := store.ApplyConfigSettings(ctx, map[string]*string{"download.cover_format": &updated}); err != nil {
		t.Fatal(err)
	}
	// A nil value deletes the row, which is how a key is reset to the layers
	// below it.
	if err := store.ApplyConfigSettings(ctx, map[string]*string{"download.max_attempts": nil}); err != nil {
		t.Fatal(err)
	}
	settings, err = store.ConfigSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings) != 1 || settings["download.cover_format"] != `"jpeg"` {
		t.Fatalf("settings after upsert and delete = %v", settings)
	}

	// Deleting a key that was never stored is not an error: a reset is
	// idempotent from the client's point of view.
	if err := store.ApplyConfigSettings(ctx, map[string]*string{"download.memory_mode": nil}); err != nil {
		t.Fatalf("resetting an unset key: %v", err)
	}
}

// TestApplyConfigSettingsIsAtomic pins the all-or-nothing write: the store's
// caller rejects a whole patch on any bad key, and the transaction must not
// leave a half-applied batch behind if a statement fails.
func TestApplyConfigSettingsIsAtomic(t *testing.T) {
	ctx := context.Background()
	store := openSettingsStore(t)
	value := `"png"`
	if err := store.ApplyConfigSettings(ctx, map[string]*string{"download.cover_format": &value}); err != nil {
		t.Fatal(err)
	}
	// A cancelled context fails the batch; the earlier value must survive.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	replacement := `"jpg"`
	if err := store.ApplyConfigSettings(cancelled, map[string]*string{"download.cover_format": &replacement}); err == nil {
		t.Fatal("cancelled batch succeeded, want an error")
	}
	settings, err := store.ConfigSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings["download.cover_format"] != `"png"` {
		t.Fatalf("failed batch changed the stored value: %v", settings)
	}
}

func TestApplyConfigSettingsEmptyBatchIsNoOp(t *testing.T) {
	store := openSettingsStore(t)
	if err := store.ApplyConfigSettings(context.Background(), nil); err != nil {
		t.Fatalf("empty batch: %v", err)
	}
}
