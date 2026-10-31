// The audit's Settings scenarios: the status board, every section, and the
// states a head, the account panel and a save can be in.

// A wrapper amdl runs in a QEMU guest (WrapperStatus.managed) in `mode`; the
// guest answers no status unless it serves.
const MANAGED_ERROR = 'guest did not reach its console within 2m0s: qemu-system-x86_64 exited: exit status 1';
export const managed = (mode, over = {}) => (w) =>
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

export const SETTINGS_SCENARIOS = [
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
];
