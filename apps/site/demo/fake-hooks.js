// An in-memory hooks.yaml for the fake backends: GET and PUT /api/v1/hooks,
// GET /api/v1/hooks/presets, PUT and DELETE /api/v1/hooks/{name} and POST
// .../test, with the server's redaction (a webhook shows its origin, header
// names and preset, never its path, headers' values or body) and its checks
// (internal/hooks/config.go validate; command entries are read-only). A
// write with {preset, values} is built into a request here, roughly as the
// server does; only what GET /hooks shows of it has to match. The site's
// demo (fake-api.js) and the console's ui-audit mock both answer from it.
// Pure: no timers, no network; a test fails when the hook's host ends in
// .invalid.

const EVENTS = ['job_queued', 'job_finished', 'job_failed', 'job_cancelled'];
const TYPES = ['song', 'album', 'playlist', 'artist', 'station'];
const METHODS = ['GET', 'POST', 'PUT'];
const DEFAULT_ATTEMPTS = 3;
const FINISHED_OR_FAILED = ['job_finished', 'job_failed'];
const FINISHED = ['job_finished'];

const server = (placeholder, def) => ({ key: 'server', label: 'Server', placeholder, default: def });
const joinURL = (base, path) => String(base).replace(/\/+$/, '') + path;

/** The presets GET /hooks/presets describes (apps/desktop/internal/hookpreset `All`), each with a rough build. */
const PRESETS = [
  {
    id: 'ntfy',
    name: 'ntfy',
    kind: 'notify',
    events: FINISHED_OR_FAILED,
    fields: [server('https://ntfy.sh', 'https://ntfy.sh'), { key: 'topic', label: 'Topic', placeholder: 'amdl-downloads' }, { key: 'token', label: 'Access token', placeholder: 'tk_…', secret: true, optional: true }],
    build: (v) => ({
      method: 'POST',
      url: joinURL(v.server, `/${encodeURIComponent(v.topic)}`),
      headers: { Title: 'AMDL', Tags: 'musical_note', 'Content-Type': 'text/plain; charset=utf-8', ...(v.token ? { Authorization: `Bearer ${v.token}` } : {}) },
    }),
  },
  {
    id: 'bark',
    name: 'Bark',
    kind: 'notify',
    events: FINISHED_OR_FAILED,
    fields: [server('https://api.day.app', 'https://api.day.app'), { key: 'key', label: 'Device key', secret: true }],
    build: (v) => ({ method: 'POST', url: joinURL(v.server, '/push'), headers: {} }),
  },
  {
    id: 'telegram',
    name: 'Telegram',
    kind: 'notify',
    events: FINISHED_OR_FAILED,
    fields: [
      { key: 'token', label: 'Bot token', placeholder: '123456:ABC…', secret: true },
      { key: 'chat', label: 'Chat ID', placeholder: '123456789' },
    ],
    build: (v) => ({ method: 'POST', url: `https://api.telegram.org/bot${v.token}/sendMessage`, headers: {} }),
  },
  {
    id: 'plex',
    name: 'Plex',
    kind: 'library',
    events: FINISHED,
    fields: [server('http://192.168.1.10:32400', ''), { key: 'token', label: 'Plex token', secret: true }],
    build: (v) => ({ method: 'GET', url: joinURL(v.server, '/library/sections/all/refresh'), headers: { 'X-Plex-Token': v.token } }),
  },
  {
    id: 'jellyfin',
    name: 'Jellyfin',
    kind: 'library',
    events: FINISHED,
    fields: [server('http://192.168.1.10:8096', ''), { key: 'key', label: 'API key', secret: true }],
    build: (v) => ({ method: 'POST', url: joinURL(v.server, '/Library/Refresh'), headers: { Authorization: `MediaBrowser Token="${v.key}"` } }),
  },
  {
    id: 'emby',
    name: 'Emby',
    kind: 'library',
    events: FINISHED,
    fields: [server('http://192.168.1.10:8096', ''), { key: 'key', label: 'API key', secret: true }],
    build: (v) => ({ method: 'POST', url: joinURL(v.server, '/emby/Library/Refresh'), headers: { 'X-Emby-Token': v.key } }),
  },
  {
    id: 'custom',
    name: 'Custom',
    kind: 'custom',
    events: FINISHED,
    fields: [
      { key: 'url', label: 'Address', placeholder: 'https://example.com/hook' },
      { key: 'method', label: 'Method', choices: ['POST', 'PUT', 'GET'], default: 'POST' },
      { key: 'header', label: 'Header', placeholder: 'Authorization: Bearer …', secret: true, optional: true },
      { key: 'body', label: 'Body', placeholder: 'The JSON payload', optional: true, multiline: true },
    ],
    build: (v) => {
      const at = v.header ? v.header.indexOf(':') : -1;
      if (v.header && at <= 0) throw new Error('Header is Name: value');
      return { method: v.method, url: v.url, headers: v.header ? { [v.header.slice(0, at).trim()]: v.header.slice(at + 1).trim() } : {}, body: v.method === 'GET' ? '' : v.body };
    },
  },
];

/** A preset as GET /hooks/presets describes it. */
const describe = ({ build: _build, ...p }) => ({ ...p, events: [...p.events], fields: p.fields.map((f) => ({ ...f })) });

/** The request preset `id` makes of `values`; throws the reason, naming the field. */
function buildPreset(id, values) {
  const p = PRESETS.find((x) => x.id === id);
  if (!p) throw new Error(`unknown preset "${id}"`);
  const v = {};
  for (const f of p.fields) {
    let s = values && values[f.key] !== undefined && values[f.key] !== null ? String(values[f.key]) : '';
    if (!f.multiline) s = s.trim();
    if (s === '') s = f.default || '';
    if (s === '' && !f.optional) throw new Error(`${f.label} is needed`);
    v[f.key] = s;
  }
  return p.build(v);
}

const error = (status, code, message) => ({ status, body: { error: { code, message } } });

function originOf(url) {
  try {
    const u = new URL(url);
    return `${u.protocol}//${u.host}`;
  } catch {
    return '';
  }
}

/** The entry as GET /hooks shows it. */
function redact(e) {
  const out = { name: e.name, enabled: e.enabled !== false, type: e.type, events: [...e.events], job_types: [...(e.job_types || [])] };
  if (e.type !== 'webhook') return out;
  out.method = e.method || 'POST';
  out.origin = originOf(e.url);
  const names = Object.keys(e.headers || {}).sort();
  if (names.length) out.header_names = names;
  out.max_attempts = e.max_attempts > 0 ? e.max_attempts : DEFAULT_ATTEMPTS;
  out.preset = e.preset || 'custom';
  return out;
}

/** Why `e` would not load, or ''. */
function invalid(e) {
  const label = `hooks.entries["${e.name}"]`;
  if (!Array.isArray(e.events) || e.events.length === 0) return `${label}: events must list at least one of ${EVENTS.join(', ')}`;
  for (const ev of e.events) if (!EVENTS.includes(ev)) return `${label}: unsupported event "${ev}"`;
  if (e.events.includes('job_queued') && e.events.length > 1) {
    return `${label}: job_queued (creation) cannot be combined with terminal events in one hook; define a separate entry for each`;
  }
  for (const t of e.job_types || []) if (!TYPES.includes(t)) return `${label}: unsupported job type "${t}"`;
  if (!e.url || !String(e.url).trim()) return `${label}: url is required for type webhook`;
  let u;
  try {
    u = new URL(e.url);
  } catch {
    u = null;
  }
  if (!u || (u.protocol !== 'http:' && u.protocol !== 'https:')) return `${label}: url: "${e.url}" must be an absolute http or https URL`;
  if (e.method && !METHODS.includes(e.method)) return `${label}: method must be one of GET, POST, PUT`;
  if (e.method === 'GET' && e.body) return `${label}: a GET request has no body`;
  return '';
}

const hostOf = (url) => {
  try {
    return new URL(url).hostname;
  } catch {
    return '';
  }
};

/**
 * A hooks.yaml holding `entries` (full entries: webhooks with url, method,
 * headers, body and the preset they were made from), its switch `enabled`.
 * `handle(method, path, body)` answers a path under /api/v1/hooks with
 * {status, body}; `writes` records every write as {method, path, body}, for
 * checks.
 */
export function createHookStore({ enabled = false, entries = [] } = {}) {
  const file = { enabled, entries: entries.map((e) => structuredClone(e)) };
  const writes = [];
  const view = () => ({ enabled: file.enabled, entries: file.entries.map(redact) });

  function handle(method, path, body) {
    if (path === '/api/v1/hooks') {
      if (method === 'GET') return { status: 200, body: view() };
      if (method === 'PUT') {
        writes.push({ method, path, body });
        if (!body || typeof body.enabled !== 'boolean') return error(400, 'bad_request', 'enabled must be a boolean');
        file.enabled = body.enabled;
        return { status: 200, body: view() };
      }
      return error(405, 'method_not_allowed', 'method not allowed');
    }
    if (path === '/api/v1/hooks/presets' && method === 'GET') return { status: 200, body: { presets: PRESETS.map(describe) } };
    const m = path.match(/^\/api\/v1\/hooks\/([^/]+)(\/test)?$/);
    if (!m) return error(404, 'not_found', `no route ${method} ${path}`);
    const name = decodeURIComponent(m[1]);
    const at = file.entries.findIndex((e) => e.name === name);
    const entry = at < 0 ? null : file.entries[at];
    const readOnly = () => error(409, 'hook_read_only', `hook ${name} is a command hook; edit it in hooks.yaml`);
    if (m[2]) {
      if (method !== 'POST') return error(405, 'method_not_allowed', 'method not allowed');
      if (!entry) return error(404, 'hook_not_found', `no hook named ${name}`);
      if (entry.type !== 'webhook') return readOnly();
      const host = hostOf(entry.url);
      if (host.endsWith('.invalid')) {
        return { status: 200, body: { succeeded: false, error: `${entry.method || 'POST'} ${originOf(entry.url)}: dial tcp: lookup ${host}: no such host` } };
      }
      return { status: 200, body: { succeeded: true } };
    }
    if (method === 'DELETE') {
      writes.push({ method, path, body: null });
      if (!entry) return error(404, 'hook_not_found', `no hook named ${name}`);
      if (entry.type !== 'webhook') return readOnly();
      file.entries.splice(at, 1);
      return { status: 200, body: { status: 'deleted' } };
    }
    if (method !== 'PUT') return error(405, 'method_not_allowed', 'method not allowed');
    writes.push({ method, path, body });
    if (!body || typeof body !== 'object') return error(400, 'bad_request', 'the body is not a JSON object');
    if (body.type !== undefined && body.type !== 'webhook') return error(400, 'bad_request', 'only webhook hooks are written here');
    if (entry && entry.type !== 'webhook') return readOnly();
    const next = entry ? structuredClone(entry) : { name, type: 'webhook', enabled: true, events: [], job_types: [] };
    for (const k of ['enabled', 'events', 'job_types', 'max_attempts', 'method', 'url', 'headers', 'body']) if (body[k] !== undefined) next[k] = structuredClone(body[k]);
    if (body.preset !== undefined) {
      let req;
      try {
        req = buildPreset(body.preset, body.values);
      } catch (e) {
        return error(422, 'invalid_hook', e.message);
      }
      Object.assign(next, { method: req.method, url: req.url, headers: req.headers, body: req.body || '', preset: body.preset });
    } else if (body.url !== undefined || body.headers !== undefined) next.preset = 'custom';
    const why = invalid(next);
    if (why) return error(422, 'invalid_hook', why);
    if (entry) file.entries[at] = next;
    else file.entries.push(next);
    return { status: 200, body: redact(next) };
  }

  return { handle, writes, view };
}
