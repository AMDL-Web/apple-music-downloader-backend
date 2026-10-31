package client

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"amdl/internal/domain"
)

// SearchQuery is the query of GET /api/v1/search; zero fields are left out and
// take the backend's defaults.
type SearchQuery struct {
	Term string
	// Types are the groups to search: songs, albums, playlists, artists. Empty
	// searches all four.
	Types []string
	// Storefront is a two-letter code; empty is catalog.default_storefront.
	Storefront string
	// Limit is hits per group (default 10, at most 50); Offset where each
	// group's page starts.
	Limit, Offset int
}

// SearchHit is the SearchSong, SearchAlbum, SearchPlaylist and SearchArtist
// schemas: the fields every hit has, then those of its type. URL submitted to POST
// /api/v1/downloads downloads exactly that item.
type SearchHit struct {
	ID                 string    `json:"id"`
	Type               string    `json:"type"`
	Name               string    `json:"name"`
	URL                string    `json:"url"`
	ArtworkURL         string    `json:"artwork_url,omitempty"`
	ArtworkBgColor     string    `json:"artwork_bg_color,omitempty"`
	ArtworkTextColor1  string    `json:"artwork_text_color1,omitempty"`
	ArtworkTextColor2  string    `json:"artwork_text_color2,omitempty"`
	ArtworkTextColor3  string    `json:"artwork_text_color3,omitempty"`
	ArtworkTextColor4  string    `json:"artwork_text_color4,omitempty"`
	ArtistName         string    `json:"artist_name,omitempty"`
	AlbumName          string    `json:"album_name,omitempty"`
	ReleaseDate        string    `json:"release_date,omitempty"`
	Genre              string    `json:"genre,omitempty"`
	TrackCount         int       `json:"track_count,omitempty"`
	DurationMS         int       `json:"duration_ms,omitempty"`
	ContentRating      string    `json:"content_rating,omitempty"`
	AppleDigitalMaster bool      `json:"apple_digital_master,omitempty"`
	AudioTraits        []string  `json:"audio_traits,omitempty"`
	PriorJob           *PriorJob `json:"prior_job,omitempty"`
}

// PriorJob is the PriorJob schema: the job that put a hit in the library
// (completed) or holds it in the queue (queued, running).
type PriorJob struct {
	ID         string           `json:"id"`
	Status     domain.JobStatus `json:"status"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`
}

// HitGroup is one page of a search group or of an artist's albums.
// NextOffset is 0 on the last page.
type HitGroup struct {
	Items      []SearchHit `json:"items"`
	NextOffset int         `json:"next_offset,omitempty"`
}

// SearchResult is the SearchResponse schema; a group that was not asked for
// is nil.
type SearchResult struct {
	Term       string    `json:"term"`
	Storefront string    `json:"storefront"`
	Songs      *HitGroup `json:"songs,omitempty"`
	Albums     *HitGroup `json:"albums,omitempty"`
	Playlists  *HitGroup `json:"playlists,omitempty"`
	Artists    *HitGroup `json:"artists,omitempty"`
}

// ArtistAlbums is the ArtistAlbumsResponse schema: the artist and one page of
// its albums.
type ArtistAlbums struct {
	Storefront string    `json:"storefront"`
	Artist     SearchHit `json:"artist"`
	Albums     HitGroup  `json:"albums"`
}

// PageQuery is the storefront and page of GET /api/v1/artists/{id}/albums;
// zero fields take the backend's defaults (25 albums, at most 100).
type PageQuery struct {
	Storefront    string
	Limit, Offset int
}

func (q PageQuery) values() url.Values {
	v := url.Values{}
	if q.Storefront != "" {
		v.Set("storefront", q.Storefront)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	if q.Offset > 0 {
		v.Set("offset", strconv.Itoa(q.Offset))
	}
	return v
}

// Search reads one page of catalog hits per group with GET /api/v1/search.
func (c *Client) Search(ctx context.Context, q SearchQuery) (SearchResult, error) {
	v := PageQuery{Storefront: q.Storefront, Limit: q.Limit, Offset: q.Offset}.values()
	v.Set("term", q.Term)
	if len(q.Types) > 0 {
		v.Set("types", strings.Join(q.Types, ","))
	}
	var r SearchResult
	err := c.do(ctx, http.MethodGet, "/api/v1/search", v, nil, &r)
	return r, err
}

// ArtistAlbums reads an artist and one page of its albums with GET
// /api/v1/artists/{id}/albums.
func (c *Client) ArtistAlbums(ctx context.Context, artistID string, q PageQuery) (ArtistAlbums, error) {
	var r ArtistAlbums
	err := c.do(ctx, http.MethodGet, "/api/v1/artists/"+url.PathEscape(artistID)+"/albums", q.values(), nil, &r)
	return r, err
}

// Album is the AlbumResponse schema: an album's metadata, under the names and
// meaning of the lookup fields, and its songs. URL submitted to POST
// /api/v1/downloads downloads the whole album.
type Album struct {
	Storefront         string       `json:"storefront"`
	ID                 string       `json:"id"`
	URL                string       `json:"url"`
	Title              string       `json:"title"`
	ArtistName         string       `json:"artist_name,omitempty"`
	ArtistURL          string       `json:"artist_url,omitempty"`
	ArtworkURL         string       `json:"artwork_url,omitempty"`
	ArtworkBgColor     string       `json:"artwork_bg_color,omitempty"`
	ArtworkTextColor1  string       `json:"artwork_text_color1,omitempty"`
	ArtworkTextColor2  string       `json:"artwork_text_color2,omitempty"`
	ArtworkTextColor3  string       `json:"artwork_text_color3,omitempty"`
	ArtworkTextColor4  string       `json:"artwork_text_color4,omitempty"`
	ReleaseDate        string       `json:"release_date,omitempty"`
	Genre              string       `json:"genre,omitempty"`
	TrackCount         int          `json:"track_count"`
	DiscCount          int          `json:"disc_count,omitempty"`
	ContentRating      string       `json:"content_rating,omitempty"`
	AppleDigitalMaster bool         `json:"apple_digital_master,omitempty"`
	AudioTraits        []string     `json:"audio_traits,omitempty"`
	Tracks             []AlbumTrack `json:"tracks"`
}

// AlbumTrack is the AlbumTrack schema: one song of an album, in the order
// Album.Tracks lists them (disc, then track number). ID is the adam_id of the
// job item that downloaded it; URL submitted to POST /api/v1/downloads
// downloads that song alone.
type AlbumTrack struct {
	ID                 string   `json:"id"`
	URL                string   `json:"url"`
	DiscNumber         int      `json:"disc_number"`
	TrackNumber        int      `json:"track_number"`
	Name               string   `json:"name"`
	ArtistName         string   `json:"artist_name,omitempty"`
	DurationMS         int      `json:"duration_ms,omitempty"`
	ContentRating      string   `json:"content_rating,omitempty"`
	AppleDigitalMaster bool     `json:"apple_digital_master,omitempty"`
	AudioTraits        []string `json:"audio_traits,omitempty"`
	HasLyrics          bool     `json:"has_lyrics,omitempty"`
}

// Album reads an album and its songs with GET /api/v1/albums/{id}. An empty
// storefront is catalog.default_storefront.
func (c *Client) Album(ctx context.Context, albumID, storefront string) (Album, error) {
	var a Album
	err := c.do(ctx, http.MethodGet, "/api/v1/albums/"+url.PathEscape(albumID), PageQuery{Storefront: storefront}.values(), nil, &a)
	return a, err
}
