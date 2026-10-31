import test from 'node:test';
import assert from 'node:assert/strict';
import {
  RATE_WINDOW_MS,
  transferred,
  avgSpeed,
  createRate,
  addSample,
  remainingMs,
  formatRate,
  formatRemaining,
  speedWords,
} from '../static/js/lib/speed.js';

const T0 = Date.parse('2026-11-01T02:00:00Z');
const item = (status, extra = {}) => ({ status, file_size: 0, progress: { download_bytes: 0 }, ...extra });

test('transferred adds the files written to what unfinished tracks have downloaded', () => {
  const items = [
    item('completed', { file_size: 50e6, progress: { download_bytes: 48e6 } }),
    item('downloading', { progress: { download_bytes: 12e6 } }),
    item('decrypting', { progress: { download_bytes: 30e6 } }),
    item('skipped_existing', { file_size: 99e6 }),
    item('queued'),
    item('failed', { progress: { download_bytes: 5e6 } }),
  ];
  // A finished track counts its file once, not its download bytes too; a skipped one moved nothing.
  assert.equal(transferred(items), 50e6 + 12e6 + 30e6 + 5e6);
  assert.equal(transferred([]), 0);
  assert.equal(transferred(null), 0);
  assert.equal(transferred([{ status: 'downloading' }, null]), 0);
});

test('the average is transferred over the run, up to now or the finish', () => {
  const items = [item('completed', { file_size: 100e6 }), item('downloading', { progress: { download_bytes: 20e6 } })];
  const job = { started_at: new Date(T0).toISOString() };
  assert.equal(avgSpeed(job, items, T0 + 10000), 12e6);
  const done = { ...job, finished_at: new Date(T0 + 60000).toISOString() };
  assert.equal(avgSpeed(done, items, T0 + 999999), 2e6);
  // Unknown: no start, under a second in, or a start in the future.
  assert.equal(avgSpeed({}, items, T0 + 10000), 0);
  assert.equal(avgSpeed(job, items, T0 + 999), 0);
  assert.equal(avgSpeed(job, items, T0 - 5000), 0);
  assert.equal(avgSpeed(null, items, T0), 0);
  assert.ok(avgSpeed(job, items, T0 + 1000) > 0);
});

const feed = (points) => {
  const r = createRate();
  for (const [s, p] of points) addSample(r, T0 + s * 1000, p);
  return r;
};

test('time left follows the pace of the samples', () => {
  // 0.1 in 10 s, 0.8 to go: 80 s.
  assert.equal(Math.round(remainingMs(feed([[0, 0.1], [10, 0.2]])) / 1000), 80);
  // Only the window counts: slow at first, then 0.05 a second.
  const r = feed([[0, 0], [5, 0.01], [10, 0.02], [40, 0.1], [50, 0.6]]);
  assert.ok(remainingMs(r) > 0 && remainingMs(r) < 30000);
});

test('time left is none without enough to go on', () => {
  assert.equal(remainingMs(createRate()), 0);
  assert.equal(remainingMs(feed([[0, 0.1]])), 0, 'one sample');
  assert.equal(remainingMs(feed([[0, 0.1], [2.9, 0.5]])), 0, 'span under 3 s');
  assert.ok(remainingMs(feed([[0, 0.1], [3, 0.5]])) > 0, 'span of 3 s');
  assert.equal(remainingMs(feed([[0, 0.3], [20, 0.3]])), 0, 'no progress');
  assert.equal(remainingMs(feed([[0, 0.5], [20, 1]])), 0, 'complete');
});

test('progress going backwards starts the samples over', () => {
  const r = feed([[0, 0.5], [10, 0.6], [20, 0.1]]);
  assert.equal(r.samples.length, 1);
  assert.equal(remainingMs(r), 0);
  addSample(r, T0 + 30000, 0.2);
  assert.equal(Math.round(remainingMs(r) / 1000), 80);
});

test('samples older than the window go, the last one outside it stays as the base', () => {
  const r = createRate();
  for (let s = 0; s <= 100; s += 10) addSample(r, T0 + s * 1000, s / 200);
  const first = r.samples[0];
  assert.ok(r.samples[r.samples.length - 1].at - first.at >= RATE_WINDOW_MS);
  assert.ok(r.samples[r.samples.length - 1].at - r.samples[1].at <= RATE_WINDOW_MS);
  // Constant pace, so the base does not change the answer: 0.5 to go at 0.05 / 10 s.
  assert.equal(Math.round(remainingMs(r) / 1000), 100);
});

test('samples a few milliseconds apart are one', () => {
  const r = feed([[0, 0.1], [0.1, 0.11], [0.2, 0.12]]);
  assert.equal(r.samples.length, 1);
  assert.equal(r.samples[0].progress, 0.12);
});

test('rates are decimal with one decimal until 100', () => {
  assert.equal(formatRate(41e6), '41 MB/s');
  assert.equal(formatRate(41.4e6), '41.4 MB/s');
  assert.equal(formatRate(1.5e6), '1.5 MB/s');
  assert.equal(formatRate(120e6), '120 MB/s');
  assert.equal(formatRate(999), '999 B/s');
  assert.equal(formatRate(1000), '1 KB/s');
  assert.equal(formatRate(2.25e9), '2.3 GB/s');
  assert.equal(formatRate(0), '');
  assert.equal(formatRate(NaN), '');
});

test('time left reads as seconds, minutes, then hours', () => {
  assert.equal(formatRemaining(58000), '~58 s');
  assert.equal(formatRemaining(58400), '~58 s');
  assert.equal(formatRemaining(4 * 60000), '~4 min');
  assert.equal(formatRemaining(3.6 * 60000), '~4 min');
  assert.equal(formatRemaining(5400000), '~1.5 h');
  assert.equal(formatRemaining(2 * 3600000), '~2 h');
  assert.equal(formatRemaining(0), '');
});

test('the words say what is known', () => {
  assert.deepEqual(speedWords(41e6, 58000), { rate: '41 MB/s', time: '~58 s', avg: 'avg 41 MB/s', left: '~58 s left' });
  assert.deepEqual(speedWords(0, 0), { rate: '', time: '', avg: '', left: '' });
  assert.equal(speedWords(41e6, 0).left, '');
  assert.equal(speedWords(0, 58000).avg, '');
});
