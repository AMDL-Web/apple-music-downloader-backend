package librarysync

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
)

// fakeLibrary serves a fixed newest-first song list, counting requests so tests
// can assert the request budget as well as the behaviour.
type fakeLibrary struct {
	songs     []applemusic.LibrarySong
	pageCalls int
	catalog   map[string]string // library album id -> catalog URL; missing = no counterpart
	failURL   map[string]bool
}

func (f *fakeLibrary) LibrarySongsNewestFirst(_ context.Context, token string, offset, limit int) ([]applemusic.LibrarySong, bool, error) {
	if token == "" {
		return nil, false, fmt.Errorf("missing token")
	}
	f.pageCalls++
	if offset >= len(f.songs) {
		return nil, false, nil
	}
	end := offset + limit
	if end > len(f.songs) {
		end = len(f.songs)
	}
	return f.songs[offset:end], end < len(f.songs), nil
}

func (f *fakeLibrary) LibraryAlbumCatalogURL(_ context.Context, _ string, libraryAlbumID string) (string, bool, error) {
	if f.failURL[libraryAlbumID] {
		return "", false, fmt.Errorf("boom")
	}
	url, ok := f.catalog[libraryAlbumID]
	return url, ok, nil
}

type fakeStore struct {
	anchor []domain.LibraryAnchorEntry
	writes int
}

func (s *fakeStore) LoadLibrarySyncAnchor(context.Context) ([]domain.LibraryAnchorEntry, error) {
	return s.anchor, nil
}

func (s *fakeStore) ReplaceLibrarySyncAnchor(_ context.Context, entries []domain.LibraryAnchorEntry) error {
	s.anchor = append([]domain.LibraryAnchorEntry(nil), entries...)
	s.writes++
	return nil
}

func (s *fakeStore) ClearLibrarySyncAnchor(context.Context) (int64, error) {
	n := int64(len(s.anchor))
	s.anchor = nil
	return n, nil
}

type fakeSubmitter struct {
	batches [][]string
}

func (f *fakeSubmitter) SubmitBatch(_ context.Context, urls []string, _ *config.DownloadOverrides) domain.BatchSubmitResponse {
	f.batches = append(f.batches, append([]string(nil), urls...))
	results := make([]domain.SubmitResult, 0, len(urls))
	for _, u := range urls {
		results = append(results, domain.SubmitResult{URL: u, Status: domain.SubmitAccepted})
	}
	return domain.BatchSubmitResponse{Accepted: len(urls), Results: results}
}

// library builds n songs, newest first, each on its own album unless albumOf
// maps it elsewhere.
func library(n int, albumOf map[int]string) ([]applemusic.LibrarySong, map[string]string) {
	songs := make([]applemusic.LibrarySong, 0, n)
	catalog := map[string]string{}
	for i := 0; i < n; i++ {
		album := fmt.Sprintf("l.album%d", i)
		if mapped, ok := albumOf[i]; ok {
			album = mapped
		}
		songs = append(songs, applemusic.LibrarySong{
			ID:             fmt.Sprintf("i.song%d", i),
			Name:           fmt.Sprintf("song %d", i),
			LibraryAlbumID: album,
			AlbumName:      album,
		})
		catalog[album] = "https://music.apple.com/cn/album/x/" + album
	}
	return songs, catalog
}

func newWatcher(t *testing.T, lib *fakeLibrary, store *fakeStore, jobs *fakeSubmitter, mutate func(*config.Config)) *Watcher {
	t.Helper()
	cfg := config.Default()
	cfg.LibrarySync.Enabled = true
	cfg.Catalog.MediaUserToken = "token"
	if mutate != nil {
		mutate(&cfg)
	}
	return New(config.NewStore(cfg), lib, store, jobs, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// A first pass records where the library stands and submits nothing: whatever
// was already there when the feature was switched on is not "new".
func TestFirstPassAnchorsWithoutSubmitting(t *testing.T) {
	songs, catalog := library(80, nil)
	lib := &fakeLibrary{songs: songs, catalog: catalog}
	store := &fakeStore{}
	jobs := &fakeSubmitter{}
	newWatcher(t, lib, store, jobs, nil).RunOnce(context.Background())

	if len(jobs.batches) != 0 {
		t.Fatalf("first pass submitted %v, want nothing", jobs.batches)
	}
	if len(store.anchor) != anchorSize {
		t.Fatalf("anchor size = %d, want %d", len(store.anchor), anchorSize)
	}
	if store.anchor[0].SongID != "i.song0" {
		t.Fatalf("anchor head = %q, want the newest song", store.anchor[0].SongID)
	}
	if lib.pageCalls != 1 {
		t.Fatalf("first pass used %d requests, want 1", lib.pageCalls)
	}
}

// The steady state: nothing added, nothing submitted, one request.
func TestQuietPassSubmitsNothing(t *testing.T) {
	songs, catalog := library(200, nil)
	lib := &fakeLibrary{songs: songs, catalog: catalog}
	store := &fakeStore{anchor: toEntries(songs[:anchorSize])}
	jobs := &fakeSubmitter{}
	newWatcher(t, lib, store, jobs, nil).RunOnce(context.Background())

	if len(jobs.batches) != 0 {
		t.Fatalf("quiet pass submitted %v, want nothing", jobs.batches)
	}
	if lib.pageCalls != 1 {
		t.Fatalf("quiet pass used %d requests, want 1", lib.pageCalls)
	}
}

// This is the regression that matters most. The pass reads anchorSize+margin
// songs, so the tail of that window is older than every anchor entry and is
// absent from the anchor only because the anchor holds a fixed number of
// entries. Treating "not in the anchor" as "new" would re-submit ~25 old albums
// on every single pass.
func TestSongsBeyondAnchorWindowAreNotNew(t *testing.T) {
	songs, catalog := library(200, nil)
	lib := &fakeLibrary{songs: songs, catalog: catalog}
	store := &fakeStore{anchor: toEntries(songs[:anchorSize])}
	jobs := &fakeSubmitter{}
	newWatcher(t, lib, store, jobs, nil).RunOnce(context.Background())

	if len(jobs.batches) != 0 {
		t.Fatalf("older songs past the anchor window were submitted: %v", jobs.batches)
	}
}

// Newly added songs submit their albums, and several new songs on one album
// collapse into a single job.
func TestNewSongsSubmitDedupedAlbums(t *testing.T) {
	// Songs 0 and 1 share an album; song 2 has its own. All three are newer
	// than the anchor, which starts at song 3.
	songs, catalog := library(200, map[int]string{0: "l.shared", 1: "l.shared"})
	lib := &fakeLibrary{songs: songs, catalog: catalog}
	store := &fakeStore{anchor: toEntries(songs[3 : 3+anchorSize])}
	jobs := &fakeSubmitter{}
	newWatcher(t, lib, store, jobs, nil).RunOnce(context.Background())

	if len(jobs.batches) != 1 {
		t.Fatalf("batches = %v, want exactly one", jobs.batches)
	}
	got := jobs.batches[0]
	if len(got) != 2 {
		t.Fatalf("submitted %v, want 2 albums (l.shared collapsed, l.album2)", got)
	}
	if got[0] != catalog["l.shared"] || got[1] != catalog["l.album2"] {
		t.Fatalf("submitted %v, want shared album first then album2", got)
	}
	if store.anchor[0].SongID != "i.song0" {
		t.Fatalf("anchor was not advanced: head = %q", store.anchor[0].SongID)
	}
}

// A library album with no catalog counterpart (personal upload, withdrawn in
// this storefront) is skipped without taking the rest of the pass with it.
func TestAlbumWithoutCatalogCounterpartIsSkipped(t *testing.T) {
	songs, catalog := library(200, nil)
	delete(catalog, "l.album0") // song 0 resolves to nothing
	lib := &fakeLibrary{songs: songs, catalog: catalog}
	store := &fakeStore{anchor: toEntries(songs[2 : 2+anchorSize])}
	jobs := &fakeSubmitter{}
	newWatcher(t, lib, store, jobs, nil).RunOnce(context.Background())

	if len(jobs.batches) != 1 || len(jobs.batches[0]) != 1 {
		t.Fatalf("batches = %v, want just the resolvable album", jobs.batches)
	}
	if jobs.batches[0][0] != catalog["l.album1"] {
		t.Fatalf("submitted %v, want album1", jobs.batches[0])
	}
}

// When the oldest remembered song is gone from the library there is no boundary
// to measure against. Submitting the whole scan would queue hundreds of albums,
// so the pass re-anchors and submits nothing.
func TestMissingAnchorReanchorsWithoutSubmitting(t *testing.T) {
	songs, catalog := library(120, nil)
	lib := &fakeLibrary{songs: songs, catalog: catalog}
	store := &fakeStore{anchor: []domain.LibraryAnchorEntry{{SongID: "i.deleted"}}}
	jobs := &fakeSubmitter{}
	newWatcher(t, lib, store, jobs, nil).RunOnce(context.Background())

	if len(jobs.batches) != 0 {
		t.Fatalf("submitted %v after losing the anchor, want nothing", jobs.batches)
	}
	if len(store.anchor) != anchorSize || store.anchor[0].SongID != "i.song0" {
		t.Fatalf("anchor not re-established: %d entries, head %v", len(store.anchor), store.anchor)
	}
}

// Without catalog.media_user_token a personal library is unreadable, so the
// watcher stays idle rather than failing a request every tick.
func TestIdleWithoutMediaUserToken(t *testing.T) {
	songs, catalog := library(80, nil)
	lib := &fakeLibrary{songs: songs, catalog: catalog}
	store := &fakeStore{}
	jobs := &fakeSubmitter{}
	w := newWatcher(t, lib, store, jobs, func(c *config.Config) { c.Catalog.MediaUserToken = "" })
	w.RunOnce(context.Background())

	if lib.pageCalls != 0 {
		t.Fatalf("made %d Apple requests without a token, want 0", lib.pageCalls)
	}
	if status := w.Status(); status.LastError == "" {
		t.Fatal("status should explain why the watcher is idle")
	}
}

// Reset forgets the watermark; the following pass re-anchors and submits
// nothing, so resetting cannot stampede the download queue.
func TestResetAnchorThenReanchorSubmitsNothing(t *testing.T) {
	songs, catalog := library(200, nil)
	lib := &fakeLibrary{songs: songs, catalog: catalog}
	store := &fakeStore{anchor: toEntries(songs[:anchorSize])}
	jobs := &fakeSubmitter{}
	w := newWatcher(t, lib, store, jobs, nil)

	cleared, err := w.ResetAnchor(context.Background())
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if cleared != int64(anchorSize) {
		t.Fatalf("cleared = %d, want %d", cleared, anchorSize)
	}
	w.RunOnce(context.Background())
	if len(jobs.batches) != 0 {
		t.Fatalf("reset then pass submitted %v, want nothing", jobs.batches)
	}
	if len(store.anchor) != anchorSize {
		t.Fatalf("anchor not re-established after reset: %d", len(store.anchor))
	}
}

// A burst larger than the margin still resolves, by reading further rather than
// mistaking the overflow for the boundary.
func TestLargeBurstPagesBeyondTheMargin(t *testing.T) {
	songs, catalog := library(300, nil)
	lib := &fakeLibrary{songs: songs, catalog: catalog}
	// Anchor starts 60 songs down: more new songs than margin (25) allows in
	// the first request.
	store := &fakeStore{anchor: toEntries(songs[60 : 60+anchorSize])}
	jobs := &fakeSubmitter{}
	newWatcher(t, lib, store, jobs, nil).RunOnce(context.Background())

	if len(jobs.batches) != 1 || len(jobs.batches[0]) != 60 {
		count := 0
		if len(jobs.batches) > 0 {
			count = len(jobs.batches[0])
		}
		t.Fatalf("submitted %d albums, want 60", count)
	}
	if lib.pageCalls < 2 {
		t.Fatalf("used %d requests, want more than one for a burst past the margin", lib.pageCalls)
	}
}
