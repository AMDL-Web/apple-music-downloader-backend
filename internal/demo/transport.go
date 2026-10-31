package demo

import (
	"bytes"
	"io"
	"net/http"
	"sync"
)

// roundTripper serves each request with h in this process. The response
// body is a buffer the handler writes into, so a stream reaches the client
// as it is written; closing the body, or ending the request's context,
// fails the handler's next write.
type roundTripper struct{ h http.Handler }

func (t roundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	body := newBody()
	w := &bodyWriter{header: http.Header{}, body: body, ready: make(chan struct{})}
	go func() {
		select {
		case <-r.Context().Done():
			body.Close()
		case <-body.gone:
		}
	}()
	go func() {
		defer func() {
			w.WriteHeader(http.StatusOK)
			body.end()
		}()
		t.h.ServeHTTP(w, r)
	}()
	select {
	case <-w.ready:
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	return &http.Response{
		Status: http.StatusText(w.status), StatusCode: w.status, Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: w.header, Body: body, ContentLength: -1, Request: r,
	}, nil
}

// bodyWriter is the handler's ResponseWriter: headers until the first
// write, then the body.
type bodyWriter struct {
	header http.Header
	body   *body
	once   sync.Once
	ready  chan struct{}
	status int
}

func (w *bodyWriter) Header() http.Header { return w.header }

func (w *bodyWriter) WriteHeader(status int) {
	w.once.Do(func() {
		w.status = status
		close(w.ready)
	})
}

func (w *bodyWriter) Write(p []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.body.write(p)
}

// Flush does nothing: every write already reaches the reader.
func (w *bodyWriter) Flush() {}

// body is a response body that never makes its writer wait: the handler
// may write while it holds the backend's lock.
type body struct {
	mu     sync.Mutex
	more   *sync.Cond
	buf    bytes.Buffer
	ended  bool // the handler returned
	closed bool // the client closed the body
	gone   chan struct{}
}

func newBody() *body {
	b := &body{gone: make(chan struct{})}
	b.more = sync.NewCond(&b.mu)
	return b
}

func (b *body) write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	n, err := b.buf.Write(p)
	b.more.Broadcast()
	return n, err
}

func (b *body) end() {
	b.mu.Lock()
	b.ended = true
	b.more.Broadcast()
	b.mu.Unlock()
}

func (b *body) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.buf.Len() == 0 && !b.ended && !b.closed {
		b.more.Wait()
	}
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	if b.buf.Len() == 0 {
		return 0, io.EOF
	}
	return b.buf.Read(p)
}

func (b *body) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		close(b.gone)
		b.more.Broadcast()
	}
	return nil
}
