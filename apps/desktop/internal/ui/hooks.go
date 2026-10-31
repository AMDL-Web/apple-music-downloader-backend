package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/internal/client"
)

// hookEvents and hookTypes are the events and job types a hook runs on, in
// the order the editor offers them.
var (
	hookEvents = []struct{ id, label string }{
		{"job_finished", "Finished"}, {"job_failed", "Failed"}, {"job_cancelled", "Cancelled"}, {"job_queued", "Queued"},
	}
	hookTypes = []struct{ id, label string }{
		{"song", "Songs"}, {"album", "Albums"}, {"playlist", "Playlists"}, {"artist", "Artists"}, {"station", "Stations"},
	}
)

// hookTestShown is how long a row says how its test went.
const hookTestShown = 10 * time.Second

// hookSheet is the sheet that adds a webhook, or edits one.
type hookSheet struct {
	open bool
	// editing is the hook edited, "" when adding; kind is the service it
	// looks made for.
	editing, kind string
	// preset is the service whose fields show, "" for the list of them;
	// replace, when editing, points the hook at a service anew.
	preset  string
	replace bool
	name    string
	fields  map[string]*string
	choice  map[string]*int
	events  map[string]bool
	types   map[string]bool
	busy    bool
	since   time.Time
	err     string
}

// hookLook is the icon and tile color of a hook: what kind of service it
// calls, or a command.
func hookLook(k *Tokens, kind string, command bool) (*ui.SVG, ui.Color) {
	switch {
	case command:
		return icTerminal, k.TileSettings
	case kind == "notify":
		return icBell, k.TileDisk
	case kind == "library":
		return icSync, k.TileSystem
	}
	return icLink, k.TileQueue
}

// hookList is the Hooks section: the master switch, then each hook of
// hooks.yaml. A webhook is switched, tested, edited and deleted here; a
// command shows where it is edited, the file.
func (a *App) hookList(c *ui.Context, k *Tokens) {
	h := a.Store.Server.Hooks
	if h == nil {
		return
	}
	ui.Row(c).Gap(12).Padding(10, 12, 10, 16).Radius(radiusControl).Border(1, k.Line).AlignItems(ui.Center).Children(func() {
		on := h.Enabled
		toggle(c, k, "hooks-on", &on, "Run hooks")
		if on != h.Enabled {
			a.Store.SetHooksEnabled(on)
		}
		ui.Text(c, "Run hooks").FontSize(13).FontWeight(600).Grow(1)
		if len(h.Entries) > 0 && len(a.Store.Server.HookPresets) > 0 && button(c, k, buttonPlain, "Add hook", icPlus).Clicked() {
			a.openHookSheet(nil, "")
		}
	})
	if len(h.Entries) == 0 {
		if len(a.Store.Server.HookPresets) > 0 {
			card(c, k).Gap(14).Children(func() {
				a.presetGrid(c, k, func(id string) { a.openHookSheet(nil, id) })
			})
		}
		return
	}
	ui.Column(c.Key("hook-list")).Padding(4, padCard).Radius(radiusCard).Background(k.Raise).Dividers(1, k.Line).Children(func() {
		for i := range h.Entries {
			a.hookRow(c, k, h, &h.Entries[i])
		}
	})
}

func (a *App) hookRow(c *ui.Context, k *Tokens, h *client.Hooks, e *client.HookEntry) {
	command := e.Type != "webhook"
	p := a.hookPreset(e.Preset)
	ui.Row(c.Key("hook-" + e.Name)).Height(58).Gap(12).AlignItems(ui.Center).Children(func() {
		ic, tile := hookLook(k, p.Kind, command)
		ui.Box(c).Size(30, 30).Radius(radiusInner).Background(tile.Alpha(0.16)).Center().Children(func() {
			ui.Icon(c, ic).FontSize(15).TextColor(tile)
		})
		// The name keeps some room; the chips give way first.
		ui.Column(c).Grow(1).Shrink(1).MinWidth(120).Gap(3).Children(func() {
			name := ui.Text(c, e.Name).FontSize(13).FontWeight(600).SingleLine().Ellipsis("…")
			if !e.Enabled || !h.Enabled {
				name.TextColor(k.Faint)
			}
			sub, col := a.hookSubtitle(c, k, e, p, command)
			mono(c, sub, 11).TextColor(col).SingleLine().Ellipsis("…")
		})
		ui.Row(c).Gap(6).Shrink(4).MinWidth(0).Children(func() {
			// One chip for the events, so a narrow row cuts one label.
			var events []string
			for _, ev := range hookEvents {
				if slices.Contains(e.Events, ev.id) {
					events = append(events, ev.label)
				}
			}
			chip(c, k, strings.Join(events, " · "), ui.Color{}).Shrink(1).MinWidth(0)
			if t := typesLabel(e.JobTypes); t != "" {
				chip(c, k, t, ui.Color{}).Shrink(1).MinWidth(0)
			}
		})
		if runs := a.hookRuns(e.Name); runs != "" {
			mono(c, runs, 11.5).TextColor(k.Muted).SingleLine()
		}
		// One width for either kind of row, so the chips end in a line.
		ui.Row(c).Width(hookControls).Gap(10).AlignItems(ui.Center).Justify(ui.End).Children(func() {
			if command {
				ui.Row(c).Gap(5).AlignItems(ui.Center).Label("Edited in hooks.yaml").Children(func() {
					ui.Icon(c, icLock).FontSize(12).TextColor(k.Faint)
					mono(c, "hooks.yaml", 11).TextColor(k.Faint)
				})
				return
			}
			a.webhookControls(c, k, e)
		})
	})
}

// hookControls is the width of a row's controls: a webhook's Test, switch
// and menu.
const hookControls = 150

func (a *App) webhookControls(c *ui.Context, k *Tokens, e *client.HookEntry) {
	if button(c, k, buttonGhost, "Test", nil).Label("Send a test of " + e.Name).Clicked() {
		a.Store.TestHook(e.Name)
	}
	on := e.Enabled
	toggle(c, k, "on-"+e.Name, &on, e.Name+" on")
	if on != e.Enabled {
		a.Store.SwitchHook(e.Name, on)
	}
	more := ui.ButtonBase(c.Key("more-"+e.Name)).Size(28, 28).Radius(radiusControl).Center().Label("More actions for " + e.Name)
	col := k.Faint
	if more.Hovered() {
		more.Background(k.Track)
		col = k.Fg
	}
	name := e.Name
	entry := *e
	more.PopoverMenuButton(func(m *ui.Menu) {
		if m.Item("Edit…").Chosen() {
			a.openHookSheet(&entry, "")
		}
		m.Separator()
		if m.Item("Delete…").Chosen() {
			a.set.edit.confirm = &pendingChange{open: true, danger: true, verb: "Delete",
				title: "Delete “" + name + "”?",
				body:  "It leaves hooks.yaml and runs no more.",
				apply: func() { a.Store.DeleteHook(name) },
			}
		}
	})
	more.Cursor(ui.CursorPointer).Children(func() { ui.Icon(c, icMore).FontSize(16).TextColor(col) })
}

// hookSubtitle is the line under a hook's name: how its last test went for
// a while after it, else what it calls.
func (a *App) hookSubtitle(c *ui.Context, k *Tokens, e *client.HookEntry, p client.HookPreset, command bool) (string, ui.Color) {
	if t, ok := a.Store.HookTests[e.Name]; ok {
		switch {
		case t.Running && waited(c, true, t.At):
			return "Sending a test…", k.Muted
		case !t.Running && c.Now().Sub(t.At) < hookTestShown:
			c.After(hookTestShown - c.Now().Sub(t.At))
			if t.Succeeded {
				return "Test sent", k.OK
			}
			return "Test failed: " + t.Error, k.Bad
		}
	}
	if command {
		return "Command", k.Muted
	}
	host := strings.TrimPrefix(strings.TrimPrefix(e.Origin, "https://"), "http://")
	if strings.EqualFold(e.Name, p.Name) || p.Kind == "custom" {
		return host, k.Muted
	}
	return p.Name + " · " + host, k.Muted
}

// hookRuns is a hook's runs in the stats window, "" when it ran none.
func (a *App) hookRuns(name string) string {
	st := a.Store.Server.Stats
	if st == nil {
		return ""
	}
	for _, o := range st.Hooks {
		if o.Name == name && o.Succeeded+o.Failed > 0 {
			return fmt.Sprintf("%d ✓ · %d ✕", o.Succeeded, o.Failed)
		}
	}
	return ""
}

// findPreset is the backend's preset with id.
func (a *App) findPreset(id string) (client.HookPreset, bool) {
	ps := a.Store.Server.HookPresets
	if i := slices.IndexFunc(ps, func(p client.HookPreset) bool { return p.ID == id }); i >= 0 {
		return ps[i], true
	}
	return client.HookPreset{}, false
}

// hookPreset is the preset a webhook was built from, or Custom for one
// written by hand.
func (a *App) hookPreset(id string) client.HookPreset {
	if p, ok := a.findPreset(id); ok {
		return p
	}
	return client.HookPreset{ID: "custom", Name: "Custom", Kind: "custom"}
}

// typesLabel names the job types a hook is limited to, "" for all of them.
func typesLabel(types []string) string {
	if len(types) == 0 || len(types) == len(hookTypes) {
		return ""
	}
	var names []string
	for _, t := range hookTypes {
		if slices.Contains(types, t.id) {
			names = append(names, t.label)
		}
	}
	return strings.Join(names, ", ")
}

// presetGrid is the services a new hook can call, by kind.
func (a *App) presetGrid(c *ui.Context, k *Tokens, pick func(id string)) {
	groups := []struct {
		label string
		kind  string
	}{{"Notify", "notify"}, {"Refresh a library", "library"}, {"Other", "custom"}}
	ui.Column(c).Gap(14).Children(func() {
		for _, g := range groups {
			ui.Column(c).Gap(8).Children(func() {
				label(c, k, g.label)
				// Three a row, one width, so the rows line up.
				ui.Grid(c).Columns(3).Gap(8).Children(func() {
					for _, p := range a.Store.Server.HookPresets {
						if p.Kind != g.kind {
							continue
						}
						ic, tile := hookLook(k, p.Kind, false)
						b := ui.ButtonBase(c.Key("preset-"+p.ID)).Height(36).PaddingX(12).Gap(8).Radius(radiusControl).AlignItems(ui.Center).
							Background(k.Raise2).Border(1, k.Line2).Label(p.Name)
						if b.Hovered() {
							b.Background(k.Track)
						}
						id := p.ID
						if b.Clicked() {
							pick(id)
						}
						b.Cursor(ui.CursorPointer).Children(func() {
							ui.Icon(c, ic).FontSize(14).TextColor(tile)
							ui.Text(c, p.Name).FontSize(13).FontWeight(600).SingleLine()
						})
					}
				})
			})
		}
	})
}

// openHookSheet opens the sheet editing e, or adding a hook of preset
// ("" to pick one) when e is nil.
func (a *App) openHookSheet(e *client.HookEntry, preset string) {
	d := &hookSheet{open: true, fields: map[string]*string{}, choice: map[string]*int{}, events: map[string]bool{}, types: map[string]bool{}}
	if e != nil {
		d.editing, d.kind = e.Name, e.Preset
		for _, ev := range e.Events {
			d.events[ev] = true
		}
		for _, t := range e.JobTypes {
			d.types[t] = true
		}
	} else if preset != "" {
		a.choosePreset(d, preset)
	}
	a.hookAsk = d
}

// choosePreset shows preset's fields in d; a new hook takes its name and
// events.
func (a *App) choosePreset(d *hookSheet, id string) {
	p, _ := a.findPreset(id)
	d.preset, d.err = id, ""
	d.fields, d.choice = map[string]*string{}, map[string]*int{}
	for _, f := range p.Fields {
		d.fields[f.Key] = new(string)
		i := max(0, slices.Index(f.Choices, f.Default))
		d.choice[f.Key] = &i
	}
	if d.editing != "" {
		return
	}
	d.name = a.freeHookName(id)
	d.events = map[string]bool{}
	for _, ev := range p.Events {
		d.events[ev] = true
	}
}

// freeHookName is base, or base-2, base-3… when a hook has it.
func (a *App) freeHookName(base string) string {
	taken := func(n string) bool {
		h := a.Store.Server.Hooks
		return h != nil && slices.ContainsFunc(h.Entries, func(e client.HookEntry) bool { return e.Name == n })
	}
	name := base
	for i := 2; taken(name); i++ {
		name = fmt.Sprintf("%s-%d", base, i)
	}
	return name
}

// hookDialog is the sheet, while one is open.
func (a *App) hookDialog(c *ui.Context, k *Tokens) {
	d := a.hookAsk
	if d == nil {
		return
	}
	ui.DialogBase(c.Key("hook-sheet"), &d.open, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.Width(480).Padding(padCard+4).Gap(18).Radius(radiusCard).Background(k.Raise).Border(1, k.Line2).Shadow(0, 18, 48, 0, ui.RGBA(0, 0, 0, 0.4))
		picking := d.preset == "" && (d.editing == "" || d.replace)
		ui.Row(c).Height(28).Gap(6).AlignItems(ui.Center).Children(func() {
			if d.preset != "" && (d.editing == "" || d.replace) || picking && d.replace {
				b := ui.ButtonBase(c.Key("hook-back")).Size(28, 28).Radius(radiusControl).Center().Label("Back")
				if b.Hovered() {
					b.Background(k.Track)
				}
				if b.Clicked() {
					if picking {
						d.replace = false
					}
					d.preset, d.err = "", ""
				}
				b.Cursor(ui.CursorPointer).Children(func() { ui.Icon(c, icBack).FontSize(14).TextColor(k.Muted) })
			}
			ui.Text(c, a.hookSheetTitle(d)).FontSize(15).FontWeight(650).SingleLine().Ellipsis("…").Shrink(1)
		})
		if picking {
			a.presetGrid(c, k, func(id string) { a.choosePreset(d, id) })
		} else {
			a.hookForm(c, k, d)
		}
		ui.Row(c).Gap(8).AlignItems(ui.Center).Padding(2, 0, 0, 0).Children(func() {
			// The reason a save failed sits beside the buttons, so it
			// grows nothing.
			ui.Text(c, d.err).FontSize(12).TextColor(k.Bad).MaxLines(2).Ellipsis("…").Grow(1).Shrink(1).MinWidth(0)
			if button(c, k, buttonPlain, "Cancel", nil).Clicked() {
				d.open = false
			}
			if picking {
				return
			}
			verb := map[bool]string{true: "Add", false: "Save"}[d.editing == ""]
			slow := waited(c, d.busy, d.since)
			if slow {
				verb = map[bool]string{true: "Adding…", false: "Saving…"}[d.editing == ""]
			}
			if buttonOff(c, k, buttonPrimary, verb, nil, slow).Clicked() {
				a.saveHook(d)
			}
		})
	})
	if !d.open {
		a.hookAsk = nil
	}
}

func (a *App) hookSheetTitle(d *hookSheet) string {
	if d.editing != "" {
		return d.editing
	}
	if p, ok := a.findPreset(d.preset); ok {
		return p.Name
	}
	return "Add hook"
}

// hookForm is the sheet's fields: the name of a new hook, the service's
// values, when the hook runs and for which downloads.
func (a *App) hookForm(c *ui.Context, k *Tokens, d *hookSheet) {
	formLabel := func(s string) { ui.Text(c, s).FontSize(12).FontWeight(600).TextColor(k.Muted) }
	ui.Column(c).Gap(14).Children(func() {
		if d.editing == "" {
			ui.Column(c).Gap(6).Children(func() {
				formLabel("Name")
				field(c, k, "hook-name", &d.name, "", true)
			})
		}
		if d.editing != "" && !d.replace {
			p := a.hookPreset(d.kind)
			e := a.hookEntry(d.editing)
			ui.Row(c).Gap(10).Padding(8, 8, 8, 12).Radius(radiusControl).Background(k.Raise2).Border(1, k.Line).AlignItems(ui.Center).Children(func() {
				ic, tile := hookLook(k, p.Kind, false)
				ui.Icon(c, ic).FontSize(14).TextColor(tile)
				mono(c, p.Name+" · "+strings.TrimPrefix(strings.TrimPrefix(e.Origin, "https://"), "http://"), 12).SingleLine().Ellipsis("…").Grow(1).Shrink(1).MinWidth(0)
				if button(c, k, buttonGhost, "Replace…", nil).Clicked() {
					d.replace, d.preset = true, ""
				}
			})
		} else if p, ok := a.findPreset(d.preset); ok {
			for _, f := range p.Fields {
				ui.Column(c).Gap(6).Children(func() {
					formLabel(f.Label)
					key := "hook-" + p.ID + "-" + f.Key
					placeholder := f.Placeholder
					if placeholder == "" {
						placeholder = f.Default
					}
					switch {
					case len(f.Choices) > 0:
						ui.Row(c).Children(func() { segmented(c, k, key, d.choice[f.Key], f.Choices, nil) })
					case f.Multiline:
						box := ui.Column(c).Height(92).Padding(8, 10).Radius(radiusControl).Background(k.Raise2).Border(1, k.Line2)
						box.Children(func() {
							in := ui.TextAreaBase(c.Key(key), d.fields[f.Key]).Grow(1).Placeholder(placeholder).Label(f.Label).FocusRing(false).Font(MonoFont).FontSize(12)
							if in.Focused() {
								focusRing(box, radiusControl)
							}
						})
					default:
						in := field(c, k, key, d.fields[f.Key], placeholder, true)
						if f.Secret {
							in.Password()
						}
					}
				})
			}
		}
		ui.Column(c).Gap(8).Children(func() {
			formLabel("Runs when a download")
			ui.Row(c).Gap(6).Wrap().Children(func() {
				for _, ev := range hookEvents {
					if choiceChip(c, k, "ev-"+ev.id, ev.label, d.events[ev.id]) {
						on := !d.events[ev.id]
						// A queued download is a hook of its own: the
						// backend runs one entry once per download.
						if on && ev.id == "job_queued" || d.events["job_queued"] {
							d.events = map[string]bool{}
						}
						d.events[ev.id] = on
					}
				}
			})
		})
		ui.Column(c).Gap(8).Children(func() {
			formLabel("For")
			ui.Row(c).Gap(6).Wrap().Children(func() {
				all := len(d.types) == 0
				if choiceChip(c, k, "type-all", "All", all) {
					d.types = map[string]bool{}
				}
				for _, t := range hookTypes {
					if choiceChip(c, k, "type-"+t.id, t.label, d.types[t.id]) {
						if d.types[t.id] {
							delete(d.types, t.id)
						} else {
							d.types[t.id] = true
						}
						if len(d.types) == len(hookTypes) {
							d.types = map[string]bool{}
						}
					}
				}
			})
		})
	})
}

func (a *App) hookEntry(name string) client.HookEntry {
	if h := a.Store.Server.Hooks; h != nil {
		if i := slices.IndexFunc(h.Entries, func(e client.HookEntry) bool { return e.Name == name }); i >= 0 {
			return h.Entries[i]
		}
	}
	return client.HookEntry{Name: name}
}

// choiceChip is one choice of several that can be on together; it reports
// a click.
func choiceChip(c *ui.Context, k *Tokens, key, text string, on bool) bool {
	b := ui.ButtonBase(c.Key(key)).Height(28).PaddingX(12).Radius(radiusFull).AlignItems(ui.Center).Label(text)
	fg, weight := k.Fg, 500
	switch {
	case on:
		b.Background(k.Pill)
		fg, weight = k.PillInk, 600
	case b.Hovered():
		b.Background(k.Track).Border(1, k.Line2)
	default:
		b.Background(k.Raise2).Border(1, k.Line2)
	}
	b.Cursor(ui.CursorPointer).Children(func() {
		// The bold label keeps the chip's width either way.
		ui.Box(c).Children(func() {
			ui.Text(c, text).FontSize(12.5).FontWeight(600).SingleLine().Invisible()
			ui.Text(c, text).FontSize(12.5).FontWeight(weight).TextColor(fg).SingleLine().TextAlign(ui.Center).Absolute().Top(0).Left(0).Right(0)
		})
	})
	return b.Clicked()
}

// saveHook sends the sheet: events and job types, and the service's
// request when the hook is new or pointed anew.
func (a *App) saveHook(d *hookSheet) {
	if d.busy {
		return
	}
	name := d.editing
	if name == "" {
		name = strings.TrimSpace(d.name)
		switch {
		case name == "":
			d.err = "Name is needed"
			return
		case a.freeHookName(name) != name:
			d.err = "A hook is named " + name
			return
		}
	}
	var w client.HookWrite
	for _, ev := range hookEvents {
		if d.events[ev.id] {
			w.Events = append(w.Events, ev.id)
		}
	}
	if len(w.Events) == 0 {
		d.err = "Pick when it runs"
		return
	}
	types := []string{}
	for _, t := range hookTypes {
		if d.types[t.id] {
			types = append(types, t.id)
		}
	}
	w.JobTypes = &types
	if d.editing == "" || d.replace {
		p, ok := a.findPreset(d.preset)
		if !ok {
			d.err = "Pick a service"
			return
		}
		// The backend builds the request from the values and says what is
		// missing.
		w.Preset, w.Values = p.ID, map[string]string{}
		for _, f := range p.Fields {
			w.Values[f.Key] = *d.fields[f.Key]
			if len(f.Choices) > 0 {
				w.Values[f.Key] = f.Choices[*d.choice[f.Key]]
			}
		}
	}
	d.busy, d.since, d.err = true, time.Now(), ""
	a.Store.PutHook(name, w, func(reason string) {
		d.busy = false
		if reason != "" {
			d.err = reason
			return
		}
		d.open = false
	})
}
