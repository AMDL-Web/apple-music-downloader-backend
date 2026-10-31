package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/store"
	"amdl/internal/client"
	"amdl/internal/format"
)

// statusBoard is Settings' first page: one tile per domain of the backend,
// each opening its section, as the web console's board.
func (a *App) statusBoard(c *ui.Context, k *Tokens, width float32) {
	sv := &a.Store.Server
	cols := 2
	if width >= 960 {
		cols = 4
	}
	ui.Grid(c).Columns(cols).Gap(14).Children(func() {
		if sv.Read.IsZero() {
			// Grid cells size themselves: skeleton's full width would
			// overflow them.
			for i := range 8 {
				el := ui.Box(c.Key(i)).Height(148).Radius(radiusCard).Background(k.Track)
				el.Opacity(0.55 + 0.45*el.Loop("sk", 1400*time.Millisecond, ui.Bounce(ui.EaseInOut)))
			}
			return
		}
		wide := map[string]int{"server": 2, "account": 2, "hooks": 2, "logging": 2}
		for _, id := range []string{"server", "account", "quality", "files", "jobs", "library_sync", "hooks", "logging"} {
			span := 1
			switch {
			case cols == 4:
				span = max(1, wide[id])
			case width < 780 && (id == "server" || id == "account"):
				// Side by side, the server's word would be cut short.
				span = 2
			}
			el := a.domainCard(c, k, id, span)
			el.Cursor(ui.CursorPointer).OnClick(func() { a.showSection(id) })
			if el.Hovered() {
				el.Background(k.Raise2)
			}
		}
	})
}

// domainCard is a domain's state: a tile of the board, and the head of its
// section.
func (a *App) domainCard(c *ui.Context, k *Tokens, id string, span int) ui.Element {
	sv := &a.Store.Server
	switch id {
	case "server":
		return a.healthCard(c, k, sv, span)
	case "account":
		return a.accountCard(c, k, sv, span)
	case "quality":
		return a.downloadedCard(c, k, sv, span)
	case "files":
		return a.filesCard(c, k, sv, span)
	case "jobs":
		return a.workersCard(c, k, sv, span)
	case "library_sync":
		return syncCard(c, k, sv, span)
	case "hooks":
		return hooksCard(c, k, sv, span)
	case "logging":
		return logsCard(c, k, sv, span)
	}
	return ui.Element{}
}

// moleCard is a card of the system screen: a colored icon square, the
// title, chips at the right, then body.
func moleCard(c *ui.Context, k *Tokens, key string, span int, tile ui.Color, ic *ui.SVG, title string, titleColor ui.Color, chips []string, body func()) ui.Element {
	el := card(c.Key(key), k).Label(title)
	if span > 1 {
		el.ColumnSpan(span)
	}
	if titleColor.A == 0 {
		titleColor = k.Muted
	}
	el.Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Size(18, 18).Radius(radiusInner).Background(tile).Center().Children(func() {
				ui.Icon(c, ic).FontSize(11).TextColor(ui.RGB(255, 255, 255))
			})
			// The title keeps its words; the chips give way when the
			// tile is narrow.
			ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(titleColor).SingleLine().Shrink(0)
			ui.Spacer(c)
			for _, ch := range chips {
				if ch != "" {
					chip(c, k, ch, ui.Color{}).Shrink(1).MinWidth(0)
				}
			}
		})
		body()
	})
	return el
}

// cardFoot is a card's last lines of detail, two at most.
func cardFoot(c *ui.Context, k *Tokens, s string) {
	ui.Text(c, s).Font(MonoFont).FontSize(11.5).FontWeight(500).LineHeight(1.45).TextColor(k.Muted).MaxLines(2).Ellipsis("…")
}

func (a *App) healthCard(c *ui.Context, k *Tokens, sv *store.Server, span int) ui.Element {
	word, col, glow := "All good", k.OK, k.Accent
	var problems []string
	switch {
	case sv.Health == nil:
		word, col, glow = "Unreachable", k.Bad, k.Bad
		problems = append(problems, sv.HealthErr)
	default:
		if sv.Health.DatabaseError != "" {
			problems = append(problems, "database: "+sv.Health.DatabaseError)
		}
		if p := accountState(sv.Wrapper, sv.WrapperErr).problem; p != "" {
			problems = append(problems, p)
		}
		if sv.System != nil {
			for _, t := range sv.System.Tools {
				if !t.Available {
					problems = append(problems, t.Name+" is missing")
				}
			}
		}
		if sv.Health.Status != "ok" || len(problems) > 0 {
			word, col, glow = "Needs a look", k.Warn, k.Warn
		}
	}
	var chips []string
	foot := "Database, wrapper and ffmpeg all answer"
	if sv.System != nil {
		chips = []string{sv.System.Version, shortCommit(sv.System.Commit)}
		foot += " · up " + uptime(sv.System.UptimeSeconds)
	}
	if len(problems) > 0 {
		foot = strings.Join(problems, " · ")
	}
	return moleCard(c, k, "health", span, k.TileSystem, icCheck, "Server", col, chips, func() {
		ui.Row(c).Gap(18).AlignItems(ui.Center).Children(func() {
			display(c, word, 44).SingleLine()
			ui.Spacer(c)
			planet(c, k, glow)
		})
		cardFoot(c, k, foot)
	})
}

// planet is the health card's glowing sphere, lit from the top left in
// the state's color. It breathes slowly unless the system asks for less
// motion.
func planet(c *ui.Context, k *Tokens, glow ui.Color) {
	deep := ui.Hex("#05104b")
	p := ui.Box(c).Size(92, 92).Radius(radiusFull)
	breath := float32(0.5)
	if !c.Preferences().ReduceMotion {
		breath = p.Loop("breath", 5*time.Second, ui.Bounce(ui.EaseInOut))
	}
	p.Shadow(0, 0, 32+16*breath, 0, glow.Alpha(0.25+0.15*breath))
	p.LinearGradient(ui.LinearGradient{Angle: 135, Stops: []ui.GradientStop{
		{Color: ui.Hex("#f7ebf6"), At: 0},
		{Color: glow, At: 0.3},
		{Color: glow.Mix(deep, 0.7), At: 0.75},
		{Color: deep, At: 1},
	}})
	p.InsetShadow(0, 0, 0, 1, ui.RGBA(255, 255, 255, 0.08))
	if !k.Dark {
		p.Border(1, k.Line)
	}
	// The light's reflection, high on the left.
	p.Clip().Children(func() {
		ui.Box(c).Size(34, 30).Radius(radiusFull).Background(ui.RGBA(255, 255, 255, 0.55)).Blur(9).Absolute().Left(16).Top(12)
	})
}

func (a *App) workersCard(c *ui.Context, k *Tokens, sv *store.Server, span int) ui.Element {
	var chips []string
	if sv.System != nil {
		chips = []string{fmt.Sprintf("queue %d / %d", sv.System.Jobs.QueueDepth, sv.System.Jobs.QueueCapacity)}
	}
	return moleCard(c, k, "workers", span, k.TileQueue, icQueue, "Jobs", ui.Color{}, chips, func() {
		if sv.System == nil {
			bigValue(c, k, "—", "", 34)
			cardFoot(c, k, "The backend did not say")
			return
		}
		j := sv.System.Jobs
		bigValue(c, k, fmt.Sprint(j.Active), fmt.Sprintf("of %d busy", j.Workers), 34)
		vals := make([]float64, len(sv.Active))
		for i, v := range sv.Active {
			vals[i] = float64(v) / float64(max(j.Workers, 1))
		}
		bars(c, k, vals, 12, k.Accent)
		p := sv.System.Pools
		cardFoot(c, k, fmt.Sprintf("Pools %d download · %d decrypt · %d wrapper", p.Downloads, p.Decrypts, p.WrapperRequests))
	})
}

func (a *App) downloadedCard(c *ui.Context, k *Tokens, sv *store.Server, span int) ui.Element {
	chips := []string{fmt.Sprintf("%d days", store.StatsDays)}
	if first := strs(a.Store.Config.Value("download.quality_priority")); len(first) > 0 {
		chips = append([]string{codecName(first[0]) + " first"}, chips...)
	}
	return moleCard(c, k, "downloaded", span, k.TileLibrary, icNote, "Quality", ui.Color{}, chips, func() {
		if sv.Stats == nil {
			bigValue(c, k, "—", "", 34)
			cardFoot(c, k, "This backend keeps no statistics")
			return
		}
		tracks, failed, written := 0, 0, int64(0)
		top := 1
		for _, d := range sv.Stats.Daily {
			tracks += d.TracksCompleted
			failed += d.JobsFailed
			written += d.BytesWritten
			top = max(top, d.TracksCompleted)
		}
		bigValue(c, k, fmt.Sprint(tracks), "tracks", 34)
		vals := make([]float64, len(sv.Stats.Daily))
		for i, d := range sv.Stats.Daily {
			vals[i] = float64(d.TracksCompleted) / float64(top)
		}
		bars(c, k, vals, len(vals), k.TileLibrary)
		foot := format.Bytes(written) + " written"
		if failed > 0 {
			foot += " · " + plural(failed, "job") + " failed"
		}
		cardFoot(c, k, foot)
	})
}

// bars is a card's small column chart of vals (0..1), oldest first, in
// slots columns; slots without a value yet stay empty on the left.
func bars(c *ui.Context, k *Tokens, vals []float64, slots int, col ui.Color) {
	ui.Row(c).Height(26).Gap(3).AlignItems(ui.End).Children(func() {
		for i := range slots {
			v, empty := 0.0, true
			if j := i - (slots - len(vals)); j >= 0 {
				v, empty = vals[j], false
			}
			b := ui.Box(c.Key(i)).Grow(1).Radius(radiusInner).Height(max(2, float32(v)*26))
			if empty || v == 0 {
				b.Background(k.Track)
			} else {
				b.Background(col.Alpha(0.85))
			}
		}
	})
}

// filesCard is the free space of the downloads folder's disk, and of the
// temporary folder's when it says another.
func (a *App) filesCard(c *ui.Context, k *Tokens, sv *store.Server, span int) ui.Element {
	st := sv.DownloadsStorage()
	var total string
	if st != nil && st.TotalBytes > 0 {
		total = format.Bytes(st.TotalBytes)
	}
	return moleCard(c, k, "files", span, k.TileDisk, icDisk, "Storage & files", ui.Color{}, []string{total}, func() {
		if st == nil || st.Error != "" || st.TotalBytes == 0 {
			bigValue(c, k, "—", "", 34)
			why := "The backend did not say"
			if st != nil && st.Error != "" {
				why = st.Error
			}
			cardFoot(c, k, why)
			return
		}
		v, unit := format.Scale(float64(st.FreeBytes))
		bigValue(c, k, format.Number(v, 2), unit+" free", 34)
		used := 1 - float64(st.FreeBytes)/float64(st.TotalBytes)
		meter(c, k, used, 8)
		foot := fmt.Sprintf("%s · %d%% used", st.Path, int(used*100+0.5))
		if t := sv.TempStorage(); t != nil && t.Error == "" && (t.TotalBytes != st.TotalBytes || t.FreeBytes != st.FreeBytes) {
			foot += " · temporary folder " + format.Bytes(t.FreeBytes) + " free"
		}
		cardFoot(c, k, foot)
	})
}

// meter is a bar of free space, filled to used (0..1) in the disk tiles'
// gold.
func meter(c *ui.Context, k *Tokens, used float64, h float32) {
	used = min(max(used, 0), 1)
	ui.Row(c).Height(h).Radius(radiusFull).Background(k.Track).Clip().Children(func() {
		// Rows do not stretch their children's height: each part sets it.
		if used > 0 {
			ui.Box(c).Grow(float32(used)).Height(h).Radius(radiusFull).LinearGradient(ui.LinearGradient{Angle: 90, Stops: []ui.GradientStop{
				{Color: ui.Hex("#8a6a3e"), At: 0}, {Color: ui.Hex("#e2c08a"), At: 1},
			}})
		}
		if used < 1 {
			ui.Box(c).Grow(float32(1 - used)).Height(h)
		}
	})
}

// account is the Apple Music account's state in words: what the Account
// card shows and what Health lists as a problem.
type account struct {
	word, state string
	kind        pillKind
	icon        *ui.SVG
	// note is the next step, or what went wrong.
	note string
	// problem is Health's line; empty when downloads can decrypt.
	problem string
	// signIn means an account can log in through the API now.
	signIn bool
}

// accountState reads the wrapper's status. A wrapper amdl runs on the
// backend's machine says first whether its guest runs at all: while it boots,
// logs in, is stopped or failed, the guest cannot answer and the status reads
// not ready and signed out. Health's line is client.WrapperStatus.Problem's,
// as the other clients say it.
func accountState(w *client.WrapperStatus, err string) account {
	if w == nil {
		return account{word: "Unreachable", state: "No answer", kind: pillBad, icon: icCross, note: err, problem: "the wrapper does not answer"}
	}
	st := wrapperWords(w)
	st.problem, _ = w.Problem()
	return st
}

func wrapperWords(w *client.WrapperStatus) account {
	if m := w.Managed; m != nil {
		switch m.Mode {
		case "starting":
			return account{word: "Starting", state: "Booting", kind: pillWait, note: "The local wrapper boots in about 15 seconds"}
		case "login":
			return account{word: "Logging in", state: "Logging in", kind: pillWait, note: "The local wrapper restarts with the account; this takes about half a minute"}
		case "stopped":
			return account{word: "Stopped", state: "Not running", kind: pillWarn, icon: icWarn, note: "Start it with amdl wrapper start"}
		case "failed":
			note := "See amdl wrapper logs"
			if m.LastError != "" {
				note = m.LastError + " · amdl wrapper logs"
			}
			return account{word: "Failed", state: "Not running", kind: pillBad, icon: icCross, note: note}
		}
	}
	switch {
	case !w.Ready:
		return account{word: "Starting", state: "Not ready", kind: pillWait, note: "The wrapper answers but is not ready yet"}
	case !w.LoggedIn:
		note := "Log in on the wrapper's own machine"
		if w.LoginSupported {
			note = "Log in so downloads can be decrypted"
		}
		return account{word: "Signed out", state: "No account", kind: pillWarn, icon: icWarn, note: note, signIn: w.LoginSupported}
	}
	note := "Signed in"
	if len(w.Regions) > 0 {
		note = "Storefronts " + strings.ToUpper(strings.Join(w.Regions, ", "))
	}
	return account{word: "Signed in", state: "Ready", kind: pillOK, icon: icCheck, note: note}
}

// wrapperKind names the wrapper: one amdl runs here, or wrapper-manager or
// wrapper-lite on another machine; only the first two log in over the API.
func wrapperKind(w *client.WrapperStatus) string {
	switch {
	case w == nil:
		return ""
	case w.Managed != nil:
		if w.Managed.AccelFallback || w.Managed.Accel == "tcg" {
			return "local wrapper · emulated"
		}
		return "local wrapper"
	case w.LoginSupported:
		return "wrapper-manager"
	}
	return "wrapper-lite"
}

func (a *App) accountCard(c *ui.Context, k *Tokens, sv *store.Server, span int) ui.Element {
	w := sv.Wrapper
	st := accountState(w, sv.WrapperErr)
	return moleCard(c, k, "account", span, k.TileSettings, icPerson, "Account", ui.Color{}, []string{wrapperKind(w)}, func() {
		ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
			display(c, st.word, 24).SingleLine()
			pill(c, k, st.kind, st.icon, st.state)
		})
		if w != nil && len(w.Accounts) > 0 {
			ui.Row(c).Gap(8).Wrap().Children(func() {
				for _, acc := range w.Accounts {
					ui.Row(c.Key(acc)).Height(28).PaddingX(10).Gap(6).Radius(radiusControl).Background(k.Raise2).Border(1, k.Line2).AlignItems(ui.Center).Children(func() {
						ui.Icon(c, icPerson).FontSize(12).TextColor(k.Faint)
						mono(c, acc, 12)
					})
				}
			})
		}
		cardFoot(c, k, st.note)
	})
}

func syncCard(c *ui.Context, k *Tokens, sv *store.Server, span int) ui.Element {
	l := sv.Sync
	var chips []string
	if l != nil && l.IntervalMinutes > 0 {
		chips = []string{fmt.Sprintf("%d min", l.IntervalMinutes)}
	}
	return moleCard(c, k, "sync", span, k.TileSystem, icSync, "Library sync", ui.Color{}, chips, func() {
		if l == nil {
			display(c, "—", 24)
			cardFoot(c, k, "This backend has no library sync")
			return
		}
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			if l.Enabled {
				display(c, "On", 24)
				ui.Spacer(c)
				pill(c, k, pillOK, icCheck, "Watching")
			} else {
				display(c, "Off", 24)
				ui.Spacer(c)
				pill(c, k, pillWait, nil, "Off in config")
			}
		})
		switch {
		case l.LastError != "":
			cardFoot(c, k, l.LastError)
		case l.LastPollAt == nil:
			cardFoot(c, k, "Not polled yet")
		case l.LastSubmitted == 0:
			cardFoot(c, k, "Last pass "+format.Clock(*l.LastPollAt)+" · nothing new")
		default:
			cardFoot(c, k, "Last pass "+format.Clock(*l.LastPollAt)+" · "+plural(l.LastSubmitted, "album")+" queued")
		}
	})
}

func hooksCard(c *ui.Context, k *Tokens, sv *store.Server, span int) ui.Element {
	h := sv.Hooks
	var chips []string
	if h != nil {
		chips = []string{fmt.Sprint(len(h.Entries))}
	}
	return moleCard(c, k, "hooks", span, k.TileQueue, icLink, "Hooks", ui.Color{}, chips, func() {
		if h == nil || len(h.Entries) == 0 || !h.Enabled {
			display(c, "None", 24)
			foot := "No hooks in hooks.yaml"
			if h != nil && len(h.Entries) > 0 {
				foot = "Hooks are switched off in hooks.yaml"
			}
			cardFoot(c, k, foot)
			return
		}
		ran := 0
		var parts []string
		if sv.Stats != nil {
			for _, o := range sv.Stats.Hooks {
				ran += o.Succeeded + o.Failed
				if o.Failed > 0 {
					parts = append(parts, fmt.Sprintf("%s ✗ %d", o.Name, o.Failed))
				} else {
					parts = append(parts, o.Name+" ✓")
				}
			}
		}
		display(c, fmt.Sprintf("%d ran", ran), 24)
		if len(parts) == 0 {
			names := make([]string, 0, len(h.Entries))
			for _, e := range h.Entries {
				names = append(names, e.Name)
			}
			parts = []string{strings.Join(names, " · ") + " · none ran lately"}
		}
		cardFoot(c, k, strings.Join(parts, " · "))
	})
}

func shortCommit(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}

// uptime writes how long the backend has run: "19 min", "5 h", "3 days".
func uptime(secs int64) string {
	switch d := time.Duration(secs) * time.Second; {
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}
