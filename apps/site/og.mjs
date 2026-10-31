#!/usr/bin/env node
// Draws the Open Graph card into a built site: static/og.html with the English
// hero's words, shot at 1200 x 630 by headless Chrome, as <out>/og.png. Social
// sites show a picture, not the page, so this is the site's one image, and it
// is drawn from the current text on every build. CHROME names the browser
// binary; otherwise the usual names and install paths are tried.
import { spawnSync } from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

import { esc } from './blocks.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
export const OG_SIZE = { width: 1200, height: 630 };

/** static/og.html filled from a content file's hero. */
export function ogHtml(c) {
  const fill = {
    kicker: esc(c.hero.eyebrow),
    title: esc(c.hero.title).replaceAll('\n', '<br>'),
    subtitle: esc(c.hero.subtitle),
  };
  return fs.readFileSync(path.join(here, 'static', 'og.html'), 'utf8').replace(/\{\{(\w+)\}\}/g, (_, k) => {
    if (!(k in fill)) throw new Error(`static/og.html has an unknown field {{${k}}}`);
    return fill[k];
  });
}

export function findChrome(env = process.env) {
  if (env.CHROME) return env.CHROME;
  const names = ['google-chrome', 'google-chrome-stable', 'chromium', 'chromium-browser'];
  for (const dir of (env.PATH || '').split(path.delimiter)) {
    for (const name of names) if (dir && fs.existsSync(path.join(dir, name))) return path.join(dir, name);
  }
  const mac = '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome';
  if (fs.existsSync(mac)) return mac;
  throw new Error('no Chrome found for the Open Graph card: set CHROME to its binary');
}

/** Width and height from a PNG's IHDR chunk. */
export function pngSize(buf) {
  if (buf.toString('ascii', 1, 4) !== 'PNG' || buf.toString('ascii', 12, 16) !== 'IHDR') throw new Error('not a PNG file');
  return { width: buf.readUInt32BE(16), height: buf.readUInt32BE(20) };
}

export function renderOg(out, { chrome = findChrome() } = {}) {
  const content = JSON.parse(fs.readFileSync(path.join(here, 'content', 'en.json'), 'utf8'));
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'amdl-og-'));
  try {
    const page = path.join(tmp, 'og.html');
    fs.writeFileSync(page, ogHtml(content));
    const png = path.join(out, 'og.png');
    // The page is ours and local, so Chrome's sandbox adds nothing; CI
    // runners refuse it.
    const run = spawnSync(
      chrome,
      [
        '--headless',
        '--no-sandbox',
        '--disable-gpu',
        '--hide-scrollbars',
        '--force-device-scale-factor=1',
        `--window-size=${OG_SIZE.width},${OG_SIZE.height}`,
        `--user-data-dir=${path.join(tmp, 'profile')}`,
        `--screenshot=${png}`,
        pathToFileURL(page).href,
      ],
      { encoding: 'utf8', timeout: 60_000 },
    );
    if (run.error) throw run.error;
    if (run.status !== 0 || !fs.existsSync(png)) throw new Error(`Chrome did not draw the card (exit ${run.status}):\n${run.stderr}`);
    const size = pngSize(fs.readFileSync(png));
    if (size.width !== OG_SIZE.width || size.height !== OG_SIZE.height) throw new Error(`og.png is ${size.width}x${size.height}`);
    return png;
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  const [flag, out] = process.argv.slice(2);
  if (flag !== '--out' || !out) {
    console.error('usage: node apps/site/og.mjs --out <dir>');
    process.exit(2);
  }
  console.log(`wrote ${renderOg(path.resolve(out))}`);
}
