package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"amdl/internal/client"
)

// pageSize is how many hits a filter's tab reads at a time, as the desktop
// app's search does; the all tab keeps the first few of each group.
const pageSize = 25

type pageMsg struct {
	r      *results
	typ    string // a filter name
	offset int
	group  *client.HitGroup
	err    error
}

// morePages reads the tab under r's filter a page at a time: its first page
// when the tab opens, the next when the cursor nears its end.
func (m *model) morePages(r *results) tea.Cmd {
	f := filters[r.filter]
	if f == "all" || r.artist != nil || r.loading {
		return nil
	}
	if !r.paged[f] {
		return m.loadPage(r, f, 0)
	}
	if n := r.next[f]; n > 0 && r.cursor >= len(r.visible())-3 {
		return m.loadPage(r, f, n)
	}
	return nil
}

func (m *model) loadPage(r *results, typ string, offset int) tea.Cmd {
	r.loading = true
	ctx, c, term := m.ctx, m.c, r.term
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		res, err := c.Search(ctx, client.SearchQuery{Term: term, Types: []string{typ + "s"}, Limit: pageSize, Offset: offset})
		g := map[string]*client.HitGroup{"album": res.Albums, "song": res.Songs, "playlist": res.Playlists, "artist": res.Artists}[typ]
		return pageMsg{r: r, typ: typ, offset: offset, group: g, err: err}
	}
}

// onPage adds a page to the results it was read for, which may no longer be
// the ones on screen, then reads what the tab now on screen still needs: a
// tab opened while the page was in flight waits for no keypress.
func (m *model) onPage(msg pageMsg) tea.Cmd {
	r := msg.r
	r.loading = false
	if msg.err != nil {
		r.err = errText(msg.err)
		return nil
	}
	if r.paged == nil {
		r.paged, r.next = map[string]bool{}, map[string]int{}
	}
	r.paged[msg.typ], r.next[msg.typ] = true, 0
	if msg.group != nil {
		r.next[msg.typ] = msg.group.NextOffset
		chosen := map[string]bool{}
		for _, p := range r.items {
			if p.selected {
				chosen[p.hit.URL] = true
			}
		}
		for _, h := range msg.group.Items {
			r.items = append(r.items, pick{hit: h, selected: chosen[h.URL], page: true})
		}
	}
	if r != m.res {
		return nil
	}
	return m.morePages(r)
}
