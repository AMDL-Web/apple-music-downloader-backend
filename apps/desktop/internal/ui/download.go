package ui

import (
	"slices"

	"github.com/egoist/mygo/ui"

	"amdl/apps/desktop/internal/store"
	"amdl/internal/client"
	"amdl/internal/config"
)

// downloadAsk is a download waiting for its sheet: which of two qualities
// to take, where the hit has both and the server's priority names both,
// and whether to replace what is on disk, where it is in the library.
type downloadAsk struct {
	hit client.SearchHit
	// codecs are the choices in the server's priority order, the first
	// the default; nil asks nothing about quality.
	codecs   []string
	priority []string
	codec    int
	// again asks whether to replace the files of an earlier download;
	// replace is the choice, 0 to skip them.
	again   bool
	replace int
	open    bool
}

// askCodecs are the qualities a download of a hit with traits asks between:
// lossless and Dolby, when the hit has both and priority lists both, in
// priority's order. Any other mix, AAC among them, the server settles on its
// own.
func askCodecs(traits, priority []string) []string {
	lossless := slices.Contains(traits, "lossless") || slices.Contains(traits, "hi-res-lossless")
	dolby := slices.Contains(traits, "atmos")
	var out []string
	hasDolby := false
	for _, c := range priority {
		switch {
		case c == "alac" && lossless && !slices.Contains(out, c):
			out = append(out, c)
		case (c == "ec3" || c == "ac3") && dolby && !hasDolby:
			out, hasDolby = append(out, c), true
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// codecLabel names a codec the sheet offers.
func codecLabel(c string) string {
	switch c {
	case "alac":
		return "Lossless"
	case "ec3":
		return "Dolby Atmos"
	case "ac3":
		return "Dolby Digital"
	}
	return c
}

// download starts h, after a sheet when there is something to ask: the
// quality where the hit offers both lossless and Dolby and the server would
// take either, and whether to replace the files of an earlier download
// unless the app is set to skip them (mode.Settings.SkipExisting).
func (a *App) download(h client.SearchHit) {
	s := a.Store
	priority := strs(s.Config.Value("download.quality_priority"))
	ask := downloadAsk{hit: h, priority: priority, codecs: askCodecs(h.AudioTraits, priority)}
	mark, _ := s.HitMark(h)
	done := mark == store.MarkDone
	ask.again = done && !a.Settings.SkipExisting
	if ask.codecs == nil && !ask.again {
		s.SubmitHit(h, ask.overrides(done))
		return
	}
	ask.open = true
	a.asking = &ask
}

// openOrDownload opens the download in the queue a hit's mark names, or
// else downloads the hit.
func (a *App) openOrDownload(h client.SearchHit) {
	s := a.Store
	if mark, job := s.HitMark(h); mark == store.MarkActive && s.Job(job) != nil {
		a.OpenJob(job)
		return
	}
	if _, on := s.Search.Starting[store.HitKey(h)]; !on {
		a.download(h)
	}
}

// overrides are the submit's: the chosen codec first, then the rest of the
// server's priority in its order; and, for a hit in the library, whether
// its files are replaced, said either way so the server's own
// force_overwrite does not decide.
func (d *downloadAsk) overrides(inLibrary bool) *config.DownloadOverrides {
	var o config.DownloadOverrides
	set := false
	if d.codecs != nil {
		first := d.codecs[d.codec]
		list := []string{first}
		for _, c := range d.priority {
			if c != first {
				list = append(list, c)
			}
		}
		o.QualityPriority, set = &list, true
	}
	if inLibrary {
		replace := d.again && d.replace == 1
		o.ForceOverwrite, set = &replace, true
	}
	if !set {
		return nil
	}
	return &o
}

// downloadSheet asks what download waits on: its quality, or whether to
// replace the files on disk, or both in one sheet.
func (a *App) downloadSheet(c *ui.Context, k *Tokens) {
	d := a.asking
	if d == nil {
		return
	}
	ui.DialogBase(c.Key("download"), &d.open, func(back, panel ui.Element) {
		back.Background(ui.RGBA(0, 0, 0, 0.45))
		panel.Width(420).Padding(padCard).Gap(16).Radius(radiusCard).Background(k.Raise).Border(1, k.Line2).Shadow(0, 18, 48, 0, ui.RGBA(0, 0, 0, 0.4))
		ui.Text(c, "Download “"+clip(d.hit.Name, 44)+"”").FontSize(15).FontWeight(650).MaxLines(2).Ellipsis("…")
		if d.codecs != nil {
			ui.Column(c).Gap(8).Children(func() {
				label(c, k, "Quality")
				names := make([]string, len(d.codecs))
				for i, cd := range d.codecs {
					names[i] = codecLabel(cd)
				}
				// A row, so the track is as wide as its choices.
				ui.Row(c).Children(func() { segmented(c, k, "codec", &d.codec, names, nil) })
			})
		}
		if d.again {
			ui.Column(c).Gap(8).Children(func() {
				label(c, k, "In your library")
				ui.Row(c).Children(func() {
					segmented(c, k, "replace", &d.replace, []string{"Skip files on disk", "Replace them"}, nil)
				})
			})
		}
		ui.Row(c).Gap(8).Justify(ui.End).Padding(4, 0, 0, 0).Children(func() {
			if button(c, k, buttonPlain, "Cancel", nil).Clicked() {
				d.open = false
			}
			if button(c, k, buttonPrimary, "Download", icDown).Clicked() {
				mark, _ := a.Store.HitMark(d.hit)
				a.Store.SubmitHit(d.hit, d.overrides(mark == store.MarkDone))
				d.open = false
			}
		})
	})
	if !d.open {
		a.asking = nil
	}
}
