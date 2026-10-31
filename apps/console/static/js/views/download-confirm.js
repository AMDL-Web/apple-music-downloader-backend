// New download: the question an artist link asks before it queues the
// artist's whole discography.

import { html } from '../ui/preact.js';
import { ConfirmDialog } from '../ui/dialogs.js';

/** Props: confirm ({names, label, run}), onClose(). */
export function ArtistConfirm({ confirm, onClose }) {
  return html`<${ConfirmDialog}
    title=${confirm.names.length === 1 ? `Download everything by ${confirm.names[0]}?` : `Download everything by ${confirm.names.length} artists?`}
    confirmLabel=${confirm.label}
    onConfirm=${() => {
      onClose();
      confirm.run();
    }}
    onCancel=${onClose}
  >
    <p>An artist link queues the artist's whole discography as one job: every album, often hundreds of tracks or more. You can cancel it from the queue.</p>
    ${confirm.names.length > 1 ? html`<p dir="auto">${confirm.names.join(', ')}</p>` : null}
  </${ConfirmDialog}>`;
}
