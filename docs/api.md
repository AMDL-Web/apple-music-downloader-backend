# API

> Type: reference

Examples assume the service listens on `http://localhost:18080`.

- OpenAPI 3.1 spec: [`internal/api/openapi.yaml`](../internal/api/openapi.yaml) in this
  repository; the server does not serve it
- Web console: <http://localhost:18080/console/>; `/` redirects there

The spec is the source of truth for every request and response shape; its `info.version`
is the contract version, while the URL prefix stays `/api/v1`. The web console is the
maintained client and changes together with the API; other clients, the iOS app and
amdl-web included, are not kept in sync. The API sends no CORS headers, so the console,
served from the same origin, is its only browser client; scripts and `curl` call it
directly.

## Errors

Every non-2xx JSON response has one shape:

```json
{"error": {"code": "job_not_found", "message": "job job_… not found"}}
```

Switch on `code`, a stable value of the `ErrorCode` enum in the spec (Go: the constants in
`internal/api/errors.go`). `message` is for humans and may change. `details` is present
only for some codes: on `POST /downloads` it holds the batch body, on a rejected config
write it lists the offending keys, on a failed `POST /lookup` it holds the parsed link.

| Status | Codes |
| --- | --- |
| 400 | `invalid_request` (query or body validation, a malformed stream cursor), `invalid_json` (the body is not well-formed JSON) |
| 401 | `authentication_failed` |
| 404 | `route_not_found`, `job_not_found`, `login_not_found`, `account_not_found`, `catalog_not_found` |
| 405 | `method_not_allowed`, with an `Allow` header |
| 409 | `job_active`, `job_not_retryable`, `job_not_cancellable`, `job_finalizing`, `duplicate_active`, `already_logged_in`, `login_pending` |
| 413 | `request_too_large` (JSON bodies are capped at 1 MiB) |
| 422 | `invalid_overrides`, `config_key_not_mutable`, `invalid_config`, `no_urls_accepted` |
| 500 | `internal_error`, `store_error` (the job store failed; retrying may succeed) |
| 501 | `login_unsupported` |
| 502 | `upstream_error` |
| 503 | `queue_full`, `wrapper_unavailable`, `feature_unavailable` |
| 504 | `upstream_timeout` |

An unknown path under `/api/` answers `404 route_not_found`; a path that exists with other
methods answers `405 method_not_allowed`. `feature_unavailable` comes only from
`GET /logs` and `GET /logs/stream`, when `logging.buffer_size` is `0` and no log history
is kept. A job lookup answers `404 job_not_found` only
when the job does not exist; a store failure is `500 store_error`, so a client never drops
a job because of a transient error.

The one exception is `GET /api/v1/health`, whose `503` body is
`{"status":"degraded","database_error":"…"}`.

## Streams

Every stream is Server-Sent Events: the per-job events, the overview feed and the log
tail. They share these rules:

- **Resume cursor.** Send the `Last-Event-ID` header or `?cursor=<id>`; when both are set
  the header wins, because `EventSource` sends it on every automatic reconnect while the
  URL still carries the first cursor. A cursor that is not a non-negative integer is
  `400 invalid_request`. The stream replays everything after the cursor, then goes live.
- **Seeding.** `GET /downloads` and `GET /downloads/{id}` return `event_cursor`, read
  before the snapshot, so resuming from it never skips an event. Never open the overview
  feed without a cursor: that replays the whole retained milestone history.
- **Keepalive.** Every stream sends a `: keepalive` comment every 15 seconds and the
  `X-Accel-Buffering: no` header, so proxies neither buffer nor time out an idle stream.

Events are named (`event: <type>`), so a browser client uses `addEventListener` with the
name; `onmessage` does not fire for them.

## System

```bash
curl http://localhost:18080/api/v1/health
```

Returns `{"status":"ok"}`, or `503` with `{"status":"degraded","database_error":"…"}`
when the SQLite store cannot be reached. This is what the container health check uses.

```bash
curl http://localhost:18080/api/v1/system
```

Returns what is running and what it has to work with:

- `version`, `commit`, `build_date`: stamped at build time (`make build`, the Docker
  image). A plain `go build` reports `version` `dev` and takes the commit and its time
  from git, if it was built inside a checkout; `go run` leaves both empty.
- `go_version`, `os`, `arch`, `started_at`, `uptime_seconds`.
- `simulate` (`simulate.enabled`), `motion_artwork_enabled`,
  `default_storefront` (`catalog.default_storefront`, a startup key). The API refuses a
  link without a storefront in its path (`invalid_url`); a client that accepts such links, like the
  web console, completes them with this value. Search, artist albums and the album page default to it.
- `jobs`: `workers` (`download.max_running_jobs` at startup), `active`, `queue_depth` of
  `queue_capacity` (a full queue rejects submissions with `queue_full`), and
  `recovery_backlog`, the jobs resumed at startup that still wait for a worker.
- `pools`: the startup sizes of the download, decrypt and wrapper-request pools.
- `tools`: each external command (`ffmpeg`) with `available` and its resolved `path`, or
  the lookup `error`.
- `storage`: the absolute `path`, `free_bytes` and `total_bytes` of the file systems
  holding `download.downloads_dir` and `download.temp_dir`, or an `error` when a directory
  cannot be inspected (it does not exist yet, or the platform is not Linux, macOS or
  FreeBSD).
- `startup`: configuration read only at startup, for a client to show as read-only facts:
  `config_path` (the absolute path of the config file a startup key is edited in, `""`
  when the config is kept in memory only), `wrapper_address` (`wrapper.address`, without
  user info, query or fragment), `listen` (`server.listen`), `database_path`
  (`database.path`, absolute), `log_buffer_size`
  (`logging.buffer_size`), `catalog_language` (`catalog.language`) and
  `max_parallel_requests` (`catalog.max_parallel_requests`, or 1 when it is 0 or less).

No secret is returned.

```bash
curl 'http://localhost:18080/api/v1/stats?days=7'
```

Download activity. `days` is 1-90 (default 14); anything else is `400 invalid_request`.

- `totals`: every retained job (`jobs`), its tracks by final status (`tracks_completed`,
  `tracks_skipped` for files already on disk, `tracks_failed`), the bytes written by
  completed tracks and their total playback time (`duration_ms`).
- `daily`: exactly `days` entries, one per UTC date, oldest first, ending today. Each has
  `tracks_completed`, `bytes_written`, `jobs_completed` and `jobs_failed`; a quiet day has
  zeros. A track counts on the day it completed, a job on each day it finished or failed
  (a failed job that is retried and completes counts on both).
- `codecs`: completed tracks of every retained job by `codec`, `bit_depth` and
  `sample_rate`, most first.
- `hooks`: per hook name, `succeeded` and `failed` runs inside the window, and
  `last_failed_at` when one failed.

Deleting a job removes it from every figure. Results are cached for 30 seconds per `days`
value; `generated_at` says when a result was computed.

### Logs

```bash
curl 'http://localhost:18080/api/v1/logs?level=error&limit=100'
curl 'http://localhost:18080/api/v1/logs?level=error&cursor=1234'
```

The first query returns the newest `limit` entries; pass the response's `next_cursor` as
`?cursor=` to page forward. `truncated` says that entries after the cursor were already
evicted from the buffer.

```bash
curl -N 'http://localhost:18080/api/v1/logs/stream?job_id=job_01JZ0000000000000000000000'
```

Each entry is an event named `log` whose `id` is the log sequence. With a cursor the
stream replays every matching entry still buffered; without one, the newest `limit`
(default 500). A client that reads too slowly is disconnected and reconnects from its last
id.

Every HTTP response carries an `X-Request-ID` the server generates; an incoming header of
that name is ignored. `GET /api/v1/logs?request_id=<id>` aggregates that request's access log with the
synchronous job operations it triggered.

In-memory retention is `logging.buffer_size`; at `0` no history is kept and both
`GET /logs` and `GET /logs/stream` answer `503 feature_unavailable`; read stdout
(`logging.console`) instead. The backend writes no log file. `logging.level` and
`logging.access_log` are runtime-mutable; `logging.console` and the buffer size need a
restart.

## Wrapper

```bash
curl http://localhost:18080/api/v1/wrapper/status
```

Returns `ready`, `logged_in`, `regions`, `accounts`, `login_supported` and
`media_user_token`. `logged_in` is true
when an account is logged in and serves a storefront. `accounts` lists the account
identifiers the wrapper reports (`[]` when none; neither wrapper-lite nor wrapper-manager
reports any). `login_supported` is true when the wrapper is a wrapper-manager, or a
wrapper amdl runs on this machine; the three routes below need one of them. The backend
does not contact the wrapper at startup, so it starts fine without a reachable wrapper,
and this endpoint is how you verify connectivity; an unreachable wrapper is
`503 wrapper_unavailable`.

`media_user_token` is the wrapper account's media-user-token as the backend reads it from
the wrapper's `MUSIC_TOKEN` file, masked like the configured token in `GET /config` (`...`
and its last four characters), or `""` when it cannot read one. The backend reads it from a
wrapper it runs itself, or from `wrapper.data_dir` ([configuration.md](configuration.md#the-media-user-token)).
While it is not `""`, downloads, lookups and library sync use the wrapper's token, and
`catalog.media_user_token` stays saved but unused.

When amdl runs the wrapper itself (`amdl wrapper install`, [install.md](install.md#the-wrapper)),
the response also carries `managed`: `mode` (`starting`, `serving`, `login`, `stopped` or
`failed`), the QEMU `accel` in use and whether it fell back to TCG (`accel_fallback`), the
`qemu` binary, the installed `guest`, `last_error`, and `login_residue`. A booting or
stopped managed wrapper answers `200` with `ready` false rather than `503`.

```bash
curl -X POST http://localhost:18080/api/v1/wrapper/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"apple-id@example.com","password":"password"}'
```

The username is trimmed; the password is forwarded exactly as sent, because spaces can be
part of it. If the response contains `"status":"needs_2fa"`, submit the code within 60
seconds using the `login_id` from that response. The password is held in memory for that
wait (at most 75 seconds) and, once a code is submitted, until its verification ends: at most
75 seconds plus `wrapper.login_timeout_seconds` (195 seconds by default) after the
`needs_2fa` response. With a wrapper-manager it is never written to disk. A wrong code ends
the login; start again from the password.

With a wrapper amdl runs, a login boots the guest with the password in a 0600 file that
QEMU passes to it through `fw_cfg` and that is deleted at the guest's first console line;
the password never appears on a command line. The guest copies it onto its own disk, and
amdl zeroes that copy before the next boot, which follows at once: `managed.login_residue`
stays true until then. A login or code answers within `internal/wrapperhost#LoginLimit`,
since the guest boots twice per login and may run emulated. A caller that disconnects from
the login before its answer ends the login; a submitted code runs to its outcome whether or
not the caller waits. The code waits up to four minutes. A username with a
colon, or either value with a line break or NUL, is `400 invalid_request`; so is a code
that is not six digits, which leaves the login waiting for the right one. While
`amdl wrapper stop` has turned the wrapper off, a login is `503 wrapper_unavailable`.
Logout gives the guest a fresh disk, whatever the username.

```bash
curl -X POST http://localhost:18080/api/v1/wrapper/login/{login_id}/2fa \
  -H 'Content-Type: application/json' \
  -d '{"two_step_code":"123456"}'
```

```bash
curl -X POST http://localhost:18080/api/v1/wrapper/logout \
  -H 'Content-Type: application/json' \
  -d '{"username":"apple-id@example.com"}'
```

Login errors: `401 authentication_failed`, `409 already_logged_in` (the account already
has a wrapper), `409 login_pending` (a login for the account, or this session's two-step
code, is already in progress), `404 login_not_found` (the two-step session expired),
`404 account_not_found` (logout of an account with no wrapper), `501 login_unsupported`
(the wrapper is a bare wrapper-lite: log in with `wrapper-lite-rootless --login` on its
host), `502 upstream_error`, `504 upstream_timeout`.

## Downloads

### Create

```bash
curl -X POST http://localhost:18080/api/v1/downloads \
  -H 'Content-Type: application/json' \
  -d '{"urls":["https://music.apple.com/us/album/example/123456789?i=987654321"],
       "overrides":{"force_overwrite":true}}'
```

`urls` is a list; each entry may itself hold several URLs separated by whitespace, commas
or semicolons, up to 100 in all. Each URL is reported back in `results[]` with a `status`
and, unless it was accepted, an `error` object `{code, message, details?}`:

| `status` | `error.code` |
| --- | --- |
| `accepted` | — (the new `job` is included) |
| `invalid` | `invalid_url`, `unsupported_input`, `unsupported_storefront` (`details`: `storefront`, `supported_storefronts`), `decryptor_unavailable`, `invalid_configuration` |
| `duplicate_in_request` | `duplicate_in_request` |
| `duplicate_active` | `duplicate_active` (`existing_job_id` names the running job) |
| `queue_full` | `queue_full` |
| `error` | `store_error`, `internal_error` — the server failed; the same submit may succeed when retried |

The response is `202` with `{accepted, rejected, results}` when at least one URL was
accepted. When none was, it is an error whose `details` is that same body: `500
store_error` if any URL failed in the job store (`500 internal_error` if the only server
failure was an unexpected validator error), else `422 no_urls_accepted`.

For album links carrying `?i=<song_id>`, `catalog.album_track_url_mode` decides the task
type: `song` (default) treats it as a single-song job using the `i` parameter, `album`
ignores `i` and downloads the whole album.

### Job overrides

`overrides` overlays the quality and overwrite keys of the runtime `download` config for
that submission only. Omitted fields keep the global value; a value that fails validation
is `422 invalid_overrides`.

| Field | Notes |
| --- | --- |
| `quality_priority`, `codec_alternative` | Codec order and whether to fall back to the next codec |
| `alac_max_sample_rate`, `alac_max_bit_depth` | ALAC limits |
| `force_overwrite` | Overwrite existing output files and their lyrics sidecars instead of skipping |

Everything else comes from the runtime config for every job: directories, path templates,
lyrics, covers, `memory_mode`, `max_attempts`, `check_integrity`, and the media-user-token
(the wrapper's when the backend can read it, else `catalog.media_user_token`). Every enabled hook in `hooks.yaml` whose `events` and
`job_types` match runs for every job. Any other field, at the top level (`force`,
`media_user_token`) or inside `overrides` (`hooks`, `downloads_dir`, `media_user_token`,
...), is `400 invalid_request` naming the unknown field.

**`force_overwrite`** — `true` overwrites an existing audio file and its lyrics sidecars;
`false` (the default, from `download.force_overwrite`) skips tracks whose output already
exists.

The overrides are persisted with each job and apply again on a retry or after a restart.
A job row written before the other fields were removed is read with them ignored, and its
terminal write drops them.

### Radio stations

Station links (`https://music.apple.com/us/station/.../ra.xxxx`) are supported when they
resolve to a track list — personalised and curated stations. They need an Apple Music
subscription token: the wrapper's, when the backend can read it (`media_user_token` in
`GET /api/v1/wrapper/status` is not `""`), else `catalog.media_user_token` (Settings in the
console, or `PUT /api/v1/config`).

```bash
curl -X POST http://localhost:18080/api/v1/downloads \
  -H 'Content-Type: application/json' \
  -d '{"urls":["https://music.apple.com/us/station/example/ra.978194965"]}'
```

Live stations such as Apple Music 1 have no static track list and end with an explicit
error. Station tracks come from Apple's rolling "next tracks" list, so one download
captures whatever is offered at that moment rather than a fixed catalogue. Output goes to
its own directory per `download.station_path_format`.

A token also unlocks private-playlist artwork: the public catalog has no cover for a
private playlist, so with a token the backend reads it from the library copy as the user.
Without one the playlist still downloads, just without its cover.

### Query, cancel, retry, delete

```bash
curl http://localhost:18080/api/v1/downloads/{job_id}
```

Returns `{job, items, hooks, event_cursor}`. A finished item (`completed` or
`skipped_existing`) carries `output_path`, the absolute path of its file on the machine
that runs the backend, fixed when the track was written; the console's Files section
shows the folder it sits in. An item
that is not finished omits it, and the item events on the stream follow the same rule.

```bash
curl 'http://localhost:18080/api/v1/downloads?limit=20&offset=0&status=failed,cancelled&type=album&q=beta&sort=updated_at&order=desc'
```

Parameters: `limit` (default 50, max 200), `offset`, `status`, `type`, `q` (substring of
the title, artist name or input), `sort` (`created_at` | `updated_at`), `order`
(`asc` | `desc`); other parameters are ignored. Jobs that tie on the sort field, such as a batch submitted in one request,
list in submission order whichever `order` is asked for, so a batch reads in paste order.
Multi-value parameters accept repeated keys or comma-separated values. The response adds `status_counts` (for the same
filter), `total`, `limit`, `offset` and the global `event_cursor`.

Every job carries `total_items` (tracks the input resolved to) and four disjoint item
counters, always present: `completed_items` (written by this job), `skipped_items`
(already on disk), `failed_items` and `cancelled_items`. They are derived from the job's
items on every read, so a running job's counters match its `items`. `progress` (0 to 1, to
three decimals) is the job's overall progress, the mean of its tracks' stage-weighted
progress as the console's album bar shows it (`internal/progress#Overall`): below 1 while
a track has not finished, 1 once the job completed. It is derived on every read too, and
event payloads leave it out. `started_at` is set
when a worker first starts the current run and kept across restarts; a retry starts a
new run and clears it. `finished_at` is set when the job reaches a terminal status and
cleared by a retry. Both are RFC 3339 and absent until set. Forcing an overwrite is `overrides.force_overwrite`.

```bash
curl -X POST http://localhost:18080/api/v1/downloads/{job_id}/cancel
```

```bash
curl -X POST http://localhost:18080/api/v1/downloads/{job_id}/retry
```

Only `failed` and `cancelled` jobs can be retried: otherwise `409 job_not_retryable`.
Only a `queued` or `running` job can be cancelled: a `completed` or `failed` one is
`409 job_not_cancellable`, and a `cancelled` one answers `200` again.
Finished tracks stay done; failed and cancelled ones run again. A job still finishing
its previous run is `409 job_finalizing` (retry a moment later), one whose input is
already queued or running again is `409 duplicate_active`, and a full queue is
`503 queue_full`.

```bash
curl -X DELETE http://localhost:18080/api/v1/downloads/{job_id}
```

Deletes a terminal job and its records. A job that is queued, running, finalizing or
running a hook is `409 job_active`.

## Events

```bash
curl -N 'http://localhost:18080/api/v1/downloads/{job_id}/events?cursor=<event_cursor>'
curl -N 'http://localhost:18080/api/v1/downloads/events?cursor=<event_cursor>'
```

The per-job stream replays the job's events after the cursor, then streams new ones; each
SSE event is named after the event type and its `id` is the event id. It ends once the
job is terminal and every hook has finished. A client that already has everything gets
`204 No Content`, which makes `EventSource` stop reconnecting. A browser reports any
non-200 answer as a generic `error`, so a client whose stream closed refetches
`GET /downloads/{job_id}` to tell "finished" from "broken".

`GET /api/v1/downloads/events` is the cross-job overview feed: it pushes only the
milestones that change how a job appears in the list, so a client can render the whole
downloads page without subscribing to each job. Messages are named `download_upserted`
(data: `{type, job, event_id, status_counts}`, the job's current snapshot) and
`download_deleted` (`{type, job_id, event_id, status_counts}`); `status_counts` is global.
`event_id` is also the SSE id and only moves forward; a resent message is idempotent.
Progress has no milestone, so every 2 seconds (`internal/api#feedProgressInterval`) the
feed also sends `download_upserted` for each running job whose `progress` moved since it
last sent it, with the `event_id` already reached. The feed never ends on its own. It is the only stream that delivers a motion artwork resolved
after a job finished.

Each event is `{id, job_id, item_id?, type, phase?, message?, payload?, created_at}`
(the `Event` schema), and `payload` is a JSON object. Job lifecycle events carry the same
public snapshot fields as the REST `Job`; item events carry the same fields as the REST
`JobItem`, plus event-only fields such as `download_attempts` and `will_retry`. So after
one initial `GET`, a client can merge subsequent events directly — no re-fetch is needed
on `item_completed`, `item_failed`, `item_skipped` or job-terminal events.

Event types (the `EventType` enum in the spec; Go constants in `internal/domain`):

| | |
| --- | --- |
| Job lifecycle | `job_queued`, `job_started`, `job_retried`, `job_recovered`, `job_finished`, `job_failed`, `job_cancelled`, `job_deleted` |
| Resolution | `resolved_input`, `motion_artwork_resolved` |
| Item progress | `item_progress` (its `phase` is the item status), `item_completed`, `item_failed`, `item_skipped`, `item_overwrite` |
| Codec and retries | `codec_selected`, `codec_fallback`, `codec_failed`, `operation_retry`, `operation_exhausted`, `operation_recovered`, `alac_repaired` |
| Hooks | `hook_started`, `hook_succeeded`, `hook_failed` |
| Other | `standalone_cover_failed` |

A retry inside a codec attempt is an `operation_*` event whose `phase` is the codec.
`job_deleted` and `motion_artwork_resolved` have no payload. Only the list-level
milestones reach the overview feed: the job lifecycle types, `resolved_input`,
`motion_artwork_resolved`, `item_completed`, `item_skipped` and `item_failed`.
`item_progress` events are kept while a job runs and deleted when it finishes; the
item rows keep their final state.

`download.progress_event_interval_ms` (default 500, range 0–10000) is the minimum gap
between two `item_progress` events for the same item in the same state. It throttles only
the download and decrypt meters; status changes, stage marks and one-shot events always
publish immediately. A value held back by the interval is flushed just before whatever
event supersedes it, so the last percentage before a state change is never lost and a
client resuming from a cursor sees the same sequence as a live one. It is read once when
a job starts.

## Link lookup

```bash
curl -X POST http://localhost:18080/api/v1/lookup \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://music.apple.com/us/album/igor/1463409338"}'
```

```json
{"input":"https://music.apple.com/us/album/igor/1463409338","type":"album","storefront":"us",
 "id":"1463409338","title":"IGOR","artist_name":"Tyler, The Creator",
 "artist_url":"https://music.apple.com/us/artist/tyler-the-creator/420368335",
 "artwork_url":"https://is1-ssl.mzstatic.com/image/thumb/…/886447710180.jpg/{w}x{h}bb.jpg",
 "artwork_bg_color":"f1a8bf","artwork_text_color1":"171615","artwork_text_color2":"272021",
 "artwork_text_color3":"423337","artwork_text_color4":"4f3b41","release_date":"2019-05-17",
 "genre":"Hip-Hop/Rap","track_count":12,"content_rating":"explicit","apple_digital_master":true,
 "audio_traits":["lossless","lossy-stereo"]}
```

Previews one link without creating a job, so a client can show the cover, title and artist
as soon as a link is pasted. The link is parsed exactly as a submission parses it: `type`
and `id` are what the job would be, and the `?i=` of an album link follows
`catalog.album_track_url_mode` (reported as `track_id` either way). The metadata fields
have the names and meaning of the job fields; `track_count` is the number of songs a
download would contain (absent for artists and stations, whose track lists are not read),
a song or album carries Apple's `audio_traits` as search hits do, and a station adds
`needs_media_user_token`. Each type costs light catalog reads only (an
album or playlist pages through its track list): never the animated-cover or Enhanced
HLS reads, never a station's next-tracks feed, at most 15 seconds in all.

The body is `{url}` only. A private playlist's cover is read with the wrapper's
media-user-token when the backend can read it, else `catalog.media_user_token`; the token is
never echoed.

| Case | Answer |
| --- | --- |
| `url` missing, unknown body field | `400 invalid_request` |
| Not an Apple Music link the backend parses | `400 invalid_request` |
| A music video or a live radio station | `400 invalid_request`, `details.link` |
| Apple has no resource with the link's id (its 404) | `404 catalog_not_found`, `details.link` |
| The catalog read fails otherwise | `502 upstream_error`, `details.link` |
| No answer within 15 seconds | `504 upstream_timeout`, `details.link` |

`details.link` carries `input`, `type`, `storefront`, `id` and `track_id` of the parsed
link, so the client can still show what was pasted.

## Catalog search

```bash
curl 'http://localhost:18080/api/v1/search?term=Taylor+Swift&storefront=us&limit=2'
```

```json
{"term":"Taylor Swift","storefront":"us",
 "songs":{"items":[{"id":"6814997425","type":"song","name":"Patient Zero",
   "url":"https://music.apple.com/us/song/patient-zero/6814997425",
   "artwork_url":"https://is1-ssl.mzstatic.com/image/thumb/…/{w}x{h}bb.jpg","artwork_bg_color":"2b080c",
   "artist_name":"Taylor Swift","album_name":"The Life of a Showgirl: The Encore",
   "release_date":"2026-09-24","genre":"Pop","duration_ms":225868,"apple_digital_master":true,
   "audio_traits":["atmos","lossless","lossy-stereo","spatial"]}, …],"next_offset":2},
 "albums":{"items":[{"id":"6814997249","type":"album","name":"The Life of a Showgirl: The Encore",
   "url":"https://music.apple.com/us/album/the-life-of-a-showgirl-the-encore/6814997249",
   "artist_name":"Taylor Swift","release_date":"2026-09-24","track_count":16,
   "content_rating":"explicit","prior_job":{"id":"job_…","status":"completed",
   "finished_at":"2026-10-30T03:12:09Z"}, …}, …],"next_offset":2},
 "playlists":{"items":[{"id":"pl.2553030a768547e9ab3e91230de8439f","type":"playlist",
   "name":"Lo-fi beats to study, work, sleep and relax to",
   "url":"https://music.apple.com/us/playlist/lo-fi-beats-to-study-work-sleep-and-relax-to/pl.2553030a768547e9ab3e91230de8439f",
   "artist_name":"BeatTape", …}, …],"next_offset":2},
 "artists":{"items":[{"id":"159260351","type":"artist","name":"Taylor Swift",
   "url":"https://music.apple.com/us/artist/taylor-swift/159260351","genre":"Pop", …}]}}
```

Searches the Apple Music catalog for songs, albums, playlists and artists, one page per group in
Apple's relevance order. Nothing is downloaded and no job is created.

| Parameter | Meaning |
| --- | --- |
| `term` | Required. Runs of white space count as one space; at most 200 characters. |
| `types` | A subset of `songs`, `albums`, `playlists`, `artists`, comma-separated or repeated; default all four. Groups come back in that order whatever order is given. |
| `storefront` | A two-letter storefront code; default `catalog.default_storefront`. |
| `limit`, `offset` | Hits per group, 1-50 (default 10), and where each group's page starts (default 0). |

Each requested group is there, `items` empty when Apple has nothing, and carries
`next_offset` when Apple has more: pass it as `offset` with that group alone in `types` to
read the next page. Apple reports no totals.

A hit's `url` is ready to submit to `POST /downloads` and downloads exactly that item: a
song's is a `/song/` link, which `catalog.album_track_url_mode` cannot turn into the whole
album. The metadata fields have the names and meaning of the `POST /lookup` fields.
A playlist hit has `type` `playlist`, the curator as `artist_name` and no `track_count`:
the catalog states none. `audio_traits` is Apple's list for the song, album or playlist, such as `lossless`,
`hi-res-lossless`, `atmos` and `spatial`, when the catalog sends one.

`prior_job` names the job that put a hit in the library or holds it in the queue: the newest
queued or running job of the same type and id, else the newest completed one, whatever the
storefront of its link. A song downloaded as part of an album or playlist job is not marked,
and a failed or cancelled job marks nothing. When the job store cannot be read the hits
come back unmarked rather than failing the search.

### Artist albums

```bash
curl 'http://localhost:18080/api/v1/artists/159260351/albums?storefront=us&limit=25'
```

Returns `storefront`, the `artist` as a search hit, and `albums`, one page of the artist's
albums in the order an artist download takes them, as album hits with `next_offset`.
`limit` is 1-100 (default 25). The artist and the page are read at once, so every page
carries the artist.

| Case | Answer |
| --- | --- |
| No `term`, a bad `types`, `storefront`, `limit` or `offset`, an artist id that is not digits | `400 invalid_request` |
| Apple answers 404 (an unknown artist id) | `404 catalog_not_found` |
| The catalog read fails otherwise (an unknown storefront included) | `502 upstream_error` |
| No answer within 10 seconds | `504 upstream_timeout` |

### Album

```bash
curl 'http://localhost:18080/api/v1/albums/1783644603?storefront=cn'
```

```json
{"storefront":"cn","id":"1783644603","url":"https://music.apple.com/cn/album/1783644603",
 "title":"迷跡波","artist_name":"MyGO!!!!!","artist_url":"https://music.apple.com/cn/artist/mygo/1653702916",
 "artwork_url":"https://is1-ssl.mzstatic.com/image/thumb/…/198704278525_Cover.jpg/{w}x{h}bb.jpg",
 "artwork_bg_color":"4e808e","artwork_text_color1":"030607","artwork_text_color2":"131313",
 "artwork_text_color3":"121e22","artwork_text_color4":"1e282b","release_date":"2023-11-01",
 "genre":"动画","track_count":13,"disc_count":1,"apple_digital_master":true,
 "audio_traits":["hi-res-lossless","lossless","lossy-stereo"],
 "tracks":[{"id":"1783644604","url":"https://music.apple.com/cn/song/%E8%BF%B7%E6%98%9F%E5%8F%AB/1783644604",
   "disc_number":1,"track_number":1,"name":"迷星叫","artist_name":"MyGO!!!!!","duration_ms":211373,
   "apple_digital_master":true,"audio_traits":["hi-res-lossless","lossless","lossy-stereo"],
   "has_lyrics":true}, …]}
```

Returns the album's metadata and `tracks`, its songs in disc and track order: the songs an
album download takes, music videos excluded. The metadata fields have the names and meaning
of the `POST /lookup` fields (`track_count` is the length of `tracks`, `disc_count` the
highest `disc_number`), and `url` downloads the whole album when submitted to
`POST /downloads`. A song has `id`, `url` (a `/song/` link that downloads that song alone),
`disc_number` (1 when the catalog sends none), `track_number`, `name` and, when the catalog
has them, `artist_name`, `duration_ms`, `content_rating`, `apple_digital_master`,
`audio_traits` and `has_lyrics`. The whole list comes in one answer; there is no paging.
`storefront` is a two-letter code, default `catalog.default_storefront`.

The response does not say which songs are on disk: where a song lands depends on the
quality and codec its download picks, and only a job records that. To mark the songs an
album job downloaded, read the job (`GET /downloads/{job_id}`, the id from the album hit's
`prior_job`) and match its items' `adam_id` with the songs' `id`; a song downloaded by a
song job or another album's job is not found that way.

| Case | Answer |
| --- | --- |
| An album id that is not digits, a bad `storefront` | `400 invalid_request` |
| Apple answers 404 (an unknown album id, or one the storefront does not have) | `404 catalog_not_found` |
| The catalog read fails otherwise | `502 upstream_error` |
| No answer within 15 seconds | `504 upstream_timeout` |

## Quality probe

```bash
curl -X POST http://localhost:18080/api/v1/quality \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://music.apple.com/cn/album/1580904295"}'
```

Reports the codecs and audio quality a song, album, playlist, artist or station URL
currently declares in its master playlist, without creating a job. It reuses the download
path's region validation, collection resolution, metadata refresh, retries, concurrency
scheduling, HLS source selection and codec/ALAC filtering rules. A song the catalog lists
no Enhanced HLS master for reports every quality with `available: false`. Apple's 404 for
the link's id or a track's is `404 catalog_not_found`; any other failure of the lookup,
including a URL it cannot parse, is `502 upstream_error`.

The success path reads one master playlist per track occurrence and summarises every
quality from it; on failure it re-acquires the source per `download.max_attempts`. It does
not read individual media playlists, verify media segments or enter encrypted media
transfer. Region validation requires the wrapper to be ready and the URL's
storefront to be among the regions it reports.

All link types return per-track `tracks`; a song returns one element, collections keep
Apple Music's original order and duplicate track occurrences. Stations use the wrapper's
media-user-token when the backend can read it, else the runtime `catalog.media_user_token`.

## Config and hooks

```bash
curl http://localhost:18080/api/v1/config
curl -X POST http://localhost:18080/api/v1/config/reload
```

`GET /api/v1/config` returns the runtime-mutable keys of the in-memory config and has no
side effects; it does not read the file. `catalog.media_user_token` is redacted in every
config response: a set token reads `...` and its last four characters. A `PUT` whose value
starts with `...` leaves the saved token as it is. Next to `config`, `persisted` says whether a
`PUT` is written to the file. `POST /api/v1/config/reload` re-reads `configs/config.yaml`
and applies its runtime keys, then answers like `GET`. When the file is unreadable or
invalid, the previous config stays active and the answer is still `200`, with
`reload_error` saying why.

`PUT /api/v1/config` changes runtime-mutable keys only, but rewrites the entire file
including startup keys — comments and custom formatting are not preserved. It re-reads the
file first, so a hand edit made since the last reload is kept. Errors: `422
config_key_not_mutable` (a startup key), listing the dotted key names in `details.keys`;
`422 invalid_config` (the result fails validation, or the file on disk no longer
validates); `400 invalid_request` for an unknown key. See [configuration.md](configuration.md).

`GET /api/v1/hooks` returns the master switch plus each entry's name, enabled state, type,
events and job types — never URLs, headers, commands or working directories.

## Library sync

```bash
curl http://localhost:18080/api/v1/library-sync
```

Watcher status: enabled flag, interval, anchor entry count and the last pass's result.
It answers even while the feature is disabled, so a client never has to guess.

```bash
curl -X POST http://localhost:18080/api/v1/library-sync/reset
```

Clears the anchor. The next pass re-anchors to the library **as it stands then** and
submits **nothing** — this is "forget history", not "replay history". To re-download an
album, `POST /api/v1/downloads` it. See [automation.md](automation.md).
