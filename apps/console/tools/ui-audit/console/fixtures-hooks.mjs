// The mock world's hooks.yaml: webhooks made by the editor's presets (one
// switched off, one whose host does not resolve, so its test fails), a
// command hook the API cannot write, and their runs for GET /stats.

import { iso, DAY, HOUR } from './fixtures.mjs';

const ENTRIES = [
  {
    name: 'ntfy',
    type: 'webhook',
    enabled: true,
    events: ['job_finished', 'job_failed'],
    job_types: [],
    method: 'POST',
    url: 'https://ntfy.sh/amdl-downloads',
    headers: { Title: 'AMDL', Tags: 'musical_note', 'Content-Type': 'text/plain; charset=utf-8' },
    preset: 'ntfy',
  },
  {
    name: 'plex',
    type: 'webhook',
    enabled: false,
    events: ['job_finished'],
    job_types: ['album', 'playlist'],
    method: 'GET',
    url: 'http://plex.local:32400/library/sections/all/refresh',
    headers: { 'X-Plex-Token': 'token' },
    preset: 'plex',
    body: '',
  },
  {
    name: 'living-room',
    type: 'webhook',
    enabled: true,
    events: ['job_finished'],
    job_types: [],
    method: 'POST',
    url: 'http://media.invalid:8096/Library/Refresh',
    headers: { Authorization: 'MediaBrowser Token="key"' },
    preset: 'jellyfin',
    body: '',
  },
  { name: 'beets-import', type: 'exec', enabled: true, events: ['job_finished'], job_types: ['album'] },
];

/** Gives `w` the hooks above, on, with their runs over the stats window. */
export function seedHooks(w) {
  w.hooks = { enabled: true, entries: structuredClone(ENTRIES) };
  w.hookStats = [
    { name: 'ntfy', succeeded: 41, failed: 1, last_failed_at: iso(w.now - 3 * DAY - 2 * HOUR) },
    { name: 'beets-import', succeeded: 17, failed: 0 },
  ];
}
