package tui

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"amdl/internal/client"
	"amdl/internal/config"
	"amdl/internal/domain"
	"amdl/internal/format"
)

// ask is a download from search waiting for its choices, the desktop app's
// sheet (apps/desktop/internal/ui#downloadAsk) as a panel: the quality, where
// a hit has lossless and Dolby and the server's priority lists both, and
// whether to replace the files of hits already in the library. A download
// with nothing to choose starts at once.
type ask struct {
	hits     []client.SearchHit
	names    []string
	priority []string
	// codecs are the qualities offered, in priority's order, the first the
	// default; nil asks nothing about quality.
	codecs []string
	codec  int
	// again asks about hits in the library; replace is the choice, 0 to skip
	// their files.
	again   bool
	replace int
	row     int // the question ←→ changes: 0 quality, 1 library
}

type choicesMsg struct {
	gen      int
	hits     []client.SearchHit
	names    []string
	priority []string
	err      error
}

func inLibrary(h client.SearchHit) bool {
	return h.PriorJob != nil && h.PriorJob.Status == domain.JobCompleted
}

// download starts hits, after the ask panel when there is something to
// choose. Which qualities a hit offers depends on the server's priority, read
// first when a hit has lossless and Dolby.
func (m *model) download(hits []client.SearchHit, names []string) tea.Cmd {
	for _, h := range hits {
		if slices.Contains(h.AudioTraits, "atmos") {
			gen := m.read("reading the quality priority")
			ctx, c := m.ctx, m.c
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
				defer cancel()
				priority, err := qualityPriority(ctx, c)
				return choicesMsg{gen: gen, hits: hits, names: names, priority: priority, err: err}
			}
		}
	}
	return m.choose(hits, names, nil)
}

func qualityPriority(ctx context.Context, c *client.Client) ([]string, error) {
	raw, err := c.Call(ctx, http.MethodGet, "/api/v1/config", nil, nil)
	if err != nil {
		return nil, err
	}
	var r struct {
		Config struct {
			Download struct {
				QualityPriority []string `json:"quality_priority"`
			} `json:"download"`
		} `json:"config"`
	}
	err = jsonInto(raw, &r)
	return r.Config.Download.QualityPriority, err
}

func (m *model) onChoices(msg choicesMsg) tea.Cmd {
	if msg.gen != m.reqGen {
		return nil // esc, or another request, came first
	}
	m.busy, m.reading = "", false
	if msg.err != nil {
		return m.fail(msg.err)
	}
	return m.choose(msg.hits, msg.names, msg.priority)
}

func (m *model) choose(hits []client.SearchHit, names []string, priority []string) tea.Cmd {
	a := &ask{hits: hits, names: names, priority: priority}
	for _, h := range hits {
		if a.codecs == nil {
			a.codecs = format.QualityChoices(h.AudioTraits, priority)
		}
		a.again = a.again || inLibrary(h)
	}
	if a.codecs == nil && !a.again {
		return m.send(a)
	}
	if a.codecs == nil {
		a.row = 1
	}
	m.ask, m.panel = a, panelAsk
	return nil
}

// overrides are h's: the chosen quality first, then the rest of the
// server's priority in its order, where h offers the choice; and, for a hit
// in the library, whether its files are replaced, said either way so the
// server's own force_overwrite does not decide.
func (a *ask) overrides(h client.SearchHit) *config.DownloadOverrides {
	var o config.DownloadOverrides
	set := false
	if a.codecs != nil && format.QualityChoices(h.AudioTraits, a.priority) != nil {
		first := a.codecs[a.codec]
		list := []string{first}
		for _, c := range a.priority {
			if c != first {
				list = append(list, c)
			}
		}
		o.QualityPriority, set = &list, true
	}
	if inLibrary(h) {
		replace := a.again && a.replace == 1
		o.ForceOverwrite, set = &replace, true
	}
	if !set {
		return nil
	}
	return &o
}

// batch is one submit: links that share their overrides.
type batch struct {
	urls []string
	o    *config.DownloadOverrides
}

// batches are a's hits grouped by their overrides, in the hits' order.
func (a *ask) batches() []batch {
	var out []batch
	at := map[string]int{}
	for _, h := range a.hits {
		o := a.overrides(h)
		k := fmt.Sprint(o != nil && o.QualityPriority != nil, inLibrary(h))
		i, ok := at[k]
		if !ok {
			i, at[k] = len(out), len(out)
			out = append(out, batch{o: o})
		}
		out[i].urls = append(out[i].urls, h.URL)
	}
	return out
}

// send submits a's hits, one request per set of overrides.
func (m *model) send(a *ask) tea.Cmd {
	m.closePanel()
	m.busy = "queueing"
	you := m.you(strings.Join(a.names, ", "))
	cmds := []tea.Cmd{m.println(func(int) string { return you })}
	for _, b := range a.batches() {
		cmds = append(cmds, submit(m.ctx, m.c, b.urls, b.o))
	}
	return tea.Sequence(cmds...)
}

// askKey moves through the ask panel: ←→ the choice, ↑↓ the question,
// enter downloads, esc goes back to the hits.
func (m *model) askKey(key string) (tea.Cmd, bool) {
	a := m.ask
	if m.input.Value() != "" {
		return nil, false
	}
	n := func() (*int, int) {
		if a.row == 0 {
			return &a.codec, len(a.codecs)
		}
		return &a.replace, 2
	}
	switch key {
	case "left", "right", "tab":
		v, k := n()
		if key == "left" {
			*v = (*v + k - 1) % k
		} else {
			*v = (*v + 1) % k
		}
	case "up", "down":
		if a.codecs != nil && a.again {
			a.row = 1 - a.row
		}
	case "enter":
		return m.send(a), true
	case "esc":
		m.ask, m.panel = nil, panelResults
		if m.res == nil {
			m.panel = panelNone
		}
	default:
		return nil, false
	}
	return nil, true
}

func (m *model) askView(w int) string {
	t := m.theme
	a := m.ask
	what := strings.Join(a.names, ", ")
	if len(a.names) > 1 {
		what = fmt.Sprintf("%d downloads", len(a.names))
	}
	lines := []string{"  " + t.style(t.fg).Render(trunc("download "+what, w-2))}
	question := func(row int, label string, choices []string, chosen int) {
		rs := rowStyle{t, row == a.row}
		mark := "  "
		if rs.sel {
			mark = "▸ "
		}
		line := "  " + rs.s(t.accent).Render(mark) + rs.s(t.dim).Render(pad(label, 12))
		for i, c := range choices {
			s := rs.s(t.dim)
			if i == chosen {
				s = rs.bold(t.fg).Bold(true).Underline(true)
			}
			if i > 0 {
				line += rs.s(t.dim).Render("   ")
			}
			line += s.Render(c)
		}
		lines = append(lines, rs.fill(truncStyled(line, w), w))
	}
	if a.codecs != nil {
		names := make([]string, len(a.codecs))
		for i, c := range a.codecs {
			names[i] = format.CodecName(c)
		}
		question(0, "quality", names, a.codec)
	}
	if a.again {
		choices := []string{"Skip files on disk", "Replace them"}
		if w < mediumCols {
			choices = []string{"Skip", "Replace"}
		}
		question(1, "in library", choices, a.replace)
	}
	return strings.Join(lines, "\n")
}
