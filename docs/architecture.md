# Architecture

> Type: reference

How the server (`amdl serve` in `apps/amdl`, which runs `internal/server#Run`) and the shared Go core under `internal/` are put
together, and the rules each layer keeps. The repository layout is in the
[README](../README.md#repository-layout). When this page and
the code disagree, the code wins and this page gets fixed in the same commit. Breaking
changes against 1.x are listed in [Upgrading to 2.0](upgrading-to-2.0.md).

## 1. Process and lifecycle

`amdl serve` only reads the config paths from its flags or the environment, mounts the console and
calls `internal/server#Run`; `internal/local#Backend` calls it with its own `net.Listener` for
the desktop app's standalone mode and `amdl --server embedded`. In `Run`, every deferred close runs on every exit path, and a startup
failure is returned and logged; the binary turns it into exit status 1. `Run` builds the
components, starts them, and when its context ends (SIGINT or SIGTERM for the binary) or
the HTTP server fails, stops them in this order
(`internal/server#shutdownSteps`; the binary ignores SIGHUP):

1. HTTP: stop accepting connections and end every SSE stream at once
   (`internal/api#Server.EndStreams`, so a connected client never holds the drain;
   EventSource reconnects and resumes from `Last-Event-ID`), give ordinary requests a
   short drain, then cancel the base context of whatever is still running.
2. Library watcher and catalog token refresher: stop and wait.
3. Job manager: cancel workers with the cause `jobs.ErrShutdown`. A job interrupted by
   shutdown is **not** cancelled: it stays `running` in the database, keeps its items
   and resume checkpoints, emits no terminal event and fires no hook.
   The next start requeues it through recovery (`job_recovered`).
4. Downloader background work (motion-artwork backfill): wait.
5. Hook dispatcher: stop retries at once, drain running attempts, then cancel what is
   still running (each records `hook_failed`).
6. Wrapper client, then store: close.

Each step waits at most its own named timeout and then logs and moves on, so a stuck
component cannot keep the process alive. The timeouts add up to about 105 s;
`deploy/docker-compose.yml` sets `stop_grace_period: 2m` above that, and a test in
`internal/server` fails when the sum outgrows it.

Every long-lived component has the same shape: a constructor that does no I/O, a
`Start` that owns its goroutines or a blocking `Run(ctx)` that the caller runs on its
own goroutine, and a `Shutdown(ctx)` or `Wait(ctx)` (or `Run` itself) that returns only
after they exit or its deadline passes. Nothing outlives `Store.Close` unless its shutdown
step timed out, which is logged as "gave up waiting during shutdown"; a job such a worker
was running stays `running` and resumes on the next start.

## 2. Packages

Products live under `apps/`; `internal/` is the core they share. The core never imports a
product: `amdl serve` hands the console's handler to `internal/server#Options`, which
passes it on through `internal/api#Deps`, and the API leaves `/` and `/console/` out when it gets none.

| Package | Owns | Must not |
| --- | --- | --- |
| `internal/api` | HTTP: route table, handlers per resource, JSON and error envelope, SSE | hold business state, talk to Apple or the wrapper directly |
| `apps/console` | the embedded web console (static files + handler) | call anything but the public `/api/v1`; be imported by `internal/` |
| `internal/jobs` | admission, queue, workers, terminal-state rules, hook dispatch trigger | know about HTTP |
| `internal/media` | the per-track pipeline: select, fetch, decrypt, remux, tag, finalize; simulate backend | write job rows except through `jobs.Reporter` |
| `internal/applemusic` | amp-api, the web-player token, catalog and library reads | fail a job because an optional read (motion artwork, album enrichment, a library cover) failed |
| `internal/wrapper` | the wrapper HTTP client (status, manifests, licenses, lyrics, console login) and the decrypt sessions that run key templates through `internal/fairplay` | |
| `internal/fairplay` | the pure-Go FairPlay sample decryptor, a translation of Temari v0.5.1 | import other internal packages |
| `internal/db` | SQLite schema, versioned migrations, queries | contain business rules |
| `internal/domain` | shared types, enums and event type constants | import other internal packages |
| `internal/events` | in-process wake signals for streams | carry events (subscribers re-read the store) |
| `internal/config` | config file, env overrides, validation, runtime store | |
| `internal/hooks` | webhook/exec hook execution | |
| `internal/librarysync` | the library watcher | |
| `internal/usertoken` | which media-user-token the backend uses: the wrapper's `MUSIC_TOKEN` when it can read one, else `catalog.media_user_token` | log or return the token |
| `internal/logging` | slog setup, in-memory ring buffer, log subscriptions | |
| `internal/limits` | semaphores and the Apple request gate | |
| `internal/ids` | random IDs (`job_<32 hex>`, `item_<32 hex>`) | |
| `internal/buildinfo` | version, commit, build date (ldflags), process start time | |
| `internal/sysproc` | how external commands start: no console window on Windows, and a background process letting go of its own | |
| `internal/client` | the Go client of `/api/v1`: typed requests, SSE with a silence watchdog, followers that resume the job list or one job | import a product or anything but `internal/config` and `internal/domain` |
| `internal/progress` | the clients' stage weights, stage bar, album bar and remaining-time estimate, kept equal to the console's by a test | |
| `internal/format` | sizes, durations, rates and quality labels the clients show | |
| `internal/capability` | the one table of client capabilities (CLI command, MCP tool, safety) and the operations parsed from the embedded spec | hold behaviour; a capability without an operation runs in its client |
| `internal/wrapperhost` | the wrapper amdl runs in a QEMU guest: pinned download of upstream's guest, QEMU arguments, the supervisor and its fw_cfg login | ship upstream's files or put a credential on a command line |
| `internal/local` | the backend run inside a client's process (the desktop app, `amdl --server embedded`): its directories, its config, and the runtime file that lets a second client find it, and the lock that keeps one backend per data directory | |

### 2.1 The wrapper and decryption

The backend holds no Apple account session and no decryption keys of its own. It talks
over HTTP (`wrapper.address`) to one of two upstream services, which look the same to
`internal/wrapper#Client`: [wrapper-lite](https://github.com/WorldObservationLog/wrapper/tree/lite),
which holds one logged-in account, or [wrapper-manager
v2](https://github.com/WorldObservationLog/wrapper-manager/tree/v2), which runs one
wrapper-lite per account behind a single port and forwards each request to an account
whose storefront can serve it. A wrapper-lite can also be one amdl runs itself in a QEMU
guest (`internal/wrapperhost`, the managed wrapper). What the backend asks for:

- `/key`: for each FairPlay key URI, a white-box template (`ctx` and `state`). The client
  caches templates across jobs, and `internal/fairplay#Template` decrypts samples in
  this process, so decryption needs no wrapper request per sample and is limited by CPUs,
  not by the wrapper. `/key` and `/m3u8` share one lock in wrapper-lite, so the client
  keeps at most 2 of them in flight, at most 1 of them `/m3u8`: a batch of quality probes
  never holds both permits while a `/key` waits. A `/key` or `/m3u8` waits at most 10
  request timeouts (`playbackWaitTimeouts`) for its permits and then has its full request
  timeout, so the wait does not use up a `/key`'s retries. A template fetch shared by
  several tracks keeps running while any of them waits for it and stops once all have
  given up.
- `/m3u8`: the device master playlist, asked for only by a Hi-Res Lossless track whose
  ALAC may exceed 48 kHz, because the web player's playlist can omit the higher
  renditions; every other master playlist comes from the catalog response.
- `/lyrics`, `/webplayback` (the AAC-LC fallback) and `/license` (Widevine), 24 at a time.
- `/status`: connectivity, the storefront regions served, and for a wrapper-manager
  `clientCount` and `ready`, whose presence is how the backend tells the two apart.

Logging in is the one thing wrapper-lite cannot do over HTTP: it logs in only from its
own command line. The login routes of `/api/v1/wrapper` therefore forward to wrapper-manager's
`POST /login` and `POST /logout`, and answer `501 login_unsupported` against a bare
wrapper-lite. The password of a login that waits for a two-step code is kept in memory for
at most 75 seconds while no code has arrived; a code submitted in that time keeps it until
its verification ends, at most 75 seconds plus `wrapper.login_timeout_seconds` (195 seconds
by default) after the code was asked for. Requests, pools and the Hi-Res rule are in
[Download pipeline](download-pipeline.md#concurrency) and
[Configuration](configuration.md#catalog-tokens-and-the-hi-res-manifest); the move from the
1.x fork is in [Upgrading to 2.0](upgrading-to-2.0.md#wrapper).

## 3. HTTP API conventions

All JSON endpoints live under `/api/v1`; 2.0 keeps that prefix and sets the spec's
`info.version` to the release version. The contract is `internal/api/openapi.yaml`;
the route table in `internal/api` and the spec are checked against each other in both
directions by a test. The server does not serve the spec, and it sends no CORS headers:
the console, served from the same origin, is the one browser client.

### 3.1 Errors

Every non-2xx JSON response is

```json
{"error": {"code": "job_not_found", "message": "job job_… not found", "details": {}}}
```

`code` is a stable snake_case identifier from the `ErrorCode` enum in the spec (Go:
constants in `internal/api`). `message` is for humans and may change. `details` is
optional and code-specific: the batch body for a submit that accepted nothing, `{"keys":
[…]}` for a rejected config write. Unknown routes under `/api/` answer
`route_not_found` with the same envelope; a path that exists with other methods answers
`405 method_not_allowed` with an `Allow` header (computed from the route table, since
`http.ServeMux` writes its own plain-text 404/405 before any handler runs). The one
non-envelope error is the `503` of `GET /health`, which keeps its `HealthResponse` body
(`{"status": "degraded", "database_error": "…"}`) because monitors read it.

| Status | Codes |
| --- | --- |
| 400 | `invalid_request` (query/body validation, a malformed stream cursor), `invalid_json` |
| 401 | `authentication_failed` (wrapper login) |
| 404 | `route_not_found`, `job_not_found`, `login_not_found`, `account_not_found` |
| 405 | `method_not_allowed` |
| 409 | `job_active`, `job_not_retryable`, `job_finalizing`, `duplicate_active`, `already_logged_in`, `login_pending` |
| 413 | `request_too_large` |
| 422 | `invalid_overrides`, `config_key_not_mutable`, `invalid_config`, `no_urls_accepted` |
| 500 | `internal_error`, `store_error` |
| 501 | `login_unsupported` (wrapper login against a bare wrapper-lite) |
| 502 | `upstream_error` |
| 503 | `queue_full`, `wrapper_unavailable`, `feature_unavailable` |
| 504 | `upstream_timeout` (wrapper login) |

`feature_unavailable` comes only from `GET /logs` and `GET /logs/stream`: `logging.buffer_size`
is `0`, so no log history is kept. Every other dependency in `internal/api#Deps` is
required, and the process does not start without it.

Per-URL submit results keep the coarse `status` enum (`accepted`, `invalid`,
`duplicate_in_request`, `duplicate_active`, `queue_full`, plus `error` for a server-side
failure) and carry the detail in `results[].error` (`{code, message, details?}`). The
codes are the spec's `SubmitErrorCode` enum: with `invalid`, `invalid_url`,
`unsupported_input`, `unsupported_storefront` (`details` carries the storefront and the
supported ones), `decryptor_unavailable` and `invalid_configuration`; with the status of
the same name, `duplicate_in_request`, `duplicate_active` and `queue_full`; with
`error`, `store_error` and `internal_error` (the validator failed unexpectedly).
`POST /downloads` answers `202` with the batch body when at least one URL was accepted.
When none was, it answers `500 store_error` if any rejection was a store failure,
`500 internal_error` if the only server-side failures came from the validator (the
client should retry either), and `422 no_urls_accepted` otherwise; the batch body is in
`error.details` in every case.

### 3.2 Jobs

- Counters are disjoint and always present: `total_items`, `completed_items` (written by
  this job), `skipped_items` (already on disk), `failed_items`, `cancelled_items`.
  `total_items` is stored when the input resolves; the other four are always derived
  from `job_items` by one store query, including when the manager decides the terminal
  status at finalize. There are no stored per-status counter columns. `done_items` and
  the legacy `force` flag are gone; forcing is `overrides.force_overwrite`.
- `started_at` is set when a worker first starts the current run and kept across
  recovery after a restart; a retry starts a new run and clears it, with `finished_at`.
  `finished_at` is set on the terminal transition. So `finished_at - started_at` is the
  wall time of the last run, including any restart in the middle of it. Both are
  RFC 3339 or absent.
- A user cancel marks every non-terminal item `cancelled`, persisted with a
  non-cancelled context; for a queued job (a recovered one can carry rows from the
  interrupted run) that happens in one write before the terminal one, so the terminal
  event and hook count them. A shutdown leaves items as they are so recovery can resume
  them.
- A panic while processing a job fails that job with `internal error: panic: <value>`
  (retryable); the stack is logged and the worker keeps running.
- `GET /downloads` and `GET /downloads/{id}` return `event_cursor`: pass it as
  `?cursor=` (or `Last-Event-ID`) to the matching stream. It is read before the
  snapshot, so resuming from it never skips an event.

### 3.3 Events and streams

- An event is `{id, job_id, item_id?, type, phase?, message?, payload?, created_at}`.
  `payload` is a JSON **object**, not a string. Event types are the constants in
  `internal/domain` and the `EventType` enum in the spec.
- Streams are Server-Sent Events only. There are no WebSocket twins.
- Every stream resumes from `Last-Event-ID` or `?cursor=<id>`; the header wins when both
  are present. A malformed cursor is `400 invalid_request`.
- Every stream sends `X-Accel-Buffering: no` and a `: keepalive` comment every 15 s.
  That comment interval is separate from the store re-drain ticker that bounds latency
  when a wake signal is dropped; keep both.
- `GET /downloads/events`: named events `download_upserted` / `download_deleted`, `id`
  = event id, `data` = `{type, job?, job_id?, event_id, status_counts}`.
- `GET /downloads/{id}/events`: named event = event type, `id` = event id. When the job
  is terminal and the client has everything, the server answers **204 No Content**,
  which makes `EventSource` close without reconnecting. A browser reports any non-200
  as a generic `error`, so a client that sees its stream close refetches the job to tell
  "finished" from "broken".
- `GET /logs` and `GET /logs/stream` page with `cursor` (log sequence) as well; the list
  returns `next_cursor`. With `logging.buffer_size: 0` both answer
  `503 feature_unavailable`; `buffer_size` is a startup key, so the API decides this
  once, in `internal/api#New`.
- Only milestone events reach the overview feed. `item_progress` events are kept while a
  job runs and pruned when it finishes.

### 3.4 Config

- `GET /config` returns the in-memory snapshot and has no side effects.
- `POST /config/reload` re-reads the file; the response is the same shape as
  `GET`, with `reload_error` set if the file is invalid (the previous snapshot stays).
- `PUT /config` re-reads the file under the same lock before applying the patch, so a
  hand edit made since the last reload is not overwritten.

### 3.5 System and stats

- `GET /system`: build info, uptime, simulate flag, job/queue capacity,
  pool sizes, tool availability, free space of `downloads_dir` and `temp_dir`.

  ```json
  {"version": "v2.0.0", "commit": "a6a6b46", "build_date": "2026-10-28T12:00:00Z",
   "go_version": "go1.26.5", "os": "linux", "arch": "amd64",
   "started_at": "2026-10-28T12:00:05Z", "uptime_seconds": 3600,
   "simulate": false, "motion_artwork_enabled": true,
   "jobs": {"workers": 3, "active": 1, "queue_depth": 0, "queue_capacity": 256, "recovery_backlog": 0},
   "pools": {"downloads": 16, "decrypts": 32, "wrapper_requests": 4},
   "tools": [{"name": "ffmpeg", "available": true, "path": "/usr/bin/ffmpeg"}],
   "storage": [{"name": "downloads_dir", "path": "data/downloads", "free_bytes": 1, "total_bytes": 2},
               {"name": "temp_dir", "path": "data/tmp", "error": "statfs data/tmp: no such file or directory"}]}
  ```

  `version`, `commit` and `build_date` are stamped by `-ldflags -X` into
  `internal/buildinfo` (the Makefile from git, the Dockerfile from the `VERSION`,
  `COMMIT` and `BUILD_DATE` build args that the image workflows pass). Without the
  stamp `version` is `dev`, and `commit`/`build_date` come from the VCS settings that
  `go build` records in a git checkout (`build_date` is then the commit time), or stay
  empty. No secret is returned. `jobs` comes from `internal/jobs#Manager.QueueStats`; `workers`
  and `pools` are startup values. `tools` is the `internal/media#ToolChecker` result.
  `storage` comes from `statfs`; when a directory cannot be inspected, its entry carries
  `error` instead of the byte counts. The backend builds only for Linux, macOS and
  FreeBSD (`statfs` here, process groups for exec hooks); release images are Linux.
- `GET /stats?days=N` (1-90, default 14; anything else is `400 invalid_request`).
  `totals` and `codecs` cover every retained job; `daily` and `hooks` cover the last
  `days` UTC calendar days, today included. `daily` has exactly `days` entries, oldest
  first, with zeros for a day without activity: a track counts on the day of its last
  `item_completed` event (its `file_size` is the day's `bytes_written`), a job once per
  `job_finished` or `job_failed` event. `hooks` groups `hook_succeeded`/`hook_failed`
  events by hook name. The numbers cover jobs that still exist, since deleting a job
  deletes its items and events. The window queries use the `job_events(type,
  created_at)` index of migration 2 (`internal/db#Store.Stats`). A result is cached for
  30 s per `days` value (`statsCacheTTL` in `internal/api`); `generated_at` says when it
  was computed.

  ```json
  {"generated_at": "2026-10-28T13:00:00Z", "days": 14,
   "totals": {"jobs": 12, "tracks_completed": 210, "tracks_skipped": 4, "tracks_failed": 1,
              "bytes_written": 9876543210, "duration_ms": 52000000},
   "daily": [{"date": "2026-10-28", "tracks_completed": 21, "bytes_written": 987654321,
              "jobs_completed": 1, "jobs_failed": 0}],
   "codecs": [{"codec": "alac", "bit_depth": 24, "sample_rate": 96000, "tracks": 120}],
   "hooks": [{"name": "notify", "succeeded": 10, "failed": 1, "last_failed_at": "2026-10-28T09:15:00Z"}]}
  ```

## 4. Storage

- Schema changes are ordered migrations gated by `PRAGMA user_version`. Migration 1
  brings any 1.x database to the final 1.x schema: it runs, once, everything 1.x ran on
  every open (`CREATE TABLE IF NOT EXISTS`, the column backfills, progress backfill,
  timestamp normalization). After that, `Open` does nothing schema-shaped.
- Migration 2 is the 2.0 schema: `jobs.started_at`/`finished_at` (backfilled from
  events), drops `jobs.force`, `jobs.done_items` and `jobs.failed_items`, indexes
  `job_events(type, created_at)` and deletes `item_progress` events of terminal jobs.
- Migration 3 adds `job_items.download_bytes`, the bytes of encrypted media the current
  attempt has received (`progress.download_bytes` in the API).
- Migrations preserve every row of user data (jobs, items, milestone events,
  library-sync anchor). A test builds a database from the frozen 1.x schema SQL, inserts
  rows, migrates, and asserts every row survived and migration 1 changed nothing on a
  database that was already current.
- Timestamps are fixed-width UTC text so lexical order is time order.

## 5. Output paths

Path templates, `{Quality}` labels and lyrics sidecar names are unchanged from 1.x,
because the "already downloaded" check compares paths and any change re-downloads whole
libraries. Path segments are cleaned by `safeName`: reserved characters replaced,
leading and trailing dots and spaces removed, each segment capped at 240 bytes on a
rune boundary.

## 6. Hooks

The hook payload's job carries the API counters of 3.2 (`completed_items`,
`skipped_items`, `failed_items`, `cancelled_items`) and `started_at`/`finished_at`; its
items' `output_path` and `AMDL_OUTPUT_PATHS` are absolute (resolved with
`domain.AbsolutePath`; the database keeps the stored form, and the API's `output_path`
is the same absolute path, shown only for a finished item). Exec hooks get the
backend's environment without any `AMDL_*` variable (the backend's own settings; one left
over from an older install may hold a secret) plus the documented hook variables, and a failed
one records at most the last 4 KiB of its stderr. Webhook URLs must be absolute
`http`/`https` URLs; an invalid one fails startup naming the entry. Attempts run on a
dispatcher-owned context, so shutdown can stop retries and cancel running attempts.

## 7. Web console

`apps/console` serves a no-build Preact+htm app at `/console/`; `/` redirects there.
It is a client of the public API only. See `apps/console/DESIGN.md`.
