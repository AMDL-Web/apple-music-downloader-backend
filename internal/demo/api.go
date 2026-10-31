package demo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"amdl/internal/buildinfo"
	"amdl/internal/client"
	"amdl/internal/domain"
)

// keepalive spaces the comments a quiet stream sends, as the real streams
// do, well inside client.Client's watchdog.
const keepalive = 15 * time.Second

func (b *Backend) handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, client.Health{Status: "ok"})
	})
	m.HandleFunc("GET /api/v1/system", b.system)
	m.HandleFunc("GET /api/v1/wrapper/status", b.wrapperStatus)
	m.HandleFunc("POST /api/v1/wrapper/login", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "needs_2fa", "login_id": "demo-login"})
	})
	m.HandleFunc("POST /api/v1/wrapper/login/{id}/2fa", b.twoStep)
	m.HandleFunc("POST /api/v1/wrapper/logout", func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.signedIn = false
		b.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"status": "logged_out"})
	})
	m.HandleFunc("GET /api/v1/stats", b.stats)
	m.HandleFunc("GET /api/v1/library-sync", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, client.LibrarySync{IntervalMinutes: 60})
	})
	m.HandleFunc("POST /api/v1/library-sync/reset", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, client.LibrarySync{IntervalMinutes: 60})
	})
	m.HandleFunc("GET /api/v1/hooks", b.listHooks)
	m.HandleFunc("PUT /api/v1/hooks", b.setHooksEnabled)
	m.HandleFunc("PUT /api/v1/hooks/{name}", b.putHook)
	m.HandleFunc("DELETE /api/v1/hooks/{name}", b.deleteHook)
	m.HandleFunc("POST /api/v1/hooks/{name}/test", b.testHook)
	m.HandleFunc("GET /api/v1/logs", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"logs": []any{}})
	})
	m.HandleFunc("GET /api/v1/config", b.getConfig)
	m.HandleFunc("PUT /api/v1/config", b.putConfig)
	m.HandleFunc("POST /api/v1/config/reload", b.getConfig)
	m.HandleFunc("GET /api/v1/downloads", b.list)
	m.HandleFunc("POST /api/v1/downloads", b.submit)
	m.HandleFunc("GET /api/v1/downloads/events", b.feedStream)
	m.HandleFunc("GET /api/v1/downloads/{id}", b.detail)
	m.HandleFunc("GET /api/v1/downloads/{id}/events", b.jobStream)
	m.HandleFunc("POST /api/v1/downloads/{id}/cancel", b.act(func(j *job, now time.Time) error {
		if j.Status.IsTerminal() {
			return errNotCancellable
		}
		b.cancel(j, now)
		return nil
	}))
	m.HandleFunc("POST /api/v1/downloads/{id}/retry", b.act(func(j *job, now time.Time) error {
		if j.Status != domain.JobFailed && j.Status != domain.JobCancelled {
			return errNotRetryable
		}
		b.retry(j, now)
		return nil
	}))
	m.HandleFunc("DELETE /api/v1/downloads/{id}", b.remove)
	m.HandleFunc("POST /api/v1/lookup", b.lookup)
	m.HandleFunc("GET /api/v1/search", b.search)
	m.HandleFunc("GET /api/v1/artists/{id}/albums", b.artistAlbums)
	m.HandleFunc("GET /api/v1/albums/{id}", b.album)
	m.HandleFunc("GET /art/{id}/{size}", b.cover)
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "the demo has no "+r.Method+" "+r.URL.Path)
	})
	return m
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// The answer goes to a client in this process; a failed write means it
	// stopped reading.
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeErrorDetails(w, status, code, message, nil)
}

func writeErrorDetails(w http.ResponseWriter, status int, code, message string, details any) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": message, "details": details}})
}

func (b *Backend) system(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	c := b.counts()
	b.mu.Unlock()
	info := client.SystemInfo{
		Version: buildinfo.Get().Version, OS: "linux", Arch: "amd64", GoVersion: "go1.27",
		StartedAt: b.now().Add(-26 * time.Hour), UptimeSeconds: 26 * 3600, DefaultStorefront: "us",
		Tools: []domain.Capability{{Name: "ffmpeg", Available: true, Path: "/usr/bin/ffmpeg"}},
		Storage: []client.Storage{
			{Name: "downloads_dir", Path: musicDir, FreeBytes: 1_870_000_000_000, TotalBytes: 3_840_000_000_000},
			{Name: "temp_dir", Path: "/tmp/amdl", FreeBytes: 41_000_000_000, TotalBytes: 64_000_000_000},
		},
	}
	info.Jobs.Workers, info.Jobs.Active, info.Jobs.QueueDepth, info.Jobs.QueueCapacity = maxRunning, c.Running, c.Queued, 200
	info.Pools.Downloads, info.Pools.Decrypts, info.Pools.WrapperRequests = 4, 2, 4
	writeJSON(w, http.StatusOK, info)
}

func (b *Backend) wrapperStatus(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	in := b.signedIn
	b.mu.Unlock()
	s := client.WrapperStatus{Ready: true, LoggedIn: in, LoginSupported: true, Regions: []string{}, Accounts: []string{}}
	if in {
		s.Regions, s.Accounts, s.MediaUserToken = []string{"us"}, []string{"demo@example.com"}, "...d3mo"
	}
	writeJSON(w, http.StatusOK, s)
}

var sixDigits = regexp.MustCompile(`^[0-9]{6}$`)

func (b *Backend) twoStep(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code string `json:"two_step_code"`
	}
	if json.NewDecoder(r.Body).Decode(&body) != nil || !sixDigits.MatchString(body.Code) {
		writeError(w, http.StatusBadRequest, "invalid_request", "the code has six digits")
		return
	}
	b.mu.Lock()
	b.signedIn = true
	b.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged_in"})
}

func (b *Backend) stats(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	days = min(max(days, 1), 90)
	s := client.Stats{GeneratedAt: b.now(), Days: days}
	today := b.now().UTC()
	for i := days - 1; i >= 0; i-- {
		d := today.AddDate(0, 0, -i)
		n := int(hash(d.Format("2006-01-02")) % 40)
		s.Daily = append(s.Daily, client.StatsDay{Date: d.Format("2006-01-02"), TracksCompleted: n,
			BytesWritten: int64(n) * 62_000_000, JobsCompleted: n / 10})
		s.Totals.TracksCompleted += n
		s.Totals.BytesWritten += int64(n) * 62_000_000
		s.Totals.Jobs += n / 10
	}
	writeJSON(w, http.StatusOK, s)
}

func (b *Backend) getConfig(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"config": b.config, "persisted": true})
}

// putConfig merges the body's sections into the configuration, as PUT
// /config does, without its validation: the demo keeps any value.
func (b *Backend) putConfig(w http.ResponseWriter, r *http.Request) {
	var body map[string]map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "the body is not a configuration: "+err.Error())
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for section, leaves := range body {
		m, ok := b.config[section].(map[string]any)
		if !ok {
			m = map[string]any{}
			b.config[section] = m
		}
		for k, v := range leaves {
			m[k] = v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": b.config, "persisted": true})
}

func (b *Backend) list(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	l := client.DownloadList{StatusCounts: b.counts(), Limit: client.SnapshotLimit, EventCursor: b.seq, Downloads: []domain.Job{}}
	for _, j := range b.sortedJobs() {
		l.Downloads = append(l.Downloads, j.snapshot())
	}
	l.Total = len(l.Downloads)
	writeJSON(w, http.StatusOK, l)
}

func (b *Backend) detail(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	j := b.job(r.PathValue("id"))
	if j == nil {
		writeError(w, http.StatusNotFound, "job_not_found", "no such job")
		return
	}
	writeJSON(w, http.StatusOK, client.DownloadDetail{Job: j.snapshot(), Items: append([]domain.JobItem(nil), j.items...),
		Hooks: []domain.HookState{}, EventCursor: b.seq})
}

var (
	errNotCancellable = &apiError{http.StatusConflict, "job_not_cancellable", "the job has already finished"}
	errNotRetryable   = &apiError{http.StatusConflict, "job_not_retryable", "only a failed or cancelled job can be retried"}
)

type apiError struct {
	status        int
	code, message string
}

func (e *apiError) Error() string { return e.message }

func (b *Backend) act(fn func(*job, time.Time) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		defer b.mu.Unlock()
		j := b.job(r.PathValue("id"))
		if j == nil {
			writeError(w, http.StatusNotFound, "job_not_found", "no such job")
			return
		}
		if err := fn(j, b.now()); err != nil {
			e := err.(*apiError)
			writeError(w, e.status, e.code, e.message)
			return
		}
		writeJSON(w, http.StatusOK, j.snapshot())
	}
}

// remove deletes a finished job, as DELETE /downloads/{id} does: one that
// is queued or running is refused.
func (b *Backend) remove(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	defer b.mu.Unlock()
	j := b.job(r.PathValue("id"))
	switch {
	case j == nil:
		writeError(w, http.StatusNotFound, "job_not_found", "no such job")
		return
	case !j.Status.IsTerminal():
		writeError(w, http.StatusConflict, "job_active", "the job is still queued or running; cancel it first")
		return
	}
	b.jobs = slices.DeleteFunc(b.jobs, func(x *job) bool { return x == j })
	b.seq++
	msg := domain.DownloadFeedMessage{Type: "download_deleted", JobID: j.ID, EventID: b.seq, StatusCounts: b.counts()}
	for ch := range b.feeds {
		select {
		case ch <- msg:
		default:
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// resolve finds what a link names in the catalog: an album, or a song of
// one with ?i=. Any other Apple Music link stands for one of the albums, so
// a pasted link of a real album still shows the demo at work.
func resolve(link string) (*album, *song, bool) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || !strings.HasSuffix(u.Hostname(), "music.apple.com") {
		return nil, nil, false
	}
	if id := u.Query().Get("i"); id != "" {
		if s, ok := findSong(id); ok {
			return s.album, &s, true
		}
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if a := findAlbum(parts[len(parts)-1]); a != nil {
		return a, nil, true
	}
	return albums[hash(u.Path)%uint32(len(albums))], nil, true
}

func (b *Backend) submit(w http.ResponseWriter, r *http.Request) {
	var req domain.DownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "the body is not a download request: "+err.Error())
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.now()
	var res domain.BatchSubmitResponse
	for _, link := range req.URLs {
		a, one, ok := resolve(link)
		if !ok {
			res.Rejected++
			res.Results = append(res.Results, domain.SubmitResult{URL: link, Status: domain.SubmitInvalid,
				Error: &domain.SubmitError{Code: "invalid_url", Message: "not an Apple Music link"}})
			continue
		}
		input := a.url()
		if one != nil {
			input = one.url()
		}
		if j := b.active(input); j != nil {
			res.Rejected++
			res.Results = append(res.Results, domain.SubmitResult{URL: link, Status: domain.SubmitDuplicateActive, ExistingJobID: j.ID,
				Error: &domain.SubmitError{Code: domain.SubmitCodeDuplicateActive, Message: "already in the queue"}})
			continue
		}
		j := b.newJob(a, one, now)
		snap := j.snapshot()
		b.feed(j)
		res.Accepted++
		res.Results = append(res.Results, domain.SubmitResult{URL: link, Status: domain.SubmitAccepted, Job: &snap})
	}
	if res.Accepted == 0 {
		writeErrorDetails(w, http.StatusUnprocessableEntity, "no_urls_accepted", "no URL was accepted; see details.results", res)
		return
	}
	writeJSON(w, http.StatusAccepted, res)
}

func (b *Backend) lookup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "the body has no url")
		return
	}
	a, one, ok := resolve(body.URL)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_url", "not an Apple Music link")
		return
	}
	l := client.Lookup{Input: body.URL, Type: "album", Storefront: "us", ID: a.id, Title: a.title, ArtistName: a.artist.name,
		ArtistURL: a.artist.url(), ArtworkURL: a.artwork(), ArtworkBgColor: a.bg, ArtworkTextColor1: a.fg,
		ArtworkTextColor2: "ffffff", ReleaseDate: a.releaseDate(), Genre: a.artist.genre, TrackCount: len(a.tracks),
		AudioTraits: a.traits()}
	if one != nil {
		l.Type, l.TrackID, l.Title, l.AlbumName, l.TrackCount, l.DurationMS = "song", one.id, a.tracks[one.index], a.title, 0, durationMS(one.id)
	}
	writeJSON(w, http.StatusOK, l)
}

func (b *Backend) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	term := strings.ToLower(strings.TrimSpace(q.Get("term")))
	if term == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "term is required")
		return
	}
	types := map[string]bool{"songs": true, "albums": true, "playlists": true, "artists": true}
	if t := q.Get("types"); t != "" {
		types = map[string]bool{}
		for _, s := range strings.Split(t, ",") {
			types[s] = true
		}
	}
	limit, offset := pageOf(q, 10)
	match := func(s ...string) bool {
		for _, v := range s {
			if strings.Contains(strings.ToLower(v), term) {
				return true
			}
		}
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	g := b.hits(match)
	if len(g.artists)+len(g.albums)+len(g.playlists)+len(g.songs) == 0 {
		// Visitors search for real artists, which the made-up catalog lacks:
		// a term that matches nothing answers with the whole catalog, as the
		// website's console demo does (apps/site/demo/fake-api.js).
		g = b.hits(func(...string) bool { return true })
	}
	res := client.SearchResult{Term: q.Get("term"), Storefront: "us"}
	if types["artists"] {
		res.Artists = page(g.artists, limit, offset)
	}
	if types["albums"] {
		res.Albums = page(g.albums, limit, offset)
	}
	if types["playlists"] {
		res.Playlists = page(g.playlists, limit, offset)
	}
	if types["songs"] {
		res.Songs = page(g.songs, limit, offset)
	}
	writeJSON(w, http.StatusOK, res)
}

type searchHits struct{ artists, albums, playlists, songs []client.SearchHit }

// hits is the catalog that match keeps, by type. The caller holds b.mu.
func (b *Backend) hits(match func(...string) bool) searchHits {
	var g searchHits
	for _, a := range artists {
		if match(a.name, a.genre) {
			g.artists = append(g.artists, a.hit())
		}
	}
	for _, a := range albums {
		if match(a.title, a.artist.name, a.artist.genre) {
			g.albums = append(g.albums, b.marked(a.hit()))
		}
	}
	for _, p := range playlists {
		if match(p.name, p.curator) {
			g.playlists = append(g.playlists, b.marked(p.hit()))
		}
	}
	for _, a := range albums {
		for i, name := range a.tracks {
			if match(name, a.title, a.artist.name) {
				g.songs = append(g.songs, b.marked(song{id: songID(a, i), album: a, index: i}.hit()))
			}
		}
	}
	return g
}

// marked gives a hit the job that holds it, as the library and queue marks
// of search read it.
func (b *Backend) marked(h client.SearchHit) client.SearchHit {
	if j, ok := b.priorJob(h.ID); ok {
		h.PriorJob = &client.PriorJob{ID: j.ID, Status: j.Status, FinishedAt: j.FinishedAt}
	}
	return h
}

func pageOf(q url.Values, def int) (limit, offset int) {
	limit, _ = strconv.Atoi(q.Get("limit"))
	offset, _ = strconv.Atoi(q.Get("offset"))
	if limit <= 0 {
		limit = def
	}
	return min(limit, 50), max(offset, 0)
}

func page(hits []client.SearchHit, limit, offset int) *client.HitGroup {
	g := &client.HitGroup{Items: []client.SearchHit{}}
	if offset < len(hits) {
		end := min(offset+limit, len(hits))
		g.Items = hits[offset:end]
		if end < len(hits) {
			g.NextOffset = end
		}
	}
	return g
}

func (b *Backend) artistAlbums(w http.ResponseWriter, r *http.Request) {
	a := findArtist(r.PathValue("id"))
	if a == nil {
		writeError(w, http.StatusNotFound, "catalog_not_found", "no such artist")
		return
	}
	limit, offset := pageOf(r.URL.Query(), 25)
	b.mu.Lock()
	defer b.mu.Unlock()
	var hits []client.SearchHit
	for _, al := range a.albums() {
		hits = append(hits, b.marked(al.hit()))
	}
	writeJSON(w, http.StatusOK, client.ArtistAlbums{Storefront: "us", Artist: a.hit(), Albums: *page(hits, limit, offset)})
}

func (b *Backend) album(w http.ResponseWriter, r *http.Request) {
	a := findAlbum(r.PathValue("id"))
	if a == nil {
		writeError(w, http.StatusNotFound, "catalog_not_found", "no such album")
		return
	}
	writeJSON(w, http.StatusOK, a.detail())
}

// cover serves an album's cover at the size of its template, {w}x{h}bb.{f}.
func (b *Backend) cover(w http.ResponseWriter, r *http.Request) {
	a := findAlbum(r.PathValue("id"))
	var px int
	if _, err := fmt.Sscanf(r.PathValue("size"), "%dx", &px); a == nil || err != nil {
		http.NotFound(w, r)
		return
	}
	px = min(max(px, 16), coverMax)
	key := fmt.Sprintf("%s/%d", a.id, px)
	b.mu.Lock()
	img, ok := b.covers[key]
	b.mu.Unlock()
	if !ok {
		img = cover(a, px)
		b.mu.Lock()
		b.covers[key] = img
		b.mu.Unlock()
	}
	w.Header().Set("Content-Type", "image/png")
	// The client reads it in this process; a failed write means it stopped.
	_, _ = w.Write(img)
}

// feedStream is GET /downloads/events: every message from now on. It
// replays nothing: the demo's clients connect with the cursor of a
// snapshot they just read.
func (b *Backend) feedStream(w http.ResponseWriter, r *http.Request) {
	ch := make(chan domain.DownloadFeedMessage, 256)
	b.mu.Lock()
	b.feeds[ch] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.feeds, ch)
		b.mu.Unlock()
	}()
	s := openStream(w)
	tick := time.NewTicker(keepalive)
	defer tick.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			err = s.ping()
		case msg := <-ch:
			err = s.send(msg.EventID, "", msg)
		}
		if err != nil {
			return // the client stopped reading
		}
	}
}

// jobStream is GET /downloads/{id}/events: the job's events after the
// cursor, then each one as it happens. A finished job's stream ends once
// it has replayed them, and answers 204 when there are none.
func (b *Backend) jobStream(w http.ResponseWriter, r *http.Request) {
	cursor, _ := strconv.ParseInt(r.URL.Query().Get("cursor"), 10, 64)
	ch := make(chan domain.Event, 1024)
	b.mu.Lock()
	j := b.job(r.PathValue("id"))
	if j == nil {
		b.mu.Unlock()
		writeError(w, http.StatusNotFound, "job_not_found", "no such job")
		return
	}
	var backlog []domain.Event
	for _, ev := range j.events {
		if ev.ID > cursor {
			backlog = append(backlog, ev)
		}
	}
	done := j.Status.IsTerminal()
	if !done {
		j.subs[ch] = true
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(j.subs, ch)
		b.mu.Unlock()
	}()
	if done && len(backlog) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	s := openStream(w)
	for _, ev := range backlog {
		if s.send(ev.ID, string(ev.Type), ev) != nil {
			return
		}
	}
	if done {
		return
	}
	tick := time.NewTicker(keepalive)
	defer tick.Stop()
	for {
		var err error
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			err = s.ping()
		case ev := <-ch:
			if ev.ID <= cursor {
				continue
			}
			err = s.send(ev.ID, string(ev.Type), ev)
			switch ev.Type {
			case domain.EventJobFinished, domain.EventJobFailed, domain.EventJobCancelled:
				return
			}
		}
		if err != nil {
			return // the client stopped reading
		}
	}
}

// stream writes Server-Sent Events.
type stream struct{ w http.ResponseWriter }

func openStream(w http.ResponseWriter) stream {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	s := stream{w}
	s.flush()
	return s
}

func (s stream) send(id int64, name string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if name != "" {
		_, err = fmt.Fprintf(s.w, "id: %d\nevent: %s\ndata: %s\n\n", id, name, raw)
	} else {
		_, err = fmt.Fprintf(s.w, "id: %d\ndata: %s\n\n", id, raw)
	}
	s.flush()
	return err
}

func (s stream) ping() error {
	_, err := fmt.Fprint(s.w, ": keepalive\n\n")
	s.flush()
	return err
}

func (s stream) flush() {
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
}
