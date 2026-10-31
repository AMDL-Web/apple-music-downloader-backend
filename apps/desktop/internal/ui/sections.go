package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/store"
	"amdl/internal/format"
)

// backendSection is one of the backend's sections in Settings: what was
// fixed at startup, its fields and its actions. Its state is its tile on
// the status board above.
func (a *App) backendSection(c *ui.Context, k *Tokens, id string) {
	s := a.Store
	sv := &s.Server
	switch id {
	case "account":
		a.accountSession(c, k)
	case "hooks":
		a.hookList(c, k)
	}
	a.facts(c, k, id)
	a.sectionFields(c, k, id)
	switch id {
	case "library_sync":
		if l := sv.Sync; l != nil && l.AnchorSize > 0 {
			actionRow(c, k, "Forget the albums seen so far and watch from now on", "Start over from now…", func() {
				a.set.edit.confirm = &pendingChange{open: true,
					title: "Start library sync over from now?", verb: "Start over", danger: true,
					body:  "Albums you added before now won't be downloaded.",
					apply: a.Store.ResetLibrarySync,
				}
			})
		}
	case "server":
		a.configFile(c, k)
	}
}

// sectionFields are the section's fields: those of no group first, then a
// card per group under its name.
func (a *App) sectionFields(c *ui.Context, k *Tokens, id string) {
	cf := &a.Store.Config
	fields := fieldsOf(id)
	if len(fields) == 0 {
		return
	}
	if cf.Values == nil {
		if cf.Err != "" {
			ui.Text(c, "The settings could not be read: "+cf.Err).FontSize(12.5).TextColor(k.Bad)
			return
		}
		card(c, k).Gap(12).Children(func() {
			for i := range min(len(fields), 6) {
				el := ui.Box(c.Key(i)).Height(14).WidthPercent(float32(40 + 9*(i%3))).Radius(radiusInner).Background(k.Track)
				el.Opacity(0.55 + 0.45*el.Loop("sk", 1400*time.Millisecond, ui.Bounce(ui.EaseInOut)))
			}
		})
		return
	}
	var groups []string
	for _, f := range fields {
		if f.group != "" && !containsString(groups, f.group) {
			groups = append(groups, f.group)
		}
	}
	for _, g := range append([]string{""}, groups...) {
		var in []*configField
		for _, f := range fields {
			if f.group == g {
				in = append(in, f)
			}
		}
		if len(in) == 0 {
			continue
		}
		if g != "" {
			label(c, k, g).Padding(6, 0, 0, 0)
		}
		ui.Column(c.Key("fields-"+g)).Padding(4, padCard).Radius(radiusCard).Background(k.Raise).Dividers(1, k.Line).Children(func() {
			for _, f := range in {
				a.fieldRow(c, k, f)
			}
		})
	}
}

func containsString(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// fact is a value fixed while the backend runs: from its config.yaml,
// built in, or neither, as its version.
type fact struct {
	label, value string
	from         factSource
	mono         bool
}

type factSource int

const (
	fromConfig factSource = iota
	fromBuild
	fromRun
)

// facts are the section's startup values. A value the backend did not
// report is left out rather than shown as a dash.
func (a *App) facts(c *ui.Context, k *Tokens, id string) {
	sv := &a.Store.Server
	sys, st := sv.System, sv.Startup
	var fs []fact
	add := func(f fact) {
		if f.value != "" {
			fs = append(fs, f)
		}
	}
	switch id {
	case "account":
		if sys != nil {
			add(fact{label: "Default storefront", value: strings.ToUpper(sys.DefaultStorefront)})
		}
	case "jobs":
		if sys != nil {
			add(fact{label: "Jobs at once", value: fmt.Sprint(sys.Jobs.Workers)})
			add(fact{label: "Parallel track downloads", value: fmt.Sprint(sys.Pools.Downloads)})
			add(fact{label: "Decrypt slots", value: fmt.Sprint(sys.Pools.Decrypts), from: fromBuild})
			add(fact{label: "Wrapper requests at once", value: fmt.Sprint(sys.Pools.WrapperRequests), from: fromBuild})
		}
	case "logging":
		if st != nil {
			add(fact{label: "Log entries kept", value: fmt.Sprint(st.LogBufferSize)})
		}
	case "server":
		if sys != nil {
			add(fact{label: "Version", value: sys.Version + " · " + shortCommit(sys.Commit), from: fromRun, mono: true})
			add(fact{label: "Started", value: sys.StartedAt.Local().Format("2 Jan 2006 15:04"), from: fromRun})
			for _, t := range sys.Tools {
				if t.Available {
					add(fact{label: t.Name, value: t.Path, mono: true})
				} else {
					add(fact{label: t.Name, value: "Missing"})
				}
			}
		}
		if st != nil {
			add(fact{label: "Listen address", value: st.Listen, mono: true})
			add(fact{label: "Database", value: st.DatabasePath, mono: true})
			add(fact{label: "Wrapper address", value: st.WrapperAddress, mono: true})
			add(fact{label: "Catalog language", value: st.CatalogLanguage})
			add(fact{label: "Apple requests at once", value: fmt.Sprint(st.MaxParallelRequests)})
		}
	}
	if len(fs) == 0 {
		return
	}
	ui.Column(c.Key("facts")).Padding(4, padCard).Radius(radiusCard).Background(k.Raise).Dividers(1, k.Line).Children(func() {
		for _, f := range fs {
			ui.Row(c.Key(f.label)).Height(40).Gap(12).AlignItems(ui.Center).Children(func() {
				// The value takes the room the label leaves, so a long path
				// shortens instead of pushing the label out.
				ui.Text(c, f.label).FontSize(13).FontWeight(500).Shrink(0).SingleLine()
				v := ui.Text(c, f.value).FontSize(12.5).TextColor(k.Muted).Grow(1).Basis(0).MinWidth(0).TextAlign(ui.End).SingleLine().Ellipsis("…")
				if f.mono {
					v.Font(MonoFont).FontSize(11.5)
				}
				switch f.from {
				case fromBuild:
					chip(c, k, "Built in", ui.Color{}).Shrink(0)
				case fromConfig:
					ui.Row(c).Shrink(0).Gap(4).AlignItems(ui.Center).Tooltip("Set in config.yaml; restart to change").Children(func() {
						ui.Icon(c, icLock).FontSize(11).TextColor(k.Faint)
						mono(c, "config.yaml", 10.5).TextColor(k.Faint)
					})
				}
			})
		}
	})
}

// folderState is the free space of a saved folder, or why it cannot be
// read.
func (a *App) folderState(c *ui.Context, k *Tokens, key string) {
	sv := &a.Store.Server
	st := sv.DownloadsStorage()
	if key == "download.temp_dir" {
		st = sv.TempStorage()
	}
	ui.Row(c).Height(16).Children(func() {
		switch {
		case st == nil:
		case st.Error != "":
			ui.Text(c, "Can't read this folder: "+st.Error).FontSize(11.5).TextColor(k.Warn).SingleLine().Ellipsis("…")
		case st.TotalBytes > 0:
			ui.Text(c, format.Bytes(st.FreeBytes)+" free of "+format.Bytes(st.TotalBytes)).FontSize(11.5).TextColor(k.Faint)
		}
	})
}

// actionRow is an action that is not a setting, beside what it does.
func actionRow(c *ui.Context, k *Tokens, what, verb string, do func()) {
	ui.Row(c).Padding(padCard).Gap(12).Radius(radiusCard).Background(k.Raise).AlignItems(ui.Center).Children(func() {
		ui.Text(c, what).FontSize(13).Grow(1).Shrink(1)
		if button(c, k, buttonPlain, verb, nil).Clicked() {
			do()
		}
	})
}

// configFile is Server's Configuration file group: whether changes reach
// config.yaml, and reading it again after a hand edit.
func (a *App) configFile(c *ui.Context, k *Tokens) {
	cf := &a.Store.Config
	if cf.Values == nil {
		return
	}
	label(c, k, "Configuration file").Padding(6, 0, 0, 0)
	ui.Column(c).Padding(padCard).Gap(10).Radius(radiusCard).Background(k.Raise).Children(func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(3).Children(func() {
				ui.Text(c, "Reload from file").FontSize(13).FontWeight(600)
				saved := "Changes here are saved to config.yaml."
				if !cf.Persisted {
					saved = "This backend keeps no config.yaml: changes last until it restarts."
				}
				ui.Text(c, saved).FontSize(11.5).TextColor(k.Faint)
			})
			wait := waited(c, cf.Reloading, cf.ReloadSince)
			text := "Reload"
			if wait {
				text = "Reloading…"
			}
			if buttonOff(c, k, buttonPlain, text, nil, wait).Clicked() && !cf.Reloading {
				a.Store.ReloadConfig()
			}
		})
		if cf.ReloadErr != "" {
			ui.Text(c, "config.yaml was not used: "+cf.ReloadErr).FontSize(12).TextColor(k.Bad).LineHeight(1.45)
		}
	})
}

// logsCard is the backend's errors of the last day, by hour.
func logsCard(c *ui.Context, k *Tokens, sv *store.Server, span int) ui.Element {
	e := sv.Errors
	return moleCard(c, k, "logs", span, k.TileSettings, icClock, "Logs", ui.Color{}, []string{"24 h"}, func() {
		switch {
		case e == nil:
			bigValue(c, k, "—", "", 34)
			cardFoot(c, k, "The backend did not say")
			return
		case e.Off:
			display(c, "Off", 24)
			cardFoot(c, k, "logging.buffer_size is 0 · config.yaml")
			return
		}
		n := fmt.Sprint(len(e.Times))
		if e.Full {
			n += "+"
		}
		if len(e.Times) == 0 {
			bigValue(c, k, "No errors", "", 34)
		} else {
			bigValue(c, k, n, "errors", 34)
		}
		var per [24]float64
		top := 1.0
		now := time.Now()
		for _, t := range e.Times {
			if h := 23 - int(now.Sub(t).Hours()); h >= 0 && h < 24 {
				per[h]++
				top = max(top, per[h])
			}
		}
		vals := make([]float64, 24)
		for i := range per {
			vals[i] = per[i] / top
		}
		bars(c, k, vals, 24, k.Bad)
		if e.Last != "" {
			cardFoot(c, k, "Last: "+e.Last)
		} else {
			cardFoot(c, k, "Nothing went wrong in the last day")
		}
	})
}

// codecName is a codec of download.quality_priority in a word.
func codecName(v string) string {
	switch v {
	case "alac":
		return "ALAC"
	case "ec3":
		return "Atmos"
	case "ac3":
		return "AC-3"
	case "aac":
		return "AAC"
	case "aac-binaural":
		return "AAC binaural"
	case "aac-downmix":
		return "AAC downmix"
	}
	return v
}
