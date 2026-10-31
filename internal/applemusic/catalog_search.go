package applemusic

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// SearchType is one result group of a catalog search, named as Apple names the
// group in the request's types parameter and in the response.
type SearchType string

// The result groups Search reads.
const (
	SearchSongs     SearchType = "songs"
	SearchAlbums    SearchType = "albums"
	SearchPlaylists SearchType = "playlists"
	SearchArtists   SearchType = "artists"
)

// SearchTypes lists every SearchType in the order results are shown.
var SearchTypes = []SearchType{SearchSongs, SearchAlbums, SearchPlaylists, SearchArtists}

// SearchResults holds one catalog search's hits per group, each in Apple's
// relevance order. NextOffset maps a group to the offset of its next page and
// holds only the groups Apple reports more hits for.
type SearchResults struct {
	Songs      []Song
	Albums     []Collection
	Playlists  []Collection
	Artists    []Artist
	NextOffset map[SearchType]int
}

// AlbumPage is one page of an artist's albums. NextOffset is the offset of the
// next page, or 0 when this page is the last.
type AlbumPage struct {
	Albums     []Collection
	NextOffset int
}

type catalogSearchResponse struct {
	Results struct {
		Songs     searchGroup[catalogSongData]     `json:"songs"`
		Albums    searchGroup[catalogAlbumData]    `json:"albums"`
		Playlists searchGroup[catalogPlaylistData] `json:"playlists"`
		Artists   searchGroup[artistData]          `json:"artists"`
	} `json:"results"`
}

type searchGroup[T any] struct {
	Data []T    `json:"data"`
	Next string `json:"next"`
}

// Search reads one page of catalog hits for term in storefront: limit hits per
// group in types, starting at offset in each. A group without hits is empty;
// Apple leaves such a group out of its response.
func (c *CatalogClient) Search(ctx context.Context, storefront, term string, types []SearchType, limit, offset int) (SearchResults, error) {
	names := make([]string, 0, len(types))
	for _, t := range types {
		names = append(names, string(t))
	}
	params := url.Values{
		"term":  []string{term},
		"types": []string{strings.Join(names, ",")},
		"limit": []string{strconv.Itoa(limit)},
		"l":     []string{c.cfg.Language},
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	var resp catalogSearchResponse
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/search", ampAPIBase, storefront), params, &resp); err != nil {
		return SearchResults{}, err
	}
	out := SearchResults{NextOffset: map[SearchType]int{}}
	for _, raw := range resp.Results.Songs.Data {
		if raw.Type == "songs" {
			out.Songs = append(out.Songs, mapSong(raw))
		}
	}
	for _, raw := range resp.Results.Albums.Data {
		if raw.Type == "albums" {
			out.Albums = append(out.Albums, mapAlbumSummary(raw))
		}
	}
	for _, raw := range resp.Results.Playlists.Data {
		if raw.Type == "playlists" {
			out.Playlists = append(out.Playlists, mapPlaylistSummary(raw))
		}
	}
	for _, raw := range resp.Results.Artists.Data {
		out.Artists = append(out.Artists, mapArtist(raw))
	}
	for group, next := range map[SearchType]string{
		SearchSongs:     resp.Results.Songs.Next,
		SearchAlbums:    resp.Results.Albums.Next,
		SearchPlaylists: resp.Results.Playlists.Next,
		SearchArtists:   resp.Results.Artists.Next,
	} {
		if n := nextOffset(next, offset+limit); n > 0 {
			out.NextOffset[group] = n
		}
	}
	return out, nil
}

// ArtistAlbumsPage reads limit of an artist's albums from offset, in the order
// of Apple's albums relationship, which is the list ArtistAlbums downloads.
// Apple answers 404 for an offset past the last album as for a missing
// artist; past the first page that is an empty page, not an error.
func (c *CatalogClient) ArtistAlbumsPage(ctx context.Context, storefront, id string, limit, offset int) (AlbumPage, error) {
	params := url.Values{
		"limit": []string{strconv.Itoa(limit)},
		"l":     []string{c.cfg.Language},
	}
	if offset > 0 {
		params.Set("offset", strconv.Itoa(offset))
	}
	var resp relationshipAlbums
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/artists/%s/albums", ampAPIBase, storefront, id), params, &resp); err != nil {
		if offset > 0 && IsNotFound(err) {
			return AlbumPage{Albums: []Collection{}}, nil
		}
		return AlbumPage{}, err
	}
	page := AlbumPage{Albums: make([]Collection, 0, len(resp.Data)), NextOffset: nextOffset(resp.Next, offset+limit)}
	seen := make(map[string]struct{}, len(resp.Data))
	for _, raw := range resp.Data {
		if _, dup := seen[raw.ID]; dup || raw.Type != "albums" {
			continue
		}
		seen[raw.ID] = struct{}{}
		page.Albums = append(page.Albums, mapAlbumSummary(raw))
	}
	return page, nil
}

// nextOffset reads the offset query parameter of Apple's next link, falling
// back to fallback when the link has none it can parse; no link means 0.
func nextOffset(next string, fallback int) int {
	next = strings.TrimSpace(next)
	if next == "" {
		return 0
	}
	if u, err := url.Parse(next); err == nil {
		if n, err := strconv.Atoi(u.Query().Get("offset")); err == nil && n > 0 {
			return n
		}
	}
	return fallback
}

// ValidStorefront reports whether s has the shape of a storefront code in an
// Apple Music link: two lowercase letters.
func ValidStorefront(s string) bool { return storefrontPattern.MatchString(s) }
