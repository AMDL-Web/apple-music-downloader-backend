// A Queue row's "…" menu (items from lib/jobactions.js rowMenu): Show
// details opens the drawer, Cancel and Retry act at once, Copy link copies
// the job's link, and Delete… asks first. One job's action runs alone, so
// the selection bar (a bulk run) stays out of it.

import { html, useState } from '../ui/preact.js';
import { cancelDownload, retryDownload, deleteDownload } from '../api.js';
import { jobTitle, isUnresolved, inputLabel } from '../lib/format.js';
import { rowMenu, actionError, bulkSummary } from '../lib/jobactions.js';
import { useApp, copyText } from '../ui/components.js';
import { ConfirmDialog } from '../ui/dialogs.js';
import { MenuButton } from '../ui/menu.js';

const CALLS = { retry: retryDownload, cancel: cancelDownload, delete: deleteDownload };

// The delete dialog's title names the job, cut like the desktop app's.
const TITLE_MAX = 48;

export const jobName = (job) => (isUnresolved(job) ? inputLabel(job.input) : jobTitle(job));

/**
 * Runs `action` on one job. A failure toasts why (lib/jobactions.js
 * actionError); a retry that went through says so, since the row may leave
 * the tab. Resolves to whether it went through.
 */
export async function runOne(app, action, job) {
  try {
    await CALLS[action](job.id);
  } catch (error) {
    app.toast({ message: actionError(action, error), role: 'danger' });
    return false;
  }
  if (action === 'retry') app.toast(bulkSummary(action, [{ ok: true, item: job }], jobTitle));
  return true;
}

/**
 * The "…" button of a Queue row and its menu; `href` opens the job's drawer.
 * onLeaving(id) runs as an action that may take the row out of the list
 * starts (a confirmed delete, a cancel, a retry), while the row is still
 * there: the Queue finds the row focus moves to if it leaves.
 */
export function JobMenu({ job, href, onLeaving }) {
  const app = useApp();
  const [asking, setAsking] = useState(false);
  const [busy, setBusy] = useState(false);
  const name = jobName(job);
  const choose = (id) => {
    if (id === 'details') app.navigate(href);
    else if (id === 'copy') copyText(app, job.input);
    else if (id === 'delete') setAsking(true);
    else {
      onLeaving(job.id);
      runOne(app, id, job);
    }
  };
  const remove = async () => {
    setBusy(true);
    onLeaving(job.id);
    await runOne(app, 'delete', job);
    setBusy(false);
    setAsking(false);
  };
  const short = name.length > TITLE_MAX ? `${name.slice(0, TITLE_MAX - 1)}…` : name;
  return [
    html`<${MenuButton}
      key="menu"
      className="job-menu-btn"
      label=${`Actions for ${name}`}
      title="More actions"
      heading=${name}
      items=${rowMenu(job)}
      onChoose=${choose}
    />`,
    asking
      ? html`<${ConfirmDialog}
          key="delete"
          title=${`Delete “${short}”?`}
          confirmLabel="Delete"
          busyLabel="Deleting…"
          danger
          busy=${busy}
          onConfirm=${remove}
          onCancel=${() => setAsking(false)}
        >The download and its history leave the queue. Files already saved stay on disk.</${ConfirmDialog}>`
      : null,
  ];
}
