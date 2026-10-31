package ui

import (
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// heroLayout is the head of the album and download pages, which share it so
// that going from one to the other of the same album moves nothing but
// what differs: the cover's side, the title's size, the room above and
// between.
type heroLayout struct {
	cover, title, top, gap float32
	compact                bool
}

// pageHero is the head's layout on a page width wide: a narrow or short
// window keeps room for the tracks under a smaller head.
func pageHero(c *ui.Context, page float32) heroLayout {
	_, height := c.Size()
	if page < 960 || height < 700 {
		return heroLayout{cover: 148, title: 34, top: 12, gap: 24, compact: true}
	}
	return heroLayout{cover: 248, title: 52, top: 26, gap: 32}
}

// heroActions is the least width of the head's buttons: its widest,
// "Download again" or "Reveal in Finder".
const heroActions = 136

// titleFor is the title's size: smaller as a title runs longer, so most fit
// two lines.
func (l heroLayout) titleFor(title string) float32 {
	switch n := utf8.RuneCountInString(title); {
	case n > 48:
		return l.title * 0.7
	case n > 26:
		return l.title * 0.84
	}
	return l.title
}

// heroSpec is what one page puts in the head: the cover drawn at the
// layout's side, the label over the title, the title, the artists, the
// tags after them, what the page says under them (band) and its buttons
// at the end (actions).
type heroSpec struct {
	cover         func(size float32)
	kicker, title string
	artist        string
	tags, band    func()
	actions       func()
	colors        heroColors
}

// collectionHero is the head of the album and download pages: the cover,
// then the label, title, artists and the page's band, then its buttons,
// bottoms aligned.
func (a *App) collectionHero(c *ui.Context, k *Tokens, l heroLayout, h heroSpec) {
	hc := h.colors
	ui.Row(c).Padding(l.top, 32, 22, 32).Gap(l.gap).AlignItems(ui.End).Children(func() {
		h.cover(l.cover)
		ui.Column(c).Grow(1).Shrink(1).MinWidth(0).Gap(12).Children(func() {
			label(c, k, h.kicker).TextColor(hc.label)
			display(c, h.title, l.titleFor(h.title)).TextColor(hc.title).MaxLines(2).Ellipsis("…")
			ui.Row(c).Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
				if h.artist != "" {
					ui.Text(c, h.artist).FontSize(17).TextColor(hc.title.Mix(hc.meta, 0.4)).SingleLine().Ellipsis("…").Shrink(1)
				}
				if h.tags != nil {
					h.tags()
				}
			})
			if h.band != nil {
				h.band()
			}
		})
		if h.actions != nil {
			// One width whatever the buttons, so the band beside them ends
			// at the same place on both pages.
			ui.Column(c).MinWidth(heroActions).Gap(8).AlignItems(ui.End).Children(h.actions)
		}
	})
}
