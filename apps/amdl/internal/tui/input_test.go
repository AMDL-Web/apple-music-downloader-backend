package tui

import (
	"strings"
	"testing"

	"amdl/internal/client"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestPanelsOpenBelowThePrompt(t *testing.T) {
	m := sample(t)
	before := strings.Split(stripANSI(render(m, 100)), "\n")
	withResults(m)
	after := strings.Split(stripANSI(render(m, 100)), "\n")
	row := func(lines []string) int {
		for i, l := range lines {
			if strings.HasPrefix(l, "╭") {
				return i
			}
		}
		return -1
	}
	if row(before) < 0 || row(before) != row(after) {
		t.Errorf("the prompt moved from row %d to %d when the results opened", row(before), row(after))
	}
	if !strings.Contains(strings.Join(after[row(after):], "\n"), "search · igor") {
		t.Error("the results open below the prompt")
	}
}

func TestPromptSaysSearchNeedsEnter(t *testing.T) {
	m := sample(t)
	m.prompt(100)
	if ph := m.input.Placeholder; !strings.Contains(ph, "press enter to search") || strings.Contains(ph, "type to search") {
		t.Errorf("placeholder %q promises search as you type", ph)
	}
}

func TestLinksAreTakenFromALine(t *testing.T) {
	got := linksIn("https://music.apple.com/us/album/a/1, https://music.apple.com/cn/album/b/2 igor")
	if len(got) != 2 || got[1] != "https://music.apple.com/cn/album/b/2" {
		t.Errorf("links = %v", got)
	}
	if linksIn("tyler igor") != nil {
		t.Error("plain text has no links")
	}
}

func TestQuitNeedsCtrlCTwice(t *testing.T) {
	m := sample(t)
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	m.onKey(ctrlC)
	if m.quitHint.IsZero() || m.ctx.Err() != nil {
		t.Fatal("the first ctrl+c only warns")
	}
	if s := stripANSI(m.status(40)); !strings.Contains(s, "ctrl+c again") || lipgloss.Width(m.status(40)) > 40 {
		t.Errorf("the warning fits a narrow footer: %q", s)
	}
	m.onKey(ctrlC)
	if m.ctx.Err() == nil {
		t.Error("the second ctrl+c quits")
	}
}

func typeText(m *model, s string) {
	for _, r := range s {
		m.onKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func TestMenuRunsOrCompletesTheChosenCommand(t *testing.T) {
	m := sample(t)
	typeText(m, "/jo")
	if out := stripANSI(render(m, 100)); !strings.Contains(out, "▸ /jobs") {
		t.Fatalf("the menu marks its choice:\n%s", out)
	}
	m.onKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.busy == "" || m.input.Value() != "" {
		t.Errorf("enter runs /jobs: busy %q, prompt %q", m.busy, m.input.Value())
	}
	m.busy = ""
	typeText(m, "/ca")
	m.onKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if got := m.input.Value(); got != "/cancel " {
		t.Errorf("enter completes a command that needs an argument, prompt %q", got)
	}
}

func TestAMistypedCodeKeepsTheLogin(t *testing.T) {
	m := sample(t)
	m.panel = panelLogin
	m.loginSt.step, m.loginSt.loginID = "waiting", "login_1"
	m.onLogin(loginMsg{err: &client.APIError{Status: 400, Code: "invalid_request", Message: "a two-step code is six digits"}})
	if m.loginSt.step != "code" || m.loginSt.loginID != "login_1" || m.panel != panelLogin {
		t.Fatalf("after a mistyped code: step %q, login %q, panel %v", m.loginSt.step, m.loginSt.loginID, m.panel)
	}
	m.onLogin(loginMsg{err: &client.APIError{Status: 401, Code: "authentication_failed", Message: "wrong code"}})
	if m.loginSt.loginID != "" || m.panel == panelLogin {
		t.Fatal("a refused code still waits for another")
	}
}

func TestEscDropsASearchInFlight(t *testing.T) {
	m := sample(t)
	typeText(m, "igor")
	m.onKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	gen := m.reqGen
	m.onKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.busy != "" {
		t.Errorf("esc stops waiting, busy %q", m.busy)
	}
	m.update(searchMsg{gen: gen, term: "igor"})
	if m.panel == panelResults {
		t.Error("results that arrive after esc stay closed")
	}
}

func TestSearchHitNamesLineUp(t *testing.T) {
	m := sample(t)
	withResults(m)
	col := -1
	for _, line := range strings.Split(stripANSI(m.resultsView(100, 20)), "\n")[1:] {
		for _, name := range []string{"IGOR", "CALL ME", "EARFQUAKE", "Tyler, The Creator Essentials"} {
			if i := strings.Index(line, name); i >= 0 && strings.HasPrefix(strings.TrimSpace(line[i:]), name) {
				c := lipgloss.Width(line[:i])
				if col >= 0 && c != col {
					t.Errorf("%q starts at column %d, the others at %d", name, c, col)
				}
				col = c
				break
			}
		}
	}
}

// The terminal's own cursor sits right after the prompt's "> ", wherever the
// prompt is, so an input method composes in place.
func TestCursorSitsInThePrompt(t *testing.T) {
	m := sample(t)
	check := func(name string, w int) {
		t.Helper()
		m.width = w
		v := m.View()
		if v.Cursor == nil {
			t.Fatalf("%s: no cursor", name)
		}
		lines := strings.Split(stripANSI(v.Content), "\n")
		y, x := v.Cursor.Position.Y, v.Cursor.Position.X
		if y < 0 || y >= len(lines) {
			t.Fatalf("%s: cursor row %d of %d lines", name, y, len(lines))
		}
		line := []rune(lines[y])
		want := strings.Index(lines[y], "> ")
		if want < 0 {
			t.Fatalf("%s: cursor row %d is %q, not the prompt", name, y, lines[y])
		}
		want = lipgloss.Width(lines[y][:want]) + 2 + lipgloss.Width(m.input.Value())
		if x != want {
			t.Errorf("%s: cursor at column %d, want %d in %q", name, x, want, string(line))
		}
	}
	check("cards", 100)
	typeText(m, "igor")
	check("typed", 100)
	withResults(m)
	check("results open", 100)
	check("no border", 30)
	m.headerDone = false
	check("banner still live", 100)
}
