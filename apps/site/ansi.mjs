// Turns a terminal frame (text with SGR escapes, as the TUI's snapshot test
// writes it) into the HTML the page shows in `.term pre`: runs of one style
// in spans with an inline color, `b` for bold, `u` for underline, and each
// wide (CJK) character in a `w` span two cells wide, so the columns line up.

import { esc } from './blocks.mjs';

const SGR = /\x1b\[([0-9;]*)m/g;
// Other control sequences (cursor moves, OSC 8 links) carry nothing to draw.
const OTHER = /\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b\[[0-9;?]*[A-Za-ln-z]/g;
const WIDE = /[ᄀ-ᅟ⺀-〾ぁ-㏿㐀-䶿一-鿿ꀀ-꓏가-힣豈-﫿︰-﹏＀-｠￠-￦\u{1f300}-\u{1f64f}\u{1f900}-\u{1f9ff}\u{20000}-\u{3fffd}]/u;

const hex = (r, g, b) => `#${[r, g, b].map((v) => v.toString(16).padStart(2, '0')).join('')}`;

function apply(st, params) {
  const codes = params === '' ? [0] : params.split(';').map((c) => (c === '' ? 0 : Number(c)));
  for (let i = 0; i < codes.length; i++) {
    const c = codes[i];
    if (c === 0) Object.assign(st, { fg: '', bg: '', b: false, u: false });
    else if (c === 1) st.b = true;
    else if (c === 22) st.b = false;
    else if (c === 4) st.u = true;
    else if (c === 24) st.u = false;
    else if (c === 39) st.fg = '';
    else if (c === 49) st.bg = '';
    else if ((c === 38 || c === 48) && codes[i + 1] === 2) {
      st[c === 38 ? 'fg' : 'bg'] = hex(codes[i + 2] ?? 0, codes[i + 3] ?? 0, codes[i + 4] ?? 0);
      i += 4;
    }
  }
}

function span(st, text) {
  const body = [...text].map((ch) => (WIDE.test(ch) ? `<span class="w">${esc(ch)}</span>` : esc(ch))).join('');
  const cls = [st.b && 'b', st.u && 'u'].filter(Boolean).join(' ');
  const style = [st.fg && `color:${st.fg}`, st.bg && `background:${st.bg}`].filter(Boolean).join(';');
  if (!cls && !style) return body;
  return `<span${cls ? ` class="${cls}"` : ''}${style ? ` style="${style}"` : ''}>${body}</span>`;
}

/** The HTML of a frame; trailing blank lines and each line's trailing spaces go. */
export function ansiToHtml(frame) {
  const lines = frame.replace(OTHER, '').replace(/\r/g, '').split('\n');
  while (lines.length && !lines[lines.length - 1].replace(SGR, '').trim()) lines.pop();
  return lines
    .map((line) => {
      const st = { fg: '', bg: '', b: false, u: false };
      const runs = [];
      let pos = 0;
      let col = 0;
      // A tab moves to the next multiple of 8 columns; a wide character takes two.
      const cells = (raw) =>
        [...raw]
          .map((ch) => {
            if (ch !== '\t') {
              col += WIDE.test(ch) ? 2 : 1;
              return ch;
            }
            const n = 8 - (col % 8);
            col += n;
            return ' '.repeat(n);
          })
          .join('');
      for (const m of [...line.matchAll(SGR), null]) {
        const text = cells(line.slice(pos, m ? m.index : line.length));
        const key = `${st.fg}|${st.bg}|${st.b}|${st.u}`;
        if (text) {
          if (runs.length && runs[runs.length - 1].key === key) runs[runs.length - 1].text += text;
          else runs.push({ key, st: { ...st }, text });
        }
        if (!m) break;
        pos = m.index + m[0].length;
        apply(st, m[1]);
      }
      return runs
        .map((r) => span(r.st, r.text))
        .join('')
        .replace(/ +$/, '');
    })
    .join('\n');
}
