package app

import (
	"time"

	"github.com/egoist/mygo/ui"

	appui "amdl/apps/desktop/internal/ui"
)

// searchShots captures the catalog search against the live backend: hits
// for words in several scripts, the arrow keys and Return driven through a
// render that shares the window's state, an artist's albums, nothing found,
// light and narrow. The one download it starts is shotAlbum's, by its hit.
func (a *App) searchShots(shot func(name string, setup func(), settle time.Duration)) {
	search := func(term string) func() {
		return func() {
			a.view.Go(appui.ScreenHome)
			a.store.SetOmniText(term)
		}
	}
	shot("14-search", search("Yorushika"), 6*time.Second)
	// A render of the same view takes the keys, as the window does once
	// the keyboard has left the field.
	shot("15-search-keys", func() { a.keys(ui.KeyDown, ui.KeyDown, ui.KeyDown) }, 3*time.Second)
	shot("16-search-artist", func() { a.keys(ui.KeyEnd, ui.KeyEnter) }, 6*time.Second)
	shot("17-search-back", func() { a.keys(ui.KeyEscape) }, 2*time.Second)
	shot("18-search-jp", search("星街すいせい"), 6*time.Second)
	// The test album, found by name and chosen with the keys: its library
	// mark, then the queue mark once its hit starts it again.
	shot("19-search-album", search("月に向かって撃て"), 6*time.Second)
	shot("20-search-chosen", func() {
		for i, h := range a.store.Search.ShownHits() {
			if h.Type == "album" && h.ID == shotAlbumID {
				keys := make([]ui.Key, i+1)
				for j := range keys {
					keys[j] = ui.KeyDown
				}
				a.keys(keys...)
				break
			}
		}
	}, 2*time.Second)
	// The test album's page from its hit, in the library, and Back to the
	// results it opened from.
	shot("32-album-done", a.openAlbum(shotAlbumID), 5*time.Second)
	shot("33-album-back", a.view.Back, 2*time.Second)
	shot("21-search-start", func() {
		for _, h := range a.store.Search.ShownHits() {
			if h.Type == "album" && h.ID == shotAlbumID {
				a.store.SubmitHit(h, nil)
			}
		}
	}, 4*time.Second)
	shot("34-album-queued", a.openAlbum(shotAlbumID), 3*time.Second)
	shot("35-album-queued-back", a.view.Back, 2*time.Second)
	a.albumShots(shot, search)
	shot("22-search-taylor", search("Taylor Swift"), 6*time.Second)
	shot("23-search-song", search("夜に駆ける"), 6*time.Second)
	shot("24-search-none", search("zqxjvw kqqp xxyz"), 6*time.Second)
	shot("25-search-light", func() { a.view.Theme = "light"; search("Yorushika")() }, 3*time.Second)
	shot("26-search-compact", func() { a.view.Theme = a.opts.Theme; a.win.SetContentSize(820, 560) }, 3*time.Second)
	a.post(func() { a.win.SetContentSize(1280, 800) })
	// Home with nothing typed: the field waits at the page's optical center.
	shot("27-home-empty", func() { a.view.Theme = a.opts.Theme; a.store.SetOmniText(""); a.view.Go(appui.ScreenHome) }, 2*time.Second)
	shot("28-home-empty-light", func() { a.view.Theme = "light" }, 2*time.Second)
	shot("29-home-empty-compact", func() { a.view.Theme = a.opts.Theme; a.win.SetContentSize(820, 560) }, 2500*time.Millisecond)
	// The sidebar closed: the traffic lights and its button over the page.
	shot("30-sidebar-hidden", func() { a.win.SetContentSize(1280, 800); a.view.ToggleSidebar() }, 2*time.Second)
	shot("31-sidebar-hidden-light", func() { a.view.Theme = "light"; a.store.SetOmniText("Yorushika") }, 5*time.Second)
	a.post(func() { a.view.Theme = a.opts.Theme; a.store.SetOmniText(""); a.view.ToggleSidebar() })
}

// openAlbum opens the album screen of the shown hit with id.
func (a *App) openAlbum(id string) func() {
	return func() {
		for _, h := range a.store.Search.ShownHits() {
			if h.Type == "album" && h.ID == id {
				a.view.OpenAlbum(h)
				return
			}
		}
	}
}

// albumShots captures the album screen beyond the test album's: a long
// title, two discs, light and narrow.
func (a *App) albumShots(shot func(name string, setup func(), settle time.Duration), search func(string) func()) {
	a.post(search("THE TORTURED POETS DEPARTMENT THE ANTHOLOGY"))
	time.Sleep(6 * time.Second)
	shot("36-album-long", a.openAlbum("1742073690"), 5*time.Second)
	a.post(func() { a.view.Back(); search("宇多田ヒカル SCIENCE FICTION")() })
	time.Sleep(6 * time.Second)
	shot("37-album-discs", a.openAlbum("1736076981"), 5*time.Second)
	shot("38-album-discs-light", func() { a.view.Theme = "light" }, 2*time.Second)
	shot("39-album-discs-compact", func() { a.view.Theme = a.opts.Theme; a.win.SetContentSize(820, 560) }, 3*time.Second)
	shot("40-album-discs-compact-light", func() { a.view.Theme = "light" }, 2*time.Second)
	a.post(func() { a.view.Theme = a.opts.Theme; a.win.SetContentSize(1280, 800); a.view.Back() })
}

// shotAlbumID is shotAlbum's catalog id.
const shotAlbumID = "1858184006"

// keys presses keys, one after another, in a render of the main view.
func (a *App) keys(keys ...ui.Key) {
	tt := ui.NewTester(a.view.View, 1280, 800)
	for _, k := range keys {
		tt.Key(0, k)
		tt.Frame()
	}
}
