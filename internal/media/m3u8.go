package media

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const prefetchKey = "skd://itunes.apple.com/P000000000/s1/e1"

type m3u8Info = selectedMediaInfo

type variant struct {
	URI        string
	Audio      string
	Codecs     string
	Bandwidth  int
	BitDepth   int
	SampleRate int
}

type PlaylistVariant struct {
	URI        string `json:"-"`
	Audio      string `json:"codec_id"`
	Codecs     string `json:"codecs,omitempty"`
	Bandwidth  int    `json:"bandwidth,omitempty"`
	BitDepth   int    `json:"bit_depth,omitempty"`
	SampleRate int    `json:"sample_rate,omitempty"`
}

type codecNotFoundError struct {
	Codec string
}

func (e codecNotFoundError) Error() string {
	return fmt.Sprintf("codec %s not found in manifest", e.Codec)
}

func (e codecNotFoundError) NonRetryable() bool {
	return true
}

var codecPatterns = map[string]*regexp.Regexp{
	"alac":         regexp.MustCompile(`audio-alac-stereo-\d{5,6}-\d{2}$`),
	"aac":          regexp.MustCompile(`audio-stereo-\d{3}$`),
	"aac-binaural": regexp.MustCompile(`audio-stereo-\d{3}-binaural$`),
	"aac-downmix":  regexp.MustCompile(`audio-stereo-\d{3}-downmix$`),
	"ec3":          regexp.MustCompile(`audio-(atmos|ec3)-\d{4}$`),
	"ac3":          regexp.MustCompile(`audio-ac3-\d{3}$`),
}

func extractMedia(ctx context.Context, httpClient *http.Client, masterURL, codec string, maxSampleRate, maxBitDepth int) (m3u8Info, error) {
	master, err := downloadText(ctx, httpClient, masterURL)
	if err != nil {
		return m3u8Info{}, err
	}
	variants := parseMaster(master, masterURL)
	var filtered []variant
	pat := codecPatterns[codec]
	for _, v := range variants {
		if pat != nil && !pat.MatchString(v.Audio) {
			continue
		}
		if codec == "alac" {
			if maxSampleRate > 0 && v.SampleRate > maxSampleRate {
				continue
			}
			if maxBitDepth > 0 && v.BitDepth > maxBitDepth {
				continue
			}
		}
		filtered = append(filtered, v)
	}
	if len(filtered) == 0 {
		return m3u8Info{}, codecNotFoundError{Codec: codec}
	}
	sort.Slice(filtered, func(i, j int) bool { return filtered[i].Bandwidth > filtered[j].Bandwidth })
	selected := filtered[0]
	media, err := downloadText(ctx, httpClient, selected.URI)
	if err != nil {
		return m3u8Info{}, err
	}
	mediaURI, keys, err := parseMedia(media, selected.URI, codec)
	if err != nil {
		return m3u8Info{}, err
	}
	return m3u8Info{MediaURI: mediaURI, Keys: keys, CodecID: selected.Audio, BitDepth: selected.BitDepth, SampleRate: selected.SampleRate, Bandwidth: selected.Bandwidth}, nil
}

func parseMaster(body, base string) []variant {
	sc := bufio.NewScanner(strings.NewReader(body))
	var out []variant
	var pending map[string]string
	mediaExtras := map[string]map[string]string{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#EXT-X-MEDIA:") {
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
			if attrs["GROUP-ID"] != "" {
				mediaExtras[attrs["GROUP-ID"]] = attrs
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			pending = parseAttrs(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			continue
		}
		if pending != nil && line != "" && !strings.HasPrefix(line, "#") {
			audio := pending["AUDIO"]
			extras := mediaExtras[audio]
			out = append(out, variant{
				URI: absURL(base, line), Audio: audio, Bandwidth: atoi(firstNonEmpty(pending["AVERAGE-BANDWIDTH"], pending["BANDWIDTH"])),
				Codecs:     pending["CODECS"],
				BitDepth:   atoi(firstNonEmpty(extras["BIT-DEPTH"], extras["bit_depth"])),
				SampleRate: atoi(firstNonEmpty(extras["SAMPLE-RATE"], extras["sample_rate"])),
			})
			pending = nil
		}
	}
	return out
}

func ParseMasterPlaylist(body, base string) []PlaylistVariant {
	private := parseMaster(body, base)
	out := make([]PlaylistVariant, 0, len(private))
	for _, v := range private {
		out = append(out, PlaylistVariant{
			URI: v.URI, Audio: v.Audio, Codecs: v.Codecs, Bandwidth: v.Bandwidth, BitDepth: v.BitDepth, SampleRate: v.SampleRate,
		})
	}
	return out
}

func FetchMasterVariants(ctx context.Context, client *http.Client, masterURL string) ([]PlaylistVariant, error) {
	body, err := downloadText(ctx, client, masterURL)
	if err != nil {
		return nil, err
	}
	return ParseMasterPlaylist(body, masterURL), nil
}

func parseMedia(body, base, codec string) (string, []string, error) {
	sc := bufio.NewScanner(strings.NewReader(body))
	keys := []string{prefetchKey}
	keySuffix := map[string]string{"alac": "c23", "aac": "c22", "aac-downmix": "c24", "aac-binaural": "c24", "ec3": "c24", "ac3": "c24"}[codec]
	if keySuffix == "" {
		keySuffix = "c6"
	}
	mediaURI := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "#EXT-X-KEY:") {
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-KEY:"))
			key := attrs["URI"]
			if strings.HasPrefix(key, "skd://") && (strings.HasSuffix(key, keySuffix) || strings.HasSuffix(key, "c6")) {
				keys = append(keys, key)
			}
		}
		if strings.HasPrefix(line, "#EXT-X-MAP:") {
			attrs := parseAttrs(strings.TrimPrefix(line, "#EXT-X-MAP:"))
			if attrs["URI"] != "" {
				mediaURI = absURL(base, attrs["URI"])
			}
		}
	}
	if mediaURI == "" {
		return "", nil, fmt.Errorf("manifest has no EXT-X-MAP media URI")
	}
	return mediaURI, keys, sc.Err()
}

func parseAttrs(v string) map[string]string {
	out := map[string]string{}
	var key strings.Builder
	var val strings.Builder
	inKey := true
	inQuote := false
	flush := func() {
		k := strings.TrimSpace(key.String())
		value := strings.Trim(strings.TrimSpace(val.String()), `"`)
		if k != "" {
			out[k] = value
		}
		key.Reset()
		val.Reset()
		inKey = true
	}
	for _, r := range v {
		switch {
		case inKey && r == '=':
			inKey = false
		case !inKey && r == '"':
			inQuote = !inQuote
			val.WriteRune(r)
		case !inKey && r == ',' && !inQuote:
			flush()
		case inKey:
			key.WriteRune(r)
		default:
			val.WriteRune(r)
		}
	}
	flush()
	return out
}

func downloadText(ctx context.Context, client *http.Client, uri string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("download %s failed: %s", uri, resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	return string(raw), err
}

// downloadBytes fetches uri into memory. onProgress, if non-nil, is called
// periodically with a value in [0,1] representing download progress based on
// Content-Length. If the server does not advertise Content-Length the callback
// receives -1 on every call.
func downloadBytes(ctx context.Context, client *http.Client, uri string, onProgress func(float64)) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download %s failed: %s", uri, resp.Status)
	}

	total, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	if total <= 0 {
		// Fallback: try X-Apple-MS-Content-Length used by Apple CDN
		total, _ = strconv.ParseInt(resp.Header.Get("X-Apple-MS-Content-Length"), 10, 64)
	}

	if onProgress == nil || total <= 0 {
		// No progress tracking possible – just read all at once
		if onProgress != nil {
			onProgress(-1)
		}
		return io.ReadAll(resp.Body)
	}

	buf := bytes.NewBuffer(make([]byte, 0, total))
	chunk := make([]byte, 32*1024)
	var downloaded int64
	for {
		n, readErr := resp.Body.Read(chunk)
		if n > 0 {
			buf.Write(chunk[:n])
			downloaded += int64(n)
			onProgress(float64(downloaded) / float64(total))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	return buf.Bytes(), nil
}

func absURL(base, ref string) string {
	u, err := url.Parse(ref)
	if err == nil && u.IsAbs() {
		return ref
	}
	b, err := url.Parse(base)
	if err != nil {
		return ref
	}
	r, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return b.ResolveReference(r).String()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func newHTTPClient() *http.Client {
	return &http.Client{Timeout: 90 * time.Second}
}

var _ = strconv.Itoa
