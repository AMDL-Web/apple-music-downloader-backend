package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"amdl/internal/client"
	"amdl/internal/domain"
	"amdl/internal/progress"
)

// A running card shows the same number of rows from its first track to its
// last, so nothing below it moves as tracks start and finish.
func TestCardKeepsItsHeightWhileTracksRun(t *testing.T) {
	m := sample(t)
	delete(m.jobs, "job_b")
	st := m.jobs["job_a"]
	heights := map[int]bool{}
	for done := 0; done <= 12; done++ {
		for i := range st.detail.Items {
			it := &st.detail.Items[i]
			switch {
			case i < done:
				it.Status, it.Progress, it.UpdatedAt = domain.ItemCompleted, full, at(i)
			case i == done:
				it.Status, it.Progress = domain.ItemDownloading, domain.ItemProgress{Resolved: true, Download: 0.5}
			default:
				it.Status, it.Progress = domain.ItemQueued, domain.ItemProgress{}
			}
		}
		heights[strings.Count(m.cards(100, 30), "\n")+1] = true
	}
	if len(heights) != 1 {
		t.Errorf("the card changes height as tracks finish: %v", heights)
	}
}

// Jobs created in the same instant keep the order they were first seen in.
func TestCardsKeepTheirOrder(t *testing.T) {
	m := sample(t)
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("job_%02d", i)
		m.jobs[id] = &jobState{job: domain.Job{ID: id, Status: domain.JobRunning, CreatedAt: at(0)}}
		m.number(id)
	}
	first := strings.Join(m.activeIDs(), ",")
	m.jobs["job_b"].job.Status = domain.JobRunning
	for i := 0; i < 10; i++ {
		if got := strings.Join(m.activeIDs(), ","); got != first {
			t.Fatalf("card order changed:\n%s\n%s", first, got)
		}
	}
}

func TestBarsReadWithoutColor(t *testing.T) {
	th := newTheme(true, nil)
	it := domain.JobItem{Status: domain.ItemDownloading, Progress: domain.ItemProgress{Resolved: true, Download: 0.5}}
	plain := stripANSI(stageBar(th, 60, it, th.accent))
	if !strings.Contains(plain, glyphDone) || !strings.Contains(plain, glyphTodo) {
		t.Errorf("a half-done track shows done and to-do in different glyphs: %q", plain)
	}
}

func TestAlbumBarCells(t *testing.T) {
	th := newTheme(true, nil)
	a := progress.Album{Total: 4, Lit: 1, Runs: []progress.Run{{Status: domain.ItemCompleted, Share: 0.25}}, Done: 0.25, Live: 0.5, Ticks: 4}
	bar := albumBar(th, 23, a, th.accent)
	if lipgloss.Width(bar) != 23 {
		t.Fatalf("width = %d", lipgloss.Width(bar))
	}
	plain := stripANSI(bar)
	if strings.Count(plain, " ") != 3 {
		t.Errorf("4 cells need 3 gaps: %q", plain)
	}
	if !strings.Contains(plain, glyphDone) || !strings.Contains(plain, glyphLive) || !strings.Contains(plain, glyphTodo) {
		t.Errorf("done, running and to-do tracks differ in glyph: %q", plain)
	}
	a.Ticks = 0
	if plain := stripANSI(albumBar(th, 23, a, th.accent)); strings.Contains(plain, " ") {
		t.Errorf("a continuous bar has no gaps: %q", plain)
	}
	a.Ticks = 24
	if plain := stripANSI(albumBar(th, 30, a, th.accent)); strings.Contains(plain, " ") {
		t.Errorf("cells narrower than 2 columns merge into one bar: %q", plain)
	}
}

func TestAlbumLineKeepsItsBarLength(t *testing.T) {
	th := newTheme(true, nil)
	bar := func(lit, pct int, eta string) int {
		a := progress.Album{Total: 14, Lit: lit, Percent: pct}
		line := stripANSI(albumLine(th, 80, a, th.accent, pad(eta, etaWidth)+"  "+pad("", speedWidth)))
		return strings.Count(line, glyphTodo) + strings.Count(line, glyphDone)
	}
	if a, b := bar(9, 5, "~49 s"), bar(10, 100, ""); a != b {
		t.Errorf("the bar is %d cells at 9/14 and %d at 10/14", a, b)
	}
}

func TestStageBarWidth(t *testing.T) {
	th := newTheme(false, nil)
	for _, w := range []int{12, 20, 35, 36, 60} {
		it := domain.JobItem{Status: domain.ItemDecrypting, Progress: domain.ItemProgress{Resolved: true, Download: 1, Decrypt: 0.4}}
		if got := lipgloss.Width(stageBar(th, w, it, th.accent)); got != w {
			t.Errorf("stage bar for %d columns is %d wide", w, got)
		}
	}
}

func TestStageWidthsKeepEveryStage(t *testing.T) {
	for w := 7; w <= 80; w++ {
		sum := 0
		for i, cw := range stageWidths(w) {
			if cw < 1 {
				t.Errorf("width %d: stage %d is %d cells", w, i, cw)
			}
			sum += cw
		}
		if sum != w {
			t.Errorf("width %d: stages add up to %d", w, sum)
		}
	}
}

func TestAccentKeepsContrast(t *testing.T) {
	for _, dark := range []bool{true, false} {
		th := newTheme(dark, nil)
		for _, bg := range []string{"000000", "ffffff", "f1a8bf", "1a1a1a", "", "zzz"} {
			if c := contrast(th.albumAccent(bg), th.bg); c < 3 {
				t.Errorf("dark=%v accent for %q has contrast %.2f", dark, bg, c)
			}
		}
	}
}

func TestTrackCountIsSingularForOne(t *testing.T) {
	for n, want := range map[int]string{1: "1 track", 2: "2 tracks", 14: "14 tracks"} {
		if got := trackCount(n); got != want {
			t.Errorf("trackCount(%d) = %q, want %q", n, got, want)
		}
	}
}

// A track working through saving and tagging must not reset the time left:
// the estimate reads the job's weighted progress, which only moves forward.
func TestTimeLeftSurvivesTracksFinishing(t *testing.T) {
	m := sample(t)
	now := at(0)
	m.now = func() time.Time { return now }
	job := domain.Job{ID: "j", Status: domain.JobRunning, TotalItems: 2, StartedAt: &now}
	m.jobs["j"] = &jobState{job: job}
	got := domain.ItemProgress{Resolved: true, Download: 1, Decrypt: 1}
	remuxed := got
	remuxed.Remuxed = true
	verified := remuxed
	verified.Verified = true
	tagged := verified
	tagged.Tagged = true
	m.update(detailMsg{client.DownloadDetail{Job: job, Items: []domain.JobItem{
		{ID: "a", Index: 1, Status: domain.ItemRemuxing, Progress: got},
		{ID: "b", Index: 2, Status: domain.ItemDownloading, Progress: domain.ItemProgress{Resolved: true}},
	}}})
	finish := []domain.JobItem{
		{ID: "a", Index: 1, Status: domain.ItemSaving, Progress: remuxed},
		{ID: "a", Index: 1, Status: domain.ItemSaving, Progress: verified},
		{ID: "a", Index: 1, Status: domain.ItemTagging, Progress: verified},
		{ID: "a", Index: 1, Status: domain.ItemTagging, Progress: tagged},
		{ID: "a", Index: 1, Status: domain.ItemCompleted, Progress: tagged},
	}
	id := int64(1)
	send := func(it domain.JobItem) {
		id++
		payload, err := json.Marshal(it)
		if err != nil {
			t.Fatal(err)
		}
		m.update(eventMsg{domain.Event{ID: id, JobID: "j", ItemID: it.ID, Type: domain.EventItemProgress, Payload: payload}})
	}
	for step := 1; step <= 12; step++ {
		now = at(0).Add(time.Duration(step) * 2 * time.Second)
		send(domain.JobItem{ID: "b", Index: 2, Status: domain.ItemDownloading,
			Progress: domain.ItemProgress{Resolved: true, Download: float64(step) / 14}})
		if step <= len(finish) {
			send(finish[step-1])
		}
		if r := m.jobs["j"].rate.Remaining(); step >= 3 && r <= 0 {
			t.Fatalf("step %d: no time left while progress rises", step)
		}
	}
}

// The card's album line carries the average speed while the job downloads,
// from the bytes of tracks still downloading as well as finished ones, where
// the terminal has room for it.
func TestCardShowsTheAverageSpeed(t *testing.T) {
	m := sample(t)
	delete(m.jobs, "job_b")
	m.now = func() time.Time { return at(10) }
	items := m.jobs["job_a"].detail.Items
	items[2].Progress.DownloadBytes = 30e6 // I THINK, downloading
	for _, w := range []int{100, 72} {
		if out := stripANSI(render(m, w)); !strings.Contains(out, "avg ") || !strings.Contains(out, "MB/s") {
			t.Errorf("%d columns: no average speed:\n%s", w, out)
		}
	}
	if out := stripANSI(render(m, 60)); strings.Contains(out, "avg ") {
		t.Errorf("60 columns keep the speed out:\n%s", out)
	}
}
