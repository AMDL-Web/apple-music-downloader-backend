package ui

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/mode"
	"amdl/apps/desktop/internal/store"
	"amdl/internal/client"
	"amdl/internal/format"
)

// skeleton is a shimmering placeholder bar.
func skeleton(c *ui.Context, k *Tokens, w, h float32) ui.Element {
	el := ui.Box(c).Height(h).Radius(radiusInner).Background(k.Track)
	if w > 0 {
		el.Width(w)
	} else {
		el.FillWidth()
	}
	el.Opacity(0.55 + 0.45*el.Loop("sk", 1400*time.Millisecond, ui.Bounce(ui.EaseInOut)))
	return el
}

// loadingState holds the queue's place while the first list loads: rows
// keep their height so nothing jumps when covers and titles arrive.
func (a *App) loadingState(c *ui.Context, k *Tokens) {
	if a.Act.StartErr() != "" {
		a.startFailed(c, k)
		return
	}
	ui.Column(c).Grow(1).Children(func() {
		ui.Row(c).Padding(18, 28).Gap(18).AlignItems(ui.Center).Children(func() {
			skeleton(c, k, 120, 120).Radius(radiusCover)
			ui.Column(c).Grow(1).Gap(10).Children(func() {
				skeleton(c, k, 140, 10)
				skeleton(c, k, 360, 22)
				skeleton(c, k, 0, 8)
			})
		})
		ui.Row(c).Padding(14, 18, 10, 18).Gap(10).AlignItems(ui.Center).Children(func() {
			display(c, "Queue", 22)
			text := "Connecting…"
			if a.Act.Starting() {
				text = "Starting the backend…"
			}
			pill(c, k, pillWait, nil, text)
		})
		for i := 0; i < 5; i++ {
			ui.Row(c.Key(i)).Padding(10, 18).Gap(14).AlignItems(ui.Center).BorderWidth(1, 0, 0, 0).BorderColor(k.Line).Children(func() {
				skeleton(c, k, 44, 44)
				ui.Column(c).Grow(1).Gap(7).Children(func() {
					skeleton(c, k, float32(120+i*37%140), 10)
					skeleton(c, k, 0, 5)
				})
			})
		}
	})
}

// offlineBanner says the backend stopped answering, when it is tried next,
// and how old the list below it is.
func (a *App) offlineBanner(c *ui.Context, k *Tokens) {
	s := a.Store
	where := hostOf(a.Settings.RemoteURL)
	name := "the server"
	if a.Settings.Mode == mode.Standalone {
		name, where = "the backend", "this computer"
	}
	ui.Column(c).Margin(10, 18, 0, 18).Padding(padCard).Gap(8).Radius(radiusCard).Background(k.BadSoft).Children(func() {
		ui.Text(c, "Can’t reach "+name).FontWeight(600).TextColor(k.Bad)
		msg := "No answer from " + where + "."
		if wait := s.Conn.RetryAt.Sub(c.Now()); wait > 0 {
			msg += fmt.Sprintf(" Trying again in %d s.", int(wait.Seconds()+0.99))
			c.After(time.Second)
		}
		if !s.LastLive.IsZero() {
			msg += " The list below is from " + format.Clock(s.LastLive) + " and may be out of date."
		}
		ui.Text(c, msg).FontSize(12.5)
		ui.Row(c).Gap(8).Children(func() {
			if button(c, k, buttonPlain, "Change server", nil).Clicked() {
				a.GoSettings("connection")
			}
		})
	})
}

// startFailed explains why the standalone backend did not start.
func (a *App) startFailed(c *ui.Context, k *Tokens) {
	ui.Column(c).Margin(18).Padding(padCard).Gap(8).Radius(radiusCard).Background(k.BadSoft).Children(func() {
		ui.Text(c, "The backend on this computer did not start").FontWeight(600).TextColor(k.Bad)
		ui.Text(c, a.Act.StartErr()).FontSize(12.5).Font(MonoFont)
		if button(c, k, buttonPlain, "Check setup", nil).AlignSelf(ui.Start).Clicked() {
			a.OpenSetup(a.Settings.Mode)
		}
	})
}

// narrowTab reports whether the queue shows only finished or only failed
// jobs, where an empty list says nothing about what downloads.
func (a *App) narrowTab() bool {
	t := store.Tab(a.Tab)
	return t == store.TabDone || t == store.TabFailed
}

// emptyQueue is the first launch, or a tab with nothing in it. quiet leaves
// out the drawing, under a hero that already holds the eye.
func (a *App) emptyQueue(c *ui.Context, k *Tokens, quiet bool) {
	if quiet {
		ui.Column(c).Grow(1).Center().Gap(6).Padding(24).Children(func() {
			if a.narrowTab() {
				ui.Text(c, "Nothing here").FontSize(15).FontWeight(650)
				return
			}
			ui.Text(c, "Nothing downloading").FontSize(15).FontWeight(650)
			ui.Text(c, "Paste a link or search Apple Music on Home ("+shortcutLabel+").").
				FontSize(12.5).TextColor(k.Muted).TextAlign(ui.Center).MaxWidth(360)
		})
		return
	}
	ui.Column(c).Grow(1).Center().Gap(14).Padding(24).Children(func() {
		stack := ui.Box(c).Size(150, 110)
		stack.Children(func() {
			for i, x := range []float32{0, 32, 64} {
				card := ui.Box(c.Key(i)).Size(86, 86).Radius(radiusControl).Absolute().Left(x).Top([]float32{18, 6, 18}[i]).Rotate([]float32{-8, 0, 8}[i])
				if i == 2 {
					card.LinearGradient(ui.LinearGradient{From: ui.Hex("#7d93bd"), To: ui.Hex("#05104b"), Angle: 160})
				} else {
					card.Border(1.5, k.Line2).BorderStyle(ui.BorderDashed)
				}
			}
		})
		if a.narrowTab() {
			display(c, "Nothing here", 24)
			return
		}
		display(c, "Nothing downloading", 24)
		ui.Text(c, "Copy an album, playlist or song link from Apple Music and press "+shortcutLabel+", or paste it on Home.").
			FontSize(13).TextColor(k.Muted).TextAlign(ui.Center).MaxWidth(300)
		if button(c, k, buttonPrimary, "New download", nil).Height(36).PaddingX(16).Clicked() {
			a.FocusField()
		}
	})
}

// unreachable stands in for a screen that reads the backend and has read
// nothing yet, when the backend did not start or does not answer. It
// reports whether it showed anything.
func (a *App) unreachable(c *ui.Context, k *Tokens) bool {
	s := a.Store
	switch {
	case a.Act.StartErr() != "":
		a.startFailed(c, k)
	case s.Client() == nil:
		ui.Column(c).Grow(1).Center().Gap(12).Children(func() {
			text := "Not connected"
			if a.Act.Starting() {
				text = "Starting the backend…"
			}
			pill(c, k, pillWait, nil, text)
			if a.Settings.Mode == "" && button(c, k, buttonPrimary, "Set up AMDL", nil).Clicked() {
				a.OpenSetup("")
			}
		})
	case s.Conn.Phase == client.Offline:
		a.offlineBanner(c, k)
	default:
		return false
	}
	return true
}
