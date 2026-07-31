package applemusic

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// maxLibraryPageLimit is Apple's per-request ceiling for library collections.
const maxLibraryPageLimit = 100

// LibrarySong is one song in the signed-in user's library, carrying the
// library album it belongs to.
//
// There is deliberately no added-at timestamp: library-songs has no dateAdded
// attribute at all (extend= and fields[] will not produce one), and
// library-albums.dateAdded marks when the album's *first* song entered, so it
// does not move when a later song joins the same album. Only the ORDER of
// sort=-dateAdded is dependable, which is why callers track positions and ids
// rather than times.
type LibrarySong struct {
	ID             string
	Name           string
	LibraryAlbumID string
	AlbumName      string
	AlbumArtist    string
}

type librarySongsResponse struct {
	Next string `json:"next"`
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Name string `json:"name"`
		} `json:"attributes"`
		Relationships struct {
			Albums struct {
				Data []struct {
					ID         string `json:"id"`
					Attributes struct {
						Name       string `json:"name"`
						ArtistName string `json:"artistName"`
					} `json:"attributes"`
				} `json:"data"`
			} `json:"albums"`
		} `json:"relationships"`
	} `json:"data"`
}

type libraryAlbumCatalogResponse struct {
	Data []struct {
		ID         string `json:"id"`
		Attributes struct {
			Name       string `json:"name"`
			ArtistName string `json:"artistName"`
			URL        string `json:"url"`
		} `json:"attributes"`
	} `json:"data"`
}

// LibrarySongsNewestFirst reads one page of the user's library ordered by add
// time descending, with each song's library album included so the caller does
// not need a second request per song. hasMore reports whether Apple offered a
// next page.
//
// mediaUserToken is required: a personal library is invisible to the developer
// token alone, and Apple answers 403 without it.
//
// Both hosts serve this. getWithUserToken routes through apiBase(), so signed
// mode reads api.music.apple.com with the internal developer token, and legacy
// mode reads amp-api.music.apple.com with a scraped web-player token plus the
// Origin header it requires. Verified against a live library in both modes,
// not assumed — /v1/me/library is one of the few paths the two hosts agree on.
func (c *CatalogClient) LibrarySongsNewestFirst(ctx context.Context, mediaUserToken string, offset, limit int) (songs []LibrarySong, hasMore bool, err error) {
	if mediaUserToken == "" {
		return nil, false, fmt.Errorf("reading the library requires a media-user-token")
	}
	if limit <= 0 || limit > maxLibraryPageLimit {
		limit = maxLibraryPageLimit
	}
	params := url.Values{}
	params.Set("limit", strconv.Itoa(limit))
	params.Set("offset", strconv.Itoa(offset))
	params.Set("sort", "-dateAdded")
	params.Set("include", "albums")
	var resp librarySongsResponse
	endpoint := c.apiBase() + "/v1/me/library/songs"
	if err := c.getWithUserToken(ctx, endpoint, params, mediaUserToken, &resp); err != nil {
		return nil, false, err
	}
	out := make([]LibrarySong, 0, len(resp.Data))
	for _, item := range resp.Data {
		song := LibrarySong{ID: item.ID, Name: item.Attributes.Name}
		if albums := item.Relationships.Albums.Data; len(albums) > 0 {
			song.LibraryAlbumID = albums[0].ID
			song.AlbumName = albums[0].Attributes.Name
			song.AlbumArtist = albums[0].Attributes.ArtistName
		}
		out = append(out, song)
	}
	return out, resp.Next != "", nil
}

// LibraryAlbumCatalogURL maps a library album id (l.xxxxxxx) to its catalog
// album page URL, which is what the download pipeline accepts — a library id is
// personal to the account and cannot be resolved as a download input.
//
// Albums with no catalog counterpart (personally uploaded, or withdrawn in this
// storefront) answer 404. That is a property of the album rather than a
// failure, so it returns ok=false with a nil error and the caller skips it
// instead of failing the whole pass.
//
// Like the library listing, this resolves on both hosts — checked in legacy
// web-token mode as well, since that path reaches amp-api instead.
func (c *CatalogClient) LibraryAlbumCatalogURL(ctx context.Context, mediaUserToken, libraryAlbumID string) (catalogURL string, ok bool, err error) {
	if mediaUserToken == "" {
		return "", false, fmt.Errorf("reading the library requires a media-user-token")
	}
	var resp libraryAlbumCatalogResponse
	endpoint := fmt.Sprintf("%s/v1/me/library/albums/%s/catalog", c.apiBase(), url.PathEscape(libraryAlbumID))
	if err := c.getWithUserToken(ctx, endpoint, nil, mediaUserToken, &resp); err != nil {
		// errors.As rather than a type assertion so this keeps working if the
		// request path ever wraps its errors.
		var requestErr catalogRequestError
		if errors.As(err, &requestErr) && requestErr.statusCode == 404 {
			return "", false, nil
		}
		return "", false, err
	}
	if len(resp.Data) == 0 || resp.Data[0].Attributes.URL == "" {
		return "", false, nil
	}
	return resp.Data[0].Attributes.URL, true, nil
}
