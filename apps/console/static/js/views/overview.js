// Overview: the server status line, recent failures, the live queue, recently
// completed artwork, the job counts and throughput from /stats.

import { html, useState, useEffect, useRef } from '../ui/preact.js';
import { listDownloads, retryDownload } from '../api.js';
import { count, jobTitle, isUnresolved, linkLabel, errorSummary } from '../lib/format.js';
import { attentionRows, attentionGroups, domainView } from '../lib/domains.js';
import { isNotFound } from '../lib/jobview.js';
import { SETTINGS_SECTIONS } from '../lib/settings-sections.js';
import { buildHash, settingsHash } from '../lib/route.js';
import { useApp, Card, Icon, Badge, ErrorNote, SkeletonJobRows, Skeleton, LinkButton, Button, RelTime, StatusText } from '../ui/components.js';
import { useNow } from '../ui/hooks.js';
import { RatesContext, useLiveRates } from '../ui/job-rate.js';
import { useStatus } from '../ui/system-status.js';
import { cached, remember } from '../lib/cache.js';
import { JobRow, applyFeedToList } from './queue.js';
import { RecentSection } from './overview-recent.js';
import { ThroughputCard } from './throughput.js';

// Sections follow the reading order of the skin, so Tab order equals the
// visual order: the server status line, Needs attention (only with recent
// failures), Live queue, Recently completed, Jobs, Throughput. The Log tail
// is the Logs page; server health is the status line and the Settings board.
export function OverviewPage() {
  const app = useApp();
  const status = useStatus(STRIP_KEYS);
  // The status line and Needs attention sit above everything and are known
  // only after their reads: what they would push down waits unseen for both
  // answers (or HOLD_MS), so neither grows into a drawn page. While the line
  // is unknown, Needs attention waits too ("strip").
  const stripKnown = STRIP_KEYS.every((k) => status[k]);
  const [attentionKnown, setAttentionKnown] = useState(() => cached(ATTENTION_KEY) !== undefined);
  const [late, setLate] = useState(false);
  useEffect(() => {
    if (attentionKnown && stripKnown) return undefined;
    const t = setTimeout(() => setLate(true), HOLD_MS);
    return () => clearTimeout(t);
  }, []);
  const held = late ? undefined : !stripKnown ? 'strip' : attentionKnown ? undefined : '';
  // With no account logged in, the empty live queue leads to logging in.
  const account = stripKnown ? domainView('account', status, { simulate: app.simulate, conn: app.conn }) : null;
  const loggedOut = Boolean(account && account.tag && account.tag.label === 'Not logged in');
  return html`<div class="page page-overview" data-held=${held}>
    <${HealthStrip} status=${status} />
    <${Attention} onKnown=${() => setAttentionKnown(true)} />
    <${LiveQueue} loggedOut=${loggedOut} />
    <${RecentSection} />
    <${KpiRow} />
    <${ThroughputCard} />
  </div>`;
}

const TILES = [
  { key: 'queued', label: 'Queued', icon: 'circle-dashed', role: 'neutral', href: buildHash('queue', { tab: 'active' }) },
  { key: 'running', label: 'Running', icon: 'dot', role: 'info', href: buildHash('queue', { tab: 'active' }) },
  { key: 'completed', label: 'Completed', icon: 'circle-check', role: 'success', href: buildHash('queue', { tab: 'all' }) },
  { key: 'failed', label: 'Failed', icon: 'circle-x', role: 'danger', href: buildHash('queue', { tab: 'failed' }) },
];

function KpiRow() {
  const { counts, conn } = useApp();
  const stale = conn.state === 'offline';
  // A feed that has not answered yet is loading; one that failed before its
  // first snapshot has nothing to show, and a placeholder would pulse forever.
  const down = stale || conn.state === 'reconnecting';
  const tile = (t, value) => html`<a key=${t.key} class="kpi" data-key=${t.key} data-role=${t.role} data-zero=${value === 0 ? '' : undefined} href=${t.href}>
    <span class="kpi-label">${t.label}</span>
    <span class="kpi-icon"><${Icon} name=${t.icon} /></span>
    <span class="kpi-value">${counts && !stale ? count(value) : counts || down ? '—' : html`<${Skeleton} kind="kpi" />`}</span>
  </a>`;
  return html`<${Card} section="kpis" title="Jobs" labelId="kpis-title" note="All time">
    <div class="kpis">
      ${TILES.map((t) => tile(t, counts && counts[t.key]))}
    </div>
  </${Card}>`;
}

const LIVE_LIMIT = 8;
const LIVE_FILTER = { statuses: ['running', 'queued'] };

function LiveQueue({ loggedOut }) {
  const app = useApp();
  const [state, setState] = useState({ jobs: [], loading: true, error: null });
  const [stamp, setStamp] = useState(0);
  // A read that is not the latest one is dropped, so a slow older answer
  // cannot overwrite a newer list.
  const seq = useRef(0);
  const load = () => {
    const mine = ++seq.current;
    return listDownloads({ status: 'running,queued', sort: 'created_at', order: 'asc', limit: LIVE_LIMIT }).then(
      (body) => {
        if (mine === seq.current) setState({ jobs: body.downloads, loading: false, error: null });
      },
      (error) => {
        if (mine === seq.current) setState((s) => ({ ...s, loading: false, error }));
      },
    );
  };
  useEffect(() => {
    load();
  }, [app.epoch, stamp]);
  const rates = useLiveRates(state.jobs);
  const jobsRef = useRef(state.jobs);
  jobsRef.current = state.jobs;
  useEffect(
    () =>
      app.subscribeFeed((batch) => {
        const held = new Set(jobsRef.current.map((j) => j.id));
        const leaving = batch.some(
          (m) =>
            (m.type === 'download_deleted' && held.has(m.job_id)) ||
            (m.job && held.has(m.job.id) && !LIVE_FILTER.statuses.includes(m.job.status)),
        );
        setState((s) => ({ ...s, jobs: applyFeedToList(s.jobs, batch, LIVE_FILTER, { append: true, limit: LIVE_LIMIT }) }));
        // A job left the top 8; the next one in line is only in a fresh read.
        if (leaving) setStamp((n) => n + 1);
      }),
    [],
  );
  const c = app.counts;
  let body;
  // The seeded counts already say how many jobs are active: the placeholder
  // has their rows, or is the idle line itself, so the card keeps its height.
  // Before the counts, it is the idle card with its words held back (the
  // common case).
  const active = c ? Math.min(LIVE_LIMIT, c.running + c.queued) : 0;
  if (state.loading && !c) body = html`<${LiveIdle} pending />`;
  else if (state.loading && active > 0) body = html`<${SkeletonJobRows} n=${active} compact />`;
  else if (state.error) body = html`<${ErrorNote} title="Couldn't load the queue" error=${state.error} onRetry=${load} />`;
  else if (state.loading || state.jobs.length === 0) body = html`<${LiveIdle} loggedOut=${loggedOut} />`;
  else
    body = html`<${RatesContext.Provider} value=${rates}>
      <ul class="job-list job-list-compact" aria-label="Active jobs">
        ${state.jobs.map((job) => html`<li key=${job.id}><${JobRow} job=${job} href=${buildHash('overview', {}, job.id)} /></li>`)}
      </ul>
    </${RatesContext.Provider}>`;
  return html`<${Card}
    className="card-flush"
    section="live"
    title="Live queue"
    labelId="live-queue-title"
    actions=${html`<${LinkButton} variant="ghost" size="sm" href=${buildHash('queue', { tab: 'active' })}>Open queue</${LinkButton}>`}
  >${body}</${Card}>`;
}

const IDLE_VIEW = { icon: 'circle-dashed', role: 'neutral', label: 'Idle' };

/**
 * The live queue with nothing in it: a card shaped like a job's (the same
 * cover square, which sets its height), as tall as one with its progress
 * row's room split above and below (the skin's padding), so the first job
 * that starts takes its place without moving the page below.
 */
function LiveIdle({ pending, loggedOut }) {
  return html`<div class="job-list job-list-compact" aria-hidden=${pending ? 'true' : undefined}>
    <div class="job-row live-idle-card" data-progress="on" data-pending=${pending ? '' : undefined}>
      <span class="art live-idle-art" aria-hidden="true"><${Icon} name="music" /></span>
      <span class="job-main">
        <span class="job-title">Nothing downloading</span>
        <span class="job-sub">
          ${loggedOut ? html`<a class="link" href=${settingsHash('account')}>Log in</a>` : html`<a class="link" href=${buildHash('new')}>Start a download</a>`}
        </span>
      </span>
      <span class="job-status"><${Badge} view=${IDLE_VIEW} /></span>
    </div>
  </div>`;
}

const ATTENTION_LIMIT = 2;
const ATTENTION_WINDOW_MS = 24 * 60 * 60 * 1000;
const FAILED_FILTER = { statuses: ['failed'] };

/**
 * The "Needs attention" card above the live queue, only while there are
 * failures: the latest failed jobs of the last 24 hours, each with Retry
 * unless Apple has nothing at its link (a retry fails the same way). A
 * retried or deleted job leaves through the feed, and the next one in line
 * comes from a fresh read.
 */
const ATTENTION_KEY = 'overview-attention';
const HOLD_MS = 400;

function Attention({ onKnown }) {
  const app = useApp();
  const now = useNow();
  const [jobs, setJobs] = useState(() => cached(ATTENTION_KEY) || []);
  const [stamp, setStamp] = useState(0);
  const [busy, setBusy] = useState(null);
  const seq = useRef(0);
  useEffect(() => {
    const mine = ++seq.current;
    listDownloads({ status: 'failed', sort: 'updated_at', order: 'desc', limit: ATTENTION_LIMIT }).then(
      (body) => {
        if (mine !== seq.current) return;
        remember(ATTENTION_KEY, body.downloads);
        setJobs(body.downloads);
        onKnown();
      },
      () => {
        if (mine !== seq.current) return;
        setJobs([]);
        onKnown();
      },
    );
    return () => {
      seq.current++;
    };
  }, [app.epoch, stamp]);
  const jobsRef = useRef(jobs);
  jobsRef.current = jobs;
  useEffect(
    () =>
      app.subscribeFeed((batch) => {
        const held = new Set(jobsRef.current.map((j) => j.id));
        const leaving = batch.some(
          (m) => (m.type === 'download_deleted' && held.has(m.job_id)) || (m.job && held.has(m.job.id) && m.job.status !== 'failed'),
        );
        setJobs((list) => applyFeedToList(list, batch, FAILED_FILTER, { limit: ATTENTION_LIMIT }));
        if (leaving) setStamp((n) => n + 1);
      }),
    [],
  );
  // Retrying disables the button that had focus and the feed then removes
  // its row; until a moment after, focus that falls to the body moves to the
  // next Retry, or to the card's "Open queue" link.
  const rescueUntil = useRef(0);
  const retry = async (job) => {
    rescueUntil.current = Infinity;
    setBusy(job.id);
    try {
      await retryDownload(job.id);
      app.toast({ message: 'Job queued again', role: 'success' });
    } catch (e) {
      app.toast({ message: e.message || 'The request failed', role: 'danger', offline: e.status === 0 });
    } finally {
      setBusy(null);
      rescueUntil.current = Date.now() + 3000;
    }
  };
  useEffect(() => {
    if (Date.now() > rescueUntil.current) return;
    const active = document.activeElement;
    if (active && active !== document.body && !active.disabled) return;
    const target = document.querySelector('.attention .btn:not(:disabled)') || document.querySelector('[data-section="live"] .card-actions a');
    if (target) target.focus({ preventScroll: true });
  });
  const recent = jobs.filter((j) => now - Date.parse(j.finished_at || j.updated_at) < ATTENTION_WINDOW_MS);
  if (recent.length === 0) return null;
  return html`<${Card}
    section="attention"
    title="Needs attention"
    labelId="attention-title"
    note="Last 24 hours"
    actions=${html`<${LinkButton} variant="ghost" size="sm" href=${buildHash('queue', { tab: 'failed' })}>All failed</${LinkButton}>`}
  ><ul class="attention" aria-label="Recent failures">
    ${recent.map(
      (job) => html`<li key=${job.id} class="attention-row" data-role="danger">
        <span class="attention-title">
          <span class="attention-label"><${Icon} name="circle-x" />Failed</span>
          ${isUnresolved(job)
            ? null
            : html`<a class="attention-link" href=${buildHash('overview', {}, job.id)} dir="auto" title=${jobTitle(job)}>${jobTitle(job)}</a>`}
          ${isUnresolved(job)
            ? html`<a class="attention-link attention-input" href=${buildHash('queue', {}, job.id)} title=${job.input}>${linkLabel(job.input)}</a>`
            : html`<span class="attention-input" title=${job.input}>${linkLabel(job.input)}</span>`}
          <span class="attention-time"><${RelTime} value=${job.finished_at || job.updated_at} /></span>
        </span>
        ${job.error
          ? html`<span class="attention-detail"><span class="attention-error" title=${job.error}>${errorSummary(job.error)}</span></span>`
          : null}
        ${isNotFound(job.error) ? null : html`<${Button} size="sm" icon="rotate" onClick=${() => retry(job)} disabled=${busy === job.id}>Retry</${Button}>`}
      </li>`,
    )}
  </ul></${Card}>`;
}

const STRIP_KEYS = ['health', 'wrapper', 'librarySync', 'system', 'config'];

/**
 * One line under the page head that answers "is the server healthy?": a quiet
 * status when everything is fine, else a notice per severity naming each
 * Settings domain that needs action (lib/domains.js attentionRows), each
 * linking to its section, and Status to the Settings board.
 * Nothing renders until every read has answered, so the line changes once.
 */
function HealthStrip({ status }) {
  const app = useApp();
  const now = useNow(15000);
  // Until then, an empty line holds the strip's place.
  if (STRIP_KEYS.some((k) => !status[k])) return html`<p class="health-strip" data-state="loading" aria-hidden="true">\u00a0</p>`;
  const bad = attentionRows(status, { now, counts: app.counts, conn: app.conn, simulate: app.simulate }, SETTINGS_SECTIONS);
  const details = html`<a class="health-strip-link" href=${settingsHash()}>Status</a>`;
  if (bad.length === 0) {
    return html`<p class="health-strip" data-state="ok" data-role="success">
      <${StatusText} view=${{ icon: 'circle-check', role: 'success', label: 'All systems healthy' }} />${details}
    </p>`;
  }
  const groups = attentionGroups(bad);
  return html`<div class="health-strips" role="status">
    ${groups.map(
      ({ role, rows }, i) => html`<div key=${role} class="health-strip notice" data-state="problem" data-role=${role}>
        <${Icon} name=${role === 'danger' ? 'circle-x' : 'triangle-alert'} />
        <div class="notice-body">
          ${rows.map((r) => html`<a key=${r.id} class="health-strip-item" href=${r.href}><strong>${r.label}</strong> ${r.text}</a>`)}
          ${i === groups.length - 1 ? details : null}
        </div>
      </div>`,
    )}
  </div>`;
}
