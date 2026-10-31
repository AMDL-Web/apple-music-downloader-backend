package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/store"
	"amdl/internal/client"
	"amdl/internal/domain"
)

// searchApp is home showing albums for "elma": many, so the results scroll,
// the first in the library through job done.
func searchApp(t *testing.T) (*App, []client.SearchHit) {
	t.Helper()
	a, _ := newTestApp(remoteSettings())
	s := a.Store
	s.SetOmniText("elma")
	s.WantSearch("elma")
	var albums []client.SearchHit
	for i := range 30 {
		albums = append(albums, client.SearchHit{Type: "album", ID: fmt.Sprint(100 + i), Name: fmt.Sprintf("Album %d", i), ArtistName: "Yorushika", TrackCount: 3,
			URL: fmt.Sprintf("https://music.apple.com/cn/album/x/%d", 100+i)})
	}
	albums[0].PriorJob = &client.PriorJob{ID: "done", Status: domain.JobCompleted}

	s.Search.Result = &client.SearchResult{Albums: &client.HitGroup{Items: albums}}
	s.Search.Status = store.SearchReady
	a.Screen = ScreenHome
	return a, albums
}

func TestAlbumTileHasOneControl(t *testing.T) {
	a, albums := searchApp(t)
	tt := ui.NewTester(a.View, 1280, 800)
	if _, ok := tt.Find("Album 0 is in your library: download again"); !ok {
		t.Errorf("no library control on the downloaded album")
	}
	if _, ok := tt.Find("In your library"); ok {
		t.Errorf("the downloaded album still has a badge besides its control")
	}
	// Another album's download shows only while pointed at.
	if _, ok := tt.Find("Download Album 1"); ok {
		t.Errorf("an album not downloaded shows its download at rest")
	}
	r, ok := tt.Find("Album 1")
	if !ok {
		t.Fatalf("no tile for %s: %q", albums[1].Name, tt.Texts())
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(300 * time.Millisecond)
	tt.Frame()
	if _, ok := tt.Find("Download Album 1"); !ok {
		t.Errorf("pointing at an album does not show its download")
	}
}

func TestAlbumOpensFromSearchAndBackReturnsThere(t *testing.T) {
	a, albums := searchApp(t)
	s := a.Store
	tt := ui.NewTester(a.View, 1280, 800)
	home := a.scrolls["home-body"]
	if home == nil || home.MaxY < 400 {
		t.Fatalf("the results do not scroll: %+v", home)
	}
	home.Y = 400
	tt.Frame()
	// A downloaded album opens its page, not its job.
	a.OpenAlbum(albums[0])
	tt.Frame()
	if a.Screen != ScreenAlbum || !tt.HasText("Results") {
		t.Fatalf("screen %v, texts %q", a.Screen, tt.Texts())
	}
	p := &s.Search.Album
	p.Status, p.Album = store.SearchReady, client.Album{ID: "100", Title: "Album 0", ArtistName: "Yorushika", DiscCount: 2, Tracks: []client.AlbumTrack{
		{ID: "t1", DiscNumber: 1, TrackNumber: 1, Name: "First Song", DurationMS: 200_000},
		{ID: "t2", DiscNumber: 2, TrackNumber: 1, Name: "Second Song", ArtistName: "Yorushika & suis", DurationMS: 61_000, ContentRating: "explicit"},
	}}
	tt.Frame()
	for _, want := range []string{"Album 0", "In your library", "Download again", "DISC 1", "DISC 2", "First Song", "Second Song", "Yorushika & suis", "3:20", "1:01", "ALBUM · 2 TRACKS"} {
		if !tt.HasText(want) {
			t.Errorf("no %q on the album page: %q", want, tt.Texts())
		}
	}
	// The detail can show only a job the feed holds.
	if tt.HasText("Show download") {
		t.Errorf("Show download offered for a job the feed does not hold")
	}
	// Back from the job returns here.
	a.OpenJob("done")
	if a.backLabel() != "Album 0" {
		t.Errorf("from the album, Back says %q", a.backLabel())
	}
	a.Back()
	tt.Frame()
	if a.Screen != ScreenAlbum || s.DetailID != "" {
		t.Fatalf("Back from the job: screen %v job %q", a.Screen, s.DetailID)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if a.Screen != ScreenHome || s.Omni.Text != "elma" || a.searchSel != store.HitKey(albums[0]) {
		t.Fatalf("Back to search: screen %v text %q chosen %q", a.Screen, s.Omni.Text, a.searchSel)
	}
	tt.Frame()
	if !tt.HasText("Album 29") || home.Y != 400 {
		t.Errorf("the results came back at %v, want 400: %q", home.Y, tt.Texts())
	}
	if !tt.Focused("Paste links or search Apple Music") {
		t.Errorf("Back to search left the keyboard out of the field")
	}
}

func TestSidebarLightsWhereAPageOpened(t *testing.T) {
	a, albums := searchApp(t)
	a.OpenAlbum(albums[0])
	if got := a.navScreen(); got != ScreenHome {
		t.Errorf("album opened from search lights %v", got)
	}
	a.OpenJob("done")
	if got := a.navScreen(); got != ScreenHome {
		t.Errorf("job opened from an album lights %v", got)
	}
	a.Go(ScreenQueue)
	a.OpenJob("done")
	if got := a.navScreen(); got != ScreenQueue {
		t.Errorf("job opened from the queue lights %v", got)
	}
	a.Go(ScreenSettings)
	if got := a.navScreen(); got != ScreenSettings {
		t.Errorf("settings lights %v", got)
	}
}

func TestBackFromAJobReturnsWhereItOpened(t *testing.T) {
	a, _ := newTestApp(remoteSettings())
	a.Go(ScreenQueue)
	a.OpenJob("j")
	if a.backLabel() != "Queue" {
		t.Errorf("from the queue, Back says %q", a.backLabel())
	}
	a.Back()
	if a.Screen != ScreenQueue {
		t.Errorf("Back from a job opened in the queue shows %v", a.Screen)
	}
	a.Go(ScreenHome)
	a.Store.SetOmniText("yorushika")
	a.Store.WantSearch("yorushika")
	a.OpenJob("j")
	if a.backLabel() != "Results" {
		t.Errorf("from the search, Back says %q", a.backLabel())
	}
	a.Back()
	if a.Screen != ScreenHome {
		t.Errorf("Back from a job opened in the search shows %v", a.Screen)
	}
	// A job opened from a notification while setup shows leaves setup
	// behind: Back goes to the queue, which the sidebar lights.
	a.OpenSetup("")
	a.OpenJob("j")
	if a.backLabel() != "Queue" || a.navScreen() != ScreenQueue {
		t.Errorf("from setup, Back says %q and the sidebar lights %v", a.backLabel(), a.navScreen())
	}
	a.Back()
	if a.Screen != ScreenQueue {
		t.Errorf("Back from a job opened over setup shows %v", a.Screen)
	}
}

func TestReturnOpensAnAlbum(t *testing.T) {
	a, albums := searchApp(t)
	a.searchSel = store.HitKey(albums[3])
	a.actOnHit()
	if a.Screen != ScreenAlbum || a.Store.Search.Album.ID != albums[3].ID {
		t.Errorf("Return on an album: screen %v album %q", a.Screen, a.Store.Search.Album.ID)
	}
	if len(a.Store.Search.Starting) != 0 {
		t.Errorf("Return on an album started a download")
	}
}

func TestAlbumKindAndClip(t *testing.T) {
	if k, n := albumKind("Halo - EP"); k != "EP" || n != "Halo" {
		t.Errorf("Halo - EP: %q %q", k, n)
	}
	if k, _ := albumKind("Elma"); k != "Album" || !strings.HasPrefix(clip("Yorushika and many friends of theirs", 10), "Yorushika") {
		t.Error("Elma is an album, and clip keeps its start")
	}
}
