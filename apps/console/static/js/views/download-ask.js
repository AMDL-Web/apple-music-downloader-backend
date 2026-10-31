// New download: the questions a download asks before it goes
// (lib/download-ask.js): which quality, when the release has both lossless
// and Dolby Atmos and Settings lists both, and what to do with files already
// on disk, when the release is in the library.

import { html, useState } from '../ui/preact.js';
import { CHOICE_LABEL } from '../lib/download-ask.js';
import { Segmented } from '../ui/components.js';
import { ConfirmDialog } from '../ui/dialogs.js';

/**
 * Props: title ("Download “…”"), asks ({codecs, overwrite}),
 * onDone({codec, replace}), onCancel(). The quality starts on the codec
 * Settings ranks first; files on disk start on Skip.
 */
export function AskDialog({ title, asks, onDone, onCancel }) {
  const [codec, setCodec] = useState(asks.codecs[0] || '');
  const [replace, setReplace] = useState(false);
  return html`<${ConfirmDialog}
    title=${title}
    confirmLabel="Download"
    confirmFocus
    onConfirm=${() => onDone({ codec, replace })}
    onCancel=${onCancel}
  >
    <div class="ask">
      ${asks.codecs.length
        ? html`<div class="ask-row" key="codec">
            <span class="ask-label">Quality</span>
            <${Segmented} label="Quality" options=${asks.codecs.map((c) => ({ value: c, label: CHOICE_LABEL[c] || c }))} value=${codec} onChange=${setCodec} />
          </div>`
        : null}
      ${asks.overwrite
        ? html`<div class="ask-row" key="overwrite">
            <span class="ask-label">Files on disk</span>
            <${Segmented}
              label="Files on disk"
              options=${[
                { value: 'skip', label: 'Skip' },
                { value: 'replace', label: 'Replace' },
              ]}
              value=${replace ? 'replace' : 'skip'}
              onChange=${(v) => setReplace(v === 'replace')}
            />
          </div>`
        : null}
    </div>
  </${ConfirmDialog}>`;
}
