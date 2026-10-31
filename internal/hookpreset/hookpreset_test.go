package hookpreset

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"text/template"
)

// render renders body as the backend does (internal/hooks renderBody): a
// text/template over the payload's JSON, with json.
func render(t *testing.T, body string, payload string) string {
	t.Helper()
	tpl, err := template.New("body").Funcs(template.FuncMap{"json": func(v any) (string, error) {
		raw, err := json.Marshal(v)
		return string(raw), err
	}}).Parse(body)
	if err != nil {
		t.Fatalf("body does not parse: %v\n%s", err, body)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := tpl.Execute(&out, data); err != nil {
		t.Fatalf("body does not render: %v", err)
	}
	return out.String()
}

const (
	finishedAlbum = `{"event":"job_finished","job":{"type":"album","title":"Elma","artist_name":"Yorushika","status":"completed","total_items":12,"completed_items":11,"skipped_items":1,"failed_items":0}}`
	failedAlbum   = `{"event":"job_failed","job":{"type":"album","title":"Elma","artist_name":"Yorushika","status":"failed","total_items":12,"completed_items":9,"failed_items":3}}`
	abortedSong   = `{"event":"job_failed","job":{"type":"song","input":"https://music.apple.com/x","status":"failed","total_items":0,"failed_items":0,"error":"wrapper unavailable"}}`
	finishedSong  = `{"event":"job_finished","job":{"type":"song","title":"Ame","artist_name":"Yorushika","status":"completed","total_items":1,"completed_items":1}}`
)

func build(t *testing.T, id string, v map[string]string) Request {
	t.Helper()
	p, ok := Find(id)
	if !ok {
		t.Fatalf("no preset %s", id)
	}
	w, err := p.Build(v)
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}
	return w
}

func TestNotificationsSayWhatHappened(t *testing.T) {
	w := build(t, "ntfy", map[string]string{"topic": "amdl downloads"})
	if w.URL != "https://ntfy.sh/amdl%20downloads" || w.Method != "POST" || (w.Headers)["Title"] != "AMDL" {
		t.Errorf("ntfy write: %+v %v", w, w.Headers)
	}
	if _, ok := (w.Headers)["Authorization"]; ok {
		t.Errorf("ntfy without a token sends Authorization")
	}
	for payload, want := range map[string]string{
		finishedAlbum: "Downloaded Elma — Yorushika · 12 tracks",
		failedAlbum:   "Failed Elma — Yorushika · 3 of 12 tracks failed",
		abortedSong:   "Failed https://music.apple.com/x: wrapper unavailable",
		finishedSong:  "Downloaded Ame — Yorushika",
	} {
		if got := render(t, w.Body, payload); got != want {
			t.Errorf("ntfy message = %q, want %q", got, want)
		}
	}
	if w := build(t, "ntfy", map[string]string{"server": "https://ntfy.example.com/", "topic": "t", "token": "tk_1"}); w.URL != "https://ntfy.example.com/t" || (w.Headers)["Authorization"] != "Bearer tk_1" {
		t.Errorf("self-hosted ntfy with a token: %+v %v", w, w.Headers)
	}
}

func TestJSONBodiesAreJSON(t *testing.T) {
	cases := []struct {
		id, url string
		values  map[string]string
		want    map[string]any
	}{
		{"telegram", "https://api.telegram.org/bot123:ABC/sendMessage", map[string]string{"token": "123:ABC", "chat": `-100"42`},
			map[string]any{"chat_id": `-100"42`, "text": "Downloaded Elma — Yorushika · 12 tracks"}},
		{"bark", "https://api.day.app/push", map[string]string{"key": "abc"},
			map[string]any{"device_key": "abc", "title": "AMDL", "body": "Downloaded Elma — Yorushika · 12 tracks", "group": "AMDL"}},
	}
	for _, c := range cases {
		w := build(t, c.id, c.values)
		if w.URL != c.url || w.Method != "POST" {
			t.Errorf("%s write: %+v", c.id, w)
		}
		var got map[string]any
		body := render(t, w.Body, finishedAlbum)
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Errorf("%s body is not JSON: %v\n%s", c.id, err, body)
			continue
		}
		for k, v := range c.want {
			if got[k] != v {
				t.Errorf("%s body %s = %v, want %v", c.id, k, got[k], v)
			}
		}
	}
}

func TestLibraryRefreshes(t *testing.T) {
	for _, c := range []struct {
		id, server, method, url, header, value string
	}{
		{"plex", "http://nas:32400/", "GET", "http://nas:32400/library/sections/all/refresh", "X-Plex-Token", "p"},
		{"jellyfin", "http://nas:8096", "POST", "http://nas:8096/Library/Refresh", "Authorization", `MediaBrowser Token="p"`},
		{"emby", "http://nas:8096", "POST", "http://nas:8096/emby/Library/Refresh", "X-Emby-Token", "p"},
	} {
		p, _ := Find(c.id)
		w := build(t, c.id, map[string]string{"server": c.server, p.Fields[1].Key: "p"})
		if w.Method != c.method || w.URL != c.url || (w.Headers)[c.header] != c.value || w.Body != "" {
			t.Errorf("%s write: %+v %v", c.id, w, w.Headers)
		}
	}
}

func TestMissingAndUnsafeValuesAreRefused(t *testing.T) {
	p, _ := Find("plex")
	if _, err := p.Build(map[string]string{"token": "p"}); err == nil || !strings.Contains(err.Error(), "Server") {
		t.Errorf("plex without a server: %v", err)
	}
	p, _ = Find("bark")
	if _, err := p.Build(map[string]string{"key": "{{.job}}"}); err == nil {
		t.Errorf("a key with template braces went into the body")
	}
	p, _ = Find("telegram")
	if _, err := p.Build(map[string]string{"token": "123/ABC", "chat": "1"}); err == nil {
		t.Errorf("a bot token with a slash went into the URL")
	}
	p, _ = Find("custom")
	w, err := p.Build(map[string]string{"url": "https://x.example", "method": "GET", "body": "ignored", "header": "X-Key: v"})
	if err != nil || w.Method != "GET" || w.Body != "" || (w.Headers)["X-Key"] != "v" {
		t.Errorf("custom GET: %+v %v", w, err)
	}
	if _, err := p.Build(map[string]string{"url": "https://x.example", "method": "PATCH"}); err == nil {
		t.Errorf("a method the preset does not offer was taken")
	}
	if _, err := p.Build(map[string]string{"url": "https://x.example", "command": "rm"}); err == nil || !strings.Contains(err.Error(), "command") {
		t.Errorf("a field the preset does not ask for: %v", err)
	}
}
