// Package tui is amdl's terminal interface for people, laid out like Claude
// Code's: it renders inline, so what is done (searches, previews, finished
// downloads) goes into the terminal's own scrollback, and only the live region
// at the bottom is redrawn: the jobs in progress, the panel of the moment, the
// prompt and a status line. It talks to the server through internal/client.
package tui

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"amdl/apps/amdl/internal/jobview"
	"amdl/internal/client"
	"amdl/internal/domain"
	"amdl/internal/progress"
)

// Options are what the TUI needs from the command that starts it.
type Options struct {
	Client *client.Client
	// Target names the server in the header, such as "nas (http://…)".
	Target  string
	Version string
}

// Run shows the TUI until the person quits or ctx ends. Jobs keep running on
// the server after it exits.
func Run(ctx context.Context, opts Options) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := newModel(ctx, cancel, opts)
	p := tea.NewProgram(m)
	m.bus.p = p
	go followFeed(ctx, opts.Client, m.bus)
	_, err := p.Run()
	return err
}

// maxFollowed bounds the jobs whose tracks the TUI streams at once; the
// others show their counters from the feed.
const maxFollowed = 3

type jobState struct {
	job       domain.Job
	detail    *client.DownloadDetail
	index     map[string]int
	rate      progress.Rate
	following bool
}

type panelKind int

const (
	panelNone panelKind = iota
	panelPreview
	panelResults
	panelConfirm
	panelLogin
	panelHelp
)

type pick struct {
	hit      client.SearchHit
	selected bool
}

type results struct {
	term   string
	filter int // index into filters
	items  []pick
	cursor int
	artist *client.SearchHit
	back   *results
	err    string
}

var filters = []string{"all", "album", "song", "playlist", "artist"}

func (r *results) visible() []int {
	var idx []int
	for i, p := range r.items {
		if filters[r.filter] == "all" || p.hit.Type == filters[r.filter] {
			idx = append(idx, i)
		}
	}
	return idx
}

type model struct {
	ctx    context.Context
	cancel context.CancelFunc
	c      *client.Client
	opts   Options
	bus    *bus

	width, height int
	theme         theme
	input         textinput.Model
	history       []string
	histPos       int

	system   *client.SystemInfo
	wrapper  *client.WrapperStatus
	conn     client.ConnState
	jobs     map[string]*jobState
	printed  map[string]bool
	expanded bool
	showAll  bool

	panel    panelKind
	preview  []lookupResult
	links    []string
	res      *results
	confirm  struct{ prompt, id string }
	loginSt  struct{ step, username, password, loginID string }
	quitHint time.Time
	busy     string
	// titles names links whose title a preview or search already showed, for
	// the queued line, since a new job has no title until it resolves.
	titles map[string]string
}

func newModel(ctx context.Context, cancel context.CancelFunc, opts Options) *model {
	in := textinput.New()
	in.Prompt = ""
	in.Focus()
	m := &model{ctx: ctx, cancel: cancel, c: opts.Client, opts: opts, bus: &bus{},
		width: 100, height: 30, theme: newTheme(true), input: in,
		jobs: map[string]*jobState{}, printed: map[string]bool{}, titles: map[string]string{}}
	m.restyleInput()
	return m
}

func (m *model) restyleInput() {
	s := textinput.DefaultStyles(m.theme.dark)
	s.Focused.Text = m.theme.style(m.theme.fg)
	s.Focused.Placeholder = m.theme.style(m.theme.dim)
	m.input.SetStyles(s)
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(connect(m.ctx, m.c), tea.RequestBackgroundColor)
}

func jsonInto(raw []byte, v any) error { return json.Unmarshal(raw, v) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.BackgroundColorMsg:
		m.theme = newTheme(msg.IsDark())
		m.restyleInput()
		return m, nil
	case connectedMsg:
		if msg.err != nil {
			return m, tea.Println(m.theme.style(m.theme.bad).Render("✗ ") + "cannot reach " + m.opts.Target + ": " + errText(msg.err))
		}
		m.system, m.wrapper = &msg.system, msg.wrapper
		return m, tea.Println(m.header())
	case snapshotMsg:
		return m, m.onSnapshot(msg.list)
	case feedMsg:
		return m, m.onFeed(msg.m)
	case connMsg:
		m.conn = msg.s
		return m, nil
	case detailMsg:
		if st := m.jobs[msg.d.Job.ID]; st != nil {
			d := msg.d
			st.detail, st.index, st.job = &d, jobview.Index(d), d.Job
			st.rate.Add(time.Now(), float64(progress.AlbumProgress(d.Job.Status, d.Items).Percent)/100)
		}
		return m, nil
	case eventMsg:
		if st := m.jobs[msg.ev.JobID]; st != nil && st.detail != nil {
			jobview.Apply(st.detail, st.index, msg.ev)
			st.job = st.detail.Job
			st.rate.Add(time.Now(), float64(progress.AlbumProgress(st.job.Status, st.detail.Items).Percent)/100)
		}
		return m, nil
	case followedMsg:
		if st := m.jobs[msg.id]; st != nil {
			st.following = false
		}
		return m, m.followMore()
	case lookupMsg:
		m.busy = ""
		m.panel, m.preview, m.links = panelPreview, msg.lookups, msg.links
		for _, l := range msg.lookups {
			if l.err == nil && l.l.Title != "" {
				m.titles[l.link] = l.l.Title
			}
		}
		return m, nil
	case searchMsg:
		return m, m.onSearch(msg)
	case albumsMsg:
		return m, m.onAlbums(msg)
	case submitMsg:
		m.busy = ""
		return m, tea.Println(m.submitted(msg))
	case printMsg:
		m.busy = ""
		return m, tea.Println(msg.text)
	case loginMsg:
		return m, m.onLogin(msg)
	case tea.PasteMsg:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(tea.PasteMsg{Content: strings.Join(strings.Fields(msg.Content), " ")})
		return m, cmd
	case tea.KeyPressMsg:
		return m, m.onKey(msg)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) onSnapshot(l client.DownloadList) tea.Cmd {
	m.jobs = map[string]*jobState{}
	for _, j := range l.Downloads {
		if client.Terminal(j.Status) {
			m.printed[j.ID] = true
			continue
		}
		delete(m.printed, j.ID)
		m.jobs[j.ID] = &jobState{job: j}
	}
	return m.followMore()
}

func (m *model) onFeed(f domain.DownloadFeedMessage) tea.Cmd {
	if f.Type == "download_deleted" {
		delete(m.jobs, f.JobID)
		return nil
	}
	if f.Job == nil {
		return nil
	}
	j := *f.Job
	st := m.jobs[j.ID]
	if client.Terminal(j.Status) {
		if m.printed[j.ID] {
			delete(m.jobs, j.ID)
			return nil
		}
		m.printed[j.ID] = true
		var d *client.DownloadDetail
		if st != nil {
			d = st.detail
		}
		delete(m.jobs, j.ID)
		return tea.Batch(tea.Println(m.finishedLine(j, d)), m.followMore())
	}
	// A retried job runs again under its ID and finishes again.
	delete(m.printed, j.ID)
	if st == nil {
		st = &jobState{}
		m.jobs[j.ID] = st
	}
	if st.detail == nil {
		st.job = j
	} else {
		st.job.Status, st.job.Title, st.job.TotalItems = j.Status, j.Title, j.TotalItems
		st.job.ArtistName, st.job.ArtworkBgColor = j.ArtistName, j.ArtworkBgColor
	}
	return m.followMore()
}

// followMore starts streaming the tracks of running jobs, oldest first, up to
// maxFollowed.
func (m *model) followMore() tea.Cmd {
	n := 0
	for _, st := range m.jobs {
		if st.following {
			n++
		}
	}
	for _, id := range m.activeIDs() {
		st := m.jobs[id]
		if n >= maxFollowed {
			break
		}
		if !st.following && st.job.Status == domain.JobRunning {
			st.following = true
			n++
			go followJob(m.ctx, m.c, m.bus, id)
		}
	}
	return nil
}

// activeIDs orders the jobs in progress: running before queued, then by when
// they were created.
func (m *model) activeIDs() []string {
	ids := make([]string, 0, len(m.jobs))
	for id := range m.jobs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, k int) bool {
		a, b := m.jobs[ids[i]].job, m.jobs[ids[k]].job
		if (a.Status == domain.JobRunning) != (b.Status == domain.JobRunning) {
			return a.Status == domain.JobRunning
		}
		return a.CreatedAt.Before(b.CreatedAt)
	})
	return ids
}

func (m *model) onSearch(msg searchMsg) tea.Cmd {
	m.busy = ""
	r := &results{term: msg.term}
	if msg.err != nil {
		r.err = errText(msg.err)
	}
	for _, g := range []*client.HitGroup{msg.res.Albums, msg.res.Songs, msg.res.Playlists, msg.res.Artists} {
		if g == nil {
			continue
		}
		for _, h := range g.Items {
			r.items = append(r.items, pick{hit: h})
		}
	}
	m.res, m.panel = r, panelResults
	return nil
}

func (m *model) onAlbums(msg albumsMsg) tea.Cmd {
	m.busy = ""
	artist := msg.artist
	r := &results{term: artist.Name, artist: &artist, back: m.res}
	if msg.err != nil {
		r.err = errText(msg.err)
	}
	for _, h := range msg.res.Albums.Items {
		r.items = append(r.items, pick{hit: h})
	}
	m.res, m.panel = r, panelResults
	return nil
}

func (m *model) onLogin(msg loginMsg) tea.Cmd {
	m.busy = ""
	ls := &m.loginSt
	switch {
	case msg.err != nil && ls.loginID != "" && isAPIError(msg.err, "invalid_request"):
		// A mistyped code: the login still waits for the right one.
		ls.step = "code"
		return tea.Println(m.theme.style(m.theme.bad).Render("  ⎿ ") + errText(msg.err) + "; enter the code again")
	case msg.err != nil:
		m.endLogin()
		return tea.Println(m.theme.style(m.theme.bad).Render("  ⎿ ") + "sign-in failed: " + errText(msg.err))
	case msg.status == "needs_2fa":
		ls.step, ls.loginID = "code", msg.loginID
		m.input.EchoMode = textinput.EchoNormal
		m.input.SetValue("")
		return nil
	}
	m.endLogin()
	return tea.Batch(tea.Println(m.theme.style(m.theme.ok).Render("  ⎿ ")+"signed in"), connect(m.ctx, m.c))
}

func (m *model) endLogin() {
	m.loginSt = struct{ step, username, password, loginID string }{}
	m.panel = panelNone
	m.input.EchoMode = textinput.EchoNormal
	m.input.SetValue("")
}
