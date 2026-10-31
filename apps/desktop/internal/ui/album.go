package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/store"
	"amdl/apps/desktop/internal/widgets"
	"amdl/internal/client"
	"amdl/internal/domain"
	"amdl/internal/format"
)

// The album screen, in DIPs.
const (
	// albumCover is the hero's cover, and albumCoverCompact the one of a
	// narrow page.
	albumCover        = 232
	albumCoverCompact = 156
	// trackHeight is a track's row.
	trackHeight = 48
	// trackControl is the round control at a row's end.
	trackControl = 28
	// trackLead is a row's room before its title: its padding, number and
	// gap.
	trackLead = 12 + 24 + 14
	// albumFade is how far the page scrolls before its light is gone.
	albumFade = 360
)

// OpenAlbum shows an album hit's page, which reads its songs; Back returns
// to the screen it was opened from.
func (a *App) OpenAlbum(h client.SearchHit) {
	// Another album opens at its top.
	if st := a.scrolls["album"]; st != nil && a.Store.Search.Album.ID != h.ID {
		st.Y, a.scrollSeen["album"] = 0, 0
	}
	a.Store.OpenAlbum(h)
	a.push(ScreenAlbum)
}

// revealAlbum chooses, back on home, the album the page showed, so the
// keys go on from it. The results keep the place they were left at, where
// it was in view when it opened.
func (a *App) revealAlbum() {
	h := a.Store.Search.Album.Hit
	for _, x := range a.Store.Search.ShownHits() {
		if store.HitKey(x) == store.HitKey(h) {
			a.searchSel, a.searchReveal = store.HitKey(h), false
			return
		}
	}
}

// backLabel names the screen Back returns to.
func (a *App) backLabel() string {
	s := a.Store
	switch a.backTo() {
	case ScreenHome:
		if s.Search.Artist.ID != "" && s.Search.Artist.Artist.Name != "" {
			return clip(s.Search.Artist.Artist.Name, 28)
		}
		if s.Search.Term != "" {
			return "Results"
		}
		return "Home"
	case ScreenAlbum:
		return clip(albumOf(&s.Search.Album).title, 28)
	case ScreenSettings:
		return "Settings"
	case ScreenDetail:
		return "Download"
	}
	return "Queue"
}

// clip cuts s to n characters, an ellipsis last.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// albumFacts are what the album screen shows of an album: the album once
// read, else the hit it opened from.
type albumFacts struct {
	title, artist, artwork, genre, year string
	traits                              []string
	explicit                            bool
	tracks                              int
	length                              time.Duration
	pal                                 Palette
}

func albumOf(p *store.AlbumPage) albumFacts {
	h := p.Hit
	f := albumFacts{title: h.Name, artist: h.ArtistName, artwork: h.ArtworkURL, genre: h.Genre, traits: h.AudioTraits,
		explicit: h.ContentRating == "explicit", tracks: h.TrackCount, pal: hitPalette(h)}
	date := h.ReleaseDate
	if p.Status == store.SearchReady {
		al := p.Album
		f.title = firstNonEmptyStr(al.Title, f.title)
		f.artist = firstNonEmptyStr(al.ArtistName, f.artist)
		f.artwork = firstNonEmptyStr(al.ArtworkURL, f.artwork)
		f.genre = firstNonEmptyStr(al.Genre, f.genre)
		date = firstNonEmptyStr(al.ReleaseDate, date)
		if len(al.AudioTraits) > 0 {
			f.traits = al.AudioTraits
		}
		f.explicit = f.explicit || al.ContentRating == "explicit"
		f.tracks = len(al.Tracks)
		for _, t := range al.Tracks {
			f.length += time.Duration(t.DurationMS) * time.Millisecond
		}
		if pal := ParsePalette(al.ArtworkBgColor, al.ArtworkTextColor1, al.ArtworkTextColor2, al.ArtworkTextColor3, al.ArtworkTextColor4); pal.OK {
			f.pal = pal
		}
	}
	if len(date) >= 4 {
		f.year = date[:4]
	}
	f.title = firstNonEmptyStr(f.title, "Album")
	return f
}

// albumKind is what Apple Music calls a collection by its title's suffix,
// and the title without it: the label above the title says it once.
func albumKind(title string) (kind, name string) {
	for _, k := range []string{"Single", "EP"} {
		if n, ok := strings.CutSuffix(title, " - "+k); ok && n != "" {
			return k, n
		}
	}
	return "Album", title
}

// titleSize is the hero title's size: smaller as a title runs longer, so
// most fit two lines.
func titleSize(title string, compact bool) float32 {
	n := utf8.RuneCountInString(title)
	size := float32(44)
	switch {
	case n > 48:
		size = 30
	case n > 26:
		size = 36
	}
	if compact {
		size *= 0.72
	}
	return size
}

// albumLength is an album's playing time in minutes, hours past the first.
func albumLength(d time.Duration) string {
	m := int(d.Round(time.Minute) / time.Minute)
	if m < 60 {
		return fmt.Sprintf("%d min", max(m, 1))
	}
	return fmt.Sprintf("%d hr %d min", m/60, m%60)
}

// albumBackdrop lights the album screen with the album's palette.
func (a *App) albumBackdrop(c *ui.Context, k *Tokens) {
	pal := albumOf(&a.Store.Search.Album).pal
	if !pal.OK {
		return
	}
	strength := float32(0.55)
	if !k.Dark {
		strength = 0.28
	}
	// The light fades as the songs scroll up over it.
	if st := a.scrolls["album"]; st != nil {
		strength *= max(0, 1-st.Y/albumFade)
	}
	widgets.Backdrop{Light: pal.Bg, Deep: pal.Bg.Mix(ui.Hex("#05081f"), 0.65), Strength: strength, Height: 480}.Show(c)
}

// albumScreen is an album: its cover and title over its palette, the one
// action that fits its state, and its songs, those on disk checked.
func (a *App) albumScreen(c *ui.Context, k *Tokens, page float32) {
	p := &a.Store.Search.Album
	if p.ID == "" {
		a.Back()
		return
	}
	a.Store.WatchConfig()
	padX := float32(32)
	if a.compact {
		padX = 20
	}
	width := max(min(page-2*padX, homeMaxWidth), 280)
	a.scrollArea(c, "album", k.Ground, 0, func() {
		ui.Column(c).FillWidth().AlignItems(ui.Center).Padding(16, 0, 40, 0).Children(func() {
			ui.Column(c).Width(width).Gap(30).Children(func() {
				a.albumHero(c, k)
				a.albumTracks(c, k)
			})
		})
	}).Grow(1).FillWidth()
}

func (a *App) albumHero(c *ui.Context, k *Tokens) {
	s := a.Store
	p := &s.Search.Album
	f := albumOf(p)
	hc := heroColorsFor(f.pal, k)
	kind, name := albumKind(f.title)
	size := float32(albumCover)
	hero := ui.Row(c).Gap(28).AlignItems(ui.End)
	if a.compact {
		// The text runs taller than a small cover: both start at the top.
		size = albumCoverCompact
		hero.AlignItems(ui.Start)
	}
	hero.Children(func() {
		// A shadow that stays below the cover: the scroll clips at the top of
		// the page, and the hero cover's own shadow rises past it.
		widgets.Cover{Image: a.Art.Get(f.artwork, int(2*size)), Placeholder: f.pal.Bg, Size: size, Radius: radiusCover, Color: 1}.Show(c).
			Shadow(0, 20, 44, -12, ui.RGBA(0, 0, 0, 0.42))
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(10).Children(func() {
			if f.genre != "" {
				kind += " · " + f.genre
			}
			label(c, k, kind).TextColor(hc.label)
			display(c, name, titleSize(name, a.compact)).TextColor(hc.title).MaxLines(2).Ellipsis("…")
			if f.artist != "" {
				ui.Text(c, f.artist).FontSize(17).FontWeight(500).TextColor(hc.title.Mix(hc.meta, 0.4)).SingleLine().Ellipsis("…")
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
				var facts []string
				if f.year != "" {
					facts = append(facts, f.year)
				}
				if f.tracks > 0 {
					facts = append(facts, plural(f.tracks, "track"))
				}
				if f.length > 0 {
					facts = append(facts, albumLength(f.length))
				}
				if len(facts) > 0 {
					ui.Text(c, strings.Join(facts, " · ")).FontSize(13).TextColor(hc.meta).SingleLine().Margin(0, 4, 0, 0)
				}
				for _, q := range qualityHints(f.traits) {
					tag(c, k, q, q == "Hi-Res Lossless", hc.meta)
				}
				if f.explicit {
					explicitBadge(c, k)
				}
			})
			ui.Box(c).Margin(10, 0, 0, 0).Children(func() { a.albumActions(c, k, hc) })
		})
	})
}

// albumActions are the album's state and what can be done with it: a
// download, or the queue's progress, or the library's check with a
// download of what is missing; the job behind a mark opens as a second
// choice.
func (a *App) albumActions(c *ui.Context, k *Tokens, hc heroColors) {
	s := a.Store
	h := s.Search.Album.Hit
	mark, job := s.HitMark(h)
	// A start that answers within a frame keeps the buttons as they were.
	since, starting := s.Search.Starting[store.HitKey(h)]
	wait := waited(c, starting, since)
	// The detail shows only a job the feed holds.
	showJob := func() {
		if s.Job(job) == nil {
			return
		}
		if button(c, k, buttonGhost, "Show download", nil).Height(34).Label("Show the download of " + h.Name).Clicked() {
			a.OpenJob(job)
		}
	}
	switch mark {
	case store.MarkActive:
		ui.Column(c).Width(420).Gap(12).Children(func() {
			if j := s.Job(job); j != nil {
				albumProgress(c, k, j, "album-progress", 6, hc.title, hc.title.Alpha(0.16), hc.accent, hc.meta, 12)
			}
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				pill(c, k, pillRun, icDown, "In the queue")
				showJob()
			})
		})
	case store.MarkDone:
		ui.Row(c).Gap(8).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Row(c).Height(34).PaddingX(12).Gap(6).Radius(radiusControl).AlignItems(ui.Center).Background(k.OKSoft).Children(func() {
				ui.Icon(c, icCheck).FontSize(14).TextColor(k.OK)
				ui.Text(c, "In your library").FontSize(12.5).FontWeight(600).TextColor(k.OK).SingleLine()
			})
			if buttonOff(c, k, buttonPlain, "Download again", icDown, wait).Height(34).Label("Download "+h.Name+" again").Clicked() && !starting {
				a.download(h)
			}
			showJob()
		})
	default:
		ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
			if buttonOff(c, k, buttonPrimary, "Download", icDown, wait).Height(34).PaddingX(16).
				Label("Download "+h.Name).Clicked() && !starting {
				a.download(h)
			}
		})
	}
}

// albumTracks lists the album's songs by disc, or stands for them while
// they load or after the read failed.
func (a *App) albumTracks(c *ui.Context, k *Tokens) {
	s := a.Store
	p := &s.Search.Album
	switch p.Status {
	case store.SearchFailed:
		searchFailed(c, k, p.Err, s.RetryAlbum)
		return
	case store.SearchReady:
	default:
		n := min(max(p.Hit.TrackCount, 6), 14)
		ui.Column(c).Children(func() {
			for i := range n {
				ui.Row(c.Key(i)).Height(trackHeight).PaddingX(12).Gap(14).AlignItems(ui.Center).Children(func() {
					skeleton(c, k, 18, 10)
					ui.Column(c).Grow(1).Children(func() { skeleton(c, k, float32(140+(i*53)%160), 10) })
					skeleton(c, k, 32, 10)
				})
			}
		})
		return
	}
	al := &p.Album
	mark, job := s.HitMark(p.Hit)
	var items []domain.JobItem
	if mark != store.MarkNone {
		items = s.JobItems(job)
	}
	discs := al.DiscCount > 1
	ui.Column(c).Children(func() {
		ui.Row(c).Height(30).PaddingX(12).Gap(14).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(k.Line).Margin(0, 0, 6, 0).Children(func() {
			label(c, k, "#").Width(24).TextAlign(ui.End)
			label(c, k, "Title").Grow(1)
			ui.Row(c).Width(44).Justify(ui.End).Children(func() { ui.Icon(c, icClock).FontSize(13).TextColor(k.Faint) })
			ui.Box(c).Width(trackControl)
		})
		for i, t := range al.Tracks {
			if discs && (i == 0 || t.DiscNumber != al.Tracks[i-1].DiscNumber) {
				top := float32(4)
				if i > 0 {
					top = 18
				}
				ui.Row(c.Key(fmt.Sprint("disc", t.DiscNumber))).Height(28).PaddingStart(trackLead).Margin(top, 0, 2, 0).AlignItems(ui.Center).Children(func() {
					label(c, k, fmt.Sprintf("Disc %d", t.DiscNumber))
				})
			}
			a.trackLine(c, k, t, al.ArtistName, mark, items)
		}
	})
}

// trackLine is one song of the album: its number, title, artists when they
// are not the album's, length, and at its end its control: a check while
// it is on disk, the queue's mark while it is on its way, else a download
// of the song alone while the row is pointed at.
func (a *App) trackLine(c *ui.Context, k *Tokens, t client.AlbumTrack, albumArtist string, albumMark store.MarkState, items []domain.JobItem) {
	s := a.Store
	mark, item := s.TrackMark(t, albumMark, items)
	// A job just started has not listed its tracks yet: they wait with it.
	queued := albumMark == store.MarkActive && item == nil && mark == store.MarkNone
	th := store.TrackHit(t)
	since, starting := s.Search.Starting[store.HitKey(th)]
	wait := waited(c, starting, since)
	row := ui.Row(c.Key(t.ID)).Height(trackHeight).PaddingX(12).Gap(14).Radius(radiusControl).AlignItems(ui.Center)
	// Only a row that downloads lights under the pointer.
	pointed := row.Hovered()
	if pointed && mark == store.MarkNone && !wait && !queued {
		row.Background(k.Fg.Alpha(0.055))
	}
	row.Children(func() {
		num := k.Faint
		if mark == store.MarkDone {
			num = k.Muted
		}
		mono(c, fmt.Sprint(t.TrackNumber), 12).TextColor(num).Width(24).TextAlign(ui.End)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, t.Name).FontSize(13.5).FontWeight(600).SingleLine().Ellipsis("…").Shrink(1)
				if t.ContentRating == "explicit" {
					explicitBadge(c, k)
				}
			})
			if t.ArtistName != "" && t.ArtistName != albumArtist {
				ui.Text(c, t.ArtistName).FontSize(12).TextColor(k.Muted).SingleLine().Ellipsis("…")
			}
		})
		length := ""
		if t.DurationMS > 0 {
			length = format.Duration(time.Duration(t.DurationMS) * time.Millisecond)
		}
		mono(c, length, 12).TextColor(k.Muted).Width(44).TextAlign(ui.End)
		ui.Box(c).Size(trackControl, trackControl).Center().Children(func() {
			switch {
			case mark == store.MarkDone:
				ui.Icon(c, icCheck).FontSize(15).TextColor(k.OK).Tooltip("On disk")
			case queued || mark == store.MarkActive && waiting(item):
				ui.Icon(c, icClock).FontSize(15).TextColor(k.Faint).Tooltip("Waiting")
			case mark == store.MarkActive:
				ui.Icon(c, icDown).FontSize(15).TextColor(k.Accent).Tooltip("Downloading")
			case wait:
				ui.Text(c, "…").FontSize(13).FontWeight(700).TextColor(k.Muted)
			default:
				a.trackDownload(c, k, th, pointed)
			}
		})
	})
}

// waiting reports a track of a running job that has not started yet; nil
// is a song downloaded alone, whose job's tracks the screen does not read.
func waiting(it *domain.JobItem) bool {
	return it != nil && (it.Status == domain.ItemQueued || it.Status == domain.ItemResolving || it.Status == domain.ItemWaitingDownload)
}

// trackDownload downloads one song of the album alone; it shows while its
// row is pointed at.
func (a *App) trackDownload(c *ui.Context, k *Tokens, th client.SearchHit, shown bool) {
	b := ui.ButtonBase(c.Key("download")).Size(trackControl, trackControl).Radius(radiusFull).Center().Label("Download " + th.Name)
	if b.Hovered() {
		b.Background(k.Track)
	} else {
		b.Background(k.Raise2)
	}
	show := float32(0)
	if shown {
		show = 1
	}
	op := b.AnimateWith("shown", show, 120*time.Millisecond, ui.EaseOut)
	b.Opacity(op).Hidden(op < 0.01)
	if b.Clicked() {
		a.download(th)
	}
	b.Cursor(ui.CursorPointer).Children(func() { ui.Icon(c, icDown).FontSize(14).TextColor(k.Fg) })
}
