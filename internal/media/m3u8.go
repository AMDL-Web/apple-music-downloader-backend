package media

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// HLS playlists are text metadata, never media payloads. This is generous
	// for even very large variant/segment lists while bounding a bad upstream.
	maxPlaylistBytes int64 = 16 << 20
	// AAC-LC is currently materialized in memory before MP4 processing. Bound
	// both advertised and streamed sizes so a forged Content-Length or endless
	// response cannot exhaust the process. 512 MiB still covers multi-hour AAC.
	maxInMemoryMediaBytes int64 = 512 << 20
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
	raw, err := readAllLimited(resp.Body, maxPlaylistBytes)
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
	if total > maxInMemoryMediaBytes {
		return nil, fmt.Errorf("download %s is too large for in-memory processing: %d bytes (max %d)", uri, total, maxInMemoryMediaBytes)
	}

	if onProgress == nil || total <= 0 {
		// No progress tracking possible – just read all at once
		if onProgress != nil {
			onProgress(-1)
		}
		return readAllLimited(resp.Body, maxInMemoryMediaBytes)
	}

	buf := bytes.NewBuffer(make([]byte, 0, total))
	chunk := make([]byte, 32*1024)
	var downloaded int64
	for {
		n, readErr := resp.Body.Read(chunk)
		if n > 0 {
			if downloaded+int64(n) > maxInMemoryMediaBytes {
				return nil, fmt.Errorf("download %s exceeded in-memory limit of %d bytes", uri, maxInMemoryMediaBytes)
			}
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

func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("response exceeded limit of %d bytes", limit)
	}
	return raw, nil
}

const resumeMetadataVersion = 1

type resumeMetadata struct {
	Version      int    `json:"version"`
	SourceHash   string `json:"source_hash"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
	Total        int64  `json:"total,omitempty"`
	Complete     bool   `json:"complete,omitempty"`
}

// downloadToFile streams uri into a stable checkpoint under dir. resumeKey is
// the final output path, which is already protected by the downloader's output
// lock, so retries and a recovered job resolve to the same checkpoint without
// allowing two tracks to append concurrently. A failed transfer deliberately
// leaves both the partial media and its small metadata sidecar in place.
//
// When a checkpoint exists, the request uses Range and, when the server supplied
// one, If-Range. A server that ignores the range or reports a changed validator
// causes a safe full restart instead of mixing bytes from different objects.
// The caller must call cleanupResumableDownload after the encrypted media is no
// longer needed.
func downloadToFile(ctx context.Context, client *http.Client, uri, dir, owner, resumeKey string, onProgress func(float64)) (string, error) {
	path, metadataPath := resumableDownloadPaths(dir, owner, resumeKey)
	offset, metadata := loadResumeCheckpoint(path, metadataPath, uri)

	// One retry is sufficient here: it is used only when a stale/invalid Range
	// response tells us to discard the checkpoint and issue a clean GET.
	for restart := 0; restart < 2; restart++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept-Encoding", "identity")
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
			if validator := resumeValidator(metadata); validator != "" {
				req.Header.Set("If-Range", validator)
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}

		restartClean := func() {
			_ = resp.Body.Close()
			cleanupResumableDownload(path)
			offset = 0
			metadata = resumeMetadata{}
		}

		var total, rangeStart, rangeEnd int64
		rangeEnd = -1
		switch {
		case offset > 0 && resp.StatusCode == http.StatusPartialContent:
			start, end, parsedTotal, ok := parseContentRange(resp.Header.Get("Content-Range"))
			expectedBody := end - start + 1
			if !ok || start != offset || (resp.ContentLength >= 0 && resp.ContentLength != expectedBody) ||
				(metadata.Total > 0 && metadata.Total != parsedTotal) || resumeObjectChanged(metadata, resp.Header) {
				restartClean()
				continue
			}
			total = parsedTotal
			rangeStart, rangeEnd = start, end
		case offset > 0 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
			parsedTotal, ok := parseUnsatisfiedContentRange(resp.Header.Get("Content-Range"))
			_ = resp.Body.Close()
			if ok && parsedTotal == offset && metadata.Complete && !resumeObjectChanged(metadata, resp.Header) {
				metadata.Total = parsedTotal
				if err := writeResumeMetadata(metadataPath, metadata); err != nil {
					return "", err
				}
				if onProgress != nil {
					onProgress(1)
				}
				return path, nil
			}
			cleanupResumableDownload(path)
			offset = 0
			metadata = resumeMetadata{}
			continue
		case offset > 0 && resp.StatusCode == http.StatusOK:
			// Range was ignored or If-Range did not match. Reuse this full body,
			// but truncate the old checkpoint before writing it.
			offset = 0
			metadata = resumeMetadata{}
			total = responseContentLength(resp.Header)
		case offset > 0:
			_ = resp.Body.Close()
			return "", fmt.Errorf("download %s range request failed: %s", uri, resp.Status)
		case resp.StatusCode == http.StatusPartialContent:
			start, end, parsedTotal, ok := parseContentRange(resp.Header.Get("Content-Range"))
			expectedBody := end - start + 1
			if !ok || start != 0 || (resp.ContentLength >= 0 && resp.ContentLength != expectedBody) {
				_ = resp.Body.Close()
				return "", fmt.Errorf("download %s returned invalid Content-Range %q", uri, resp.Header.Get("Content-Range"))
			}
			total = parsedTotal
			rangeStart, rangeEnd = start, end
		case resp.StatusCode >= 300:
			_ = resp.Body.Close()
			return "", fmt.Errorf("download %s failed: %s", uri, resp.Status)
		default:
			total = responseContentLength(resp.Header)
		}

		flags := os.O_CREATE | os.O_WRONLY
		if offset > 0 {
			flags |= os.O_APPEND
		} else {
			flags |= os.O_TRUNC
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			_ = resp.Body.Close()
			return "", err
		}
		f, err := os.OpenFile(path, flags, 0o600)
		if err != nil {
			_ = resp.Body.Close()
			return "", err
		}

		metadata = resumeMetadata{
			Version:      resumeMetadataVersion,
			SourceHash:   sourceFingerprint(uri),
			ETag:         firstNonEmpty(resp.Header.Get("ETag"), metadata.ETag),
			LastModified: firstNonEmpty(resp.Header.Get("Last-Modified"), metadata.LastModified),
			Total:        total,
			Complete:     false,
		}
		if err := writeResumeMetadata(metadataPath, metadata); err != nil {
			_ = f.Close()
			_ = resp.Body.Close()
			return "", err
		}

		downloaded := offset
		if onProgress != nil {
			if total > 0 {
				onProgress(min(1, float64(downloaded)/float64(total)))
			} else {
				onProgress(-1)
			}
		}
		chunk := make([]byte, 32*1024)
		var transferErr error
		for {
			n, readErr := resp.Body.Read(chunk)
			if n > 0 {
				if _, writeErr := f.Write(chunk[:n]); writeErr != nil {
					transferErr = writeErr
					break
				}
				downloaded += int64(n)
				if onProgress != nil && total > 0 {
					onProgress(min(1, float64(downloaded)/float64(total)))
				}
			}
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				transferErr = readErr
				break
			}
		}
		_ = resp.Body.Close()
		if syncErr := f.Sync(); transferErr == nil {
			transferErr = syncErr
		}
		if closeErr := f.Close(); transferErr == nil {
			transferErr = closeErr
		}
		if transferErr != nil {
			return "", transferErr
		}
		if rangeEnd >= 0 && downloaded-offset != rangeEnd-rangeStart+1 {
			return "", fmt.Errorf("download %s returned %d range bytes, want %d", uri, downloaded-offset, rangeEnd-rangeStart+1)
		}
		if total > 0 && downloaded != total {
			return "", fmt.Errorf("download %s ended at %d of %d bytes", uri, downloaded, total)
		}
		if total <= 0 {
			metadata.Total = downloaded
		}
		metadata.Complete = true
		if err := writeResumeMetadata(metadataPath, metadata); err != nil {
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("download %s returned an invalid range response", uri)
}

func resumableDownloadPaths(dir, owner, resumeKey string) (string, string) {
	sum := sha256.Sum256([]byte(resumeKey))
	name := "resume-" + hex.EncodeToString(sum[:]) + ".mp4"
	path := filepath.Join(resumeOwnerDir(dir, owner), name)
	return path, path + ".json"
}

func resumeOwnerDir(dir, owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return filepath.Join(dir, "resume-job-"+hex.EncodeToString(sum[:]))
}

func loadResumeCheckpoint(path, metadataPath, uri string) (int64, resumeMetadata) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	var metadata resumeMetadata
	if json.Unmarshal(raw, &metadata) != nil || metadata.Version != resumeMetadataVersion {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	// The resume key is the output path, so a retry or codec fallback that
	// resolves the same output to a different media object would otherwise pick
	// up the previous object's partial bytes. If-Range alone cannot catch that
	// when validators collide across variants, so bind the checkpoint to its
	// source and restart cleanly on any mismatch.
	if metadata.SourceHash != sourceFingerprint(uri) {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	// Without a strong ETag or Last-Modified, a same-URL/same-size object can
	// change between requests and a naked Range would silently splice two
	// representations. Restart from zero rather than trade integrity for reuse.
	if resumeValidator(metadata) == "" {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	if metadata.Total > 0 && info.Size() > metadata.Total {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	// Total is written before the body so progress survives interruption. Only
	// the complete marker is committed after the media file has been synced and
	// closed; a crash in that final window must restart rather than trust length
	// alone (a sparse/page-cache tail can have the right stat size).
	if metadata.Total > 0 && info.Size() == metadata.Total && !metadata.Complete {
		cleanupResumableDownload(path)
		return 0, resumeMetadata{}
	}
	return info.Size(), metadata
}

// sourceFingerprint identifies the object a checkpoint belongs to. CDN URLs
// for the same media commonly rotate query-string signatures between fetches,
// so only the scheme, host, and path participate; a different variant or codec
// lands on a different path and invalidates the checkpoint.
func sourceFingerprint(uri string) string {
	if u, err := url.Parse(uri); err == nil && u.Host != "" {
		uri = u.Scheme + "://" + u.Host + u.Path
	}
	sum := sha256.Sum256([]byte(uri))
	return hex.EncodeToString(sum[:])
}

func writeResumeMetadata(path string, metadata resumeMetadata) error {
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func resumeValidator(metadata resumeMetadata) string {
	if metadata.ETag != "" && !strings.HasPrefix(strings.TrimSpace(metadata.ETag), "W/") {
		return metadata.ETag
	}
	return metadata.LastModified
}

func resumeObjectChanged(metadata resumeMetadata, header http.Header) bool {
	if metadata.ETag != "" && header.Get("ETag") != "" && metadata.ETag != header.Get("ETag") {
		return true
	}
	return metadata.LastModified != "" && header.Get("Last-Modified") != "" && metadata.LastModified != header.Get("Last-Modified")
}

func responseContentLength(header http.Header) int64 {
	total, _ := strconv.ParseInt(header.Get("Content-Length"), 10, 64)
	if total <= 0 {
		total, _ = strconv.ParseInt(header.Get("X-Apple-MS-Content-Length"), 10, 64)
	}
	return total
}

func parseContentRange(value string) (start, end, total int64, ok bool) {
	if _, err := fmt.Sscanf(value, "bytes %d-%d/%d", &start, &end, &total); err != nil || start < 0 || end < start || total <= end {
		return 0, 0, 0, false
	}
	return start, end, total, true
}

func parseUnsatisfiedContentRange(value string) (int64, bool) {
	var total int64
	if _, err := fmt.Sscanf(value, "bytes */%d", &total); err != nil || total < 0 {
		return 0, false
	}
	return total, true
}

func cleanupResumableDownload(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
	_ = os.Remove(path + ".json")
	_ = os.Remove(path + ".json.tmp")
	parent := filepath.Dir(path)
	if strings.HasPrefix(filepath.Base(parent), "resume-job-") {
		_ = os.Remove(parent) // removes only an already-empty owner dir
	}
}

func cleanupResumeForKey(dir, owner, resumeKey string) {
	path, _ := resumableDownloadPaths(dir, owner, resumeKey)
	cleanupResumableDownload(path)
}

func cleanupResumeOwner(dir, owner string) {
	if dir == "" || owner == "" {
		return
	}
	_ = os.RemoveAll(resumeOwnerDir(dir, owner))
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
