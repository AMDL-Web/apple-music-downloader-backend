// Package hookpreset is the services a webhook hook can be made for, which
// GET /api/v1/hooks/presets lists and PUT /api/v1/hooks/{name} builds from:
// what each asks for, and the request that calls it. Each request is the one
// the service documents; the backend sends it as built, its body a template
// over the hook payload (internal/hooks renders it).
package hookpreset

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Kind groups the presets in an editor.
type Kind string

const (
	Notify  Kind = "notify"
	Library Kind = "library"
	Custom  Kind = "custom"
)

// Field is one value a preset asks for. A field with Choices is picked from
// them; Multiline takes a body; Secret is shown masked.
type Field struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Placeholder string   `json:"placeholder,omitempty"`
	Default     string   `json:"default,omitempty"`
	Secret      bool     `json:"secret,omitempty"`
	Optional    bool     `json:"optional,omitempty"`
	Multiline   bool     `json:"multiline,omitempty"`
	Choices     []string `json:"choices,omitempty"`
}

// Preset is one service.
type Preset struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Events are the events a new hook of the preset runs on.
	Events []string `json:"events"`
	Fields []Field  `json:"fields"`
	build  func(v map[string]string) (Request, error)
}

// Request is the webhook request a preset builds; Body empty sends the JSON
// payload, or nothing for a GET.
type Request struct {
	Method, URL string
	Headers     map[string]string
	Body        string
}

// Build is the request that calls the service with values, keyed by field.
// A required field left empty, a key the preset does not ask for and
// template braces in a value are errors, worded for the person filling the
// fields in.
func (p Preset) Build(values map[string]string) (Request, error) {
	for k := range values {
		if !slices.ContainsFunc(p.Fields, func(f Field) bool { return f.Key == k }) {
			return Request{}, fmt.Errorf("%s has no field %q", p.Name, k)
		}
	}
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
			return Request{}, fmt.Errorf("%s is needed", f.Label)
		}
		if len(f.Choices) > 0 && s != "" && !slices.Contains(f.Choices, s) {
			return Request{}, fmt.Errorf("%s is one of %s", f.Label, strings.Join(f.Choices, ", "))
		}
		if !f.Multiline && (strings.Contains(s, "{{") || strings.Contains(s, "}}")) {
			return Request{}, fmt.Errorf("%s cannot hold {{ or }}", f.Label)
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
		build: func(v map[string]string) (Request, error) {
			h := map[string]string{"Title": title, "Tags": "musical_note", "Content-Type": "text/plain; charset=utf-8"}
			if v["token"] != "" {
				h["Authorization"] = "Bearer " + v["token"]
			}
			return Request{Method: http.MethodPost, URL: joinURL(v["server"], "/"+url.PathEscape(v["topic"])),
				Headers: h, Body: message + "{{$m}}"}, nil
		}},
	{ID: "bark", Name: "Bark", Kind: Notify, Events: finishedOrFailed,
		Fields: []Field{
			server("https://api.day.app", "https://api.day.app"),
			{Key: "key", Label: "Device key", Secret: true},
		},
		// Bark's POST /push takes a JSON body with the device's key.
		build: func(v map[string]string) (Request, error) {
			body := fmt.Sprintf(`%s{"device_key": %s, "title": %s, "body": {{json $m}}, "group": %s}`,
				message, jsonLiteral(v["key"]), jsonLiteral(title), jsonLiteral(title))
			return Request{Method: http.MethodPost, URL: joinURL(v["server"], "/push"), Headers: map[string]string{}, Body: body}, nil
		}},
	{ID: "telegram", Name: "Telegram", Kind: Notify, Events: finishedOrFailed,
		Fields: []Field{
			{Key: "token", Label: "Bot token", Placeholder: "123456:ABC…", Secret: true},
			{Key: "chat", Label: "Chat ID", Placeholder: "123456789"},
		},
		// The Bot API's sendMessage takes chat_id and text as JSON.
		build: func(v map[string]string) (Request, error) {
			if strings.ContainsAny(v["token"], "/ ?#") {
				return Request{}, fmt.Errorf("Bot token is the one BotFather gave, as 123456:ABC…")
			}
			body := fmt.Sprintf(`%s{"chat_id": %s, "text": {{json $m}}}`, message, jsonLiteral(v["chat"]))
			return Request{Method: http.MethodPost, URL: "https://api.telegram.org/bot" + v["token"] + "/sendMessage",
				Headers: map[string]string{}, Body: body}, nil
		}},
	{ID: "plex", Name: "Plex", Kind: Library, Events: finished,
		Fields: []Field{
			server("http://192.168.1.10:32400", ""),
			{Key: "token", Label: "Plex token", Secret: true},
		},
		// Plex scans every library on a GET of sections/all/refresh.
		build: func(v map[string]string) (Request, error) {
			return Request{Method: http.MethodGet, URL: joinURL(v["server"], "/library/sections/all/refresh"),
				Headers: map[string]string{"X-Plex-Token": v["token"]}}, nil
		}},
	{ID: "jellyfin", Name: "Jellyfin", Kind: Library, Events: finished,
		Fields: []Field{
			server("http://192.168.1.10:8096", ""),
			{Key: "key", Label: "API key", Secret: true},
		},
		// Jellyfin scans on POST /Library/Refresh, its key in the
		// Authorization header's MediaBrowser scheme.
		build: func(v map[string]string) (Request, error) {
			return Request{Method: http.MethodPost, URL: joinURL(v["server"], "/Library/Refresh"),
				Headers: map[string]string{"Authorization": `MediaBrowser Token="` + v["key"] + `"`}}, nil
		}},
	{ID: "emby", Name: "Emby", Kind: Library, Events: finished,
		Fields: []Field{
			server("http://192.168.1.10:8096", ""),
			{Key: "key", Label: "API key", Secret: true},
		},
		// Emby scans on POST /emby/Library/Refresh with X-Emby-Token.
		build: func(v map[string]string) (Request, error) {
			return Request{Method: http.MethodPost, URL: joinURL(v["server"], "/emby/Library/Refresh"),
				Headers: map[string]string{"X-Emby-Token": v["key"]}}, nil
		}},
	{ID: "custom", Name: "Custom", Kind: Custom, Events: finished,
		Fields: []Field{
			{Key: "url", Label: "Address", Placeholder: "https://example.com/hook"},
			{Key: "method", Label: "Method", Choices: []string{http.MethodPost, http.MethodPut, http.MethodGet}, Default: http.MethodPost},
			{Key: "header", Label: "Header", Placeholder: "Authorization: Bearer …", Secret: true, Optional: true},
			{Key: "body", Label: "Body", Placeholder: "The JSON payload", Optional: true, Multiline: true},
		},
		build: func(v map[string]string) (Request, error) {
			h := map[string]string{}
			if line := v["header"]; line != "" {
				name, value, ok := strings.Cut(line, ":")
				if !ok || strings.TrimSpace(name) == "" {
					return Request{}, fmt.Errorf("Header is Name: value")
				}
				h[strings.TrimSpace(name)] = strings.TrimSpace(value)
			}
			body := v["body"]
			if v["method"] == http.MethodGet {
				body = ""
			}
			return Request{Method: v["method"], URL: v["url"], Headers: h, Body: body}, nil
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
