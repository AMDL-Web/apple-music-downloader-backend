package tui

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"amdl/internal/client"
	"amdl/internal/domain"
	"amdl/internal/format"
)

// Width breakpoints of the live region.
const (
	maxCols    = 120 // wider terminals keep this width rather than stretch the bars
	wideCols   = 100 // track rows say why a track failed
	mediumCols = 72  // cards name the artist and track rows carry a status column
	narrowCols = 48  // below this, cards drop their track rows
	boxCols    = 40  // below this, the prompt loses its border
)

var spinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// contentWidth is the width the live region and scrollback are laid out in.
func (m *model) contentWidth() int { return min(max(m.width, 20), maxCols) }

func (m *model) View() tea.View {
	w := m.contentWidth()
	var top []string
	if !m.headerDone {
		top = append(top, strings.TrimSuffix(m.header(w), "\n"))
	}
	prompt := m.prompt(w)
	promptH := strings.Count(prompt, "\n") + 1
	status := m.status(w)
	fixed := promptH + 1 + len(top)
	if len(top) > 0 {
		fixed += strings.Count(top[0], "\n")
	}
	room := max(1, m.height-1-fixed) // one row spare, so the region never fills the screen
	panel := m.panelView(w, room)
	if panel != "" {
		room -= strings.Count(panel, "\n") + 1
	}
	if c := m.cards(w, room); c != "" {
		top = append(top, c)
	}
	m.promptRow = 0
	for _, b := range top {
		m.promptRow += strings.Count(b, "\n") + 1
	}
	blocks := append(top, prompt)
	if panel != "" {
		blocks = append(blocks, panel)
	}
	blocks = append(blocks, status)
	v := tea.NewView(strings.Join(blocks, "\n"))
	if c := m.input.Cursor(); c != nil && m.busy == "" {
		c.X += m.promptInset
		c.Y += m.promptRow
		if promptH > 1 {
			c.Y++
		}
		v.Cursor = c
	}
	if pct, ok := m.overall(); ok {
		v.ProgressBar = tea.NewProgressBar(tea.ProgressBarDefault, pct)
	}
	return v
}

func (m *model) you(s string) string {
	return m.theme.style(m.theme.accent).Render("> ") + m.theme.style(m.theme.fg).Render(s)
}

func (m *model) reply(s string) string {
	return m.theme.style(m.theme.dim).Render("  ⎿ ") + s
}

func jobName(j domain.Job) string {
	if j.Title != "" {
		return j.Title
	}
	return linkName(j.Input)
}

// linkName is what a link names before its title is known: the slug of an
// Apple Music link, "nothing-here" for music.apple.com/cn/album/nothing-here/1,
// else the link.
func linkName(link string) string {
	u, err := url.Parse(link)
	if err != nil || !strings.HasSuffix(u.Host, "music.apple.com") {
		return link
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) >= 4 && parts[2] != "" {
		if s, err := url.PathUnescape(parts[2]); err == nil {
			return s
		}
	}
	return link
}

// ref is how a person names a job to the commands: its session number, or
// the start of its ID for one this session has not seen.
func (m *model) ref(id string) string {
	if n, ok := m.num[id]; ok {
		return fmt.Sprintf("#%d", n)
	}
	return shortID(id)
}

func shortID(id string) string {
	s := strings.TrimPrefix(id, "job_")
	if len(s) > 6 {
		s = s[:6]
	}
	return s
}

// finishedLine is what goes into scrollback when a job ends.
func (m *model) finishedLine(j domain.Job, d *client.DownloadDetail, n int) string {
	t := m.theme
	mark, c := "✓ ", t.ok
	switch j.Status {
	case domain.JobFailed:
		mark, c = "✗ ", t.bad
	case domain.JobCancelled:
		mark, c = "⊘ ", t.dim
	}
	var parts []string
	if j.TotalItems > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d saved", j.CompletedItems+j.SkippedItems, j.TotalItems))
	}
	if j.SkippedItems > 0 {
		parts = append(parts, fmt.Sprintf("%d already on disk", j.SkippedItems))
	}
	if d != nil {
		var size int64
		quality := ""
		for _, it := range d.Items {
			size += it.FileSize
			if quality == "" && it.Finished() {
				quality = format.Quality(it.Codec, it.BitDepth, it.SampleRate, it.Bitrate)
			}
		}
		if quality != "" {
			parts = append(parts, quality)
		}
		if size > 0 {
			parts = append(parts, format.Bytes(size))
		}
	}
	if j.StartedAt != nil && j.FinishedAt != nil && j.FinishedAt.Sub(*j.StartedAt) >= time.Second {
		parts = append(parts, format.Duration(j.FinishedAt.Sub(*j.StartedAt)))
	}
	parts = append(parts, fmt.Sprintf("#%d", n))
	name := t.bold().Render(jobName(j))
	if j.ArtistName != "" {
		name += t.style(t.dim).Render(" — " + j.ArtistName)
	}
	line := t.style(c).Render(mark) + name + t.style(t.dim).Render(" · "+strings.Join(parts, " · "))
	if j.FailedItems > 0 || j.Status == domain.JobFailed || j.Status == domain.JobCancelled {
		why := ""
		if j.FailedItems > 0 {
			why = fmt.Sprintf("%d failed", j.FailedItems)
		}
		if j.Error != "" {
			why = strings.TrimPrefix(why+": "+j.Error, ": ")
		}
		var b strings.Builder
		b.WriteString("\n  ")
		if why != "" {
			b.WriteString(t.style(t.bad).Render(why) + t.style(t.dim).Render(" · "))
		}
		b.WriteString(t.style(t.dim).Render(fmt.Sprintf("/retry %d", n)))
		line += b.String()
	}
	return line
}

func (m *model) submitted(msg submitMsg) string {
	t := m.theme
	if msg.err != nil {
		return t.style(t.bad).Render("  ⎿ ") + errText(msg.err)
	}
	var lines []string
	for _, r := range msg.batch.Results {
		switch {
		case r.Job != nil:
			name := r.Job.Title
			if name == "" {
				name = m.titles[r.URL]
			}
			if name == "" {
				name = linkName(r.URL)
			}
			lines = append(lines, "queued "+t.bold().Render(name)+t.style(t.dim).Render(" "+m.ref(r.Job.ID)))
		case r.ExistingJobID != "":
			lines = append(lines, t.style(t.dim).Render("already downloading "+m.ref(r.ExistingJobID)))
		case r.Error != nil:
			lines = append(lines, t.style(t.bad).Render(r.Error.Message))
		}
	}
	return t.style(t.dim).Render("  ⎿ ") + strings.Join(lines, "\n    ")
}

func (m *model) jobTable(jobs []domain.Job) string {
	if len(jobs) == 0 {
		return "none"
	}
	t := m.theme
	nameW := 0
	for _, j := range jobs {
		nameW = max(nameW, lipgloss.Width(trunc(jobName(j), 40)))
	}
	var b strings.Builder
	for i, j := range jobs {
		if i > 0 {
			b.WriteString("\n    ")
		}
		c := t.dim
		switch j.Status {
		case domain.JobCompleted:
			c = t.ok
		case domain.JobFailed:
			c = t.bad
		case domain.JobRunning:
			c = t.accent
		}
		b.WriteString(t.style(c).Render(fmt.Sprintf("%-9s", j.Status)) + " " +
			t.style(t.dim).Render(fmt.Sprintf("%-7s", m.ref(j.ID))) + " " +
			t.style(t.fg).Render(pad(trunc(jobName(j), 40), nameW)) +
			t.style(t.dim).Render(fmt.Sprintf("  %d/%d", j.CompletedItems+j.SkippedItems, j.TotalItems)))
	}
	return b.String()
}

func (m *model) prompt(w int) string {
	t := m.theme
	ph := "paste a link, or type and press enter to search"
	if w < mediumCols {
		ph = "link, search or /"
	}
	if m.panel == panelLogin {
		ph = map[string]string{"username": "Apple ID", "password": "password", "code": "code"}[m.loginSt.step]
	}
	m.input.Placeholder = ph
	prefix := t.style(t.accent).Render("> ")
	if m.busy != "" {
		prefix = t.style(t.accent).Render(spinner[m.spin%len(spinner)] + " ")
	}
	if w < boxCols {
		m.input.SetWidth(max(4, w-3))
		m.promptInset = 2
		return prefix + m.input.View()
	}
	m.input.SetWidth(max(4, w-7))
	m.promptInset = 4
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(t.faint).
		Width(w).Padding(0, 1).Render(prefix + m.input.View())
}

// status is the one row under everything: the connection and the jobs on the
// left, the keys that matter right now on the right.
func (m *model) status(w int) string {
	t := m.theme
	running, queued := 0, 0
	for _, st := range m.jobs {
		if st.job.Status == domain.JobRunning {
			running++
		} else {
			queued++
		}
	}
	var left string
	switch {
	case m.conn.Phase == client.Offline:
		left = t.style(t.warn).Render("○ offline")
	case m.conn.Phase == client.Connecting && m.system == nil:
		left = t.style(t.dim).Render("◌ connecting…")
	default:
		left = t.style(t.ok).Render("●") + t.style(t.dim).Render(fmt.Sprintf(" %d running · %d queued", running, queued))
	}
	if m.busy != "" {
		left += t.style(t.dim).Render(" · " + m.busy + "…")
	}
	left = "  " + left
	if !m.quitHint.IsZero() {
		quit := t.style(t.bad).Render("press ctrl+c again to quit")
		if w-lipgloss.Width(quit)-1 < lipgloss.Width(left) {
			return truncStyled("  "+quit, w)
		}
		return pad(left, w-lipgloss.Width(quit)-1) + quit
	}
	room := w - lipgloss.Width(left) - 3
	var right string
	for _, h := range m.hints() {
		next := h
		if right != "" {
			next = right + " · " + h
		}
		if lipgloss.Width(next) > room {
			break
		}
		right = next
	}
	if right == "" {
		return truncStyled(left, w)
	}
	return pad(left, w-lipgloss.Width(right)-1) + t.style(t.dim).Render(right)
}

// hints are the keys of the moment, most useful first.
func (m *model) hints() []string {
	switch m.panel {
	case panelResults:
		h := []string{"enter downloads", "space selects", "esc closes"}
		if m.res != nil && m.res.artist != nil {
			return append(h, "← back")
		}
		return append(h, "tab filters", "→ albums")
	case panelPreview:
		return []string{"enter downloads", "esc dismisses"}
	case panelConfirm:
		return []string{"y deletes", "n keeps"}
	case panelPriority:
		return []string{"enter saves", "space turns on/off", "shift+↑↓ moves", "esc cancels"}
	case panelAsk:
		h := []string{"enter downloads", "←→ chooses", "esc goes back"}
		if m.ask != nil && m.ask.codecs != nil && m.ask.again {
			h = append(h, "↑↓ question")
		}
		return h
	case panelLogin:
		return []string{"esc cancels"}
	case panelHelp:
		return []string{"esc closes"}
	}
	if len(m.menu()) > 0 {
		return []string{"enter runs", "tab completes", "↑↓ choose"}
	}
	if v := strings.TrimSpace(m.input.Value()); v != "" {
		if len(linksIn(v)) > 0 {
			return []string{"enter previews", "esc clears"}
		}
		if !strings.HasPrefix(v, "/") {
			return []string{"enter searches", "esc clears"}
		}
		return []string{"enter runs", "esc clears"}
	}
	if len(m.jobs) > 0 {
		return []string{"? shortcuts", "ctrl+o tracks"}
	}
	return []string{"? shortcuts", "/ commands"}
}

func trackCount(n int) string {
	if n == 1 {
		return "1 track"
	}
	return fmt.Sprintf("%d tracks", n)
}

func pad(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// trunc cuts s to w columns with an ellipsis.
func trunc(s string, w int) string {
	if w <= 1 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > w {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}
