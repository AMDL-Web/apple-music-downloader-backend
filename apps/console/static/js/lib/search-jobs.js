// Pure helpers for which job a catalog search hit (lib/search.js) is in the
// library or the queue through: the key a hit is tracked by, its earlier or
// just-submitted job kept live by the feed, and what a submitted hit's
// result row shows until its job resolves. No DOM.

/** The key a hit is tracked by: its type and id, as a job of it is. */
export const hitKey = (hit) => (hit ? `${hit.type}:${hit.id}` : '');

const ACTIVE = new Set(['queued', 'running']);

/**
 * The job a hit is in the library or the queue through, as lib/lookup.js
 * priorJob reports it: {state: 'active'|'done', job} or null. `sent` maps a
 * hitKey to the id of a job this page submitted for it; `jobs` maps a job id
 * to its latest snapshot (submit results and the feed), which wins over the
 * hit's prior_job from the search. A job that failed, was cancelled or was
 * deleted ({deleted: true}) marks nothing.
 */
export function hitPrior(hit, sent = new Map(), jobs = new Map()) {
  if (!hit) return null;
  const prior = hit.prior_job && hit.prior_job.id ? hit.prior_job : null;
  const id = sent.get(hitKey(hit)) || (prior ? prior.id : '');
  if (!id) return null;
  const job = jobs.get(id) || (prior && prior.id === id ? prior : null);
  if (!job || job.deleted) return null;
  if (ACTIVE.has(job.status)) return { state: 'active', job };
  if (job.status === 'completed') return { state: 'done', job };
  return null;
}

/**
 * Applies feed messages to `jobs` (a job id -> snapshot map) for the ids in
 * `watched`: an upsert replaces the snapshot unless it is older, a delete
 * marks it {deleted: true}. Returns `jobs` itself when nothing changed.
 */
export function trackJobs(jobs, batch, watched) {
  let next = jobs;
  const put = (id, value) => {
    if (next === jobs) next = new Map(jobs);
    next.set(id, value);
  };
  for (const msg of batch || []) {
    if (!msg) continue;
    if (msg.type === 'download_deleted' && watched.has(msg.job_id)) {
      put(msg.job_id, { id: msg.job_id, deleted: true });
      continue;
    }
    const job = msg.type === 'download_upserted' ? msg.job : null;
    if (!job || !job.id || !watched.has(job.id)) continue;
    const have = next.get(job.id);
    if (have && !have.deleted && (Date.parse(job.updated_at || '') || 0) < (Date.parse(have.updated_at || '') || 0)) continue;
    put(job.id, job);
  }
  return next;
}

/** The preview a submitted hit's result row shows until its job resolves (lib/submit.js previewSeed). */
export function hitPreview(hit) {
  if (!hit) return null;
  const out = { title: hit.name, type: hit.type };
  for (const key of ['artist_name', 'artwork_url', 'release_date', 'genre', 'artwork_bg_color', 'artwork_text_color1', 'artwork_text_color2', 'artwork_text_color3', 'artwork_text_color4']) {
    if (hit[key]) out[key] = hit[key];
  }
  if (hit.type === 'artist') out.artist_name = hit.name;
  return out;
}

/**
 * The job a submit result puts a hit in the queue with, for hitPrior: the new
 * job of an accepted URL, or the job already downloading it
 * (duplicate_active), as {id, status}; null otherwise.
 */
export function sentJob(result) {
  if (!result) return null;
  if (result.status === 'accepted' && result.job && result.job.id) return result.job;
  if (result.status === 'duplicate_active' && result.existing_job_id) return { id: result.existing_job_id, status: 'queued' };
  return null;
}
