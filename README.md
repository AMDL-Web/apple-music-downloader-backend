# AMDL

<p align="center">
  <a href="https://github.com/AMDL-Web/apple-music-downloader/actions/workflows/ci.yml"><img src="https://github.com/AMDL-Web/apple-music-downloader/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/AMDL-Web/apple-music-downloader/releases"><img src="https://img.shields.io/github/v/release/AMDL-Web/apple-music-downloader?label=release" alt="Latest release"></a>
  <a href="https://github.com/AMDL-Web/apple-music-downloader/pkgs/container/apple-music-downloader"><img src="https://img.shields.io/badge/ghcr.io-amd64%20%2B%20arm64-blue" alt="Container image on GHCR"></a>
  <img src="https://img.shields.io/github/go-mod/go-version/AMDL-Web/apple-music-downloader" alt="Go version">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-AGPL--3.0-green" alt="AGPL-3.0"></a>
</p>

<p align="center"><a href="README.zh-CN.md">中文</a></p>

amdl downloads music from Apple Music to your own disk. Paste a song, album, playlist,
artist or radio station link into its web console, or search the catalog there; it resolves
the tracks, downloads the encrypted media, decrypts it, remuxes and tags it, and saves
finished `.m4a` files.

This repository holds the whole AMDL project. Each product has its own directory under
`apps/`, and they share one Go core under `internal/`.

## Products

### Server

`amdl serve` (`apps/amdl`) is the backend you deploy: one Go process with a SQLite database, a REST +
Server-Sent Events API on `/api/v1`, and the web console on the same port. It ships as a
container image built from `deploy/server/`. [Quick start](#quick-start) runs it; the
rest of this page describes it.

### Web console

`apps/console` is the browser client: a no-build Preact app, embedded into the server and
served at `/console/`. It uses only the public API. Its design contract is
[apps/console/DESIGN.md](apps/console/DESIGN.md).

### Command line

`apps/amdl` builds `amdl`, a client of the API written for agents and scripts: every
result is JSON on stdout, errors are JSON on stderr with an exit status per kind of
failure, and `amdl cli search "<task>"` finds the command for a task. `amdl mcp` serves the same
capabilities as MCP tools, and `amdl` alone in a terminal opens an interactive interface
for people. Without a server of its own, `amdl service install` runs the backend on this
machine as a background service that starts at login, and `amdl wrapper install` gives it
a wrapper of its own: upstream's wrapper-lite in a QEMU guest that the backend runs
([docs/install.md](docs/install.md#a-wrapper-amdl-runs)). [docs/cli.md](docs/cli.md)
describes it all.

### Website

`apps/site` is the project's website, in English and Simplified Chinese: static pages built
by a zero-dependency Node script. [apps/site/README.md](apps/site/README.md) explains how to
build and preview it.

### Desktop app

`apps/desktop` is a native app for macOS, Windows and Linux, drawn with
[MyGo](https://github.com/egoist/mygo)'s native UI. It runs either as a client of a remote
server or as a standalone backend that runs the pipeline in-process and talks to a wrapper:
one on this computer, which the app's setup screen installs with QEMU and ffmpeg as
`amdl wrapper install` does, or one on another machine. Standalone mode runs the backend through `internal/local` on a loopback port, or
uses the one this computer already runs, such as `amdl service`, so the app and the command
line share one backend and one download history; the app's own packages sit under
apps/desktop/internal, private to it. Each release attaches unsigned packages of it for
macOS, Windows and Linux ([docs/install.md](docs/install.md#the-desktop-app)), and
`make desktop-web` builds the same app for browsers: a demo on a made-up backend held in
memory.

## Repository layout

| Path | What it holds |
| --- | --- |
| `apps/` | One directory per product: `amdl` (the server and the command line), `console`, `desktop` and `site` (the website). A non-Go app keeps its own build system in its directory. |
| `internal/` | The Go core every product shares: the HTTP API, jobs, the download pipeline, the Apple and wrapper clients, storage and config. [Architecture](docs/architecture.md#2-packages) lists each package. |
| `deploy/` | The Compose file and, under `deploy/server/`, the server's Dockerfile and entrypoint. |
| `configs/` | The example config and hooks template every install is seeded from. |
| `docs/` | Reference docs and runbooks ([index](docs/README.md)). |
| `tools/` | Repository tooling: doc checks, the file splitter, the Temari parity check, the README diagrams. |

What the server needs:

- An Apple Music subscription.
- A wrapper: the upstream service that holds your Apple Music login. It runs on Linux
  (x86_64 or arm64). The native and Docker launchers need user namespaces (`unshare -Ur true`
  must succeed); on a host without them, such as Synology DSM, use wrapper-manager v2's
  QEMU variant. See [Wrapper](docs/deployment.md#the-wrapper). With the binary install
  on macOS, Windows or Linux, amdl can run one itself in QEMU (`amdl wrapper install`).
- Docker with Compose, or Go (version in [`go.mod`](go.mod)) and `ffmpeg`.

## Quick start

Two ways to run it, side by side:

| | Binary | Docker |
| --- | --- | --- |
| Suits | A desktop or laptop (macOS, Linux, Windows), no Docker | A NAS or server, next to the wrapper |
| Install | `curl -fsSL https://raw.githubusercontent.com/AMDL-Web/apple-music-downloader/main/deploy/install/install.sh \| sh` (PowerShell: see the guide) | Copy [`deploy/docker-compose.yml`](deploy/docker-compose.yml) |
| Runs as | A service that starts at login: `amdl service install --wrapper <url>` | `docker compose up -d` |
| ffmpeg | Install it yourself; `amdl doctor` prints the command | In the image |
| Wrapper | Elsewhere, or one amdl runs in QEMU: `amdl wrapper install`; none to [try it in simulate mode](docs/install.md#try-it-without-a-wrapper) | A compose service on the same host |
| Guide | [docs/install.md](docs/install.md) | Below and [docs/deployment.md](docs/deployment.md) |

The rest of this section is the Docker path. The repository's
[`deploy/docker-compose.yml`](deploy/docker-compose.yml) runs amdl from the prebuilt
image on GHCR and carries both wrapper setups as commented services. Copy it to an empty
directory; the first start creates `configs/` and `data/` next to it.

### With wrapper-manager v2 (recommended)

[wrapper-manager v2](https://github.com/WorldObservationLog/wrapper-manager/tree/v2) runs
wrapper-lite for you and lets you log in from the console.

1. In `docker-compose.yml`, uncomment the `wrapper-manager` service and the `volumes:`
   block at the end of the file, and set
   `AMDL_WRAPPER_ADDRESS: "http://wrapper-manager:8080"`.
2. Start both:

   ```bash
   docker compose up -d
   ```

   Compose builds wrapper-manager from its `v2` branch, and its first start downloads the
   wrapper-lite payload, so the wrapper host needs internet access.
3. Open `http://localhost:18080/`, go to **Settings › Account** and log in with your
   Apple ID. If Apple asks for a two-step code, the console asks you for it.
4. Paste a link into **New download**. Finished files land in `./data/downloads`.

### With a bare wrapper-lite

[wrapper-lite](https://github.com/WorldObservationLog/wrapper/tree/lite) serves one
account and logs in from its own command line, not from the console.

- **As a Compose service:** uncomment the `wrapper-lite` service, set
  `AMDL_WRAPPER_ADDRESS: "http://wrapper-lite:12340"`, and put `USERNAME` and `PASSWORD`
  in a `.env` file next to `docker-compose.yml` for the first start, which logs in. If
  Apple asks for a two-step code, write it to `2fa.txt` in `./data/wrapper`. The image
  build downloads the Android NDK and takes a while.
- **On the host:** keep the default `AMDL_WRAPPER_ADDRESS`, then log in once and serve
  (upstream's commands):

  ```bash
  ./wrapper-lite-rootless --login user:pass --code-from-file --base-dir /data
  ./wrapper-lite-rootless --base-dir /data --host 0.0.0.0 --port 12340
  ```

**Settings › Account** in the console shows the login command too. Then open
`http://localhost:18080/` and paste a link.

### From source

```bash
go run ./apps/amdl serve
```

This reads `configs/config.yaml`, created from
[`configs/config.example.yaml`](configs/config.example.yaml) on first start, and listens on
the `server.listen` address. Point `wrapper.address` at your wrapper.

Image tags, mounts, `PUID`/`PGID`, reverse proxies and the wrapper options in detail:
[docs/deployment.md](docs/deployment.md).

## Upgrading from 1.x

2.0 changes the API, the config keys and the wrapper. amdl talks to an upstream
wrapper-manager v2 or wrapper-lite over HTTP instead of the 1.x wrapper-manager fork, and
the first start migrates the database in a way 1.x cannot read. Back up the database and
read [docs/upgrading-to-2.0.md](docs/upgrading-to-2.0.md) before you upgrade.

## How it works

<p align="center">
  <img src="./docs/assets/architecture.svg" width="100%"
       alt="Architecture: the web console calls amdl's REST and SSE API; amdl keeps jobs in SQLite, runs each track through its pipeline and writes files to downloads_dir; the wrapper supplies key templates, manifests, lyrics and licenses; Apple's amp-api serves the catalog and its CDN the encrypted segments">
</p>

**The wrapper.** amdl holds no Apple account and no keys. For each FairPlay key it asks
the wrapper for a white-box template (`/key`) and decrypts the samples itself, in-process,
with `internal/fairplay`, a pure-Go translation of
[Temari](https://github.com/WorldObservationLog/Temari). The wrapper also serves lyrics,
the AAC-LC license, and the device manifest for Hi-Res ALAC tracks. amdl starts without
a wrapper; `GET /api/v1/wrapper/status` and the console's Settings page say whether it is
reachable and logged in.

**Apple's catalog.** Every catalog read goes to `amp-api.music.apple.com` with a token
scraped from `music.apple.com`; amdl needs no Apple developer key. That API is
undocumented and can change without notice.

<p align="center">
  <img src="./docs/assets/track-download.svg" width="100%"
       alt="Statechart of one track: resolve, then per codec a fetch phase (choose a manifest, claim the output path, fetch cover and lyrics once, download) and a decrypt phase (decrypt, remux, verify, tag, save), falling back to the next codec when a phase runs out of attempts; ends completed, skipped_existing, failed or cancelled">
</p>

**One track.** Each track is resolved, then downloaded and decrypted. Decrypted samples
are piped straight into `ffmpeg` for the remux, so no plaintext copy of the whole track is
written to disk; verifying and tagging happen in `download.temp_dir`, and only the
finished file moves into `download.downloads_dir`. If that file already exists the track
is skipped before its cover and lyrics are fetched.

**Codec fallback.** `download.quality_priority` lists the codecs to try in order. The
download and decrypt phases of each codec have their own `download.max_attempts` budget,
with backoff between tries. With `download.codec_alternative` on, a codec that runs out
hands over to the next one, and AAC-LC is appended as the last resort; with it off, only
the first codec is tried.

**Concurrency.** Downloads, decryption, wrapper requests and running jobs each share one
process-wide pool across all jobs. A full pool serves the earliest-submitted job first, so
jobs tend to finish one after another.

Stages, retries, pools and memory modes: [docs/download-pipeline.md](docs/download-pipeline.md).
Process lifecycle and package boundaries: [docs/architecture.md](docs/architecture.md).

## Where files land

Finished files go under `download.downloads_dir` (`./data/downloads` with Compose). Songs,
albums, artists, playlists and stations each have a path template there, built from
variables such as `{ArtistName}`, `{AlbumName}` and `{SongName}`. Standalone covers and
`.lrc` / `.ttml` lyrics files can be saved next to the audio. The templates, their
variables and the sidecar options are documented in
[`configs/config.example.yaml`](configs/config.example.yaml) and
[docs/download-pipeline.md](docs/download-pipeline.md#output-paths).

## API

[`internal/api/openapi.yaml`](internal/api/openapi.yaml) is the source of truth for every
request and response. Every path below is under `/api/v1`. The web console at `/console/`
is the API's only browser client: responses carry no CORS headers.

| | |
| --- | --- |
| **Downloads** | `POST /downloads` · `GET /downloads` · `GET/DELETE /downloads/{id}` · `POST /downloads/{id}/cancel` · `POST /downloads/{id}/retry` |
| **Events** | `GET /downloads/{id}/events` per job · `GET /downloads/events` overview feed (SSE) |
| **Lookup** | `POST /lookup`: preview a link's title, artist and cover without creating a job |
| **Search** | `GET /search`: search the catalog for songs, albums, playlists and artists · `GET /artists/{id}/albums`: an artist's albums, page by page · `GET /albums/{id}`: an album's songs |
| **Quality** | `POST /quality`: probe a link's codecs and audio quality without creating a job |
| **Wrapper** | `GET /wrapper/status` · `POST /wrapper/login` · `POST /wrapper/login/{login_id}/2fa` · `POST /wrapper/logout` |
| **Config** | `GET /config` · `PUT /config` · `POST /config/reload` · `GET /hooks` |
| **Library sync** | `GET /library-sync` · `POST /library-sync/reset` |
| **System** | `GET /health` · `GET /system` · `GET /stats` · `GET /logs` · `GET /logs/stream` |

Submit an album and follow it:

```bash
curl -X POST http://localhost:18080/api/v1/downloads \
  -H 'Content-Type: application/json' \
  -d '{"urls":["https://music.apple.com/cn/album/1580904295"]}'

curl -N http://localhost:18080/api/v1/downloads/{job_id}/events
```

Errors, stream resumption, job overrides and curl examples for every route:
[docs/api.md](docs/api.md).

## Configuration

One file, `configs/config.yaml`, created from
[`configs/config.example.yaml`](configs/config.example.yaml) on first start. The example's
comments document every key: allowed values, units and defaults. Runtime keys change
through `PUT /api/v1/config` (the console's Settings page), which rewrites the file without
its comments; startup keys need a restart. `AMDL_SERVER_LISTEN`, `AMDL_WRAPPER_ADDRESS`,
`AMDL_WRAPPER_DATA_DIR` and `AMDL_DATABASE_PATH` override their keys. Radio stations need
your subscription token: the backend reads it from the wrapper when it runs the wrapper
itself or `wrapper.data_dir` points at the wrapper's files, and otherwise from
`catalog.media_user_token`. Details: [docs/configuration.md](docs/configuration.md).

## Automation

- **Job hooks** ([`configs/hooks.yaml`](configs/hooks.yaml)) call a webhook or run a
  command when a job is queued or ends, for example to refresh a media server.
- **Library sync** watches your Apple Music library and downloads the album behind each
  song you add. It needs a subscription token, the wrapper's or `catalog.media_user_token`.

Both are off by default. See [docs/automation.md](docs/automation.md).

## Limits

- **Audio only.** Music videos are not supported: under L3 the pipeline can only reach
  low-resolution video.
- **One user, no accounts, no auth.** Every endpoint is open on purpose. Put access control
  in a reverse proxy or gateway in front of it.
- **Radio is a rolling list.** A station resolves to the tracks Apple offers now, not a
  fixed tracklist. Live stations such as Apple Music 1 fail with an explicit error.
- **Undocumented catalog.** `amp-api.music.apple.com` serves the Enhanced HLS manifests
  and animated covers, and may change without warning.

## Documentation

| | |
| --- | --- |
| [docs/deployment.md](docs/deployment.md) | Docker, the wrapper, mounts, `PUID`/`PGID`, reverse proxies, releases and image tags |
| [docs/configuration.md](docs/configuration.md) | Config file, defaults, environment overrides, runtime vs startup keys, reload |
| [docs/upgrading-to-2.0.md](docs/upgrading-to-2.0.md) | Every breaking change from 1.x, with an upgrade checklist |
| [docs/install.md](docs/install.md) | The `amdl` binary without Docker: install scripts, ffmpeg, a wrapper amdl runs, the background service |
| [docs/cli.md](docs/cli.md) | The `amdl` command line, its JSON output and exit statuses, the MCP server and the terminal interface |
| [docs/api.md](docs/api.md) | Every endpoint with curl examples, job overrides, error codes, SSE semantics |
| [docs/architecture.md](docs/architecture.md) | Process lifecycle, packages, API conventions, storage |
| [docs/download-pipeline.md](docs/download-pipeline.md) | Stages, retries, codec fallback, concurrency, memory modes, lyrics, output paths |
| [docs/automation.md](docs/automation.md) | Job hooks and library sync |
| [docs/benchmarks.md](docs/benchmarks.md) | Measured post-decrypt and end-to-end results |
| [docs/references.md](docs/references.md) | Outside practices adopted, adapted or declined |
| [docs/README.md](docs/README.md) | Index of every page in `docs/` |

## Reading map

Where to start reading for a given question. Each pointer names a package and a symbol,
so it stays valid when files inside the package are split.

| Question | Start at |
| --- | --- |
| How is the process wired together? | `internal/server#Run` |
| Which HTTP routes exist? | `internal/api#Server.Routes` |
| What exactly does the API accept and return? | `internal/api/openapi.yaml` |
| How are jobs queued, run, cancelled and retried? | `internal/jobs#Manager` |
| How is one track downloaded, decrypted, remuxed and tagged? | `internal/media#Downloader` |
| Which Apple host answers a request? | `internal/applemusic#ampAPIBase`, `internal/applemusic#CatalogClient.doWithWebAuth` |
| How does amdl talk to the wrapper? | `internal/wrapper#Client` |
| How are samples decrypted? | `internal/fairplay#Template` |
| Where are jobs stored, and how does the schema migrate? | `internal/db#Store` |
| How is config loaded and validated? | `internal/config#Load`, `internal/config#Config.Validate` |
| What do job hooks send? | `internal/hooks#Dispatcher` |
| How does library sync pick albums? | `internal/librarysync#Watcher` |
| How is the web console served? | `apps/console#Handler` |
| Which commands and MCP tools exist? | `internal/capability#All`, `apps/amdl/internal/cli#Run`, `apps/amdl/internal/mcpserver#New` |
| How does the terminal interface work? | `apps/amdl/internal/tui#Run` |
| How do the clients call the API? | `internal/client#Client` |
| How does a client run the backend on this machine? | `internal/local#Backend` |
| How does amdl run a wrapper in QEMU? | `internal/wrapperhost#Supervisor` |
| How is the website built? | `apps/site/build.mjs#renderPage` |

## Development

You need Go (version in [`go.mod`](go.mod)), `ffmpeg` on `PATH`, and Node.js 26 for the
script tests.

| Command | What it does |
| --- | --- |
| `make check` | The CI gate: gofmt check, build, vet, tests, race detector, `govulncheck`, Node script tests |
| `make test`, `make race` | The test suite, without or with the race detector |
| `make dev` | The API in simulate mode on `127.0.0.1:18080`, with a throwaway config and database under `data/dev/` |
| `make docker` | Build the container image locally |
| `node apps/site/build.mjs --serve 8080` | Build the website and preview it on `127.0.0.1:8080` |
| `make help` | Every target |

`make dev` needs no wrapper: simulate mode reads real catalog data and selects real media,
then simulates the transfer, decryption and file writes while publishing the same statuses
and events as a real download.

CI runs the `make check` steps on every push to `main` and `dev` and on every pull
request, and builds the Docker image. Work lands on `dev`; `main` changes only through a
release pull request. Every commit needs a DCO sign-off (`git commit -s`); see
[CONTRIBUTING.md](CONTRIBUTING.md). Coding agents start at [AGENTS.md](AGENTS.md).

## License

[AGPL-3.0](LICENSE).

`internal/fairplay` is a translation of [Temari](https://github.com/WorldObservationLog/Temari),
which is MIT-licensed; its notice is in `internal/fairplay/doc.go`.
