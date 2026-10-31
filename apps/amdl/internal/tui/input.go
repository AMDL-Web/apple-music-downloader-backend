package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"amdl/internal/client"
	"amdl/internal/domain"
	"amdl/internal/format"
)

// slash is one command of the / menu.
type slash struct{ name, args, help string }

var slashes = []slash{
	{"/search", "<text>", "search the catalog (typing plain text does the same)"},
	{"/download", "<link>…", "download links (pasting a link does the same)"},
	{"/jobs", "", "list recent jobs"},
	{"/cancel", "<job>", "cancel a queued or running job"},
	{"/retry", "<job>", "re-run a failed or cancelled job"},
	{"/remove", "<job>", "delete a finished job's records (asks first)"},
	{"/copy", "<job>", "copy a job's link"},
	{"/library", "[text]", "list finished downloads"},
	{"/logs", "[job]", "show recent warnings and errors"},
	{"/system", "", "show the server's version, queue and free space"},
	{"/wrapper", "", "show whether an Apple ID is signed in"},
	{"/login", "", "sign the wrapper in to an Apple ID"},
	{"/quality", "", "order the qualities the server tries, or turn them off"},
	{"/config", "[key]", "show the runtime config or one key"},
	{"/reload", "", "re-read config.yaml on the server"},
	{"/help", "", "keys and commands"},
	{"/quit", "", "leave; downloads keep running on the server"},
}

func (m *model) onKey(k tea.KeyPressMsg) tea.Cmd {
	key := k.String()
	if key != "ctrl+c" {
		m.quitHint = time.Time{}
	}
	switch key {
	case "ctrl+c":
		if m.input.Value() != "" || m.panel != panelNone {
			m.closePanel()
			m.input.SetValue("")
			return nil
		}
		if !m.quitHint.IsZero() && time.Since(m.quitHint) < 2*time.Second {
			return m.quit()
		}
		m.quitHint = time.Now()
		return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return nil })
	case "ctrl+d":
		if m.input.Value() == "" {
			return m.quit()
		}
	case "ctrl+l":
		return m.redraw()
	case "ctrl+o":
		m.expanded = !m.expanded
		return nil
	case "ctrl+t":
		m.showAll = !m.showAll
		return nil
	}
	switch m.panel {
	case panelLogin:
		return m.loginKey(k)
	case panelConfirm:
		switch key {
		case "y", "Y", "enter":
			id, ref := m.confirm.id, m.ref(m.confirm.id)
			m.panel = panelNone
			return m.do("removing", func(ctx context.Context) (string, error) {
				_, err := m.c.Call(ctx, http.MethodDelete, "/api/v1/downloads/"+id, nil, nil)
				return "removed " + ref, err
			})
		case "n", "N", "esc":
			m.panel = panelNone
			text := m.reply("kept " + m.ref(m.confirm.id))
			return m.println(func(int) string { return text })
		}
		return nil
	case panelResults:
		if cmd, ok := m.resultsKey(key); ok {
			return cmd
		}
	case panelAsk:
		if cmd, ok := m.askKey(key); ok {
			return cmd
		}
	case panelPriority:
		if cmd, ok := m.priorityKey(key); ok {
			return cmd
		}
	case panelPreview:
		if key == "esc" {
			m.closePanel()
			return nil
		}
		if key == "enter" && strings.TrimSpace(m.input.Value()) == "" {
			links := m.links
			m.closePanel()
			m.busy = "queueing"
			return submit(m.ctx, m.c, links, nil)
		}
	case panelHelp:
		if key == "esc" || key == "?" {
			m.panel = panelNone
			return nil
		}
	}
	if ms := m.menu(); len(ms) > 0 {
		if cmd, ok := m.menuKey(key, ms); ok {
			return cmd
		}
	}
	switch key {
	case "enter":
		return m.enter()
	case "esc":
		m.dropRead()
		m.input.SetValue("")
		return nil
	case "up", "down":
		if len(m.history) == 0 {
			return nil
		}
		if key == "up" && m.histPos > 0 {
			m.histPos--
		} else if key == "down" && m.histPos < len(m.history) {
			m.histPos++
		}
		if m.histPos < len(m.history) {
			m.input.SetValue(m.history[m.histPos])
		} else {
			m.input.SetValue("")
		}
		m.input.CursorEnd()
		return nil
	case "?":
		if m.input.Value() == "" {
			m.panel = panelHelp
			return nil
		}
	}
	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	if m.input.Value() != before {
		m.menuSel = 0
	}
	return cmd
}

// menuKey handles the keys of the / menu; ok is false for keys the prompt
// should get instead. Enter runs the chosen command, or completes it when it
// takes an argument.
func (m *model) menuKey(key string, ms []slash) (tea.Cmd, bool) {
	sel := min(m.menuSel, len(ms)-1)
	complete := func() {
		m.input.SetValue(ms[sel].name + " ")
		m.input.CursorEnd()
		m.menuSel = 0
	}
	switch key {
	case "up":
		m.menuSel = (sel + len(ms) - 1) % len(ms)
	case "down":
		m.menuSel = (sel + 1) % len(ms)
	case "tab":
		complete()
	case "enter":
		if ms[sel].args != "" && !strings.HasPrefix(ms[sel].args, "[") {
			complete()
			return nil, true
		}
		m.input.SetValue(ms[sel].name)
		m.menuSel = 0
		return m.enter(), true
	default:
		return nil, false
	}
	return nil, true
}

func (m *model) quit() tea.Cmd {
	m.cancel()
	return tea.Sequence(tea.Println(m.theme.style(m.theme.dim).Render("  downloads keep running on the server")), tea.Quit)
}

func (m *model) closePanel() {
	if m.panel == panelLogin {
		m.endLogin()
	}
	m.panel, m.preview, m.links, m.res, m.ask, m.priority = panelNone, nil, nil, nil, nil, nil
}

// resultsKey handles the keys of the search list; ok is false for keys the
// prompt should get instead.
func (m *model) resultsKey(key string) (tea.Cmd, bool) {
	r := m.res
	vis := r.visible()
	switch key {
	case "up":
		if r.cursor > 0 {
			r.cursor--
		}
	case "down":
		if r.cursor < len(vis)-1 {
			r.cursor++
		}
		return m.morePages(r), true
	case "space":
		if m.input.Value() != "" || len(vis) == 0 {
			return nil, false
		}
		p := &r.items[vis[r.cursor]]
		on := !p.selected
		for i := range r.items {
			if r.items[i].hit.URL == p.hit.URL {
				r.items[i].selected = on
			}
		}
	case "tab":
		if r.artist == nil {
			r.filter = (r.filter + 1) % len(filters)
			r.cursor = 0
			return m.morePages(r), true
		}
	case "right":
		if m.input.Value() != "" || len(vis) == 0 {
			return nil, false
		}
		if h := r.items[vis[r.cursor]].hit; h.Type == "artist" {
			return artistAlbums(m.ctx, m.c, m.read("reading "+h.Name+"'s albums"), h), true
		}
	case "left":
		if m.input.Value() != "" || r.back == nil {
			return nil, false
		}
		m.res = r.back
	case "esc":
		m.dropRead()
		m.closePanel()
	case "enter":
		if strings.TrimSpace(m.input.Value()) != "" {
			return nil, false
		}
		var hits []client.SearchHit
		var names []string
		seen := map[string]bool{}
		for _, p := range r.items {
			if p.selected && !seen[p.hit.URL] {
				seen[p.hit.URL] = true
				hits, names = append(hits, p.hit), append(names, p.hit.Name)
				m.titles[p.hit.URL] = p.hit.Name
			}
		}
		if len(hits) == 0 && len(vis) > 0 {
			h := r.items[vis[r.cursor]].hit
			if h.Type == "artist" {
				return artistAlbums(m.ctx, m.c, m.read("reading "+h.Name+"'s albums"), h), true
			}
			hits, names = []client.SearchHit{h}, []string{h.Name}
			m.titles[h.URL] = h.Name
		}
		if len(hits) == 0 {
			return nil, true
		}
		return m.download(hits, names), true
	default:
		return nil, false
	}
	return nil, true
}

func (m *model) enter() tea.Cmd {
	line := strings.TrimSpace(m.input.Value())
	if line == "" {
		return nil
	}
	m.input.SetValue("")
	m.history = append(m.history, line)
	m.histPos = len(m.history)
	you := m.you(unescape(line))
	echo := m.println(func(int) string { return you })
	m.closePanel()
	if strings.HasPrefix(line, "/") {
		return tea.Sequence(echo, m.command(line))
	}
	if links := linksIn(line); len(links) > 0 {
		return tea.Sequence(echo, lookupLinks(m.ctx, m.c, m.read("looking up"), links))
	}
	return tea.Sequence(echo, search(m.ctx, m.c, m.read("searching"), line))
}

// read starts a request whose answer only opens a panel or prints a list, so
// esc or a newer request can drop it; the answer carries the returned number.
func (m *model) read(busy string) int {
	m.reqGen++
	m.reading, m.busy = true, busy
	return m.reqGen
}

// dropRead forgets the read in flight: its answer arrives under an old gen
// and goes unread.
func (m *model) dropRead() {
	if m.reading {
		m.reqGen++
		m.reading, m.busy = false, ""
	}
}

// unescape shows a pasted link's percent-encoded titles as text.
func unescape(line string) string {
	if !strings.Contains(line, "%") {
		return line
	}
	if s, err := url.PathUnescape(line); err == nil {
		return s
	}
	return line
}

// linksIn returns the Apple Music links in a line, or nil when it has none.
func linksIn(line string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(line, func(r rune) bool { return r == ' ' || r == ',' || r == ';' || r == '\t' }) {
		if strings.HasPrefix(f, "https://music.apple.com/") || strings.HasPrefix(f, "http://music.apple.com/") || strings.HasPrefix(f, "music.apple.com/") {
			out = append(out, f)
		}
	}
	return out
}

func (m *model) menu() []slash {
	v := strings.TrimSpace(m.input.Value())
	if !strings.HasPrefix(v, "/") || strings.Contains(v, " ") {
		return nil
	}
	var out []slash
	for _, s := range slashes {
		if strings.HasPrefix(s.name, v) {
			out = append(out, s)
		}
	}
	return out
}

// do runs a request off the update loop and prints its answer, or the error.
func (m *model) do(busy string, fn func(ctx context.Context) (string, error)) tea.Cmd {
	m.busy = busy
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
		defer cancel()
		out, err := fn(ctx)
		if out == "" {
			out = busy
		}
		return doneMsg{out: out, err: err}
	}
}

// jobRef resolves what a person typed for a job: its session number, with or
// without #, or the start of its ID.
func (m *model) jobRef(ref string) string {
	ref = strings.TrimPrefix(ref, "#")
	if n, err := strconv.Atoi(ref); err == nil && len(ref) <= 4 {
		for id, k := range m.num {
			if k == n {
				return id
			}
		}
	}
	if !strings.HasPrefix(ref, "job_") {
		ref = "job_" + ref
	}
	for id := range m.num {
		if strings.HasPrefix(id, ref) {
			return id
		}
	}
	return ref
}

// say prints a reply under the line the person entered.
func (m *model) say(s string) tea.Cmd {
	text := m.reply(s)
	return m.println(func(int) string { return text })
}

func (m *model) command(line string) tea.Cmd {
	fields := strings.Fields(line)
	name, args := fields[0], fields[1:]
	arg := strings.Join(args, " ")
	needJob := func() (string, bool) {
		if len(args) != 1 {
			return "", false
		}
		return m.jobRef(args[0]), true
	}
	switch name {
	case "/help":
		m.panel = panelHelp
		return nil
	case "/quit", "/exit":
		return m.quit()
	case "/search":
		if arg == "" {
			return m.say("usage: /search <text>")
		}
		return search(m.ctx, m.c, m.read("searching"), arg)
	case "/download":
		links := linksIn(arg)
		if len(links) == 0 {
			return m.say("usage: /download <link>…")
		}
		m.busy = "queueing"
		return submit(m.ctx, m.c, links, nil)
	case "/jobs":
		return listJobs(m.ctx, m.c, m.read("reading jobs"), client.ListQuery{Limit: 10})
	case "/library":
		return listJobs(m.ctx, m.c, m.read("reading the library"), client.ListQuery{Limit: 10, Status: []domain.JobStatus{domain.JobCompleted}, Search: arg, Sort: "updated_at"})
	case "/cancel", "/retry":
		id, ok := needJob()
		if !ok {
			return m.say("usage: " + name + " <job>")
		}
		ref := m.ref(id)
		return m.do(strings.TrimPrefix(name, "/")+" sent", func(ctx context.Context) (string, error) {
			if name == "/cancel" {
				return "cancelling " + ref, m.c.Cancel(ctx, id)
			}
			return "retrying " + ref, m.c.Retry(ctx, id)
		})
	case "/remove":
		id, ok := needJob()
		if !ok {
			return m.say("usage: /remove <job>")
		}
		if st := m.jobs[id]; st != nil && !st.job.Status.IsTerminal() {
			return m.say(m.ref(id) + " has not finished; /cancel " + strings.TrimPrefix(m.ref(id), "#") + " stops it")
		}
		m.confirm.id, m.confirm.prompt = id, "delete the records of "+m.ref(id)+"? the files stay on disk"
		m.panel = panelConfirm
		return nil
	case "/copy":
		id, ok := needJob()
		if !ok {
			return m.say("usage: /copy <job>")
		}
		m.busy = "copying"
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
			defer cancel()
			d, err := m.c.Download(ctx, id)
			return doneMsg{out: "copied " + d.Job.Input, clip: d.Job.Input, err: err}
		}
	case "/system":
		return m.do("system", func(ctx context.Context) (string, error) {
			s, err := m.c.System(ctx)
			if err != nil {
				return "", err
			}
			version := s.Version
			if s.Commit != "" {
				version += " (" + s.Commit + ")"
			}
			out := fmt.Sprintf("amdl %s · %s/%s · up %s · %d/%d workers busy · queue %d/%d",
				version, s.OS, s.Arch, format.Duration(time.Duration(s.UptimeSeconds)*time.Second),
				s.Jobs.Active, s.Jobs.Workers, s.Jobs.QueueDepth, s.Jobs.QueueCapacity)
			for _, st := range s.Storage {
				if st.Error == "" {
					out += fmt.Sprintf("\n    %s: %s free of %s", st.Name, format.Bytes(st.FreeBytes), format.Bytes(st.TotalBytes))
				}
			}
			return out, nil
		})
	case "/wrapper":
		return m.do("wrapper", func(ctx context.Context) (string, error) {
			w, err := m.c.WrapperStatus(ctx)
			if err != nil {
				return "", err
			}
			if what := wrapperProblem(w); what != "" {
				return what, nil
			}
			return "signed in · regions " + strings.Join(w.Regions, ", "), nil
		})
	case "/login":
		m.panel = panelLogin
		m.loginSt.step = "username"
		m.input.SetValue("")
		return nil
	case "/logs":
		q := map[string][]string{"level": {"warn,error"}, "limit": {"8"}}
		if arg != "" {
			q["job_id"] = []string{m.jobRef(arg)}
		}
		return m.do("logs", func(ctx context.Context) (string, error) {
			raw, err := m.c.Call(ctx, http.MethodGet, "/api/v1/logs", q, nil)
			if err != nil {
				return "", err
			}
			var r struct {
				Logs []struct {
					Time       time.Time      `json:"time"`
					Level      string         `json:"level"`
					Message    string         `json:"message"`
					Attributes map[string]any `json:"attributes"`
				} `json:"logs"`
			}
			if err := jsonInto(raw, &r); err != nil {
				return "", err
			}
			if len(r.Logs) == 0 {
				return "no warnings or errors buffered", nil
			}
			var b strings.Builder
			for i, l := range r.Logs {
				if i > 0 {
					b.WriteString("\n    ")
				}
				msg := l.Message
				if e, ok := l.Attributes["error"].(string); ok && e != "" {
					msg += ": " + e
				}
				fmt.Fprintf(&b, "%s %-5s %s", format.Seconds(l.Time), l.Level, msg)
			}
			return b.String(), nil
		})
	case "/quality":
		return m.openPriority()
	case "/config":
		return m.do("config", func(ctx context.Context) (string, error) {
			raw, err := m.c.Call(ctx, http.MethodGet, "/api/v1/config", nil, nil)
			if err != nil {
				return "", err
			}
			var r struct {
				Config map[string]any `json:"config"`
			}
			if err := jsonInto(raw, &r); err != nil {
				return "", err
			}
			var v any = r.Config
			for _, part := range strings.Split(arg, ".") {
				if part == "" {
					continue
				}
				obj, ok := v.(map[string]any)
				if !ok || obj[part] == nil {
					return "", fmt.Errorf("%s is not a runtime config key", arg)
				}
				v = obj[part]
			}
			out, _ := json.MarshalIndent(v, "    ", "  ") // values decoded from JSON marshal back
			return string(out), nil
		})
	case "/reload":
		return m.do("config reloaded", func(ctx context.Context) (string, error) {
			_, err := m.c.Call(ctx, http.MethodPost, "/api/v1/config/reload", nil, nil)
			return "", err
		})
	}
	return m.say("unknown command " + name + " · /help")
}

// loginKey walks the sign-in: Apple ID, password (never echoed), then the
// two-step code when the wrapper asks for one.
func (m *model) loginKey(k tea.KeyPressMsg) tea.Cmd {
	ls := &m.loginSt
	switch k.String() {
	case "esc":
		m.endLogin()
		text := m.reply("sign-in cancelled")
		return m.println(func(int) string { return text })
	case "enter":
		v := m.input.Value()
		m.input.SetValue("")
		switch ls.step {
		case "username":
			if strings.TrimSpace(v) == "" {
				return nil
			}
			ls.username, ls.step = strings.TrimSpace(v), "password"
			m.input.EchoMode = textinput.EchoPassword
			return nil
		case "password":
			ls.step = "waiting"
			m.busy = "signing in"
			return login(m.ctx, m.c, ls.username, v)
		case "code":
			ls.step = "waiting"
			m.busy = "checking the code"
			return twoStep(m.ctx, m.c, ls.loginID, strings.TrimSpace(v))
		}
		return nil
	}
	if ls.step == "waiting" {
		return nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return cmd
}
