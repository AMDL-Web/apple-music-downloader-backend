package ui

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/store"
	"amdl/internal/domain"
)

// panelWindow stands for the tray panel's window: FitPanel resizes it, as
// the flyout's SetContentSize does, once the frame asking for it ends.
type panelWindow struct {
	*fakeActions
	tt   *ui.Tester
	fits []float32
	// shown is the window's height now.
	shown int
}

func (w *panelWindow) FitPanel(h float32) { w.fits = append(w.fits, h) }

// height is the window's height: the last fit, or the one it opened at.
func (w *panelWindow) height(open float32) float32 {
	if len(w.fits) == 0 {
		return open
	}
	return w.fits[len(w.fits)-1]
}

// resize gives the window the height the panel last asked for. A window
// resized draws a frame; one left as it is draws none.
func (w *panelWindow) resize() {
	if h := int(w.height(460) + 0.999); h != w.shown {
		w.shown = h
		w.tt.SetSize(PanelWidth, h)
	}
}

// panelBusy is a download under way, with two more queued: the panel's
// tallest ordinary state, with chips, the "downloading now" card and two
// small rows.
func panelBusy(s *store.Store, now time.Time) {
	started := now.Add(-2 * time.Minute)
	running := &store.Job{Job: domain.Job{ID: "run", Type: "album", Status: domain.JobRunning, Title: "Midnight Archive", ArtistName: "The Long Signal",
		TotalItems: 12, CompletedItems: 4, CreatedAt: started, StartedAt: &started}}
	q1 := &store.Job{Job: domain.Job{ID: "q1", Type: "album", Status: domain.JobQueued, Title: "Second Album", ArtistName: "Someone", TotalItems: 9, CreatedAt: now}}
	q2 := &store.Job{Job: domain.Job{ID: "q2", Type: "album", Status: domain.JobQueued, Title: "Third Album", ArtistName: "Someone Else", TotalItems: 7, CreatedAt: now}}
	s.Loaded = true
	s.Jobs = []*store.Job{running, q1, q2}
}

// panelIdle is what panelBusy turns into once every download finished.
func panelIdle(s *store.Store, now time.Time) {
	s.Loaded = true
	if len(s.Jobs) == 0 {
		panelBusy(s, now)
	}
	for _, j := range s.Jobs {
		done := now
		j.Status, j.FinishedAt, j.CompletedItems = domain.JobCompleted, &done, j.TotalItems
	}
}

// settledPanelHeight is the height a panel newly opened on a state fits
// itself to.
func settledPanelHeight(t *testing.T, seed func(*store.Store, time.Time), now time.Time) float32 {
	t.Helper()
	a, act := newTestApp(remoteSettings())
	seed(a.Store, now)
	w := &panelWindow{fakeActions: act, shown: 460}
	a.Act = w
	w.tt = ui.NewTester(a.Panel, PanelWidth, 460)
	for i := 0; i < 5; i++ {
		w.resize()
		w.tt.Advance(5 * time.Millisecond)
	}
	if len(w.fits) == 0 {
		t.Fatal("the panel never fitted its window")
	}
	return w.height(460)
}

// The panel's window follows its content as the store changes, without
// waiting for another frame to come by: the store's change is the only
// frame the window gets (App.post invalidates it), and the next one the
// panel asks for itself is ten seconds away (store.ServerEvery).
func TestPanelFitsItsContentAsTheStoreChanges(t *testing.T) {
	now := time.Now()
	busy := settledPanelHeight(t, panelBusy, now)
	idle := settledPanelHeight(t, panelIdle, now)
	if busy <= idle {
		t.Fatalf("busy panel %v is not taller than the idle one %v", busy, idle)
	}

	for _, tc := range []struct {
		name       string
		from, to   func(*store.Store, time.Time)
		want, open float32
	}{
		{"downloads finish", panelBusy, panelIdle, idle, busy},
		{"a download starts", panelIdle, panelBusy, busy, idle},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, act := newTestApp(remoteSettings())
			tc.from(a.Store, now)
			w := &panelWindow{fakeActions: act, shown: 460}
			a.Act = w
			w.tt = ui.NewTester(a.Panel, PanelWidth, 460)
			for i := 0; i < 5; i++ {
				w.resize()
				w.tt.Advance(5 * time.Millisecond)
			}
			if got := w.height(460); got != tc.open {
				t.Fatalf("opened at %v, want %v", got, tc.open)
			}
			tc.to(a.Store, now)
			// App.post's Invalidate, and the frames that one asks for
			// at once: no timer runs.
			w.tt.Frame()
			if got := w.height(460); got != tc.want {
				t.Errorf("after the change the window is %v high, its content %v", got, tc.want)
			}
			// Settled, the panel asks for no more frames or fitting.
			w.resize()
			if w.tt.FramePending() {
				t.Error("the settled panel still asks for frames")
			}
			fits := len(w.fits)
			w.tt.Advance(time.Second)
			if len(w.fits) != fits {
				t.Errorf("the settled panel fitted its window again: %v", w.fits[fits:])
			}
		})
	}
}
