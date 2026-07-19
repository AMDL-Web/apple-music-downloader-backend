package media

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"

	"amdl/internal/applemusic"
	"amdl/internal/config"
	"amdl/internal/limits"
)

type QualityRequest struct {
	URL string `json:"url"`
}

type QualitySong struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Artist    string `json:"artist"`
	Album     string `json:"album"`
	HasLyrics bool   `json:"has_lyrics"`
}

type QualityOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Available   bool   `json:"available"`
	Codec       string `json:"codec,omitempty"`
	CodecID     string `json:"codec_id,omitempty"`
	Channels    int    `json:"channels,omitempty"`
	BitDepth    int    `json:"bit_depth,omitempty"`
	SampleRate  int    `json:"sample_rate,omitempty"`
	Bitrate     int    `json:"bitrate,omitempty"`
	Description string `json:"description,omitempty"`
}

type QualityResult struct {
	Input      string          `json:"input"`
	Storefront string          `json:"storefront"`
	Type       string          `json:"type"`
	AdamID     string          `json:"adam_id"`
	Song       *QualitySong    `json:"song,omitempty"`
	Qualities  []QualityOption `json:"qualities,omitempty"`
	Tracks     []QualityTrack  `json:"tracks,omitempty"`
}

type QualityTrack struct {
	Song      QualitySong     `json:"song"`
	Qualities []QualityOption `json:"qualities"`
}

type QualityService struct {
	// store is the live runtime config; cfg is the fixed fallback used by
	// test constructors that don't wire a store.
	store       *config.Store
	cfg         config.Config
	catalog     qualityCatalog
	wrapper     qualityWrapper
	http        *http.Client
	requestGate *limits.RequestGate
}

type qualityCatalog interface {
	Song(context.Context, string, string) (applemusic.Song, error)
	SongMetadata(context.Context, string, string) (applemusic.Song, error)
	Album(context.Context, string, string) (applemusic.Collection, error)
	Playlist(context.Context, string, string, string) (applemusic.Collection, error)
	StationTracks(context.Context, string, string, string) (applemusic.Collection, error)
	ArtistAlbums(context.Context, string, string) (applemusic.ArtistAlbums, error)
	EnhancedHLSViaWebToken(context.Context, string, string) (string, error)
}

type qualityWrapper interface {
	M3U8(context.Context, string) (string, error)
}

func NewQualityService(store *config.Store, catalog *applemusic.CatalogClient, wrapperClient qualityWrapper) *QualityService {
	return &QualityService{store: store, catalog: catalog, wrapper: wrapperClient, http: newHTTPClient(), requestGate: catalog.RequestGate()}
}

func NewQualityServiceWithCatalog(cfg config.Config, catalog qualityCatalog) *QualityService {
	return &QualityService{cfg: cfg, catalog: catalog, http: newHTTPClient()}
}

func (s *QualityService) baseConfig() config.Config {
	if s.store != nil {
		return s.store.Get()
	}
	return s.cfg
}

func (s *QualityService) QueryQuality(ctx context.Context, req QualityRequest) (QualityResult, error) {
	cfg := s.baseConfig()
	parsed, err := applemusic.ParseWithAlbumTrackMode(strings.TrimSpace(req.URL), cfg.Catalog.AlbumTrackURLMode)
	if err != nil {
		return QualityResult{}, err
	}
	songs, err := s.resolveQualityTracks(ctx, cfg, parsed)
	if err != nil {
		return QualityResult{}, err
	}
	if len(songs) == 0 {
		return QualityResult{}, fmt.Errorf("%s %s has no songs", parsed.Type, parsed.ID)
	}

	tracks, err := s.queryTrackQualities(ctx, cfg, parsed.Storefront, songs, parsed.Type != applemusic.TypeSong)
	if err != nil {
		return QualityResult{}, err
	}
	result := QualityResult{
		Input: req.URL, Storefront: parsed.Storefront, Type: string(parsed.Type), AdamID: parsed.ID,
	}
	if parsed.Type == applemusic.TypeSong {
		result.Song = &tracks[0].Song
		result.Qualities = tracks[0].Qualities
	} else {
		result.Tracks = tracks
	}
	return result, nil
}

func (s *QualityService) resolveQualityTracks(ctx context.Context, cfg config.Config, parsed applemusic.ParsedURL) ([]applemusic.Song, error) {
	switch parsed.Type {
	case applemusic.TypeSong:
		song, err := s.catalog.Song(ctx, parsed.Storefront, parsed.ID)
		if err != nil {
			return nil, err
		}
		return []applemusic.Song{song}, nil
	case applemusic.TypeAlbum:
		album, err := s.catalog.Album(ctx, parsed.Storefront, parsed.ID)
		if err != nil {
			return nil, err
		}
		return album.Tracks, nil
	case applemusic.TypePlaylist:
		playlist, err := s.catalog.Playlist(ctx, parsed.Storefront, parsed.ID, strings.TrimSpace(cfg.Catalog.MediaUserToken))
		if err != nil {
			return nil, err
		}
		return playlist.Tracks, nil
	case applemusic.TypeStation:
		station, err := s.catalog.StationTracks(ctx, parsed.Storefront, parsed.ID, strings.TrimSpace(cfg.Catalog.MediaUserToken))
		if err != nil {
			return nil, err
		}
		return station.Tracks, nil
	case applemusic.TypeArtist:
		artist, err := s.catalog.ArtistAlbums(ctx, parsed.Storefront, parsed.ID)
		if err != nil {
			return nil, err
		}
		var songs []applemusic.Song
		for _, summary := range artist.Albums {
			album, err := s.catalog.Album(ctx, parsed.Storefront, summary.ID)
			if err != nil {
				return nil, err
			}
			songs = append(songs, album.Tracks...)
		}
		return songs, nil
	default:
		return nil, fmt.Errorf("quality query does not support input type %s", parsed.Type)
	}
}

type qualityProbeResult struct {
	song      applemusic.Song
	qualities []QualityOption
}

func (s *QualityService) queryTrackQualities(ctx context.Context, cfg config.Config, storefront string, songs []applemusic.Song, refreshManifest bool) ([]QualityTrack, error) {
	unique := make([]applemusic.Song, 0, len(songs))
	indexes := make(map[string]int, len(songs))
	for _, song := range songs {
		if _, exists := indexes[song.ID]; exists {
			continue
		}
		indexes[song.ID] = len(unique)
		unique = append(unique, song)
	}

	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make([]qualityProbeResult, len(unique))

	var wg sync.WaitGroup
	var errOnce sync.Once
	var firstErr error
	// Match download scheduling: fan out the tracks here and let the shared
	// catalog request gate and wrapper data-request pool enforce the configured
	// process-wide concurrency limits. A separate per-query cap would make this
	// endpoint behave differently from downloads and leave shared capacity idle.
	for i := range unique {
		if probeCtx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			song, qualities, err := s.querySongQuality(probeCtx, cfg, storefront, unique[i], refreshManifest)
			if err != nil {
				errOnce.Do(func() {
					firstErr = fmt.Errorf("query quality for song %s: %w", unique[i].ID, err)
					cancel()
				})
				return
			}
			results[i] = qualityProbeResult{song: song, qualities: qualities}
		}(i)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	tracks := make([]QualityTrack, len(songs))
	for i, song := range songs {
		probe := results[indexes[song.ID]]
		tracks[i] = QualityTrack{
			Song:      qualitySong(mergeQualitySong(song, probe.song)),
			Qualities: probe.qualities,
		}
	}
	return tracks, nil
}

func (s *QualityService) querySongQuality(ctx context.Context, cfg config.Config, storefront string, song applemusic.Song, refreshManifest bool) (applemusic.Song, []QualityOption, error) {
	master := song.EnhancedHLS
	if cfg.Catalog.DeveloperTokenSigningEnabled() {
		// A self-signed developer token cannot read enhancedHls, so quality is
		// analyzed from either the authorized device manifest or a scraped
		// web-player token, per catalog.signed_mode_hls_source.
		if cfg.Catalog.EnhancedHLSFromWebToken() {
			hls, err := s.catalog.EnhancedHLSViaWebToken(ctx, storefront, song.ID)
			if err != nil {
				return applemusic.Song{}, nil, fmt.Errorf("fetch enhanced hls via web token: %w", err)
			}
			master = hls
		} else {
			if s.wrapper == nil {
				return applemusic.Song{}, nil, fmt.Errorf("developer-token signing enabled but wrapper is not configured")
			}
			m3u8, err := s.wrapper.M3U8(ctx, song.ID)
			if err != nil {
				return applemusic.Song{}, nil, fmt.Errorf("request device m3u8: %w", err)
			}
			master = m3u8
		}
	} else if refreshManifest && strings.TrimSpace(master) == "" {
		metadata, err := s.catalog.SongMetadata(ctx, storefront, song.ID)
		if err != nil {
			return applemusic.Song{}, nil, err
		}
		song = mergeQualitySong(song, metadata)
		master = metadata.EnhancedHLS
	}
	if strings.TrimSpace(master) == "" {
		return applemusic.Song{}, nil, fmt.Errorf("song %s has no enhanced hls manifest", song.ID)
	}
	variants, err := FetchMasterVariants(ctx, s.http, master, s.requestGate)
	if err != nil {
		return applemusic.Song{}, nil, err
	}
	return song, SummarizeQualities(variants), nil
}

func qualitySong(song applemusic.Song) QualitySong {
	return QualitySong{ID: song.ID, Name: song.Name, Artist: song.ArtistName, Album: song.AlbumName, HasLyrics: song.HasLyrics}
}

func mergeQualitySong(preferred, fallback applemusic.Song) applemusic.Song {
	preferred.ID = firstNonEmpty(preferred.ID, fallback.ID)
	preferred.Name = firstNonEmpty(preferred.Name, fallback.Name)
	preferred.ArtistName = firstNonEmpty(preferred.ArtistName, fallback.ArtistName)
	preferred.AlbumName = firstNonEmpty(preferred.AlbumName, fallback.AlbumName)
	preferred.HasLyrics = preferred.HasLyrics || fallback.HasLyrics
	preferred.EnhancedHLS = firstNonEmpty(preferred.EnhancedHLS, fallback.EnhancedHLS)
	return preferred
}

type qualitySpec struct {
	id       string
	label    string
	codec    string
	channels int
	pattern  *regexp.Regexp
}

var qualitySpecs = []qualitySpec{
	{id: "aac", label: "AAC", codec: "AAC", channels: 2, pattern: codecPatterns["aac"]},
	{id: "aac-binaural", label: "AAC Binaural", codec: "AAC", channels: 2, pattern: codecPatterns["aac-binaural"]},
	{id: "aac-downmix", label: "AAC Downmix", codec: "AAC", channels: 2, pattern: codecPatterns["aac-downmix"]},
	{id: "alac", label: "Lossless", codec: "ALAC", channels: 2, pattern: codecPatterns["alac"]},
	{id: "ec3", label: "Dolby Atmos", codec: "E-AC-3", channels: 16, pattern: codecPatterns["ec3"]},
	{id: "ac3", label: "Dolby Audio", codec: "AC-3", channels: 6, pattern: codecPatterns["ac3"]},
}

func SummarizeQualities(variants []PlaylistVariant) []QualityOption {
	out := make([]QualityOption, 0, len(qualitySpecs))
	for _, spec := range qualitySpecs {
		option := QualityOption{ID: spec.id, Label: spec.label, Codec: spec.codec}
		if selected, ok := bestVariant(variants, spec.pattern); ok {
			option.Available = true
			option.CodecID = selected.Audio
			option.Channels = spec.channels
			option.BitDepth = selected.BitDepth
			option.SampleRate = selected.SampleRate
			option.Bitrate = selected.Bandwidth
			option.Description = qualityDescription(option)
		}
		out = append(out, option)
	}
	return out
}

func bestVariant(variants []PlaylistVariant, pattern *regexp.Regexp) (PlaylistVariant, bool) {
	var filtered []PlaylistVariant
	for _, v := range variants {
		if pattern != nil && pattern.MatchString(v.Audio) {
			filtered = append(filtered, v)
		}
	}
	if len(filtered) == 0 {
		return PlaylistVariant{}, false
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].SampleRate != filtered[j].SampleRate {
			return filtered[i].SampleRate > filtered[j].SampleRate
		}
		if filtered[i].BitDepth != filtered[j].BitDepth {
			return filtered[i].BitDepth > filtered[j].BitDepth
		}
		return filtered[i].Bandwidth > filtered[j].Bandwidth
	})
	return filtered[0], true
}

func qualityDescription(option QualityOption) string {
	parts := []string{option.Codec}
	if option.Channels > 0 {
		parts = append(parts, fmt.Sprintf("%d Channel", option.Channels))
	}
	if option.BitDepth > 0 && option.SampleRate > 0 {
		parts = append(parts, fmt.Sprintf("%d-bit/%d kHz", option.BitDepth, option.SampleRate/1000))
	} else if option.Bitrate > 0 {
		parts = append(parts, fmt.Sprintf("%d kbps", option.Bitrate/1000))
	}
	return strings.Join(parts, " | ")
}
