// Package app runs the desktop app: it loads the settings, connects to a
// remote backend or starts the embedded one, owns the main window, the tray
// and the store, and does what the screens ask of it.
package app

import (
	"cmp"
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/artwork"
	"amdl/apps/desktop/internal/ffmpeg"
	"amdl/apps/desktop/internal/mode"
	"amdl/apps/desktop/internal/motion"
	"amdl/apps/desktop/internal/setup"
	"amdl/apps/desktop/internal/shell"
	"amdl/apps/desktop/internal/store"
	appui "amdl/apps/desktop/internal/ui"
	"amdl/internal/buildinfo"
	"amdl/internal/client"
	"amdl/internal/demo"
	"amdl/internal/wrapperhost"
)

// Options are the command line's choices. The flags exist for development
// and screenshots; a person launches the app without any.
type Options struct {
	// DataDir replaces the user data directory, to keep a test run apart.
	DataDir string
	// Remote, or Wrapper, starts in that mode with that address instead of
	// the saved settings, without saving them.
	Remote, Wrapper string
	// Theme forces "light" or "dark".
	Theme string
	// Shots captures every screen into this directory as PNGs, then quits.
	Shots string
	// Submit downloads this link once the list loads, before the shots.
	Submit string
	// Demo runs the app on a made-up backend in memory (package demo), as
	// the browser build always does: it reads and writes no files.
	Demo bool
}

// App is the running desktop app. Its fields belong to the UI thread.
type App struct {
	opts     Options
	log      *slog.Logger
	dirs     mode.Dirs
	settings mode.Settings

	win *mygo.Window
	// panel is the tray icon's open panel, or nil.
	panel *mygo.Window
	store *store.Store
	shell *shell.Shell
	view  *appui.App

	// panelH is the height in DIPs the panel last fitted its content to,
	// which the next one opens at, or 0 before the first.
	panelH int

	// standaloneBackend is the backend standalone mode runs or uses.
	standaloneBackend
	// demo is the backend the demo runs on, or nil.
	demo     *demo.Backend
	starting bool
	startErr string
	// gen counts connections, so a backend that finishes starting after the
	// settings changed again is stopped instead of used.
	gen      int
	quitting bool
	// menuSet is set once the menu bar is installed, with the sidebar's
	// state its item names in menuSidebar (syncMenu).
	menuSet, menuSidebar bool
	// appearance is the theme source last handed to MyGo (syncAppearance).
	appearance mygo.ThemeSource
}

// Run runs the app until it quits.
func Run(opts Options) error {
	a := &App{opts: opts, log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))}
	mygo.App.SetName("AMDL")
	if opts.DataDir != "" {
		// A test run downloads under its own directory too, not into the
		// user's music folder.
		mygo.App.SetPath(mygo.PathMusic, filepath.Join(opts.DataDir, "music"))
		mygo.App.SetPath(mygo.PathUserData, filepath.Join(opts.DataDir, "data"))
		mygo.App.SetPath(mygo.PathCache, filepath.Join(opts.DataDir, "cache"))
		mygo.App.SetPath(mygo.PathLogs, filepath.Join(opts.DataDir, "logs"))
	}
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { a.quit() })
	mygo.App.WhenReady(a.ready)
	return mygo.App.Run()
}

func (a *App) ready() {
	if err := a.load(); err != nil {
		mygo.Dialog.Error("AMDL cannot start", err.Error())
		mygo.App.Quit()
		return
	}
	if err := appui.RegisterFonts(); err != nil {
		// The system's monospace face stands in; the app still works.
		a.log.Warn("embedded fonts unreadable", "error", err)
	}
	dirs := a.dirs

	a.store = store.New(a.post, nil)
	art := artwork.New(a.post)
	if a.demo != nil {
		art = artwork.NewWith(a.post, a.demo.Transport())
	}
	a.view = &appui.App{Store: a.store, Art: art, Settings: a.settings, Dirs: dirs, Act: a, Theme: a.opts.Theme, Demo: a.demo != nil}
	a.view.Motion = motion.New(a.post, filepath.Join(dirs.Cache, "motion"), a.log)
	a.view.ScrollbarsAlways = scrollbarsAlways()
	deps := setup.SystemDeps(dirs.Resources, wrapperhost.Home{Dir: a.wrapperDir()},
		wrapperhost.NightlyLink{Client: &http.Client{}}, func(ctx context.Context) (wrapperhost.Pin, error) {
			pin, _, err := wrapperhost.CurrentPin(ctx, &http.Client{}, time.Now())
			return pin, err
		})
	deps.Diagnose = func(ctx context.Context, address string) setup.Server {
		c, err := a.newClient(address)
		if err != nil {
			return setup.Server{Address: address, State: setup.Failed, Reach: setup.Line{State: setup.Failed, Text: err.Error()}}
		}
		return setup.DiagnoseClient(ctx, c, address, buildinfo.Get().Version)
	}
	a.view.Setup = setup.New(a.post, deps)
	a.view.Opaque = a.opaque()
	a.appearance = appearanceOf(a.view)
	mygo.Theme.SetSource(a.appearance)
	a.findFFmpeg()
	switch {
	case a.settings.Mode == "":
		a.view.OpenSetup("")
	case a.demo != nil:
		// The demo opens where its jobs move.
		a.view.Go(appui.ScreenQueue)
	default:
		a.view.FocusField()
	}
	a.win = mygo.NewWindow(mygo.WindowOptions{
		Title:           "AMDL",
		Width:           1280,
		Height:          800,
		MinWidth:        820,
		MinHeight:       560,
		StateKey:        "main",
		TitleBarStyle:   mygo.TitleBarHidden,
		Vibrancy:        vibrancy(),
		BackgroundColor: "light-dark(#f6f7fa, #111219)",
		Content: ui.View(func(c *ui.Context) {
			a.view.View(c)
			a.syncAppearance()
			a.syncMenu()
		}),
	})
	keepOnScreen(a.win)
	mygo.Theme.OnUpdated(func() {
		opaque := a.opaque()
		a.post(func() { a.view.Opaque = opaque })
	})
	a.shell = shell.New(shell.Options{
		Open:      a.showWindow,
		OpenJob:   func(id string) { a.showWindow(); a.view.OpenJob(id) },
		Panel:     a.togglePanel,
		Notify:    func() bool { return a.settings.Notify },
		KeepAwake: func() bool { return a.settings.KeepAwake },
		Log:       a.log,
	})
	a.store.SetNotifier(a.shell)
	a.win.OnClose(func(e *mygo.CloseEvent) {
		// With a tray icon, closing the window keeps downloads and their
		// notifications going; the tray's Quit ends the app.
		if !a.quitting && a.shell.HasTray() {
			e.PreventDefault()
			a.win.Hide()
		}
	})
	a.connect()
	if a.opts.Shots != "" {
		go a.captureScreens(a.opts.Shots)
	}
}

// load reads the app's directories and settings, with the command line's
// choices over the settings. The demo has neither: it starts on its own
// backend and keeps its settings in memory.
func (a *App) load() error {
	if a.opts.Demo {
		a.demo = demo.New(time.Now)
		a.demo.Start(context.Background())
		a.settings = mode.DefaultSettings()
		a.settings.Mode, a.settings.RemoteURL = mode.Remote, demo.Address
		return nil
	}
	dirs, err := mode.AppDirs()
	if err != nil {
		return err
	}
	a.dirs = dirs
	if a.settings, err = mode.LoadSettings(dirs.Data); err != nil {
		a.log.Warn("settings unreadable; starting from the defaults", "error", err)
	}
	switch {
	case a.opts.Remote != "":
		a.settings.Mode, a.settings.RemoteURL = mode.Remote, a.opts.Remote
	case a.opts.Wrapper != "":
		a.settings.Mode, a.settings.WrapperAddress = mode.Standalone, a.opts.Wrapper
	}
	return nil
}

// newClient is a client of the backend at address: in the demo, the demo's
// backend, whatever the address.
func (a *App) newClient(address string) (*client.Client, error) {
	if a.demo != nil {
		return client.NewWith(address, a.demo.Transport())
	}
	return client.New(address)
}

// keepOnScreen moves w inside the work area of the display nearest it.
// MyGo restores a remembered frame as long as part of it shows, so after a
// display shrank the hidden title bar, the one strip that drags the window,
// can sit above the screen.
func keepOnScreen(w *mygo.Window) {
	b := w.Bounds()
	d := mygo.Screen.DisplayNearestPoint(mygo.Point{X: b.X + b.Width/2, Y: b.Y + b.Height/2}).WorkArea
	if d.Width == 0 || d.Height == 0 {
		return
	}
	x := min(max(b.X, d.X), d.X+max(d.Width-b.Width, 0))
	y := min(max(b.Y, d.Y), d.Y+max(d.Height-b.Height, 0))
	if x != b.X || y != b.Y {
		w.SetPosition(x, y)
	}
}

// findFFmpeg gives the motion covers the ffmpeg the settings lead to. In
// remote mode the backend's own ffmpeg is on its machine, so motion covers
// need one here too; without one the stills show.
func (a *App) findFFmpeg() {
	p, _, err := ffmpeg.Find(a.settings.FFmpeg, a.dirs.Resources)
	if err != nil {
		a.log.Info("no ffmpeg for motion covers", "error", err)
	}
	a.view.Motion.SetFFmpeg(p)
}

// vibrancy is the window's material, which shows through the sidebar (as
// MyGo 云音乐's): the sidebar material on macOS, which blurs the desktop
// behind the window, and Mica, the closest backdrop, on Windows 11 22H2
// and later (windowsMaterial). Older Windows and Linux have none, and the
// sidebar draws its own ground there (ui.Context.Vibrancy).
func vibrancy() mygo.Vibrancy {
	if runtime.GOOS == "darwin" || windowsMaterial() {
		return mygo.VibrancySidebar
	}
	return mygo.VibrancyNone
}

// opaque reports whether the sidebar draws its own ground over the
// window's material: the desktop asks for opaque surfaces, or the run
// captures screens, which Window.CapturePage renders without the material.
func (a *App) opaque() bool {
	return a.opts.Shots != "" || mygo.Theme.Preferences().ReduceTransparency
}

// syncAppearance has the window's own appearance, which its material and
// window controls take, follow the theme the view shows (appearanceOf)
// once a frame changed it. Without it a light theme would sit on a dark
// material where the system is dark. The call goes through the main loop,
// not inside the frame.
func (a *App) syncAppearance() {
	if src := appearanceOf(a.view); src != a.appearance {
		a.appearance = src
		go mygo.Theme.SetSource(src)
	}
}

// appearanceOf is the appearance v shows: the screenshots' or the
// settings' choice, else the system's.
func appearanceOf(v *appui.App) mygo.ThemeSource {
	theme := v.Theme
	if theme == "" {
		theme = v.Settings.Theme
	}
	switch theme {
	case "light":
		return mygo.ThemeLight
	case "dark":
		return mygo.ThemeDark
	}
	return mygo.ThemeSystem
}

// post runs fn on the UI thread, then refreshes what the shell shows, and
// redraws the window.
func (a *App) post(fn func()) {
	a.win.Update(func() {
		fn()
		if a.shell != nil {
			a.shell.Sync(a.store.Summary())
		}
		if a.panel != nil {
			a.panel.Invalidate()
		}
	})
}

// togglePanel opens the tray icon's panel next to the icon, or closes it.
func (a *App) togglePanel() {
	if a.panel != nil {
		a.panel.Close()
		return
	}
	tray := a.shell.Tray()
	if tray == nil {
		a.showWindow()
		return
	}
	a.panel = mygo.NewFlyout(mygo.FlyoutOptions{
		Tray:      tray,
		Placement: mygo.PlacementBottom,
		Gap:       4,
		Width:     appui.PanelWidth,
		// Opened at the height the last one fitted, it does not show at
		// another for the frames before the view measures its content.
		Height:    cmp.Or(a.panelH, 460),
		Focusable: true,
		Shadow:    true,
		Popover:   runtime.GOOS == "darwin",
		Content:   ui.View(a.view.Panel),
	})
	a.panel.OnClosed(func() {
		a.panel = nil
		a.view.PanelClosed()
	})
}

func (a *App) showWindow() {
	a.win.Show()
	a.win.Focus()
}

// connect follows the backend the settings name, starting the embedded one
// in standalone mode.
func (a *App) connect() {
	a.gen++
	gen := a.gen
	a.startErr = ""
	old := a.backend
	a.standaloneBackend = standaloneBackend{}
	a.store.Connect(nil)
	switch a.settings.Mode {
	case mode.Remote:
		if old != nil {
			go a.stopBackend(old)
		}
		c, err := a.newClient(a.settings.RemoteURL)
		if err != nil {
			a.startErr = err.Error()
			return
		}
		a.store.Connect(c)
	case mode.Standalone:
		a.startLocal(gen, old)
	default:
		if old != nil {
			go a.stopBackend(old)
		}
	}
}

func (a *App) quit() {
	if a.quitting {
		return
	}
	a.quitting = true
	if a.store != nil {
		a.store.Close()
	}
	if a.shell != nil {
		a.shell.Close()
	}
	if a.backend != nil {
		// The backend saves its state and stops its workers; unfinished
		// jobs resume on the next launch.
		a.stopBackend(a.backend)
	}
}
