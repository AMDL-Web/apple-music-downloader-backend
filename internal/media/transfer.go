package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"amdl/internal/limits"
)

const (
	// HLS playlists are text metadata, never media payloads. This is generous
	// for even very large variant/segment lists while bounding a bad upstream.
	maxPlaylistBytes int64 = 16 << 20
	// AAC-LC and high memory mode hold the encrypted track in memory before MP4
	// processing. Bound both advertised and streamed sizes so a forged
	// Content-Length or endless response cannot exhaust the process. 512 MiB
	// still covers multi-hour AAC.
	maxInMemoryMediaBytes int64 = 512 << 20

	// playlistRequestTimeout bounds one playlist or manifest request, body
	// included. Playlists are small, so a request still running after this is
	// stuck rather than slow.
	playlistRequestTimeout = 30 * time.Second
	// mediaStallTimeout ends an encrypted media transfer when no body byte has
	// arrived for this long. Media transfers have no whole-request deadline: a
	// 24-bit/192 kHz track is several hundred MB, and a slow but steady link must
	// be allowed to finish it. Waiting for the response itself is bounded by the
	// transport timeouts in newHTTPClient.
	mediaStallTimeout = 60 * time.Second

	transportDialTimeout           = 30 * time.Second
	transportTLSHandshakeTimeout   = 15 * time.Second
	transportResponseHeaderTimeout = 30 * time.Second
)

// errTransferStalled is the cause of a media request cancelled because its
// body stopped arriving for longer than the stall timeout.
var errTransferStalled = errors.New("transfer stalled")

// newHTTPClient returns the client for Apple's media CDN. It has no
// Client.Timeout, which would also bound reading the body: connecting, the TLS
// handshake and waiting for response headers are bounded by the transport, a
// stalled body by mediaStallTimeout, and playlist requests by
// playlistRequestTimeout (see downloadText).
func newHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: transportDialTimeout, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = transportTLSHandshakeTimeout
	transport.ResponseHeaderTimeout = transportResponseHeaderTimeout
	return &http.Client{Transport: transport}
}

// downloadText fetches a playlist or manifest. The whole request, body
// included, is bounded by playlistRequestTimeout on a copy of client, so the
// clock starts only once the request gate has admitted the request.
func downloadText(ctx context.Context, client *http.Client, uri string, gates ...*limits.RequestGate) (string, error) {
	bounded := *client
	if bounded.Timeout <= 0 || bounded.Timeout > playlistRequestTimeout {
		bounded.Timeout = playlistRequestTimeout
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return "", err
	}
	var resp *http.Response
	if len(gates) > 0 && gates[0] != nil {
		resp, err = gates[0].DoWith429Retry(ctx, &bounded, req, false, limits.DefaultRetryDelay)
	} else {
		resp, err = bounded.Do(req)
	}
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

// transferProgress is told how many bytes of a download are held and how many
// the response announced; total is 0 while the size is unknown.
type transferProgress func(done, total int64)

// downloadBytes fetches uri into memory without an internal reconnect. AAC-LC
// uses this path and leaves retry/backoff to its caller.
func downloadBytes(ctx context.Context, client *http.Client, uri string, onProgress transferProgress) ([]byte, error) {
	return downloadToMemory(ctx, client, uri, false, mediaStallTimeout, onProgress)
}

// downloadBytesWithRangeResume fetches uri into memory. If a response body is
// interrupted after making progress and the server supplied a usable If-Range
// validator, one transparent Range/If-Range reconnect preserves the bytes
// already held in memory. The reconnect is deliberately bounded to one request;
// the caller's outer retry policy remains responsible for backoff and further
// attempts.
//
// onProgress, if non-nil, is called periodically with the bytes held so far
// and the announced total, which is 0 when the size is unknown.
func downloadBytesWithRangeResume(ctx context.Context, client *http.Client, uri string, onProgress transferProgress) ([]byte, error) {
	return downloadToMemory(ctx, client, uri, true, mediaStallTimeout, onProgress)
}

func downloadToMemory(ctx context.Context, client *http.Client, uri string, allowRangeResume bool, stall time.Duration, onProgress transferProgress) ([]byte, error) {
	sink := &memorySink{uri: uri}
	transfer := &rangeTransfer{
		client: client, uri: uri, stall: stall, sink: sink,
		maxTotal: maxInMemoryMediaBytes, onProgress: onProgress,
		beforeBody: func(meta resumeMetadata) error {
			sink.reserve(meta.Total)
			return nil
		},
	}
	resumed := false
	for {
		err := transfer.attempt(ctx)
		if err == nil {
			return sink.buf.Bytes(), nil
		}
		var body *bodyError
		if !errors.As(err, &body) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// A naked Range could splice two objects at the same URL. Only keep the
		// in-memory prefix when If-Range can bind the follow-up request.
		if !allowRangeResume || !body.resumable || resumed || sink.Len() == 0 || resumeValidator(transfer.meta) == "" {
			return nil, body.err
		}
		resumed = true
	}
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
func downloadToFile(ctx context.Context, client *http.Client, uri, dir, owner, resumeKey string, onProgress transferProgress) (string, error) {
	return downloadToCheckpoint(ctx, client, uri, dir, owner, resumeKey, mediaStallTimeout, onProgress)
}

func downloadToCheckpoint(ctx context.Context, client *http.Client, uri, dir, owner, resumeKey string, stall time.Duration, onProgress transferProgress) (string, error) {
	path, metadataPath := resumableDownloadPaths(dir, owner, resumeKey)
	offset, metadata := loadResumeCheckpoint(path, metadataPath, uri)
	sink := &fileSink{path: path, n: offset}
	defer sink.close()
	checkpoint := func(meta resumeMetadata, complete bool) resumeMetadata {
		return resumeMetadata{
			Version: resumeMetadataVersion, SourceHash: sourceFingerprint(uri),
			ETag: meta.ETag, LastModified: meta.LastModified, Total: meta.Total, Complete: complete,
		}
	}
	transfer := &rangeTransfer{
		client: client, uri: uri, stall: stall, sink: sink, meta: metadata,
		requireComplete: true, onProgress: onProgress,
		// Total is written before the body so progress survives interruption;
		// the complete marker only after the media has been synced and closed.
		beforeBody: func(meta resumeMetadata) error {
			if err := sink.open(); err != nil {
				return err
			}
			return writeResumeMetadata(metadataPath, checkpoint(meta, false))
		},
	}
	// One restart is enough: it happens only when a stale or invalid Range
	// response says to discard the checkpoint and issue a clean GET.
	for restart := 0; restart < 2; restart++ {
		err := transfer.attempt(ctx)
		var rejected *resumeRejectedError
		if errors.As(err, &rejected) {
			sink.discard()
			transfer.meta = resumeMetadata{}
			continue
		}
		closeErr := sink.close()
		if err != nil {
			return "", unwrapBodyError(err)
		}
		if closeErr != nil {
			return "", closeErr
		}
		done := checkpoint(transfer.meta, true)
		if done.Total <= 0 {
			done.Total = sink.Len()
		}
		if err := writeResumeMetadata(metadataPath, done); err != nil {
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("download %s returned an invalid range response", uri)
}

// rangeSink holds the bytes of one transfer. Its length is the offset the next
// request resumes from.
type rangeSink interface {
	io.Writer
	Len() int64
	// Truncate discards everything held, so the next write is byte 0 again.
	Truncate() error
}

// rangeTransfer is the Range/If-Range resume state machine shared by the
// in-memory and checkpoint downloads. One attempt issues one request from the
// sink's current length and validates the answer against the object the held
// bytes came from, so bytes of two representations are never mixed. The
// callers decide what a rejected resume or an interrupted body means.
type rangeTransfer struct {
	client *http.Client
	uri    string
	// stall ends the request once no body byte has arrived for this long; zero
	// disables it.
	stall time.Duration
	sink  rangeSink
	// meta describes the object the sink's bytes belong to: its validators and
	// total size, plus the checkpoint's complete marker.
	meta resumeMetadata
	// maxTotal rejects a response that announces more bytes; zero is unbounded.
	maxTotal int64
	// requireComplete accepts a 416 for the held length only when meta says a
	// previous transfer completed (the on-disk checkpoint).
	requireComplete bool
	onProgress      transferProgress
	// beforeBody runs once the response headers are accepted, before the first
	// body byte is written.
	beforeBody func(resumeMetadata) error
}

// resumeRejectedError reports a resumed request whose answer cannot extend the
// held bytes. The checkpoint download discards its bytes and restarts; the
// in-memory download fails.
type resumeRejectedError struct{ message string }

func (e *resumeRejectedError) Error() string { return e.message }

// bodyError reports a response body that ended badly after the headers were
// accepted. resumable is false when the sink refused bytes, so asking for the
// rest cannot help.
type bodyError struct {
	err       error
	resumable bool
}

func (e *bodyError) Error() string { return e.err.Error() }
func (e *bodyError) Unwrap() error { return e.err }

func unwrapBodyError(err error) error {
	var body *bodyError
	if errors.As(err, &body) {
		return body.err
	}
	return err
}

func (t *rangeTransfer) progress(done, total int64) {
	if t.onProgress == nil {
		return
	}
	t.onProgress(done, max(total, 0))
}

func (t *rangeTransfer) attempt(ctx context.Context) error {
	offset := t.sink.Len()
	reqCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, t.uri, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept-Encoding", "identity")
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		if validator := resumeValidator(t.meta); validator != "" {
			req.Header.Set("If-Range", validator)
		}
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var total int64
	rangeStart, rangeEnd := int64(0), int64(-1)
	switch {
	case offset > 0 && resp.StatusCode == http.StatusPartialContent:
		start, end, parsedTotal, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != offset || (resp.ContentLength >= 0 && resp.ContentLength != end-start+1) ||
			(t.meta.Total > 0 && t.meta.Total != parsedTotal) || resumeObjectChanged(t.meta, resp.Header) {
			return &resumeRejectedError{fmt.Sprintf("download %s returned invalid resume range %q", t.uri, resp.Header.Get("Content-Range"))}
		}
		total = parsedTotal
		rangeStart, rangeEnd = start, end
	case offset > 0 && resp.StatusCode == http.StatusRequestedRangeNotSatisfiable:
		// The held bytes may already be the whole object: accept that only when
		// the server's total matches and the object did not change.
		parsedTotal, ok := parseUnsatisfiedContentRange(resp.Header.Get("Content-Range"))
		if ok && parsedTotal == offset && (t.meta.Total <= 0 || t.meta.Total == parsedTotal) &&
			(!t.requireComplete || t.meta.Complete) && !resumeObjectChanged(t.meta, resp.Header) {
			t.meta.Total = parsedTotal
			if t.onProgress != nil {
				t.onProgress(parsedTotal, parsedTotal)
			}
			return nil
		}
		return &resumeRejectedError{fmt.Sprintf("download %s could not resume at byte %d", t.uri, offset)}
	case offset > 0 && resp.StatusCode == http.StatusOK:
		// Range was ignored or If-Range did not match. Reuse the full response,
		// but discard the old prefix so representations can never be mixed.
		if err := t.sink.Truncate(); err != nil {
			return err
		}
		offset = 0
		t.meta = resumeMetadata{}
		total = responseContentLength(resp.Header)
	case offset > 0:
		return fmt.Errorf("download %s range request failed: %s", t.uri, resp.Status)
	case resp.StatusCode == http.StatusPartialContent:
		start, end, parsedTotal, ok := parseContentRange(resp.Header.Get("Content-Range"))
		if !ok || start != 0 || (resp.ContentLength >= 0 && resp.ContentLength != end-start+1) {
			return fmt.Errorf("download %s returned invalid Content-Range %q", t.uri, resp.Header.Get("Content-Range"))
		}
		total = parsedTotal
		rangeStart, rangeEnd = start, end
	case resp.StatusCode >= 300:
		return fmt.Errorf("download %s failed: %s", t.uri, resp.Status)
	default:
		total = responseContentLength(resp.Header)
	}

	if t.maxTotal > 0 && total > t.maxTotal {
		return fmt.Errorf("download %s is too large for in-memory processing: %d bytes (max %d)", t.uri, total, t.maxTotal)
	}
	t.meta = resumeMetadata{
		ETag:         firstNonEmpty(resp.Header.Get("ETag"), t.meta.ETag),
		LastModified: firstNonEmpty(resp.Header.Get("Last-Modified"), t.meta.LastModified),
		Total:        total,
	}
	if t.beforeBody != nil {
		if err := t.beforeBody(t.meta); err != nil {
			return err
		}
	}
	t.progress(offset, total)

	resumable, transferErr := t.readBody(reqCtx, cancel, resp.Body, total)
	if transferErr == nil && rangeEnd >= 0 && t.sink.Len()-offset != rangeEnd-rangeStart+1 {
		transferErr = fmt.Errorf("download %s returned %d range bytes, want %d", t.uri, t.sink.Len()-offset, rangeEnd-rangeStart+1)
	}
	if transferErr == nil && total > 0 && t.sink.Len() != total {
		transferErr = fmt.Errorf("download %s ended at %d of %d bytes", t.uri, t.sink.Len(), total)
	}
	if transferErr != nil {
		return &bodyError{err: transferErr, resumable: resumable}
	}
	return nil
}

// readBody copies the body into the sink. Every byte that arrives pushes the
// stall deadline out again; when it passes, the request context is cancelled
// with errTransferStalled, which the read then reports.
func (t *rangeTransfer) readBody(reqCtx context.Context, cancel context.CancelCauseFunc, body io.Reader, total int64) (resumable bool, err error) {
	var stallTimer *time.Timer
	if t.stall > 0 {
		stallTimer = time.AfterFunc(t.stall, func() { cancel(errTransferStalled) })
		defer stallTimer.Stop()
	}
	chunk := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(chunk)
		if n > 0 {
			if stallTimer != nil {
				stallTimer.Reset(t.stall)
			}
			if _, err := t.sink.Write(chunk[:n]); err != nil {
				return false, err
			}
			if t.onProgress != nil && total > 0 {
				t.onProgress(t.sink.Len(), total)
			}
		}
		if readErr == io.EOF {
			return true, nil
		}
		if readErr != nil {
			if errors.Is(context.Cause(reqCtx), errTransferStalled) {
				return true, fmt.Errorf("download %s stalled: no data for %s: %w", t.uri, t.stall, errTransferStalled)
			}
			return true, readErr
		}
	}
}

// memorySink holds a transfer in memory, refusing to grow past
// maxInMemoryMediaBytes.
type memorySink struct {
	uri string
	buf bytes.Buffer
}

func (s *memorySink) Write(p []byte) (int, error) {
	if int64(s.buf.Len())+int64(len(p)) > maxInMemoryMediaBytes {
		return 0, fmt.Errorf("download %s exceeded in-memory limit of %d bytes", s.uri, maxInMemoryMediaBytes)
	}
	return s.buf.Write(p)
}

func (s *memorySink) Len() int64 { return int64(s.buf.Len()) }

func (s *memorySink) Truncate() error {
	s.buf.Reset()
	return nil
}

// reserve grows the buffer once to the announced total, so a large track is
// not copied through every doubling on the way.
func (s *memorySink) reserve(total int64) {
	if total > int64(s.buf.Cap()) {
		s.buf.Grow(int(total) - s.buf.Len())
	}
}

// fileSink is the on-disk checkpoint. It opens lazily, appending to the bytes
// already there or truncating when there are none.
type fileSink struct {
	path string
	f    *os.File
	n    int64
}

func (s *fileSink) open() error {
	if s.f != nil {
		return nil
	}
	flags := os.O_CREATE | os.O_WRONLY
	if s.n > 0 {
		flags |= os.O_APPEND
	} else {
		flags |= os.O_TRUNC
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.path, flags, 0o600)
	if err != nil {
		return err
	}
	s.f = f
	return nil
}

func (s *fileSink) Write(p []byte) (int, error) {
	if err := s.open(); err != nil {
		return 0, err
	}
	n, err := s.f.Write(p)
	s.n += int64(n)
	return n, err
}

func (s *fileSink) Len() int64 { return s.n }

func (s *fileSink) Truncate() error {
	s.n = 0
	if s.f == nil {
		return nil
	}
	return s.f.Truncate(0)
}

// discard drops the checkpoint and its metadata entirely.
func (s *fileSink) discard() {
	if s.f != nil {
		_ = s.f.Close()
		s.f = nil
	}
	s.n = 0
	cleanupResumableDownload(s.path)
}

// close syncs and closes the checkpoint; it is a no-op when nothing was
// opened, and safe to call twice.
func (s *fileSink) close() error {
	if s.f == nil {
		return nil
	}
	f := s.f
	s.f = nil
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
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
