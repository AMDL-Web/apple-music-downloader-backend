package ui

import (
	"slices"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/store"
)

func TestADownloadAsksBetweenLosslessAndDolbyOnlyWhenBothApply(t *testing.T) {
	both := []string{"atmos", "lossless", "lossy-stereo"}
	for _, tc := range []struct {
		traits, priority, want []string
	}{
		{both, []string{"alac", "ec3"}, []string{"alac", "ec3"}},
		{both, []string{"ec3", "alac", "aac"}, []string{"ec3", "alac"}},
		{both, []string{"alac"}, nil},
		{both, []string{"alac", "aac"}, nil},
		{[]string{"lossless", "lossy-stereo"}, []string{"alac", "ec3"}, nil},
		{[]string{"atmos", "lossy-stereo"}, []string{"alac", "ec3"}, nil},
	} {
		if got := askCodecs(tc.traits, tc.priority); !slices.Equal(got, tc.want) {
			t.Errorf("traits %v, priority %v: asks %v, want %v", tc.traits, tc.priority, got, tc.want)
		}
	}

	d := downloadAsk{codecs: []string{"alac", "ec3"}, priority: []string{"alac", "ec3", "aac"}, codec: 1}
	o := d.overrides(false)
	if o == nil || !slices.Equal(*o.QualityPriority, []string{"ec3", "alac", "aac"}) || o.ForceOverwrite != nil {
		t.Errorf("Dolby chosen: %+v", o)
	}
	again := downloadAsk{again: true}
	if o := again.overrides(true); o == nil || *o.ForceOverwrite {
		t.Errorf("skip chosen: %+v", o)
	}
	again.replace = 1
	if o := again.overrides(true); o == nil || !*o.ForceOverwrite {
		t.Errorf("replace chosen: %+v", o)
	}
	// SkipExisting: in the library, not asked, the files are kept whatever
	// the server's force_overwrite.
	if o := (&downloadAsk{}).overrides(true); o == nil || *o.ForceOverwrite {
		t.Errorf("skipped without asking: %+v", o)
	}
	if o := (&downloadAsk{}).overrides(false); o != nil {
		t.Errorf("nothing to ask: %+v", o)
	}
}

func TestSearchShowsOneGroupUnderTabsAndReadsItsNextPages(t *testing.T) {
	a, run := demoApp(t)
	a.Screen = ScreenHome
	tt := ui.NewTester(a.View, 1280, 800)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	a.Store.SetOmniText("a")
	tt.Frame()
	run(func() bool { return a.Store.Search.Status == store.SearchReady })
	tt.Frame()
	songs := len(a.Store.Search.Result.Songs.Items)
	if songs != store.SearchFirst || !tt.HasText("Playlists") {
		t.Fatalf("first page: %d songs, texts %q", songs, tt.Texts())
	}
	if err := tt.Click("Show more songs"); err != nil {
		t.Fatal(err)
	}
	if a.Store.Search.Focus != "songs" {
		t.Fatalf("focus %q after Show more songs", a.Store.Search.Focus)
	}
	// The short page asks for the next one at once.
	run(func() bool {
		tt.Frame()
		return len(a.Store.Search.Result.Songs.Items) > songs
	})
	for _, g := range store.SearchGroups {
		if _, ok := tt.Find(groupTitle(g)); !ok {
			t.Errorf("no %s tab in %q", groupTitle(g), tt.Texts())
		}
	}
	if hits := a.Store.Search.ShownHits(); len(hits) == 0 || hits[0].Type != "song" {
		t.Errorf("the keys walk %d hits, the first a %s", len(hits), hits[0].Type)
	}
	if err := tt.Click("Playlists"); err != nil {
		t.Fatal(err)
	}
	if a.Store.Search.Focus != "playlists" || tt.HasText("Show more") {
		t.Errorf("after the Playlists tab: focus %q, texts %q", a.Store.Search.Focus, tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)
	if a.Store.Search.Focus != "" {
		t.Errorf("Escape left the focus on %q", a.Store.Search.Focus)
	}
}

func TestAnAlbumInLosslessAndDolbyAsksWhichBeforeItDownloads(t *testing.T) {
	a, run := demoApp(t)
	a.Screen = ScreenHome
	tt := ui.NewTester(a.View, 1280, 800)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	a.Store.SetOmniText("glass weather")
	tt.Frame()
	run(func() bool { return a.Store.Search.Status == store.SearchReady && a.Store.Config.Values != nil })
	tt.Frame()
	jobs := len(a.Store.Jobs)
	if err := tt.Click("Download Glass Weather"); err != nil {
		t.Fatalf("%v in %q", err, tt.Texts())
	}
	for _, want := range []string{"Download “Glass Weather”", "Lossless", "Dolby Atmos"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in the sheet: %q", want, tt.Texts())
		}
	}
	if strings.Contains(strings.Join(tt.Texts(), "\n"), "In your library") {
		t.Errorf("a new album asks about the library: %q", tt.Texts())
	}
	if len(a.Store.Search.Starting) != 0 {
		t.Fatal("the download started before the sheet's answer")
	}
	if err := tt.Click("Download"); err != nil {
		t.Fatal(err)
	}
	run(func() bool { return len(a.Store.Jobs) > jobs })
	tt.Frame()
	if tt.HasText("Dolby Atmos") {
		t.Errorf("the sheet stayed: %q", tt.Texts())
	}
}
