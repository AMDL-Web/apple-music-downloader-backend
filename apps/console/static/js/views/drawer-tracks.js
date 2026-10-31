// The job drawer's track sections: the album progress bar and the track list. A track is one line of number, title, length and size;
// its artist, codec chip and status with stage meters appear only when they
// say something (lib/jobview.js trackView).

import { html } from '../ui/preact.js';
import { trackLength, bytes, codecMeaning } from '../lib/format.js';
import { albumCodec, trackView, trackNote, stagePercent } from '../lib/jobview.js';
import { itemStatusView, isTerminal } from '../lib/status.js';
import { albumProgress } from '../lib/album-progress.js';
import { useJobRate } from '../ui/job-rate.js';
import { StatusText, StageTrack, Icon, Chip } from '../ui/components.js';

const OUTCOME_WORDS = { completed: 'saved', skipped_existing: 'already on disk', failed: 'failed', cancelled: 'cancelled' };

/**
 * The album bar, after the AMDL iOS app (lib/album-progress.js): a solid fill
 * of the finished tracks in the order they finished, a translucent fill to the
 * overall stage-weighted progress, and under it the finished count and the
 * percent. A job that failed or was cancelled counts the tracks on disk
 * instead and drops the percent, which no longer measures anything; so does
 * a single track, whose row has its own stage percent beside it. Drawn
 * from the first render, resolving included, so nothing below it moves when
 * tracks arrive or the job ends.
 */
export function AlbumProgress({ job, items }) {
  const p = albumProgress(job.status, items);
  const rate = useJobRate(job, items, p.fraction);
  const speed = [rate.avg, rate.left].filter(Boolean).join(' · ');
  const resolving = p.total === 0;
  const short = isTerminal(job.status) && job.status !== 'completed';
  const counts = {};
  for (const it of items) if (OUTCOME_WORDS[it.status]) counts[it.status] = (counts[it.status] || 0) + 1;
  const extra = Object.keys(OUTCOME_WORDS)
    .filter((k) => k !== 'completed' && counts[k])
    .map((k) => `${counts[k]} ${OUTCOME_WORDS[k]}`);
  const text = resolving
    ? 'Resolving tracks'
    : short
      ? [`${p.onDisk} of ${p.total} tracks on disk`, ...extra].join(', ')
      : [`${p.lit} of ${p.total} tracks finished`, ...extra, `${p.percent}% overall`].join(', ');
  return html`<div class="abar-wrap">
    <div
      class=${resolving && !isTerminal(job.status) ? 'abar is-indeterminate' : 'abar'}
      role="progressbar"
      aria-label="Album progress"
      aria-valuemin="0"
      aria-valuemax="100"
      aria-valuenow=${resolving || short ? undefined : p.percent}
      aria-valuetext=${text}
      data-ticks=${p.ticks ? '' : undefined}
      style=${{ '--n': String(p.ticks || 1) }}
    >
      <span class="abar-live" style=${{ '--w': String(p.live) }}></span>
      <span class="abar-done">
        ${p.runs.map((r, i) => html`<span key=${i} class="abar-run" data-state=${r.state} data-role=${r.role} style=${{ '--w': String(r.share) }}></span>`)}
      </span>
    </div>
    <div class="abar-caption num" aria-hidden="true">
      <span class="abar-count">${resolving ? 'Resolving' : `${short ? p.onDisk : p.lit} / ${p.total}`}${speed ? html`<span class="abar-rate" title=${speed}>${speed}</span>` : null}</span>
      <span class="abar-pct">${resolving || short || p.total === 1 ? '' : `${p.percent}%`}</span>
    </div>
  </div>`;
}

export function TrackList({ items, albumArtist, keepStatus }) {
  const mode = albumCodec(items);
  return html`<ol class="tracks">
    ${items.map((it) => {
      const v = itemStatusView(it);
      const t = trackView(it, albumArtist, mode, keepStatus);
      const note = it.status === 'failed' && it.error ? '' : trackNote(it, v.label);
      const pct = it.file_size > 0 ? '' : stagePercent(it);
      return html`<li key=${it.id} class="track" data-row tabindex="-1" data-status=${it.status} data-detail=${t.status ? '' : undefined}>
        <span class="track-index num">${it.index}</span>
        <span class="track-main">
          <span class="track-title" dir="auto" title=${it.title}>${it.title || html`<span class="muted">Untitled</span>`}</span>
          ${t.artist ? html`<span class="track-artist" dir="auto">${t.artist}</span>` : null}
        </span>
        <span class="track-codec">${t.codec ? html`<${Chip} title=${codecMeaning(it) || undefined}>${t.codec}</${Chip}>` : null}</span>
        <span class="track-len num">${trackLength(it.duration_ms)}</span>
        <span class="track-size num">${it.file_size > 0 ? bytes(it.file_size) : pct ? html`<span class="track-pct">${pct}</span>` : ''}</span>
        ${t.status
          ? html`<span class="track-stages"><${StageTrack} item=${it} /></span>
              <span class="track-status">
                <${StatusText} view=${v} />
                ${v.retry ? html`<span class="badge" data-role="warning"><${Icon} name="rotate" size="sm" /><span class="badge-label">${v.retry}</span></span>` : null}
                ${note ? html`<span class="track-note" title=${note}>${note}</span>` : null}
              </span>`
          : null}
        ${it.status === 'failed' && it.error ? html`<p class="track-error">${it.error}</p>` : null}
      </li>`;
    })}
  </ol>`;
}
