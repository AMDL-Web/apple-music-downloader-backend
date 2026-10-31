import assert from 'node:assert/strict';
import { test } from 'node:test';

import { answerOverrides, codecChoices, downloadAsks, hasAsks, readSkip } from '../static/js/lib/download-ask.js';

const both = ['lossless', 'atmos'];

test('the quality is asked only when the release and the server both have lossless and Dolby', () => {
  assert.deepEqual(codecChoices(both, ['alac', 'ec3', 'aac']), ['alac', 'ec3']);
  assert.deepEqual(codecChoices(['hi-res-lossless', 'atmos'], ['ec3', 'alac']), ['ec3', 'alac']);
  assert.deepEqual(codecChoices(both, ['ac3', 'alac', 'ec3']), ['ac3', 'alac', 'ec3']);
  assert.deepEqual(codecChoices(['lossless'], ['alac', 'ec3']), []);
  assert.deepEqual(codecChoices(['atmos'], ['alac', 'ec3']), []);
  assert.deepEqual(codecChoices(both, ['alac', 'aac']), []);
  assert.deepEqual(codecChoices(both, ['ec3', 'aac']), []);
  assert.deepEqual(codecChoices(undefined, undefined), []);
});

test('a quality chosen on the page asks nothing; files on disk are asked unless always skipped', () => {
  assert.deepEqual(downloadAsks({ traits: both, priority: ['alac', 'ec3'], follow: false }), { codecs: [], overwrite: false });
  assert.deepEqual(downloadAsks({ traits: both, priority: ['alac', 'ec3'], inLibrary: true }), { codecs: ['alac', 'ec3'], overwrite: true });
  assert.equal(downloadAsks({ traits: [], priority: [], inLibrary: true, alwaysSkip: true }).overwrite, false);
  assert.equal(hasAsks(downloadAsks({ traits: [], priority: [] })), false);
  assert.equal(hasAsks(downloadAsks({ traits: [], priority: [], inLibrary: true })), true);
});

test('the answer leads quality_priority with the choice and always says force_overwrite for a release in the library', () => {
  assert.deepEqual(answerOverrides({ codec: 'ec3', priority: ['alac', 'ec3', 'aac'] }), { quality_priority: ['ec3', 'alac', 'aac'] });
  assert.deepEqual(answerOverrides({ inLibrary: true }), { force_overwrite: false });
  assert.deepEqual(answerOverrides({ inLibrary: true, replace: true }), { force_overwrite: true });
  assert.deepEqual(answerOverrides({ replace: true }), {});
});

test('the stored preference is on only for "true"', () => {
  assert.equal(readSkip('true'), true);
  assert.equal(readSkip(null), false);
  assert.equal(readSkip('1'), false);
});
