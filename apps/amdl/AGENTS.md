# apps/amdl

The `amdl` binary: `amdl serve`, the agent-facing commands, the MCP server
and the terminal interface (`docs/cli.md`).

## The terminal interface in a browser

`apps/amdl/web` runs `apps/amdl/internal/tui` over the in-memory backend of
`internal/demo`, compiled to WebAssembly and drawn by xterm.js. `make tui-web`
builds the page; CI runs it.

- bubbletea and github.com/atotto/clipboard ship no js files. The build adds
  the ones in `apps/amdl/web/build/shim` to copies of the two modules
  (`apps/amdl/web/build#shims`), so a bump of either must still pass
  `make tui-web`. When it fails, fit the shim to the new release's files for
  the other platforms; when the release brings a file of the shim's name,
  the build says so and the shim goes.
- The TUI reaches the backend only through `internal/client`. A call it
  starts making needs a route in `internal/demo` too, or the browser build
  answers it with an error.
- xterm.js and its WebGL renderer come from npm, pinned with their
  integrity at `apps/amdl/web/build#xterm`.

## Verify

```bash
go test ./apps/amdl/...
make tui-web
```
