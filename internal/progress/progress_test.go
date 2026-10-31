package progress

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"amdl/internal/domain"
)

// The cases below are the console's album-progress.test.mjs, so the Go and
// JavaScript bars agree on the same inputs.

func at(s int) time.Time { return time.Date(2026, 10, 30, 10, 0, s, 0, time.UTC) }

func item(index int, status domain.ItemStatus, p domain.ItemProgress) domain.JobItem {
	return domain.JobItem{Index: index, Status: status, Progress: p, UpdatedAt: at(0)}
}

func near(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestWeightsMatchConsole(t *testing.T) {
	raw, err := os.ReadFile("../../apps/console/static/js/lib/status.js")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`STAGE_WEIGHTS = Object\.freeze\(\{([^}]*)\}\)`).FindSubmatch(raw)
	if m == nil {
		t.Fatal("STAGE_WEIGHTS not found in status.js")
	}
	keys := map[string]Stage{"resolve": StageResolve, "download": StageDownload, "decrypt": StageDecrypt,
		"remux": StageRemux, "verify": StageVerify, "tag": StageTag, "save": StageSave}
	seen := 0
	for _, kv := range regexp.MustCompile(`(\w+):\s*([0-9.]+)`).FindAllSubmatch(m[1], -1) {
		s, ok := keys[string(kv[1])]
		if !ok {
			t.Errorf("console stage %q has no Go stage", kv[1])
			continue
		}
		v, _ := strconv.ParseFloat(string(kv[2]), 64)
		if v != Weights[s] {
			t.Errorf("weight of %s: console %v, Go %v", kv[1], v, Weights[s])
		}
		seen++
	}
	if seen != int(StageCount) {
		t.Errorf("console lists %d stages, Go has %d", seen, StageCount)
	}

	raw, err = os.ReadFile("../../apps/console/static/js/lib/album-progress.js")
	if err != nil {
		t.Fatal(err)
	}
	tm := regexp.MustCompile(`TICK_MAX = (\d+);`).FindSubmatch(raw)
	if tm == nil || string(tm[1]) != strconv.Itoa(TickMax) {
		t.Errorf("console TICK_MAX %q, Go %d", tm, TickMax)
	}
}

// The stage bar draws the four stages after decrypt at one width.
func TestWeightsSumToOneAndTheShortStagesMatch(t *testing.T) {
	sum := 0.0
	for _, w := range Weights {
		sum += w
	}
	near(t, sum, 1)
	for s := StageVerify; s < StageCount; s++ {
		if Weights[s] != Weights[StageRemux] {
			t.Errorf("weight of stage %d = %v, want %v like remux", s, Weights[s], Weights[StageRemux])
		}
	}
	if Weights[StageDecrypt] <= Weights[StageRemux] {
		t.Errorf("decrypt weight %v is not wider than the stages after it (%v)", Weights[StageDecrypt], Weights[StageRemux])
	}
}

func TestItemFraction(t *testing.T) {
	if ItemFraction(item(1, domain.ItemCompleted, domain.ItemProgress{})) != 1 ||
		ItemFraction(item(1, domain.ItemSkipped, domain.ItemProgress{})) != 1 ||
		ItemFraction(item(1, domain.ItemQueued, domain.ItemProgress{})) != 0 {
		t.Error("finished tracks count whole, a queued one as nothing")
	}
	near(t, ItemFraction(item(1, domain.ItemDownloading, domain.ItemProgress{Resolved: true, Download: 0.5})), Weights[StageResolve]+Weights[StageDownload]*0.5)
	near(t, ItemFraction(item(1, domain.ItemTagging, domain.ItemProgress{Resolved: true, Download: 1, Decrypt: 1, Remuxed: true})), Weights[StageResolve]+Weights[StageDownload]+Weights[StageDecrypt]+Weights[StageRemux])
	// Saved ends the pipeline even with integrity checks off.
	near(t, ItemFraction(item(1, domain.ItemSaving, domain.ItemProgress{Resolved: true, Download: 1, Decrypt: 1, Remuxed: true, Tagged: true, Saved: true})), 1)
	near(t, ItemFraction(item(1, domain.ItemFailed, domain.ItemProgress{Resolved: true, Download: 0.25})), Weights[StageResolve]+Weights[StageDownload]*0.25)
}

func TestAlbumPercent(t *testing.T) {
	half := domain.ItemProgress{Resolved: true, Download: 1}
	p := AlbumProgress(domain.JobRunning, []domain.JobItem{item(1, domain.ItemCompleted, domain.ItemProgress{}), item(2, domain.ItemDownloading, half)})
	if want := int(math.Round((1 + Weights[StageResolve] + Weights[StageDownload]) / 2 * 100)); p.Percent != want {
		t.Errorf("percent = %d, want %d", p.Percent, want)
	}
	full := domain.ItemProgress{Resolved: true, Download: 1, Decrypt: 1, Remuxed: true, Verified: true, Tagged: true}
	many := make([]domain.JobItem, 200)
	for i := range many {
		st := domain.ItemCompleted
		if i == 0 {
			st = domain.ItemTagging
		}
		many[i] = item(i+1, st, full)
	}
	if got := AlbumProgress(domain.JobRunning, many).Percent; got != 99 {
		t.Errorf("one track left of 200: percent = %d, want 99", got)
	}
	done := AlbumProgress(domain.JobCompleted, []domain.JobItem{item(1, domain.ItemCompleted, domain.ItemProgress{}), item(2, domain.ItemSkipped, domain.ItemProgress{})})
	if done.Percent != 100 {
		t.Errorf("completed job percent = %d", done.Percent)
	}
	if AlbumProgress(domain.JobRunning, nil).Percent != 0 {
		t.Error("no items should be 0")
	}
}

func TestAlbumFinishOrder(t *testing.T) {
	items := []domain.JobItem{
		item(1, domain.ItemDownloading, domain.ItemProgress{}),
		{Index: 2, Status: domain.ItemCompleted, UpdatedAt: at(30)},
		{Index: 3, Status: domain.ItemFailed, UpdatedAt: at(10)},
		{Index: 4, Status: domain.ItemCompleted, UpdatedAt: at(20)},
		{Index: 5, Status: domain.ItemSkipped, UpdatedAt: at(40)},
	}
	p := AlbumProgress(domain.JobRunning, items)
	if p.Lit != 4 {
		t.Errorf("lit = %d", p.Lit)
	}
	want := []struct {
		st domain.ItemStatus
		n  int
	}{{domain.ItemFailed, 1}, {domain.ItemCompleted, 2}, {domain.ItemSkipped, 1}}
	if len(p.Runs) != len(want) {
		t.Fatalf("runs = %+v", p.Runs)
	}
	for i, w := range want {
		if p.Runs[i].Status != w.st || int(math.Round(p.Runs[i].Share*5)) != w.n {
			t.Errorf("run %d = %+v, want %v×%d", i, p.Runs[i], w.st, w.n)
		}
	}
	near(t, p.Done, 0.8)
	if p.Live < p.Done {
		t.Error("live behind done")
	}

	var noTimes []domain.JobItem
	for i, st := range []domain.ItemStatus{domain.ItemCancelled, domain.ItemFailed, domain.ItemSkipped, domain.ItemCompleted, domain.ItemCompleted} {
		noTimes = append(noTimes, domain.JobItem{Index: i + 1, Status: st})
	}
	var got []domain.ItemStatus
	for _, r := range AlbumProgress(domain.JobCancelled, noTimes).Runs {
		got = append(got, r.Status)
	}
	wantOrder := []domain.ItemStatus{domain.ItemCompleted, domain.ItemSkipped, domain.ItemFailed, domain.ItemCancelled}
	if len(got) != 4 {
		t.Fatalf("runs without times = %v", got)
	}
	for i := range wantOrder {
		if got[i] != wantOrder[i] {
			t.Errorf("runs without times = %v, want %v", got, wantOrder)
			break
		}
	}
}

func TestAlbumLiveNeverBehindDone(t *testing.T) {
	p := AlbumProgress(domain.JobRunning, []domain.JobItem{item(1, domain.ItemFailed, domain.ItemProgress{}), item(2, domain.ItemQueued, domain.ItemProgress{})})
	near(t, p.Done, 0.5)
	near(t, p.Live, 0.5)
}

func TestAlbumTicks(t *testing.T) {
	n := func(k int) []domain.JobItem {
		out := make([]domain.JobItem, k)
		for i := range out {
			out[i] = item(i+1, domain.ItemQueued, domain.ItemProgress{})
		}
		return out
	}
	for k, want := range map[int]int{1: 0, 2: 2, 24: 24, 25: 0} {
		if got := AlbumProgress(domain.JobRunning, n(k)).Ticks; got != want {
			t.Errorf("%d tracks: ticks = %d, want %d", k, got, want)
		}
	}
}

func TestAlbumSingleTrack(t *testing.T) {
	p := AlbumProgress(domain.JobRunning, []domain.JobItem{item(1, domain.ItemDownloading, domain.ItemProgress{Resolved: true, Download: 0.5})})
	if len(p.Runs) != 1 {
		t.Fatalf("runs = %+v", p.Runs)
	}
	near(t, p.Done, Weights[StageResolve]+Weights[StageDownload]*0.5)
	near(t, p.Live, p.Done)
	f := AlbumProgress(domain.JobFailed, []domain.JobItem{item(1, domain.ItemFailed, domain.ItemProgress{})})
	if len(f.Runs) != 1 || f.Runs[0].Status != domain.ItemFailed || f.Runs[0].Share != 1 {
		t.Errorf("failed single track runs = %+v", f.Runs)
	}
}

func TestItemStagesCountsPassedStages(t *testing.T) {
	// Verified stays false with integrity checks off; tagging means remux and
	// verify are behind the track.
	s := ItemStages(domain.JobItem{Status: domain.ItemTagging, Progress: domain.ItemProgress{Resolved: true, Download: 1, Decrypt: 1, Remuxed: true}})
	if s.Current() != StageTag {
		t.Errorf("current = %v, want tag", s.Current())
	}
	near(t, s.Weighted(), Weights[StageResolve]+Weights[StageDownload]+Weights[StageDecrypt]+Weights[StageRemux]+Weights[StageVerify])
}

// The pipeline publishes saving (verify) before tagging
// (internal/media#Downloader.finalizeTrackOutput), so a track's stages must
// only move forward along that order: a step back reads as a retry to Rate
// and throws away the time-left estimate at every finished track.
func TestItemStagesNeverStepBackThroughTheFinishPhase(t *testing.T) {
	done := domain.ItemProgress{Resolved: true, Download: 1, Decrypt: 1}
	remuxed := done
	remuxed.Remuxed = true
	verified := remuxed
	verified.Verified = true
	tagged := verified
	tagged.Tagged = true
	for _, tc := range []struct {
		name string
		seq  []domain.JobItem
	}{
		{"checked", []domain.JobItem{
			item(1, domain.ItemRemuxing, done), item(1, domain.ItemRemuxing, remuxed),
			item(1, domain.ItemSaving, remuxed), item(1, domain.ItemSaving, verified),
			item(1, domain.ItemTagging, verified), item(1, domain.ItemTagging, tagged),
			item(1, domain.ItemCompleted, tagged),
		}},
		{"unchecked", []domain.JobItem{
			item(1, domain.ItemRemuxing, remuxed), item(1, domain.ItemSaving, remuxed),
			item(1, domain.ItemTagging, remuxed), item(1, domain.ItemTagging, remuxed),
			item(1, domain.ItemCompleted, remuxed),
		}},
	} {
		prev := ItemStages(tc.seq[0])
		for i, it := range tc.seq[1:] {
			cur := ItemStages(it)
			for st := range cur {
				if cur[st] < prev[st] {
					t.Errorf("%s step %d (%s): stage %d went from %v back to %v", tc.name, i+1, it.Status, st, prev[st], cur[st])
				}
			}
			if cur.Weighted() < prev.Weighted() {
				t.Errorf("%s step %d (%s): weighted %v fell from %v", tc.name, i+1, it.Status, cur.Weighted(), prev.Weighted())
			}
			prev = cur
		}
	}
}

func TestRateRemaining(t *testing.T) {
	var r Rate
	t0 := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	r.Add(t0, 0.2)
	r.Add(t0.Add(10*time.Second), 0.4)
	if got := r.Remaining(); got != 30*time.Second {
		t.Errorf("remaining = %v, want 30s", got)
	}
	r.Add(t0.Add(11*time.Second), 0.1) // a retry went back
	if r.Remaining() != 0 {
		t.Error("a retry should restart the estimate")
	}
}

// The average counts the bytes of tracks still downloading, so it has a value
// before the first track finishes, and a finished track by its file size.
func TestAvgSpeedCountsTracksStillDownloading(t *testing.T) {
	started := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	j := domain.Job{Status: domain.JobRunning, StartedAt: &started}
	items := []domain.JobItem{
		{Status: domain.ItemDownloading, Progress: domain.ItemProgress{Download: 0.5, DownloadBytes: 30 << 20}},
		{Status: domain.ItemDecrypting, Progress: domain.ItemProgress{Download: 1, DownloadBytes: 20 << 20}},
	}
	if got, want := AvgSpeed(j, items, started.Add(10*time.Second)), float64(5<<20); got != want {
		t.Fatalf("nothing finished: %v, want %v", got, want)
	}
	items[1] = domain.JobItem{Status: domain.ItemCompleted, FileSize: 21 << 20, Progress: domain.ItemProgress{Download: 1, DownloadBytes: 20 << 20}}
	if got, want := AvgSpeed(j, items, started.Add(10*time.Second)), float64(51<<20)/10; got != want {
		t.Fatalf("one finished: %v, want %v", got, want)
	}
	if got := AvgSpeed(domain.Job{Status: domain.JobQueued}, items, started); got != 0 {
		t.Errorf("a job that has not started: %v, want 0", got)
	}
}
