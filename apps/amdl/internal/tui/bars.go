package tui

import (
	"fmt"
	"image/color"
	"math"
	"strings"

	"charm.land/lipgloss/v2"

	"amdl/internal/domain"
	"amdl/internal/progress"
)

// Bar glyphs: done, in progress and not started differ in shape, not only in
// color, so the bars read under NO_COLOR.
const (
	glyphDone = "━"
	glyphHalf = "╸"
	glyphLive = "╍"
	glyphTodo = "─"
)

// fill draws w cells filled to f, to the nearest half cell.
func fill(t theme, w int, f float64, on color.Color) string {
	if w <= 0 {
		return ""
	}
	halves := int(math.Round(math.Max(0, math.Min(1, f)) * float64(w) * 2))
	full := halves / 2
	var b strings.Builder
	b.WriteString(t.style(on).Render(strings.Repeat(glyphDone, full)))
	if halves%2 == 1 && full < w {
		b.WriteString(t.style(on).Render(glyphHalf))
		full++
	}
	b.WriteString(t.style(t.faint).Render(strings.Repeat(glyphTodo, w-full)))
	return b.String()
}

// stageWidths splits w cells by the stage weights, each stage at least one
// cell and download and decrypt sharing the rest.
func stageWidths(w int) [progress.StageCount]int {
	var out [progress.StageCount]int
	small := 0
	for i, x := range progress.Weights {
		if s := progress.Stage(i); s != progress.StageDownload && s != progress.StageDecrypt {
			out[i] = max(1, int(math.Round(x*float64(w))))
			small += out[i]
		}
	}
	left := max(2, w-small)
	dl, dc := progress.Weights[progress.StageDownload], progress.Weights[progress.StageDecrypt]
	out[progress.StageDownload] = min(left-1, int(math.Round(float64(left)*dl/(dl+dc))))
	out[progress.StageDecrypt] = left - out[progress.StageDownload]
	return out
}

// stageBar draws one track's seven stages, each as wide as its share of the
// measured time, with a one-cell gap between stages when the bar is wide
// enough. A failed track paints the stage it stopped in.
func stageBar(t theme, w int, it domain.JobItem, accent color.Color) string {
	st := progress.ItemStages(it)
	failedAt := -1
	if it.Status == domain.ItemFailed {
		failedAt = int(st.Current())
	}
	gaps := w >= 36
	inner := w
	if gaps {
		inner -= int(progress.StageCount) - 1
	}
	widths := stageWidths(inner)
	var b strings.Builder
	for i, cw := range widths {
		on, f := accent, st[i]
		switch {
		case i == failedAt:
			on, f = t.bad, 1
		case it.Status == domain.ItemSkipped || it.Status == domain.ItemCancelled:
			on = t.skipped
		}
		b.WriteString(fill(t, cw, f, on))
		if gaps && i < len(widths)-1 {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// albumBar draws the iOS app's album bar in w cells: the finished tracks in
// finish order, coloured by outcome, then the rest of the stage-weighted
// progress as a dashed run. With 2..progress.TickMax tracks and room for it,
// the bar is cut into one cell per track.
func albumBar(t theme, w int, a progress.Album, accent color.Color) string {
	if w <= 0 {
		return ""
	}
	cells := a.Ticks
	if cells > 0 && (w+1)/cells < 3 { // each cell plus a one-column gap
		cells = 0
	}
	outcome := func(s domain.ItemStatus) color.Color {
		switch s {
		case domain.ItemSkipped:
			return t.skipped
		case domain.ItemFailed:
			return t.bad
		case domain.ItemCancelled:
			return t.dim
		}
		return accent
	}
	// at is the glyph of the bar at fraction x of its length.
	at := func(x float64) string {
		edge := 0.0
		for _, r := range a.Runs {
			edge += r.Share
			if x < edge-1e-9 {
				return t.style(outcome(r.Status)).Render(glyphDone)
			}
		}
		if x < a.Live-1e-9 {
			return t.style(t.soft(accent)).Render(glyphLive)
		}
		return t.style(t.faint).Render(glyphTodo)
	}
	var b strings.Builder
	if cells == 0 {
		for i := 0; i < w; i++ {
			b.WriteString(at((float64(i) + 0.5) / float64(w)))
		}
		return b.String()
	}
	avail := w - (cells - 1)
	for c := 0; c < cells; c++ {
		lo := int(math.Round(float64(c) * float64(avail) / float64(cells)))
		hi := int(math.Round(float64(c+1) * float64(avail) / float64(cells)))
		for i := lo; i < hi; i++ {
			b.WriteString(at((float64(i) + 0.5) / float64(avail)))
		}
		if c < cells-1 {
			b.WriteByte(' ')
		}
	}
	return b.String()
}

// etaWidth fits the longest time left format.Remaining writes, "~59 min",
// and speedWidth the longest average speed, "avg 99.9 MB/s".
const (
	etaWidth   = 7
	speedWidth = 13
)

// albumLine is the album bar between its count and its percent, then tail,
// the time left and average speed padded to their widths:
// "3/12  ━━━━ ━━━━ ╍╍╍╍ ────  42%  ~1 min   avg 4.1 MB/s". The count,
// percent and tail keep their widths, so the bar does not change length as
// they tick.
func albumLine(t theme, w int, a progress.Album, accent color.Color, tail string) string {
	digits := len(fmt.Sprint(a.Total))
	left := pad(fmt.Sprintf("%d/%d", a.Lit, a.Total), 2*digits+1)
	right := fmt.Sprintf("%3d%%", a.Percent)
	if tail != "" {
		right += "  " + tail
	}
	bw := w - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if bw < 8 {
		return t.style(t.fg).Render(left) + " " + t.style(t.dim).Render(right)
	}
	return t.style(t.fg).Render(left) + " " + albumBar(t, bw, a, accent) + " " + t.style(t.dim).Render(right)
}
