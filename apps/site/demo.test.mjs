import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { describe, test } from 'node:test';
import { fileURLToPath } from 'node:url';

import { albumProgress } from '../console/static/js/lib/album-progress.js';
import { CONSOLE_STATIC, DEMO_CSP, SCENES, buildDemo, demoIndex } from './demo/bundle.mjs';
import { albums, artists, playlists, songs } from './demo/catalog.js';
import { createDemoFetch, createFakeApi } from './demo/fake-api.js';

const here = path.dirname(fileURLToPath(import.meta.url));
const origin = 'https://amdl.example.org/demo/console/';

// Timers that never fire, so a stream's keepalive does not hold the test open.
const timers = { setInterval: () => 0, clearInterval: () => {} };
const fresh = () => createFakeApi({ progress: albumProgress, timers });
const call = (api, method, url, body) => api.handle({ method, url: new URL(url, origin).href, body: body === undefined ? undefined : JSON.stringify(body) });
const settle = (api) => {
  for (let i = 0; i < 2000 && [...api.jobs.values()].some((j) => j.status === 'running' || j.status === 'queued'); i++) api.tick();
};

/** Reads an SSE response until `enough(events)` or the body ends; returns the events. */
async function readEvents(res, enough = () => false) {
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  const events = [];
  let text = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) return events;
    text += decoder.decode(value, { stream: true });
    let cut;
    while ((cut = text.indexOf('\n\n')) >= 0) {
      const block = text.slice(0, cut);
      text = text.slice(cut + 2);
      const event = /^event: (.*)$/m.exec(block)?.[1];
      if (event) events.push({ event, data: JSON.parse(/^data: (.*)$/m.exec(block)[1]) });
    }
    if (enough(events)) break;
  }
  await reader.cancel();
  return events;
}

describe('fake API', () => {
  test('answers every call the console makes', async () => {
    // Every /api/v1 path in the console's client, with the method it is called with.
    const src = fs.readFileSync(path.join(CONSOLE_STATIC, 'js', 'api.js'), 'utf8');
    const calls = [...src.matchAll(/request\('(\w+)',\s*[`'](\/api\/v1\/[^`']*)[`']/g)].map((m) => [m[1], m[2]]);
    calls.push(...[...src.matchAll(/resolveUrl\([`'](\/api\/v1\/[^`']*)[`']/g)].map((m) => ['GET', m[1]]));
    assert.ok(calls.length >= 25, `found only ${calls.length} API calls in api.js`);

    const api = fresh();
    const done = [...api.jobs.values()].find((j) => j.status === 'completed');
    const ids = { downloads: done.id, artists: artists[0].id, login: 'login_1' };
    const bodies = {
      'POST /api/v1/downloads': { urls: [albums[3].url] },
      'POST /api/v1/lookup': { url: albums[0].url },
      'POST /api/v1/quality': { url: albums[0].url },
      'PUT /api/v1/config': { logging: { level: 'debug' } },
    };
    for (const [method, raw] of calls) {
      const route = raw.replace(/\/(\w+)\/\$\{[^}]+\}/g, (_, seg) => `/${seg}/${ids[seg]}`);
      assert.doesNotMatch(route, /\$\{|undefined/, `${raw}: no id for this path`);
      const ctl = new AbortController();
      const res = await api.handle({ method, url: new URL(route.slice(1), origin.replace('console/', '')).href, body: JSON.stringify(bodies[`${method} ${raw}`] ?? {}), signal: ctl.signal });
      ctl.abort();
      const label = `${method} ${route}`;
      assert.ok([200, 202, 204, 409, 501].includes(res.status), `${label}: HTTP ${res.status}`);
      if (res.status === 501) assert.match(raw, /\/wrapper\/login|\/wrapper\/logout/, `${label} answered 501`);
    }
  });

  test('running and queued jobs finish, and the feed reports each one', async () => {
    const api = fresh();
    const open = [...api.jobs.values()].filter((j) => j.status === 'running' || j.status === 'queued').map((j) => j.id);
    assert.ok(open.length >= 4);
    const res = await call(api, 'GET', '../api/v1/downloads/events');
    // The stream keeps what the ticks send until it is read.
    settle(api);
    const done = (events) => open.every((id) => events.some((e) => e.event === 'download_upserted' && e.data.job.id === id && e.data.job.status === 'completed'));
    const events = await readEvents(res, done);
    const finished = new Set(events.filter((e) => e.event === 'download_upserted' && e.data.job.status === 'completed').map((e) => e.data.job.id));
    for (const id of open) {
      assert.equal(api.jobs.get(id).status, 'completed', id);
      assert.ok(finished.has(id), `the feed never said ${id} finished`);
      for (const it of api.items.get(id)) assert.equal(it.status, 'completed');
    }
  });

  test('a link submitted from the console becomes a job that runs', async () => {
    const api = fresh();
    const album = albums.find((a) => a.name === 'Every Bridge Is a Promise');
    const res = await call(api, 'POST', '../api/v1/downloads', { urls: [album.url, 'https://example.org/not-apple'] });
    assert.equal(res.status, 202);
    const body = await res.json();
    assert.equal(body.accepted, 1);
    assert.equal(body.results[1].status, 'invalid');
    const job = api.jobs.get(body.results[0].job.id);
    assert.equal(job.title, album.name);
    settle(api);
    assert.equal(job.status, 'completed');
    assert.equal(job.completed_items, album.track_count);
    assert.equal(api.items.get(job.id)[0].sample_rate, 96000);
  });

  test('retry runs a failed job again', async () => {
    const api = fresh();
    const failed = [...api.jobs.values()].find((j) => j.status === 'failed');
    assert.equal((await call(api, 'POST', `../api/v1/downloads/${failed.id}/retry`)).status, 202);
    settle(api);
    assert.equal(failed.status, 'completed');
    assert.equal(failed.failed_items, 0);
  });

  test("a finished job's stream replays its history, then answers 204", async () => {
    const api = fresh();
    const done = [...api.jobs.values()].find((j) => j.status === 'completed');
    const events = await readEvents(await call(api, 'GET', `../api/v1/downloads/${done.id}/events`));
    assert.equal(events.at(-1).event, 'job_finished');
    const last = events.at(-1).data.id;
    assert.equal((await call(api, 'GET', `../api/v1/downloads/${done.id}/events?cursor=${last}`)).status, 204);
  });

  test('search finds by name and artist, and answers something for any term', async () => {
    const api = fresh();
    const hit = await (await call(api, 'GET', '../api/v1/search?term=moth')).json();
    assert.deepEqual(
      hit.songs.items.map((s) => s.name),
      ['Moth Light'],
    );
    const byArtist = await (await call(api, 'GET', '../api/v1/search?term=hollow%20pines&types=albums')).json();
    assert.equal(byArtist.songs, undefined);
    assert.ok(byArtist.albums.items.length >= 2);
    const any = await (await call(api, 'GET', '../api/v1/search?term=zzzz')).json();
    assert.ok(any.songs.items.length > 0 && any.albums.items.length > 0);
  });
});

describe('pinned views', () => {
  // A route whose job or search the demo lacks would show an empty drawer or
  // a page of no results where the site means to show the console at work.
  test("each view's job is in the demo's world, finished, and its search finds songs", async () => {
    const api = fresh();
    for (const [name, v] of Object.entries(SCENES)) {
      const job = /^#\/\w+\/(job_[0-9a-f]+)$/.exec(v.route)?.[1];
      const term = new URLSearchParams(v.route.split('?')[1] ?? '').get('q');
      assert.ok(job || term || v.route === '#/', `${name}: ${v.route}`);
      if (job) {
        const res = await call(api, 'GET', `../api/v1/downloads/${job}`);
        assert.equal(res.status, 200, name);
        assert.equal((await res.json()).job.status, 'completed', name);
      }
      if (term) {
        const res = await call(api, 'GET', `../api/v1/search?term=${encodeURIComponent(term)}`);
        assert.ok((await res.json()).songs.items.length >= 4, name);
      }
    }
  });
});

describe('demo fetch', () => {
  const api = fresh();
  const seen = [];
  const demoFetch = createDemoFetch({ api, origin, fetch: async (input) => (seen.push(String(input)), new Response('file')) });

  test('another host fails like a network error, without a request', async () => {
    for (const url of ['https://mvod.itunes.apple.com/x.m3u8', 'https://music.apple.com/us/album/1', 'http://amdl.example.org/demo/console/']) {
      await assert.rejects(demoFetch(url), TypeError, url);
    }
    assert.deepEqual(seen, []);
  });

  test('the console files load as usual and /api/v1 goes to the fake', async () => {
    assert.equal(await (await demoFetch('icons.svg')).text(), 'file');
    assert.deepEqual(seen, ['icons.svg']);
    const res = await demoFetch(new URL('../api/v1/health', origin).href, { method: 'GET' });
    assert.deepEqual(await res.json(), { status: 'ok' });
    assert.equal(seen.length, 1);
  });
});

describe('made-up catalog', () => {
  test('every cover is a drawn SVG and every id is distinct', () => {
    const all = [...albums, ...songs, ...artists, ...playlists];
    for (const x of all) assert.match(x.artwork_url, /^data:image\/svg\+xml,/, x.name);
    assert.equal(new Set(all.map((x) => x.id)).size, all.length);
  });

  test('the demo world names no other host and has no motion artwork', async () => {
    for (const file of ['catalog.js', 'fake-api.js', 'boot.js']) {
      assert.doesNotMatch(fs.readFileSync(path.join(here, 'demo', file), 'utf8'), /mzstatic|itunes\.apple\.com/, file);
    }
    const body = await (await call(fresh(), 'GET', '../api/v1/downloads?limit=200')).text();
    assert.doesNotMatch(body, /motion_artwork/);
  });
});

describe('built demo', () => {
  test('the console runs boot.js first, under a policy that allows only its own files', () => {
    const out = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-demo-'));
    try {
      buildDemo(out);
      const dir = path.join(out, 'demo', 'console');
      const html = fs.readFileSync(path.join(dir, 'index.html'), 'utf8');
      assert.ok(html.includes(`content="${DEMO_CSP}"`));
      assert.ok(html.indexOf('src="demo/boot.js"') < html.indexOf('src="js/main.js"'));
      assert.doesNotMatch(DEMO_CSP, /https?:/);
      // boot.js imports the console's own module from beside it.
      for (const m of fs.readFileSync(path.join(dir, 'demo', 'boot.js'), 'utf8').matchAll(/from '(\.[^']+)'/g)) {
        assert.ok(fs.existsSync(path.join(dir, 'demo', m[1])), m[1]);
      }
      assert.equal(fs.existsSync(path.join(out, 'demo', 'desktop')), false);
    } finally {
      fs.rmSync(out, { recursive: true, force: true });
    }
  });

  test('a console index.html that moved a mark fails the build', () => {
    assert.throws(() => demoIndex('<!doctype html><title>AMDL Console</title>'), /no longer has exactly one/);
  });
});
