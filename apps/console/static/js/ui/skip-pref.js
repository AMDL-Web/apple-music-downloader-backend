// "Always skip files already downloaded": a choice this browser keeps
// (lib/download-ask.js SKIP_KEY). New download reads it before asking about
// files on disk; Settings › Storage & files sets it.

import { html, useState } from './preact.js';
import { readPref, writePref } from '../lib/prefs.js';
import { SKIP_KEY, readSkip } from '../lib/download-ask.js';

const storage = () => {
  try {
    return window.localStorage;
  } catch {
    return null;
  }
};

/** The stored choice; false when storage is unavailable. */
export const loadSkipExisting = () => readPref(storage(), SKIP_KEY, readSkip);

/** Stores the choice; when storage refuses it, it lasts for this page view. */
export const saveSkipExisting = (on) => writePref(storage(), SKIP_KEY, on ? 'true' : 'false');

/** The Settings row, shaped like a settings field but saved at once, in this browser only. */
export function SkipExistingField() {
  const [on, setOn] = useState(loadSkipExisting);
  const id = 'field-local-skip-existing';
  return html`<div class="field" data-key="local.skip_existing" data-type="switch">
    <div class="field-text">
      <div class="field-label-row"><label class="field-label" for=${id}>Always skip files already downloaded</label></div>
      <p class="field-help" id=${`${id}-help`}>This browser: a download skips tracks already on disk instead of asking.</p>
    </div>
    <div class="field-control">
      <input
        type="checkbox"
        role="switch"
        class="field-switch"
        id=${id}
        checked=${on}
        aria-describedby=${`${id}-help`}
        onChange=${(e) => {
          const v = e.currentTarget.checked;
          saveSkipExisting(v);
          setOn(v);
        }}
      />
    </div>
  </div>`;
}
