package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/mode"
	"amdl/apps/desktop/internal/store"
	"amdl/apps/desktop/internal/widgets"
	"amdl/internal/format"
)

// wallTile is a cover's size in the row of recent downloads, wallCompact
// its size in a narrow window, and wallGap the space between covers.
const wallTile, wallCompact, wallGap = 64, 44, 12

// latestMotion is the motion cover the latest download's hero plays: the
// 3:4 loop where the album has one, else the square one.
func latestMotion(j *store.Job) (master string, tall bool) {
	if j.MotionArtworkTallURL != "" {
		return j.MotionArtworkTallURL, true
	}
	return j.MotionArtworkURL, false
}

// latestPalette is the palette of what the latest download's hero shows:
// its motion cover's while that plays, else the still's.
func latestPalette(j *store.Job, playing bool) Palette {
	_, tall := latestMotion(j)
	switch {
	case !playing:
		return jobPalette(j, false)
	case tall:
		return tallPalette(j)
	}
	return jobPalette(j, true)
}

// latestHero shows the download that finished last while nothing
// downloads, where the "downloading now" hero goes and in its colors: the
// 3:4 motion loop, and how much landed where the percent would be.
func (a *App) latestHero(c *ui.Context, k *Tokens, j *store.Job, compact bool) {
	a.Store.WantTracks(j)
	size, height := float32(196), float32(0)
	var video *ui.VideoPlayer
	if compact {
		size = 88
	} else {
		master, tall := latestMotion(j)
		if video = a.motionPlayer(c, master, 180); video != nil && tall {
			size, height = 180, 240
		}
	}
	pal := latestPalette(j, video != nil)
	hc := heroColorsFor(pal, k)
	ui.Row(c).Padding(18, 28, 20, 28).Gap(28).AlignItems(ui.End).Children(func() {
		a.heroCover(c, j, func() {
			cover := widgets.Cover{Image: a.Art.Get(j.ArtworkURL, int(size*2)), Placeholder: pal.Bg, Size: size, Height: height, Radius: radiusCover, Color: 1, Shadow: true, Video: video}.Show(c)
			switch {
			case compact:
			case height > 0:
				cover.Children(func() { motionBadge(c, "3:4 LOOP") })
			case j.MotionArtworkURL != "":
				cover.Children(func() { motionBadge(c, "MOTION") })
			}
		})
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(10).Children(func() {
			when := ""
			if j.FinishedAt != nil {
				when = " · finished " + dayClock(*j.FinishedAt, c.Now())
			}
			label(c, k, "Latest download"+when).TextColor(hc.label)
			title := display(c, j.Name(), 42).TextColor(hc.title).MaxLines(2).Ellipsis("…")
			if compact {
				title.FontSize(24).LetterSpacing(-0.48).MaxLines(1)
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, j.By()).FontSize(15).TextColor(hc.meta).SingleLine().Shrink(1)
				if q := jobQuality(j); q != "" {
					tag(c, k, q, isHiRes(q), hc.meta)
				}
				if j.TotalItems > 0 {
					tag(c, k, plural(j.TotalItems, "track"), false, hc.meta)
				}
			})
		})
		if compact {
			return
		}
		ui.Column(c).AlignItems(ui.End).Gap(10).Children(func() {
			if b := j.Bytes(); b > 0 {
				num, unit, _ := strings.Cut(format.Bytes(b), " ")
				ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
					display(c, num, 52).LetterSpacing(-1.5).TextColor(hc.title).FontFeatures("tnum")
					ui.Text(c, unit).FontSize(20).FontWeight(700).TextColor(hc.meta)
				})
			}
			ui.Row(c).Gap(8).Children(func() {
				dir := ""
				if a.Settings.Mode == mode.Standalone {
					dir = savedDir(j)
				}
				if dir == "" {
					if artButton(c, "Details", hc.title, hc.ink).Clicked() {
						a.OpenJob(j.ID)
					}
					return
				}
				if button(c, k, buttonOnArt, "Details", nil).Clicked() {
					a.OpenJob(j.ID)
				}
				if artButton(c, revealLabel, hc.title, hc.ink).Clicked() {
					a.Act.Reveal(dir)
				}
			})
		})
	})
}

// recentWall is the downloads that finished before the hero's, newest
// first, as one row of covers as wide as the screen. A cover opens its
// detail, and its motion cover plays under the pointer.
func (a *App) recentWall(c *ui.Context, k *Tokens, finished []*store.Job, hero *store.Job, compact bool) {
	// A download run again shows its cover once.
	seen := map[string]bool{}
	if hero != nil {
		seen[coverKey(hero)] = true
	}
	var jobs []*store.Job
	for _, j := range finished {
		if key := coverKey(j); !seen[key] {
			seen[key] = true
			jobs = append(jobs, j)
		}
	}
	if len(jobs) == 0 {
		return
	}
	size, side := float32(wallTile), float32(232)
	if compact {
		size, side = wallCompact, 60
	}
	w, _ := c.Size()
	n := min(len(jobs), max(1, int((w-side-56+wallGap)/(size+wallGap))))
	ui.Column(c).Padding(0, 28, 10, 28).Gap(8).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			label(c, k, "Recently finished")
			ui.Spacer(c)
			shown := n
			if hero != nil {
				shown++
			}
			if done := a.Store.Count(store.TabDone); done > shown {
				more := ui.ButtonBase(c.Key("wall-all")).Label("Show every finished download")
				// On the Done tab the link keeps its room, so the row's height
				// does not change with the tab.
				if store.Tab(a.Tab) == store.TabDone {
					more.Invisible()
				}
				col := k.Muted
				if more.Hovered() {
					col = k.Fg
				}
				if more.Clicked() {
					a.Tab = int(store.TabDone)
				}
				more.Cursor(ui.CursorPointer).Children(func() {
					mono(c, fmt.Sprintf("All %d in Done →", done), 11).TextColor(col)
				})
			}
		})
		ui.Row(c).Gap(wallGap).Padding(2, 0, 4, 0).Children(func() {
			for _, j := range jobs[:n] {
				a.wallCover(c, k, j, size)
			}
		})
	})
}

func coverKey(j *store.Job) string { return j.Type + "\x00" + j.Name() + "\x00" + j.By() }

// wallCover is one cover of the recent row, lifted and playing its motion
// cover while hovered.
func (a *App) wallCover(c *ui.Context, k *Tokens, j *store.Job, size float32) {
	t := ui.ButtonBase(c.Key(j.ID)).Size(size, size).Label(j.Name())
	hover := t.Hovered()
	lift := t.AnimateTo("lift", map[bool]float32{true: 1, false: 0}[hover], ui.Snappy)
	if t.Clicked() {
		a.OpenJob(j.ID)
	}
	var video *ui.VideoPlayer
	if hover {
		video = a.motionPlayer(c, j.MotionArtworkURL, size)
	}
	t.Cursor(ui.CursorPointer).Tooltip(j.Name() + " — " + j.By()).Children(func() {
		frame := ui.Box(c).Size(size, size).Radius(radiusControl).Scale(1+0.06*lift, 1+0.06*lift)
		if lift > 0.01 {
			frame.Shadow(0, 10*lift, 24*lift, 0, ui.RGBA(0, 0, 0, 0.45*lift))
		}
		frame.Children(func() {
			widgets.Cover{Image: a.Art.Get(j.ArtworkURL, int(size*2)), Placeholder: jobPalette(j, false).Bg, Size: size, Radius: radiusControl, Color: 1, Video: video}.Show(c)
			if lift > 0.01 {
				ui.Box(c).Fill().Radius(radiusControl).Absolute().Top(0).Left(0).Border(2, k.Accent.Alpha(lift)).PassThrough()
			}
			if j.MotionArtworkURL != "" && video == nil {
				ui.Box(c).Size(16, 16).Radius(radiusFull).Background(ui.RGBA(10, 12, 20, 0.55)).Absolute().Right(5).Top(5).Center().Children(func() {
					ui.Icon(c, icPlay).FontSize(7).TextColor(ui.Hex("#f2f3f8"))
				})
			}
		})
	})
}

// dayClock says when something happened: "today 10:43", "yesterday
// 22:05", else the date.
func dayClock(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	same := func(a, b time.Time) bool {
		ay, am, ad := a.Date()
		by, bm, bd := b.Date()
		return ay == by && am == bm && ad == bd
	}
	switch {
	case same(t, now):
		return "today " + format.Clock(t)
	case same(t, now.AddDate(0, 0, -1)):
		return "yesterday " + format.Clock(t)
	case t.Year() == now.Year():
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2, 2006")
}
