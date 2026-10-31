import test from 'node:test';
import assert from 'node:assert/strict';
import {
  JOB_ACTIONS,
  jobAllows,
  actionPlan,
  actionDone,
  actionReason,
  actionError,
  bulkProgressText,
  bulkLabel,
  bulkSummary,
  rowMenu,
} from '../static/js/lib/jobactions.js';

const job = (id, status, title = id) => ({ id, status, title });
const err = (status, code, message = '') => Object.assign(new Error(message), { status, code });

test('each status allows exactly the actions the backend accepts', () => {
  const allowed = (status) => JOB_ACTIONS.filter((a) => jobAllows(job('j', status), a));
  assert.deepEqual(allowed('queued'), ['cancel']);
  assert.deepEqual(allowed('running'), ['cancel']);
  assert.deepEqual(allowed('failed'), ['retry', 'delete']);
  assert.deepEqual(allowed('cancelled'), ['retry', 'delete'], 'a cancelled job can be retried like a failed one');
  assert.deepEqual(allowed('completed'), ['delete']);
  assert.deepEqual(allowed('mystery'), []);
  assert.equal(jobAllows(null, 'delete'), false);
  assert.equal(jobAllows(job('j', 'failed'), 'archive'), false);
});

test('actionPlan splits a selection by action, keeping order', () => {
  const list = [job('a', 'failed'), job('b', 'running'), job('c', 'completed'), job('d', 'failed'), job('e', 'queued'), job('f', 'cancelled')];
  const plan = actionPlan(list);
  assert.deepEqual(plan.retry.map((j) => j.id), ['a', 'd', 'f']);
  assert.deepEqual(plan.cancel.map((j) => j.id), ['b', 'e']);
  assert.deepEqual(plan.delete.map((j) => j.id), ['a', 'c', 'd', 'f']);
  assert.deepEqual(actionPlan([]), { retry: [], cancel: [], delete: [] });
});

test('a job already gone counts as deleted, not as cancelled or retried', () => {
  assert.equal(actionDone('delete', err(404, 'job_not_found')), true);
  assert.equal(actionDone('delete', err(409, 'job_active')), false);
  assert.equal(actionDone('cancel', err(404, 'job_not_found')), false);
  assert.equal(actionDone('delete', undefined), false);
});

test('actionReason words every refusal the job routes answer', () => {
  assert.equal(actionReason('delete', err(409, 'job_active', 'job x is still running')), 'still running, finishing or running a hook');
  assert.equal(actionReason('delete', err(404, 'job_not_found')), 'already deleted');
  assert.equal(actionReason('cancel', err(404, 'job_not_found')), 'no longer exists');
  assert.equal(actionReason('retry', err(409, 'job_not_retryable')), 'only failed or cancelled jobs can be retried');
  assert.equal(actionReason('cancel', err(409, 'job_not_cancellable')), 'already finished');
  assert.equal(actionReason('retry', err(409, 'job_finalizing')), 'still finishing its last run, try again in a moment');
  assert.equal(actionReason('retry', err(409, 'duplicate_active')), 'another active job already downloads this link');
  assert.equal(actionReason('retry', err(503, 'queue_full')), 'the queue is full');
  assert.equal(actionReason('cancel', err(0, 'network_error', 'Cannot reach the server.')), 'cannot reach the server');
  assert.equal(actionReason('cancel', err(500, '')), 'HTTP 500');
  assert.equal(actionReason('cancel', undefined), 'the request failed');
});

test('actionError is one sentence for the drawer', () => {
  assert.equal(actionError('delete', err(409, 'job_active')), "Couldn't delete this job: still running, finishing or running a hook.");
  assert.equal(actionError('retry', err(503, 'queue_full')), "Couldn't retry this job: the queue is full.");
});

test('progress and labels count jobs', () => {
  assert.equal(bulkProgressText('delete', 3, 12), 'Deleting 3 of 12');
  assert.equal(bulkProgressText('retry', 0, 1), 'Retrying 0 of 1');
  assert.equal(bulkLabel('cancel', 1), 'Cancel 1 job');
  assert.equal(bulkLabel('delete', 12), 'Delete 12 jobs');
});

const ok = (item) => ({ item, ok: true, value: null });
const bad = (item, e) => ({ item, ok: false, error: e });
const title = (j) => j.title;

test('bulkSummary reports full success in the success role', () => {
  assert.deepEqual(bulkSummary('delete', [ok(job('a', 'completed')), ok(job('b', 'failed'))], title), {
    message: 'Deleted 2 jobs',
    role: 'success',
  });
  assert.deepEqual(bulkSummary('retry', [ok(job('a', 'failed'))], title), { message: 'Queued 1 job again', role: 'success' });
  assert.equal(bulkSummary('cancel', [], title).role, 'neutral');
});

test('bulkSummary counts an already deleted job as deleted', () => {
  const s = bulkSummary('delete', [ok(job('a', 'completed')), bad(job('b', 'completed'), err(404, 'job_not_found'))], title);
  assert.deepEqual(s, { message: 'Deleted 2 jobs', role: 'success' });
});

test('bulkSummary names failures grouped by reason, three titles at most', () => {
  const active = err(409, 'job_active');
  const outcomes = [
    ok(job('a', 'completed')),
    bad(job('b', 'completed', 'IGOR'), active),
    bad(job('c', 'completed', 'folklore'), active),
    bad(job('d', 'completed', 'Blonde'), active),
    bad(job('e', 'completed', 'GUTS'), active),
    bad(job('f', 'completed', 'Currents'), err(500, 'store_error', 'delete job: disk I/O error')),
  ];
  assert.deepEqual(bulkSummary('delete', outcomes, title), {
    message:
      'Deleted 1 of 6 jobs. 5 failed: IGOR, folklore, Blonde and 1 more (still running, finishing or running a hook); Currents (delete job: disk I/O error).',
    role: 'danger',
  });
});

test('bulkSummary says so when nothing went through', () => {
  const s = bulkSummary('retry', [bad(job('a', 'failed', 'IGOR'), err(409, 'duplicate_active'))], title);
  assert.deepEqual(s, { message: "Couldn't retry 1 job: IGOR (another active job already downloads this link).", role: 'danger' });
  const r = bulkSummary('retry', [ok(job('a', 'failed')), bad(job('b', 'failed', 'IGOR'), err(503, 'queue_full'))], title);
  assert.equal(r.message, 'Queued 1 of 2 jobs again. 1 failed: IGOR (the queue is full).');
});

test('a row menu offers what the status of its job allows, as in the desktop app', () => {
  const ids = (status, input = 'https://music.apple.com/us/album/x/1') =>
    rowMenu({ id: 'j', status, input }).map((it) => (it.separator ? '-' : it.id));
  assert.deepEqual(ids('queued'), ['details', 'cancel', '-', 'copy']);
  assert.deepEqual(ids('running'), ['details', 'cancel', '-', 'copy'], 'a job under way can only be cancelled');
  assert.deepEqual(ids('completed'), ['details', '-', 'copy', '-', 'delete']);
  assert.deepEqual(ids('failed'), ['details', 'retry', '-', 'copy', '-', 'delete']);
  assert.deepEqual(ids('cancelled'), ['details', 'retry', '-', 'copy', '-', 'delete']);
  assert.deepEqual(ids('completed', ''), ['details', '-', 'delete'], 'no link, nothing to copy');
  const gone = rowMenu({ id: 'j', status: 'failed', input: 'x', error: 'Apple Music has no album 1 in storefront cn (404)' });
  assert.deepEqual(gone.map((it) => (it.separator ? '-' : it.id)), ['details', '-', 'copy', '-', 'delete'], 'a link Apple has nothing at gets no Retry');
  const labels = rowMenu({ id: 'j', status: 'failed', input: 'x' }).filter((it) => !it.separator).map((it) => it.label);
  assert.deepEqual(labels, ['Show details', 'Retry', 'Copy link', 'Delete…']);
});
