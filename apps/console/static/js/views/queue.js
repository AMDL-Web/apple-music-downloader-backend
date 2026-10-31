// Queue page: All / Active / Completed / Failed / Cancelled tabs (All first and the default), search and type filter, a dense
// job list kept live from the downloads feed, and a selection mode for
// Retry, Cancel and Delete on many jobs (views/queue-select.js).

import { html, useState, useEffect, useRef } from '../ui/preact.js';
import { listDownloads } from '../api.js';
import { jobTitle, isUnresolved, inputLabel, count } from '../lib/format.js';
import { activeFirst, isNotFound } from '../lib/jobview.js';
import { appendPage } from '../lib/store.js';
import { buildHash, queueFilters, queueQuery, queueMatchFilter, queueAppends, rememberQueue } from '../lib/route.js';
import { toggleId, setIds, rangeIds, selectionState } from '../lib/selection.js';
import { actionPlan, jobAllows } from '../lib/jobactions.js';
import { useApp, Icon, Segmented, ErrorNote, SkeletonJobRows, Button } from '../ui/components.js';
import { useDebounced, useWatchShown } from '../ui/hooks.js';
import { useBulkRun, SelectionBar, BulkConfirm } from './queue-select.js';
import { JobRow, applyFeedToList } from './queue-row.js';
import { useChipRow } from './queue-chips.js';
import { QueueEmpty } from './queue-empty.js';
import { cached, remember } from '../lib/cache.js';

export { JobRow, applyFeedToList };

const PAGE_SIZE = 50;

const TYPE_OPTIONS = [
  ['', 'All types'],
  ['album', 'Albums'],
  ['playlist', 'Playlists'],
  ['song', 'Songs'],
  ['artist', 'Artists'],
  ['station', 'Stations'],
];

const HOLD_MS = 400;

export function QueuePage({ route }) {
  const app = useApp();
  const filters = queueFilters(route.params);
  const [qInput, setQInput] = useState(filters.q);
  const q = useDebounced(qInput, 300);
  const listKey = `queue|${filters.tab}|${filters.type}|${filters.q}`;
  // A list seen before in this tab is drawn at once and read again behind it.
  const [state, setState] = useState(() => {
    const last = cached(listKey);
    return last ? { jobs: last.jobs, total: last.total, loading: true, error: null, more: false } : { jobs: [], total: 0, loading: true, error: null, more: false };
  });
  const key = `${listKey}|${app.epoch}`;
  const seq = useRef(0);
  const known = useRef(new Map());

  const setParams = (patch) => {
    app.navigate(buildHash('queue', { ...route.params, ...patch }, route.jobId), { replace: true });
  };

  const haveCounts = Boolean(app.counts);
  useEffect(() => {
    rememberQueue({ tab: route.params.tab || '', type: filters.type, q: filters.q });
  }, [route.params.tab, filters.type, filters.q]);
  const chooseTab = (tab) => setParams({ tab });
  // The tabs carry the counts: on a cold load they wait unseen for them (or
  // HOLD_MS) instead of widening when the numbers arrive.
  const [held, setHeld] = useState(!haveCounts);
  useEffect(() => {
    if (haveCounts) setHeld(false);
    if (!held || haveCounts) return undefined;
    const t = setTimeout(() => setHeld(false), HOLD_MS);
    return () => clearTimeout(t);
  }, [haveCounts]);

  // The search box and the route's q follow each other. A q this page wrote
  // itself (ownQ) comes back through the route and must not reset a box the
  // user has typed on; any other change of the route's q (Back, Forward, a
  // link) replaces the box, and drops a debounced value that is now stale.
  const ownQ = useRef(null);
  useEffect(() => {
    if (q !== filters.q) {
      ownQ.current = q;
      setParams({ q });
    }
  }, [q]);
  useEffect(() => {
    if (filters.q === ownQ.current) return;
    ownQ.current = null;
    if (filters.q !== qInput) setQInput(filters.q);
  }, [filters.q]);

  const load = (offset) => {
    const mine = ++seq.current;
    const last = offset === 0 ? cached(listKey) : null;
    if (last) setState((s) => ({ ...s, jobs: last.jobs, total: last.total, loading: true, error: null }));
    else if (offset === 0) setState((s) => ({ ...s, loading: true, error: null }));
    else setState((s) => ({ ...s, more: true }));
    listDownloads(queueQuery(filters, offset, PAGE_SIZE)).then(
      (body) => {
        if (mine !== seq.current) return;
        if (offset === 0) remember(listKey, { jobs: body.downloads, total: body.total });
        setState((s) => ({
          jobs: offset === 0 ? body.downloads : appendPage(s.jobs, body.downloads),
          total: body.total,
          loading: false,
          error: null,
          more: false,
        }));
      },
      (error) => {
        if (mine !== seq.current) return;
        setState((s) => ({ ...s, loading: false, more: false, error }));
      },
    );
  };

  useEffect(() => {
    load(0);
  }, [key]);

  useEffect(() => {
    const filter = queueMatchFilter(filters);
    const opts = { append: queueAppends(filters) };
    // The snapshot's total moves with the rows the feed adds and removes
    // (an estimate: a change to a job past the loaded pages is not seen).
    return app.subscribeFeed((batch) => {
      // Selected jobs the filters hide stay selected, so the registry of jobs
      // follows the whole feed, not only the rows of this list.
      for (const msg of batch) {
        if (msg.type === 'download_deleted') {
          known.current.delete(msg.job_id);
          setSelected((s) => toggleId(s, msg.job_id, false));
        } else if (msg.type === 'download_upserted' && msg.job) {
          const old = known.current.get(msg.job.id);
          if (!old || Date.parse(msg.job.updated_at) >= Date.parse(old.updated_at)) known.current.set(msg.job.id, msg.job);
        }
      }
      setState((s) => {
        const jobs = applyFeedToList(s.jobs, batch, filter, opts);
        return jobs === s.jobs ? s : { ...s, jobs, total: Math.max(jobs.length, s.total + jobs.length - s.jobs.length) };
      });
    });
  }, [key]);

  // Selection: a Set of ids. The jobs behind them come from `known`, the
  // latest version of every job seen, so a status change drops actions, a job
  // the feed deletes leaves the selection, and a search that hides a selected
  // job leaves it selected (the bar says how many are shown). Another tab
  // starts a new selection.
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState(() => new Set());
  const [confirm, setConfirm] = useState(null);
  const bulk = useBulkRun(app);
  for (const j of state.jobs) {
    const old = known.current.get(j.id);
    if (!old || Date.parse(j.updated_at) >= Date.parse(old.updated_at)) known.current.set(j.id, j);
  }
  // The rows as shown (running jobs first, then queued ones, on Active and
  // All), which is the order Shift-click ranges follow.
  const rows = filters.tab === 'active' || filters.tab === 'all' ? activeFirst(state.jobs) : state.jobs;
  // A job that finishes leaves Active at once; its toast (Open) says where it went.
  useWatchShown(app, filters.tab === 'active' ? state.jobs.map((j) => j.id) : []);
  const jobsRef = useRef(rows);
  jobsRef.current = rows;
  const anchorId = useRef(null);
  useEffect(() => {
    setSelected((s) => (s.size === 0 ? s : new Set()));
    anchorId.current = null;
  }, [filters.tab]);

  const toggle = (id, range) => {
    const from = anchorId.current;
    anchorId.current = id;
    setSelected((s) => {
      const on = !s.has(id);
      return range && from ? setIds(s, rangeIds(jobsRef.current, from, id), on) : toggleId(s, id, on);
    });
  };
  const leaveSelection = () => {
    setSelecting(false);
    setSelected(new Set());
    anchorId.current = null;
  };
  const chosen = [...selected].map((id) => known.current.get(id)).filter(Boolean);
  const shownSelected = rows.filter((j) => selected.has(j.id)).length;
  const plan = actionPlan(chosen);
  // A run disables or removes the control that had focus (the action
  // buttons, Retry all, the confirm dialog), and the feed then removes rows
  // and buttons; until a moment after it ends, focus that falls to the body
  // moves to the selection controls (the effect below).
  const rescueUntil = useRef(0);
  const runAction = async (action, targets) => {
    rescueUntil.current = Infinity;
    const reached = await bulk.start(action, targets);
    if (action === 'delete') setSelected((s) => setIds(s, reached, false));
    setConfirm(null);
    rescueUntil.current = Date.now() + 3000;
  };
  useEffect(() => {
    if (Date.now() > rescueUntil.current) return;
    const active = document.activeElement;
    if (active && active !== document.body) return;
    const target = RESCUE.map((q) => document.querySelector(q)).find(Boolean);
    if (target) target.focus({ preventScroll: true });
  });
  const onAction = (action) => {
    if (action === 'retry') runAction('retry', plan.retry);
    else setConfirm({ action, jobs: null });
  };
  // A link Apple has nothing at fails the same way again: Retry all leaves
  // it out, and its row offers no Retry (the drawer still does).
  const retryable = state.jobs.filter((j) => jobAllows(j, 'retry') && !isNotFound(j.error));

  // Escape leaves selection mode; x toggles the focused row (and enters the
  // mode). Both stand down while typing, under the drawer or any overlay.
  const selectingRef = useRef(selecting);
  selectingRef.current = selecting;
  useEffect(() => {
    const onKey = (e) => {
      if (e.metaKey || e.ctrlKey || e.altKey || route.jobId || typing(e.target)) return;
      if (document.querySelector('.overlay-host, .drawer-host')) return;
      if (e.key === 'Escape') {
        if (!selectingRef.current) return;
        e.preventDefault();
        const inList = document.activeElement && document.activeElement.closest('.bulk-bar, .job-check');
        leaveSelection();
        const btn = document.querySelector('.queue-select');
        if (inList && btn) btn.focus();
        return;
      }
      if (e.key !== 'x') return;
      const li = document.activeElement && document.activeElement.closest('.page-queue [data-job-id]');
      if (!li) return;
      e.preventDefault();
      setSelecting(true);
      toggle(li.getAttribute('data-job-id'), false);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [route.jobId]);

  const c = app.counts;
  // The failed count is the one number that asks for attention: danger text.
  const failedCount = c && c.failed > 0 ? html`<span class="seg-count-danger">${c.failed}</span>` : c ? c.failed : undefined;
  const tabs = [
    { value: 'all', label: 'All', count: c ? c.total : undefined },
    { value: 'active', label: 'Active', count: c ? c.queued + c.running : undefined },
    { value: 'completed', label: 'Completed', count: c ? c.completed : undefined },
    { value: 'failed', label: 'Failed', count: failedCount },
    { value: 'cancelled', label: 'Cancelled', count: c ? c.cancelled : undefined },
  ];
  const rowHref = (job) => buildHash('queue', route.params, job.id);
  // On a narrow phone the status tabs scroll sideways; the open one stays in view.
  useChipRow(`${filters.tab}|${haveCounts}`);

  // The search's result count and Retry all head the list inside its card:
  // in the toolbar they would change its width or its rows with every tab.
  const showCount = filters.q && (!state.loading || state.total > 0);
  const showRetryAll = (filters.tab === 'failed' || filters.tab === 'cancelled') && retryable.length > 0 && !selecting;
  const listBar =
    state.jobs.length > 0 && (showCount || showRetryAll)
      ? html`<div class="queue-list-bar">
          ${showCount ? html`<span class="queue-count" role="status">${count(state.total)} ${state.total === 1 ? 'result' : 'results'}</span>` : null}
          ${showRetryAll
            ? html`<${Button}
                icon="rotate"
                size="sm"
                className="queue-retry-all"
                aria-label=${`Retry all ${retryable.length} loaded ${filters.tab} jobs`}
                disabled=${Boolean(bulk.run)}
                onClick=${() => runAction('retry', retryable)}
              >Retry all (${retryable.length})</${Button}>`
            : null}
        </div>`
      : null;

  let body;
  if (state.loading && state.jobs.length === 0) body = html`<${SkeletonJobRows} n=${6} />`;
  else if (state.error && state.jobs.length === 0)
    body = html`<${ErrorNote} title="Couldn't load downloads" error=${state.error} onRetry=${() => load(0)} />`;
  else if (state.jobs.length === 0) body = html`<${QueueEmpty} filters=${filters} tabs=${tabs} onClear=${() => { setQInput(''); setParams({ q: '', type: '' }); }} onShowAll=${() => chooseTab('all')} />`;
  else
    body = html`<ul class=${selecting ? 'job-list is-selecting' : 'job-list'} aria-label="Jobs">
        ${rows.map((job) => {
          const on = selecting && selected.has(job.id);
          const retry = !selecting && job.status === 'failed' && !isNotFound(job.error);
          return html`<li key=${job.id} class="job-item" data-job-id=${job.id} data-selected=${on ? '' : undefined} data-action=${retry ? 'retry' : undefined}>
            ${selecting
              ? html`<label class="job-check">
                  <input
                    type="checkbox"
                    class="job-check-input"
                    checked=${on}
                    aria-label=${`Select ${isUnresolved(job) ? inputLabel(job.input) : jobTitle(job)}`}
                    onClick=${(e) => toggle(job.id, e.shiftKey)}
                  />
                </label>`
              : null}
            <${JobRow}
              job=${job}
              href=${rowHref(job)}
              selected=${selecting ? on : undefined}
              onSelect=${selecting ? (e) => toggle(job.id, e.shiftKey) : undefined}
            />
            ${retry
              ? html`<${Button}
                  size="sm"
                  icon="rotate"
                  className="job-retry"
                  aria-label=${`Retry ${isUnresolved(job) ? inputLabel(job.input) : jobTitle(job)}`}
                  disabled=${Boolean(bulk.run)}
                  onClick=${() => runAction('retry', [job])}
                >Retry</${Button}>`
              : null}
          </li>`;
        })}
      </ul>
      ${state.jobs.length < state.total
        ? html`<div class="load-more">
            <${Button} onClick=${() => load(state.jobs.length)} disabled=${state.more}>${state.more ? 'Loading…' : `Load more (${state.total - state.jobs.length} left)`}</${Button}>
          </div>`
        : null}`;

  return html`<div class="page page-queue" data-selecting=${selecting || bulk.run ? '' : undefined} data-held=${held ? '' : undefined}>
    <div class="toolbar">
      <${Segmented} label="Job status" options=${tabs} value=${filters.tab} onChange=${chooseTab} />
      <div class="toolbar-filters">
        <label class="search">
          <${Icon} name="search" />
          <input
            type="search"
            data-filter
            placeholder="Filter this list"
            aria-label="Filter jobs by title, artist or link"
            value=${qInput}
            onInput=${(e) => setQInput(e.currentTarget.value)}
          />
        </label>
        <select class="select" aria-label="Job type" value=${filters.type} onChange=${(e) => setParams({ type: e.currentTarget.value })}>
          ${TYPE_OPTIONS.map(([v, l]) => html`<option key=${v} value=${v}>${l}</option>`)}
        </select>
      </div>
      ${html`<div class="queue-tools">
            <${Button}
              icon="square-check"
              className="queue-select"
              aria-pressed=${String(selecting)}
              aria-keyshortcuts="x"
              title="Select jobs (x selects the focused one, Esc leaves)"
              disabled=${!selecting && state.jobs.length === 0}
              onClick=${() => (selecting ? leaveSelection() : setSelecting(true))}
            >${selecting ? 'Done' : 'Select'}</${Button}>
          </div>`}
    </div>
    ${state.error && state.jobs.length > 0 ? html`<${ErrorNote} title="Couldn't refresh downloads" error=${state.error} onRetry=${() => load(0)} />` : null}
    ${selecting || bulk.run
      ? html`<${SelectionBar}
          jobs=${rows}
          selected=${selected}
          count=${chosen.length}
          shown=${shownSelected}
          plan=${plan}
          selecting=${selecting}
          run=${bulk.run}
          onSelectAll=${() => setSelected((s) => setIds(s, rows.map((j) => j.id), selectionState(s, rows) !== 'all'))}
          onAction=${onAction}
        />`
      : null}
    <div class="card card-flush" data-section="jobs"><div class="card-body">${listBar}${body}</div></div>
    ${confirm
      ? html`<${BulkConfirm}
          action=${confirm.action}
          jobs=${confirm.jobs || plan[confirm.action]}
          skipped=${confirm.jobs ? confirm.skipped : chosen.length - plan[confirm.action].length}
          run=${bulk.run}
          onConfirm=${() => {
            // The targets freeze here: rows the run deletes leave the live plan.
            const targets = plan[confirm.action];
            setConfirm({ action: confirm.action, jobs: targets, skipped: chosen.length - targets.length });
            runAction(confirm.action, targets);
          }}
          onCancel=${() => setConfirm(null)}
        />`
      : null}
  </div>`;
}

const RESCUE = ['.bulk-bar .bulk-action:not(:disabled)', '.bulk-check:not(:disabled)', '.queue-retry-all:not(:disabled)', '.queue-select', '.page-title'];

const typing = (el) =>
  Boolean(el) &&
  ((el.tagName === 'INPUT' && !['checkbox', 'radio', 'button', 'submit'].includes(el.type)) ||
    el.tagName === 'TEXTAREA' ||
    el.tagName === 'SELECT' ||
    el.isContentEditable);
