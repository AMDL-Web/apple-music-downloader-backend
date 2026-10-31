import assert from 'node:assert/strict';
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { after, before, describe, test } from 'node:test';
import { fileURLToPath } from 'node:url';

import { commands } from './blocks.mjs';
import { LANGS, REF, REPO, SHOTS, UsageError, build, inline, loadContent, parseArgs, webpSize } from './build.mjs';

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

describe('images', () => {
  const dir = path.join(here, 'static', 'img');

  test('each file is as wide as its name says', () => {
    for (const file of fs.readdirSync(dir)) {
      const width = Number(/-(\d+)\.webp$/.exec(file)?.[1]);
      assert.ok(width, `${file} does not end in -<width>.webp`);
      assert.equal(webpSize(fs.readFileSync(path.join(dir, file))).width, width, file);
    }
  });

  test('every screenshot exists in both themes and every width', () => {
    for (const [name, spec] of Object.entries(SHOTS)) {
      for (const theme of ['light', 'dark']) {
        for (const w of spec.widths) assert.ok(fs.existsSync(path.join(dir, `${name}-${theme}-${w}.webp`)), `${name}-${theme}-${w}.webp`);
      }
    }
  });

  test('webpSize rejects other files', () => {
    assert.throws(() => webpSize(Buffer.from('not an image at all, honestly')), /not a WebP/);
  });
});

describe('desktop demo', () => {
  test('a desktop build given with --desktop is embedded', () => {
    const web = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-desktop-'));
    const out = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-site-'));
    try {
      fs.writeFileSync(path.join(web, 'index.html'), '<!doctype html><title>AMDL</title>');
      build({ out, desktop: parseArgs(['--desktop', web]).desktop });
      assert.ok(fs.existsSync(path.join(out, 'demo', 'desktop', 'index.html')));
      assert.match(fs.readFileSync(path.join(out, 'en', 'index.html'), 'utf8'), /<iframe data-src="\.\.\/demo\/desktop\/"/);
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

  test('without a desktop build, the desktop demo is a screenshot', () => {
    for (const lang of LANGS) {
      const html = pages[`${lang.code}/index.html`];
      assert.match(html, /<iframe src="\.\.\/demo\/console\/"/);
      assert.doesNotMatch(html, /demo\/desktop/);
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
        assert.match(img, /\salt="[^"]+"/, img);
      }
    }
  });

  test('no private addresses leak into the pages', () => {
    for (const [page, html] of Object.entries(pages)) {
      assert.doesNotMatch(html, /\b(?:127\.0\.0\.1|192\.168\.\d+\.\d+|10\.\d+\.\d+\.\d+)\b/, page);
    }
  });
});
