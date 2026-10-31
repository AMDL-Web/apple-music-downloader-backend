// The amdl console as an audit subject: its mock backend, every page and
// state worth looking at, how to reach each, and the console's selectors.
// Scenario ids are `<page>/<state>`; `--only queue` runs one page.

import { readdirSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { startMockServer } from './mock-server.mjs';
import { jobId, JOB_KEYS, CATALOG, LOSSY_ALBUM } from './fixtures.mjs';

const root = fileURLToPath(new URL('../../../../../', import.meta.url));
const css = join(root, 'apps/console/static/css');
const cssFiles = [...readdirSync(css).filter((f) => f.endsWith('.css')).map((f) => join(css, f)), join(css, 'skins/music.css')];

const LATENCY = 25;

const albumUrl = (i) => CATALOG.albums[i].url;
const NEW_LINKS = [
  albumUrl(5),
  albumUrl(12),
  `${CATALOG.songs[3].url}`,
  'https://music.apple.com/cn/playlist/todays-hits/pl.f4d106fed2bd41149aaacabb233eb5eb',
  'https://music.apple.com/cn/artist/yorushika/1250709916',
  'https://music.apple.com/cn/album/1234567890',
  'https://music.apple.com/cn/music-video/x/1500000000',
  'https://example.com/not-apple',
];

// A wrapper amdl runs in a QEMU guest (WrapperStatus.managed) in `mode`; the
// guest answers no status unless it serves.
const MANAGED_ERROR = 'guest did not reach its console within 2m0s: qemu-system-x86_64 exited: exit status 1';
const managed = (mode, over = {}) => (w) =>
  Object.assign(w.wrapper, {
    ready: mode === 'serving',
    status: mode === 'serving',
    regions: mode === 'serving' ? ['cn'] : [],
    accounts: [],
    login_supported: true,
    managed: { mode, accel: 'tcg', accel_fallback: false, qemu: '/usr/bin/qemu-system-x86_64', guest: 'import-d2c150c07e20', login_residue: false, ...(mode === 'failed' ? { last_error: MANAGED_ERROR } : {}), ...over },
  });
// Reaches the two-step code step of the login form.
const toCodeStep = async (ctx) => {
  await ctx.page.locator('#login-user').fill('someone@example.com');
  await ctx.page.locator('#login-pass').fill('not-a-real-password');
  await ctx.page.locator('.login-form .btn-primary').click();
  await ctx.page.locator('#login-code').waitFor();
};

const drawer = (key, extra = {}) => ({ path: `#/queue/${jobId(key)}`, world: 'active', require: '.drawer', ...extra });

/** Opens the palette from anywhere. */
const palette = async (ctx) => {
  await ctx.page.keyboard.press('Control+k');
  await ctx.page.waitForSelector('[role=dialog] [role=combobox], .palette input', { timeout: 3000 });
};

const live = (n, gap = 250) => async (ctx) => {
  for (let i = 0; i < n; i++) {
    ctx.control.tick();
    await ctx.wait(gap);
  }
};

const nav = (hash) => ({ id: `to-${hash.replace(/[^a-z]+/gi, '-').replace(/^-|-$/g, '') || 'overview'}`, title: `navigate to ${hash}`, expectScroll: 0, run: (ctx) => ctx.hash(hash) });

const SCENARIOS = [
  // ---- Overview
  { id: 'overview/empty', world: 'empty', path: '#/' },
  {
    id: 'overview/idle',
    world: 'idle',
    path: '#/',
    transitions: [
      {
        id: 'idle-to-active',
        title: 'a submitted job appears and starts',
        run: async (ctx) => {
          await ctx.control.submit([albumUrl(20)]);
          await ctx.wait(400);
          await live(4)(ctx);
        },
      },
    ],
  },
  {
    id: 'overview/active',
    world: 'active',
    path: '#/',
    transitions: [
      { id: 'live-progress', title: 'live progress for 3 s', run: live(12) },
      { id: 'job-finishes', title: 'a job finishes', run: live(30, 120), hold: 300 },
      nav('#/queue'),
      { id: 'theme-toggle', title: 'theme toggle', expectThemeChange: true, ignoreFocus: true, run: (ctx) => ctx.page.locator('.theme button, button.theme-cycle').first().click() },
    ],
  },
  { id: 'overview/many', world: 'many', path: '#/' },
  { id: 'overview/slow', world: 'active', path: '#/', latency: 700 },
  { id: 'overview/offline', world: 'active', path: '#/', faults: { 'GET /api/v1/downloads': { status: 503, code: 'unavailable' }, 'GET /api/v1/downloads/events': { status: 503 }, 'GET /api/v1/stats': { status: 500 }, 'GET /api/v1/health': { status: 503 } } },
  { id: 'overview/degraded', world: 'active', path: '#/', mutate: (w) => Object.assign(w.wrapper, { ready: false, logged_in: false, regions: [] }) && (w.storageFree = 2.1e9) },
  { id: 'overview/wrapper-failed', world: 'active', path: '#/', mutate: managed('failed') },
  { id: 'boot/theme-stored-dark', world: 'active', path: '#/', themes: ['light'], storage: { 'amdl.console.theme': 'dark' }, noStates: true },

  // ---- Queue
  ...['active', 'completed', 'failed', 'cancelled', 'all'].map((tab) => ({ id: `queue/${tab}`, world: 'many', path: `#/queue?tab=${tab}` })),
  { id: 'queue/empty', world: 'empty', path: '#/queue?tab=active' },
  { id: 'queue/empty-failed', world: 'idle', path: '#/queue?tab=cancelled' },
  { id: 'queue/search', world: 'many', path: '#/queue?tab=all&q=yoru' },
  { id: 'queue/search-none', world: 'many', path: '#/queue?tab=all&q=zzzzzz' },
  { id: 'queue/type-song', world: 'many', path: '#/queue?tab=all&type=song' },
  {
    id: 'queue/selecting',
    world: 'many',
    path: '#/queue?tab=failed',
    setup: async (ctx) => {
      await ctx.page.locator('.queue-select').first().click();
      await ctx.page.locator('.job-check-input').nth(1).check({ force: true });
      await ctx.page.locator('.job-check-input').nth(2).check({ force: true });
    },
  },
  {
    id: 'queue/switching',
    world: 'many',
    path: '#/queue?tab=active',
    noStates: true,
    transitions: [
      { id: 'tab-completed', title: 'tab Active → Completed', expectContent: true, run: (ctx) => ctx.hash('#/queue?tab=completed') },
      { id: 'tab-failed', title: 'tab Completed → Failed', expectContent: true, run: (ctx) => ctx.hash('#/queue?tab=failed') },
      { id: 'type-filter', title: 'type filter Song', expectContent: true, run: (ctx) => ctx.hash('#/queue?tab=failed&type=song') },
      {
        id: 'search-typing',
        title: 'typing a search',
        expectContent: true,
        run: async (ctx) => {
          await ctx.hash('#/queue?tab=all');
          await ctx.settle();
          await ctx.page.locator('.page-queue input[type=search]').first().pressSequentially('radio', { delay: 90 });
        },
      },
      { id: 'load-more', title: 'Load more', expectContent: true, run: (ctx) => ctx.page.locator('button:has-text("Load more")').first().click({ timeout: 3000 }).catch(() => {}) },
      { id: 'open-drawer', title: 'open a job from the list', ignoreFocus: true, run: (ctx) => ctx.page.locator('a.job-row').first().click() },
      { id: 'close-drawer', title: 'close the drawer with Esc', ignoreFocus: true, run: (ctx) => ctx.page.keyboard.press('Escape') },
    ],
  },

  // ---- Job drawer
  { id: 'drawer/queued', ...drawer(JOB_KEYS.queued) },
  { id: 'drawer/resolving', ...drawer(JOB_KEYS.resolving) },
  { id: 'drawer/running', ...drawer(JOB_KEYS.running22), transitions: [{ id: 'live', title: 'live progress for 4 s', run: live(16) }, { id: 'to-end', title: 'running to completion', run: live(60, 80), hold: 400 }] },
  { id: 'drawer/done-1', ...drawer(JOB_KEYS.done1) },
  { id: 'drawer/done-5', ...drawer(JOB_KEYS.done5) },
  { id: 'drawer/done-22', ...drawer(JOB_KEYS.done22) },
  { id: 'drawer/done-150', ...drawer(JOB_KEYS.done150) },
  { id: 'drawer/failed', ...drawer(JOB_KEYS.failed) },
  { id: 'drawer/failed-404', ...drawer(JOB_KEYS.failed404) },
  { id: 'drawer/cancelled', ...drawer(JOB_KEYS.cancelled) },
  { id: 'drawer/skipped', ...drawer(JOB_KEYS.skipped) },
  { id: 'drawer/mixed-codecs', ...drawer(JOB_KEYS.mixed) },
  { id: 'drawer/long-titles', ...drawer(JOB_KEYS.long) },
  {
    id: 'drawer/details-open',
    ...drawer(JOB_KEYS.failed),
    setup: async (ctx) => {
      await ctx.page.locator('.drawer-details > summary').first().click();
    },
  },
  {
    id: 'drawer/remove-confirm',
    ...drawer(JOB_KEYS.done22),
    setup: async (ctx) => {
      await ctx.page.locator('.drawer-delete').first().click();
      await ctx.page.waitForSelector('[data-dialog=confirm]', { timeout: 3000 });
    },
  },
  {
    id: 'drawer/switching',
    ...drawer(JOB_KEYS.done22),
    noStates: true,
    transitions: [
      { id: 'job-to-job', title: 'switch to another job', ignoreFocus: true, run: (ctx) => ctx.hash(`#/queue/${jobId(JOB_KEYS.done5)}`) },
      { id: 'job-to-failed', title: 'switch to a failed job', ignoreFocus: true, run: (ctx) => ctx.hash(`#/queue/${jobId(JOB_KEYS.failed)}`) },
      { id: 'details', title: 'open Details', expectContent: true, run: (ctx) => ctx.page.locator('.drawer-details > summary').first().click() },
      { id: 'cancel-running', title: 'cancel a running job', ignoreFocus: true, before: (ctx) => ctx.hash(`#/queue/${jobId(JOB_KEYS.running22)}`), run: async (ctx) => {
        await ctx.page.locator('.drawer button:has-text("Cancel")').first().click();
        await ctx.wait(150);
        await ctx.page.locator('.drawer button:has-text("Cancel")').last().click();
      } },
    ],
  },

  // ---- New download
  { id: 'new/empty', world: 'active', path: '#/new' },
  { id: 'new/links', world: 'active', path: `#/new?url=${encodeURIComponent(NEW_LINKS.join('\n'))}` },
  { id: 'new/one-album', world: 'active', path: `#/new?url=${encodeURIComponent(albumUrl(7))}` },
  { id: 'new/lookup-error', world: 'active', path: `#/new?url=${encodeURIComponent(albumUrl(8))}`, faults: { 'POST /api/v1/lookup': { status: 502, code: 'upstream_error', message: 'Apple answered 503 Service Unavailable' } } },
  { id: 'new/search', world: 'active', path: '#/new?q=yorushika' },
  { id: 'new/search-none', world: 'active', path: '#/new?q=zzzz' },
  { id: 'new/search-group', world: 'active', path: '#/new?q=yorushika&group=albums' },
  { id: 'new/search-group-empty', world: 'active', path: '#/new?q=beethoven&group=playlists' },
  { id: 'new/search-group-page-error', world: 'active', path: '#/new?q=yorushika&group=songs', faults: { 'GET /api/v1/search?offset': { status: 504, code: 'upstream_timeout', message: 'Apple did not answer in time.' } } },
  { id: 'new/search-error', world: 'active', path: '#/new?q=radiohead', faults: { 'GET /api/v1/search': { status: 504, code: 'upstream_timeout', message: 'Apple did not answer in time.' } } },
  { id: 'new/artist', world: 'active', path: '#/new?q=yorushika&artist=1250709916' },
  {
    id: 'new/submitted',
    world: 'active',
    path: `#/new?url=${encodeURIComponent([albumUrl(7), albumUrl(9)].join('\n'))}`,
    setup: async (ctx) => {
      await ctx.settle();
      await ctx.page.locator('.new-dock-go').first().click();
      await ctx.page.waitForSelector('.new-results', { timeout: 4000 });
    },
  },
  { id: 'new/many-links', world: 'active', path: `#/new?url=${encodeURIComponent(Array.from({ length: 30 }, (_, i) => albumUrl(i + 20)).join('\n'))}` },
  {
    id: 'new/ask',
    world: 'active',
    path: `#/new?url=${encodeURIComponent(albumUrl(7))}`,
    setup: async (ctx) => {
      await ctx.settle();
      await ctx.page.locator('.new-link-pick input:not([disabled])').first().check({ timeout: 4000 });
      await ctx.page.locator('.new-dock-go').first().click();
      await ctx.page.waitForSelector('.ask', { timeout: 4000 });
    },
  },
  { id: 'new/config-error', world: 'active', path: '#/new', faults: { 'GET /api/v1/config': { status: 500, code: 'internal_error', message: 'config unreadable' } } },
  {
    id: 'new/submit-failed',
    world: 'active',
    path: `#/new?url=${encodeURIComponent(albumUrl(7))}`,
    faults: { 'POST /api/v1/downloads': { status: 503, code: 'store_error', message: 'database is locked' } },
    setup: async (ctx) => {
      await ctx.settle();
      await ctx.page.locator('.new-dock-go').first().click();
      await ctx.page.waitForSelector('.new-notes .notice', { timeout: 4000 });
    },
  },
  {
    id: 'new/quality-sheet',
    world: 'active',
    path: `#/new?url=${encodeURIComponent(albumUrl(7))}`,
    setup: async (ctx) => {
      await ctx.settle();
      await ctx.page.locator('.new-link-quality').first().click();
      await ctx.page.waitForSelector('[data-dialog=quality] .quality-slots', { timeout: 4000 });
    },
  },
  {
    id: 'new/aac-only-sheet',
    world: 'active',
    path: `#/new?url=${encodeURIComponent(LOSSY_ALBUM.url)}`,
    setup: async (ctx) => {
      await ctx.settle();
      await ctx.page.locator('.new-link-quality').first().click();
      await ctx.page.waitForSelector('[data-dialog=quality] .quality-same', { timeout: 4000 });
    },
  },
  {
    id: 'new/custom-sheet',
    world: 'active',
    path: `#/new?url=${encodeURIComponent(albumUrl(7))}`,
    setup: async (ctx) => {
      await ctx.settle();
      // Custom… stays disabled until GET /config answers; a setup that cannot
      // open the sheet fails the scenario instead of auditing the page under it.
      await ctx.page.waitForSelector('select#new-quality option[value=custom-edit]:not([disabled])', { state: 'attached', timeout: 4000 });
      await ctx.page.locator('select#new-quality').selectOption('custom-edit');
      await ctx.page.waitForSelector('[data-dialog=custom]', { timeout: 3000 });
    },
  },
  {
    id: 'new/typing',
    world: 'active',
    path: '#/new',
    noStates: true,
    transitions: [
      { id: 'type-search', title: 'typing a search term', ignoreFocus: false, expectContent: true, run: (ctx) => ctx.page.locator('#new-input').pressSequentially('yorushika', { delay: 80 }) },
      { id: 'paste-link', title: 'pasting a link', expectContent: true, run: async (ctx) => {
        await ctx.page.locator('#new-input').fill('');
        await ctx.page.locator('#new-input').fill(albumUrl(12));
      } },
      { id: 'drill-artist', title: 'opening an artist from the hits', ignoreFocus: true, expectContent: true, before: async (ctx) => {
        await ctx.page.locator('#new-input').fill('yorushika');
      }, run: (ctx) => ctx.page.locator('.new-tile[data-kind=artist] .new-tile-open').first().click({ timeout: 3000 }) },
    ],
  },
  {
    id: 'new/typing-stale',
    world: 'active',
    path: '#/new?q=yorushika',
    noStates: true,
    transitions: [
      { id: 'refine', title: 'refining the term over earlier hits', ignoreFocus: false, expectContent: true, run: (ctx) => ctx.page.locator('#new-input').pressSequentially(' sea', { delay: 80 }) },
      { id: 'clear', title: 'clearing the box with Esc', ignoreFocus: false, run: (ctx) => ctx.page.locator('#new-input').press('Escape') },
    ],
  },

  // ---- Logs
  { id: 'logs/default', world: 'active', path: '#/logs' },
  { id: 'logs/errors', world: 'many', path: '#/logs?level=error,warn' },
  { id: 'logs/job', world: 'active', path: `#/logs?job_id=${jobId(JOB_KEYS.running22)}` },
  { id: 'logs/empty', world: 'empty', path: '#/logs?q=zzzzzz' },
  {
    id: 'logs/live',
    world: 'active',
    path: '#/logs',
    noStates: true,
    transitions: [
      { id: 'tail', title: 'live tail', run: async (ctx) => {
        for (let i = 0; i < 12; i++) {
          ctx.control.log({ level: i % 5 === 0 ? 'warn' : 'info', message: `track saved ${i}`, attributes: { component: 'jobs', job_id: jobId(JOB_KEYS.running22) } });
          await ctx.wait(150);
        }
      } },
      {
        id: 'level-chip',
        title: 'toggle a level chip',
        before: async (ctx) => {
          // On a phone the chips sit behind the Filters disclosure.
          if (!(await ctx.page.locator('button.level-chip:visible').count())) await ctx.page.locator('button:has-text("Filters")').first().click();
        },
        run: (ctx) => ctx.page.locator('button.level-chip:visible').first().click(),
      },
    ],
  },

  // ---- Settings: the status board, every section, and the states a head can be in
  { id: 'settings/board', world: 'active', path: '#/settings' },
  { id: 'settings/board-slow', world: 'active', path: '#/settings', latency: 1500 },
  { id: 'settings/board-empty', world: 'empty', path: '#/settings' },
  { id: 'settings/board-simulate', world: 'active', path: '#/settings', worldOpts: { simulate: true }, faults: { 'GET /api/v1/wrapper/status': { status: 503, code: 'wrapper_unavailable', message: 'dial tcp 127.0.0.1:1: connect: connection refused' } } },
  { id: 'settings/board-alarms', world: 'active', path: '#/settings', mutate: (w) => Object.assign(w.wrapper, { ready: false, logged_in: false, regions: [] }) && (w.storageFree = 2.1e9) },
  {
    id: 'settings/board-missing',
    world: 'active',
    path: '#/settings',
    faults: Object.fromEntries(['GET /api/v1/stats', 'GET /api/v1/hooks', 'GET /api/v1/library-sync'].map((k) => [k, { status: 404, code: 'not_found', message: 'not found' }])),
  },
  ...['account', 'quality', 'files', 'lyrics_artwork', 'jobs', 'library_sync', 'hooks', 'logging', 'server'].map((s) => ({ id: `settings/${s}`, world: 'active', path: `#/settings?section=${s}` })),
  { id: 'settings/account-down', world: 'active', path: '#/settings?section=account', faults: { 'GET /api/v1/wrapper/status': { status: 503, code: 'wrapper_unavailable', message: 'dial tcp 127.0.0.1:1: connect: connection refused' } } },
  { id: 'settings/list', world: 'active', path: '#/settings', widths: [375, 320] },
  { id: 'settings/section-phone', world: 'active', path: '#/settings?section=library_sync', widths: [375, 320] },
  { id: 'settings/account-login', world: 'active', path: '#/settings?section=account', mutate: (w) => Object.assign(w.wrapper, { login_supported: true, logged_in: false, regions: [] }) },
  { id: 'settings/account-wrapper-token', world: 'active', path: '#/settings?section=account', mutate: (w) => Object.assign(w.wrapper, { media_user_token: '...a1b2' }) },
  {
    id: 'settings/account-wrapper-and-saved-token',
    world: 'active',
    path: '#/settings?section=account',
    mutate: (w) => Object.assign(w.wrapper, { media_user_token: '...a1b2' }) && Object.assign(w.config.catalog, { media_user_token: '...wxyz' }),
  },
  ...['serving', 'starting', 'login', 'stopped', 'failed'].map((mode) => ({
    id: `settings/account-managed-${mode}`,
    world: 'active',
    path: '#/settings?section=account',
    mutate: managed(mode, mode === 'login' ? { login_residue: true } : {}),
  })),
  { id: 'settings/board-managed-failed', world: 'active', path: '#/settings', mutate: managed('failed') },
  { id: 'settings/account-code', world: 'active', path: '#/settings?section=account', mutate: managed('serving'), noStates: true, setup: toCodeStep },
  {
    id: 'settings/account-code-short',
    world: 'active',
    path: '#/settings?section=account',
    mutate: managed('serving'),
    noStates: true,
    setup: async (ctx) => {
      await toCodeStep(ctx);
      await ctx.page.locator('#login-code').fill('12345');
      await ctx.page.locator('.login-form .btn-primary').click();
    },
  },
  { id: 'settings/load-error', world: 'active', path: '#/settings?section=quality', faults: { 'GET /api/v1/config': { status: 500, code: 'internal_error', message: 'config store unavailable' } } },
  {
    id: 'settings/dirty',
    world: 'active',
    path: '#/settings?section=quality',
    setup: async (ctx) => {
      await ctx.page.locator('.settings-section input.field-switch').first().click();
    },
  },
  {
    id: 'settings/save-error',
    world: 'active',
    path: '#/settings?section=quality',
    faults: { 'PUT /api/v1/config': { status: 422, code: 'invalid_config', message: 'download.max_attempts: must be between 1 and 10', details: {} } },
    setup: async (ctx) => {
      await ctx.page.locator('.settings-section input.field-switch').first().click();
      await ctx.page.locator('.save-bar-save').first().click();
    },
  },
  {
    id: 'settings/switching',
    world: 'active',
    path: '#/settings?section=account',
    noStates: true,
    transitions: ['quality', 'files', 'jobs', 'server'].map((s) => ({ id: `to-${s}`, title: `section → ${s}`, expectScroll: 0, run: (ctx) => ctx.hash(`#/settings?section=${s}`) })),
  },
  {
    id: 'settings/board-switching',
    world: 'active',
    path: '#/settings',
    noStates: true,
    transitions: ['#/settings?section=files', '#/settings', '#/', '#/settings'].map((h) => ({ id: `to-${h}`, title: `→ ${h}`, expectScroll: 0, run: (ctx) => ctx.hash(h) })),
  },

  // ---- Overlays
  { id: 'overlay/palette', world: 'active', path: '#/', setup: palette },
  {
    id: 'overlay/palette-query',
    world: 'active',
    path: '#/',
    setup: async (ctx) => {
      await palette(ctx);
      await ctx.page.keyboard.type('set');
    },
  },
  { id: 'overlay/shortcuts', world: 'active', path: '#/', setup: (ctx) => ctx.page.keyboard.press('Shift+?') },
  {
    id: 'overlay/toast',
    world: 'active',
    path: drawer(JOB_KEYS.done22).path,
    setup: async (ctx) => {
      await ctx.page.locator('.drawer-delete').first().click();
      await ctx.page.locator('[data-dialog=confirm] .dialog-confirm-btn').first().click();
      await ctx.page.waitForSelector('.toast', { timeout: 3000 });
    },
  },
  {
    id: 'shell/pages',
    world: 'active',
    path: '#/',
    noStates: true,
    transitions: ['#/queue', '#/new', '#/logs', '#/settings', '#/', '#/queue'].map(nav),
  },
];

export default {
  name: 'amdl console',
  root,
  defaultLatency: LATENCY,
  async start() {
    // CONSOLE_STATIC audits another checkout's static files (a baseline, a branch).
    const s = await startMockServer({ world: 'active', ...(process.env.CONSOLE_STATIC ? { staticDir: process.env.CONSOLE_STATIC } : {}) });
    return {
      baseUrl: s.url,
      stop: () => s.close(),
      control: {
        async prepare(sc) {
          const w = s.setWorld(sc.world || 'active', { latency: sc.latency ?? LATENCY, faults: sc.faults, latencies: sc.latencies, ...(sc.worldOpts || {}) });
          if (sc.mutate) sc.mutate(w);
        },
        tick: (n) => s.tick(n),
        log: (e) => s.world.addLog(e),
        submit: (urls) => fetch(`${s.origin}/api/v1/downloads`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ urls }) }).then((r) => r.json()),
      },
    };
  },
  // Covers come from Apple's CDN once and then from disk; motion covers are
  // left out, so a page looks the same every run.
  async routes(context) {
    const { imageRoute } = await import('./images.mjs');
    await context.route(/mzstatic\.com/, imageRoute);
    await context.route(/mvod\.itunes\.apple\.com/, (r) => r.abort());
  },
  variants: { stateWidths: [1280, 375] },
  selectors: {
    container: 'section.card, .card, [data-dialog], .drawer-section, .settings-panel, .notice, .empty, .kpi, .live-idle-card, .job-row, .new-link, .new-hit, .status-tile, .domain-head, .settings-list-group, .sheet',
    row: 'li, .job-row, .track, .field, .new-link, .new-hit, .log-row, .fact-row, .kv > div, .fact',
    icon: 'svg, .icon, .status-icon, .conn-dot, .domain-icon, .log-dot, .badge-dot',
    badge: '.nav-badge, .seg-count',
    nav: 'nav.nav, nav.tabbar, nav.settings-nav',
    region: 'main.main',
    skeleton: '.skel, .is-skel, [class*="skel-"]',
    ignore: 'svg.sprite, .sr-only, .boot',
    modal: '[aria-modal=true]',
  },
  components: {
    button: '.btn',
    'segment button': '.seg-btn',
    chip: '.chip',
    badge: '.badge',
    input: '.field-input, input[type=search], .select',
    'nav item': '.nav-item, .tab',
    'icon button': '.btn-icon, .icon-btn',
  },
  rhythm: [
    { name: 'page head → content', selector: '.page-head', measure: 'gapToNext' },
    { name: 'section → next section', selector: '.page > section.card, .page > .card', measure: 'gapToNext' },
    { name: 'card padding', selector: 'section.card:not(.card-flush)', measure: 'padding' },
    { name: 'card head → body', selector: '.card-head', measure: 'gapToNext' },
    { name: 'page gutter', selector: '.page', measure: 'edges', within: 'main.main' },
    { name: 'page head gutter', selector: '.page-head', measure: 'edges', within: 'main.main' },
    { name: 'card title inset', selector: 'section.card', measure: 'firstTextLeft' },
  ],
  allow: {
    // By design (DESIGN.md): progress legends list every state, zeros included; KPI tiles are counts.
    noise: '.jprog-key, .kpi-value, .seg-count, .tp-totals, .throughput .chart-xaxis, .chart, [data-zero]',
    prose: '.dialog-body, .notice-raw, .log-msg, .attention-error, .job-error, .track-error, .new-result-msg, .notice-more',
    // A track's formats repeat values across their chips ("2 ch" in each).
    duplicate: '.sr-only, .chart, .tp-formats, .quality-opt-spec',
  },
  css: {
    files: cssFiles,
    tokens: [join(css, 'tokens.css'), join(css, 'skins/music.css')],
  },
  scenarios: SCENARIOS,
};
