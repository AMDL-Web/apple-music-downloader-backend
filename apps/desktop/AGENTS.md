# apps/desktop

The desktop app for macOS, Windows and Linux, drawn with MyGo's native UI
(`github.com/egoist/mygo/ui`), over the public HTTP and SSE API in both
modes: a client of a remote amdl, or standalone, where this computer runs
the backend with a wrapper here or on another machine.

## Rules

- The app reads the backend only through `internal/client`, in both modes,
  so it never depends on more than the API contract; bars and numbers come
  from `internal/progress` and `internal/format`, shared with the other Go
  clients.
- Standalone mode runs or finds the backend only through `internal/local`
  (`apps/desktop/internal/app#startOrAttach`): one backend per data
  directory. When another program, such as `amdl service`, already runs it,
  the app uses that one; otherwise it runs its own, which the CLI finds.
- `internal/` never imports this directory. Desktop-only code stays here.
- No `AMDL_*` environment variables: `internal/config#UnreadEnv` warns about
  each one. Development switches are command-line flags (`main.go`).
- Store fields change only on the UI thread. Background work hands results
  over through the store's post function (`Window.Update`).
- A remote backend's paths are its machine's: never pass them through
  `path/filepath` or show them as this computer's.
- Colors come from `apps/desktop/internal/ui#Tokens`; an album's palette is
  used only after a contrast check (`ui#Readable`, `ui#AccentFor`).
- The views' corner radii come from the levels beside the tokens
  (`apps/desktop/internal/ui#radiusCard` and the constants after it), never
  a number: a box inside another takes the outer radius less the room
  between them, so both corners share a center. Continuous (squircle)
  corners are macOS only, by choice: MyGo turns them on there alone
  (continuousCorners in its ui/paint.go).
- Focus shows as MyGo's own ring, no accent border or glow: on a text field
  whenever it has the focus, as macOS's text fields, on other controls for
  keyboard focus only. A box whose focused element is a part of it, as a
  field's input, rings itself with `focusRing`.
- A button that can be disabled is built with `buttonOff`, not `button`
  then `Disabled`: MyGo reports hover until the element is disabled.
- Scroll containers go through `scrollArea` (or `scrollState` where one
  cannot): MyGo draws thumbs at all times over the content, so the app hides
  them at rest where the desktop does, keeps lists clear of them and fades a
  cut last line.
- A drawing with its own rate (`Painter.AnimationFrameRate`, as a stage
  bar's stripes) holds still while a list scrolls (`App.scrolling`,
  `widgets.StageBar` Still): MyGo paces the window, and on macOS the
  display, to that rate, so the list would scroll at 30 frames a second.
- A quick answer must not flash or move the layout: waiting words, greyed
  controls and dimmed results wait for `apps/desktop/internal/ui#waited`
  (`statusDelay`), not a store's busy flag; earlier results, typed fields
  and errors stay until the answer. Each async action is tested with
  `apps/desktop/internal/ui/flicker_test.go#noFlash`.
- No cgo: every target cross-compiles with `CGO_ENABLED=0`.
- MyGo paints absolute children above in-flow ones, in build order: a
  decorative layer goes before an absolute content layer, as the main
  window's view (`apps/desktop/internal/ui#App`) builds it, never into a
  card beside its content.
- A shortcut that must work while the keyboard is in home's field is
  declared from the view's root (`apps/desktop/internal/ui#App` builds them
  in `shortcuts`): one declared deeper only hears its own subtree. Keys the
  field edits with, Return and the arrows, reach the field first, so it
  takes them itself with OnTextKey (`fieldKey`).
- The setup screen runs a package manager (Homebrew or winget, never one
  needing sudo) only from Run, under the exact command it shows. Tests and
  shots give `apps/desktop/internal/setup#Deps` fakes: nothing installs or
  downloads.
- ffmpeg and other console programs start through
  `internal/sysproc#NoConsole`, or each start flashes a console on Windows.
- The browser build (`make desktop-web`) is the demo, on the in-memory
  backend of `internal/demo`: what runs this computer's backend stays in
  `!js` files, and a new client call gets a route there.

## Verify

```bash
go test ./apps/desktop/...
for os in windows darwin linux js; do GOOS=$os GOARCH=$([ $os = js ] && echo wasm || echo amd64) go vet ./apps/desktop/...; done
```

Screens: build the app and run it with `-shots <dir>` (and `-demo`,
`-remote` or `-wrapper`, `-data <dir>`), which captures every screen in a 1280×800 window
with `Window.CapturePage`, renders playing motion covers without a window
(`*-motion-N.png`), and quits. The window shows stills where the desktop asks
for less motion; the renders do not. CapturePage draws no window material,
so shots give the sidebar its solid ground
(`apps/desktop/internal/app#App.opaque`).
