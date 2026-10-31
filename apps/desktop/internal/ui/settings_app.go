package ui

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/ffmpeg"
	"amdl/apps/desktop/internal/mode"
)

// The app's own settings: where downloads run, and how the app looks and
// tells. A switch applies at once; an address applies with its button,
// since applying it connects again; the other mode opens the setup screen,
// which checks what it needs before it applies.

func (a *App) connectionSettings(c *ui.Context, k *Tokens) {
	f := &a.set
	d := &f.draft
	if d.Mode != a.Settings.Mode {
		d.Mode, f.checks = a.Settings.Mode, nil
	}
	ui.Column(c).Gap(10).Children(func() {
		label(c, k, "Where downloads run")
		ui.Row(c).Gap(14).Children(func() {
			modeCard := func(m mode.Kind, ic *ui.SVG, title, body string) {
				on, off := d.Mode == m, a.Demo && m == mode.Standalone
				if off {
					body = demoNoLocal
				}
				card := ui.ButtonBase(c.Key(string(m))).Disabled(off).Grow(1).Basis(0).Padding(padCard).Gap(8).Radius(radiusCard).Border(1, k.Line2).Background(k.Raise).
					AlignItems(ui.Start).Justify(ui.Start).Label(title)
				card.Column()
				if off {
					card.Opacity(0.5)
				}
				if on {
					card.Border(1, k.Accent)
				}
				if card.Clicked() && !on {
					a.OpenSetup(m)
				}
				col := k.Muted
				if on {
					col = k.Accent
				}
				if !off {
					card.Cursor(ui.CursorPointer)
				}
				card.Children(func() {
					ui.Icon(c, ic).FontSize(22).TextColor(col)
					ui.Text(c, title).FontSize(14).FontWeight(650)
					ui.Text(c, body).FontSize(12).TextColor(k.Muted).LineHeight(1.5)
				})
			}
			modeCard(mode.Remote, icServer, "A server on my network", "Use the amdl running on your NAS. This computer only shows and controls it.")
			modeCard(mode.Standalone, icLaptop, "This computer", "Download, decrypt and tag here, with a wrapper on this computer or another one.")
		})
	})
	if d.Mode == "" {
		return
	}
	actionRow(c, k, "Setup checks", "Check setup…", func() { a.OpenSetup(a.Settings.Mode) })
	here := d.Mode == mode.Standalone && a.Settings.Mode == mode.Standalone
	if shared := a.Act.Shared(); shared != "" && here {
		ui.Row(c).Padding(10, 14).Gap(10).Radius(radiusControl).Background(k.AccentSoft).AlignItems(ui.Start).Children(func() {
			ui.Icon(c, icLaptop).FontSize(16).TextColor(k.Accent)
			ui.Column(c).Grow(1).Shrink(1).Gap(3).Children(func() {
				ui.Text(c, "Using the backend already running on this computer").FontSize(12.5).FontWeight(650)
				ui.Text(c, shared+" · the settings below apply when the app runs the backend itself").
					FontSize(12).TextColor(k.Muted).LineHeight(1.5)
			})
		})
	}
	local := here && a.Store.Server.Wrapper != nil && a.Store.Server.Wrapper.Managed != nil
	// A test that answers quickly shows no Testing…, no grey button and no
	// dimmed results: they would only flash.
	slow := waited(c, f.testing, f.testFrom)
	card(c, k).Gap(14).Children(func() {
		addr, name, help := &d.RemoteURL, "Server address", "The address of the amdl backend, as the web console opens it."
		if d.Mode == mode.Standalone {
			addr, name, help = &d.WrapperAddress, "Wrapper address", "The wrapper-lite or wrapper-manager on your NAS. It has no password, so only open its port to this computer."
			if local {
				help = "Not used: this computer runs its own wrapper."
			}
		}
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, name).FontSize(12).FontWeight(600)
			ui.Row(c).Gap(8).Children(func() {
				in := field(c, k, "addr-"+string(d.Mode), addr, "http://192.168.3.5:18570", true)
				if in.Changed() {
					f.checks = nil
				}
				if buttonOff(c, k, buttonPlain, "Test connection", nil, slow || *addr == "").Clicked() && !f.testing {
					a.runTest(*addr, c.Now())
				}
				if slow {
					pill(c, k, pillWait, nil, "Testing…")
				}
			})
			ui.Text(c, help).FontSize(11.5).TextColor(k.Faint)
		})
		// The last results stay, dimmed, until the new ones replace them,
		// so the card keeps its height.
		if len(f.checks) > 0 {
			list := ui.Column(c)
			if slow {
				list.Opacity(0.5)
			}
			list.Children(func() { a.checkList(c, k, f.checks) })
		}
	})
	if d.Mode == mode.Standalone {
		ui.Column(c).Gap(6).Children(func() {
			ui.Text(c, "ffmpeg").FontSize(12).FontWeight(600)
			placeholder := "Not found — set a path"
			if p, src, err := ffmpeg.Find("", a.Dirs.Resources); err == nil {
				placeholder = fmt.Sprintf("%s · %s", src, p)
			}
			field(c, k, "ffmpeg", &d.FFmpeg, placeholder, true)
			ui.Text(c, "Leave empty to use the one found here.").FontSize(11.5).TextColor(k.Faint)
		})
	}
	ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
		changed := d.RemoteURL != a.Settings.RemoteURL || d.WrapperAddress != a.Settings.WrapperAddress || d.FFmpeg != a.Settings.FFmpeg
		if buttonOff(c, k, buttonPrimary, "Apply", nil, !changed).Clicked() {
			next := a.Settings
			next.RemoteURL, next.WrapperAddress, next.FFmpeg = d.RemoteURL, d.WrapperAddress, d.FFmpeg
			a.Act.ApplySettings(next)
		}
		if changed {
			ui.Text(c, "Applying reconnects; downloads on the server keep going.").FontSize(11.5).TextColor(k.Faint)
		}
	})
	if d.Mode == mode.Standalone {
		on := a.Settings.KeepAwake
		if settingRow(c, k, "awake", &on, "Keep the computer awake while downloading",
			"Closing a laptop's lid still sleeps it; unfinished downloads pick up again on the next launch.").Changed() {
			next := a.Settings
			next.KeepAwake = on
			a.Act.ApplySettings(next)
		}
	}
}

func (a *App) runTest(addr string, now time.Time) {
	f := &a.set
	f.testing, f.tested, f.testFrom = true, addr, now
	done := func(checks []mode.Check) {
		if f.tested == addr {
			f.testing, f.checks = false, checks
		}
	}
	if f.draft.Mode == mode.Standalone {
		a.Act.TestWrapper(addr, done)
	} else {
		a.Act.TestServer(addr, done)
	}
}

func (a *App) checkList(c *ui.Context, k *Tokens, checks []mode.Check) {
	allOK := true
	for _, ch := range checks {
		allOK = allOK && ch.OK
	}
	bg := k.OKSoft
	if !allOK {
		bg = k.BadSoft
	}
	ui.Column(c).Padding(4, 14).Radius(radiusControl).Background(bg).Dividers(1, k.Line).Children(func() {
		for i, ch := range checks {
			ui.Row(c.Key(i)).Gap(10).PaddingY(7).AlignItems(ui.Center).Children(func() {
				ic, col := icCheck, k.OK
				if !ch.OK {
					ic, col = icCross, k.Bad
				}
				ui.Icon(c, ic).FontSize(14).TextColor(col)
				ui.Text(c, ch.Text).FontSize(12.5).Grow(1).Shrink(1)
				note := ch.Note
				if ch.Taken > 0 {
					if note != "" {
						note += " · "
					}
					note += fmt.Sprintf("%d ms", ch.Taken.Round(time.Millisecond).Milliseconds())
				}
				mono(c, note, 11).TextColor(k.Faint)
			})
		}
	})
}

func (a *App) appearanceSettings(c *ui.Context, k *Tokens) {
	ui.Column(c).Gap(8).Children(func() {
		label(c, k, "Theme")
		theme := a.set.theme
		segmented(c, k, "theme", &a.set.theme, []string{"System", "Light", "Dark"}, nil)
		if a.set.theme != theme {
			next := a.Settings
			next.Theme = themeNames[a.set.theme]
			a.set.draft.Theme = next.Theme
			a.Act.ApplySettings(next)
		}
	})
	on := a.Settings.Notify
	if settingRow(c, k, "notify", &on, "Notify when a download finishes or fails", "Shown by the system; packaged builds only on macOS.").Changed() {
		next := a.Settings
		next.Notify = on
		a.set.draft.Notify = on
		a.Act.ApplySettings(next)
	}
	skip := a.Settings.SkipExisting
	if settingRow(c, k, "skip-existing", &skip, "Skip files already downloaded", "Downloading again then skips the tracks on disk without asking.").Changed() {
		next := a.Settings
		next.SkipExisting = skip
		a.set.draft.SkipExisting = skip
		a.Act.ApplySettings(next)
	}
}

func (a *App) aboutSettings(c *ui.Context, k *Tokens) {
	ui.Column(c).Gap(6).Children(func() {
		display(c, "AMDL", 28)
		ui.Text(c, "Downloads music from Apple Music. Free software under the GNU AGPL 3.0.").TextColor(k.Muted)
		ui.Text(c, "Built with MyGo (MIT). ffmpeg, when shipped with the app, is under the LGPL.").FontSize(12).TextColor(k.Faint)
		ui.Text(c, "JetBrains Mono © 2020 The JetBrains Mono Project Authors, under the SIL Open Font License 1.1.").FontSize(12).TextColor(k.Faint)
		ui.Text(c, "Data: "+a.Dirs.Data).Font(MonoFont).FontSize(11.5).TextColor(k.Faint)
		ui.Text(c, "Web console: "+a.consoleURL()).Font(MonoFont).FontSize(11.5).TextColor(k.Faint)
	})
}
