# Download pipeline

Audio only. Apple Music music videos will not be supported: under L3 the pipeline can
only reach low-resolution video, which does not meet this project's quality bar.

## Stages

Every track walks the same path, and each transition publishes an `item_progress` event
whose `phase` is the item status:

```text
queued → resolving → waiting_download → downloading → waiting_decrypt
       → decrypting → remuxing → saving → tagging → completed
```

Terminal item states are `completed`, `failed`, `skipped_existing` and `cancelled`.
Alongside the status, each item carries a durable stage breakdown — `resolved`,
`remuxed`, `verified`, `tagged`, `saved` — so a client reconnecting mid-job can redraw
the same progress without replaying the stream.

The normal path is: encrypted input → fragment-by-fragment decrypt → `ffmpeg` stdin →
flattened file → integrity check → tagging → final save. No plaintext whole track is ever
written to disk. Flatten, verify and tag all happen in `download.temp_dir`, so a possibly
slow downloads volume only ever sees one sequential write.

Tagging rewrites only the smaller `moov` and artwork region for the tail-`moov` layout
`ffmpeg` normally produces, leaving the media in place; layouts that cannot be updated
safely in place fall back to the old whole-track rewrite.

If `temp_dir` and `downloads_dir` share a filesystem, the final move is an atomic rename;
across filesystems it is one copy through a `.part` file, which preserves complete-file
publication and failure-cleanup semantics.

`ffmpeg` (`tools.ffmpeg`) is the only external command required — it flattens the remuxed
MP4 and runs the optional integrity check. Sample extraction, re-encapsulation, metadata
and cover art are all handled in-process by the Go `mp4ff` and `go-mp4tag` libraries, so
`gpac`, `MP4Box` and the Bento4 tools (`mp4extract`, `mp4edit`) are no longer needed.

## Retries and codec fallback

- `download.quality_priority` — the ordered Enhanced HLS fallback chain. Supported:
  `alac`, `aac`, `aac-binaural`, `aac-downmix`, `ec3`, `ac3`.
- `aac-lc` never needs listing. With codec fallback enabled it is appended automatically
  as the final WebPlayback floor.
- `download.codec_alternative` — whether to continue down the chain after a codec's
  retries are exhausted. Off means only the first codec is tried.
- `download.max_attempts` — maximum total attempts (including the first) for metadata,
  cover, lyrics, and each codec's download and decrypt phase. Positive values allow
  `1-10`; `<= 0` behaves as 1 (one try, no retry). Every codec in the chain, including the
  implicit AAC-LC floor, gets its own budget, and its download and decrypt phases count
  separately.
- Retryable errors use exponential backoff with jitter. When Apple's Catalog API returns
  `Retry-After`, the wait is never shorter than that hint, so a batch of requests cannot
  synchronously replay.
- Retries, exhaustion, recovery and codec fallback all surface as job SSE events. Each
  item in the job detail also carries `retry_kind`, `attempt`, `max_attempts` and
  `status_message`, where `attempt` is the 1-based try number within the current
  `retry_kind` phase.

## Concurrency

All pools are process-wide, shared across every job, sized at startup, and not shared
between backend replicas. Changing any of them requires a restart.

| Key | Default | Bounds |
| --- | ---: | --- |
| `download.max_running_jobs` | 3 | Worker slots — concurrently running jobs |
| `download.max_parallel_downloads` | 16 | Encrypted media transfers in flight |
| `download.max_parallel_decrypts` | 32 | Decrypt operations in flight |
| `download.max_parallel_wrapper_requests` | 24 | Wrapper data RPCs — M3U8, lyrics, web playback, license |
| `catalog.max_parallel_requests` | 16 | Apple control-plane HTTP: Catalog API, web token, HLS manifests, artwork |
| `catalog.requests_per_second` | 10 | Sustained authenticated Catalog / amp-api rate |
| `catalog.request_burst` | 16 | Token-bucket burst for the same traffic |

Wrapper login and logout are exempt from `max_parallel_wrapper_requests` so operator
access is never starved; decrypt streams are bounded by `max_parallel_decrypts` instead.
Media already downloaded but not yet decrypted is additionally held back by internal
in-flight backpressure — with `D` download and `X` decrypt permits the internal in-flight
ceiling is `D+X`.

When Apple returns 429 the backend honours `Retry-After`, triggers a global cooldown and
retries once automatically.

When several jobs compete for a full pool, permits go to the **earliest submitted**
unfinished job, so jobs tend to complete one after another rather than all creeping
forward together. Recovered jobs keep their original submission time and do not lose their
place across a restart. Priority only matters while the pool is saturated — when the
leading job cannot fill it, spare permits go straight to later jobs, so no throughput is
wasted. URL validation, quality probes and other interactive API requests never queue
behind jobs.

`download.progress_event_interval_ms` is documented in [api.md](api.md#events).

## Memory modes

`download.memory_mode` controls the per-track RAM/disk trade-off on the Enhanced HLS path.
Both modes decrypt fragment by fragment straight into `ffmpeg`; neither writes a plaintext
whole-track file.

**`low` (default)** streams the encrypted media into a resumable checkpoint on disk and
reads it back fragment by fragment. RAM stays fragment-sized, but the encrypted checkpoint
and the flattened output coexist in `temp_dir` — roughly two track-sized files per
in-flight track.

**`high`** keeps one encrypted track in RAM instead of that checkpoint, so only the
flattened output remains in `temp_dir`. The observed Go heap can temporarily approach twice
the track size because the garbage collector retains allocation headroom, and both process
memory and temp usage scale with the pools above. In-memory media is capped at 512 MiB per
track; larger responses fail explicitly rather than risking an OOM. If a CDN response
carries a usable `ETag` or `Last-Modified` validator and is interrupted after making
progress, high mode retries once with a `Range` request from its in-memory prefix — it does
not persist that prefix, so a process or container restart still begins at byte zero.

AAC-LC WebPlayback is unaffected by this option: both modes keep its encrypted retry buffer
in memory and stream the decrypted output straight into `ffmpeg`. The Widevine parser may
still hold decoded track structures until that stream completes.

A per-job `overrides.memory_mode` can pick a different mode without changing the runtime
default.

**Which to use.** Keep `low` unless you have measured a temp-disk bottleneck. High mode is
5–19% faster in tests that exclude CDN variance, but the gain comes from skipping the
encrypted checkpoint's filesystem I/O, and it is easily hidden by a slow network or a fast
local temp disk. Its memory cost cannot be estimated per worker or per average track:
every downloaded-but-not-yet-decrypted track holds a complete encrypted buffer until
decryption consumes it. Start at 1/1 or 2/2 concurrency and only raise it once the peak is
comfortable. Full numbers in [benchmarks.md](benchmarks.md).

## Lyrics

- `download.embed_lyrics` — write lyrics into the MP4 tag.
- `download.save_lyrics_file` — save an `.lrc` or `.ttml` sidecar next to the audio.
- `download.lyrics_format` — `lrc` or `ttml`; `ttml` preserves the wrapper's original TTML.
- `download.lyrics_type` — `lyrics` or `syllable-lyrics` (word-timed).
- `download.lyrics_extras` — `translation`, `pronunciation`; only applied when
  `lyrics_format: "lrc"`.

Lyrics, word-timed lyrics, translations and transliterations all require `wrapper-manager`
to hold a valid Apple Music subscription session. A lyrics fetch or conversion failure
never aborts the audio download: the file is saved without lyrics and the item's status
explains why. Each item also carries a `lyrics_status` of `fetched`, `failed`, `none`
(catalog reports no lyrics) or `disabled` (neither embedding nor sidecars are on).

## Output paths

One template per task type, resolved relative to `download.downloads_dir`. The last
segment is the file name and gets `.m4a` appended. Every segment is sanitised
independently, so a template value can never create extra directories.

| Key | Default |
| --- | --- |
| `download.song_path_format` | `songs/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}` |
| `download.album_path_format` | `albums/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}` |
| `download.artist_path_format` | `artists/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}` |
| `download.playlist_path_format` | `playlists/{PlaylistName}/{SongNumber:02d}. {SongName}` |
| `download.station_path_format` | `stations/{StationName}/{SongNumber:02d}. {SongName}` |

Artist jobs expand into that artist's albums and songs. `{SongNumber}` is the 1-based
position within a playlist or station.

With defaults, an album track lands at:

```text
data/downloads/albums/{ArtistName}/{AlbumName}/{TrackNumber:02d}. {SongName}.m4a
```

The full variable list is in the `download` section of
[`configs/config.example.yaml`](../configs/config.example.yaml). In directory segments
`{ArtistName}` resolves to the collection's grouping artist (album artist when available)
so all of an album's tracks share one folder; in the file-name segment it is the track's
own artist. Numeric variables — `{SongNumber}`, `{DiscNumber}`, `{DiscCount}`,
`{TrackNumber}`, `{TrackCount}` — support `:02d` padding. Unknown variables are preserved
literally.

### Standalone covers

- `download.save_album_cover: true` — `cover.jpg` (or `.png`) in the album directory.
- `download.save_artist_cover: true` — `artist.jpg` (or `.png`) in the artist directory.
- `download.save_playlist_cover: true` — `cover.jpg` (or `.png`) in the playlist directory.

Cover destinations are located through the path template's variables: the album cover goes
into the deepest directory segment referencing `{AlbumName}` or `{AlbumId}` (falling back
to the directory holding the audio), and the artist cover into the deepest segment
referencing `{ArtistName}`, `{UrlArtistName}`, `{AlbumArtist}` or `{ArtistId}` — if no
directory segment references the artist, the artist cover is skipped. The extension follows
`download.cover_format`. Playlists and stations are flat directories: they can hold their
own cover, but no album or artist covers are written there.
