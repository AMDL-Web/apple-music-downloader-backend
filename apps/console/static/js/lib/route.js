// Pure hash routing and filter mapping. Routes are #/ (also #/overview),
// #/overview/:id, #/queue, #/queue/:id, #/logs, #/new, #/new/:id and
// #/settings; filters live in the hash query so a filtered view is a
// shareable link.

export const PAGES = ['overview', 'queue', 'logs', 'new', 'settings'];

const PAGE_PATHS = { overview: '', queue: 'queue', logs: 'logs', new: 'new', settings: 'settings' };
// #/overview is an alias of #/: a natural guess at the address. buildHash
// writes #/, or #/overview/:id with a job. #/showcase is the address of a
// page the Overview replaced; its links, drawer included, land there.
const PATH_PAGES = { ...Object.fromEntries(Object.entries(PAGE_PATHS).map(([k, v]) => [v, k])), overview: 'overview', showcase: 'overview' };
// Addresses of pages merged into another: the route reports `replace`, and
// the router rewrites the address in place, so Back does not return to it.
const PAGE_ALIASES = { system: 'settings' };

/** Pages whose route may carry a job id that opens the job drawer. */
const DRAWER_PAGES = new Set(['overview', 'queue', 'new']);

/** decodeURIComponent, or null for a malformed escape such as "%E0%A4%A". */
function decodeSegment(s) {
  try {
    return decodeURIComponent(s);
  } catch {
    return null;
  }
}

/**
 * Parses location.hash into {page, jobId, params}, plus `replace: true` for
 * an alias address. Unknown paths, and a job id with a malformed percent
 * escape, give page "notfound".
 */
export function parseHash(hash) {
  let h = typeof hash === 'string' ? hash : '';
  if (h.startsWith('#')) h = h.slice(1);
  const qi = h.indexOf('?');
  const path = qi >= 0 ? h.slice(0, qi) : h;
  const query = qi >= 0 ? h.slice(qi + 1) : '';
  const params = {};
  for (const [k, v] of new URLSearchParams(query)) params[k] = v;
  const parts = path.split('/').filter(Boolean);
  if (parts.length === 1 && PAGE_ALIASES[parts[0]]) return { page: PAGE_ALIASES[parts[0]], jobId: null, params: {}, replace: true };
  const page = PATH_PAGES[parts[0] || ''];
  if (!page) return { page: 'notfound', jobId: null, params };
  if (parts.length === 1 || parts.length === 0) return { page, jobId: null, params };
  if (parts.length === 2 && DRAWER_PAGES.has(page)) {
    const jobId = decodeSegment(parts[1]);
    if (jobId !== null) return { page, jobId, params };
  }
  return { page: 'notfound', jobId: null, params };
}

/** Builds a hash from a page, optional job id and params (empty values omitted). */
// The Queue's last tab, type and words, kept for the life of the browser
// tab, so a plain way back to it (the sidebar, the tab bar, `g q`, the
// palette) returns to them; a link that names its own filters still wins.
let queueKept = {};

/** Keeps the Queue's filters; `tab` is '' when the link named none. */
export function rememberQueue({ tab = '', type = '', q = '' } = {}) {
  queueKept = { tab, type, q };
}

/** The plain way to the Queue: its last filters. */
export function queueHash() {
  return buildHash('queue', queueKept);
}

export function buildHash(page, params = {}, jobId = null) {
  const withJob = Boolean(jobId) && DRAWER_PAGES.has(page);
  // The Overview's path is empty; with a job it needs its name back.
  let path = `#/${withJob && page === 'overview' ? 'overview' : (PAGE_PATHS[page] ?? '')}`;
  if (withJob) path += `/${encodeURIComponent(jobId)}`;
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '' || v === false) continue;
    qs.set(k, String(v));
  }
  const q = qs.toString();
  return q ? `${path}?${q}` : path;
}

/**
 * The console's position in the tab's history: every entry it shows is
 * stamped history.state = {amdlIdx: n}, counting up from the first.
 * Returns n, or null for an entry without a stamp (one a link or a
 * location.hash assignment just pushed).
 */
export function historyIndex(state) {
  return state && Number.isInteger(state.amdlIdx) ? state.amdlIdx : null;
}

/**
 * Undoing a hash change the leave guard refused, without adding an entry:
 * {undo, redo}, history.go() deltas. A pushed entry (no stamp) is undone
 * by going back one and redone by pushing the hash again (redo null); a
 * Back or Forward to the entry stamped `state` is undone by going the
 * other way and redone by going there again. `shownIdx` is the stamp of
 * the entry shown before the change. {undo: 0} means it cannot be undone
 * through the history.
 */
export function refusedMove(shownIdx, state) {
  const idx = historyIndex(state);
  if (idx === null) return { undo: -1, redo: null };
  return { undo: shownIdx - idx, redo: idx - shownIdx };
}

/** The Queue's tabs in the order drawn; the first, All, is the default. */
export const QUEUE_TABS = ['all', 'active', 'completed', 'failed', 'cancelled'];

const TAB_STATUSES = { active: ['running', 'queued'], completed: ['completed'], failed: ['failed'], cancelled: ['cancelled'], all: [] };

/** Normalizes queue route params: tab, type and q. */
export function queueFilters(params = {}) {
  const tab = QUEUE_TABS.includes(params.tab) ? params.tab : QUEUE_TABS[0];
  const type = ['album', 'playlist', 'song', 'artist', 'station'].includes(params.type) ? params.type : '';
  const q = typeof params.q === 'string' ? params.q : '';
  return { tab, type, q };
}

/** The list filter (for store.jobMatches) of a queue view. */
export function queueMatchFilter(filters) {
  return { statuses: TAB_STATUSES[filters.tab] || [], types: filters.type ? [filters.type] : [], q: filters.q };
}

/**
 * GET /api/v1/downloads query for a queue view. Active jobs read oldest
 * first, which approximates execution order; finished ones by when they
 * finished, newest first.
 */
export function queueQuery(filters, offset = 0, limit = 50) {
  const query = { limit, offset };
  const statuses = TAB_STATUSES[filters.tab] || [];
  if (statuses.length) query.status = statuses.join(',');
  if (filters.type) query.type = filters.type;
  if (filters.q) query.q = filters.q;
  if (filters.tab === 'active') {
    query.sort = 'created_at';
    query.order = 'asc';
  } else if (filters.tab !== 'all') {
    query.sort = 'updated_at';
    query.order = 'desc';
  }
  return query;
}

/** Whether new rows go to the end of a queue view (oldest-first order). */
export function queueAppends(filters) {
  return filters.tab === 'active';
}

/**
 * The links #/new?url= carries: one or more, separated by whitespace or
 * newlines (URL-encoded in the hash). Not validated here; lib/links.js
 * parses them.
 */
export function newDownloadUrls(params = {}) {
  return typeof params.url === 'string' ? params.url.split(/\s+/).filter(Boolean) : [];
}

/** The #/new hash pre-filled with these links, newline-separated; none gives #/new. */
export function newDownloadHash(urls = []) {
  return buildHash('new', { url: urls.join('\n') });
}

/** The #/new hash that searches the catalog for `term` (the text box takes it unless it holds links). */
export function newSearchHash(term = '') {
  return buildHash('new', { q: term });
}

/**
 * The section #/settings?section= names, or '' (the page's first section).
 * Section names are lowercase words joined by underscores; the settings page
 * owns the list (lib/settings-sections.js SETTINGS_SECTIONS), which the command
 * palette also searches.
 */
export function settingsSection(params = {}) {
  const s = params.section;
  return typeof s === 'string' && /^[a-z][a-z0-9_]*$/.test(s) ? s : '';
}

/**
 * The field #/settings?field= names (a config key such as download.max_attempts,
 * which the page focuses once its section is open), or ''.
 */
export function settingsField(params = {}) {
  const f = params.field;
  return typeof f === 'string' && /^[a-z][a-z0-9_.]*$/.test(f) ? f : '';
}

/** The #/settings hash for a section ('' for the page itself), optionally naming a field in it. */
export function settingsHash(section = '', field = '') {
  return buildHash('settings', { section, field });
}

export const LOG_LEVELS = ['debug', 'info', 'warn', 'error'];
// Every component tag the backend logs with; TestLogComponentsMatchBackend keeps
// this list equal to the tags in the Go code.
export const LOG_COMPONENTS = ['api', 'jobs', 'media', 'catalog', 'hooks', 'librarysync', 'usertoken', 'http', 'limits', 'main'];

/** Normalizes log route params. `level` is a comma list of exact levels. */
export function logFilters(params = {}) {
  const levels = (params.level || 'info,warn,error')
    .split(',')
    .map((l) => l.trim())
    .filter((l) => LOG_LEVELS.includes(l));
  return {
    levels: levels.length ? LOG_LEVELS.filter((l) => levels.includes(l)) : ['info', 'warn', 'error'],
    component: typeof params.component === 'string' ? params.component : '',
    job_id: typeof params.job_id === 'string' ? params.job_id : '',
    request_id: typeof params.request_id === 'string' ? params.request_id : '',
    q: typeof params.q === 'string' ? params.q : '',
  };
}

/** Route params for log filters (the inverse of logFilters). */
export function logParams(filters) {
  const level = filters.levels.join(',');
  return {
    level: level === 'info,warn,error' ? '' : level,
    component: filters.component,
    job_id: filters.job_id,
    request_id: filters.request_id,
    q: filters.q,
  };
}

/** GET /api/v1/logs and /logs/stream query for log filters. */
export function logQuery(filters, extra = {}) {
  const query = { level: filters.levels.join(',') };
  if (filters.component) query.component = filters.component;
  if (filters.job_id) query.job_id = filters.job_id;
  if (filters.request_id) query.request_id = filters.request_id;
  if (filters.q) query.q = filters.q;
  return { ...query, ...extra };
}

/** Encodes a query object as a URL query string (no leading "?"). */
export function encodeQuery(query = {}) {
  const qs = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === null || v === '') continue;
    qs.set(k, String(v));
  }
  return qs.toString();
}
