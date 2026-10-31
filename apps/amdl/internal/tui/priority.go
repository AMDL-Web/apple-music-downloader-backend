package tui

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// codecs are the qualities download.quality_priority takes, in the desktop
// app's words (apps/desktop/internal/ui#configFields). AAC-LC is not one:
// the server always tries it last.
var codecs = []struct{ id, label string }{
	{"alac", "ALAC (lossless)"}, {"ec3", "Dolby Atmos (E-AC-3)"}, {"ac3", "Dolby Digital (AC-3)"},
	{"aac", "AAC (Enhanced HLS)"}, {"aac-binaural", "AAC binaural"}, {"aac-downmix", "AAC Dolby downmix"},
}

// priorityEdit is the /quality panel: the qualities the server tries, in
// order, and the ones it does not.
type priorityEdit struct {
	on     []string
	cursor int
	last   bool // the last quality on was turned off, which is refused
}

type priorityMsg struct {
	gen      int
	priority []string
	err      error
}

// rows are the qualities on, in order, then the rest in codecs' order.
func (p *priorityEdit) rows() []string {
	rows := slices.Clone(p.on)
	for _, c := range codecs {
		if !slices.Contains(p.on, c.id) {
			rows = append(rows, c.id)
		}
	}
	return rows
}

func (m *model) openPriority() tea.Cmd {
	gen := m.read("reading the quality priority")
	ctx, c := m.ctx, m.c
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		priority, err := qualityPriority(ctx, c)
		return priorityMsg{gen: gen, priority: priority, err: err}
	}
}

func (m *model) onPriority(msg priorityMsg) tea.Cmd {
	if msg.gen != m.reqGen {
		return nil
	}
	m.busy, m.reading = "", false
	if msg.err != nil {
		return m.fail(msg.err)
	}
	p := &priorityEdit{}
	for _, c := range msg.priority {
		if slices.ContainsFunc(codecs, func(k struct{ id, label string }) bool { return k.id == c }) && !slices.Contains(p.on, c) {
			p.on = append(p.on, c)
		}
	}
	m.priority, m.panel = p, panelPriority
	return nil
}

// priorityKey edits the order: ↑↓ the row, shift+↑↓ moves a quality that is
// on, space turns one on or off, enter saves, esc leaves it as it was.
func (m *model) priorityKey(key string) (tea.Cmd, bool) {
	p := m.priority
	if m.input.Value() != "" {
		return nil, false
	}
	rows := p.rows()
	p.last = false
	switch key {
	case "up":
		p.cursor = max(0, p.cursor-1)
	case "down":
		p.cursor = min(len(rows)-1, p.cursor+1)
	case "shift+up", "shift+down":
		to := p.cursor - 1
		if key == "shift+down" {
			to = p.cursor + 1
		}
		if p.cursor < len(p.on) && to >= 0 && to < len(p.on) {
			p.on[p.cursor], p.on[to] = p.on[to], p.on[p.cursor]
			p.cursor = to
		}
	case "space":
		c := rows[p.cursor]
		if i := slices.Index(p.on, c); i >= 0 {
			if len(p.on) == 1 {
				p.last = true
				return nil, true
			}
			p.on = slices.Delete(p.on, i, i+1)
		} else {
			p.on = append(p.on, c)
		}
		p.cursor = slices.Index(p.rows(), c)
	case "enter":
		return m.savePriority(slices.Clone(p.on)), true
	case "esc":
		m.priority, m.panel = nil, panelNone
	default:
		return nil, false
	}
	return nil, true
}

func (m *model) savePriority(on []string) tea.Cmd {
	m.priority, m.panel = nil, panelNone
	return m.do("quality priority", func(ctx context.Context) (string, error) {
		body := map[string]any{"download": map[string]any{"quality_priority": on}}
		_, err := m.c.Call(ctx, http.MethodPut, "/api/v1/config", nil, body)
		return "quality priority: " + strings.Join(on, ", ") + ", then AAC-LC", err
	})
}

func (m *model) priorityView(w, room int) string {
	t := m.theme
	p := m.priority
	head := "  " + t.style(t.fg).Render("quality priority")
	if p.last {
		head += t.style(t.warn).Render("  · keep one on")
	}
	lines := []string{truncStyled(head, w)}
	rows := p.rows()
	n := min(len(rows), max(1, room-1))
	start := max(0, p.cursor-n+1)
	for i := start; i < start+n; i++ {
		c := rows[i]
		rs := rowStyle{t, i == p.cursor}
		mark := "  "
		if rs.sel {
			mark = "▸ "
		}
		box, order := rs.s(t.dim).Render("○ "), rs.s(t.dim).Render("   ")
		label := codecs[slices.IndexFunc(codecs, func(k struct{ id, label string }) bool { return k.id == c })].label
		name := rs.s(t.dim).Render(label)
		if k := slices.Index(p.on, c); k >= 0 {
			box, order, name = rs.s(t.accent).Render("● "), rs.s(t.dim).Render(pad(strconv.Itoa(k+1), 3)), rs.bold(t.fg).Render(label)
		}
		lines = append(lines, rs.fill(truncStyled("  "+rs.s(t.accent).Render(mark)+box+order+name, w), w))
	}
	return strings.Join(lines, "\n")
}
