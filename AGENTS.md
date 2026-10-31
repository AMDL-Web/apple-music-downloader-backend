# amdl

The monorepo for the AMDL project, which downloads music from Apple Music: resolve a
link, then download, decrypt, remux and tag each track. Code map: the
[README reading map](README.md#reading-map); vocabulary: `GLOSSARY.md`; docs index:
`docs/README.md`.

## Products and layout

- `apps/amdl`: the one binary `amdl`. `amdl serve` is the backend, a thin wrapper around
  `internal/server#Run`, which wires the core and serves REST and SSE under `/api/v1`
  with a SQLite store; its image is `deploy/server/`. The other commands are the
  agent-facing client (`docs/cli.md`).
- `apps/console`: the web console, embedded into the server. Rules in
  `apps/console/AGENTS.md`.
- `apps/site`: the bilingual static website, built by a zero-dependency Node script. Rules
  in `apps/site/AGENTS.md`.
- `apps/desktop`: the desktop app for macOS, Windows and Linux, drawn with MyGo's native
  UI. It imports the same core and embeds the server in standalone mode. Each product
  keeps its own directory under `apps/`, including a future non-Go app with its own build.
- `internal/`: the Go core every product shares. It never imports a product; the server
  passes the console in through `internal/api#Deps`.
- `deploy/`: the Compose file and per-service image files. `tools/`: repository tooling.

## Working agreements

- What you decide alone and what waits for the maintainer is in `HUMAN.md`.
- Do the lightest fix first. A new mechanism (check, hook, CI step, scheduled job)
  needs the maintainer's OK after you state what it prevents, when it runs and who
  reads the result.
- Before changing a source file over about 20 KB, split one responsibility out of
  it in a move-only commit ([amdl-refactor](.claude/skills/amdl-refactor/SKILL.md)),
  or say there is no natural seam.
- Every wait and every outbound call is bounded: `timeout` on shell waits, a context
  deadline or client timeout in Go. A step that runs longer than expected goes to the
  background, and the report says what it is waiting on.
- A change to something a person uses is done only after you used it end to end
  yourself (for the console: start a download from the page and watch it finish). A
  gap you find is fixed or named in the report.
- Docs change in the commit that changes the behavior. `rg` the changed symbols,
  keys and rule words across `README.md`, `README.zh-CN.md`, `docs/`, the AGENTS.md
  files, `REVIEW.md`, `GLOSSARY.md`, `.claude/`, `internal/api/openapi.yaml`,
  `configs/config.example.yaml` and the website's `apps/site/content/`. When a doc
  and the code disagree, fix the doc.
- Reports keep changed, tested, pushed and released apart, and label behavior
  inferred from reading code as inference.
- Rules for some paths live in `.claude/rules/` and in `AGENTS.md` files under
  `apps/` and `internal/`; open the matching one before editing there.

## This service has no auth, on purpose

Every endpoint is unauthenticated; access control belongs to the deployment layer
in front of it (reverse proxy, gateway). Each install serves one user, and the
project is distributed as open source. Don't add auth middleware or multi-user
features, and don't report missing auth in a review.

## Contracts and breaking changes

The HTTP API (`internal/api/openapi.yaml`), config keys, the DB schema, SSE event
types and output paths are contracts that installs and the clients in this repository depend on.

- A breaking change needs a reason: it fixes a defect, removes duplication or makes
  a contract machine-checkable.
- Mark it with a `!` Conventional Commit (`feat(api)!: …`) and add a row (old
  shape, new shape, affected clients) to the upgrade guide in `docs/` for the next
  major version. Rename directly; no compatibility shims.
- Output paths are the exception. A change to path templates, `{Quality}` labels,
  name cleaning or sidecar names makes the "already downloaded" check miss and
  libraries download again, so it needs the maintainer's OK and an upgrade note
  (`internal/media/AGENTS.md`).
- What changes together with each surface (spec, handler, docs, README table,
  console, config example, migration) is listed in
  [amdl-api-change](.claude/skills/amdl-api-change/SKILL.md).
- The clients here change in the same commit as an API they use: the console, and
  `internal/client` and `internal/capability` for the CLI, MCP server, TUI and
  desktop app. The iOS app and amdl-web are set aside and get no follow-ups.

## Config

`configs/config.yaml` is generated: copied from `configs/config.example.yaml` on
first start, then rewritten without comments by `PUT /api/v1/config`. The example
is therefore the documentation. Each key's comment states allowed values, units,
the behavior when the key is absent, whether it is a runtime or a startup key, and
any template variables or list options.

## Apple catalog reads

Every catalog read goes to `amp-api.music.apple.com`, Apple's undocumented web-player
API, with a JWT scraped from music.apple.com plus an `Origin` header. The backend holds
no developer token and never calls the documented `api.music.apple.com`. Reads are of
two kinds (`internal/applemusic/AGENTS.md`):

- Required: the link's resource (song, album, playlist, station tracks, artist and its
  albums) and each track's song metadata. A retryable error is tried up to
  `download.max_attempts` times, a plain 4xx only once, and then the job or that track
  fails; there is nothing to download without them.
- Optional: `editorialVideo` motion artwork, the album read that enriches a song, a
  private playlist's library cover, and the artist read for a standalone artist cover.
  Each degrades to "not available" and never fails a job. A song with no usable web or
  device manifest falls back to the next codec.

## Data safety

- A schema change is a new migration that keeps every existing row
  (`internal/db/AGENTS.md`). Before applying one to a real database, describe its
  impact and get the maintainer's confirmation.
- Never commit `configs/config.yaml`, `configs/hooks.yaml`, `*.p8` files, `keys/` or
  `data/`: they hold tokens, private keys and local state.

## Language

Everything in the repository is English: code, comments, docs, config comments, CI,
OpenAPI descriptions and commit messages. Exceptions: `README.zh-CN.md` (a
translation of `README.md`), the website's Chinese page
(`apps/site/content/zh-CN.json`), hand-written release notes (may be Chinese), and
review-bot PR comments (Simplified Chinese, set in `REVIEW.md`).

## Verification

CI (`.github/workflows/ci.yml`) runs the `make check` steps on pushes to `main` and
`dev` and on pull requests, and builds the Docker image. Locally, run what the
change touches; [amdl-verify](.claude/skills/amdl-verify/SKILL.md) has the live API
and console checks and the report format.

| Change | Run |
| --- | --- |
| Docs, AGENTS.md, rules, skills | `go test -count=1 ./tools/repocheck/` |
| Go logic | the package's tests, then `make test` |
| Decryption (`internal/fairplay`, `tools/temaridiff`) | `go test ./internal/fairplay/` and `make temaridiff`, which `make check` does not run |
| Concurrency (jobs, media pools, events, SSE) | `make race` |
| API routes, schemas, SSE | `go test ./internal/api/` and a live check with `make dev` |
| Config keys | `go test ./internal/config/ ./internal/api/`, then reread the key's example comment |
| DB schema | `go test ./internal/db/` and a run against a copy of a real database |
| Console | the commands in `apps/console/AGENTS.md` and a browser check |
| Website | `node --test apps/site/` and a browser check |
| Dependencies | `make vuln` |
| Scripts, release tooling | `make node-test` |
| Dockerfile, entrypoint | `make docker` |
| Before pushing to dev | `make check` |

Add a behavior test where a regression is likely, not one that restates the
implementation. Say what you could not verify and why.

## Commits and releases

- Work lands on `dev`; small changes need no branch of their own. `main` changes
  only through the `dev` → `main` release PR. Rebase onto `origin/dev`.
- Every commit carries a DCO `Signed-off-by` trailer (`git commit -s`). Non-merge
  commits follow Conventional Commits.
- Only Codex appends `Co-authored-by: Codex <noreply@openai.com>`; other agents use
  their own attribution.
- Releases follow [amdl-release](.claude/skills/amdl-release/SKILL.md); release-note
  attribution is in [CONTRIBUTING.md](CONTRIBUTING.md#releases).

## Merging with `--admin`

A branch-protection bypass needs the maintainer's approval for that specific
bypass; "open and merge the PR" is not it. When reviews, checks or unresolved
conversations block a merge, report what blocks it and wait.

## Related repositories

[wrapper-lite](https://github.com/WorldObservationLog/wrapper/tree/lite) holds the
Apple Music account session and serves manifests, licenses, lyrics and per-key
decryption templates over HTTP (`internal/wrapper`); the backend decrypts in-process.
[wrapper-manager v2](https://github.com/WorldObservationLog/wrapper-manager/tree/v2) may
sit in front of it and adds the login API the console uses; a bare wrapper-lite answers
`501 login_unsupported`.

## Rule -> enforced by

| Rule | Enforced by |
| --- | --- |
| gofmt, build, vet, tests, race detector, govulncheck, Node tests | CI, one step per `make` target |
| The Docker image builds | CI docker build job |
| Config example key set and defaults match the struct | tests in `internal/config` |
| Routes, error codes and runtime config keys match `internal/api/openapi.yaml` | parity tests in `internal/api` |
| Output paths | golden tests in `internal/media` |
| `internal/fairplay` decrypts like upstream Temari, and `internal/fairplay/rounds_gen.go` is the pinned version's translation | CI Temari parity job (`make temaridiff`); Renovate opens a PR per Temari release |
| Console: API paths exist in the spec, Settings cover runtime keys, design guard, vendor checksum | tests in `apps/console` |
| Docs links, backticked paths, `path#Symbol` pointers, AGENTS.md size, CLAUDE.md pairs, docs types and index | `tools/repocheck` tests |
| DCO sign-off | DCO GitHub App on PRs; `.claude/hooks/guard-commands.mjs` on `git commit` |
| `main` only through a PR | branch protection; guard hook on `git push` |
| No `--admin` merge without approval | guard hook on `gh pr merge` (Claude Code only) |
| Never commit config, keys or data | `.gitignore`; guard hook on `git add -f` |
| No auth, breaking-change marking, clients updated with the API, full config comments, row-preserving migrations, optional amp-api reads degrade | text and `REVIEW.md` |

The guard hook runs where Claude Code loads `.claude/settings.json`; other agents
follow the text.

## Code review rules

@REVIEW.md
