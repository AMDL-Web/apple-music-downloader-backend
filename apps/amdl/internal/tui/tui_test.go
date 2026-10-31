package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"amdl/internal/client"
	"amdl/internal/domain"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func at(s int) time.Time { return time.Date(2026, 10, 30, 10, 0, s, 0, time.UTC) }

var full = domain.ItemProgress{Resolved: true, Download: 1, Decrypt: 1, Remuxed: true, Verified: true, Tagged: true, Saved: true}

// sample is a running album with tracks in every state, plus a queued
// playlist, as the live region would hold them once the banner is printed.
func sample(t *testing.T) *model {
	t.Helper()
	c, err := client.New("http://127.0.0.1:9")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m := newModel(ctx, cancel, Options{Client: c, Target: "nas (http://nas.local:18570)", Version: "2.0.0"})
	started := at(0)
	job := domain.Job{ID: "job_a", Input: "x", Type: "album", Status: domain.JobRunning, Title: "IGOR", ArtistName: "Tyler, The Creator",
		ArtworkBgColor: "f1a8bf", TotalItems: 12, CompletedItems: 2, SkippedItems: 1, StartedAt: &started, CreatedAt: at(0)}
	items := []domain.JobItem{
		{ID: "i1", Index: 1, Title: "IGOR'S THEME", Status: domain.ItemCompleted, Progress: full, Codec: "alac", BitDepth: 24, SampleRate: 96000, UpdatedAt: at(10)},
		{ID: "i2", Index: 2, Title: "EARFQUAKE", Status: domain.ItemCompleted, Progress: full, UpdatedAt: at(30)},
		{ID: "i3", Index: 3, Title: "I THINK", Status: domain.ItemDownloading, Progress: domain.ItemProgress{Resolved: true, Download: 0.62}},
		{ID: "i4", Index: 4, Title: "EXACTLY WHAT YOU RUN FROM YOU END UP CHASING", Status: domain.ItemSkipped, Progress: domain.ItemProgress{Resolved: true}, UpdatedAt: at(20)},
		{ID: "i5", Index: 5, Title: "RUNNING OUT OF TIME", Status: domain.ItemDecrypting, Progress: domain.ItemProgress{Resolved: true, Download: 1, Decrypt: 0.35}},
		{ID: "i6", Index: 6, Title: "NEW MAGIC WAND", Status: domain.ItemFailed, Error: "decrypt failed", Progress: domain.ItemProgress{Resolved: true, Download: 1, Decrypt: 0.8}},
	}
	for i := 7; i <= 12; i++ {
		items = append(items, domain.JobItem{ID: fmt.Sprintf("i%d", i), Index: i, Title: fmt.Sprintf("TRACK %d", i), Status: domain.ItemQueued})
	}
	d := client.DownloadDetail{Job: job, Items: items}
	st := &jobState{job: job, detail: &d, index: map[string]int{}}
	st.rate.Add(time.Now().Add(-20*time.Second), 0.20)
	st.rate.Add(time.Now(), 0.35)
	m.jobs["job_a"] = st
	m.jobs["job_b"] = &jobState{job: domain.Job{ID: "job_b", Type: "playlist", Status: domain.JobQueued, Title: "City Pop Essentials", CreatedAt: at(5)}}
	m.number("job_a")
	m.number("job_b")
	m.wrapper = &client.WrapperStatus{Ready: true, LoggedIn: true, Regions: []string{"cn"}}
	m.system = &client.SystemInfo{Storage: []client.Storage{{Name: "downloads_dir", Path: "/volume1/music", FreeBytes: 1.2e12, TotalBytes: 4e12}}}
	m.counts = domain.JobStatusCounts{Completed: 128, Failed: 2}
	m.headerDone = true
	m.height = 40
	return m
}

func render(m *model, w int) string {
	m.width = w
	return m.View().Content
}

func withResults(m *model) {
	m.res = &results{term: "igor", items: []pick{
		{hit: client.SearchHit{Type: "album", Name: "IGOR", ArtistName: "Tyler, The Creator", ReleaseDate: "2019-05-17", TrackCount: 12, PriorJob: &client.PriorJob{Status: domain.JobCompleted}}},
		{hit: client.SearchHit{Type: "album", Name: "CALL ME IF YOU GET LOST", ArtistName: "Tyler, The Creator", ReleaseDate: "2021-06-25", TrackCount: 16}, selected: true},
		{hit: client.SearchHit{Type: "song", Name: "EARFQUAKE", ArtistName: "Tyler, The Creator", DurationMS: 190000}},
		{hit: client.SearchHit{Type: "playlist", Name: "Tyler, The Creator Essentials", ArtistName: "Apple Music Hip-Hop"}},
		{hit: client.SearchHit{Type: "artist", Name: "Tyler, The Creator"}},
	}}
	m.panel = panelResults
}

func TestEveryLineFitsTheTerminal(t *testing.T) {
	m := sample(t)
	withResults(m)
	dir := os.Getenv("AMDL_TUI_SNAPSHOTS")
	for _, w := range []int{200, 120, 100, 72, 46, 40, 30} {
		out := render(m, w)
		head := m.header(m.contentWidth())
		for i, line := range strings.Split(head+"\n"+out, "\n") {
			if lw := lipgloss.Width(line); lw > w {
				t.Errorf("width %d: line %d is %d columns: %q", w, i, lw, line)
			}
		}
		if dir != "" {
			full := head + "\n" + m.you("tyler igor") + "\n" + out
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("tui-%d.ansi", w)), []byte(full), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestWideTerminalsKeepTheContentWidth(t *testing.T) {
	m := sample(t)
	for _, line := range strings.Split(render(m, 200), "\n") {
		if lw := lipgloss.Width(line); lw > maxCols {
			t.Errorf("a 200-column terminal draws a %d-column line: %q", lw, stripANSI(line))
		}
	}
}

func TestSmallTerminalsDropDetail(t *testing.T) {
	m := sample(t)
	if out := stripANSI(render(m, 100)); !strings.Contains(out, "Download 62%") || !strings.Contains(out, "Tyler, The Creator") {
		t.Errorf("a wide terminal shows track states and the artist:\n%s", out)
	}
	if out := stripANSI(render(m, 46)); strings.Contains(out, "I THINK") || strings.Contains(out, "Tyler") {
		t.Errorf("a narrow terminal drops track rows and the artist:\n%s", out)
	}
	m.height = 6
	if out := stripANSI(render(m, 100)); strings.Contains(out, "IGOR") {
		t.Errorf("a short terminal keeps only the prompt and status:\n%s", out)
	}
}

func TestBackendAddressShowsOnlyInTheHeader(t *testing.T) {
	m := sample(t)
	for _, w := range []int{46, 72, 100, 140} {
		if s := m.status(w); strings.Contains(s, "nas.local") || strings.Contains(s, "nas") {
			t.Errorf("the footer at %d columns repeats the backend: %q", w, s)
		}
	}
	if s := stripANSI(m.status(100)); !strings.Contains(s, "1 running · 1 queued") {
		t.Errorf("the footer keeps the job counts: %q", s)
	}
	if h := stripANSI(m.header(100)); !strings.Contains(h, "nas · nas.local:18570") || !strings.Contains(h, "/volume1/music") {
		t.Errorf("the header names the backend and where it saves: %q", h)
	}
}

// favicon.svg's bars are 12, 18, 9 and 15 units tall, bottom-aligned; in three
// rows of half-cell steps that is 4, 6, 3 and 5 halves.
func TestLogoIsTheFaviconsBars(t *testing.T) {
	logo := newTheme(true, nil).logo()
	want := [3]string{
		"   ██    ▄▄",
		"██ ██ ▄▄ ██",
		"██ ██ ██ ██",
	}
	for i, row := range logo {
		if got := stripANSI(row); got != want[i] {
			t.Errorf("logo row %d = %q, want %q", i, got, want[i])
		}
	}
}

func TestHeaderShowsBeforeTheServerAnswers(t *testing.T) {
	m := sample(t)
	m.system, m.headerDone, m.gotCounts, m.bgKnown = nil, false, false, false
	if out := stripANSI(render(m, 100)); !strings.Contains(out, "connecting to nas") || !strings.Contains(out, "amdl 2.0.0") {
		t.Fatalf("the banner shows while connecting:\n%s", out)
	}
	m.update(connectedMsg{system: client.SystemInfo{}})
	if m.headerDone {
		t.Fatal("the banner waits for the job counts")
	}
	m.update(snapshotMsg{list: client.DownloadList{StatusCounts: domain.JobStatusCounts{Completed: 3}}})
	if m.headerDone {
		t.Fatal("the banner waits to know the terminal's background")
	}
	if cmd := m.update(tea.BackgroundColorMsg{Color: lipgloss.Color("#fafafa")}); cmd == nil || !m.headerDone {
		t.Fatal("the banner goes to scrollback once the counts and the background are in")
	}
	if m.theme.dark {
		t.Error("a light background picks the light palette")
	}
	if out := stripANSI(render(m, 100)); strings.Contains(out, "amdl 2.0.0") {
		t.Errorf("the printed banner leaves the live region:\n%s", out)
	}
	if h := stripANSI(m.header(100)); !strings.Contains(h, "3 saved") {
		t.Errorf("the banner says what the backend has done: %q", h)
	}
}

func TestWhatIsPrintedBeforeTheBannerFollowsIt(t *testing.T) {
	m := sample(t)
	m.system, m.headerDone, m.gotCounts, m.bgKnown = nil, false, false, false
	typeText(m, "tidal")
	m.onKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(m.transcript) != 0 {
		t.Fatalf("printed before the banner: %d entries", len(m.transcript))
	}
	m.update(connectedMsg{system: client.SystemInfo{}})
	m.update(snapshotMsg{list: client.DownloadList{}})
	m.update(tea.BackgroundColorMsg{Color: lipgloss.Color("#000000")})
	if len(m.transcript) != 2 {
		t.Fatalf("transcript has %d entries, want the banner and the line typed before it", len(m.transcript))
	}
	if first := stripANSI(m.transcript[0](100)); !strings.Contains(first, "amdl 2.0.0") {
		t.Errorf("the banner comes first: %q", first)
	}
	if second := stripANSI(m.transcript[1](100)); !strings.Contains(second, "tidal") {
		t.Errorf("then the line typed while it waited: %q", second)
	}
}

func TestPrintedLinesFitTheTerminal(t *testing.T) {
	m := sample(t)
	m.width = 46
	long := func(int) string {
		return "✓ Glass Weather — Mira Solenne · 12/12 saved · ALAC 24/48 · 864.6 MB · 3:42 · #1\n  2 failed: the wrapper closed the connection · /retry 1"
	}
	lines := m.printLines(long)
	if len(lines) < 4 {
		t.Errorf("%d lines, want the two long ones wrapped", len(lines))
	}
	for _, l := range lines {
		if w := ansi.StringWidth(l); w > 46 {
			t.Errorf("%d columns in a 46-column terminal: %q", w, l)
		}
	}
}

func TestJobsAreNamedBySessionNumber(t *testing.T) {
	m := sample(t)
	for ref, want := range map[string]string{"2": "job_b", "#1": "job_a", "a": "job_a"} {
		if got := m.jobRef(ref); got != want {
			t.Errorf("jobRef(%q) = %q, want %q", ref, got, want)
		}
	}
}

func TestFinishedJobGoesToScrollbackOnce(t *testing.T) {
	m := sample(t)
	fin := at(90)
	j := m.jobs["job_a"].job
	j.Status, j.FinishedAt, j.CompletedItems = domain.JobCompleted, &fin, 11
	if cmd := m.onFeed(domain.DownloadFeedMessage{Type: "download_upserted", Job: &j}); cmd == nil {
		t.Fatal("a finished job should print its line")
	}
	if _, ok := m.jobs["job_a"]; ok {
		t.Error("a finished job leaves the live region")
	}
	if cmd := m.onFeed(domain.DownloadFeedMessage{Type: "download_upserted", Job: &j}); cmd != nil {
		t.Error("a resent message prints nothing")
	}
	line := stripANSI(m.finishedLine(j, nil, 1))
	if !strings.HasPrefix(line, "✓ IGOR — Tyler, The Creator · 12/12 saved · 1 already on disk · 1:30 · #1") {
		t.Errorf("finished line = %q", line)
	}
	if strings.Contains(line, "/retry") {
		t.Errorf("a clean finish offers no retry: %q", line)
	}
	j.Status, j.FailedItems = domain.JobFailed, 1
	if line := stripANSI(m.finishedLine(j, nil, 1)); !strings.Contains(line, "1 failed") || !strings.Contains(line, "/retry 1") {
		t.Errorf("a failed job says how to retry it: %q", line)
	}
}

func TestRetriedJobFinishesAgain(t *testing.T) {
	m := sample(t)
	j := m.jobs["job_a"].job
	j.Status = domain.JobFailed
	m.onFeed(domain.DownloadFeedMessage{Type: "download_upserted", Job: &j})
	// POST /downloads/{id}/retry puts the same job back in the queue.
	j.Status = domain.JobQueued
	m.onFeed(domain.DownloadFeedMessage{Type: "download_upserted", Job: &j})
	if _, ok := m.jobs["job_a"]; !ok {
		t.Fatal("a retried job is live again")
	}
	if m.num["job_a"] != 1 {
		t.Errorf("a retried job keeps its number, got %d", m.num["job_a"])
	}
	j.Status = domain.JobCompleted
	if cmd := m.onFeed(domain.DownloadFeedMessage{Type: "download_upserted", Job: &j}); cmd == nil {
		t.Error("the retry's finish prints its own line")
	}
	if _, ok := m.jobs["job_a"]; ok {
		t.Error("the retried job's card stays after it finishes")
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	esc := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			esc = true
		case esc && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'):
			esc = false
		case !esc:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func TestHeaderNamesTheManagedWrappersState(t *testing.T) {
	m := sample(t)
	m.wrapper = &client.WrapperStatus{Managed: &client.ManagedWrapper{Mode: "stopped"}}
	if h := stripANSI(m.header(100)); !strings.Contains(h, "the local wrapper is stopped") || strings.Contains(h, "no Apple ID") {
		t.Fatalf("header = %q", h)
	}
}

func TestLinksAreNamedBeforeTheirTitleIsKnown(t *testing.T) {
	for in, want := range map[string]string{
		"https://music.apple.com/cn/album/nothing-here/1999999999":       "nothing-here",
		"https://music.apple.com/jp/album/%E5%A4%8F%E8%8D%89/1648881903": "夏草",
		"https://music.apple.com/us/playlist/pl.u-abc":                   "https://music.apple.com/us/playlist/pl.u-abc",
		"https://example.com/cn/album/x/1":                               "https://example.com/cn/album/x/1",
	} {
		if got := linkName(in); got != want {
			t.Errorf("linkName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPrintedTextFollowsTheColorProfile(t *testing.T) {
	m := sample(t)
	m.update(tea.ColorProfileMsg{Profile: colorprofile.ASCII})
	if out := m.downsample(m.header(100)); strings.Contains(out, "38;2;") || strings.Contains(out, "38;5;") {
		t.Errorf("an ASCII terminal gets colors in scrollback: %q", out)
	}
	m.update(tea.ColorProfileMsg{Profile: colorprofile.ANSI256})
	if out := m.downsample(m.header(100)); strings.Contains(out, "38;2;") || !strings.Contains(out, "38;5;") {
		t.Errorf("a 256-color terminal gets 24-bit colors in scrollback: %q", out)
	}
}
