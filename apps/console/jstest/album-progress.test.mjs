import test from 'node:test';
import assert from 'node:assert/strict';
import { albumProgress, itemFraction, TICK_MAX } from '../static/js/lib/album-progress.js';
import { STAGE_WEIGHTS as W } from '../static/js/lib/status.js';

const at = (s) => `2026-10-30T10:00:${String(s).padStart(2, '0')}Z`;
const item = (index, status, extra = {}) => ({ index, status, progress: {}, updated_at: at(0), ...extra });
const near = (a, b) => assert.ok(Math.abs(a - b) < 1e-9, `${a} != ${b}`);

test('itemFraction weights the stages and counts a finished track whole', () => {
  assert.equal(itemFraction(item(1, 'completed')), 1);
  assert.equal(itemFraction(item(1, 'skipped_existing')), 1);
  assert.equal(itemFraction(item(1, 'queued')), 0);
  near(itemFraction(item(1, 'downloading', { progress: { resolved: true, download: 0.5 } })), W.resolve + W.download * 0.5);
  near(itemFraction(item(1, 'tagging', { progress: { resolved: true, download: 1, decrypt: 1, remuxed: true } })), W.resolve + W.download + W.decrypt + W.remux);
  // Saved ends the pipeline even when integrity checks are off (verified false).
  assert.equal(itemFraction(item(1, 'saving', { progress: { resolved: true, download: 1, decrypt: 1, remuxed: true, tagged: true, saved: true } })), 1);
  // A failed track stays where it stopped.
  near(itemFraction(item(1, 'failed', { progress: { resolved: true, download: 0.25 } })), W.resolve + W.download * 0.25);
});

test('the overall percent is the stage-weighted mean, below 100 while a track works', () => {
  const half = { resolved: true, download: 1, decrypt: 0 };
  const p = albumProgress('running', [item(1, 'completed'), item(2, 'downloading', { progress: half })]);
  assert.equal(p.percent, Math.round(((1 + W.resolve + W.download) / 2) * 100));
  const many = Array.from({ length: 200 }, (_, i) => item(i + 1, i ? 'completed' : 'tagging', { progress: { resolved: true, download: 1, decrypt: 1, remuxed: true, verified: true, tagged: true } }));
  assert.equal(albumProgress('running', many).percent, 99);
  assert.equal(albumProgress('completed', [item(1, 'completed'), item(2, 'skipped_existing')]).percent, 100);
  assert.equal(albumProgress('running', []).percent, 0);
});

test('segments light left to right in finish order, whichever track finished', () => {
  const items = [
    item(1, 'downloading'),
    item(2, 'completed', { updated_at: at(30) }),
    item(3, 'failed', { updated_at: at(10) }),
    item(4, 'completed', { updated_at: at(20) }),
    item(5, 'skipped_existing', { updated_at: at(40) }),
  ];
  const p = albumProgress('running', items);
  assert.equal(p.lit, 4);
  assert.deepEqual(
    p.runs.map((r) => [r.state, Math.round(r.share * 5)]),
    [['failed', 1], ['completed', 2], ['skipped_existing', 1]],
  );
  near(p.done, 4 / 5);
  assert.ok(p.live >= p.done);
});

test('without finish times the order is completed, skipped, failed, cancelled', () => {
  const items = [item(1, 'cancelled'), item(2, 'failed'), item(3, 'skipped_existing'), item(4, 'completed'), item(5, 'completed')].map((it) => ({ ...it, updated_at: undefined }));
  assert.deepEqual(
    albumProgress('cancelled', items).runs.map((r) => r.role),
    ['success', 'skipped', 'danger', 'neutral'],
  );
});

test('the translucent fill never sits behind the solid one', () => {
  // A failed track counts in the solid fill but keeps its partial fraction in the mean.
  const p = albumProgress('running', [item(1, 'failed'), item(2, 'queued')]);
  near(p.done, 0.5);
  assert.equal(p.live, 0.5);
});

test('the bar is cut into cells up to TICK_MAX tracks, then drawn whole', () => {
  const n = (k) => Array.from({ length: k }, (_, i) => item(i + 1, 'queued'));
  assert.equal(albumProgress('running', n(2)).ticks, 2);
  assert.equal(albumProgress('running', n(TICK_MAX)).ticks, TICK_MAX);
  assert.equal(albumProgress('running', n(TICK_MAX + 1)).ticks, 0);
  assert.equal(albumProgress('running', n(1)).ticks, 0);
});

test('a single track is not segmented: the solid fill is its own progress', () => {
  const p = albumProgress('running', [item(1, 'downloading', { progress: { resolved: true, download: 0.5 } })]);
  assert.equal(p.runs.length, 1);
  assert.equal(p.runs[0].role, 'info');
  near(p.done, W.resolve + W.download * 0.5);
  assert.equal(p.live, p.done);
  assert.deepEqual(albumProgress('failed', [item(1, 'failed')]).runs.map((r) => [r.role, r.share]), [['danger', 1]]);
});

test('albumProgress counts the tracks on disk for a job that ended short', () => {
  const items = [item(1, 'completed'), item(2, 'skipped_existing'), item(3, 'failed', { progress: { resolved: true } }), item(4, 'cancelled')];
  const p = albumProgress('failed', items);
  assert.equal(p.lit, 4);
  assert.equal(p.onDisk, 2);
  assert.equal(albumProgress('running', [item(1, 'completed'), item(2, 'queued')]).onDisk, 1);
});

test('the fraction is the overall progress before it is rounded to a percent', () => {
  const p = albumProgress('running', [item(1, 'completed'), item(2, 'downloading', { progress: { resolved: true, download: 0.5 } })]);
  near(p.fraction, (1 + W.resolve + W.download * 0.5) / 2);
  assert.equal(p.percent, Math.round(p.fraction * 100));
  assert.equal(albumProgress('completed', [item(1, 'completed')]).fraction, 1);
  assert.equal(albumProgress('running', []).fraction, 0);
});
