package applemusic

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"amdl/internal/config"
	"amdl/internal/limits"
)

// CatalogClient reads Apple Music catalog, artwork and library metadata. One
// client is shared process-wide; it owns the Apple request gate and both token
// caches.
type CatalogClient struct {
	cfg    config.CatalogConfig
	http   *http.Client
	logger *slog.Logger
	gate   *limits.RequestGate
	// retryDelay is replaceable by same-package tests so 429 behavior can be
	// verified without sleeping through the production fallback delay.
	retryDelay func(http.Header) time.Duration
	// webToken is the scraped music.apple.com web-player JWT that authorizes
	// every request, amp-api being the only host this client talks to.
	webToken tokenCache
}

// NewCatalogClient builds the client. It does no I/O; run RunTokenRefresher for
// the process lifetime.
func NewCatalogClient(cfg config.CatalogConfig, logger *slog.Logger) *CatalogClient {
	c := &CatalogClient{
		cfg:        cfg,
		http:       &http.Client{Timeout: 30 * time.Second},
		logger:     logger,
		gate:       limits.NewRequestGate(cfg.MaxParallelRequests, config.CatalogRequestsPerSecond, config.CatalogRequestBurst),
		retryDelay: defaultRetryDelay,
	}
	c.webToken.fetch = c.scrapeWebToken
	return c
}

// RequestGate exposes the process-wide Apple small-request gate owned by this
// singleton. Callers fetching Apple manifests must reuse it rather than create
// a per-job limiter.
func (c *CatalogClient) RequestGate() *limits.RequestGate {
	return c.gate
}

// Song reads a catalog song and, when it belongs to an album, enriches it from
// that album (EnrichWithAlbum). A failed album read leaves the song as the song
// resource describes it.
func (c *CatalogClient) Song(ctx context.Context, storefront, id string) (Song, error) {
	song, err := c.SongMetadata(ctx, storefront, id)
	if err != nil {
		return Song{}, err
	}
	if song.AlbumID != "" {
		if album, err := c.Album(ctx, storefront, song.AlbumID); err == nil && len(album.Tracks) > 0 {
			song = EnrichWithAlbum(song, album)
		}
	}
	return song, nil
}

// SongMetadata reads the song resource without following its album
// relationship. Collection downloads already hold album metadata from their
// resolve phase, so they use this lighter request and merge that existing
// context instead of downloading the same album once per track.
func (c *CatalogClient) SongMetadata(ctx context.Context, storefront, id string) (Song, error) {
	var resp catalogSongResponse
	params := url.Values{
		"include": []string{"albums,artists"},
		// extendedAssetUrls carries enhancedHls.
		"extend": []string{"extendedAssetUrls"},
		"l":      []string{c.cfg.Language},
	}
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/songs/%s", ampAPIBase, storefront, id), params, &resp); err != nil {
		return Song{}, err
	}
	if len(resp.Data) == 0 {
		return Song{}, fmt.Errorf("song %s not found", id)
	}
	return mapSong(resp.Data[0]), nil
}

// Album reads a catalog album with every page of its tracks, each carrying the
// album-level fields (album artist, release, label, disc and track totals).
func (c *CatalogClient) Album(ctx context.Context, storefront, id string) (Collection, error) {
	var resp catalogAlbumResponse
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/albums/%s", ampAPIBase, storefront, id), url.Values{
		"include":        []string{"tracks,artists,record-labels"},
		"include[songs]": []string{"artists"},
		// artistUrl is the only extended attribute Apple documents as available
		// to a third party on Albums, and it is a second source for the artist
		// page below — the included artist resource may not always carry one.
		"extend": []string{"artistUrl"},
		"l":      []string{c.cfg.Language},
	}, &resp); err != nil {
		return Collection{}, err
	}
	if len(resp.Data) == 0 {
		return Collection{}, fmt.Errorf("album %s not found", id)
	}
	album := resp.Data[0]
	rawTracks, err := c.allTrackPages(ctx, album.Relationships.Tracks)
	if err != nil {
		return Collection{}, err
	}
	var albumArtistID, albumArtistArtworkURL, albumArtistURL string
	if len(album.Relationships.Artists.Data) > 0 {
		albumArtist := album.Relationships.Artists.Data[0]
		albumArtistID = albumArtist.ID
		albumArtistArtworkURL = albumArtist.Attributes.Artwork.URL
		albumArtistURL = albumArtist.Attributes.URL
	}
	albumArtistURL = firstNonEmpty(albumArtistURL, album.Attributes.ArtistURL)
	tracks := make([]Song, 0, len(rawTracks))
	discCount := 0
	for _, raw := range rawTracks {
		if raw.Type != "songs" {
			continue
		}
		s := mapSong(raw)
		s.AlbumID = album.ID
		s.AlbumName = firstNonEmpty(s.AlbumName, album.Attributes.Name)
		s.AlbumArtist = album.Attributes.ArtistName
		s.AlbumArtistID = albumArtistID
		s.AlbumArtistArtworkURL = albumArtistArtworkURL
		s.AlbumRelease = album.Attributes.ReleaseDate
		s.Copyright = album.Attributes.Copyright
		s.RecordLabel = album.Attributes.RecordLabel
		s.UPC = album.Attributes.UPC
		s.TrackCount = album.Attributes.TrackCount
		if s.DiscNumber > discCount {
			discCount = s.DiscNumber
		}
		tracks = append(tracks, s)
	}
	for i := range tracks {
		if discCount > 0 {
			tracks[i].DiscCount = discCount
		}
	}
	// Apple's album-level trackCount counts the whole release, so both discs of
	// a 29+19 album report 48. "Track N of M" means M tracks on this disc to
	// every tagger that reads it, so a multi-disc release gets its own disc's
	// total instead. Single-disc albums keep Apple's count: it is the one
	// authoritative total, and unlike a per-disc figure it needs nothing
	// inferred when the relationship omits a track.
	if discCount > 1 {
		perDisc := discTrackTotals(tracks)
		for i := range tracks {
			if total := perDisc[max(1, tracks[i].DiscNumber)]; total > 0 {
				tracks[i].TrackCount = total
			}
		}
	}
	return Collection{
		ID: album.ID, Type: TypeAlbum, Name: album.Attributes.Name, Artist: album.Attributes.ArtistName,
		ArtworkURL: album.Attributes.Artwork.URL, ArtworkColors: album.Attributes.Artwork.colors(),
		ArtistID: albumArtistID, ArtistArtworkURL: albumArtistArtworkURL, ArtistURL: albumArtistURL,
		ReleaseDate: album.Attributes.ReleaseDate, GenreNames: album.Attributes.GenreNames,
		ContentRating: album.Attributes.ContentRating, AppleDigitalMaster: album.Attributes.IsMasteredForItunes,
		AudioTraits: album.Attributes.AudioTraits, Tracks: tracks,
	}, nil
}

// Playlist fetches a catalog playlist. The fetch itself never carries the
// media-user-token, so an invalid or expired token can never fail a playlist
// that resolves fine without one. mediaUserToken only powers a best-effort
// artwork enrichment afterwards: a private (user-shared, pl.u-) playlist's
// public catalog artwork may be Apple's generated track mosaic, while the
// owner's library copy — only visible with the user's subscription identity —
// carries the actual user-selected cover (see libraryArtworkURL).
func (c *CatalogClient) Playlist(ctx context.Context, storefront, id, mediaUserToken string) (Collection, error) {
	var resp catalogPlaylistResponse
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/playlists/%s", ampAPIBase, storefront, id), url.Values{
		"l": []string{c.cfg.Language},
	}, &resp); err != nil {
		return Collection{}, err
	}
	if len(resp.Data) == 0 {
		return Collection{}, fmt.Errorf("playlist %s not found", id)
	}
	playlist := resp.Data[0]
	rawTracks, err := c.allTrackPages(ctx, playlist.Relationships.Tracks)
	if err != nil {
		return Collection{}, err
	}
	tracks := make([]Song, 0, len(rawTracks))
	for _, raw := range rawTracks {
		if raw.Type != "songs" {
			continue
		}
		tracks = append(tracks, mapSong(raw))
	}
	artworkURL := playlist.Attributes.Artwork.URL
	if mediaUserToken != "" && strings.HasPrefix(id, "pl.u-") {
		artworkURL = firstNonEmpty(c.libraryArtworkURL(ctx, storefront, id, mediaUserToken), artworkURL)
	}
	// ArtworkColors always describes the public catalog artwork: the private
	// library-copy cover (when it replaces artworkURL above) carries no
	// palette of its own in the library relationship payload.
	out := mapPlaylistSummary(playlist)
	out.ArtworkURL, out.Tracks = artworkURL, tracks
	return out, nil
}

// libraryArtworkURL fetches a private playlist's cover from the owner's
// library copy: the catalog playlist is re-requested with include=library and
// the Media-User-Token header, and the artwork hangs off the returned
// library-playlists relationship as a pre-signed direct image link (no
// {w}x{h}/{f} placeholders), which the cover fetcher passes through
// unchanged. Purely best-effort enrichment: any failure returns "" and the
// download proceeds without a playlist cover, it never fails the job.
func (c *CatalogClient) libraryArtworkURL(ctx context.Context, storefront, id, mediaUserToken string) string {
	var resp catalogPlaylistResponse
	if err := c.getWithUserToken(ctx, fmt.Sprintf("%s/v1/catalog/%s/playlists/%s", ampAPIBase, storefront, id), url.Values{
		"include": []string{"library"},
		"l":       []string{c.cfg.Language},
	}, mediaUserToken, &resp); err != nil {
		c.logger.Warn("private playlist library artwork lookup failed; continuing without playlist cover", "playlist_id", id, "error", err)
		return ""
	}
	if len(resp.Data) == 0 {
		return ""
	}
	for _, lib := range resp.Data[0].Relationships.Library.Data {
		if lib.Attributes.Artwork.URL != "" {
			return lib.Attributes.Artwork.URL
		}
	}
	return ""
}

// Station fetches a radio station's catalog metadata. The web-player token
// alone authorizes this read (no media-user-token needed); the returned
// Format distinguishes a downloadable "tracks" station from a live "stream".
func (c *CatalogClient) Station(ctx context.Context, storefront, id string) (StationInfo, error) {
	var resp catalogStationResponse
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/stations/%s", ampAPIBase, storefront, id), url.Values{
		"l": []string{c.cfg.Language},
	}, &resp); err != nil {
		return StationInfo{}, err
	}
	if len(resp.Data) == 0 {
		return StationInfo{}, fmt.Errorf("station %s not found", id)
	}
	station := resp.Data[0]
	return StationInfo{
		ID: station.ID, Name: station.Attributes.Name, ArtworkURL: station.Attributes.Artwork.URL,
		ArtworkColors: station.Attributes.Artwork.colors(),
		Format:        station.Attributes.PlayParams.Format, IsLive: station.Attributes.IsLive,
	}, nil
}

// StationTracks resolves a personalized/curated radio station into a finite
// collection of catalog songs via POST /v1/me/stations/next-tracks/{id}, which
// requires the user's media-user-token (an Apple subscription token) in
// addition to the web-player token. Live broadcast stations ("stream" format)
// have no static track list and return an error. The next-tracks feed is a
// rolling window, so a station "download" captures whichever upcoming songs the
// feed currently returns rather than a fixed catalog listing.
func (c *CatalogClient) StationTracks(ctx context.Context, storefront, id, mediaUserToken string) (Collection, error) {
	info, err := c.Station(ctx, storefront, id)
	if err != nil {
		return Collection{}, err
	}
	if !info.Downloadable() {
		return Collection{}, fmt.Errorf("station %q is not downloadable: only track-based stations (playParams.format=tracks) are supported, not live/stream stations", info.Name)
	}
	if strings.TrimSpace(mediaUserToken) == "" {
		return Collection{}, fmt.Errorf("station downloads require a media_user_token")
	}
	var resp stationTracksResponse
	if err := c.stationNextTracks(ctx, id, mediaUserToken, &resp); err != nil {
		return Collection{}, err
	}
	tracks := make([]Song, 0, len(resp.Data))
	for _, raw := range resp.Data {
		if raw.Type != "songs" {
			continue
		}
		tracks = append(tracks, mapSong(raw))
	}
	return Collection{ID: info.ID, Type: TypeStation, Name: info.Name, Artist: StationCuratorName, ArtworkURL: info.ArtworkURL, ArtworkColors: info.ArtworkColors, Tracks: tracks}, nil
}

// stationNextTracksLimit is the limit sent to next-tracks, and so the most
// songs one station job resolves. The endpoint is absent from Apple's
// documented API (its station reference covers metadata reads only), so there
// is no published ceiling to use instead. Every station download has requested
// 10; whether Apple honors a larger value is unverified, so raising it needs a
// live check first.
const stationNextTracksLimit = 10

// stationNextTracks performs the authenticated POST to the personalized
// next-tracks endpoint, with the user's Media-User-Token next to the catalog
// token.
func (c *CatalogClient) stationNextTracks(ctx context.Context, id, mediaUserToken string, out any) error {
	params := url.Values{
		"include[songs]": []string{"artists,albums"},
		"limit":          []string{strconv.Itoa(stationNextTracksLimit)},
		"l":              []string{c.cfg.Language},
	}
	endpoint := fmt.Sprintf("%s/v1/me/stations/next-tracks/%s?%s", ampAPIBase, id, params.Encode())
	resp, err := c.doWithWebAuth(ctx, appleRequest{method: http.MethodPost, url: endpoint, mediaUserToken: mediaUserToken})
	if err != nil {
		return fmt.Errorf("station next-tracks: %w", err)
	}
	if err := decodeCatalogResponse(resp, out); err != nil {
		return fmt.Errorf("station next-tracks: %w", err)
	}
	return nil
}

// ArtistAlbums reads an artist and every page of its albums, deduplicated by id.
func (c *CatalogClient) ArtistAlbums(ctx context.Context, storefront, id string) (ArtistAlbums, error) {
	var resp catalogArtistResponse
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/artists/%s", ampAPIBase, storefront, id), url.Values{
		"include": []string{"albums"},
		"l":       []string{c.cfg.Language},
	}, &resp); err != nil {
		return ArtistAlbums{}, err
	}
	if len(resp.Data) == 0 {
		return ArtistAlbums{}, fmt.Errorf("artist %s not found", id)
	}
	raw := resp.Data[0]
	rawAlbums, err := c.allAlbumPages(ctx, raw.Relationships.Albums)
	if err != nil {
		return ArtistAlbums{}, err
	}
	albums := make([]Collection, 0, len(rawAlbums))
	seen := make(map[string]struct{}, len(rawAlbums))
	for _, rawAlbum := range rawAlbums {
		if rawAlbum.Type != "albums" {
			continue
		}
		if _, exists := seen[rawAlbum.ID]; exists {
			continue
		}
		seen[rawAlbum.ID] = struct{}{}
		albums = append(albums, mapAlbumSummary(rawAlbum))
	}
	return ArtistAlbums{
		Artist: Artist{ID: raw.ID, Name: raw.Attributes.Name, URL: raw.Attributes.URL, ArtworkURL: raw.Attributes.Artwork.URL, ArtworkColors: raw.Attributes.Artwork.colors()},
		Albums: albums,
	}, nil
}

func (c *CatalogClient) allTrackPages(ctx context.Context, first relationshipSongs) ([]catalogSongData, error) {
	tracks := append([]catalogSongData(nil), first.Data...)
	for next := strings.TrimSpace(first.Next); next != ""; {
		var page relationshipSongs
		if err := c.get(ctx, c.catalogNextURL(next), nil, &page); err != nil {
			return nil, err
		}
		tracks = append(tracks, page.Data...)
		next = strings.TrimSpace(page.Next)
	}
	return tracks, nil
}

func (c *CatalogClient) allAlbumPages(ctx context.Context, first relationshipAlbums) ([]catalogAlbumData, error) {
	albums := append([]catalogAlbumData(nil), first.Data...)
	for next := strings.TrimSpace(first.Next); next != ""; {
		var page relationshipAlbums
		if err := c.get(ctx, c.catalogNextURL(next), nil, &page); err != nil {
			return nil, err
		}
		albums = append(albums, page.Data...)
		next = strings.TrimSpace(page.Next)
	}
	return albums, nil
}

func (c *CatalogClient) catalogNextURL(next string) string {
	if strings.HasPrefix(next, "http://") || strings.HasPrefix(next, "https://") {
		return next
	}
	if strings.HasPrefix(next, "/") {
		return ampAPIBase + next
	}
	return ampAPIBase + "/" + next
}

// Artist reads a catalog artist's name, page, genres and artwork.
func (c *CatalogClient) Artist(ctx context.Context, storefront, id string) (Artist, error) {
	var resp catalogArtistResponse
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/artists/%s", ampAPIBase, storefront, id), url.Values{
		"l": []string{c.cfg.Language},
	}, &resp); err != nil {
		return Artist{}, err
	}
	if len(resp.Data) == 0 {
		return Artist{}, fmt.Errorf("artist %s not found", id)
	}
	return mapArtist(resp.Data[0]), nil
}
