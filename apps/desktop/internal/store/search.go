package store

import (
	"strings"
	"time"
	"unicode/utf8"

	"amdl/internal/client"
	"amdl/internal/config"
	"amdl/internal/domain"
)

// Catalog search pages and marks hits as the web console's lib/search.js
// does: keep the page sizes in step. The console shows no playlists and no
// group alone.
const (
	// SearchMaxTerm is the longest term the backend searches (GET search).
	SearchMaxTerm = 200
	// SearchFirst is the hits per group of a new term; SearchMore what a
	// group's next page adds; ArtistPage the albums per page of an artist.
	SearchFirst, SearchMore, ArtistPage = 5, 25, 25
	// searchDelay waits for typing to pause before a search starts.
	searchDelay = 300 * time.Millisecond
	// searchCacheMax bounds the answers kept, oldest dropped first.
	searchCacheMax = 30
)

// SearchGroups are the result groups in the order they are shown.
var SearchGroups = []string{"albums", "songs", "playlists", "artists"}

// SearchStatus is how far a search or an artist's first page has come.
type SearchStatus int

// Search statuses.
const (
	SearchIdle SearchStatus = iota
	SearchLoading
	SearchReady
	SearchFailed
)

// PageState is a further page being read, or the last one that failed.
type PageState int

// Page states.
const (
	PageIdle PageState = iota
	PageLoading
	PageFailed
)

// Search is the catalog search home shows for words in its field, and the
// artist opened from it.
type Search struct {
	// Term is the words searched; "" when idle.
	Term   string
	Status SearchStatus
	// Result is the answer for Term. While a new term loads it keeps the last
	// answer and Stale is set, so the list keeps its height until the new one
	// lands.
	Result *client.SearchResult
	Stale  bool
	Err    string
	// More is a group's next page while it loads or after it failed.
	More map[string]PageState
	// Focus is the group shown alone, page after page, or "" for the first
	// hits of every group. It stays as the words change.
	Focus string
	// Artist is the artist whose albums show instead of the hits.
	Artist ArtistPages
	// Album is the album the album screen shows, opened from a hit.
	Album AlbumPage
	// Starting holds the hits whose download is being submitted, by HitKey,
	// with when each left.
	Starting map[string]time.Time

	want  string
	gen   int
	cache *lru[client.SearchResult]
	// sent maps a HitKey to the job a download from here started.
	sent map[string]string
	// gone holds the jobs deleted while the app watched.
	gone map[string]bool
}

// ArtistPages is one artist and the pages of its albums read so far.
type ArtistPages struct {
	ID     string
	Status SearchStatus
	Artist client.SearchHit
	Albums client.HitGroup
	Err    string
	More   PageState

	gen   int
	cache *lru[client.ArtistAlbums]
}

func (s *Search) reset() {
	*s = Search{
		cache:    newLRU[client.SearchResult](searchCacheMax),
		sent:     map[string]string{},
		gone:     map[string]bool{},
		Starting: map[string]time.Time{},
	}
	s.Artist.cache = newLRU[client.ArtistAlbums](searchCacheMax)
	s.Album = newAlbumPage()
}

// SearchTerm is what the field's text searches for: its words joined by
// single spaces. It is "" when the text holds a link, which previews instead,
// and when it runs past SearchMaxTerm characters (tooLong).
func SearchTerm(text string, hasLinks bool) (term string, tooLong bool) {
	if hasLinks {
		return "", false
	}
	term = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(term) > SearchMaxTerm {
		return "", true
	}
	return term, false
}

// HitKey is the key a hit is tracked by: its type and id, as a job of it is.
func HitKey(h client.SearchHit) string { return h.Type + ":" + h.ID }

// WantSearch asks for term's hits. The screen calls it every frame it shows
// the search: a new term starts once typing pauses, an answer already read
// for it comes back at once, and "" clears the search.
func (s *Store) WantSearch(term string) {
	q := &s.Search
	if term == q.want {
		return
	}
	q.want = term
	q.gen++
	// A new term shows its own hits, not the artist opened from the last.
	s.OpenArtist("")
	if term == "" {
		*q = Search{cache: q.cache, sent: q.sent, gone: q.gone, Starting: q.Starting, Artist: q.Artist, Album: q.Album, Focus: q.Focus, want: term, gen: q.gen}
		return
	}
	if r, ok := q.cache.get(term); ok {
		q.Term, q.Status, q.Result, q.Stale, q.Err, q.More = term, SearchReady, &r, false, "", nil
		return
	}
	if s.client == nil {
		q.Term, q.Status, q.Result, q.Stale, q.Err, q.More = term, SearchFailed, nil, false, "Not connected to a server", nil
		return
	}
	// The list dims at once and keeps its height while typing pauses.
	q.Term, q.Status, q.Stale, q.Err, q.More = term, SearchLoading, q.Result != nil, "", nil
	gen, ctx := q.gen, s.ctx
	time.AfterFunc(searchDelay, func() {
		s.postIf(ctx, func() {
			if s.Search.gen == gen {
				s.startSearch(term)
			}
		})
	})
}

// RetrySearch searches the current term again after an error.
func (s *Store) RetrySearch() {
	if q := &s.Search; q.Status == SearchFailed && q.Term != "" {
		q.gen++
		s.startSearch(q.Term)
	}
}

func (s *Store) startSearch(term string) {
	q := &s.Search
	q.Term, q.Status, q.Stale, q.Err, q.More = term, SearchLoading, q.Result != nil, "", nil
	c, ctx, gen := s.client, s.ctx, q.gen
	if c == nil {
		q.Status, q.Stale, q.Err = SearchFailed, false, "Not connected to a server"
		return
	}
	go func() {
		r, err := c.Search(ctx, client.SearchQuery{Term: term, Limit: SearchFirst})
		s.postIf(ctx, func() { s.applySearch(gen, term, r, err) })
	}()
}

// applySearch takes the answer of the search started at gen, unless the
// field has moved on since.
func (s *Store) applySearch(gen int, term string, r client.SearchResult, err error) {
	q := &s.Search
	if q.gen != gen {
		return
	}
	if err != nil {
		q.Status, q.Result, q.Stale, q.Err = SearchFailed, nil, false, err.Error()
		return
	}
	q.cache.put(term, r)
	q.Status, q.Result, q.Stale, q.Err = SearchReady, &r, false, ""
}

// MoreSearch reads group's next page and adds it after the hits shown.
func (s *Store) MoreSearch(group string) {
	q := &s.Search
	if q.Status != SearchReady || q.Result == nil || q.More[group] == PageLoading {
		return
	}
	g := hitGroup(q.Result, group)
	if g == nil || g.NextOffset == 0 || s.client == nil {
		return
	}
	if q.More == nil {
		q.More = map[string]PageState{}
	}
	q.More[group] = PageLoading
	c, ctx, gen, term, offset := s.client, s.ctx, q.gen, q.Term, g.NextOffset
	go func() {
		r, err := c.Search(ctx, client.SearchQuery{Term: term, Types: []string{group}, Offset: offset, Limit: SearchMore})
		s.postIf(ctx, func() { s.applyMore(gen, term, group, hitGroup(&r, group), err) })
	}()
}

func (s *Store) applyMore(gen int, term, group string, page *client.HitGroup, err error) {
	q := &s.Search
	if q.gen != gen || q.Result == nil {
		return
	}
	if err != nil {
		q.More[group] = PageFailed
		return
	}
	delete(q.More, group)
	next := *q.Result
	merged := appendPage(hitGroup(&next, group), page)
	switch group {
	case "songs":
		next.Songs = merged
	case "albums":
		next.Albums = merged
	case "playlists":
		next.Playlists = merged
	case "artists":
		next.Artists = merged
	}
	q.Result = &next
	q.cache.put(term, next)
}

// hitGroup is r's group of that name, or nil.
func hitGroup(r *client.SearchResult, group string) *client.HitGroup {
	switch group {
	case "songs":
		return r.Songs
	case "albums":
		return r.Albums
	case "playlists":
		return r.Playlists
	case "artists":
		return r.Artists
	}
	return nil
}

// HitGroup is r's group of that name, or nil.
func HitGroup(r *client.SearchResult, group string) *client.HitGroup { return hitGroup(r, group) }

// FocusGroup shows group alone, its pages following as they are read;
// "" shows every group again.
func (s *Store) FocusGroup(group string) { s.Search.Focus = group }

// appendPage is g with page's hits after its own, each id once, and page's
// next offset.
func appendPage(g, page *client.HitGroup) *client.HitGroup {
	out := &client.HitGroup{}
	seen := map[string]bool{}
	for _, gg := range []*client.HitGroup{g, page} {
		if gg == nil {
			continue
		}
		for _, h := range gg.Items {
			if !seen[h.ID] {
				seen[h.ID] = true
				out.Items = append(out.Items, h)
			}
		}
	}
	if page != nil {
		out.NextOffset = page.NextOffset
	}
	return out
}

// ShownHits are the hits in the order the rows show them and the arrow keys
// move through: the open artist's albums, else the focused group's hits,
// else every group's.
func (q *Search) ShownHits() []client.SearchHit {
	if q.Artist.ID != "" {
		return q.Artist.Albums.Items
	}
	if q.Result == nil {
		return nil
	}
	if q.Focus != "" {
		if hg := hitGroup(q.Result, q.Focus); hg != nil {
			return hg.Items
		}
		return nil
	}
	var out []client.SearchHit
	for _, g := range SearchGroups {
		if hg := hitGroup(q.Result, g); hg != nil {
			out = append(out, hg.Items...)
		}
	}
	return out
}

// OpenArtist shows the artist's albums instead of the hits; "" goes back.
func (s *Store) OpenArtist(id string) {
	a := &s.Search.Artist
	if id == a.ID && a.Status != SearchIdle {
		return
	}
	a.gen++
	if id == "" {
		*a = ArtistPages{cache: a.cache, gen: a.gen}
		return
	}
	if e, ok := a.cache.get(id); ok {
		a.ID, a.Status, a.Artist, a.Albums, a.Err, a.More = id, SearchReady, e.Artist, e.Albums, "", PageIdle
		return
	}
	s.firstArtistPage(id)
}

// RetryArtist reads the artist's first page again after an error.
func (s *Store) RetryArtist() {
	if a := &s.Search.Artist; a.Status == SearchFailed && a.ID != "" {
		a.gen++
		s.firstArtistPage(a.ID)
	}
}

func (s *Store) firstArtistPage(id string) {
	a := &s.Search.Artist
	a.ID, a.Status, a.Artist, a.Albums, a.Err, a.More = id, SearchLoading, client.SearchHit{}, client.HitGroup{}, "", PageIdle
	c, ctx, gen := s.client, s.ctx, a.gen
	if c == nil {
		a.Status, a.Err = SearchFailed, "Not connected to a server"
		return
	}
	go func() {
		r, err := c.ArtistAlbums(ctx, id, client.PageQuery{Limit: ArtistPage})
		s.postIf(ctx, func() { s.applyArtist(gen, id, r, err, false) })
	}()
}

// MoreArtist reads the artist's next page of albums.
func (s *Store) MoreArtist() {
	a := &s.Search.Artist
	if a.Status != SearchReady || a.Albums.NextOffset == 0 || a.More == PageLoading || s.client == nil {
		return
	}
	a.More = PageLoading
	c, ctx, gen, id, offset := s.client, s.ctx, a.gen, a.ID, a.Albums.NextOffset
	go func() {
		r, err := c.ArtistAlbums(ctx, id, client.PageQuery{Limit: ArtistPage, Offset: offset})
		s.postIf(ctx, func() { s.applyArtist(gen, id, r, err, true) })
	}()
}

// applyArtist takes a page of the artist opened at gen; more adds it to the
// albums shown instead of replacing them.
func (s *Store) applyArtist(gen int, id string, r client.ArtistAlbums, err error, more bool) {
	a := &s.Search.Artist
	if a.gen != gen || a.ID != id {
		return
	}
	switch {
	case err != nil && more:
		a.More = PageFailed
		return
	case err != nil:
		a.Status, a.Err = SearchFailed, err.Error()
		return
	case more:
		a.Albums, a.More = *appendPage(&a.Albums, &r.Albums), PageIdle
	default:
		a.Status, a.Artist, a.Albums = SearchReady, r.Artist, *appendPage(nil, &r.Albums)
	}
	a.cache.put(id, client.ArtistAlbums{Storefront: r.Storefront, Artist: a.Artist, Albums: a.Albums})
}

// MarkState is whether a hit is in the library or the queue.
type MarkState int

// Mark states.
const (
	MarkNone MarkState = iota
	// MarkActive: a job of the hit is queued or running.
	MarkActive
	// MarkDone: a job of the hit completed, so it is in the library.
	MarkDone
)

// HitMark is whether h is in the library or the queue, and through which
// job. A job this app started for it, then the store's live copy of the job,
// win over the snapshot the search carried; a failed, cancelled or deleted
// job marks nothing.
func (s *Store) HitMark(h client.SearchHit) (MarkState, string) {
	q := &s.Search
	id := q.sent[HitKey(h)]
	if id == "" && h.PriorJob != nil {
		id = h.PriorJob.ID
	}
	if id == "" || q.gone[id] {
		return MarkNone, ""
	}
	status := domain.JobStatus("")
	if j := s.byID[id]; j != nil {
		status = j.Status
	} else if h.PriorJob != nil && h.PriorJob.ID == id {
		status = h.PriorJob.Status
	}
	switch status {
	case domain.JobQueued, domain.JobRunning:
		return MarkActive, id
	case domain.JobCompleted:
		return MarkDone, id
	}
	return MarkNone, ""
}

// SubmitHit downloads exactly h under overrides (nil: the backend's
// settings).
func (s *Store) SubmitHit(h client.SearchHit, overrides *config.DownloadOverrides) {
	key := HitKey(h)
	q := &s.Search
	if _, on := q.Starting[key]; on {
		return
	}
	q.Starting[key] = s.now()
	s.submit([]string{h.URL}, overrides, func(res domain.BatchSubmitResponse, err error) {
		delete(q.Starting, key)
		for _, r := range res.Results {
			if r.Job != nil {
				q.sent[key] = r.Job.ID
				delete(q.gone, r.Job.ID)
			}
		}
		switch {
		case err != nil && res.Rejected == 0:
			s.setNotice(err.Error(), true)
		case res.Accepted > 0:
			s.setNotice("Downloading "+h.Name, false)
		case len(res.Results) > 0 && res.Results[0].Error != nil:
			s.setNotice(res.Results[0].Error.Message, true)
		}
	})
}

// lru is a small most-recently-used cache.
type lru[V any] struct {
	max   int
	order []string
	items map[string]V
}

func newLRU[V any](max int) *lru[V] { return &lru[V]{max: max, items: map[string]V{}} }

func (c *lru[V]) get(k string) (V, bool) {
	v, ok := c.items[k]
	if ok {
		c.touch(k)
	}
	return v, ok
}

func (c *lru[V]) put(k string, v V) {
	c.items[k] = v
	c.touch(k)
	for len(c.order) > c.max {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
}

func (c *lru[V]) touch(k string) {
	for i, o := range c.order {
		if o == k {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, k)
}
