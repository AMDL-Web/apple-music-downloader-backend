# Configuration

Four layers decide each key's effective value, highest first:

| Layer | Where | Who writes it |
| --- | --- | --- |
| **environment** | `AMDL_<SECTION>_<KEY>` | you |
| **file** | `configs/config.yaml` (`AMDL_CONFIG` picks the path) | you |
| **database** | the `settings` table | `PUT /api/v1/config` |
| **defaults** | compiled into the binary | nobody |

On first start the backend copies
[`configs/config.example.yaml`](../configs/config.example.yaml) verbatim to `config.yaml`
next to it, and never touches that copy again — **the backend never writes the config
file**. `config.yaml` is not tracked in git, so an install's own settings cannot be
committed by accident; the example is, and it is the documentation: allowed enum values,
units, defaults and template variables live in its comments, key by key.

The file is an optional, partial override layer: only the keys actually spelled out in it
override anything, and an install without one (delete it, or start with no example beside
it) runs on stored settings and defaults alone. The shipped example leaves every
runtime-mutable key commented out, so the copy a fresh install gets pins nothing and the
API owns them.

## Pinning

Writing a key into the file (or setting its `AMDL_*` variable) **pins** it: both layers
outrank the database, so a stored value would never take effect. `PUT /api/v1/config`
therefore rejects a change to a pinned key with `422` rather than accepting a write that
does nothing. Submitting a pinned key's *current* value is fine, so a client can echo the
whole config back without special-casing anything.

`GET /api/v1/config` reports this directly: `sources` gives the winning layer per key
(`env`, `file`, `db`, `default`) and `locked` lists the pinned keys, which a UI should show
read-only. Presence is what pins, not difference — writing a key at its default value still
pins it. Remove the key from the file (or unset the variable) and restart to hand it back
to the API; the stored value, if there was one, takes over again.

## Runtime vs startup keys

`PUT /api/v1/config` may only change runtime-mutable keys, and the database layer holds
only those. Startup keys are consumed once while the process boots, so the file and the
environment are the only places they can be set.

Edit runtime keys in the file by hand and the next `GET /api/v1/config` re-reads and
applies them immediately (which also pins them). Startup keys still need a restart.

| | |
| --- | --- |
| **Startup** | `server.listen`, `database.path`, `wrapper.*`, `tools.ffmpeg`, most of `logging.*`, the developer-token signing keys, and every pool size (`download.max_running_jobs`, `max_parallel_downloads`, `max_parallel_decrypts`, `max_parallel_wrapper_requests`, `catalog.max_parallel_requests`, `catalog.requests_per_second`, `catalog.request_burst`) |
| **Runtime** | `logging.level`, `logging.access_log`, `catalog.album_track_url_mode`, `catalog.media_user_token`, `catalog.signed_mode_hls_source`, `catalog.motion_artwork_enabled`, all remaining `download.*` keys, the whole `simulate` section and the whole `library_sync` section |

Set these before first real use. The startup ones go in the file (or the environment); the
runtime one is easiest through the API, and putting it in the file only pins it:

- `server.listen` — API listen address.
- `wrapper.address` — `wrapper-manager` gRPC address.
- `database.path` — SQLite file (default `data/db/amdl.db`), which also holds the stored
  settings, so it is the one key the database layer can never supply.
- `logging.*` — format, in-memory retention, optional rotating file.
- `tools.ffmpeg` — path or command name.
- `download.downloads_dir` — where finished files go (runtime).

## Environment overrides

Any key can be overridden with `AMDL_<SECTION>_<KEY>` — the YAML path uppercased with `_`
as the separator: `AMDL_SERVER_LISTEN`, `AMDL_WRAPPER_ADDRESS`, `AMDL_DATABASE_PATH`,
`AMDL_LOGGING_LEVEL`, `AMDL_DOWNLOAD_QUALITY_PRIORITY`.

- Overrides sit on top of every other layer, on every start and every config reload.
  Nothing is ever written back to the file or the database because of them.
- Value syntax: strings verbatim; booleans `true`/`false`; integers as digits; string
  lists comma-separated (`alac,aac`), with an empty value meaning an empty list.
- Unrecognised `AMDL_*` variables **fail startup**, so a typo can never be silently
  ignored. `AMDL_CONFIG` and `AMDL_HOOKS_CONFIG` are exempt.
- A field pinned by an environment variable cannot be changed through
  `PUT /api/v1/config` — it returns `422`. Change the variable and restart.

## Developer-token signing

The three `catalog.apple_music_*` keys form one unit.

| State | Behaviour |
| --- | --- |
| All three empty | Legacy mode. The bearer token is scraped from `music.apple.com` and catalog metadata is read from `amp-api.music.apple.com`, with `enhancedHls` included in the response. |
| All three set | The backend signs its own 24-hour ES256 developer token at startup (any signing error fails startup) and reads catalog metadata from the official `api.music.apple.com`. That token cannot read `enhancedHls`, so the Enhanced HLS source is chosen separately by `catalog.signed_mode_hls_source`. |
| Only some set | Startup fails with a configuration error. |

`catalog.signed_mode_hls_source` only matters in signed mode: `wrapper` (default) takes
the master playlist from the wrapper's authorized device manifest; `web_token` scrapes a
web-player JWT and reads `enhancedHls` from `amp-api.music.apple.com` independently of the
signed token. Use `web_token` when the wrapper cannot supply a usable device manifest.

`catalog.motion_artwork_enabled` (default `true`) controls the lookup of Apple's animated
album covers. When on, one extra `amp-api` request per album runs out of band on a context
detached from the job — it can never block or fail a download, and the fields arrive a
moment later via the `motion_artwork_resolved` event. Turn it off if nothing you use
renders animated covers and you would rather not generate undocumented-endpoint traffic.

## Simulate mode

`simulate.enabled` runs the pipeline without downloading or decrypting anything: no
`ffmpeg`, no output file, and the wrapper decryptor status check is skipped so jobs can be
submitted without a running wrapper. Catalog metadata and Enhanced HLS media selection
still run for real, so titles, artwork and the reported bit depth / sample rate / bitrate
are authentic and selection failures fall back through `quality_priority` exactly like a
real download. Every API response, status, progress breakdown and SSE event matches a real
job. Transfer speed is randomised between `min_speed_kbps` and `max_speed_kbps`.

Note that with developer-token signing enabled, manifests normally come from the wrapper —
without one, selection falls back to a faked AAC-LC.

## Resetting a key

`PUT /api/v1/config` with a key set to `null` drops its stored row, handing the key back to
the config file or, failing that, the built-in default. Omitting a key leaves it alone;
`null` is the only way to say "forget what I set".

Wiping the whole `settings` table resets the backend to the shipped configuration. It holds
nothing else — no job or library state — so that is a safe thing to do.

## Upgrade notes

**From 1.x.** Breaking, with no automatic migration. 1.x also seeded `config.yaml` from the
example, but then rewrote it on every `PUT`; 2.0 never writes it and keeps runtime settings
in the database instead. The 1.x example activated every key, so a 1.x `config.yaml` is a
full copy of all of them — leaving it in place pins every key and takes them all away from
the API. Replace it with the 2.0 example (or delete it and let the backend re-seed) and
re-apply the settings you want through `PUT /api/v1/config`.

**Removed in 2.0.** `AMDL_RUNTIME_CONFIG` and the one-time `runtime.yaml` merge it drove
are gone, as is the deprecated `catalog.media_user_token_priority` key — a config file or
API payload still carrying it now fails as an unknown key. Use `overrides.media_user_token`
per job.

**Removed keys fail startup.** Old per-job concurrency keys such as
`download.max_parallel_tracks` are rejected as unknown fields — remove them by hand before
starting. They were replaced by the process-wide pools described in
[download-pipeline.md](download-pipeline.md#concurrency).

**Removed environment variables.** `AMDL_LISTEN` and `AMDL_WRAPPER_ADDR` no longer exist;
leaving them set fails startup with an unknown-variable error. Use `AMDL_SERVER_LISTEN`
and `AMDL_WRAPPER_ADDRESS`.
