<p align="center">
  <img src="./assets/readme/hero.svg" width="100%"
       alt="AMDL Backend — paste an Apple Music link and get tagged lossless files on your own disk, through a resolve, download, decrypt, remux, verify, tag and save pipeline">
</p>

<p align="center">
  <a href="https://github.com/AMDL-Web/apple-music-downloader-backend/actions/workflows/ci.yml"><img src="https://github.com/AMDL-Web/apple-music-downloader-backend/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/AMDL-Web/apple-music-downloader-backend/releases"><img src="https://img.shields.io/github/v/release/AMDL-Web/apple-music-downloader-backend?label=release" alt="Latest release"></a>
  <a href="https://github.com/AMDL-Web/apple-music-downloader-backend/pkgs/container/apple-music-downloader-backend"><img src="https://img.shields.io/badge/ghcr.io-amd64%20%2B%20arm64-blue" alt="Container image on GHCR"></a>
  <img src="https://img.shields.io/github/go-mod/go-version/AMDL-Web/apple-music-downloader-backend" alt="Go version">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0-green" alt="AGPL-3.0"></a>
</p>

<p align="center"><a href="README.zh-CN.md">中文</a></p>

**Send it an Apple Music URL. It resolves the songs, pulls the encrypted Enhanced HLS
media, decrypts it, remuxes and tags it, and writes a finished file to your disk —
publishing every stage as it happens.**

One Go binary, one SQLite file, a REST + SSE + WebSocket API on `/api/v1`, and an
OpenAPI 3.1 spec that the AMDL web and iOS clients are built against. It handles
songs, albums, playlists, artists and radio stations.

---

## Quick start

You need a reachable [`wrapper-manager`](#how-it-works) instance — the backend gets
device manifests, licenses and lyrics from it and cannot download anything without one.

```bash
docker compose up -d
```

That pulls the multi-arch image from GHCR (no local build), seeds `configs/config.yaml`
from the bundled example, and listens on `:18080`. Point it at your wrapper with
`AMDL_WRAPPER_ADDRESS` in `docker-compose.yml`.

From source instead — Go per [`go.mod`](go.mod), plus `ffmpeg` on `PATH`:

```bash
go run ./cmd/amdl-api
```

Submit an album:

```bash
curl -X POST http://localhost:18080/api/v1/downloads \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://music.apple.com/cn/album/1580904295"}'
```

Watch it run — one line per stage transition, per track:

```bash
curl -N http://localhost:18080/api/v1/downloads/{job_id}/events
```

And when it finishes:

```text
data/downloads/albums/<artist>/<album>/01. Song.m4a   ← ALAC, cover and lyrics embedded
                                       cover.jpg      ← standalone artwork
```

Interactive API reference: <http://localhost:18080/docs>.

## How it works

<p align="center">
  <img src="./assets/readme/architecture.svg" width="100%"
       alt="Web and iOS clients call the backend over REST, SSE and WebSocket; the backend keeps its job queue in SQLite, runs the download pipeline and writes files to disk; it requires wrapper-manager over gRPC and reads Apple Music from two different hosts">
</p>

**`wrapper-manager` is not optional.** It holds the Apple Music account session and
serves the device manifest, Widevine licenses and lyrics over gRPC. The connection is
lazy, so the backend starts without it — `GET /api/v1/wrapper/status` tells you whether
it is actually reachable.

**Apple is read from two hosts, and they are not interchangeable.**
`api.music.apple.com` is the documented API and takes a self-signed developer token.
`amp-api.music.apple.com` is Apple's internal web-player endpoint, answers only to a JWT
scraped from `music.apple.com`, and is the **only** source of `enhancedHls` manifests and
`editorialVideo` animated covers. Being undocumented, it can change without notice;
anything read from it degrades to "this album doesn't have that" rather than failing a job.

**There is no authentication here, on purpose.** Every endpoint is open, including
`GET /api/v1/developer-token`. This is the download core — access control belongs to the
reverse proxy, gateway or session layer you put in front of it.

## How a track is downloaded

Every track walks the same path. Each transition is published as an `item_progress`
event whose `phase` carries the item status:

```text
queued → resolving → waiting_download → downloading → waiting_decrypt
       → decrypting → remuxing → saving → tagging → completed
```

Alongside the status, each item carries a durable stage breakdown — `resolved`,
`remuxed`, `verified`, `tagged`, `saved` — so a client that reconnects mid-job can
redraw the exact same progress without replaying the stream.

Media is decrypted fragment by fragment straight into `ffmpeg`; no plaintext whole track
is ever written to disk. The flatten, integrity check and tag rewrite all happen in
`download.temp_dir`, and only the finished file moves to `downloads_dir`.

### Codec fallback

<p align="center">
  <img src="./assets/readme/codec-fallback.svg" width="100%"
       alt="Codecs from download.quality_priority are tried in order; when one codec's attempts are exhausted the next is tried, with AAC-LC over WebPlayback always appended as the implicit final fallback">
</p>

`download.quality_priority` is an ordered list — `alac`, `aac`, `aac-binaural`,
`aac-downmix`, `ec3`, `ac3`. Each codec gets `download.max_attempts` tries, with the
download and decrypt phases counted separately, and retries use exponential backoff with
jitter. AAC-LC never needs listing: it is appended automatically as the last-resort
WebPlayback format. Set `codec_alternative: false` to stop after the first codec.

### Concurrency

Three process-wide pools, shared across all jobs and fixed at startup:
`max_parallel_downloads` (16), `max_parallel_decrypts` (32) and
`max_parallel_wrapper_requests` (24), plus `max_running_jobs` (3) worker slots. When a
pool is full, permits go to the earliest-submitted unfinished job first, so jobs tend to
finish one after another instead of all creeping forward together. Interactive API calls
never queue behind jobs.

`download.memory_mode` trades RAM for temp-disk I/O: `low` (default) checkpoints the
encrypted track to disk and re-reads it; `high` keeps it in RAM instead. See
[docs/download-pipeline.md](docs/download-pipeline.md) and the measured numbers in
[docs/benchmarks.md](docs/benchmarks.md).

## Where files land

One template per task type, relative to `download.downloads_dir`. The last segment is
the file name and gets `.m4a` appended.

| Config key | Default |
| --- | --- |
| `download.song_path_format` | `songs/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}` |
| `download.album_path_format` | `albums/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}` |
| `download.artist_path_format` | `artists/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}` |
| `download.playlist_path_format` | `playlists/{PlaylistName}/{SongNumber:02d}. {SongName}` |
| `download.station_path_format` | `stations/{StationName}/{SongNumber:02d}. {SongName}` |

Variables like `{AlbumArtist}`, `{ReleaseYear}`, `{UPC}`, `{DiscNumber}` and `{Codec}`
are listed in [`configs/config.example.yaml`](configs/config.example.yaml); numeric ones
take `:02d` padding. In directory segments `{ArtistName}` resolves to the collection's
grouping artist so an album stays in one folder; in the file-name segment it is the
track's own artist. Standalone `cover.jpg` / `artist.jpg` files and `.lrc` / `.ttml`
lyrics sidecars are written next to the audio when enabled.

## API

`http://localhost:18080/docs` is a live Swagger UI over
[`internal/api/openapi.yaml`](internal/api/openapi.yaml), which is the source of truth
for every request and response shape.

| | |
| --- | --- |
| **Downloads** | `POST /downloads` · `GET /downloads` · `GET/DELETE /downloads/{id}` · `POST /downloads/{id}/cancel` · `POST /downloads/{id}/retry` |
| **Events** | `GET /downloads/{id}/events` (+ `/ws`) per job · `GET /downloads/events` (+ `/ws`) overview feed |
| **Quality** | `POST /quality` — probe a URL's declared codecs and audio quality without creating a job |
| **Wrapper** | `GET /wrapper/status` · `POST /wrapper/login` · `POST /wrapper/login/{login_id}/2fa` · `POST /wrapper/logout` |
| **Config** | `GET /config` · `PUT /config` · `GET /hooks` |
| **Library sync** | `GET /library-sync` · `POST /library-sync/reset` |
| **System** | `GET /health` · `GET /developer-token` · `GET /logs` · `GET /logs/stream` (+ `/ws`) |

Submissions take `url` or `urls`, and an `overrides` object that overlays the runtime
config for that batch only — quality priority, memory mode, path templates, lyrics
settings, `force_overwrite`, the hook allow-list, and the `media_user_token` that radio
stations and private playlists need. Tokens are never echoed back in any response.

Every response carries an `X-Request-ID`; send your own and
`GET /logs?request_id=<id>` aggregates that request's access and job logs.

Full walkthrough with curl examples: [docs/api.md](docs/api.md).

## Configuration

A single file, `configs/config.yaml`, bootstrapped from
[`configs/config.example.yaml`](configs/config.example.yaml) on first start. The example
is the documentation — every key's allowed values, units and defaults live in its comments.

- **Runtime keys** (quality, paths, lyrics, covers, retries, simulate, library sync) apply
  immediately via `PUT /api/v1/config`, which rewrites the whole file and drops comments.
- **Startup keys** (listen address, database path, wrapper address, pool sizes, log
  format) need a restart.
- **Any key** can be overridden with `AMDL_<SECTION>_<KEY>`, e.g.
  `AMDL_DOWNLOAD_QUALITY_PRIORITY=alac,aac`. Unknown `AMDL_*` variables fail startup
  rather than being silently ignored, and env-pinned fields are rejected by `PUT` with 422.

Details, upgrade notes and the Docker specifics: [docs/configuration.md](docs/configuration.md)
and [docs/deployment.md](docs/deployment.md).

## Automation

**Job hooks** ([`configs/hooks.yaml`](configs/hooks.yaml)) fire a webhook or a local
command when a job is queued or reaches a terminal state — refresh a media server, run a
post-processing script. Disabled by default.

**Library sync** polls your signed-in Apple Music library and submits the *album* behind
any newly added song as a normal download job: favourite one track on your phone, get the
album on your NAS. Disabled by default — it is the only feature that creates jobs on its
own. Because Apple exposes no reliable "added at" timestamp for library songs, it anchors
on the *order* of `sort=-dateAdded` instead of a clock.

Both are covered in [docs/automation.md](docs/automation.md).

## Limits

- **Audio only.** Apple Music music videos will not be supported: under L3 the pipeline
  can only reach low-resolution video, which does not meet this project's quality bar.
- **One user, no accounts.** There is no concept of a user anywhere in this codebase, and
  no auth. Put a gateway in front of it.
- **Radio is a rolling list.** Stations resolve to Apple's current "next tracks", so a
  download captures what is offered now, not a fixed tracklist. Live stations
  (Apple Music 1 and friends) fail with an explicit error.
- **Some catalog data is undocumented.** `enhancedHls` and animated covers come from
  `amp-api.music.apple.com` and may disappear without warning.

## Documentation

| | |
| --- | --- |
| [docs/api.md](docs/api.md) | Every endpoint with curl examples, job overrides, SSE/WS semantics |
| [docs/configuration.md](docs/configuration.md) | Config file, env overrides, runtime vs startup keys, upgrade notes |
| [docs/deployment.md](docs/deployment.md) | Docker, mounts, `PUID`/`PGID`, seeding, releases and image tags |
| [docs/download-pipeline.md](docs/download-pipeline.md) | Retries, codec fallback, concurrency pools, memory modes, lyrics |
| [docs/automation.md](docs/automation.md) | Job hooks and library sync |
| [docs/benchmarks.md](docs/benchmarks.md) | Measured post-decrypt and end-to-end results |

## Development

```bash
go test ./...
```

Add `-count=1` to bypass the Go test cache. CI runs `gofmt`, `go vet`, the full test suite,
the race detector and `govulncheck` on every push to `main` and every pull request.

`ffmpeg` is the only external command the pipeline needs. Sample extraction, remuxing,
metadata and cover art are all done in-process with `mp4ff` and `go-mp4tag`.

Work lands on `dev`; `main` is release-only and requires a pull request. Every commit
needs a DCO sign-off (`git commit -s`) — see [CONTRIBUTING.md](CONTRIBUTING.md).
Releases are cut by merging the `dev` → `main` PR with a version in its title, or by
running the `Release` workflow manually; both create the GitHub Release and push the
multi-arch image to GHCR.

## License

[AGPL-3.0](LICENSE).
