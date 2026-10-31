// Package tui is amdl's terminal interface for people, laid out like Claude
// Code's: it renders inline, so what is done (the banner, searches, finished
// downloads) goes into the terminal's own scrollback, and only the live region
// at the bottom is redrawn: the jobs in progress, the prompt, the panel of the
// moment below it and a status line. It talks to the server through
// internal/client.
package tui

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

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
	_, err := New(ctx, opts).Run()
	return err
}

// New builds the TUI as a program and starts following the server's jobs,
// for the caller to run. A host that is not a terminal, such as the browser
// demo, passes its own input, output and window size as options.
func New(ctx context.Context, opts Options, popts ...tea.ProgramOption) *tea.Program {
	ctx, cancel := context.WithCancel(ctx)
	m := newModel(ctx, cancel, opts)
	if os.Getenv("NO_COLOR") != "" {
		// no-color.org: any non-empty value. colorprofile only honors values
		// that parse as true.
		m.profile = colorprofile.ASCII
		popts = append(popts, tea.WithColorProfile(colorprofile.ASCII))
	}
	p := tea.NewProgram(m, popts...)
	m.bus.p = p
	go followFeed(ctx, opts.Client, m.bus)
	return p
}

// maxFollowed bounds the jobs whose tracks the TUI streams at once; the
// others show their counters from the feed.
const maxFollowed = 3

// redrawDelay lets a terminal being dragged to a new width settle before the
// screen is redrawn.
const redrawDelay = 150 * time.Millisecond

// headerWait bounds how long the banner waits for the terminal's background
// and the job counts; it still waits for the server to answer.
const headerWait = 2 * time.Second

// maxTranscript bounds the entries kept for redrawing the scrollback.
const maxTranscript = 500

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
	panelAsk
	panelPriority
)

type pick struct {
	hit      client.SearchHit
	selected bool
	page     bool // read under its filter's tab, so shown there and not under all
}

type results struct {
	term   string
	filter int // index into filters
	items  []pick
	cursor int
	artist *client.SearchHit
	back   *results
	err    string
	// paged are the tabs read a page at a time, next the offset of each
	// one's next page (0 when none is left), loading a page in flight.
	paged   map[string]bool
	next    map[string]int
	loading bool
}

var filters = []string{"all", "album", "song", "playlist", "artist"}

func (r *results) visible() []int {
	f := filters[r.filter]
	var idx []int
	for i, p := range r.items {
		if f == "all" && !p.page || p.hit.Type == f && p.page == r.paged[f] {
			idx = append(idx, i)
		}
	}
	return idx
}

// entry is one block of scrollback, kept so a resize can print it again at
// the new width.
type entry func(w int) string

type model struct {
	ctx    context.Context
	cancel context.CancelFunc
	c      *client.Client
	opts   Options
	bus    *bus

	width, height int
	profile       colorprofile.Profile
	sized         bool
	resizeGen     int
	theme         theme
	input         textinput.Model
	history       []string
	histPos       int

	system      *client.SystemInfo
	wrapper     *client.WrapperStatus
	connErr     error
	conn        client.ConnState
	counts      domain.JobStatusCounts
	gotCounts   bool
	bgKnown     bool
	headerWait  bool
	headerDone  bool
	held        []entry // printed before the banner, which comes first
	transcript  []entry
	promptRow   int
	promptInset int

	jobs     map[string]*jobState
	printed  map[string]bool
	num      map[string]int // the session's number for each job, from 1
	nums     int
	expanded bool
	showAll  bool

	panel    panelKind
	menuSel  int
	preview  []lookupResult
	links    []string
	res      *results
	ask      *ask
	priority *priorityEdit
	confirm  struct{ prompt, id string }
	loginSt  struct{ step, username, password, loginID string }
	quitHint time.Time
	now      func() time.Time
	busy     string
	reqGen   int
	reading  bool // busy is a read that esc can drop
	spinning bool
	spin     int
	// titles names links whose title a preview or search already showed, for
	// the queued line, since a new job has no title until it resolves.
	titles map[string]string
}

func newModel(ctx context.Context, cancel context.CancelFunc, opts Options) *model {
	in := textinput.New()
	in.Prompt = ""
	in.SetVirtualCursor(false) // the terminal's own cursor, where an IME composes
	in.Focus()
	m := &model{ctx: ctx, cancel: cancel, c: opts.Client, opts: opts, bus: &bus{},
		width: 100, height: 30, now: time.Now, profile: colorprofile.TrueColor, theme: newTheme(true, nil), input: in, headerWait: true,
		jobs: map[string]*jobState{}, printed: map[string]bool{}, num: map[string]int{}, titles: map[string]string{}}
	m.restyleInput()
	return m
}

func (m *model) restyleInput() {
	s := textinput.DefaultStyles(m.theme.dark)
	s.Focused.Text = m.theme.style(m.theme.fg)
	s.Focused.Placeholder = m.theme.style(m.theme.dim)
	s.Cursor.Color = m.theme.accent
	m.input.SetStyles(s)
}

type (
	resizedMsg     struct{ gen int }
	headerTimedOut struct{}
	spinMsg        struct{}
)

func (m *model) Init() tea.Cmd {
	return tea.Batch(connect(m.ctx, m.c), tea.RequestBackgroundColor,
		tea.Tick(headerWait, func(time.Time) tea.Msg { return headerTimedOut{} }))
}

func jsonInto(raw []byte, v any) error { return json.Unmarshal(raw, v) }

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	wasBusy := m.busy != ""
	cmd := m.update(msg)
	if m.busy != "" && !wasBusy {
		m.spin = 0
	}
	if m.busy != "" && !m.spinning {
		m.spinning = true
		cmd = tea.Batch(cmd, spinTick())
	}
	return m, cmd
}

func spinTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return spinMsg{} })
}

func (m *model) update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		widthChanged := m.sized && msg.Width != m.width
		m.width, m.height, m.sized = msg.Width, msg.Height, true
		if !widthChanged {
			return nil
		}
		m.resizeGen++
		gen := m.resizeGen
		return tea.Tick(redrawDelay, func(time.Time) tea.Msg { return resizedMsg{gen} })
	case resizedMsg:
		if msg.gen != m.resizeGen {
			return nil
		}
		return m.redraw()
	case spinMsg:
		if m.busy == "" {
			m.spinning = false
			return nil
		}
		m.spin++
		return spinTick()
	case tea.ColorProfileMsg:
		m.profile = msg.Profile
		return nil
	case tea.BackgroundColorMsg:
		m.theme, m.bgKnown = newTheme(msg.IsDark(), msg.Color), true
		m.restyleInput()
		return m.commitHeader()
	case connectedMsg:
		if msg.err != nil {
			if m.headerDone {
				return m.println(func(int) string {
					return m.theme.style(m.theme.bad).Render("✗ ") + "cannot reach " + targetName(m.opts.Target) + ": " + errText(msg.err)
				})
			}
			m.connErr = msg.err
			return m.commitHeader()
		}
		reconnected := m.headerDone && m.system == nil
		m.connErr, m.system, m.wrapper = nil, &msg.system, msg.wrapper
		if reconnected {
			line := m.serverLine()
			return m.println(func(int) string { return m.theme.style(m.theme.ok).Render("● ") + line })
		}
		return m.commitHeader()
	case headerTimedOut:
		m.headerWait = false
		return m.commitHeader()
	case snapshotMsg:
		m.counts, m.gotCounts = msg.list.StatusCounts, true
		return tea.Batch(m.onSnapshot(msg.list), m.commitHeader())
	case feedMsg:
		return m.onFeed(msg.m)
	case connMsg:
		was := m.conn.Phase
		m.conn = msg.s
		if msg.s.Phase == client.Live && was != client.Live && m.headerDone && m.system == nil {
			return connect(m.ctx, m.c)
		}
		return nil
	case detailMsg:
		if st := m.jobs[msg.d.Job.ID]; st != nil {
			d := msg.d
			st.detail, st.index, st.job = &d, jobview.Index(d), d.Job
			st.rate.Add(m.now(), progress.JobStages(d.Job, d.Items).Weighted())
		}
		return nil
	case eventMsg:
		if st := m.jobs[msg.ev.JobID]; st != nil && st.detail != nil {
			jobview.Apply(st.detail, st.index, msg.ev)
			st.job = st.detail.Job
			st.rate.Add(m.now(), progress.JobStages(st.job, st.detail.Items).Weighted())
		}
		return nil
	case followedMsg:
		if st := m.jobs[msg.id]; st != nil {
			st.following = false
		}
		return m.followMore()
	case lookupMsg:
		if msg.gen != m.reqGen {
			return nil
		}
		m.busy, m.reading = "", false
		m.panel, m.preview, m.links = panelPreview, msg.lookups, msg.links
		for _, l := range msg.lookups {
			if l.err == nil && l.l.Title != "" {
				m.titles[l.link] = l.l.Title
			}
		}
		return nil
	case searchMsg:
		if msg.gen != m.reqGen {
			return nil
		}
		m.reading = false
		return m.onSearch(msg)
	case pageMsg:
		return m.onPage(msg)
	case choicesMsg:
		return m.onChoices(msg)
	case priorityMsg:
		return m.onPriority(msg)
	case albumsMsg:
		if msg.gen != m.reqGen {
			return nil
		}
		m.reading = false
		return m.onAlbums(msg)
	case submitMsg:
		m.busy = ""
		for _, r := range msg.batch.Results {
			if r.Job != nil {
				m.number(r.Job.ID)
			}
		}
		return m.println(func(int) string { return m.submitted(msg) })
	case doneMsg:
		m.busy = ""
		if msg.err != nil {
			return m.fail(msg.err)
		}
		if msg.clip != "" {
			return tea.Batch(tea.SetClipboard(msg.clip), m.say(msg.out))
		}
		return m.say(msg.out)
	case jobsMsg:
		if msg.gen != m.reqGen {
			return nil
		}
		m.busy, m.reading = "", false
		if msg.err != nil {
			return m.fail(msg.err)
		}
		return m.say(m.jobTable(msg.jobs))
	case loginMsg:
		return m.onLogin(msg)
	case tea.PasteMsg:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(tea.PasteMsg{Content: strings.Join(strings.Fields(msg.Content), " ")})
		return cmd
	case tea.KeyPressMsg:
		return m.onKey(msg)
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return cmd
}

// fail prints an error under the line the person entered.
func (m *model) fail(err error) tea.Cmd {
	text := m.theme.style(m.theme.bad).Render("  ⎿ ") + errText(err)
	return m.println(func(int) string { return text })
}

// println puts a block into scrollback and keeps it for redraws.
func (m *model) println(e entry) tea.Cmd {
	if !m.headerDone {
		m.held = append(m.held, e)
		return nil
	}
	m.transcript = append(m.transcript, e)
	if len(m.transcript) > maxTranscript {
		m.transcript = m.transcript[len(m.transcript)-maxTranscript:]
	}
	// One row per print. bubbletea scrolls the screen by the rows it prints
	// above the live region, a line the terminal wraps counting as several,
	// before it moves up to write them; when the region and those rows are
	// taller than the screen, the region's top rows go into the scrollback,
	// where it cannot erase them. View keeps the region a row short of the
	// screen, so one row always fits.
	lines := m.printLines(e)
	cmds := make([]tea.Cmd, len(lines))
	for i, l := range lines {
		cmds[i] = tea.Println(l)
	}
	return tea.Sequence(cmds...)
}

// printLines is e as it goes to the scrollback: in the terminal's colors,
// wrapped here to the content width rather than by the terminal, and with a
// space for a blank line, since bubbletea prints nothing for an empty one.
func (m *model) printLines(e entry) []string {
	w := m.contentWidth()
	lines := strings.Split(ansi.Wrap(m.downsample(e(w)), w, ""), "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = " "
		}
	}
	return lines
}

// downsample fits printed text to the terminal's colors, as the renderer
// does for the live region; tea.Println writes its text as given.
func (m *model) downsample(s string) string {
	if m.profile == colorprofile.TrueColor || m.profile == colorprofile.Unknown {
		return s
	}
	var b strings.Builder
	w := colorprofile.Writer{Forward: &b, Profile: m.profile}
	_, _ = w.WriteString(s) // a strings.Builder does not fail
	return b.String()
}

// redraw clears the screen and its scrollback and prints the session's
// scrollback again at the current width. A terminal that rewraps its lines
// on a resize moves the live region where the renderer cannot follow it, and
// the old region would stay on screen. The clear rides in the same write as
// the reprint, which the renderer makes at once, so no frame lands in between;
// the renderer still holds the frame it drew before the clear and would only
// paint what changed, so ClearScreen makes it paint the whole region again.
func (m *model) redraw() tea.Cmd {
	blocks := make([]string, 0, len(m.transcript))
	for _, e := range m.transcript {
		blocks = append(blocks, strings.Join(m.printLines(e), "\n"))
	}
	const clear = "\x1b[H\x1b[2J\x1b[3J" // home, erase the screen, erase the scrollback
	return tea.Sequence(tea.Println(clear+strings.Join(blocks, "\n")), tea.ClearScreen)
}

// commitHeader moves the banner from the live region into scrollback once
// the server has answered, the terminal has said its background (the banner
// is printed in the matching palette) and the job counts are in, or the wait
// for the last two has run out.
func (m *model) commitHeader() tea.Cmd {
	if m.headerDone || (m.connErr == nil && m.system == nil) {
		return nil
	}
	if m.headerWait && !(m.bgKnown && (m.gotCounts || m.connErr != nil)) {
		return nil
	}
	m.headerDone = true
	cmds := []tea.Cmd{m.println(m.header)}
	for _, e := range m.held {
		cmds = append(cmds, m.println(e))
	}
	m.held = nil
	return tea.Sequence(cmds...)
}

// number gives a job its session number the first time it is seen.
func (m *model) number(id string) int {
	if n, ok := m.num[id]; ok {
		return n
	}
	m.nums++
	m.num[id] = m.nums
	return m.nums
}

func (m *model) onSnapshot(l client.DownloadList) tea.Cmd {
	m.jobs = map[string]*jobState{}
	active := make([]domain.Job, 0, len(l.Downloads))
	for _, j := range l.Downloads {
		if client.Terminal(j.Status) {
			m.printed[j.ID] = true
			continue
		}
		delete(m.printed, j.ID)
		active = append(active, j)
	}
	sort.SliceStable(active, func(i, k int) bool { return active[i].CreatedAt.Before(active[k].CreatedAt) })
	for _, j := range active {
		m.number(j.ID)
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
		n := m.number(j.ID)
		return tea.Batch(m.println(func(int) string { return m.finishedLine(j, d, n) }), m.followMore())
	}
	// A retried job runs again under its ID and finishes again.
	delete(m.printed, j.ID)
	m.number(j.ID)
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

// followMore starts streaming the tracks of running jobs, in card order, up to
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

// activeIDs orders the jobs in progress by their session number, so a card
// keeps its place from the moment it appears until it ends.
func (m *model) activeIDs() []string {
	ids := make([]string, 0, len(m.jobs))
	for id := range m.jobs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, k int) bool { return m.num[ids[i]] < m.num[ids[k]] })
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
	bad := m.theme.style(m.theme.bad).Render("  ⎿ ")
	switch {
	case msg.err != nil && ls.loginID != "" && isAPIError(msg.err, "invalid_request"):
		// A mistyped code: the login still waits for the right one.
		ls.step = "code"
		text := errText(msg.err)
		return m.println(func(int) string { return bad + text + "; enter the code again" })
	case msg.err != nil:
		m.endLogin()
		text := errText(msg.err)
		return m.println(func(int) string { return bad + "sign-in failed: " + text })
	case msg.status == "needs_2fa":
		ls.step, ls.loginID = "code", msg.loginID
		m.input.EchoMode = textinput.EchoNormal
		m.input.SetValue("")
		return nil
	}
	m.endLogin()
	ok := m.theme.style(m.theme.ok).Render("  ⎿ ")
	return tea.Batch(m.println(func(int) string { return ok + "signed in" }), connect(m.ctx, m.c))
}

func (m *model) endLogin() {
	m.loginSt = struct{ step, username, password, loginID string }{}
	m.panel = panelNone
	m.input.EchoMode = textinput.EchoNormal
	m.input.SetValue("")
}
