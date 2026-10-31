// A menu behind a button: button[aria-haspopup=menu][aria-expanded] opens a
// role=menu panel of role=menuitem buttons (ui/dialogs.js Dialog, so it
// keeps focus, Escape and a click outside close it, focus returns to the
// button, and the app's single-key shortcuts stand down while it is open).
// From 640 px it hangs from the button, its right edge at the button's, and
// opens upward when the room below is short; it closes when the page
// scrolls or resizes under it. Under 640 px it is a bottom sheet.

import { html, useState, useEffect, useRef } from './preact.js';
import { Icon } from './components.js';
import { Dialog } from './dialogs.js';
import { useMedia } from './hooks.js';

// The drawn heights (css/overlays.css .menu) the room below is measured
// against; a wrong guess only picks the other side.
const ITEM_H = 32;
const ITEM_H_TOUCH = 44;
const SEP_H = 9;
const PAD = 10;
const GAP = 4;
const EDGE = 8;

let seq = 0;

const itemsOf = (panel) => Array.from(panel.querySelectorAll('[role="menuitem"]'));

/** Where the anchored menu goes, as a style object: under the button, or over it. */
function placeBy(button, items) {
  const r = button.getBoundingClientRect();
  const root = document.documentElement;
  const item = window.matchMedia('(pointer: coarse)').matches ? ITEM_H_TOUCH : ITEM_H;
  const h = PAD + items.reduce((n, it) => n + (it.separator ? SEP_H : item), 0);
  const right = `${Math.max(EDGE, root.clientWidth - r.right)}px`;
  if (r.bottom + GAP + h <= root.clientHeight - EDGE || r.top - GAP - h < EDGE) return { top: `${r.bottom + GAP}px`, right };
  return { bottom: `${root.clientHeight - r.top + GAP}px`, right };
}

/**
 * The button and its menu. `items` are {id, label} or {separator: true};
 * choosing one closes the menu, puts focus back on the button and calls
 * onChoose(id). `label` names the button and the menu; `heading` titles the
 * sheet on a phone; `size` ('sm') and `disabled` style the button as Button's.
 */
export function MenuButton({ label, title, heading, items, onChoose, className = '', size = '', disabled = false }) {
  const [open, setOpen] = useState(null);
  const [headId] = useState(() => `menu-${++seq}-head`);
  const btn = useRef(null);
  const phone = useMedia('(max-width: 639px)');

  const show = (last) => setOpen({ last, place: placeBy(btn.current, items) });
  const close = (refocus) => {
    setOpen(null);
    if (refocus && btn.current) btn.current.focus({ preventScroll: true });
  };
  useEffect(() => {
    if (!open || phone) return undefined;
    const off = () => setOpen(null);
    window.addEventListener('scroll', off, true);
    window.addEventListener('resize', off);
    return () => {
      window.removeEventListener('scroll', off, true);
      window.removeEventListener('resize', off);
    };
  }, [open, phone]);

  const onButtonKey = (e) => {
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    show(e.key === 'ArrowUp');
  };
  const onMenuKey = (e) => {
    const list = itemsOf(e.currentTarget);
    const at = list.indexOf(document.activeElement);
    let next = -1;
    if (e.key === 'ArrowDown') next = at < 0 ? 0 : (at + 1) % list.length;
    else if (e.key === 'ArrowUp') next = at < 0 ? list.length - 1 : (at - 1 + list.length) % list.length;
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = list.length - 1;
    else if (e.key === 'Tab') {
      e.preventDefault();
      e.stopImmediatePropagation();
      close(true);
      return;
    }
    if (next < 0) return;
    e.preventDefault();
    list[next].focus();
  };
  const choose = (itemId) => {
    close(true);
    onChoose(itemId);
  };

  const real = items.filter((it) => !it.separator);
  const autofocus = open && real.length ? real[open.last ? real.length - 1 : 0].id : null;
  const menu = open
    ? html`<${Dialog}
        key="menu"
        name="menu"
        role="menu"
        className=${phone ? 'sheet menu-sheet' : 'menu'}
        placement=${phone ? 'bottom' : 'anchor'}
        labelledBy=${phone && heading ? headId : undefined}
        label=${phone && heading ? undefined : label}
        style=${phone ? undefined : open.place}
        onKeyDown=${onMenuKey}
        onClose=${() => close(true)}
      >
        ${phone && heading ? html`<h2 id=${headId} class="menu-heading" dir="auto">${heading}</h2>` : null}
        ${items.map((it, i) =>
          it.separator
            ? html`<div key=${`sep-${i}`} class="menu-sep" role="separator"></div>`
            : html`<button
                key=${it.id}
                type="button"
                role="menuitem"
                class="menu-item"
                data-item=${it.id}
                tabindex="-1"
                data-autofocus=${it.id === autofocus ? '' : undefined}
                onClick=${() => choose(it.id)}
              >${it.label}</button>`,
        )}
      </${Dialog}>`
    : null;
  return [
    html`<button
      key="button"
      type="button"
      class=${`btn btn-ghost${size ? ` btn-${size}` : ''} btn-icon menu-btn ${className}`.trim()}
      disabled=${disabled}
      aria-label=${label}
      title=${title}
      aria-haspopup="menu"
      aria-expanded=${open ? 'true' : 'false'}
      ref=${btn}
      onKeyDown=${onButtonKey}
      onClick=${() => (open ? close(false) : show(false))}
    ><${Icon} name="more" /></button>`,
    menu,
  ];
}
