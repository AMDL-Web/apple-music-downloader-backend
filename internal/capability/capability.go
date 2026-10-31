// Package capability is the one table of what the amdl clients can do: each
// entry names the API operations it calls (internal/api/openapi.yaml), its CLI
// command and its MCP tool, with a safety level and a summary. The CLI builds
// its commands and the MCP server its tools from this table and the embedded
// spec, so neither can drift from the API: TestEveryOperationIsExposed fails
// when an operation has no entry.
package capability

import "strings"

// Safety is how much a capability changes.
type Safety string

// Safety levels. A destructive capability asks for confirmation in a terminal
// and needs --yes elsewhere; the MCP server marks its tool destructive.
const (
	Read        Safety = "read"
	Write       Safety = "write"
	Destructive Safety = "destructive"
)

// Capability is one thing a client can do.
type Capability struct {
	// Command is the CLI command path, such as "jobs list"; empty when the
	// capability has no command.
	Command string `json:"command,omitempty"`
	// Tool is the MCP tool name; empty when the capability has no tool.
	Tool string `json:"tool,omitempty"`
	// Ops are the operationIds it calls; the first is the one its parameters
	// and output come from. Empty for a capability that runs locally.
	Ops    []string `json:"operations,omitempty"`
	Safety Safety   `json:"safety"`
	// Args are the parameters or body fields the command takes positionally,
	// in order; the rest are flags.
	Args    []string `json:"args,omitempty"`
	Summary string   `json:"summary"`
	// ToolSummary is the MCP tool's description when Summary, worded for the
	// command line, would mislead there: it names the shape the tool returns.
	ToolSummary string `json:"tool_summary,omitempty"`
	// Examples are full command lines.
	Examples []string `json:"examples,omitempty"`
}

// ID returns the capability's identifier: its command with dots, such as
// "jobs.list", or its tool name when it has no command.
func (c Capability) ID() string {
	if c.Command == "" {
		return c.Tool
	}
	return strings.ReplaceAll(c.Command, " ", ".")
}

// All is every capability, in the order help lists them.
var All = []Capability{
	{Command: "search", Tool: "search_catalog", Ops: []string{"searchCatalog"}, Safety: Read, Args: []string{"term"},
		Summary:  "Search the Apple Music catalog for songs, albums, playlists and artists",
		Examples: []string{`amdl search "igor tyler" --types albums --limit 3`}},
	{Command: "artists albums", Tool: "list_artist_albums", Ops: []string{"listArtistAlbums"}, Safety: Read, Args: []string{"artist_id"},
		Summary:  "List an artist's albums, one page at a time",
		Examples: []string{"amdl artists albums 420368335 --limit 50"}},
	{Command: "albums get", Tool: "get_album", Ops: []string{"getAlbum"}, Safety: Read, Args: []string{"album_id"},
		Summary:  "Show an album's metadata and its songs in disc and track order, each with a link that downloads it alone",
		Examples: []string{"amdl albums get 1783644603 --storefront cn"}},
	{Command: "lookup", Tool: "lookup_link", Ops: []string{"lookupLink"}, Safety: Read, Args: []string{"url"},
		Summary:  "Preview an Apple Music link (title, artist, cover, track count) without downloading",
		Examples: []string{"amdl lookup https://music.apple.com/us/album/igor/1463409338"}},
	{Command: "quality", Tool: "probe_quality", Ops: []string{"queryQuality"}, Safety: Read, Args: []string{"url"},
		Summary:  "Show the codecs and audio quality each track of a link can be downloaded in",
		Examples: []string{"amdl quality https://music.apple.com/us/album/igor/1463409338"}},
	{Command: "download", Tool: "start_download", Ops: []string{"createDownload", "streamDownloadEvents"}, Safety: Write, Args: []string{"urls"},
		Summary:     "Download songs, albums, playlists, stations or artists by link; --wait blocks until done",
		ToolSummary: "Queue songs, albums, playlists, stations or artists by link and answer at once with {accepted: [{url, job_id}], rejected: [{url, status, code, message, existing_job_id}]}; follow each job_id with wait_for_job",
		Examples: []string{
			"amdl download https://music.apple.com/us/album/igor/1463409338 --wait --timeout 20m",
			"amdl search igor --types albums | jq -r '.albums.items[].url' | amdl download -",
		}},
	{Command: "jobs list", Tool: "list_jobs", Ops: []string{"listDownloads"}, Safety: Read,
		Summary:     "List download jobs, filtered by status, type or text",
		ToolSummary: "List download jobs as compact rows, filtered by status, type or text: {jobs, total, offset, status_counts}; get_job shows one job in full",
		Examples:    []string{"amdl jobs list --status failed --fields id,title,error"}},
	{Command: "jobs get", Tool: "get_job", Ops: []string{"getDownload"}, Safety: Read, Args: []string{"job_id"},
		Summary:     "Show one job with its tracks, album progress and each finished track's output path (where the files are)",
		ToolSummary: "Show one job with its album progress and one short entry per track; detail=full returns every field the API has",
		Examples:    []string{"amdl jobs get job_8f3c... --view full"}},
	{Command: "jobs watch", Ops: []string{"streamDownloadEvents", "streamDownloadsFeed"}, Safety: Read, Args: []string{"job_id"},
		Summary:  "Stream a job's events, or every job's milestones without an id, as NDJSON",
		Examples: []string{"amdl jobs watch job_8f3c... --types item_completed,item_failed"}},
	{Command: "jobs wait", Tool: "wait_for_job", Ops: []string{"getDownload", "streamDownloadEvents"}, Safety: Read, Args: []string{"job_id"},
		Summary:     "Wait until a job finishes and print it; the exit status says how it ended",
		ToolSummary: "Wait until a job finishes or timeout_s passes, then answer {done, job}; while done is false the job is still running, so call again",
		Examples:    []string{"amdl jobs wait job_8f3c... --timeout 10m"}},
	{Command: "jobs cancel", Tool: "cancel_job", Ops: []string{"cancelDownload"}, Safety: Write, Args: []string{"job_id"},
		Summary: "Cancel a queued or running job; finished tracks stay on disk"},
	{Command: "jobs retry", Tool: "retry_job", Ops: []string{"retryDownload"}, Safety: Write, Args: []string{"job_id"},
		Summary: "Re-run a failed or cancelled job; finished tracks stay done"},
	{Command: "jobs delete", Tool: "delete_job", Ops: []string{"deleteDownload"}, Safety: Destructive, Args: []string{"job_id"},
		Summary: "Delete a finished job and its records (the downloaded files stay)"},
	{Command: "library list", Ops: []string{"listDownloads"}, Safety: Read,
		Summary:  "List finished downloads: albums, playlists or songs",
		Examples: []string{"amdl library list --kind album --q reol"}},
	{Command: "library sync get", Ops: []string{"getLibrarySyncStatus"}, Safety: Read,
		Summary: "Show the Apple Music library watcher's status"},
	{Command: "library sync reset", Ops: []string{"resetLibrarySyncAnchor"}, Safety: Destructive,
		Summary: "Forget the library watcher's anchor; the next pass re-anchors and submits nothing"},
	{Command: "logs list", Tool: "get_logs", Ops: []string{"listLogs"}, Safety: Read,
		Summary:  "Read buffered log entries, filtered by level, job, request or text",
		Examples: []string{"amdl logs list --job-id job_8f3c... --level error --limit 20"}},
	{Command: "logs tail", Ops: []string{"streamLogs"}, Safety: Read,
		Summary: "Stream log entries as NDJSON"},
	{Command: "system get", Tool: "get_status", Ops: []string{"getSystem", "getHealth", "getWrapperStatus", "getLibrarySyncStatus"}, Safety: Read,
		Summary:     "Show the server's version, queue, pools, tools and free space",
		ToolSummary: "Show whether the server is up and ready to download: {system (version, queue, pools, tools, free space), health, wrapper (signed in or not), library_sync}; a section that fails carries its error"},
	{Command: "system health", Ops: []string{"getHealth"}, Safety: Read,
		Summary: "Check that the server and its database answer"},
	{Command: "system stats", Ops: []string{"getStats"}, Safety: Read,
		Summary: "Show download activity per day, codec and hook"},
	{Command: "hooks list", Ops: []string{"listHooks"}, Safety: Read,
		Summary: "List the configured job hooks"},
	{Command: "hooks switch", Ops: []string{"setHooksEnabled"}, Safety: Write,
		Summary:  "Turn every hook on or off",
		Examples: []string{"amdl hooks switch --enabled=false"}},
	{Command: "hooks set", Ops: []string{"updateHook"}, Safety: Write, Args: []string{"name"},
		Summary:  "Create or change a webhook hook; omitted fields keep their values, and command hooks are edited in hooks.yaml",
		Examples: []string{`amdl hooks set jellyfin --events job_finished --url http://jellyfin.local:8096/Library/Refresh --headers '{"Authorization":"MediaBrowser Token=\"KEY\""}'`}},
	{Command: "hooks test", Ops: []string{"testHook"}, Safety: Write, Args: []string{"name"},
		Summary: "Send a webhook hook once with a sample payload and say whether its endpoint took it"},
	{Command: "hooks delete", Ops: []string{"deleteHook"}, Safety: Destructive, Args: []string{"name"},
		Summary: "Delete a webhook hook"},
	{Command: "config get", Tool: "get_config", Ops: []string{"getConfig"}, Safety: Read, Args: []string{"keys"},
		Summary:  "Show the runtime config, or the dotted keys given",
		Examples: []string{"amdl config get download.quality_priority logging.level"}},
	{Command: "config set", Tool: "set_config", Ops: []string{"updateConfig"}, Safety: Write, Args: []string{"changes"},
		Summary:     "Change runtime config keys (key=value, values in YAML)",
		ToolSummary: "Change runtime config keys: changes maps dotted keys to their new values and the answer is {config: those keys' new values, persisted}",
		Examples:    []string{"amdl config set logging.level=debug download.quality_priority=[alac,aac]"}},
	{Command: "config reload", Ops: []string{"reloadConfig"}, Safety: Write,
		Summary: "Re-read config.yaml on the server and apply its runtime keys"},
	{Command: "wrapper status", Ops: []string{"getWrapperStatus"}, Safety: Read,
		Summary: "Show whether the wrapper is reachable and an account is signed in"},
	{Command: "wrapper login", Ops: []string{"loginWrapper"}, Safety: Write,
		Summary:  "Sign the wrapper in to an Apple ID; a terminal asks for the password and any two-step code without echoing them, a pipe needs --password-stdin",
		Examples: []string{"amdl wrapper login --username me@example.com", "printf '%s\\n' \"$PASSWORD\" | amdl wrapper login --username me@example.com --password-stdin"}},
	{Command: "wrapper 2fa", Ops: []string{"submitWrapperTwoStepCode"}, Safety: Write, Args: []string{"login_id"},
		Summary:  "Submit the two-step code of a login that answered needs_2fa, within 60 seconds; the code is asked for in a terminal, else read from stdin",
		Examples: []string{"amdl wrapper 2fa login_1", "printf '%s\\n' \"$CODE\" | amdl wrapper 2fa login_1"}},
	{Command: "wrapper logout", Ops: []string{"logoutWrapper"}, Safety: Destructive,
		Summary: "Sign an account out of the wrapper"},
	{Command: "wrapper install", Safety: Write,
		Summary:  "Run a wrapper on this machine in QEMU: download upstream's guest, checked against its pinned sha256 (installs QEMU with consent)",
		Examples: []string{"amdl wrapper install", "amdl wrapper install --qemu /opt/homebrew/bin/qemu-system-x86_64"}},
	{Command: "wrapper update", Safety: Write, Summary: "Install the current pinned guest for the local wrapper, keeping its account"},
	{Command: "wrapper import", Safety: Write, Args: []string{"path"},
		Summary: "Install the local wrapper's guest from a downloaded qemu-assets zip, a wrapper-lite-qemu package or a directory"},
	{Command: "wrapper uninstall", Safety: Write, Summary: "Remove the local wrapper's guest; --purge also deletes its account disk"},
	{Command: "wrapper start", Safety: Write, Summary: "Have the backend run the local wrapper again after wrapper stop"},
	{Command: "wrapper stop", Safety: Write, Summary: "Have the backend stop the local wrapper until wrapper start"},
	{Command: "wrapper logs", Safety: Read, Summary: "Show the end of the local wrapper's console log, secrets redacted"},

	{Command: "cli search", Safety: Read, Args: []string{"task"},
		Summary:  "Find the commands for a task, offline; start here",
		Examples: []string{`amdl cli search "retry the failed downloads"`}},
	{Command: "cli commands", Safety: Read, Summary: "List every command and MCP tool with its safety level"},
	{Command: "cli setup", Safety: Read, Summary: "Print the snippets that connect a coding agent to amdl: MCP config and an AGENTS.md note",
		Examples: []string{"amdl cli setup --server nas"}},
	{Command: "schema", Safety: Read, Args: []string{"command"},
		Summary:  "Show a command's flags, API request and exit codes; --view full adds parameter descriptions and the body and output schemas",
		Examples: []string{"amdl schema jobs list"}},
	{Command: "context list", Safety: Read, Summary: "List the saved servers and which one is current"},
	{Command: "context add", Safety: Write, Args: []string{"name", "url"}, Summary: "Save a server under a name and make it current",
		Examples: []string{"amdl context add nas http://192.168.3.5:18570"}},
	{Command: "context use", Safety: Write, Args: []string{"name"}, Summary: "Make a saved server current"},
	{Command: "context remove", Safety: Write, Args: []string{"name"}, Summary: "Forget a saved server"},
	{Command: "doctor", Safety: Read,
		Summary:  "Check what downloading needs: a server that answers, its ffmpeg and wrapper, and ffmpeg here when this machine runs the backend",
		Examples: []string{"amdl doctor", "amdl doctor --server embedded"}},
	{Command: "version", Ops: []string{"getSystem"}, Safety: Read, Summary: "Show this client's and the server's versions"},
	{Command: "serve", Safety: Write, Summary: "Run the amdl backend and web console in this process"},
	{Command: "service install", Safety: Write,
		Summary:  "Run the local backend as a background service that starts at login: launchd, systemd --user, or a logon task on Windows",
		Examples: []string{"amdl service install --wrapper http://nas:30020"}},
	{Command: "service uninstall", Safety: Write, Summary: "Remove the background service; the local data stays"},
	{Command: "service start", Safety: Write, Summary: "Start the background service now"},
	{Command: "service stop", Safety: Write, Summary: "Stop the background service until the next login (on Windows, until service start)"},
	{Command: "service status", Safety: Read, Summary: "Show the background service and the local backend: PID, version, URL and log file"},
	{Command: "tui", Safety: Write, Summary: "Open the interactive terminal interface (what amdl alone opens in a terminal)"},
	{Command: "mcp", Safety: Write, Summary: "Serve the amdl tools to an MCP client over stdio",
		Examples: []string{"claude mcp add amdl -- amdl mcp --server nas"}},
}

// NotExposed lists the operations no capability calls, with the reason.
var NotExposed = map[string]string{}

// Find returns the capability with a command or tool name.
func Find(name string) (Capability, bool) {
	for _, c := range All {
		if c.Command == name || c.Tool == name || c.ID() == name {
			return c, true
		}
	}
	return Capability{}, false
}
