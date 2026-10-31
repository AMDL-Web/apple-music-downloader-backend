// Package ui builds the desktop app's screens with MyGo's native UI, to the
// design canvas: home with its one field for links and catalog search, the
// queue with the album-lit "downloading now" hero, the job detail, settings,
// system, the menu bar panel, and their empty, loading and error states, dark
// and light.
package ui

import (
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/artwork"
	"amdl/apps/desktop/internal/mode"
	"amdl/apps/desktop/internal/motion"
	"amdl/apps/desktop/internal/setup"
	"amdl/apps/desktop/internal/store"
)

// Screen is a page of the main window.
type Screen int

// Screens. Home, the zero Screen, is the one the window opens on.
const (
	ScreenHome Screen = iota
	ScreenQueue
	ScreenSettings
	ScreenDetail
	ScreenAlbum
	// ScreenSetup checks and installs what a mode needs (OpenSetup).
	ScreenSetup
)

// Actions are what the screens ask of the app around them.
type Actions interface {
	// ApplySettings saves the settings and, when the mode or its address
	// changed, connects again.
	ApplySettings(s mode.Settings)
	// TestServer and TestWrapper run a connection test and hand its checks
	// back on the UI thread.
	TestServer(address string, done func([]mode.Check))
	TestWrapper(address string, done func([]mode.Check))
	// Reveal shows a file or folder of the backend's machine in the file
	// manager, when it is this computer.
	Reveal(path string)
	// CopyText puts text on the clipboard.
	CopyText(s string)
	// ChooseFolder asks for a folder of this computer, starting at start,
	// and hands it to done on the UI thread unless the user cancels.
	ChooseFolder(start string, done func(dir string))
	// ShowWindow brings the main window forward, closing the tray panel.
	ShowWindow()
	// FitPanel sizes the tray panel to its content's height in DIPs.
	FitPanel(height float32)
	// Starting reports a standalone backend that is starting, and StartErr
	// why the last start failed.
	Starting() bool
	StartErr() string
	// Shared names the backend another program on this computer runs, such
	// as amdl service, which standalone mode uses instead of its own; "" when
	// the app runs its own.
	Shared() string
	// Reconnect connects to the backend the settings name again, restarting
	// the one standalone mode runs, so that it picks up a guest or a tool
	// installed since it started.
	Reconnect()
	// ChooseFile asks for a file of this computer and hands it to done on
	// the UI thread unless the user cancels.
	ChooseFile(title string, done func(path string))
	// OpenURL opens a web page in the browser.
	OpenURL(url string)
}

// App is the main window's view and the state of its screens. Use it on the
// UI thread.
type App struct {
	Store *store.Store
	Art   *artwork.Cache
	// Motion plays motion covers; nil shows the stills only.
	Motion   *motion.Cache
	Settings mode.Settings
	Dirs     mode.Dirs
	Act      Actions
	// Setup checks and installs what standalone mode needs here.
	Setup *setup.Setup
	// Theme overrides the system appearance for screenshots: "light",
	// "dark", or "" to follow the settings.
	Theme string
	// NoMotion shows stills in place of motion covers, for a render of the
	// view beside its window.
	NoMotion bool
	// Opaque is set while the desktop asks for opaque surfaces (Reduce
	// Transparency, Windows's transparency effects off): the sidebar draws
	// its own ground over the window's material.
	Opaque bool
	// Demo is set while the app runs the demo (package demo): there is no
	// backend of this computer to offer.
	Demo bool

	Screen Screen
	// trail holds the screens Back returns to, the last one on top: the
	// album screen and the detail push the one they were opened from.
	trail []Screen
	// Tab is the queue's filter.
	Tab int

	// winW is the window's width as last built; compact is set while the
	// page is narrower than compactContent. openNarrow is set while the
	// user keeps the sidebar open in a window too narrow for it.
	winW       float32
	compact    bool
	openNarrow bool

	// ScrollbarsAlways shows the scroll thumbs at all times, as the desktop
	// does where its setting says so; otherwise they show while a list
	// moves (scrollbars).
	ScrollbarsAlways bool
	// scrolls are the places of the scroll containers by key, scrollSeen
	// where each was last built, and scrolledAt when one last moved.
	scrolls    map[string]*ui.ScrollState
	scrollSeen map[string]float32
	scrolledAt time.Time

	queue ui.ListState
	// field is home's field, and fieldTxt its text, which follows the
	// store's Omni.Text.
	field    ui.Handle
	fieldTxt string
	// focusField asks home to put the keyboard in its field as it builds
	// it, in the view that does: a render beside the window binds the
	// field too, which an unaddressed Handle.Focus refuses.
	focusField bool
	set        settingsForm
	su         setupForm
	// searchSel is the HitKey of the hit the search acts on; searchReveal
	// scrolls it into view once, after the keyboard moved it.
	searchSel    string
	searchReveal bool
	// confirmAll is the artist whose "Download all" waits for its second
	// click since confirmAt.
	confirmAll string
	confirmAt  time.Time
	// deleting is the job whose delete waits for its dialog's answer;
	// deleteOpen keeps the dialog up.
	deleting   *store.Job
	deleteOpen bool
	// nowShown is the job the sidebar's now card shows, kept while the
	// card closes after it.
	nowShown *store.Job
	// panelH is the height the tray panel was last fitted to.
	panelH float32
}

// tokens returns the colors of the current appearance.
func (a *App) tokens(c *ui.Context) *Tokens {
	dark := c.Theme().Dark
	switch {
	case a.Theme == "light":
		dark = false
	case a.Theme == "dark":
		dark = true
	case a.Settings.Theme == "light":
		dark = false
	case a.Settings.Theme == "dark":
		dark = true
	}
	if dark {
		return &DarkTokens
	}
	return &LightTokens
}

// Go switches the main window to a screen, forgetting where Back would
// return. Home puts the keyboard in its field.
func (a *App) Go(s Screen) {
	if s != ScreenDetail {
		a.Store.CloseDetail()
	}
	a.trail = nil
	a.Screen = s
	if s == ScreenHome {
		a.focusField = true
	}
}

// OpenJob shows a job's detail; Back returns to the screen it opened from.
func (a *App) OpenJob(id string) {
	a.Store.OpenDetail(id)
	a.push(ScreenDetail)
}

// push shows s, which Back leaves for the screen shown now. Setup is left
// behind: Back never returns to it.
func (a *App) push(s Screen) {
	if a.Screen != s && a.Screen != ScreenSetup {
		a.trail = append(a.trail, a.Screen)
	}
	a.Screen = s
}

// backTo is the screen Back returns to: the one the album screen or the
// detail was opened from, else home for the album screen and the queue for
// the detail.
func (a *App) backTo() Screen {
	if n := len(a.trail); n > 0 {
		return a.trail[n-1]
	}
	if a.Screen == ScreenAlbum {
		return ScreenHome
	}
	return ScreenQueue
}

// navScreen is the sidebar's screen for the one shown: the album screen and
// the detail light the screen they were opened from.
func (a *App) navScreen() Screen {
	for i := len(a.trail) - 1; i >= 0; i-- {
		if s := a.trail[i]; s != ScreenAlbum && s != ScreenDetail {
			return s
		}
	}
	switch a.Screen {
	case ScreenAlbum:
		return ScreenHome
	case ScreenDetail:
		return ScreenQueue
	}
	return a.Screen
}

// Back returns to the screen the album screen or the detail was opened
// from: home keeps its words, results, artist and place, so the search
// comes back as it was left, with the keyboard in its field.
func (a *App) Back() {
	to := a.backTo()
	if n := len(a.trail); n > 0 {
		a.trail = a.trail[:n-1]
	}
	if to != ScreenDetail {
		a.Store.CloseDetail()
	}
	if to == ScreenHome {
		if a.Screen == ScreenAlbum {
			a.revealAlbum()
		}
		a.focusField = true
	}
	a.Screen = to
}

// FocusField shows home and puts the keyboard in its field.
func (a *App) FocusField() { a.Go(ScreenHome) }

// PanelClosed forgets the tray panel's size, so the next one fits again.
func (a *App) PanelClosed() { a.panelH = 0 }
