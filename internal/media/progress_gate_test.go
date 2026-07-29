package media

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/domain"
)

// fakeClock drives progressGate deterministically.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

func newTestGate(base *recordingReporter, interval time.Duration) (*progressGate, *fakeClock) {
	clock := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	gate := newProgressGate(base, interval)
	gate.now = clock.now
	return gate, clock
}

func TestProgressGateAllowsFirstUpdateThenEnforcesInterval(t *testing.T) {
	gate, clock := newTestGate(&recordingReporter{}, 500*time.Millisecond)

	if !gate.allow() {
		t.Fatal("the first meter update must never be withheld")
	}
	gate.emitted()
	if gate.allow() {
		t.Fatal("a meter update inside the interval must be withheld")
	}
	clock.advance(499 * time.Millisecond)
	if gate.allow() {
		t.Fatal("499ms into a 500ms interval must still be withheld")
	}
	clock.advance(time.Millisecond)
	if !gate.allow() {
		t.Fatal("the interval elapsed; the meter update must be allowed")
	}
}

func TestProgressGateWithoutIntervalNeverWithholds(t *testing.T) {
	gate, _ := newTestGate(&recordingReporter{}, 0)
	for i := range 5 {
		if !gate.allow() {
			t.Fatalf("update %d withheld with coalescing disabled", i)
		}
		gate.emitted()
	}
}

// A held meter value must reach the wire immediately BEFORE the event that
// supersedes it — never after, which would rewind the item on every client.
func TestProgressGateFlushesHeldUpdateAheadOfTerminalEvent(t *testing.T) {
	base := &recordingReporter{}
	gate, _ := newTestGate(base, time.Hour) // nothing may pass on the time gate
	gate.emit = func() {
		gate.emitted()
		_ = base.Event(context.Background(), domain.Event{
			ItemID: "item-1", Type: eventItemProgress, Phase: "downloading", Message: "held 100%",
		})
	}

	gate.emitted() // an earlier publish starts the interval
	gate.hold()    // a later meter move is withheld

	if err := gate.Event(context.Background(), domain.Event{
		ItemID: "item-1", Type: "item_completed", Message: "done",
	}); err != nil {
		t.Fatal(err)
	}

	if len(base.events) != 2 {
		t.Fatalf("events = %d, want the flushed progress plus the terminal event", len(base.events))
	}
	if base.events[0].Type != eventItemProgress || base.events[0].Message != "held 100%" {
		t.Fatalf("first event = %+v, want the flushed meter value", base.events[0])
	}
	if base.events[1].Type != "item_completed" {
		t.Fatalf("second event = %+v, want item_completed", base.events[1])
	}
	if gate.held {
		t.Fatal("the held update was not cleared, so it can be published a second time")
	}
}

// The gate must not insert a spurious progress event when nothing is held, and
// must never withhold a non-progress event.
func TestProgressGatePassesNonProgressEventsStraightThrough(t *testing.T) {
	base := &recordingReporter{}
	gate, _ := newTestGate(base, time.Hour)
	gate.emit = func() { t.Fatal("nothing was held; emit must not be called") }

	for _, ty := range []string{"codec_selected", "item_failed", "item_skipped", "operation_retry"} {
		if err := gate.Event(context.Background(), domain.Event{ItemID: "item-1", Type: ty}); err != nil {
			t.Fatal(err)
		}
	}
	if len(base.events) != 4 {
		t.Fatalf("events = %d, want all 4 delivered untouched", len(base.events))
	}
}

// simulateTrackEvents runs one simulated track end to end at the given
// coalescing interval and returns the recorded event stream. durationMS sets
// the track length, which (with the 2.5 Mbps manifest below) decides the
// simulated byte count and therefore how long the meters sweep for.
func simulateTrackEvents(t *testing.T, intervalMS, durationMS, minKBps, maxKBps int) []domain.Event {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/master.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n" +
				"#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"audio-alac-stereo-44100-24\",NAME=\"Lossless\",BIT-DEPTH=24,SAMPLE-RATE=44100\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=2500000,AVERAGE-BANDWIDTH=2500000,AUDIO=\"audio-alac-stereo-44100-24\",CODECS=\"alac\"\n" +
				"media.m3u8\n"))
		case "/media.m3u8":
			_, _ = w.Write([]byte("#EXTM3U\n" +
				"#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://itunes.apple.com/P000000000/s1/e1/c23\"\n" +
				"#EXT-X-MAP:URI=\"encrypted.mp4\"\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	cfg := config.Default()
	cfg.Download.DownloadsDir = t.TempDir()
	cfg.Download.QualityPriority = []string{"alac"}
	cfg.Download.CodecAlternative = false
	cfg.Download.ProgressEventIntervalMS = intervalMS
	cfg.Simulate = config.SimulateConfig{Enabled: true, MinSpeedKBps: minKBps, MaxSpeedKBps: maxKBps}

	song := applemusic.Song{
		ID: "song-1", Name: "Track", ArtistName: "Artist", AlbumName: "Album",
		DurationInMillis: durationMS, EnhancedHLS: server.URL + "/master.m3u8",
	}
	downloader := (&Downloader{
		cfg:     cfg,
		catalog: fakeDownloaderCatalog{song: song},
		http:    server.Client(),
	}).withConfig(cfg)

	reporter := &recordingReporter{}
	if err := downloader.processTrack(context.Background(), domain.Job{ID: "job-1"},
		domain.JobItem{ID: "item-1", JobID: "job-1"}, applemusic.Song{ID: "song-1"}, "cn",
		applemusic.TypeAlbum, "Album", "album-1", 1, "", reporter); err != nil {
		t.Fatalf("simulated processTrack failed: %v", err)
	}
	return reporter.events
}

func itemProgressSnapshot(t *testing.T, ev domain.Event) domain.JobItem {
	t.Helper()
	var snapshot domain.JobItem
	if err := json.Unmarshal([]byte(ev.Payload), &snapshot); err != nil {
		t.Fatalf("decode %s payload: %v", ev.Type, err)
	}
	return snapshot
}

// The headline invariant: with coalescing turned up so high that essentially
// every meter tick is withheld, the item must still finish, still walk every
// pipeline state, and still report a full meter at the end.
func TestCoalescingNeverSwallowsStateTransitionsOrTheFinalValue(t *testing.T) {
	// A 10s interval against a ~2s simulated transfer: no meter update clears
	// the time gate on its own, so everything that still gets published is
	// published because it is a state change or a flush.
	events := simulateTrackEvents(t, 10_000, 30_000, 20480, 30720)

	var lastTerminal = -1
	var lastProgressBeforeTerminal *domain.JobItem
	seenStatuses := map[string]bool{}
	for i, ev := range events {
		switch ev.Type {
		case eventItemProgress:
			seenStatuses[ev.Phase] = true
			if lastTerminal >= 0 {
				t.Fatalf("item_progress at index %d arrived AFTER the terminal event at %d: "+
					"a client would rewind the finished item to %q", i, lastTerminal, ev.Phase)
			}
			snapshot := itemProgressSnapshot(t, ev)
			lastProgressBeforeTerminal = &snapshot
		case "item_completed", "item_failed", "item_skipped":
			lastTerminal = i
		}
	}

	if lastTerminal < 0 {
		t.Fatal("the item never reached a terminal event")
	}
	// Every pipeline state must survive coalescing, including the two waiting
	// states, which exist only to be seen.
	for _, status := range []domain.ItemStatus{
		domain.ItemResolving, domain.ItemWaitingDownload, domain.ItemDownloading,
		domain.ItemWaitingDecrypt, domain.ItemDecrypting,
		domain.ItemRemuxing, domain.ItemSaving, domain.ItemTagging,
	} {
		if !seenStatuses[string(status)] {
			t.Errorf("state %q was coalesced away; only progress within a state may be delayed", status)
		}
	}

	// The final meter value must survive. The last progress event before the
	// terminal one has to show a finished download, not the stale percentage
	// that happened to clear the gate several seconds earlier.
	if lastProgressBeforeTerminal == nil {
		t.Fatal("no item_progress was published at all")
	}
	if lastProgressBeforeTerminal.Progress.Download != 1 {
		t.Errorf("last item_progress before the terminal event has download=%v, want 1: "+
			"the final meter value was swallowed", lastProgressBeforeTerminal.Progress.Download)
	}

	// And the terminal event itself must carry the completed breakdown, so a
	// client that only reads terminal events also converges.
	completed := itemProgressSnapshot(t, events[lastTerminal])
	if completed.Progress.Download != 1 || completed.Progress.Decrypt != 1 {
		t.Errorf("item_completed breakdown = %+v, want both meters pinned to 1", completed.Progress)
	}
	if completed.Status != domain.ItemCompleted {
		t.Errorf("terminal snapshot status = %q, want completed", completed.Status)
	}
}

// Coalescing must actually reduce the meter feed, and must do so without
// touching the count of state-transition events.
func TestCoalescingReducesMeterEventsButNotTransitions(t *testing.T) {
	countMeter := func(events []domain.Event) (meter, transitions int) {
		for _, ev := range events {
			switch {
			case ev.Type != eventItemProgress:
				continue
			case ev.Phase == string(domain.ItemDownloading) || ev.Phase == string(domain.ItemDecrypting):
				meter++
			default:
				transitions++
			}
		}
		return meter, transitions
	}

	// A slow enough transfer that the ungated run emits a long meter sweep.
	ungatedMeter, ungatedTransitions := countMeter(simulateTrackEvents(t, 0, 30_000, 2048, 2048))
	gatedMeter, gatedTransitions := countMeter(simulateTrackEvents(t, 1000, 30_000, 2048, 2048))

	if gatedMeter >= ungatedMeter {
		t.Errorf("meter events: gated %d, ungated %d — coalescing had no effect", gatedMeter, ungatedMeter)
	}
	if gatedTransitions != ungatedTransitions {
		t.Errorf("state-transition item_progress events changed under coalescing: gated %d, ungated %d",
			gatedTransitions, ungatedTransitions)
	}
}

// The event stream a resuming client replays is the persisted one, so the only
// way coalescing could strand a client on a stale value is by leaving the last
// published meter value behind the item's real state. Assert the two agree.
func TestLastPublishedProgressMatchesFinalItemState(t *testing.T) {
	events := simulateTrackEvents(t, 2000, 30_000, 20480, 30720)

	var last domain.JobItem
	var found bool
	for _, ev := range events {
		if ev.Type == eventItemProgress || ev.Type == "item_completed" {
			last = itemProgressSnapshot(t, ev)
			found = true
		}
	}
	if !found {
		t.Fatal("no snapshot-carrying event was published")
	}
	if last.Status != domain.ItemCompleted {
		t.Fatalf("last snapshot status = %q, want completed", last.Status)
	}
	if last.Progress.Download != 1 || last.Progress.Decrypt != 1 || !last.Progress.Saved {
		t.Fatalf("last published snapshot = %+v, want a fully finished breakdown", last.Progress)
	}
}

func TestProgressEventIntervalReadsConfig(t *testing.T) {
	cfg := config.Default()
	if got := progressEventInterval(cfg); got != 500*time.Millisecond {
		t.Fatalf("default interval = %v, want 500ms", got)
	}
	cfg.Download.ProgressEventIntervalMS = 0
	if got := progressEventInterval(cfg); got != 0 {
		t.Fatalf("interval for 0 = %v, want 0 (coalescing disabled)", got)
	}
	cfg.Download.ProgressEventIntervalMS = 1500
	if got := progressEventInterval(cfg); got != 1500*time.Millisecond {
		t.Fatalf("interval = %v, want 1.5s", got)
	}
}
