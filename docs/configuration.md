# Configuration

> Type: reference

Everything lives in one file. `AMDL_CONFIG` picks its path; the default is:

```text
configs/config.yaml
```

On first start the backend creates it from
[`configs/config.example.yaml`](../configs/config.example.yaml), which is built into the
binary (`configs#Example`), so no example needs to be on disk. **The example file is the documentation** — allowed enum values, units,
defaults and template variables are written into its comments, key by key. `config.yaml`
itself is managed by the backend and its comments do not survive a write.

## Defaults

A key missing from `config.yaml` takes the value `configs/config.example.yaml` sets, so a
sparse file behaves the way the example documents. The one exception is
`download.album_path_format` and `download.artist_path_format`: a config without them
predates `{SongNumber}` paths and keeps the older `{TrackNumber}` layout, so files already
downloaded are still found. Tests in `internal/config` hold `Default()` equal to the
example and every `Default:` note in its comments equal to `Default()`.

Unknown keys fail startup with an error naming each one; keys removed in 2.0 are listed in
[Upgrading to 2.0](upgrading-to-2.0.md#config-and-environment). That includes the old
per-job concurrency keys such as `download.max_parallel_tracks`, replaced by the
process-wide pools in [download-pipeline.md](download-pipeline.md#concurrency).

## Runtime vs startup keys

`PUT /api/v1/config` may only change runtime-mutable keys, but it rewrites the whole file
including startup keys, dropping all comments in the process.

| | |
| --- | --- |
| **Startup** | `server.listen`, `database.path`, `wrapper.*`, `tools.ffmpeg`, `logging.console`, `logging.buffer_size`, `catalog.token_cache_ttl_hours`, and every pool size (`download.max_running_jobs`, `download.max_parallel_downloads`, `catalog.max_parallel_requests`) |
| **Runtime** | `logging.level`, `logging.access_log`, `catalog.album_track_url_mode`, `catalog.media_user_token`, `catalog.motion_artwork_enabled`, all remaining `download.*` keys, the whole `simulate` section and the whole `library_sync` section |

Hand edits reach the running service like this:

| Trigger | Effect |
| --- | --- |
| `GET /api/v1/config` | Returns the in-memory snapshot; never reads the file. |
| `POST /api/v1/config/reload` (the console's "Reload from file") | Re-reads the file and applies its runtime keys. If the file is unreadable or invalid, the previous snapshot stays live and the response reports the error as `reload_error`. |
| `PUT /api/v1/config` | Re-reads the file under the same lock first, then applies the patch, so a hand edit made since the last reload is kept rather than overwritten. An invalid file fails the `PUT` and changes nothing. |
| Restart | Applies startup keys. A reload or `PUT` never makes a startup-key edit live, and a `PUT` keeps the edited value on disk for the next start. |

Set these before first real use:

- `server.listen` — API listen address.
- `wrapper.address` — the wrapper's URL ([Wrapper](#wrapper)).
- `database.path` — SQLite file (default `data/db/amdl.db`).
- `download.downloads_dir` — where finished files go.
- `logging.*` — level, stdout on or off, in-memory retention. There is no log file.
- `tools.ffmpeg` — path or command name.

## Environment overrides

Six environment variables exist, for what a container needs before it can read its own
config file:

| Variable | Sets |
| --- | --- |
| `AMDL_CONFIG` | Path of `config.yaml` (default `configs/config.yaml`) |
| `AMDL_HOOKS_CONFIG` | Path of `hooks.yaml` (default `configs/hooks.yaml`, git-ignored; `configs/hooks.example.yaml` is the commented template) |
| `AMDL_SERVER_LISTEN` | `server.listen` |
| `AMDL_WRAPPER_ADDRESS` | `wrapper.address` |
| `AMDL_WRAPPER_DATA_DIR` | `wrapper.data_dir` |
| `AMDL_DATABASE_PATH` | `database.path` |

- The four key overrides win over the file on every start and reload, and are never
  written back. They are all startup keys, so `PUT /api/v1/config` never meets one.
- Every other key is set in `config.yaml`. Any other `AMDL_*` variable is ignored, and
  startup logs one warning per variable naming it; move such a setting into the file.

## Wrapper

The `wrapper` section points the backend at the HTTP service that holds the Apple Music
account session. All four keys are startup keys.

- `wrapper.address` is `http://host:port` or `https://host:port`, with an optional path
  prefix for a reverse proxy; the default is `http://127.0.0.1:12340`. A bare `host:port`
  fails startup. The scheme picks the transport: `http` is plaintext, `https` is TLS 1.2+
  with the certificate and hostname verified against the system trust store. There is no
  option to skip verification.
- The URL is either a wrapper-lite or a wrapper-manager v2 in front of one. Both serve
  manifests, lyrics, licenses and the decryption templates; only wrapper-manager has the
  login API, so only it lets the console's Account page log in. The backend tells them
  apart from `/status` and reports it as `login_supported` in
  `GET /api/v1/wrapper/status`. Container setups for both are in
  [Deployment](deployment.md#the-wrapper); what the backend asks of the wrapper is in
  [Architecture](architecture.md#21-the-wrapper-and-decryption).
- `wrapper.timeout_seconds` (default 30) bounds one wrapper HTTP request; a failed key
  template request is retried up to three more times, 1, 2 and 4 seconds apart. A `/key` or
  `/m3u8` waits at most ten times this for a free playback slot. A `/key` that runs out of
  that wait skips the retries above, though the track's decrypt attempts
  (`download.max_attempts`) still apply; a `/m3u8` falls back to the web manifest.
  Decryption runs in the backend and has no wrapper timeout.
- `wrapper.login_timeout_seconds` (default 120) bounds one console login request to a
  wrapper-manager. A login that needs a two-step code takes two requests.
- `wrapper.data_dir` (default empty) is the wrapper's data directory as the backend sees
  it, so the backend can read the wrapper's media-user-token
  ([The media-user-token](#the-media-user-token)).
- Simulate mode (below) skips the wrapper status check, so jobs can be submitted without one.

### The media-user-token

Station downloads, private playlist covers and library sync need the Apple Music
media-user-token of a subscribed account. The wrapper keeps its logged-in account's token
in a file named `MUSIC_TOKEN` and refreshes it itself, so the backend uses that one
whenever it can read it, and `catalog.media_user_token` only when it cannot:

- A wrapper the backend runs itself (`amdl wrapper install`): read from the guest's disk,
  with nothing to configure. `wrapper.data_dir` is ignored, and startup says so when it is
  set.
- Any other wrapper whose files the backend can see: set `wrapper.data_dir` (or
  `AMDL_WRAPPER_DATA_DIR`) to the wrapper's data directory. A wrapper-lite base directory
  holds `MUSIC_TOKEN` directly; a wrapper-manager's data directory holds one per account
  under `instances/<id>/`. amdl 2.0 supports one wrapper account, a wrapper-manager's
  included; with several, the newest token is read. In containers, mount that directory
  read-only into the backend ([Deployment](deployment.md#the-wrapper)). The file must be
  readable by the user the backend runs as.
- A wrapper on another machine with no shared files: paste the token into
  `catalog.media_user_token` (the console's Settings › Account).

`GET /api/v1/wrapper/status` reports the token in use from the wrapper, masked, as
`media_user_token` (`""` when there is none). A token typed into
`catalog.media_user_token` is never overwritten or removed; it is simply not used while the
wrapper has one. The wrapper's file is read afresh on every use, so a login or a refreshed
token takes effect with the next request.

## Catalog tokens and the Hi-Res manifest

Every catalog read goes to `amp-api.music.apple.com` with a bearer token the backend
scrapes from `music.apple.com`; `catalog.token_cache_ttl_hours` sets how long it is
trusted. The backend holds no Apple developer key and never signs a token.

`catalog.language` is the language Apple answers in: the genre tag, the album, song and
artist names that go into tags and output paths, and lyrics translations. Set it before
the first download; changing it later renames what new downloads write, so the
"already downloaded" check misses files saved under the old names.

The Enhanced HLS master playlist normally comes from the catalog response (`enhancedHls`).
The web player's playlist can leave out a Hi-Res track's higher ALAC renditions that the
wrapper's device manifest lists: an 88.2, 96 or 192 kHz stream may be missing while 48 kHz
is there, and nothing in the playlist says so. So a track with the `hi-res-lossless` audio
trait takes the device manifest from the wrapper (`/m3u8`) when `download.alac_max_sample_rate`
is above 48000 or is 0 or less (no ceiling) and the codec is ALAC. From 1 to 48000, for Atmos
and AAC, and for any other track, the catalog playlist is used and the wrapper is not asked, because the wrapper serves
`/m3u8` and `/key` from one lock. The answer is remembered per storefront and song for 12
hours (at most 1024 songs), and dropped as soon as the playlist it points to fails to load.
If the wrapper cannot supply a manifest the track keeps the catalog playlist and may be
capped at 48 kHz. `download.alac_max_sample_rate` then picks from whichever playlist was read.

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

Without a running wrapper a Hi-Res track keeps the catalog playlist, which may lack its
88.2 kHz and higher ALAC renditions.
