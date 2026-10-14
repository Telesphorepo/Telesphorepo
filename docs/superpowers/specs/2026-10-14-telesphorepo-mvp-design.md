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
| Image | `oven/bun` → `golang` → `gcr.io/distroless/static-debian13:nonroot` |

No other runtime dependencies without a written justification in this file.

## 3. Configuration **[M1.2: in-app settings]**

**Principle:** environment variables are a bare-minimum bootstrap plus an override mechanism for
tests. Everything an operator tunes is configured in the app (admin UI / `/api/settings`) and
stored in SQLite, with secret values encrypted at rest. There is no `.env` file to maintain.

**Boot environment (the only variables a deployment needs, all optional):**

| Var | Default | Meaning |
|---|---|---|
| `TR_DATA_DIR` | `./data` (image: `/data`) | DB, upload spool, FS blobs, master key |
| `TR_LISTEN` | `:8080` | listen address (restart to change) |
| `TR_MASTER_KEY` | contents of `$TR_DATA_DIR/master.key`, generated on first start (0600) | 32-byte key (base64) that encrypts secret settings. Set it from a secret manager to keep it off the data volume. Losing it means re-entering secrets. |

**Settings store** (next migration):
```sql
CREATE TABLE settings (
  key        TEXT PRIMARY KEY,          -- e.g. "storage.s3.secret_access_key"
  value      TEXT NOT NULL,             -- JSON value; for secrets: base64(nonce || AES-256-GCM ciphertext)
  secret     INTEGER NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  updated_by INTEGER REFERENCES users(id) ON DELETE SET NULL
) WITHOUT ROWID;
```
- Encryption: AES-256-GCM (stdlib), key = HKDF-SHA256(master key, info "telesphorepo settings v1") via stdlib `crypto/hkdf`,
  AAD = the setting key (a ciphertext cannot be moved to another key). Random 12-byte nonce.
- The server holds the effective settings in memory (atomic snapshot), reloaded on every write;
  request paths never query `settings`.
- **Override:** any setting can be forced with env `TR_<KEY>` (dots → `_`, upper-case). Keys are
  named so the env names equal the catalogue below (e.g. key `s3.bucket` ↔ `TR_S3_BUCKET`,
  `deb.debounce` ↔ `TR_DEB_DEBOUNCE`). Overridden settings are shown read-only in the UI as "set by
  environment". This exists for e2e tests and container platforms, not as the primary config path.
- Secret settings are write-only over the API (`GET` returns `{"set": true}`), never logged.
- Changes apply live, except: storage backend changes are validated first (`POST
  /api/settings/storage/test` performs put/head/get/delete of a probe object), then hot-swapped;
  existing blobs are **not** moved (the UI warns and points to `telesphorepo move`, M3).
- Debian signing key: generated on first start (or uploaded / regenerated in the UI) and stored as
  an encrypted secret setting; the public key stays at `/keys/debian.asc`. This supersedes the
  brief's "key from a mounted file, never in the DB": the key is never stored in plaintext and the
  master key is not in the DB. A mounted file via `TR_GPG_KEY_FILE` remains as an override.
- OIDC settings (M2), including the client secret, are configured the same way.
- First-run admin: random password printed once (§4); `TR_BOOTSTRAP_ADMIN_USER/PASSWORD` stay
  as an override for tests.
- API: `GET /api/settings` (admin; grouped, with `source: "db"|"env"|"default"`),
  `PATCH /api/settings` (admin; partial update, validated), `POST /api/settings/storage/test`,
  `POST /api/settings/signing-key` (generate or upload). UI: an admin **Settings** page with
  sections General, Storage, Security, Debian, SSO.

**Setting catalogue** (M1 env names shown; in M1.2 each becomes a DB setting with the same default,
overridable by env):


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
| `TR_GPG_KEY_FILE` | — (key stored encrypted in the DB, auto-generated if absent) | optional mounted armored OpenPGP **private** key; replaces the stored key |
| `TR_GPG_PASSPHRASE_FILE` | — | optional file containing the key passphrase |
| `TR_DEB_DEBOUNCE` | `2s` | quiet period before a Debian index rebuild |
| `TR_DEB_MAX_DELAY` | `30s` | max delay of a rebuild under continuous uploads |
| `TR_BOOTSTRAP_ADMIN_USER` / `TR_BOOTSTRAP_ADMIN_PASSWORD` | — | if set and the users table is empty at startup, create this admin |
| `TR_LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |

Secrets are never logged and never stored in plaintext.

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
| `telesphorepo keygen [FILE]` | write a new Ed25519 signing key (default `./signing.asc`, refuses to overwrite) and print its fingerprint |
| `telesphorepo version` | print version |
| `move` / `import nexus` | **[M3]**, designed to the same rule (e.g. `telesphorepo move s3` copies from the current backend to the named one using env config) |

**Startup defaults that remove setup steps:**
- `TR_DATA_DIR` defaults to `./data` for the binary; the Docker image sets `/data`.
- First start with an empty users table and no `TR_BOOTSTRAP_ADMIN_*`: create `admin` with a random
  password and print it once to stderr in a clearly marked block (never to the JSON log stream).
- No `gpg.key_file`: use the stored `debian.signing_key` (encrypted, §3), generating it on first
  start (logs the fingerprint); a legacy `$TR_DATA_DIR/signing.asc` is imported once. An explicit
  `TR_GPG_KEY_FILE` that does not exist is a startup error (no silent generation over a
  misconfigured mount).
- If the embedded UI was not built (only `dist/.keep`), `/` serves a small page explaining how to
  build it (`go tool task build`) or run the Vite dev server.

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

### 5.4 Format plugins **[M2.5]**

**Goal:** formats are plugins. Adding Cargo, npm, PyPI, Yum/RPM or a generic "raw" format means
writing one package and adding one registration line; nothing in the core (DB schema, repos, API,
server wiring, settings, CLI, UI shell, dev tooling) names a specific format. Maven and Debian get
no special treatment — they are just the first two plugins.

**Plugin contract** (`internal/format`):
```go
type Format interface {
    Name() string                                   // URL segment and repos.format value: "maven", "debian", "cargo"
    Title() string                                  // "Maven / Gradle", "Debian (apt)"
    DefaultConfig() json.RawMessage                 // repo config for new repos
    ValidateConfig(raw json.RawMessage) (json.RawMessage, error) // normalize + validate repo config
    Register(mux *http.ServeMux, h Host)            // mounts /{name}/{repo}/... (and any extra paths it owns)
}
// Optional capabilities, discovered by type assertion:
type Starter      interface { Start(ctx context.Context) }                        // background workers
type Migrator     interface { Migrations() fs.FS }                                 // format-owned schema
type Configurable interface { Settings() []settings.Def }                          // contributes a settings group
type StatusReporter interface { Status(ctx context.Context, repo *repos.Repo) (any, error) } // e.g. {"pending": n}
type ConfigObserver interface { RepoConfigChanged(ctx context.Context, old, new *repos.Repo) error }
type Describer    interface { Usage(repo *repos.Repo, baseURL string) []Snippet } // client setup snippets for UI/CLI
```
`Host` is the narrow set of core services a plugin may use: auth gate, assets (spool, lock, put,
serve, upsert-in-tx), DB handle, live config, settings, signing, logger. Plugins never import each
other.

**Registry:** `internal/formats/all.go` is the only file listing formats
(`format.Register(maven.New, debian.New, …)`); the server iterates the registry for mounting,
migrations, `Start`, settings and status.

**Core changes:**
- Schema: drop the `repos.format` CHECK (migration rebuilds `repos` once); the format is validated
  against the registry instead. Format tables keep a format prefix (`deb_*`, `cargo_*`).
- Migrations: every owner (core, and each `Migrator` plugin) has its own track in
  `schema_migrations(component TEXT PRIMARY KEY, version INTEGER NOT NULL)`; core is applied first.
  Nobody runs the system yet, so each track starts as one `0001_init.sql`: core's holds the core
  tables, and the debian plugin's holds the `deb_*` tables. A track gets a second file only once a
  release has shipped.
- `repos`: no format constants or config rules; `Create`/`Update` call the plugin's
  `ValidateConfig`. Unknown format → 400 with the list of available formats.
- API: `GET /api/formats` → `[{name, title, default_config, settings_group?}]`;
  `GET /api/repos/{name}/status` (generic, from `StatusReporter`, `{}` otherwise) replaces
  `/debian/status`; `GET /api/repos/{name}/usage` returns `Describer` snippets. The repo PATCH calls
  `ConfigObserver` generically.
- Settings: the registry merges plugin `Settings()`; the Debian group moves into the debian plugin.
  Signing stays a core service because several formats (Debian, Yum) need it.
- CLI `dev`: creates one demo repo per registered format and prints each one's `Usage` snippets.
- UI: a format UI registry (`web/src/formats/<name>.tsx`, optional) for custom config editors;
  everything else is generic and data-driven (`/api/formats`, `/status`, `/usage`), with a JSON
  config editor fallback.
- Dev proxy: Vite proxies every non-UI path to the backend (one rule), not a per-format list.
- E2E: `ForEachStorage` stays; each format owns its `e2e/<format>_test.go`.

**Proof of pluggability (acceptance):** in the same milestone add a third format, **raw** (generic
hosted files: `PUT/GET/HEAD /raw/{repo}/{path...}`, overwrite policy in its repo config), touching
only `internal/raw/`, `internal/formats/all.go` and `e2e/raw_test.go`. If that requires editing any
other core file, the abstraction is incomplete and must be fixed before M3. AGENTS.md's "Adding a
repo format" section is rewritten to this flow.

### 5.5 Data access and query performance **[M2.5]**

**Goal:** the cost of a request does not grow with the data (§1.1), and a test proves it. The
choice of data-access layer was measured, not guessed: raw `database/sql`, sqlc and Bun ran the
same seven request-path operations on 100k and 1M seeded assets. Per-row cost was the same in all
three; the access pattern decided speed. Bun used about 3× the memory per operation, formats
argument values into the SQL text on the client (no prepared-statement reuse), and needed raw
SQL fragments for FTS, joins and upserts anyway.

**Layer: sqlc** (`go tool sqlc`, registered with `tool` in `go.mod`; codegen only, nothing is
linked into the binary).
- Each owner keeps its queries in `queries/*.sql` next to its package: `internal/db/queries` for
  core, `internal/<format>/queries` for a plugin. Each owner has its own `sqlc.yaml` in its
  package directory (so a new format edits no core file); a plugin's config reads the core
  schema plus its own migration. Generated code goes in a `dbq` subpackage of the owner and is
  committed. `go tool task gen` runs sqlc for every `internal/**/sqlc.yaml`.
- Services call the generated `Queries` on the reader pool or on a write transaction
  (`db.Tx`). The generated code uses `database/sql`, so the writer/reader pools and pragmas stay.
- Two kinds of query stay hand-written, in the owner's `dbq/extra.go`: the FTS5 search (sqlc
  cannot reference `rowid` or `assets_fts MATCH`, and its fallback sorts with a temp B-tree, 3×
  slower at 1M rows and growing) and row streaming with `iter.Seq2` (sqlc has no iterator mode).
  Optional filters become separate named queries, not runtime string building.
- Every hand-written query is a named constant registered with the plan guard below.

**Schema rules:**
- Every foreign key has an index on the child column (e.g. `deb_packages(asset_id)`): otherwise
  each parent write scans the child table. The 1M spike showed `SCAN deb_packages` on every asset
  upsert.
- Low-cardinality filters on request paths use partial indexes (e.g. `repos(id) WHERE
  anonymous_read = 1`).
- Keyset pagination only: `WHERE key > ? ORDER BY key LIMIT ?` on an index whose order matches,
  with exactly one lower bound per key column. SQLite uses one lower bound in the range scan and
  checks a second one (e.g. `path >= prefix AND path > after`) row by row, so deep pages scan
  from the start. The 1M scale test showed this as a 2× slower asset page.
  Substring search only through `assets_fts` (trigram).
- `STRICT` tables, `WITHOUT ROWID` where the key is the natural primary key, `ANALYZE` stats
  refreshed with `PRAGMA optimize` on close and every 6 h.

**Plan guard.** `telesphorepo dbcheck` (hidden command) opens a fresh database, runs `EXPLAIN QUERY
PLAN` on every query (all sqlc queries, read from the embedded query files, and every registered
hand-written one), and fails on a `SCAN` of a real table or a `USE TEMP B-TREE`. The few queries
that scan by design (startup recovery, background index rebuilds) are named in an allow-list
with the reason. The e2e test `TestQueryPlans` runs it in the real image, so every query is
checked even if no other test reaches it.

**Scale test** (brought forward from M4 into M2.5): `e2e/scale_test.go` behind `//go:build scale`
(`go tool task test scale`). The hidden command `telesphorepo seed N` bulk-inserts N synthetic
asset rows, split over 10 repos per format that implements `format.Seeder` (Debian adds a package row per
asset), straight into the database; the test runs it in the image at
100k and at 1M, then measures over HTTP: Maven GET (point lookup), asset list pages, search, and
Basic-auth with a token. It fails when the p95 of an operation at 1M is more than 2× its p95 at
100k.

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
- JSON API (`/api/…`): session cookie **or** HTTP Basic. HTTP Basic (password or read token)
  reaches only GET routes that are not admin routes. Routes that change state and admin routes
  need the UI session (§7.6).
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
  bootstrap break-glass admin. Basic auth works on package endpoints and the read routes of the
  API (§7.2).
- `oidc` — created on first SSO login, identified by `(oidc_issuer, oidc_subject)`. No password
  (`password_hash` is the unusable sentinel `"!"`), cannot create API tokens, cannot authenticate
  with Basic anywhere. UI session only. SSO is for administrators only: an OIDC login that does not
  qualify as admin is refused (`403` page), and no user row is created.

**Protocol:** OpenID Connect authorization-code flow with PKCE (S256), via
`golang.org/x/oauth2` + `github.com/coreos/go-oidc/v3` (discovery from `TR_OIDC_ISSUER`, ID-token
verification of signature, issuer, audience, expiry and nonce).

**Config** — per §3 these are DB settings in the Settings page's **SSO** group (keys `oidc.issuer`,
`oidc.client_id`, `oidc.client_secret` (secret, encrypted), `oidc.redirect_url`, `oidc.scopes`,
`oidc.admin_claim`, `oidc.admin_values`, `oidc.admin_emails`, `oidc.session_ttl`,
`admin_password_login`), each overridable by the env name shown. SSO is enabled iff `oidc.issuer`
is set; saving SSO settings validates them by fetching the issuer's discovery document.
The client secret is a normal secret setting (`TR_OIDC_CLIENT_SECRET` override), replacing the
`_FILE` variant below:

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
| `TR_ADMIN_PASSWORD_LOGIN` | `true` | `false` disables UI password login for local admins once SSO works (break-glass: set `TR_ADMIN_PASSWORD_LOGIN=true` env and restart) |

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

### 7.6 Publisher credentials **[M3.5]**

**Goal:** a leaked secret cannot publish. Every write on a format route (Maven PUT, Debian
upload, raw PUT, any future plugin) needs a **publish session**: a random credential the server
generates just before a publish, for one repo, that ends when the publish is done and never
lives longer than `publish.session_ttl` (default `15m`, max `1h`). Passwords and long-lived
tokens keep working for **reads** only; existing API tokens become **read tokens**. In-process
writers (Nexus sync, storage move) do not pass the HTTP gate and are unaffected.

The server alone generates, stores (SHA-256 of the secret), scopes, expires and revokes every
publish session. A caller only proves who it is; three proofs are accepted:

| Who publishes | Proof | One-time setup | Each publish |
|---|---|---|---|
| Developer, Gradle (local) | Enrolled device key (Gradle plugin) | `./gradlew telesphorepoLogin`, approve in a browser with password + second factor | Automatic |
| Developer, Maven (local) | Enrolled device key (Maven extension) | `mvn telesphorepo:login`, approve the same way | Automatic |
| GitHub Actions, Jenkins, any OIDC CI | The CI's signed identity token + an admin trust policy | Admin adds a trust policy; workflow gets `id-token: write` (GitHub) or the OIDC Provider plugin (Jenkins) | Automatic with the plugin/extension, or two `curl` lines |
| Anyone, by hand | Web UI login with password + second factor | none | "New publish session" in the UI, copy the token (15 min) |

**Second factor (local accounts; SSO stays admin-only, §7.5).**
- TOTP (`github.com/pquerna/otp`: SHA-1, 6 digits, 30 s, ±1 step; a used step is refused again)
  and passkeys (`github.com/go-webauthn/webauthn`; RP ID and origin from `base_url`). A user may
  register several of each, plus 10 one-time recovery codes (argon2id-hashed). These are the only
  new modules; both are the standard Go libraries for their protocol.
- A local user with a second factor signs in with password, then the factor. A user without one
  signs in to a session that can only reach `/api/me`, `/api/auth/logout` and the second-factor
  setup routes (403 `{"error":"second_factor_required"}` elsewhere). The break-glass `admin` follows
  the same rule. CLI `user passwd` keeps working.
- **The second factor cannot be skipped through the API.** Every `/api` route that changes state
  (POST, PUT, PATCH, DELETE) and every admin route (GET too) needs the UI session cookie. HTTP
  Basic with a password or a read token reaches only the GET routes that are not admin routes
  (repo list, tree, assets, search, status, usage, `/api/me`, and the caller's own tokens,
  devices and sessions), plus format reads. Anything else answers 401
  `{"error":"sign in through the web UI for this action"}`; a signed-in user without the right
  gets 403. So a stolen admin password cannot create a trust policy, a user or a permission, or
  change a setting. The exceptions take no credential or their own token: sign-in,
  `/api/auth/config`, `/api/formats`, device enrollment and poll, the publish challenge, the device
  and OIDC session mints, closing a session with its token, and the upload and delete routes with
  a publish session token. `http.CrossOriginProtection` still guards cookie requests. Dev mode
  keeps this rule; it only lets an admin session exist without a factor.
- Tables: `user_totp(id, user_id, secret BLOB /*encrypted, AAD user_totp:<id>*/, name, created_at,
  last_step)`, `user_passkeys(id, user_id, credential_id BLOB UNIQUE, public_key, sign_count, name,
  created_at, last_used_at)`, `user_recovery_codes(user_id, code_hash, used_at)`.

**Device enrollment** (the device flow of RFC 8628, so a terminal never needs a browser of its own):
1. The plugin generates an Ed25519 key pair, stores the private key in
   `~/.telesphorepo/<host>/device.json` (owner-only permissions), and calls
   `POST /api/devices/enroll` `{name, kind: "gradle"|"maven", public_key}` (no auth). Answer:
   `{device_code, user_code: "ABCD-EFGH", verification_url: "<base_url>/#/device", expires_in: 600,
   interval: 5}`. The plugin prints the URL and the code.
2. The developer opens the URL in any browser (phone included), signs in (password + second
   factor), sees the code, device name and kind, and approves with a fresh second-factor check:
   `POST /api/devices/approve` `{user_code, totp_code | passkey assertion}`.
3. The plugin polls `POST /api/devices/enroll/poll` `{device_code}` until it gets `{device_id}`
   (`{"error":"authorization_pending"}` before; `expired_token` after 10 min). Codes are single
   use; the poll is rate-limited.
- A device stays valid for `publish.device_ttl` (default `90d`; then the plugin enrolls again)
  unless revoked. Users list and revoke their devices in the UI; admins list and revoke anyone's.
  Revoking a device ends its open sessions at once.
- Tables: `devices(id, user_id, name, kind, public_key BLOB UNIQUE, created_at, expires_at,
  last_used_at, revoked_at)`, `device_enrollments(device_code_hash PRIMARY KEY, user_code UNIQUE,
  name, kind, public_key, created_at, expires_at, approved_by, device_id)`.

**Minting from a device:**
1. `POST /api/publish/challenge` `{device_id}` → `{nonce}` (32 random bytes, base64url, single use,
   60 s).
2. `POST /api/publish/sessions` `{device_id, repo, nonce, signature}`; the signature is Ed25519
   (base64url) over `"telesphorepo publish v1\n" + nonce + "\n" + device_id + "\n" + repo`.
3. The server checks the signature, that the device is not revoked or expired, and that its user
   has `write` on the repo now, then answers `{id, token: "trs_<12hex>_<secret>", repo,
   expires_at}`. The token is the HTTP Basic password (any username) for the publish; it also
   reads that repo. `DELETE /api/publish/sessions/{id}` with the same token ends it.

**Manual sessions:** `POST /api/publish/sessions/manual` `{repo, totp_code | passkey assertion}`
with a UI login session → the same answer. The UI shows the token once with copy-paste snippets.

**CI trust policies (OIDC):**
- Admins manage `publish_trusts(id, name UNIQUE, issuer, audience, claims TEXT /*JSON*/, jwks TEXT
  /*optional pasted JWKS*/, created_by, created_at, disabled)` and `publish_trust_repos(trust_id,
  repo_id)`. `claims` maps a claim name to allowed values, each exact or a `*` glob, e.g.
  `{"repository": ["acme/app"], "ref": ["refs/tags/v*"], "workflow_ref": ["acme/app/.github/workflows/release.yml@*"]}`.
  Every listed claim must match. `audience` defaults to `base_url`. When `jwks` is set the server
  uses it instead of fetching the issuer's keys (a Jenkins the server cannot reach).
- `POST /api/publish/sessions/oidc` `{token, repo}`: the server verifies the token's signature
  (issuer discovery + JWKS, cached, or the pasted JWKS), issuer, audience, `exp`, and `iat` at most
  10 min old; finds an enabled trust whose issuer, audience and claims match and that lists the
  repo; refuses a token it has seen before (`jti`, else SHA-256 of the token, kept until `exp`);
  then mints a session with `subject` = the token's `sub`.
- UI presets: **GitHub Actions** (issuer `https://token.actions.githubusercontent.com`) and
  **Jenkins** (issuer `<jenkins url>/oidc` from the OIDC Provider plugin). Any issuer works.

**Sessions table:** `publish_sessions(id, public_id UNIQUE, secret_hash, repo_id, user_id NULL,
device_id NULL, trust_id NULL, via TEXT CHECK IN ('device','oidc','manual'), subject TEXT,
created_at, expires_at, closed_at)` with indexes on `expires_at`, `device_id` and `trust_id`.
Ended rows are kept 30 days as an audit trail (admin **Publish log** page, keyset by id), then a
background job deletes them.

**Write gate.** `auth.RepoGate` with `need >= Write` accepts only an open publish session for
that repo, and still checks the user's current permission (device and manual sessions) and
`locked_by`. Anything else gets 401 with `WWW-Authenticate: Basic` and
`{"error":"publishing needs a publish session: <base_url>/#/publishing"}`.

**Clients** (own repository, https://github.com/Telesphorepo/clients, MIT; Java 17, no
dependencies beyond the build tool's API and the JDK):
- **Gradle plugin** `gradle/`, settings plugin id `com.telesphorepo`:
  `telesphorepo { url = "https://repo.acme.com" }` in `settings.gradle.kts`. It gives every Maven
  repository on that host lazy credentials. At the first task that publishes to such a repo it
  mints a session for that repo (GitHub OIDC when `ACTIONS_ID_TOKEN_REQUEST_URL` is set; an OIDC
  token from the env var named by `telesphorepo.oidcTokenEnv` for Jenkins; else the device key)
  and closes it at build end through a `BuildService`. `telesphorepoLogin` enrolls; with no
  device and no CI token, a publish fails with "run ./gradlew telesphorepoLogin".
- **Maven extension** `maven/`, `com.telesphorepo:telesphorepo-maven-extension`,
  declared in `.mvn/extensions.xml`. It mints a session per target repo before `deploy` uploads
  (same three proofs) and closes them when the build ends. `mvn telesphorepo:login` enrolls.
  `settings.xml` needs no password for the server.
- **CI without the plugin/extension**: documented `curl` snippets for GitHub Actions and Jenkins
  in the UI's **Publishing** page and in the server `README.md`.
- **Releases**: the clients repo has a workflow (`.github/workflows/release.yml`) that publishes
  the Gradle plugin and the Maven artifacts to a Nexus release repository on a `v*` tag, with the
  credentials in GitHub secrets. The Plugin Portal and Maven Central jobs follow after namespace
  verification.

**UI:** login with second factor; **Security** (TOTP, passkeys, recovery codes); **Devices**;
device approval page (`#/device`); **Publishing** (new manual session, client setup snippets);
admin **Trust policies** (with presets) and **Publish log**; **Tokens** renamed **Read tokens**.

**E2E:**
- Core zone: the harness gets `PublishToken(t, s, user, repo)`, which enrolls a device the real
  way (the test computes TOTP codes) and mints a session. Every existing publish switches to it.
  New tests: write refused with a password or read token; revoked and expired device; closed and
  expired session; session for another repo; second-factor login and the setup-only session;
  enrollment code expiry and reuse; replayed nonce; manual session; OIDC exchange with Keycloak as
  the CI issuer (a client whose tokens carry GitHub-shaped claims), wrong claim, wrong audience,
  replayed token, pasted JWKS.
- Zone `clients` (`go tool task test clients`): Gradle and Maven containers build the plugin and
  the extension from the clients repo (`E2E_CLIENTS_DIR`, or a shallow `git clone` of
  `E2E_CLIENTS_REF`, default `main`), publish them to a test server, then (1) enroll (the test reads
  the code from the container output and approves through the API), publish a library and a
  Gradle plugin, resolve them; (2) publish as "GitHub Actions" with a fake
  `ACTIONS_ID_TOKEN_REQUEST_URL` container that serves a Keycloak-minted token.
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

## 11. Storage move **[M3]**

**Goal:** an admin changes the storage backend in Settings and the server moves every blob to
the new backend with no downtime and no CLI. The source is never modified or deleted.

**Start.** A PATCH of `/api/settings` that changes a backend key (§3) no longer swaps the store
directly. It probes the target as today, then:
- If `assets` is empty, it saves the settings and swaps at once (as before).
- Otherwise it saves nothing to `settings` and starts a move: 202 `{move: Move}`. The target
  configuration is stored in the move row, encrypted like a secret setting (AES-256-GCM, AAD
  `storage_moves:<id>`). Only one move runs at a time (409 otherwise). An env-locked storage key
  stays 409, as today.

**Table** (core migration): `storage_moves(id INTEGER PRIMARY KEY, target BLOB NOT NULL, state TEXT
NOT NULL CHECK (state IN ('copying','done','failed','cancelled')), cursor INTEGER NOT NULL DEFAULT
0, total INTEGER NOT NULL, copied INTEGER NOT NULL DEFAULT 0, skipped INTEGER NOT NULL DEFAULT 0,
failed INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', started_by INTEGER REFERENCES
users(id) ON DELETE SET NULL, started_at INTEGER NOT NULL, finished_at INTEGER) STRICT`, plus
`storage_move_failures(move_id, asset_id, reason, PRIMARY KEY(move_id, asset_id)) WITHOUT ROWID`.

**During the move** the `Live` store holds a `Mirror{src, dst}`:
- Reads (`Get`, `Head`, `PresignGet`) use `src`.
- `Put` and `Delete` go to `dst` first, then `src`; an error on either fails the call.
- The copier walks `assets` by `id > cursor` in pages of 500. For each asset it takes the asset
  lock (`assets.Service.Lock`, the same lock uploads take), then `Head`s `dst`: if the size
  matches it skips; else it streams `src` → `dst` while hashing. A source blob whose SHA-256 is
  not `assets.sha256`, or a missing source blob, is a failure row; the copier goes on. It
  persists `cursor` and the counters after each page.
- Blobs written by uploads after the move started are already in `dst` (mirror writes), so one
  pass is enough.

**Finish.** When the walk ends with `failed = 0`, one settings write saves the target as the
active storage settings, `Live` swaps to `dst`, and the move becomes `done`. With failures the
move becomes `failed`: `Live` drops the mirror and stays on `src`, and the UI lists the failures
(keyset over `storage_move_failures`). The admin can fix them and start again; already copied
blobs are skipped by the size check. Cancel (`DELETE`) stops the copier and drops the mirror.

**Restart.** On startup a move in state `copying` resumes: the server rebuilds the mirror from
the stored target and continues from `cursor`. If the target can no longer be opened, the move
fails with the error.

**API** (admin): `GET /api/storage/moves/current` → Move or 404; `GET /api/storage/moves/{id}`;
`GET /api/storage/moves/{id}/failures?after=&limit=`; `DELETE /api/storage/moves/{id}` (cancel).
A Move is `{id, state, total, copied, skipped, failed, error, started_at, finished_at, target:
{storage, endpoint?, bucket?, prefix?, fs_root?}}` — never secrets.

**UI:** the Settings storage section shows a progress bar while a move runs, then the result.

**E2E:** fs → SeaweedFS and SeaweedFS → fs with Maven, Debian and raw content; uploads during the
move (both stores get them); a forced failure (a blob removed from `src`); restart mid-move (the
harness restarts the container on its data volume); after `done`, real clients still resolve
and the source still holds every blob.

## 12. Import from Nexus **[M3]**

**Goal:** an admin moves hosted repos from a Sonatype Nexus 3 server with a warm cut-over: the
server mirrors the chosen Nexus repos until the admin cuts over. Formats: every format with a
plugin that implements `Importer` (Maven incl. Gradle plugins, Debian (apt) and raw today).
Nexus is read only through its REST API.

**Plugin capability** (`internal/format`), not Nexus-specific in the plugin:
```go
type Importer interface {
    ImportFormats() []string // source format ids it accepts, e.g. "maven2", "apt", "raw"
    Import(ctx context.Context, repo *repos.Repo, item ImportItem) (ImportOutcome, error)
}
type ImportItem struct {
    Path   string                              // source path, forward slashes
    Size   int64
    SHA256 string                              // "" when the source does not give one
    Open   func(ctx context.Context) (io.ReadCloser, error)
}
type ImportOutcome string // "imported", "skipped" (already present, same bytes), "ignored" (not this format's file)
```
- maven: stores every file at its path through the same rules as PUT (identical bytes → skipped,
  `maven-metadata.xml*` replaced).
- debian: takes `*.deb` files under `pool/` through the upload pipeline (parse, validate,
  `deb_packages`, dirty mark) into the Nexus repo's distribution (from the Nexus repo's
  `apt.distribution`, passed in the target repo config) and component `main`; ignores `dists/`.
  The indexer rebuilds the indexes.
- raw: stores every file at its path.

**Sources, not one-shot jobs.** An admin adds a **Nexus source** once (name, base URL, username,
password; credentials encrypted with AAD `nexus_sources:<id>`, never returned or logged). The
source page lists every Nexus repo with its format. A repo whose format no plugin's
`ImportFormats()` accepts shows "no plugin for format X" and becomes selectable once a plugin
for it is installed, so repos can be added to a source at any time.

**Mirroring and cut-over.** A source is `mirroring` until it is `cut_over`.
- A **sync** walks each selected repo with `GET /service/rest/v1/assets?repository=X` (paged by
  `continuationToken`) and calls the target plugin's `Import` per asset. The plugin skips an
  asset whose path and SHA-256 already match without downloading it, so a sync downloads only
  new or changed files. The continuation token is saved after each page, so a restart resumes
  the sync where it stopped.
- Syncs run every `nexus.sync_interval` (setting, default `15m`) while a source is mirroring, and
  on "Sync now". One sync runs at a time per source.
- A repo that mirrors a source is **read-only for uploads** here (writes answer 409 "repo mirrors
  Nexus source X; cut over first"), so the two servers cannot diverge. Reads work normally.
- **Cut over** runs a final sync. When the sync walks every mapped repo to the end, it clears the
  mirror flag on the repos, sets the source to `cut_over`, stops its syncs and deletes its stored
  credentials. Files that failed to import do not stop the cut-over: they stay in the failure list
  of the cut-over sync, and the source keeps their count (`cutover_failed`) and the sync id
  (`cutover_sync_id`) as a warning. When a repo cannot be walked to the end (Nexus unreachable, a
  listing error), the cut-over stops and the source stays `mirroring`, because the missing files
  are unknown.
- Assets deleted in Nexus are not deleted here.

**Tables** (core): `nexus_sources(id, name UNIQUE, base_url, credentials BLOB, state CHECK IN
('mirroring','cut_over'), created_by, created_at, last_sync_at, last_error, cutover_sync_id, cutover_failed) STRICT`;
`nexus_source_repos(source_id, source_repo, source_format, target_repo_id, PRIMARY KEY(source_id,
source_repo)) WITHOUT ROWID` (plus an index on `target_repo_id`, which is how the upload path checks
"is this repo mirroring"); `nexus_syncs(id, source_id, state CHECK IN ('running','done','failed',
'cancelled'), started_at, finished_at, error)` with per-repo progress `nexus_sync_repos(sync_id,
source_repo, continuation, imported, skipped, ignored, failed, state)` and
`nexus_sync_failures(sync_id, source_repo, path, reason)`. `internal/nexus` holds the client, the
scheduler and the sync runner, and names no format.

**API** (admin): `POST /api/nexus/sources` `{name, base_url, username, password}` (checks the
connection first); `GET /api/nexus/sources`; `GET /api/nexus/sources/{id}` (repos with format,
supported flag, mapping, last sync); `PUT /api/nexus/sources/{id}/repos` `[{source_repo, target,
create}]` (creates missing target repos with the plugin's default config, plus the distribution
for Debian); `PATCH /api/nexus/sources/{id}` (new credentials); `POST .../sync`; `GET
.../syncs?after=&limit=`, `GET /api/nexus/syncs/{id}`, `GET /api/nexus/syncs/{id}/failures`,
`DELETE /api/nexus/syncs/{id}` (cancel); `POST /api/nexus/sources/{id}/cutover`;
`DELETE /api/nexus/sources/{id}` (only after cut-over or with no mapped repos).

**UI:** an admin **Nexus** page: add a source, pick repos and targets, watch syncs and failures,
"Sync now", "Cut over".

**E2E** (zone `import`, see §15): a real Nexus container (`sonatype/nexus3`, pinned). The test
reads the generated admin password, creates maven2, apt (with a test GPG key) and raw hosted repos
through the Nexus REST API, and uploads content with the components API. It adds the source, maps
the repos and syncs, then resolves with real `mvn`/`gradle`, `apt-get install` and curl. It uploads
a new version to Nexus and syncs again (only the new files download; the rest are skipped),
checks that an upload here is refused while mirroring, cuts over, and checks that uploads here
work and the source's credentials are gone.
## 13. Web UI **[M4]**

**Stack:** Svelte 5 + Vite (no SvelteKit), Tailwind CSS v4, shadcn-svelte with the **Maia** style,
TypeScript, bun. A static build in `web/dist`, embedded in the binary and served at `/`. Routing
stays hash-based (`/#/...`), so URLs that clients print (e.g. `/#/device?code=`) keep working. The
UI is only a client of the JSON API. No UI tests; every API it uses is covered by e2e.

**Two areas:**
- **Browse** (anyone; anonymous users see anonymous-read repos). Header: logo, global search,
  sign in / account menu. Home: repositories (name, format, description, visibility). A repo
  opens as a file tree: breadcrumbs, folders first, then files with size, modified time and
  uploader **username** (never an IP). A file panel shows checksums, download, and the client
  snippet for that artifact (Maven/Gradle dependency for a jar or pom, apt line for a `.deb`).
  Users with write access see **Upload** and **Delete**. Signing out returns to Browse.
- **Settings** (signed-in users; admin sections only for admins). A left sidebar by category:
  - **Account**: profile and password, second factor, devices, read tokens, new publish session.
  - **Repositories**: list and create; per repo: general (description, anonymous read),
    permissions, format config, danger zone (delete repo).
  - **Users & access**: users, SSO.
  - **Publishing**: trust policies, publish log.
  - **Storage**: backend settings and moves.
  - **Migration**: Nexus sources.
  - **System**: general settings (base URL, limits, log level, signing key, sync interval, …).
  - **Audit log**.
  Settings that come from the settings registry render generically by group; pages group them
  under these categories.

**Design rules:** one width scale (content max width; forms in a narrow column; tables full
width), consistent spacing, inputs sized to their content, empty states, loading and error
states for every request, confirm dialogs that list what an action changes, keyboard access,
light and dark theme.

**Server support added in M4:**
- `repos.description TEXT NOT NULL DEFAULT ''` (core migration), editable in the repo PATCH.
- `GET /api/repos/{name}/tree?path=&after=&limit=` → the direct children of a folder:
  `{items: [{name, type: "dir"|"file", size?, updated_at?, uploaded_by?}], next}`. Each child is
  found with one indexed seek on `(repo_id, path)` (skip to the next sibling), so a folder page
  costs `limit` seeks however large the repo is.
- **Deletes** (`write` on the repo, through a publish session created in the UI with a fresh
  second factor, or an admin UI session): `DELETE /api/repos/{name}/files?path=` (one file) and
  `?prefix=` (a folder: a version, a package, any subtree; keyset batches). The answer lists the
  count. Blobs are removed from storage after the rows. A format plugin may implement
  `format.DeleteObserver{ AssetsDeleted(ctx, tx, repo, paths) }`: Debian removes the matching
  `deb_packages` rows and marks the dists dirty, so the indexes drop the package; Maven
  regenerates nothing (clients own `maven-metadata.xml`; deleting a version folder leaves the
  metadata listing it until the next publish, and the UI says so). Deleting a repo
  (`DELETE /api/repos/{name}`, admin) removes its rows and blobs.
- **Manual upload**: `POST /api/repos/{name}/upload` (multipart, publish session) calls the plugin's
  `format.ManualUploader{ Upload(ctx, repo, files, fields) }`. Maven: a pom plus artifacts (jar,
  sources, javadoc); coordinates come from the pom (or fields for a lone jar); the server writes
  `.md5`/`.sha1`/`.sha256` files and creates or updates `maven-metadata.xml`. Debian: `.deb` files
  with distribution and component. Raw: files under a target folder.
- **Audit log**: table `audit_events(id, at, actor_user_id NULL, actor TEXT, action, repo_id NULL,
  target TEXT, detail TEXT /*JSON*/, ip TEXT)` with indexes on `at` and `(repo_id, id)`; written for
  publish sessions (mint/close), uploads by session, deletes, repo and permission changes, user
  changes, settings changes (keys only, never values of secrets), sign-ins and failed sign-ins.
  `GET /api/audit?after=&limit=&action=&repo=` (admin, keyset by id, newest first). IP addresses
  appear only here. Rows older than `audit.retention` (setting, default `8760h`) are deleted.
- **Second factor rule** (replaces the sign-in rule of §7.6): a second factor is required to
  approve a device, to create a manual or UI session that can publish or delete, and to sign in
  as an admin. Other users sign in with a password and set up a second factor when they first
  enroll a device. An admin without a second factor gets the setup-only session. Every API
  route that changes state, and every admin route, needs the UI session (§7.6), so HTTP Basic
  with an admin password cannot go around the factor.
- **Dev mode**: `telesphorepo dev` sets an in-process flag (not a setting, not an env var, not
  reachable from `serve`) that makes the second factor optional everywhere; the dev banner says
  so. A factor that is enrolled is still checked. e2e runs `serve`, so enforcement stays tested.
## 13.1 Ease of use and network rules **[M5]**

Owner's direction (2026-10-14): the product must be what Nexus should have been in 2026: easy to
use, easy to understand, no jargon.

- **Usability pass** over every page: plain words, a clear list of what repositories exist (name,
  format, who can read and publish, size, last publish), obvious next actions, helpful empty
  states. Selection and hover highlight the whole element (lighter/darker fill); never a left
  border accent, in the app and on the website.
- **Folder upload**: drop a whole folder (a build output, a Maven-layout tree, a folder of
  `.deb`s). The server scans it for key files only (`*.pom` with the jars/classifiers that match
  it, `.module` files, `.deb`), validates each strictly (pom coordinates match the path when the
  tree has a Maven layout, jar names match the pom, `.deb` control parses), ignores everything
  else, and returns a preview of the artifacts it found. Nothing is stored until the user
  confirms the preview. It is meant for one-off uploads of libraries that are published
  nowhere else.
- **IP rules per repo**: two optional allow-lists, **read** and **publish**, of CIDRs and presets.
  Empty means unrestricted (the default). Presets: **GitHub Actions** (ranges from
  `https://api.github.com/meta`, key `actions`), **Blacksmith** (if Blacksmith publishes its egress
  ranges), and named custom lists. Preset ranges refresh daily in the background and are cached in
  the database; a failed refresh keeps the last good list. A refused request gets 403 and an audit
  event. Admin UI sessions are not exempt from read rules on format routes.
- **Client IP**: the server uses `X-Forwarded-For` only when the TCP peer is a trusted proxy. The
  `trusted_proxies` setting accepts CIDRs, addresses and the presets `private` (loopback + RFC
  1918 + ULA) and `cloudflare` (Cloudflare's published ranges, refreshed daily). The default is
  empty: nothing is trusted. Behind Docker's userland proxy or rootless Podman every outside
  request arrives from a private gateway address, so trusting `private` would let anyone who
  reaches the port directly forge `X-Forwarded-For` and get around the IP rules. The chain is
  read right to left, skipping trusted hops; the first untrusted address is the client. Docs
  recommend Caddy as the reverse proxy and ship a Caddyfile.
- **One-click proxy setup**: when an untrusted peer sends `X-Forwarded-For`, `Forwarded` or
  `CF-Connecting-IP`, the server remembers it in memory (last seen, count, header; at most 20
  peers, no database). The admin API `GET /api/network/proxy-candidates` lists them, and the
  "Reverse proxy" card asks "Requests from 10.89.0.5 carry forwarded headers. Is this your reverse
  proxy?" with a "Trust this address" button that adds the address to `trusted_proxies` through
  the settings PATCH.
## 13.2 Single sign-on for developers **[M6, direction]**

Owner's direction (2026-10-14): allow developers (not only admins) to use SSO, while local
accounts keep working. Nothing is in production, so the account and permission model may be
redesigned freely (one `0001_init.sql`, no migrations). To decide before building:
- **Two layers, two jobs.** The IdP proves who the person is and whether they are still allowed
  (sign-in, MFA, offboarding). Telesphorepo still mints a short-lived publish session per publish.
- **Provisioning**: SSO users are created on first sign-in. Access comes from IdP group →
  repo/level mappings, or from grants an admin adds after the first sign-in (decide which, or both).
- **Device enrollment** through an SSO sign-in. IdP MFA counts as the second factor when the ID
  token's `amr` shows it; otherwise the local TOTP/passkey rule applies.
- **Offboarding**: the server stores the IdP refresh token (encrypted) per SSO user and refreshes
  it when a device of that user mints a session (at most every few minutes). A refused refresh
  disables the user, revokes their devices and ends their sessions.
- Local accounts (break-glass admin, developers who are not in the IdP) stay.
## 14. Docker image
Multi-stage: `oven/bun` builds `web/` → `golang:1.27-alpine` builds with
`CGO_ENABLED=0 -trimpath -ldflags="-s -w"` → `gcr.io/distroless/static-debian13:nonroot`.
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
- **Storage**: the Maven, Debian and raw flows also run against S3 (a SeaweedFS container with
  generated keys; no setup).
- **Security**: path traversal attempts (`..`, `%2e%2e`, `%2f`) → 400; oversize upload → 413;
  decompression-bomb `.deb` → 400.

**Zones.** Tests are grouped by build tag so a run starts only the containers it needs. The
harness files carry every tag.

| Zone | Tag | Command | Contents |
|---|---|---|---|
| core | `e2e` | `go tool task test` | Formats, auth, API, settings, storage move, query plans |
| import | `import` | `go tool task test import` | Nexus source, sync and cut-over (starts a Nexus container) |
| scale | `scale` | `go tool task test scale` | 100k vs 1M latency (§5.5) |
| clients | `clients` | `go tool task test clients` | Gradle plugin and Maven extension built from the clients repo (§7.6) |
| all | all tags | `go tool task test all` | Every zone, one after the other |

Extra `go test` args still pass through (`go tool task test import -run TestNexus`). A milestone
runs the core zone plus the zones it touches; `all` runs before a push that touches several.

Gotchas for test authors:
- Maven ≥ 3.8.1 blocks `http://` repos on non-localhost hosts via the `maven-default-http-blocker`
  mirror; test `settings.xml` must override it (redeclare a mirror with that id, `mirrorOf` = a
  dummy id).
- Gradle needs `isAllowInsecureProtocol = true` for `http://` repos.
- Use a shared named Docker volume for `~/.m2`/Gradle caches so Central downloads aren't repeated,
  but delete the test's own group dir before resolving (or use a unique groupId per run).

## 16. Developer experience **[M1.1]**

**One command to run everything locally**, with `dev / build / preview` verbs (inspired by
`bun run dev/build/preview`, but Go-native): a Go tool registered in `go.mod`
(`tool …/tools/task`), invoked as `go tool task <verb>`. It needs only Go plus bun (preferred) or
npm for the web build, and behaves identically on Windows, macOS and Linux — no bash, make or root
`package.json`. (`go tool` is used rather than `go run` because it passes the real exit code
through, so Ctrl+C exits cleanly.)

| Command | What it does |
|---|---|
| `go tool task dev` | Checks the environment (Go version from `go.mod`; bun, else npm, else a clear install hint). Installs web deps when `node_modules` is missing or the lockfile changed. Starts the backend (`telesphorepo dev`, §4) on :8080 and rebuilds/restarts it when `.go`, `go.mod` or migration files change (polling watcher, no extra deps; a failed build keeps the old process running and prints the error). Starts Vite with HMR on :5173, proxying `/api`, `/maven`, `/debian`, `/keys` to :8080. Interleaved, prefixed logs (`api │`, `web │`). Prints one banner: open http://localhost:5173, admin username/password, data dir. Ctrl+C stops both cleanly. |
| `go tool task build` | Same checks/installs, builds the UI, then `bin/telesphorepo` (`.exe` on Windows) with the UI embedded, `CGO_ENABLED=0`, version stamped from git. `go tool task build image` also builds the container image `telesphorepo:local` with the detected engine. |
| `go tool task preview` | `build`, then runs `bin/telesphorepo dev` on :8080 — the production binary with the embedded UI, local dev data. |
| `go tool task test` | Runs the e2e suite (`go test -tags e2e ./e2e/... -count=1`); extra args pass through, e.g. `go tool task test -run TestMaven`. |
| `go tool task doctor` | Prints what was detected (Go, bun/npm, container engine) and what is missing. |

Production is the container image (or `bin/telesphorepo serve`); first start prints the admin password and everything else is configured in the UI (§3).

**Repo documentation:** `README.md` is for humans and stays short: what it is, how to run it locally, how to run it in production, and the few commands people use most — not a changelog, wiki or full CLI reference (a website will hold detailed docs later). `SPEC.md` describes the system as currently built (architecture, data model, endpoints, conventions) and is updated whenever behaviour changes. `AGENTS.md` holds agent-oriented working rules (build/test commands, no unit tests, no flag slop, minimal env, package `path` for keys, where things live). No `CLAUDE.md`: Claude Code reads `AGENTS.md` directly.

**Package manager:** the web app's committed lockfile is `web/bun.lock`; the Dockerfile's UI stage
uses the official `oven/bun` image with `bun install --frozen-lockfile`. The npm fallback runs
`npm install` and prints a note that bun is preferred.

**Container engine auto-detection** (`internal/containerengine`, used by `tools/run` and the e2e
harness; not linked into the server): honour `TR_ENGINE=docker|podman` and an existing `DOCKER_HOST`;
otherwise Docker if its daemon responds, else Podman: socket from `podman info` (Linux rootless or
rootful) or `podman machine inspect` (macOS unix socket, Windows named pipe); then set `DOCKER_HOST`
and, for Podman, `TESTCONTAINERS_RYUK_DISABLED=true`. A clear error lists what was tried
(e.g. "podman machine exists but is stopped: run podman machine start").

**Local S3 for tests:** e2e uses one S3-compatible server, **SeaweedFS**, so the S3 storage adapter
is tested without a real bucket. `ForEachStorage` runs `fs`, `seaweedfs`, and `r2` (only when the
`TR_E2E_R2_*` env vars are set).

## 17. Milestones
- **M1 (now)**: everything marked [M1]: config, DB, storage FS+S3, auth, Maven, Debian, JSON API,
  UI shell, CLI `serve`/`user create`/`signing-key generate`, Dockerfile, e2e suite above.
- **M1.1**: DX (§16). **M1.2**: in-app settings (§3), SPEC.md/AGENTS.md, lean README.
- **M2**: OIDC admin sign-on (§7.5) + Keycloak e2e.
- **M2.5**: format plugins (§5.4) + `raw` format as the pluggability proof; sqlc data layer, one
  migration per owner, plan guard and scale test (§5.5).
- **M3**: storage move from Settings (§11) and Nexus import from the admin UI (§12) + e2e.
- **M3.5**: publisher credentials (§7.6): publish sessions, second factor, device enrollment, CI
  trust policies, Gradle plugin, Maven extension + e2e zone `clients`.
- **M5**: usability pass, folder upload, per-repo IP rules, trusted-proxy presets (§13.1).
- **M6**: SSO for developers (§13.2), after the design points there are decided.
- **M4**: new UI (Svelte + Tailwind + shadcn-svelte Maia; Browse + Settings), folder tree,
  deletes, manual upload, audit log, second-factor rule and dev mode (§13); Debian by-hash GC.
