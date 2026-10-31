# apps/site

The static website, built by `apps/site/build.mjs` from one template and a content file
per language. How to build, preview and publish: [README.md](README.md).

## Rules

- No dependencies, no CDN, no web fonts, no analytics: the pages load nothing from
  another host. `build.test.mjs` fails on a remote script, stylesheet, image or CSS
  import.
- `content/en.json` and `content/zh-CN.json` keep the same keys and list lengths and
  change in the same commit. The Chinese is natural Simplified Chinese, not a literal
  translation.
- The site describes what is on the branch it is published from. A feature or product
  that is not released is marked as in development; a behavior change that touches a
  claim here updates the content in the same commit.
- Links into the repository go through `{repo}` and `{blob}`, which follow
  `build.mjs#REF`; links inside the site stay relative.
- The page has no screenshots or fixed captures: the console views are the demo at a
  route (`demo/bundle.mjs#SCENES`), the terminal frame and `og.png` are made by
  `make site`. The tests reject private addresses in the built pages, and the build
  rejects a terminal frame that shows one.
- `static/hero.html` stays decorative (`aria-hidden`), animates only `transform` and
  `opacity`, and stops on `prefers-reduced-motion`.
- Theme follows `prefers-color-scheme` unless `data-theme` is set on `<html>`, and every
  color is a custom property on `:root` in `static/site.css`, defined for both themes.
- The live demo runs `apps/console/static` unchanged; `demo/` adds only the fake backend.
  Its names and covers are made up and it reaches no other host. `demo.test.mjs` checks
  both, and that every `/api/v1` call in the console's `api.js` gets an answer.

## Verify

```bash
node --test apps/site/
node apps/site/build.mjs --serve 8080
```

Then check both languages at 375, 768 and 1440 px, in light and dark, and click through
the demo: Queue, a job, New download with a search, Settings.
