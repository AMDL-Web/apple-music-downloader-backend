package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/store"
	"amdl/apps/desktop/internal/widgets"
	"amdl/internal/client"
	"amdl/internal/format"
)

// The result grids, in DIPs.
const (
	// tileGap is the room between tiles, and tileMin the narrowest tile
	// before a row holds one fewer; a group that tileSnug more fits on one
	// row takes its tiles down to that width, so no tile sits alone below.
	tileGap  = 16
	tileMin  = 160
	tileSnug = 120
	// songMin is the narrowest song card before a row holds one fewer.
	songMin = 300
	// songHeight is a song card's height around its padTile-inset cover.
	songHeight = 64
)

// tileSize is the width of count tiles' tile when a row of them fills
// width; count 0 is a row of any number.
func tileSize(width float32, count int) float32 {
	fit := func(min float32) int { return max(3, int((width+tileGap)/(min+tileGap))) }
	n := fit(tileMin)
	if count > n && count <= fit(tileSnug) {
		n = count
	}
	return (width - float32(n-1)*tileGap) / float32(n)
}

// tileGrid lays hits out as tiles of their covers, rows filling width.
func (a *App) tileGrid(c *ui.Context, k *Tokens, key string, hits []client.SearchHit, sel *client.SearchHit, width float32) {
	size := tileSize(width, len(hits))
	ui.Row(c).Width(width).Wrap().GapX(tileGap).GapY(24).Children(func() {
		for i, h := range hits {
			a.hitTile(c, k, h, fmt.Sprint(key, i), sel, size)
		}
	})
}

// hitTile is an album's, a playlist's or an artist's tile: the cover large,
// its title and one line of facts under it. An album's cover opens its page
// and a playlist's downloads it, or opens the download a mark names; both
// carry the best quality and one control, which downloads it or says it is
// in the queue or the library (tileControl). An artist's opens its albums.
func (a *App) hitTile(c *ui.Context, k *Tokens, h client.SearchHit, key string, sel *client.SearchHit, size float32) {
	hk := store.HitKey(h)
	on := sel != nil && store.HitKey(*sel) == hk
	round := h.Type == "artist"
	radius := float32(radiusCover)
	if round {
		radius = radiusFull
	}
	ui.Column(c.Key(key)).Width(size).Gap(10).Children(func() {
		art := ui.Box(c.Key("art")).Size(size, size)
		hover := art.Hovered()
		art.Children(func() {
			open := ui.ButtonBase(c.Key("open")).Size(size, size).Radius(radius).Label(h.Name).
				Shadow(0, 12, 28, -14, ui.RGBA(0, 0, 0, 0.5))
			if on {
				focusRing(open, radius)
				if a.searchReveal {
					open.ScrollIntoView()
					a.searchReveal = false
				}
			}
			if open.Clicked() {
				switch h.Type {
				case "artist":
					a.openArtist(h)
				case "album":
					a.OpenAlbum(h)
				default:
					a.openOrDownload(h)
				}
			}
			open.Cursor(ui.CursorPointer).Children(func() {
				widgets.Cover{Image: a.Art.Get(h.ArtworkURL, int(2*size)), Placeholder: hitPalette(h).Bg, Size: size, Radius: radius, Color: 1}.Show(c)
			})
			if round {
				return
			}
			if hints := shortHints(h.AudioTraits, size); hints != "" {
				ui.Row(c).Absolute().Left(8).Bottom(8).Height(22).PaddingX(8).Radius(radiusFull).AlignItems(ui.Center).
					Material(glass.Glass{}).PassThrough().Children(func() {
					ui.Text(c, hints).FontSize(11).FontWeight(650).TextColor(k.Fg).SingleLine()
				})
			}
			a.tileControl(c, k, h, hover || on)
		})
		texts := ui.Column(c).Gap(3)
		if round {
			texts.AlignItems(ui.Center)
		}
		texts.Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).MaxWidth(size).Children(func() {
				ui.Text(c, h.Name).FontSize(13).FontWeight(600).SingleLine().Ellipsis("…").Shrink(1)
				if h.ContentRating == "explicit" {
					explicitBadge(c, k)
				}
			})
			if sub := strings.Join(tileFacts(h), " · "); sub != "" {
				ui.Text(c, sub).FontSize(12).TextColor(k.Muted).SingleLine().Ellipsis("…").MaxWidth(size)
			}
		})
	})
}

// tileControlSize is the round control in an album tile's corner.
const tileControlSize = 32

// tileControl is an album tile's one control, in its bottom corner: a
// download button that shows while the tile is pointed at or chosen; the
// queue's mark while a download of the album runs, which opens its page;
// the library's check, which turns into a download button while pointed at,
// to fetch the tracks missing from disk (those on disk are skipped).
func (a *App) tileControl(c *ui.Context, k *Tokens, h client.SearchHit, shown bool) {
	s := a.Store
	hk := store.HitKey(h)
	mark, _ := s.HitMark(h)
	startedAt, starting := s.Search.Starting[hk]
	// The glyph changes only when the start is slow, as a pill's does.
	wait := waited(c, starting, startedAt)
	b := ui.ButtonBase(c.Key("control")).Size(tileControlSize, tileControlSize).Radius(radiusFull).Center().
		Absolute().Right(8).Bottom(8)
	pointed := b.Hovered()
	ic, fg := icDown, k.Fg
	switch {
	case wait:
		b.Material(glass.Glass{}).Label("Starting the download of " + h.Name)
		ic = nil
	case mark == store.MarkActive:
		b.Background(k.Accent).Shadow(0, 2, 6, 0, ui.RGBA(0, 0, 0, 0.3)).Label(h.Name + " is in the queue").Tooltip("In the queue")
		fg = k.AccentInk
	case mark == store.MarkDone && !pointed:
		b.Background(k.OK).Shadow(0, 2, 6, 0, ui.RGBA(0, 0, 0, 0.3)).Label(h.Name + " is in your library: download again")
		ic, fg = icCheck, ui.RGB(255, 255, 255)
	case mark == store.MarkDone:
		b.Material(glass.Glass{Interactive: true}).Label(h.Name + " is in your library: download again").Tooltip("Download again")
	default:
		b.Material(glass.Glass{Interactive: true}).Label("Download " + h.Name)
	}
	// Only the download button waits for the pointer; a mark stays.
	op := float32(1)
	if mark == store.MarkNone {
		show := float32(0)
		if shown || starting {
			show = 1
		}
		op = b.AnimateWith("shown", show, 120*time.Millisecond, ui.EaseOut)
	}
	b.Opacity(op).Hidden(op < 0.01)
	if b.Clicked() && !starting {
		switch {
		case mark == store.MarkActive && h.Type == "album":
			a.OpenAlbum(h)
		case mark == store.MarkActive:
			a.openOrDownload(h)
		default:
			a.download(h)
		}
	}
	b.Cursor(ui.CursorPointer).Children(func() {
		if ic == nil {
			ui.Text(c, "…").FontSize(13).FontWeight(700).TextColor(fg)
			return
		}
		ui.Icon(c, ic).FontSize(15).TextColor(fg)
	})
}

// songGrid lays songs out as cards, rows filling width.
func (a *App) songGrid(c *ui.Context, k *Tokens, hits []client.SearchHit, sel *client.SearchHit, width float32) {
	const gap = 12
	cols := max(1, int((width+gap)/(songMin+gap)))
	cw := (width - gap*float32(cols-1)) / float32(cols)
	ui.Row(c).Width(width).Wrap().Gap(gap).Children(func() {
		for i, h := range hits {
			a.songCard(c, k, h, fmt.Sprint("songs", i), sel, cw)
		}
	})
}

// songCard is a song: its cover, title, artist and album, and its mark or
// download button.
func (a *App) songCard(c *ui.Context, k *Tokens, h client.SearchHit, key string, sel *client.SearchHit, width float32) {
	s := a.Store
	hk := store.HitKey(h)
	on := sel != nil && store.HitKey(*sel) == hk
	card := ui.Row(c.Key(key)).Width(width).Height(songHeight).Padding(padTile).PaddingEnd(10).Gap(12).Radius(radiusTile).
		AlignItems(ui.Center).Background(k.Raise).Border(1, k.Line)
	if card.Hovered() {
		card.Background(k.Raise2)
	}
	if on {
		focusRing(card, radiusTile)
		if a.searchReveal {
			card.ScrollIntoView()
			a.searchReveal = false
		}
	}
	card.Children(func() {
		widgets.Cover{Image: a.Art.Get(h.ArtworkURL, 96), Placeholder: hitPalette(h).Bg, Size: songHeight - 2*padTile, Radius: radiusControl, Color: 1}.Show(c)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(3).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, h.Name).FontSize(13).FontWeight(600).Ellipsis("…").SingleLine().Shrink(1)
				if h.ContentRating == "explicit" {
					explicitBadge(c, k)
				}
			})
			ui.Text(c, strings.Join(hitFacts(h), " · ")).FontSize(12).TextColor(k.Muted).Ellipsis("…").SingleLine()
		})
		mark, job := s.HitMark(h)
		startedAt, startingOn := s.Search.Starting[hk]
		switch {
		case mark == store.MarkDone:
			if markButton(c, k, "In library", pillOK, icCheck).Clicked() {
				a.OpenJob(job)
			}
		case mark == store.MarkActive:
			if markButton(c, k, "In the queue", pillRun, icDown).Clicked() {
				a.OpenJob(job)
			}
		case waited(c, startingOn, startedAt):
			pill(c, k, pillWait, nil, "Starting…")
		default:
			dl := ui.ButtonBase(c.Key("download")).Size(32, 32).Radius(radiusFull).Center().Background(k.Raise2).Label("Download " + h.Name)
			if dl.Hovered() {
				dl.Background(k.Track)
			}
			if dl.Clicked() {
				a.download(h)
			}
			dl.Cursor(ui.CursorPointer).Children(func() { ui.Icon(c, icDown).FontSize(15).TextColor(k.Fg) })
		}
	})
}

// markButton is a hit's library or queue mark, which opens its job.
func markButton(c *ui.Context, k *Tokens, text string, kind pillKind, ic *ui.SVG) ui.Element {
	b := ui.ButtonBase(c.Key("mark")).Radius(radiusFull).Label(text + ": open the download")
	b.Cursor(ui.CursorPointer).Children(func() { pill(c, k, kind, ic, text) })
	return b
}

func explicitBadge(c *ui.Context, k *Tokens) {
	ui.Box(c).Size(15, 15).Radius(radiusInner).Background(k.Faint.Alpha(0.35)).Center().Children(func() {
		ui.Text(c, "E").FontSize(9.5).FontWeight(700).TextColor(k.Fg)
	})
}

// tileFacts is a tile's line of facts: an album's artist and year, an
// artist's genre.
func tileFacts(h client.SearchHit) []string {
	var out []string
	if h.Type == "artist" {
		if h.Genre != "" {
			out = append(out, h.Genre)
		}
		return out
	}
	if h.ArtistName != "" {
		out = append(out, h.ArtistName)
	}
	if len(h.ReleaseDate) >= 4 {
		out = append(out, h.ReleaseDate[:4])
	}
	return out
}

// hitFacts is a song card's line of facts: its artist, album and length.
func hitFacts(h client.SearchHit) []string {
	var out []string
	for _, v := range []string{h.ArtistName, h.AlbumName} {
		if v != "" {
			out = append(out, v)
		}
	}
	if h.DurationMS > 0 {
		out = append(out, format.Duration(time.Duration(h.DurationMS)*time.Millisecond))
	}
	return out
}

// qualityHints names what a hit's audio traits promise, best lossless
// first, then Atmos, as the web console does.
func qualityHints(traits []string) []string {
	has := map[string]bool{}
	for _, t := range traits {
		has[t] = true
	}
	var out []string
	switch {
	case has["hi-res-lossless"]:
		out = append(out, "Hi-Res Lossless")
	case has["lossless"]:
		out = append(out, "Lossless")
	}
	if has["atmos"] {
		out = append(out, "Dolby Atmos")
	}
	return out
}

// shortHints is the chip on a tile's cover: the best quality in short, and
// Atmos beside it on a tile wide enough.
func shortHints(traits []string, size float32) string {
	short := map[string]string{"Hi-Res Lossless": "Hi-Res", "Lossless": "Lossless", "Dolby Atmos": "Atmos"}
	hints := qualityHints(traits)
	if len(hints) > 1 && size < 180 {
		hints = hints[:1]
	}
	for i, h := range hints {
		hints[i] = short[h]
	}
	return strings.Join(hints, " · ")
}

func hitPalette(h client.SearchHit) Palette {
	return ParsePalette(h.ArtworkBgColor, h.ArtworkTextColor1, h.ArtworkTextColor2, h.ArtworkTextColor3, h.ArtworkTextColor4)
}

// skeletonGroups stand for the first answer of a term while it loads: a row
// of album tiles and a row of song cards, at the sizes they will take.
func skeletonGroups(c *ui.Context, k *Tokens, width float32) {
	ui.Column(c).Gap(32).Children(func() {
		ui.Column(c.Key("albums")).Gap(14).Children(func() {
			skeleton(c, k, 72, 14).Margin(8, 0)
			skeletonTiles(c, k, width, 0)
		})
		ui.Column(c.Key("songs")).Gap(14).Children(func() {
			skeleton(c, k, 64, 14).Margin(8, 0)
			const gap = 12
			cols := max(1, int((width+gap)/(songMin+gap)))
			cw := (width - gap*float32(cols-1)) / float32(cols)
			ui.Row(c).Gap(gap).Children(func() {
				for i := range cols {
					ui.Row(c.Key(i)).Width(cw).Height(songHeight).Padding(padTile).Gap(12).Radius(radiusTile).AlignItems(ui.Center).
						Background(k.Raise).Border(1, k.Line).Children(func() {
						skeleton(c, k, songHeight-2*padTile, songHeight-2*padTile).Radius(radiusControl)
						ui.Column(c).Grow(1).Gap(8).Children(func() {
							skeleton(c, k, float32(110+(i*37)%60), 10)
							skeleton(c, k, float32(150+(i*53)%70), 8)
						})
					})
				}
			})
		})
	})
}

// skeletonTiles is a row of tiles while they load; n 0 fills the row.
func skeletonTiles(c *ui.Context, k *Tokens, width float32, n int) {
	size := tileSize(width, store.SearchFirst)
	if fit := int((width + tileGap) / (size + tileGap)); n == 0 || n > fit {
		n = fit
	}
	ui.Row(c).Gap(tileGap).Children(func() {
		for i := range n {
			ui.Column(c.Key(i)).Width(size).Gap(10).Children(func() {
				skeleton(c, k, size, size).Radius(radiusCover)
				skeleton(c, k, size*0.7, 10)
				skeleton(c, k, size*0.45, 8)
			})
		}
	})
}
