# apps/site

The AMDL website: one static page in English and one in Simplified Chinese, each at its
own path (`en/`, `zh-CN/`), plus a root page that sends a visitor to the language they
picked last or, failing that, the one their browser prefers.

It is plain HTML and CSS built by one zero-dependency Node script, `build.mjs`: a
template, two content files and a handful of static assets are all it takes, so a static
site generator would add more than it saves. Fonts are the system's, and the pages load
nothing from another host.

## Layout

| Path | What it holds |
| --- | --- |
| `build.mjs` | The template and the build: renders `content/<lang>.json` into a page, inlines `static/site.css` and `static/site.js`, copies the images |
| `content/` | The page text, one file per language with the same keys. Strings may use `` `code` ``, `**strong**` and `[text](href)`; an href may start with `{repo}` or `{blob}` |
| `static/` | Stylesheet, script, favicon, Open Graph card, the hero animation (`hero.html`), the terminal capture (`tui.html`), the CLI and MCP samples, and the screenshots in `img/` |
| `demo/` | The live console demo: the fake backend (`fake-api.js`), its made-up catalog, the module that installs it (`boot.js`), and `bundle.mjs`, which puts the demo into the build |
| `serve.mjs` | The preview server behind `--serve` |
| `build.test.mjs` | Content parity, links and anchors, local-only resources, image sizes, arguments |
| `demo.test.mjs` | The fake backend answers every console call, stays on its own origin, and runs jobs to the end |

## Build and preview

Node.js 26 (the version the rest of the repository uses):

```bash
node apps/site/build.mjs --serve 8080
```

builds into `apps/site/dist/` and serves it on `http://127.0.0.1:8080/`. Without
`--serve` it only builds. `--out <dir>` writes elsewhere, and `--base <url>` sets the
absolute address the site will be served from, which adds the canonical, `hreflang` and
Open Graph URLs:

```bash
node apps/site/build.mjs --base https://amdl.example.org/
```

Every link inside the site is relative, so the output works at a domain's root or under
a subpath such as a GitHub Pages project site. Links into the repository point at the
branch in `build.mjs#REF`.

```bash
node --test apps/site/
```

runs the tests; `make node-test` includes them.

## Live demo

Under the hero, a window runs the real web console on made-up data, with a second tab
for the desktop app.

- The build copies `apps/console/static` to `demo/console/` and loads `demo/boot.js` in
  front of the console's `main.js`. boot.js replaces `fetch`: calls under `/api/v1`, the
  event and log streams included, go to `demo/fake-api.js`, which keeps the jobs in memory
  and moves the running ones on a timer (`demo/boot.js#TICK_MS`) while the window is on
  screen; the console's own files load as usual; any other host fails like a network
  error, and the page's Content-Security-Policy allows only its own origin. Links out of
  the demo do nothing.
- Artists, albums, playlists and covers come from `demo/catalog.js`: made up, with each
  cover drawn as an SVG color field.
- `demo.test.mjs` checks that every `/api/v1` call in `apps/console/static/js/api.js`
  gets an answer, so a new console call needs a handler in `fake-api.js`. Nothing checks
  that a response keeps its shape: click through the demo after changing one.
- The desktop tab shows the `desktop` screenshot. Given the desktop app's web build with
  `--desktop <dir>` (a directory with its `index.html`), the build copies it to
  `demo/desktop/`, and a button over the screenshot starts it in the window: it is several
  megabytes of wasm, so it loads only then, and only where the window is at least 820 px
  wide, the app's minimum. The page passes its theme in as `data-mygo-theme`.
- Up to 720 px wide the console window is a scaled-down live view; a tap opens
  `demo/console/` full screen, where the console lays itself out for the screen.

## Publishing

GitHub Pages serves the site, built by `.github/workflows/pages.yml`. A push to the
branch in `build.mjs#REF` that touches the site, the desktop app or the workflow builds
the desktop demo with `make desktop-web`, builds the site with
`--base https://amdl.lyjw.me/` and deploys it; `gh workflow run pages.yml --ref <branch>`
runs it by hand. The workflow refuses a branch other than the one `REF` names, so the
page's install commands and repository links point at the branch it was built from. The
`github-pages` environment lets `dev` and `main` deploy:

```bash
gh api repos/AMDL-Web/apple-music-downloader/environments/github-pages/deployment-branch-policies \
  --jq '.branch_policies[].name'
```

The domain is in the maintainer's Cloudflare zone: `amdl` is a CNAME to
`amdl-web.github.io` with the proxy on, and the repository's Pages settings name
`amdl.lyjw.me` as the custom domain. Cloudflare holds the certificate, redirects `http`
to `https` (the zone's Always Use HTTPS) and sends the files Brotli-compressed, the
desktop demo's `.wasm` included: GitHub Pages answers with gzip, which Cloudflare would
pass through, so a compression rule on `amdl.lyjw.me` (Brotli first, then Auto) has it
re-encode them. It reaches
GitHub over HTTPS without checking GitHub's certificate (the zone's SSL mode Full):
GitHub cannot issue a certificate for a proxied name, so Pages' own certificate and its
Enforce HTTPS setting stay off.

## Release day

The site follows `dev` until a release and `main` from the release on.

1. **Point the site at `main`.** One commit on `dev`, before the release PR:
   `build.mjs#REF` becomes `'main'`, the hero eyebrow in both content files drops "in
   development" ("2.0 · Self-hosted · Open source", "2.0 · 自托管 · 开源"), and
   `.github/workflows/pages.yml` runs on pushes to `main` instead of `dev`. From this
   commit on, pushes to `dev` no longer publish the site. Check: `node --test apps/site/`,
   and `rg -n "in development|开发中" apps/site/content` finds nothing.
2. **Merge the release PR.** Its push to `main` runs the Pages workflow. Check:
   `gh run list --workflow pages.yml --branch main --limit 1`, then
   `timeout 900 gh run watch <run-id> --exit-status`; afterwards
   `curl -sI https://amdl.lyjw.me/en/` answers `200`.
3. **Check the page's links.** Every link into the repository on the live page (the docs,
   the install scripts, the Compose file) answers `200` and points at `main`, and the
   install script finds the release:

   ```bash
   curl -s https://amdl.lyjw.me/en/ \
     | grep -oE 'https://(raw\.githubusercontent\.com|github\.com)/AMDL-Web/[^"<# ]*' | sort -u \
     | xargs -n1 curl -so /dev/null -w '%{http_code} %{url_effective}\n'
   curl -fsSL https://raw.githubusercontent.com/AMDL-Web/apple-music-downloader/main/deploy/install/install.sh | sh -s -- --dry-run
   ```

## Changing the content

- Edit both `content/en.json` and `content/zh-CN.json` in the same change. The Chinese
  is written for Chinese readers, not translated word for word.
- Every claim comes from the repository: the root README, `docs/`, or what the code
  does. A feature that is not released says so.
- Screenshots are WebP in `static/img`, named `<shot>-<light|dark>-<width>.webp`, two
  widths per theme as `build.mjs#SHOTS` lists. Take them from a simulate-mode server
  (`make dev`) or a test install, with no personal paths, addresses or account names in
  view.
- `static/hero.html` is the hero animation: one inline SVG with its own `<style>`
  (`ah-*` classes, `--ah-*` custom properties for both themes) and an optional script
  that pauses it off screen and adds pointer parallax. Its ids are `ah-*`, so a page holds
  one. `--ah-hole` is the page background, the color the record's centre hole shows.
- The terminal capture is `tmux capture-pane -e` output converted to HTML spans; a wide
  (CJK) character sits in a `<span class="w">` so the columns line up.

## Theme and language

The page follows `prefers-color-scheme` until the visitor picks a theme with the toggle,
which sets `data-theme` on `<html>` and keeps the choice in `localStorage`
(`amdl-site-theme`). Each language page records itself as the visitor's language
(`amdl-site-lang`) for the root page to use next time.
