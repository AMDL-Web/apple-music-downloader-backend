import test from 'node:test';
import assert from 'node:assert/strict';
import {
  historyIndex,
  refusedMove,
  parseHash,
  buildHash,
  QUEUE_TABS,
  queueFilters,
  rememberQueue,
  queueHash,
  queueQuery,
  queueMatchFilter,
  logFilters,
  logParams,
  logQuery,
  encodeQuery,
  newDownloadUrls,
  newDownloadHash,
  settingsSection,
  settingsHash,
} from '../static/js/lib/route.js';
import { resolveUrl, toApiError } from '../static/js/api.js';

test('parseHash', () => {
  assert.deepEqual(parseHash(''), { page: 'overview', jobId: null, params: {} });
  assert.deepEqual(parseHash('#/'), { page: 'overview', jobId: null, params: {} });
  assert.deepEqual(parseHash('#/queue?tab=failed&q=a%20b'), { page: 'queue', jobId: null, params: { tab: 'failed', q: 'a b' } });
  assert.deepEqual(parseHash('#/queue/job_abc?tab=all'), { page: 'queue', jobId: 'job_abc', params: { tab: 'all' } });
  assert.deepEqual(parseHash('#/overview/job_x'), { page: 'overview', jobId: 'job_x', params: {} });
  assert.equal(parseHash('#/logs/job_x').page, 'notfound', 'logs has no drawer route');
  assert.equal(parseHash('#/nope').page, 'notfound');
});

test('#/overview is an alias of #/, and buildHash keeps #/ until a job opens', () => {
  assert.deepEqual(parseHash('#/overview'), { page: 'overview', jobId: null, params: {} });
  assert.deepEqual(parseHash('#/overview?x=1'), { page: 'overview', jobId: null, params: { x: '1' } });
  assert.equal(buildHash('overview'), '#/');
  assert.equal(buildHash('overview', {}, 'job_1'), '#/overview/job_1');
  assert.deepEqual(parseHash(buildHash('overview', {}, 'job 1')), { page: 'overview', jobId: 'job 1', params: {} });
});

test('#/showcase, a page the Overview replaced, lands on the Overview, drawer included', () => {
  assert.deepEqual(parseHash('#/showcase'), { page: 'overview', jobId: null, params: {} });
  assert.deepEqual(parseHash('#/showcase/job_x?type=song'), { page: 'overview', jobId: 'job_x', params: { type: 'song' } });
});

test('a job id with a malformed percent escape is Not found, not a thrown URIError', () => {
  for (const h of ['#/queue/foo%80', '#/showcase/%E0%A4%A', '#/new/%', '#/queue/%E0%A4%A?tab=all']) {
    assert.equal(parseHash(h).page, 'notfound', h);
    assert.equal(parseHash(h).jobId, null, h);
  }
  assert.equal(parseHash('#/queue/a%20b').jobId, 'a b', 'a valid escape still decodes');
});

test('buildHash round-trips and drops empty params', () => {
  assert.equal(buildHash('overview'), '#/');
  assert.equal(buildHash('queue', { tab: '', q: 'a b' }, 'job_1'), '#/queue/job_1?q=a+b');
  assert.equal(buildHash('logs', { job_id: 'job_1', level: '' }), '#/logs?job_id=job_1');
  assert.equal(buildHash('logs', {}, 'job_1'), '#/logs', 'pages without a drawer drop the id');
  // System is part of Settings: its address opens the board and is replaced in place.
  assert.deepEqual(parseHash('#/system'), { page: 'settings', jobId: null, params: {}, replace: true });
  assert.equal(parseHash('#/settings').replace, undefined);
  assert.equal(settingsHash(), '#/settings');
  const h = buildHash('queue', { tab: 'failed', type: 'album' }, 'job_9');
  const back = parseHash(h);
  assert.equal(back.jobId, 'job_9');
  assert.deepEqual(back.params, { tab: 'failed', type: 'album' });
});

test('#/new carries one or more links in ?url=', () => {
  assert.deepEqual(parseHash('#/new'), { page: 'new', jobId: null, params: {} });
  assert.equal(newDownloadHash([]), '#/new');
  const a = 'https://music.apple.com/us/album/igor/1463409338';
  const b = 'https://music.apple.com/us/album/midnights/1650841512?i=1650841515';
  const h = newDownloadHash([a, b]);
  assert.ok(!/[\s]/.test(h), 'the hash has no raw whitespace');
  assert.deepEqual(newDownloadUrls(parseHash(h).params), [a, b], 'two links round-trip through the hash');
  assert.deepEqual(newDownloadUrls({ url: ` ${a}\n\n ${b}  ` }), [a, b], 'newlines and spaces both separate');
  assert.deepEqual(newDownloadUrls(parseHash(`#/new?url=${encodeURIComponent(`${a} ${b}`)}`).params), [a, b]);
  assert.deepEqual(newDownloadUrls({}), []);
  const drawer = parseHash(buildHash('new', { url: a }, 'job_1'));
  assert.deepEqual(drawer, { page: 'new', jobId: 'job_1', params: { url: a } }, 'the drawer opens over New download and keeps its links');
});

test('#/settings takes an optional section', () => {
  assert.deepEqual(parseHash('#/settings'), { page: 'settings', jobId: null, params: {} });
  assert.equal(settingsHash(), '#/settings');
  assert.equal(settingsHash('library_sync'), '#/settings?section=library_sync');
  assert.equal(settingsSection(parseHash('#/settings?section=account').params), 'account');
  assert.equal(settingsSection({ section: 'Bad Name' }), '');
  assert.equal(settingsSection({}), '');
});

test('queue tabs map to API queries', () => {
  assert.deepEqual(QUEUE_TABS, ['all', 'active', 'completed', 'failed', 'cancelled'], 'All comes first');
  assert.deepEqual(queueFilters({}), { tab: 'all', type: '', q: '' }, 'the Queue opens on All');
  const active = queueFilters({ tab: 'active' });
  assert.deepEqual(queueQuery(active), { limit: 50, offset: 0, status: 'running,queued', sort: 'created_at', order: 'asc' });
  assert.deepEqual(queueQuery(queueFilters({ tab: 'failed', type: 'album', q: 'x' }), 50, 25), {
    limit: 25,
    offset: 50,
    status: 'failed',
    type: 'album',
    q: 'x',
    sort: 'updated_at',
    order: 'desc',
  });
  assert.deepEqual(queueQuery(queueFilters({ tab: 'all' })), { limit: 50, offset: 0 });
  assert.deepEqual(queueQuery(queueFilters({ tab: 'completed' })), { limit: 50, offset: 0, status: 'completed', sort: 'updated_at', order: 'desc' });
  assert.deepEqual(queueQuery(queueFilters({ tab: 'cancelled' })), { limit: 50, offset: 0, status: 'cancelled', sort: 'updated_at', order: 'desc' });
  assert.equal(queueFilters({ tab: 'cancelled' }).tab, 'cancelled');
  assert.equal(queueFilters({ tab: 'bogus', type: 'bogus' }).tab, 'all');
  assert.deepEqual(queueMatchFilter({ tab: 'failed', type: 'song', q: 'z' }), { statuses: ['failed'], types: ['song'], q: 'z' });
});

test('log filters, params and queries', () => {
  const def = logFilters({});
  assert.deepEqual(def.levels, ['info', 'warn', 'error']);
  assert.equal(logParams(def).level, '', 'the default levels are not written to the URL');
  const f = logFilters({ level: 'error,debug,bogus', job_id: 'job_1', component: 'media' });
  assert.deepEqual(f.levels, ['debug', 'error'], 'kept in severity order, unknown levels dropped');
  assert.deepEqual(logQuery(f, { limit: 200 }), { level: 'debug,error', component: 'media', job_id: 'job_1', limit: 200 });
  assert.deepEqual(logFilters({ level: 'bogus' }).levels, ['info', 'warn', 'error']);
});

test('encodeQuery skips empty values', () => {
  assert.equal(encodeQuery({ a: 1, b: '', c: null, d: undefined, e: 'x y' }), 'a=1&e=x+y');
});

test('API URLs resolve against the server root above the console', () => {
  assert.equal(resolveUrl('/api/v1/downloads', { limit: 1 }, 'http://h:18080/console/'), 'http://h:18080/api/v1/downloads?limit=1');
  assert.equal(resolveUrl('/api/v1/health', undefined, 'https://h/amdl/console/'), 'https://h/amdl/api/v1/health');
  assert.equal(resolveUrl('/api/v1/system', {}, 'http://h/console/#/system'), 'http://h/api/v1/system');
});

test('toApiError reads the 2.0 error envelope', () => {
  const e = toApiError(404, { error: { code: 'job_not_found', message: 'job job_1 not found', details: {} } }, 'fallback');
  assert.equal(e.status, 404);
  assert.equal(e.code, 'job_not_found');
  assert.equal(e.message, 'job job_1 not found');
  const bare = toApiError(502, null, 'GET /x failed with HTTP 502.');
  assert.equal(bare.message, 'GET /x failed with HTTP 502.');
  assert.equal(bare.code, '');
});

test('refusedMove undoes a refused hash change without adding an entry', () => {
  assert.equal(historyIndex({ amdlIdx: 3 }), 3);
  assert.equal(historyIndex(null), null);
  assert.equal(historyIndex({ other: 1 }), null);
  assert.deepEqual(refusedMove(5, null), { undo: -1, redo: null }, 'a link pushed a new entry');
  assert.deepEqual(refusedMove(5, { amdlIdx: 4 }), { undo: 1, redo: -1 }, 'Back');
  assert.deepEqual(refusedMove(5, { amdlIdx: 7 }), { undo: -2, redo: 2 }, 'Forward twice');
  assert.equal(refusedMove(5, { amdlIdx: 5 }).undo, 0, 'the same stamp cannot be undone through the history');
});

test('the plain way to the Queue returns to its last filters', () => {
  assert.equal(queueHash(), '#/queue', 'nothing kept yet');
  rememberQueue({ tab: 'failed', type: 'album', q: 'ado' });
  assert.equal(queueHash(), '#/queue?tab=failed&type=album&q=ado');
  rememberQueue({ tab: '', type: '', q: 'ado' });
  assert.equal(queueHash(), '#/queue?q=ado', 'no tab in the link: the default, All');
  rememberQueue({});
  assert.equal(queueHash(), '#/queue');
});
