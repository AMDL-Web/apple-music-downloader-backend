# Apple Music Developer Token Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Sign and cache a 24-hour Apple Music developer token when complete `.p8` configuration exists, bypassing website token discovery.

**Architecture:** Extend `CatalogConfig` with an all-or-none signing unit. Put standard-library ES256 JWT construction in `developer_token.go`; make `CatalogClient.Token` select local signing, explicit token, or legacy website discovery in strict precedence order.

**Tech Stack:** Go 1.24, Go standard-library cryptography, YAML v3, Go testing package.

## Global Constraints

- Complete signing configuration never falls back after an error.
- JWT lifetime is 24 hours; cache renewal begins five minutes before expiry.
- Never log the private key or complete JWT.
- Add no third-party JWT dependency.
- Preserve website discovery only when signing and `authorization_token` are absent.

---

### Task 1: Signing configuration and validation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `configs/config.yaml`

**Interfaces:**
- Produces fields `AppleMusicPrivateKeyPath`, `AppleMusicKeyID`, and `AppleMusicTeamID` on `CatalogConfig`.
- Produces `func (CatalogConfig) DeveloperTokenEnabled() bool`.

- [ ] **Step 1: Write failing tests**

Add table tests showing all-empty disables signing, all-present enables it, and every partial combination makes `Load` fail with `requires all three fields`. Use `writeConfig` for strict YAML loading.

- [ ] **Step 2: Verify RED**

Run `go test ./internal/config -run DeveloperToken -count=1`. Expect compilation failure because the fields and helper do not exist.

- [ ] **Step 3: Implement minimal configuration support**

Add the three `yaml` fields with `json:"-"`; implement `DeveloperTokenEnabled` using trimmed nonempty values; in `Config.validate`, count configured values and reject counts one or two with an error naming all three YAML keys.

- [ ] **Step 4: Update sample configuration**

Add these fully commented values under `catalog`:

```yaml
# string path: Media Services PKCS#8 .p8 key; set together with key ID and team ID. All empty preserves legacy behavior.
apple_music_private_key_path: "keys/AuthKey_88KBJL3CKU.p8"
# string: Media Services key ID used as JWT kid; required with private key path.
apple_music_key_id: "88KBJL3CKU"
# string: Apple Developer team ID used as JWT iss; required with private key path.
apple_music_team_id: "2VTXNMR2GL"
```

- [ ] **Step 5: Verify GREEN and commit**

Run `gofmt -w internal/config/config.go internal/config/config_test.go && go test ./internal/config -count=1`. Expect PASS. Commit source, tests, and sample config as `feat: configure Apple Music developer tokens` with the required Codex co-author footer.

---

### Task 2: ES256 developer token signer

**Files:**
- Create: `internal/applemusic/developer_token.go`
- Create: `internal/applemusic/developer_token_test.go`

**Interfaces:**
- Consumes the Task 1 `CatalogConfig` fields.
- Produces `signDeveloperToken(cfg config.CatalogConfig, issuedAt time.Time) (token string, expiresAt time.Time, err error)`.
- Produces constants `developerTokenLifetime = 24*time.Hour` and `developerTokenRefreshBefore = 5*time.Minute`.

- [ ] **Step 1: Write failing signer tests**

Generate an ephemeral P-256 key, marshal it as PKCS#8 PEM into `t.TempDir`, call `signDeveloperToken`, decode all JWT segments, assert `alg=ES256`, configured `kid`/`iss`, exact `iat`, and `exp-iat=86400`, then verify the raw 64-byte ECDSA signature. Add error cases for unreadable path, invalid PEM, invalid PKCS#8, non-ECDSA key, and non-P-256 key.

- [ ] **Step 2: Verify RED**

Run `go test ./internal/applemusic -run DeveloperToken -count=1`. Expect compilation failure because the signer does not exist.

- [ ] **Step 3: Implement minimal signer**

Read and PEM-decode the key, parse PKCS#8, require a P-256 `*ecdsa.PrivateKey`, JSON-marshal JWT header/claims, encode unpadded base64url segments, hash `header.payload` with SHA-256, call `ecdsa.Sign`, left-pad `r` and `s` to 32 bytes, and return the compact JWT plus `issuedAt.Add(24*time.Hour)`. Wrap errors with operation context and never include key contents.

- [ ] **Step 4: Verify GREEN and commit**

Run `gofmt -w internal/applemusic/developer_token*.go && go test ./internal/applemusic -run DeveloperToken -count=1`. Expect PASS. Commit as `feat: sign Apple Music developer tokens` with the required footer.

---

### Task 3: Token source precedence and caching

**Files:**
- Modify: `internal/applemusic/catalog.go`
- Modify: `internal/applemusic/catalog_test.go`

**Interfaces:**
- Consumes `DeveloperTokenEnabled`, `signDeveloperToken`, and signer constants.
- Produces `Token` precedence: signed developer token, explicit token, then website discovery.

- [ ] **Step 1: Write failing selection tests**

Add tests using `roundTripFunc` to prove: complete signing makes zero HTTP discovery calls; a bad configured key returns an error with zero HTTP calls even if `authorization_token` is present; explicit `authorization_token` returns directly and strips optional `Bearer `; two signed-token calls reuse the cache; an expired `tokenUntil` renews it; no configured source still performs mocked HTML and JS discovery.

- [ ] **Step 2: Verify RED**

Run `go test ./internal/applemusic -run 'Token(Signing|Invalid|Explicit|Cache|Discovery)' -count=1`. Expect failures because current code discovers from the website first.

- [ ] **Step 3: Implement strict precedence**

After the cache check, branch on `DeveloperTokenEnabled`. Sign locally and cache until `expiresAt.Add(-5*time.Minute)`. Otherwise, if the trimmed explicit token is nonempty, strip `Bearer ` and cache using `TokenTTL`. Only otherwise call `fetchToken`. Return signing errors directly and store successful results under the existing mutex.

- [ ] **Step 4: Verify GREEN and commit**

Run `gofmt -w internal/applemusic/catalog.go internal/applemusic/catalog_test.go && go test ./internal/applemusic -count=1`. Expect PASS. Commit as `feat: prefer signed Apple Music tokens` with the required footer.

---

### Task 4: Full and live verification

**Files:**
- Modify only if verification reveals a defect in planned behavior.

**Interfaces:**
- Consumes all prior tasks and produces fresh completion evidence.

- [ ] **Step 1: Static verification**

Run `gofmt` on all touched Go files, `git diff --check`, and `go vet ./...`. Expect exit code zero from each.

- [ ] **Step 2: Full tests**

Run `go test ./... -count=1`. Expect all packages PASS with zero failures.

- [ ] **Step 3: Live configured-path smoke test**

Load `configs/config.yaml`, construct the real `CatalogClient`, and request a known Apple Music catalog song through a temporary non-committed harness or focused test. Print only status and metadata, never the JWT or key. Expect returned song metadata using the configured signing path.

- [ ] **Step 4: Final repository inspection**

Run `git diff HEAD~3 --stat` and `git status --short`. Confirm only intended code, tests, and config changed and that `.p8` remains ignored.

