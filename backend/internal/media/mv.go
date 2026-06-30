package media

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// widevineKeyFormat is the HLS KEYFORMAT identifying the Widevine EXT-X-KEY.
const widevineKeyFormat = "urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed"

var mvAudioRankRe = regexp.MustCompile(`_gr(\d+)_`)

// mvVariant is a single video rendition from the music-video master playlist.
// Apple bundles an audio group reference with each video variant, so audio is
// selected via the chosen variant's AudioGroup to guarantee both renditions are
// decryptable at the same robustness level.
type mvVariant struct {
	URI          string
	Resolution   string
	Height       int
	Bandwidth    int
	AudioGroup   string
	AllowedCPC   string
	SoftwareOnly bool // true when Widevine software (L3) decryption is permitted
}

// mvMedia describes a decrypt-pending media playlist: its Widevine PSSH (the
// full base64 pssh box from the data: URI), the original key URI to forward to
// the license server, the init (EXT-X-MAP) segment and ordered media segments.
type mvMedia struct {
	KeyURI   string // full Widevine "data:text/plain;base64,..." URI
	PSSHB64  string // base64-encoded Widevine pssh box
	InitURL  string
	Segments []string
}

// selectMVStreams chooses the best video variant the embedded (L3/software)
// Widevine device can decrypt, no taller than maxHeight, and resolves its
// bundled audio rendition. audioType nudges variant selection toward a matching
// audio group when several otherwise-equal candidates exist.
func selectMVStreams(master, masterURL string, maxHeight int, audioType string) (video mvVariant, audioURL string, err error) {
	variants := parseMVVariants(master, masterURL)
	if len(variants) == 0 {
		return mvVariant{}, "", fmt.Errorf("music video master playlist has no video variants")
	}

	// Prefer variants the software CDM can decrypt.
	candidates := make([]mvVariant, 0, len(variants))
	for _, v := range variants {
		if v.SoftwareOnly {
			candidates = append(candidates, v)
		}
	}
	if len(candidates) == 0 {
		// No software-capable variant advertised; fall back to everything and
		// hope the license server cooperates.
		candidates = variants
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Bandwidth > candidates[j].Bandwidth })

	withinHeight := candidates[:0:0]
	if maxHeight > 0 {
		for _, v := range candidates {
			if v.Height == 0 || v.Height <= maxHeight {
				withinHeight = append(withinHeight, v)
			}
		}
	}
	if len(withinHeight) == 0 {
		// nothing within the cap: pick the shortest available rendition.
		shortest := candidates[0]
		for _, v := range candidates {
			if v.Height > 0 && (shortest.Height == 0 || v.Height < shortest.Height) {
				shortest = v
			}
		}
		withinHeight = []mvVariant{shortest}
	}

	video = withinHeight[0]
	// If an audio preference is configured, prefer a (still best-bandwidth)
	// variant whose bundled audio group matches.
	if pref := mvAudioGroupHints(audioType); len(pref) > 0 {
		for _, hint := range pref {
			for _, v := range withinHeight {
				if strings.Contains(strings.ToLower(v.AudioGroup), hint) {
					video = v
					goto picked
				}
			}
		}
	}
picked:

	audios := parseMVAudioStreams(master, masterURL)
	if video.AudioGroup != "" {
		for _, a := range audios {
			if a.groupID == video.AudioGroup {
				return video, a.uri, nil
			}
		}
	}
	if len(audios) == 0 {
		return mvVariant{}, "", fmt.Errorf("music video master playlist has no audio renditions")
	}
	// Audio group not found by exact match: fall back to the highest-ranked one.
	sort.Slice(audios, func(i, j int) bool { return audios[i].rank > audios[j].rank })
	return video, audios[0].uri, nil
}

func mvAudioGroupHints(audioType string) []string {
	switch strings.ToLower(strings.TrimSpace(audioType)) {
	case "ac3":
		return []string{"ac3", "stereo-256"}
	case "aac", "stereo":
		return []string{"stereo-256", "stereo-128"}
	case "atmos":
		return []string{"atmos", "ac3", "stereo-256"}
	default:
		return nil
	}
}

func parseMVVariants(master, base string) []mvVariant {
	sc := bufio.NewScanner(strings.NewReader(master))
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	var out []mvVariant
	var pending map[string]string
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		// Skip I-frame (trick play) variants entirely.
		if strings.HasPrefix(line, "#EXT-X-I-FRAME-STREAM-INF:") {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			pending = parseAttrs(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			continue
		}
		if pending != nil && line != "" && !strings.HasPrefix(line, "#") {
			cpc := pending["ALLOWED-CPC"]
			v := mvVariant{
				URI:          absURL(base, line),
				Resolution:   pending["RESOLUTION"],
				Bandwidth:    atoi(firstNonEmpty(pending["AVERAGE-BANDWIDTH"], pending["BANDWIDTH"])),
				AudioGroup:   pending["AUDIO"],
				AllowedCPC:   cpc,
				SoftwareOnly: strings.Contains(cpc, "WIDEVINE_SOFTWARE"),
			}
			v.Height = parseResolutionHeight(pending["RESOLUTION"])
			out = append(out, v)
			pending = nil
		}
	}
	return out
}

func parseResolutionHeight(res string) int {
	parts := strings.SplitN(strings.ToLower(res), "x", 2)
	if len(parts) == 2 {
		return atoi(parts[1])
	}
	return 0
}

type mvAudioStream struct {
	uri     string
	groupID string
	rank    int
}

func parseMVAudioStreams(master, base string) []mvAudioStream {
	sc := bufio.NewScanner(strings.NewReader(master))
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	var out []mvAudioStream
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "#EXT-X-MEDIA:") {
			continue
		}
		attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
		if !strings.EqualFold(attrs["TYPE"], "AUDIO") {
			continue
		}
		uri := attrs["URI"]
		if uri == "" {
			continue
		}
		rank := 0
		if m := mvAudioRankRe.FindStringSubmatch(uri); len(m) == 2 {
			rank = atoi(m[1])
		}
		out = append(out, mvAudioStream{uri: absURL(base, uri), groupID: attrs["GROUP-ID"], rank: rank})
	}
	return out
}

// parseMVMedia reads a media playlist and extracts the Widevine pssh, the
// original key URI, the init segment and the ordered list of media segments.
func parseMVMedia(body, base string) (mvMedia, error) {
	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	var media mvMedia
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "#EXT-X-KEY:"):
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			if !strings.EqualFold(attrs["KEYFORMAT"], widevineKeyFormat) {
				continue
			}
			keyURI := attrs["URI"]
			if keyURI == "" {
				continue
			}
			media.KeyURI = keyURI
			if idx := strings.Index(keyURI, "base64,"); idx >= 0 {
				media.PSSHB64 = keyURI[idx+len("base64,"):]
			}
		case strings.HasPrefix(line, "#EXT-X-MAP:"):
			mapURI := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MAP:"))["URI"]
			if mapURI != "" {
				media.InitURL = absURL(base, mapURI)
			}
		case strings.HasPrefix(line, "#"):
			// other tags ignored
		default:
			media.Segments = append(media.Segments, absURL(base, line))
		}
	}
	if err := sc.Err(); err != nil {
		return mvMedia{}, err
	}
	if media.InitURL == "" {
		return mvMedia{}, fmt.Errorf("media playlist has no EXT-X-MAP init segment")
	}
	if len(media.Segments) == 0 {
		return mvMedia{}, fmt.Errorf("media playlist has no media segments")
	}
	if media.KeyURI == "" || media.PSSHB64 == "" {
		return mvMedia{}, fmt.Errorf("media playlist has no Widevine key")
	}
	return media, nil
}

// downloadMVSegments downloads the init segment followed by all media segments
// using bounded concurrency, returning them concatenated in playlist order.
func downloadMVSegments(ctx context.Context, client *http.Client, initURL string, segments []string, onProgress func(float64)) ([]byte, error) {
	urls := make([]string, 0, len(segments)+1)
	urls = append(urls, initURL)
	urls = append(urls, segments...)

	results := make([][]byte, len(urls))
	const maxConcurrency = 6
	sem := make(chan struct{}, maxConcurrency)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	var done int

	for i, u := range urls {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case sem <- struct{}{}:
		}
		wg.Add(1)
		go func(idx int, segURL string) {
			defer wg.Done()
			defer func() { <-sem }()
			data, err := downloadSegmentWithRetry(ctx, client, segURL)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("download segment %d: %w", idx, err)
				}
				return
			}
			results[idx] = data
			done++
			if onProgress != nil {
				onProgress(float64(done) / float64(len(urls)))
			}
		}(i, u)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	var total int
	for _, d := range results {
		total += len(d)
	}
	out := make([]byte, 0, total)
	for _, d := range results {
		out = append(out, d...)
	}
	return out, nil
}

// downloadSegmentWithRetry fetches a single MV segment, retrying a few times on
// transient CDN failures (e.g. sporadic 403s under concurrent load).
func downloadSegmentWithRetry(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	const maxTries = 5
	var lastErr error
	for attempt := 0; attempt < maxTries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt) * 400 * time.Millisecond):
			}
		}
		data, err := downloadBytes(ctx, client, url, nil)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}
