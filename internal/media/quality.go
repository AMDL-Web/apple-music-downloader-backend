package media

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"amdl/internal/applemusic"
	"amdl/internal/config"
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
	downloader *Downloader
}

func NewQualityService(downloader *Downloader) *QualityService {
	return &QualityService{downloader: downloader}
}

func NewQualityServiceWithCatalog(cfg config.Config, catalog downloaderCatalog, wrapperClient downloaderWrapper) *QualityService {
	return NewQualityService(&Downloader{cfg: cfg, catalog: catalog, wrapper: wrapperClient, http: newHTTPClient()})
}

func (s *QualityService) QueryQuality(ctx context.Context, req QualityRequest) (QualityResult, error) {
	if s.downloader == nil {
		return QualityResult{}, fmt.Errorf("quality downloader is not configured")
	}
	downloader := s.downloader.withConfig(s.downloader.baseConfig())
	validated, err := downloader.validateRequest(ctx, strings.TrimSpace(req.URL))
	if err != nil {
		return QualityResult{}, err
	}
	parsed := applemusic.ParsedURL{
		Raw: req.URL, Storefront: validated.Storefront, Type: applemusic.URLType(validated.Type), ID: validated.ID,
	}
	resolved, _, err := retryValue(ctx, downloader.cfg.Download.MaxAttempts, retryBackoff, func(int) (resolvedCollection, error) {
		return downloader.resolveCollection(ctx, parsed)
	}, nil)
	if err != nil {
		return QualityResult{}, err
	}
	if len(resolved.Tracks) == 0 {
		return QualityResult{}, fmt.Errorf("no downloadable songs found")
	}

	tracks, err := downloader.queryTrackQualities(ctx, parsed, resolved.Tracks)
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

func (d *Downloader) queryTrackQualities(ctx context.Context, parsed applemusic.ParsedURL, songs []applemusic.Song) ([]QualityTrack, error) {
	tracks := make([]QualityTrack, len(songs))
	metadata := newTrackMetadataResolver(d, parsed.Storefront)
	finished := make([]bool, len(songs))
	err := runTrackTasks(ctx, len(songs), finished, func(trackCtx context.Context, i int) error {
		song, _, err := retryValue(trackCtx, d.cfg.Download.MaxAttempts, retryBackoff, func(int) (applemusic.Song, error) {
			return metadata.song(trackCtx, songs[i], parsed.Type)
		}, nil)
		if err != nil {
			return fmt.Errorf("resolve metadata for song %s: %w", songs[i].ID, err)
		}
		qualities, err := d.querySongQualities(trackCtx, parsed.Storefront, song)
		if err != nil {
			return fmt.Errorf("query quality for song %s: %w", song.ID, err)
		}
		tracks[i] = QualityTrack{Song: qualitySong(song), Qualities: qualities}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return tracks, nil
}

func (d *Downloader) querySongQualities(ctx context.Context, storefront string, song applemusic.Song) ([]QualityOption, error) {
	variants, _, err := retryValue(ctx, d.cfg.Download.MaxAttempts, retryBackoff, func(int) ([]variant, error) {
		master, err := d.resolveEnhancedHLS(ctx, storefront, song)
		if err != nil {
			return nil, err
		}
		return fetchMasterVariants(ctx, d.http, master, d.requestGate)
	}, nil)
	if err != nil {
		return nil, err
	}

	qualities := make([]QualityOption, 0, len(qualitySpecs))
	// The endpoint reports every supported enhanced codec, not only the
	// configured download fallback subset. It inventories the one fetched
	// master playlist with the same selector downloads use, but intentionally
	// stops before fetching any concrete media playlist.
	for _, spec := range qualitySpecs {
		option := QualityOption{ID: spec.id, Label: spec.label, Codec: spec.codec}
		selected, err := selectVariant(variants, spec.id, d.cfg.Download.ALACMaxSampleRate, d.cfg.Download.ALACMaxBitDepth)
		if err != nil {
			var notFound codecNotFoundError
			if errors.As(err, &notFound) {
				qualities = append(qualities, option)
				continue
			}
			return nil, err
		}
		option.Available = true
		option.CodecID = selected.Audio
		option.Channels = spec.channels
		option.BitDepth = selected.BitDepth
		option.SampleRate = selected.SampleRate
		option.Bitrate = selected.Bandwidth
		option.Description = qualityDescription(option)
		qualities = append(qualities, option)
	}
	return qualities, nil
}

func qualitySong(song applemusic.Song) QualitySong {
	return QualitySong{ID: song.ID, Name: song.Name, Artist: song.ArtistName, Album: song.AlbumName, HasLyrics: song.HasLyrics}
}

type qualitySpec struct {
	id       string
	label    string
	codec    string
	channels int
}

var qualitySpecs = []qualitySpec{
	{id: "aac", label: "AAC", codec: "AAC", channels: 2},
	{id: "aac-binaural", label: "AAC Binaural", codec: "AAC", channels: 2},
	{id: "aac-downmix", label: "AAC Downmix", codec: "AAC", channels: 2},
	{id: "alac", label: "Lossless", codec: "ALAC", channels: 2},
	{id: "ec3", label: "Dolby Atmos", codec: "E-AC-3", channels: 16},
	{id: "ac3", label: "Dolby Audio", codec: "AC-3", channels: 6},
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
