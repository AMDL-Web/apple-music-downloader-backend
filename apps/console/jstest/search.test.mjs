import test from 'node:test';
import assert from 'node:assert/strict';
import { scanLinks } from '../static/js/lib/links.js';
import {
  searchTerm,
  appendPage,
  createSearcher,
  createArtistPages,
  qualityHints,
  hitView,
  shownHits,
  nextStop,
  searchCountLine,
  SEARCH_FIRST,
  SEARCH_MORE,
  SEARCH_MAX_TERM,
  SEARCH_CACHE_MAX,
  ARTIST_PAGE,
} from '../static/js/lib/search.js';
import { hitKey, hitPrior, trackJobs, hitPreview, sentJob } from '../static/js/lib/search-jobs.js';

const tick = () => new Promise((r) => setTimeout(r, 0));
const settle = async () => {
  for (let i = 0; i < 4; i++) await tick();
};

/** A fake network function whose answers the test releases one by one. */
function fakeNet() {
  const calls = [];
  const fn = (...args) =>
    new Promise((resolve, reject) => {
      const signal = args[args.length - 1];
      const call = { args, signal, resolve, reject };
      signal.addEventListener('abort', () => reject(Object.assign(new Error('cancelled'), { name: 'AbortError' })));
      calls.push(call);
    });
  return { calls, fn };
}

const song = (id, extra = {}) => ({ id, type: 'song', name: `Song ${id}`, url: `https://music.apple.com/cn/song/s/${id}`, ...extra });
const album = (id, extra = {}) => ({ id, type: 'album', name: `Album ${id}`, url: `https://music.apple.com/cn/album/a/${id}`, ...extra });
const answer = (term, songs = [song('1')], next = {}) => ({
  term,
  storefront: 'cn',
  songs: { items: songs, ...(next.songs ? { next_offset: next.songs } : {}) },
  albums: { items: [album('10')] },
  artists: { items: [] },
});

test('the box searches only when it holds no link', () => {
  const term = (text) => searchTerm(text, scanLinks(text, 'cn'));
  assert.deepEqual(term('  Taylor   Swift \n'), { term: 'Taylor Swift', tooLong: false });
  assert.deepEqual(term('星街すいせい'), { term: '星街すいせい', tooLong: false });
  assert.deepEqual(term('   '), { term: '', tooLong: false });
  assert.deepEqual(term('see https://music.apple.com/us/album/igor/1463409338'), { term: '', tooLong: false });
  assert.deepEqual(term('https://example.com/x'), { term: '', tooLong: false }, 'any link previews instead');
  assert.deepEqual(term('a'.repeat(SEARCH_MAX_TERM)), { term: 'a'.repeat(SEARCH_MAX_TERM), tooLong: false });
  assert.deepEqual(term('a'.repeat(SEARCH_MAX_TERM + 1)), { term: '', tooLong: true });
  // Characters, not UTF-16 units: 200 emoji are still a term.
  assert.equal(term('🎵'.repeat(SEARCH_MAX_TERM)).term.length, SEARCH_MAX_TERM * 2);
});

test('a page appends each hit once and carries the next offset', () => {
  const g = appendPage({ items: [song('1'), song('2')], next_offset: 2 }, { items: [song('2'), song('3')], next_offset: 27 });
  assert.deepEqual(g.items.map((h) => h.id), ['1', '2', '3']);
  assert.equal(g.next_offset, 27);
  assert.deepEqual(appendPage(g, { items: [song('4')] }).next_offset, undefined, 'the last page has none');
  assert.deepEqual(appendPage(null, null), { items: [] });
});

test('a search asks for the first page and keeps the answer', async () => {
  const net = fakeNet();
  let changes = 0;
  const s = createSearcher({ search: net.fn, onChange: () => changes++ });
  s.run('Yorushika');
  assert.equal(s.state().status, 'loading');
  assert.equal(s.state().stale, false, 'nothing shown yet');
  await tick();
  assert.deepEqual(net.calls[0].args[0], { term: 'Yorushika', limit: SEARCH_FIRST });
  net.calls[0].resolve(answer('Yorushika'));
  await settle();
  assert.equal(s.state().status, 'ready');
  assert.deepEqual(shownHits(s.state().data).map(hitKey), ['album:10', 'song:1'], 'albums come first, as the page shows them');
  assert.ok(changes >= 2);
});

test('a new term keeps the last answer on screen until its own lands, and aborts the old search', async () => {
  const net = fakeNet();
  const s = createSearcher({ search: net.fn });
  s.run('Yo');
  await tick();
  net.calls[0].resolve(answer('Yo'));
  await settle();
  s.run('Yor');
  await tick();
  s.run('Yoru');
  await tick();
  assert.equal(s.state().stale, true);
  assert.equal(s.state().data.term, 'Yo', 'the old answer holds the height');
  assert.equal(net.calls[1].signal.aborted, true, 'the search typed past is aborted');
  net.calls[2].resolve(answer('Yoru', [song('2')]));
  await settle();
  assert.equal(s.state().term, 'Yoru');
  assert.equal(s.state().stale, false);
  assert.deepEqual(s.state().data.songs.items.map((h) => h.id), ['2']);
});

test('a late answer for an old term never replaces the current one', async () => {
  const net = fakeNet();
  // A server that answers although the request was aborted.
  const search = (q, signal) => net.fn(q, { addEventListener() {}, aborted: false, real: signal });
  const s = createSearcher({ search });
  s.run('a');
  await tick();
  s.run('b');
  await tick();
  net.calls[1].resolve(answer('b', [song('b')]));
  await settle();
  net.calls[0].resolve(answer('a', [song('a')]));
  await settle();
  assert.equal(s.state().term, 'b');
  assert.deepEqual(s.state().data.songs.items.map((h) => h.id), ['b']);
});

test('answers are cached per term and come back at once', async () => {
  const net = fakeNet();
  const cache = new Map();
  const s = createSearcher({ search: net.fn, cache });
  s.run('x');
  await tick();
  net.calls[0].resolve(answer('x'));
  await settle();
  s.run('y');
  await tick();
  s.run('x');
  assert.equal(s.state().status, 'ready', 'no wait for a term already answered');
  assert.equal(net.calls.length, 2);
  assert.equal(net.calls[1].signal.aborted, true);
  for (let i = 0; i < SEARCH_CACHE_MAX + 5; i++) cache.set(`t${i}`, {});
  s.run('z');
  await tick();
  net.calls[2].resolve(answer('z'));
  await settle();
  assert.ok(cache.size <= SEARCH_CACHE_MAX);
});

test('an error is shown, not cached, and Retry searches again', async () => {
  const net = fakeNet();
  const cache = new Map();
  const s = createSearcher({ search: net.fn, cache });
  s.run('x');
  await tick();
  net.calls[0].reject(Object.assign(new Error('read Apple Music catalog: 502'), { status: 502 }));
  await settle();
  assert.equal(s.state().status, 'error');
  assert.equal(cache.size, 0);
  s.retry();
  await tick();
  assert.equal(net.calls.length, 2);
  net.calls[1].resolve(answer('x'));
  await settle();
  assert.equal(s.state().status, 'ready');
});

test('clearing the term clears the results', async () => {
  const net = fakeNet();
  const s = createSearcher({ search: net.fn });
  s.run('x');
  s.run('');
  assert.equal(s.state().status, 'idle');
  assert.equal(s.state().data, null);
  await tick();
  assert.equal(net.calls.length, 0, 'a search cleared before it started never reaches the network');
});

test('more reads the next page of one group and appends it', async () => {
  const net = fakeNet();
  const s = createSearcher({ search: net.fn });
  s.run('x');
  await tick();
  net.calls[0].resolve(answer('x', [song('1'), song('2')], { songs: 2 }));
  await settle();
  s.more('albums');
  assert.equal(net.calls.length, 1, 'a group without a next page reads nothing');
  s.more('songs');
  s.more('songs');
  await tick();
  assert.equal(net.calls.length, 2, 'one page at a time');
  assert.deepEqual(net.calls[1].args[0], { term: 'x', types: 'songs', offset: 2, limit: SEARCH_MORE });
  assert.equal(s.state().more.songs, 'loading');
  net.calls[1].resolve({ term: 'x', songs: { items: [song('3')] } });
  await settle();
  assert.deepEqual(s.state().data.songs.items.map((h) => h.id), ['1', '2', '3']);
  assert.equal(s.state().data.songs.next_offset, undefined);
  assert.equal(s.state().more.songs, undefined);
  assert.deepEqual(s.state().data.albums.items.map((h) => h.id), ['10'], 'other groups are untouched');
});

test('a failed page leaves the hits and says so', async () => {
  const net = fakeNet();
  const s = createSearcher({ search: net.fn });
  s.run('x');
  await tick();
  net.calls[0].resolve(answer('x', [song('1')], { songs: 1 }));
  await settle();
  s.more('songs');
  await tick();
  net.calls[1].reject(new Error('timeout'));
  await settle();
  assert.equal(s.state().more.songs, 'error');
  assert.equal(s.state().data.songs.items.length, 1);
});

test('an artist opens with its first page, pages on, and is cached', async () => {
  const net = fakeNet();
  const a = createArtistPages({ load: net.fn });
  a.open('159260351');
  assert.equal(a.state().status, 'loading');
  await tick();
  assert.deepEqual(net.calls[0].args.slice(0, 2), ['159260351', { limit: ARTIST_PAGE }]);
  net.calls[0].resolve({ storefront: 'cn', artist: { id: '159260351', type: 'artist', name: 'Taylor Swift' }, albums: { items: [album('1')], next_offset: 25 } });
  await settle();
  assert.equal(a.state().status, 'ready');
  assert.equal(a.state().artist.name, 'Taylor Swift');
  a.more();
  await tick();
  assert.deepEqual(net.calls[1].args.slice(0, 2), ['159260351', { offset: 25, limit: ARTIST_PAGE }]);
  net.calls[1].resolve({ artist: { id: '159260351' }, albums: { items: [album('2')] } });
  await settle();
  assert.deepEqual(a.state().albums.items.map((h) => h.id), ['1', '2']);
  a.open('');
  assert.equal(a.state().status, 'idle');
  a.open('159260351');
  assert.equal(a.state().status, 'ready', 'cached');
  assert.equal(a.state().albums.items.length, 2);
  assert.equal(net.calls.length, 2);
});

test('an artist that fails to load can be retried', async () => {
  const net = fakeNet();
  const a = createArtistPages({ load: net.fn });
  a.open('1');
  await tick();
  net.calls[0].reject(new Error('Apple Music has no artist 1 in storefront cn (404)'));
  await settle();
  assert.equal(a.state().status, 'error');
  a.retry();
  await tick();
  assert.equal(net.calls.length, 2);
});

test('quality hints name the best lossless format, then Atmos', () => {
  assert.deepEqual(qualityHints(['atmos', 'hi-res-lossless', 'lossless', 'lossy-stereo', 'spatial']), ['Hi-Res Lossless', 'Dolby Atmos']);
  assert.deepEqual(qualityHints(['lossless', 'lossy-stereo']), ['Lossless']);
  assert.deepEqual(qualityHints(['lossy-stereo']), []);
  assert.deepEqual(qualityHints(undefined), []);
});

test('a hit row shows what its type has', () => {
  assert.deepEqual(hitView(song('1', { artist_name: 'Yorushika', album_name: '夏草が邪魔をする', duration_ms: 242913, content_rating: 'explicit', audio_traits: ['lossless'] })), {
    title: 'Song 1',
    facts: [
      { key: 'artist', text: 'Yorushika', auto: true },
      { key: 'album', text: '夏草が邪魔をする', auto: true },
      { key: 'length', text: '4:03', auto: false },
    ],
    explicit: true,
    quality: ['Lossless'],
  });
  assert.deepEqual(hitView(album('2', { artist_name: 'Taylor Swift', release_date: '2022-10-21', track_count: 20 })).facts.map((f) => f.text), ['Taylor Swift', '2022', '20 tracks']);
  assert.deepEqual(hitView(album('3', { track_count: 1 })).facts.map((f) => f.text), ['1 track']);
  // A single's album is named after the song: the meta line leaves it out.
  assert.deepEqual(hitView({ id: '5', type: 'song', name: 'Show', artist_name: 'Ado', album_name: 'Show' }).facts.map((f) => f.key), ['artist']);
  assert.deepEqual(hitView({ id: '4', type: 'artist', name: 'Micah', genre: '动画', audio_traits: ['lossless'] }), {
    title: 'Micah',
    facts: [{ key: 'genre', text: '动画', auto: true }],
    explicit: false,
    quality: [],
  });
  assert.deepEqual(hitView({ id: '5', type: 'artist', name: 'X' }).facts, [{ key: 'genre', text: 'Artist', auto: false }]);
  assert.equal(hitView(null), null);
});

test('a hit is in the library or the queue through its prior job, the live one winning', () => {
  const done = song('1', { prior_job: { id: 'job_a', status: 'completed', finished_at: '2026-10-29T10:00:00Z' } });
  assert.deepEqual(hitPrior(done), { state: 'done', job: done.prior_job });
  assert.equal(hitPrior(song('2')), null);
  // The feed says the library job was deleted.
  assert.equal(hitPrior(done, new Map(), new Map([['job_a', { id: 'job_a', deleted: true }]])), null);
  // This page submitted it again: the new job is in the queue.
  const sent = new Map([['song:1', 'job_b']]);
  const jobs = new Map([['job_b', { id: 'job_b', status: 'running' }]]);
  assert.equal(hitPrior(done, sent, jobs).state, 'active');
  assert.equal(hitPrior(done, sent, new Map([['job_b', { id: 'job_b', status: 'completed' }]])).state, 'done');
  assert.equal(hitPrior(done, sent, new Map([['job_b', { id: 'job_b', status: 'failed' }]])), null, 'a failed job marks nothing');
});

test('the feed updates watched jobs only, never with an older snapshot', () => {
  const jobs = new Map([['job_a', { id: 'job_a', status: 'running', updated_at: '2026-10-30T10:00:01Z' }]]);
  const watched = new Set(['job_a']);
  const older = trackJobs(jobs, [{ type: 'download_upserted', job: { id: 'job_a', status: 'queued', updated_at: '2026-10-30T10:00:00Z' } }], watched);
  assert.equal(older, jobs);
  const other = trackJobs(jobs, [{ type: 'download_upserted', job: { id: 'job_x', status: 'completed' } }], watched);
  assert.equal(other, jobs);
  const next = trackJobs(jobs, [{ type: 'download_upserted', job: { id: 'job_a', status: 'completed', updated_at: '2026-10-30T10:05:00Z' } }], watched);
  assert.notEqual(next, jobs);
  assert.equal(next.get('job_a').status, 'completed');
  assert.equal(jobs.get('job_a').status, 'running', 'the old map is left alone');
  assert.deepEqual(trackJobs(next, [{ type: 'download_deleted', job_id: 'job_a' }], watched).get('job_a'), { id: 'job_a', deleted: true });
});

test('a submit result names the job a hit is queued in', () => {
  assert.deepEqual(sentJob({ status: 'accepted', job: { id: 'job_1', status: 'queued' } }), { id: 'job_1', status: 'queued' });
  assert.deepEqual(sentJob({ status: 'duplicate_active', existing_job_id: 'job_2' }), { id: 'job_2', status: 'queued' });
  assert.equal(sentJob({ status: 'invalid' }), null);
  assert.equal(sentJob(null), null);
});

test('a submitted hit seeds its result row like a link preview', () => {
  assert.deepEqual(hitPreview(album('9', { artist_name: 'Yorushika', artwork_url: 'a/{w}x{h}bb.jpg', track_count: 7, prior_job: { id: 'j' } })), {
    title: 'Album 9',
    type: 'album',
    artist_name: 'Yorushika',
    artwork_url: 'a/{w}x{h}bb.jpg',
  });
  assert.equal(hitPreview({ id: '1', type: 'artist', name: 'Yorushika' }).artist_name, 'Yorushika');
});

test('the count line says what was found, or that nothing was', () => {
  assert.equal(searchCountLine({ term: 'x', status: 'loading' }), 'Searching…');
  const ready = (data) => ({ term: 'Yorushika', status: 'ready', data });
  assert.equal(searchCountLine(ready(answer('Yorushika', [song('1'), song('2')]))), '1 album and 2 songs for “Yorushika”');
  assert.equal(
    searchCountLine(ready({ songs: { items: [song('1')] }, albums: { items: [album('1'), album('2')] }, artists: { items: [{ id: 'a', type: 'artist' }] } })),
    '2 albums, 1 song and 1 artist for “Yorushika”',
  );
  assert.equal(searchCountLine(ready({ songs: { items: [] }, albums: { items: [] }, artists: { items: [] } })), 'Nothing found for “Yorushika”');
  assert.equal(searchCountLine({ term: '', status: 'idle' }), '');
  assert.equal(searchCountLine(null, { status: 'ready', artist: { name: 'Yorushika' }, albums: { items: [album('1'), album('2')], next_offset: 25 } }), 'Yorushika: 2 albums so far');
  assert.equal(searchCountLine(null, { status: 'loading' }), 'Loading albums…');
});

test('the arrow keys move among the hits by where they are drawn', () => {
  const box = (left, top, group, w = 100, h = 100) => ({ left, top, right: left + w, bottom: top + h, group });
  // Albums: one row of three tiles; Songs: two rows of two, wider; Artists: one tile.
  const stops = [box(0, 0, 'a'), box(120, 0, 'a'), box(240, 0, 'a'), box(0, 200, 's', 160, 40), box(180, 200, 's', 160, 40), box(0, 250, 's', 160, 40), box(180, 250, 's', 160, 40), box(0, 400, 'r')];
  assert.equal(nextStop(stops, 0, 'ArrowRight'), 1);
  assert.equal(nextStop(stops, 2, 'ArrowRight'), 2, 'the last of a group stays');
  assert.equal(nextStop(stops, 3, 'ArrowLeft'), 3, 'the first of a group stays');
  assert.equal(nextStop(stops, 2, 'ArrowDown'), 4, 'down to the nearest across in the next row');
  assert.equal(nextStop(stops, 4, 'ArrowDown'), 6);
  assert.equal(nextStop(stops, 6, 'ArrowDown'), 7, 'across sections');
  assert.equal(nextStop(stops, 7, 'ArrowDown'), 7, 'nothing below: stay');
  assert.equal(nextStop(stops, 5, 'ArrowUp'), 3);
  assert.equal(nextStop(stops, 1, 'ArrowUp'), -1, 'nothing above: back to the box');
  assert.equal(nextStop(stops, 4, 'Home'), 0);
  assert.equal(nextStop(stops, 4, 'End'), 7);
  assert.equal(nextStop(stops, 1, 'Enter'), 1);
});
