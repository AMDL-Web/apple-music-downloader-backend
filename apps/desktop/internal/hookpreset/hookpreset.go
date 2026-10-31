// Package hookpreset is the services the hooks editor knows: what each asks
// for, and the webhook that calls it, as a write to PUT /api/v1/hooks/{name}.
// Each request is the one the service documents; the backend sends it as
// written, its body a template over the hook payload.
package hookpreset

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"amdl/internal/client"
)

// Kind groups the presets in the editor.
type Kind int

const (
	Notify Kind = iota
	Library
	Custom
)

// Field is one value a preset asks for. A field with Choices is picked from
// them; Multiline takes a body.
type Field struct {
	Key, Label, Placeholder, Default string
	Secret, Optional, Multiline      bool
	Choices                          []string
}

// Preset is one service.
type Preset struct {
	ID, Name string
	Kind     Kind
	Fields   []Field
	// Events are the events a new hook of the preset runs on.
	Events []string
	build  func(v map[string]string) (client.HookWrite, error)
}

// Build is the write that points a hook at the service with values, keyed
// by field: its address, method, headers and body, all set, so a hook that
// called another service before is replaced whole. Events, job types and
// the switch are the caller's.
func (p Preset) Build(values map[string]string) (client.HookWrite, error) {
	v := map[string]string{}
	for _, f := range p.Fields {
		s := strings.TrimSpace(values[f.Key])
		if f.Multiline {
			s = values[f.Key]
		}
		if s == "" {
			s = f.Default
		}
		if s == "" && !f.Optional {
			return client.HookWrite{}, fmt.Errorf("%s is needed", f.Label)
		}
		if strings.Contains(s, "{{") || strings.Contains(s, "}}") {
			if !f.Multiline {
				return client.HookWrite{}, fmt.Errorf("%s cannot hold {{ or }}", f.Label)
			}
		}
		v[f.Key] = s
	}
	return p.build(v)
}

// message sets $m to a notification's line, what happened to which
// download, from the payload as the backend renders a body over it.
const message = `{{$t := or .job.title .job.input}}{{with .job.artist_name}}{{$t = printf "%s — %s" $t .}}{{end}}` +
	`{{$m := printf "Downloaded %s · %v tracks" $t .job.total_items}}{{if eq .job.type "song"}}{{$m = printf "Downloaded %s" $t}}{{end}}` +
	`{{if eq .event "job_failed"}}{{if .job.failed_items}}{{$m = printf "Failed %s · %v of %v tracks failed" $t .job.failed_items .job.total_items}}` +
	`{{else}}{{$m = printf "Failed %s" $t}}{{with .job.error}}{{$m = printf "%s: %s" $m .}}{{end}}{{end}}` +
	`{{else if eq .event "job_cancelled"}}{{$m = printf "Cancelled %s" $t}}{{else if eq .event "job_queued"}}{{$m = printf "Queued %s" $t}}{{end}}`

// title is what notifications are titled.
const title = "AMDL"

var (
	finishedOrFailed = []string{"job_finished", "job_failed"}
	finished         = []string{"job_finished"}
)

func server(placeholder, def string) Field {
	return Field{Key: "server", Label: "Server", Placeholder: placeholder, Default: def}
}

// joinURL is base with path after it, base's trailing slashes dropped.
func joinURL(base, path string) string { return strings.TrimRight(base, "/") + path }

func str(s string) *string { return &s }

func headers(h map[string]string) *map[string]string { return &h }

// jsonLiteral is s as a JSON string, to write into a body template; Build
// has kept {{ and }} out of it.
func jsonLiteral(s string) string {
	raw, _ := json.Marshal(s) // a string always marshals
	return string(raw)
}

// All is every preset, in the editor's order.
var All = []Preset{
	{ID: "ntfy", Name: "ntfy", Kind: Notify, Events: finishedOrFailed,
		Fields: []Field{
			server("https://ntfy.sh", "https://ntfy.sh"),
			{Key: "topic", Label: "Topic", Placeholder: "amdl-downloads"},
			{Key: "token", Label: "Access token", Placeholder: "tk_…", Secret: true, Optional: true},
		},
		// ntfy publishes a POST to the topic's URL as a message: the body
		// is its text, Title and Tags its headers.
		build: func(v map[string]string) (client.HookWrite, error) {
			h := map[string]string{"Title": title, "Tags": "musical_note", "Content-Type": "text/plain; charset=utf-8"}
			if v["token"] != "" {
				h["Authorization"] = "Bearer " + v["token"]
			}
			return client.HookWrite{Method: http.MethodPost, URL: joinURL(v["server"], "/"+url.PathEscape(v["topic"])),
				Headers: headers(h), Body: str(message + "{{$m}}")}, nil
		}},
	{ID: "bark", Name: "Bark", Kind: Notify, Events: finishedOrFailed,
		Fields: []Field{
			server("https://api.day.app", "https://api.day.app"),
			{Key: "key", Label: "Device key", Secret: true},
		},
		// Bark's POST /push takes a JSON body with the device's key.
		build: func(v map[string]string) (client.HookWrite, error) {
			body := fmt.Sprintf(`%s{"device_key": %s, "title": %s, "body": {{json $m}}, "group": %s}`,
				message, jsonLiteral(v["key"]), jsonLiteral(title), jsonLiteral(title))
			return client.HookWrite{Method: http.MethodPost, URL: joinURL(v["server"], "/push"), Headers: headers(map[string]string{}), Body: &body}, nil
		}},
	{ID: "telegram", Name: "Telegram", Kind: Notify, Events: finishedOrFailed,
		Fields: []Field{
			{Key: "token", Label: "Bot token", Placeholder: "123456:ABC…", Secret: true},
			{Key: "chat", Label: "Chat ID", Placeholder: "123456789"},
		},
		// The Bot API's sendMessage takes chat_id and text as JSON.
		build: func(v map[string]string) (client.HookWrite, error) {
			if strings.ContainsAny(v["token"], "/ ?#") {
				return client.HookWrite{}, fmt.Errorf("Bot token is the one BotFather gave, as 123456:ABC…")
			}
			body := fmt.Sprintf(`%s{"chat_id": %s, "text": {{json $m}}}`, message, jsonLiteral(v["chat"]))
			return client.HookWrite{Method: http.MethodPost, URL: "https://api.telegram.org/bot" + v["token"] + "/sendMessage",
				Headers: headers(map[string]string{}), Body: &body}, nil
		}},
	{ID: "plex", Name: "Plex", Kind: Library, Events: finished,
		Fields: []Field{
			server("http://192.168.1.10:32400", ""),
			{Key: "token", Label: "Plex token", Secret: true},
		},
		// Plex scans every library on a GET of sections/all/refresh.
		build: func(v map[string]string) (client.HookWrite, error) {
			return client.HookWrite{Method: http.MethodGet, URL: joinURL(v["server"], "/library/sections/all/refresh"),
				Headers: headers(map[string]string{"X-Plex-Token": v["token"]}), Body: str("")}, nil
		}},
	{ID: "jellyfin", Name: "Jellyfin", Kind: Library, Events: finished,
		Fields: []Field{
			server("http://192.168.1.10:8096", ""),
			{Key: "key", Label: "API key", Secret: true},
		},
		// Jellyfin scans on POST /Library/Refresh, its key in the
		// Authorization header's MediaBrowser scheme.
		build: func(v map[string]string) (client.HookWrite, error) {
			return client.HookWrite{Method: http.MethodPost, URL: joinURL(v["server"], "/Library/Refresh"),
				Headers: headers(map[string]string{"Authorization": `MediaBrowser Token="` + v["key"] + `"`}), Body: str("")}, nil
		}},
	{ID: "emby", Name: "Emby", Kind: Library, Events: finished,
		Fields: []Field{
			server("http://192.168.1.10:8096", ""),
			{Key: "key", Label: "API key", Secret: true},
		},
		// Emby scans on POST /emby/Library/Refresh with X-Emby-Token.
		build: func(v map[string]string) (client.HookWrite, error) {
			return client.HookWrite{Method: http.MethodPost, URL: joinURL(v["server"], "/emby/Library/Refresh"),
				Headers: headers(map[string]string{"X-Emby-Token": v["key"]}), Body: str("")}, nil
		}},
	{ID: "custom", Name: "Custom", Kind: Custom, Events: finished,
		Fields: []Field{
			{Key: "url", Label: "Address", Placeholder: "https://example.com/hook"},
			{Key: "method", Label: "Method", Choices: []string{http.MethodPost, http.MethodPut, http.MethodGet}, Default: http.MethodPost},
			{Key: "header", Label: "Header", Placeholder: "Authorization: Bearer …", Secret: true, Optional: true},
			{Key: "body", Label: "Body", Placeholder: "The JSON payload", Optional: true, Multiline: true},
		},
		build: func(v map[string]string) (client.HookWrite, error) {
			h := map[string]string{}
			if line := v["header"]; line != "" {
				name, value, ok := strings.Cut(line, ":")
				if !ok || strings.TrimSpace(name) == "" {
					return client.HookWrite{}, fmt.Errorf("Header is Name: value")
				}
				h[strings.TrimSpace(name)] = strings.TrimSpace(value)
			}
			body := v["body"]
			if v["method"] == http.MethodGet {
				body = ""
			}
			return client.HookWrite{Method: v["method"], URL: v["url"], Headers: headers(h), Body: &body}, nil
		}},
}

// Find is the preset with id.
func Find(id string) (Preset, bool) {
	i := slices.IndexFunc(All, func(p Preset) bool { return p.ID == id })
	if i < 0 {
		return Preset{}, false
	}
	return All[i], true
}

// Recognize is the preset a webhook looks made by, from what the backend
// shows of it, its origin and header names; Custom when none fits.
func Recognize(e client.HookEntry) Preset {
	host := ""
	if u, err := url.Parse(e.Origin); err == nil {
		host = u.Hostname()
	}
	has := func(name string) bool { return slices.Contains(e.HeaderNames, name) }
	id := "custom"
	switch {
	case has("X-Plex-Token"):
		id = "plex"
	case has("X-Emby-Token"):
		id = "emby"
	case host == "api.telegram.org":
		id = "telegram"
	case host == "api.day.app":
		id = "bark"
	case host == "ntfy.sh" || (has("Title") && has("Tags")):
		id = "ntfy"
	case len(e.HeaderNames) == 1 && has("Authorization") && e.Method == http.MethodPost:
		id = "jellyfin"
	}
	p, _ := Find(id)
	return p
}
