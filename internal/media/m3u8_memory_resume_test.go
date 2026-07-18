package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

type memoryResumeRoundTripFunc func(*http.Request) (*http.Response, error)

func (f memoryResumeRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type finalErrorReadCloser struct {
	raw    []byte
	err    error
	cancel context.CancelFunc
	closed *atomic.Bool
}

func (r *finalErrorReadCloser) Read(p []byte) (int, error) {
	if len(r.raw) == 0 {
		return 0, r.err
	}
	n := copy(p, r.raw)
	r.raw = r.raw[n:]
	if len(r.raw) == 0 {
		if r.cancel != nil {
			r.cancel()
		}
		return n, r.err
	}
	return n, nil
}

func (r *finalErrorReadCloser) Close() error {
	if r.closed != nil {
		r.closed.Store(true)
	}
	return nil
}

func TestDownloadBytesResumesInterruptedTransfer(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 16*1024)
	cut := 73 * 1024
	for _, tc := range []struct {
		name        string
		totalHeader string
	}{
		{name: "content-length", totalHeader: "Content-Length"},
		{name: "apple-content-length", totalHeader: "X-Apple-MS-Content-Length"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var requests []http.Header
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				requests = append(requests, r.Header.Clone())
				requestNumber := len(requests)
				mu.Unlock()

				w.Header().Set("ETag", `"memory-v1"`)
				if requestNumber == 1 {
					w.Header().Set(tc.totalHeader, strconv.Itoa(len(payload)))
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(payload[:cut])
					return
				}
				if got, want := r.Header.Get("Range"), fmt.Sprintf("bytes=%d-", cut); got != want {
					t.Errorf("Range = %q, want %q", got, want)
				}
				if got := r.Header.Get("If-Range"); got != `"memory-v1"` {
					t.Errorf("If-Range = %q, want memory ETag", got)
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(payload)-1, len(payload)))
				w.Header().Set("Content-Length", strconv.Itoa(len(payload)-cut))
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(payload[cut:])
			}))
			defer server.Close()

			var progress []float64
			raw, err := downloadBytesWithRangeResume(context.Background(), server.Client(), server.URL, func(value float64) {
				progress = append(progress, value)
			})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(raw, payload) {
				t.Fatalf("downloaded %d bytes, want %d", len(raw), len(payload))
			}
			if len(progress) == 0 || progress[len(progress)-1] != 1 {
				t.Fatalf("progress = %v, want final 1", progress)
			}
			sawPartialProgress := false
			for i := 1; i < len(progress); i++ {
				if progress[i] < progress[i-1] {
					t.Fatalf("progress decreased at %d: %v", i, progress)
				}
				if progress[i] > 0 && progress[i] < 1 {
					sawPartialProgress = true
				}
			}
			if !sawPartialProgress {
				t.Fatalf("progress did not report an intermediate value: %v", progress)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(requests) != 2 {
				t.Fatalf("requests = %d, want 2", len(requests))
			}
			for i, header := range requests {
				if got := header.Get("Accept-Encoding"); got != "identity" {
					t.Errorf("request %d Accept-Encoding = %q, want identity", i+1, got)
				}
			}
		})
	}
}

func TestDownloadBytesDiscardsPrefixWhenRangeIsIgnored(t *testing.T) {
	oldPayload := bytes.Repeat([]byte("old"), 32*1024)
	newPayload := bytes.Repeat([]byte("replacement"), 12*1024)
	cut := 31 * 1024
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		if requestNumber == 1 {
			w.Header().Set("ETag", `"old"`)
			w.Header().Set("Content-Length", strconv.Itoa(len(oldPayload)))
			_, _ = w.Write(oldPayload[:cut])
			return
		}
		if got := r.Header.Get("Range"); got != fmt.Sprintf("bytes=%d-", cut) {
			t.Errorf("Range = %q, want resume offset", got)
		}
		w.Header().Set("ETag", `"new"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(newPayload)))
		_, _ = w.Write(newPayload) // 200: If-Range missed or Range was ignored.
	}))
	defer server.Close()

	raw, err := downloadBytesWithRangeResume(context.Background(), server.Client(), server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, newPayload) {
		t.Fatalf("download mixed the old prefix into the replacement: got %d bytes, want %d", len(raw), len(newPayload))
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
}

func TestDownloadBytesDoesNotResumeWithoutValidator(t *testing.T) {
	payload := bytes.Repeat([]byte("unvalidated"), 16*1024)
	cut := 29 * 1024
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Range") != "" {
			t.Errorf("unsafe Range without validator: %q", r.Header.Get("Range"))
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload[:cut])
	}))
	defer server.Close()

	if _, err := downloadBytesWithRangeResume(context.Background(), server.Client(), server.URL, nil); err == nil {
		t.Fatal("truncated unvalidated response succeeded")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 clean attempt", got)
	}
}

func TestDownloadBytesLeavesRangeResumeDisabled(t *testing.T) {
	payload := bytes.Repeat([]byte("aac-lc-memory"), 16*1024)
	cut := 35 * 1024
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Range") != "" {
			t.Errorf("downloadBytes sent Range %q", r.Header.Get("Range"))
		}
		w.Header().Set("ETag", `"strong-but-disabled"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload[:cut])
	}))
	defer server.Close()

	if _, err := downloadBytes(context.Background(), server.Client(), server.URL, nil); err == nil {
		t.Fatal("truncated response succeeded")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests = %d, want no internal reconnect", got)
	}
}

func TestDownloadBytesRejectsChangedResumeObject(t *testing.T) {
	payload := bytes.Repeat([]byte("validated"), 16*1024)
	cut := 37 * 1024
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		if requestNumber == 1 {
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload[:cut])
			return
		}
		w.Header().Set("ETag", `"v2"`)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(payload)-1, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)-cut))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[cut:])
	}))
	defer server.Close()

	if _, err := downloadBytesWithRangeResume(context.Background(), server.Client(), server.URL, nil); err == nil {
		t.Fatal("changed resume object succeeded")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want initial plus one bounded resume", got)
	}
}

func TestDownloadBytesUsesLastModifiedForResume(t *testing.T) {
	payload := bytes.Repeat([]byte("last-modified"), 16*1024)
	cut := 41 * 1024
	const modified = "Wed, 21 Oct 2015 07:28:00 GMT"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		w.Header().Set("Last-Modified", modified)
		if requestNumber == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload[:cut])
			return
		}
		if got := r.Header.Get("If-Range"); got != modified {
			t.Errorf("If-Range = %q, want Last-Modified", got)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(payload)-1, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)-cut))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[cut:])
	}))
	defer server.Close()

	raw, err := downloadBytesWithRangeResume(context.Background(), server.Client(), server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, payload) {
		t.Fatalf("downloaded %d bytes, want %d", len(raw), len(payload))
	}
}

func TestDownloadBytesBoundsTransparentReconnects(t *testing.T) {
	payload := bytes.Repeat([]byte("bounded"), 32*1024)
	firstCut := 43 * 1024
	secondCut := 31 * 1024
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := requests.Add(1)
		w.Header().Set("ETag", `"bounded"`)
		switch requestNumber {
		case 1:
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload[:firstCut])
		case 2:
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", firstCut, len(payload)-1, len(payload)))
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)-firstCut))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[firstCut : firstCut+secondCut])
		default:
			t.Errorf("unexpected transparent reconnect %d", requestNumber)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", firstCut+secondCut, len(payload)-1, len(payload)))
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[firstCut+secondCut:])
		}
	}))
	defer server.Close()

	if _, err := downloadBytesWithRangeResume(context.Background(), server.Client(), server.URL, nil); err == nil {
		t.Fatal("a second interrupted response succeeded")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want exactly one transparent reconnect", got)
	}
}

func TestDownloadBytesRejectsInconsistentResumeTotal(t *testing.T) {
	payload := bytes.Repeat([]byte("content-range"), 16*1024)
	cut := 47 * 1024
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestNumber := requests.Add(1)
		w.Header().Set("ETag", `"same"`)
		if requestNumber == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload[:cut])
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", cut, len(payload)-1, len(payload)+1))
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)-cut))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[cut:])
	}))
	defer server.Close()

	if _, err := downloadBytesWithRangeResume(context.Background(), server.Client(), server.URL, nil); err == nil {
		t.Fatal("resume with inconsistent total succeeded")
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want initial plus rejected resume", got)
	}
}

func TestDownloadBytesRejectsTruncatedBufferOn416(t *testing.T) {
	payload := bytes.Repeat([]byte("range-not-satisfiable"), 8*1024)
	cut := 39 * 1024
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestNumber := requests.Add(1)
		w.Header().Set("ETag", `"same"`)
		if requestNumber == 1 {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload[:cut])
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", cut))
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
	}))
	defer server.Close()

	if _, err := downloadBytesWithRangeResume(context.Background(), server.Client(), server.URL, nil); err == nil {
		t.Fatal("416 accepted a buffer shorter than the original total")
	}
}

func TestDownloadBytesAcceptsValidatedCompleteBufferOn416(t *testing.T) {
	payload := []byte("the body completed before the transport reported a reset")
	var requests atomic.Int32
	var firstClosed, secondClosed atomic.Bool
	client := &http.Client{Transport: memoryResumeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestNumber := requests.Add(1)
		if requestNumber == 1 {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				ContentLength: int64(len(payload)),
				Header: http.Header{
					"Content-Length": {strconv.Itoa(len(payload))},
					"Etag":           {`"complete"`},
				},
				Body: &finalErrorReadCloser{raw: append([]byte(nil), payload...), err: errors.New("connection reset after body"), closed: &firstClosed},
			}, nil
		}
		if got := req.Header.Get("Range"); got != fmt.Sprintf("bytes=%d-", len(payload)) {
			t.Errorf("Range = %q, want completed offset", got)
		}
		return &http.Response{
			StatusCode: http.StatusRequestedRangeNotSatisfiable,
			Status:     "416 Requested Range Not Satisfiable",
			Header: http.Header{
				"Content-Range": {fmt.Sprintf("bytes */%d", len(payload))},
				"Etag":          {`"complete"`},
			},
			Body: &finalErrorReadCloser{err: io.EOF, closed: &secondClosed},
		}, nil
	})}

	raw, err := downloadBytesWithRangeResume(context.Background(), client, "https://cdn.example.test/media", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, payload) {
		t.Fatalf("downloaded %q, want %q", raw, payload)
	}
	if requests.Load() != 2 || !firstClosed.Load() || !secondClosed.Load() {
		t.Fatalf("requests/closed = %d/%t/%t, want 2/true/true", requests.Load(), firstClosed.Load(), secondClosed.Load())
	}
}

func TestDownloadBytesAcceptsValidatedUnknownTotalBufferOn416(t *testing.T) {
	payload := []byte("chunked body completed before the transport reported a reset")
	var requests atomic.Int32
	var firstClosed, secondClosed atomic.Bool
	client := &http.Client{Transport: memoryResumeRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestNumber := requests.Add(1)
		if requestNumber == 1 {
			return &http.Response{
				StatusCode:    http.StatusOK,
				Status:        "200 OK",
				ContentLength: -1,
				Header: http.Header{
					"Etag": {`"unknown-total"`},
				},
				Body: &finalErrorReadCloser{raw: append([]byte(nil), payload...), err: errors.New("connection reset after body"), closed: &firstClosed},
			}, nil
		}
		if got := req.Header.Get("Range"); got != fmt.Sprintf("bytes=%d-", len(payload)) {
			t.Errorf("Range = %q, want completed offset", got)
		}
		if got := req.Header.Get("If-Range"); got != `"unknown-total"` {
			t.Errorf("If-Range = %q, want unknown-total ETag", got)
		}
		return &http.Response{
			StatusCode: http.StatusRequestedRangeNotSatisfiable,
			Status:     "416 Requested Range Not Satisfiable",
			Header: http.Header{
				"Content-Range": {fmt.Sprintf("bytes */%d", len(payload))},
				"Etag":          {`"unknown-total"`},
			},
			Body: &finalErrorReadCloser{err: io.EOF, closed: &secondClosed},
		}, nil
	})}

	raw, err := downloadBytesWithRangeResume(context.Background(), client, "https://cdn.example.test/unknown-total", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, payload) {
		t.Fatalf("downloaded %q, want %q", raw, payload)
	}
	if requests.Load() != 2 || !firstClosed.Load() || !secondClosed.Load() {
		t.Fatalf("requests/closed = %d/%t/%t, want 2/true/true", requests.Load(), firstClosed.Load(), secondClosed.Load())
	}
}

func TestDownloadBytesDoesNotResumeAfterContextCancellation(t *testing.T) {
	payload := []byte("partial bytes before cancellation")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requests atomic.Int32
	var bodyClosed atomic.Bool
	client := &http.Client{Transport: memoryResumeRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			ContentLength: int64(len(payload) + 10),
			Header: http.Header{
				"Content-Length": {strconv.Itoa(len(payload) + 10)},
				"Etag":           {`"cancelled"`},
			},
			Body: &finalErrorReadCloser{raw: append([]byte(nil), payload...), err: errors.New("connection reset"), cancel: cancel, closed: &bodyClosed},
		}, nil
	})}

	if _, err := downloadBytesWithRangeResume(ctx, client, "https://cdn.example.test/cancel", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
	if requests.Load() != 1 || !bodyClosed.Load() {
		t.Fatalf("requests/closed = %d/%t, want 1/true", requests.Load(), bodyClosed.Load())
	}
}
