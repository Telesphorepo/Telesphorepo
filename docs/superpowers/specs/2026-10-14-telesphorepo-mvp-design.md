# Telesphorepo MVP — Design

Date: 2026-10-14 · Status: awaiting review

A lightweight, self-hosted artifact repository for **hosted** Maven and Debian repos, replacing
Sonatype Nexus OSS. Not a Nexus drop-in: no Nexus API/URL compatibility, no proxy/cache repos.

Status legend: **[M1]** = Milestone 1 (built first), **[M2]**…**[M4]** = later milestones, **[Future]** = designed but unscheduled.

## 0. Intent, scope and key decisions

**Outcome:** a single small container that hosts private Maven (incl. Gradle plugin) and Debian
repos for CI and developer machines, with per-repo permissions, and that stays fast as repos grow.

**Success criteria for M1:** real `mvn deploy`/resolve, Gradle plugin publish/apply, and
`apt-get install` from a signed repo all pass in e2e tests against the image built from the
Dockerfile, on both FS and S3 storage, with the auth matrix enforced.

**Non-goals:** proxy/cache repos, Nexus compatibility, HA/multi-instance, unit tests, UI tests.

**Chosen architecture (approach A):** blobs are addressed by repo path (`repos/{repo}/{path}`),
and one `assets` table records every stored file, including generated index files. Every download
is one indexed point lookup followed by a stream or a presigned redirect. Rejected alternatives:
content-addressed blobs (requires GC from day one because `maven-metadata.xml` is rewritten
constantly) and serving straight from storage without DB rows (no fast 404, no search/browse).

**Deviations from / clarifications of the original brief:**
1. Basic-auth password checks are cached in memory (argon2id per request would be prohibitive);
   API tokens use SHA-256, not argon2 (256-bit random secrets).
2. `maven-metadata.xml*` stays mutable at every level; re-uploading identical bytes to an immutable
   path is an idempotent `200`.
3. Debian indexes use `Acquire-By-Hash` and a strict write order so clients never see a torn
   snapshot; pending rebuilds are durable in SQLite; `Release` is rebuilt from stored index-file
   hashes, not from packages.
4. CSRF protection uses stdlib `http.CrossOriginProtection` (Go 1.25+) plus `SameSite=Strict`.
5. Storage backend is named `s3` (R2 is one S3 endpoint; `r2` accepted as alias). The R2 302
   redirect is the default but can be disabled (`TR_S3_REDIRECT=false`); `HEAD` is never
   redirected.
6. The `Secure` cookie flag is configurable so e2e tests can run over plain HTTP.
7. M1 includes Debian (the brief's Phase 2), because Maven + Debian + auth is the usable core.
8. Two kinds of people use the system (§7.5): **administrators** sign in to the UI through a
   generic OIDC IdP (Keycloak or any other); **publishers** (CI service accounts, developers) are
   local accounts with password/API tokens and never use SSO.

---

## 1. Principles (non-negotiable)

1. **Request-path cost does not grow with repo size.** Every request-path SQL query is served by an
   index (point lookup or bounded range scan). No `OFFSET`, no `LIKE '%x%'`, no full scans, no
   listing of storage in handlers. Work proportional to repo contents (Debian index generation) runs
   only in background workers, scoped to the smallest affected unit.
2. **Use mature libraries for parsers, crypto and auth primitives.** Keep the dependency tree small.
3. **Light.** One static binary (`CGO_ENABLED=0`), distroless image, one SQLite file, one blob store.
4. **E2E tests only.** No unit tests, no mocks. Every test runs the image built from the real
   Dockerfile, driven by real clients (Maven, Gradle, apt, curl/HTTP).
5. **Single process.** SQLite + in-process locks/debouncers mean exactly one server instance per
   data volume. Horizontal scaling is a non-goal.
6. Repo paths and storage keys always use package `path` (forward slashes), never `path/filepath`.

## 2. Stack & dependencies

| Concern | Choice |
|---|---|
| Language | Go (toolchain 1.27, `go` directive ≥ 1.25), `CGO_ENABLED=0` |
| HTTP | `net/http` ServeMux (method + wildcard patterns), `http.CrossOriginProtection` (CSRF) |
| DB | `modernc.org/sqlite` (WAL, FTS5 trigram), migrations embedded via `go:embed` |
| Blob storage | local FS, or any S3-compatible store (Cloudflare R2, SeaweedFS, …) via `aws-sdk-go-v2` |
| OpenPGP | `github.com/ProtonMail/go-crypto` (clearsign + detached armored signatures) |
| Debian | `pault.ag/go/debian` (`deb.LoadAr`, `deb.DecompressorFor`, `control`, `version`) |
| Passwords | argon2id, `golang.org/x/crypto/argon2` |
| Rate limiting | `golang.org/x/time/rate` |
| OIDC **[M2]** | `golang.org/x/oauth2` + `github.com/coreos/go-oidc/v3` |
| Logging | `log/slog` (JSON) |
| Frontend | React 19 + TypeScript + Vite, embedded with `go:embed`, no UI framework |
| Tests | `go test` + `testcontainers-go` (behind `//go:build e2e`) |
| Image | `node` → `golang` → `gcr.io/distroless/static-debian12:nonroot` |

No other runtime dependencies without a written justification in this file.

## 3. Configuration (environment variables)

| Var | Default | Meaning |
|---|---|---|
| `TR_LISTEN` | `:8080` | listen address |
| `TR_DATA_DIR` | `./data` (image: `/data`) | holds `telesphorepo.db`, `tmp/` (upload spool), and `blobs/` for FS storage |
| `TR_BASE_URL` | — | public base URL (used in UI hints only) |
| `TR_STORAGE` | `fs` | `fs` or `s3` (`r2` accepted as alias of `s3`) |
| `TR_FS_ROOT` | `$TR_DATA_DIR/blobs` | FS blob root |
| `TR_S3_ENDPOINT` | — | e.g. `https://<acct>.r2.cloudflarestorage.com`, `http://seaweed:8333` |
| `TR_S3_REGION` | `auto` | |
| `TR_S3_BUCKET` | — | required for s3 |
| `TR_S3_ACCESS_KEY_ID` / `TR_S3_SECRET_ACCESS_KEY` | — | required for s3 |
| `TR_S3_PREFIX` | `""` | optional key prefix (tests use a unique prefix per run) |
| `TR_S3_PATH_STYLE` | `false` | path-style addressing (needed by SeaweedFS) |
| `TR_S3_REDIRECT` | `true` | GET downloads 302 to a presigned URL; `false` = stream through server |
| `TR_PRESIGN_TTL` | `5m` | presigned URL lifetime |
| `TR_MAX_UPLOAD_BYTES` | `2147483648` | max artifact upload body |
| `TR_MAX_JSON_BYTES` | `1048576` | max JSON API body |
| `TR_COOKIE_SECURE` | `true` | `Secure` flag on session cookie (tests set `false` because they use plain HTTP) |
| `TR_SESSION_TTL` | `168h` | absolute session lifetime |
| `TR_TRUSTED_PROXIES` | `""` | comma-separated CIDRs whose `X-Forwarded-For` is trusted for client IP |
| `TR_GPG_KEY_FILE` | `$TR_DATA_DIR/signing.asc` (auto-generated if absent) | armored OpenPGP **private** key used to sign Debian indexes |
| `TR_GPG_PASSPHRASE_FILE` | — | optional file containing the key passphrase |
| `TR_DEB_DEBOUNCE` | `2s` | quiet period before a Debian index rebuild |
| `TR_DEB_MAX_DELAY` | `30s` | max delay of a rebuild under continuous uploads |
| `TR_BOOTSTRAP_ADMIN_USER` / `TR_BOOTSTRAP_ADMIN_PASSWORD` | — | if set and the users table is empty at startup, create this admin |
| `TR_LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |

Secrets are never logged and never stored in the DB.

## 4. CLI (one binary: `telesphorepo`)

**DX rule (applies to every command, now and later): no flag slop.** Write the human invocation
first. Positional arguments for the obvious thing, interactive prompts on a TTY (passwords without
echo), defaults derived from env/data dir, and flags only for genuinely optional knobs. Complex
operations get a simple wrapper. `--help` on every command prints a short, example-led usage.

| Command | Behaviour |
|---|---|
| `telesphorepo` / `telesphorepo serve` | run the server from `TR_*` env (§3) |
| `telesphorepo dev` | zero-config local server: data in `./.telesphorepo-dev/`, `http://localhost:8080`, insecure cookies allowed, short Debian debounce, signing key auto-generated, an `admin` user plus demo repos `maven` and `debian` created on first run; prints URL, username and password. `TR_*` env still overrides. |
| `telesphorepo user add NAME [--admin]` | prompts for the password twice on a TTY; reads one line from stdin when piped |
| `telesphorepo user passwd NAME` | reset a password (same input rules) |
| `telesphorepo keygen [FILE]` | write a new Ed25519 signing key (default `$TR_DATA_DIR/signing.asc`, refuses to overwrite) and print its fingerprint |
| `telesphorepo version` | print version |
| `move` / `import nexus` | **[M3]**, designed to the same rule (e.g. `telesphorepo move s3` copies from the current backend to the named one using env config) |

**Startup defaults that remove setup steps:**
- `TR_DATA_DIR` defaults to `./data` for the binary; the Docker image sets `/data`.
- First start with an empty users table and no `TR_BOOTSTRAP_ADMIN_*`: create `admin` with a random
  password and print it once to stderr in a clearly marked block (never to the JSON log stream).
- No `TR_GPG_KEY_FILE`: use `$TR_DATA_DIR/signing.asc`, generating it on first start (logs the
  fingerprint). The key is on the data volume, never in the DB. An explicit `TR_GPG_KEY_FILE` that
  does not exist is a startup error (no silent generation over a misconfigured mount).
- If the embedded UI was not built (only `dist/.keep`), `/` serves a small page explaining how to
  build it (`npm --prefix web run build`) or run the Vite dev server.

## 5. Data model

### 5.1 Storage keys (identical on every backend)

```
repos/{repo}/{path}                      # every stored file of every repo
```
- Maven: `{path}` is the Maven layout path, e.g. `com/acme/lib/1.0/lib-1.0.jar`.
- Debian: `pool/{component}/{prefix}/{source}/{package}_{version-without-epoch}_{arch}.deb`
  (`prefix` = first letter of source, or `lib` + 4th letter for `lib*` sources) and generated
  `dists/{dist}/...` files.

FS backend writes to a temp file in the same directory tree then `rename`s (atomic replace).

### 5.2 SQLite schema (becomes `internal/db/migrations/0001_init.sql`)

Times are unix seconds (`INTEGER`). Booleans are `INTEGER` 0/1.

```sql
CREATE TABLE users (
  id                  INTEGER PRIMARY KEY,
  username            TEXT NOT NULL UNIQUE,          -- ^[a-z0-9][a-z0-9._-]{0,63}$
  password_hash       TEXT NOT NULL,                 -- argon2id PHC string
  is_admin            INTEGER NOT NULL DEFAULT 0,
  disabled            INTEGER NOT NULL DEFAULT 0,
  password_changed_at INTEGER NOT NULL,
  created_at          INTEGER NOT NULL
);

CREATE TABLE api_tokens (
  id           INTEGER PRIMARY KEY,
  user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  public_id    TEXT NOT NULL UNIQUE,                 -- the 12-hex id embedded in the token
  secret_hash  BLOB NOT NULL,                        -- SHA-256(secret)
  name         TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER,                              -- NULL = never
  last_used_at INTEGER,                              -- updated at most once per minute
  UNIQUE (user_id, name)
);

CREATE TABLE sessions (
  id_hash    BLOB PRIMARY KEY,                       -- SHA-256(cookie value)
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX sessions_expires ON sessions(expires_at);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE repos (
  id             INTEGER PRIMARY KEY,
  name           TEXT NOT NULL UNIQUE,
  format         TEXT NOT NULL CHECK (format IN ('maven','debian')),
  anonymous_read INTEGER NOT NULL DEFAULT 0,
  config         TEXT NOT NULL DEFAULT '{}',         -- JSON, format-specific
  created_at     INTEGER NOT NULL
);

CREATE TABLE repo_permissions (
  repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  level   TEXT NOT NULL CHECK (level IN ('read','write')),
  PRIMARY KEY (repo_id, user_id)
) WITHOUT ROWID;
CREATE INDEX repo_permissions_user ON repo_permissions(user_id);

CREATE TABLE assets (
  id           INTEGER PRIMARY KEY,
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  path         TEXT NOT NULL,                        -- repo-relative, forward slashes
  size         INTEGER NOT NULL,
  sha256       TEXT NOT NULL,                        -- lowercase hex
  content_type TEXT NOT NULL,
  kind         TEXT NOT NULL CHECK (kind IN ('artifact','metadata','index')),
  uploaded_by  INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  UNIQUE (repo_id, path)
);

-- Substring search over paths; generated index files are not indexed.
CREATE VIRTUAL TABLE assets_fts USING fts5(path, content='assets', content_rowid='id', tokenize='trigram');
CREATE TRIGGER assets_ai AFTER INSERT ON assets WHEN new.kind <> 'index' BEGIN
  INSERT INTO assets_fts(rowid, path) VALUES (new.id, new.path);
END;
CREATE TRIGGER assets_ad AFTER DELETE ON assets WHEN old.kind <> 'index' BEGIN
  INSERT INTO assets_fts(assets_fts, rowid, path) VALUES ('delete', old.id, old.path);
END;
-- path and kind never change on UPDATE (upserts only touch size/sha256/content_type/updated_at),
-- so no update trigger is needed.

CREATE TABLE deb_packages (
  id           INTEGER PRIMARY KEY,
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  distribution TEXT NOT NULL,
  component    TEXT NOT NULL,
  architecture TEXT NOT NULL,
  package      TEXT NOT NULL,
  version      TEXT NOT NULL,
  asset_id     INTEGER NOT NULL REFERENCES assets(id),
  filename     TEXT NOT NULL,                        -- pool path, e.g. pool/main/h/hello/hello_1.0_amd64.deb
  size         INTEGER NOT NULL,
  md5          TEXT NOT NULL,
  sha1         TEXT NOT NULL,
  sha256       TEXT NOT NULL,
  control      TEXT NOT NULL,                        -- control stanza without hash/Filename/Size fields
  created_at   INTEGER NOT NULL,
  UNIQUE (repo_id, distribution, component, architecture, package, version)
);
-- The UNIQUE index above doubles as the streaming index for Packages generation.

CREATE TABLE deb_dirty (
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  distribution TEXT NOT NULL,
  component    TEXT NOT NULL,
  architecture TEXT NOT NULL,
  marked_at    INTEGER NOT NULL,                     -- unix nanoseconds, for the "re-dirtied during rebuild" check
  PRIMARY KEY (repo_id, distribution, component, architecture)
) WITHOUT ROWID;

CREATE TABLE deb_index_files (
  repo_id      INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  distribution TEXT NOT NULL,
  path         TEXT NOT NULL,                        -- relative to dists/{dist}/, e.g. main/binary-amd64/Packages.gz
  size         INTEGER NOT NULL,
  md5          TEXT NOT NULL,
  sha256       TEXT NOT NULL,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY (repo_id, distribution, path)
) WITHOUT ROWID;
```

`assets` is the single source of truth for "what files exist in a repo" — for every format, and
for generated index files too (`kind = 'index'`, excluded from search). All downloads go through
`assets` (indexed point lookup on `(repo_id, path)`), so the request path never touches storage
listings and a missing file is a fast 404 without a storage round-trip.

Connection setup: WAL, `synchronous=NORMAL`, `foreign_keys=ON`, `busy_timeout=5000`. One
writer pool (`MaxOpenConns=1`, `_txlock=immediate`) and one reader pool. Migrations: ordered
`NNNN_name.sql` files applied in a transaction each, tracked by `PRAGMA user_version`.

### 5.3 Repo types

`repos.format` ∈ {`maven`, `debian`} (later: `rpm`). Each format is a Go package implementing:

```go
type Format interface {
    Name() string                     // "maven", "debian"
    Register(mux *http.ServeMux)      // mounts /{format}/{repo}/...
}
```
Shared machinery (path validation, asset write/serve, auth, background "dirty index" workers) is
format-agnostic so Yum/RPM is a new package + a migration, not a restructuring.
Format-specific per-repo settings live in `repos.config` (JSON object).

Repo name: `^[a-z0-9][a-z0-9._-]{0,63}$`, immutable after creation.

## 6. Path validation (all formats)

Applied to the raw request before any storage key is built:
- Reject if `r.URL.RawPath`/`EscapedPath()` contains `%2f`, `%2F`, `%5c`, `%5C`, `%00` (encoded
  separators/NUL).
- Split the decoded path on `/`; every segment must be non-empty, not `.` or `..`, ≤ 255 bytes,
  valid UTF-8, and contain no control characters (0x00–0x1F, 0x7F) or `\`.
- Whole path ≤ 1024 bytes.
- Invalid → `400`. Validation lives in one function (`internal/httpx.ValidRepoPath`).

## 7. Authentication & authorization

### 7.1 Credentials
- **Passwords**: argon2id (`m=64MiB, t=3, p=2`, 16-byte salt, 32-byte key), PHC string format.
  Hashing concurrency is bounded by a semaphore (4) to cap memory.
- **Basic-auth verification cache**: a successful password check is cached in memory for 10 min,
  keyed by `HMAC-SHA256(processSecret, username \x00 password)` → user id. (Without this, Maven
  fetching 200 files with a password would run argon2 200 times.) Cache is dropped for a user when
  their password changes or they are disabled. `processSecret` is random per process, never stored.
- **API tokens**: format `trp_<12 hex id>_<43 char base64url secret>` (32 random bytes). DB stores the
  id (unique, indexed) and `SHA-256(secret)`; lookup by id, compare hash with
  `subtle.ConstantTimeCompare`. Shown once at creation. Optional expiry. Tokens are high-entropy
  so a fast hash is correct (argon2 would be pointless cost).
- **Sessions** (UI): 32 random bytes in cookie `tr_session` (`HttpOnly; SameSite=Strict; Path=/`;
  `Secure` unless `TR_COOKIE_SECURE=false`). DB stores `SHA-256(id)`. Absolute TTL.

### 7.2 Where each credential is accepted
- Package endpoints (`/maven/…`, `/debian/…`): HTTP Basic only. Username + password, or any
  username + token as password (token recognized by the `trp_` prefix; the token's owner is the
  principal regardless of the username sent).
- JSON API (`/api/…`): session cookie **or** HTTP Basic (so CI/tests can script it).
- CSRF: all `/api/` routes are wrapped in `http.CrossOriginProtection` (Fetch-metadata / Origin
  based; non-browser clients without those headers pass) in addition to `SameSite=Strict`.

### 7.3 Authorization
- `users.is_admin` → everything.
- `repo_permissions(level)`: `read` ⊂ `write`. Write implies read.
- `repos.anonymous_read` → unauthenticated (and authenticated) read.
- Disabled users authenticate as failures.
- Decision table for a repo request:
  - no credentials + anonymous read + read op → allow
  - no credentials otherwise → `401` + `WWW-Authenticate: Basic realm="telesphorepo"`
  - bad credentials → `401` (+ challenge), counts toward rate limit
  - valid credentials, insufficient permission → `403`
  - repo does not exist → `404` (after auth; an anonymous caller gets `401` for a nonexistent repo
    so names don't leak).

### 7.4 Rate limiting
In-memory `rate.Limiter` per client IP and per username for **failed** attempts (burst 10, refill
1 per 6s). When exhausted, further auth attempts from that IP/username get `429` with
`Retry-After` before any password verification. Map entries expire after 15 min idle (bounded
sweep). Client IP = `RemoteAddr`, or the right-most untrusted `X-Forwarded-For` hop when
`RemoteAddr` is in `TR_TRUSTED_PROXIES`.

### 7.5 Account kinds and OIDC admin sign-on **[M2]**

**Account kinds** (`users.kind`):
- `local` — username + password (argon2id) + API tokens. Publishers (Jenkins, developers) and the
  bootstrap break-glass admin. Basic auth works on package endpoints and the API.
- `oidc` — created on first SSO login, identified by `(oidc_issuer, oidc_subject)`. No password
  (`password_hash` is the unusable sentinel `"!"`), cannot create API tokens, cannot authenticate
  with Basic anywhere. UI session only. SSO is for administrators only: an OIDC login that does not
  qualify as admin is refused (`403` page), and no user row is created.

**Protocol:** OpenID Connect authorization-code flow with PKCE (S256), via
`golang.org/x/oauth2` + `github.com/coreos/go-oidc/v3` (discovery from `TR_OIDC_ISSUER`, ID-token
verification of signature, issuer, audience, expiry and nonce).

**Config** (SSO enabled iff `TR_OIDC_ISSUER` is set):

| Var | Default | Meaning |
|---|---|---|
| `TR_OIDC_ISSUER` | — | issuer URL, e.g. `https://kc.example.com/realms/ops` |
| `TR_OIDC_CLIENT_ID` | — | required |
| `TR_OIDC_CLIENT_SECRET_FILE` | — | file with the client secret (confidential client; never an env value) |
| `TR_OIDC_REDIRECT_URL` | `$TR_BASE_URL/api/auth/oidc/callback` | must be registered at the IdP |
| `TR_OIDC_SCOPES` | `openid profile email` | space-separated |
| `TR_OIDC_ADMIN_CLAIM` | `groups` | claim checked for admin rights; dotted paths allowed (`realm_access.roles` for Keycloak realm roles) |
| `TR_OIDC_ADMIN_VALUES` | — | comma-separated values; the claim (string or array) must contain one |
| `TR_OIDC_ADMIN_EMAILS` | — | alternative/additional allowlist; matched only when `email_verified` is true |
| `TR_OIDC_SESSION_TTL` | `12h` | session lifetime for OIDC admins (shorter than local sessions) |
| `TR_ADMIN_PASSWORD_LOGIN` | `true` | `false` disables UI password login for local admins once SSO works (break-glass: flip back via env + restart) |

At least one of `TR_OIDC_ADMIN_VALUES` / `TR_OIDC_ADMIN_EMAILS` is required when SSO is enabled.

**Flow:**
1. `GET /api/auth/oidc/login` → generate `state`, `nonce`, PKCE verifier; keep them in an in-memory
   map (TTL 10 min, bounded) keyed by `SHA-256(state)`; set cookie `tr_oidc` = state
   (`HttpOnly; SameSite=Lax; Path=/api/auth/oidc; Max-Age=600` — **Lax**, because the callback is a
   cross-site top-level navigation from the IdP); `302` to the IdP.
2. `GET /api/auth/oidc/callback?code&state` → state must equal the `tr_oidc` cookie and be in the
   map (single use); exchange the code with the verifier; verify the ID token and nonce; evaluate
   the admin rule against ID-token claims (falling back to the UserInfo endpoint for missing claims).
3. Not admin → clear state, `302 /#/login?error=not_authorized`. Admin → upsert the user by
   `(issuer, subject)`: username = normalized `preferred_username` (lowercased; chars outside
   `[a-z0-9._-]` → `-`); if that username is taken by a different account the login fails with
   `error=username_conflict` — accounts are never merged by name. Set `is_admin = 1`, update email,
   create a session (`TR_OIDC_SESSION_TTL`), `302 /`.
4. Admin rights are re-evaluated at every SSO login; an OIDC user whose claim no longer qualifies is
   refused and their sessions deleted. Disabling the user in the UI also works immediately.
5. `POST /api/auth/logout` ends the local session only (no RP-initiated IdP logout in M2).

The SPA's login page shows "Sign in with SSO" when `GET /api/auth/config` returns
`{"oidc": true}`, plus the password form for publishers (and local admins unless disabled).
The `302 /` after the callback is part of a cross-site navigation chain, so the `SameSite=Strict`
session cookie is not sent on that document load — that is fine because the SPA authenticates via
same-origin `fetch` calls, which do send it.

**Schema (migration `0002_oidc.sql`):**
```sql
ALTER TABLE users ADD COLUMN kind TEXT NOT NULL DEFAULT 'local' CHECK (kind IN ('local','oidc'));
ALTER TABLE users ADD COLUMN oidc_issuer TEXT;
ALTER TABLE users ADD COLUMN oidc_subject TEXT;
ALTER TABLE users ADD COLUMN email TEXT;
CREATE UNIQUE INDEX users_oidc ON users(oidc_issuer, oidc_subject) WHERE oidc_subject IS NOT NULL;
ALTER TABLE api_tokens ADD COLUMN kind TEXT NOT NULL DEFAULT 'personal' CHECK (kind IN ('personal','minted'));
```

**E2E:** a Keycloak container (`quay.io/keycloak/keycloak`, `start-dev`, realm imported from
JSON with a confidential client, a `repo-admins` group, a groups mapper, one admin and one
non-admin user). The test drives the browser flow with a Go HTTP client + cookie jar (follow
redirects, submit Keycloak's login form), then asserts: admin gets a session and can create a
repo; non-admin is refused and no user row exists; OIDC admin cannot use Basic or create tokens;
tampered `state` → 400; reused `state` → 400.

### 7.6 Publisher device keys, 2FA and short-lived tokens **[Future]**

Goal: developers publish from their machines (Gradle plugin, later a CLI) with short-lived tokens
only, so a leaked password — or a leaked token — is useless long term. CI service accounts keep
long-lived tokens issued by an admin.

Principle: **a password alone can never produce a token or publish.** Every token is minted with
proof of a key that lives on an enrolled machine, and enrolling a machine requires a second factor.

- **Second factor per publisher:** passkeys (WebAuthn, via `github.com/go-webauthn/webauthn`) and/or
  TOTP (via `github.com/pquerna/otp`). Required for any publisher with `password_basic = false`.
- **Per-user flag `password_basic`** (default `true` for backward compatibility / CI accounts): when
  `false`, the password works only for UI login (which then also demands the second factor), never
  for Basic on `/maven` or `/debian`.
- **Device enrollment:** on first use the plugin generates an Ed25519 key pair on the machine
  (private key stays local, file mode 0600 or the OS keychain) and starts an enrollment: it gets a
  short code and a URL; the user opens it, signs in with password + passkey/TOTP, and approves the
  device by name. The server stores the device's public key (`devices` table: user, name,
  public key, created_at, last_used_at, revoked_at).
- **Minting:** `POST /api/tokens/mint` → server returns a nonce; the plugin signs
  `nonce || device_id || timestamp` with the device key; the server verifies and returns a `minted`
  token (`api_tokens.kind = 'minted'`, TTL ~12h, bound to the device). The plugin caches it and
  re-mints when expired. No password or second factor is needed per mint — the device key is the
  possession factor and enrollment already required 2FA.
- **Mint at publish time (Databricks-CLI model):** like `databricks auth login` followed by
  `generate-database-credential` for Lakebase, the one-time enrollment leaves a long-lived
  credential on the machine (the device key), and a short-lived credential is minted on demand
  immediately before use. Minted tokens default to a 1h TTL (configurable, max 12h) and are never
  written to `settings.xml`/`gradle.properties`.
- **Client integration:**
  - **Gradle plugin** (`telesphorepo` settings/project plugin): `telesphorepo { url = "…" }`;
    first run (or `./gradlew telesphorepoLogin`) performs enrollment; the plugin then configures
    every Maven repository pointing at the server with a lazy credentials provider that mints a
    token when a publish/resolve task actually executes, caching it in memory (or a 0600 file
    under `~/.telesphorepo/`) until shortly before expiry.
  - **CLI** (`telesphorepo login --url …`, `telesphorepo token`): same enrollment; `token` prints a
    fresh minted token so Maven, apt (`auth.conf.d` via a hook) or scripts can use it.
  - Both share the on-disk layout `~/.telesphorepo/<host>/{device.key,device.json}`.
- **Revocation:** revoking a device in the UI invalidates its unexpired minted tokens and blocks
  future mints. Users and admins can list devices and their last use.
- The M1/M2 token model already carries `expires_at` and `kind`; this adds `devices`,
  `user_2fa` tables and the `password_basic` flag, with no restructuring.

## 8. Maven (also Gradle plugins) **[M1]**

Routes: `GET`, `HEAD`, `PUT` on `/maven/{repo}/{path...}`.

- **PUT**: requires write. Body capped at `TR_MAX_UPLOAD_BYTES` (`413` beyond). Body is spooled to
  `TR_DATA_DIR/tmp` while computing SHA-256 + size, then stored. Responses:
  - new path → `201`
  - overwrite allowed → `200`
  - release overwrite with **identical** SHA-256 → `200` (idempotent retry, nothing rewritten)
  - release overwrite with different content → `409`
- **Overwrite rule**: a path is mutable iff its file name starts with `maven-metadata.xml`
  (metadata + its checksum files, at any level), **or** its parent directory (version dir) ends
  in `-SNAPSHOT`. Everything else is an immutable release file.
- Concurrency: a per-`(repo,path)` in-process lock is held across check → store → DB upsert.
- **GET**: requires read. Point lookup in `assets`; missing → `404`. FS: stream with
  `Content-Length`, `Content-Type`, `ETag` (`"sha256"`), `Last-Modified`, range support.
  S3 with `TR_S3_REDIRECT=true`: `302` to a presigned GET URL; otherwise stream.
- **HEAD**: requires read. Answered from the `assets` row (never redirected — presigned URLs are
  method-specific).
- Content types: by extension (`.pom`/`.xml` → `application/xml`, `.jar` → `application/java-archive`,
  `.sha1/.sha256/.sha512/.md5` → `text/plain`, else `application/octet-stream`).
- Gradle plugin marker artifacts (`{id}/{id}.gradle.plugin/...`) are ordinary paths; no special code.
- No directory listing over the Maven URL space (browse via the JSON API).

## 9. Debian (hosted) **[M1]**

### 9.1 URLs
- `GET/HEAD /debian/{repo}/dists/...` and `/debian/{repo}/pool/...` — served from `assets`, exactly
  like Maven downloads (read permission).
- `PUT /debian/{repo}/upload/{dist}/{component}` — raw `.deb` body (`curl -T x.deb`). Requires write.
  Also accepts `POST` with the same body.
- `GET /keys/debian.asc` — public signing key, always anonymous.
- `dist`, `component`: `^[a-z0-9][a-z0-9._-]{0,63}$`.
- apt source: `deb [signed-by=/etc/apt/keyrings/tr.asc] https://host/debian/{repo} {dist} {component}`.

### 9.2 Upload pipeline
1. Spool body to temp file (size cap), computing MD5/SHA1/SHA256 in one pass.
2. Parse with `deb.LoadAr` over the temp file; find `control.tar{,.gz,.xz,.zst}`; decompress via
   `deb.DecompressorFor` wrapped in `io.LimitReader` (**cap: 16 MiB decompressed control.tar**,
   `./control` member ≤ 1 MiB; exceeding → `400`). `data.tar.*` is never decompressed.
   XZ dictionary capped via `deb.SetXZMaxDict(64 MiB)`.
3. Parse the stanza with `pault.ag/go/debian/control` (`ParagraphReader`), validate:
   `Package` (`^[a-z0-9][a-z0-9+.-]+$`), `Version` (parse with `pault.ag/go/debian/version`),
   `Architecture` (`^[a-z0-9-]+$`; must be `all` or in the repo's architecture list).
   Fields `Filename`, `Size`, `MD5sum`, `SHA1`, `SHA256`, `SHA512` in the control file are dropped.
4. Per-`(repo,dist,component,package,version,arch)` lock. If that row exists: identical SHA-256 →
   `200` no-op; else `409`.
5. Store blob at the pool key (if the pool key already exists with a different hash → `409`; same
   hash → reuse, so one `.deb` can be in multiple dists).
6. In one transaction: upsert `assets` row, insert `deb_packages` row, upsert `deb_dirty` rows for
   `(dist, component, arch)` — for `arch = all`, one row per configured architecture.
7. Notify the indexer; respond `201` with JSON `{package, version, architecture, filename, sha256}`.

### 9.3 Index generation (background, incremental, debounced)
- `deb_dirty` is durable: a crash before rebuild is recovered at startup (all dirty rows enqueued).
- The indexer debounces per `(repo, dist)`: rebuild after `TR_DEB_DEBOUNCE` of quiet, or at most
  `TR_DEB_MAX_DELAY` after the first mark. Bursts of uploads → one rebuild.
- For each dirty `(component, arch)`: stream rows (`architecture IN (arch,'all')`, ordered by
  package, version) from SQLite via the covering index, writing `Packages` (stanza + `Filename`,
  `Size`, `MD5sum`, `SHA1`, `SHA256`) and `Packages.gz` simultaneously through a temp file with
  hashing writers. Upload, in this order:
  1. `by-hash/SHA256/<hex>` copies of `Packages` and `Packages.gz` (immutable)
  2. `Packages`, `Packages.gz`
  3. record sizes/hashes in `deb_index_files`
- Then regenerate the dist's `Release` from `deb_index_files` only (O(components × arches)):
  `Origin`, `Label` (= repo name), `Suite`/`Codename` (= dist), `Date` (RFC 1123 UTC),
  `Architectures` (configured list), `Components` (those present), `Acquire-By-Hash: yes`,
  `MD5Sum:` and `SHA256:` lists. Write `Release.gpg` (detached, armored) and `InRelease`
  (clearsigned) **last**. Clients therefore always see a consistent snapshot (by-hash).
- Dirty rows are deleted only if `marked_at` ≤ the rebuild start (uploads during a rebuild re-dirty).
- Without a signing key: `Release` only, a warning at startup; clients must use `[trusted=yes]`.
- Repo config: `{"architectures": ["amd64","arm64"]}` (default). Empty dists still return 404.
- `GET /api/repos/{name}/debian/status` → `{"pending": <count of deb_dirty rows>}` so tests and
  CI can wait for indexes.
- Old by-hash files are not pruned in M1 **[M4]**.

## 10. JSON API **[M1]**

All under `/api/`, JSON in/out, errors as `{"error": "message"}` with a proper status code.
Request bodies capped at `TR_MAX_JSON_BYTES`. Times are RFC 3339 strings. List endpoints use keyset
pagination: `?limit=` (default 50, max 200) and `?after=<cursor>`; responses include
`"next": "<cursor>"` or `null`.

| Method & path | Auth | Description |
|---|---|---|
| `GET /healthz` | none | `200 ok` (outside /api) |
| `POST /api/auth/login` | none | `{username,password}` → sets cookie, returns `User` |
| `POST /api/auth/logout` | session | deletes session, clears cookie, `204` |
| `GET /api/me` | any | current `User` (401 if anonymous) |
| `GET /api/tokens` | any | caller's tokens `[{id,name,created_at,expires_at,last_used_at}]` |
| `POST /api/tokens` | any | `{name, expires_in_days?}` → `{id,name,token,...}` (token shown once) |
| `DELETE /api/tokens/{id}` | any | revoke own token (admin: any), `204` |
| `GET /api/users` | admin | list users |
| `POST /api/users` | admin | `{username,password,is_admin}` → `User` (`409` duplicate) |
| `PATCH /api/users/{username}` | admin (or self for `password`) | `{password?,is_admin?,disabled?}` |
| `GET /api/repos` | any/anon | repos visible to caller: `[{name,format,anonymous_read,config,permission}]` |
| `POST /api/repos` | admin | `{name,format,anonymous_read?,config?}` (`409` duplicate) |
| `GET /api/repos/{name}` | read | single repo |
| `PATCH /api/repos/{name}` | admin | `{anonymous_read?,config?}` |
| `GET /api/repos/{name}/permissions` | admin | `[{username,level}]` |
| `PUT /api/repos/{name}/permissions/{username}` | admin | `{level:"read"|"write"}` |
| `DELETE /api/repos/{name}/permissions/{username}` | admin | `204` |
| `GET /api/repos/{name}/assets?prefix=&after=&limit=` | read | `{items:[{path,size,sha256,content_type,kind,updated_at}],next}` ordered by path; prefix is a range scan |
| `GET /api/search?q=&repo=&after=&limit=` | any/anon | FTS5 trigram over asset paths (q ≥ 3 chars), only repos the caller can read, ordered by asset id (keyset) |
| `GET /api/repos/{name}/debian/status` | read | `{pending}` |

`User` = `{id, username, is_admin, disabled, created_at}`.

Search permission filtering: when the caller is not admin, the query joins against the set of
readable repo ids (`repo_id IN (...)`, a small set) — the FTS match drives the scan.

## 11. `move` **[M3]**
Copy all blobs between backends; idempotent and resumable (skip keys present with matching size +
SHA-256 — expected SHA-256 comes from `assets`), verify hash after each copy, never delete from
source, print `copied/skipped/mismatched` report. Iterates `assets` by keyset, not storage listings.

## 12. `import nexus` **[M3]**
Page Nexus components REST API (`continuationToken`), download each asset, push it through this
server's HTTP upload endpoints (Maven `PUT`, Debian upload) with a token, so all validation and
indexing runs. Resumable via checksum skip (HEAD + `ETag`). Maven (incl. Gradle plugins) + Debian.

## 13. Web UI **[M1 shell / M4 full]**
Static Vite build embedded in the binary, served at `/` with SPA fallback to `index.html` for
non-`/api`, non-format paths. M1 pages: login, repo list, repo browser (keyset "load more"),
search, my tokens (create/revoke), and simple admin pages (users, create repo, permissions).
The UI is purely a client of §10. No tests for the UI.

## 14. Docker image
Multi-stage: `node:22-alpine` builds `web/` → `golang:1.27-alpine` builds with
`CGO_ENABLED=0 -trimpath -ldflags="-s -w"` → `gcr.io/distroless/static-debian12:nonroot`.
`VOLUME /data`, `EXPOSE 8080`, `ENTRYPOINT ["/telesphorepo"]`, `CMD ["serve"]`. The nonroot user is
uid 65532; the data volume must be writable by it.

## 15. E2E tests **[M1 subset]**
`go test -tags e2e ./e2e/...`. `TestMain` builds the image once from the real Dockerfile. Each test
starts its own server container(s) on a shared Docker network (alias `repo`), with
`TR_COOKIE_SECURE=false` and a bootstrap admin. Clients run in their own containers on that network.

M1 coverage:
- **Maven**: `maven:3.9` container deploys a small jar project (release + snapshot); a second
  container with an empty local repo resolves and runs it. Release re-deploy → fails (409).
- **Gradle plugin**: `gradle` container publishes a plugin (`java-gradle-plugin` + `maven-publish`);
  a separate project applies it via `plugins {}` with this server in `pluginManagement`.
- **Debian**: build a `.deb` with `dpkg-deb` in a `debian` container, upload with curl, wait for
  `/debian/status` pending=0, then `apt-get update && apt-get install` in a clean `debian`
  container trusting `/keys/debian.asc` and using `/etc/apt/auth.conf.d/`. Upload a second version
  → `apt-get install` picks it up (incremental rebuild).
- **Auth/permissions** with real clients: wrong password, valid user without access, read-only user
  attempting deploy, token auth, anonymous read on/off, rate-limit 429.
- **JSON API** over HTTP: login/session/logout, CSRF rejection (cross-site `Sec-Fetch-Site`),
  tokens, users, repos, permissions, assets listing pagination, search.
- **Storage**: the Maven + Debian flows also run against S3 (SeaweedFS container; real R2 when
  `TR_E2E_R2_*` env vars are set, unique prefix per run, cleaned up afterwards).
- **Security**: path traversal attempts (`..`, `%2e%2e`, `%2f`) → 400; oversize upload → 413;
  decompression-bomb `.deb` → 400.

Gotchas for test authors:
- Maven ≥ 3.8.1 blocks `http://` repos on non-localhost hosts via the `maven-default-http-blocker`
  mirror; test `settings.xml` must override it (redeclare a mirror with that id, `mirrorOf` = a
  dummy id).
- Gradle needs `isAllowInsecureProtocol = true` for `http://` repos.
- Use a shared named Docker volume for `~/.m2`/Gradle caches so Central downloads aren't repeated,
  but delete the test's own group dir before resolving (or use a unique groupId per run).

## 16. Developer experience **[M1.1]**

Three ways to run, from lightest to fullest; all work on macOS, Linux and Windows:
1. **Native:** `go run ./cmd/telesphorepo dev` (UI: `npm --prefix web run dev` for hot reload,
   proxied to :8080, or `npm --prefix web run build` once to embed it).
2. **Container stack:** `go run ./tools/stack up` builds the image and starts it with persistent
   data; `go run ./tools/stack up s3` adds a Garage S3 server and points the app at it;
   `down`, `logs`, `reset` (deletes stack data after confirmation). Uses `compose.yaml`, which also
   works directly with `docker compose up` / `podman compose up`.
3. **E2E:** `go test -tags e2e ./e2e/...`.

**Container engine auto-detection** (`internal/containerengine`, used by `tools/stack` and the e2e
harness; not linked into the server binary): honour `TR_ENGINE=docker|podman` and an existing
`DOCKER_HOST`; otherwise use Docker if its daemon responds, else Podman: socket from
`podman info` (Linux rootless/rootful) or `podman machine inspect` (macOS unix socket, Windows named
pipe); then set `DOCKER_HOST` and, for Podman, `TESTCONTAINERS_RYUK_CONTAINER_PRIVILEGED=true` for
testcontainers. Clear error listing what was tried when nothing is found.

**S3 test targets:** SeaweedFS and **Garage** (`dxflrs/garage`, configured via its CLI: layout
assign/apply, key import, bucket create/allow, region `garage`) both run in e2e via
`ForEachStorage`; real R2 runs when `TR_E2E_R2_*` is set. Garage is the closest local stand-in for
R2's strict S3 behaviour (SigV4 region checks, no ACLs).

## 17. Milestones
- **M1 (now)**: everything marked [M1]: config, DB, storage FS+S3, auth, Maven, Debian, JSON API,
  UI shell, CLI `serve`/`user create`/`signing-key generate`, Dockerfile, e2e suite above.
- **M2**: OIDC admin sign-on (§7.5) + Keycloak e2e.
- **M3**: `move`, `import nexus` + e2e.
- **M4**: full admin UI polish, deletes (assets, deb packages) + blob/by-hash GC, scaling test
  behind `//go:build scale`.
