# Benchmarks

Two measurements, both on the current implementation: a post-decrypt micro-benchmark that
isolates the flatten/verify/tag path, and an end-to-end run over two real albums with the
CDN taken out of the picture.

## Post-decrypt disk path

The optimised path is: encrypted input → fragment-by-fragment decrypt → `ffmpeg` stdin →
flattened file → integrity check → tagging → final save. Tagging rewrites only the smaller
`moov` and artwork region for the tail-`moov` layout `ffmpeg` normally produces, leaving
the media in place; compatible layouts that cannot be updated safely in place fall back to
the previous whole-track rewrite.

ROG (Ryzen 9 9900X, WSL Docker) ran an interleaved A/B of the pre-optimisation baseline
against the current implementation, using an offline 192 kHz/24-bit ALAC fixture of about
70.1 MB, 45 segments, roughly 90 seconds. Both ran in the same production image with
`ffmpeg` 6.1.2. Five runs per mode on the same-volume Windows layout after warm-up, three
per mode on the deployment-like layout. Temporary space is the simultaneous peak of
`download.temp_dir` and the system temp directory, excluding the final downloads directory.

| Layout | Mode | Before | Now | Speed-up | Temp before | Temp now |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Temp and output both on the Windows volume | Low | 4774.6 ms | 2585.3 ms | 45.9% | 267.3 MiB | 133.7 MiB |
| Temp and output both on the Windows volume | High | 2962.0 ms | 1341.0 ms | 54.7% | 133.6 MiB | 66.8 MiB |
| WSL-local temp → Windows output volume | Low | 1989.0 ms | 1911.5 ms | 3.9% | 267.3 MiB | 133.7 MiB |
| WSL-local temp → Windows output volume | High | 1947.0 ms | 1845.9 ms | 5.2% | 133.6 MiB | 66.8 MiB |

Every run had external network access blocked and produced no unexpected HTTP requests.
The final files decode completely and their 32-bit PCM SHA-256 matched the golden file on
every run. A tagging micro-benchmark over 64 MiB of media plus a 1 MiB cover went from a
median of 46.0 ms to 11.4 ms (about 4.0×) on the same machine; that gain comes from not
copying the media region and does not translate directly into whole-pipeline speed-up.

Both memory modes have eliminated plaintext intermediate files, so `high` now differs from
`low` by exactly one encrypted checkpoint. On the cross-filesystem layout that resembles a
real deployment, High finished this single-track fixture only about 3.4% faster than Low
while a whole encrypted track raises the heap peak substantially. `low` remains the safer
default. If your temp directory sits on a slow download volume, memory is plentiful and
concurrency is controlled, `high` can still remove that checkpoint I/O. The final
cross-filesystem save still keeps one `.part` copy to preserve complete-file publication
and failure-cleanup semantics; putting `download.temp_dir` on the same filesystem as the
downloads directory degrades that step to an atomic rename.

## End-to-end, by memory mode

To remove Apple CDN variance, the raw encrypted ALAC media for two real albums was cached
first, then replayed read-only with origin fetches forbidden. Everything else is the full
production path: job creation through the API, metadata resolution, `wrapper-manager`
decryption, `ffmpeg` remux, integrity decode, tagging and the final save. Each unit used a
fresh backend container, database, temp directory and output directory, and the clock
started only after `wrapper-manager` had two Ready instances again.

- The formal matrix ran on v1.3.2 / `59befc1`. v1.3.3 / `13c49a7`, merged during testing,
  only refactored the media-discovery code shared by the download paths — source
  resolution, variant selection and transfer logic are equivalent, confirmed with paired
  Low/High smoke runs on the same cache, unit tests and audio verification.
- [月姫 -A piece of blue glass moon- THEME SONG E.P](https://music.apple.com/cn/album/1580904295):
  8 tracks, about 841 MiB of raw media.
  [Fate/stay night [Realta Nua] Soundtrack Reproduction](https://music.apple.com/cn/album/1576634760):
  62 tracks, about 1.14 GiB.
- `download.max_parallel_downloads` and `download.max_parallel_decrypts` were both set to
  1, 2, 4 and 8. Each album × mode × D/X combination ran three interleaved rounds; the
  tables are three-round averages.
- D/X are process-wide pools shared across jobs, not "at most p tracks per job". The
  internal in-flight ceiling is D+X, so at D=X=p the pipeline allows up to 2p downloaded
  but not yet decrypted tracks.
- Memory is the whole container's cgroup peak, including process memory and the file page
  cache charged to the cgroup. Temporary space is the simultaneous peak of
  `download.temp_dir` and the system temp directory, excluding the downloads directory.
- All 48 units completed, ALAC was forced throughout, and there were zero failed tracks and
  zero cache origin fetches. Per-track audio packet SHA-256 matched across both modes at
  every concurrency.

**月姫** — long Hi-Res tracks, which amplify whole-track memory and checkpoint costs:

| D/X | Low time | High time | High gain | Low cgroup peak | High cgroup peak | Low temp peak | High temp peak |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1/1 | 51.3 s | 44.6 s | 13.0% | 137 MiB | 772 MiB | 394 MiB | 135 MiB |
| 2/2 | 32.1 s | 26.7 s | 16.9% | 150 MiB | 1,345 MiB | 714 MiB | 233 MiB |
| 4/4 | 23.8 s | 19.3 s | 19.2% | 227 MiB | 1,931 MiB | 1,260 MiB | 446 MiB |
| 8/8 | 22.1 s | 18.8 s | 14.8% | 344 MiB | 2,017 MiB | 1,550 MiB | 677 MiB |

**Fate** — many short tracks, so fixed session, `ffmpeg` startup, verification and tagging
overheads weigh more:

| D/X | Low time | High time | High gain | Low cgroup peak | High cgroup peak | Low temp peak | High temp peak |
| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 1/1 | 157.1 s | 134.3 s | 14.5% | 210 MiB | 369 MiB | 144 MiB | 72 MiB |
| 2/2 | 75.6 s | 69.4 s | 8.2% | 218 MiB | 485 MiB | 217 MiB | 72 MiB |
| 4/4 | 63.1 s | 58.7 s | 6.9% | 214 MiB | 641 MiB | 245 MiB | 98 MiB |
| 8/8 | 59.1 s | 56.0 s | 5.3% | 266 MiB | 960 MiB | 382 MiB | 106 MiB |

Fate 1/1 High was 160.2 s on the first round and 121.6 s / 121.0 s on the next two — a
wall-clock coefficient of variation of 16.7%; an extra run measured 119.9 s. The table
keeps the pre-agreed three-round average without discarding the cold sample. Every other
combination had a three-round CV between 0.2% and 8.5%.

### Reading the numbers

High does not use a faster decryption algorithm. Both modes decrypt fragment by fragment
straight into `ffmpeg`. The difference is that Low writes a resumable encrypted `raw-*`
checkpoint to the temp volume and reads it back, while High substitutes an in-memory whole
track for that write and re-read. That is why High is roughly 5–19% faster here with about
5–9% less container CPU time — the saving is filesystem I/O and syscalls, which
Windows/WSL mounted volumes amplify. On a slow public download or with the temp directory
on fast local storage, that I/O is hidden behind network or CPU work and the gap usually
narrows.

For a track being remuxed, Low's temp directory holds similarly sized `raw-*` and `flat-*`
files at once, so about twice a single track's size is expected — it is not a plaintext
`dec-*` reappearing. Across a concurrent album the peak also depends on which tracks happen
to be in which stage, so it is not strictly double. High mostly keeps only `flat-*`.

High's memory cost cannot be estimated per worker or per average album track: every
downloaded-but-not-yet-decrypted track keeps a complete encrypted buffer until decryption
consumes it, on top of Go's GC allocation headroom. At D=X=p the internal ceiling allows
D+X media in flight, so the peak grows quickly at high concurrency. Keep the default `low`
when the memory budget is unclear; with `high`, start at 1/1 or 2/2 and raise it only after
confirming that temp-disk I/O is the bottleneck and that the peak fits. Local replay shows
the backend's processing ceiling, not absolute download times on an ordinary CDN.
