package ui

import (
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/store"
	"amdl/apps/desktop/internal/widgets"
	"amdl/internal/domain"
	"amdl/internal/format"
	"amdl/internal/progress"
)

// detail is one job: the album in big type over its palette, its numbers,
// every track's stage bar, and what happened, newest first.
func (a *App) detail(c *ui.Context, k *Tokens) {
	j := a.Store.Detail()
	if j == nil {
		a.Go(ScreenQueue)
		return
	}
	a.detailJob(c, k, j)
}

// detailJob builds the detail screen of j.
func (a *App) detailJob(c *ui.Context, k *Tokens, j *store.Job) {
	page := a.pageWidth(c)
	_, height := c.Size()
	// A narrow or short window keeps room for the tracks under a smaller
	// head.
	coverSize, titleSize, padTop, gap := float32(248), float32(52), float32(26), float32(32)
	if page < 960 || height < 700 {
		coverSize, titleSize, padTop, gap = 148, 34, 12, 24
	}
	video := a.motionPlayer(c, j.MotionArtworkURL, coverSize)
	pal := jobPalette(j, video != nil)
	hc := heroColorsFor(pal, k)
	ui.Row(c).Padding(padTop, 32, 22, 32).Gap(gap).AlignItems(ui.End).Children(func() {
		cover := widgets.Cover{Image: a.Art.Get(j.ArtworkURL, 512), Placeholder: pal.Bg, Size: coverSize, Radius: radiusCover, Color: 1, Shadow: true, Video: video}.Show(c)
		if j.MotionArtworkURL != "" {
			cover.Children(func() { motionBadge(c, "MOTION") })
		}
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(12).Children(func() {
			kind := strings.ToUpper(j.Type[:min(1, len(j.Type))]) + j.Type[min(1, len(j.Type)):]
			if j.TotalItems > 0 {
				kind += " · " + plural(j.TotalItems, "track")
			}
			label(c, k, kind).TextColor(hc.label)
			display(c, j.Name(), titleSize).TextColor(hc.title).MaxLines(2).Ellipsis("…")
			ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Text(c, j.By()).FontSize(17).TextColor(hc.title.Mix(hc.meta, 0.4)).SingleLine()
				if it := firstWithCodec(j); it != nil {
					q := format.LongQuality(it.Codec, it.BitDepth, it.SampleRate)
					tag(c, k, q, isHiRes(format.Quality(it.Codec, it.BitDepth, it.SampleRate, 0)), hc.meta)
				}
				if b := j.Bytes(); b > 0 {
					tag(c, k, plural(j.TotalItems, "track")+" · "+format.Bytes(b), false, hc.meta)
				}
			})
			ui.Row(c).Gap(12).MaxWidth(620).Margin(8, 0, 0, 0).Children(func() {
				stat := func(name, value, unit string) {
					ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
						label(c, k, name).TextColor(hc.meta.Alpha(0.8))
						ui.Row(c).AlignItems(ui.Baseline).Children(func() {
							mono(c, value, 22).FontWeight(600).TextColor(hc.title)
							if unit != "" {
								mono(c, unit, 14).TextColor(hc.meta.Alpha(0.8))
							}
						})
					})
				}
				stat("Done", fmt.Sprint(j.Done()), fmt.Sprintf(" / %d", j.TotalItems))
				if sp := j.AvgSpeed(c.Now()); sp > 0 {
					v, u := format.Scale(sp)
					stat("Avg speed", format.Number(v, 1), " "+u+"/s")
				} else {
					stat("Avg speed", "—", "")
				}
				if r := format.Remaining(j.Remaining); r != "" {
					stat("Left", r, "")
				} else if j.FinishedAt != nil {
					stat("Finished", format.Clock(*j.FinishedAt), "")
				} else {
					stat("Left", "—", "")
				}
				if j.StartedAt != nil {
					stat("Started", format.Clock(*j.StartedAt), "")
				} else {
					stat("Created", format.Clock(j.CreatedAt), "")
				}
			})
			ui.Column(c).FillWidth().MaxWidth(620).Margin(6, 0, 0, 0).Children(func() {
				albumProgress(c, k, j, "album", 8, hc.title, hc.title.Alpha(0.16), hc.accent, hc.meta, 12)
			})
		})
		ui.Column(c).Gap(8).Children(func() {
			if dir := a.revealDir(j); dir != "" && button(c, k, buttonOnArt, revealLabel, nil).Clicked() {
				a.Act.Reveal(dir)
			}
			switch j.Status {
			case domain.JobQueued, domain.JobRunning:
				if button(c, k, buttonOnArt, "Cancel", nil).Clicked() {
					a.Store.Cancel(j.ID)
				}
			case domain.JobFailed, domain.JobCancelled:
				if button(c, k, buttonOnArt, "Retry", nil).Clicked() {
					a.Store.Retry(j.ID)
				}
			}
			if j.Status.IsTerminal() && button(c, k, buttonOnArt, "Delete", nil).Label("Delete this download").Clicked() {
				a.askDelete(j)
			}
		})
	})
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Background(k.Ink.Alpha(detailInk)).BorderWidth(1, 0, 0, 0).BorderColor(k.Line).Children(func() {
		activity := float32(280)
		if page < 1000 {
			activity = 240
		}
		a.trackList(c, k, j, hc.accent, stageWidth(page))
		a.activity(c, k, j, activity)
	})
}

// albumProgress is a job's album bar with how many tracks finished under
// its left end and the overall percent under its right. ink is the text
// color the bar's live fill is drawn from.
func albumProgress(c *ui.Context, k *Tokens, j *store.Job, key string, h float32, ink, track, working, text ui.Color, size float32) {
	a := j.Album()
	lit, total := a.Lit, a.Total
	if total == 0 {
		// Before the tracks arrive the list feed still has the counts.
		lit, total = j.Done(), j.TotalItems
	}
	resolving := total == 0 && (j.Status == domain.JobQueued || j.Status == domain.JobRunning)
	ui.Column(c).Gap(5).Children(func() {
		widgets.AlbumBar{Album: a, Completed: k.OK, Skipped: k.Skip, Failed: k.Bad, Cancelled: k.Muted, Working: working,
			Live: ink.Alpha(0.38), Track: track, Resolving: resolving, Height: h}.Show(c, key)
		ui.Row(c).Gap(8).Children(func() {
			switch {
			case resolving:
				mono(c, "Resolving tracks", size).TextColor(text)
			default:
				mono(c, fmt.Sprintf("%d / %d finished", lit, total), size).TextColor(text)
				ui.Spacer(c)
				mono(c, fmt.Sprintf("%d%%", a.Percent), size).TextColor(text)
			}
		})
	})
}

func firstWithCodec(j *store.Job) *domain.JobItem {
	for i := range j.Items {
		if j.Items[i].Codec != "" {
			return &j.Items[i]
		}
	}
	return nil
}

// savedDir is the folder a job's tracks went to, from the first track's
// output path, with the separator of the machine that wrote it: a remote
// backend's paths are its own, not this computer's.
func savedDir(j *store.Job) string {
	for _, it := range j.Items {
		if p := it.OutputPath; p != "" {
			if strings.Contains(p, "/") && !strings.Contains(p, `\`) {
				return path.Dir(p) + "/"
			}
			return filepath.Dir(p) + `\`
		}
	}
	return ""
}

// The track list's columns and rows, in DIPs.
const (
	// trackStateWidth is the state column's width, which its longest label
	// ("Preparing", "Cancelled") fits.
	trackStateWidth = 104
	// trackInset is the room between a row's hairline and its highlight,
	// so two highlighted rows stand apart; the highlight's own padding
	// keeps the row as tall as an idle one.
	trackInset = 3
	trackPadY  = 11 - trackInset
	// legendSize is the stage legend's type size, and legendAdvance a
	// character's width in it: JetBrains Mono advances 600 of 1000 units.
	legendSize    = 10
	legendAdvance = 0.6 * legendSize
	// legendAir is the least room an initial keeps before the next
	// label, and legendSpill how far the last one may run past its
	// segment, into the row's gap.
	legendAir   = 3
	legendSpill = 8
	// stageGap is the room between the stage bar's segments, the default
	// of widgets.StageBar.
	stageGap = 2
)

// pageWidth is the width of the page beside the sidebar.
func (a *App) pageWidth(c *ui.Context) float32 {
	w, _ := c.Size()
	if a.SidebarShown() {
		w -= sidebarWidth
	}
	return w
}

// stageWidth is the width of the stage bars on a page width wide: all of
// 300 on a wide page, less as the page narrows, so the titles keep room.
func stageWidth(page float32) float32 {
	return min(max((page-560)*0.45, 160), 300)
}

// stageWords name the stage bar's segments in its legend.
var stageWords = [progress.StageCount]string{"Resolve", "Download", "Decrypt", "Remux", "Verify", "Tag", "Save"}

// stageInitials stand for the words where a segment is too narrow: M for
// remux, as R is resolve.
var stageInitials = [progress.StageCount]string{"R", "D", "D", "M", "V", "T", "S"}

// legendLabel is one segment's label in the stage legend, at the
// segment's left edge, and the segment's width.
type legendLabel struct {
	text  string
	width float32
}

// stageLegend lays the legend over a stage bar width wide, as
// widgets.StageBar lays its segments under progress.Weights: a segment
// shows its word where the word fits, else its initial where that fits in
// the segment and the gap after it with a little air, else nothing, so
// labels never meet whatever the weights. The last segment's initial may
// run into the row's gap before the state column.
func stageLegend(width float32, weights [progress.StageCount]float64) [progress.StageCount]legendLabel {
	var out [progress.StageCount]legendLabel
	avail := width - stageGap*float32(progress.StageCount-1)
	for i, w := range weights {
		sw := max(avail*float32(w), 3)
		out[i].width = sw
		room := sw + stageGap
		if i == len(weights)-1 {
			room = sw + legendSpill
		}
		switch {
		case float32(len(stageWords[i]))*legendAdvance <= sw:
			out[i].text = stageWords[i]
		case legendAdvance+legendAir <= room:
			out[i].text = stageInitials[i]
		}
	}
	return out
}

func (a *App) trackList(c *ui.Context, k *Tokens, j *store.Job, accent ui.Color, bar float32) {
	ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Padding(10, 24, 0, 32).Children(func() {
		ui.Row(c).Gap(16).Padding(11, 4, 4, 4).Margin(0, scrollGutter, 0, 0).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Width(28)
			label(c, k, "Track").Grow(1)
			ui.Row(c).Width(bar).Gap(stageGap).Children(func() {
				for _, l := range stageLegend(bar, progress.Weights) {
					// The label may be wider than a narrow segment, by
					// no more than stageLegend allows.
					ui.Row(c).Width(l.width).Children(func() {
						if l.text != "" {
							mono(c, l.text, legendSize).TextColor(k.Faint).Shrink(0)
						}
					})
				}
			})
			label(c, k, "State").Width(trackStateWidth).TextAlign(ui.End)
		})
		if len(j.Items) == 0 {
			ui.Text(c, "Tracks appear once the link resolves.").TextColor(k.Muted).Padding(12, 4)
			return
		}
		// The list's own scroll counts from this frame on.
		a.scrollState(c, "tracks")
		still := a.scrolling(c)
		a.scrollArea(c, "tracks", detailGround(k), scrollGutter, func() {
			for i := range j.Items {
				next := i+1 < len(j.Items) && trackLit(&j.Items[i+1])
				a.trackRow(c, k, &j.Items[i], accent, bar, next, still)
			}
		}).Grow(1)
	})
}

// itemRunning reports whether a track is in the pipeline now.
func itemRunning(it *domain.JobItem) bool {
	switch it.Status {
	case domain.ItemQueued, domain.ItemCompleted, domain.ItemSkipped, domain.ItemFailed, domain.ItemCancelled:
		return false
	}
	return true
}

// trackLit reports whether a track's row is highlighted: it is being
// worked on, not waiting for a slot.
func trackLit(it *domain.JobItem) bool {
	if !itemRunning(it) {
		return false
	}
	_, _, working := stateLabel(it)
	return working
}

// trackRow is one track: a hairline row whose content sits in a rounded
// highlight while the track is worked on. The highlight is inset from the
// hairline, so adjacent highlighted rows read as two, and the hairline
// beside one is hidden rather than removed, so a row keeps its height
// either way.
func (a *App) trackRow(c *ui.Context, k *Tokens, it *domain.JobItem, accent ui.Color, bar float32, nextLit, still bool) {
	running, lit := itemRunning(it), trackLit(it)
	line := k.Line
	if lit || nextLit {
		line = ui.Transparent
	}
	ui.Column(c.Key(it.ID)).PaddingY(trackInset).BorderWidth(0, 0, 1, 0).BorderColor(line).Children(func() {
		row := ui.Row(c).Gap(16).Padding(trackPadY, 4).AlignItems(ui.Center).Radius(radiusControl)
		if lit {
			row.Background(k.AccentSoft.Alpha(0.45))
		}
		row.Children(func() {
			mono(c, fmt.Sprint(it.Index), 12).TextColor(k.Faint).Width(28).TextAlign(ui.End)
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, it.Title).FontWeight(600).Ellipsis("…").SingleLine()
				if sub := trackSub(it); sub != "" {
					mono(c, sub, 11).TextColor(k.Muted).Ellipsis("…")
				} else {
					mono(c, " ", 11)
				}
			})
			ui.Box(c).Width(bar).Children(func() {
				widgets.StageBar{Stages: progress.ItemStages(*it), Color: accent, Track: k.Track, Running: running, Still: still,
					Indeterminate: progress.Indeterminate(*it), Failed: it.Status == domain.ItemFailed, Bad: k.Bad,
					Finished: true, Done: k.Fg.Alpha(0.85)}.Show(c, "bar")
			})
			ui.Row(c).Width(trackStateWidth).Gap(6).Justify(ui.End).AlignItems(ui.Center).Children(func() { trackState(c, k, it) })
		})
	})
}

func trackSub(it *domain.JobItem) string {
	q := strings.ToLower(format.Quality(it.Codec, it.BitDepth, it.SampleRate, it.Bitrate))
	switch {
	case it.Status == domain.ItemQueued:
		return "waiting for a download slot"
	case it.Attempt > 1 && it.MaxAttempts > 0 && it.Status != domain.ItemCompleted:
		if q == "" {
			return fmt.Sprintf("retry %d of %d", it.Attempt, it.MaxAttempts)
		}
		return fmt.Sprintf("%s → retry %d of %d", q, it.Attempt, it.MaxAttempts)
	case it.FileSize > 0:
		return q + " · " + format.Bytes(it.FileSize)
	case it.StatusMessage != "" && it.Status == domain.ItemFailed:
		return it.StatusMessage
	case it.Status == domain.ItemResolving:
		return "fetching metadata, cover, lyrics"
	}
	return q
}

// stateLabel is what the state column says of a track, its icon and
// whether it is in the accent (working) or muted (waiting): short words of
// one column width.
func stateLabel(it *domain.JobItem) (ic *ui.SVG, text string, working bool) {
	pct := func(f float64, word string) string {
		if f > 0 {
			return fmt.Sprintf("%d%%", int(f*100))
		}
		return word
	}
	switch it.Status {
	case domain.ItemQueued, domain.ItemWaitingDownload:
		return icClock, "Waiting", false
	case domain.ItemResolving:
		return icSearch, "Preparing", true
	case domain.ItemDownloading:
		return icDown, pct(it.Progress.Download, "Download"), true
	case domain.ItemWaitingDecrypt:
		return icLock, "Waiting", false
	case domain.ItemDecrypting:
		return icLock, pct(it.Progress.Decrypt, "Decrypt"), true
	case domain.ItemRemuxing:
		return icNote, "Remux", true
	case domain.ItemTagging:
		return icNote, "Tagging", true
	case domain.ItemSaving:
		return icNote, "Saving", true
	}
	return icNote, "Working", true
}

func trackState(c *ui.Context, k *Tokens, it *domain.JobItem) {
	show := func(ic *ui.SVG, text string, col ui.Color) {
		ui.Icon(c, ic).FontSize(13).TextColor(col)
		ui.Text(c, text).FontSize(12).FontWeight(600).FontFeatures("tnum").TextColor(col).SingleLine()
	}
	switch it.Status {
	case domain.ItemCompleted:
		show(icCheck, "Saved", k.OK)
	case domain.ItemSkipped:
		show(icCheck, "On disk", k.Skip)
	case domain.ItemFailed:
		show(icCross, "Failed", k.Bad)
	case domain.ItemCancelled:
		show(icCross, "Cancelled", k.Muted)
	default:
		ic, text, working := stateLabel(it)
		col := k.Muted
		if working {
			col = k.Accent
		}
		show(ic, text, col)
	}
}

func (a *App) activity(c *ui.Context, k *Tokens, j *store.Job, width float32) {
	ui.Column(c).Width(width).Padding(16, 20).Gap(4).BorderWidth(0, 0, 0, 1).BorderColor(k.Line).Children(func() {
		label(c, k, "Activity").Margin(0, 0, 6, 0)
		a.scrollArea(c, "activity", detailGround(k), scrollGutter, func() {
			if len(j.Activity) == 0 {
				ui.Text(c, "Nothing yet.").FontSize(12).TextColor(k.Muted)
			}
			for i, ev := range j.Activity {
				ui.Row(c.Key(i)).FillWidth().Gap(10).PaddingY(6).AlignItems(ui.Start).Children(func() {
					mono(c, format.Seconds(ev.At), 11).TextColor(k.Faint).Width(62).Shrink(0)
					col := k.Fg
					switch ev.Kind {
					case store.ActivityWarn:
						col = k.Warn
					case store.ActivityFailed:
						col = k.Bad
					}
					// Long lines, as a retry's error with its URL, wrap
					// inside the column.
					ui.Text(c, ev.Text).FontSize(12).LineHeight(1.35).TextColor(col).Grow(1).Shrink(1).MinWidth(0)
				})
			}
		}).Grow(1)
		if dir := savedDir(j); dir != "" {
			ui.Column(c).Gap(4).Padding(10, 0, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(k.Line).Children(func() {
				label(c, k, "Saved to")
				ui.Text(c, dir).Font(MonoFont).FontSize(11).TextColor(k.Muted)
			})
		}
	})
}

// revealLabel names the file manager as the platform does.
var revealLabel = func() string {
	switch runtime.GOOS {
	case "darwin":
		return "Reveal in Finder"
	case "windows":
		return "Show in Explorer"
	}
	return "Show in folder"
}()

// detailInk is how much of the ink the detail's lower half lays over the
// ground, and detailGround the color that makes, which its lists fade into.
const detailInk = 0.72

func detailGround(k *Tokens) ui.Color { return k.Ground.Mix(k.Ink, detailInk) }
