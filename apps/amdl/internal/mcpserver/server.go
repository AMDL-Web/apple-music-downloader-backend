// Package mcpserver serves the amdl tools to an MCP client: one tool per
// capability of internal/capability that names one, its input schema taken
// from internal/api/openapi.yaml, each call sent to an amdl server through
// internal/client. amdl mcp runs it over stdio.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"amdl/apps/amdl/internal/jobview"
	"amdl/internal/capability"
	"amdl/internal/client"
	"amdl/internal/domain"
	"amdl/internal/progress"
)

// Rules are the sentences the instructions share, word for word, with the
// agent skill (apps/amdl/skill), which a test checks.
var rules = []string{
	"Find music with search_catalog (or list_artist_albums for an artist hit), pass the hits' url values to start_download, then follow each job_id with wait_for_job, calling it again while it answers done=false.",
	"Signing in to Apple is done by a person, never by an agent: do not ask for, type, read or store an Apple ID password or two-step code.",
	"Do not route around a refusal.",
}

// instructions tell a model how the tools fit together: a short form of the
// skill.
var instructions = "amdl downloads music from Apple Music through an amdl server.\n" + rules[0] + "\n" +
	"lookup_link previews a pasted link; get_album lists an album's songs. get_status says whether the wrapper is signed in. " + rules[1] + "\n" +
	"A tool error carries a code and often a hint. " + rules[2]

// New returns an MCP server whose tools call the amdl server behind c.
func New(c *client.Client, version string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "amdl", Title: "AMDL", Version: version},
		&mcp.ServerOptions{Instructions: instructions})
	h := handlers{c: c}
	for _, cap := range capability.All {
		if cap.Tool == "" {
			continue
		}
		t := tool(cap)
		run, ok := h.custom()[cap.Tool]
		if !ok {
			run = h.passthrough(cap)
		}
		s.AddTool(t, wrap(t.InputSchema.(map[string]any), run))
	}
	return s
}

// Run serves the tools over in and out, the process's stdin and stdout, until
// ctx ends or the client leaves.
func Run(ctx context.Context, c *client.Client, version string, in io.Reader, out io.Writer) error {
	return New(c, version).Run(ctx, &mcp.IOTransport{Reader: io.NopCloser(in), Writer: nopWriteCloser{out}})
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

type runner func(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (any, error)

// wrap decodes the arguments, checks them against the tool's schema and turns
// the result or error into a tool result: the JSON as text, once. The tools
// declare no output schema, so structured content would only repeat it.
func wrap(schema map[string]any, run runner) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := map[string]any{}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return errorResult("invalid_request", "arguments are not a JSON object: "+err.Error(), nil), nil
			}
		}
		if err := checkArgs(schema, args); err != nil {
			return errorResult("invalid_request", err.Error(), nil), nil
		}
		out, err := run(ctx, req, args)
		if err != nil {
			var api *client.APIError
			if errors.As(err, &api) {
				return errorResult(api.Code, api.Message, api.Details), nil
			}
			var ae argError
			if errors.As(err, &ae) {
				return errorResult("invalid_request", ae.msg, nil), nil
			}
			return unreachable(err), nil
		}
		raw, err := toJSON(out)
		if err != nil {
			return nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil
	}
}

// checkArgs rejects an argument the tool does not declare and one of the wrong
// JSON type, naming the field. A null counts as absent.
func checkArgs(schema map[string]any, args map[string]any) error {
	props, _ := schema["properties"].(map[string]any)
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, ok := props[name]; !ok {
			return fmt.Errorf("unknown argument %q; %s", name, takes(props))
		}
	}
	for _, name := range names {
		if args[name] == nil {
			delete(args, name)
			continue
		}
		prop, _ := props[name].(map[string]any)
		if want := wantType(prop, args[name]); want != "" {
			return fmt.Errorf("%s must be %s", name, want)
		}
	}
	return nil
}

func takes(props map[string]any) string {
	if len(props) == 0 {
		return "this tool takes no arguments"
	}
	names := make([]string, 0, len(props))
	for name := range props {
		names = append(names, name)
	}
	sort.Strings(names)
	return "this tool takes " + strings.Join(names, ", ")
}

// wantType names the JSON type a property asks for when v is not one, and is
// empty when v fits or the schema states no plain type.
func wantType(prop map[string]any, v any) string {
	t, _ := prop["type"].(string)
	switch t {
	case "string":
		if _, ok := v.(string); !ok {
			return "a string"
		}
	case "integer":
		if n, ok := v.(float64); !ok || n != math.Trunc(n) {
			return "a whole number"
		}
	case "number":
		if _, ok := v.(float64); !ok {
			return "a number"
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return "true or false"
		}
	case "object":
		if _, ok := v.(map[string]any); !ok {
			return "an object"
		}
	case "array":
		items, _ := prop["items"].(map[string]any)
		want := "an array"
		if items["type"] == "string" {
			want = "an array of strings"
		}
		list, ok := v.([]any)
		if !ok {
			return want
		}
		if items["type"] == "string" {
			for _, x := range list {
				if _, ok := x.(string); !ok {
					return want
				}
			}
		}
	}
	return ""
}

func errorResult(code, message string, details json.RawMessage) *mcp.CallToolResult {
	body := map[string]any{"code": code, "message": message}
	if len(details) > 0 {
		body["details"] = details
	}
	raw, _ := json.Marshal(map[string]any{"error": body}) // strings and raw JSON
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
}

// unreachableHint is what an agent can do when the server does not answer.
const unreachableHint = "start the amdl backend (amdl service start, or amdl serve), or give amdl mcp --server <url> in this MCP client's config"

// unreachable is the tool error for a server that did not answer: every tool
// says it the same way, naming the address rather than the request.
func unreachable(err error) *mcp.CallToolResult {
	msg := err.Error()
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if u, perr := url.Parse(urlErr.URL); perr == nil {
			msg = "the amdl server does not answer at " + u.Scheme + "://" + u.Host
		}
	}
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{ // strings only
		"code": "server_unreachable", "message": msg, "hint": unreachableHint}})
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
}

type argError struct{ msg string }

func (e argError) Error() string { return e.msg }

func toJSON(v any) (json.RawMessage, error) {
	if raw, ok := v.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(v)
}

// tool describes a capability as an MCP tool.
func tool(c capability.Capability) *mcp.Tool {
	t := &mcp.Tool{Name: c.Tool, Description: or(c.ToolSummary, c.Summary) + ".", InputSchema: inputSchema(c)}
	ann := &mcp.ToolAnnotations{ReadOnlyHint: c.Safety == capability.Read, OpenWorldHint: ptr(openWorld[c.Tool])}
	if c.Safety != capability.Read {
		ann.DestructiveHint = ptr(c.Safety == capability.Destructive || alsoDestructive[c.Tool])
		ann.IdempotentHint = idempotent[c.Tool]
	}
	t.Annotations = ann
	return t
}

// openWorld are the tools that read Apple's catalog rather than only the amdl
// server's own state.
var openWorld = map[string]bool{"search_catalog": true, "list_artist_albums": true, "get_album": true, "lookup_link": true, "probe_quality": true}

// idempotent are the changing tools that a repeat leaves as the first call did.
var idempotent = map[string]bool{"start_download": true, "cancel_job": true, "delete_job": true}

// alsoDestructive are changing tools marked destructive although their CLI
// command needs no --yes: set_config replaces settings and cancel_job throws
// away a job's unfinished work, so a client should confirm them.
var alsoDestructive = map[string]bool{"set_config": true, "cancel_job": true}

func ptr[T any](v T) *T { return &v }

// inputSchema is the JSON Schema of a tool's arguments: the custom ones'
// declared below, the rest the operation's parameters and body fields.
func inputSchema(c capability.Capability) map[string]any {
	if s, ok := customSchemas[c.Tool]; ok {
		return s
	}
	op := capability.Lookup(c.Ops[0])
	props := map[string]any{}
	var required []string
	describe := func(name string, schema map[string]any, desc string) map[string]any {
		if d, ok := paramDescriptions[c.Tool+"."+name]; ok {
			desc = d
		} else if d, ok := paramDescriptions[name]; ok && desc == "" {
			desc = d
		}
		return withDescription(schema, desc)
	}
	for _, p := range op.Params {
		props[p.Name] = describe(p.Name, p.Schema, p.Description)
		if p.Required {
			required = append(required, p.Name)
		}
	}
	for _, f := range op.BodyFields() {
		props[f.Name] = describe(f.Name, f.Schema, f.Description)
		if f.Required {
			required = append(required, f.Name)
		}
	}
	if paletteTools[c.Tool] {
		props[paletteArg] = withDescription(toggle, "Keep the artwork color fields (artwork_bg_color, artwork_text_color1 to 4), which are left out by default.")
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// paramDescriptions word the parameters whose spec description is missing or
// written for an HTTP client: by "tool.param", else by parameter name where the
// spec has none.
var paramDescriptions = map[string]string{
	"job_id":               "The job's id, job_<32 hex>.",
	"url":                  "One Apple Music link, such as the url of a search hit.",
	"search_catalog.types": "The groups to search: any of songs, albums, playlists, artists; default all four.",
	"get_logs.cursor":      "Only entries after this sequence number: the next_cursor of the previous page.",
	"get_logs.limit":       "Maximum number of entries to return; default 200.",
}

// paletteTools are the tools whose answer carries the artwork color palette of
// each hit, which a model has no use for; paletteArg brings it back.
var paletteTools = map[string]bool{"search_catalog": true, "list_artist_albums": true, "get_album": true, "lookup_link": true}

const paletteArg = "artwork_colors"

func or(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func withDescription(schema map[string]any, desc string) map[string]any {
	out := make(map[string]any, len(schema)+1)
	for k, v := range schema {
		out[k] = v
	}
	if desc != "" {
		out["description"] = strings.Join(strings.Fields(desc), " ")
	}
	return out
}

func obj(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

var (
	str    = map[string]any{"type": "string"}
	strs   = map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	jobID  = map[string]any{"type": "string", "description": "The job's id, job_<32 hex>."}
	toggle = map[string]any{"type": "boolean"}
)

var customSchemas = map[string]map[string]any{
	"start_download": obj(map[string]any{
		"urls":            withDescription(strs, "Apple Music links: songs, albums, playlists, stations or artists, such as the url of a search hit."),
		"codec_priority":  withDescription(strs, "Codec order for this download, such as [\"alac\",\"aac\"]; omit for the server's setting."),
		"codec_fallback":  withDescription(toggle, "Fall back to the next codec when one is unavailable; omit for the server's setting."),
		"max_sample_rate": map[string]any{"type": "integer", "description": "Highest ALAC sample rate in Hz."},
		"max_bit_depth":   map[string]any{"type": "integer", "description": "Highest ALAC bit depth."},
		"overwrite":       withDescription(toggle, "Replace files already on disk instead of skipping them. Destructive; only when the user asks for it."),
	}, "urls"),
	"list_jobs": obj(map[string]any{
		"status": withDescription(strs, "Job statuses to keep: queued, running, completed, failed, cancelled."),
		"type":   withDescription(strs, "Job types to keep: song, album, playlist, artist, station."),
		"q":      withDescription(str, "Text to find in the title, artist name or link."),
		"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 200, "description": "Jobs per page, default 50."},
		"offset": map[string]any{"type": "integer", "minimum": 0, "description": "Jobs to skip, default 0; pass offset plus the rows returned to read the next page."},
	}),
	"get_job": obj(map[string]any{
		"job_id": jobID,
		"detail": map[string]any{"type": "string", "enum": []string{"summary", "full"},
			"description": "summary (default): the job, its album progress and one short entry per track; full: every field the API has."},
	}, "job_id"),
	"wait_for_job": obj(map[string]any{
		"job_id":    jobID,
		"timeout_s": map[string]any{"type": "integer", "minimum": 1, "maximum": maxWait, "description": "How long to wait, default 120 seconds. On done=false call again. A finished job answers done=true at once, with hooks_running=true while its post-download hooks still run."},
	}, "job_id"),
	"get_status": obj(map[string]any{}),
	"get_config": obj(map[string]any{
		"keys": withDescription(strs, "Dotted keys such as download.quality_priority; omit for the whole runtime config."),
	}),
	"set_config": obj(map[string]any{
		"changes": map[string]any{"type": "object", "description": "Dotted runtime config keys and their new values, such as {\"logging.level\": \"debug\"}."},
	}, "changes"),
}

// progressEvery is the least time between two progress notifications.
var progressEvery = time.Second

// maxWait bounds one wait_for_job call, so a client that drops the request
// stream loses at most this much and simply calls again.
const maxWait = 300

type handlers struct{ c *client.Client }

func (h handlers) custom() map[string]runner {
	return map[string]runner{
		"start_download": h.startDownload,
		"list_jobs":      h.listJobs,
		"get_job":        h.getJob,
		"wait_for_job":   h.waitForJob,
		"get_status":     h.getStatus,
		"get_config":     h.getConfig,
		"set_config":     h.setConfig,
	}
}

// passthrough sends the operation built from the arguments and returns the
// answer as is.
func (h handlers) passthrough(c capability.Capability) runner {
	op := capability.Lookup(c.Ops[0])
	return func(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (any, error) {
		keepPalette := false
		if paletteTools[c.Tool] {
			keepPalette, _ = args[paletteArg].(bool)
			delete(args, paletteArg)
		}
		values, err := typed(op, args)
		if err != nil {
			return nil, err
		}
		req, err := op.Build(values)
		if err != nil {
			return nil, argError{err.Error()}
		}
		var body any
		if req.Body != nil {
			body = req.Body
		}
		raw, err := h.c.Call(ctx, req.Method, req.Path, req.Query, body)
		if err != nil {
			return raw, err
		}
		if len(raw) == 0 {
			return map[string]any{"ok": true}, nil
		}
		if paletteTools[c.Tool] && !keepPalette {
			raw = jobview.DropPalette(raw)
		}
		return raw, nil
	}
}

// typed converts JSON arguments to what capability.Operation.Build takes:
// whole numbers for integer parameters and string lists for arrays.
func typed(op capability.Operation, args map[string]any) (map[string]any, error) {
	types := map[string]string{}
	for _, p := range op.Params {
		types[p.Name] = p.Type()
	}
	for _, f := range op.BodyFields() {
		t, _ := f.Schema["type"].(string)
		types[f.Name] = t
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		switch types[k] {
		case "integer":
			n, ok := v.(float64)
			if !ok || n != float64(int64(n)) {
				return nil, argError{k + " must be a whole number"}
			}
			out[k] = int(n)
		case "array":
			list, ok := v.([]any)
			if !ok {
				return nil, argError{k + " must be an array"}
			}
			var ss []string
			for _, x := range list {
				ss = append(ss, fmt.Sprint(x))
			}
			out[k] = ss
		default:
			out[k] = v
		}
	}
	return out, nil
}

func stringArg(args map[string]any, name string) (string, error) {
	s, ok := args[name].(string)
	if !ok || s == "" {
		return "", argError{name + " is required"}
	}
	return s, nil
}

func stringsArg(args map[string]any, name string) []string {
	list, _ := args[name].([]any)
	var out []string
	for _, x := range list {
		if s, ok := x.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func intArg(args map[string]any, name string, def int) int {
	if n, ok := args[name].(float64); ok {
		return int(n)
	}
	return def
}

func (h handlers) startDownload(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (any, error) {
	urls := stringsArg(args, "urls")
	if len(urls) == 0 {
		return nil, argError{"urls is required"}
	}
	overrides := map[string]any{}
	if v := stringsArg(args, "codec_priority"); len(v) > 0 {
		overrides["quality_priority"] = v
	}
	if v, ok := args["codec_fallback"].(bool); ok {
		overrides["codec_alternative"] = v
	}
	if v := intArg(args, "max_sample_rate", 0); v > 0 {
		overrides["alac_max_sample_rate"] = v
	}
	if v := intArg(args, "max_bit_depth", 0); v > 0 {
		overrides["alac_max_bit_depth"] = v
	}
	if v, _ := args["overwrite"].(bool); v {
		overrides["force_overwrite"] = true
	}
	body := map[string]any{"urls": urls}
	if len(overrides) > 0 {
		body["overrides"] = overrides
	}
	raw, err := h.c.Call(ctx, http.MethodPost, "/api/v1/downloads", nil, body)
	var batch domain.BatchSubmitResponse
	var refused *client.APIError
	if err != nil {
		if !errors.As(err, &refused) || refused.Code != "no_urls_accepted" || json.Unmarshal(refused.Details, &batch) != nil {
			return nil, err
		}
	} else if err := json.Unmarshal(raw, &batch); err != nil {
		return nil, err
	}
	type accepted struct {
		URL   string `json:"url"`
		JobID string `json:"job_id"`
	}
	type rejected struct {
		URL           string `json:"url"`
		Status        string `json:"status"`
		Code          string `json:"code,omitempty"`
		Message       string `json:"message,omitempty"`
		ExistingJobID string `json:"existing_job_id,omitempty"`
	}
	out := struct {
		Accepted []accepted `json:"accepted"`
		Rejected []rejected `json:"rejected"`
	}{Accepted: []accepted{}, Rejected: []rejected{}}
	for _, r := range batch.Results {
		if r.Job != nil {
			out.Accepted = append(out.Accepted, accepted{r.URL, r.Job.ID})
			continue
		}
		rj := rejected{URL: r.URL, Status: string(r.Status), ExistingJobID: r.ExistingJobID}
		if r.Error != nil {
			rj.Code, rj.Message = r.Error.Code, r.Error.Message
		}
		out.Rejected = append(out.Rejected, rj)
	}
	if refused != nil && !batch.AllDuplicates() {
		// Nothing was queued and not because the links are already running:
		// an error, as in the CLI, with the reasons per link.
		details, _ := json.Marshal(map[string]any{"rejected": out.Rejected}) // strings only
		return nil, &client.APIError{Status: refused.Status, Code: refused.Code, Message: "no URL was accepted; see details.rejected", Details: details}
	}
	return out, nil
}

func (h handlers) listJobs(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (any, error) {
	q := client.ListQuery{Limit: intArg(args, "limit", 50), Offset: intArg(args, "offset", 0),
		Types: stringsArg(args, "type")}
	for _, s := range stringsArg(args, "status") {
		q.Status = append(q.Status, domain.JobStatus(s))
	}
	q.Search, _ = args["q"].(string)
	list, err := h.c.Downloads(ctx, q)
	if err != nil {
		return nil, err
	}
	rows := make([]map[string]any, 0, len(list.Downloads))
	for _, j := range list.Downloads {
		rows = append(rows, jobview.Row(j))
	}
	return map[string]any{"jobs": rows, "total": list.Total, "offset": list.Offset, "status_counts": list.StatusCounts}, nil
}

func (h handlers) getJob(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (any, error) {
	id, err := stringArg(args, "job_id")
	if err != nil {
		return nil, err
	}
	d, err := h.c.Download(ctx, id)
	if err != nil {
		return nil, err
	}
	if args["detail"] == "full" {
		return d, nil
	}
	return jobview.Summary(d), nil
}

// waitForJob follows a job until it finishes or the timeout passes, sending a
// progress notification at most once a second when the client asked for them.
func (h handlers) waitForJob(ctx context.Context, req *mcp.CallToolRequest, args map[string]any) (any, error) {
	id, err := stringArg(args, "job_id")
	if err != nil {
		return nil, err
	}
	timeout := intArg(args, "timeout_s", 120)
	if v, ok := args["timeout_s"].(float64); (!ok && args["timeout_s"] != nil) || v != float64(int64(v)) {
		return nil, argError{"timeout_s must be a whole number"}
	}
	if timeout < 1 || timeout > maxWait {
		return nil, argError{fmt.Sprintf("timeout_s must be between 1 and %d", maxWait)}
	}
	wctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	token := req.Params.GetProgressToken()
	var d client.DownloadDetail
	var index map[string]int
	var last time.Time
	lastPercent := -1
	notify := func() {
		if token == nil || req.Session == nil {
			return
		}
		a := progress.AlbumProgress(d.Job.Status, d.Items)
		if a.Percent == lastPercent || time.Since(last) < progressEvery {
			return
		}
		last, lastPercent = time.Now(), a.Percent
		msg := fmt.Sprintf("%d/%d tracks", a.Lit, a.Total)
		if d.Job.Title != "" {
			msg = d.Job.Title + " · " + msg
		}
		_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ // a lost notification is only a missed update
			ProgressToken: token, Progress: float64(a.Percent), Total: 100, Message: msg})
	}
	err = h.c.FollowJob(wctx, id, client.JobHandlers{
		StopAtTerminal: true,
		Detail:         func(dd client.DownloadDetail) { d, index = dd, jobview.Index(dd); notify() },
		Event:          func(ev domain.Event) { jobview.Apply(&d, index, ev); notify() },
	})
	switch {
	case errors.Is(err, client.ErrJobGone):
		return nil, &client.APIError{Status: http.StatusNotFound, Code: "job_not_found", Message: "job " + id + " no longer exists"}
	case err != nil && wctx.Err() != nil && ctx.Err() == nil:
		// The timeout passed: report where the job is, which may be the end.
		if fresh, ferr := h.c.Download(ctx, id); ferr == nil {
			d = fresh
		}
	case err != nil:
		return nil, err
	}
	if !client.Terminal(d.Job.Status) && err == nil {
		if d, err = h.c.Download(ctx, id); err != nil {
			return nil, err
		}
	}
	out := map[string]any{"done": client.Terminal(d.Job.Status), "job": jobview.Summary(d)}
	if out["done"] == true && d.HooksRunning() {
		out["hooks_running"] = true
	}
	return out, nil
}

// getStatus gathers what an agent checks before downloading: the server, its
// health, the wrapper and the library watcher. A section that fails carries
// its error instead.
func (h handlers) getStatus(ctx context.Context, _ *mcp.CallToolRequest, _ map[string]any) (any, error) {
	out := map[string]any{}
	section := func(name, path string) {
		raw, err := h.c.Call(ctx, http.MethodGet, path, nil, nil)
		if err != nil {
			var api *client.APIError
			if errors.As(err, &api) && len(raw) == 0 {
				out[name] = map[string]any{"error": map[string]any{"code": api.Code, "message": api.Message}}
				return
			}
			out[name] = map[string]any{"error": map[string]any{"code": "server_unreachable", "message": err.Error()}}
			return
		}
		out[name] = raw
	}
	section("system", "/api/v1/system")
	if _, ok := out["system"].(json.RawMessage); !ok {
		return nil, errors.New("the amdl server does not answer at " + h.c.Base())
	}
	h2, err := h.c.Health(ctx)
	if err == nil {
		out["health"] = h2
	}
	section("wrapper", "/api/v1/wrapper/status")
	section("library_sync", "/api/v1/library-sync")
	return out, nil
}

func (h handlers) getConfig(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (any, error) {
	raw, err := h.c.Call(ctx, http.MethodGet, "/api/v1/config", nil, nil)
	if err != nil {
		return nil, err
	}
	keys := stringsArg(args, "keys")
	if len(keys) == 0 {
		return raw, nil
	}
	var resp struct {
		Config map[string]any `json:"config"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	out := map[string]any{}
	for _, k := range keys {
		v, ok := dig(resp.Config, k)
		if !ok {
			return nil, &client.APIError{Status: http.StatusNotFound, Code: "config_key_not_found", Message: k + " is not a runtime config key"}
		}
		out[k] = v
	}
	return out, nil
}

func dig(m map[string]any, key string) (any, bool) {
	var cur any = m
	for _, part := range strings.Split(key, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = obj[part]; !ok {
			return nil, false
		}
	}
	return cur, true
}

func (h handlers) setConfig(ctx context.Context, _ *mcp.CallToolRequest, args map[string]any) (any, error) {
	changes, ok := args["changes"].(map[string]any)
	if !ok || len(changes) == 0 {
		return nil, argError{"changes is required: an object of dotted keys and values"}
	}
	patch := map[string]any{}
	for key, v := range changes {
		parts := strings.Split(key, ".")
		node := patch
		for _, p := range parts[:len(parts)-1] {
			next, ok := node[p].(map[string]any)
			if !ok {
				next = map[string]any{}
				node[p] = next
			}
			node = next
		}
		node[parts[len(parts)-1]] = v
	}
	raw, err := h.c.Call(ctx, http.MethodPut, "/api/v1/config", nil, patch)
	if err != nil {
		return nil, err
	}
	// The answer is the whole runtime config; echo only the keys set, as the
	// CLI does.
	var resp struct {
		Config    map[string]any `json:"config"`
		Persisted bool           `json:"persisted"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	set := map[string]any{}
	for key := range changes {
		if v, ok := dig(resp.Config, key); ok {
			set[key] = v
		}
	}
	return map[string]any{"config": set, "persisted": resp.Persisted}, nil
}
