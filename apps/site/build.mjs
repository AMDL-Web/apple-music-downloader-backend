#!/usr/bin/env node
// Builds the AMDL website: one page per language from content/<lang>.json,
// with the stylesheets and static/site.js inlined, into a directory that any
// static host can serve at its root or under a subpath.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { ansiToHtml } from './ansi.mjs';
import { ICON, NOSCRIPT_TABS, codeBlock, copyButton, esc, tabs } from './blocks.mjs';
import { SCENES, buildDemo } from './demo/bundle.mjs';
import { serve } from './serve.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));

export const REPO = 'https://github.com/AMDL-Web/apple-music-downloader';
// Links into the repository, the install scripts and the Compose file follow the
// branch the published site describes, so a visitor reads and installs what the
// page shows.
export const REF = 'dev';
const BLOB = `${REPO}/blob/${REF}`;
const RAW = `https://raw.githubusercontent.com/AMDL-Web/apple-music-downloader/${REF}`;

export const LANGS = [
  { code: 'en', og: 'en_US' },
  { code: 'zh-CN', og: 'zh_CN' },
];

const THEME_KEY = 'amdl-site-theme';
const LANG_KEY = 'amdl-site-lang';

const USAGE = `usage: node apps/site/build.mjs [--out <dir>] [--base <url>] [--desktop <dir>] [--tui <file>] [--serve <port>]

  --out <dir>      where to write the site (default: apps/site/dist)
  --base <url>     absolute URL the site is served from, such as https://example.org/;
                   adds canonical, hreflang and Open Graph URLs (og.mjs draws og.png)
  --desktop <dir>  the desktop app's web build (an index.html and its files), embedded
                   as the live desktop demo; without it the page has no desktop demo
  --tui <file>     a frame of the terminal interface with its color escapes, as the
                   TUI's snapshot test writes it; without it the page shows none
  --serve <port>   after building, serve the output on 127.0.0.1:<port>`;

export class UsageError extends Error {}

export function parseArgs(argv) {
  const opts = { out: path.join(here, 'dist'), base: '', desktop: '', tui: '', serve: 0 };
  for (let i = 0; i < argv.length; i++) {
    const flag = argv[i];
    if (!['--out', '--base', '--desktop', '--tui', '--serve'].includes(flag)) throw new UsageError(`unknown argument: ${flag}`);
    const value = argv[++i];
    if (value === undefined || value.startsWith('--')) throw new UsageError(`${flag} needs a value`);
    if (flag === '--out') opts.out = path.resolve(value);
    if (flag === '--base') {
      let url;
      try {
        url = new URL(value);
      } catch {
        throw new UsageError(`--base is not an absolute URL: ${value}`);
      }
      if (url.protocol !== 'https:' && url.protocol !== 'http:') throw new UsageError(`--base must be http or https: ${value}`);
      opts.base = url.href.endsWith('/') ? url.href : `${url.href}/`;
    }
    if (flag === '--desktop') {
      opts.desktop = path.resolve(value);
      if (!fs.existsSync(path.join(opts.desktop, 'index.html'))) throw new UsageError(`--desktop has no index.html: ${value}`);
    }
    if (flag === '--tui') {
      opts.tui = path.resolve(value);
      if (!fs.statSync(opts.tui, { throwIfNoEntry: false })?.isFile()) throw new UsageError(`--tui is not a file: ${value}`);
    }
    if (flag === '--serve') {
      const port = Number(value);
      if (!Number.isInteger(port) || port < 1 || port > 65535) throw new UsageError(`--serve needs a port number: ${value}`);
      opts.serve = port;
    }
  }
  return opts;
}

// ---- text ------------------------------------------------------------------


const expand = (href) => href.replace('{blob}', BLOB).replace('{repo}', REPO);

// Content strings carry three marks: `code`, **strong** and [text](href),
// where href may start with {blob} or {repo}.
export function inline(s) {
  const parts = String(s).split(/(`[^`]+`)/);
  return parts
    .map((part) => {
      if (part.startsWith('`') && part.endsWith('`') && part.length > 1) return `<code>${esc(part.slice(1, -1))}</code>`;
      return esc(part)
        .replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>')
        .replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, (_, text, href) => `<a href="${esc(expand(href))}">${text}</a>`);
    })
    .join('');
}

// ---- code blocks -----------------------------------------------------------

// Code is the same in every language. Each block is a list of lines: "$ cmd"
// is a command, "# text" a comment, anything else output, shown as JSON when
// it parses as JSON.
const CODE = {
  hero: 'docker compose up -d',
  server: [
    '$ curl -X POST http://localhost:18080/api/v1/downloads \\',
    "    -H 'Content-Type: application/json' \\",
    `    -d '{"urls":["https://music.apple.com/us/album/1519740112"]}'`,
    '{"accepted":1,"rejected":0,"results":[{"status":"accepted","job":{"id":"job_4467…","status":"queued",…}}]}',
    '',
    '$ curl -N http://localhost:18080/api/v1/downloads/{job_id}/events',
  ],
  // One list of code blocks per step; a block may carry a label.
  quickStart: {
    binary: [
      [
        { label: 'macOS · Linux', lines: [`$ curl -fsSL ${RAW}/deploy/install/install.sh | sh`] },
        {
          label: 'Windows · PowerShell',
          lines: [`PS> [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072; irm ${RAW}/deploy/install/install.ps1 | iex`],
        },
      ],
      [{ lines: ['$ amdl doctor'] }],
      [{ lines: ['$ amdl wrapper install'] }],
      [{ lines: ['$ amdl service install', '$ amdl service status'] }],
    ],
    docker: [
      [{ lines: ['$ mkdir amdl && cd amdl', '$ mkdir -p data/downloads && chown 1000:1000 data/downloads', `$ curl -fsSLO ${RAW}/deploy/docker-compose.yml`] }],
      [{ lines: ['AMDL_WRAPPER_ADDRESS: "http://wrapper-manager:8080"'] }],
      [{ lines: ['$ docker compose up -d'] }],
      [],
    ],
  },
};


// ---- terminal --------------------------------------------------------------

// Loopback and LAN addresses: a frame of a real install may show one, and the
// page must not. The tests check the built pages; the build checks the TUI
// frame it is given.
export const PRIVATE_ADDRESS = /\b(?:127\.0\.0\.1|192\.168\.\d+\.\d+|10\.\d+\.\d+\.\d+)\b/;

export function terminalFrame(file) {
  const html = ansiToHtml(fs.readFileSync(file, 'utf8'));
  const leak = PRIVATE_ADDRESS.exec(html);
  if (leak) throw new Error(`${file} shows the private address ${leak[0]}`);
  return html;
}

// ---- page ------------------------------------------------------------------

export function renderPage(c, ctx) {
  const { lang, other, base } = ctx;
  const ui = c.ui;
  const url = (code) => `${base}${code}/`;
  const head = [
    `<meta charset="utf-8">`,
    `<meta name="viewport" content="width=device-width, initial-scale=1">`,
    `<title>${esc(c.meta.title)}</title>`,
    `<meta name="description" content="${esc(c.meta.description)}">`,
    `<meta name="color-scheme" content="light dark">`,
    `<meta name="theme-color" media="(prefers-color-scheme: light)" content="#ffffff">`,
    `<meta name="theme-color" media="(prefers-color-scheme: dark)" content="#0a0a0a">`,
    `<link rel="icon" href="../favicon.svg" type="image/svg+xml">`,
    `<meta property="og:type" content="website">`,
    `<meta property="og:site_name" content="AMDL">`,
    `<meta property="og:title" content="${esc(c.meta.title)}">`,
    `<meta property="og:description" content="${esc(c.meta.description)}">`,
    `<meta property="og:locale" content="${lang.og}">`,
    `<meta name="twitter:card" content="summary_large_image">`,
  ];
  if (base) {
    head.push(
      `<link rel="canonical" href="${url(lang.code)}">`,
      ...LANGS.map((l) => `<link rel="alternate" hreflang="${l.code}" href="${url(l.code)}">`),
      `<link rel="alternate" hreflang="x-default" href="${base}">`,
      `<meta property="og:url" content="${url(lang.code)}">`,
      `<meta property="og:image" content="${base}og.png">`,
      `<meta property="og:image:alt" content="${esc(c.meta.ogAlt)}">`,
    );
  }
  head.push(
    `<script>try{var d=document.documentElement,t=localStorage.getItem(${JSON.stringify(THEME_KEY)});if(t==="light"||t==="dark")d.setAttribute("data-theme",t);localStorage.setItem(${JSON.stringify(LANG_KEY)},d.lang)}catch(e){}</script>`,
    `<style>${ctx.css}</style>`,
    NOSCRIPT_TABS,
  );

  const p = c.products;
  const header = `<header class="top">
<a class="brand" href="./" aria-label="${esc(ui.home)}">AMDL<span class="meter" aria-hidden="true"><i></i><i></i><i></i><i></i></span></a>
<nav class="nav" aria-label="${esc(ui.nav)}"><a href="#products">${esc(c.nav.products)}</a><a href="#quick-start">${esc(c.nav.quickStart)}</a><a href="#agents">${esc(c.nav.agents)}</a><a href="${BLOB}/docs/README.md">${esc(c.nav.docs)}</a></nav>
<div class="tools">
<a class="lang" href="../${other.code}/" hreflang="${other.code}" lang="${other.code}" title="${esc(ui.otherLangLabel)}">${esc(ui.otherLang)}</a>
<a class="icon" href="${REPO}" aria-label="${esc(ui.github)}">${ICON.github}</a>
<button class="icon" type="button" data-theme-toggle aria-label="${esc(ui.theme)}">${ICON.sun}${ICON.moon}</button>
</div>
</header>`;

  const hero = `<section class="hero" aria-labelledby="hero-title">
<div class="hero-copy">
<p class="eyebrow"><span class="dot" aria-hidden="true"></span>${esc(c.hero.eyebrow)}</p>
<h1 id="hero-title">${esc(c.hero.title).replaceAll('\n', '<br>')}<span>${esc(c.hero.subtitle)}</span></h1>
<p class="lead">${inline(c.hero.lead)}</p>
<div class="cta">
<div class="cmd"><code><span class="c-p" aria-hidden="true">$</span> ${esc(CODE.hero)}</code>${copyButton(ui, CODE.hero)}</div>
<a class="btn" href="#quick-start">${esc(c.hero.cta)}${ICON.arrow}</a>
</div>
</div>
<div class="hero-art">${ctx.hero}</div>
</section>`;

  // The live demos: the console on made-up data, and the desktop app's web
  // build when there is one, each in a window. The desktop app is a few
  // megabytes of wasm, so it loads when the visitor starts it. site.js loads
  // the console as it nears the screen and scales a window drawn at a fixed
  // size (data-scale) to fit.
  const d = c.demo;
  const win = (kind, title, extra, body) =>
    `<div class="window win-${kind}"><div class="win-bar"><span class="lights" aria-hidden="true"><i></i><i></i><i></i></span><span class="win-title">${title}</span>${extra}</div><div class="win-body" data-scale="1280">${body}</div></div>`;
  const full = (src) => `<a class="win-action" href="${src}">${esc(d.fullScreen)}${ICON.out}</a>`;
  const consoleWin = win(
    'console',
    esc(d.address),
    full('../demo/console/'),
    `<iframe data-demo data-src="../demo/console/" title="${esc(d.consoleFrame)}"></iframe><a class="win-open" href="../demo/console/"><span>${esc(d.open)}${ICON.out}</span></a>`,
  );
  const icon = ctx.desktop?.icon ? `<img src="../demo/desktop/${ctx.desktop.icon}" width="72" height="72" alt="">` : '';
  const desktopWin =
    ctx.desktop &&
    win(
      'desktop',
      'AMDL',
      full('../demo/desktop/'),
      `<iframe data-src="../demo/desktop/" title="${esc(d.desktopFrame)}"></iframe><button class="win-start" type="button">${icon}<span>${esc(d.start)}${ICON.arrow}</span></button>`,
    );
  const demo = `<section class="demo" aria-label="${esc(d.label)}">
${
  desktopWin
    ? tabs(d.label, [
        { id: 'demo-console', label: d.tabs.console, html: consoleWin },
        { id: 'demo-desktop', label: d.tabs.desktop, html: desktopWin },
      ])
    : `<div class="panel">${consoleWin}</div>`
}
<p class="demo-note">${inline(d.note)}</p>
</section>`;

  // A view of the console pinned to a route (demo/bundle.mjs#SCENES), where a
  // screenshot would be: the demo, drawn at its own size and scaled to fit,
  // taking no input.
  const view = (name, alt) => {
    const v = SCENES[name];
    const shown = v.view ?? v.width;
    return `<div class="live" role="img" aria-label="${esc(alt)}" data-scale="${shown}" style="--vw:${v.width};--vh:${v.height};--vx:${v.x ?? 0};--cw:${shown}"><iframe data-demo data-src="../demo/console/${v.route}" title="${esc(alt)}" tabindex="-1" inert></iframe></div>`;
  };

  const stats = `<dl class="stats">${c.stats
    .map((s) => `<div><dt>${esc(s.label)}</dt><dd>${esc(s.value)}${s.unit ? `<span>${esc(s.unit)}</span>` : ''}</dd></div>`)
    .join('')}</dl>`;

  const secHead = (s, id) =>
    `<header class="sec-head"><p class="kicker">${esc(s.kicker)}</p><h2 id="${id}">${esc(s.title)}</h2>${s.lead ? `<p class="lead">${inline(s.lead)}</p>` : ''}</header>`;

  const how = `<section class="sec" id="how" aria-labelledby="how-title">${secHead(c.how, 'how-title')}
<ol class="steps">${c.how.steps
    .map((s, i) => `<li><span class="num">${String(i + 1).padStart(2, '0')}</span><h3>${esc(s.title)}</h3><p>${inline(s.text)}</p></li>`)
    .join('')}</ol></section>`;

  const points = (list) => `<ul class="points">${list.map((x) => `<li>${inline(x)}</li>`).join('')}</ul>`;
  const prodCopy = (x, extra = '') =>
    `<div class="prod-copy"><p class="kicker">${esc(x.label)}${x.badge ? ` <span class="badge">${esc(x.badge)}</span>` : ''}</p><h3>${esc(x.name)}</h3><p>${inline(x.text)}</p>${x.points ? points(x.points) : ''}${extra}</div>`;

  const products = `<section class="sec" id="products" aria-labelledby="products-title">${secHead(p, 'products-title')}
<article class="prod prod-split" id="server" aria-label="${esc(p.server.label)}">${prodCopy(p.server)}<div class="prod-media">${codeBlock(ui, CODE.server, { label: p.server.codeTitle, wrap: true })}</div></article>
<article class="prod" id="console" aria-label="${esc(p.console.label)}">${prodCopy(p.console)}
<div class="gallery">
<div class="g-main"><figure>${view('overview', p.console.shots.overview)}</figure><figure class="g-phone">${view('phone', p.console.shots.phone)}</figure></div>
<figure>${view('drawer', p.console.shots.drawer)}</figure>
<figure>${view('search', p.console.shots.search)}</figure>
</div></article>
<article class="prod" id="terminal" aria-label="${esc(p.terminal.label)}">${prodCopy(p.terminal)}${ctx.tui ? `<div class="term-wrap"><div class="term" role="img" aria-label="${esc(p.terminal.shot)}"><pre>${ctx.tui}</pre></div></div>` : ''}</article>
<article class="prod" id="desktop" aria-label="${esc(p.desktop.label)}">${prodCopy(p.desktop, ctx.desktop ? `<div class="cta"><a class="btn" href="#tab-demo-desktop">${esc(p.desktop.try)}${ICON.arrow}</a></div>` : '')}</article>
</section>`;

  const features = `<section class="sec" id="details" aria-labelledby="details-title">${secHead(c.features, 'details-title')}
<ul class="features">${c.features.items
    .map((f) => `<li><a href="${BLOB}/${f.doc}"><h3>${esc(f.title)}${ICON.arrow}</h3><p>${inline(f.text)}</p></a></li>`)
    .join('')}</ul></section>`;

  const q = c.quickStart;
  const steps = (list, code) =>
    `<ol class="qs">${list
      .map((s, i) => {
        const blocks = (code[i] ?? []).map((b) => codeBlock(ui, b.lines, { label: b.label })).join('');
        return `<li><span class="num">${i + 1}</span><div class="qs-body"><div><h3>${esc(s.title)}</h3><p>${inline(s.text)}</p></div>${blocks ? `<div class="qs-code">${blocks}</div>` : ''}</div></li>`;
      })
      .join('')}</ol>`;
  const quick = `<section class="sec" id="quick-start" aria-labelledby="quick-title">${secHead(q, 'quick-title')}
${tabs(q.kicker, [
  { id: 'binary', label: q.tabs.binary, html: steps(q.binary.steps, CODE.quickStart.binary) },
  { id: 'docker', label: q.tabs.docker, html: steps(q.docker.steps, CODE.quickStart.docker) },
])}
<aside class="callout"><p class="kicker">${esc(q.nas.label)}</p><p>${inline(q.nas.text)} <a href="${BLOB}/docs/deployment.md#the-wrapper">${esc(q.nas.link)}</a></p></aside>
<p class="more">${inline(q.more)}</p>
</section>`;

  const a = c.agents;
  const agents = `<section class="sec" id="agents" aria-labelledby="agents-title">${secHead(a, 'agents-title')}
${tabs(a.kicker, [
  { id: 'cli', label: a.tabs.cli, html: `${codeBlock(ui, ctx.cli, { label: 'shell', copy: false, wrap: true })}<p class="panel-note">${inline(a.install)} <a href="${BLOB}/docs/cli.md">${esc(a.cliDoc)}</a></p>` },
  { id: 'mcp', label: a.tabs.mcp, html: `${codeBlock(ui, ctx.mcp[0], { label: 'shell', copy: false, wrap: true })}${codeBlock(ui, ctx.mcp[1], { label: 'mcp.json', copy: false })}<p class="panel-note">${inline(a.mcpNote)}</p>` },
])}</section>`;

  const notice = `<aside class="notice" aria-label="${esc(c.notice.label)}"><p class="kicker">${esc(c.notice.label)}</p><p>${inline(c.notice.text)}</p></aside>`;

  const links = `<nav class="links" aria-labelledby="links-title"><h2 id="links-title" class="kicker">${esc(c.links.title)}</h2><ul>${c.links.items
    .map((l) => `<li><a href="${esc(expand(l.href))}"><span>${esc(l.title)}${ICON.out}</span><small>${esc(l.text)}</small></a></li>`)
    .join('')}</ul></nav>`;

  const footer = `<footer class="foot"><span>AMDL · ${esc(c.footer.license)}</span><span class="foot-links"><a href="${BLOB}/docs/README.md">${esc(c.footer.docs)}</a><a href="${REPO}/releases">${esc(c.footer.releases)}</a><a href="${REPO}">${esc(c.footer.github)}</a></span></footer>`;

  return `<!doctype html>
<html lang="${lang.code}">
<head>
${head.join('\n')}
</head>
<body>
<a class="skip" href="#main">${esc(ui.skip)}</a>
<div class="frame">
${header}
<main id="main">
${hero}
${demo}
${stats}
${how}
${products}
${features}
${quick}
${agents}
${notice}
${links}
</main>
${footer}
</div>
<script>${ctx.js}</script>
</body>
</html>
`;
}

// The root page sends a visitor to their language: the one they picked last,
// else the first Chinese or English entry in navigator.languages, else English.
export function renderRoot(base) {
  const links = base ? LANGS.map((l) => `<link rel="alternate" hreflang="${l.code}" href="${base}${l.code}/">`).join('\n') : '';
  return `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>AMDL</title>
${links}
${base ? `<link rel="alternate" hreflang="x-default" href="${base}">` : ''}
<link rel="icon" href="favicon.svg" type="image/svg+xml">
<script>(function(){var l;try{l=localStorage.getItem(${JSON.stringify(LANG_KEY)})}catch(e){}if(l!=="en"&&l!=="zh-CN"){l="en";var a=navigator.languages||[navigator.language||""];for(var i=0;i<a.length;i++){var s=String(a[i]).toLowerCase();if(s.indexOf("zh")===0){l="zh-CN";break}if(s.indexOf("en")===0)break}}location.replace(l+"/"+location.hash)})()</script>
<style>body{font:16px/1.6 system-ui,sans-serif;margin:0;display:grid;place-items:center;min-height:100vh;background:#fff;color:#0a0a0a}@media(prefers-color-scheme:dark){body{background:#0a0a0a;color:#fafafa}}a{color:inherit;margin:0 .75em}</style>
</head>
<body>
<p><a href="en/" hreflang="en">English</a><a href="zh-CN/" hreflang="zh-CN" lang="zh-CN">简体中文</a></p>
</body>
</html>
`;
}

// Drops comments and the whitespace around punctuation. Safe for site.css,
// which has no strings that contain these characters and no selector where a
// colon or comma is followed by a space that matters.
const squeeze = (css) =>
  css
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/\s*([{};])\s*/g, '$1')
    .replace(/([,:])\s+/g, '$1')
    .replace(/;}/g, '}')
    .trim();

export function loadContent(code) {
  return JSON.parse(fs.readFileSync(path.join(here, 'content', `${code}.json`), 'utf8'));
}

export function build({ out, base = '', desktop = '', tui = '' }) {
  const staticDir = path.join(here, 'static');
  const read = (rel) => fs.readFileSync(path.join(staticDir, rel), 'utf8');
  const shared = {
    css: squeeze(`${read('site.css')}\n${read('demo.css')}`),
    js: read('site.js').trim(),
    tui: tui ? terminalFrame(tui) : '',
    hero: read('hero.html').trim(),
    cli: read('cli.txt').trimEnd().split('\n'),
    mcp: read('mcp.txt').trimEnd().split('\n---\n').map((b) => b.split('\n')),
    base,
    // The page shows the app's icon on the button that starts it.
    desktop: desktop ? { icon: fs.readdirSync(desktop).find((f) => /^icon\.(?:png|svg|webp)$/.test(f)) ?? '' } : null,
  };

  fs.rmSync(out, { recursive: true, force: true });
  fs.mkdirSync(out, { recursive: true });
  fs.copyFileSync(path.join(staticDir, 'favicon.svg'), path.join(out, 'favicon.svg'));
  buildDemo(out, { desktop });

  const pages = [];
  for (const lang of LANGS) {
    const other = LANGS.find((l) => l !== lang);
    const html = renderPage(loadContent(lang.code), { ...shared, lang, other });
    fs.mkdirSync(path.join(out, lang.code), { recursive: true });
    fs.writeFileSync(path.join(out, lang.code, 'index.html'), html);
    pages.push(`${lang.code}/index.html`);
  }
  fs.writeFileSync(path.join(out, 'index.html'), renderRoot(base));
  pages.push('index.html');
  return pages;
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  let opts;
  try {
    opts = parseArgs(process.argv.slice(2));
  } catch (err) {
    if (!(err instanceof UsageError)) throw err;
    console.error(`${err.message}\n\n${USAGE}`);
    process.exit(2);
  }
  const pages = build(opts);
  console.log(`wrote ${pages.join(', ')} to ${opts.out}`);
  if (opts.serve) serve(opts.out, opts.serve);
}
