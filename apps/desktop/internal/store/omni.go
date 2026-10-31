package store

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"amdl/internal/client"
	"amdl/internal/config"
	"amdl/internal/domain"
)

// Omnibox is home's one field: pasted Apple Music links become
// downloads to preview and start, anything else searches the list.
type Omnibox struct {
	// Text is what the field holds.
	Text string
	// Links are the Apple Music links in Text, in order, without repeats.
	Links []*Link
	// Submitting is set while a submit is under way, and SubmitSince is
	// when it left.
	Submitting  bool
	SubmitSince time.Time
	// Result is the outcome of the last submit, or nil.
	Result *domain.BatchSubmitResponse

	cache map[string]*Link
	gen   int
}

func (o *Omnibox) reset() {
	*o = Omnibox{cache: map[string]*Link{}}
}

// Query is the search text: Text when it holds no link.
func (o *Omnibox) Query() string {
	if len(o.Links) > 0 {
		return ""
	}
	return strings.TrimSpace(o.Text)
}

// LinkState is how far a link's preview has come.
type LinkState int

// Link states.
const (
	LinkPending LinkState = iota
	LinkReady
	// LinkUnsupported: the backend parsed the link but does not download its
	// type, such as a music video.
	LinkUnsupported
	LinkFailed
)

// Link is one pasted link and its preview.
type Link struct {
	URL    string
	State  LinkState
	Lookup client.Lookup
	// Err says why the preview failed.
	Err string
}

// linkPattern finds Apple Music links in pasted text.
var linkPattern = regexp.MustCompile(`https?://(?:[a-z0-9-]+\.)*music\.apple\.com/[^\s"'<>]+`)

// FindLinks returns the Apple Music links in text, in order, without
// repeats.
func FindLinks(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range linkPattern.FindAllString(text, -1) {
		m = strings.TrimRight(m, ".,;)")
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	return out
}

// lookupDelay waits for typing to pause before links are looked up.
const lookupDelay = 300 * time.Millisecond

// SetOmniText changes the field's text, finds its links and looks up the new
// ones once typing pauses.
func (s *Store) SetOmniText(text string) {
	o := &s.Omni
	if text == o.Text {
		return
	}
	o.Text = text
	o.Result = nil
	o.Links = o.Links[:0]
	var fresh []*Link
	for _, u := range FindLinks(text) {
		l := o.cache[u]
		if l == nil {
			l = &Link{URL: u}
			o.cache[u] = l
			fresh = append(fresh, l)
		}
		o.Links = append(o.Links, l)
	}
	if len(fresh) == 0 || s.client == nil {
		return
	}
	o.gen++
	gen, c, ctx := o.gen, s.client, s.ctx
	time.AfterFunc(lookupDelay, func() {
		s.postIf(ctx, func() {
			if s.Omni.gen != gen {
				return // the text changed again; that change looks them up
			}
			for _, l := range fresh {
				s.lookup(ctx, c, l)
			}
		})
	})
}

func (s *Store) lookup(ctx context.Context, c *client.Client, l *Link) {
	if l.State != LinkPending {
		return
	}
	go func() {
		res, err := c.Lookup(ctx, l.URL)
		s.postIf(ctx, func() {
			switch {
			case err == nil:
				l.State, l.Lookup = LinkReady, res
			case client.ErrorCode(err) == "invalid_request":
				l.State, l.Err = LinkUnsupported, err.Error()
			default:
				l.State, l.Err = LinkFailed, err.Error()
			}
		})
	}()
}

// Running returns the queued or running job that downloads the same thing as
// the link, or nil.
func (s *Store) Running(l *Link) *Job {
	if l.State != LinkReady {
		return nil
	}
	for _, j := range s.Jobs {
		if j.Status != domain.JobQueued && j.Status != domain.JobRunning {
			continue
		}
		if t, id, ok := parseLink(j.Input); ok && t == l.Lookup.Type && id == l.Lookup.ID {
			return j
		}
	}
	return nil
}

// jobLinkPattern takes the type and id from a music.apple.com path:
// /cn/album/name/1858184006, /cn/album/1858184006?i=…, /us/playlist/x/pl.….
var jobLinkPattern = regexp.MustCompile(`music\.apple\.com/[a-z]{2}/(album|song|playlist|artist|station)/(?:[^/?#]+/)?([^/?#]+)(?:\?i=(\d+))?`)

func parseLink(u string) (typ, id string, ok bool) {
	m := jobLinkPattern.FindStringSubmatch(u)
	if m == nil {
		return "", "", false
	}
	if m[1] == "album" && m[3] != "" {
		// The backend decides by catalog.album_track_url_mode whether this is
		// the album or the song; compare the album, which both previews show.
		return "album", m[2], true
	}
	return m[1], m[2], true
}

// Startable returns the links a submit would start: previewed, supported
// and not already running.
func (s *Store) Startable() []*Link {
	var out []*Link
	for _, l := range s.Omni.Links {
		if l.State == LinkReady && s.Running(l) == nil {
			out = append(out, l)
		}
	}
	return out
}

// Submit starts the startable links with the backend's own settings. When every link
// is accepted the field clears; otherwise it keeps the text and Result says
// why each link was refused.
func (s *Store) Submit() {
	links := s.Startable()
	if len(links) == 0 || s.client == nil || s.Omni.Submitting {
		return
	}
	urls := make([]string, len(links))
	for i, l := range links {
		urls[i] = l.URL
	}
	o := &s.Omni
	o.Submitting, o.SubmitSince = true, s.now()
	s.submit(urls, nil, func(res domain.BatchSubmitResponse, err error) {
		o.Submitting = false
		o.Result = &res
		switch {
		case err != nil && res.Rejected == 0:
			s.setNotice(err.Error(), true)
		case res.Rejected == 0:
			s.setNotice(plural(res.Accepted, "download")+" started", false)
			o.Text, o.Links, o.Result = "", nil, nil
		default:
			s.setNotice(plural(res.Accepted, "download")+" started, "+plural(res.Rejected, "link")+" refused", res.Accepted == 0)
		}
	})
}

// submit starts urls under overrides (nil: the backend's settings), adds
// the jobs it created and hands the outcome to then on the UI thread.
func (s *Store) submit(urls []string, overrides *config.DownloadOverrides, then func(domain.BatchSubmitResponse, error)) {
	c, ctx := s.client, s.ctx
	if c == nil {
		return
	}
	go func() {
		res, err := c.Submit(ctx, urls, overrides)
		s.postIf(ctx, func() {
			for _, r := range res.Results {
				if r.Job != nil {
					s.upsert(*r.Job)
				}
			}
			then(res, err)
		})
	}()
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
