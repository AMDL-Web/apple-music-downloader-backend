package media

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"amdl/internal/applemusic"
	"amdl/internal/config"
)

type QualityRequest struct {
	URL string `json:"url"`
}

type QualitySong struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Artist string `json:"artist"`
	Album  string `json:"album"`
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
	Song       QualitySong     `json:"song"`
	Qualities  []QualityOption `json:"qualities"`
}

type QualityService struct {
	cfg     config.Config
	catalog qualityCatalog
	wrapper qualityWrapper
	http    *http.Client
}

type qualityCatalog interface {
	Song(context.Context, string, string) (applemusic.Song, error)
}

type qualityWrapper interface {
	M3U8(context.Context, string) (string, error)
}

func NewQualityService(cfg config.Config, catalog *applemusic.CatalogClient, wrapperClient qualityWrapper) *QualityService {
	return &QualityService{cfg: cfg, catalog: catalog, wrapper: wrapperClient, http: newHTTPClient()}
}

func NewQualityServiceWithCatalog(cfg config.Config, catalog qualityCatalog) *QualityService {
	return &QualityService{cfg: cfg, catalog: catalog, http: newHTTPClient()}
}

func (s *QualityService) QueryQuality(ctx context.Context, req QualityRequest) (QualityResult, error) {
	parsed, err := applemusic.ParseWithAlbumTrackMode(strings.TrimSpace(req.URL), s.cfg.Catalog.AlbumTrackURLMode)
	if err != nil {
		return QualityResult{}, err
	}
	if parsed.Type != applemusic.TypeSong {
		return QualityResult{}, fmt.Errorf("quality query only supports song URLs")
	}
	song, err := s.catalog.Song(ctx, parsed.Storefront, parsed.ID)
	if err != nil {
		return QualityResult{}, err
	}
	master := song.EnhancedHLS
	if s.cfg.Catalog.DeveloperTokenSigningEnabled() {
		// A self-signed developer token cannot read enhancedHls, so quality is
		// analyzed from the authorized device manifest instead.
		if s.wrapper == nil {
			return QualityResult{}, fmt.Errorf("developer-token signing enabled but wrapper is not configured")
		}
		m3u8, err := s.wrapper.M3U8(ctx, song.ID)
		if err != nil {
			return QualityResult{}, fmt.Errorf("request device m3u8: %w", err)
		}
		master = m3u8
	}
	if strings.TrimSpace(master) == "" {
		return QualityResult{}, fmt.Errorf("song %s has no enhanced hls manifest", song.ID)
	}
	variants, err := FetchMasterVariants(ctx, s.http, master)
	if err != nil {
		return QualityResult{}, err
	}
	return QualityResult{
		Input: req.URL, Storefront: parsed.Storefront, Type: string(parsed.Type), AdamID: song.ID,
		Song:      QualitySong{ID: song.ID, Name: song.Name, Artist: song.ArtistName, Album: song.AlbumName},
		Qualities: SummarizeQualities(variants),
	}, nil
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
