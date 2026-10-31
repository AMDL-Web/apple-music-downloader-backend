package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"

	"amdl/internal/client"
	"amdl/internal/domain"
	"amdl/internal/format"
	"amdl/internal/progress"
)

// tracksPerCard is how many track rows a running card keeps, whatever its
// tracks are doing, so the card does not change height as they start and end.
const tracksPerCard = 3

// overall is the mean progress of the running jobs, for the terminal's tab
// or taskbar progress.
func (m *model) overall() (int, bool) {
	n, sum := 0, 0
	for _, st := range m.jobs {
		if st.job.Status == domain.JobRunning {
			n++
			sum += m.album(st).Percent
		}
	}
	if n == 0 {
		return 0, false
	}
	return sum / n, true
}

func (m *model) album(st *jobState) progress.Album {
	if st.detail != nil {
		return progress.AlbumProgress(st.job.Status, st.detail.Items)
	}
	// A job not followed has only its counters: its finished tracks fill the
	// bar in outcome order.
	j := st.job
	var items []domain.JobItem
	add := func(n int, s domain.ItemStatus) {
		for i := 0; i < n; i++ {
			items = append(items, domain.JobItem{Index: len(items) + 1, Status: s})
		}
	}
	add(j.CompletedItems, domain.ItemCompleted)
	add(j.SkippedItems, domain.ItemSkipped)
	add(j.FailedItems, domain.ItemFailed)
	add(j.CancelledItems, domain.ItemCancelled)
	add(j.TotalItems-len(items), domain.ItemQueued)
	return progress.AlbumProgress(j.Status, items)
}

// cardTheme is the palette of the cards: greyed out while the server is not
// answering, since what they show may be stale.
func (m *model) cardTheme() theme {
	t := m.theme
	if m.conn.Phase == client.Offline {
		t.fg, t.accent, t.ok, t.bad = t.dim, t.dim, t.dim, t.dim
	}
	return t
}

// cards draws the jobs in progress, in the order they appeared, in at most
// room rows. A running card has a fixed number of track rows; when the cards
// do not fit, the track rows go first, then the cards from the end.
func (m *model) cards(w, room int) string {
	ids := m.activeIDs()
	if len(ids) == 0 || room < 2 {
		return ""
	}
	t := m.cardTheme()
	shown := min(len(ids), 3)
	tracks := tracksPerCard
	if m.showAll {
		shown, tracks = len(ids), 0
	}
	if w < narrowCols {
		tracks = 0
	}
	height := func(n, k int) int {
		h := 0
		for i := 0; i < n; i++ {
			h += m.cardHeight(m.jobs[ids[i]], k)
		}
		if n < len(ids) {
			h++ // the "+N more" line
		}
		return h
	}
	if m.expanded && !m.showAll && w >= narrowCols {
		shown = 1
		tracks = max(tracksPerCard, room-height(1, 0)-1)
	}
	for tracks > 0 && height(shown, tracks) > room {
		tracks--
	}
	for shown > 1 && height(shown, tracks) > room {
		shown--
	}
	if height(shown, tracks) > room {
		return ""
	}
	out := make([]string, 0, shown+1)
	for _, id := range ids[:shown] {
		out = append(out, m.card(t, w, id, tracks))
	}
	if shown < len(ids) {
		more := fmt.Sprintf("  +%d more", len(ids)-shown)
		if !m.showAll {
			more += " · ctrl+t shows all"
		}
		out = append(out, t.style(t.dim).Render(trunc(more, w)))
	}
	return strings.Join(out, "\n")
}

// cardHeight is the rows card draws for st with k track rows.
func (m *model) cardHeight(st *jobState, k int) int {
	j := st.job
	if j.Status == domain.JobQueued {
		return 1
	}
	if st.detail == nil || len(st.detail.Items) == 0 {
		return 2
	}
	return 2 + min(k, len(st.detail.Items))
}

func (m *model) card(t theme, w int, id string, tracks int) string {
	st := m.jobs[id]
	j := st.job
	accent := t.albumAccent(j.ArtworkBgColor)
	if m.conn.Phase == client.Offline {
		accent = t.dim
	}
	right := ""
	switch {
	case j.Status == domain.JobQueued:
		right = "queued"
	case st.detail == nil || len(st.detail.Items) == 0:
		right = "resolving"
	case w >= mediumCols:
		right = m.quality(st)
	}
	num := t.style(t.dim).Render(" " + m.ref(id))
	titleW := w - 2 - lipgloss.Width(num) - lipgloss.Width(right) - 2
	head := t.style(accent).Render("◆ ")
	name := jobName(j)
	if w >= mediumCols && j.ArtistName != "" {
		artistW := min(lipgloss.Width(j.ArtistName), max(8, titleW/3))
		nameW := max(4, titleW-artistW-3)
		head += t.bold().Render(trunc(name, nameW)) + t.style(t.dim).Render(" · "+trunc(j.ArtistName, titleW-min(lipgloss.Width(name), nameW)-3))
	} else {
		head += t.bold().Render(trunc(name, max(4, titleW)))
	}
	head += num
	line := pad(head, w-lipgloss.Width(right)) + t.style(t.dim).Render(right)
	if right == "queued" {
		return line
	}
	lines := []string{line}
	a := m.album(st)
	if right == "resolving" {
		lines = append(lines, "  "+t.style(t.dim).Render("reading the tracklist…"))
		return strings.Join(lines, "\n")
	}
	tail := ""
	if w >= narrowCols {
		tail = pad(format.Remaining(st.rate.Remaining()), etaWidth)
	}
	if w >= mediumCols {
		speed := ""
		if sp := progress.AvgSpeed(st.job, st.detail.Items, m.now()); sp > 0 {
			speed = "avg " + format.Rate(sp)
		}
		tail += "  " + pad(speed, speedWidth)
	}
	lines = append(lines, "  "+albumLine(t, w-2, a, accent, tail))
	if tracks == 0 {
		return strings.Join(lines, "\n")
	}
	for _, it := range trackWindow(st.detail.Items, tracks) {
		lines = append(lines, m.trackRow(t, w, it, accent))
	}
	return strings.Join(lines, "\n")
}

// trackWindow picks k of a job's tracks to show, in track order: the first
// that have not finished, then, near the end, the last to finish, so the
// rows stay k while the job runs.
func trackWindow(items []domain.JobItem, k int) []domain.JobItem {
	var open, done []domain.JobItem
	for _, it := range items {
		if it.Finished() {
			done = append(done, it)
		} else {
			open = append(open, it)
		}
	}
	sort.Slice(open, func(i, j int) bool { return open[i].Index < open[j].Index })
	if len(open) >= k {
		return open[:k]
	}
	sort.Slice(done, func(i, j int) bool { return done[i].UpdatedAt.After(done[j].UpdatedAt) })
	out := append(open, done[:min(len(done), k-len(open))]...)
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out
}

func (m *model) quality(st *jobState) string {
	if st.detail == nil {
		return ""
	}
	for _, it := range st.detail.Items {
		if q := format.Quality(it.Codec, it.BitDepth, it.SampleRate, it.Bitrate); q != "" {
			return q
		}
	}
	return ""
}

func (m *model) trackRow(t theme, w int, it domain.JobItem, accent color.Color) string {
	titleW := 24
	if w < mediumCols {
		titleW = 14
	}
	metaW := 0
	switch {
	case w >= wideCols:
		metaW = 18
	case w >= mediumCols:
		metaW = 13
	}
	barW := w - 2 - 3 - titleW - 2
	if metaW > 0 {
		barW -= metaW + 2
	}
	titleC := t.fg
	if !it.Finished() && it.Status == domain.ItemQueued {
		titleC = t.dim
	}
	row := "  " + t.style(t.dim).Render(fmt.Sprintf("%02d ", it.Index)) +
		t.style(titleC).Render(pad(trunc(it.Title, titleW), titleW)) + "  " + stageBar(t, barW, it, accent)
	if metaW > 0 {
		c := t.dim
		switch it.Status {
		case domain.ItemFailed:
			c = t.bad
		case domain.ItemCompleted:
			c = t.ok
		}
		state := itemState(it)
		if it.Status == domain.ItemWaitingDecrypt && lipgloss.Width(state) > metaW {
			state = "Waiting" // the stage bar shows which stage it waits for
		}
		row += "  " + t.style(c).Render(trunc(state, metaW))
	}
	return row
}

func itemState(it domain.JobItem) string {
	p := it.Progress
	switch it.Status {
	case domain.ItemDownloading:
		if p.Download > 0 {
			return fmt.Sprintf("Download %d%%", int(p.Download*100))
		}
		return "Downloading"
	case domain.ItemDecrypting:
		if p.Decrypt > 0 {
			return fmt.Sprintf("Decrypt %d%%", int(p.Decrypt*100))
		}
		return "Decrypting"
	case domain.ItemWaitingDownload:
		return "Waiting"
	case domain.ItemWaitingDecrypt:
		return "Waiting to decrypt"
	case domain.ItemResolving:
		return "Resolving"
	case domain.ItemRemuxing:
		return "Remuxing"
	case domain.ItemTagging:
		return "Tagging"
	case domain.ItemSaving:
		return "Saving"
	case domain.ItemCompleted:
		return "✓ Saved"
	case domain.ItemSkipped:
		return "On disk"
	case domain.ItemFailed:
		if it.Error != "" {
			return "✗ " + it.Error
		}
		return "✗ Failed"
	case domain.ItemCancelled:
		return "Cancelled"
	}
	return "Queued"
}
