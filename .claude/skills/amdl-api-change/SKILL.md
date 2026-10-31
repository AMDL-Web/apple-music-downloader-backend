---
name: amdl-api-change
description: What changes together when an amdl contract surface changes - HTTP routes, request and response schemas, error codes, SSE event types, job statuses, config keys, DB columns, output paths - and how a breaking change is marked. Use when adding or changing an endpoint, field, enum value, event, setting or column, or when asked to "add an API", "expose X" or "add a setting".
---

# Change a contract surface

The rule for when a break is allowed and how it is marked is root `AGENTS.md`
"Contracts and breaking changes". This page lists what lands in the same commit.
Parity tests catch some omissions; the docs, README and console items below are
checked by nothing but you.

## Compatible or breaking

| Surface | Compatible | Breaking |
| --- | --- | --- |
| Route | new route | removed or renamed route, new required parameter |
| Request, response | new optional field, new enum value clients may ignore | removed or renamed field, changed type or meaning, newly required field |
| SSE event | new event type, new payload field | renamed event, removed field, changed cursor semantics |
| Config key | new key whose default keeps behavior | renamed or removed key, changed default behavior |
| DB schema | new column with `DEFAULT`, new table | dropped, renamed or rebuilt table or column |
| Output path | new template variable | changed default template, `safeName`, quality labels, sidecar names: ask the maintainer first |

New fields follow their neighbors in `internal/api/openapi.yaml` (snake_case,
existing enum spellings) and say in the spec whether they are always present.
A field filled by an optional catalog read (root `AGENTS.md` "Apple catalog reads"),
or one Apple may leave out of a response, is optional.

## Same commit

Route, request or response:

- `internal/api/openapi.yaml`;
- the handler and its line in `internal/api#Server.routes`;
- a handler test (httptest against `Server.Routes`, errors checked with
  `assertErrorCode`);
- [docs/api.md](../../../docs/api.md) and the API table in `README.md` and
  `README.zh-CN.md`;
- `apps/console` when it calls the route or reads the field, and then the website
  demo's fake backend, `apps/site/demo/fake-api.js`;
- an `internal/capability` entry for a new operation (a test fails without it),
  and `internal/client` when a Go client (CLI, MCP server, TUI, desktop app)
  calls the route or reads the field.

Error code: the constant in `internal/api/errors.go`, the spec's `ErrorCode` enum
and the table in docs/api.md. A per-URL submit code also goes in the spec's
`SubmitErrorCode` enum.

Event type or job status: the `internal/domain` constant, the OpenAPI enum, the
internal/db queries that filter on status, the stream close rules in internal/api
(`eventsExhausted`), the [Events](../../../docs/api.md#events) section of
docs/api.md, and for hook events the allowed list in internal/hooks,
`configs/hooks.example.yaml` and [docs/automation.md](../../../docs/automation.md).

Config key: the struct field (yaml and json tags), `Default()`, `Config.Validate`,
the full comment in `configs/config.example.yaml`, the runtime or startup
classification (`internal/config/AGENTS.md`). A runtime key also gets its
`RuntimeConfig` property in the spec and its Settings field in
`apps/console/static/js/lib/settings.js`; a runtime `download.*` key gets a
job override or an exclusion. Concept changes go to
[docs/configuration.md](../../../docs/configuration.md).

DB column: a new migration (`internal/db/AGENTS.md`), the read and write queries
(`jobColumns` and `scanJob` for jobs), the `internal/domain` field, a migration test
case, and the spec and docs/api.md if the field is exposed.

Breaking change: a `!` commit title and a row in the upgrade guide in `docs/`.

## Verify

```bash
go test ./internal/api/ ./internal/config/ ./internal/db/ ./internal/client/ ./internal/capability/ ./apps/console/
go test -count=1 ./tools/repocheck/
```

Then `make dev` and exercise the change with curl, error cases included
([amdl-verify](../amdl-verify/SKILL.md)). Done when a live request returns exactly
what the spec says. The report names the surface, compatible or breaking, and
whether and where the clients were updated.
