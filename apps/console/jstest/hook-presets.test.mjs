import test from 'node:test';
import assert from 'node:assert/strict';
import {
  presetGroups,
  presetValues,
  missingValue,
  entryPreset,
  webhookTarget,
  toggleEvent,
  toggleType,
  typesLabel,
  freeName,
  hookWrite,
  withEntry,
  withoutEntry,
} from '../static/js/lib/hook-presets.js';

// Two descriptors as GET /api/v1/hooks/presets answers them.
const NTFY = {
  id: 'ntfy',
  name: 'ntfy',
  kind: 'notify',
  events: ['job_finished', 'job_failed'],
  fields: [
    { key: 'server', label: 'Server', placeholder: 'https://ntfy.sh', default: 'https://ntfy.sh', secret: false, optional: false, multiline: false },
    { key: 'topic', label: 'Topic', placeholder: 'amdl-downloads', default: '', secret: false, optional: false, multiline: false },
    { key: 'token', label: 'Access token', placeholder: 'tk_…', default: '', secret: true, optional: true, multiline: false },
  ],
};
const CUSTOM = {
  id: 'custom',
  name: 'Custom',
  kind: 'custom',
  events: ['job_finished'],
  fields: [
    { key: 'url', label: 'Address', default: '' },
    { key: 'method', label: 'Method', choices: ['POST', 'PUT', 'GET'], default: 'POST' },
    { key: 'body', label: 'Body', optional: true, multiline: true, default: '' },
  ],
};
const PLEX = { id: 'plex', name: 'Plex', kind: 'library', events: ['job_finished'], fields: [] };

test('presets group by kind in the editor’s order', () => {
  assert.deepEqual(
    presetGroups([CUSTOM, PLEX, NTFY]).map((g) => [g.id, g.presets.map((p) => p.id)]),
    [
      ['notify', ['ntfy']],
      ['library', ['plex']],
      ['custom', ['custom']],
    ],
  );
  assert.deepEqual(presetGroups(undefined), []);
});

test('a preset’s fields start empty, a choice at its default', () => {
  assert.deepEqual(presetValues(NTFY), { server: '', topic: '', token: '' });
  assert.deepEqual(presetValues(CUSTOM), { url: '', method: 'POST', body: '' });
});

test('a required field without a default is named when empty', () => {
  assert.equal(missingValue(NTFY, { server: '', topic: '  ', token: '' }), 'Topic is needed');
  assert.equal(missingValue(NTFY, { server: '', topic: 't', token: '' }), '');
});

test('a webhook says what it calls, by the preset the server names', () => {
  const presets = [NTFY, PLEX, CUSTOM];
  assert.equal(entryPreset({}), 'custom');
  assert.equal(webhookTarget({ name: 'ntfy', origin: 'https://ntfy.sh', preset: 'ntfy' }, presets), 'ntfy.sh');
  assert.equal(webhookTarget({ name: 'nas', origin: 'http://nas:32400', preset: 'plex' }, presets), 'Plex · nas:32400');
  assert.equal(webhookTarget({ name: 'nas', origin: 'http://nas:32400' }, presets), 'nas:32400');
  assert.equal(webhookTarget({ name: 'nas', origin: 'http://nas:32400', preset: 'plex' }, undefined), 'nas:32400');
});

test('queued runs alone; the other events combine', () => {
  assert.deepEqual(toggleEvent(['job_finished'], 'job_failed'), ['job_finished', 'job_failed']);
  assert.deepEqual(toggleEvent(['job_finished', 'job_failed'], 'job_queued'), ['job_queued']);
  assert.deepEqual(toggleEvent(['job_queued'], 'job_cancelled'), ['job_cancelled']);
  assert.deepEqual(toggleEvent(['job_queued'], 'job_queued'), []);
  assert.deepEqual(toggleEvent(['job_failed', 'job_finished'], 'job_failed'), ['job_finished']);
});

test('all five job types are the same as all of them', () => {
  assert.deepEqual(toggleType([], 'album'), ['album']);
  assert.deepEqual(toggleType(['album'], 'song'), ['song', 'album']);
  assert.deepEqual(toggleType(['song', 'album', 'playlist', 'artist'], 'station'), []);
  assert.deepEqual(toggleType(['album'], 'all'), []);
  assert.equal(typesLabel([]), '');
  assert.equal(typesLabel(['playlist', 'album']), 'Albums, Playlists');
});

test('a new hook takes a free name', () => {
  const entries = [{ name: 'ntfy' }, { name: 'ntfy-2' }];
  assert.equal(freeName('ntfy', entries), 'ntfy-3');
  assert.equal(freeName('plex', entries), 'plex');
});

test('the sheet sends {preset, values} when adding or replacing, else events and types', () => {
  const entries = [{ name: 'ntfy' }];
  const draft = { name: 'ntfy-2', editing: '', replace: false, preset: NTFY, values: { server: '', topic: ' amdl ', token: '' }, events: ['job_failed', 'job_finished'], types: [] };
  assert.deepEqual(hookWrite(draft, entries), {
    name: 'ntfy-2',
    body: { events: ['job_finished', 'job_failed'], job_types: [], preset: 'ntfy', values: { server: '', topic: 'amdl', token: '' }, enabled: true },
  });
  assert.deepEqual(hookWrite({ ...draft, editing: 'ntfy', types: ['album'] }, entries), { name: 'ntfy', body: { events: ['job_finished', 'job_failed'], job_types: ['album'] } });
  assert.deepEqual(hookWrite({ ...draft, editing: 'ntfy', replace: true, preset: CUSTOM, values: { url: 'https://x.example', method: 'GET', body: ' {} ' } }, entries), {
    name: 'ntfy',
    body: { events: ['job_finished', 'job_failed'], job_types: [], preset: 'custom', values: { url: 'https://x.example', method: 'GET', body: ' {} ' } },
  });
  assert.deepEqual(hookWrite({ ...draft, name: 'ntfy' }, entries), { error: 'A hook is named ntfy' });
  assert.deepEqual(hookWrite({ ...draft, name: ' ' }, entries), { error: 'Name is needed' });
  assert.deepEqual(hookWrite({ ...draft, events: [] }, entries), { error: 'Pick when it runs' });
  assert.deepEqual(hookWrite({ ...draft, values: {} }, entries), { error: 'Topic is needed' });
  assert.deepEqual(hookWrite({ ...draft, preset: null }, entries), { error: 'Pick a service' });
});

test('a written entry replaces its namesake or is appended; a deleted one leaves', () => {
  const h = { enabled: true, entries: [{ name: 'a', enabled: true }, { name: 'b', enabled: true }] };
  assert.deepEqual(withEntry(h, { name: 'a', enabled: false }).entries.map((e) => e.enabled), [false, true]);
  assert.deepEqual(withEntry(h, { name: 'c' }).entries.map((e) => e.name), ['a', 'b', 'c']);
  assert.deepEqual(withoutEntry(h, 'a'), { enabled: true, entries: [{ name: 'b', enabled: true }] });
});
