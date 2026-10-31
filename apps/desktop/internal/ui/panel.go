package ui

import (
	"fmt"
	"runtime"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/mode"
	"amdl/apps/desktop/internal/store"
	"amdl/apps/desktop/internal/widgets"
	"amdl/internal/client"
	"amdl/internal/domain"
	"amdl/internal/format"
)

// PanelWidth is the tray panel's width in DIPs.
const PanelWidth = 340

// panelMiniRows bounds the other jobs the panel lists under the album
// downloading now.
const panelMiniRows = 2

// Panel builds the tray icon's panel: how many downloads run, the album
// downloading now, today's count, the free space, the next jobs, and the
// way into the app.
func (a *App) Panel(c *ui.Context) {
	k := a.tokens(c)
	applyTheme(c, k)
	s := a.Store
	s.WatchServer()
	c.After(store.ServerEvery)
	c.Root().Background(ui.Transparent)
	box := ui.Column(c).Padding(padCard).Gap(10)
	switch {
	case c.Vibrancy(): // the popover's material on macOS
	case runtime.GOOS == "windows": // which rounds and outlines the flyout
		c.Root().Background(k.Raise)
		box.Background(k.Raise)
	default:
		box.Background(k.Raise).Radius(radiusCard).Border(1, k.Line2)
	}
	sum := s.Summary()
	box.Children(func() {
		a.panelHead(c, k, sum)
		if sum.Now != nil {
			a.panelChips(c, k, sum)
			a.panelNow(c, k, sum.Now)
		}
		ui.Grid(c).Columns(2).Gap(8).Children(func() {
			smallCard(c, k, "today", "Today", func() {
				bigValue(c, k, fmt.Sprint(sum.TracksToday), "tracks", 22)
				mono(c, plural(sum.CompletedToday, "download")+" finished", 10.5).TextColor(k.Muted)
			})
			title := "Disk"
			if a.Settings.Mode == mode.Remote {
				title = "Server disk"
			}
			smallCard(c, k, "disk", title, func() {
				st := s.Server.DownloadsStorage()
				if st == nil || st.TotalBytes == 0 {
					bigValue(c, k, "—", "", 22)
					return
				}
				v, unit := format.Scale(float64(st.FreeBytes))
				bigValue(c, k, format.Number(v, 2), unit+" free", 22)
				meter(c, k, 1-float64(st.FreeBytes)/float64(st.TotalBytes), 5)
			})
		})
		shown := 0
		for _, j := range s.Jobs {
			if shown == panelMiniRows {
				break
			}
			if j == sum.Now || (j.Status != domain.JobRunning && j.Status != domain.JobQueued) {
				continue
			}
			a.panelMini(c, k, j)
			shown++
		}
		ui.Row(c).Gap(8).Margin(2, 0, 0, 0).Children(func() {
			if button(c, k, buttonPlain, "Paste link "+shortcutLabel, nil).Grow(1).Clicked() {
				a.Act.ShowWindow()
				a.FocusField()
			}
			b := ui.ButtonBase(c.Key("open")).Grow(1).Height(30).Radius(radiusControl).AlignItems(ui.Center).Justify(ui.Center).Background(k.Pill)
			if b.Clicked() {
				a.Act.ShowWindow()
			}
			b.Cursor(ui.CursorPointer).Children(func() {
				ui.Text(c, "Open AMDL").FontSize(12.5).FontWeight(700).TextColor(k.PillInk)
			})
		})
	})
	// The flyout does not size itself to native content: fit it to what
	// the last frame laid out. Bounds are a frame late, so the frame that
	// lays out another height asks for the next one as it paints: the
	// store's change may be the last frame the panel gets for seconds.
	if h := box.Bounds().H; h > 0 && h != a.panelH {
		a.panelH = h
		a.Act.FitPanel(h)
	}
	box.Draw(func(_ *ui.Painter, r ui.Rect) {
		if r.H != a.panelH {
			c.Invalidate()
		}
	})
}

func (a *App) panelHead(c *ui.Context, k *Tokens, sum store.Summary) {
	ui.Row(c).Gap(8).AlignItems(ui.Baseline).Children(func() {
		switch {
		case sum.Running > 0:
			bigValue(c, k, fmt.Sprint(sum.Running), "", 30)
			ui.Text(c, "downloading").FontWeight(600)
		case sum.Queued > 0:
			bigValue(c, k, fmt.Sprint(sum.Queued), "", 30)
			ui.Text(c, "queued").FontWeight(600)
		default:
			display(c, "All done", 24).SingleLine()
		}
		ui.Spacer(c)
		where := "Server"
		if a.Settings.Mode == mode.Standalone {
			where = "Local"
		}
		switch a.Store.Conn.Phase {
		case client.Live:
			pill(c, k, pillOK, icCheck, where+" live")
		case client.Offline:
			pill(c, k, pillBad, icCross, where+" offline")
		default:
			pill(c, k, pillWait, nil, "Connecting…")
		}
	})
}

func (a *App) panelChips(c *ui.Context, k *Tokens, sum store.Summary) {
	j := sum.Now
	ui.Row(c).Gap(6).Wrap().Children(func() {
		if sp := j.AvgSpeed(c.Now()); sp > 0 {
			chip(c, k, format.Rate(sp), ui.Color{})
		}
		if r := format.Remaining(j.Remaining); r != "" {
			chip(c, k, r+" left", ui.Color{})
		}
		if q := jobQuality(j); q != "" {
			chip(c, k, q, ui.Color{})
		}
		if sum.FailedToday > 0 {
			chip(c, k, fmt.Sprintf("%d failed today", sum.FailedToday), k.Bad)
		}
	})
}

// panelNow is the album downloading now, on a wash of its own colors.
func (a *App) panelNow(c *ui.Context, k *Tokens, j *store.Job) {
	pal := jobPalette(j, false)
	accent := AccentFor(pal, k)
	t1, t2 := k.Fg, k.Muted
	card := ui.ButtonBase(c.Key("now")).Padding(8).Gap(12).Radius(radiusControl).AlignItems(ui.Center).Justify(ui.Start).Label("Downloading now")
	if pal.OK {
		deep := pal.Bg.Mix(ui.Hex("#05104b"), 0.6)
		card.LinearGradient(ui.LinearGradient{Angle: 135, Stops: []ui.GradientStop{{Color: pal.Bg.Alpha(0.55), At: 0}, {Color: deep.Alpha(0.55), At: 1}}})
		under := k.Raise.Mix(pal.Bg, 0.55)
		t1, t2 = Readable(pal.Text1, under, k), Readable(pal.Text2, under, k)
		accent = Readable(pal.Text1, under, k).Mix(accent, 0.3)
	} else {
		card.Background(k.Raise2)
	}
	if card.Clicked() {
		a.Act.ShowWindow()
		a.OpenJob(j.ID)
	}
	card.Cursor(ui.CursorPointer).Children(func() {
		widgets.Cover{Image: a.Art.Get(j.ArtworkURL, 112), Placeholder: pal.Bg, Size: 56, Radius: radiusInner, Color: 1}.Show(c)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(5).Children(func() {
			ui.Text(c, j.Name()).FontSize(14).FontWeight(700).TextColor(t1).SingleLine().Ellipsis("…")
			mono(c, j.By(), 10.5).TextColor(t2).Ellipsis("…")
			albumProgress(c, k, j, "panel-now", 5, t1, t1.Alpha(0.18), accent, t2, 10.5)
		})
	})
}

// panelMini is one more running or queued job, small.
func (a *App) panelMini(c *ui.Context, k *Tokens, j *store.Job) {
	pal := jobPalette(j, false)
	queued := j.Status == domain.JobQueued
	ui.Row(c.Key(j.ID)).Gap(10).AlignItems(ui.Center).Children(func() {
		color, dim := float32(0.65), float32(0)
		if queued {
			color, dim = 0, 0.2
		}
		widgets.Cover{Image: a.Art.Get(j.ArtworkURL, 64), Placeholder: pal.Bg, Size: 32, Radius: radiusInner, Color: color, Dim: dim}.Show(c)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, j.Name()).FontSize(12.5).FontWeight(600).SingleLine().Ellipsis("…")
			ui.Row(c).Children(func() {
				mono(c, j.By(), 10.5).TextColor(k.Muted).Shrink(1).Ellipsis("…")
				ui.Spacer(c)
				state := "queued"
				if !queued {
					state = fmt.Sprintf("%d/%d · %d%%", j.Done(), j.TotalItems, j.Percent())
				}
				mono(c, state, 10.5).TextColor(k.Muted)
			})
			widgets.StageBar{Stages: j.Stages, Color: AccentFor(pal, k), Track: k.Track, Height: 3, Running: !queued}.Show(c, "mini")
		})
	})
}

// smallCard is one of the panel's two small cards.
func smallCard(c *ui.Context, k *Tokens, key, title string, body func()) {
	ui.Column(c.Key(key)).Padding(10, 12).Gap(6).Radius(radiusControl).Background(k.Fg.Alpha(0.05)).MinWidth(0).Children(func() {
		ui.Text(c, title).FontSize(11).FontWeight(600).TextColor(k.Muted)
		body()
	})
}
