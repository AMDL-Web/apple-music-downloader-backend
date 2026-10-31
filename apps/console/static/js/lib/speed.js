// A running job's average download speed and the time it has left, as the
// desktop app computes them (apps/desktop/internal/store/jobs.go Transferred
// and AvgSpeed, internal/progress#Rate, internal/format#Rate and #Remaining;
// keep the two in step). The API reports no live transfer rate, so the speed
// is an average over the run, and the time left comes from how fast the
// job's overall progress moved over the last RATE_WINDOW_MS.

/** How far back the samples behind the time-left estimate reach (internal/progress rateWindow). */
export const RATE_WINDOW_MS = 30000;

/** Samples closer than this are one: the drawer's tracks update many times a second. */
const SAMPLE_GAP_MS = 500;

const bytesOf = (n) => (typeof n === 'number' && n > 0 ? n : 0);

/**
 * The bytes a run has moved so far: the sizes of the tracks written plus
 * what the tracks still in flight have downloaded, so the average has a value
 * from the first downloaded bytes. Tracks already on disk were not moved.
 */
export function transferred(items) {
  let n = 0;
  for (const it of Array.isArray(items) ? items : []) {
    if (!it) continue;
    n += it.status === 'completed' ? bytesOf(it.file_size) : bytesOf(it.progress && it.progress.download_bytes);
  }
  return n;
}

/**
 * Bytes per second over the run: until `nowMs`, or until the job finished.
 * 0 when unknown (no start time, or under a second in).
 */
export function avgSpeed(job, items, nowMs) {
  const start = Date.parse((job && job.started_at) || '');
  if (!Number.isFinite(start)) return 0;
  const finish = Date.parse((job && job.finished_at) || '');
  const end = Number.isFinite(finish) ? finish : nowMs;
  const elapsed = (end - start) / 1000;
  if (!(elapsed >= 1)) return 0;
  return transferred(items) / elapsed;
}

/** An empty set of progress samples. */
export function createRate() {
  return { samples: [] };
}

/**
 * Records a job's overall progress (0..1) at a moment, dropping samples older
 * than the window but keeping the oldest one inside it as the rate's base.
 * Progress that went back (a retry) starts over.
 */
export function addSample(rate, atMs, progress) {
  const s = rate.samples;
  const last = s[s.length - 1];
  if (last && progress < last.progress) s.length = 0;
  else if (last && atMs - last.at < SAMPLE_GAP_MS) s.pop();
  s.push({ at: atMs, progress });
  let cut = 0;
  while (cut < s.length - 2 && atMs - s[cut + 1].at > RATE_WINDOW_MS) cut++;
  if (cut > 0) s.splice(0, cut);
}

/** The estimated milliseconds left, or 0 with too little to go on. */
export function remainingMs(rate) {
  const s = rate.samples;
  if (s.length < 2) return 0;
  const first = s[0];
  const last = s[s.length - 1];
  const dt = last.at - first.at;
  const dp = last.progress - first.progress;
  if (dt < 3000 || dp <= 0 || last.progress >= 1) return 0;
  return (dt * (1 - last.progress)) / dp;
}

const trimmed = (v) => String(Number(v.toFixed(1)));

/** Bytes per second in decimal units: "41 MB/s", "1.5 MB/s", "120 MB/s". '' for none. */
export function formatRate(bps) {
  if (!(bps > 0)) return '';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let v = bps;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 || i === 0 ? v.toFixed(0) : trimmed(v)} ${units[i]}/s`;
}

/** An estimate of time left: "~58 s", "~4 min", "~1.5 h". '' for none. */
export function formatRemaining(ms) {
  if (!(ms > 0)) return '';
  if (ms < 60000) return `~${Math.round(ms / 1000)} s`;
  if (ms < 3600000) return `~${Math.round(ms / 60000)} min`;
  return `~${trimmed(ms / 3600000)} h`;
}

/**
 * The words for a job's speed, each '' when unknown: `rate` ("41 MB/s"),
 * `time` ("~1 min"), and with the filler words `avg` ("avg 41 MB/s") and
 * `left` ("~1 min left"). `time` is only for a running job.
 */
export function speedWords(avgBps, leftMs) {
  const rate = formatRate(avgBps);
  const time = formatRemaining(leftMs);
  return { rate, time, avg: rate ? `avg ${rate}` : '', left: time ? `${time} left` : '' };
}
