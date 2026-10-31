package api

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"amdl/internal/applemusic"
	"amdl/internal/db"
	"amdl/internal/domain"
	"amdl/internal/jobs"
	"amdl/internal/logging"
)

// searchTimeout bounds GET /search and GET /artists/{id}/albums: the console
// searches as the person types, and an answer later than this is no use.
const searchTimeout = 10 * time.Second

// Page sizes per result group. amp-api refuses a search limit above 50; the
// albums relationship takes up to 100.
const (
	searchDefaultLimit       = 10
	searchMaxLimit           = 50
	artistAlbumsDefaultLimit = 25
	artistAlbumsMaxLimit     = 100
	searchMaxTermRunes       = 200
)

// catalogIDPattern is a catalog artist or album id: Apple's are all digits,
// and the id goes into the upstream request path.
var catalogIDPattern = regexp.MustCompile(`^[0-9]{1,20}$`)

// searchHit is the SearchSong, SearchAlbum, SearchPlaylist and SearchArtist
// schemas: the fields every hit has, then the ones its type has. The
// metadata fields carry the names and meaning of the LookupResponse fields
// of the same name.
type searchHit struct {
	ID                 string             `json:"id"`
	Type               applemusic.URLType `json:"type"`
	Name               string             `json:"name"`
	URL                string             `json:"url"`
	ArtworkURL         string             `json:"artwork_url,omitempty"`
	ArtworkBgColor     string             `json:"artwork_bg_color,omitempty"`
	ArtworkTextColor1  string             `json:"artwork_text_color1,omitempty"`
	ArtworkTextColor2  string             `json:"artwork_text_color2,omitempty"`
	ArtworkTextColor3  string             `json:"artwork_text_color3,omitempty"`
	ArtworkTextColor4  string             `json:"artwork_text_color4,omitempty"`
	ArtistName         string             `json:"artist_name,omitempty"`
	AlbumName          string             `json:"album_name,omitempty"`
	ReleaseDate        string             `json:"release_date,omitempty"`
	Genre              string             `json:"genre,omitempty"`
	TrackCount         int                `json:"track_count,omitempty"`
	DurationMS         int                `json:"duration_ms,omitempty"`
	ContentRating      string             `json:"content_rating,omitempty"`
	AppleDigitalMaster bool               `json:"apple_digital_master,omitempty"`
	AudioTraits        []string           `json:"audio_traits,omitempty"`
	PriorJob           *priorJob          `json:"prior_job,omitempty"`
}

// priorJob is the PriorJob schema: the job that put a hit in the library or
// holds it in the queue.
type priorJob struct {
	ID         string           `json:"id"`
	Status     domain.JobStatus `json:"status"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`
}

// hitGroup is the SearchGroup schema.
type hitGroup struct {
	Items      []*searchHit `json:"items"`
	NextOffset int          `json:"next_offset,omitempty"`
}

type searchResponse struct {
	Term       string    `json:"term"`
	Storefront string    `json:"storefront"`
	Songs      *hitGroup `json:"songs,omitempty"`
	Albums     *hitGroup `json:"albums,omitempty"`
	Playlists  *hitGroup `json:"playlists,omitempty"`
	Artists    *hitGroup `json:"artists,omitempty"`
}

type artistAlbumsResponse struct {
	Storefront string     `json:"storefront"`
	Artist     *searchHit `json:"artist"`
	Albums     hitGroup   `json:"albums"`
}

func (h *searchHit) setArtwork(url string, colors applemusic.ArtworkColors) {
	h.ArtworkURL = url
	h.ArtworkBgColor = colors.BgColor
	h.ArtworkTextColor1 = colors.TextColor1
	h.ArtworkTextColor2 = colors.TextColor2
	h.ArtworkTextColor3 = colors.TextColor3
	h.ArtworkTextColor4 = colors.TextColor4
}

func songHit(storefront string, song applemusic.Song) *searchHit {
	h := &searchHit{
		ID: song.ID, Type: applemusic.TypeSong, Name: song.Name, URL: hitURL(applemusic.TypeSong, storefront, song.ID, song.URL),
		ArtistName: song.ArtistName, AlbumName: song.AlbumName, ReleaseDate: song.ReleaseDate,
		Genre: applemusic.PrimaryGenre(song.GenreNames), DurationMS: song.DurationInMillis,
		ContentRating: song.ContentRating, AppleDigitalMaster: song.AppleDigitalMaster, AudioTraits: song.AudioTraits,
	}
	h.setArtwork(song.DisplayArtwork())
	return h
}

func albumHit(storefront string, album applemusic.Collection) *searchHit {
	h := &searchHit{
		ID: album.ID, Type: applemusic.TypeAlbum, Name: album.Name, URL: hitURL(applemusic.TypeAlbum, storefront, album.ID, album.URL),
		ArtistName: album.Artist, ReleaseDate: album.ReleaseDate, Genre: applemusic.PrimaryGenre(album.GenreNames),
		TrackCount: album.TrackCount, ContentRating: album.ContentRating, AppleDigitalMaster: album.AppleDigitalMaster,
		AudioTraits: album.AudioTraits,
	}
	h.setArtwork(album.ArtworkURL, album.ArtworkColors)
	return h
}

func playlistHit(storefront string, playlist applemusic.Collection) *searchHit {
	h := &searchHit{
		ID: playlist.ID, Type: applemusic.TypePlaylist, Name: playlist.Name, URL: hitURL(applemusic.TypePlaylist, storefront, playlist.ID, playlist.URL),
		ArtistName: playlist.Artist, AudioTraits: playlist.AudioTraits,
	}
	h.setArtwork(playlist.ArtworkURL, playlist.ArtworkColors)
	return h
}

func artistHit(storefront string, artist applemusic.Artist) *searchHit {
	h := &searchHit{
		ID: artist.ID, Type: applemusic.TypeArtist, Name: artist.Name, URL: hitURL(applemusic.TypeArtist, storefront, artist.ID, artist.URL),
		Genre: applemusic.PrimaryGenre(artist.GenreNames),
	}
	h.setArtwork(artist.ArtworkURL, artist.ArtworkColors)
	return h
}

// hitURL is the link a submission of the hit downloads exactly that item from:
// Apple's page without its query (the l= language Apple appends), or one built
// from the kind and id when Apple sent none. A song gets a /song/ link, since
// Apple's own is an album link whose i parameter catalog.album_track_url_mode
// may read as the whole album.
func hitURL(kind applemusic.URLType, storefront, id, appleURL string) string {
	u, err := url.Parse(appleURL)
	if err != nil || u.Host != "music.apple.com" || appleURL == "" {
		return fmt.Sprintf("https://music.apple.com/%s/%s/%s", storefront, kind, id)
	}
	if kind == applemusic.TypeSong {
		// Apple's path is /<storefront>/album/<song slug>/<album id>.
		parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
		if len(parts) == 4 && parts[1] == "album" && parts[2] != "" {
			return fmt.Sprintf("https://music.apple.com/%s/song/%s/%s", storefront, parts[2], id)
		}
		return fmt.Sprintf("https://music.apple.com/%s/song/%s", storefront, id)
	}
	u.RawQuery, u.Fragment = "", ""
	return u.String()
}

// search answers GET /search: one page of catalog hits per requested group.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	term := strings.Join(strings.Fields(q.Get("term")), " ")
	if term == "" {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "term is required")
		return
	}
	if utf8.RuneCountInString(term) > searchMaxTermRunes {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, fmt.Sprintf("term must be at most %d characters", searchMaxTermRunes))
		return
	}
	types, err := parseSearchTypes(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	storefront, err := s.browseStorefront(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	limit, offset, err := parseBrowsePage(q, searchDefaultLimit, searchMaxLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()
	res, err := s.catalog.Search(ctx, storefront, term, types, limit, offset)
	if err != nil {
		s.writeCatalogError(ctx, w, r, err, searchTimeout, nil, "search failed", "term", term, "storefront", storefront)
		return
	}
	resp := searchResponse{Term: term, Storefront: storefront}
	var hits []*searchHit
	for _, t := range types {
		group := &hitGroup{Items: []*searchHit{}, NextOffset: res.NextOffset[t]}
		switch t {
		case applemusic.SearchSongs:
			for _, song := range res.Songs {
				group.Items = append(group.Items, songHit(storefront, song))
			}
			resp.Songs = group
		case applemusic.SearchAlbums:
			for _, album := range res.Albums {
				group.Items = append(group.Items, albumHit(storefront, album))
			}
			resp.Albums = group
		case applemusic.SearchPlaylists:
			for _, playlist := range res.Playlists {
				group.Items = append(group.Items, playlistHit(storefront, playlist))
			}
			resp.Playlists = group
		case applemusic.SearchArtists:
			for _, artist := range res.Artists {
				group.Items = append(group.Items, artistHit(storefront, artist))
			}
			resp.Artists = group
		}
		hits = append(hits, group.Items...)
	}
	s.markPriorJobs(r.Context(), hits)
	writeJSON(w, http.StatusOK, resp)
}

// artistAlbums answers GET /artists/{artist_id}/albums: the artist and one
// page of its albums, the list an artist download takes.
func (s *Server) artistAlbums(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("artist_id")
	if !catalogIDPattern.MatchString(id) {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, "artist_id must be an Apple Music catalog id (digits)")
		return
	}
	q := r.URL.Query()
	storefront, err := s.browseStorefront(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	limit, offset, err := parseBrowsePage(q, artistAlbumsDefaultLimit, artistAlbumsMaxLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), searchTimeout)
	defer cancel()
	var (
		wg        sync.WaitGroup
		artist    applemusic.Artist
		artistErr error
	)
	wg.Go(func() { artist, artistErr = s.catalog.Artist(ctx, storefront, id) })
	page, err := s.catalog.ArtistAlbumsPage(ctx, storefront, id, limit, offset)
	wg.Wait()
	if err == nil {
		err = artistErr
	}
	if err != nil {
		s.writeCatalogError(ctx, w, r, err, searchTimeout, nil, "artist albums failed", "artist_id", id, "storefront", storefront)
		return
	}
	resp := artistAlbumsResponse{Storefront: storefront, Artist: artistHit(storefront, artist), Albums: hitGroup{Items: []*searchHit{}, NextOffset: page.NextOffset}}
	for _, album := range page.Albums {
		resp.Albums.Items = append(resp.Albums.Items, albumHit(storefront, album))
	}
	s.markPriorJobs(r.Context(), append([]*searchHit{resp.Artist}, resp.Albums.Items...))
	writeJSON(w, http.StatusOK, resp)
}

// parseSearchTypes reads types, a subset of songs, albums, playlists and
// artists (all four when absent), in the order results are shown whatever
// the order given.
func parseSearchTypes(q url.Values) ([]applemusic.SearchType, error) {
	given := parseCSVQuery(q, "types")
	if len(given) == 0 {
		return applemusic.SearchTypes, nil
	}
	want := map[applemusic.SearchType]bool{}
	for _, t := range given {
		st := applemusic.SearchType(t)
		switch st {
		case applemusic.SearchSongs, applemusic.SearchAlbums, applemusic.SearchPlaylists, applemusic.SearchArtists:
			want[st] = true
		default:
			return nil, fmt.Errorf("types: %q is not supported; allowed: songs, albums, playlists, artists", t)
		}
	}
	var types []applemusic.SearchType
	for _, t := range applemusic.SearchTypes {
		if want[t] {
			types = append(types, t)
		}
	}
	return types, nil
}

// browseStorefront reads the storefront parameter, defaulting to
// catalog.default_storefront.
func (s *Server) browseStorefront(q url.Values) (string, error) {
	storefront := strings.ToLower(strings.TrimSpace(q.Get("storefront")))
	if storefront == "" {
		storefront = strings.ToLower(strings.TrimSpace(s.currentConfig().Catalog.DefaultStorefront))
	}
	if !applemusic.ValidStorefront(storefront) {
		return "", fmt.Errorf("storefront must be a two-letter Apple Music storefront code such as us or jp, got %q", storefront)
	}
	return storefront, nil
}

// parseBrowsePage reads limit (1 to max, def when absent) and offset (0 or more).
func parseBrowsePage(q url.Values, def, max int) (limit, offset int, err error) {
	limit = def
	if raw := strings.TrimSpace(q.Get("limit")); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 || limit > max {
			return 0, 0, fmt.Errorf("limit must be an integer between 1 and %d", max)
		}
	}
	if raw := strings.TrimSpace(q.Get("offset")); raw != "" {
		if offset, err = strconv.Atoi(raw); err != nil || offset < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer")
		}
	}
	return limit, offset, nil
}

// markPriorJobs sets prior_job on every hit a job has downloaded or is
// downloading: the newest queued or running job of the same type and id, else
// the newest completed one, from a link in any storefront. A song downloaded
// inside an album or playlist job is not marked. A failed store read leaves the
// hits unmarked; the search still answers.
func (s *Server) markPriorJobs(ctx context.Context, hits []*searchHit) {
	if s.store == nil || len(hits) == 0 {
		return
	}
	typeSet := map[string]bool{}
	var types, ids []string
	for _, h := range hits {
		if !typeSet[string(h.Type)] {
			typeSet[string(h.Type)] = true
			types = append(types, string(h.Type))
		}
		ids = append(ids, h.ID)
	}
	marks, err := s.store.JobMarks(ctx, types, ids)
	if err != nil {
		logging.FromContext(ctx, s.logger).Warn("read earlier jobs of search hits; answering without them", "error", err)
		return
	}
	best := map[string]db.JobMark{}
	for _, m := range marks {
		jobType, _, id, ok := jobs.ParseCanonicalKey(m.CanonicalKey)
		if !ok {
			continue
		}
		key := jobType + "\x00" + id
		if cur, seen := best[key]; !seen || newerMark(m, cur) {
			best[key] = m
		}
	}
	for _, h := range hits {
		if m, ok := best[string(h.Type)+"\x00"+h.ID]; ok {
			h.PriorJob = &priorJob{ID: m.ID, Status: m.Status, FinishedAt: m.FinishedAt}
		}
	}
}

// newerMark orders the jobs of one item: a queued or running job before a
// completed one, then the newest.
func newerMark(a, b db.JobMark) bool {
	aActive, bActive := a.Status != domain.JobCompleted, b.Status != domain.JobCompleted
	if aActive != bActive {
		return aActive
	}
	if aActive || a.FinishedAt == nil || b.FinishedAt == nil {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.FinishedAt.After(*b.FinishedAt)
}
