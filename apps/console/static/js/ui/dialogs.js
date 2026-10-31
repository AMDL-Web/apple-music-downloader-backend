// Modal dialogs: the shared focus handling (useDialog), a generic Dialog
// (a scrim and a panel in .overlay-host) and ConfirmDialog. Every open
// dialog panel carries data-dialog, which the app's single-key shortcuts and
// the global paste check before they act (isDialogOpen).

import { html, useEffect, useRef, useState } from './preact.js';
import { Button } from './components.js';
import { useMedia } from './hooks.js';

const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

const shown = (n) => n.offsetParent !== null;

/** Whether a modal dialog panel (data-dialog) is open in the document. */
export function isDialogOpen() {
  return document.querySelector('[data-dialog]') !== null;
}

/**
 * Focus handling for a modal panel in `ref`: on mount it focuses the first
 * visible [data-autofocus] element, else the first focusable one, else the
 * panel; Tab and Shift+Tab stay inside; Escape calls onClose (when given)
 * and never reaches the window shortcuts, so a dialog over the job drawer
 * closes alone; on unmount focus returns to the element that had it.
 */
export function useDialog(ref, onClose) {
  const close = useRef(onClose);
  close.current = onClose;
  useEffect(() => {
    const el = ref.current;
    if (!el) return undefined;
    const opener = document.activeElement;
    const target =
      Array.from(el.querySelectorAll('[data-autofocus]')).find(shown) || Array.from(el.querySelectorAll(FOCUSABLE)).find(shown) || el;
    target.focus({ preventScroll: true });
    const onKey = (e) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        if (close.current) close.current();
        return;
      }
      if (e.key !== 'Tab') return;
      const nodes = Array.from(el.querySelectorAll(FOCUSABLE)).filter(shown);
      if (nodes.length === 0) {
        e.preventDefault();
        return;
      }
      const first = nodes[0];
      const last = nodes[nodes.length - 1];
      if (e.shiftKey && (document.activeElement === first || document.activeElement === el)) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    el.addEventListener('keydown', onKey);
    return () => {
      el.removeEventListener('keydown', onKey);
      // Only when focus is still ours: a page change focuses its heading.
      const active = document.activeElement;
      const ours = !active || active === document.body || el.contains(active);
      if (ours && opener && opener.isConnected && typeof opener.focus === 'function') opener.focus({ preventScroll: true });
    };
  }, []);
}

/**
 * A modal panel over a scrim: .overlay-host[data-placement=center|bottom] >
 * .scrim + .<className>[role][aria-modal][data-dialog=<name>]. Clicking the
 * scrim or pressing Escape calls onClose; without onClose the dialog can only
 * be closed by its own buttons. Label it with `labelledBy` (an id inside
 * `children`) or `label`. `style` places the panel (an anchored menu), and
 * `onKeyDown` sees keys before the shared handling (Escape, Tab).
 */
export function Dialog({ name = '', role = 'dialog', className = 'sheet', placement = 'center', label, labelledBy, describedBy, style, onKeyDown, onClose, children }) {
  const ref = useRef(null);
  useDialog(ref, onClose);
  return html`<div class="overlay-host" data-placement=${placement}>
    <div class="scrim" onClick=${onClose || undefined}></div>
    <div
      class=${className}
      role=${role}
      aria-modal="true"
      aria-label=${label}
      aria-labelledby=${labelledBy}
      aria-describedby=${describedBy}
      data-dialog=${name}
      tabindex="-1"
      style=${style}
      onKeyDown=${onKeyDown}
      ref=${ref}
    >
      ${children}
    </div>
  </div>`;
}

let confirmSeq = 0;

/**
 * Asks before a destructive or irreversible action: role=alertdialog, the
 * title, an optional body (children), Cancel (focused, the safe choice) and
 * the confirm button (`danger` styles it as destructive; `confirmFocus`
 * focuses it instead, for a choice that is not destructive). While `busy` both
 * buttons are aria-disabled, the confirm button reads busyLabel (the `altLabel`
 * button does when there is one: it is the choice that runs), and Escape
 * and the scrim do nothing; the caller closes the dialog when its request
 * settles. Escape or Cancel calls onCancel; focus returns to the opener.
 * `altLabel` + `onAlt` add a third, primary choice (a "Save and leave" next
 * to a destructive "Discard and leave"): the three sit in one row in macOS
 * order, the destructive one alone on the left, Cancel then the primary on
 * the right. Under 640 px the dialog is a bottom sheet whose full-width
 * buttons read primary, destructive, Cancel. The DOM follows the order drawn,
 * so Tab and a screen reader meet the buttons as they are seen.
 */
export function ConfirmDialog({ title, children, confirmLabel = 'Confirm', cancelLabel = 'Cancel', busyLabel = 'Working…', altLabel = '', danger = false, confirmFocus = false, busy = false, onConfirm, onCancel, onAlt }) {
  const [id] = useState(() => `confirm-${++confirmSeq}`);
  const phone = useMedia('(max-width: 639px)');
  const cancel = busy ? undefined : onCancel;
  const off = busy ? 'true' : undefined;
  const confirm = html`<${Button}
    key="confirm"
    variant=${danger ? 'danger' : 'primary'}
    className="dialog-confirm-btn"
    aria-disabled=${off}
    aria-busy=${altLabel ? undefined : off}
    onClick=${busy ? undefined : onConfirm}
    data-autofocus=${confirmFocus || undefined}
  >${busy && !altLabel ? busyLabel : confirmLabel}</${Button}>`;
  const dismiss = html`<${Button} key="cancel" variant="secondary" className="dialog-cancel" aria-disabled=${off} onClick=${cancel} data-autofocus=${confirmFocus ? undefined : true}>${cancelLabel}</${Button}>`;
  const alt = altLabel
    ? html`<${Button} key="alt" variant="primary" className="dialog-alt-btn" aria-disabled=${off} aria-busy=${off} onClick=${busy ? undefined : onAlt}>${busy ? busyLabel : altLabel}</${Button}>`
    : null;
  const buttons = phone ? [alt, confirm, dismiss] : alt ? [confirm, dismiss, alt] : [dismiss, confirm];
  return html`<${Dialog}
    name="confirm"
    role="alertdialog"
    className="sheet dialog-confirm"
    placement=${phone ? 'bottom' : 'center'}
    labelledBy=${`${id}-title`}
    describedBy=${children ? `${id}-body` : undefined}
    onClose=${cancel}
  >
    <h2 id=${`${id}-title`} class="card-title dialog-title">${title}</h2>
    ${children ? html`<div id=${`${id}-body`} class="dialog-body">${children}</div>` : null}
    <div class="dialog-actions" data-choices=${altLabel ? '3' : undefined}>
      ${buttons}
    </div>
  </${Dialog}>`;
}
