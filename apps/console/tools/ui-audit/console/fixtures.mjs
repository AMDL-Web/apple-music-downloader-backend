// Deterministic worlds for the mock backend: jobs, tracks, events, logs and
// the read-only panels, shaped as internal/api/openapi.yaml describes them.
// Catalog metadata (titles, artwork, palettes) is real Apple data read once
// from GET /search; everything else is generated from a seed, so the same
// world renders the same page every run.

import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { seedHooks } from './fixtures-hooks.mjs';

const here = (p) => fileURLToPath(new URL(p, import.meta.url));
export const CATALOG = JSON.parse(readFileSync(here('./fixtures/catalog.json'), 'utf8'));
const BASE_CONFIG = JSON.parse(readFileSync(here('./fixtures/config.json'), 'utf8'));

export function rng(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function hash(s) {
  let h = 2166136261;
  for (const c of String(s)) h = Math.imul(h ^ c.charCodeAt(0), 16777619);
  return h >>> 0;
}

export function hexId(prefix, key) {
  const r = rng(hash(key));
  let out = '';
  while (out.length < 32) out += Math.floor(r() * 16).toString(16);
  return `${prefix}_${out}`;
}

const iso = (ms) => new Date(ms).toISOString();
const MIN = 60000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

const albumById = new Map(CATALOG.albums.map((a) => [a.id, a]));
const songs = CATALOG.songs;

/** Album metadata by index into the catalog, so worlds are stable. */
export function album(i) {
  return CATALOG.albums[((i % CATALOG.albums.length) + CATALOG.albums.length) % CATALOG.albums.length];
}

export function findAlbum(id) {
  return albumById.get(String(id));
}

/**
 * Search's playlists, made up from the catalog: two per artist (its
 * Essentials and Deep Cuts, curated by Apple Music) with the cover and
 * palette of one of its albums, the way GET /search sends a playlist.
 */
export const PLAYLISTS = CATALOG.artists.flatMap((a, i) => {
  const own = CATALOG.albums.filter((x) => x.artist_name === a.name);
  return [
    ['Essentials', own[0]],
    ['Deep Cuts', own[1] || own[0]],
  ]
    .filter(([, cover]) => cover)
    .map(([what, cover], j) => {
      const id = `pl.${hexId('pl', `${a.id}-${j}`).slice(-32)}`;
      const name = `${a.name} ${what}`;
      const palette = Object.fromEntries(Object.entries(cover).filter(([k]) => k.startsWith('artwork_')));
      return {
        id,
        type: 'playlist',
        name,
        url: `https://music.apple.com/cn/playlist/${name.toLowerCase().replace(/[^a-z0-9]+/g, '-')}/${id}`,
        ...palette,
        artist_name: 'Apple Music',
        audio_traits: (i + j) % 3 === 0 ? ['atmos', 'lossless', 'lossy-stereo'] : ['lossless', 'lossy-stereo'],
      };
    });
});

/** An album with no enhanced formats (lossy-stereo only), outside CATALOG so its indices stay. */
export const LOSSY_ALBUM = {
  ...CATALOG.albums[10],
  id: '1537248340',
  name: 'MEMORIA - EP',
  url: 'https://music.apple.com/cn/album/memoria-ep/1537248340',
  release_date: '2011-08-24',
  track_count: 4,
  audio_traits: ['lossy-stereo'],
};

const PALETTE_KEYS = ['artwork_bg_color', 'artwork_text_color1', 'artwork_text_color2', 'artwork_text_color3', 'artwork_text_color4'];

const CODECS = {
  'alac-24-96': { codec: 'alac', bit_depth: 24, sample_rate: 96000, bitrate: 2565559 },
  'alac-24-48': { codec: 'alac', bit_depth: 24, sample_rate: 48000, bitrate: 1877679 },
  'alac-16-44': { codec: 'alac', bit_depth: 16, sample_rate: 44100, bitrate: 1011000 },
  'alac-24-192': { codec: 'alac', bit_depth: 24, sample_rate: 192000, bitrate: 4800000 },
  'ec3': { codec: 'ec3', bit_depth: 0, sample_rate: 48000, bitrate: 768000 },
  'aac': { codec: 'aac', bit_depth: 0, sample_rate: 44100, bitrate: 256000 },
};

/** Item stage → status, progress and the server's status_message (from a recorded real run). */
function stageFields(stage, frac, codecKey) {
  const c = CODECS[codecKey] || CODECS['alac-24-48'];
  const label = c.codec === 'alac' ? 'ALAC' : c.codec.toUpperCase();
  const p = { download: 0, decrypt: 0, download_bytes: 0, resolved: false, remuxed: false, verified: false, tagged: false, saved: false };
  const withCodec = { codec: c.codec, bit_depth: c.bit_depth, sample_rate: c.sample_rate, bitrate: c.bitrate };
  switch (stage) {
    case 'queued':
      return { status: 'queued', progress: p, status_message: '' };
    case 'resolving':
      return { status: 'resolving', progress: p, status_message: 'resolving metadata' };
    case 'waiting_download':
      return { status: 'waiting_download', progress: { ...p, resolved: true }, status_message: 'waiting for download slot', ...withCodec };
    case 'downloading':
      return {
        status: 'downloading',
        progress: { ...p, resolved: true, download: frac },
        status_message: frac > 0 ? `${label} download ${Math.round(frac * 100)}%` : `Downloading ${label} encrypted media`,
        ...withCodec,
      };
    case 'waiting_decrypt':
      return { status: 'waiting_decrypt', progress: { ...p, resolved: true, download: 1 }, status_message: 'waiting for decrypt slot', ...withCodec };
    case 'decrypting':
      return { status: 'decrypting', progress: { ...p, resolved: true, download: 1, decrypt: frac }, status_message: 'opening decrypt session', ...withCodec };
    case 'remuxing':
      return { status: 'remuxing', progress: { ...p, resolved: true, download: 1, decrypt: 1 }, status_message: 'remuxing', ...withCodec };
    case 'tagging':
      return { status: 'tagging', progress: { ...p, resolved: true, download: 1, decrypt: 1, remuxed: true }, status_message: 'writing metadata', ...withCodec };
    case 'saving':
      return { status: 'saving', progress: { ...p, resolved: true, download: 1, decrypt: 1, remuxed: true, tagged: true, verified: true }, status_message: 'saving', ...withCodec };
    case 'completed':
      return {
        status: 'completed',
        progress: { download: 1, decrypt: 1, resolved: true, remuxed: true, verified: true, tagged: true, saved: true },
        status_message: 'saved',
        ...withCodec,
      };
    case 'skipped_existing':
      return { status: 'skipped_existing', progress: { ...p, resolved: true }, status_message: 'File already exists; skipped', ...withCodec };
    case 'failed':
      return { status: 'failed', progress: { ...p, resolved: true, download: frac || 0 }, status_message: '', ...withCodec };
    case 'cancelled':
      return { status: 'cancelled', progress: p, status_message: 'cancelled' };
    default:
      throw new Error(`unknown stage ${stage}`);
  }
}

const PAST_DOWNLOAD = new Set(['waiting_decrypt', 'decrypting', 'remuxing', 'tagging', 'saving', 'completed']);

/**
 * `progress.download_bytes` of a track of `size` bytes in a stage: the share
 * of the file received while it downloads (or when it failed part-way), all of
 * it once the transfer is over, 0 before.
 */
export function downloadBytes(stage, frac, size) {
  if (PAST_DOWNLOAD.has(stage)) return size;
  return stage === 'downloading' || stage === 'failed' ? Math.round((frac || 0) * size) : 0;
}

/** The order a track moves through; used by the live ticker. */
export const STAGE_ORDER = ['queued', 'resolving', 'waiting_download', 'downloading', 'waiting_decrypt', 'decrypting', 'remuxing', 'tagging', 'saving', 'completed'];

const LONG_TITLES = [
  'Symphony No. 9 in D Minor, Op. 125 "Choral": IV. Presto – Allegro assai – Allegro assai vivace (Alla marcia)',
  'Symphony No. 9 in D Minor, Op. 125 "Choral": III. Adagio molto e cantabile – Andante moderato',
  'Piano Concerto No. 5 in E-Flat Major, Op. 73 "Emperor": II. Adagio un poco mosso',
];

/**
 * Builds one job and its tracks.
 * spec: {key, album (index or object), type, status, tracks, stages (array or fn(i)), codecs (fn(i) → codec key),
 *        createdAgo, durationMs, error, failedItems, storefront, title, input, unresolved}
 */
export function makeJob(world, spec) {
  const r = rng(hash(spec.key));
  const a = typeof spec.album === 'object' ? spec.album : album(spec.album ?? 0);
  const id = spec.id || hexId('job', spec.key);
  const type = spec.type || 'album';
  const created = world.now - (spec.createdAgo ?? HOUR);
  const status = spec.status || 'completed';
  const started = status === 'queued' ? null : created + 2000;
  const finished = ['completed', 'failed', 'cancelled'].includes(status) ? started + (spec.durationMs ?? 90000) : null;
  const sf = spec.storefront || 'cn';
  const input = spec.input || `https://music.apple.com/${sf}/${type}/${encodeURIComponent((a.name || 'x').toLowerCase().replace(/\s+/g, '-'))}/${a.id}`;
  const job = {
    id,
    input,
    type,
    storefront: sf,
    status,
    total_items: 0,
    completed_items: 0,
    skipped_items: 0,
    failed_items: 0,
    cancelled_items: 0,
    created_at: iso(created),
    updated_at: iso(finished || started || created),
  };
  if (!spec.unresolved) {
    Object.assign(job, {
      title: spec.title || a.name,
      artwork_url: a.artwork_url,
      artist_name: a.artist_name,
      artist_url: `https://music.apple.com/${sf}/artist/x/${hash(a.artist_name) % 100000000}`,
      release_date: a.release_date,
      genre: a.genre,
    });
    for (const k of PALETTE_KEYS) if (a[k]) job[k] = a[k];
    if (spec.motion) Object.assign(job, CATALOG.motion_sample);
    if (type === 'playlist') {
      delete job.artist_name;
      delete job.artist_url;
      job.curator_name = spec.curator || 'Apple Music';
    }
  }
  if (started) job.started_at = iso(started);
  if (finished) job.finished_at = iso(finished);
  if (spec.error) job.error = spec.error;
  if (spec.overrides) job.overrides = spec.overrides;

  const n = spec.unresolved ? 0 : spec.tracks ?? a.track_count ?? 10;
  const items = [];
  for (let i = 0; i < n; i++) {
    const s = songs[(hash(spec.key) + i) % songs.length];
    const stage = typeof spec.stages === 'function' ? spec.stages(i, n) : Array.isArray(spec.stages) ? spec.stages[i] : status === 'completed' ? 'completed' : status === 'cancelled' ? 'cancelled' : 'queued';
    const frac = typeof spec.fracs === 'function' ? spec.fracs(i, n) : 0.4;
    const codecKey = spec.codecs ? spec.codecs(i, n) : 'alac-24-48';
    const f = stageFields(stage, frac, codecKey);
    const title = spec.longTitles && i < LONG_TITLES.length ? LONG_TITLES[i] : s.name;
    const durationMs = s.duration_ms || 180000 + Math.floor(r() * 120000);
    const size = Math.round((durationMs / 1000) * ((f.bitrate || 1800000) / 8) * (0.95 + r() * 0.1));
    const updated = finished ? finished - (n - i) * 1500 : (started || created) + i * 1000;
    f.progress = { ...f.progress, download_bytes: downloadBytes(stage, frac, size) };
    const item = {
      id: hexId('item', `${spec.key}:${i}`),
      job_id: id,
      adam_id: String(1000000000 + (hash(`${spec.key}:${i}`) % 900000000)),
      kind: 'song',
      index: i + 1,
      title,
      artist: spec.trackArtist ? spec.trackArtist(i) : a.artist_name,
      album: a.name,
      duration_ms: durationMs,
      artwork_url: a.artwork_url,
      has_lyrics: r() > 0.3,
      lyrics_status: 'fetched',
      ...f,
      file_size: ['completed', 'skipped_existing'].includes(stage) ? size : 0,
      attempt: 1,
      max_attempts: 3,
      created_at: iso(created + 500),
      updated_at: iso(updated),
    };
    if (['completed', 'skipped_existing'].includes(stage)) {
      item.output_path = `/app/data/downloads/albums/${a.artist_name}/${a.name}/${String(i + 1).padStart(2, '0')}. ${title}.m4a`;
    }
    if (stage === 'failed') {
      item.error = spec.itemError ? spec.itemError(i) : 'decrypt: wrapper closed the connection';
      item.attempt = 3;
    }
    if (spec.retrying && spec.retrying(i)) {
      item.attempt = 2;
      item.retry_kind = 'download';
    }
    items.push(item);
  }
  job.total_items = n;
  for (const it of items) {
    if (it.status === 'completed') job.completed_items++;
    else if (it.status === 'skipped_existing') job.skipped_items++;
    else if (it.status === 'failed') job.failed_items++;
    else if (it.status === 'cancelled') job.cancelled_items++;
  }
  world.jobs.set(id, job);
  world.items.set(id, items);
  seedEvents(world, job, items);
  return job;
}

/** The persisted event history a finished or running job would have. */
function seedEvents(world, job, items) {
  const add = (type, extra) => world.addEvent({ job_id: job.id, type, ...extra }, false);
  add('job_queued', { message: 'job queued', payload: { ...job, status: 'queued' }, created_at: job.created_at });
  if (job.status === 'queued') return;
  add('job_started', { message: 'job started', payload: { ...job, status: 'running' }, created_at: job.started_at });
  if (!job.title) return;
  add('resolved_input', { message: job.type, payload: { ...job, status: 'running' }, created_at: job.started_at });
  for (const it of items) {
    if (it.codec) add('codec_selected', { item_id: it.id, phase: it.codec, payload: { ...it }, created_at: it.updated_at });
    if (it.status === 'completed') add('item_completed', { item_id: it.id, message: 'saved', payload: { ...it }, created_at: it.updated_at });
    if (it.status === 'skipped_existing') add('item_skipped', { item_id: it.id, message: 'File already exists; skipped', payload: { ...it }, created_at: it.updated_at });
    if (it.status === 'failed') add('item_failed', { item_id: it.id, message: it.error, payload: { ...it }, created_at: it.updated_at });
    if (it.attempt > 1 && it.status !== 'failed') add('operation_retry', { item_id: it.id, phase: 'download', message: 'download attempt 1/3 failed: connection reset', payload: { ...it }, created_at: it.updated_at });
  }
  if (job.status === 'completed') add('job_finished', { message: 'job finished', payload: { ...job }, created_at: job.finished_at });
  if (job.status === 'failed') add('job_failed', { message: job.error || 'job failed', payload: { ...job }, created_at: job.finished_at });
  if (job.status === 'cancelled') add('job_cancelled', { message: 'job cancelled', payload: { ...job }, created_at: job.finished_at });
}

export function emptyWorld(now = Date.now()) {
  let nextEvent = 1000;
  let nextLog = 1;
  const world = {
    now,
    jobs: new Map(),
    items: new Map(),
    events: [],
    logs: [],
    config: structuredClone(BASE_CONFIG.config),
    wrapper: { ready: true, logged_in: true, regions: ['cn', 'us', 'jp'], accounts: [], login_supported: false, media_user_token: '' },
    hooks: { enabled: false, entries: [] },
    librarySync: { enabled: false, interval_minutes: 15, anchor_size: 0, last_submitted: 0, total_submitted: 0 },
    health: { status: 'ok' },
    simulate: false,
    storageFree: 161959772160,
    storageTotal: 491051491328,
    statsMode: 'rich',
    faults: {},
    addEvent(ev, live = true) {
      const e = { id: nextEvent++, item_id: undefined, phase: undefined, message: undefined, created_at: iso(Date.now()), ...ev };
      for (const k of Object.keys(e)) if (e[k] === undefined) delete e[k];
      world.events.push(e);
      if (live && world.onEvent) world.onEvent(e);
      return e;
    },
    addLog(entry) {
      const l = { sequence: nextLog++, time: iso(entry.at ?? Date.now()), level: entry.level || 'info', message: entry.message, attributes: entry.attributes || {} };
      world.logs.push(l);
      if (world.onLog) world.onLog(l);
      return l;
    },
    get cursor() {
      return nextEvent - 1;
    },
  };
  return world;
}

const LOG_LINES = [
  ['info', 'amdl backend listening', { addr: '[::]:18080', component: 'main' }],
  ['info', 'job started', { component: 'jobs', job_id: '%J' }],
  ['debug', 'codec selected', { component: 'media', codec: 'alac', bit_depth: 24, sample_rate: 48000, job_id: '%J' }],
  ['info', 'track saved', { component: 'jobs', job_id: '%J', path: '/app/data/downloads/albums/Yorushika/second person/01. Early morning, mailbox.m4a' }],
  ['warn', 'lyrics attempt 1/3 failed; retrying', { component: 'lyrics', job_id: '%J', error: 'wrapper /lyrics: context deadline exceeded' }],
  ['error', 'decrypt failed', { component: 'fairplay', job_id: '%J', error: 'wrapper closed the connection while a sample was in flight; the decrypt session is gone' }],
  ['info', 'GET /api/v1/downloads', { component: 'http', status: 200, duration_ms: 3, request_id: 'req_7f3a' }],
  ['debug', 'feed client connected', { component: 'http', remote: '192.168.3.20' }],
];

function seedLogs(world, n) {
  const ids = [...world.jobs.keys()];
  for (let i = 0; i < n; i++) {
    const [level, message, attrs] = LOG_LINES[i % LOG_LINES.length];
    const attributes = {};
    for (const [k, v] of Object.entries(attrs)) attributes[k] = v === '%J' ? ids[i % Math.max(1, ids.length)] || 'job_none' : v;
    world.addLog({ level, message, attributes, at: world.now - (n - i) * 7000 });
  }
}

const ERRORS = {
  decrypt: 'decrypt: wrapper closed the connection while a sample was in flight; the decrypt session is gone and 3 attempts were used',
  notFound: 'lookup album 1234567890 in cn: catalog request failed: 404 Not Found',
  storefront: 'unsupported_storefront: the wrapper account does not serve storefront "us"',
  json: '{"error":{"code":"upstream_error","message":"Apple answered 503 Service Unavailable"}}',
};

/** The world presets the scenarios name. */
export const WORLDS = {
  /** No jobs at all: first run. */
  empty(now) {
    const w = emptyWorld(now);
    w.statsMode = 'none';
    seedLogs(w, 3);
    return w;
  },
  /** A few finished albums, nothing running. */
  idle(now) {
    const w = emptyWorld(now);
    for (let i = 0; i < 9; i++) makeJob(w, { key: `idle${i}`, album: i * 7 + 3, status: i === 5 ? 'failed' : 'completed', createdAgo: (i + 1) * 5 * HOUR, error: i === 5 ? ERRORS.decrypt : undefined, stages: i === 5 ? (k) => (k < 3 ? 'completed' : 'failed') : undefined, motion: i === 0 });
    w.statsMode = 'rich';
    seedLogs(w, 40);
    return w;
  },
  /** The everyday busy state: running, queued, finished, one recent failure. */
  active(now) {
    const w = emptyWorld(now);
    buildActive(w);
    seedLogs(w, 120);
    return w;
  },
  /** 160 jobs over two weeks: pagination, long lists, many failures. */
  many(now) {
    const w = emptyWorld(now);
    buildActive(w);
    const statuses = ['completed', 'completed', 'completed', 'failed', 'completed', 'cancelled', 'completed'];
    for (let i = 0; i < 150; i++) {
      const st = statuses[i % statuses.length];
      makeJob(w, {
        key: `many${i}`,
        album: i,
        status: st,
        type: i % 11 === 0 ? 'song' : i % 13 === 0 ? 'playlist' : 'album',
        tracks: i % 11 === 0 ? 1 : undefined,
        createdAgo: 3 * HOUR + i * 2 * HOUR,
        error: st === 'failed' ? [ERRORS.decrypt, ERRORS.notFound, ERRORS.storefront, ERRORS.json][i % 4] : undefined,
        unresolved: st === 'failed' && i % 4 === 1,
        stages: st === 'failed' ? (k) => (k % 3 === 0 ? 'failed' : 'completed') : st === 'cancelled' ? (k) => (k < 2 ? 'completed' : 'cancelled') : undefined,
      });
    }
    seedLogs(w, 400);
    return w;
  },
};

/** Stable ids for the drawer scenarios. */
export const JOB_KEYS = {
  running22: 'running22',
  resolving: 'resolving',
  queued: 'queued1',
  done1: 'done1',
  done5: 'done5',
  done22: 'done22',
  done150: 'done150',
  failed: 'failed1',
  failed404: 'failed404',
  cancelled: 'cancelled1',
  skipped: 'skipped1',
  mixed: 'mixed1',
  long: 'long1',
};

export const jobId = (key) => hexId('job', key);

function buildActive(w) {
  makeJob(w, {
    key: JOB_KEYS.running22,
    album: 12,
    status: 'running',
    tracks: 22,
    createdAgo: 4 * MIN,
    stages: (i) => (i < 8 ? 'completed' : i === 8 ? 'downloading' : i === 9 ? 'decrypting' : i === 10 ? 'waiting_download' : i === 11 ? 'resolving' : 'queued'),
    fracs: (i) => (i === 8 ? 0.42 : 0.6),
    retrying: (i) => i === 8,
  });
  makeJob(w, { key: JOB_KEYS.resolving, album: 30, status: 'running', unresolved: true, createdAgo: 20000, input: 'https://music.apple.com/cn/album/1580904295' });
  makeJob(w, { key: JOB_KEYS.queued, album: 44, status: 'queued', tracks: 12, createdAgo: 10000 });
  makeJob(w, { key: 'queued2', album: 52, status: 'queued', tracks: 9, createdAgo: 9000 });
  makeJob(w, { key: JOB_KEYS.done1, album: 3, type: 'song', status: 'completed', tracks: 1, createdAgo: 30 * MIN });
  makeJob(w, { key: JOB_KEYS.done5, album: 9, status: 'completed', tracks: 5, createdAgo: 50 * MIN, motion: true });
  makeJob(w, { key: JOB_KEYS.done22, album: 15, status: 'completed', tracks: 22, createdAgo: 2 * HOUR });
  makeJob(w, { key: JOB_KEYS.done150, album: 21, type: 'playlist', status: 'completed', tracks: 150, createdAgo: 5 * HOUR, durationMs: 40 * MIN, trackArtist: (i) => album(i * 3).artist_name });
  makeJob(w, {
    key: JOB_KEYS.failed,
    album: 27,
    status: 'failed',
    tracks: 14,
    createdAgo: 3 * HOUR,
    error: ERRORS.decrypt,
    stages: (i) => (i < 9 ? 'completed' : i < 12 ? 'failed' : 'cancelled'),
    itemError: (i) => (i === 9 ? ERRORS.decrypt : 'download: unexpected EOF after 3 attempts'),
  });
  makeJob(w, { key: JOB_KEYS.failed404, album: 33, status: 'failed', unresolved: true, createdAgo: 6 * HOUR, error: ERRORS.notFound, input: 'https://music.apple.com/cn/album/1234567890' });
  makeJob(w, { key: JOB_KEYS.cancelled, album: 38, status: 'cancelled', tracks: 10, createdAgo: 8 * HOUR, stages: (i) => (i < 4 ? 'completed' : 'cancelled') });
  makeJob(w, { key: JOB_KEYS.skipped, album: 41, status: 'completed', tracks: 7, createdAgo: 10 * HOUR, stages: () => 'skipped_existing' });
  makeJob(w, {
    key: JOB_KEYS.mixed,
    album: 47,
    status: 'completed',
    tracks: 12,
    createdAgo: 20 * HOUR,
    codecs: (i) => (i === 0 ? 'alac-24-96' : i === 5 ? 'aac' : i === 7 ? 'alac-16-44' : 'alac-24-48'),
    stages: (i) => (i === 3 ? 'skipped_existing' : 'completed'),
  });
  makeJob(w, { key: JOB_KEYS.long, album: { ...album(60), name: 'Beethoven: Symphonies Nos. 1–9 · Overtures · Missa solemnis (Remastered 2026, Deluxe Edition with Rehearsal Excerpts)' }, status: 'completed', tracks: 9, createdAgo: 30 * HOUR, longTitles: true });
  seedHooks(w);
}

export { stageFields, CODECS, ERRORS, DAY, HOUR, MIN, iso };
