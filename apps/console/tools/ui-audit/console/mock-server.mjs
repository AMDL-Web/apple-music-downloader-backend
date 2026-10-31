// A stand-in for `amdl serve` that the audit drives: it serves
// apps/console/static under /console/ with the Go server's CSP, and answers
// /api/v1 from an in-memory world (fixtures.mjs), streams included. Worlds,
// latency and faults are switched per scenario, and `tick()` moves running
// jobs one deterministic step, so a live page reaches the same states every
// run.
//
//   node apps/console/tools/ui-audit/console/mock-server.mjs [--port 18571] [--world active]
//
// opens it for a look by hand; POST /__mock/{world,tick,fault} steer it. A
// fault or latency keyed `GET /api/v1/search?offset` hits only the later
// pages of a search group shown alone.

import http from 'node:http';
import { readFile } from 'node:fs/promises';
import { readFileSync } from 'node:fs';
import { extname, join, normalize } from 'node:path';
import { fileURLToPath } from 'node:url';
import { albumProgress } from '../../../static/js/lib/album-progress.js';
import { WORLDS, CATALOG, PLAYLISTS, LOSSY_ALBUM, CODECS, STAGE_ORDER, stageFields, downloadBytes, findAlbum, makeJob, stats, system, iso } from './fixtures.mjs';

const STATIC_DIR = fileURLToPath(new URL('../../../static/', import.meta.url));
const CONSOLE_GO = fileURLToPath(new URL('../../../console.go', import.meta.url));

/** The CSP the Go server sends, read from console.go so the two cannot drift. */
export function readCsp(src = readFileSync(CONSOLE_GO, 'utf8')) {
  const m = src.match(/const ContentSecurityPolicy = ((?:"[^"]*"\s*\+?\s*)+)/);
  if (!m) throw new Error('ContentSecurityPolicy not found in console.go');
  return [...m[1].matchAll(/"([^"]*)"/g)].map((x) => x[1]).join('');
}

const TYPES = {
  '.html': 'text/html; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8',
  '.mjs': 'text/javascript; charset=utf-8',
  '.css': 'text/css; charset=utf-8',
  '.svg': 'image/svg+xml',
  '.json': 'application/json',
  '.png': 'image/png',
  '.ico': 'image/x-icon',
  '.webmanifest': 'application/manifest+json',
};

const TERMINAL = new Set(['completed', 'failed', 'cancelled']);
const MILESTONES = new Set(['job_queued', 'job_started', 'resolved_input', 'job_finished', 'job_failed', 'job_cancelled', 'job_retried', 'item_completed', 'item_failed', 'item_skipped', 'job_deleted']);

/** A job as the API sends it: with progress, derived from its items like the server's internal/progress#Overall. */
function withProgress(world, job) {
  return { ...job, progress: albumProgress(job.status, world.items.get(job.id) || []).percent / 100 };
}

function statusCounts(world) {
  const c = { queued: 0, running: 0, completed: 0, failed: 0, cancelled: 0, total: 0 };
  for (const j of world.jobs.values()) {
    c[j.status]++;
    c.total++;
  }
  return c;
}

function list(v) {
  if (!v) return [];
  return String(v)
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean);
}

function json(res, status, body) {
  const raw = JSON.stringify(body);
  res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' });
  res.end(raw);
}

function apiError(res, status, code, message, details) {
  json(res, status, { error: { code, message, ...(details ? { details } : {}) } });
}

function readBody(req) {
  return new Promise((resolve) => {
    let s = '';
    req.on('data', (c) => (s += c));
    req.on('end', () => {
      try {
        resolve(s ? JSON.parse(s) : null);
      } catch {
        resolve(null);
      }
    });
  });
}

function parseLink(url) {
  const m = String(url || '').match(/music\.apple\.com\/([a-z]{2})\/(album|song|playlist|artist|station|music-video)\/(?:[^/?#]*\/)?([A-Za-z0-9.-]+)(?:\?i=(\d+))?/);
  if (!m) return null;
  return { storefront: m[1], type: m[2], id: m[3], track_id: m[4] };
}

export async function startMockServer({ port = 0, host = '127.0.0.1', world = 'active', staticDir = STATIC_DIR } = {}) {
  const csp = readCsp();
  const feeds = new Set();
  const jobStreams = new Map();
  const logStreams = new Set();
  const state = { world: null, latency: 0, latencies: {}, faults: {} };

  function sse(res) {
    res.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache', 'X-Accel-Buffering': 'no' });
    res.write(': open\n\n');
  }
  const send = (res, id, name, data) => res.write(`id: ${id}\nevent: ${name}\ndata: ${JSON.stringify(data)}\n\n`);

  function setWorld(name, opts = {}) {
    const make = typeof name === 'function' ? name : Object.hasOwn(WORLDS, name) ? WORLDS[name] : undefined;
    if (!make) throw new Error(`unknown world ${name}`);
    const w = make(Date.now());
    Object.assign(w, opts);
    w.onEvent = (ev) => {
      for (const res of jobStreams.get(ev.job_id) || []) send(res, ev.id, ev.type, ev);
      if (MILESTONES.has(ev.type)) {
        const job = w.jobs.get(ev.job_id);
        const msg = job
          ? { type: 'download_upserted', job: withProgress(w, job), event_id: ev.id, status_counts: statusCounts(w) }
          : { type: 'download_deleted', job_id: ev.job_id, event_id: ev.id, status_counts: statusCounts(w) };
        for (const res of feeds) send(res, ev.id, msg.type, msg);
      }
    };
    w.onLog = (l) => {
      for (const s of logStreams) if (s.match(l)) send(s.res, l.sequence, 'log', l);
    };
    // Streams opened on the previous world end, as on a server restart.
    for (const res of feeds) res.end();
    for (const set of jobStreams.values()) for (const res of set) res.end();
    for (const s of logStreams) s.res.end();
    feeds.clear();
    jobStreams.clear();
    logStreams.clear();
    state.world = w;
    state.faults = { ...(opts.faults || {}) };
    state.latencies = { ...(opts.latencies || {}) };
    state.latency = opts.latency ?? 0;
    return w;
  }

  /** Advances every running or queued job one step; returns how many items moved. */
  function tick(steps = 1) {
    const w = state.world;
    let moved = 0;
    for (let s = 0; s < steps; s++) {
      for (const job of [...w.jobs.values()]) {
        if (job.status === 'queued') {
          const running = [...w.jobs.values()].filter((j) => j.status === 'running').length;
          if (running < 3) {
            Object.assign(job, { status: 'running', started_at: iso(Date.now()), updated_at: iso(Date.now()) });
            w.addEvent({ job_id: job.id, type: 'job_started', message: 'job started', payload: { ...job } });
            moved++;
          }
          continue;
        }
        if (job.status !== 'running') continue;
        const items = w.items.get(job.id);
        if (!job.title) {
          const a = CATALOG.albums[(job.id.charCodeAt(8) + 5) % CATALOG.albums.length];
          const tmp = { now: Date.now(), jobs: new Map(), items: new Map(), addEvent: () => {} };
          const fresh = makeJob(tmp, { key: `${job.id}:resolved`, id: job.id, album: a, status: 'running', tracks: 8, stages: () => 'queued' });
          Object.assign(job, { ...fresh, input: job.input, created_at: job.created_at, started_at: job.started_at, updated_at: iso(Date.now()) });
          w.items.set(job.id, tmp.items.get(job.id));
          w.addEvent({ job_id: job.id, type: 'resolved_input', message: job.type, payload: { ...job } });
          moved++;
          continue;
        }
        let inFlight = items.filter((it) => !TERMINAL.has(it.status) && it.status !== 'skipped_existing' && it.status !== 'queued');
        while (inFlight.length < 2) {
          const next = items.find((it) => it.status === 'queued');
          if (!next) break;
          Object.assign(next, stageFields('resolving', 0), { updated_at: iso(Date.now()) });
          w.addEvent({ job_id: job.id, item_id: next.id, type: 'item_progress', phase: 'resolving', message: next.status_message, payload: { ...next } });
          inFlight = [...inFlight, next];
          moved++;
        }
        for (const it of inFlight) {
          const codecKey = it.codec === 'alac' ? `alac-${it.bit_depth}-${Math.floor(it.sample_rate / 1000)}` : it.codec || 'alac-24-48';
          let stage = it.status;
          let frac = stage === 'downloading' ? it.progress.download : stage === 'decrypting' ? it.progress.decrypt : 0;
          if (stage === 'downloading' && frac < 1) frac = Math.min(1, Math.round((frac + 0.2) * 100) / 100);
          else if (stage === 'decrypting' && frac < 1) frac = Math.min(1, frac + 0.5);
          else {
            stage = STAGE_ORDER[STAGE_ORDER.indexOf(stage) + 1] || 'completed';
            frac = 0;
            if (stage === 'downloading' && !it.codec) {
              Object.assign(it, stageFields('waiting_download', 0, 'alac-24-48'));
              w.addEvent({ job_id: job.id, item_id: it.id, type: 'codec_selected', phase: 'alac', payload: { ...it } });
            }
          }
          Object.assign(it, stageFields(stage, frac, CODECS[codecKey] ? codecKey : 'alac-24-48'), { updated_at: iso(Date.now()) });
          const size = Math.round((it.duration_ms / 1000) * ((it.bitrate || 1800000) / 8));
          it.progress.download_bytes = downloadBytes(stage, frac, size);
          if (stage === 'completed') {
            it.file_size = size;
            it.output_path = `/app/data/downloads/albums/${job.artist_name}/${job.title}/${String(it.index).padStart(2, '0')}. ${it.title}.m4a`;
            w.addEvent({ job_id: job.id, item_id: it.id, type: 'item_completed', message: 'saved', payload: { ...it } });
          } else {
            w.addEvent({ job_id: job.id, item_id: it.id, type: 'item_progress', phase: it.status, message: it.status_message, payload: { ...it } });
          }
          moved++;
        }
        recount(job, items);
        if (items.every((it) => TERMINAL.has(it.status) || it.status === 'skipped_existing')) {
          Object.assign(job, { status: job.failed_items ? 'failed' : 'completed', finished_at: iso(Date.now()), updated_at: iso(Date.now()) });
          w.addEvent({ job_id: job.id, type: job.status === 'failed' ? 'job_failed' : 'job_finished', message: 'job finished', payload: { ...job } });
        } else job.updated_at = iso(Date.now());
      }
    }
    // The feed pushes a running job again every 2 s while its progress moves,
    // between the milestones.
    const last = w.events.length ? w.events[w.events.length - 1].id : 0;
    for (const job of w.jobs.values()) {
      if (job.status !== 'running' || !job.title) continue;
      const msg = { type: 'download_upserted', job: withProgress(w, job), event_id: last, status_counts: statusCounts(w) };
      for (const res of feeds) send(res, last, msg.type, msg);
    }
    return moved;
  }

  function recount(job, items) {
    job.completed_items = items.filter((i) => i.status === 'completed').length;
    job.skipped_items = items.filter((i) => i.status === 'skipped_existing').length;
    job.failed_items = items.filter((i) => i.status === 'failed').length;
    job.cancelled_items = items.filter((i) => i.status === 'cancelled').length;
  }

  async function serveStatic(req, res, path) {
    let rel = path.replace(/^\/console\/?/, '');
    if (rel === '' || rel.endsWith('/')) rel += 'index.html';
    const file = normalize(join(staticDir, decodeURIComponent(rel)));
    if (!file.startsWith(normalize(staticDir))) return apiError(res, 404, 'not_found', 'not found');
    try {
      const body = await readFile(file);
      res.writeHead(200, {
        'Content-Type': TYPES[extname(file)] || 'application/octet-stream',
        'Content-Security-Policy': csp,
        'X-Content-Type-Options': 'nosniff',
        'Cache-Control': 'no-cache',
      });
      res.end(body);
    } catch {
      res.writeHead(404, { 'Content-Type': 'text/plain' });
      res.end('not found');
    }
  }

  function filteredJobs(q) {
    const w = state.world;
    const statuses = q.getAll('status').flatMap(list);
    const types = q.getAll('type').flatMap(list);
    const term = (q.get('q') || '').toLowerCase();
    let out = [...w.jobs.values()].filter((j) => {
      if (statuses.length && !statuses.includes(j.status)) return false;
      if (types.length && !types.includes(j.type)) return false;
      if (term && !`${j.title || ''}\n${j.input}\n${j.artist_name || ''}`.toLowerCase().includes(term)) return false;
      return true;
    });
    const sort = q.get('sort') || 'created_at';
    const dir = q.get('order') === 'asc' ? 1 : -1;
    out.sort((a, b) => dir * (Date.parse(a[sort]) - Date.parse(b[sort])) || dir * a.id.localeCompare(b.id));
    return out;
  }

  async function api(req, res, url) {
    const w = state.world;
    const path = url.pathname;
    const q = url.searchParams;
    const key = `${req.method} ${path.replace(/job_[0-9a-f]{32}/, '{id}')}`;
    // `GET /api/v1/search?offset` names a later page of a search alone.
    const paged = q.get('offset') ? `${key}?offset` : '';
    const fault = (paged && state.faults[paged]) || state.faults[key] || state.faults[`${req.method} ${path}`];
    // A scenario's latency, capped so that no request can hold the server.
    const wait = Math.min(Number((paged && state.latencies[paged]) ?? state.latencies[key] ?? state.latency) || 0, 60_000);
    if (wait > 0) await new Promise((r) => setTimeout(r, wait));
    if (fault) {
      if (fault.hang) return;
      return apiError(res, fault.status || 500, fault.code || 'internal_error', fault.message || 'Injected failure', fault.details);
    }
    const m = path.match(/^\/api\/v1\/downloads\/(job_[0-9a-f]+)(\/(cancel|retry|events))?$/);
    if (m) return jobRoute(req, res, m[1], m[3], q);
    const am = path.match(/^\/api\/v1\/artists\/([^/]+)\/albums$/);
    if (am) return artistAlbums(res, am[1], q);
    switch (`${req.method} ${path}`) {
      case 'GET /api/v1/health':
        return json(res, w.health.status === 'ok' ? 200 : 503, w.health);
      case 'GET /api/v1/system':
        return json(res, 200, system(w));
      case 'GET /api/v1/stats':
        return json(res, 200, stats(w, Number(q.get('days')) || 14));
      case 'GET /api/v1/config':
        return json(res, 200, { config: w.config, persisted: true });
      case 'PUT /api/v1/config': {
        const body = (await readBody(req)) || {};
        for (const [sec, vals] of Object.entries(body)) w.config[sec] = { ...w.config[sec], ...vals };
        return json(res, 200, { config: w.config, persisted: true });
      }
      case 'POST /api/v1/config/reload':
        return json(res, 200, { config: w.config, persisted: true, ...(w.reloadError ? { reload_error: w.reloadError } : {}) });
      case 'GET /api/v1/hooks':
        return json(res, 200, w.hooks);
      case 'GET /api/v1/library-sync':
        return json(res, 200, w.librarySync);
      case 'POST /api/v1/library-sync/reset':
        w.librarySync.anchor_size = 0;
        return json(res, 200, w.librarySync);
      case 'GET /api/v1/wrapper/status':
        return json(res, 200, w.wrapper);
      case 'POST /api/v1/wrapper/login':
        return json(res, 200, { status: 'needs_2fa', login_id: 'login_1' });
      case 'POST /api/v1/wrapper/logout':
        return json(res, 200, { status: 'logged_out' });
      case 'GET /api/v1/logs': {
        const entries = logMatcher(q);
        const limit = Number(q.get('limit')) || 200;
        const rows = w.logs.filter(entries).slice(-limit);
        return json(res, 200, { logs: rows, next_cursor: w.logs.length, oldest_sequence: 1, truncated: false });
      }
      case 'GET /api/v1/logs/stream': {
        sse(res);
        const s = { res, match: logMatcher(q) };
        const after = Number(q.get('cursor')) || 0;
        for (const l of w.logs) if (l.sequence > after && s.match(l)) send(res, l.sequence, 'log', l);
        logStreams.add(s);
        req.on('close', () => logStreams.delete(s));
        return;
      }
      case 'GET /api/v1/downloads': {
        const all = filteredJobs(q);
        const limit = Math.min(200, Number(q.get('limit')) || 50);
        const offset = Number(q.get('offset')) || 0;
        return json(res, 200, { downloads: all.slice(offset, offset + limit).map((j) => withProgress(w, j)), total: all.length, limit, offset, status_counts: statusCounts(w), event_cursor: w.cursor });
      }
      case 'POST /api/v1/downloads':
        return submit(req, res);
      case 'GET /api/v1/downloads/events': {
        sse(res);
        feeds.add(res);
        req.on('close', () => feeds.delete(res));
        return;
      }
      case 'POST /api/v1/lookup':
        return lookup(req, res);
      case 'POST /api/v1/quality':
        return quality(req, res);
      case 'GET /api/v1/search':
        return search(res, q);
    }
    return apiError(res, 404, 'not_found', `no route ${req.method} ${path}`);
  }

  function logMatcher(q) {
    const levels = list(q.get('level'));
    const comp = q.get('component');
    const jobId = q.get('job_id');
    const term = (q.get('q') || '').toLowerCase();
    return (l) =>
      (!levels.length || levels.includes(l.level)) &&
      (!comp || l.attributes.component === comp) &&
      (!jobId || l.attributes.job_id === jobId) &&
      (!term || JSON.stringify(l).toLowerCase().includes(term));
  }

  async function jobRoute(req, res, id, action, q) {
    const w = state.world;
    const job = w.jobs.get(id);
    if (!job) return apiError(res, 404, 'not_found', 'job not found');
    if (!action && req.method === 'GET') {
      return json(res, 200, { job: withProgress(w, job), items: w.items.get(id) || [], hooks: [], event_cursor: w.cursor });
    }
    if (!action && req.method === 'DELETE') {
      if (!TERMINAL.has(job.status)) return apiError(res, 409, 'job_active', 'job is still active');
      w.jobs.delete(id);
      w.addEvent({ job_id: id, type: 'job_deleted', message: 'job deleted' });
      return json(res, 200, { deleted: true });
    }
    if (action === 'cancel') {
      Object.assign(job, { status: 'cancelled', finished_at: iso(Date.now()), updated_at: iso(Date.now()) });
      for (const it of w.items.get(id) || []) if (!TERMINAL.has(it.status) && it.status !== 'skipped_existing') Object.assign(it, stageFields('cancelled', 0));
      recount(job, w.items.get(id) || []);
      w.addEvent({ job_id: id, type: 'job_cancelled', message: 'job cancelled', payload: { ...job } });
      return json(res, 200, job);
    }
    if (action === 'retry') {
      Object.assign(job, { status: 'queued', updated_at: iso(Date.now()) });
      delete job.error;
      delete job.finished_at;
      for (const it of w.items.get(id) || []) if (it.status === 'failed' || it.status === 'cancelled') Object.assign(it, stageFields('queued', 0));
      recount(job, w.items.get(id) || []);
      w.addEvent({ job_id: id, type: 'job_retried', message: 'job retried', payload: { ...job } });
      return json(res, 202, job);
    }
    if (action === 'events') {
      sse(res);
      const after = Number(q.get('cursor')) || 0;
      for (const ev of w.events) if (ev.job_id === id && ev.id > after) send(res, ev.id, ev.type, ev);
      if (TERMINAL.has(job.status)) return res.end();
      const set = jobStreams.get(id) || new Set();
      set.add(res);
      jobStreams.set(id, set);
      req.on('close', () => set.delete(res));
      return;
    }
    return apiError(res, 405, 'method_not_allowed', 'method not allowed');
  }

  async function submit(req, res) {
    const w = state.world;
    const body = (await readBody(req)) || {};
    const results = [];
    let accepted = 0;
    for (const u of body.urls || []) {
      const link = parseLink(u);
      if (!link) {
        results.push({ url: u, status: 'invalid', error: { code: 'invalid_url', message: 'not an Apple Music link' } });
        continue;
      }
      const a = findAlbum(link.id) || CATALOG.albums[0];
      const job = makeJob({ ...w, addEvent: () => {} }, { key: `submit:${u}:${w.events.length}`, album: a, status: 'queued', tracks: a.track_count || 8, createdAgo: 0, input: u });
      w.addEvent({ job_id: job.id, type: 'job_queued', message: 'job queued', payload: { ...job } });
      results.push({ url: u, status: 'accepted', job });
      accepted++;
    }
    const out = { accepted, rejected: results.length - accepted, results };
    if (!accepted) return apiError(res, 422, 'no_urls_accepted', 'No URL was accepted.', out);
    return json(res, 202, out);
  }

  async function lookup(req, res) {
    const body = (await readBody(req)) || {};
    const link = parseLink(body.url);
    if (!link) return apiError(res, 400, 'invalid_url', 'Not an Apple Music link.');
    const base = { input: body.url, type: link.type, storefront: link.storefront, id: link.id, ...(link.track_id ? { track_id: link.track_id } : {}) };
    if (link.id === '1234567890') return apiError(res, 404, 'catalog_not_found', `read Apple Music catalog: Apple Music has no ${link.type} ${link.id} in storefront ${link.storefront} (404)`, { link: base });
    const a = link.id === LOSSY_ALBUM.id ? LOSSY_ALBUM : findAlbum(link.id);
    const song = CATALOG.songs.find((s) => s.id === link.id || s.id === link.track_id);
    const artist = CATALOG.artists.find((x) => x.id === link.id);
    const src = a || song || artist || CATALOG.albums[[...link.id].reduce((n, c) => n + c.charCodeAt(0), 0) % CATALOG.albums.length];
    const out = { ...base, title: src.name, artwork_url: src.artwork_url };
    for (const k of ['artwork_bg_color', 'artwork_text_color1', 'artwork_text_color2', 'artwork_text_color3', 'artwork_text_color4', 'release_date', 'genre']) if (src[k]) out[k] = src[k];
    if (link.type !== 'playlist' && link.type !== 'station') out.artist_name = src.artist_name || src.name;
    if (link.type === 'album') out.track_count = src.track_count || 10;
    if (link.type === 'song') out.album_name = src.album_name || src.name;
    if (link.type === 'playlist') out.curator_name = 'Apple Music';
    if (src.duration_ms) out.duration_ms = src.duration_ms;
    if (src.audio_traits) out.audio_traits = src.audio_traits;
    return json(res, 200, out);
  }

  async function quality(req, res) {
    const body = (await readBody(req)) || {};
    const link = parseLink(body.url);
    if (!link) return apiError(res, 400, 'invalid_url', 'Not an Apple Music link.');
    const lossy = link.id === LOSSY_ALBUM.id;
    const a = lossy ? LOSSY_ALBUM : findAlbum(link.id) || CATALOG.albums[0];
    const n = link.type === 'song' ? 1 : Math.min(a.track_count || 10, 30);
    // A release without Enhanced HLS: every quality unavailable, as the server answers.
    const opt = (id, label, extra, available = true) => ({ id, label, available: available && !lossy, ...(available && !lossy ? extra : {}) });
    const tracks = [];
    for (let i = 0; i < n; i++) {
      const s = CATALOG.songs[i % CATALOG.songs.length];
      const hi = i % 5 === 0;
      tracks.push({
        song: { id: String(3000000000 + i), name: s.name, artist: a.artist_name, album: a.name, has_lyrics: true },
        qualities: [
          opt('aac', 'AAC', { codec: 'aac', codec_id: 'audio-stereo-256', channels: 2, bitrate: 256000, sample_rate: 44100 }),
          opt('aac-binaural', 'AAC Binaural', {}, false),
          opt('aac-downmix', 'AAC Downmix', {}, false),
          opt('alac', 'Lossless (ALAC)', { codec: 'alac', codec_id: `audio-alac-stereo-${hi ? 96000 : 48000}-24`, channels: 2, bit_depth: 24, sample_rate: hi ? 96000 : 48000, bitrate: hi ? 2565559 : 1877679 }),
          opt('ec3', 'Dolby Atmos', { codec: 'ec3', codec_id: 'audio-atmos-2768', channels: 16, bitrate: 768000, sample_rate: 48000 }, i % 3 !== 0),
          opt('ac3', 'Dolby Digital', {}, false),
        ],
      });
    }
    return json(res, 200, { input: body.url, storefront: link.storefront, type: link.type, adam_id: link.id, tracks });
  }

  // A term's hits: the recorded ones first, then the rest of the catalog,
  // so albums, songs and playlists run to several pages. Playlists come
  // from the term's artists; a term with none of them ("beethoven") has
  // none, and "zzzz" finds nothing at all.
  function search(res, q) {
    const term = (q.get('term') || '').toLowerCase().trim();
    const types = list(q.get('types'));
    const limit = Number(q.get('limit')) || 10;
    const offset = Number(q.get('offset')) || 0;
    const hit = Object.entries(CATALOG.searches).find(([k]) => k.includes(term) || term.includes(k)) || Object.entries(CATALOG.searches)[0];
    const byId = (arr, ids) => ids.map((id) => arr.find((x) => x.id === id)).filter(Boolean);
    const padded = (arr, ids) => {
      const first = byId(arr, ids);
      const seen = new Set(first.map((x) => x.id));
      return [...first, ...arr.filter((x) => !seen.has(x.id))];
    };
    const artists = byId(CATALOG.artists, hit[1].artists);
    const names = new Set(artists.map((a) => a.name));
    const own = PLAYLISTS.filter((p) => [...names].some((n) => p.name.startsWith(`${n} `)));
    const groups =
      term === 'zzzz'
        ? { songs: [], albums: [], playlists: [], artists: [] }
        : {
            songs: padded(CATALOG.songs, hit[1].songs),
            albums: padded(CATALOG.albums, hit[1].albums),
            playlists: own.length ? [...own, ...PLAYLISTS.filter((p) => !own.includes(p))] : [],
            artists,
          };
    // prior_job as the server sets it: the newest active job of the hit's
    // type and id, else the newest completed one.
    const jobs = [...state.world.jobs.values()].sort((x, y) => y.created_at.localeCompare(x.created_at));
    const withPrior = (h) => {
      const mine = jobs.filter((j) => j.type === h.type && j.input.endsWith(`/${h.id}`));
      const j = mine.find((x) => x.status === 'queued' || x.status === 'running') || mine.find((x) => x.status === 'completed');
      return j ? { ...h, prior_job: { id: j.id, status: j.status, ...(j.finished_at ? { finished_at: j.finished_at } : {}) } } : h;
    };
    const out = { term: q.get('term'), storefront: 'cn' };
    for (const [g, arr] of Object.entries(groups)) {
      if (types.length && !types.includes(g)) continue;
      out[g] = { items: arr.slice(offset, offset + limit).map(withPrior), ...(offset + limit < arr.length ? { next_offset: offset + limit } : {}) };
    }
    return json(res, 200, out);
  }

  function artistAlbums(res, id, q) {
    const artist = CATALOG.artists.find((a) => a.id === id) || CATALOG.artists[0];
    const albums = CATALOG.albums.filter((a) => a.artist_name === artist.name);
    const offset = Number(q.get('offset')) || 0;
    const limit = Number(q.get('limit')) || 25;
    const page = albums.slice(offset, offset + limit);
    return json(res, 200, { storefront: 'cn', artist, albums: { items: page, ...(offset + limit < albums.length ? { next_offset: offset + limit } : {}) } });
  }

  async function control(req, res, url) {
    const body = (await readBody(req)) || {};
    if (url.pathname === '/__mock/world') {
      setWorld(body.name || 'active', body.opts || {});
      return json(res, 200, { ok: true, cursor: state.world.cursor });
    }
    if (url.pathname === '/__mock/tick') return json(res, 200, { moved: tick(body.steps || 1) });
    if (url.pathname === '/__mock/fault') {
      state.faults = body.faults || {};
      state.latencies = body.latencies || state.latencies;
      if (body.latency !== undefined) state.latency = body.latency;
      return json(res, 200, { ok: true });
    }
    return apiError(res, 404, 'not_found', 'unknown control');
  }

  const server = http.createServer((req, res) => {
    const url = new URL(req.url, 'http://x');
    if (url.pathname.startsWith('/__mock/')) return control(req, res, url);
    if (url.pathname.startsWith('/api/')) return api(req, res, url).catch((e) => apiError(res, 500, 'internal_error', `mock failed: ${e && e.message}`));
    if (url.pathname === '/' || url.pathname === '/console') {
      res.writeHead(302, { Location: '/console/' });
      return res.end();
    }
    if (url.pathname.startsWith('/console/')) return serveStatic(req, res, url.pathname);
    res.writeHead(404);
    res.end();
  });
  // Streams stay open; keep-alive pings stop the console's 45 s idle timer.
  const ping = setInterval(() => {
    for (const r of feeds) r.write(': keepalive\n\n');
    for (const set of jobStreams.values()) for (const r of set) r.write(': keepalive\n\n');
    for (const s of logStreams) s.res.write(': keepalive\n\n');
  }, 15000);
  setWorld(world);
  await new Promise((r) => server.listen(port, host, r));
  const addr = server.address();
  return {
    url: `http://${host}:${addr.port}/console/`,
    origin: `http://${host}:${addr.port}`,
    setWorld,
    tick,
    setFaults(faults = {}, { latency, latencies } = {}) {
      state.faults = faults;
      if (latency !== undefined) state.latency = latency;
      if (latencies) state.latencies = latencies;
    },
    get world() {
      return state.world;
    },
    close() {
      clearInterval(ping);
      for (const r of feeds) r.end();
      for (const set of jobStreams.values()) for (const r of set) r.end();
      for (const s of logStreams) s.res.end();
      server.closeAllConnections?.();
      return new Promise((r) => server.close(r));
    },
  };
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  const args = process.argv.slice(2);
  const opt = { port: 18571, world: 'active' };
  for (let i = 0; i < args.length; i++) {
    const a = args[i];
    const v = args[i + 1];
    if ((a === '--port' || a === '--world' || a === '--host') && v && !v.startsWith('--')) {
      opt[a.slice(2)] = a === '--port' ? Number(v) : v;
      i++;
    } else {
      process.stderr.write('usage: mock-server.mjs [--port N] [--host H] [--world empty|idle|active|many]\n');
      process.exit(2);
    }
  }
  if (!WORLDS[opt.world] || !Number.isInteger(opt.port)) {
    process.stderr.write('usage: mock-server.mjs [--port N] [--host H] [--world empty|idle|active|many]\n');
    process.exit(2);
  }
  const s = await startMockServer(opt);
  process.stdout.write(`mock amdl at ${s.url} (world ${opt.world})\n`);
}
