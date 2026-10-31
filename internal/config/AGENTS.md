# internal/config

Config loading, validation and persistence. The runtime-key list, env overrides and
the `Default()`-equals-example rule are in the package comment
(`go doc ./internal/config`) and its tests. What a key's example comment must say
is in root `AGENTS.md` "Config".

- Classify every new key with `isRuntimeKey`. Environment overrides exist only for
  startup keys, so `PUT /api/v1/config` never has to refuse a key the environment
  would mask.
- API writes go through `Store.UpdateAndSave`, which re-reads the file under its
  lock so a hand edit is not overwritten; `Store.Set` does not touch the file.
- Unknown keys fail startup, so renaming or removing a key is a breaking change
  (root `AGENTS.md` "Contracts and breaking changes").
- A runtime `download.*` key either gets a same-named `DownloadOverrides` field or
  an entry in `overrideExclusions` with the reason in the `DownloadOverrides`
  godoc.
- `configs/hooks.example.yaml` belongs to `internal/hooks`; its comments document
  it the same way.

The full list of files a new key touches is in
[amdl-api-change](../../.claude/skills/amdl-api-change/SKILL.md).

## Verify

```bash
go test ./internal/config/ ./internal/api/ ./apps/console/
```
