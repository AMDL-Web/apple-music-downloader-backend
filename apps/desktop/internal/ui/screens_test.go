package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/mode"
	"amdl/apps/desktop/internal/store"
	"amdl/internal/client"
	"amdl/internal/domain"
)

func remoteSettings() mode.Settings {
	s := mode.DefaultSettings()
	s.Mode, s.RemoteURL = mode.Remote, "http://nas:18570"
	return s
}

func TestHomeSaysWhatWillStart(t *testing.T) {
	a, _ := newTestApp(remoteSettings())
	a.Screen = ScreenHome
	s := a.Store
	s.SetOmniText("https://music.apple.com/cn/album/x/1858184006 https://music.apple.com/cn/music-video/y/1")
	if len(s.Omni.Links) != 2 {
		t.Fatalf("links = %d, want 2", len(s.Omni.Links))
	}
	album, video := s.Omni.Links[0], s.Omni.Links[1]
	album.State = store.LinkReady
	album.Lookup = client.Lookup{Type: "album", ID: "1858184006", Title: "Test Album", ArtistName: "Some Artist", TrackCount: 5, Storefront: "cn"}
	video.State = store.LinkUnsupported
	tt := ui.NewTester(a.View, 1280, 800)
	for _, want := range []string{"Test Album", "Ready", "Not supported", "1 album · 5 tracks", "1 not supported", "Download 1 album"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
}

func TestQueueShowsTheLatestDownloadUntilOneRuns(t *testing.T) {
	a, _ := newTestApp(remoteSettings())
	a.Screen = ScreenQueue
	s := a.Store
	now := time.Now()
	job := func(id, title string, status domain.JobStatus, created time.Time, finished *time.Time) *store.Job {
		return &store.Job{Job: domain.Job{ID: id, Type: "album", Status: status, Title: title, ArtistName: "Artist " + id, TotalItems: 5, CreatedAt: created, FinishedAt: finished}}
	}
	earlier, later := now.Add(-time.Hour), now.Add(-time.Minute)
	// The feed lists jobs by creation: a, added first, finished last, as a
	// retried download does.
	s.Loaded = true
	s.Counts = domain.JobStatusCounts{Completed: 12, Total: 12}
	s.Jobs = []*store.Job{
		job("b", "Older Album", domain.JobCompleted, now.Add(-2*time.Hour), &earlier),
		job("a", "Newest Album", domain.JobCompleted, now.Add(-3*time.Hour), &later),
	}
	// The album downloaded once before: the hero's cover is not repeated.
	again := job("a0", "Newest Album", domain.JobCompleted, now.Add(-5*time.Hour), &earlier)
	again.ArtistName = "Artist a"
	s.Jobs = append(s.Jobs, again)
	tt := ui.NewTester(a.View, 1280, 800)
	for _, want := range []string{"LATEST DOWNLOAD · FINISHED " + strings.ToUpper(dayClock(later, now)), "Newest Album", "Older Album", "All 12 in Done →"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	sortY := func() float32 {
		r, ok := tt.Find("Newest first")
		if !ok {
			t.Fatalf("no sort control in %q", tt.Texts())
		}
		return r.Y
	}
	before := sortY()
	if err := tt.Click("All 12 in Done →"); err != nil {
		t.Fatal(err)
	}
	if store.Tab(a.Tab) != store.TabDone {
		t.Errorf("the wall's link shows tab %d, want Done", a.Tab)
	}
	tt.Frame()
	if after := sortY(); after != before {
		t.Errorf("the list moved from %v to %v as the wall's link hid", before, after)
	}
	a.Tab = int(store.TabActive)
	tt.Frame()
	// Counted as whole texts: the hero cover's label names the album too.
	if got := strings.Count("\n"+strings.Join(tt.Texts(), "\n")+"\n", "\nNewest Album\n"); got != 1 {
		t.Errorf("Newest Album shows %d times, want once, in the hero: %q", got, tt.Texts())
	}

	// A download under way takes the hero's place; the covers stay. The
	// Active tab lists neither finished album, so only the covers name them.
	s.Jobs = append([]*store.Job{job("c", "Running Album", domain.JobRunning, now, nil)}, s.Jobs...)
	tt.Frame()
	if !tt.HasText("DOWNLOADING NOW") || tt.HasText("LATEST DOWNLOAD · FINISHED "+strings.ToUpper(dayClock(later, now))) {
		t.Errorf("with a download running: %q", tt.Texts())
	}
	if !tt.HasText("Newest Album") || !tt.HasText("Older Album") {
		t.Errorf("the covers left with a download running: %q", tt.Texts())
	}
}

func TestQueueRowOpensOnAClickAndKeepsItsMenuForTheRightButton(t *testing.T) {
	// Seeded through the store's snapshot, as the feed does: the detail
	// screen looks the job up by id and returns to the queue without it.
	// The finished album takes the latest-download hero, so the failed
	// job's row is the only place its label appears.
	setup := func() (*App, *ui.Tester, ui.Rect) {
		a, _ := newTestApp(remoteSettings())
		a.Screen = ScreenQueue
		now := time.Now()
		done := now.Add(-time.Minute)
		a.Store.ApplySnapshot(client.DownloadList{
			StatusCounts: domain.JobStatusCounts{Completed: 1, Failed: 1, Total: 2},
			Downloads: []domain.Job{
				{ID: "h", Type: "album", Status: domain.JobCompleted, Title: "Hero Album", ArtistName: "Artist h", TotalItems: 5, CreatedAt: now.Add(-time.Hour), FinishedAt: &done},
				{ID: "f", Type: "album", Status: domain.JobFailed, Title: "Broken Album", ArtistName: "Artist f", TotalItems: 5, CreatedAt: now},
			},
		})
		a.Tab = int(store.TabFailed)
		tt := ui.NewTester(a.View, 1280, 800)
		r, ok := tt.Find("Artist f · Album")
		if !ok {
			t.Fatalf("no row in %q", tt.Texts())
		}
		return a, tt, r
	}

	a, tt, r := setup()
	tt.RightClickAt(r.X+r.W/2, r.Y+r.H/2)
	if menu := tt.Menu(); len(menu) == 0 || menu[0] != "Show details" {
		t.Errorf("a right click shows the menu %q, want the job's", menu)
	}
	if a.Screen != ScreenQueue {
		t.Errorf("a right click left the queue for screen %v", a.Screen)
	}

	a, tt, r = setup()
	tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	if menu := tt.Menu(); menu != nil {
		t.Errorf("a click shows the menu %q", menu)
	}
	if a.Screen != ScreenDetail || a.Store.DetailID != "f" {
		t.Errorf("a click shows screen %v with job %q, want the job's detail", a.Screen, a.Store.DetailID)
	}
	if !tt.HasText("Broken Album") {
		t.Errorf("the detail shows %q", tt.Texts())
	}
}

func TestQueueOpensOnAllAndTheHeroCoverOpensTheJob(t *testing.T) {
	for _, status := range []domain.JobStatus{domain.JobRunning, domain.JobCompleted} {
		a, _ := newTestApp(remoteSettings())
		a.Screen = ScreenQueue
		now := time.Now()
		done := now.Add(-time.Minute)
		job := domain.Job{ID: "h", Type: "album", Status: status, Title: "Hero Album", ArtistName: "Artist h", TotalItems: 5, CreatedAt: now.Add(-time.Hour), StartedAt: &done}
		if status == domain.JobCompleted {
			job.FinishedAt = &done
		}
		a.Store.ApplySnapshot(client.DownloadList{
			StatusCounts: domain.JobStatusCounts{Running: 1, Completed: 1, Failed: 1, Total: 3},
			Downloads: []domain.Job{job,
				{ID: "f", Type: "album", Status: domain.JobFailed, Title: "Broken Album", ArtistName: "Artist f", TotalItems: 5, CreatedAt: now.Add(-2 * time.Hour)}},
		})
		tt := ui.NewTester(a.View, 1280, 800)
		all, ok := tt.Find("All")
		active, ok2 := tt.Find("Active · 1")
		if !ok || !ok2 || all.X >= active.X {
			t.Errorf("%s: the All tab at %v, Active at %v: want All first", status, all, active)
		}
		if !tt.HasText("Artist f · Album") {
			t.Errorf("%s: the queue opens without the failed job, as if not on All: %q", status, tt.Texts())
		}
		if err := tt.Click("Open Hero Album"); err != nil {
			t.Fatal(err)
		}
		if a.Screen != ScreenDetail || a.Store.DetailID != "h" {
			t.Errorf("%s: the hero's cover shows screen %v with job %q, want the job's detail", status, a.Screen, a.Store.DetailID)
		}
	}
}

func TestStatusBoardTilesOpenTheirSections(t *testing.T) {
	a, _ := newTestApp(remoteSettings())
	a.GoSettings("")
	sv := &a.Store.Server
	sv.Read = time.Now()
	sv.Health = &client.Health{Status: "ok"}
	sv.System = &client.SystemInfo{Version: "2.0-dev", Commit: "01339f0aa", UptimeSeconds: 19 * 60, DefaultStorefront: "us"}
	sv.System.Jobs.Workers = 3
	sv.System.Tools = []domain.Capability{{Name: "ffmpeg", Available: true, Path: "/usr/bin/ffmpeg"}}
	sv.System.Storage = []client.Storage{{Name: "downloads_dir", Path: "data/downloads", FreeBytes: 1_620_000_000_000, TotalBytes: 7_670_000_000_000}}
	sv.Wrapper = &client.WrapperStatus{Ready: true, LoggedIn: true, Accounts: []string{"someone@example.com"}, Regions: []string{"cn", "us"}}
	sv.Errors = &store.ErrorLogs{Times: []time.Time{time.Now().Add(-2 * time.Hour)}, Last: "wrapper: connection refused"}
	tt := ui.NewTester(a.View, 1280, 800)
	for _, want := range []string{"All good", "01339f0", "of 3 busy", "Storage & files", "1.62", "TB free", "Signed in", "someone@example.com", "Last: wrapper: connection refused"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
	if tt.HasText("System") {
		t.Errorf("the System screen is Settings' status board now: %q", tt.Texts())
	}
	if err := tt.Click("Account"); err != nil {
		t.Fatal(err)
	}
	if a.set.page != "account" || !tt.HasText("Default storefront") || !tt.HasText("US") {
		t.Errorf("the Account tile opened %q: %q", a.set.page, tt.Texts())
	}
}

func TestPanelWhenNothingRuns(t *testing.T) {
	a, _ := newTestApp(remoteSettings())
	tt := ui.NewTester(a.Panel, PanelWidth, 460)
	for _, want := range []string{"All done", "Today", "Server disk", "Open AMDL"} {
		if !tt.HasText(want) {
			t.Errorf("no %q in %q", want, tt.Texts())
		}
	}
}

func TestSearchHitRowsSayWhatTheirTypeHas(t *testing.T) {
	if got := qualityHints([]string{"atmos", "lossless", "hi-res-lossless", "lossy-stereo"}); strings.Join(got, ",") != "Hi-Res Lossless,Dolby Atmos" {
		t.Errorf("hints %v", got)
	}
	if got := qualityHints([]string{"lossy-stereo"}); len(got) != 0 {
		t.Errorf("lossy hints %v", got)
	}
	if got := shortHints([]string{"atmos", "hi-res-lossless"}, 200); got != "Hi-Res · Atmos" {
		t.Errorf("a wide tile's chip %q", got)
	}
	if got := shortHints([]string{"atmos", "hi-res-lossless"}, 160); got != "Hi-Res" {
		t.Errorf("a narrow tile's chip %q", got)
	}
	song := client.SearchHit{Type: "song", ArtistName: "Yorushika", AlbumName: "Elma", DurationMS: 245_000}
	if got := strings.Join(hitFacts(song), " · "); got != "Yorushika · Elma · 4:05" {
		t.Errorf("song facts %q", got)
	}
	cases := []struct {
		hit  client.SearchHit
		want string
	}{
		{client.SearchHit{Type: "album", ArtistName: "Yorushika", ReleaseDate: "2020-07-29", TrackCount: 1}, "Yorushika · 2020"},
		{client.SearchHit{Type: "artist", Genre: "J-Pop"}, "J-Pop"},
		{client.SearchHit{Type: "artist"}, ""},
	}
	for _, c := range cases {
		if got := strings.Join(tileFacts(c.hit), " · "); got != c.want {
			t.Errorf("%s tile facts %q, want %q", c.hit.Type, got, c.want)
		}
	}
}
