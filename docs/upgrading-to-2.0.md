# Upgrading to 2.0

> Type: runbook

2.0 changes the HTTP contract, removes 1.x upgrade shims and migrates the database. The
target design is in [Architecture](architecture.md). The embedded console ships with
the new API; any other client of `internal/api/openapi.yaml`, such as your scripts,
needs the API changes below before it works against 2.0.

## Before you upgrade

1. Stop the container and back up the database file (`database.path`, default
   `data/db/amdl.db`). The first 2.0 start migrates it in place and 1.x cannot open the
   result.
2. If you still have `configs/runtime.yaml`, start 1.x once so it merges the file, or
   copy its keys into `config.yaml` by hand: 2.0 leaves the file alone and never reads
   it.
3. Move every `AMDL_*` variable except `AMDL_CONFIG`, `AMDL_HOOKS_CONFIG`,
   `AMDL_SERVER_LISTEN`, `AMDL_WRAPPER_ADDRESS`, `AMDL_WRAPPER_DATA_DIR` and
   `AMDL_DATABASE_PATH` into
   `config.yaml` (for example `AMDL_SIMULATE_ENABLED=true` becomes `enabled: true` under
   `simulate:`), then unset it: 2.0 ignores the rest and logs a warning naming each one.
4. Remove the keys listed as removed under [Config and environment](#config-and-environment)
   from `config.yaml` if present, and the removed keys under [Hooks](#hooks) from
   `hooks.yaml` (`timeout_seconds` and `max_concurrent` at the top level,
   `send_payload` and `timeout_seconds` in entries): 2.0 rejects them at startup.
5. If `config.yaml` omits any key in the defaults table below, decide whether you want
   the new default or should write the old value into the file.
6. Give the container time to stop: `stop_grace_period: 2m` in Compose or
   `--stop-timeout 120` with `docker run`. Interrupted jobs now resume on the next start
   instead of being cancelled.
7. Replace the wrapper-manager fork with an upstream wrapper (see
   [Wrapper](#wrapper) and [Deployment](deployment.md#the-wrapper)): either
   [wrapper-manager v2](https://github.com/WorldObservationLog/wrapper-manager/tree/v2),
   which fronts wrapper-lite over HTTP and has the login API the console's Account page
   uses, or a bare
   [wrapper-lite](https://github.com/WorldObservationLog/wrapper/tree/lite), logged in
   with its own `--login`. Set `wrapper.address` (or `AMDL_WRAPPER_ADDRESS`) to its URL,
   such as `http://wrapper-manager:8080` or `http://127.0.0.1:12340`. Without Docker, the
   `amdl` binary can also run wrapper-lite itself in a QEMU guest on the backend's machine
   (`amdl wrapper install`, [Install](install.md#a-wrapper-amdl-runs)), which sets
   `wrapper.address` for you and has a login of its own. 2.0 no longer speaks the fork's
   gRPC and decrypts media itself.

## Upgrade checklist

1. Back up the database (step 1 above) and stop the container.
2. In `configs/config.yaml`, delete every key 2.0 rejects at startup:
   - `wrapper.insecure`;
   - `catalog.apple_music_private_key_path`, `catalog.apple_music_key_id`,
     `catalog.apple_music_team_id`, `catalog.signed_mode_hls_source`,
     `catalog.developer_token_ttl_hours`, `catalog.allowed_origins`;
   - `catalog.requests_per_second`, `catalog.request_burst`;
   - `download.max_parallel_decrypts`, `download.max_parallel_wrapper_requests`;
   - `logging.format`, `logging.include_source`, `logging.file_enabled`,
     `logging.file_path`, `logging.max_size_mb`, `logging.max_backups`,
     `logging.max_age_days`, `logging.compress`;
   - `catalog.media_user_token_priority`, if still present.
3. Rename or rewrite what changed shape:
   - `wrapper.address`: `host:port` becomes `http://host:port` or `https://host:port`;
   - `download.save_lyrics_file` and `download.lyrics_format` become the list
     `download.lyrics_files`; `download.lyrics_type` becomes `download.lyrics_timing`
     (`lyrics` is `line`, `syllable-lyrics` is `word`).
4. In the Compose file:
   - add the wrapper service (wrapper-manager v2 or wrapper-lite) from
     [`deploy/docker-compose.yml`](../deploy/docker-compose.yml), or keep a wrapper on the host;
   - set `AMDL_WRAPPER_ADDRESS` to its URL, for example `http://wrapper-manager:8080`
     (a `host:port` value fails startup);
   - drop the `./data/logs` mount and any `.p8` key mount;
   - keep `stop_grace_period: 2m`;
   - remove every other `AMDL_*` variable except the six 2.0 reads;
   - make sure the host downloads directory is writable by `PUID:PGID`: the 2.0
     entrypoint re-owns only the backend's own state and no longer changes the
     downloads directory's owner ([Deployment](deployment.md#ownership));
   - if a `command:`, `entrypoint:` or health check names `/app/amdl-api`, change it to
     `/app/amdl serve`, and if you build the image yourself, see
     [Image and source tree](#image-and-source-tree).
5. Check the wrapper host: `unshare -Ur true` must succeed, or use a QEMU guest (see [Wrapper](#wrapper)).
6. Start 2.0, then confirm `GET /api/v1/wrapper/status` reports `ready` and a region, and
   read `login_supported` to know whether the console can log in.

## HTTP API

| Area | 1.x | 2.0 | Affects |
| --- | --- | --- | --- |
| Error body | `{"error": "text"}` | `{"error": {"code", "message", "details?"}}`; codes are the spec's `ErrorCode` enum (`GET /health` keeps its `503` body) | every client error handler |
| Unknown route, wrong method | `http.ServeMux` plain text `404` / `405` | JSON `404 route_not_found`; `405 method_not_allowed` with an `Allow` header | generic error handlers |
| Job lookup on a store error | `404` (`GET /downloads/{id}`, its stream) | `500 store_error`; `404 job_not_found` only for a missing job | job detail and stream clients |
| Submit result error | `results[].error: "code: message"`, only on some rejections | `results[].error: {code, message, details?}` on every result that is not `accepted`; codes are the spec's `SubmitErrorCode` enum | submit flows |
| Submit store failure | `status: "invalid"` with the raw error text | `status: "error"`, `error.code: "store_error"` (retry the submit) | submit flows, status enums |
| Submit, nothing accepted | `422` with the batch body | `422 no_urls_accepted` (or `500 store_error`) envelope; the batch body is in `error.details` | submit flows |
| Wrapper login already pending | `401` (wrapper-manager's `login already pending` reply) | `409 login_pending`; a two-step code already being verified is also `login_pending` | account screens |
| Wrapper login timeout | `504` | `504 upstream_timeout` | account screens |
| Wrapper login without a login API | not applicable (wrapper-manager always had one) | `501 login_unsupported` from login, two-step and logout when `wrapper.address` is a bare wrapper-lite; log in with its `--login` instead | account screens |
| Wrapper logout | listed accounts to pick from | the request names the Apple ID; no wrapper lists its accounts | account screens |
| Wrapper login password | trimmed | sent as typed; only an all-whitespace password is `400` | account screens |
| Apple has no such id | `502` from `POST /quality` | `404 catalog_not_found` from `POST /quality`, `POST /lookup` and `GET /artists/{id}/albums`; other catalog failures stay `502 upstream_error` | quality and link-preview clients |
| Quality of a song with no Enhanced HLS master | `502` | `200`, every quality `available: false` | quality clients |
| `catalog.media_user_token` in config responses | the token in full on `GET /config`, `PUT /config` and `POST /config/reload` | redacted: `...` and its last four characters, `""` when unset; a `PUT` of a value starting with `...` keeps the saved token | scripts that read the token back from the API |
| Cancel a finished job | `200 {"status":"cancelled"}` whatever the job's state | a `completed` or `failed` job is `409 job_not_cancellable`; a `cancelled` one is still `200` | scripts and clients that cancel |
| Config `PUT` with an invalid file on disk | `500` | `422 invalid_config` | settings screens |
| Logs with `logging.buffer_size: 0` | `GET /logs` an empty `200`; `GET /logs/stream` live records only | `503 feature_unavailable` on both | log viewers |
| Stream cursor in snapshots | `last_event_id` on `GET /downloads` and `GET /downloads/{id}` | `event_cursor` | stream clients |
| Job counters | `done_items` (completed + skipped), `failed_items` | `completed_items`, `skipped_items`, `failed_items`, `cancelled_items` | job lists, progress bars |
| Job timing | none | `started_at`, `finished_at` | new |
| Job `force` | legacy boolean | removed; use `overrides.force_overwrite` | submit forms |
| Event `payload` | JSON string (parse twice) | JSON object | per-job event consumers |
| WebSocket streams | `/logs/stream/ws`, `/downloads/events/ws`, `/downloads/{id}/events/ws` | removed; use the SSE endpoints | cross-origin clients |
| Stream resume | header only on SSE; `last_event_id` / `after` query on WS and logs | `Last-Event-ID` header or `?cursor=` on every stream (the header wins); a malformed cursor is `400 invalid_request` instead of being read as `0` | stream clients |
| Logs paging | `after`, `next_cursor` | `cursor`, `next_cursor` | log viewers |
| Per-job stream already complete | `409` | `204 No Content` | stream clients |
| Never-emitted event types | `codec_retry`, `codec_exhausted`, `codec_recovered` documented | removed from the docs; codec retries are `operation_*` with `phase` = codec | event handlers |
| Wrapper status | `accounts_supported`, `accounts` omitted when empty, `regions` `null` when empty | `accounts_supported` removed; `accounts` and `regions` always arrays, and `accounts` stays empty because no 2.0 wrapper names its accounts; `login_supported` says whether the wrapper has a login (wrapper-manager v2 or a wrapper amdl runs), and `managed` describes a wrapper amdl runs | account screens |
| Wrapper logged in | `status` on `GET /wrapper/status` (true when an account is logged in) | renamed `logged_in` | account screens, scripts that check the login |
| Wrapper client count | `client_count` on `GET /wrapper/status` (wrapper-manager's count of connected clients) | removed; the response is `ready`, `logged_in`, `regions`, `accounts`, `login_supported` | account screens that showed the count |
| `GET /config` | re-read the file and applied it | pure read; use `POST /config/reload` | settings screens |
| Log records | `source` (file and line) when `logging.include_source` was on | no `source` field | log viewers |
| `PUT /config` with a removed key | `logging.format`, the log file keys, `catalog.requests_per_second`, `catalog.request_burst`, `download.max_parallel_decrypts`, `download.max_parallel_wrapper_requests` were startup keys, and changing one was a `422` with a plain-text body | `400 invalid_request` naming the unknown field, like any removed key | settings screens |
| `GET /config` and `PUT /config` lyrics and catalog fields | `download.save_lyrics_file`, `download.lyrics_format`, `download.lyrics_type` and `catalog.signed_mode_hls_source` (enum `wrapper`, `web_token`) were runtime fields | the three lyrics fields are replaced by `download.lyrics_files` (array of `lrc`, `ttml`) and `download.lyrics_timing` (`line`, `word`); `catalog.signed_mode_hls_source` is gone; `PUT` with an old field is `400 invalid_request` naming it | settings screens, scripts that PUT lyrics settings |
| Env-pinned keys | an `AMDL_*` variable could set any runtime key; `PUT /config` refused a key one set with a `422` and a plain-text body, and `GET /config` did not say which keys were set that way | no environment variable can set a runtime key any more, so `PUT /config` accepts every runtime key | settings screens |
| Developer token | `GET /developer-token` signed a token for other clients in local signing mode (a `409` with a plain-text body in legacy mode) | removed (`404 route_not_found`); the backend no longer signs any token and reads the catalog with the web player's token | browser players that fetched a token |
| API reference | Swagger UI at `/docs` (from unpkg.com) and the spec at `/api/openapi.yaml` | neither is served (`404`); read `internal/api/openapi.yaml` and `docs/api.md` in the repository | API explorers, client generators that fetched the spec |
| CORS | `Access-Control-Allow-Origin: *` on every response; `OPTIONS` answered `204` | no CORS headers; `OPTIONS` under `/api/` is `405 method_not_allowed`. Only same-origin pages (the console) and non-browser clients can call the API | web clients on another origin, such as amdl-web |
| Request id | a well-formed incoming `X-Request-ID` was adopted and echoed | always generated by the server; read it from the response to filter `GET /logs?request_id=` | scripts that set their own id |
| `GET /downloads` filters | `storefront`, `created_after`, `created_before`, `updated_after`, `updated_before` | removed and ignored like any unknown parameter: the response is no longer narrowed by them (`status`, `type` and `q` still filter) | scripts that filtered by storefront or date |
| Submit `overrides` | overlaid the whole job-mutable `download` section (directories, path templates, lyrics, covers, `memory_mode`, `max_attempts`, `check_integrity`) plus `media_user_token` and a `hooks` allow-list | only `quality_priority`, `codec_alternative`, `alac_max_sample_rate`, `alac_max_bit_depth`, `force_overwrite`; any other field is `400 invalid_request` naming it. Set the rest in the runtime config; every enabled matching hook runs | scripts that sent per-batch folders, lyrics or cover settings, a token or a hook list |
| Media-user-token per request | `overrides.media_user_token` on `POST /downloads` and `media_user_token` on `POST /lookup` overlaid `catalog.media_user_token` (`""` cleared it) | removed (`400 invalid_request` naming the field); downloads and lookups use the backend's own token: the wrapper's when the backend can read it, else `catalog.media_user_token` | scripts that passed a token per request |
| Job `overrides` in responses and events | could hold every field above; the token was redacted | holds only the five fields above | job detail clients |
| Spec version | `info.version: 1.0.0` | `2.0.0`; the URL prefix stays `/api/v1` | client generators that key on `info.version`; requests are unchanged |
| New | | `GET /system` (with `default_storefront`; it reports no signing mode, because the backend signs no token), `GET /stats`, `POST /config/reload`, `POST /lookup` (a link preview that creates no job), `GET /search`, `GET /artists/{id}/albums` and `GET /albums/{id}` (catalog search, an artist's albums and an album's songs), web console at `/console/` (`/` redirects there) | none: additions only |

## Config and environment

- Environment overrides: 1.x read `AMDL_<SECTION>_<KEY>` for every key. 2.0 reads six
  variables: `AMDL_CONFIG`, `AMDL_HOOKS_CONFIG` and the overrides `AMDL_SERVER_LISTEN`,
  `AMDL_WRAPPER_ADDRESS`, `AMDL_WRAPPER_DATA_DIR`, `AMDL_DATABASE_PATH`. Every other `AMDL_*` variable, such as
  `AMDL_SIMULATE_ENABLED`, `AMDL_DOWNLOAD_DOWNLOADS_DIR` or `AMDL_LOGGING_LEVEL`, is
  ignored, and startup logs one warning per variable; set the key in `config.yaml`
  instead. That covers the removed `AMDL_RUNTIME_CONFIG` (a leftover `runtime.yaml` is
  ignored), `AMDL_LISTEN` and `AMDL_WRAPPER_ADDR`.
- SIGHUP no longer reloads the config file; the backend ignores it. Use
  `POST /config/reload` (the console's "Reload from file") after a hand edit;
  `docker compose kill -s SIGHUP amdl-backend` no longer does anything.
- `catalog.media_user_token_priority` is rejected in the file and in a `PUT /config` body
  (`400`).
- Logging writes only to stdout and the in-memory buffer. `logging.format` (always text
  now), `logging.include_source`, and the log file keys `logging.file_enabled`,
  `logging.file_path`, `logging.max_size_mb`, `logging.max_backups`,
  `logging.max_age_days` and `logging.compress` are rejected in the file; delete them and
  collect stdout (`docker logs`) to keep logs. The `./data/logs` mount is no longer used.
- `catalog.requests_per_second` (10), `catalog.request_burst` (16) and
  `download.max_parallel_wrapper_requests` (4) are fixed at those values, and
  `download.max_parallel_decrypts` at the number of CPUs the backend uses (`GOMAXPROCS`,
  which follows a container CPU limit). All four are rejected in the file; delete them.
  `catalog.max_parallel_requests`, `download.max_parallel_downloads` and
  `download.max_running_jobs` remain keys.
- `catalog.developer_token_ttl_hours` and `catalog.allowed_origins` configured the removed
  `GET /developer-token`. Both are rejected in the file; delete them.
- `catalog.apple_music_private_key_path`, `catalog.apple_music_key_id`,
  `catalog.apple_music_team_id` and `catalog.signed_mode_hls_source` are rejected in the
  file; delete them. The backend no longer signs a developer token. Every catalog read
  goes to `amp-api.music.apple.com` with the token scraped from `music.apple.com`, which
  is what an install with the three signing keys empty already did, so such an install
  sees no change. An install that set the keys now reads the web player's `enhancedHls`
  from the catalog response and no longer asks the wrapper for a manifest per track: only a
  `hi-res-lossless` track with ALAC and `download.alac_max_sample_rate` above 48000 or at 0 or less
  (no ceiling) takes the wrapper's device manifest, remembered for 12 hours. Remove any `.p8` key mount.
  `catalog.token_cache_ttl_hours` now applies to every install.
- Built-in defaults now equal the values in `configs/config.example.yaml`, so a key
  missing from `config.yaml` behaves the way the example documents. The exceptions are
  `download.album_path_format` and `download.artist_path_format`: a config without them
  predates `{SongNumber}` paths, and keeps `{TrackNumber}` so existing files are found.
  Defaults that changed for keys missing from `config.yaml`:

  | Key | 1.x default | 2.0 default |
  | --- | --- | --- |
  | `wrapper.address` | `127.0.0.1:8080` | `http://127.0.0.1:12340` |
  | `AMDL_WRAPPER_ADDRESS` in the image, when Compose does not set it | `host.docker.internal:8080` | `http://host.docker.internal:12340` |
  | `catalog.default_storefront` | `us` | `cn` |
  | `catalog.language` | `en-US` | `zh-Hans_CN` |
  | `download.quality_priority` | `[alac, aac]` | `[alac]` |
  | `download.max_running_jobs` | `2` | `3` |
  | `download.max_attempts` | `4` | `3` |
  | `download.save_album_cover`, `save_artist_cover`, `save_playlist_cover` | `false` | `true` |
- A hand edit to `config.yaml` is no longer applied by `GET /config`; send
  `POST /config/reload`. `PUT /config` re-reads the file first, so it keeps hand edits.
- An invalid webhook URL in `hooks.yaml` fails startup instead of failing every run.
- `download.cover_size` must be `{width}x{height}` in pixels. 1.x accepted any string,
  empty included, and silently fell back to the standard sizes when Apple could not
  serve it; 2.0 fails startup and `PUT /config` (`422 invalid_config`). Set it in
  `config.yaml` to a size such as `5000x5000`.
- `download.*_path_format` templates must stay relative to `download.downloads_dir`
  and may carry only the `:02d` format spec. 1.x stored absolute templates, `..`
  segments and other `{Variable:spec}` forms verbatim; expansion sanitized them into
  `_` folders or literal braces rather than escaping the downloads folder, so nothing
  was ever written outside it, but the result never matched the intent. 2.0 fails
  startup and `PUT /config` (`422 invalid_config`) on them. Fix the template to a
  relative path.
- Lyrics keys: `download.save_lyrics_file` and `download.lyrics_format` became the list
  `download.lyrics_files` (`save_lyrics_file: true` with `lyrics_format: lrc` is
  `[lrc]`, with `ttml` it is `[ttml]`, `false` is `[]`; both can be written now), and
  `download.lyrics_type` became `download.lyrics_timing` (`lyrics` is `line`,
  `syllable-lyrics` is `word`). The old keys are rejected at startup. `lyrics_extras`
  keeps its values and now shapes LRC only. Lyrics come from the wrapper account's
  storefront, whatever the job's, with the translation in `catalog.language`.

## Image and source tree

The repository holds every AMDL product, so it was renamed to `AMDL-Web/apple-music-downloader`
and the server's entry point, Dockerfile and Compose file moved. The paths inside the
image (`/app/configs`, `/app/data`, the entrypoint), the port, the environment variables and
the Compose service name stay as they were.

| Area | 1.x | 2.0 | Affects |
| --- | --- | --- | --- |
| Image | `ghcr.io/amdl-web/apple-music-downloader-backend` | `ghcr.io/amdl-web/apple-music-downloader`; 1.x tags stay at the old name | every Compose `image:` or `docker pull` that names the published image |
| Repository | `github.com/AMDL-Web/apple-music-downloader-backend` | `github.com/AMDL-Web/apple-music-downloader`; GitHub redirects the old URL | clones and links; update a clone's remote with `git remote set-url origin` |
| Server binary | `/app/amdl-api` in the image; `go run ./cmd/amdl-api` from source | `/app/amdl serve`, the `serve` command of the one `amdl` binary; `go run ./apps/amdl serve` | a Compose `command:` or `entrypoint:`, `docker exec` or a process monitor that names the binary; the image's entrypoint starts it, so the shipped Compose file needs no change |
| Compose file | `docker-compose.yml` at the repository root | `deploy/docker-compose.yml` | anyone who downloads it from the repository or runs Compose inside a clone, where its relative `./configs` and `./data` now live under `deploy/`; a copy in its own directory is unaffected |
| Dockerfile | `Dockerfile` at the root: `docker build .`, Compose `build: .` | `deploy/server/Dockerfile`, still with the repository root as context: `docker build -f deploy/server/Dockerfile .`; Compose from `deploy/` uses `context: ..` and `dockerfile: deploy/server/Dockerfile` | anyone who builds the image from source |

## Wrapper

| Area | 1.x | 2.0 | Affects |
| --- | --- | --- | --- |
| Wrapper service | the wrapper-manager fork over gRPC; it decrypted samples | wrapper-lite over HTTP; it returns a key template per key URI and the backend decrypts | deployments |
| `wrapper.address` | gRPC `host:port` | wrapper URL (wrapper-lite or wrapper-manager v2), `http://host:port` or `https://host:port`; anything else, in the file or in `AMDL_WRAPPER_ADDRESS`, fails startup | `config.yaml`, Compose files |
| `wrapper.insecure` | `true` for plaintext gRPC, `false` for TLS | rejected in the file; the URL scheme picks plaintext (`http`) or TLS 1.2+ (`https`) | `config.yaml` |
| `wrapper.timeout_seconds` | bound on a unary RPC and on each decrypt-fragment wait | bound on one wrapper HTTP request; decryption has no wrapper timeout | `config.yaml` |
| Where the wrapper runs | whatever your wrapper-manager deployment needed | `wrapper-lite-rootless`, which both wrapper-lite's Docker image and wrapper-manager v2 run, needs user namespaces and `chroot`: `unshare -Ur true` must succeed on the wrapper host, and a Docker container needs `privileged: true` or a seccomp profile that allows them. A host without them uses wrapper-manager v2's QEMU variant, which needs `/dev/kvm` ([deployment guide](deployment.md#the-wrapper)), or, without Docker, a QEMU guest the `amdl` binary runs ([Install](install.md#a-wrapper-amdl-runs)) | wrapper hosts, Compose files |
| Logging in from the console | `POST /wrapper/login`, `/login/{login_id}/2fa` and `/logout` went to the fork's gRPC login | they work when `wrapper.address` is a wrapper-manager v2, whose `POST /login` and `POST /logout` the backend forwards to, and with a wrapper amdl runs, which logs in by booting its guest; against a bare wrapper-lite they are `501 login_unsupported` and the console's Account page shows the wrapper's `--login` command instead. `GET /wrapper/status` reports which one it is in `login_supported` | the console's Account page, scripts that log in through the API |
| A wrapper amdl runs | none | `amdl wrapper install` downloads upstream's guest and the backend runs it in QEMU on its own machine, with `wrapper.address` pointing at it (`amdl serve --local`, `amdl service install`, the desktop app); see [Install](install.md#a-wrapper-amdl-runs) | new: binary installs without Docker |
| Wrapper requests | gRPC calls, with `download.max_parallel_wrapper_requests` bounding them | HTTP; 4 lyrics, web playback and license requests and 2 `/key` and `/m3u8` requests at a time, fixed in the code | `config.yaml` files that set `download.max_parallel_wrapper_requests`, which is rejected (see [Config and environment](#config-and-environment)) |

## Hooks

| Area | 1.x | 2.0 | Affects |
| --- | --- | --- | --- |
| Job counters in the payload | `job.done_items` (completed + skipped), `job.failed_items` | `job.completed_items`, `job.skipped_items`, `job.failed_items`, `job.cancelled_items`, the API's counters | webhook receivers |
| Job counters in the exec environment | `AMDL_DONE_ITEMS`, `AMDL_FAILED_ITEMS` | `AMDL_COMPLETED_ITEMS`, `AMDL_SKIPPED_ITEMS`, `AMDL_FAILED_ITEMS`, `AMDL_CANCELLED_ITEMS` | exec scripts |
| Job timing in the payload | none | `job.started_at`, `job.finished_at` (RFC 3339, absent until set) | new |
| Output paths | as stored, relative when `download.downloads_dir` is (the default `data/downloads`) | absolute, in the payload's `items[].output_path` and `AMDL_OUTPUT_PATHS` | scripts and media servers that resolved them |
| Exec environment | the whole backend environment, `AMDL_*` overrides included (for example `AMDL_CATALOG_MEDIA_USER_TOKEN`) | the backend environment without any `AMDL_*` variable, plus the hook variables; pass what a script needs explicitly | exec scripts |
| Failed exec hook message | all of stderr | the last 4 KiB of stderr | hook status views |
| Restart while a job runs | the job ended `cancelled` and fired `job_cancelled` | no hook: the job resumes on the next start and fires its terminal hook when it finishes | `job_cancelled` receivers |
| Hook selection per submission | `overrides.hooks` limited a batch to named entries (`[]` ran none) | removed; every enabled entry whose `events` and `job_types` match runs for every job. `GET /api/v1/hooks` still lists the entries | scripts that turned hooks off for a batch |
| Hook limits | top-level `timeout_seconds` (default 30) and `max_concurrent` (default 2) in `hooks.yaml` | removed, fixed at 30 seconds per attempt and 2 executions at once; a file that still has either fails startup naming it | `hooks.yaml` files that set either |
| Per-entry `timeout_seconds` | overrode the top-level timeout for one hook | removed; every attempt is cut off after 30 seconds; a file that still has it fails startup naming the entry | `hooks.yaml` entries that set it |
| Webhook `send_payload` | `send_payload: false` sent an empty body | removed; the body is the JSON payload unless the entry's `body` template sets another, and a receiver that ignores the body ignores it; a file that still has it fails startup naming the entry | `hooks.yaml` webhook entries that set it |
| Shutdown while a hook retries | slept out every remaining retry | retries stop; an attempt still running after the drain is cancelled and recorded as `hook_failed` | hooks whose last retry used to run during a shutdown; nothing to change |

## Output paths

Unchanged, with the exceptions below. The first three only affect unusual titles; the
last two only templates that use `{SongNumber}` or `{TrackCount}`:

- Leading dots (and spaces between them) are stripped from path segments, so a title
  such as "...Baby One More Time" is no longer written as a hidden file. Files written
  by 1.x under the dotted name are not recognized and are downloaded again.
- Dots and spaces are trimmed from the end of a segment together: 1.x left a trailing
  space when a name ended in a dot, a space and more dots ("Title . ." became
  "Title . "); 2.0 writes "Title", so such a track downloads again.
- Segments longer than 240 bytes are truncated on a character boundary. 1.x failed
  longer names only after the whole download (file names over 251 bytes, folder names
  over 255); a name between 241 bytes and those limits is now shorter, so that track
  downloads again.
- In an artist job, `{SongNumber}` restarts at 1 with each album when
  `download.artist_path_format` names `{AlbumName}` or `{AlbumId}`; 1.x numbered the whole
  discography in one run. An install whose own artist template uses `{SongNumber}` that
  way downloads those artists' albums again. 1.x's example and a config without the key
  use `{TrackNumber}` there and are unaffected.
- `{TrackCount}` is the number of tracks on the track's own disc; 1.x used the album-wide
  count. A template that uses `{TrackCount}` downloads multi-disc albums again.

## Downloads

- The track-total tag of a multi-disc album counts the tracks on its own disc ("3 of
  19", not "3 of 48"). Files 1.x wrote keep the old total unless a job runs with
  `force_overwrite`.
- Cancelling a job ends each of its unfinished items `cancelled`; 1.x left them in
  their last state, such as `downloading`. Items of a running job each get an
  `item_progress` event whose `phase` is `cancelled`; a queued job's leftover items
  (from a run a restart interrupted) are ended in one write before its `job_cancelled`
  event. Job detail views and stream clients see the new terminal item status.
  `POST /api/v1/downloads/{id}/retry` re-queues the unfinished tracks of a cancelled job
  as it does for a failed one.
- Stopping the backend no longer cancels running jobs: they stay `running` and resume
  on the next start (`job_recovered`), keeping finished tracks and, while the temp
  directory survives, partial transfers.
- Media transfers no longer have a 90 s whole-request timeout: a transfer fails only
  when no data arrives for 60 s, so a large Hi-Res track on a slow link finishes.
- A song without an Enhanced HLS manifest falls back to the next codec at once instead
  of after `download.max_attempts` retries, and a failed integrity check reports
  ffmpeg's own message.
- Jobs queued, running or retryable at the upgrade keep the overrides they were
  submitted with, read without the removed fields: they run under the runtime config's
  folders, path templates, lyrics, covers and retry settings, the backend's own
  media-user-token (the wrapper's, else the runtime `catalog.media_user_token`), and every
  enabled hook. A station job submitted with its own token needs one of those to run
  again. Resume scratch that a
  job left under its own former `overrides.temp_dir` is not cleaned up: that job starts
  its tracks again, and the old folder can be deleted by hand.
- Duplicate detection ignores the destination folder now that a submission cannot pick
  one: the same album, song or playlist is one download. A job from before the upgrade
  keeps its old key, so while it is queued or running (also after a retry of a failed
  or cancelled one) it neither blocks a new submission of the same link nor is refused
  because of one. Before retrying such a job, check that the same link is not already
  queued or running.

- Lyrics are rebuilt on wrapper-lite's `/lyrics`. A `.ttml` sidecar is always Apple's
  word-timed document with the translation and romanization in its head (1.x saved the
  line-timed one unless `lyrics_type` was `syllable-lyrics`). LRC output changes for the
  same song: background vocals are no longer dropped from word-timed lines, a
  translation line no longer disappears when its line has background vocals, romanized
  syllables are no longer split by spaces, and a translation or romanization that reads
  the same as its line is no longer repeated. `embed_lyrics: false` now keeps lyrics out
  of the tag even when a sidecar is written. Existing files are left alone unless a job
  runs with `force_overwrite`. Jellyfin reads sidecar lyrics only, so it needs
  `lyrics_files: [lrc]`.

## Database

The first start applies migrations in one transaction each:

- records the schema version (`PRAGMA user_version`);
- adds `jobs.started_at` / `jobs.finished_at`, backfilled from job events (`started_at`
  from the latest run, after the last retry);
- folds a 1.x `jobs.force = 1` into `overrides.force_overwrite = true`, then drops
  `jobs.force`, `jobs.done_items` and `jobs.failed_items` (counters are derived from
  items);
- indexes `job_events(type, created_at)` for `/stats`;
- deletes `item_progress` events of finished jobs (live progress telemetry; item rows
  keep their final state);
- adds `job_items.download_bytes` (0 for every existing row), the bytes of encrypted
  media an item's current attempt has received. A database that has run this
  migration is refused by an older build, so a release candidate from before it cannot
  open the file again.

No migration rewrites `jobs.overrides`: a row that still holds a `media_user_token`,
`hooks` or another removed field is read with it ignored, and the job's next terminal
write stores only the fields that remain.
