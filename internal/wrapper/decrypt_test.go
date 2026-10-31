package wrapper

import (
	"amdl/internal/config"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// xorTemplate stands in for a FairPlay template: it XORs every byte of the
// 16-byte-aligned prefix with its value, so a test can build ciphertext from
// known plaintext and tell which template decrypted it.
type xorTemplate byte

func (x xorTemplate) DecryptSample(sample []byte) {
	for i := range len(sample) / 16 * 16 {
		sample[i] ^= byte(x)
	}
}

// parseXOR reads {"xor": n}; a reply without it is a template wrapper-lite
// failed to capture.
func parseXOR(data []byte) (SampleDecrypter, error) {
	var body struct {
		XOR *int `json:"xor"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	if body.XOR == nil {
		return nil, errors.New("reply has no template fields")
	}
	return xorTemplate(*body.XOR), nil
}

func encrypt(plain []byte, x byte) []byte {
	out := append([]byte(nil), plain...)
	xorTemplate(x).DecryptSample(out)
	return out
}

type keyServer struct {
	mu     sync.Mutex
	xor    map[string]int
	calls  map[string]int
	adamID map[string][]string
}

func newKeyServer(xor map[string]int) *keyServer {
	return &keyServer{xor: xor, calls: map[string]int{}, adamID: map[string][]string{}}
}

func (s *keyServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	uri := r.URL.Query().Get("uri")
	s.mu.Lock()
	s.calls[uri]++
	s.adamID[uri] = append(s.adamID[uri], r.URL.Query().Get("adamId"))
	x, ok := s.xor[uri]
	s.mu.Unlock()
	if r.URL.Path != "/key" || !ok {
		writeReply(w, 400, "unexpected request", nil)
		return
	}
	writeReply(w, 0, "SUCCESS", map[string]any{"xor": x})
}

func (s *keyServer) callCount(uri string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[uri]
}

func TestDecryptFragmentReusesTemplatesAcrossSessions(t *testing.T) {
	const key = "skd://itunes.apple.com/p1/c6"
	keys := newKeyServer(map[string]int{key: 0x5a})
	c := newTestClient(t, keys)

	aligned := []byte("0123456789abcdef0123456789abcdef")
	unaligned := []byte("0123456789abcdef0123456789abcdefTAIL!")
	for _, adamID := range []string{"101", "101", "202"} {
		session, err := c.NewDecryptSession(context.Background(), adamID)
		if err != nil {
			t.Fatal(err)
		}
		samples := [][]byte{encrypt(aligned, 0x5a), encrypt(unaligned, 0x5a)}
		got, err := session.DecryptFragment(key, samples)
		if err != nil {
			t.Fatalf("DecryptFragment: %v", err)
		}
		if string(got[0]) != string(aligned) || string(got[1]) != string(unaligned) {
			t.Fatalf("plaintext = %q, %q; want %q, %q", got[0], got[1], aligned, unaligned)
		}
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if n := keys.callCount(key); n != 1 {
		t.Fatalf("/key calls = %d, want 1 for one key across three sessions", n)
	}
}

// A track's keys fetched ahead by PrepareKeys are what its decrypt session
// uses: the session sends no /key of its own.
func TestPrepareKeysWarmsTheSessionsTemplates(t *testing.T) {
	const key = "skd://itunes.apple.com/p1/c6"
	keys := newKeyServer(map[string]int{key: 0x5a, prefetchKeyURI: 0x11})
	c := newTestClient(t, keys)

	if err := c.PrepareKeys(context.Background(), "101", []string{prefetchKeyURI, key, "", key}); err != nil {
		t.Fatalf("PrepareKeys: %v", err)
	}
	session, _ := c.NewDecryptSession(context.Background(), "101")
	plain := []byte("0123456789abcdef")
	for k, x := range map[string]byte{prefetchKeyURI: 0x11, key: 0x5a} {
		got, err := session.DecryptFragment(k, [][]byte{encrypt(plain, x)})
		if err != nil || string(got[0]) != string(plain) {
			t.Fatalf("DecryptFragment(%s) = %q, %v", k, got, err)
		}
	}
	for _, k := range []string{prefetchKeyURI, key} {
		if n := keys.callCount(k); n != 1 {
			t.Fatalf("/key calls for %s = %d, want 1", k, n)
		}
	}
	if err := c.PrepareKeys(context.Background(), "101", []string{key, "skd://unknown"}); err == nil {
		t.Fatal("PrepareKeys with a key the wrapper refuses returned nil")
	}
}

// The built-in parser decrypts Temari's public golden sample (track
// 1720704575, MIT) from a /key reply shaped like wrapper-lite's, whose data
// object also carries adamId, keyUri and contentKey.
func TestBuiltInParserDecryptsTheGoldenSample(t *testing.T) {
	read := func(ext string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "fairplay", "testdata", "track_1720704575_s1."+ext))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var data map[string]any
	if err := json.Unmarshal(read("json"), &data); err != nil {
		t.Fatal(err)
	}
	const key = "skd://itunes.apple.com/p1720704575/c6"
	data["adamId"], data["keyUri"], data["contentKey"] = "1720704575", key, "00"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeReply(w, 0, "SUCCESS", data)
	}))
	defer srv.Close()
	c, err := NewClient(config.WrapperConfig{Address: srv.URL, TimeoutSeconds: 5})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	session, _ := c.NewDecryptSession(context.Background(), "1720704575")
	got, err := session.DecryptFragment(key, [][]byte{read("ct")})
	if err != nil {
		t.Fatalf("DecryptFragment: %v", err)
	}
	if !bytes.Equal(got[0], read("pt")) {
		t.Fatal("plaintext differs from the golden plaintext")
	}
}

func TestDecryptFragmentRejectsAFragmentWithoutKey(t *testing.T) {
	c := newTestClient(t, newKeyServer(nil))
	session, _ := c.NewDecryptSession(context.Background(), "1")
	if out, err := session.DecryptFragment("", nil); out != nil || err != nil {
		t.Fatalf("empty fragment = (%v, %v), want (nil, nil)", out, err)
	}
	if _, err := session.DecryptFragment("", [][]byte{make([]byte, 16)}); err == nil {
		t.Fatal("DecryptFragment accepted a fragment with no key URI")
	}
}

func TestPrefetchKeyIsFetchedOnceForAdamIDZero(t *testing.T) {
	var calls atomic.Int32
	var adamIDs sync.Map
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		adamIDs.Store(r.URL.Query().Get("adamId"), true)
		time.Sleep(50 * time.Millisecond)
		writeReply(w, 0, "SUCCESS", map[string]any{"xor": 7})
	}))
	c.templates = newTemplateCache(1)

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Go(func() {
			session, _ := c.NewDecryptSession(context.Background(), fmt.Sprint(1000+i))
			_, err := session.DecryptFragment(prefetchKeyURI, [][]byte{make([]byte, 16)})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	// Fill the one-entry cache with other keys; the prefetch template stays.
	session, _ := c.NewDecryptSession(context.Background(), "1")
	for _, key := range []string{"skd://a", "skd://b", prefetchKeyURI} {
		if _, err := session.DecryptFragment(key, [][]byte{make([]byte, 16)}); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("/key calls = %d, want 3: the prefetch key once, then skd://a and skd://b", n)
	}
	var got []string
	adamIDs.Range(func(k, _ any) bool { got = append(got, k.(string)); return true })
	if _, ok := adamIDs.Load("0"); !ok || len(got) != 2 {
		t.Fatalf("adamIds sent = %v, want 0 for the prefetch key and 1 for the others", got)
	}
}

func TestKeyTemplateRetries(t *testing.T) {
	ok := func(w http.ResponseWriter) { writeReply(w, 0, "SUCCESS", map[string]any{"xor": 1}) }
	tests := []struct {
		name      string
		fail      func(w http.ResponseWriter)
		failures  int // replies that fail before one succeeds
		wantCalls int
		wantErr   bool
		wantCode  int
	}{
		{
			name: "handler exception after a token refresh",
			fail: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusInternalServerError)
				writeReply(w, 500, "internal error", nil)
			},
			failures: 2, wantCalls: 3,
		},
		{
			name:     "proxy error page",
			fail:     func(w http.ResponseWriter) { http.Error(w, "<html>bad gateway</html>", http.StatusBadGateway) },
			failures: 1, wantCalls: 2,
		},
		{
			name:     "template not captured",
			fail:     func(w http.ResponseWriter) { writeReply(w, 0, "SUCCESS", map[string]any{"contentKey": "x"}) },
			failures: 1, wantCalls: 2,
		},
		{
			name:     "bad request is not retried",
			fail:     func(w http.ResponseWriter) { writeReply(w, 400, "invalid uri for adamId", nil) },
			failures: 99, wantCalls: 1, wantErr: true, wantCode: 400,
		},
		{
			name: "gives up after every retry",
			fail: func(w http.ResponseWriter) {
				w.WriteHeader(http.StatusInternalServerError)
				writeReply(w, 500, "internal error", nil)
			},
			failures: 99, wantCalls: len(keyRetryDelays) + 1, wantErr: true, wantCode: 500,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if int(calls.Add(1)) <= tt.failures {
					tt.fail(w)
					return
				}
				ok(w)
			}))
			session, _ := c.NewDecryptSession(context.Background(), "1")
			_, err := session.DecryptFragment("skd://k", [][]byte{make([]byte, 16)})
			if got := int(calls.Load()); got != tt.wantCalls {
				t.Fatalf("/key calls = %d, want %d", got, tt.wantCalls)
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("DecryptFragment: %v", err)
				}
				return
			}
			var reply *ReplyError
			if !errors.As(err, &reply) || reply.Code != tt.wantCode || !errors.Is(err, ErrWrapperResponse) {
				t.Fatalf("error = %v, want a ReplyError with code %d wrapping ErrWrapperResponse", err, tt.wantCode)
			}
		})
	}
}

func TestFailedTemplateFetchIsNotCached(t *testing.T) {
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			writeReply(w, 400, "missing uri", nil)
			return
		}
		writeReply(w, 0, "SUCCESS", map[string]any{"xor": 1})
	}))
	session, _ := c.NewDecryptSession(context.Background(), "1")
	if _, err := session.DecryptFragment("skd://k", [][]byte{make([]byte, 16)}); err == nil {
		t.Fatal("first DecryptFragment succeeded, want the 400 reply")
	}
	if _, err := session.DecryptFragment("skd://k", [][]byte{make([]byte, 16)}); err != nil {
		t.Fatalf("second DecryptFragment: %v", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("/key calls = %d, want 2", n)
	}
}

func TestTemplateCacheEvictsTheLeastRecentlyUsedKey(t *testing.T) {
	keys := newKeyServer(map[string]int{"skd://a": 1, "skd://b": 2, "skd://c": 3})
	c := newTestClient(t, keys)
	c.templates = newTemplateCache(2)
	session, _ := c.NewDecryptSession(context.Background(), "1")
	for _, key := range []string{"skd://a", "skd://b", "skd://a", "skd://c", "skd://a", "skd://b"} {
		if _, err := session.DecryptFragment(key, [][]byte{make([]byte, 16)}); err != nil {
			t.Fatal(err)
		}
	}
	got := map[string]int{"skd://a": keys.callCount("skd://a"), "skd://b": keys.callCount("skd://b"), "skd://c": keys.callCount("skd://c")}
	want := map[string]int{"skd://a": 1, "skd://b": 2, "skd://c": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("/key calls = %v, want %v (b evicted when c arrived)", got, want)
	}
}

func TestCancelledCallerDoesNotFailOthersWaitingForTheSameKey(t *testing.T) {
	arrived := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(arrived)
		}
		<-release
		writeReply(w, 0, "SUCCESS", map[string]any{"xor": 9})
	}))
	plain := []byte("0123456789abcdef")

	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() {
		session, _ := c.NewDecryptSession(ctx, "1")
		_, err := session.DecryptFragment("skd://k", [][]byte{encrypt(plain, 9)})
		first <- err
	}()
	<-arrived
	second := make(chan []byte, 1)
	go func() {
		session, _ := c.NewDecryptSession(context.Background(), "2")
		out, err := session.DecryptFragment("skd://k", [][]byte{encrypt(plain, 9)})
		if err != nil {
			t.Errorf("waiting caller: %v", err)
			second <- nil
			return
		}
		second <- out[0]
	}()
	// Cancel only once the second caller waits too: the fetch ends when
	// nobody is left waiting for it.
	waitFor(t, func() bool {
		c.templates.mu.Lock()
		defer c.templates.mu.Unlock()
		entry := c.templates.entries["skd://k"]
		return entry != nil && entry.waiters == 2
	})
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller = %v, want context.Canceled", err)
	}
	close(release)
	if got := <-second; string(got) != string(plain) {
		t.Fatalf("waiting caller plaintext = %q, want %q", got, plain)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("/key calls = %d, want the one shared fetch", n)
	}
}

func TestSplitSamples(t *testing.T) {
	sizes := func(chunks [][][]byte) []int {
		var out []int
		for _, chunk := range chunks {
			out = append(out, len(chunk))
		}
		return out
	}
	samples := func(n, size int) [][]byte {
		out := make([][]byte, n)
		for i := range out {
			out[i] = make([]byte, size)
		}
		return out
	}
	tests := []struct {
		name    string
		samples [][]byte
		parts   int
		want    []int // samples per chunk
	}{
		{name: "even split", samples: samples(8, 64<<10), parts: 4, want: []int{2, 2, 2, 2}},
		{name: "fewer samples than parts", samples: samples(3, 64<<10), parts: 8, want: []int{1, 1, 1}},
		{name: "too little data to split", samples: samples(40, 1<<10), parts: 8, want: []int{40}},
		{name: "one worker", samples: samples(8, 64<<10), parts: 1, want: []int{8}},
		{name: "chunks of at least minChunkBytes", samples: samples(16, 8<<10), parts: 8, want: []int{4, 4, 4, 4}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sizes(splitSamples(tt.samples, tt.parts)); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("chunk sizes = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSamplePoolDecryptsEverySampleInPlace(t *testing.T) {
	pool := newSamplePool(4)
	var wg sync.WaitGroup
	for round := range 8 {
		wg.Go(func() {
			x := byte(round + 1)
			plain := make([][]byte, 300)
			cipher := make([][]byte, len(plain))
			for i := range plain {
				plain[i] = make([]byte, (i*733+round*97)%9000)
				for j := range plain[i] {
					plain[i][j] = byte(i + j)
				}
				cipher[i] = encrypt(plain[i], x)
			}
			pool.decrypt(xorTemplate(x), cipher)
			for i := range plain {
				if string(cipher[i]) != string(plain[i]) {
					t.Errorf("round %d sample %d (%d bytes) not decrypted", round, i, len(plain[i]))
					return
				}
			}
		})
	}
	wg.Wait()
}
