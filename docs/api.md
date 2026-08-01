# API

Examples assume the service listens on `http://localhost:18080`.

- Interactive Swagger UI: <http://localhost:18080/docs>
- OpenAPI 3.1 spec: <http://localhost:18080/api/openapi.yaml> (source:
  [`internal/api/openapi.yaml`](../internal/api/openapi.yaml))

The spec is the source of truth for every request and response shape. The iOS and web
clients mirror those JSON shapes by hand, so a response-shape change here is normally a
multi-repository change.

## System

```bash
curl http://localhost:18080/api/v1/health
```

Returns `{"status":"ok"}`, or `503` with `{"status":"degraded","database_error":"…"}`
when the SQLite store cannot be reached. This is what the container health check uses.

```bash
curl http://localhost:18080/api/v1/developer-token
```

Mints a shareable Apple Music developer token. Available only when local signing is
enabled (all three `catalog.apple_music_*` keys set); otherwise it returns `409`. The
token's lifetime is `catalog.developer_token_ttl_hours` and its `origin` claim comes from
`catalog.allowed_origins`. This is a different token from the backend's own internal one,
which always lasts 24 hours.

### Logs

```bash
curl 'http://localhost:18080/api/v1/logs?level=error&limit=100'
```

```bash
curl -N 'http://localhost:18080/api/v1/logs/stream?job_id=job_01JZ0000000000000000000000'
```

The SSE `id` works as `Last-Event-ID` on reconnect. A WebSocket equivalent is available
at `GET /api/v1/logs/stream/ws`.

Every HTTP response carries an `X-Request-ID`. Send your own header of the same name and
`GET /api/v1/logs?request_id=<id>` aggregates that request's access log with the
synchronous job operations it triggered.

In-memory retention is `logging.buffer_size`; at `0` no history is kept but the live SSE
stream still pushes new records. File output is off by default and rotates by
`max_size_mb` / `max_backups` / `max_age_days`, optionally gzipped. `logging.level` and
`logging.access_log` are runtime-mutable; format, destination, buffer size and rotation
need a restart.

## Wrapper

```bash
curl http://localhost:18080/api/v1/wrapper/status
```

The gRPC connection is lazy — the backend starts fine without a reachable wrapper, and
this endpoint is how you verify connectivity.

```bash
curl -X POST http://localhost:18080/api/v1/wrapper/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"apple-id@example.com","password":"password"}'
```

If the response contains `"status":"needs_2fa"`, submit the code within 30 seconds using
the `login_id` from that response:

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

## Downloads

### Create

```bash
curl -X POST http://localhost:18080/api/v1/downloads \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://music.apple.com/us/album/example/123456789?i=987654321",
       "overrides":{"force_overwrite":true}}'
```

Takes either `url` or `urls`. Each input is reported back with its own submit status:
`accepted`, `invalid`, `duplicate_in_request`, `duplicate_active` or `queue_full`.

For album links carrying `?i=<song_id>`, `catalog.album_track_url_mode` decides the task
type: `song` (default) treats it as a single-song job using the `i` parameter, `album`
ignores `i` and downloads the whole album.

### Job overrides

`overrides` overlays the job-mutable runtime config for that submission only. Omitted
fields keep the global value.

| Field | Notes |
| --- | --- |
| `quality_priority`, `codec_alternative`, `memory_mode`, `max_attempts` | Per-batch pipeline behaviour |
| `downloads_dir`, `temp_dir`, `*_path_format` | Per-batch output destination |
| `cover_size`, `cover_format`, `embed_cover`, `save_album_cover`, `save_artist_cover`, `save_playlist_cover` | Artwork |
| `embed_lyrics`, `save_lyrics_file`, `lyrics_format`, `lyrics_type`, `lyrics_extras` | Lyrics |
| `alac_max_sample_rate`, `alac_max_bit_depth`, `check_integrity` | Audio limits and the integrity gate |
| `force_overwrite` | Overwrite existing output files and their lyrics sidecars instead of skipping |
| `hooks` | Allow-list of hook entry names |
| `media_user_token` | Apple Music subscription token for stations and private playlists |

The old top-level `force` and `media_user_token` request fields were removed — sending
them now returns `400` as unknown fields.

**`force_overwrite`** — `true` overwrites an existing audio file and its lyrics sidecar;
`false` (the default, from `download.force_overwrite`) skips tracks whose output already
exists.

**`hooks`** — values are `name`s from [`configs/hooks.yaml`](../configs/hooks.yaml). Call
`GET /api/v1/hooks` first to build the list; it returns each entry's name, enabled state,
type, events and job types, and never returns URLs, headers, commands or working
directories. Omitting the field keeps the default (every enabled, matching hook may run);
`[]` runs no hooks for this batch; a non-empty list restricts to those names. The list
cannot enable a `enabled: false` entry and never bypasses `events` / `job_types` matching.
Duplicate names behave like one entry; unknown names return `422` when any hook entry is
configured. The selection is persisted with the job: the `job_queued` hook dispatches only
on first submission (never on retry or restart recovery), and terminal-event hooks
arriving after a retry or restart stay bound to the same allow-list.

**`media_user_token`** — three-state: omitted uses the global
`catalog.media_user_token` fallback, a non-empty string uses that value for this batch,
and an explicit `""` clears the fallback for this batch.

The token is persisted only onto the jobs that actually need it — stations and private
playlists (`pl.u-xxx`). Songs, albums, artists and other playlists in the same batch do
not store it. It is cleared when a job completes or is cancelled, and retained on failure
so a retry can still resolve. It is never echoed back in create, list, detail, SSE or
WebSocket responses. The global fallback in `catalog.media_user_token` does persist to the
config file and may be returned by `GET /api/v1/config`.

`catalog.media_user_token_priority` is kept only for old configs; it is deprecated and no
longer takes part in selection.

### Radio stations

Station links (`https://music.apple.com/us/station/.../ra.xxxx`) are supported when they
resolve to a track list — personalised and curated stations. They need an Apple Music
subscription token, per job via `overrides.media_user_token` or globally via
`catalog.media_user_token`.

```bash
curl -X POST http://localhost:18080/api/v1/downloads \
  -H 'Content-Type: application/json' \
  -d '{"urls":["https://music.apple.com/us/station/example/ra.978194965"],
       "overrides":{"media_user_token":"<your media-user-token>"}}'
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

```bash
curl 'http://localhost:18080/api/v1/downloads?limit=20&offset=0&status=failed,cancelled&type=album&storefront=cn&q=beta&created_after=2024-07-01&sort=updated_at&order=desc'
```

Parameters: `limit` (default 50, max 200), `offset`, `status`, `type`, `storefront`, `q`,
`created_after`, `created_before`, `updated_after`, `updated_before`, `sort`
(`created_at` | `updated_at`), `order` (`asc` | `desc`). Multi-value parameters accept
repeated keys or comma-separated values. The response adds `total`, `limit` and `offset`.

```bash
curl -X POST http://localhost:18080/api/v1/downloads/{job_id}/cancel
```

```bash
curl -X POST http://localhost:18080/api/v1/downloads/{job_id}/retry
```

Only `failed` jobs can be retried. A non-failed job, one still finishing its previous run,
or one whose canonical key is already running returns `409`.

```bash
curl -X DELETE http://localhost:18080/api/v1/downloads/{job_id}
```

Deletes a terminal job and its records.

## Events

```bash
curl -N http://localhost:18080/api/v1/downloads/{job_id}/events
```

`GET /api/v1/downloads/{job_id}/events/ws` is the equivalent WebSocket stream for clients
that prefer WS.

`GET /api/v1/downloads/events` (and `/events/ws`) is the cross-job overview feed: it
pushes only the milestones that change how a job appears in the list, so a client can
render the whole downloads page without subscribing to each job.

In per-job streams, `payload` is a JSON **string** that needs a second parse. Job
lifecycle events carry the same public snapshot fields as the REST `Job`; item events
carry the same fields as the REST `JobItem`, plus event-only fields such as
`download_attempts` and `will_retry`. So after one initial `GET`, a client can merge
subsequent events directly — no re-fetch is needed on `item_completed`, `item_failed`,
`item_skipped` or job-terminal events. `media_user_token` stays hidden here exactly as in
every other response.

Event types you will see:

| | |
| --- | --- |
| Job lifecycle | `job_queued`, `job_started`, `job_retried`, `job_recovered`, `job_finished`, `job_failed`, `job_cancelled`, `job_deleted` |
| Resolution | `resolved_input`, `motion_artwork_resolved` |
| Item progress | `item_progress` (its `phase` is the item status), `item_completed`, `item_failed`, `item_skipped`, `item_overwrite` |
| Codec and retries | `codec_selected`, `codec_fallback`, `codec_failed`, `codec_retry`, `codec_exhausted`, `codec_recovered`, `operation_retry`, `operation_exhausted`, `operation_recovered`, `alac_repaired` |
| Hooks | `hook_started`, `hook_succeeded`, `hook_failed` |
| Other | `standalone_cover_failed` |

`download.progress_event_interval_ms` (default 500, range 0–10000) is the minimum gap
between two `item_progress` events for the same item in the same state. It throttles only
the download and decrypt meters; status changes, stage marks and one-shot events always
publish immediately. A value held back by the interval is flushed just before whatever
event supersedes it, so the last percentage before a state change is never lost and a
client resuming from `last_event_id` sees the same sequence as a live one. It is read once
when a job starts.

## Quality probe

```bash
curl -X POST http://localhost:18080/api/v1/quality \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://music.apple.com/cn/album/1580904295"}'
```

Reports the codecs and audio quality a song, album, playlist, artist or station URL
currently declares in its master playlist, without creating a job. It reuses the download
path's region validation, collection resolution, metadata refresh, retries, concurrency
scheduling, HLS source selection and codec/ALAC filtering rules.

The success path reads one master playlist per track occurrence and summarises every
quality from it; on failure it re-acquires the source per `download.max_attempts`. It does
not read individual media playlists, verify media segments or enter encrypted media
transfer. Region validation requires the wrapper/decryptor to be ready and the URL's
storefront to be among the regions it reports.

All link types return per-track `tracks`; a song returns one element, collections keep
Apple Music's original order and duplicate track occurrences. Stations use the runtime
`catalog.media_user_token`.

## Config and hooks

```bash
curl http://localhost:18080/api/v1/config
```

`PUT /api/v1/config` changes runtime-mutable keys only, but rewrites the entire file
including startup keys — comments and custom formatting are not preserved. See
[configuration.md](configuration.md).

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
