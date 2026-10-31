// Pure status mapping: every state the API can report becomes one
// {icon, role, label, detail?} view, so color is never the only cue.
// Roles are the status token families in css/tokens.css.

import { errorSummary } from './format.js';

/** @typedef {{icon: string, role: 'info'|'success'|'danger'|'warning'|'neutral'|'skipped', label: string, detail?: string, pulse?: boolean}} StatusView */

/** Counters of a job, with the 2.0 disjoint fields defaulted to 0. */
export function jobCounts(job) {
  const n = (v) => (typeof v === 'number' && v > 0 ? v : 0);
  const total = n(job && job.total_items);
  const completed = n(job && job.completed_items);
  const skipped = n(job && job.skipped_items);
  const failed = n(job && job.failed_items);
  const cancelled = n(job && job.cancelled_items);
  return { total, completed, skipped, failed, cancelled, done: completed + skipped };
}

/** Whether a completed job wrote nothing because every track was already on disk. */
export function isAlreadyInLibrary(job) {
  const c = jobCounts(job);
  return Boolean(job && job.status === 'completed' && c.total > 0 && c.completed === 0 && c.skipped === c.total);
}

/** @returns {StatusView} */
export function jobStatusView(job) {
  const status = job && job.status;
  const c = jobCounts(job);
  switch (status) {
    case 'queued':
      return { icon: 'circle-dashed', role: 'neutral', label: 'Queued' };
    case 'running':
      return { icon: 'dot', role: 'info', label: 'Running', pulse: true, detail: c.total === 0 ? 'Resolving' : undefined };
    case 'completed':
      if (isAlreadyInLibrary(job)) {
        return { icon: 'archive', role: 'skipped', label: 'Already in library', detail: 'Every track was already on disk' };
      }
      return {
        icon: 'circle-check',
        role: 'success',
        label: 'Completed',
        detail: c.skipped > 0 ? `${c.skipped} already on disk` : undefined,
      };
    case 'failed':
      return { icon: 'circle-x', role: 'danger', label: 'Failed', detail: (job && job.error) || undefined };
    case 'cancelled':
      return { icon: 'circle-slash', role: 'neutral', label: 'Cancelled' };
    default:
      return { icon: 'circle-help', role: 'neutral', label: status ? String(status) : 'Unknown' };
  }
}

const ITEM_STAGES = {
  resolving: ['search', 'Resolving'],
  downloading: ['download', 'Downloading'],
  decrypting: ['key', 'Decrypting'],
  remuxing: ['layers', 'Remuxing'],
  tagging: ['tag', 'Tagging'],
  saving: ['save', 'Saving'],
};

/** @returns {StatusView & {retry?: string}} */
export function itemStatusView(item) {
  const status = item && item.status;
  let view;
  if (ITEM_STAGES[status]) {
    const [icon, label] = ITEM_STAGES[status];
    view = { icon, role: 'info', label };
  } else {
    switch (status) {
      case 'queued':
        view = { icon: 'circle-dashed', role: 'neutral', label: 'Queued' };
        break;
      case 'waiting_download':
        view = { icon: 'clock', role: 'neutral', label: 'Waiting for download slot' };
        break;
      case 'waiting_decrypt':
        view = { icon: 'clock', role: 'neutral', label: 'Waiting to decrypt' };
        break;
      case 'completed':
        view = { icon: 'circle-check', role: 'success', label: 'Completed' };
        break;
      case 'skipped_existing':
        view = { icon: 'archive', role: 'skipped', label: 'Already on disk' };
        break;
      case 'failed':
        view = { icon: 'circle-x', role: 'danger', label: 'Failed', detail: (item && item.error) || undefined };
        break;
      case 'cancelled':
        view = { icon: 'circle-slash', role: 'neutral', label: 'Cancelled' };
        break;
      default:
        view = { icon: 'circle-help', role: 'neutral', label: status ? String(status) : 'Unknown' };
    }
  }
  if (item && item.attempt > 1 && !['completed', 'skipped_existing', 'cancelled'].includes(status)) {
    view.retry = item.max_attempts > 0 ? `Retry ${item.attempt}/${item.max_attempts}` : `Retry ${item.attempt}`;
  }
  return view;
}

/** @returns {StatusView} */
export function hookStatusView(status) {
  switch (status) {
    case 'running':
      return { icon: 'dot', role: 'info', label: 'Running' };
    case 'succeeded':
      return { icon: 'circle-check', role: 'success', label: 'Succeeded' };
    case 'failed':
      return { icon: 'circle-x', role: 'danger', label: 'Failed' };
    case 'interrupted':
      return { icon: 'triangle-alert', role: 'warning', label: 'Interrupted' };
    default:
      return { icon: 'circle-help', role: 'neutral', label: status ? String(status) : 'Unknown' };
  }
}

const LEVELS = {
  debug: { short: 'DBG', role: 'neutral', label: 'Debug' },
  info: { short: 'INF', role: 'neutral', label: 'Info' },
  warn: { short: 'WRN', role: 'warning', label: 'Warn' },
  error: { short: 'ERR', role: 'danger', label: 'Error' },
};

/** Log level marker: 3-letter code, role and label. */
export function logLevelView(level) {
  return LEVELS[level] || { short: String(level || '?').slice(0, 3).toUpperCase(), role: 'neutral', label: String(level || 'Unknown') };
}

/** @returns {StatusView} */
/**
 * Copy for the log routes answering 503 feature_unavailable, which the backend
 * does when logging.buffer_size is 0 and it keeps no log history.
 */
export const LOG_RETENTION_OFF = 'In-memory log retention is off (logging.buffer_size: 0)';

export function connectionView(state) {
  switch (state) {
    case 'live':
      return { icon: 'dot', role: 'success', label: 'Live updates on' };
    case 'reconnecting':
      return { icon: 'dot', role: 'warning', label: 'Reconnecting' };
    case 'offline':
      return { icon: 'dot', role: 'danger', label: 'Offline' };
    case 'paused':
      return { icon: 'dot', role: 'neutral', label: 'Paused' };
    default:
      return { icon: 'dot', role: 'neutral', label: 'Connecting' };
  }
}

/**
 * Segmented progress of a job: done (completed + skipped), failed, of total.
 * The text is a count, except while a running job has nothing finished yet:
 * the count would stand still for the whole first track, so the job's overall
 * progress (Job.progress, from each track's stages) fills the track and the
 * text is its percentage.
 */
export function jobProgress(job) {
  const c = jobCounts(job);
  const indeterminate = c.total === 0 && job && (job.status === 'running' || job.status === 'queued');
  const working = Boolean(job) && job.status === 'running' && c.total > 0 && c.done === 0 && c.failed === 0 && c.cancelled === 0;
  const fraction = working ? clamp01(job.progress) : 0;
  const percent = Math.floor(fraction * 100);
  let aria = `${c.done} of ${c.total} tracks`;
  if (c.skipped) aria += `, ${c.skipped} already on disk`;
  if (c.failed) aria += `, ${c.failed} failed`;
  if (c.cancelled) aria += `, ${c.cancelled} cancelled`;
  if (working) aria += `, ${percent}% done`;
  return {
    ...c,
    indeterminate,
    working,
    fraction,
    text: indeterminate ? 'Resolving' : working ? `${percent}%` : `${c.done} / ${c.total}`,
    aria: indeterminate ? 'Resolving tracks' : aria,
  };
}

/** Jobs with at most this many tracks get one progress cell per track. */
export const CELL_MAX = 60;

const CELL_ROLES = { completed: 'success', skipped: 'skipped', failed: 'danger', cancelled: 'neutral', remaining: '' };

/**
 * One cell per track for the per-track progress strip, as ordered runs:
 * completed, skipped (already on disk), failed, cancelled, then remaining.
 * The list payload carries counts only, so cells are not in track order.
 * Null when the job has no tracks yet or more than CELL_MAX.
 * @returns {{state: string, role: string}[] | null}
 */
export function progressCells(job) {
  const c = jobCounts(job);
  if (c.total === 0 || c.total > CELL_MAX) return null;
  const out = [];
  const push = (state, n) => {
    for (let i = 0; i < n && out.length < c.total; i++) out.push({ state, role: CELL_ROLES[state] });
  };
  push('completed', c.completed);
  push('skipped', c.skipped);
  push('failed', c.failed);
  push('cancelled', c.cancelled);
  push('remaining', c.total - out.length);
  return out;
}

const LEGEND_LABELS = { completed: 'saved', skipped: 'on disk', failed: 'failed', cancelled: 'cancelled', remaining: 'remaining' };

/**
 * The progress legend: one entry per cell state, in cell order, with its
 * count. Every state is listed, zeros included (a skin may hide zeros);
 * "remaining" covers tracks in flight and waiting alike, because the list
 * payload does not tell them apart. Null while the job has no tracks.
 * @returns {{state: string, role: string, n: number, label: string}[] | null}
 */
export function progressLegend(job) {
  const c = jobCounts(job);
  if (c.total === 0) return null;
  let left = c.total;
  const take = (n) => {
    const v = Math.min(n, left);
    left -= v;
    return v;
  };
  const counts = { completed: take(c.completed), skipped: take(c.skipped), failed: take(c.failed), cancelled: take(c.cancelled) };
  counts.remaining = left;
  return Object.keys(LEGEND_LABELS).map((state) => ({ state, role: CELL_ROLES[state], n: counts[state], label: LEGEND_LABELS[state] }));
}

const clamp01 = (v) => (typeof v === 'number' && v > 0 ? Math.min(1, v) : 0);

const STAGE_LABELS = { resolve: 'Resolve', download: 'Download', decrypt: 'Decrypt', remux: 'Remux', verify: 'Verify', tag: 'Tag', save: 'Save' };

/**
 * Each stage's share of a track's wall time, which sets the width of its
 * segment: the measured shares of a typical ALAC track on a home connection.
 * Download dominates and depends on the network; resolve and decrypt take
 * about a tenth each and the finishing steps a sliver. They add up to 1: they
 * size the stage segments, and the drawer's album bar weights its overall
 * progress with them (lib/album-progress.js). The Go clients use the same
 * values (internal/progress#Weights); TestWeightsMatchConsole there keeps them
 * equal.
 */
export const STAGE_WEIGHTS = Object.freeze({ resolve: 0.13, download: 0.8, decrypt: 0.02, remux: 0.0125, verify: 0.0125, tag: 0.0125, save: 0.0125 });

/**
 * The seven pipeline stages of one item. Download and decrypt carry their
 * own fraction; the others are done/not done. Each carries its
 * STAGE_WEIGHTS share as `weight`. The API carries no overall percentage
 * (ItemProgress); lib/album-progress.js computes the drawer's.
 */
export function stageSegments(item) {
  const p = (item && item.progress) || {};
  const status = item && item.status;
  const finished = status === 'completed';
  const download = finished ? 1 : clamp01(p.download);
  const decrypt = finished ? 1 : clamp01(p.decrypt);
  const frac = (key, label, v, active) => {
    let state = v >= 1 ? 'done' : v > 0 ? 'partial' : 'empty';
    if (active && v === 0) state = 'indeterminate';
    return { key, label, state, fraction: v, weight: STAGE_WEIGHTS[key] };
  };
  const flag = (key, label, on) => ({ key, label, state: on ? 'done' : 'empty', fraction: on ? 1 : 0, weight: STAGE_WEIGHTS[key] });
  // A track already on disk ran no stage, but it is finished: a full bar in
  // the skipped state, which the skin draws in its quiet ink, not success.
  if (status === 'skipped_existing') {
    return Object.entries(STAGE_LABELS).map(([key, label]) => ({ key, label, state: 'skipped', fraction: 1, weight: STAGE_WEIGHTS[key] }));
  }
  const verify = flag('verify', 'Verify', Boolean(p.verified));
  if (finished && !p.verified) verify.state = 'unverified';
  return [
    flag('resolve', 'Resolve', Boolean(p.resolved) || finished),
    frac('download', 'Download', download, status === 'downloading'),
    frac('decrypt', 'Decrypt', decrypt, false),
    flag('remux', 'Remux', Boolean(p.remuxed) || finished),
    verify,
    flag('tag', 'Tag', Boolean(p.tagged) || finished),
    flag('save', 'Save', Boolean(p.saved) || finished),
  ];
}

/** Accessible description of the stage segments, e.g. "Download 45%, decrypt not started". */
export function stageText(segments) {
  if (segments.length && segments.every((s) => s.state === 'skipped')) return 'Already on disk, nothing downloaded';
  return segments
    .map((s) => {
      if (s.state === 'unverified') return `${s.label}: not verified`;
      if (s.state === 'indeterminate') return `${s.label}: running, size unknown`;
      if (s.key === 'download' || s.key === 'decrypt') {
        if (s.state === 'partial') return `${s.label}: ${Math.round(s.fraction * 100)}%`;
      }
      return `${s.label}: ${s.state === 'done' ? 'done' : 'not started'}`;
    })
    .join(', ');
}

const ACTIVITY_VIEWS = {
  job_queued: ['Queued', 'neutral'],
  job_started: ['Started', 'info'],
  job_retried: ['Retried', 'info'],
  job_recovered: ['Recovered after a restart', 'warning'],
  job_finished: ['Finished', 'success'],
  job_failed: ['Failed', 'danger'],
  job_cancelled: ['Cancelled', 'neutral'],
  resolved_input: ['Resolved', 'neutral'],
  motion_artwork_resolved: ['Motion cover found', 'neutral'],
  standalone_cover_failed: ['Cover failed', 'warning'],
  item_failed: ['Track failed', 'danger'],
  item_overwrite: ['Track replaced', 'neutral'],
  codec_fallback: ['Codec fallback', 'warning'],
  codec_failed: ['Codec failed', 'warning'],
  alac_repaired: ['ALAC repaired', 'neutral'],
  operation_retry: ['Retrying', 'warning'],
  operation_exhausted: ['Gave up', 'danger'],
  operation_recovered: ['Recovered', 'neutral'],
  hook_started: ['Hook started', 'info'],
  hook_succeeded: ['Hook succeeded', 'success'],
  hook_failed: ['Hook failed', 'danger'],
};

/**
 * One Activity entry of the job drawer as {label, role, text}: a word for the
 * event type, and the event's own message as one line when it adds something
 * (the default "job queued" repeats the label; an error loses its JSON body).
 */
export function activityView(entry) {
  const type = (entry && entry.type) || '';
  const known = ACTIVITY_VIEWS[type];
  const plain = type.replaceAll('_', ' ');
  const label = known ? known[0] : plain.charAt(0).toUpperCase() + plain.slice(1);
  const message = errorSummary(entry && entry.message);
  const phase = entry && entry.phase ? String(entry.phase).replaceAll('_', ' ') : '';
  const text = message && message.toLowerCase() !== plain ? message : phase;
  return { label, role: known ? known[1] : 'neutral', text };
}

/**
 * The Active list with running jobs first, each group in the order given: a
 * batch shares one created_at, so the server cannot say which of its jobs
 * run first, but the ones already running belong on top.
 */
export function runningFirst(jobs) {
  const running = jobs.filter((j) => j.status === 'running');
  return running.length === 0 || running.length === jobs.length ? jobs : [...running, ...jobs.filter((j) => j.status !== 'running')];
}

const TERMINAL = new Set(['completed', 'failed', 'cancelled']);

/** Whether a job status is terminal. */
export function isTerminal(status) {
  return TERMINAL.has(status);
}
