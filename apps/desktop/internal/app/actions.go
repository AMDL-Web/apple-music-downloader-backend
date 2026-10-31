package app

import (
	"context"
	"math"

	"github.com/egoist/mygo"

	"amdl/apps/desktop/internal/mode"
	appui "amdl/apps/desktop/internal/ui"
)

// The methods below implement ui.Actions.

// ApplySettings saves the settings and connects again when where downloads
// run changed. The demo keeps them in memory.
func (a *App) ApplySettings(s mode.Settings) {
	prev := a.settings
	a.settings = s
	a.view.Settings = s
	if a.demo == nil {
		if err := mode.SaveSettings(a.dirs.Data, s); err != nil {
			a.log.Error("settings not saved", "error", err)
		}
	}
	if s.FFmpeg != prev.FFmpeg {
		a.findFFmpeg()
	}
	if s.Mode != prev.Mode || s.RemoteURL != prev.RemoteURL || s.WrapperAddress != prev.WrapperAddress ||
		(s.Mode == mode.Standalone && s.FFmpeg != prev.FFmpeg) {
		a.connect()
	}
	a.shell.Sync(a.store.Summary())
}

// TestServer tests a remote backend's address off the UI thread.
func (a *App) TestServer(address string, done func([]mode.Check)) {
	go func() {
		var checks []mode.Check
		if c, err := a.newClient(address); err != nil {
			checks = []mode.Check{{Text: err.Error()}}
		} else {
			checks = mode.TestServer(context.Background(), c)
		}
		a.post(func() { done(checks) })
	}()
}

// TestWrapper tests a wrapper address off the UI thread.
func (a *App) TestWrapper(address string, done func([]mode.Check)) {
	go func() {
		checks := mode.TestWrapper(context.Background(), address)
		a.post(func() { done(checks) })
	}()
}

// Reveal shows a path of this computer in the file manager.
func (a *App) Reveal(path string) { mygo.Shell.ShowItemInFolder(path) }

// CopyText puts text on the clipboard.
func (a *App) CopyText(s string) { mygo.Clipboard.WriteText(s) }

// Starting reports an embedded backend that is starting.
func (a *App) Starting() bool { return a.starting }

// StartErr is why the backend could not be reached or started.
func (a *App) StartErr() string { return a.startErr }

// Reconnect connects again, restarting the backend standalone mode runs.
func (a *App) Reconnect() { a.connect() }

// OpenURL opens url in the browser.
func (a *App) OpenURL(url string) {
	if err := mygo.Shell.OpenExternal(url); err != nil {
		a.log.Warn("open url", "url", url, "error", err)
	}
}

// ChooseFile shows the system's file chooser off the UI thread.
func (a *App) ChooseFile(title string, done func(string)) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{Parent: a.win, Title: title})
		if err != nil {
			a.log.Warn("file chooser", "error", err)
			return
		}
		if len(paths) > 0 {
			a.post(func() { done(paths[0]) })
		}
	}()
}

// ChooseFolder shows the system's folder chooser off the UI thread.
func (a *App) ChooseFolder(start string, done func(string)) {
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent: a.win, Title: "Download folder", DefaultPath: start, Directory: true, CreateDirectories: true,
		})
		if err != nil {
			a.log.Warn("folder chooser", "error", err)
			return
		}
		if len(paths) > 0 {
			a.post(func() { done(paths[0]) })
		}
	}()
}

// ShowWindow brings the main window forward and closes the tray panel.
func (a *App) ShowWindow() {
	if a.panel != nil {
		a.panel.Close()
	}
	a.showWindow()
}

// FitPanel sizes the tray panel to the height its content laid out.
func (a *App) FitPanel(height float32) {
	if a.panel != nil {
		a.panelH = int(math.Ceil(float64(height)))
		a.panel.SetContentSize(appui.PanelWidth, a.panelH)
	}
}
