// Package wrapper is the client for wrapper-lite, which holds the Apple Music
// account session. wrapper-lite serves device manifests, lyrics, web playback
// manifests and Widevine licenses over HTTP, and for each FairPlay key URI a
// white-box key template that this package runs in-process to decrypt media
// samples.
package wrapper

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"amdl/internal/config"
	"amdl/internal/limits"
)

var (
	ErrAuthenticationFailed = errors.New("wrapper authentication failed")
	ErrAlreadyLoggedIn      = errors.New("wrapper account already logged in")
	ErrLoginPending         = errors.New("a wrapper login for this account is already pending")
	ErrLoginSessionNotFound = errors.New("wrapper login session not found")
	ErrLoginSessionBusy     = errors.New("wrapper login session is already being verified")
	ErrLoginTimeout         = errors.New("wrapper login timed out")
	ErrAccountNotFound      = errors.New("wrapper account not found")
	ErrWrapperResponse      = errors.New("wrapper returned an unexpected response")
	// ErrLoginUnsupported answers every login, two-step and logout call on a
	// wrapper without wrapper-manager's login API: wrapper-lite logs in only
	// from its own command line (--login).
	ErrLoginUnsupported = errors.New("the wrapper has no login API; log in on the wrapper host")
	// ErrCredentialsUnusable is a username or password a managed wrapper
	// cannot pass to lite: its argument file is one argument per line and its
	// login argument is user:pass.
	ErrCredentialsUnusable = errors.New("the username or password contains a line break or NUL, or the username a colon")
	// ErrInvalidTwoStepCode is a code in a form Apple never sends; the login
	// it was meant for goes on.
	ErrInvalidTwoStepCode = errors.New("invalid two-step code")
	// ErrUnavailable means the wrapper is not running to take the call.
	ErrUnavailable = errors.New("the wrapper is not running")
)

const (
	LoginStatusLoggedIn     = "logged_in"
	LoginStatusNeedsTwoStep = "needs_2fa"
)

const maxReplyBytes = 16 << 20

// playbackRequestLimit bounds /key and /m3u8 requests in flight. wrapper-lite
// answers both behind one mutex, one at a time; more in flight would only queue
// inside wrapper-lite, where the wait runs down the request timeout and holds
// the HTTP worker threads /status and /lyrics need.
const playbackRequestLimit = 2

// manifestRequestLimit bounds /m3u8 requests, and /key requests for keys
// nobody needs yet (withKeyPermits), holding or waiting for a playback permit.
// With one of the two permits out of their reach, a batch of quality probes
// cannot hold both while a needed /key waits behind them, and since
// wrapper-lite answers /m3u8 one at a time anyway they lose no throughput.
const manifestRequestLimit = 1

// playbackWaitTimeouts bounds the wait for the permits of one /key or /m3u8
// request, in request timeouts: 5 minutes at the default 30 seconds. Every
// permit holder is bounded by one request timeout and two hold at a time, so
// the wait outlasts about twenty requests ahead that each run out their
// timeout, while a healthy wrapper answers one in well under a second. A queue
// that has not cleared by then means the wrapper is stuck or higher-priority
// requests keep overtaking, and the request fails with errPlaybackWait rather
// than wait on.
const playbackWaitTimeouts = 10

// errPlaybackWait is a /key or /m3u8 that gave up waiting for its permits.
var errPlaybackWait = errors.New("no playback slot became free")

// connectionLimit bounds the connections the client keeps to the wrapper.
// wrapper-lite serves HTTP on 8 threads and keeps each thread on its
// connection until that connection has been idle for 30 seconds, so a request
// sent on a ninth connection is not even read until one of them times out,
// and by then a request with the default 30-second timeout has failed. The
// data and playback permits plus one /status call fit inside the limit, so a
// permitted request never waits for a connection, and one thread stays free
// for another client of the same wrapper.
const connectionLimit = config.MaxParallelWrapperRequests + playbackRequestLimit + 1

// idleConnectionTimeout closes a connection idle this long, well before
// wrapper-lite's own 30 seconds, so a thread it holds goes back to the wrapper
// soon after a burst of requests ends.
const idleConnectionTimeout = 5 * time.Second

type LoginResult struct {
	Status  string `json:"status"`
	LoginID string `json:"login_id,omitempty"`
}

// Status is the wrapper's state as GET /api/v1/wrapper/status returns it.
// Ready means the wrapper answered and, for a wrapper-manager, has restored its
// accounts; LoggedIn means an account is logged in, which wrapper-lite shows by
// listing its storefront in Regions. Neither wrapper-lite nor wrapper-manager
// names its accounts, so Accounts stays empty. LoginSupported is true when the
// wrapper has the login API (a wrapper-manager). Regions and Accounts are never
// nil, so they encode as [] when empty.
type Status struct {
	Ready          bool     `json:"ready"`
	LoggedIn       bool     `json:"logged_in"`
	Regions        []string `json:"regions"`
	Accounts       []string `json:"accounts"`
	LoginSupported bool     `json:"login_supported"`
	// Managed is set when amdl runs the wrapper on this machine
	// (internal/wrapperhost).
	Managed *Managed `json:"managed,omitempty"`
}

// Managed describes a wrapper amdl runs in a QEMU guest on this machine.
type Managed struct {
	// Mode is starting, serving, login, stopped or failed.
	Mode  string `json:"mode"`
	Accel string `json:"accel"`
	// AccelFallback means the chosen accelerator failed and the guest runs on
	// TCG, plain emulation.
	AccelFallback bool   `json:"accel_fallback"`
	QEMU          string `json:"qemu"`
	// Guest names the installed guest: its upstream commit, or an import.
	Guest string `json:"guest"`
	// LoginResidue means the guest disk may still hold the last login's
	// arguments, password included: amdl zeroes them before the next
	// serving boot.
	LoginResidue bool   `json:"login_residue"`
	LastError    string `json:"last_error,omitempty"`
}

// LyricsRequestOptions are wrapper-lite's /lyrics query parameters, sent as
// given. Language and Script are left out when empty, so wrapper-lite applies
// its own defaults; Syllable is always sent because wrapper-lite defaults to
// word-timed lyrics.
type LyricsRequestOptions struct {
	Language string
	Syllable bool
	Script   string
}

// ReplyError is a wrapper reply that is not a success. Code and Msg are
// wrapper-lite's envelope fields, which carry an HTTP-like status (400 for a
// bad request, 404 when the item has no such resource, 500 when the wrapper
// failed); Code is 0 when the body was not an envelope. HTTPStatus is the
// response status, which wrapper-lite keeps at 200 except for unknown routes
// and handler exceptions.
type ReplyError struct {
	Endpoint   string
	HTTPStatus int
	Code       int
	Msg        string
}

func (e *ReplyError) Error() string {
	if e.Code == 0 {
		return fmt.Sprintf("wrapper %s: HTTP %d: %s", e.Endpoint, e.HTTPStatus, e.Msg)
	}
	return fmt.Sprintf("wrapper %s: code %d: %s", e.Endpoint, e.Code, e.Msg)
}

func (e *ReplyError) Unwrap() error { return ErrWrapperResponse }

// clientFault reports a 4xx answer: asking again cannot change it.
func (e *ReplyError) clientFault() bool {
	status := e.Code
	if status == 0 {
		status = e.HTTPStatus
	}
	return status >= 400 && status < 500
}

// Client talks to one wrapper-lite instance. It is safe for concurrent use.
type Client struct {
	base          *url.URL
	http          *http.Client
	timeout       time.Duration
	loginTimeout  time.Duration
	dataLimit     *limits.Semaphore
	playbackLimit *limits.Semaphore
	manifestLimit *limits.Semaphore

	parse       TemplateParser
	templates   *templateCache
	samples     *samplePool
	retryDelays []time.Duration

	logins loginBook

	// life ends at Close and stops template fetches no caller waits for.
	life     context.Context
	shutdown context.CancelFunc
}

type ClientOption func(*Client)

// WithTemplateParser replaces the FairPlay template implementation, which is
// internal/fairplay by default.
func WithTemplateParser(parse TemplateParser) ClientOption {
	return func(c *Client) { c.parse = parse }
}

// NewClient builds a client for cfg.Address, an http:// or https:// URL. It
// does not contact the wrapper.
func NewClient(cfg config.WrapperConfig, options ...ClientOption) (*Client, error) {
	base, err := url.Parse(cfg.Address)
	if err != nil {
		return nil, fmt.Errorf("parse wrapper address %q: %w", cfg.Address, err)
	}
	if (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("wrapper address %q is not an http:// or https:// URL", cfg.Address)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	transport.MaxConnsPerHost = connectionLimit
	transport.MaxIdleConnsPerHost = connectionLimit
	transport.IdleConnTimeout = idleConnectionTimeout
	life, shutdown := context.WithCancel(context.Background())
	c := &Client{
		base:          base,
		http:          &http.Client{Transport: transport},
		timeout:       cfg.Timeout(),
		loginTimeout:  cfg.LoginTimeout(),
		dataLimit:     limits.NewSemaphore(config.MaxParallelWrapperRequests),
		playbackLimit: limits.NewSemaphore(playbackRequestLimit),
		manifestLimit: limits.NewSemaphore(manifestRequestLimit),
		parse:         parseFairPlayTemplate,
		templates:     newTemplateCache(templateCacheSize),
		samples:       newSamplePool(config.MaxParallelDecrypts),
		retryDelays:   keyRetryDelays,
		life:          life,
		shutdown:      shutdown,
	}
	for _, option := range options {
		option(c)
	}
	if c.parse == nil {
		shutdown()
		return nil, errors.New("wrapper: nil template parser")
	}
	return c, nil
}

// Close stops template fetches in flight and releases idle connections.
func (c *Client) Close() error {
	c.shutdown()
	c.http.CloseIdleConnections()
	return nil
}

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// call sends one request bounded by the configured timeout and returns the
// reply's data object.
func (c *Client) call(ctx context.Context, method, endpoint string, query url.Values, body any) (json.RawMessage, error) {
	return c.callWithin(ctx, c.timeout, method, endpoint, query, body)
}

// callWithin is call with an explicit bound.
func (c *Client) callWithin(parent context.Context, timeout time.Duration, method, endpoint string, query url.Values, body any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	target := c.base.JoinPath(endpoint)
	target.RawQuery = query.Encode()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode wrapper %s body: %w", endpoint, err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, fmt.Errorf("build wrapper %s request: %w", endpoint, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, requestError(parent, endpoint, timeout, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes))
	if err != nil {
		return nil, fmt.Errorf("read wrapper %s reply: %w", endpoint, err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, &ReplyError{Endpoint: endpoint, HTTPStatus: resp.StatusCode, Msg: snippet(raw)}
	}
	if env.Code != 0 || resp.StatusCode != http.StatusOK {
		return nil, &ReplyError{Endpoint: endpoint, HTTPStatus: resp.StatusCode, Code: env.Code, Msg: env.Msg}
	}
	return env.Data, nil
}

// requestError is a request that got no reply, worded for an item message.
// The *url.Error from http.Client repeats the method, URL and query, which
// tell a reader nothing the endpoint does not, so only its cause is kept,
// still wrapped for errors.Is; a request that ran out its own timeout says so
// in words.
func requestError(parent context.Context, endpoint string, timeout time.Duration, err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	if parent.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return &timeoutError{endpoint: endpoint, after: timeout, err: err}
	}
	return fmt.Errorf("wrapper %s: %w", endpoint, err)
}

// timeoutError is a wrapper request that got no reply within its timeout.
type timeoutError struct {
	endpoint string
	after    time.Duration
	err      error
}

func (e *timeoutError) Error() string {
	return fmt.Sprintf("wrapper %s: no reply within %s", e.endpoint, e.after)
}

func (e *timeoutError) Unwrap() error { return e.err }

func snippet(raw []byte) string {
	const n = 200
	if len(raw) > n {
		return string(raw[:n]) + "..."
	}
	return string(raw)
}

func (c *Client) get(ctx context.Context, endpoint string, query url.Values, out any) error {
	data, err := c.call(ctx, http.MethodGet, endpoint, query, nil)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%w: wrapper %s data: %v", ErrWrapperResponse, endpoint, err)
	}
	return nil
}

// withPlaybackPermits runs run once it holds one permit from each of sems,
// taken in order. The wait for all of them together ends after
// playbackWaitTimeouts request timeouts; the request run sends gets its own
// full timeout from ctx however long the wait took.
func (c *Client) withPlaybackPermits(ctx context.Context, endpoint string, sems []*limits.Semaphore, run func() error) error {
	wait := playbackWaitTimeouts * c.timeout
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for _, sem := range sems {
		release, err := sem.Acquire(waitCtx)
		if err != nil {
			return playbackWaitError(ctx, endpoint, wait, err)
		}
		defer release()
	}
	return run()
}

// playbackWaitError is the error of a wait for playback permits that ended
// with err: errPlaybackWait once the wait's own bound ran out, else err.
func playbackWaitError(ctx context.Context, endpoint string, wait time.Duration, err error) error {
	if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("wrapper %s: %w within %s", endpoint, errPlaybackWait, wait)
	}
	return err
}

func withPermit(ctx context.Context, limit *limits.Semaphore, run func() error) error {
	release, err := limit.Acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return run()
}

// Status asks /status whether the wrapper is up and logged in. A wrapper-lite
// answers {regions}; a wrapper-manager v2 adds clientCount and ready, and is the
// only one with a login API.
func (c *Client) Status(ctx context.Context) (Status, error) {
	body, err := c.fetchStatus(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Ready:          true,
		LoggedIn:       len(body.Regions) > 0,
		Regions:        nonNil(body.Regions),
		Accounts:       []string{},
		LoginSupported: body.ClientCount != nil,
	}
	if body.Ready != nil {
		status.Ready = *body.Ready
	}
	return status, nil
}

type statusBody struct {
	Regions     []string `json:"regions"`
	ClientCount *int     `json:"clientCount"`
	Ready       *bool    `json:"ready"`
}

func (c *Client) fetchStatus(ctx context.Context) (statusBody, error) {
	var body statusBody
	err := c.get(ctx, "/status", nil, &body)
	return body, err
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// M3U8 returns the device master playlist for adamID. It takes the manifest
// permit before the playback permit, so the /m3u8 requests queued behind it
// wait outside the playback queue and a needed /key never waits behind more
// than one. Keys fetched ahead of need queue in the same manifest lane, after
// the /m3u8 requests of their job (withKeyPermits). The wait for both permits
// is bounded as withPlaybackPermits describes.
func (c *Client) M3U8(ctx context.Context, adamID string) (string, error) {
	var body struct {
		M3U8 string `json:"m3u8"`
	}
	err := c.withPlaybackPermits(ctx, "/m3u8", []*limits.Semaphore{c.manifestLimit, c.playbackLimit}, func() error {
		return c.get(ctx, "/m3u8", url.Values{"adamId": {adamID}}, &body)
	})
	return body.M3U8, err
}

// Lyrics returns adamID's lyrics as wrapper-lite renders them for opts.
func (c *Client) Lyrics(ctx context.Context, adamID string, opts LyricsRequestOptions) (string, error) {
	query := url.Values{"adamId": {adamID}, "syllable": {"0"}}
	if opts.Syllable {
		query.Set("syllable", "1")
	}
	if opts.Language != "" {
		query.Set("language", opts.Language)
	}
	if opts.Script != "" {
		query.Set("script", opts.Script)
	}
	var body struct {
		Lyrics string `json:"lyrics"`
	}
	err := withPermit(ctx, c.dataLimit, func() error {
		return c.get(ctx, "/lyrics", query, &body)
	})
	return body.Lyrics, err
}

// WebPlayback returns the web-player playlist URL for adamID, which carries
// the AAC-LC rendition.
func (c *Client) WebPlayback(ctx context.Context, adamID string) (string, error) {
	var body struct {
		M3U8 string `json:"m3u8"`
	}
	err := withPermit(ctx, c.dataLimit, func() error {
		return c.get(ctx, "/webplayback", url.Values{"adamId": {adamID}}, &body)
	})
	return body.M3U8, err
}

// License exchanges a base64 Widevine challenge for a base64 license.
func (c *Client) License(ctx context.Context, adamID, challenge, uri string) (string, error) {
	var body struct {
		License string `json:"license"`
	}
	err := withPermit(ctx, c.dataLimit, func() error {
		data, err := c.call(ctx, http.MethodPost, "/license", nil, map[string]string{
			"adamId": adamID, "challenge": challenge, "uri": uri,
		})
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &body); err != nil {
			return fmt.Errorf("%w: wrapper /license data: %v", ErrWrapperResponse, err)
		}
		return nil
	})
	if err == nil && body.License == "" {
		err = fmt.Errorf("%w: wrapper /license reply has no license", ErrWrapperResponse)
	}
	return body.License, err
}

// NormalizeAddress completes a wrapper address typed by a person: a scheme
// defaults to http, and a trailing slash is dropped.
func NormalizeAddress(s string) string {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	if s != "" && !strings.Contains(s, "://") {
		s = "http://" + s
	}
	return s
}
