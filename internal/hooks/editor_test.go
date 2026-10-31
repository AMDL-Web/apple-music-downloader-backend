package hooks

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const mixedHooks = `# my hooks
enabled: true
entries:
  # runs after every album
  - name: "tag"
    type: "exec"
    events: ["job_finished"]
    command: "/opt/tag.sh --all"
    workdir: "/opt"
  - name: "plex"
    type: "webhook"
    events: ["job_finished"]
    url: "http://plex.local:32400/library/sections/all/refresh"
    method: "GET"
    headers:
      X-Plex-Token: "secret"
`

func newEditor(t *testing.T, body string) (*Editor, *Dispatcher, string) {
	t.Helper()
	path := writeHooksConfig(t, body)
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(cfg, nil, discardLogger())
	return NewEditor(path, d), d, path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestEditorWritesWebhooksAndLeavesCommandsAlone(t *testing.T) {
	ed, d, path := newEditor(t, mixedHooks)
	before, _ := LoadConfig(path)

	url := "https://ntfy.sh/amdl-topic"
	events := []string{"job_finished", "job_failed"}
	if _, err := ed.PutWebhook("notify", WebhookPatch{URL: &url, Events: &events}); err != nil {
		t.Fatal(err)
	}
	off := false
	if _, err := ed.PutWebhook("plex", WebhookPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}

	after, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("the written file does not load: %v\n%s", err, readFile(t, path))
	}
	if !reflect.DeepEqual(after.Entries[0], before.Entries[0]) {
		t.Errorf("the command hook changed:\n%+v\n%+v", before.Entries[0], after.Entries[0])
	}
	// A patch keeps what it does not name: the address and its token.
	plex := after.Entries[1]
	if plex.IsEnabled() || plex.URL != before.Entries[1].URL || plex.Headers["X-Plex-Token"] != "secret" || plex.Method != "GET" {
		t.Errorf("plex after the patch: %+v", plex)
	}
	if len(after.Entries) != 3 || after.Entries[2].Name != "notify" || after.Entries[2].URL != url {
		t.Errorf("entries after the put: %+v", after.Entries)
	}
	if out := readFile(t, path); !strings.Contains(out, "# my hooks") || !strings.Contains(out, "# runs after every album") {
		t.Errorf("the comments went:\n%s", out)
	}
	// The dispatcher has the file's new state.
	if got := d.List(); len(got.Entries) != 3 || got.Entries[1].Enabled {
		t.Errorf("dispatcher listing after the writes: %+v", got)
	}

	if err := ed.Delete("notify"); err != nil {
		t.Fatal(err)
	}
	if got := d.List(); len(got.Entries) != 2 {
		t.Errorf("dispatcher listing after the delete: %+v", got)
	}
}

func TestEditorRefusesCommandHooks(t *testing.T) {
	ed, _, path := newEditor(t, mixedHooks)
	url := "https://example.com/x"
	if _, err := ed.PutWebhook("tag", WebhookPatch{URL: &url}); !errors.Is(err, ErrReadOnly) {
		t.Errorf("put on a command hook: %v, want ErrReadOnly", err)
	}
	if err := ed.Delete("tag"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("delete of a command hook: %v, want ErrReadOnly", err)
	}
	if err := ed.Test(context.Background(), "tag"); !errors.Is(err, ErrReadOnly) {
		t.Errorf("test of a command hook: %v, want ErrReadOnly", err)
	}
	if err := ed.Delete("nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete of a missing hook: %v, want ErrNotFound", err)
	}
	if got := readFile(t, path); got != mixedHooks {
		t.Errorf("a refused write changed the file:\n%s", got)
	}
}

func TestEditorKeepsTheFileWhenAWriteWouldNotLoad(t *testing.T) {
	ed, d, path := newEditor(t, mixedHooks)
	bad := "ftp://example.com"
	events := []string{"job_finished"}
	_, err := ed.PutWebhook("bad", WebhookPatch{URL: &bad, Events: &events})
	var invalid *InvalidError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "http or https") {
		t.Errorf("a bad address: %v, want an InvalidError naming the scheme", err)
	}
	get, body := "GET", "{}"
	if _, err := ed.PutWebhook("plex", WebhookPatch{Method: &get, Body: &body}); !errors.As(err, &invalid) {
		t.Errorf("a GET with a body: %v, want an InvalidError", err)
	}
	if got := readFile(t, path); got != mixedHooks || len(d.List().Entries) != 2 {
		t.Errorf("a refused write changed the file or the dispatcher:\n%s", got)
	}

	// A hand edit that broke the file stops writes until it is fixed.
	if err := os.WriteFile(path, []byte("enabled: maybe\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var fileErr *FileError
	if _, err := ed.SetEnabled(false); !errors.As(err, &fileErr) {
		t.Errorf("a write over a broken file: %v, want a FileError", err)
	}
}

func TestEditorStartsFromTheShippedFile(t *testing.T) {
	shipped, err := os.ReadFile(filepath.Join("..", "..", "configs", "hooks.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	ed, d, path := newEditor(t, string(shipped))
	if _, err := ed.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	url := "http://jellyfin.local:8096/Library/Refresh"
	events := []string{"job_finished"}
	if _, err := ed.PutWebhook("jellyfin", WebhookPatch{URL: &url, Events: &events}); err != nil {
		t.Fatal(err)
	}
	got := d.List()
	if !got.Enabled || len(got.Entries) != 1 || got.Entries[0].Origin != "http://jellyfin.local:8096" {
		t.Errorf("listing after the writes: %+v", got)
	}
	// The examples under entries are documentation; they stay.
	if out := readFile(t, path); !strings.Contains(out, "exec example") || !strings.Contains(out, "JSON payload") {
		t.Errorf("the shipped comments went:\n%s", out)
	}

	// No file at all is an empty, disabled one.
	missing := NewEditor(filepath.Join(t.TempDir(), "hooks.yaml"), d)
	if _, err := missing.PutWebhook("jellyfin", WebhookPatch{URL: &url, Events: &events}); err != nil {
		t.Fatal(err)
	}
	if cfg, err := LoadConfig(missing.path); err != nil || cfg.Enabled || len(cfg.Entries) != 1 {
		t.Errorf("the created file: %+v, %v", cfg, err)
	}
}

func TestWebhookSendsItsMethodAndBody(t *testing.T) {
	type seen struct{ method, contentType, body, token string }
	got := make(chan seen, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.Header.Get("Content-Type"), string(raw), r.Header.Get("X-Plex-Token")}
	}))
	defer server.Close()

	cases := []struct {
		name  string
		entry Entry
		want  seen
	}{
		{"a GET carries no body", Entry{Method: "GET", Headers: map[string]string{"X-Plex-Token": "t"}},
			seen{method: "GET", token: "t"}},
		{"a template writes a JSON body", Entry{Body: `{"chat_id": {{json "42"}}, "text": {{json (printf "%s: %v saved" (or .job.title .job.input) .job.completed_items)}}}`},
			seen{method: "POST", contentType: "application/json", body: `{"chat_id": "42", "text": "Test from amdl: 1 saved"}`}},
		{"a header names another type", Entry{Method: "PUT", Body: "{{.job.title}} done", Headers: map[string]string{"Content-Type": "text/plain"}},
			seen{method: "PUT", contentType: "text/plain", body: "Test from amdl done"}},
	}
	for _, c := range cases {
		c.entry.URL, c.entry.Type, c.entry.Events = server.URL, typeWebhook, []string{"job_finished"}
		if err := (&webhookRunner{client: server.Client()}).Run(context.Background(), c.entry, samplePayload(c.entry)); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if s := <-got; s != c.want {
			t.Errorf("%s: sent %+v, want %+v", c.name, s, c.want)
		}
	}

	// A queued job has no title yet: or falls back without "<no value>".
	queued := Entry{Events: []string{"job_queued"}}
	body, err := renderBody(`{{or .job.title .job.input}}`, samplePayload(queued))
	if err != nil || string(body) != "https://music.apple.com/" {
		t.Errorf("queued body = %q, %v", body, err)
	}
}

func TestEditorTestSendsTheWebhookOnce(t *testing.T) {
	calls := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls <- r.Method
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	ed, _, _ := newEditor(t, "enabled: false\nentries:\n  - name: n\n    type: webhook\n    events: [job_failed]\n    url: "+server.URL+"\n    max_attempts: 3\n")
	err := ed.Test(context.Background(), "n")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("test against a 401: %v", err)
	}
	if len(calls) != 1 {
		t.Errorf("the test sent %d requests, want 1 whatever max_attempts says", len(calls))
	}
}
