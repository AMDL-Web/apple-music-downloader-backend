// Pieces of HTML the page template puts together: escaping, icons, code
// blocks with their copy buttons, and tab lists. None of it depends on the
// content or the repository the site points at.

// ---- text ------------------------------------------------------------------

export const esc = (s) =>
  String(s).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');

// ---- code blocks -----------------------------------------------------------

export function codeLines(lines) {
  return lines
    .map((line) => {
      if (line.startsWith('$ ')) return `<span class="c-p" aria-hidden="true">$</span> ${esc(line.slice(2))}`;
      if (line.startsWith('PS> ')) return `<span class="c-p" aria-hidden="true">PS&gt;</span> ${esc(line.slice(4))}`;
      if (line.startsWith('# ')) return `<span class="c-c">${esc(line)}</span>`;
      if (/^\s*[[{}\]"]/.test(line)) return `<span class="c-o">${json(line)}</span>`;
      if (/^\s/.test(line)) return esc(line);
      return line === '' ? '' : `<span class="c-o">${esc(line)}</span>`;
    })
    .join('\n');
}

// Colors keys, strings and numbers of one JSON line; "…" marks elided parts.
function json(line) {
  return esc(line).replace(/(&quot;(?:[^&]|&(?!quot;))*?&quot;)(\s*:)?|(-?\b\d+(?:\.\d+)?\b)/g, (m, str, colon, num) => {
    if (str) return colon ? `<span class="c-k">${str}</span>${colon}` : `<span class="c-s">${str}</span>`;
    return `<span class="c-n">${num}</span>`;
  });
}

// The commands of a block, for its copy button: prompt lines, each with the
// indented lines a trailing backslash continues it onto.
export function commands(lines) {
  const out = [];
  let open = false;
  for (const line of lines) {
    if (/^(\$|PS>) /.test(line)) out.push(line.replace(/^(\$|PS>) /, ''));
    else if (open && /^\s/.test(line)) out[out.length - 1] += `\n${line}`;
    else {
      open = false;
      continue;
    }
    open = line.endsWith('\\');
  }
  return out.join('\n');
}

// ---- icons -----------------------------------------------------------------

const svg = (body, extra = '') =>
  `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"${extra}>${body}</svg>`;
export const ICON = {
  // Octicons mark-github (MIT).
  github:
    '<svg viewBox="0 0 16 16" width="18" height="18" fill="currentColor" aria-hidden="true"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8z"/></svg>',
  sun: svg('<circle cx="12" cy="12" r="4"/><path d="M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.3 5.3l1.4 1.4M17.3 17.3l1.4 1.4M5.3 18.7l1.4-1.4M17.3 6.7l1.4-1.4"/>', ' class="i-sun"'),
  moon: svg('<path d="M20 14.5A8.5 8.5 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z"/>', ' class="i-moon"'),
  copy: svg('<rect x="8" y="8" width="12" height="12" rx="2"/><path d="M16 8V5a1 1 0 0 0-1-1H5a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h3"/>', ' class="i-copy"'),
  check: svg('<polyline points="5 12.5 10 17.5 19 7"/>', ' class="i-check"'),
  arrow: svg('<line x1="5" y1="12" x2="19" y2="12"/><polyline points="13 6 19 12 13 18"/>'),
  out: svg('<line x1="7" y1="17" x2="17" y2="7"/><polyline points="8 7 17 7 17 16"/>'),
};

export const copyButton = (ui, text) =>
  `<button class="copy" type="button" data-copy="${esc(text)}" data-copied="${esc(ui.copied)}" aria-label="${esc(ui.copy)}">${ICON.copy}${ICON.check}</button>`;

export const codeBlock = (ui, lines, { label = '', copy = true, wrap = false } = {}) =>
  `<div class="code${label ? '' : ' code-solo'}${wrap ? ' code-wrap' : ''}">${label ? `<div class="code-bar"><span>${esc(label)}</span>${copy ? copyButton(ui, commands(lines)) : ''}</div>` : ''}<pre tabindex="0"><code>${codeLines(lines)}</code></pre>${!label && copy ? copyButton(ui, commands(lines).length ? commands(lines) : lines.join('\n')) : ''}</div>`;

// A tab list over panels; site.js wires it up, and without the script every
// panel shows, one after another.
export const tabs = (label, items) =>
  `<div class="tabs"><div class="tablist" role="tablist" aria-label="${esc(label)}" hidden>${items
    .map((t, i) => `<button type="button" role="tab" id="tab-${t.id}" aria-controls="panel-${t.id}" aria-selected="${i === 0}"${i ? ' tabindex="-1"' : ''}>${esc(t.label)}</button>`)
    .join('')}</div>${items.map((t) => `<div class="panel" id="panel-${t.id}" role="tabpanel" aria-labelledby="tab-${t.id}">${t.html}</div>`).join('')}</div>`;
