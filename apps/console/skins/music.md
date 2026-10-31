# Skin: music (Apple Music, immersive)

The console's one skin. Implementation: `apps/console/static/css/skins/music.css`.
The shared contract (structure, tokens the skin must declare, DOM hooks, what a
skin file may do) is [DESIGN.md](../DESIGN.md#skin); this page only says what
is particular to this skin.

## Intent

Artwork leads. Running jobs, the latest-release banner and the job
drawer's header take each album's own Apple colors (`--art-bg`, `--art-t1` to
`--art-t3`). Large rounded radii, SF Pro Display-style tight headlines, a red
accent close to Apple Music's. Dark first, with a designed light theme. The
operations data stays complete but is typeset like Apple's own inset grouped
lists, one level below the artwork. Under 640 px a frosted bottom tab bar is
the navigation.

## Tokens

Contrast is measured on `--bg-surface` (the grouped surface) unless noted.

| Token | Light | Dark | Use | Contrast L / D |
| --- | --- | --- | --- | --- |
| `--bg-canvas` | `#ffffff` | `#000000` | page | |
| `--bg-surface` | `#f5f5f7` | `#1c1c1e` | grouped surfaces, cards | |
| `--bg-raised` | `#ffffff` | `#1c1c1e` | drawer, palette | |
| `--bg-group` | `#f5f5f7` | `#2c2c2e` | grouped cells inside overlays (drawer times, buttons) | |
| `--bg-fill` | `#e8e8ed` | `#2c2c2e` | segmented track, search trigger, chips | |
| `--bg-inset` | `#ffffff` | `#2c2c2e` | cells and fields inside a grouped surface: text fields, selects, codec and list editors, bookmarklet code, link tags | secondary text 5.07 / 5.42 |
| `--fill-raised` / `-hover` | `#e8e8ed` / `#e3e3e8` | `#3a3a3c` / `#48484a` | filled buttons on the frosted save and selection bars, where `--bg-fill` matches the bar in dark | text 13.78 / 10.42 |
| `--switch-thumb` | `#ffffff` | same | switch thumb | |
| `--bg-hover` / `--bg-selected` | `#ececf0` / `#e3e3e8` | `#2c2c2e` | row hover, current nav item | |
| `--seg-thumb` | `#ffffff` | `#636366` | pressed segment | |
| `--chip-on-bg` | `#e8e8ed` | `#636366` | pressed toggle chip (log levels, Pause, Wrap); raised with `--chip-on-shadow` in dark only | |
| `--chip-off-bg` / `--chip-off-ring` | transparent / 1 px `#86868b` | `#2c2c2e` / none | toggle chip that is off | ring 3.33 |
| `--bg-chrome` | `rgba(246,246,248,.86)` | `rgba(28,28,30,.82)` | sidebar, tab bar (blurred) | |
| `--topbar-bg` | `rgba(255,255,255,.78)` | `rgba(0,0,0,.72)` | sticky top bar (blurred, no rule) | |
| `--text-primary` | `#1d1d1f` | `#f5f5f7` | text | 15.6 / 15.4 |
| `--text-secondary` | `#636368` | `#a1a1a6` | labels, meta; light passes 4.5:1 on every fill it sits on (4.67 on `--bg-selected`) | 5.49 / 6.61 (5.97 / 8.16 on canvas) |
| `--border-control` | `#86868b` | `#6e6e73` | outlined remaining cells, field outline | 3.33 / 3.36 |
| `--accent` | `#e0213a` | same | the only red fill: primary buttons, New download (sidebar pill, rail disc, tab mark), and a switch that is on, the one fill that is not an action (an Apple toggle in the app's tint; the fill says the state) | white on it 4.72 |
| `--accent-hover` / `-pressed` | `#c81c33` / `#b0172c` | same | primary button hover / pressed, the current page's New download pill | white 5.72 / 6.99 |
| `--disabled-bg` / `-text` | `--bg-fill` / `#636366` | `--bg-fill` / `#98989d` | a disabled primary button: gray, not faded red | 4.90 / 4.85 |
| `--accent-text` | `#d60f2c` | `#ff4d61` | "See all" links, current tab | 4.87 / 5.26 |
| `--mark` / `--brand` | `#fa2d48` / `#e0213a` | same | logo | non-text |
| `--focus` | `--accent-text` | `--accent-text` | 2 px focus ring in the accent as in Apple Music: offset 2 px, on a select drawn over its border (offset -1 px) so it is one ring | 5.30 on white / 6.49 on black |
| `--focus-field` | `#1d1d1f` at 72% | `#f5f5f7` at 72% | the ring of a field you type in (text fields, the filter box, the compose field's 1 px edge): neutral, so typing never reads as an error | 6.67 on white, 6.42 on surface / 9.70 on black, 8.59 on surface |
| `--compose-bg` / `-hover` / `-focus` | `--bg-surface` / `#f0f0f3` / `#ffffff` | `--bg-surface` / `#222224` / `#222224` | New download's compose field at rest, under the pointer, typed in; its placeholder is `--text-secondary` | placeholder 5.49 / 6.61 at rest, 5.97 / 6.17 typed in |
| `--compose-edge` / `--compose-shadow` | `--border-subtle` / a red halo | same | the field's 1 px edge at rest; the halo under it while typed in | non-text |
| `--dock-bg` | `--toast-bg` | `--toast-bg` | the New download dock, frosted | secondary text 5.97 / 5.72 |
| `--stage-glow-a` / `-b` / `-tint` | red 10%, purple 8%, the cover 16% | red 16%, purple 10%, the cover 24% | the glow behind the compose field; `-tint` mixes `--stage-tint`, the first cover's colour | non-text |
| `--info` | `#fa2d48` | same | running: equalizer, cells | 3.79 on white / 5.53 on black |
| `--info-text` | `#d60f2c` | `#ff4d61` | "Running" label | 4.87 / 5.26 |
| `--success` / `-text` | `#248a3d` / `#1b7a34` | `#30d158` | completed marks / "Healthy" | text 4.97 / 8.42 |
| `--danger` / `-text` | `#d70015` / `#c4001a` | `#ff6961` | failed marks / failed chips and red text actions | text 5.74 / 6.03 (4.82 / 4.79 on its tint) |
| `--warning` / `-text` | `#b25000` | `#ffd60a` | warnings | 4.77 / 12.05 |
| `--neutral` | `#77777c` | `#8e8e93` | off, queued (marks) | 3.6 / 5.22 |
| `--skipped` / `-text` | `#8944ab` | `#bf5af2` / `#da8fff` | already in library | 5.54 / 7.58 |
| `--on-role` | `#ffffff` | `#000000` | glyph on a solid health tile, the bar in a partly checked circle | 4.3+ / 6.6+ |
| `--select-chevron` | chevron in `#636368` | in `#a1a1a6` | the select arrow (a data image, so it has a value per theme) | non-text |
| `--track` | `#e8e8ed` | `#3a3a3c` | empty bars | |
| `--chart-bar` | `#6b7b93` | `#8d9bb3` | per-day chart bars: blue-gray, so red stays with the primary action and failures | 3.95 / 6.05 |
| `--series-1` to `-4` | `#007aff`, `#ff9500`, `#8944ab`, `#77777c` | `#0a84ff`, `#ff9f0a`, `#bf5af2`, `#8e8e93` | the formats bar (Settings › Quality), by position | non-text |

Tints (`--*-tint`) are the role color at 10-20 % alpha. Badges carry no tint:
an icon (or the equalizer) plus a word, so status never rests on color. The
exception is failure: a failed badge or status word is a chip, the danger
tint behind the circle-x glyph and the word in `--danger-text`, so a failure
is labelled and never a solid red block. In the drawer head the tint sits on
`--bg-raised`, so the chip reads the same on any album color.
`--badge-text` per role: running, failed, warning and skipped labels wear their
role text color; completed and neutral labels stay `--text-secondary` so a long
completed list stays quiet. Health badges use the role text for every state.

## Type

System stacks only: `-apple-system`, SF Pro Display / SF Pro Text, Helvetica
Neue, PingFang SC, then `--font-system`; mono is `ui-monospace`, SF Mono, Menlo.

| Role | Size / line | Weight | Tracking |
| --- | --- | --- | --- |
| Body (`--fs-body`) | 15 / 22 | 400 | -0.01em |
| Page title | 34 / 40 (30 / 36 phone) | 700 | -0.03em |
| Section title | 22 / 28 (20 / 26 phone) | 700 | -0.02em |
| Hero title | 64 / 66 (34 / 38 phone) | 800 | -0.035em |
| Live card title (`--fs-live-title`) | 26 / 30 (20 / 24 phone) | 700 | -0.025em |
| Live card artist (`--fs-artist`) | 17 / 22 (15 / 20 phone) | 500 | |
| KPI value | 30 / 34 (26 / 30 phone) | 600 | -0.03em, tabular |
| Eyebrow (status, hero kicker) | 12 / 16 | 600 | +0.04em, uppercase |
| Logs (`--fs-mono-log`) | 12.5 mono | 400 | |

## Shape

Radii nest as DESIGN.md's "Radii nest" says, in three levels. Cards, tiles,
running cards, the banner, the drawer sheet, dialogs and the notices that
stand on the page take `--r-card`, the
panel radius plus the card padding (28 px, 24 under 640 px), and so does the
top of a phone bottom sheet. What they hold takes `--r-panel`, 8 px: fields,
nav and palette items, artwork and covers, inset lists,
notices, drawer groups, quality slots and icon tiles. Keys,
inline code, loading bars and a chart bar's top take `--r-tag`, 4 px. The
palette and toasts float with `--r-float`, 16 px, since their items sit
8 px in. Buttons, badges, chips, level chips, the segmented control and its
thumb, the selection bar, the save bar, New download's compose field and
its dock are pills (the field keeps its half-height radius when its text
runs to more lines); on phones, where its
actions can wrap under the count, the selection bar's radius is its 44 px
pills' plus its 8 px padding (30), still a capsule on one row. Data marks keep
`--r-mark`, 2 px. Every other corner is `--corner`, `superellipse(1.25)`
where the browser draws `corner-shape` (Chrome and Edge 139+); Safari and
Firefox draw the same radii as circular arcs. Large pills (Download N, dialog and save bar
buttons) are 36 px (`--btn-lg-h`, 44 on phones). The switch
is 44 x 26 with a 20 px thumb; selection circles are 22 px
(`--check-size`). Progress cells are 8 px capsules with
a 3 px gap; remaining cells are outlined, not filled. In the queue list the
cells join into one 6 px capsule bar (no gaps, remaining = `--track`), because
the cell count changes from row to row.

## Elevation and motion

Surfaces are flat: no card shadows or borders. Only artwork and floating layers
lift. Covers on album-colored cards cast a shadow tinted with the album's own
text color; shelf covers and New download's link covers cast one tinted with
`--art-bg`. Sidebar, tab bar, top bar, palette, sheets and toasts are
translucent with a backdrop blur; the save bar and the Queue selection bar
float over the content they stick to, so they are frosted `--toast-bg` with
`--shadow-float`. Live covers scale 1.02 on hover,
only under `prefers-reduced-motion: no-preference`. Running shows a
three-bar equalizer (two pseudo-elements animating `scaleY`, out of phase); it
freezes into a static glyph under reduced motion. Everything else uses the
base durations (120 ms color, 200 ms overlays).

## Components

- **Shell**: brand mark is a 34 px red gradient tile; "Console" group label via
  `.nav::before`; nav glyphs in secondary ink (the current one primary); the
  current item is filled, with the panel radius. Logs at the sidebar's foot
  has a secondary, regular-weight label until it is the current page. The top
  bar is the frosted canvas with, in simulate mode, a neutral "Simulate" tag;
  the live dot alone while connected ("Reconnecting in 3 s" or "Offline" in
  a surface pill otherwise; on phones without the fill, the word still next
  to the dot); the search pill ("Search or paste a link…", ⌘K or Ctrl K);
  and one theme button that cycles System, Light and Dark. A segment's count
  (the Queue tabs) is primary ink in regular weight, since secondary text
  falls under 4.5:1 on the track and on the dark thumb. Inline code is
  primary ink on `--bg-fill` for the same reason. The icon rail (640-1023) shows the active count as a
  neutral `--bg-fill` badge.
- **New download entry points**: in the sidebar a red pill as tall as a nav
  row (36 px, 15 px semibold) above the "Console" label, a slightly darker
  fill (`--accent-hover`, no ring) while New download is open; on the rail a
  40 px red disc. Under 640
  px the third tab's mark is a 48 x 30 red capsule with a soft red shadow
  that rises 6 px over the 24 px icon line, so "New" sits on the other
  labels' line; Settings turns red like any current tab while Logs is
  shown.
- **Page head**: title with its summary line beneath (15 px secondary; only
  Overview (the date) and Queue (its live counts) have one), as an Apple
  large title. Page actions are icon buttons at the row's end (Settings' config
  keys, Logs' link to its settings); New download lives in the sidebar and
  the tab bar.
- **Sections**: every card with a head is a shelf: the head sits on the
  canvas, the body is the grouped surface. Section actions are red text links
  with a chevron; an action in place (a `button`, such as Clear or Reset) is
  red text without one. A shelf never clips (only its body does), so a
  heading's glyph overshoot is never cut by the rounded corner.
- **Fields in a grouped surface**: text fields, selects and inset lists are `--bg-inset` cells (white on the gray surface in light,
  the second grouped level in dark), still outlined in `--border-control`.
  Dotted config keys (`.field-key`, shown on demand) are quiet mono text with no
  fill. Selects draw their own 12 px chevron 12 px in from the right edge
  (`appearance: none`), at the height and radius of the text fields beside
  them. On touch screens (under 640 px or a coarse pointer) every field is
  16 px, so iOS Safari does not zoom into it.
- **Live cards**: under the cells a legend names each shape with its count
  (filled pill "saved", outlined "remaining"; states at zero are left out).
  Failures of the last 24 h sit above them in a "Needs attention" card as grouped rows: a red circle
  glyph, "Failed" as a tinted chip, the title and the mono link, the error and its age,
  and a Retry button (under the text on phones).
- **Overview order**: Needs attention (only with failures), Now downloading (an
  idle card as tall as a job's when nothing runs, its content centred), Recently completed, Jobs, Throughput, each full width, so
  the phone and desktop reading order are the same.
- **Live queue**: each job is a card filled with its album gradient, in
  columns at least 440 px wide (a lone job spans the row, as the idle card does); 136 px
  cover (96 on phones); eyebrow = equalizer + RUNNING in `--art-t3`; title on
  one line, so every card (and the idle one) is the same height; artist in `--art-t2`; genre · year · started below; per-track
  cells in `--art-t1`, remaining cells outlined at 75 %; before a track
  finishes, the job's progress fills the cells in `--art-t1` too. A job without a palette
  (queued, resolving) falls back to the grouped surface.
- **Recently completed**: no surface behind it. The latest release is a card in
  its album gradient like a running job's (112 px cover, 88 on phones, its
  motion cover playing; an uppercase LATEST · ALBUM eyebrow, the title at the
  live-card size, the artist, one line of facts). Under it a wall of two rows
  of covers with no tile fill (at least 100 px each, so three per row on a
  phone): the cover with a tinted shadow, the title in 14 px, the artist in
  13 px secondary.
- **Jobs**: one grouped surface, five hairline-separated columns (three per row
  on phones); icon and label share a line; a zero value goes secondary. No
  sublabel: label and number set the height, equal across columns. A hovered
  column fills its whole box and the surface's radius clips the end ones, as
  a grouped-list row does.
- **Settings status**: board tiles are cards; a tile's or a head's icon tile
  is `--bg-fill` with a secondary glyph, solid in its role (glyph in
  `--on-role`) only for a warning or danger. Charts use `--track` for the
  empty part, `--chart-bar` for meters and hourly bars, `--series-1..4` for
  the formats bar, `--success`/`--danger` for hook outcomes and `--info`
  for busy worker cells. The `{ }` keys button sits at the end of the page
  head's title row.
- **Grouped lists (keyline)**: every hairline starts at the text and runs to
  the card's right edge: label/value lists, Settings fields, fact rows,
  hooks and action blocks.
- **Throughput**: totals as 20 px figures over their labels; the per-day
  bars are `--chart-bar`. The formats bar (Settings › Quality) takes one hue
  per position (`--series-1` to `-4`: blue, orange, purple, gray).
- **Queue**: one grouped list, 48 px covers, hairlines inset past the cover.
  Only a queued or running job draws progress; a finished row is title, then
  artist and track count, and a plain completed status is a green check with
  no word. A failed row says what went wrong in red under the link and ends
  in a Retry pill. Phones get two-line rows with progress under the title, a
  status-tab chip row that scrolls sideways when it must, an edge fading where
  chips lie past it (the Failed count in red text) and
  Select, which reads Done while selecting.
- **Queue selection**: Select wears the toggle pill of Pause and Wrap
  (outlined or filled while off, the pressed fill while on). Rows gain Apple
  edit-mode circles: a 22 px ring in `--border-control`, then an `--ink` disc
  with an `--on-ink` check (the select-all circle is a neutral gray disc
  with a bar while only some are chosen); under `forced-colors` the system checkbox returns. The circle
  sits one row gap (12 px) before the cover, the hairline starts at the text,
  and the whole row, circle included, hovers and fills `--bg-selected` as
  one. The bar above the list is a frosted raised capsule with the float
  shadow (it sticks under the top bar while the list scrolls; on a phone it
  docks above the tab bar): the count in 15 px semibold ("3 selected · 1 shown"
  when a search hides some), Retry and Cancel as filled pills, "Remove 3 from
  list" red text; during a run the red meter and "Deleting 3 of 12" in
  semibold.
- **Drawer**: a floating 22 px sheet inset 8 px from the window (full screen on
  phones); the head is an album-colored card with a 112 px cover; times and
  action buttons are grouped cells, Remove (finished jobs) in red text and
  glyph as the one destructive cell. The album bar under the tracks head
  takes the base drawing in the skin's 6 px capsule (`--bar-h`, `--r-bar`).
  Track stage fills are calm secondary ink and turn red only on a track that
  is still running.
- **Logs**: level chips and Pause are one kind of toggle pill: pressed is
  `--chip-on-bg` (the gray fill in light, the raised thumb in dark) and a
  level chip then shows a check; off is outlined in both themes, and an off
  level chip hollows its dot. Wrap lines is the Apple switch with its label.
  Filled attribute chips, a grouped log surface.
- **New download**: the compose field is a filled capsule (half its height,
  round corners) whose glyph and Clear sit in circles 8 px in (6 on
  phones); typed in, it turns `--compose-bg-focus` with a 1 px
  `--focus-field` edge over a soft red halo, and its text is display type
  in medium weight with a red caret (body text once it holds a link). The
  options under it are the shared select and switch. The dock is the same
  capsule shape, frosted `--dock-bg` with `--shadow-float`, a 15 px
  semibold count, a 13 px secondary line and the red Download pill 8 px
  in. A glow sits behind the field (`--stage-glow-*`). Detected links
  are Queue-style rows: 48 px covers (8 px radius) with an `--art-bg` shadow,
  hairlines from the text, a 15 px semibold title with a gray E mark and
  Apple Digital Master (and a storefront that is not the default) as quiet
  filled tags on `--bg-inset`, one 13 px meta line, Available qualities a
  filled pill and remove a secondary glyph. Submitted reuses
  the Queue list language (covers, the joined capsule bar, hairlines from the
  text; on phones the bar starts under the title); an outcome row names its result in its role color beside the role
  glyph. Download is the red pill, the only solid red on the page. The
  browser shortcut's code is
  an inset cell; the steps are 13 px secondary.
  Search hits sit in Overview's section language: titled sections with a
  red Show more; a group alone puts a round ghost Back and the segmented
  track of the four groups where its title was. Albums, Playlists and
  Artists are its tile wall on the page (covers
  with the `--art-bg` shadow, 14 px titles, 13 px secondary lines; artists
  round and centred); an album's cover carries a frosted `--bg-chrome`
  quality tag, a library or queue badge and a 36 px frosted Download circle
  that turns red under the pointer. Songs are the link rows without the
  include box on one grouped surface, in up to three columns with
  hairlines from the text and none across columns, Download a secondary
  glyph. An open artist heads its albums with a 120 px round cover with the
  tinted shadow, the name in page-title type, Back a ghost button and
  Download all the large pill.
- **Qualities sheet**: per-slot summaries are 16 px `--bg-group` cells with a
  15 px semibold label; each track's formats are filled pills with the label
  in semibold; hairlines between tracks start at the title.
- **Settings**: the section list repeats the sidebar (secondary 18 px glyphs, 15 px
  labels, the current section a filled pill in semibold; 14 px on the rail).
  Fields are grouped rows whose hairlines run from the text to the card's
  right edge, as do panel rules, the rules between Server's groups, fact
  rows, hooks, action blocks and the account rows. Labels are 15 px; the switch is an Apple toggle (white thumb with a
  soft shadow, red when on, the off track keeping its `--border-control`
  outline, no On/Off word beside it). The ordered list is an inset list like
  the codec ranking. Panel actions
  in a block head (Refresh) and the token's Replace are red text, and so are an account's Log out and the token's Remove. The save bar is a frosted floating
  capsule with the float shadow: the count in 15 px semibold, where in 14 px
  secondary, Discard red text and Save a red pill, both 36 px; on phones the
  count may take two lines and the bar stays a capsule.
- **Dialogs**: sheets take the card radius. A confirm is an Apple alert: a
  centered 20 px bold title and 14 px consequence, then Cancel and the
  confirm button as two equal 36 px pills; a destructive one is red text,
  not a red fill. Three choices share one row, 520 px wide. On phones every
  bottom sheet takes the card radius on its top corners and 24 px above its
  content for the grabber.
- **Palette and toasts**: frosted raised surface, secondary item glyphs, 20 px input
  with no focus rule (the caret shows focus; the 1 px divider stays). On phones
  the palette is inset 8 px from the screen edge and drops the key hints;
  on touch its rows are 44 px and Cancel is red 16 px text. The shortcut
  sheet's keys and context words are secondary, what they do primary.
- **Notices**: role tint with the panel radius (the card radius on the page), 18 px icon; a link inside a warning or
  failure notice is underlined in the role's text color.
- **Buttons**: only the primary action is a red fill; it darkens on hover
  and darker still, at 98 % scale, while pressed. Disabled it turns gray
  (`--disabled-bg` / `--disabled-text`), never faded red. Destructive
  buttons (`.btn-danger`: Delete, Discard, Cancel job, Log out) are red text
  with the danger tint on hover.
- **Empty / loading / error**: base structure; skeletons follow the radii
  (the panel radius for tiles, the card radius for the hero).

## Do

- Put album color only where Apple provides it (`--art-*`); fall back to the
  grouped surface, never to a made-up tint.
- Pair every status color with a glyph and a word.
- Add new display sizes as `--fs-*` tokens in the token block.

## Don't

- Put shadows or borders on surfaces; elevation is for artwork and overlays.
- Use the accent red for success or for decoration on data that is healthy.
- Hide operations data to make room for artwork; move it down instead.

## Open questions

- SF Pro Display only exists on Apple platforms; Windows and Linux fall back to
  Helvetica Neue or Segoe UI and the tight tracking looks heavier there.
- Many light album colors at once on a black page can look loud.
- The live list payload cannot tell a track in flight from a waiting one, so
  the legend says "remaining" for both and there is no "13 % transferred".
- A meaningful sublabel per Jobs column ("of 256 slots", "3 workers") and
  icon tiles for the runtime facts need DOM that the console does not render
  yet.
- In dark, a field's `--border-control` outline is 3.36:1 against the surface
  around it but 2.75:1 against its own `--bg-inset` fill.
- The skin is one file per the skin contract (scoped by its name), past 60
  KB; it is ordered by area so a change stays local.
