// Thin client for the public /api/v1 contract (internal/api/openapi.yaml).
// Paths are written out in full so the Go contract guard in console_test.go
// can check each one against the spec. They resolve against the console's
// own location, so the app also works behind a path prefix.

import { encodeQuery } from './lib/route.js';

/** An error response: HTTP status (0 for a network failure) plus the 2.0 error envelope. */
export class ApiError extends Error {
  constructor(status, code, message, details, body) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.code = code;
    this.details = details;
    this.body = body;
  }
}

const REQUEST_TIMEOUT_MS = 15000;
// A login or 2FA answer comes within wrapper.login_timeout_seconds (120 s by
// default) from a wrapper-manager, and within several minutes from a wrapper
// amdl runs, which reboots its guest; keep in sync with client.LoginWait,
// which console_test.go checks. A quality probe reads one master playlist per
// track and has no server-side deadline. Both outlast the default bound.
const LOGIN_TIMEOUT_MS = 600000;
const QUALITY_TIMEOUT_MS = 180000;
// The server bounds a lookup at 15 s and a search at 10 s and answers 504;
// wait a little longer.
const LOOKUP_TIMEOUT_MS = 20000;
// The server cuts a hook test off after 30 s and then answers.
const HOOK_TEST_TIMEOUT_MS = 40000;

/** Resolves an "/api/..." path against the server root above /console/. */
export function resolveUrl(path, query, base = globalThis.document ? globalThis.document.baseURI : 'http://localhost/console/') {
  const root = new URL('../', base);
  const url = new URL(path.replace(/^\//, ''), root);
  const qs = encodeQuery(query);
  if (qs) url.search = qs;
  return url.toString();
}

/** Turns a non-2xx body into an ApiError, reading {error: {code, message, details}}. */
export function toApiError(status, body, fallback) {
  const err = body && body.error;
  if (err && typeof err === 'object') {
    return new ApiError(status, err.code || '', err.message || fallback, err.details, body);
  }
  if (typeof err === 'string') return new ApiError(status, '', err, undefined, body);
  return new ApiError(status, '', fallback, undefined, body);
}

/**
 * One bounded API call. `opts.body` is sent as JSON; `opts.timeoutMs`
 * replaces the default bound for calls the server may hold longer;
 * `opts.signal` cancels the call, which then rejects with code "aborted".
 */
async function request(method, path, query, opts = {}) {
  const { body: payload, timeoutMs = REQUEST_TIMEOUT_MS, signal } = opts;
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  const cancel = () => controller.abort();
  if (signal) {
    if (signal.aborted) cancel();
    else signal.addEventListener('abort', cancel, { once: true });
  }
  const headers = { Accept: 'application/json' };
  const init = { method, headers, cache: 'no-store', signal: controller.signal };
  if (payload !== undefined) {
    headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(payload);
  }
  let res;
  try {
    res = await fetch(resolveUrl(path, query), init);
  } catch (e) {
    clearTimeout(timer);
    if (signal) signal.removeEventListener('abort', cancel);
    if (signal && signal.aborted) throw new ApiError(0, 'aborted', 'The request was cancelled.');
    const timedOut = e && e.name === 'AbortError';
    throw new ApiError(0, timedOut ? 'timeout' : 'network_error', timedOut ? 'The server did not answer in time.' : 'Cannot reach the server.');
  }
  let body = null;
  try {
    const text = await res.text();
    body = text ? JSON.parse(text) : null;
  } catch {
    body = null;
  } finally {
    clearTimeout(timer);
    if (signal) signal.removeEventListener('abort', cancel);
  }
  if (!res.ok) throw toApiError(res.status, body, `${method} ${path} failed with HTTP ${res.status}.`);
  return body;
}

/** Runs a request and returns {data} or {error}, for widgets that render either. */
export async function settle(promise) {
  try {
    return { data: await promise };
  } catch (error) {
    return { error };
  }
}

export const getHealth = () => request('GET', '/api/v1/health');
export const getSystem = () => request('GET', '/api/v1/system');
export const getStats = (days = 14) => request('GET', '/api/v1/stats', { days });
export const getConfig = () => request('GET', '/api/v1/config');
export const reloadConfig = () => request('POST', '/api/v1/config/reload');
export const getHooks = () => request('GET', '/api/v1/hooks');
/** The services a webhook can be made from: {presets: [{id, name, kind, events, fields}]}; the backend builds each one's request. */
export const getHookPresets = () => request('GET', '/api/v1/hooks/presets');
/** Sets hooks.yaml's master switch; resolves to the HooksResponse after it. */
export const setHooksEnabled = (enabled) => request('PUT', '/api/v1/hooks', undefined, { body: { enabled } });
/**
 * Writes the webhook entry `name` (WebhookWrite: an omitted field keeps its
 * value; {preset, values} in place of the request makes the backend build
 * it) and resolves to the HookEntry as written; 409 hook_read_only for a
 * command entry, 422 invalid_hook or invalid_hooks_file, 503 when the server
 * has no hooks.yaml path.
 */
export const putHook = (name, body) => request('PUT', `/api/v1/hooks/${encodeURIComponent(name)}`, undefined, { body });
/** Sends the webhook `name` once with a sample payload; resolves to {succeeded, error?}. */
export const testHook = (name) => request('POST', `/api/v1/hooks/${encodeURIComponent(name)}/test`, undefined, { timeoutMs: HOOK_TEST_TIMEOUT_MS });
/** Removes the webhook entry `name` from hooks.yaml. */
export const deleteHook = (name) => request('DELETE', `/api/v1/hooks/${encodeURIComponent(name)}`);
export const getLibrarySync = () => request('GET', '/api/v1/library-sync');
export const getWrapperStatus = () => request('GET', '/api/v1/wrapper/status');
export const listDownloads = (query) => request('GET', '/api/v1/downloads', query);
export const getDownload = (id) => request('GET', `/api/v1/downloads/${encodeURIComponent(id)}`);
export const cancelDownload = (id) => request('POST', `/api/v1/downloads/${encodeURIComponent(id)}/cancel`);
export const retryDownload = (id) => request('POST', `/api/v1/downloads/${encodeURIComponent(id)}/retry`);
export const listLogs = (query) => request('GET', '/api/v1/logs', query);

/**
 * Submits links: body {urls: string[], overrides?: DownloadOverrides}.
 * Resolves to the BatchSubmitResponse (202, at least one URL accepted).
 * When none was, it rejects with ApiError 422 no_urls_accepted (or 500
 * store_error) whose `details` is that BatchSubmitResponse.
 */
export const createDownload = (body) => request('POST', '/api/v1/downloads', undefined, { body });
/** Deletes a finished job; 409 job_active while it is queued or running. */
export const deleteDownload = (id) => request('DELETE', `/api/v1/downloads/${encodeURIComponent(id)}`);
/** The qualities each track of a link offers (QualityResponse); 404 catalog_not_found or 502 upstream_error when the lookup fails. */
export const queryQuality = (url, signal) =>
  request('POST', '/api/v1/quality', undefined, { body: { url }, timeoutMs: QUALITY_TIMEOUT_MS, signal });
/**
 * Previews one link without creating a job (LookupResponse). Errors after the
 * link parsed carry it in details.link.
 */
export const lookupLink = (url, { signal } = {}) =>
  request('POST', '/api/v1/lookup', undefined, { body: { url }, timeoutMs: LOOKUP_TIMEOUT_MS, signal });
/**
 * Searches the catalog (SearchResponse): query {term, types?, offset?,
 * limit?}; the backend's default storefront unless query names one.
 */
export const searchCatalog = (query, { signal } = {}) => request('GET', '/api/v1/search', query, { timeoutMs: LOOKUP_TIMEOUT_MS, signal });
/** An artist and one page of its albums (ArtistAlbumsResponse): query {offset?, limit?}. */
export const getArtistAlbums = (id, query, { signal } = {}) =>
  request('GET', `/api/v1/artists/${encodeURIComponent(id)}/albums`, query, { timeoutMs: LOOKUP_TIMEOUT_MS, signal });
/**
 * Merges `partial` into the runtime config and answers the new ConfigResponse.
 * Send only the changed fields of a fresh GET; 422 config_key_not_mutable or
 * invalid_config name what was refused.
 */
export const putConfig = (partial) => request('PUT', '/api/v1/config', undefined, { body: partial });
/** Resolves to {status: 'logged_in'} or {status: 'needs_2fa', login_id}. */
export const loginWrapper = ({ username, password }) =>
  request('POST', '/api/v1/wrapper/login', undefined, { body: { username, password }, timeoutMs: LOGIN_TIMEOUT_MS });
/** Completes a needs_2fa login; resolves to {status: 'logged_in'}. */
export const submitWrapperTwoStep = (loginId, code) =>
  request('POST', `/api/v1/wrapper/login/${encodeURIComponent(loginId)}/2fa`, undefined, { body: { two_step_code: code }, timeoutMs: LOGIN_TIMEOUT_MS });
export const logoutWrapper = (username) => request('POST', '/api/v1/wrapper/logout', undefined, { body: { username } });
/** Clears the library-sync anchor; the next poll re-anchors without submitting albums. */
export const resetLibrarySync = () => request('POST', '/api/v1/library-sync/reset');

/** Stream URLs; StreamClient appends ?cursor=. */
export const feedStreamUrl = () => resolveUrl('/api/v1/downloads/events');
export const jobStreamUrl = (id) => resolveUrl(`/api/v1/downloads/${encodeURIComponent(id)}/events`);
export const logStreamUrl = (query) => resolveUrl('/api/v1/logs/stream', query);

/** Parses SSE data as JSON, or null. */
export function parseData(ev) {
  try {
    return JSON.parse(ev.data);
  } catch {
    return null;
  }
}
