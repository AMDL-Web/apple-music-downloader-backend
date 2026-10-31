package ui

import (
	"strings"

	"github.com/egoist/mygo/ui"
)

// label is the design's small caps label: 11 px, uppercase, spaced out.
func label(c *ui.Context, k *Tokens, s string) ui.Element {
	return ui.Text(c, strings.ToUpper(s)).FontSize(11).FontWeight(600).LetterSpacing(0.77).TextColor(k.Faint).SingleLine()
}

// mono sets an audio fact or a count in the monospace face with digits of
// one width.
func mono(c *ui.Context, s string, size float32) ui.Element {
	return ui.Text(c, s).Font(MonoFont).FontSize(size).FontWeight(500).FontFeatures("tnum").SingleLine()
}

// display is a big title in the system font's heaviest weight, tracked
// tight.
func display(c *ui.Context, s string, size float32) ui.Element {
	return ui.Text(c, s).FontSize(size).FontWeight(800).LetterSpacing(-0.02 * size).LineHeight(1.05)
}

// bigValue is a big number with its small unit after it, on one baseline.
func bigValue(c *ui.Context, k *Tokens, value, unit string, size float32) ui.Element {
	return ui.Row(c).AlignItems(ui.Baseline).Gap(3).Children(func() {
		display(c, value, size).SingleLine()
		if unit != "" {
			ui.Text(c, unit).FontSize(size * 0.44).FontWeight(600).TextColor(k.Muted).SingleLine()
		}
	})
}

// pillKind colors a status pill.
type pillKind int

const (
	pillRun pillKind = iota
	pillOK
	pillBad
	pillWait
	pillWarn
)

// pill is a status: icon, word and color together, never color alone.
func pill(c *ui.Context, k *Tokens, kind pillKind, ic *ui.SVG, text string) ui.Element {
	bg, fg := k.AccentSoft, k.Accent
	switch kind {
	case pillOK:
		bg, fg = k.OKSoft, k.OK
	case pillBad:
		bg, fg = k.BadSoft, k.Bad
	case pillWait:
		bg, fg = k.Track, k.Muted
	case pillWarn:
		bg, fg = k.WarnSoft, k.Warn
	}
	return ui.Row(c).Height(20).PaddingX(8).Gap(5).Radius(radiusFull).Background(bg).AlignItems(ui.Center).Children(func() {
		if ic != nil {
			ui.Icon(c, ic).FontSize(11).TextColor(fg)
		}
		ui.Text(c, text).FontSize(11).FontWeight(600).TextColor(fg).SingleLine()
	})
}

// tag is the small outlined label of an audio fact; hi gives Hi-Res its
// gold.
func tag(c *ui.Context, k *Tokens, s string, hi bool, fg ui.Color) ui.Element {
	el := ui.Row(c).Height(18).PaddingX(6).Radius(radiusInner).AlignItems(ui.Center)
	if hi {
		el.LinearGradient(ui.LinearGradient{Stops: []ui.GradientStop{{Color: ui.Hex("#c9b27a"), At: 0}, {Color: ui.Hex("#e9d7a6"), At: 1}}, Angle: 90})
		fg = ui.Hex("#2a2210")
	} else {
		if fg.A == 0 {
			fg = k.Muted
		}
		el.Border(1, fg.Alpha(0.3))
	}
	el.Children(func() {
		mono(c, s, 10).FontWeight(600).TextColor(fg)
	})
	return el
}

// chip is the Mole card's small tag at the right of a heading.
func chip(c *ui.Context, k *Tokens, s string, fg ui.Color) ui.Element {
	if fg.A == 0 {
		fg = k.Muted
	}
	return ui.Row(c).PaddingX(7).PaddingY(2).Radius(radiusInner).Background(k.Track).Children(func() {
		mono(c, s, 11).FontWeight(600).TextColor(fg).SingleLine().Ellipsis("…").Shrink(1)
	})
}

// buttonKind styles a button.
type buttonKind int

const (
	buttonPlain buttonKind = iota
	buttonPrimary
	buttonGhost
	// buttonPill is Mole's big white pill, the one action of a bar.
	buttonPill
	// buttonOnArt sits on an album-colored hero: translucent white.
	buttonOnArt
)

// button is the design's button; it returns the base, whose Clicked reports
// a press.
func button(c *ui.Context, k *Tokens, kind buttonKind, text string, ic *ui.SVG) ui.Element {
	return buttonOff(c, k, kind, text, ic, false)
}

// buttonOff is button, disabled when off. A button disabled after it is
// built still reports the hover and press it styles itself by, so a button
// that can be disabled is built with this.
func buttonOff(c *ui.Context, k *Tokens, kind buttonKind, text string, ic *ui.SVG, off bool) ui.Element {
	b := ui.ButtonBase(c).Disabled(off).Height(30).PaddingX(12).Gap(6).Radius(radiusControl).AlignItems(ui.Center).Justify(ui.Center)
	fg := k.Fg
	switch kind {
	case buttonPrimary:
		b.Background(k.Accent)
		fg = k.AccentInk
	case buttonGhost:
		// No fill at rest, so the fill is the only sign of a hover or a
		// press, as the sidebar's toggle has it.
		fg = k.Muted
		switch {
		case b.Pressed():
			b.Background(k.Track.Mix(k.Fg, 0.08))
			fg = k.Fg
		case b.Hovered():
			b.Background(k.Track)
			fg = k.Fg
		}
	case buttonPill:
		b.Height(44).PaddingX(34).Radius(radiusFull).Background(k.Pill)
		fg = k.PillInk
	case buttonOnArt:
		b.Background(ui.RGBA(255, 255, 255, 0.08)).Border(1, ui.RGBA(247, 235, 246, 0.16))
	default:
		b.Background(k.Raise).Border(1, k.Line2)
	}
	if b.Hovered() && kind != buttonGhost {
		b.Opacity(0.92)
	}
	b.Cursor(ui.CursorPointer).Children(func() {
		if ic != nil {
			ui.Icon(c, ic).FontSize(14).TextColor(fg)
		}
		if text != "" {
			w := 500
			if kind == buttonPrimary || kind == buttonPill {
				w = 650
			}
			size := float32(12.5)
			if kind == buttonPill {
				size = 14
			}
			ui.Text(c, text).FontSize(size).FontWeight(w).TextColor(fg).SingleLine()
		}
	})
	return b
}

// segmented is the design's segmented control: the chosen segment white on
// dark (ink on light), the others muted. colors may tint a segment's label,
// as Failed in red.
func segmented(c *ui.Context, k *Tokens, key string, selected *int, labels []string, colors map[int]ui.Color) {
	s := ui.SegmentedBase(c.Key(key), selected, len(labels))
	s.Track.Padding(padControl).Radius(radiusControl).Background(k.Raise2).Border(1, k.Line).Children(func() {
		for i, l := range labels {
			seg := s.Segment(i).PaddingX(10).PaddingY(2).Radius(radiusInner)
			fg := k.Muted
			if col, ok := colors[i]; ok {
				fg = col
			}
			weight := 500
			if i == *selected {
				seg.Background(k.Pill).Shadow(0, 1, 2, 0, ui.RGBA(0, 0, 0, 0.25))
				fg, weight = k.PillInk, 600
			}
			// Each segment keeps the width of its bold label, so choosing one
			// moves neither the others nor the track's edge.
			seg.Cursor(ui.CursorPointer).Children(func() {
				ui.Box(c).Children(func() {
					ui.Text(c, l).FontSize(12).FontWeight(600).SingleLine().Invisible()
					ui.Text(c, l).FontSize(12).FontWeight(weight).TextColor(fg).SingleLine().TextAlign(ui.Center).
						Absolute().Top(0).Left(0).Right(0)
				})
			})
		}
	})
}

// toggle is a switch in the design's look.
func toggle(c *ui.Context, k *Tokens, key string, on *bool, name string) ui.Element {
	sw := ui.SwitchBase(c.Key(key), on).Width(34).Height(20).Radius(radiusFull).Label(name)
	// The track's color moves with the knob, on the same curve.
	track := k.Track
	if *on {
		track = k.Accent
	}
	sw.Background(sw.AnimateColor("track", track, ui.Snappy))
	x := sw.AnimateTo("knob", map[bool]float32{true: 16, false: 2}[*on], ui.Snappy)
	sw.Cursor(ui.CursorPointer).Children(func() {
		ui.Box(c).Size(16, 16).Radius(radiusFull).Background(ui.RGB(255, 255, 255)).Absolute().Top(2).Left(x)
	})
	return sw
}

// focusRing draws the platform's focus ring, MyGo's own, around el rounded
// by radius: for a box whose focused element is a part of it, as a field's
// input, and for the result the arrow keys chose while the field keeps the
// keyboard. A text field rings whenever it has the focus, as macOS's do;
// other controls only for focus from the keyboard, as MyGo's ring does.
func focusRing(el ui.Element, radius float32) {
	el.DrawOver(func(p *ui.Painter, r ui.Rect) { p.FocusRing(r, [4]float32{radius, radius, radius, radius}) })
}

// card is the Mole card: large radius, no border.
func card(c *ui.Context, k *Tokens) ui.Element {
	return ui.Column(c).Padding(padCard).Gap(10).Radius(radiusCard).Background(k.Raise)
}

// artButton is a solid button in an album's colors, as the hero's Details.
func artButton(c *ui.Context, text string, bg, fg ui.Color) ui.Element {
	b := ui.ButtonBase(c).Height(30).PaddingX(12).Radius(radiusControl).AlignItems(ui.Center).Justify(ui.Center).Background(bg)
	if b.Hovered() {
		b.Opacity(0.92)
	}
	b.Cursor(ui.CursorPointer).Children(func() {
		ui.Text(c, text).FontSize(12.5).FontWeight(650).TextColor(fg).SingleLine()
	})
	return b
}
