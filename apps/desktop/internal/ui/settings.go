package ui

import (
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/mode"
	"amdl/apps/desktop/internal/store"
)

// settingsForm is the Settings screen's state between frames.
type settingsForm struct {
	// page is the section the navigation marks: "" for the status board, a
	// section id of the web console's, or one of the app's own. The
	// sections run as one page; page follows its scroll.
	page string
	// tops are the sections' tops in the window, and areaTop the scroll
	// area's, as the last frame laid them out.
	tops    map[string]float32
	areaTop float32
	// pin is a section held at the top until the user scrolls, while the
	// sections above it may still grow as their state arrives; pinY is
	// where it put the scroll last.
	pin  string
	pinY float32
	// draft holds the app's connection settings until Apply, since
	// applying them connects again.
	loaded  bool
	draft   mode.Settings
	theme   int
	testing bool
	// testFrom is when the running test started.
	testFrom time.Time
	checks   []mode.Check
	tested   string
	edit     fieldEdit
	login    loginForm
}

// statusDelay is how long a request runs before the screen says it is under
// way: one that answers sooner shows its answer alone, with no flash of the
// waiting state.
const statusDelay = 250 * time.Millisecond

// waited reports whether the request that is on its way (active) left at
// since at least statusDelay ago: only then does the screen show its waiting
// state, grey a control or dim the last results. Until then it asks for the
// frame that shows them, so a quick answer shows its result alone.
func waited(c *ui.Context, active bool, since time.Time) bool {
	if !active {
		return false
	}
	d := c.Now().Sub(since)
	if d >= statusDelay {
		return true
	}
	c.After(statusDelay - d)
	return false
}

// settingsPage is a page of Settings' navigation.
type settingsPage struct {
	id, label string
	icon      *ui.SVG
}

// settingsNav lists the pages in groups: the status board, the backend's
// nine sections in the web console's order and ids
// (apps/console/static/js/lib/settings-sections.js), then the app's own.
var settingsNav = [][]settingsPage{
	{{"", "Status", icSystem}},
	{
		{"account", "Account", icPerson}, {"quality", "Quality", icNote}, {"files", "Storage & files", icDisk},
		{"lyrics_artwork", "Lyrics & artwork", icLibrary}, {"jobs", "Jobs", icQueue},
	},
	{
		{"library_sync", "Library sync", icSync}, {"hooks", "Hooks", icLink}, {"logging", "Logs", icClock},
		{"server", "Server", icServer},
	},
	{{"connection", "Connection", icLaptop}, {"appearance", "Appearance", icSettings}, {"about", "About", icNote}},
}

var themeNames = []string{"system", "light", "dark"}

// GoSettings opens Settings at page.
func (a *App) GoSettings(page string) {
	a.showSection(page)
	a.Go(ScreenSettings)
}

// showSection brings section id to the top of Settings' page and holds it
// there until the user scrolls.
func (a *App) showSection(id string) {
	f := &a.set
	f.page, f.pin, f.pinY = id, id, -1
}

func (a *App) settings(c *ui.Context, k *Tokens) {
	f := &a.set
	f.edit.init()
	if !f.loaded {
		f.loaded, f.draft = true, a.Settings
		f.theme = 0
		for i, n := range themeNames {
			if n == a.Settings.Theme {
				f.theme = i
			}
		}
	}
	ui.Row(c).Padding(0, 18).Height(44).AlignItems(ui.Center).Children(func() {
		ui.Text(c, "Settings").FontSize(15).FontWeight(600)
	})
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Scroll(c.Key("settings-nav")).Width(196).BorderWidth(0, 1, 0, 0).BorderColor(k.Line).TrackScroll(a.scrollState(c, "settings-nav")).Children(func() {
			ui.Column(c).Padding(10, 10, 16, 10).Gap(2).Children(func() {
				for g, group := range settingsNav {
					if g > 0 {
						ui.Box(c.Key(g)).Height(1).Background(k.Line).Margin(8, 6)
					}
					if g == len(settingsNav)-1 {
						ui.Text(c, "This app").FontSize(11).FontWeight(600).TextColor(k.Faint).Padding(0, 10, 4, 10)
					}
					for _, p := range group {
						a.settingsNavItem(c, k, p)
					}
				}
			})
		})
		width := min(max(a.pageWidth(c)-settingsNavWidth-2*settingsPad, 320), settingsMaxWidth)
		area := a.scrollArea(c, "settings", k.Ground, 0, func() {
			ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(20, settingsPad, 64, settingsPad).Children(func() {
				ui.Column(c).Width(width).Gap(40).Children(func() { a.settingsSections(c, k, width) })
			})
		}).Grow(1)
		f.areaTop = area.Bounds().Y
	})
	a.followSections(c)
	a.confirmDialog(c, k)
}

// Settings' page, in DIPs: the navigation's width, the room either side of
// the sections, and the widest they grow.
const settingsNavWidth, settingsPad, settingsMaxWidth = 196, 28, 1160

// settingsSections are Settings' sections, one after another in the
// navigation's order: the status board, the backend's, then the app's. A
// backend that cannot be read says so once, in place of its sections.
func (a *App) settingsSections(c *ui.Context, k *Tokens, width float32) {
	s := a.Store
	s.WatchServer()
	s.WatchConfig()
	c.After(store.ServerEvery)
	down := s.Server.Read.IsZero() && a.unreachable(c, k)
	for g, group := range settingsNav {
		if g == len(settingsNav)-1 {
			label(c, k, "This app").Margin(8, 0, -24, 0)
		}
		for _, p := range group {
			switch p.id {
			case "connection":
				a.settingsSection(c, k, p, func() { a.connectionSettings(c, k) })
			case "appearance":
				a.settingsSection(c, k, p, func() { a.appearanceSettings(c, k) })
			case "about":
				a.settingsSection(c, k, p, func() { a.aboutSettings(c, k) })
			case "":
				if !down {
					a.settingsSection(c, k, p, func() { a.statusBoard(c, k, width) })
				}
			default:
				if !down {
					a.settingsSection(c, k, p, func() { a.backendSection(c, k, p.id) })
				}
			}
		}
	}
}

// settingsSection is one section of Settings' page: its name over its
// content. Its top is kept for the navigation.
func (a *App) settingsSection(c *ui.Context, k *Tokens, p settingsPage, body func()) {
	f := &a.set
	if f.tops == nil {
		f.tops = map[string]float32{}
	}
	sec := ui.Column(c.Key("section-" + p.id)).Gap(14)
	if b := sec.Bounds(); b.H > 0 {
		f.tops[p.id] = b.Y
	}
	sec.Children(func() {
		ui.Row(c).Height(28).Gap(10).AlignItems(ui.Center).Children(func() {
			ui.Icon(c, p.icon).FontSize(16).TextColor(k.Muted)
			display(c, p.label, 20).SingleLine()
		})
		body()
	})
}

// followSections keeps a pinned section at the top until the user scrolls,
// and else marks in the navigation the section at the top of the page.
func (a *App) followSections(c *ui.Context) {
	f := &a.set
	st := a.scrolls["settings"]
	if st == nil {
		return
	}
	if f.pin != "" {
		top, ok := f.tops[f.pin]
		switch {
		case f.pinY >= 0 && st.Y != f.pinY:
			// The user scrolled: the pin lets go.
			f.pin = ""
		case !ok:
			// Not laid out yet: the next frame places it.
			c.Invalidate()
			return
		default:
			y := max(0, min(st.Y+top-f.areaTop, st.MaxY))
			if y != st.Y {
				st.Y = y
				c.Invalidate()
			}
			f.pinY, f.page = st.Y, f.pin
			return
		}
	}
	if st.Scrolling() {
		return
	}
	cur := ""
	for _, group := range settingsNav {
		for _, p := range group {
			if top, ok := f.tops[p.id]; ok && top-f.areaTop <= 96 {
				cur = p.id
			}
		}
	}
	if st.MaxY > 0 && st.Y >= st.MaxY-1 && f.page != "" {
		// At the end the last sections cannot reach the top: the one asked
		// for keeps its mark.
		return
	}
	f.page = cur
}

func (a *App) settingsNavItem(c *ui.Context, k *Tokens, p settingsPage) {
	f := &a.set
	it := ui.ButtonBase(c.Key("nav-" + p.id)).Height(30).PaddingX(10).Gap(9).Radius(radiusControl).AlignItems(ui.Center).Label(p.label)
	col, w := k.Muted, 500
	if p.id == f.page {
		it.Background(k.Raise2)
		col, w = k.Fg, 600
	} else if it.Hovered() {
		it.Background(k.Raise)
	}
	if it.Clicked() {
		f.page, f.pin = p.id, ""
		if st, top := a.scrolls["settings"], f.tops[p.id]; st != nil {
			st.ScrollTo(0, max(0, min(st.Y+top-f.areaTop, st.MaxY)), ui.Smooth)
		}
	}
	it.Cursor(ui.CursorPointer).Children(func() {
		ui.Icon(c, p.icon).FontSize(14).TextColor(col)
		ui.Text(c, p.label).FontSize(13).FontWeight(w).TextColor(col).Grow(1).SingleLine()
		if ic, mark := a.navMark(k, p.id); ic != nil {
			ui.Icon(c, ic).FontSize(13).TextColor(mark)
		}
	})
}

// navMark is a page's mark in the navigation: the board and the section
// heads carry the values, so the navigation shows only what needs a look.
func (a *App) navMark(k *Tokens, page string) (*ui.SVG, ui.Color) {
	for key := range a.Store.Config.Failed {
		for i := range configFields {
			if configFields[i].key == key && configFields[i].section == page {
				return icCross, k.Bad
			}
		}
	}
	sv := &a.Store.Server
	if sv.Read.IsZero() {
		return nil, ui.Color{}
	}
	switch page {
	case "account":
		if st := accountState(sv.Wrapper, sv.WrapperErr); st.kind == pillBad {
			return icCross, k.Bad
		} else if st.problem != "" {
			return icWarn, k.Warn
		}
	case "server":
		if sv.Health == nil || sv.Health.Status != "ok" {
			return icWarn, k.Warn
		}
	}
	return nil, ui.Color{}
}

func (a *App) consoleURL() string {
	if a.Store.Client() == nil {
		return "/console/"
	}
	return a.Store.Client().Base() + "/console/"
}

// fieldBox is a one-line text input in the design's look: the box and the
// input inside it.
func fieldBox(c *ui.Context, k *Tokens, key string, value *string, placeholder string, monoFace bool) (box, in ui.Element) {
	box = ui.Row(c).Grow(1).Height(32).PaddingX(10).Radius(radiusControl).AlignItems(ui.Center).Background(k.Raise2).Border(1, k.Line2)
	box.Children(func() {
		in = ui.TextInputBase(c.Key(key), value).Grow(1).Placeholder(placeholder).Label(placeholder).FocusRing(false)
		if monoFace {
			in.Font(MonoFont).FontSize(12)
		} else {
			in.FontSize(13)
		}
	})
	if in.Focused() {
		focusRing(box, radiusControl)
	}
	return box, in
}

// field is fieldBox's input.
func field(c *ui.Context, k *Tokens, key string, value *string, placeholder string, monoFace bool) ui.Element {
	_, in := fieldBox(c, k, key, value, placeholder, monoFace)
	return in
}

// settingRow is a switch with its title and an explanation.
func settingRow(c *ui.Context, k *Tokens, key string, on *bool, title, help string) ui.Element {
	var sw ui.Element
	ui.Row(c).Gap(12).Padding(12, 16).Radius(radiusControl).Border(1, k.Line).AlignItems(ui.Center).Children(func() {
		sw = toggle(c, k, key, on, title)
		ui.Column(c).Grow(1).Gap(2).Children(func() {
			ui.Text(c, title).FontSize(13).FontWeight(600)
			ui.Text(c, help).FontSize(11.5).TextColor(k.Faint)
		})
	})
	return sw
}
