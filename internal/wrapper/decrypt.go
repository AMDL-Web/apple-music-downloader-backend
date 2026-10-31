package wrapper

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"amdl/internal/fairplay"
	"amdl/internal/limits"
)

// SampleDecrypter decrypts one FairPlay sample in place: the 16-byte-aligned
// prefix is decrypted and a shorter tail is left as is. It must be safe for
// concurrent use.
type SampleDecrypter interface {
	DecryptSample(sample []byte)
}

// TemplateParser builds a SampleDecrypter from the data object of one /key
// reply (ctx, state and the entry registers).
type TemplateParser func(data []byte) (SampleDecrypter, error)

// parseFairPlayTemplate is the TemplateParser NewClient uses by default.
func parseFairPlayTemplate(data []byte) (SampleDecrypter, error) {
	t, err := fairplay.ParseTemplate(data)
	if err != nil {
		// A nil *fairplay.Template inside the interface would not equal nil.
		return nil, err
	}
	return t, nil
}

// DecryptSession decrypts one track's samples fragment by fragment, so a whole
// track's plaintext never has to be in memory at once.
type DecryptSession interface {
	// DecryptFragment decrypts one fragment's samples, all of which share key,
	// and returns their plaintext in the same order. It blocks until every
	// sample in the batch is done.
	DecryptFragment(key string, samples [][]byte) ([][]byte, error)
	// Close releases the session.
	Close() error
}

const (
	// prefetchKeyURI is the key every enhanced-HLS track opens with. Its
	// template is shared by all tracks and wrapper-lite serves it only for
	// adamId 0.
	prefetchKeyURI = "skd://itunes.apple.com/P000000000/s1/e1"
	prefetchAdamID = "0"

	// templateCacheSize bounds the per-key-URI templates kept across jobs. A
	// parsed template is about 72 KiB (the 32 KiB ctx table as bytes and as
	// words, and an 8 KiB state), so the cache stays under 19 MB.
	templateCacheSize = 256

	// minChunkBytes is the least sample data worth a goroutine of its own.
	minChunkBytes = 32 << 10
)

// keyRetryDelays are the waits before each /key retry. wrapper-lite can fail
// /key several times in a row for a few seconds ("Invalid CKC error", answered
// as HTTP 500) around its periodic token refresh.
var keyRetryDelays = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}

// NewDecryptSession returns a session for adamID's samples. Each key's template
// is fetched from the wrapper once per process and decryption runs locally;
// ctx bounds the template requests the session waits for.
func (c *Client) NewDecryptSession(ctx context.Context, adamID string) (DecryptSession, error) {
	return &decryptSession{client: c, ctx: ctx, adamID: adamID}, nil
}

type decryptSession struct {
	client *Client
	ctx    context.Context
	adamID string
}

// DecryptFragment decrypts samples in place and returns the same slices.
func (s *decryptSession) DecryptFragment(key string, samples [][]byte) ([][]byte, error) {
	if len(samples) == 0 {
		return nil, nil
	}
	if key == "" {
		return nil, errors.New("wrapper decrypt: fragment has no key URI")
	}
	dec, err := s.client.template(s.ctx, s.adamID, key)
	if err != nil {
		return nil, err
	}
	s.client.samples.decrypt(dec, samples)
	return samples, nil
}

func (s *decryptSession) Close() error { return nil }

// PrepareKeys fetches the templates of adamID's keys that are not cached yet
// and returns once each is cached or has failed, joining fetches already in
// flight. wrapper-lite answers /key one at a time, about two seconds each, so
// a track's keys are worth fetching while its media downloads: its decrypt
// session then finds them cached and runs at CPU speed. Empty and repeated
// keys are skipped. The error joins every key's failure.
func (c *Client) PrepareKeys(ctx context.Context, adamID string, keys []string) error {
	errs := make([]error, len(keys))
	var wg sync.WaitGroup
	for i, key := range keys {
		if key == "" || slices.Contains(keys[:i], key) {
			continue
		}
		wg.Go(func() { _, errs[i] = c.template(ctx, adamID, key) })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// template returns key's decrypter from the cache, fetching it on a miss. The
// prefetch key is fetched for adamId 0 and never evicted.
func (c *Client) template(ctx context.Context, adamID, key string) (SampleDecrypter, error) {
	pinned := key == prefetchKeyURI
	if pinned {
		adamID = prefetchAdamID
	}
	return c.templates.get(ctx, key, pinned, func(fetchCtx context.Context) (SampleDecrypter, error) {
		fetchCtx, cancel := context.WithCancel(fetchCtx)
		defer cancel()
		stop := context.AfterFunc(c.life, cancel)
		defer stop()
		return c.fetchTemplate(fetchCtx, adamID, key)
	})
}

// fetchTemplate asks for key's template, retrying after keyRetryDelays. Each
// attempt waits at most playbackWaitTimeouts request timeouts for a playback
// permit and then has the full request timeout for its /key; the wait is not
// charged to the request. An attempt that runs out of wait ends the fetch
// without a retry, which would only queue again.
func (c *Client) fetchTemplate(ctx context.Context, adamID, key string) (SampleDecrypter, error) {
	for attempt := 0; ; attempt++ {
		dec, err := c.fetchTemplateOnce(ctx, adamID, key)
		if err == nil {
			return dec, nil
		}
		if attempt == len(c.retryDelays) || !retryable(err) || ctx.Err() != nil {
			return nil, fmt.Errorf("fetch key template for %s (attempt %d): %w", key, attempt+1, err)
		}
		timer := time.NewTimer(c.retryDelays[attempt])
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("fetch key template for %s (attempt %d): %w", key, attempt+1, err)
		case <-timer.C:
		}
	}
}

func (c *Client) fetchTemplateOnce(ctx context.Context, adamID, key string) (SampleDecrypter, error) {
	var dec SampleDecrypter
	err := c.withPlaybackPermits(ctx, "/key", []*limits.Semaphore{c.playbackLimit}, func() error {
		data, err := c.call(ctx, http.MethodGet, "/key", url.Values{"adamId": {adamID}, "uri": {key}}, nil)
		if err != nil {
			return err
		}
		// wrapper-lite answers code 0 without the template fields when it
		// got the content key but could not capture the template.
		dec, err = c.parse(data)
		if err != nil {
			return fmt.Errorf("parse key template: %w", err)
		}
		return nil
	})
	return dec, err
}

// retryable reports whether a failed /key may succeed when asked again: any
// failure except a 4xx answer, a wait for a permit that ran out and the
// caller's own cancellation.
func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, errPlaybackWait) {
		return false
	}
	var reply *ReplyError
	if errors.As(err, &reply) {
		return !reply.clientFault()
	}
	return true
}

// templateCache holds one decrypter per key URI, shares a fetch in flight
// between its callers and evicts the least recently used unpinned entry beyond
// its limit. Failed fetches are not kept.
//
// A fetch outlives the caller that started it, since other tracks may wait for
// the same key and one cancelled job must not fail them, but it ends once
// every caller waiting for it has given up.
type templateCache struct {
	mu      sync.Mutex
	limit   int
	entries map[string]*templateEntry
	lru     *list.List // of key URIs; front is the most recently used
}

type templateEntry struct {
	ready  chan struct{} // closed once dec or err is set
	dec    SampleDecrypter
	err    error
	elem   *list.Element // nil while loading and for pinned entries
	loaded bool          // dec or err is set; guarded by templateCache.mu
	// waiters counts the callers waiting while the fetch runs, and cancel
	// ends the fetch. Both are guarded by templateCache.mu.
	waiters int
	cancel  context.CancelFunc
}

func newTemplateCache(limit int) *templateCache {
	return &templateCache{limit: limit, entries: map[string]*templateEntry{}, lru: list.New()}
}

// get returns key's decrypter, starting load on a miss. load's context carries
// the values of the ctx that started it, such as its priority, and is
// cancelled when every caller has stopped waiting.
func (c *templateCache) get(ctx context.Context, key string, pinned bool, load func(context.Context) (SampleDecrypter, error)) (SampleDecrypter, error) {
	c.mu.Lock()
	entry, ok := c.entries[key]
	if ok && entry.loaded {
		if entry.elem != nil {
			c.lru.MoveToFront(entry.elem)
		}
		c.mu.Unlock()
		return entry.dec, entry.err
	}
	if !ok {
		fetchCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		entry = &templateEntry{ready: make(chan struct{}), cancel: cancel}
		c.entries[key] = entry
		go c.fill(fetchCtx, key, entry, pinned, load)
	}
	entry.waiters++
	c.mu.Unlock()
	select {
	case <-entry.ready:
		return entry.dec, entry.err
	case <-ctx.Done():
		c.leave(key, entry)
		return nil, ctx.Err()
	}
}

// leave drops a caller that stopped waiting for entry. The last one to leave a
// fetch still running cancels it and unlists the entry, so a later caller
// starts a fresh fetch instead of joining one that is ending.
func (c *templateCache) leave(key string, entry *templateEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry.waiters--
	if entry.waiters > 0 || entry.loaded {
		return
	}
	if c.entries[key] == entry {
		delete(c.entries, key)
	}
	entry.cancel()
}

func (c *templateCache) fill(ctx context.Context, key string, entry *templateEntry, pinned bool, load func(context.Context) (SampleDecrypter, error)) {
	dec, err := load(ctx)
	entry.cancel()
	c.mu.Lock()
	entry.dec, entry.err, entry.loaded = dec, err, true
	switch {
	case c.entries[key] != entry:
		// Every caller left; the entry is no longer listed.
	case err != nil:
		delete(c.entries, key)
	case !pinned:
		entry.elem = c.lru.PushFront(key)
		for c.lru.Len() > c.limit {
			oldest := c.lru.Back()
			c.lru.Remove(oldest)
			delete(c.entries, oldest.Value.(string))
		}
	}
	c.mu.Unlock()
	close(entry.ready)
}

// samplePool spreads DecryptSample calls over at most size goroutines across
// every session at once.
type samplePool struct {
	size  int
	slots chan struct{}
}

func newSamplePool(size int) *samplePool {
	size = max(size, 1)
	return &samplePool{size: size, slots: make(chan struct{}, size)}
}

func (p *samplePool) decrypt(dec SampleDecrypter, samples [][]byte) {
	chunks := splitSamples(samples, p.size)
	if len(chunks) == 1 {
		decryptAll(dec, chunks[0])
		return
	}
	var wg sync.WaitGroup
	for _, chunk := range chunks {
		p.slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-p.slots }()
			decryptAll(dec, chunk)
		})
	}
	wg.Wait()
}

func decryptAll(dec SampleDecrypter, samples [][]byte) {
	for _, sample := range samples {
		dec.DecryptSample(sample)
	}
}

// splitSamples cuts samples into at most parts contiguous runs of about equal
// byte size, none smaller than minChunkBytes unless it is the only run.
func splitSamples(samples [][]byte, parts int) [][][]byte {
	total := 0
	for _, sample := range samples {
		total += len(sample)
	}
	parts = min(parts, len(samples), total/minChunkBytes)
	if parts <= 1 {
		return [][][]byte{samples}
	}
	chunks := make([][][]byte, 0, parts)
	start, done := 0, 0
	for i, sample := range samples {
		done += len(sample)
		if len(chunks) < parts-1 && done*parts >= total*(len(chunks)+1) {
			chunks = append(chunks, samples[start:i+1])
			start = i + 1
		}
	}
	if start < len(samples) {
		chunks = append(chunks, samples[start:])
	}
	return chunks
}
