// Album progress for the job drawer, after the AMDL iOS app's
// SegmentedProgressBar (DownloadDetailSummaryView.swift): one bar with a solid
// fill for the tracks that have finished and a translucent fill for the
// overall progress, cut into one cell per track while the cells stay legible.
// internal/progress#AlbumProgress is the Go version for the CLI, TUI and
// desktop app; keep the two in step.

import { STAGE_WEIGHTS } from './status.js';

/** Above this many tracks the bar is not cut into cells (iOS `segmentedTicks`). */
export const TICK_MAX = 24;

/** Track statuses that end a track, with the role the bar draws them in. */
const OUTCOMES = { completed: 'success', skipped_existing: 'skipped', failed: 'danger', cancelled: 'neutral' };

/** The order finished tracks take when their finish times are unknown. */
const OUTCOME_RANK = { completed: 0, skipped_existing: 1, failed: 2, cancelled: 3 };

const clamp01 = (v) => (typeof v === 'number' && v > 0 ? Math.min(1, v) : 0);

/**
 * One track's progress, 0..1, weighted by STAGE_WEIGHTS. Completed and
 * already-on-disk tracks count as 1; a failed or cancelled track stays where it
 * stopped.
 */
export function itemFraction(item) {
  const status = item && item.status;
  if (status === 'completed' || status === 'skipped_existing') return 1;
  const p = (item && item.progress) || {};
  if (p.saved) return 1;
  const w = STAGE_WEIGHTS;
  return clamp01(
    (p.resolved ? w.resolve : 0) +
      w.download * clamp01(p.download) +
      w.decrypt * clamp01(p.decrypt) +
      (p.remuxed ? w.remux : 0) +
      (p.verified ? w.verify : 0) +
      (p.tagged ? w.tag : 0),
  );
}

const finishTime = (item) => {
  const t = Date.parse((item && item.updated_at) || '');
  return Number.isFinite(t) ? t : Infinity;
};

/**
 * The drawer's album bar from the job status and its items.
 *
 * - `runs`: the solid fill, left to right, as runs of one outcome each, in the
 *   order the tracks finished (`updated_at`, else completed, skipped, failed,
 *   cancelled). `share` is the run's part of the bar's width. Segment k lights
 *   when the k-th track finishes, whichever track that is.
 * - `live`: the translucent fill's end, never behind the solid fill.
 * - `percent`: the overall progress, the mean of itemFraction. It stays below
 *   100 while a track is still working (200 tracks with one left would round
 *   up) and is 100 once the job completes. `fraction` is the same, unrounded
 *   (0..1), for the time-left estimate (lib/speed.js).
 * - `ticks`: cells to cut the bar into, 0 for none (one track, or more than
 *   TICK_MAX).
 * - `onDisk`: the tracks saved or already on disk, what a job that ended
 *   short of completion counts.
 *
 * A single track is not segmented: the solid fill is its own progress.
 * @returns {{total: number, lit: number, onDisk: number, runs: {state: string, role: string, share: number}[], done: number, live: number, percent: number, fraction: number, ticks: number}}
 */
export function albumProgress(jobStatus, items) {
  const list = Array.isArray(items) ? items.filter(Boolean) : [];
  const total = list.length;
  const finished = list
    .filter((it) => OUTCOMES[it.status])
    .sort(
      (a, b) =>
        finishTime(a) - finishTime(b) ||
        OUTCOME_RANK[a.status] - OUTCOME_RANK[b.status] ||
        (a.index || 0) - (b.index || 0),
    );
  const mean = total ? list.reduce((sum, it) => sum + itemFraction(it), 0) / total : 0;
  const working = finished.length < total;
  const overall = jobStatus === 'completed' ? 1 : working ? Math.min(mean, 0.99) : mean;

  const runs = [];
  if (total === 1) {
    const state = finished.length ? finished[0].status : 'running';
    runs.push({ state, role: OUTCOMES[state] || 'info', share: finished.length ? 1 : overall });
  } else {
    for (const it of finished) {
      const last = runs[runs.length - 1];
      if (last && last.state === it.status) last.share += 1 / total;
      else runs.push({ state: it.status, role: OUTCOMES[it.status], share: 1 / total });
    }
  }
  const done = Math.min(1, runs.reduce((sum, r) => sum + r.share, 0));
  return {
    total,
    lit: finished.length,
    onDisk: finished.filter((it) => it.status === 'completed' || it.status === 'skipped_existing').length,
    runs: runs.filter((r) => r.share > 0),
    done,
    live: total > 1 ? Math.max(done, overall) : done,
    percent: Math.round(overall * 100),
    fraction: overall,
    ticks: total >= 2 && total <= TICK_MAX ? total : 0,
  };
}
