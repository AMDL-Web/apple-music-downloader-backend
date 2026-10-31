package tui

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"amdl/internal/format"
)

// logoHeights are favicon.svg's four bars, bottom-aligned at heights
// 12 : 18 : 9 : 15, in half rows of a three-row logo.
var logoHeights = [4]int{4, 6, 3, 5}

// logoWidth is four two-cell bars with a cell between them.
const logoWidth = 11

// logo draws the bars with full and lower-half blocks, so their heights read
// without color.
func (t theme) logo() [3]string {
	var rows [3]string
	for r := range rows {
		var b strings.Builder
		for i, h := range logoHeights {
			if i > 0 {
				b.WriteByte(' ')
			}
			top := 6 - h
			cell := "  "
			switch {
			case 2*r >= top:
				cell = "██"
			case 2*r+1 >= top:
				cell = "▄▄"
			}
			b.WriteString(t.style(t.bars[i]).Render(cell))
		}
		rows[r] = b.String()
	}
	return rows
}

// header is the banner the session starts with, like Claude Code's: the logo,
// then amdl and its version, the backend and its wrapper, and where the
// backend saves; below it, what the backend has done so far.
func (m *model) header(w int) string {
	t := m.theme
	name := t.bold().Render("amdl") + t.style(t.dim).Render(" "+m.opts.Version)
	lines := []string{name, m.serverLine(), m.placeLine()}
	textW := w
	if w >= 40 {
		textW = w - logoWidth - 3
	}
	for i := range lines {
		lines[i] = truncStyled(lines[i], textW)
	}
	if w >= 40 {
		logo := t.logo()
		for i := range lines {
			lines[i] = logo[i] + "   " + lines[i]
		}
	}
	if r := m.recent(); r != "" {
		lines = append(lines, "", truncStyled("  "+r, w))
	}
	return strings.Join(lines, "\n") + "\n"
}

// serverLine names the backend and says whether its wrapper can decrypt.
func (m *model) serverLine() string {
	t := m.theme
	server := targetName(m.opts.Target)
	switch {
	case m.connErr != nil:
		return t.style(t.bad).Render("cannot reach " + server + ": " + errText(m.connErr))
	case m.system == nil:
		return t.style(t.dim).Render("connecting to " + server + "…")
	}
	s := t.style(t.dim).Render(server)
	if m.system.Simulate {
		s += t.style(t.dim).Render(" · ") + t.style(t.warn).Render("simulate")
	}
	if m.wrapper != nil {
		if what := wrapperProblem(*m.wrapper); what != "" {
			s += t.style(t.dim).Render(" · ") + t.style(t.bad).Render(what)
		} else {
			w := "wrapper ready"
			if len(m.wrapper.Regions) > 0 {
				w += " (" + strings.Join(m.wrapper.Regions, ", ") + ")"
			}
			s += t.style(t.dim).Render(" · " + w)
		}
	}
	return s
}

// placeLine is where the backend saves and the room left there, or the
// directory amdl runs in before the backend has said.
func (m *model) placeLine() string {
	t := m.theme
	if m.system != nil {
		for _, s := range m.system.Storage {
			if s.Name != "downloads_dir" || s.Path == "" {
				continue
			}
			line := m.shortPath(s.Path)
			if s.Error == "" && s.TotalBytes > 0 {
				line += " · " + format.Bytes(s.FreeBytes) + " free"
			}
			return t.style(t.dim).Render(line)
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return t.style(t.dim).Render(homeShort(wd))
}

// shortPath writes a backend path with ~ for the home directory, but only for
// a backend on this machine, whose paths are this machine's.
func (m *model) shortPath(p string) string {
	u, err := url.Parse(m.c.Base())
	if err != nil {
		return p
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return homeShort(p)
	}
	return p
}

func homeShort(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return p
	}
	if p == home {
		return "~"
	}
	if rel, ok := strings.CutPrefix(p, home+string(filepath.Separator)); ok {
		return "~" + string(filepath.Separator) + rel
	}
	return p
}

// recent is the backend's record so far: saved and failed jobs.
func (m *model) recent() string {
	t := m.theme
	c := m.counts
	var parts []string
	if c.Completed > 0 {
		parts = append(parts, t.style(t.dim).Render(fmt.Sprintf("%d saved", c.Completed)))
	}
	if c.Failed > 0 {
		parts = append(parts, t.style(t.bad).Render(fmt.Sprintf("%d failed", c.Failed))+t.style(t.dim).Render(" (/jobs)"))
	}
	return strings.Join(parts, t.style(t.dim).Render(" · "))
}

// targetName shortens the CLI's "name (http://host:port)" or plain URL to
// "name · host:port".
func targetName(target string) string {
	name, rest, ok := strings.Cut(target, " (")
	if ok {
		return name + " · " + strings.TrimSuffix(stripScheme(rest), ")")
	}
	return stripScheme(target)
}

func stripScheme(s string) string {
	s = strings.TrimPrefix(s, "http://")
	return strings.TrimSuffix(s, "/")
}

// truncStyled cuts a styled line to w columns with an ellipsis.
func truncStyled(s string, w int) string {
	if lipgloss.Width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}
