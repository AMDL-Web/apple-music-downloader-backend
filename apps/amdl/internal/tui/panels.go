package tui

import (
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"amdl/internal/domain"
	"amdl/internal/format"
)

// menuRows bounds the / menu.
const menuRows = 6

// rowStyle is a list row's palette: the selected row carries a background
// across its whole width, so every piece of it has to.
type rowStyle struct {
	t   theme
	sel bool
}

func (r rowStyle) s(c color.Color) lipgloss.Style {
	st := r.t.style(c)
	if r.sel {
		st = st.Background(r.t.sel)
	}
	return st
}

func (r rowStyle) bold(c color.Color) lipgloss.Style { return r.s(c).Bold(r.sel) }

// fill pads a row to w columns in the row's background.
func (r rowStyle) fill(line string, w int) string {
	if d := w - lipgloss.Width(line); d > 0 {
		line += r.s(r.t.fg).Render(strings.Repeat(" ", d))
	}
	return line
}

// panelView is what opens below the prompt, in at most room rows.
func (m *model) panelView(w, room int) string {
	t := m.theme
	switch m.panel {
	case panelPreview:
		return m.previewView(w)
	case panelResults:
		return m.resultsView(w, room)
	case panelConfirm:
		return "  " + t.style(t.bad).Render(trunc(m.confirm.prompt, w-2))
	case panelLogin:
		what := map[string]string{"username": "your Apple ID", "password": "the password (not shown)", "code": "the two-step code from your device", "waiting": "signing in…"}[m.loginSt.step]
		return "  " + t.style(t.dim).Render(trunc("sign in · "+what, w-2))
	case panelHelp:
		return m.helpView(w)
	}
	return m.menuView(w, room)
}

func (m *model) menuView(w, room int) string {
	ms := m.menu()
	if len(ms) == 0 {
		return ""
	}
	t := m.theme
	rows := min(len(ms), menuRows, max(1, room))
	sel := min(m.menuSel, len(ms)-1)
	start := 0
	if sel >= rows {
		start = sel - rows + 1
	}
	var lines []string
	for i := start; i < start+rows; i++ {
		s := ms[i]
		r := rowStyle{t, i == sel}
		mark := "  "
		if r.sel {
			mark = "▸ "
		}
		line := "  " + r.s(t.accent).Render(mark) + r.bold(t.fg).Render(pad(s.name, 10))
		if w >= narrowCols {
			line += r.s(t.dim).Render(pad(s.args, 9) + trunc(s.help, w-25))
		}
		lines = append(lines, r.fill(line, w))
	}
	return strings.Join(lines, "\n")
}

func (m *model) previewView(w int) string {
	t := m.theme
	var lines []string
	for _, p := range m.preview {
		if p.err != nil {
			lines = append(lines, truncStyled("  "+t.style(t.bad).Render("✗ ")+t.style(t.fg).Render(linkName(p.link))+t.style(t.dim).Render(" · "+errText(p.err)), w))
			continue
		}
		l := p.l
		accent := t.albumAccent(l.ArtworkBgColor)
		line := "  " + t.style(accent).Render("◆ ") + t.bold().Render(trunc(l.Title, w/2))
		var facts []string
		if l.ArtistName != "" && w >= mediumCols {
			facts = append(facts, l.ArtistName)
		}
		facts = append(facts, l.Type)
		if len(l.ReleaseDate) >= 4 {
			facts = append(facts, l.ReleaseDate[:4])
		}
		if l.TrackCount > 0 {
			facts = append(facts, trackCount(l.TrackCount))
		}
		if l.AppleDigitalMaster && w >= wideCols {
			facts = append(facts, "Apple Digital Master")
		}
		lines = append(lines, truncStyled(line+t.style(t.dim).Render(" · "+strings.Join(facts, " · ")), w))
	}
	return strings.Join(lines, "\n")
}

func (m *model) resultsView(w, room int) string {
	t := m.theme
	r := m.res
	head := "search · " + r.term
	if r.artist != nil {
		head = r.artist.Name + " · albums"
	}
	vis := r.visible()
	line := "  " + t.style(t.fg).Render(trunc(head, max(8, w/2)))
	if r.artist == nil && w >= narrowCols {
		var tabs []string
		for i, f := range filters {
			s := t.style(t.dim)
			if i == r.filter {
				s = t.bold().Underline(true)
			}
			tabs = append(tabs, s.Render(f))
		}
		line += "   " + strings.Join(tabs, "  ")
	}
	lines := []string{truncStyled(line, w)}
	if r.err != "" {
		lines = append(lines, "  "+t.style(t.bad).Render(trunc(r.err, w-2)))
	}
	if len(vis) == 0 && r.err == "" {
		lines = append(lines, "  "+t.style(t.dim).Render("no hits"))
	}
	maxRows := min(len(vis), max(1, room-len(lines)))
	start := 0
	if r.cursor >= maxRows {
		start = r.cursor - maxRows + 1
	}
	nameW := 30
	if w < mediumCols {
		nameW = max(8, w-20)
	}
	for row := start; row < start+maxRows && row < len(vis); row++ {
		p := r.items[vis[row]]
		h := p.hit
		rs := rowStyle{t, row == r.cursor}
		mark := "  "
		if rs.sel {
			mark = "▸ "
		}
		box := rs.s(t.dim).Render("○ ")
		if p.selected {
			box = rs.s(t.accent).Render("● ")
		}
		line := "  " + rs.s(t.accent).Render(mark) + box + rs.s(t.dim).Render(pad(h.Type, 9)) + rs.bold(t.fg).Render(pad(trunc(h.Name, nameW), nameW))
		if w >= mediumCols {
			var facts []string
			if h.ArtistName != "" && h.Type != "artist" {
				facts = append(facts, h.ArtistName)
			}
			if len(h.ReleaseDate) >= 4 {
				facts = append(facts, h.ReleaseDate[:4])
			}
			if h.TrackCount > 0 {
				facts = append(facts, trackCount(h.TrackCount))
			}
			if h.DurationMS > 0 {
				facts = append(facts, format.Duration(time.Duration(h.DurationMS)*time.Millisecond))
			}
			mark := ""
			if h.PriorJob != nil {
				mark = "✓ in library"
				if h.PriorJob.Status != domain.JobCompleted {
					mark = "↻ downloading"
				}
			}
			factW := w - lipgloss.Width(line) - 2 - lipgloss.Width(mark) - 2
			line += rs.s(t.dim).Render("  " + pad(trunc(strings.Join(facts, " · "), factW), factW))
			if mark != "" {
				line += rs.s(t.ok).Render("  " + mark)
			}
		}
		lines = append(lines, rs.fill(truncStyled(line, w), w))
	}
	return strings.Join(lines, "\n")
}

func (m *model) helpView(w int) string {
	t := m.theme
	keys := [][2]string{
		{"enter", "search, or open a link"},
		{"↑ ↓", "history; move in a list"},
		{"tab", "complete; change the filter"},
		{"space", "select in search"},
		{"ctrl+o", "all tracks of the first job"},
		{"ctrl+t", "every job in progress"},
		{"/", "commands"},
		{"ctrl+l", "clear and reprint the session"},
		{"esc", "close the panel or clear"},
		{"ctrl+c ×2", "quit; downloads go on"},
	}
	cols := 1
	if w >= mediumCols {
		cols = 2
	}
	colW := (w - 2) / cols
	var lines []string
	for i := 0; i < len(keys); i += cols {
		line := "  "
		for c := 0; c < cols && i+c < len(keys); c++ {
			k := keys[i+c]
			cell := t.style(t.fg).Render(pad(k[0], 11)) + t.style(t.dim).Render(trunc(k[1], colW-13))
			if c < cols-1 {
				cell = pad(cell, colW)
			}
			line += cell
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
