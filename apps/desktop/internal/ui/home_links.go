package ui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/store"
	"amdl/apps/desktop/internal/widgets"
)

// The dock, the glass bar that starts the pasted links.
const (
	dockHeight = 64
	// dockBottom is the room under it.
	dockBottom = 20
	// dockPad is the room inside it around its pill button.
	dockPad = 8
)

// linkCards preview the pasted links before they start: one card per link
// with its catalog cover and facts and whether it can start.
func (a *App) linkCards(c *ui.Context, k *Tokens, width float32) {
	const gap = 12
	// Two columns only where each card keeps room for its title beside its
	// status.
	cols := 2
	if width < 820 {
		cols = 1
	}
	cw := (width - gap*float32(cols-1)) / float32(cols)
	ui.Row(c).Width(width).Wrap().Gap(gap).Children(func() {
		for _, l := range a.Store.Omni.Links {
			a.linkCard(c, k, l, cw)
		}
	})
}

func (a *App) linkCard(c *ui.Context, k *Tokens, l *store.Link, width float32) {
	s := a.Store
	lk := l.Lookup
	ui.Row(c.Key(l.URL)).Width(width).Padding(padTile).PaddingEnd(10).Gap(12).Radius(radiusTile).AlignItems(ui.Center).
		Background(k.Raise).Border(1, k.Line).Children(func() {
		pal := ParsePalette(lk.ArtworkBgColor, lk.ArtworkTextColor1, lk.ArtworkTextColor2, lk.ArtworkTextColor3, lk.ArtworkTextColor4)
		if l.State == store.LinkReady {
			widgets.Cover{Image: a.Art.Get(lk.ArtworkURL, 128), Placeholder: pal.Bg, Size: 64, Radius: radiusControl, Color: 1}.Show(c)
		} else {
			ic := icLink
			if strings.Contains(l.URL, "/music-video/") {
				ic = icVideo
			}
			ui.Box(c).Size(64, 64).Radius(radiusControl).Background(k.Raise2).Center().Children(func() {
				ui.Icon(c, ic).FontSize(20).TextColor(k.Faint)
			})
		}
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(4).Children(func() {
			title, meta := linkText(l)
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, title).FontSize(13.5).FontWeight(600).Ellipsis("…").SingleLine().Shrink(1)
				if lk.ContentRating == "explicit" {
					explicitBadge(c, k)
				}
			})
			mono(c, meta, 11).TextColor(k.Faint).Ellipsis("…").SingleLine()
			if why := refused(s, l); why != "" {
				ui.Text(c, why).FontSize(12).TextColor(k.Bad).Ellipsis("…").SingleLine()
			}
		})
		switch {
		case l.State == store.LinkPending && s.Client() == nil:
			pill(c, k, pillWait, nil, "Not connected")
		case l.State == store.LinkPending:
			pill(c, k, pillWait, nil, "Looking up…")
		case l.State == store.LinkUnsupported:
			pill(c, k, pillBad, icCross, "Not supported")
		case l.State == store.LinkFailed:
			pill(c, k, pillBad, icCross, "Not found")
		case s.Running(l) != nil:
			pill(c, k, pillWarn, icWarn, "Already running")
		case refused(s, l) != "":
			pill(c, k, pillBad, icCross, "Refused")
		default:
			pill(c, k, pillOK, icCheck, "Ready")
		}
		rm := ui.ButtonBase(c.Key("remove")).Size(28, 28).Radius(radiusFull).Center().Label("Remove " + title(l))
		if rm.Hovered() {
			rm.Background(k.Track)
		}
		if rm.Clicked() {
			a.removeLink(l.URL)
		}
		rm.Cursor(ui.CursorPointer).Children(func() { ui.Icon(c, icCross).FontSize(13).TextColor(k.Muted) })
	})
}

func title(l *store.Link) string {
	t, _ := linkText(l)
	return t
}

// removeLink takes url out of the field, which keeps the other links one
// to a line.
func (a *App) removeLink(url string) {
	var rest []string
	for _, l := range a.Store.Omni.Links {
		if l.URL != url {
			rest = append(rest, l.URL)
		}
	}
	a.Store.SetOmniText(strings.Join(rest, "\n"))
}

// linkText is a link card's title and its line of facts.
func linkText(l *store.Link) (title, meta string) {
	lk := l.Lookup
	if l.State != store.LinkReady {
		u := l.URL
		if i := strings.Index(u, "music.apple.com/"); i >= 0 {
			u = u[i+len("music.apple.com/"):]
		}
		switch l.State {
		case store.LinkUnsupported:
			if strings.Contains(l.URL, "/music-video/") {
				return "Music video", u
			}
			return "Not a downloadable link", u
		case store.LinkFailed:
			return "Could not look it up", l.Err
		}
		return "Looking up…", u
	}
	parts := []string{}
	if by := firstNonEmptyStr(lk.ArtistName, lk.CuratorName); by != "" {
		parts = append(parts, by)
	}
	parts = append(parts, lk.Type)
	if lk.TrackCount > 0 {
		parts = append(parts, plural(lk.TrackCount, "track"))
	}
	parts = append(parts, lk.Storefront)
	return lk.Title, strings.Join(parts, " · ")
}

// refused is the backend's reason for not starting l in the last submit,
// or "".
func refused(s *store.Store, l *store.Link) string {
	r := s.Omni.Result
	if r == nil {
		return ""
	}
	for _, res := range r.Results {
		if res.URL == l.URL && res.Error != nil {
			return res.Error.Message
		}
	}
	return ""
}

// dockShown reports whether home shows the dock: while links are pasted.
func (a *App) dockShown() bool {
	return a.Screen == ScreenHome && len(a.Store.Omni.Links) > 0
}

// dock is the glass bar over home's cards: what will start on the left,
// the one button on the right.
func (a *App) dock(c *ui.Context, k *Tokens, width float32) {
	s := a.Store
	start := s.Startable()
	tracks, kinds := 0, map[string]int{}
	for _, l := range start {
		tracks += l.Lookup.TrackCount
		kinds[l.Lookup.Type]++
	}
	var running, unsupported int
	for _, l := range s.Omni.Links {
		switch {
		case l.State == store.LinkUnsupported || l.State == store.LinkFailed:
			unsupported++
		case s.Running(l) != nil:
			running++
		}
	}
	ui.Row(c.Key("dock-lane")).Absolute().Left(0).Right(0).Bottom(dockBottom).Justify(ui.Center).PassThrough().Children(func() {
		bar := ui.Row(c.Key("dock")).Width(width).Height(dockHeight).Padding(dockPad).PaddingStart(14).Gap(12).Radius(radiusFull).
			AlignItems(ui.Center).Material(glass.Glass{}).Border(1, k.Line)
		bar.Shadow(0, 18, 40, -14, ui.RGBA(0, 0, 0, 0.4))
		bar.Children(func() {
			ui.Row(c).Children(func() {
				for i, l := range start {
					if i == 3 {
						break
					}
					pal := ParsePalette(l.Lookup.ArtworkBgColor, "", "", "", "")
					cv := widgets.Cover{Image: a.Art.Get(l.Lookup.ArtworkURL, 72), Placeholder: pal.Bg, Size: 36, Radius: radiusControl, Color: 1}.Show(c)
					if i > 0 {
						cv.Margin(0, 0, 0, -12)
					}
					cv.Border(1.5, k.Raise)
				}
			})
			ui.Column(c).Shrink(1).MinWidth(0).Gap(1).Children(func() {
				ui.Text(c, summaryText(kinds, tracks)).FontSize(13.5).FontWeight(650).SingleLine().Ellipsis("…")
				var notes []string
				if running > 0 {
					notes = append(notes, fmt.Sprintf("%d already running", running))
				}
				if unsupported > 0 {
					notes = append(notes, fmt.Sprintf("%d not supported", unsupported))
				}
				if pending := len(s.Omni.Links) - len(start) - running - unsupported; pending > 0 {
					notes = append(notes, fmt.Sprintf("%d not ready", pending))
				}
				if len(notes) > 0 {
					mono(c, strings.Join(notes, " · "), 11).TextColor(k.Muted).SingleLine().Ellipsis("…")
				}
			})
			ui.Spacer(c)
			text := "Download"
			if n := len(start); n > 0 {
				text = "Download " + summaryNoun(kinds, n)
			}
			// A submit that answers within a frame shows no Starting… and no
			// dimmed button.
			wait := waited(c, s.Omni.Submitting, s.Omni.SubmitSince)
			if wait {
				text = "Starting…"
			}
			ready := len(start) > 0 && !wait
			// A minimum width keeps the button's edge still as its words change.
			b := ui.ButtonBase(c.Key("go")).Height(dockHeight - 2*dockPad).MinWidth(180).PaddingX(20).Gap(10).Radius(radiusFull).
				AlignItems(ui.Center).Justify(ui.Center).Background(k.Pill).Disabled(!ready).Label(text)
			if !ready {
				b.Opacity(0.45)
			} else if b.Hovered() {
				b.Opacity(0.92)
			}
			if b.Clicked() && !s.Omni.Submitting {
				s.Submit()
			}
			b.Cursor(ui.CursorPointer).Children(func() {
				ui.Text(c, text).FontSize(14).FontWeight(650).TextColor(k.PillInk).SingleLine()
				mono(c, submitLabel, 11).TextColor(k.PillInk.Alpha(0.6))
			})
		})
	})
}

// summaryText sums up what will start: "1 album · 12 tracks".
func summaryText(kinds map[string]int, tracks int) string {
	n := 0
	for _, v := range kinds {
		n += v
	}
	if n == 0 {
		return "Nothing ready to start"
	}
	s := summaryNoun(kinds, n)
	if tracks > 0 {
		s += " · " + plural(tracks, "track")
	}
	return s
}

// summaryNoun names n links by their type when they share one: "2 albums",
// else "3 links".
func summaryNoun(kinds map[string]int, n int) string {
	if len(kinds) == 1 {
		for t := range kinds {
			return plural(n, t)
		}
	}
	return plural(n, "link")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func firstNonEmptyStr(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
