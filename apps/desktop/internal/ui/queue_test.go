package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/artwork"
	"amdl/apps/desktop/internal/demo"
	"amdl/apps/desktop/internal/store"
	"amdl/internal/client"
)

// demoApp is an App on the demo's in-memory backend, whose answers wait
// until run applies them on the test's goroutine, as the UI thread would.
func demoApp(t *testing.T) (*App, func(until func() bool)) {
	t.Helper()
	a, _ := newTestApp(remoteSettings())
	queue := make(chan func(), 1024)
	post := func(fn func()) { queue <- fn }
	b := demo.New(time.Now)
	a.Store = store.New(post, nil)
	// Covers too come back on the test's goroutine, from the demo.
	a.Art = artwork.NewWith(post, b.Transport())
	c, err := client.NewWith(demo.Address, b.Transport())
	if err != nil {
		t.Fatal(err)
	}
	a.Store.Connect(c)
	t.Cleanup(a.Store.Close)
	return a, func(until func() bool) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for !until() {
			select {
			case fn := <-queue:
				fn()
			case <-deadline:
				t.Fatal("the demo backend's answer never arrived")
			}
		}
	}
}

func TestTheRowMenuOpensOnAClickAndDeletesOnlyAFinishedJob(t *testing.T) {
	a, run := demoApp(t)
	a.Screen = ScreenQueue
	run(func() bool { return a.Store.Loaded })

	// A job under way is cancelled from its menu, never deleted.
	a.Tab = int(store.TabActive)
	tt := ui.NewTester(a.View, 1280, 800)
	// Still stripes let each click's frames settle.
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	cancels := strings.Count(strings.Join(tt.Texts(), "\n"), "Cancel")
	if err := tt.Click("More actions"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Show details") || strings.Count(strings.Join(tt.Texts(), "\n"), "Cancel") <= cancels || tt.HasText("Delete…") {
		t.Errorf("a running job's menu shows %q", tt.Texts())
	}
	tt.Key(0, ui.KeyEscape)

	a.Tab = int(store.TabDone)
	tt.Frame()
	done := a.Store.Filter(store.TabDone, "")[0]
	if err := tt.Click("More actions"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Delete…"); err != nil {
		t.Fatalf("a finished job's menu: %v in %q", err, tt.Texts())
	}
	if !tt.HasText("Delete “" + done.Name() + "”?") {
		t.Fatalf("no delete dialog in %q", tt.Texts())
	}
	if err := tt.Click("Delete"); err != nil {
		t.Fatal(err)
	}
	run(func() bool { return a.Store.Job(done.ID) == nil })
	tt.Frame()
	if tt.HasText(done.Name()) {
		t.Errorf("%s still shows after its delete: %q", done.Name(), tt.Texts())
	}
}
