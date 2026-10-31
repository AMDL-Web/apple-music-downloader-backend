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

// The album screen's track list, in DIPs. Its rows take the download
// page's metrics (trackInset, trackPadY, trackStateWidth) so the two lists
// of one album line up.
const (
	// trackHeight is a skeleton row while the songs load.
	trackHeight = 48
	// trackControl is the round control at a row's end.
	trackControl = 28
	// trackNumber is the number's column, and trackLead a row's room before
	// its title: its padding, number and gap.
	trackNumber = 28
	trackLead   = 4 + trackNumber + 16
	// trackLength is the length's column.
	trackLength = 44
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

// albumBackdrop lights the album screen with the album's palette, as the
// download page is lit with its job's.
func (a *App) albumBackdrop(c *ui.Context, k *Tokens) {
	paletteBackdrop(c, k, albumOf(&a.Store.Search.Album).pal)
}

// albumScreen is an album in the download page's layout: its cover and
// title over its palette, what it is and the one action its state takes,
// then its songs in the lower panel, those on disk or on their way with the
// state the download page gives them.
func (a *App) albumScreen(c *ui.Context, k *Tokens, page float32) {
	s := a.Store
	p := &s.Search.Album
	if p.ID == "" {
		a.Back()
		return
	}
	s.WatchConfig()
	f := albumOf(p)
	hc := heroColorsFor(f.pal, k)
	kind, name := albumKind(f.title)
	if f.tracks > 0 {
		kind += " · " + plural(f.tracks, "track")
	}
	a.collectionHero(c, k, pageHero(c, page), heroSpec{kicker: kind, title: name, artist: f.artist, colors: hc,
		cover: func(size float32) {
			widgets.Cover{Image: a.Art.Get(f.artwork, 512), Placeholder: f.pal.Bg, Size: size, Radius: radiusCover, Color: 1, Shadow: true}.Show(c)
		},
		tags: func() {
			for _, q := range qualityHints(f.traits) {
				tag(c, k, q, q == "Hi-Res Lossless", hc.meta)
			}
			if f.explicit {
				explicitBadge(c, k)
			}
			if mark, _ := s.HitMark(p.Hit); mark == store.MarkDone {
				pill(c, k, pillOK, icCheck, "In your library")
			}
		},
		band:    func() { a.albumBand(c, k, f, hc) },
		actions: func() { a.albumActions(c, k, hc) },
	})
	ui.Column(c).Grow(1).MinHeight(0).Background(k.Ink.Alpha(detailInk)).BorderWidth(1, 0, 0, 0).BorderColor(k.Line).Children(func() {
		a.albumTracks(c, k)
	})
}

// albumBand is what the album is under its title, in the download
// page's figures: when it came out, its genre and length; and its job's
// bar once a job holds it, as the download page draws it, so the two
// pages of one album differ in their figures alone.
func (a *App) albumBand(c *ui.Context, k *Tokens, f albumFacts, hc heroColors) {
	s := a.Store
	ui.Row(c).Gap(12).MaxWidth(620).Margin(8, 0, 0, 0).Children(func() {
		heroStat(c, k, hc, "Released", firstNonEmptyStr(f.year, "—"), "")
		heroStat(c, k, hc, "Genre", firstNonEmptyStr(f.genre, "—"), "")
		if m := int(f.length.Round(time.Minute) / time.Minute); m > 0 {
			heroStat(c, k, hc, "Length", fmt.Sprint(m), " min")
		} else {
			heroStat(c, k, hc, "Length", "—", "")
		}
	})
	if _, job := s.HitMark(s.Search.Album.Hit); s.Job(job) != nil {
		ui.Column(c).FillWidth().MaxWidth(620).Margin(6, 0, 0, 0).Children(func() {
			albumProgress(c, k, s.Job(job), "album-progress", 8, hc.title, hc.title.Alpha(0.16), hc.accent, hc.meta, 12)
		})
	}
}

// heroStat is one figure of the head's band: its name over its value, as
// the download page's figures.
func heroStat(c *ui.Context, k *Tokens, hc heroColors, name, value, unit string) {
	ui.Column(c).Grow(1).Basis(0).MinWidth(0).Gap(2).Children(func() {
		label(c, k, name).TextColor(hc.meta.Alpha(0.8))
		ui.Row(c).AlignItems(ui.Baseline).Children(func() {
			mono(c, value, 22).FontWeight(600).TextColor(hc.title).SingleLine().Ellipsis("…").Shrink(1)
			if unit != "" {
				mono(c, unit, 14).TextColor(hc.meta.Alpha(0.8))
			}
		})
	})
}

// albumActions are what the album's state lets be done, at the head's
// end as the download page's buttons: a download; or, once a job holds it,
// that job's page, and a download again of what is missing once it is in
// the library.
func (a *App) albumActions(c *ui.Context, k *Tokens, hc heroColors) {
	s := a.Store
	h := s.Search.Album.Hit
	mark, job := s.HitMark(h)
	// A start that answers within a frame keeps the buttons as they were.
	since, starting := s.Search.Starting[store.HitKey(h)]
	wait := waited(c, starting, since)
	if mark == store.MarkNone {
		if wait {
			pill(c, k, pillWait, nil, "Starting…")
		} else if artButton(c, "Download", hc.title, hc.ink).Label("Download "+h.Name).Clicked() && !starting {
			a.download(h)
		}
		return
	}
	if mark == store.MarkDone && buttonOff(c, k, buttonOnArt, "Download again", nil, wait).Label("Download "+h.Name+" again").Clicked() && !starting {
		a.download(h)
	}
	// The download page shows only a job the feed holds.
	if s.Job(job) != nil && button(c, k, buttonOnArt, "Show download", nil).Label("Show the download of "+h.Name).Clicked() {
		a.OpenJob(job)
	}
}

// albumTracks lists the album's songs by disc in the lower panel, in the
// download page's columns, or stands for them while they load or after
// the read failed.
func (a *App) albumTracks(c *ui.Context, k *Tokens) {
	s := a.Store
	p := &s.Search.Album
	ui.Column(c).Grow(1).MinHeight(0).Padding(10, 24, 0, 32).Children(func() {
		ui.Row(c).Gap(16).Padding(11, 4, 4, 4).Margin(0, scrollGutter, 0, 0).AlignItems(ui.Center).Children(func() {
			ui.Box(c).Width(trackNumber)
			label(c, k, "Track").Grow(1)
			ui.Row(c).Width(trackLength).Justify(ui.End).Children(func() { ui.Icon(c, icClock).FontSize(13).TextColor(k.Faint) })
			label(c, k, "State").Width(trackStateWidth).TextAlign(ui.End)
		})
		switch p.Status {
		case store.SearchFailed:
			searchFailed(c, k, p.Err, s.RetryAlbum)
			return
		case store.SearchReady:
		default:
			n := min(max(p.Hit.TrackCount, 6), 14)
			for i := range n {
				ui.Row(c.Key(i)).Height(trackHeight).PaddingX(4).Gap(16).AlignItems(ui.Center).Children(func() {
					ui.Row(c).Width(trackNumber).Justify(ui.End).Children(func() { skeleton(c, k, 18, 10) })
					ui.Column(c).Grow(1).Children(func() { skeleton(c, k, float32(140+(i*53)%160), 10) })
					skeleton(c, k, 32, 10)
				})
			}
			return
		}
		al := &p.Album
		mark, job := s.HitMark(p.Hit)
		var items []domain.JobItem
		if mark != store.MarkNone {
			items = s.JobItems(job)
		}
		lines := make([]trackMark, len(al.Tracks))
		for i, t := range al.Tracks {
			lines[i].mark, lines[i].item = s.TrackMark(t, mark, items)
		}
		discs := al.DiscCount > 1
		a.scrollArea(c, "album", detailGround(k), scrollGutter, func() {
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
				nextLit := i+1 < len(lines) && lines[i+1].lit()
				a.trackLine(c, k, t, al.ArtistName, mark, lines[i], nextLit)
			}
		}).Grow(1)
	})
}

// trackMark is a song's mark and, while a job of its album holds it, the
// job's track.
type trackMark struct {
	mark store.MarkState
	item *domain.JobItem
}

// lit reports whether the song's row is highlighted, as a track's on the
// download page: its job works on it now.
func (m trackMark) lit() bool { return m.item != nil && trackLit(m.item) }

// trackLine is one song of the album, in the download page's row: its
// number, title, and under it the artists when they are not the album's or
// what its job made of it, its length, and its state: the job's for a song
// its album's job holds, else a check for one on disk, the queue's mark for
// one on its way, else a download of the song alone while the row is
// pointed at.
func (a *App) trackLine(c *ui.Context, k *Tokens, t client.AlbumTrack, albumArtist string, albumMark store.MarkState, m trackMark, nextLit bool) {
	s := a.Store
	// A job just started has not listed its tracks yet: they wait with it.
	queued := albumMark == store.MarkActive && m.item == nil && m.mark == store.MarkNone
	th := store.TrackHit(t)
	since, starting := s.Search.Starting[store.HitKey(th)]
	wait := waited(c, starting, since)
	lit := m.lit()
	line := k.Line
	if lit || nextLit {
		line = ui.Transparent
	}
	ui.Column(c.Key(t.ID)).PaddingY(trackInset).BorderWidth(0, 0, 1, 0).BorderColor(line).Children(func() {
		row := ui.Row(c).Gap(16).Padding(trackPadY, 4).AlignItems(ui.Center).Radius(radiusControl)
		// Only a row that downloads lights under the pointer.
		pointed := row.Hovered()
		switch {
		case lit:
			row.Background(k.AccentSoft.Alpha(0.45))
		case pointed && m.mark == store.MarkNone && !wait && !queued:
			row.Background(k.Fg.Alpha(0.055))
		}
		row.Children(func() {
			num := k.Faint
			if m.mark == store.MarkDone {
				num = k.Muted
			}
			mono(c, fmt.Sprint(t.TrackNumber), 12).TextColor(num).Width(trackNumber).TextAlign(ui.End)
			ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(2).Children(func() {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Text(c, t.Name).FontWeight(600).SingleLine().Ellipsis("…").Shrink(1)
					if t.ContentRating == "explicit" {
						explicitBadge(c, k)
					}
				})
				sub := " "
				switch {
				case t.ArtistName != "" && t.ArtistName != albumArtist:
					sub = t.ArtistName
				case m.item != nil && trackSub(m.item) != "":
					sub = trackSub(m.item)
				}
				mono(c, sub, 11).TextColor(k.Muted).Ellipsis("…")
			})
			length := ""
			if t.DurationMS > 0 {
				length = format.Duration(time.Duration(t.DurationMS) * time.Millisecond)
			}
			mono(c, length, 12).TextColor(k.Muted).Width(trackLength).TextAlign(ui.End)
			ui.Row(c).Width(trackStateWidth).Height(trackControl).Gap(6).Justify(ui.End).AlignItems(ui.Center).Children(func() {
				switch {
				case m.item != nil:
					trackState(c, k, m.item)
				case m.mark == store.MarkDone:
					stateWord(c, icCheck, "On disk", k.OK)
				case queued || m.mark == store.MarkActive && waiting(m.item):
					stateWord(c, icClock, "Waiting", k.Muted)
				case m.mark == store.MarkActive:
					stateWord(c, icDown, "In the queue", k.Accent)
				case wait:
					ui.Text(c, "…").FontSize(13).FontWeight(700).TextColor(k.Muted)
				default:
					a.trackDownload(c, k, th, pointed)
				}
			})
		})
	})
}

// stateWord is a state in the track lists' state column: its icon and word
// in col, as trackState draws a job's.
func stateWord(c *ui.Context, ic *ui.SVG, text string, col ui.Color) {
	ui.Icon(c, ic).FontSize(13).TextColor(col)
	ui.Text(c, text).FontSize(12).FontWeight(600).TextColor(col).SingleLine()
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
