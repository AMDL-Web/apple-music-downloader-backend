# Console design

The shared contract for the embedded console: structure, states, accessibility,
motion and the skin layer. How the console *looks* belongs to its one skin,
`music`, specified in [skins/music.md](skins/music.md). When this page and the code disagree, the code
wins and this page is fixed in the same change. Sources are listed in
[docs/references.md](../../docs/references.md#console-design-references).

## Principles

1. **Show only what the API knows.** Lists show track counts, and while a
   running job has nothing finished yet, its overall progress: the API sends
   it as `Job.progress` (`internal/progress#Overall`), the mean of each
   track's stage-weighted progress. The job drawer's album bar computes the
   same from the items it has (`lib/album-progress.js`), each stage weighted by
   its measured share of a track's time (the weights in `lib/status.js`),
   because the track count alone stands still while a long track downloads. Simulate mode is
   announced by a "Simulate" tag in the top bar, whose title says what it means.
2. **Status is never color alone.** Every status has an icon (or glyph), a label
   and a color role; the skin may change the look of a role, never drop the label.
3. **Artwork colors are data.** Apple's per-album palette reaches the page as
   custom properties; text colors are contrast-checked before they are used.
4. **Both themes.** The skin ships a light and a dark theme; light is
   designed, not inverted.
5. **English UI copy**, `lang="en"`. Apple-provided text (titles, artists,
   genres) is shown as received with `dir="auto"`.
6. **Say a fact once, and only what the maintainer acts on.** A fact appears
   in one place on a screen. No explanatory sentence under a page title and no
   text about what a card is for; a view holds data, state and actions. A
   destructive action's confirmation keeps one sentence on its consequence.

## Information architecture

| Route | Page | Content |
| --- | --- | --- |
| `#/` (also `#/overview`) | Overview | Sections (in DOM order) a server-health strip (`.health-strip`, one line when healthy, a notice per severity naming each Settings domain that needs action (`lib/domains.js` `attentionRows`), each linking to its section, and Status to the Settings board), `attention` (only while a job failed in the last 24 h: up to 2, each with Retry unless Apple has nothing at its link), `live` (8 active jobs, each opening its drawer over the Overview; when none, a card shaped like a job's, its link Log in while no account is logged in), `recent` (the latest release as a banner, then two rows of earlier ones, one tile per album; See all is the Queue's Completed tab), `kpis` (Queued, Running, Completed, Failed: label and number only), `throughput` (from `/stats`; one sentence under three days of data) |
| `#/queue?tab=all\|active\|completed\|failed\|cancelled&type=&q=` | Queue | Tabs with counts (All first and the default) from the feed's `status_counts`, search over title, artist and URL, type filter, dense job list, Load more, selection mode (Retry, Cancel, Delete on many jobs), Retry all on the Failed and Cancelled tabs (leaving out links Apple has nothing at) |
| `#/overview/:id`, `#/queue/:id`, `#/new/:id` | Job drawer | Over its page: header with a summary line ("ALAC 24/44.1 · 14 tracks · 692 MB", the size once every track has finished), the failure reason, Cancel/Retry/Remove, the tracks (one line each; stage meters only on a track that is working or has an outcome, and on every track once the drawer has shown the job live), Files (completed jobs), and a closed Details (times, job ID, Activity with persisted history plus live events, hooks) |
| `#/logs?level=&component=&job_id=&request_id=&q=` | Logs | Level chips, component, search, filter chips, live tail, expandable rows; the page head links to Settings › Logs |
| `#/new?url=&q=&artist=&group=` | New download | The page head, then a column `--compose-w` wide at most: the compose field (the text box) with the quality preset under it, section `links` (a row per link found) or the search results (catalog hits for words without a link, in sections `hits-albums`, `hits-songs`, `hits-playlists`, `hits-artists`, `--stage-max` wide at most), the dock (count, the quality it will download or asks for, Download) while the box holds links, `results` (after a submit), and in the empty page a closed "Browser shortcut" disclosure. `url` carries one or more links, newline- or space-separated and URL-encoded (`lib/route.js` `newDownloadHash`, `newDownloadUrls`), and follows the text box; `lib/links.js` parses them. `q` is the search term (`newSearchHash`), which follows the text box too, `artist` the artist whose albums the search card shows, a pushed entry so Back returns to the hits, and `group` the group shown alone (`lib/search.js` `searchGroup`), pushed from every group and replaced between groups, kept while the term changes and dropped with it |
| `#/settings?section=&field=` | Settings | The server's status and its runtime configuration on one page. Without a section, the status board: one tile per domain with a status (`lib/settings-sections.js` `BOARD_SECTIONS`), each a link to its section (under 640 px the list of sections instead, icon, label, live one-line summary, mark and chevron, in two groups; a section then opens as its own view headed by a "Settings" back link, the page title kept for screen readers only). Nine sections (`SETTINGS_SECTIONS`; `section` is lowercase words joined by `_`, `settingsSection`): `account`, `quality`, `files`, `lyrics_artwork`, `jobs`, then `library_sync`, `hooks`, `logging`, `server`. Each is one card: a status head (`views/settings-head.js` `DomainHead`; Lyrics & artwork has no status, so a plain head), then its fact rows (startup values from `/system`, `lib/facts.js`; first, so their late answer moves nothing), fields and action blocks. Server has sub-headed groups (`SETTINGS_GROUPS`: `simulate`, `config_file`); a group id as `section` opens Server scrolled to it, and retired ids (`advanced`, `downloading`, `apple_requests`, `downloads`, `artwork`, `lyrics`, `catalog`) open the section that holds their settings now (`sectionById`). A known `field` wins over `section`: its own section opens. `#/system`, the page this one replaced, opens the board and is rewritten in place (`lib/route.js` `PAGE_ALIASES`). The palette finds every section and field, also by words such as `parallel`, `template` or `health` (`lib/palette.js`); a field result opens `#/settings?section=<id>&field=<config key>`, which focuses that field's enabled control, if any, and scrolls it to the middle (`ui/field-focus.js`) |

New download is the primary action and has these entry points: the
`.sidebar-new` button first in the sidebar (icon-only with a tooltip on the
640-1023 rail), "Start a
download" in the Overview live queue and the Queue's empty states, the
tab bar's middle tab under 640 px, `n` (and `g n`), the palette's "New
download", and a global paste: pasting text that holds at least one
music.apple.com link while focus is outside a text field and no overlay or
dialog is open opens `#/new?url=<its links>`, other sites' too so the page
flags them (`lib/links.js` `pastedLinks`; on `#/new` the links join those
already in `url`; from another page they start a new download and replace
the draft). Under 640 px the tab bar holds Overview, Queue, New, Settings and
Logs, five equal tabs with New in the middle; each is current only on its
own page.

Logs is opened seldom, so it is the one quiet entry in the sidebar: its foot
(`.nav-secondary`, the `NAV` entries marked `secondary`); under 640 px it is
the last tab. Besides, under 640 px the Settings list's Logs row opens the
Logs page (its errors when there are some) rather than the section, and
`g l`, the palette, Settings › Logs' Open Logs and a job drawer's View logs
open it too. The Logs page's
head links back to its settings.

Filters live in the hash query, so a filtered view is a link. The sidebar,
the tab bar, `g q` and the palette open the Queue on its last tab, type and
words for the life of the browser tab (`lib/route.js` `queueHash`), else on
All. KPI tiles open the matching Queue tab (Queued and Running the Active tab), and
the live queue's Open queue the Active tab; the drawer's "View logs" opens `#/logs?job_id=…`; a log
row's job chip filters to that job. The tab title reads `"{Page} · AMDL"`, and
`"{n} running · {Page} · AMDL"` while jobs run.

## Layout

The DOM frame is fixed; the skin restyles it.

```
.shell
  .sidebar (nav: .brand > .brand-mark + .brand-text > .brand-name, .brand-sub; a.sidebar-new; .nav > .nav-item; .nav-secondary > .nav-item at the foot)
  .main-col
    header.topbar (.topbar-right: .sim-tag in simulate mode, .conn, .search-trigger, .theme)
    main.main[data-page] (.page-head > h1.page-title + .page-sub; .banners; .page.page-<name>)
  nav.tabbar (> .tab x2, a.tab.tab-new, .tab x2, under 640 px)
```

| Width | Base frame | Notes |
| --- | --- | --- |
| ≥1024 | `--sidebar-w` sidebar, `--topbar-h` top bar | Overview is a 12-column grid, every section 12 wide |
| 640-1023 | `--rail-w` icon rail (labels kept for screen readers) | two-column cards stack |
| <640 | top bar, `--tabbar-h` bottom tab bar + safe area, 16 px gutters | single column, 44 px targets and 16 px fields (also on any coarse pointer at any width) |

Siblings are spaced by the parent's `gap` (`--page-gap` between sections,
`--grid-gap` inside grids, `--card-gap` inside cards), never by child margins.
No horizontal page scroll at 390 px in either theme.

The music skin's rhythm, the same on every page; the ui-audit tool's
`rhythm` check (`apps/console/tools/ui-audit/console/audit.config.mjs`) measures it and
flags a page that differs:

| Relationship (px) | ≥1024 | 640-1023 | <640 | Set by |
| --- | --- | --- | --- | --- |
| Page gutter, page head included | 40 | 24 | 16 | the skin's `--main-x` |
| Page head → content, section → section | 40 | 40 | 32 | `--page-gap` |
| Section title → its card | 12 | 12 | 12 | the skin's titled card (`gap: var(--s-3)`) |
| Inside a card | 20 | 20 | 16 | `--card-pad`, `--card-pad-x` |

New download keeps the gutter; its field, options, cards and dock stop at
`--compose-w` (800 px, 880 from 1680 px, the full width under 640 px), and
its search results at `--stage-max` (1240 px).

## Skin

The skin is the console's look: colors, radii, shadows, font stacks, display
type and a few component rules. There is one, `music`; the theme (system,
light, dark) is a separate axis, and the skin supports all three.

### Mechanism

- `<html data-skin="music">` is static in `index.html`, so the first frame has
  every token before `main.js` runs. Nothing switches it; an `amdl.console.skin`
  value an earlier version stored in the browser is never read.
- The theme is stored per browser under `localStorage["amdl.console.theme"]`
  with a guarded read and write (`lib/prefs.js`: `normalizeTheme`, `readPref`,
  `writePref`); `js/theme-boot.js`, a classic script in `<head>`, puts it on
  `<html>` before the first paint (a module runs only after one paint in the
  system theme), and `main.js` applies it again before mounting.
- The theme is `<html data-theme="light|dark">` or no attribute for "system";
  `css/tokens.css` sets `color-scheme` from it, and the skin writes every color
  as `light-dark(<light>, <dark>)`, which resolves against that. The skin never
  sets `color-scheme`.

### Files and what each may do

| File | Holds | May | May not |
| --- | --- | --- | --- |
| `static/css/tokens.css` | Skin-neutral scales: type scale `--fs-*`/`--lh-*`, weights, spacing `--s-*`, sizes, motion, `--on-art-light/dark`, system font fallbacks | change a shared scale | hold a skin's color, radius, shadow or font stack |
| `static/css/*.css` (the base: `base.css`, `shell.css`, `controls.css`, `layout.css`, one file per page area, `overlays.css`, `responsive.css`, then the newer pages' `download-form.css`, `download.css` and `settings.css`) | Structure: layout, component structure and behavior, all visual values through tokens, one file per area. `index.html` links them after `tokens.css` and before the skin, and that link order is the cascade order: `responsive.css` follows the older areas, and `download-form.css`, `download.css` and `settings.css` follow it, so each carries its own media queries (the phone rules at the end of `download.css` cover both New download files) | add structure, in the file of its area | contain a color literal, `!important` (outside reduced motion) or a skin-specific choice |
| `static/css/skins/<name>.css` | One `:root[data-skin="<name>"]` token block (light and dark via `light-dark()`), responsive token overrides, and that skin's component rules, every selector prefixed `:root[data-skin="<name>"]` | set any token, override any base rule (repeating its selector chain when it is deeper than three classes), show opt-in hooks, reorder overview sections, use CSS counters and `::before` text | leave its scope, add JS, use `font-size` other than `var(--fs-*)`, use `transition: all`, rely on `color-scheme` it sets itself |
| `skins/<name>.md` | The skin's spec: intent, token table with contrast, component notes | | restate the shared contract |

The prefix `:root[data-skin="<name>"]` adds (0,2,0) (`:root` is a
pseudo-class), so a skin selector with one class wins over any base selector
of up to three classes, attributes and pseudo-classes (a tie goes to the
skin, linked later), including those inside base media queries; a skin
therefore carries its own responsive overrides. For a deeper base selector
(the chart hover chain, the Submitted row chain
`.job-list.job-list-compact.new-results > .new-result > .job-row`), repeat
its chain under the skin prefix.

### Tokens the skin must declare

`TestSkinsDeclareEveryBaseToken` fails when the skin misses one that a base
stylesheet reads and `tokens.css` does not declare. Declare the whole table anyway (for
example every role's `-text`, though the base reads only `--danger-text` and
`--warning-text`), so the skin's rules can rely on it.

| Group | Tokens |
| --- | --- |
| Surfaces | `--bg-canvas`, `--bg-surface`, `--bg-raised`, `--bg-hover`, `--bg-selected`, `--bg-chrome` (sidebar, top bar, tab bar) |
| Lines | `--border-subtle` (decorative), `--border-control` (3:1 outlines), `--chrome-border`, `--card-divider`, `--row-divider` (border shorthands) |
| Text | `--text-primary`, `--text-secondary` (4.5:1 on `--bg-surface`), `--text-disabled` (placeholders only) |
| Accent | `--ink`/`--on-ink` (skip link), `--accent`/`--on-accent` (primary button), `--accent-text` (links), `--focus` (2 px ring), `--focus-field` (the ring of a field you type in), `--brand`/`--on-brand` (logo mark) |
| Status roles | `--info`, `--success`, `--danger`, `--warning`, `--neutral`, `--skipped`, each with `-tint` and `-text` |
| Data | `--track` (empty bars and cells), `--chart-bar`, `--chart-grid`, `--scrim` |
| Type | `--font-sans`, `--font-display`, `--font-mono`, `--fs-body`/`--lh-body`, `--tracking-body`, `--fs-page-title`/`--lh-page-title`, `--fs-card-title`/`--lh-card-title`, `--fs-kpi`/`--lh-kpi`, `--fs-hero-title`/`--lh-hero-title`, `--w-display`, `--w-title`, `--tracking-display`, `--tracking-title` |
| Shape | `--r-badge`, `--r-bar`, `--r-control`, `--r-control-inner`, `--r-card`, `--r-art`, `--r-overlay`, `--r-drawer`, `--r-notice`, `--r-icon-tile`, `--r-mark` (data marks), `--r-compose`, `--r-dock` (New download's capsules), `--bar-h`, `--cell-h`, `--cell-gap` |
| Cards | `--card-bg`, `--card-border` (shorthand), `--card-shadow`, `--card-pad`, `--card-pad-x` (flush rows), `--card-gap` |
| Layout | `--page-gap`, `--grid-gap` |
| Elevation | `--shadow-art` (covers), `--shadow-float` (drawer, palette, toasts) |
| New download | `--compose-bg`, `--compose-bg-hover`, `--compose-bg-focus`, `--compose-edge`, `--compose-shadow` (the compose field), `--dock-bg`, `--fs-compose`/`--lh-compose` |

### DOM hooks

Stable class names and attributes the skin can style without touching JS:

| Surface | Hooks |
| --- | --- |
| Section / card | `section.card[data-section]` > `header.card-head` (`h2.card-title`, `.card-note`, `.card-actions`) + `.card-body`; `.card-flush` for edge-to-edge lists. Overview sections: `attention`, `live`, `recent`, `kpis`, `throughput`; Queue `jobs`; Logs `log` |
| Page | `main.main[data-page]`, `.page-head`, `.page-title`, `.page-sub` (Overview: the date; Queue: live counts), `.banners`, `.page.page-<name>` (`page-new`, `page-settings` included) |
| KPI | `a.kpi[data-key=queued\|running\|completed\|failed][data-role][data-zero]` > `.kpi-label`, `.kpi-icon`, `.kpi-value` (no line under the number: a count of zero there said nothing and made the card taller) |
| Job row | `a.job-row[data-status][data-progress=on\|off][.has-palette]` > `.art`, `.job-main` (`.job-facts` > `.job-fact[data-key=type\|genre\|year\|started]`, `.job-title[.is-unresolved]` (a failed link reads "Link not found" for a 404), `.job-sub` (one line: artist, the type unless it is Album, the storefront unless it is the default, the track count of a finished job; a failed link's address), `.job-error` (failed jobs: the reason on its own line, at most two lines, full text in the title attribute)), `.job-progress` (empty unless the job is queued or running; `data-progress=off` sets `--job-prog` to 0 so the title takes the room), `.job-status` (a plain completed job is `.status-text.status-icon[role=img]`, the icon alone; every other status a badge), `.job-updated`, `.job-chevron`. The Queue adds `li.job-item[data-action=retry]` > `.btn.job-retry` after the row for a failed job, unless Apple has nothing at its link (a button cannot sit inside the link); every Queue row reserves the button's width as `--job-trail` (the chevron sits at its end), so the status, the time and Retry line up down the list |
| Progress | `.jprog[data-mode=cells\|bar][data-working]` (a running job with nothing finished yet: the cells fill in order to `Job.progress`, each `.pcell::before` to its share `--f`, or `.jprog-live` fills the bar; translucent text color, or on an album's colour (the Overview's live cards, `--live-fill`) its foreground; the text is that percentage): up to 60 tracks `.pcells` (`--n`) > `.pcell[data-state=completed\|skipped\|failed\|cancelled\|remaining][data-role]` in that order (the list payload has counts, not per-track order); above 60 `.pbar` > `.pbar-seg[data-role]`; both followed by `.jprog-text` ("12 / 14") and `.jprog-legend` > `.jprog-key[data-state][data-role][data-zero]` > `.jprog-key-n` + `.jprog-key-label` (saved, on disk, failed, cancelled, remaining; every state, zeros included). On the Overview's live cards (a `RatesContext`, `ui/job-rate.js`) the legend ends with `.jprog-rate` (`.jprog-rate-word` " left", hidden under 640 px): the running job's time left, on the legend's line, taking what the keys leave and ellipsized before they are. The list payload cannot tell a track in flight from a waiting one, so "remaining" covers both |
| Facts | `dl.facts` > `.fact[data-key][data-role]` > `dt.fact-label` + `dd.fact-value` (throughput totals `.tp-totals` with `tracks`, `tracks-failed`, `bytes`, `jobs`, `jobs-failed`, the failed rows only when non-zero and `data-role=danger`, hero facts `.hero-facts`) |
| Recently completed | `.recent[style=--cols]` > `a.latest[.has-palette][.is-skel]` > `.art`, `.latest-text` (`.latest-kicker` "Latest · Album", `.latest-title`, `.latest-artist`, `.latest-facts`: genre · year · tracks · finished, every line one line); `ul.wall` (two rows of `--cols` from `useColumns`, keyed by place so a new completion changes what tiles show, not where they are) > `li` > `a.wall-tile[.is-skel]` > `.art`, `.wall-title`, `.wall-sub`; one entry per link target (`uniqueByTarget`) |
| Status strip, Activity, failure | Overview `p.health-strip[data-state=ok]` or `.health-strips[role=status]` > one `.health-strip.notice[data-state=problem][data-role=danger\|warning]` per severity, danger first (`attentionGroups`), the last holding `.health-strip-link`; drawer `details.drawer-section.drawer-details` (closed) > `.drawer-details-body` > `dl.drawer-times`, `.drawer-id` (`code` + Copy), `section.activity` > `ol.activity-list` > `li.activity-row[data-role]` (`.activity-time`, `.activity-label`, `.activity-msg`) and a Hooks `section`; failure notice with `details.notice-more` > `pre.notice-raw`; album bar `.abar-wrap` > `.abar[data-ticks][.is-indeterminate]` (`--n` cells) > `.abar-live` (`--w`) + `.abar-done` > `.abar-run[data-state][data-role]` (`--w`, one per run of an outcome), then `.abar-caption` > `.abar-count` (+ `.abar-rate`) + `.abar-pct`; Files `section` > `.drawer-files` > `code.drawer-files-dir` + Copy, `p.drawer-files-note` (several folders only), `a.drawer-files-link`; tracks `ol.tracks` > `li.track[data-status][data-detail]` (`data-detail` on a track with stage meters and a status) > `.track-index`, `.track-main` (`.track-title`, `.track-artist` only when it differs from the album artist), `.track-codec` (only when it differs from the album's), `.track-len`, `.track-size` (the file's size; while a track downloads or decrypts, `.track-pct` with that stage's percentage), then `.track-stages` (`.stages` > `.stage[data-key][data-state=empty\|partial\|done\|indeterminate\|unverified\|skipped]`, each `--w` wide; a track already on disk is a full bar of `skipped`, which the skin draws in its quiet ink), `.track-status` (with `.track-note`, the status message on one line, its full text the tooltip), `.track-error` (a failed track's reason, its own line) for a detail track; Logs `.log-err`, `.attr-chip-job` > `.attr-chip-name`, `.filter-chip-name`, rows `[data-seq]` with the one tab stop |
| Needs attention | `section.card[data-section=attention]` > `ul.attention` > `li.attention-row[data-role=danger]` > `.attention-title` (`.attention-label` "Failed" with a circle-x icon, `a.attention-link` to the drawer, mono `.attention-input` (the link, `linkLabel`; beside a title it gives way first), `.attention-time`), `.attention-detail` (`.attention-error`, clamped to two lines), Retry `.btn` (none for a link Apple has nothing at); `.live-idle-card` is the empty live queue (a `.job-row` without `data-row`: `.live-idle-art`, the title and a Start a download link, with a job card's progress row split into its padding, so it is as tall as a job's card); until Needs attention and the status line have answered (at most 400 ms) the page is `.page-overview[data-held]` and the sections below them are laid out but not drawn (`data-held=strip` hides this card too while the line is unknown), so neither arrives late and pushes the page down |
| Throughput | `.tp-totals` (Facts); `.chart-xaxis` > one `span[data-day]` per day, `.is-first`, `.is-mid`, `.is-latest` on the three the base shows |
| Logs | `.log-entry[data-level]` on log page rows (`.log-row`) > `.log-time`, `.log-level[data-role]`, `.log-comp`, `.log-msg` (`.log-err`, then up to three `key=value` pairs in `.log-attrs`); toolbar `button.level-chip[aria-pressed]` (a pressed one holds a check icon, an unpressed one the hollow `.log-dot`) and `button.wrap-switch[aria-pressed]` > `.wrap-switch-track` "Wrap lines" |
| Shell | `.brand-sub`, `a.sidebar-new.btn.btn-primary[aria-current]` > `.icon` + `.sidebar-new-label` (visually hidden on the rail), `.nav-item[aria-current]`, `.nav-badge`, `ul.nav-secondary` (the sidebar's foot, Logs), `.tab[data-page][aria-current]`, `a.tab.tab-new[aria-label][title]` > `.tab-new-mark` (one large accent pill around the plus, the middle of five tabs, no visible label), `.theme` > `button.theme-cycle` (one button cycling System, Light, Dark; its label names the next), `.sim-tag` (simulate mode, explanation in its title and an `.sr-only` span); loading shell `.boot` > `.boot-mark` + `.boot-text` (static in `index.html`, replaced on mount); `svg.sprite` at the start of `body`, the in-document icon sprite (`ui/icons.js`), never styled by the skin; `meta[name=theme-color]` set from the body background; `.conn[data-role][data-state=connecting\|live\|reconnecting\|offline\|paused]` > `.conn-dot` + `.conn-label` (the base hides the label visually unless `data-state` is `reconnecting` or `offline`, and always under 640 px) |
| Dialogs | `.overlay-host[data-placement=center\|bottom]` > `.scrim` + the panel `[role=dialog\|alertdialog][aria-modal][data-dialog=<name>]` (`ui/dialogs.js` `Dialog`). Confirm: `.sheet.dialog-confirm[role=alertdialog][data-dialog=confirm]` > `h2.dialog-title`, `.dialog-body`, `.dialog-actions[data-choices=3]` > `.btn.dialog-cancel` + `.btn.dialog-confirm-btn[aria-busy]` (`btn-danger` when destructive), in drawn order: with three choices `.dialog-confirm-btn` first and `.btn.dialog-alt-btn` last, on a phone sheet the confirm button before Cancel and `.dialog-alt-btn` first; a busy button is `.btn[aria-disabled=true]`; a bottom panel draws its grabber as `::before`. Shortcut sheet `.sheet.sheet-shortcuts[data-dialog=shortcuts]` > `.sheet-head` + `.shortcut-body` > `.shortcut-groups` > `section.shortcut-group` > `h3.shortcut-group-title` + `dl.kv` (`dt.shortcut-keys` with `kbd`s) |
| Status | `[data-role=info\|success\|danger\|warning\|neutral\|skipped]` sets `--role` and `--role-tint` for everything inside it; `.badge`, `.status-text`, `.notice` |
| Settings | `.page-settings[data-section-active][data-dirty][data-view=board\|section\|list]` > `nav.settings-nav` (`ul.settings-nav-list` > `a.settings-nav-item[data-domain][aria-current=page]` (Status first, `li.settings-nav-split` before the second group) > `.icon`, `.settings-nav-label`, `.settings-nav-mark[data-role]` (always there; `.settings-nav-dot` when unsaved, else an icon) with its `.sr-only` words; hidden under 640 px), `.settings-main` (a size container) > `.settings-error`, then the board `section.settings-board` > `ul.status-tiles` (four across from a 1040 px main column, two from 600 px, else one) > `a.status-tile[data-domain][data-role]` (rows: `.tile-top` > `.domain-icon`, `.tile-label`, `.domain-tag`; `.domain-figure`; `.domain-viz[data-kind]`; `.domain-note`), or on phones `nav.settings-list-card` > `.card.settings-list-group` > `ul.settings-list` > `a.settings-list-row[data-domain]` (`.settings-list-icon[data-role]`, `.settings-list-text` > `.settings-list-label` + `.settings-list-summary[data-role]`, `.settings-nav-mark`, `.settings-list-chevron`), or `a.settings-back` and `section.card.settings-section[data-domain][data-held]` (data-held: the head's first answer is out, at most 400 ms, so the body is laid out but not drawn and never moves in sight) > `header.domain-head[data-role][data-plain][data-compact]` (data-compact: no chart and no note, so neither row is drawn; `.domain-title-row` > `.domain-icon`, `h2.card-title`, `.domain-tag`, `.domain-actions` > `.domain-refresh[data-busy]` or Open Logs; `.domain-figure` > `.domain-value[data-word]`, `.domain-unit`, `.domain-second`; `.domain-viz[data-kind=chips\|shares\|meter\|cells\|bars\|checks]` > `.viz-chips`, `.viz-split` > `.viz-seg[data-tone]`, `.viz-meter[role=meter]` > `.viz-meter-fill`, `.viz-cells` > `.viz-cell[data-on]`, `.viz-bars` > `.viz-bar[data-zero]`, `.viz-checks` > `.viz-check[data-role]`; `.domain-note` > `.domain-asof`, `.domain-note-text[data-role]`) + `.settings-section-body` > `dl.fact-list` > `.fact-row[data-fact][data-role]` > `dt.fact-row-text`, `dd.fact-row-value` (`.fact-chip`), in Server `section.settings-group#settings-group-<id>[data-group]` (`.settings-group-title`), `.field-list` > `.field[data-key][data-type][data-wide][data-changed][data-invalid][data-disabled]` > `.field-text` (`.field-label`, `.field-changed`, `button.field-revert`, `button.field-reset`, `.field-help` (a select's chosen option adds its `.field-option-hint` to it), `code.field-key` only while keys are shown), `.field-control` (a `fieldset` while disabled: `input.field-switch[role=switch]`, `.field-select` > `.select` + `input.field-custom` (a `custom` select's Custom…), `.field-number` > `.field-input` + `.field-unit`, `.field-input`, `.list-editor[data-ordered]` > `ol.list-items` > `li.list-item[data-item]` (`.list-rank`, `.list-label`, `.list-actions` > `.list-up`, `.list-down`, `.list-remove`; rank and arrows only with two or more items) + `.list-add` > `select`, `.check-group` > `label.check`), then `.field-default` (`code.field-default-value`), `.field-error`, `.field-warning`, `.field-after` (a folder's `.folder-status[data-role]`); action blocks `.settings-actions[data-action=sync-reset\|config-reload]` (`.settings-actions-label` + button), `.settings-panel[data-panel=config-file]` (`.config-reload`, `.config-outcome`, `.settings-footnote`), `ul.settings-hooks` > `li.settings-hook[data-off]` > `.settings-hook-line` (`.settings-hook-name`, `.settings-hook-chip`, `.settings-hook-runs`) + `.settings-hook-scope` (`.settings-hook-event`, `.settings-hook-types`), a template's `.template-field` > `.template-row` (`.field-input` + `.template-vars-btn[aria-expanded]`), `.template-popover` > `.template-chips` > `button.template-chip[data-var]` + `.template-popover-foot` (`.template-var-desc`, `.template-vars-done`), `.template-example`, `.account` > `.account-block` (`.account-block-head`, `ul.account-rows` > `.account-row` > `.account-name` + `.account-logout`, `details.account-error-details`), `form.login-form[data-step=credentials\|code]` > `.form-row` (the password in `.field-secret`), `.form-actions`; the token's `.token-field` > `.token-row[.token-set]` (`.token-state`, `.token-replace`, `.token-remove`, or the box) + `.token-help` (`.token-help-btn`, `ol.token-steps`), or while the wrapper's token is in use `.token-field[data-source=wrapper]` > `.token-row.token-set` (`.token-state`) and, with a saved token, `.token-row.token-set.token-unused` (`.token-state`, `.token-remove`); `.save-bar[data-state=dirty\|saving][style=--save-x,--save-w]` > `.save-bar-text` (`.save-bar-count`, `.save-bar-where` > a link per section) + `.save-bar-actions` (`.save-bar-discard`, `.save-bar-save`); the page head's `.page-actions` > `.settings-keys-btn[aria-pressed]` (on Logs, a link to Settings › Logs) |
| Job selection (Queue) | Tabs `.seg-count` > `.seg-count-danger` (the Failed count while it is above zero); under 640 px the tabs are a chip row that scrolls when it must, `.seg[data-fade=start\|end\|both]` marking the edges that hide chips (`views/queue-chips.js`); toolbar `.queue-tools` > `.btn.queue-select[aria-pressed]`; the page is `.page-queue[data-held]` (the toolbar laid out but not drawn) on a cold load until the feed's counts arrive or 400 ms pass, so the tabs never widen under the pointer; at the top of the jobs card `.queue-list-bar` > `span.queue-count` ("5 results", while the filter is set) and `.btn.queue-retry-all` (Failed and Cancelled tabs), where neither changes the toolbar (reads "Select", "Done" while selecting); `.bulk-bar[data-state=select\|run]` (sticky at `top: var(--topbar-h)`; a skin whose top bar does not stick would override `top`) > `label.bulk-all` > `input.bulk-check` (indeterminate when some), `.bulk-count` ("3 selected", or "3 selected · 1 shown" when a filter hides some), `.bulk-actions` > `.btn.bulk-action[data-action=retry\|cancel\|delete]` ("Retry 3", "Cancel 3", "Remove 3 from list") or `.bulk-hint`, or during a run `.bulk-progress` > `.bulk-progress-text` + `.bulk-meter[role=progressbar]` > `.bulk-meter-fill` (`--f`); `ul.job-list.is-selecting` > `li.job-item[data-job-id][data-selected]` > `label.job-check` > `input.job-check-input` + `a.job-row[data-selected]` (under 640 px without its cover, the checkbox leading the row). Drawer: `.btn.drawer-delete` (finished jobs) |
| New download | `.page.page-new[data-mode=empty\|docked][data-view=none\|search\|links\|artist][data-tinted][style=--stage-tint]` (its `::before` and `::after` draw the stage glow) > `.new-top`, card `links` or `.new-search-room`, `.new-dock-wrap`, card `results`, `details.new-shortcut` (empty mode only). Top: `.new-compose[data-dense][data-busy]` > `.new-compose-glyph > .new-compose-icon[data-glyph=search\|link]` (fades in when it changes), `textarea#new-input.new-compose-input[aria-label="Search or paste links"]` (its keys in `aria-keyshortcuts`; the shortcut sheet lists them, no hint line), `.new-compose-trail` > `.btn.new-compose-clear` (with text) or `.new-paste` (empty, a coarse pointer with clipboard reading); `.new-options` > `.new-controls` (`.new-quality` > `select#new-quality.select.new-quality-select`) and `.btn.new-cfg-retry` (GET config failed); `p#new-status.sr-only[role=status]` (the count line). Dock `.new-dock-wrap[data-on]` (sticky at the bottom of the viewport; under 640 px fixed above the tab bar, and the page keeps `--new-bar-h`, its measured height set on the root, free below; toasts rise above it by the same) > `.new-dock` (only with links) > `.new-dock-text` (`p.new-dock-count[aria-hidden]`, `p.new-dock-asks`) + `.btn.new-dock-go[aria-busy]` > `.new-dock-what` (the count in the label, "2 albums", or "to download" after "Select a link"; under 400 px it and `.new-dock-asks` hide and the count takes two lines) and `.new-dock-key` (⌘↵ or Ctrl ↵; hidden while disabled, for a coarse pointer and under 640 px), then `.new-notes` (the last submit's `.notice`s). Links: `ul.new-links` > `li.new-link[data-kind][data-state=queued\|loading\|ready\|error\|idle\|invalid][data-issue=not_apple\|no_storefront\|unsupported\|music_video\|bad_storefront\|storefront_not_served\|refused\|not_found\|needs_token\|preview_failed][data-blocking][data-excluded][.has-palette]` > `label.new-link-pick > input.job-check-input[type=checkbox]` (the Queue selection control, its label a 44 px hit area around the box; disabled and hidden when blocking), `.art.art-48.new-link-art`, `.new-link-main` (`.new-link-head` > `.new-link-title[.is-url]` (a row with no preview: its link, `linkLabel`), `.new-link-explicit`, `.chip.new-link-adm` (Apple Digital Master, hidden under 640 px), a storefront `.chip.new-link-sf` (only when it is not the server's default or was borrowed; it never shrinks), `.new-link-sub` (one line, ellipsized, its text the tooltip; under 640 px it wraps between facts, two lines at most, and with the title (two lines at most) beside the row's buttons it runs under them) > `.new-link-fact[data-key=type\|artist\|album\|tracks\|year\|length\|scope]` and `.new-link-loading`, `.new-link-note[data-note=storefront]`, `span.new-link-prior[data-state=active\|done]` > `a.link.new-link-open`, `.new-link-will` (once the link's qualities were read; a `.status-text[data-role=warning]` when the first codec is missing), `.new-link-issue.status-text[data-role]`, with `a.link.new-link-fix` when a page fixes it), `.new-link-actions` > `.new-link-quality` (albums and songs; its text hidden under 640 px), `.new-link-retry`, `.new-link-remove`; the row's `title` is the link; `.new-links-all` > `.new-links-toggle[aria-expanded]`, `.new-links-more`. Search: `.new-search-room[style=min-height]` (keeps the height the results reached while a term is in the box; `--stage-max` wide at most) > `section.new-search[data-state=loading\|stale\|ready\|error\|artist]` (a size container) > `h2#new-search-title.sr-only`, then titled cards `section.card.new-search-group[data-section=hits-albums\|hits-songs\|hits-playlists\|hits-artists]` (Albums, Playlists and Artists with no surface, as Overview's Recently completed; Songs `.card-flush`), whose `.card-actions` hold `.btn-ghost.new-search-more-btn` (Show more, shown when the group has hits past its first six). A group alone: `.new-search-alone` > `.new-group-head` > `.btn-icon.new-group-back[aria-label="All results"]` + `.seg` of the four groups (scrolls sideways inside itself when it does not fit), then `section.new-search-group[data-section]` (Songs `.card.card-flush`) with the wall `[data-expanded]` or the rows, and `.new-search-foot[data-state=wait\|loading\|error]` (one height; "Loading more…", or "Couldn't load more" + `.new-search-foot-retry`), observed to read the next page within 600 px of the viewport. Albums, Playlists and Artists: `ul.wall.new-wall[data-kind=album\|playlist\|artist][data-expanded]` > `li.new-tile[data-kind][data-id][data-prior=active\|done][.has-palette]` > `.wall-tile` (an artist: `button.wall-tile.new-tile-open[data-hit-primary]`) > `.new-tile-cover` (`a.new-tile-job` around the cover when the hit has a job, `.art.art-square`, `.new-tile-chip` (first quality), `.new-tile-badge[data-state]`, `button.new-tile-go[data-hit-primary][aria-busy][aria-disabled]` (aria-disabled while it is sent and once the hit is in the queue)), `.wall-title.new-tile-title` > `.new-tile-name` + `.new-link-explicit`, `.wall-sub.new-tile-sub` > `.new-tile-fact` (artist, year, tracks; a playlist's curator; under 640 px it wraps between facts, two lines at most); while loading `.wall-tile.is-skel`. Songs: `ul.new-hits` > `li.new-hit[data-kind=song][data-id][data-prior=active\|done][.has-palette][.is-skel]` > `.art.art-48.new-hit-art`, `.new-hit-main` (a size container) > `.new-hit-head` (`.new-hit-title`, `.new-link-explicit`, `.chip.new-hit-quality` "Hi-Res Lossless" or "Lossless", "Dolby Atmos", the second gone under a 440 px row and both under 300 px), `.new-hit-sub` > `.new-hit-fact[data-key=artist\|album\|length]`, `span.new-link-prior.new-hit-prior[data-state]`; `.btn-ghost.new-hit-download[data-hit-primary][aria-busy][aria-disabled]` (a glyph with a 44 px target). Notes: `p.new-search-note` (`.new-search-empty`), `.new-search-fail` > `.notice` > `.new-search-fail-text` + `.new-search-retry`. An artist: `.new-artist` > `.new-artist-head[.has-palette]` > `.new-artist-back[data-hit-primary]` ("Results"), `.art.new-artist-art` (120 px, 88 under 640 px), `.new-artist-main` > `h2.new-artist-name` + `.new-artist-sub`, `.new-artist-all` (Download all), then `ul.wall.new-wall` of album tiles and `.new-search-more` > `.new-search-more-btn`. Results: `#new-results-summary.new-results-summary[role=status][tabindex=-1]`, `ul.job-list.job-list-compact.new-results` > `li.new-result[data-status=accepted\|invalid\|duplicate_active\|duplicate_in_request\|queue_full\|error\|deleted]` > a Job row, or `.new-result-row[data-role]` > `.new-result-icon`, `.new-result-main` (`.new-result-label`, `.new-result-title` (a deleted job), `.new-result-url`, `.new-result-msg`, `.new-result-detail`), `.new-result-actions`; head `.new-results-retry`, `.new-results-clear`. Custom quality sheet `.sheet.sheet-custom[data-dialog=custom]` > `.sheet-head` (`h2#new-custom-title.card-title[tabindex=-1]`, focused on open), `.new-custom-body` (scrolls) > `.new-custom-group` (`.new-custom-label`, the Settings `.list-editor`), `label.new-custom-switch`, `.new-custom-limits` > `label.new-custom-field` > `.select`; `.dialog-actions.new-custom-actions` > `.new-custom-follow`, `.new-custom-done`. Browser shortcut (hidden for a coarse pointer): `details.new-shortcut` > `summary.new-shortcut-summary`, `.new-shortcut-body` > `.new-hint`, `.new-bookmarklet` > `code#new-bookmarklet-code.new-bookmarklet-code` + `.new-bookmarklet-copy`, `ol.new-steps`. Quality sheet `.sheet.sheet-quality[data-dialog=quality]` > `.quality-for`, `.quality-wait`, `.quality-body[role=region][tabindex=0]` (the part that scrolls, so the head and its Close stay) > `ul.quality-slots` > `li.quality-slot[data-id]` (`.quality-slot-label` > `.quality-slot-codec` (only when the label does not say it), `.quality-slot-count`, `.quality-slot-best`), `p.quality-same` > `.quality-all` (Show tracks), `ol.quality-tracks` (the tracks whose formats differ, all after Show tracks) > `li.quality-track` > `.quality-track-head` (`.quality-track-no`, `.quality-track-name`, `.chip`) + `.quality-track-opts` > `.quality-opt[data-id]` (`.quality-opt-label`, `.quality-opt-spec`). Download's questions: a Confirm dialog > `.ask` > `.ask-row` (Quality, Files on disk) > `.ask-label` + `.seg` |

**Artwork palette.** `.job-row` and `.latest` carry
`--art-bg` and `--art-t1`, `--art-t2`, `--art-t3` (from `lib/format.js`
`artPalette`/`paletteStyle`) plus `.has-palette` when Apple sent one; the
drawer's `.drawer-head` carries the properties without the class. Each `--art-t*` already reaches 4.5:1 on `--art-bg` or is replaced by
`--on-art-light`/`--on-art-dark`. A background lighter than 24 % HSL
lightness also gets a dark-theme variant (`darkArtColor`: same hue, darkened
until white text reaches 4.5:1, its text light), and each property is then a
`light-dark()` pair, so a light album never paints a bright slab on the dark
page. A playing motion cover switches to the
`motion_artwork_*` palette; the two are never mixed. `.art` also sets `--art-bg`
behind a cover while it loads or when it fails.

**Covers.** Every cover is requested as WebP (`lib/format.js` `artworkUrl`;
Apple's `{w}x{h}bb.jpg` extension is a format slot) with a `srcset` of 1x-3x
the CSS size it is drawn at (`artWidths`) and `sizes` for that box, lazy
below the fold. **Motion covers** (`ui/motion.js` `MotionCover`) lie over the
still in the drawer header and the Overview's latest-release banner (both
always) and its wall tiles (on hover or focus): `lib/motion.js` picks the smallest
H.264 rendition of Apple's HLS master whose side reaches the box times the
pixel ratio and plays the single MP4 its byte ranges cut, muted, looped,
inline, paused off screen, never under `prefers-reduced-motion`; Safari falls
back to the HLS itself if that MP4 fails. The video is invisible until it
plays, and any failure leaves the still.

**Hover and focus shapes.** A row or cell on a card's surface (Queue rows,
Jobs cells) fills its whole box on hover and draws its focus ring inside
(`outline-offset: -2px`); the ones at the card's corners take the card's
radius and corner shape, so neither is cut square by the corner. A free-standing item (the
sidebar and Settings navigation) is its own rounded highlight. A tile on the
page surface (the Recently completed wall) gets no fill: its title underlines.

**Opt-in hooks** are hidden by the base and shown by the skin when it wants them:
`.brand-sub`, `.job-facts`, `.health-icon`,
`.jprog-legend`,
`.attention`, and the x-axis day labels other than the first, middle and
latest.

## Color and contrast rules

The skin meets these; its spec lists the measured values.

- Body and secondary text 4.5:1 on `--bg-surface` and `--bg-canvas`;
  `--border-control`, focus rings and progress fills 3:1 against neighbors.
- `--*-text` is used only on `--bg-surface`. A role may share a color with
  another as long as the icon and label differ.
- The danger and warning roles never look like the accent of a healthy state.
- Under `forced-colors`, badges (with inline padding, so the border clears the
  text), bars, cells and dots get borders (base); a progress cell with an
  outcome stays filled in `CanvasText`; a switch is a hollow track with the
  thumb at the start when off and a `Highlight` track with the thumb at the end
  when on.

## Type, spacing, shape, motion

System fonts only: no web fonts, no CDN. The skin picks stacks for `--font-sans`,
`--font-display` and `--font-mono` and falls back to `--font-system` or
`--font-system-mono`. Every `font-size` is a `--fs-*` token (a Go test
checks every CSS file); the skin adds display roles as tokens. Numbers that
update live use `tabular-nums`. Long titles truncate with the full text in
`title`.

Spacing steps (px): 2, 4, 6, 8, 12, 16, 20, 24, 32, 40, 48, 64 (`--s-0_5` …
`--s-16`). Controls are 32 px (28 in toolbars), 44 px under 640 px and on any coarse
pointer (a phone in landscape, a tablet), where the theme switch
becomes a cycle button and the icon rail scrolls. Under those conditions a
standalone link or a chip's remove button has a 44 px box; the log attribute
chips stay 20 px drawn and get an invisible 44 px hit area inside a row that is
44 px tall (so `getBoundingClientRect` reads 20, `elementFromPoint` 44). On the
phone the Queue's Select button is icon-only beside the filters, and the
selection bar keeps its actions on the select-all row. Radii and
shadows are skin tokens.

**Radii nest.** A rounded element inside a rounded surface takes the
surface's radius minus the inset between them (r = R − p), so the two arcs
share a centre. Levels run from the outside in: cards, tiles, dialogs and the
notices that stand on the page (the connection banners, the Overview's
problem strip); the fields, controls, artwork, inset lists and notices they
hold; the chips, keys and segments inside those. Every element of a level shares one radius, radii
and the insets between nested shapes sit on the 4 px grid, and no radius is
under 4 px except data marks (chart and meter bars, worker cells, the
equalizer, a checkbox's dash), which keep `--r-mark`. A skin writes a
surface's radius as a `calc()` of its content's radius and padding, so the
two never drift apart. Pills and circles (`--r-full`, `--r-badge`, `--r-bar`)
stay round: each such `border-radius` declaration carries `corner-shape:
round`, and a rule that turns a pill into a rounded box sets `corner-shape`
back, unless that radius is its own round pills' radius plus the inset to
them (a capsule bar whose actions can wrap), which keeps both arcs circular
and concentric. Every other corner is the skin's `--corner`, which a skin may make a
continuous-curvature superellipse where the browser draws `corner-shape`
(Chrome and Edge 139+); elsewhere the same radii are circular arcs. Outlines,
box shadows (inset ones too) and overflow clipping follow the corner shape,
so focus rings, hover fills and clipped children keep their box's outline.

Loading placeholders (`.skel`, `.skel-badge`) hold the room of what they
stand for but stay transparent for `--skel-delay` and then fade in, so an
answer faster than that replaces them unseen. A page opened again draws the
last answer of each read (the status store below; `lib/cache.js`: the
Queue's list per tab and filter, Needs attention) while it reads again,
instead of its placeholders.

Motion: 120 ms hover color fades, 200 ms drawer/palette/toast/insert,
`--ease-out` in. Movement uses only `transform` and `opacity` (stage and meter
fills: `scaleX`, linear); never `transition: all`. The running pulse and the
skeleton pulse are the only loops the skin gets for free; any other loop (an
equalizer glyph, a live dot) is the skin's and must stop under
`prefers-reduced-motion`, which also removes the drawer slide, cover movement
and motion covers.

## Components

- **Badge**: icon (or the running pulse) + label on the role tint. Mappings
  live in `lib/status.js` (`jobStatusView`, `itemStatusView`, `hookStatusView`)
  and `lib/health.js` (health views), unit-tested. A completed job whose tracks were all on disk is
  "Already in library" (skipped role). An item with `attempt > 1` adds a
  warning "Retry n/m" badge.
- **KPI tile**: label, value, meta, role icon; the tile links to its Queue tab.
  Values switch without count-up; "—" while disconnected.
- **Job row**: cover, facts, title, artist/curator · type · storefront,
  progress, status badge, relative time, chevron. Indeterminate "Resolving"
  while `total_items` is 0; a running job with nothing finished fills its track
  to `Job.progress` and reads its percentage instead of a "0 / 1" that never moves. A job without a title shows its link (`lib/format.js`
  `linkLabel`: the path without the shared music.apple.com host, cut in the
  middle so two links never read the same up to an ellipsis) muted, with a
  hint line instead of an artist. A failed job shows a one-line
  reason (`errorSummary` in `lib/format.js`), never raw JSON; a stored legacy
  catalog failure ("catalog request failed: 404 Not Found") reads in plain words
  (`legacyCatalogError`), and so does the server's "Apple Music has no album
  1 in storefront cn (404)" ("Apple Music has nothing at this link in the CN
  storefront"); the drawer keeps the stored text under "Full error". Under 640 px it is a two-line card.
- **Stage track**: seven segments (Resolve, Download, Decrypt, Remux, Verify,
  Tag, Save). Download and decrypt fill to their fraction; download at 0 while
  downloading is striped ("size unknown"); a completed item with
  `verified=false` shows a hollow "Not verified" segment, never red.
- **Job drawer**: dialog with focus trap, Esc and Back close it, focus returns
  to the row. Close is first in the DOM, so Tab goes from it to the header's
  Back on a phone and then the body (`.drawer-bar` is `row-reverse`,
  which keeps the drawn order). Header with cover, artist link, meta, a summary
  line computed from the tracks (`lib/jobview.js` `drawerSummary`), status, live
  tag; for a failed job the notice with the trimmed reason (full error under a
  disclosure) comes first, then the actions in two rows that every status
  keeps, so a job finishing or being cancelled changes no height: View logs
  and Open in Apple Music, then Cancel (inline confirm) for queued or
  running, Retry for failed or cancelled, Remove (a
  `ConfirmDialog` beside the panel; it removes the job's records, never the
  files) for completed, failed or cancelled jobs,
  which closes the drawer. A job Apple has nothing at (a 404) has no Retry,
  as in the lists, Open in Apple Music stays quiet, and its header reads as its
  queue row does ("Link not found", the link under it, no type or region);
  on a phone the actions fill their rows except Remove, which keeps its
  width at the row's end, away from the others. Under the Tracks
  heading, the album bar, after the AMDL iOS app's summary bar: one bar whose
  solid fill lights one cell per finished track, left to right in the order
  the tracks finished (by `updated_at`) and in that track's outcome color, with
  the overall stage-weighted progress as a translucent fill behind it; up to
  24 tracks (`TICK_MAX`) gaps cut it into cells, above that it is drawn whole.
  Under it, the finished count left and the percent right (not for a single
  track, whose row shows its stage's percent). After the count, on the same
  line, a running job reads "avg 41 MB/s · ~1 min left" (a completed one, only
  its average): the transferred bytes (the sizes of the written tracks plus
  `progress.download_bytes` of the others) over the run's time, and the time
  left from how fast the overall progress moved in the last 30 s, none until
  it has moved for 3 s (`lib/speed.js`, as the desktop app computes them).
  The Overview's live cards say only the time left, from the overall progress
  the feed carries (it carries no tracks, so no bytes and no average).
  The figures only change text inside lines that are there, so nothing moves. It is drawn from
  the first render (striped while resolving) and stays, full, when the job
  completes, so nothing below it moves. Tracks:
  one 44 px line each (number, title, length, size); the artist shows when it
  differs from the album artist, a codec chip such as `ALAC 24/96` when it
  differs from the album's (the header's summary line says "Mostly ALAC 24/96"
  when the codecs differ, so a second codec turning up moves nothing), and stage meters, status and error only for a track that is
  working, failed, skipped or waiting, or for every track once the drawer has
  shown the job live (so finished rows keep their height). The stage
  segments are as wide as each stage's share of a track's run
  (`lib/status.js` `STAGE_WEIGHTS`); a status message that only restates the
  stage is not repeated under it (`lib/jobview.js` `trackNote`), and the stage's
  percentage stands in the size column (`stagePercent`); no tracks section for a finished job that
  has none. A completed job shows Files: the one folder its tracks
  went to (the common folder of the items' `output_path`, from `lib/files.js`;
  it wraps after a slash, a folder name kept whole unless it alone overflows
  a line) with a Copy button, or, when they went to several folders, the downloads folder and
  "Spread over N folders"; a link to Settings follows. Details is closed:
  times in one short format (`dateTime`, the exact time in the title), the job
  id with Copy, Activity (the job's persisted events, which the job stream
  replays from the start; `lib/store.js` keeps those at or before the snapshot
  cursor out of the job and items; newest first, with a "Gave up" folded into
  the "Failed" that says the same) and hooks.
- **Recently completed** (Overview): the newest release as a banner in its
  album's colors, its motion cover playing, then two rows of the releases
  before it, one tile per album (jobs of one link target collapse to the
  newest), cover, title and artist on the page surface. Wall tiles play their
  motion cover on hover or focus, one at a time, while motion is allowed;
  titles underline on hover. A tile opens the job drawer over the Overview.
  The skeleton has as many tiles as the completed count allows, so loading
  moves nothing. There is no split by release type: the Queue filters by
  type, and the wall is about what finished last.
- **Throughput**: no card under three days with data, where a chart says
  nothing; otherwise totals as facts (the formats mix is in Settings ›
  Quality), then two small multiples sharing the date
  axis (tracks per day, bytes per day), each with its own 0/max axis plus a
  max/2 gridline and label once max reaches 2 (for tracks only when max/2 is
  whole), a hover readout, and a screen-reader table.
- **Log viewer**: `role="log"`, `aria-live="off"`, mono rows (time, level
  marker, component, message, up to three attributes in secondary text), job/request chips that filter (the job chip shows the job's title),
  a trimmed `attrs.error` inline, one tab stop for the list with arrow keys, Home and End moving between rows, expandable pretty
  JSON with Copy, follow-bottom within 24 px else an "N new lines" pill, at most
  2,000 rows. Warn and error rows are marked in the skin's warning and danger
  roles. The card is as tall as its rows (or its empty message) up to the
  window's height, and scrolls past it.
- **Status heads** (Settings): one view per domain (`lib/domains.js`
  `domainView`: tag, figure, chart, note, role) drawn as a board tile and
  as its section's head, the same three tracks in every state. The role
  (the worst condition wins) also gives the nav mark and the phone list's
  summary; Overview's status line shows the danger roles and the Account
  and Library sync warnings. Thresholds: a disk warns at 85 % used and is
  danger at 95 %, the queue at 60 % and 90 %, a hook failure in the last
  24 h warns and is never danger. Simulate never calls the session
  service, so Account is neutral then. Only a role change is announced.
- **Settings**: one field per `RuntimeConfig` leaf (`lib/settings.js`
  `FIELDS`; `TestSettingsFieldsCoverRuntimeConfig` fails on a leaf with
  neither a field nor a `NOT_EDITED` reason), in nine sections (above). A field
  is a label in plain words, one short hint about its effect, and a control
  by type (switch, select, number with unit, text, path, template, token,
  ordered list, checkbox set). Every field carries its `defaultValue`, which
  a test holds to `configs/config.example.yaml`; the dotted key and a
  "Default: …" line show only while the page head's `{ }` button ("Show
  config keys and defaults", `aria-pressed`) is on (a per-device
  preference, `amdl.console.settings.keys`); fact rows then show theirs. A
  field that applies only while another value allows it (the ALAC limits,
  lyrics timing and extras, the simulated speeds: `fieldDisabled`) is
  disabled, never hidden. A
  saved value that differs from its default offers "Reset to default", an
  edit like any other. Controls end on one edge: switches without an On/Off
  word, selects as wide as their longest option, numbers with the unit inside
  the box; cover size is a select of the documented sizes plus Custom…. The
  codec list adds a codec as soon as it is picked from "+ Add codec…", hides
  rank and arrows while it holds one, and its last entry's Remove says why it
  cannot go. Each path template shows the path it gives a sample track
  (track 1 of Blonde, `templateExample`, which follows the backend's
  `formatPattern` and `safeName`) while it is typed, and its Variables
  button opens chips that insert a variable at the caret; the panel stays
  open for the next one until Done, Escape or a press outside, and names the
  focused or last inserted variable's meaning under the chips. A select's
  chosen option may add a note to the help line; the control column holds
  only the control. Edits stay in a draft; the save bar, fixed over the
  main column with its room kept below the page so it moves nothing,
  counts them and links each section they are in (announced politely, debounced), Discard drops them and reads
  `GET /config` again (it asks first when more than one field is edited), Save (or
  Cmd/Ctrl+S, unless a dialog is up) sends `PUT /config` with only the
  changed leaves, lists whole (`buildPatch`); an edit made while the save
  runs stays in the draft (`draftAfterSave`). A section opens at its top. A refused save
  keeps the draft: a notice says why and links to each field at fault by its
  label, and `fieldErrors` puts the message on each field it names
  (`details.keys` for `config_key_not_mutable`, the dotted keys in the message for
  `invalid_config` and 400), with every known key in it replaced by its label
  (`keysToLabels`). A number that is not whole or lies outside the field's
  `min`/`max` is an inline error as it is typed ("Use a number from 1 to
  10", `rangeError`); Save stays enabled but sends nothing while one is
  marked and moves focus to the first, opening its section. An odd cover
  size or an unknown template variable is a warning, and a saved one keeps
  its warning. The page reads `GET /config` when it opens,
  on Retry after a failed load and on Discard, and takes the answer to a save
  or to "Reload from file" as the new saved values;
  focus, a reconnect and changes made in another tab or in the file do not
  reload it, so a save from a long-open tab sends its edited keys over
  whatever was saved since (only those keys: the rest keep the file's
  values).
  A save reads again the status keys its changes touch (`lib/facts.js`
  `refreshKeys`: a simulate change flips the top bar's tag at once), and
  every answer to a config read is the status store's config snapshot.
  Library sync's body is its two fields and "Start over from now" (the
  anchor reset, a `ConfirmDialog`, offered only once a check has recorded
  the library); its head says Off, Needs token (neither the wrapper nor
  Settings has a media user token), Error or Watching, and stays loading
  until the config and the wrapper status say whether there is one. Account
  shows, below its head and when the session service has a login API
  (`login_supported`), Log out (a `ConfirmDialog`: per named account, else one
  that asks for the Apple ID, because no wrapper lists its accounts) and the
  login form (with a show-password toggle) with its two-step code step, which
  takes six digits, keeps the step on a 400 or 409 and otherwise starts over
  from the password after a failed code; a wrapper amdl runs
  (`managed` of `/wrapper/status`) heads the section with its mode while it
  does not serve (Starting and Logging in neutral, Stopped a warning naming
  `amdl wrapper start`, Failed a danger with `last_error`, its full text and
  `amdl wrapper logs` in the body), never "No account", keeps its login form
  disabled while stopped, says in one line that a login replaces its one
  account (where the password goes is in [the API reference](../../docs/api.md#wrapper)), and adds one line
  while `login_residue` is true. Account's head names the one storefront
  its account serves, or counts them and names each; the default storefront is its fact row,
  and the head speaks of it only when the account does not serve it; a session service
  without a login API (a bare wrapper-lite, the usual service) shows its
  logged-in account in one line and no Log out, and the command-line steps
  in place of the form only while no account is logged in; until the status
  loads that one line holds its room unseen (`.holds-room`);
  while the service cannot be reached, the head says so once (the tag
  Unreachable, the figure No answer, the note the error in a line with the
  server's own text as its tooltip) and the body holds only the media user
  token; the page
  and its errors say "session service" and leave wrapper-manager to that
  text. Then the media user token: while the backend reads the wrapper's
  own (`media_user_token` of `GET /wrapper/status`), one line "From the
  wrapper · ends …abcd" and, when a token is also saved, a second "Saved
  token · ends …wxyz · not used" with Remove, no box and no steps;
  otherwise a saved token
  is never put in the page, only "Token set · ends …a1b2" with Replace and
  Remove; without one an empty box, and "Where do I find this?" opens three
  steps; Server's Configuration file group shows
  `persisted` and "Reload from file", which reports how many
  settings changed; a file that cannot be used is one warning under the button
  (`reload_error`, also after leaving and returning, but announced as an alert
  only for a reload made on the panel): the parser's reason, the server's path
  behind a "Which file" details line; one footnote names the startup
  settings no fact row shows.
- **Primitives**: buttons (primary, secondary, ghost, danger), segmented
  control, search field, select, chips, notices, toasts (`role="status"`; 5 s,
  8 s with an action, errors 10 s, every timer paused while the pointer or
  focus is on them; `sticky` ones stay until dismissed or the page changes;
  one raised with `offline` (a request that could not reach the server)
  also goes once the feed is live again, after 3 s up and not while the pointer
  or focus is on the toasts; the toast list is
  `ui/toasts.js`;
  `.toast` > `.icon`, `.toast-msg`, `a.toast-action` (or a `button` for an action in place, such as Undo), Dismiss; the icon and
  the buttons sit on the message's first line),
  completion toasts (`app.watchJobs(ids)`: when watched jobs end, one toast
  per 3 s burst, "Album X finished" with Open to its drawer or "3 downloads
  finished" with a link to the Queue, `lib/watch.js`; such a job is not also
  sent to the milestone region. New download watches what it submits; the
  Queue's Active tab watches the jobs it shows while it shows them
  (`ui/hooks.js` `useWatchShown`, `app.unwatchJobs`), so a job that finishes
  and leaves the list says where it went), command palette (Cmd/Ctrl+K, never over a
  modal dialog; combobox + listbox; focus handling from `useDialog`, so
  focus returns to the opener; rows from `lib/palette.js`: a query holding
  Apple Music links offers "Download this link" first, Settings sections and
  fields match by label, key or panel, job rows add status and date and
  time (`dateTime`); a
  visually hidden polite status reads the result count once the job search
  answers; under `(hover: none)` a `.palette-close` "Cancel" text button
  shows, the key hints and `.palette-foot` hide, rows are 44 px with the sub
  line under the label and the Navigate group is left out until something is
  typed), shortcut sheet
  (`?`: groups Global, Lists, New download, Settings, two flowing columns
  from 640 px (CSS columns, so a group follows the one above it), keycaps only
  for real keys, the body scrolls under its head).
- **Dialogs** (`ui/dialogs.js`): `Dialog` is a scrim plus a modal panel
  (`useDialog`: focus the first `[data-autofocus]`, trap Tab, Escape closes
  without reaching the page shortcuts, focus returns to the opener).
  `ConfirmDialog` asks before a destructive or irreversible action: an
  alertdialog with the title, the consequence, Cancel (focused; the
  confirm button instead with `confirmFocus`, for Download's questions) and
  the confirm button; with a third, primary choice the three share one row in
  macOS order (the destructive one alone on the left, then Cancel and the
  primary). Under 640 px it is a bottom sheet with full-width buttons:
  primary, destructive, Cancel. The DOM follows the drawn order. While busy every button is disabled and Escape does nothing (the
  Settings leave dialog is busy while its Save saves, and that button reads
  "Saving…"). The
  shortcut sheet is a `Dialog`. Every bottom sheet
  (`placement="bottom"`: Qualities and confirms on phones) is full
  width on the tab bar (to the screen edge over the drawer), with rounded top
  corners, a 36 x 4 px grabber and `overscroll-behavior: contain`. A change of page or drawer
  (a link, Back, a shortcut) closes the palette and the shell's sheets; a sheet pushes
  no history entry, so Back leaves the page rather than closing it first.
- **Queue selection** (`views/queue-select.js`, rules in `lib/jobactions.js`):
  Select (or `x` on a row) adds a checkbox column; a plain click on a row
  then selects it (Shift selects a range), Cmd/Ctrl-click still opens it.
  The bar offers only what the selection allows, with counts: Retry for
  failed or cancelled jobs (the API retries nothing else), Cancel for queued or running,
  Delete for completed, failed or cancelled; Cancel and Delete ask first.
  Requests run at most 4 at a time (`lib/bulk.js` `runLimited`) with
  "Deleting 3 of 12" in the bar and the dialog, then one toast names the
  failures by reason. The selection is a set of ids checked against the live
  list, so a deleted job leaves it and a changed status changes the actions;
  rows leave through the feed, never by hand.
- **New download** (views/download.js and its download-* parts; the logic is
  pure in `lib/links.js` `scanLinks`/`removeLink`, `lib/lookup.js`,
  `lib/submit-check.js`, `lib/submit.js` and `lib/overrides.js`): the text box is the source of the
  links; `?url=` prefills it (`lib/links.js` `openingText`: links brought from
another page replace the draft, with a "Replaced your earlier draft" toast
whose Undo brings the draft back with the box's links joined to it
(`views/download-address.js` `useDraft`); a draft that holds them all, after
a reload or Back, stays) and the page writes its links back (replace), so a
  reload and the global paste keep them, also after `n` or the sidebar opened
  a bare `#/new`. It takes focus one frame after the
  shell focuses the heading, with a fine pointer only. Each row shows the
  link's kind at once, then a `POST /lookup` preview (a 48 px cover, the
  title with E and Apple Digital Master, one meta line): the first
  100 previewable links (one submit chunk), 4 at a time, a lookup the text no
  longer holds is aborted, answers are cached per tab (an album `?i=` link's
  only under the `catalog.album_track_url_mode` it was read with); after a
  feed resync (`app.epoch`) failed lookups run again. The first 12 rows are
  listed plus every row with an issue (blocking or a warning) or an earlier job
  still queued or running, then "Show all N"; its hint counts the hidden rows
  that are only in the library. The earlier-job search uses the id a job
  would download (`lib/lookup.js` `priorQuery`: the song for an album `?i=`
  link unless `album_track_url_mode` is `album`). Legacy
  `itunes.apple.com` links and `id`-prefixed ids read as music.apple.com
  links; a link without a storefront takes the first one another link in the
  text has, else the server's `catalog.default_storefront` (`GET /system`
  `default_storefront`, `app.defaultStorefront`), else it is flagged; two
  links glued together split at the second `http`.
  Repeats are merged and counted in the card note; a row shows the storefront
  only when it is not the server's default or was borrowed, and keeps the
  link as its tooltip. Links that are not Apple Music, not a
  downloadable kind, music videos, refused by the lookup (400), in a
  storefront Apple does not have, in a storefront the wrapper's account does not
  serve (read from `GET /wrapper/status` `regions`, polled every 30 s; unknown
  while that is not loaded, failed, not ready or in simulate mode, and then
  nothing is flagged; the submit's `unsupported_storefront` still has the last
  word), or unknown to Apple (its 404, private
  playlists included: the catalog read never carries the token) are flagged
  and not sent; the count line counts them as "can't download", and warnings
  apart. Albums, songs and playlists also ask `GET /downloads?q=<id>` (2 at a
  time, cached per tab, `lib/lookup.js` `priorJob`, kept live by the feed
  through `applyFeedToJobs`) and show "In your library · Oct 29 · Open" or
  "In the queue now · Open". Every row that can be sent has a checkbox, and
  only checked rows are sent and counted on Download; a row in the queue
  starts unchecked with its cover dimmed, and so does one in the library
  (`isPicked`; the person's own check wins). The count line reads "3 links · 1 can't download · 1
  merged · 1 new" (merged: repeats folded into one link; "to check" and
  "queued" too): what stops or needs a look comes first, and each part is
  short, so two lines on a phone hold it. An artist row says it queues the whole discography (a "Whole discography" fact; a name the row's title already says is not repeated in its meta line), and a
  submit with artist links asks first. The media-user-token is the
  wrapper's when the backend reads one (`media_user_token` of
  `GET /wrapper/status`), else Settings' `catalog.media_user_token`: while
  neither has one a station (or a
  link whose preview says `needs_media_user_token`) gets a warning with a
  "Set it in Settings" link to `#/settings?section=account&field=catalog.media_user_token`,
  which scrolls to the field (nothing is flagged until the config and the
  wrapper status have answered); a private playlist downloads without one
  and uses it only for the owner's cover, and is looked up again when the
  token in use changes.
  Available qualities (albums and songs only) opens a read-only sheet from
  `POST /quality`: each offered slot once, then only the tracks whose formats
  differ from the album's ("All 12 tracks offer the same formats" otherwise,
  Show tracks lists them all); a release with no enhanced format says only
  "No enhanced formats. Downloads fall back to AAC-LC, 256 kbps."; errors show
  inline with the server's message.
  The answer is kept per link for the tab, and the row then says what the
  chosen quality gets there ("Will download: Lossless (ALAC), 24-bit / 44.1
  kHz", or AAC-LC through the fallback), with a warning when its first codec
  is missing ("Dolby Atmos not offered", `lib/overrides.js` `probedQuality`).
  Before that, a preview whose `audio_traits` hold none of lossless, Dolby
  Atmos or spatial already says "Will download: AAC-LC, 256 kbps"
  (`linkQuality`).
  The compose field is a capsule in display type (28 px, 32 from 1680 px,
  20 under 640 px) with a search glyph that turns to a link glyph once the
  box holds a link and breathes while a search is under way; a link or a
  line break sets it dense (16 px body text) and it grows to six lines, then
  scrolls; the placeholder keeps to one line. Escape empties it as one undo step (Cmd/Ctrl+Z brings the text
  back), then leaves it; Clear does the same with the pointer. The page has
  no top-bar search: the field is it. A glow sits behind the field, red and
  purple, then the first cover's colour once one has loaded
  (`--stage-tint`).
  The dock shows while the box holds links: a frosted capsule that floats
  at the bottom of the viewport while the links run past it (under 640 px
  it is fixed above the tab bar), with the count line, a quality line and
  Download. The quality line says what the links will download once the
  qualities of every link to be sent were read and agree, as each row's
  Will download line does ("Will download: Lossless (ALAC), 16-bit / 44.1
  kHz"), else what the chosen quality asks for, said as an ask ("Asks for
  Dolby Atmos, then Lossless (ALAC) up to 24-bit / 192 kHz"; `dockQuality`,
  `qualityText`), so a ceiling never reads as what an album has; toasts rise above it so they never
  cover Download, and a failed submit's notice comes under it. The options
  sit under the field. The Quality menu offers Quality: Settings, Lossless (ALAC), Dolby Atmos then
  lossless, AAC, and Custom… (`lib/overrides.js` `PRESETS`; the closed menu shows
  only the name, the default's naming what it follows); Custom… opens a 420 px sheet (a bottom
  sheet under 640 px) with the Settings codec list editor, the fallback switch
  and the ALAC limits (while ALAC is listed), Done and Use Settings; it opens
  with its heading focused and Escape returns focus to the menu. While Custom
  is active the menu names its codecs. A submit sends only what differs from
  `GET /config` `download` as `overrides` (`presetOverrides`;
  `quality_priority` order matters), so Quality: Settings sends none,
  plus what its questions answer. Before it goes a download asks, in one
  dialog (`views/download-ask.js` `AskDialog`, `lib/download-ask.js`), the
  desktop app's two questions: Quality (Lossless, Dolby Atmos, Dolby
  Digital in `quality_priority` order, the first chosen) for a search hit
  whose `audio_traits` have lossless and atmos while Quality: Settings is
  chosen and `quality_priority` lists ALAC and ec3 or ac3, sent as
  `quality_priority` led by the choice; and Files on disk (Skip, the
  default, or Replace) for a hit or checked links in the library, unless
  Settings › Storage & files' "Always skip files already downloaded" is on
  in this browser (`localStorage["amdl.console.skipExisting"]`,
  `ui/skip-pref.js`). A release in the library always sends
  `force_overwrite`, so the server's own setting never decides it. A
  download with nothing to ask goes at once. Until the config loads only Quality: Settings can be
  chosen. The choice lasts for the tab, so the next album can follow with it.
  Download names what it sends ("Download 1 album", "Download 3 items" for
  mixed kinds; "Select a link to download" while none is checked;
  Cmd/Ctrl+Enter, hinted on the button only with a fine pointer, toasts why
  when nothing can be sent) and posts `POST /downloads` in chunks of 100, hands the
  accepted job ids to `app.watchJobs`, and the per-URL
  results replace the previous ones: accepted jobs as live Job rows (feed
  batches through `applyFeedToResults`) that open the drawer over the page,
  from 640 px up in two lines so the title and artist keep the main
  column's width (the art beside both, title and artist across the row,
  progress and status under them, no relative time; the base sets it with a
  five-class chain that outranks the skin's `.job-list-compact` rules),
  each showing its link's preview (title, artist, cover) until Apple names
  the job (`seedJob`), the rest with their reason, View job for `duplicate_active`, Retry for
  `queue_full`, `decryptor_unavailable` and server errors. The handled
  links then leave the text box; an error under Download ("Cannot reach the
  server") clears when the text is edited and after a reconnect; the rest of it stays, links typed during
  the request included, and focus moves to the results summary, right under
  the dock. The summary and the toast use the same words ("Started 2
  downloads", then how started jobs finished, `submitSummary`); the toast
  offers View in Queue. The whole text box lasts for the
  tab when the page is left. The results stay until Clear (which moves focus to
  the text box) or the next submit, for the life of the tab (module state in
  `views/download-results.js` `useResults`, and
  `sessionStorage["amdl.console.download.results"]` over a reload, without
  the token): coming back to the page shows them, and their jobs (at most
  25) are read again since the page missed the feed meanwhile. The Browser
  shortcut disclosure (closed, absent for a coarse pointer) shows
  `javascript:location.href='<this page>#/new?url='+encodeURIComponent(location.href)`
  as copyable text, never as a link.
  **Search**: words without a link search the catalog
  (`views/download-search.js`, `views/download-search-state.js`,
  `views/download-songs.js`, `views/download-search-group.js`, logic in
  `lib/search.js` and
  `lib/search-jobs.js`): `GET /search` once typing pauses 300 ms (Return searches
  at once), six hits per group (six fill one, two, three or six columns),
  in four titled sections, Albums, Songs, Playlists, Artists (groups
  without hits left out), each with Show more in its head when it has more
  than those six. Show more shows that group alone (`#/new?group=`, a
  history entry): its title row turns into a round Back ("All results",
  also Escape and the browser's Back) and the four groups as a segmented
  control that switches group in place; the page comes back to the top of
  the group, and its next 25 are read whenever the foot under the list
  comes within 600 px of the viewport, with "Loading more…" or "Couldn't
  load more" and Retry in a foot of one height; a group with nothing says
  "No playlists for “…”". The group stays as the words change. Back in every
  group, each section again shows its first six and its Show more takes
  focus. One search in flight, a term typed past is aborted, answers (the
  pages read included) cached per tab.
  The results run wider than the field, up to `--stage-max` (1240 px).
  Albums, Playlists and Artists are Overview's tile wall (six across, three
  on a narrower page; on a phone a shelf that scrolls sideways, two columns
  in a group alone): an album or a playlist is its
  cover with its first quality tag from `audio_traits` (`qualityHints`) and
  Download over it (a glass circle, shown on hover or focus with a mouse,
  always on a touch screen), then title with E and artist · year · tracks
  (its title is not a link and is not underlined on hover), a playlist
  with its curator under its title; an artist
  is a round cover over its name and genre, one button. Songs are link rows
  without the include box on one grouped surface, one to three columns:
  cover, title with E and the quality tags, the meta line (artist, album,
  length), and a round Download. The library or queue state comes from
  `prior_job` kept live by the feed (`hitPrior`, `trackJobs`): a song shows
  the note, a tile a badge, and its cover opens that job's drawer. Download
  sends the hit's `url` alone with the chosen quality and adds its outcome
  to the results (an artist asks first, like an artist link). While it is
  sent, and once the hit is in the queue, Download is `aria-disabled`, not
  `disabled`, so focus and the arrow keys stay on it. An artist opens its albums in place of the
  sections (`GET /artists/{id}/albums`, 25 a page, Show more) under Back
  ("Results", also Escape), a 120 px round cover (88 on a phone), the name
  in page-title type and Download all; it is a history entry, so the
  browser's Back returns too, and focus goes to Back and then to that
  artist's tile. Keys: ArrowDown on the box's last line or Return go to the
  first hit. The hits are one Tab stop (the one last focused, else the
  first); each hit has one stop (Download, an artist's tile). ArrowLeft and
  ArrowRight move within a section; ArrowUp and ArrowDown go to the nearest
  hit in the row above or below by where it is drawn, across sections
  (`lib/search.js` `nextStop`), and ArrowUp with nothing above returns to
  the box; Home and End; Return downloads or opens; Escape leaves an open
  artist, then a group alone, then returns to the box. A move scrolls the
  whole tile or row clear of the top bar (and the phone tab bar). While a
  term is in the box and the view stays (every group, a group alone, an
  artist) the room the results take never shrinks, so results that come
  back shorter leave canvas under them instead of pulling up what follows;
  another view starts from its own height; hits are keyed by position, so a new
  term's hits replace the old ones in place. The count
  line, announced, reads "6 albums, 6 songs, 6 playlists and 1 artist for
  “Yorushika”", and in a group alone "31 albums so far for “Yorushika”";
  there is no dock while searching. The palette offers "Search Apple Music for “…”"
  for words, which opens `#/new?q=`; a box holding links keeps them and a
  toast says why.

## States

| Surface | Loading | Empty | Error |
| --- | --- | --- | --- |
| KPI | skeleton of final size | zeros | "—" offline, or while the feed reconnects before its first snapshot |
| Queue | 6 skeleton rows | per tab, with Clear filters when filtered; Active and All offer "Start a download" | inline note + Retry |
| Drawer | skeleton tracks | "No tracks resolved yet" | 404 on first load "No download with this id"; 404 after it loaded "This job was deleted"; a failed first load retries when the feed reconnects; 204 on the stream means finished |
| Recently completed | skeleton banner and tiles (cover plus two text lines) | "No completed downloads" | inline note + Retry; a broken cover shows bg color + glyph |
| Logs | 12 skeleton lines | "No records match" + Clear filters | the API's validation message |
| Throughput | skeleton rows | no card under three days of data | inline note + Retry; a 404 endpoint shows a quiet "not available" note |
| Settings | heads and tiles keep their tracks: for `--skel-delay` their slots are invisible, then the skeleton pulse; a revisit paints the store's last answers; fact rows keep their labels and room with empty values until `/system` answers; field labels at once, 6 skeleton lines for the controls | words, never "0" or "—" ("No tracks yet", "No runs", "Not anchored yet", "Idle") | a head with no answer reads "Couldn't read status" (neutral, never a fault), a 404 "Not on this server", and after data the values stay with "As of 14:02 ·" before the note; a refused save keeps the draft with a notice and per-field errors; wrapper errors as notices in plain words with the server's text under "Details from the server"; a failed config load retries only with Retry |
| New download | a skeleton cover and line per link being looked up; a search shows its sections with 6 placeholder tiles, 6 rows, 6 tiles and 2 round tiles (a group alone: 6 of its own) until its first answer, a group alone's next page a "Loading more…" foot, then the last hits dimmed (`data-state=stale`) while a new term loads; until `GET /config` answers the Quality menu offers Quality: Settings only and Overwrite waits | the field and its options alone, with the browser shortcut and no dock; a search that finds nothing says so on the page ("Nothing in Apple Music matches …") and in the count line | a failed lookup flags its row (warning, Retry) and the link is still sent, and it runs again after a feed resync; config: a "Settings unavailable · Retry" button after the options (also reloaded after a resync), and a submit sends no overrides; a refused submit shows a notice under the dock, `invalid_overrides` with a Custom… button; a failed search or artist read says what failed with the server's reason and Retry, a failed page of a group alone or an artist says so beside its Retry, and both run again after a feed resync |
| Feed | | | top bar: the live dot alone while connected, "Reconnecting in N s" (the wait before the next attempt); banner after 10 s; Offline (and the Disconnected banner) after 60 s with Retry; the browser's `online` event retries at once |
| First load | static "Loading the AMDL console…" in `#app` (`.boot`, `index.html`) once the stylesheets are in, on the skin's canvas in the system theme; replaced on mount | | |

## Accessibility

Landmarks (`nav`, `header`, `main`), a skip button, one `h1` per page (focused on
navigation). Every control shows the 2 px focus ring in `--focus`, a field you type
in in `--focus-field`; a button a dialog focuses on open draws none when a
click or a tap opened it (`main.js` sets `data-input=pointer|key` on the root
from the last input). Keyboard: `n`
opens New download, `g` then `o/q/s/l/,/y/n` (`,` and `y` are Settings), `/` focuses
the page filter, `j`/`k` move between rows, Enter opens, `x` selects the
focused Queue row, Esc closes (then leaves selection mode; in a field it
clears a search first, then leaves the field), Cmd/Ctrl+K, Cmd/Ctrl+S saves
Settings,
`?`; single keys and the global paste are ignored while typing,
while the palette or a sheet is open and while any `[data-dialog]` is up. The
theme control is a button group with `aria-pressed` and the accessible name
"Theme". Milestones ("Album X completed") go to one
polite status region, at most one announcement per 5 s. Progress carries
`role="progressbar"` with `aria-valuetext` such as "12 of 14 tracks, 1 failed",
whether it renders cells or a bar. Text the skin adds with `::before` (section
numbers, "No.") is decoration and repeats nothing a screen reader needs.
With unsaved settings, leaving the page asks "Save changes?" and names up to
three changed fields ("Don't Save" in red text, Cancel, and Save while no
field is invalid): a page sets
`setLeaveGuard` from the app context, every hash change passes it (links,
the palette, shortcuts, Back), and a refused change is undone through the
history, so no entry is added (a pushed entry is dropped with Back, a Back
or Forward is reversed; entries carry `history.state.amdlIdx`,
`lib/route.js` `refusedMove`) while the page shows its `ConfirmDialog`;
leaving after all repeats the same move (`resumeLeave`). The browser's own
prompt covers closing the tab. Discard with more than one change asks
"Discard N changes?" (Discard, Keep Editing). Moving between settings
sections never asks.

## Data flow

`GET /downloads?limit=1` seeds counts and `event_cursor`; the feed stream then
resumes from that cursor and every page applies its batches (at most 10 a
second) to its own list (`lib/store.js`). The drawer and logs page each open one
secondary stream from their snapshot cursor. Streams pause 30 s after the tab
hides and resume from the stored cursor. When the feed is live again after
any outage, or the browser fires `online`, `app.epoch` (AppContext) goes up:
every page refetches what it loads, and a page showing a load error reloads.
The status endpoints (health, system, wrapper status, library sync, hooks,
stats, config) are read once for the whole tab (`lib/status-store.js`, bound
in `ui/system-status.js`): a key is read while a page shows it, at most once
per its interval, every page that shows it shares the answer, and a write
that changes it (a Settings save, a login) reads it again.
Settings is the exception for its config base only: it reads it only as its
component notes say, so a reconnect never replaces values being edited; its
status heads poll through the shared store like every other page.

## Verification

Run the backend with `make dev` (simulate mode), open `/console/`, and check
it at 1440 and 390 px wide in light and dark: Overview with the drawer, Queue, Logs, New
download, Settings (the board and a section), the command palette, and under 640 px the tab bar (five tabs, the plus in the middle) and
the Settings list with its Logs row. No horizontal scroll at 390 px, status icon plus label everywhere,
visible focus rings.
