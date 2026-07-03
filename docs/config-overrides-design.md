# Config Override Architecture (request > user > global)

## Goal

Resolve the effective download configuration per job from three layers:

1. **Request level** — the optional `config` object in `POST /api/v1/downloads`.
2. **User level** — the per-user overlay stored in `users.overrides_json`,
   managed via `PUT /api/v1/me/config` (self) or the admin users API.
3. **Global fallback** — the `download:` section of `configs/config.yaml`.

Higher layers win field-by-field; unset fields inherit from the layer below.

## Core types and flow

- `domain.DownloadOverrides` (internal/domain/domain.go) is a sparse overlay:
  every overridable field of `config.DownloadConfig` as a pointer, JSON keys
  matching the yaml keys. `ParseDownloadOverrides` decodes strictly (unknown
  fields are a 400), `MergeDownloadOverrides` flattens layers (later wins).
- `config.ApplyDownloadOverrides` (internal/config/overrides.go) applies an
  overlay onto a `DownloadConfig`. `config.ValidateDownloadOverrides` reuses
  the global download-section validation plus stricter bounds for
  user-supplied values (`max_parallel_tracks` 1–16, `retries` 0–10, cover
  size shape, positive ALAC limits).
- **Submit** (`jobs.Manager.SubmitBatch`): loads the submitting user's stored
  overlay, merges `user < request`, and snapshots the merged overlay onto
  every accepted job (`jobs.overrides_json`). The global config is *not*
  baked into the snapshot — it stays the live fallback at execution time.
- **Execute** (`media.Downloader.forJob`): applies the job's snapshot onto
  the global download config, then scopes `downloads_dir` to the owner's
  directory. All path segments still pass through `safeName`, so overridden
  folder names and templates cannot escape the download root.

## Snapshot semantics

The user+request merge is frozen at submit time. Editing a user's config (or
restarting the backend) never changes how an already-queued job downloads.
Changing `configs/config.yaml` *does* affect queued jobs for fields no layer
overrode — the global layer is resolved live.

## Not overridable

`downloads_dir`, `temp_dir`, and `max_running_jobs` are system-owned:
worker-pool size is fixed at startup and filesystem roots must not be
reachable from API callers. `domain.DownloadOverrides` simply has no fields
for them.

## Storage

- `users.overrides_json TEXT NOT NULL DEFAULT '{}'` — existed since the
  multi-user schema; now read/written by the users store. The overlay is
  written only by `CreateUser` and the targeted `UpdateUserOverrides`;
  `UpdateUser` deliberately never touches it, so a read-modify-write update
  of role/avatar/identities cannot revert a concurrent config save.
- `jobs.overrides_json TEXT NOT NULL DEFAULT '{}'` — new column, added by an
  idempotent `pragma_table_info` check + `ALTER TABLE` on startup (same
  pattern as `jobs.user_id`). Existing rows read back as "no overrides".
- Read-back is *lenient* (unknown keys ignored), unlike the strict API
  parser: a row written by a newer or older binary must still scan, or its
  owner would be locked out of auth and job recovery would abort startup.
  Reflection-based tests keep the struct, `Merge`, `Apply`, and the OpenAPI
  schema field lists in lockstep.

## API surface

- `POST /api/v1/downloads` — optional `config` object; invalid → 400. A
  failure to load the submitter's stored config is an infrastructure error
  (500, batch unprocessed, safe to retry) — never a per-URL `invalid`
  verdict.
- `GET /api/v1/me/config` — `{config, effective}`; `effective` is the user
  layer applied to the global config, projected through the overridable
  field set so system-owned fields (present and future) stay hidden.
- `PUT /api/v1/me/config` — replaces the caller's layer; `{}` clears; 409 in
  single-user mode (no user profile exists).
- `POST/PATCH /api/v1/users` — admin can set a user's `config`; on PATCH the
  field replaces the whole layer when present, `{}`/`null` clears, absent
  leaves unchanged.
- `Job` JSON now carries the snapshot as `config`.

## Dedup interaction

Canonical keys ignore overrides: submitting the same content with different
config while a job is active still reports `duplicate_active`. Overrides
change *how* content downloads, not *what* content a job represents.
