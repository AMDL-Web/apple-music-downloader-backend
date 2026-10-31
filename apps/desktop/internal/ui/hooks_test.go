package ui

import (
	"context"
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"

	"amdl/internal/client"
)

// hooksApp is Settings at Hooks over the demo backend, which starts with a
// command hook and a Jellyfin webhook.
func hooksApp(t *testing.T) (*App, *ui.Tester, func(until func() bool)) {
	t.Helper()
	a, run := demoApp(t)
	a.GoSettings("hooks")
	tt := ui.NewTester(a.View, 1280, 900)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	run(func() bool { return a.Store.Server.Hooks != nil && a.Store.Server.HookPresets != nil })
	// The pin brings Hooks to the top over a few frames.
	for range 4 {
		tt.Frame()
	}
	return a, tt, run
}

func hookNamed(a *App, name string) (client.HookEntry, bool) {
	h := a.Store.Server.Hooks
	if h == nil {
		return client.HookEntry{}, false
	}
	i := slices.IndexFunc(h.Entries, func(e client.HookEntry) bool { return e.Name == name })
	if i < 0 {
		return client.HookEntry{}, false
	}
	return h.Entries[i], true
}

func TestACommandHookIsOnlyShown(t *testing.T) {
	_, tt, _ := hooksApp(t)
	if !tt.HasText("tag-and-move") {
		t.Fatalf("no command hook in %q", tt.Texts())
	}
	if _, ok := tt.Find("Edited in hooks.yaml"); !ok {
		t.Errorf("the command hook does not say where it is edited")
	}
	for _, control := range []string{"More actions for tag-and-move", "Send a test of tag-and-move", "tag-and-move on"} {
		if _, ok := tt.Find(control); ok {
			t.Errorf("the command hook has %q", control)
		}
	}
	if _, ok := tt.Find("More actions for jellyfin"); !ok {
		t.Errorf("the webhook has no menu")
	}
}

func TestAPresetAddsAWebhook(t *testing.T) {
	a, tt, run := hooksApp(t)
	for _, step := range []string{"Add hook", "ntfy"} {
		if err := tt.Click(step); err != nil {
			t.Fatalf("%s: %v in %q", step, err, tt.Texts())
		}
	}
	// A required field left empty is the backend's to say, beside the
	// buttons.
	if err := tt.Click("Add"); err != nil {
		t.Fatal(err)
	}
	run(func() bool { return a.hookAsk.err != "" })
	tt.Frame()
	if !tt.HasText("Topic is needed") {
		t.Fatalf("an empty topic was sent: %q", tt.Texts())
	}
	if err := tt.Click("amdl-downloads"); err != nil {
		t.Fatal(err)
	}
	tt.Type("my-amdl")
	if err := tt.Click("Add"); err != nil {
		t.Fatal(err)
	}
	run(func() bool { _, ok := hookNamed(a, "ntfy"); return ok })
	tt.Frame()
	e, _ := hookNamed(a, "ntfy")
	if e.Origin != "https://ntfy.sh" || e.Preset != "ntfy" || !slices.Equal(e.Events, []string{"job_finished", "job_failed"}) || a.hookAsk != nil {
		t.Errorf("added %+v, sheet %v", e, a.hookAsk)
	}
	if !tt.HasText("ntfy.sh") {
		t.Errorf("the new hook's row does not show its host: %q", tt.Texts())
	}
}

func TestEditingAHookSendsOnlyWhatChanged(t *testing.T) {
	a, tt, run := hooksApp(t)
	for _, step := range []string{"More actions for jellyfin", "Edit…", "Failed"} {
		if err := tt.Click(step); err != nil {
			t.Fatalf("%s: %v in %q", step, err, tt.Texts())
		}
	}
	noFlash(t, tt, "Save", func() { run(func() bool { return !a.hookAsk.open }) })
	e, _ := hookNamed(a, "jellyfin")
	// The address stays: the edit sent events and types alone.
	if !slices.Contains(e.Events, "job_failed") || e.Origin != "http://jellyfin.local:8096" {
		t.Errorf("jellyfin after the edit: %+v", e)
	}
}

func TestHooksSwitchAndDeleteReachTheBackend(t *testing.T) {
	a, tt, run := hooksApp(t)
	if err := tt.Click("Run hooks"); err != nil {
		t.Fatal(err)
	}
	if a.Store.Server.Hooks.Enabled {
		t.Errorf("the switch did not turn off at once")
	}
	for _, step := range []string{"More actions for jellyfin", "Delete…", "Delete"} {
		if err := tt.Click(step); err != nil {
			t.Fatalf("%s: %v in %q", step, err, tt.Texts())
		}
	}
	run(func() bool { _, ok := hookNamed(a, "jellyfin"); return !ok })
	h, err := a.Store.Client().Hooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if h.Enabled || len(h.Entries) != 1 || h.Entries[0].Name != "tag-and-move" {
		t.Errorf("the backend has %+v", h)
	}
}
