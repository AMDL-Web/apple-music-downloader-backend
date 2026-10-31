// Package client talks to an amdl backend over its REST and SSE API
// (internal/api/openapi.yaml): typed requests, one SSE connection with a
// silence watchdog, and followers that keep a job list or one job current
// across disconnects. Every Go client of the backend shares it: the amdl CLI,
// its MCP server and TUI, and the desktop app; a backend embedded in the same
// process is just another base URL on 127.0.0.1.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"amdl/internal/config"
	"amdl/internal/domain"
)

// requestTimeout bounds every non-streaming call; POST /lookup is bounded
// server-side at 15 s, so this leaves it room to answer.
const requestTimeout = 25 * time.Second

// headerTimeout bounds the wait for a response's headers, a stream's too.
var headerTimeout = 20 * time.Second

// Client is one backend. Its methods are safe for concurrent use.
type Client struct {
	base   string
	api    *http.Client
	stream *http.Client
	// Watchdog ends an SSE connection that delivers nothing, not even the
	// server's 15 s keepalive comment, for this long. A half-open TCP
	// connection (a NAS reboot, a laptop waking from sleep) otherwise blocks
	// the read forever.
	Watchdog time.Duration
	// RetryMin and RetryMax bound the backoff of the followers between
	// failed attempts: 1 s doubling up to 30 s unless set.
	RetryMin, RetryMax time.Duration
}

// New returns a client for the backend at base, such as
// "http://192.168.3.5:18570". A path, query or trailing slash is dropped:
// every route lives under /api/v1 of the origin.
func New(base string) (*Client, error) {
	origin, err := NormalizeBase(base)
	if err != nil {
		return nil, err
	}
	// The backend is on the local network or loopback, so system proxies
	// are not consulted: one set for the internet would only get in the way.
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		ResponseHeaderTimeout: headerTimeout,
	}
	return newClient(origin, transport), nil
}

// NewWith returns a client for base whose requests go through rt instead of
// the network, such as a backend served in the same process.
func NewWith(base string, rt http.RoundTripper) (*Client, error) {
	origin, err := NormalizeBase(base)
	if err != nil {
		return nil, err
	}
	return newClient(origin, rt), nil
}

func newClient(origin string, rt http.RoundTripper) *Client {
	return &Client{
		base:     origin,
		api:      &http.Client{Transport: rt, Timeout: requestTimeout},
		stream:   &http.Client{Transport: rt},
		Watchdog: 45 * time.Second,
	}
}

// NormalizeBase returns the origin of a backend address typed by a person:
// a scheme defaults to http, and only scheme and host are kept.
func NormalizeBase(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", errors.New("no server address")
	}
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("server address %q: %w", base, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("server address %q: scheme must be http or https", base)
	}
	if u.Host == "" {
		return "", fmt.Errorf("server address %q has no host", base)
	}
	return u.Scheme + "://" + u.Host, nil
}

// Base returns the backend's origin.
func (c *Client) Base() string { return c.base }

// APIError is a non-2xx answer: the HTTP status and the backend's
// ErrorResponse, when the body held one.
type APIError struct {
	Status  int
	Code    string
	Message string
	// Details is error.details as sent, such as the BatchSubmitResponse of
	// no_urls_accepted or {"link": …} of a lookup error.
	Details json.RawMessage
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

// ErrorCode returns the backend error code of err, or "" when err is not an
// APIError.
func ErrorCode(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

func (c *Client) url(path string, query url.Values) string {
	u := c.base + path
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		u += sep + query.Encode()
	}
	return u
}

// Call sends one request and returns the body of a 2xx answer as is, nil for an
// empty one, for callers that pass the backend's JSON through unchanged.
func (c *Client) Call(ctx context.Context, method, path string, query url.Values, body any) (json.RawMessage, error) {
	var raw json.RawMessage
	err := c.do(ctx, method, path, query, body, &raw)
	return raw, err
}

// LoginWait bounds a wrapper login or two-step request: longer than a wrapper
// amdl runs may take (wrapperhost.LoginLimit, which a test checks) and than
// wrapper.login_timeout_seconds' default for a wrapper-manager.
const LoginWait = 10 * time.Minute

// WithTimeout returns a client whose non-streaming calls wait up to d, for the
// response headers too; c keeps its own bounds.
func (c *Client) WithTimeout(d time.Duration) *Client {
	rt := c.api.Transport
	if t, ok := rt.(*http.Transport); ok {
		t = t.Clone()
		t.ResponseHeaderTimeout = d
		rt = t
	}
	cp := *c
	cp.api = &http.Client{Transport: rt, Timeout: d}
	return &cp
}

// do sends one request and decodes a 2xx JSON body into out (when non-nil).
// okStatus lists the statuses that count as success; empty means any 2xx.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any, okStatus ...int) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path, query), reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.api.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if !statusOK(resp.StatusCode, okStatus) {
		return decodeAPIError(resp.StatusCode, raw)
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

func statusOK(status int, ok []int) bool {
	if len(ok) == 0 {
		return status >= 200 && status < 300
	}
	for _, s := range ok {
		if s == status {
			return true
		}
	}
	return false
}

func decodeAPIError(status int, raw []byte) error {
	apiErr := &APIError{Status: status}
	var body struct {
		Error struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil {
		apiErr.Code, apiErr.Message, apiErr.Details = body.Error.Code, body.Error.Message, body.Error.Details
	}
	return apiErr
}

// Health reads GET /api/v1/health. A degraded backend answers 503 with the
// same body, which is returned without an error.
func (c *Client) Health(ctx context.Context) (Health, error) {
	var h Health
	err := c.do(ctx, http.MethodGet, "/api/v1/health", nil, nil, &h, http.StatusOK, http.StatusServiceUnavailable)
	return h, err
}

// System reads GET /api/v1/system.
func (c *Client) System(ctx context.Context) (SystemInfo, error) {
	var s SystemInfo
	err := c.do(ctx, http.MethodGet, "/api/v1/system", nil, nil, &s)
	return s, err
}

// WrapperStatus reads GET /api/v1/wrapper/status.
func (c *Client) WrapperStatus(ctx context.Context) (WrapperStatus, error) {
	var s WrapperStatus
	err := c.do(ctx, http.MethodGet, "/api/v1/wrapper/status", nil, nil, &s)
	return s, err
}

// Stats reads GET /api/v1/stats over the last days UTC days.
func (c *Client) Stats(ctx context.Context, days int) (Stats, error) {
	var s Stats
	err := c.do(ctx, http.MethodGet, "/api/v1/stats", url.Values{"days": {strconv.Itoa(days)}}, nil, &s)
	return s, err
}

// LibrarySync reads GET /api/v1/library-sync.
func (c *Client) LibrarySync(ctx context.Context) (LibrarySync, error) {
	var s LibrarySync
	err := c.do(ctx, http.MethodGet, "/api/v1/library-sync", nil, nil, &s)
	return s, err
}

// Hooks reads GET /api/v1/hooks.
func (c *Client) Hooks(ctx context.Context) (Hooks, error) {
	var h Hooks
	err := c.do(ctx, http.MethodGet, "/api/v1/hooks", nil, nil, &h)
	return h, err
}

// HookPresets reads GET /api/v1/hooks/presets.
func (c *Client) HookPresets(ctx context.Context) ([]HookPreset, error) {
	var r struct {
		Presets []HookPreset `json:"presets"`
	}
	err := c.do(ctx, http.MethodGet, "/api/v1/hooks/presets", nil, nil, &r)
	return r.Presets, err
}

// SetHooksEnabled sets hooks.yaml's master switch (PUT /api/v1/hooks).
func (c *Client) SetHooksEnabled(ctx context.Context, on bool) (Hooks, error) {
	var h Hooks
	err := c.do(ctx, http.MethodPut, "/api/v1/hooks", nil, map[string]bool{"enabled": on}, &h)
	return h, err
}

// PutHook creates or changes the webhook hook name (PUT /api/v1/hooks/{name}).
func (c *Client) PutHook(ctx context.Context, name string, w HookWrite) (HookEntry, error) {
	var e HookEntry
	err := c.do(ctx, http.MethodPut, "/api/v1/hooks/"+url.PathEscape(name), nil, w, &e)
	return e, err
}

// DeleteHook removes the webhook hook name.
func (c *Client) DeleteHook(ctx context.Context, name string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/hooks/"+url.PathEscape(name), nil, nil, nil)
}

// TestHook sends the webhook hook name once with a sample payload.
func (c *Client) TestHook(ctx context.Context, name string) (HookTest, error) {
	var t HookTest
	err := c.do(ctx, http.MethodPost, "/api/v1/hooks/"+url.PathEscape(name)+"/test", nil, nil, &t)
	return t, err
}

// Downloads reads one page of GET /api/v1/downloads.
func (c *Client) Downloads(ctx context.Context, q ListQuery) (DownloadList, error) {
	var l DownloadList
	err := c.do(ctx, http.MethodGet, "/api/v1/downloads", q.values(), nil, &l)
	return l, err
}

// Download reads GET /api/v1/downloads/{id}.
func (c *Client) Download(ctx context.Context, id string) (DownloadDetail, error) {
	var d DownloadDetail
	err := c.do(ctx, http.MethodGet, "/api/v1/downloads/"+url.PathEscape(id), nil, nil, &d)
	return d, err
}

// Submit posts a batch to POST /api/v1/downloads. When no URL was accepted
// the backend answers with an error whose details are the batch result; Submit
// returns that result alongside the APIError, so a caller can show each URL's
// reason either way.
func (c *Client) Submit(ctx context.Context, urls []string, overrides *config.DownloadOverrides) (domain.BatchSubmitResponse, error) {
	var res domain.BatchSubmitResponse
	err := c.do(ctx, http.MethodPost, "/api/v1/downloads", nil,
		domain.DownloadRequest{URLs: urls, Overrides: overrides}, &res, http.StatusAccepted)
	var apiErr *APIError
	if errors.As(err, &apiErr) && len(apiErr.Details) > 0 {
		// A details body that is not a batch result leaves res empty, and the
		// APIError still says what went wrong.
		_ = json.Unmarshal(apiErr.Details, &res)
	}
	return res, err
}

// Lookup previews one link with POST /api/v1/lookup.
func (c *Client) Lookup(ctx context.Context, link string) (Lookup, error) {
	var l Lookup
	err := c.do(ctx, http.MethodPost, "/api/v1/lookup", nil, map[string]string{"url": link}, &l, http.StatusOK)
	return l, err
}

// Cancel cancels a queued or running job.
func (c *Client) Cancel(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/downloads/"+url.PathEscape(id)+"/cancel", nil, nil, nil)
}

// Delete removes a finished job and its records with DELETE
// /api/v1/downloads/{id}; a queued or running one answers job_active.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/v1/downloads/"+url.PathEscape(id), nil, nil, nil)
}

// Retry re-enqueues a failed or cancelled job.
func (c *Client) Retry(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/v1/downloads/"+url.PathEscape(id)+"/retry", nil, nil, nil)
}
