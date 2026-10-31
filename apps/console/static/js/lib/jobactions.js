// Pure rules for acting on jobs: which of Retry, Cancel and Delete a job's
// status allows, the words for each refusal the API can answer, and the
// progress line and summary of a bulk run. The status rules mirror
// internal/jobs: only a failed or cancelled job can be retried (POST .../retry
// answers 409 job_not_retryable otherwise), a queued or running one can be
// cancelled, and only a finished one (completed, failed, cancelled) can be
// deleted.

import { isTerminal } from './status.js';
import { isNotFound } from './jobview.js';

export const JOB_ACTIONS = ['retry', 'cancel', 'delete'];

const WORDS = {
  retry: { verb: 'Retry', ing: 'Retrying', did: 'Queued', after: ' again', fail: 'retry' },
  cancel: { verb: 'Cancel', ing: 'Cancelling', did: 'Cancelled', after: '', fail: 'cancel' },
  delete: { verb: 'Delete', ing: 'Deleting', did: 'Deleted', after: '', fail: 'delete' },
};

const jobs = (n) => (n === 1 ? '1 job' : `${n} jobs`);

/** Whether a job's status allows `action` ('retry', 'cancel' or 'delete'). */
export function jobAllows(job, action) {
  const status = job && job.status;
  switch (action) {
    case 'retry':
      return status === 'failed' || status === 'cancelled';
    case 'cancel':
      return status === 'queued' || status === 'running';
    case 'delete':
      return isTerminal(status);
    default:
      return false;
  }
}

const MENU_LABELS = { details: 'Show details', retry: 'Retry', cancel: 'Cancel', copy: 'Copy link', delete: 'Delete…' };

/**
 * A Queue row's menu, as the desktop app draws it: Show details; Cancel
 * (queued, running) or Retry (failed, cancelled); then Copy link (the job's
 * input), and for a finished job Delete… after a separator of its own. A job
 * under way can only be cancelled; a link Apple has nothing at (a 404) gets no
 * Retry, as in the lists. Entries are {id, label} or {separator: true}.
 */
export function rowMenu(job) {
  const ids = ['details'];
  if (jobAllows(job, 'cancel')) ids.push('cancel');
  else if (jobAllows(job, 'retry') && !isNotFound(job.error)) ids.push('retry');
  if (job && job.input) ids.push('-', 'copy');
  if (jobAllows(job, 'delete')) ids.push('-', 'delete');
  return ids.map((id) => (id === '-' ? { separator: true } : { id, label: MENU_LABELS[id] }));
}

/** The jobs each action applies to, in the order given: {retry, cancel, delete}. */
export function actionPlan(list) {
  const plan = { retry: [], cancel: [], delete: [] };
  for (const job of list) {
    for (const action of JOB_ACTIONS) if (jobAllows(job, action)) plan[action].push(job);
  }
  return plan;
}

/**
 * Whether an error still leaves the job where the action wanted it: deleting
 * a job that is already gone (404) counts as done.
 */
export function actionDone(action, err) {
  return action === 'delete' && Boolean(err) && err.status === 404;
}

const stripStop = (s) => String(s || '').replace(/[.\s]+$/, '');

/**
 * Why the API refused `action` for one job, as a short lowercase phrase
 * ("still running, finishing or running a hook"). Unknown errors fall back to
 * the server's message.
 */
export function actionReason(action, err) {
  const code = (err && err.code) || '';
  const status = err ? err.status : undefined;
  if (code === 'job_not_found' || status === 404) return action === 'delete' ? 'already deleted' : 'no longer exists';
  if (code === 'job_active') return 'still running, finishing or running a hook';
  if (code === 'job_not_retryable') return 'only failed or cancelled jobs can be retried';
  if (code === 'job_not_cancellable') return 'already finished';
  if (code === 'job_finalizing') return 'still finishing its last run, try again in a moment';
  if (code === 'duplicate_active') return 'another active job already downloads this link';
  if (code === 'queue_full') return 'the queue is full';
  const message = stripStop(err && err.message);
  if (message) return message.charAt(0).toLowerCase() + message.slice(1);
  return status ? `HTTP ${status}` : 'the request failed';
}

/** One sentence for a failed single-job action: "Couldn't delete this job: <reason>." */
export function actionError(action, err) {
  const w = WORDS[action] || { fail: 'update' };
  return `Couldn't ${w.fail} this job: ${actionReason(action, err)}.`;
}

/** The progress line of a bulk run: "Deleting 3 of 12". */
export function bulkProgressText(action, done, total) {
  const w = WORDS[action] || { ing: 'Working on' };
  return `${w.ing} ${done} of ${total}`;
}

/** A button or dialog label for running `action` on n jobs: "Delete 3 jobs". */
export function bulkLabel(action, n) {
  const w = WORDS[action] || { verb: 'Update' };
  return `${w.verb} ${jobs(n)}`;
}

const NAMES_PER_REASON = 3;

/**
 * The toast after a bulk run of `action` over runLimited outcomes, naming
 * the jobs that failed (grouped by reason, at most three titles each):
 * {message, role}. Success is "Deleted 12 jobs" (success role); any failure
 * makes it "Deleted 10 of 12 jobs. 2 failed: IGOR, folklore (still
 * running, …)." in the danger role, which stays until dismissed.
 * `titleOf(item)` names a job; deleting a job that is already gone counts as
 * done (actionDone).
 */
export function bulkSummary(action, outcomes, titleOf = (job) => (job && job.id) || '') {
  const w = WORDS[action] || { did: 'Updated', after: '', fail: 'update' };
  const total = outcomes.length;
  const failed = outcomes.filter((o) => !o.ok && !actionDone(action, o.error));
  const ok = total - failed.length;
  if (failed.length === 0) return { message: `${w.did} ${jobs(total)}${w.after}`, role: total === 0 ? 'neutral' : 'success' };
  const groups = new Map();
  for (const o of failed) {
    const reason = actionReason(action, o.error);
    if (!groups.has(reason)) groups.set(reason, []);
    groups.get(reason).push(titleOf(o.item));
  }
  const parts = [];
  for (const [reason, names] of groups) {
    const shown = names.slice(0, NAMES_PER_REASON);
    const more = names.length - shown.length;
    const list = more > 0 ? `${shown.join(', ')} and ${more} more` : shown.join(', ');
    parts.push(`${list} (${reason})`);
  }
  const head = ok === 0 ? `Couldn't ${w.fail} ${jobs(total)}` : `${w.did} ${ok} of ${jobs(total)}${w.after}`;
  const tail = ok === 0 ? `: ${parts.join('; ')}.` : `. ${failed.length} failed: ${parts.join('; ')}.`;
  return { message: head + tail, role: 'danger' };
}
