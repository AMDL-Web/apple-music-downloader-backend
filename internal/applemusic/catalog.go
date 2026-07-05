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
	"strings"
	"sync"
	"time"

	"amdl/internal/config"
)

type CatalogClient struct {
	cfg        config.CatalogConfig
	http       *http.Client
	logger     *slog.Logger
	mu         sync.Mutex
	signer     *developerTokenSigner
	token      string
	tokenUntil time.Time
}

func NewCatalogClient(cfg config.CatalogConfig, logger *slog.Logger) *CatalogClient {
	return &CatalogClient{
		cfg:    cfg,
		http:   &http.Client{Timeout: 30 * time.Second},
		logger: logger,
	}
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
	token, exp, err := signer.sign(time.Now(), internalDeveloperTokenTTL, nil)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.token = token
	c.tokenUntil = exp.Add(-5 * time.Minute)
	c.mu.Unlock()
	return nil
}

// MintDeveloperToken signs a fresh developer token for external clients using
// the configured endpoint TTL and allowed-origins list. It fails when
// developer-token signing is disabled (legacy web-token mode).
func (c *CatalogClient) MintDeveloperToken() (string, time.Time, error) {
	if c.signer == nil {
		return "", time.Time{}, fmt.Errorf("developer token signing is not configured")
	}
	return c.signer.sign(time.Now(), c.cfg.DeveloperTokenTTL(), c.cfg.AllowedOrigins)
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
	song := mapSong(resp.Data[0])
	if song.AlbumID != "" {
		if album, err := c.Album(ctx, storefront, song.AlbumID); err == nil && len(album.Tracks) > 0 {
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
		}
	}
	return song, nil
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

func (c *CatalogClient) Playlist(ctx context.Context, storefront, id string) (Collection, error) {
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
	return Collection{ID: playlist.ID, Type: TypePlaylist, Name: playlist.Attributes.Name, Artist: firstNonEmpty(playlist.Attributes.CuratorName, playlist.Attributes.ArtistName), ArtworkURL: playlist.Attributes.Artwork.URL, Tracks: tracks}, nil
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
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("cover request failed: %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func (c *CatalogClient) get(ctx context.Context, endpoint string, params url.Values, out any) error {
	token, err := c.Token(ctx)
	if err != nil {
		return err
	}
	if len(params) > 0 {
		endpoint += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if !c.cfg.DeveloperTokenSigningEnabled() {
		// The web player token is scoped to music.apple.com; a self-signed
		// developer token carries no origin claim, so no Origin header is sent.
		req.Header.Set("Origin", "https://music.apple.com")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("catalog request failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

var (
	indexJSPattern = regexp.MustCompile(`/assets/index~[^"']+\.js`)
	jwtPattern     = regexp.MustCompile(`eyJ[A-Za-z0-9_\-=]+\.[A-Za-z0-9_\-=]+\.[A-Za-z0-9_\-=]+`)
)

func (c *CatalogClient) Token(ctx context.Context) (string, error) {
	c.mu.Lock()
	if c.token != "" && time.Now().Before(c.tokenUntil) {
		token := c.token
		c.mu.Unlock()
		return token, nil
	}
	c.mu.Unlock()

	if c.signer != nil {
		token, exp, err := c.signer.sign(time.Now(), internalDeveloperTokenTTL, nil)
		if err != nil {
			return "", err
		}
		c.mu.Lock()
		c.token = token
		c.tokenUntil = exp.Add(-5 * time.Minute)
		c.mu.Unlock()
		return token, nil
	}

	token, err := c.fetchToken(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.token = token
	c.tokenUntil = time.Now().Add(c.cfg.TokenTTL())
	c.mu.Unlock()
	return token, nil
}

func (c *CatalogClient) fetchToken(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://music.apple.com", nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	jsPath := indexJSPattern.FindString(string(body))
	if jsPath == "" {
		return "", fmt.Errorf("cannot find Apple Music index js")
	}
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, "https://music.apple.com"+jsPath, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0")
	jsResp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer jsResp.Body.Close()
	jsBody, err := io.ReadAll(jsResp.Body)
	if err != nil {
		return "", err
	}
	token := jwtPattern.FindString(string(jsBody))
	if token == "" {
		return "", fmt.Errorf("cannot find Apple Music authorization token")
	}
	return token, nil
}

func mapSong(raw catalogSongData) Song {
	s := Song{
		ID: raw.ID, Name: raw.Attributes.Name, ArtistName: raw.Attributes.ArtistName, AlbumName: raw.Attributes.AlbumName,
		ComposerName: raw.Attributes.ComposerName, GenreNames: raw.Attributes.GenreNames, ReleaseDate: raw.Attributes.ReleaseDate,
		TrackNumber: raw.Attributes.TrackNumber, DiscNumber: raw.Attributes.DiscNumber, ISRC: raw.Attributes.ISRC,
		ContentRating: raw.Attributes.ContentRating, HasLyrics: raw.Attributes.HasLyrics || raw.Attributes.HasTimeSyncedLyrics,
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
