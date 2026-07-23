package applemusic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"amdl/internal/config"
	"amdl/internal/limits"
)

type CatalogClient struct {
	cfg    config.CatalogConfig
	http   *http.Client
	logger *slog.Logger
	gate   *limits.RequestGate
	// retryDelay is replaceable by same-package tests so 429 behavior can be
	// verified without sleeping through the production fallback delay.
	retryDelay func(http.Header) time.Duration
	mu         sync.Mutex
	// Refresh locks serialize cache misses without holding mu across network
	// requests. The cache is checked again after acquiring each lock so a burst
	// of callers shares one scrape/sign operation instead of stampeding Apple.
	tokenRefreshMu sync.Mutex
	webRefreshMu   sync.Mutex
	signer         *developerTokenSigner
	token          string
	tokenUntil     time.Time
	// webToken/webTokenUntil cache a scraped music.apple.com web-player JWT,
	// kept separate from token/tokenUntil above so that fetching it for
	// EnhancedHLSViaWebToken never disturbs the signed developer-token cache
	// used by every other catalog request.
	webToken      string
	webTokenUntil time.Time
}

type catalogRequestError struct {
	statusCode int
	status     string
	body       string
	retryAfter time.Duration
}

func (e catalogRequestError) Error() string {
	return fmt.Sprintf("catalog request failed: %s: %s", e.status, e.body)
}

// NonRetryable lets download operations stop immediately on deterministic
// client errors such as a missing artist. Rate limits and request timeouts are
// transient and retain the normal retry path.
func (e catalogRequestError) NonRetryable() bool {
	return e.statusCode >= 400 && e.statusCode < 500 && e.statusCode != http.StatusRequestTimeout && e.statusCode != http.StatusTooManyRequests
}

// RetryDelay exposes Apple's Retry-After hint without coupling the generic
// retry helper to the catalog package.
func (e catalogRequestError) RetryDelay() time.Duration { return e.retryAfter }

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	when, err := http.ParseTime(value)
	if err != nil || !when.After(now) {
		return 0
	}
	return when.Sub(now)
}

const (
	maxCatalogResponseBytes int64 = 64 << 20
	maxArtworkBytes         int64 = 64 << 20
	maxWebPlayerPageBytes   int64 = 16 << 20
	maxWebPlayerJSBytes     int64 = 64 << 20
)

func NewCatalogClient(cfg config.CatalogConfig, logger *slog.Logger) *CatalogClient {
	return &CatalogClient{
		cfg:        cfg,
		http:       &http.Client{Timeout: 30 * time.Second},
		logger:     logger,
		gate:       limits.NewRequestGate(cfg.MaxParallelRequests, cfg.RequestsPerSecond, cfg.RequestBurst),
		retryDelay: defaultRetryDelay,
	}
}

// RequestGate exposes the process-wide Apple small-request gate owned by this
// singleton. Callers fetching Apple manifests must reuse it rather than create
// a per-job limiter.
func (c *CatalogClient) RequestGate() *limits.RequestGate {
	return c.gate
}

// InitDeveloperToken loads the signing key and mints the first developer token
// when signing is configured. It must be called once at startup; any error
// should stop the process from starting. It is a no-op in legacy mode.
func (c *CatalogClient) InitDeveloperToken() error {
	if !c.cfg.DeveloperTokenSigningEnabled() {
		return nil
	}
	signer, err := newDeveloperTokenSigner(c.cfg.AppleMusicPrivateKeyPath, c.cfg.AppleMusicKeyID, c.cfg.AppleMusicTeamID)
	if err != nil {
		return err
	}
	c.signer = signer
	token, _, err := signer.sign(time.Now(), internalDeveloperTokenTTL, nil)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.token = token
	// Cache for only half the signed lifetime: a still-valid-on-paper token
	// can start getting rejected by Apple before its exp claim, so refreshing
	// proactively at the half-life avoids waiting for a 401 to notice.
	c.tokenUntil = cacheUntil(time.Now(), internalDeveloperTokenTTL)
	c.mu.Unlock()
	return nil
}

// MintDeveloperToken signs a fresh developer token for external clients using
// the configured endpoint TTL and allowed-origins list. It fails when
// developer-token signing is disabled (legacy web-token mode). Expiry is not
// returned: clients read the exp claim from the JWT payload.
func (c *CatalogClient) MintDeveloperToken() (string, error) {
	if c.signer == nil {
		return "", fmt.Errorf("developer token signing is not configured")
	}
	token, _, err := c.signer.sign(time.Now(), c.cfg.DeveloperTokenTTL(), c.cfg.AllowedOrigins)
	return token, err
}

// apiBase returns the catalog host. A self-signed developer token uses the
// official api.music.apple.com; legacy web tokens use the amp-api host.
func (c *CatalogClient) apiBase() string {
	if c.cfg.DeveloperTokenSigningEnabled() {
		return "https://api.music.apple.com"
	}
	return "https://amp-api.music.apple.com"
}

func (c *CatalogClient) Song(ctx context.Context, storefront, id string) (Song, error) {
	song, err := c.SongMetadata(ctx, storefront, id)
	if err != nil {
		return Song{}, err
	}
	if song.AlbumID != "" {
		if album, err := c.Album(ctx, storefront, song.AlbumID); err == nil && len(album.Tracks) > 0 {
			song = enrichSongWithAlbum(song, album)
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
		"l":       []string{c.cfg.Language},
	}
	if !c.cfg.DeveloperTokenSigningEnabled() {
		// extendedAssetUrls carries enhancedHls, which a self-signed developer
		// token cannot access; only request it in legacy mode.
		params.Set("extend", "extendedAssetUrls")
	}
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/songs/%s", c.apiBase(), storefront, id), params, &resp); err != nil {
		return Song{}, err
	}
	if len(resp.Data) == 0 {
		return Song{}, fmt.Errorf("song %s not found", id)
	}
	return mapSong(resp.Data[0]), nil
}

func enrichSongWithAlbum(song Song, album Collection) Song {
	song.AlbumArtworkURL = album.ArtworkURL
	song.AlbumArtistID = album.ArtistID
	song.AlbumArtistArtworkURL = album.ArtistArtworkURL
	for _, track := range album.Tracks {
		if track.ID == song.ID {
			song.TrackCount = track.TrackCount
			song.DiscCount = track.DiscCount
			break
		}
	}
	if song.TrackCount == 0 {
		song.TrackCount = len(album.Tracks)
	}
	if song.DiscCount == 0 {
		song.DiscCount = maxDisc(album.Tracks)
	}
	song.AlbumArtist = album.Artist
	return song
}

func (c *CatalogClient) Album(ctx context.Context, storefront, id string) (Collection, error) {
	var resp catalogAlbumResponse
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/albums/%s", c.apiBase(), storefront, id), url.Values{
		"include":        []string{"tracks,artists,record-labels"},
		"include[songs]": []string{"artists"},
		"l":              []string{c.cfg.Language},
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
	var albumArtistID, albumArtistArtworkURL string
	if len(album.Relationships.Artists.Data) > 0 {
		albumArtist := album.Relationships.Artists.Data[0]
		albumArtistID = albumArtist.ID
		albumArtistArtworkURL = albumArtist.Attributes.Artwork.URL
	}
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
	return Collection{
		ID: album.ID, Type: TypeAlbum, Name: album.Attributes.Name, Artist: album.Attributes.ArtistName,
		ArtworkURL: album.Attributes.Artwork.URL, ArtistID: albumArtistID, ArtistArtworkURL: albumArtistArtworkURL, Tracks: tracks,
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
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/playlists/%s", c.apiBase(), storefront, id), url.Values{
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
	return Collection{ID: playlist.ID, Type: TypePlaylist, Name: playlist.Attributes.Name, Artist: firstNonEmpty(playlist.Attributes.CuratorName, playlist.Attributes.ArtistName), ArtworkURL: artworkURL, Tracks: tracks}, nil
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
	if err := c.getWithUserToken(ctx, fmt.Sprintf("%s/v1/catalog/%s/playlists/%s", c.apiBase(), storefront, id), url.Values{
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

// Station fetches a radio station's catalog metadata. The developer token
// alone authorizes this read (no media-user-token needed); the returned
// Format distinguishes a downloadable "tracks" station from a live "stream".
func (c *CatalogClient) Station(ctx context.Context, storefront, id string) (StationInfo, error) {
	var resp catalogStationResponse
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/stations/%s", c.apiBase(), storefront, id), url.Values{
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
		Format: station.Attributes.PlayParams.Format, IsLive: station.Attributes.IsLive,
	}, nil
}

// StationTracks resolves a personalized/curated radio station into a finite
// collection of catalog songs via POST /v1/me/stations/next-tracks/{id}, which
// requires the user's media-user-token (an Apple subscription token) in
// addition to the developer token. Live broadcast stations ("stream" format)
// have no static track list and return an error. The next-tracks feed is a
// rolling window, so a station "download" captures whichever upcoming songs the
// feed currently returns rather than a fixed catalog listing.
func (c *CatalogClient) StationTracks(ctx context.Context, storefront, id, mediaUserToken string) (Collection, error) {
	info, err := c.Station(ctx, storefront, id)
	if err != nil {
		return Collection{}, err
	}
	if info.Format != "tracks" {
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
	return Collection{ID: info.ID, Type: TypeStation, Name: info.Name, Artist: "Apple Music Station", ArtworkURL: info.ArtworkURL, Tracks: tracks}, nil
}

// stationNextTracks performs the authenticated POST to the personalized
// next-tracks endpoint. It mirrors get() (bearer developer token, legacy Origin
// header) but adds the Media-User-Token header and uses POST, which get() does
// not support.
func (c *CatalogClient) stationNextTracks(ctx context.Context, id, mediaUserToken string, out any) error {
	params := url.Values{
		"include[songs]": []string{"artists,albums"},
		"limit":          []string{"10"},
		"l":              []string{c.cfg.Language},
	}
	endpoint := fmt.Sprintf("%s/v1/me/stations/next-tracks/%s?%s", c.apiBase(), id, params.Encode())
	resp, err := c.doWithCatalogAuth(ctx, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Media-User-Token", mediaUserToken)
		if !c.cfg.DeveloperTokenSigningEnabled() {
			req.Header.Set("Origin", "https://music.apple.com")
		}
		req.Header.Set("User-Agent", "Mozilla/5.0")
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("station next-tracks request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return decodeJSONLimited(resp.Body, maxCatalogResponseBytes, out)
}

func (c *CatalogClient) ArtistAlbums(ctx context.Context, storefront, id string) (ArtistAlbums, error) {
	var resp catalogArtistResponse
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/artists/%s", c.apiBase(), storefront, id), url.Values{
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
		Artist: Artist{ID: raw.ID, Name: raw.Attributes.Name, ArtworkURL: raw.Attributes.Artwork.URL},
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
		return c.apiBase() + next
	}
	return c.apiBase() + "/" + next
}

func (c *CatalogClient) Artist(ctx context.Context, storefront, id string) (Artist, error) {
	var resp catalogArtistResponse
	if err := c.get(ctx, fmt.Sprintf("%s/v1/catalog/%s/artists/%s", c.apiBase(), storefront, id), url.Values{
		"l": []string{c.cfg.Language},
	}, &resp); err != nil {
		return Artist{}, err
	}
	if len(resp.Data) == 0 {
		return Artist{}, fmt.Errorf("artist %s not found", id)
	}
	raw := resp.Data[0]
	return Artist{ID: raw.ID, Name: raw.Attributes.Name, ArtworkURL: raw.Attributes.Artwork.URL}, nil
}

func (c *CatalogClient) Cover(ctx context.Context, artworkURL, format, size string) ([]byte, error) {
	return c.FetchCover(ctx, []string{artworkURL}, format, size)
}

func (c *CatalogClient) FetchCover(ctx context.Context, artworkURLs []string, format, size string) ([]byte, error) {
	if format == "" {
		format = "jpg"
	}
	var lastErr error
	for _, artworkURL := range artworkURLs {
		if artworkURL == "" {
			continue
		}
		for _, coverSize := range coverSizeFallbacks(size) {
			data, err := c.fetchCoverOnce(ctx, artworkURL, format, coverSize)
			if err == nil && len(data) > 0 {
				return data, nil
			}
			lastErr = err
		}
	}
	if lastErr == nil {
		return nil, fmt.Errorf("no artwork url available")
	}
	return nil, lastErr
}

func coverSizeFallbacks(preferred string) []string {
	defaults := []string{"5000x5000", "3000x3000", "1400x1400", "600x600", "300x300"}
	if preferred == "" {
		return defaults
	}
	out := []string{preferred}
	for _, size := range defaults {
		if size != preferred {
			out = append(out, size)
		}
	}
	return out
}

func formatArtworkURL(artworkURL, format, size string) string {
	u := artworkURL
	if format != "" && format != "jpg" && strings.Contains(u, "bb.jpg") {
		u = strings.Replace(u, "bb.jpg", "bb."+format, 1)
	}
	u = strings.ReplaceAll(u, "{w}x{h}", size)
	u = strings.ReplaceAll(u, "{f}", format)
	return u
}

func (c *CatalogClient) fetchCoverOnce(ctx context.Context, artworkURL, format, size string) ([]byte, error) {
	if artworkURL == "" {
		return nil, fmt.Errorf("empty artwork url")
	}
	u := formatArtworkURL(artworkURL, format, size)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")
	req.Header.Set("Referer", "https://music.apple.com/")
	resp, err := c.doAppleRequest(ctx, req, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cover request failed: %s", resp.Status)
	}
	return readLimited(resp.Body, maxArtworkBytes)
}

func (c *CatalogClient) get(ctx context.Context, endpoint string, params url.Values, out any) error {
	return c.getWithUserToken(ctx, endpoint, params, "", out)
}

// getWithUserToken is get plus an optional Media-User-Token header, for the
// few catalog reads whose response is richer when the request carries the
// user's subscription identity (e.g. private playlist artwork via
// include=library). An empty token degrades to a plain get.
func (c *CatalogClient) getWithUserToken(ctx context.Context, endpoint string, params url.Values, mediaUserToken string, out any) error {
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	resp, err := c.doWithCatalogAuth(ctx, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if mediaUserToken != "" {
			req.Header.Set("Media-User-Token", mediaUserToken)
		}
		if !c.cfg.DeveloperTokenSigningEnabled() {
			// The web player token is scoped to music.apple.com; a self-signed
			// developer token carries no origin claim, so no Origin header is sent.
			req.Header.Set("Origin", "https://music.apple.com")
		}
		req.Header.Set("User-Agent", "Mozilla/5.0")
		return req, nil
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return catalogRequestError{
			statusCode: resp.StatusCode,
			status:     resp.Status,
			body:       strings.TrimSpace(string(body)),
			retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
		}
	}
	return decodeJSONLimited(resp.Body, maxCatalogResponseBytes, out)
}

var (
	indexJSPattern = regexp.MustCompile(`/assets/index~[^"']+\.js`)
	jwtPattern     = regexp.MustCompile(`eyJ[A-Za-z0-9_\-=]+\.[A-Za-z0-9_\-=]+\.[A-Za-z0-9_\-=]+`)
)

// cacheUntil is the half-life proactive-refresh policy: a token is only
// trusted from the cache for half of its validity window (ttl), so it
// rotates well before Apple's own expiry - or an early rejection of a
// still-technically-valid token - could otherwise stall catalog requests
// until a full TTL passes.
func cacheUntil(now time.Time, ttl time.Duration) time.Time {
	return now.Add(ttl / 2)
}

func (c *CatalogClient) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token != "" && time.Now().Before(c.tokenUntil) {
		token := c.token
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	c.tokenRefreshMu.Lock()
	defer c.tokenRefreshMu.Unlock()
	// Another caller may have refreshed while this one waited.
	c.mu.Lock()
	if c.token != "" && time.Now().Before(c.tokenUntil) {
		token := c.token
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	if c.signer != nil {
		token, _, err := c.signer.sign(time.Now(), internalDeveloperTokenTTL, nil)
		if err != nil {
			return "", err
		}
		c.mu.Lock()
		c.token = token
		c.tokenUntil = cacheUntil(time.Now(), internalDeveloperTokenTTL)
		c.mu.Unlock()
		return token, nil
	}

	token, err := c.fetchToken(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.token = token
	c.tokenUntil = cacheUntil(time.Now(), c.cfg.TokenTTL())
	c.mu.Unlock()
	return token, nil
}

// invalidateToken clears rejected only when it is still the cached token. A
// concurrent request may already have refreshed the cache after this request
// was sent; an old 401 must not evict that newer token.
func (c *CatalogClient) invalidateToken(rejected string) {
	c.mu.Lock()
	if c.token == rejected {
		c.token = ""
		c.tokenUntil = time.Time{}
	}
	c.mu.Unlock()
}

// doWithCatalogAuth sends a request built by buildReq (called with the
// current bearer token) and, if Apple responds 401, invalidates the cached
// token, re-authenticates once via Token(), and retries the same request
// exactly once with the new token. Any other status - including a second
// 401 - is returned as-is; callers keep their existing status/body handling.
func (c *CatalogClient) doWithCatalogAuth(ctx context.Context, buildReq func(token string) (*http.Request, error)) (*http.Response, error) {
	token, err := c.Token(ctx)
	if err != nil {
		return nil, err
	}
	req, err := buildReq(token)
	if err != nil {
		return nil, err
	}
	resp, err := c.doAppleRequest(ctx, req, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	resp.Body.Close()

	c.invalidateToken(token)
	token, err = c.Token(ctx)
	if err != nil {
		return nil, err
	}
	req, err = buildReq(token)
	if err != nil {
		return nil, err
	}
	return c.doAppleRequest(ctx, req, true)
}

// doAppleRequest applies the singleton Apple request gate. A 429 penalizes
// every caller through the shared cooldown and is retried once; a second 429
// is returned to the existing status handling at the call site.
func (c *CatalogClient) doAppleRequest(ctx context.Context, req *http.Request, rateLimited bool) (*http.Response, error) {
	return c.gate.DoWith429Retry(ctx, c.http, req, rateLimited, c.retryDelay)
}

func defaultRetryDelay(header http.Header) time.Duration {
	return limits.DefaultRetryDelay(header)
}

func (c *CatalogClient) fetchToken(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://music.apple.com", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := c.doAppleRequest(ctx, req, false)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch Apple Music web player failed: %s", resp.Status)
	}
	body, err := readLimited(resp.Body, maxWebPlayerPageBytes)
	if err != nil {
		return "", err
	}
	jsPath := indexJSPattern.FindString(string(body))
	if jsPath == "" {
		return "", fmt.Errorf("cannot find Apple Music index js")
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "https://music.apple.com"+jsPath, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	jsResp, err := c.doAppleRequest(ctx, req, false)
	if err != nil {
		return "", err
	}
	defer jsResp.Body.Close()
	if jsResp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch Apple Music web player script failed: %s", jsResp.Status)
	}
	jsBody, err := readLimited(jsResp.Body, maxWebPlayerJSBytes)
	if err != nil {
		return "", err
	}
	token := jwtPattern.FindString(string(jsBody))
	if token == "" {
		return "", fmt.Errorf("cannot find Apple Music authorization token")
	}
	return token, nil
}

// webJWTToken returns a cached music.apple.com web-player JWT, scraping a
// fresh one when the cache is empty or expired. It is independent of Token,
// which serves the signed developer token once signing is enabled; this
// cache exists so EnhancedHLSViaWebToken can keep working in signed mode
// without touching the signed-token cache.
func (c *CatalogClient) webJWTToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.webToken != "" && time.Now().Before(c.webTokenUntil) {
		token := c.webToken
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	c.webRefreshMu.Lock()
	defer c.webRefreshMu.Unlock()
	// Another caller may have refreshed while this one waited.
	c.mu.Lock()
	if c.webToken != "" && time.Now().Before(c.webTokenUntil) {
		token := c.webToken
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	token, err := c.fetchToken(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.webToken = token
	c.webTokenUntil = cacheUntil(time.Now(), c.cfg.TokenTTL())
	c.mu.Unlock()
	return token, nil
}

func (c *CatalogClient) invalidateWebToken(rejected string) {
	c.mu.Lock()
	if c.webToken == rejected {
		c.webToken = ""
		c.webTokenUntil = time.Time{}
	}
	c.mu.Unlock()
}

// tokenRefreshCheckInterval bounds how often the background refresher started
// by StartTokenRefresher wakes to re-check the scraped-token caches. Each wake
// only compares timestamps under a lock; it scrapes Apple solely when a cached
// token is within tokenRefreshLead of its cached expiry.
const tokenRefreshCheckInterval = time.Minute

// tokenRefreshLead is how far before a scraped token's cached expiry the
// background refresher renews it, so a request never has to scrape inline and
// the token never lapses in the gap between two refresh checks. It stays
// comfortably below the smallest realistic effective TTL (half of the one-hour
// minimum token_cache_ttl) and above tokenRefreshCheckInterval.
const tokenRefreshLead = 5 * time.Minute

// StartTokenRefresher runs a background loop that proactively re-scrapes the
// music.apple.com web tokens shortly before their cached validity lapses, so
// catalog and enhanced-HLS requests in legacy or signed_mode_hls_source=
// web_token mode never pay the inline scrape latency and never hit a lapsed
// token. It only renews a token that has already been fetched at least once (a
// non-empty cache); it never scrapes a token nothing has used yet, preserving
// the lazy-first behavior for deployments that never touch the scraped path.
// The loop stops when ctx is done; call it once at startup.
func (c *CatalogClient) StartTokenRefresher(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(tokenRefreshCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				c.refreshExpiringWebTokens(ctx)
			}
		}
	}()
}

// refreshExpiringWebTokens renews any scraped token within tokenRefreshLead of
// its cached expiry. The self-signed developer token is skipped: when a signer
// is configured Token mints it locally with no scrape, so there is nothing to
// keep warm.
func (c *CatalogClient) refreshExpiringWebTokens(ctx context.Context) {
	if c.signer == nil {
		c.refreshLegacyTokenIfExpiring(ctx)
	}
	c.refreshWebTokenIfExpiring(ctx)
}

func (c *CatalogClient) refreshLegacyTokenIfExpiring(ctx context.Context) {
	c.mu.Lock()
	cached, until := c.token, c.tokenUntil
	c.mu.Unlock()
	if cached == "" || time.Now().Before(until.Add(-tokenRefreshLead)) {
		return
	}

	c.tokenRefreshMu.Lock()
	defer c.tokenRefreshMu.Unlock()
	// An inline caller may have refreshed while this loop waited; re-read under
	// the same lock Token uses so the two never scrape at once.
	c.mu.Lock()
	cached, until = c.token, c.tokenUntil
	c.mu.Unlock()
	if cached == "" || time.Now().Before(until.Add(-tokenRefreshLead)) {
		return
	}

	token, err := c.fetchToken(ctx)
	if err != nil {
		c.logger.Warn("proactive legacy token refresh failed; keeping cached token until it expires", "error", err)
		return
	}
	c.mu.Lock()
	c.token = token
	c.tokenUntil = cacheUntil(time.Now(), c.cfg.TokenTTL())
	c.mu.Unlock()
}

func (c *CatalogClient) refreshWebTokenIfExpiring(ctx context.Context) {
	c.mu.Lock()
	cached, until := c.webToken, c.webTokenUntil
	c.mu.Unlock()
	if cached == "" || time.Now().Before(until.Add(-tokenRefreshLead)) {
		return
	}

	c.webRefreshMu.Lock()
	defer c.webRefreshMu.Unlock()
	// Re-read under the same lock webJWTToken uses so a concurrent inline miss
	// and this proactive refresh never scrape at once.
	c.mu.Lock()
	cached, until = c.webToken, c.webTokenUntil
	c.mu.Unlock()
	if cached == "" || time.Now().Before(until.Add(-tokenRefreshLead)) {
		return
	}

	token, err := c.fetchToken(ctx)
	if err != nil {
		c.logger.Warn("proactive web token refresh failed; keeping cached token until it expires", "error", err)
		return
	}
	c.mu.Lock()
	c.webToken = token
	c.webTokenUntil = cacheUntil(time.Now(), c.cfg.TokenTTL())
	c.mu.Unlock()
}

// doWithWebAuth mirrors doWithCatalogAuth for the independent web-player JWT
// cache used by signed_mode_hls_source=web_token.
func (c *CatalogClient) doWithWebAuth(ctx context.Context, buildReq func(token string) (*http.Request, error)) (*http.Response, error) {
	token, err := c.webJWTToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err := buildReq(token)
	if err != nil {
		return nil, err
	}
	resp, err := c.doAppleRequest(ctx, req, true)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	resp.Body.Close()

	c.invalidateWebToken(token)
	token, err = c.webJWTToken(ctx)
	if err != nil {
		return nil, err
	}
	req, err = buildReq(token)
	if err != nil {
		return nil, err
	}
	return c.doAppleRequest(ctx, req, true)
}

// EnhancedHLSViaWebToken fetches a song's Enhanced HLS master playlist URL
// from amp-api.music.apple.com using a scraped music.apple.com web-player
// JWT, regardless of whether developer-token signing is enabled. It is used
// when signing is enabled but catalog.signed_mode_hls_source is "web_token":
// the self-signed developer token cannot read enhancedHls at all, so this
// reads it through the same legacy-style web token as unsigned mode while
// catalog metadata itself keeps using the official signed-token endpoint.
func (c *CatalogClient) EnhancedHLSViaWebToken(ctx context.Context, storefront, id string) (string, error) {
	params := url.Values{
		"extend": []string{"extendedAssetUrls"},
		"l":      []string{c.cfg.Language},
	}
	endpoint := fmt.Sprintf("https://amp-api.music.apple.com/v1/catalog/%s/songs/%s?%s", storefront, id, params.Encode())
	resp, err := c.doWithWebAuth(ctx, func(token string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Origin", "https://music.apple.com")
		req.Header.Set("User-Agent", "Mozilla/5.0")
		return req, nil
	})
	if err != nil {
		return "", fmt.Errorf("fetch enhanced hls with web token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("catalog request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var out catalogSongResponse
	if err := decodeJSONLimited(resp.Body, maxCatalogResponseBytes, &out); err != nil {
		return "", err
	}
	if len(out.Data) == 0 {
		return "", fmt.Errorf("song %s not found", id)
	}
	master := out.Data[0].Attributes.ExtendedAssetURLs.EnhancedHLS
	if master == "" {
		return "", fmt.Errorf("song %s has no enhanced hls manifest", id)
	}
	return master, nil
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("upstream response exceeded limit of %d bytes", limit)
	}
	return raw, nil
}

func decodeJSONLimited(r io.Reader, limit int64, out any) error {
	raw, err := readLimited(r, limit)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func mapSong(raw catalogSongData) Song {
	s := Song{
		ID: raw.ID, Name: raw.Attributes.Name, ArtistName: raw.Attributes.ArtistName, AlbumName: raw.Attributes.AlbumName,
		ComposerName: raw.Attributes.ComposerName, GenreNames: raw.Attributes.GenreNames, ReleaseDate: raw.Attributes.ReleaseDate,
		TrackNumber: raw.Attributes.TrackNumber, DiscNumber: raw.Attributes.DiscNumber, ISRC: raw.Attributes.ISRC,
		DurationInMillis: raw.Attributes.DurationInMillis,
		ContentRating:    raw.Attributes.ContentRating, HasLyrics: raw.Attributes.HasLyrics || raw.Attributes.HasTimeSyncedLyrics,
		ArtworkURL: raw.Attributes.Artwork.URL, EnhancedHLS: raw.Attributes.ExtendedAssetURLs.EnhancedHLS,
	}
	if len(raw.Relationships.Albums.Data) > 0 {
		album := raw.Relationships.Albums.Data[0]
		s.AlbumID = album.ID
		s.AlbumArtist = album.Attributes.ArtistName
		s.AlbumRelease = album.Attributes.ReleaseDate
		s.AlbumArtworkURL = album.Attributes.Artwork.URL
		s.Copyright = album.Attributes.Copyright
		s.RecordLabel = album.Attributes.RecordLabel
		s.UPC = album.Attributes.UPC
		s.TrackCount = album.Attributes.TrackCount
	}
	if len(raw.Relationships.Artists.Data) > 0 {
		artist := raw.Relationships.Artists.Data[0]
		s.ArtistID = artist.ID
		s.ArtistArtworkURL = artist.Attributes.Artwork.URL
	}
	return s
}

func mapAlbumSummary(raw catalogAlbumData) Collection {
	var artistID, artistArtworkURL string
	if len(raw.Relationships.Artists.Data) > 0 {
		artist := raw.Relationships.Artists.Data[0]
		artistID = artist.ID
		artistArtworkURL = artist.Attributes.Artwork.URL
	}
	return Collection{
		ID: raw.ID, Type: TypeAlbum, Name: raw.Attributes.Name, Artist: raw.Attributes.ArtistName,
		ArtworkURL: raw.Attributes.Artwork.URL, ArtistID: artistID, ArtistArtworkURL: artistArtworkURL,
	}
}

func maxDisc(tracks []Song) int {
	max := 0
	for _, track := range tracks {
		if track.DiscNumber > max {
			max = track.DiscNumber
		}
	}
	if max == 0 {
		return 1
	}
	return max
}

func firstNonEmpty(vals ...string) string {
	for _, val := range vals {
		if strings.TrimSpace(val) != "" {
			return val
		}
	}
	return ""
}
