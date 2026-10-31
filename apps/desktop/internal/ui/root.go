package ui

import (
	"runtime"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/store"
)

// shortcutLabel names the field's shortcut as the platform writes it.
var shortcutLabel = map[bool]string{true: "⌘L", false: "Ctrl L"}[runtime.GOOS == "darwin"]

// The main window's columns, in DIPs.
const (
	// sidebarWidth is the sidebar's width.
	sidebarWidth = 232
	// compactContent is the page width below which the pages use their
	// compact layout (the 820×560 board with the sidebar open).
	compactContent = 768
	// sidebarFits is the narrowest window the sidebar stays open in by
	// itself; a narrower one closes it until the user opens it there.
	sidebarFits = sidebarWidth + compactContent
	// sidebarSlide is how long the sidebar takes to open or close.
	sidebarSlide = 200 * time.Millisecond
)

// View builds the main window.
func (a *App) View(c *ui.Context) {
	k := a.tokens(c)
	applyTheme(c, k)
	a.shortcuts(c)
	a.set.login.settle(a.Store.Login.Busy)
	a.su.login.settle(a.Store.Login.Busy)
	w, _ := c.Size()
	a.followWidth(w)
	// The setup screen has the window to itself; the sidebar keeps the
	// user's choice for the screens after it.
	inSetup := a.Screen == ScreenSetup
	shown := a.SidebarShown() && !inSetup
	target := float32(0)
	if shown {
		target = sidebarWidth
	}
	a.compact = w-target < compactContent
	vib := c.Vibrancy()
	root := ui.Box(c).Fill()
	// The sidebar's width as it slides; the page takes the rest.
	side := root.AnimateWith("sidebar", target, sidebarSlide, ui.EaseOut)
	page := w - side
	root.Children(func() {
		ui.Row(c).Fill().AlignItems(ui.Stretch).Children(func() {
			a.sidebar(c, k, vib, side)
			main := ui.Box(c).Grow(1).Shrink(1).MinWidth(0).Background(k.Ground).Clip()
			main.Children(func() {
				// Absolute children paint above the flow, in the order they
				// are built, so the content is a layer of its own over the
				// backdrop.
				a.backdrop(c, k)
				ui.Column(c.Key("content")).Absolute().Top(0).Left(0).Right(0).Bottom(0).Children(func() {
					if inSetup {
						a.setupScreen(c, k)
						return
					}
					a.titleBar(c, k, shown)
					ui.Column(c).Grow(1).MinHeight(0).Children(func() {
						switch a.Screen {
						case ScreenSettings:
							a.settings(c, k)
						case ScreenDetail:
							a.detail(c, k)
						case ScreenAlbum:
							a.albumScreen(c, k, page)
						case ScreenQueue:
							a.queueScreen(c, k, a.compact)
						default:
							a.home(c, k, page)
						}
					})
				})
			})
		})
		if !inSetup {
			a.sidebarToggle(c, k)
		}
	})
	a.deleteDialog(c, k)
	a.toast(c, k)
	a.scrollbars(c, k)
	// Last: SetTheme, which scrollbars calls, gives the root the theme's
	// ground, which would cover the window's material under the sidebar.
	if vib {
		c.Root().Background(ui.Transparent)
	} else {
		c.Root().Background(k.Ground)
	}
}

// shortcuts are the window's keys: Cmd+1 home, Cmd+2 the queue, Cmd+,
// settings, Cmd+L home's field from any screen, and the sidebar's, which
// the View menu holds on macOS (sidebarKey).
func (a *App) shortcuts(c *ui.Context) {
	if runtime.GOOS != "darwin" {
		c.OnShortcut(sidebarMods, ui.KeyS, a.ToggleSidebar)
	}
	c.OnShortcut(ui.Cmd, ui.KeyL, a.FocusField)
	c.OnShortcut(ui.Cmd, ui.Key1, func() { a.Go(ScreenHome) })
	c.OnShortcut(ui.Cmd, ui.Key2, func() { a.Go(ScreenQueue) })
	c.OnShortcut(ui.Cmd, ui.KeyComma, func() { a.Go(ScreenSettings) })
	switch a.Screen {
	case ScreenDetail, ScreenAlbum:
		if c.Shortcut(0, ui.KeyEscape) {
			a.Back()
		}
	case ScreenHome:
		a.homeKeys(c)
	}
}

// titleBar is the content's top strip, which drags the window, clear of
// the window controls; with the sidebar closed it keeps the traffic lights
// and the sidebar button (sidebarToggle) on the content's own ground.
func (a *App) titleBar(c *ui.Context, k *Tokens, sidebar bool) {
	bar := c.TitleBar()
	right := float32(18)
	if bar.Right > 0 {
		right = bar.Right + 8
	}
	left := float32(18)
	if !sidebar {
		left = toggleRight(bar) + 10
	}
	ui.Row(c).Height(max(52, bar.Height)).PaddingStart(left).PaddingEnd(right).Gap(10).AlignItems(ui.Center).DragWindow().Children(func() {
		if a.Screen == ScreenDetail || a.Screen == ScreenAlbum {
			if button(c, k, buttonGhost, a.backLabel(), icBack).Clicked() {
				a.Back()
			}
		}
	})
}

// toast shows the store's latest notice for a few seconds.
func (a *App) toast(c *ui.Context, k *Tokens) {
	n := a.Store.Notice
	if n.Text == "" {
		return
	}
	age := c.Now().Sub(n.At)
	if age > 4*time.Second {
		return
	}
	c.After(4*time.Second - age)
	// Over home's dock, the toast rises above it.
	bottom := float32(24)
	if a.dockShown() {
		bottom = dockBottom + dockHeight + 12
	}
	ui.Overlay(c, func() {
		ui.Row(c).Attach(ui.AnchorBottom, ui.AnchorBottom).Bottom(bottom).
			PaddingX(14).PaddingY(9).Gap(8).Radius(radiusFull).AlignItems(ui.Center).
			Background(k.Pill).Shadow(0, 12, 32, 0, ui.RGBA(0, 0, 0, 0.35)).Children(func() {
			ic, col := icCheck, k.OK
			if n.Error {
				ic, col = icCross, k.Bad
			}
			ui.Icon(c, ic).FontSize(13).TextColor(col.Mix(k.PillInk, 0.2))
			ui.Text(c, n.Text).FontSize(12.5).FontWeight(600).TextColor(k.PillInk)
		})
	})
}

// jobPalette is the palette of the asset a job shows: its motion cover's
// while that plays, else the still cover's. Pairing a motion asset with the
// still's palette puts dark text on a dark video (domain.Job).
func jobPalette(j *store.Job, motion bool) Palette {
	if motion && j.MotionArtworkURL != "" && j.MotionArtworkBgColor != "" {
		return ParsePalette(j.MotionArtworkBgColor, j.MotionArtworkTextColor1, j.MotionArtworkTextColor2, j.MotionArtworkTextColor3, j.MotionArtworkTextColor4)
	}
	return ParsePalette(j.ArtworkBgColor, j.ArtworkTextColor1, j.ArtworkTextColor2, j.ArtworkTextColor3, j.ArtworkTextColor4)
}
