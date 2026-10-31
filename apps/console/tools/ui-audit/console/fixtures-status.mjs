// The status answers of the mock world: GET /stats and GET /system.

import { DAY, iso, rng } from './fixtures.mjs';

/** GET /stats: rich (14 days of data), sparse (under three days) or none. */
export function stats(world, days = 14) {
  const daily = [];
  const r = rng(7);
  let tracks = 0;
  let bytes = 0;
  for (let d = days - 1; d >= 0; d--) {
    const date = new Date(world.now - d * DAY).toISOString().slice(0, 10);
    let t = 0;
    if (world.statsMode === 'rich') t = Math.floor(r() * 60) * (r() > 0.25 ? 1 : 0);
    if (world.statsMode === 'sparse') t = d < 2 ? 12 + d * 5 : 0;
    const b = t * 42000000;
    tracks += t;
    bytes += b;
    daily.push({ date, tracks_completed: t, bytes_written: b, jobs_completed: Math.ceil(t / 11), jobs_failed: t && r() > 0.8 ? 1 : 0 });
  }
  const out = {
    generated_at: iso(world.now),
    days,
    totals: { jobs: daily.reduce((s, x) => s + x.jobs_completed, 0), tracks_completed: tracks, tracks_skipped: 5, tracks_failed: 3, bytes_written: bytes, duration_ms: tracks * 210000 },
    daily,
  };
  if (tracks) out.codecs = [
    { codec: 'alac', bit_depth: 24, sample_rate: 48000, tracks: Math.round(tracks * 0.55) },
    { codec: 'alac', bit_depth: 16, sample_rate: 44100, tracks: Math.round(tracks * 0.25) },
    { codec: 'alac', bit_depth: 24, sample_rate: 96000, tracks: Math.round(tracks * 0.12) },
    { codec: 'ec3', bit_depth: 0, sample_rate: 48000, tracks: Math.round(tracks * 0.08) },
  ];
  return out;
}

/** GET /system. */
export function system(world) {
  const active = [...world.jobs.values()].filter((j) => j.status === 'running').length;
  const queued = [...world.jobs.values()].filter((j) => j.status === 'queued').length;
  return {
    version: '2.0-dev',
    commit: 'cdd142eba7c3fa63d18c7c61e2eede08067cd542',
    build_date: '2026-10-30T05:56:33Z',
    go_version: 'go1.27.3',
    os: 'linux',
    arch: 'amd64',
    started_at: iso(world.now - 5789000),
    uptime_seconds: 5789,
    simulate: world.simulate,
    motion_artwork_enabled: true,
    default_storefront: 'cn',
    jobs: { workers: 3, active, queue_depth: queued, queue_capacity: 256, recovery_backlog: 0 },
    pools: { downloads: 16, decrypts: 4, wrapper_requests: 24 },
    tools: [{ name: 'ffmpeg', available: true, path: '/usr/bin/ffmpeg' }],
    storage: [
      { name: 'downloads_dir', path: '/app/data/downloads', free_bytes: world.storageFree, total_bytes: world.storageTotal },
      { name: 'temp_dir', path: '/app/data/tmp', free_bytes: world.storageFree, total_bytes: world.storageTotal },
    ],
    startup: {
      wrapper_address: 'http://wrapper:12340',
      listen: '0.0.0.0:18080',
      database_path: 'data/db/amdl.db',
      log_buffer_size: 2000,
      catalog_language: 'zh-Hans-CN',
      max_parallel_requests: 16,
    },
  };
}
