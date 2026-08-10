# amdl-backend

Go service that resolves Apple Music links and runs the download → decrypt →
remux → tag pipeline. REST + SSE on `/api/v1`, SQLite store. It owns
`internal/api/openapi.yaml`, which the iOS app and web frontend mirror by hand
(see the [repository map](../AGENTS.md)).

Stable phase: existing API contracts, config keys, DB schema, and output-path
conventions are load-bearing for deployed installs.

## This service has no auth, on purpose

It is the download core. Every endpoint is unauthenticated, including
`GET /api/v1/developer-token`, which returns a usable Apple Music developer
token. Access control is the responsibility of the deployment layer above this
service (reverse proxy, gateway, or frontend session), not this codebase.

Don't add auth middleware here, and don't report missing auth as a finding in a
review — it's a deliberate architecture boundary, not an oversight.

## Config: four layers, and the backend writes only one

Effective value = environment (`AMDL_*`) over `configs/config.yaml` over the
database over `Default()`. `PUT /api/v1/config` writes the **database** layer and
nothing else — no code writes the config file. `internal/config/resolve.go` is
where the stack is merged and every key's winning layer is recorded.

`configs/config.yaml` is hand-written, tracked, optional, and **partial**: only
the keys actually spelled out in it override anything. That makes presence
meaningful — a key written there is pinned, and `PUT` answers 422 rather than
accepting a write that a higher layer would shadow. The shipped file keeps
startup-bound keys active (nothing else can set them) and every runtime-mutable
key commented out (the API owns them). Don't activate a runtime key in it.

It is also the documentation, so it has to carry the full comment for each key —
allowed enum values, valid booleans, numeric units and default behavior, list
item options, supported template variables. `internal/config/config_test.go`
fails if its key set drifts from the struct, or if uncommenting every key stops
resolving to exactly `Default()`; it can't check whether your comment is any
good.

`isRuntimeKey` in `runtime.go` is the single authority on which keys the
database layer may hold. Adding a key there also lets a stored row set it, so
it must be something the running process actually re-reads.

## Two Apple hosts, not one

`api.music.apple.com` is the documented API and takes the self-signed developer
token. `amp-api.music.apple.com` is Apple's internal web-player endpoint and only
answers to a JWT scraped out of music.apple.com's JS bundle, with an `Origin`
header attached.

Some fields exist **only** on the amp-api side, and no amount of `extend=` will
coax them out of the public host — `enhancedHls` and `editorialVideo` (animated
covers) are both like this. Apple states outright that a third party may load
only `artistUrl` as an extended attribute on Albums. So MusicKit in the iOS app
cannot reach them either; it goes through the public host. Verified on a real
device, not assumed.

`apiBase()` picks the host, and `doWithWebAuth` handles the scraped token. When a
field turns up empty for no apparent reason, check which host you asked.

Being undocumented, amp-api can change without notice. Anything read from it must
degrade to "this album doesn't have that" rather than failing a job.

## Data safety

Schema changes need a migration that preserves existing rows, and confirmation
before you apply one. Describe the migration impact rather than assuming a
destructive rebuild is acceptable.

`explore/` is archived upstream repos: read-only, outside the build, no longer
used for design guidance.

## Commits and releases

Full workflow in [CONTRIBUTING.md](CONTRIBUTING.md). What bites in practice:

- Only `main` requires a pull request. Work lands on `dev` directly — small
  changes don't need their own branch, and cutting one off `main` puts the work
  in the wrong place. `dev` is promoted into `main` by PR for releases.
- Every commit needs a DCO `Signed-off-by` trailer (`git commit -s`). The
  [DCO app](https://github.com/apps/dco) blocks the PR otherwise. Non-merge
  commits follow [Conventional Commits](https://www.conventionalcommits.org/).
- Codex — and only Codex — appends `Co-authored-by: Codex <noreply@openai.com>`.
  Other agents use their own attribution and leave this footer alone.
- Release notes may be hand-written at `.github/release-notes/<version>.md`; the
  release workflow prefers that file when non-empty, else auto-generates.
- In those hand-written notes, each entry lists its commit author and all
  `Co-authored-by` contributors, deduplicated. Use the `@username` form whenever
  the GitHub account is identifiable — plain model-name text renders as text, not
  a mention, so the avatar never shows up. `Codex <noreply@openai.com>` → `@codex`;
  any `Claude … <noreply@anthropic.com>` co-author, whatever the model name → `@claude`.

## Merging with `--admin`

Bypassing branch protection needs the user's approval **for that specific bypass**.
"Open and merge the PR" is not it. If required reviews, status checks, or
conversation resolution block the merge, report what's blocking and wait.
