package db

import (
	"context"
	"path/filepath"
	"testing"

	"amdl/internal/domain"
)

func libraryAnchorStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "amdl.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// Order is the whole point of the anchor — position 0 must come back as
// position 0, because "newer than the oldest remembered entry" is what makes a
// song count as new.
func TestLibrarySyncAnchorRoundTripPreservesOrder(t *testing.T) {
	store := libraryAnchorStore(t)
	ctx := context.Background()

	if got, err := store.LoadLibrarySyncAnchor(ctx); err != nil || len(got) != 0 {
		t.Fatalf("fresh store anchor = %v, err = %v, want empty", got, err)
	}

	want := []domain.LibraryAnchorEntry{
		{SongID: "i.newest", Name: "newest", AlbumName: "A"},
		{SongID: "i.middle", Name: "middle", AlbumName: "B"},
		{SongID: "i.oldest", Name: "oldest", AlbumName: "C"},
	}
	if err := store.ReplaceLibrarySyncAnchor(ctx, want); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err := store.LoadLibrarySyncAnchor(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("loaded %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Replacement is total: a shorter anchor must not leave the old tail behind,
// or the watcher would measure against a boundary that no longer exists.
func TestLibrarySyncAnchorReplaceDropsOldEntries(t *testing.T) {
	store := libraryAnchorStore(t)
	ctx := context.Background()

	long := make([]domain.LibraryAnchorEntry, 0, 10)
	for i := 0; i < 10; i++ {
		long = append(long, domain.LibraryAnchorEntry{SongID: string(rune('a' + i))})
	}
	if err := store.ReplaceLibrarySyncAnchor(ctx, long); err != nil {
		t.Fatalf("replace long: %v", err)
	}
	short := []domain.LibraryAnchorEntry{{SongID: "only"}}
	if err := store.ReplaceLibrarySyncAnchor(ctx, short); err != nil {
		t.Fatalf("replace short: %v", err)
	}
	got, err := store.LoadLibrarySyncAnchor(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 || got[0].SongID != "only" {
		t.Fatalf("anchor after shrink = %v, want just the new entry", got)
	}
}

func TestLibrarySyncAnchorClearReportsCount(t *testing.T) {
	store := libraryAnchorStore(t)
	ctx := context.Background()

	entries := []domain.LibraryAnchorEntry{{SongID: "a"}, {SongID: "b"}}
	if err := store.ReplaceLibrarySyncAnchor(ctx, entries); err != nil {
		t.Fatalf("replace: %v", err)
	}
	cleared, err := store.ClearLibrarySyncAnchor(ctx)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared != 2 {
		t.Fatalf("cleared = %d, want 2", cleared)
	}
	got, err := store.LoadLibrarySyncAnchor(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("anchor after clear = %v, want empty", got)
	}
	// Clearing an already-empty anchor is a normal repeat call, not an error.
	if cleared, err := store.ClearLibrarySyncAnchor(ctx); err != nil || cleared != 0 {
		t.Fatalf("second clear = %d, err = %v, want 0 and no error", cleared, err)
	}
}
