import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, before, describe, test } from 'node:test';
import { fileURLToPath } from 'node:url';

import { ansiToHtml } from './ansi.mjs';
import { commands } from './blocks.mjs';
import { LANGS, PRIVATE_ADDRESS, REF, REPO, UsageError, build, inline, loadContent, parseArgs, terminalFrame } from './build.mjs';
import { SCENES } from './demo/bundle.mjs';
import { OG_SIZE, findChrome, ogHtml, pngSize, renderOg } from './og.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const script = path.join(here, 'build.mjs');

describe('arguments', () => {
  test('defaults', () => {
    const opts = parseArgs([]);
    assert.equal(opts.out, path.join(here, 'dist'));
    assert.equal(opts.base, '');
    assert.equal(opts.serve, 0);
  });

  test('--base gains a trailing slash', () => {
    assert.equal(parseArgs(['--base', 'https://example.org/amdl']).base, 'https://example.org/amdl/');
    assert.equal(parseArgs(['--base', 'https://example.org/']).base, 'https://example.org/');
  });

  for (const [name, argv] of [
    ['an unknown flag', ['--outdir', 'x']],
    ['a flag without a value', ['--out']],
    ['a flag followed by another flag', ['--out', '--base']],
    ['a relative --base', ['--base', 'example.org']],
    ['a non-http --base', ['--base', 'ftp://example.org/']],
    ['a --serve that is not a port', ['--serve', 'abc']],
    ['a --serve out of range', ['--serve', '70000']],
    ['a --desktop without an index.html', ['--desktop', fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-desktop-'))]],
    ['a --tui that is not a file', ['--tui', os.tmpdir()]],
    ['a --tui-web without an index.html', ['--tui-web', fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-tui-web-'))]],
  ]) {
    test(`rejects ${name}`, () => assert.throws(() => parseArgs(argv), UsageError));
  }

  test('bad arguments print usage and exit 2', () => {
    const run = spawnSync(process.execPath, [script, '--nope'], { encoding: 'utf8' });
    assert.equal(run.status, 2);
    assert.match(run.stderr, /unknown argument: --nope/);
    assert.match(run.stderr, /^usage: /m);
  });
});

describe('content', () => {
  // Both languages render from one template, so they need the same shape.
  // A stat's unit is the one string that may be empty.
  const shape = (value, at) => {
    if (Array.isArray(value)) return value.map((v, i) => shape(v, `${at}[${i}]`));
    if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([k, v]) => [k, shape(v, `${at}.${k}`)]));
    assert.equal(typeof value, 'string', `${at} is not a string`);
    if (!at.endsWith('.unit')) assert.notEqual(value.trim(), '', `${at} is empty`);
    return typeof value;
  };

  test('every language has the same keys and list lengths, and no empty strings', () => {
    const [first, ...rest] = LANGS.map((l) => shape(loadContent(l.code), l.code));
    for (const other of rest) assert.deepEqual(other, first);
  });

  test('a copy button takes whole commands, continuation lines included', () => {
    const block = ["$ curl -X POST http://x/downloads \\", "    -d '{}'", '{"accepted":1}', '', '$ curl -N http://x/events', 'PS> irm http://x | iex'];
    assert.equal(commands(block), "curl -X POST http://x/downloads \\\n    -d '{}'\ncurl -N http://x/events\nirm http://x | iex");
  });

  test('inline marks', () => {
    assert.equal(inline('run `a <b>` now'), 'run <code>a &lt;b&gt;</code> now');
    assert.equal(inline('**Settings › Account**'), '<strong>Settings › Account</strong>');
    assert.equal(
      inline('see [the guide]({blob}/docs/deployment.md)'),
      `see <a href="${REPO}/blob/${REF}/docs/deployment.md">the guide</a>`,
    );
    assert.equal(inline('`**not bold**`'), '<code>**not bold**</code>');
  });
});

describe('terminal frame', () => {
  const tmp = (text) => {
    const file = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-tui-')), 'tui.ansi');
    fs.writeFileSync(file, text);
    return file;
  };

  test('escapes become spans, wide characters take two cells and tabs line up', () => {
    const frame = '\x1b[38;2;217;119;87m✻ \x1b[1;38;2;232;230;220mamdl\x1b[0m <b>\n\x1b]8;;https://x\x1b\\\x1b[4mlink\x1b[24m\x1b]8;;\x1b\\ 雾港\tx  \n\n';
    assert.equal(
      ansiToHtml(frame),
      '<span style="color:#d97757">✻ </span><span class="b" style="color:#e8e6dc">amdl</span> &lt;b&gt;\n<span class="u">link</span> <span class="w">雾</span><span class="w">港</span>       x',
    );
  });

  test('block glyphs and the vertical line reach the line edges', () => {
    assert.equal(
      ansiToHtml('\x1b[38;2;251;113;133m█▄\x1b[0m ▀ │x'),
      '<span style="color:#fb7185"><span class="kf">█</span><span class="kl">▄</span></span> <span class="ku">▀</span> <span class="kv">│</span>x',
    );
  });

  test('a frame given with --tui is on the page', () => {
    const out = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-site-'));
    try {
      build({ out, tui: parseArgs(['--tui', tmp('\x1b[1mamdl\x1b[0m 2.0.0')]).tui });
      assert.match(fs.readFileSync(path.join(out, 'en', 'index.html'), 'utf8'), /<pre role="img" aria-label="[^"]+"><span class="b">amdl<\/span> 2\.0\.0<\/pre>/);
    } finally {
      fs.rmSync(out, { recursive: true, force: true });
    }
  });

  test('a TUI web build given with --tui-web starts in place of the frame', () => {
    const web = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-tui-web-'));
    const out = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-site-'));
    try {
      fs.writeFileSync(path.join(web, 'index.html'), '<!doctype html><title>amdl</title>');
      assert.throws(() => parseArgs(['--tui-web', web]), /--tui-web needs --tui/);
      const opts = parseArgs(['--tui', tmp('amdl'), '--tui-web', web]);
      build({ out, tui: opts.tui, tuiWeb: opts.tuiWeb });
      assert.ok(fs.existsSync(path.join(out, 'demo', 'terminal', 'index.html')));
      const html = fs.readFileSync(path.join(out, 'en', 'index.html'), 'utf8');
      assert.match(html, /<pre role="img"[^>]*>amdl<\/pre><iframe data-src="\.\.\/demo\/terminal\/\?theme=dark"[^>]*><\/iframe><button class="term-start" type="button">/);
    } finally {
      fs.rmSync(web, { recursive: true, force: true });
      fs.rmSync(out, { recursive: true, force: true });
    }
  });

  test('a frame that shows a private address is refused', () => {
    assert.throws(() => terminalFrame(tmp('nas (http://192.168.3.5:18570)')), /private address 192\.168\.3\.5/);
  });
});

describe('Open Graph card', () => {
  test('the card carries the English hero', () => {
    const c = loadContent('en');
    const html = ogHtml(c);
    assert.doesNotMatch(html, /\{\{/);
    assert.ok(html.includes(c.hero.subtitle));
  });

  test('pngSize reads a PNG header and rejects other files', () => {
    const head = Buffer.alloc(24);
    head.write('\x89PNG\r\n\x1a\n', 0, 'latin1');
    head.write('IHDR', 12, 'ascii');
    head.writeUInt32BE(1200, 16);
    head.writeUInt32BE(630, 20);
    assert.deepEqual(pngSize(head), { width: 1200, height: 630 });
    assert.throws(() => pngSize(Buffer.from('not an image at all, honestly')), /not a PNG/);
  });

  let chrome = '';
  try {
    chrome = findChrome();
  } catch {}
  test('Chrome draws it at its size', { skip: !chrome && 'no Chrome here' }, () => {
    const out = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-og-'));
    try {
      assert.deepEqual(pngSize(fs.readFileSync(renderOg(out, { chrome }))), OG_SIZE);
    } finally {
      fs.rmSync(out, { recursive: true, force: true });
    }
  });
});

describe('desktop demo', () => {
  test('a desktop build given with --desktop is embedded', () => {
    const web = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-desktop-'));
    const out = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-site-'));
    try {
      fs.writeFileSync(path.join(web, 'index.html'), '<!doctype html><title>AMDL</title>');
      fs.writeFileSync(path.join(web, 'icon.png'), '');
      build({ out, desktop: parseArgs(['--desktop', web]).desktop });
      assert.ok(fs.existsSync(path.join(out, 'demo', 'desktop', 'index.html')));
      const html = fs.readFileSync(path.join(out, 'en', 'index.html'), 'utf8');
      assert.match(html, /<iframe data-src="\.\.\/demo\/desktop\/"/);
      assert.match(html, /<button class="win-start" type="button"><img src="\.\.\/demo\/desktop\/icon\.png"/);
      assert.match(html, /id="tab-demo-desktop"/);
    } finally {
      fs.rmSync(web, { recursive: true, force: true });
      fs.rmSync(out, { recursive: true, force: true });
    }
  });
});

describe('built site', () => {
  let out;
  let pages;
  before(() => {
    out = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-site-'));
    build({ out, base: 'https://example.org/amdl/' });
    pages = Object.fromEntries([...LANGS.map((l) => `${l.code}/index.html`), 'index.html'].map((p) => [p, fs.readFileSync(path.join(out, p), 'utf8')]));
  });
  after(() => fs.rmSync(out, { recursive: true, force: true }));

  test('without a desktop build or a terminal frame, the page has neither', () => {
    for (const lang of LANGS) {
      const html = pages[`${lang.code}/index.html`];
      assert.match(html, /<iframe data-demo data-src="\.\.\/demo\/console\/"/);
      assert.doesNotMatch(html, /demo\/desktop|id="tab-demo-/);
      assert.doesNotMatch(html, /class="term"/);
    }
  });

  test('the console views are the demo at their routes, with no screenshots', () => {
    for (const lang of LANGS) {
      const html = pages[`${lang.code}/index.html`];
      for (const v of Object.values(SCENES)) assert.ok(html.includes(`data-src="../demo/console/${v.route}"`), v.route);
      assert.doesNotMatch(html, /<img |srcset=/);
    }
  });

  test('each language page declares its language and links the other', () => {
    for (const lang of LANGS) {
      const html = pages[`${lang.code}/index.html`];
      assert.match(html, new RegExp(`<html lang="${lang.code}">`));
      assert.match(html, new RegExp(`<link rel="canonical" href="https://example.org/amdl/${lang.code}/">`));
      assert.match(html, /<link rel="alternate" hreflang="x-default" href="https:\/\/example.org\/amdl\/">/);
      for (const other of LANGS) {
        assert.match(html, new RegExp(`<link rel="alternate" hreflang="${other.code}" href="https://example.org/amdl/${other.code}/">`));
      }
    }
  });

  test('the root page offers every language', () => {
    for (const lang of LANGS) assert.match(pages['index.html'], new RegExp(`href="${lang.code}/"`));
  });

  test('every local link, image and anchor resolves', () => {
    for (const [page, html] of Object.entries(pages)) {
      const ids = new Set([...html.matchAll(/\sid="([^"]+)"/g)].map((m) => m[1]));
      const refs = [...html.matchAll(/\s(?:href|src)="([^"]+)"/g)].map((m) => m[1]);
      for (const m of html.matchAll(/\ssrcset="([^"]+)"/g)) refs.push(...m[1].split(',').map((s) => s.trim().split(/\s+/)[0]));
      for (const ref of refs) {
        if (/^https?:/.test(ref)) continue;
        if (ref.startsWith('#')) {
          assert.ok(ids.has(ref.slice(1)), `${page}: no element with id ${ref}`);
          continue;
        }
        const target = path.join(out, path.dirname(page), ref);
        const file = ref.endsWith('/') ? path.join(target, 'index.html') : target;
        assert.ok(fs.existsSync(file), `${page}: ${ref} does not exist`);
      }
    }
  });

  test('pages load nothing from another host', () => {
    for (const [page, html] of Object.entries(pages)) {
      assert.doesNotMatch(html, /<script[^>]+src=/, `${page} loads a script`);
      assert.doesNotMatch(html, /<link[^>]+rel="(?:stylesheet|preload|preconnect|dns-prefetch)"/, `${page} links a resource`);
      assert.doesNotMatch(html, /<img[^>]+src="https?:/, `${page} loads a remote image`);
      assert.doesNotMatch(html, /@import|url\(\s*["']?https?:/, `${page} pulls in remote CSS`);
    }
  });

  test('every image has a size and alt text', () => {
    for (const lang of LANGS) {
      for (const [img] of pages[`${lang.code}/index.html`].matchAll(/<img [^>]+>/g)) {
        assert.match(img, /\swidth="\d+" height="\d+"/, img);
        assert.match(img, /\salt="[^"]*"/, img);
      }
    }
  });

  test('no private addresses leak into the pages', () => {
    for (const [page, html] of Object.entries(pages)) {
      assert.doesNotMatch(html, PRIVATE_ADDRESS, page);
    }
  });
});
