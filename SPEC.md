# Telesphorepo: system as built

This file describes what the code does today. Update it in the same change whenever behaviour
changes. Design rationale and future milestones are in
[docs/superpowers/specs/2026-10-14-telesphorepo-mvp-design.md](docs/superpowers/specs/2026-10-14-telesphorepo-mvp-design.md)
(called "the design doc" below).

## Purpose

A self-hosted repository for **hosted** Maven repos (including Gradle plugins), Debian (apt)
repos and raw file repos. Each format is a plugin. It ships as one Go binary with the web UI embedded. Metadata is in SQLite and blobs are on
the local filesystem or in any S3-compatible bucket (Cloudflare R2, SeaweedFS, AWS). It has no
proxy or cache repos and is not compatible with the Nexus API.

## Architecture

| Package | Role |
|---|---|
| `cmd/telesphorepo` | CLI: `serve` (default), `dev`, `user add/passwd`, `keygen`, `version`, `help`; hidden `dbcheck` and `seed` |
| `internal/server` | Wires everything together: logger, DB, settings, live storage, services, bootstrap admin, mux, access log |
| `internal/config` | Boot env (`LoadBoot`) and the effective `Config` struct; services read it through `config.Provider` |
| `internal/settings` | Settings registry, AES-256-GCM secrets, master key, atomic snapshot, env overrides, subscribers |
| `internal/db` | SQLite (modernc, no cgo), WAL, separate writer/reader pools, per-component migrations (`Migrate`), `PRAGMA optimize`, the plan guard (`CheckPlans`) |
| `internal/db/dbq` | sqlc-generated core queries (`internal/db/queries/*.sql`) plus hand-written FTS queries in `extra.go` |
| `internal/storage` | `Store` interface; `fs`, `s3`, and `Live`, a hot-swappable wrapper with `Open`, `SameBackend` and `Probe` |
| `internal/assets` | The `assets` table: spooled uploads, point lookups, downloads (stream or presigned redirect), listing, FTS search |
| `internal/auth` | Users (kinds `local`/`oidc`), argon2id passwords, Basic-auth cache, API tokens, sessions, rate limiting, permission checks |
| `internal/oidc` | OIDC admin sign-on: `/api/auth/oidc/login` and `/callback` (go-oidc + x/oauth2, PKCE S256) |
| `internal/repos` | Repos and their per-format JSON config. A `Validator` (from `internal/format`) checks the config |
| `internal/format` | The plugin contract: `Format` and the optional interfaces, `Host`, and the registry (`Register`, `All`, `Get`, `ValidateRepoConfig`) |
| `internal/formats` | `all.go`: the one list of built-in formats. The only file that names them |
| `internal/maven` | Format `maven`: `/maven/{repo}/...` GET/HEAD/PUT and the overwrite rules |
| `internal/debian` | Format `debian`: `.deb` upload/parse, durable dirty queue, debounced indexer, `/keys/debian.asc`. Owns its tables, queries (`queries/`, `dbq/`), `deb.*` settings and architecture config |
| `internal/raw` | Format `raw`: plain files at `/raw/{repo}/{path...}` with an `overwrite` policy |
| `internal/signing` | OpenPGP key `Generate`, `Parse`, `Load`, `ReadPassphrase`, clearsign/detach-sign (ProtonMail go-crypto) |
| `internal/api` | JSON API under `/api/`, wrapped in `http.CrossOriginProtection` |
| `internal/httpx` | JSON helpers, error shape, `Limit` (default 50, max 200) |
| `internal/containerengine` | Docker/Podman detection for tooling and e2e; not linked into the server |
| `web/` | React + Vite UI, embedded via `web/embed.go` (`go:embed all:dist`) |
| `tools/task` | `go tool task`: dev, build, preview, test, gen and doctor |
| `e2e/` | End-to-end suite (`//go:build e2e`, plus `scale`) using testcontainers |

## Data model (SQLite)

Core tables are in `internal/db/migrations/0001_init.sql`. Each format plugin that stores data has
its own track (for debian: `internal/debian/migrations/`). `schema_migrations` holds one row per
track (`component`, `version`). `db.Migrate` applies the files `NNNN_*.sql` of a track in order.
It refuses duplicate version numbers and a database newer than the binary. Core runs first, then
each plugin in registration order. All tables are `STRICT`. Tables with a natural key are also
`WITHOUT ROWID`. Every foreign-key child column has an index. SQLite runs `PRAGMA optimize` at
close and every 6 hours.

| Table | Key / indexes | Notes |
|---|---|---|
| `users` | PK `id`, UNIQUE `username`, UNIQUE `users_oidc (oidc_issuer, oidc_subject)` where `oidc_subject` is set | argon2id PHC hash (`!` for SSO users, never verifies), `is_admin`, `disabled`, `password_changed_at`, `kind` (`local`/`oidc`), `email` |
| `api_tokens` | UNIQUE `public_id`, UNIQUE `(user_id, name)` | `secret_hash` = SHA-256(secret); optional `expires_at`; `last_used_at` written at most once per minute; `kind` (`personal`, `minted` reserved for §7.6) |
| `sessions` | PK `id_hash` (WITHOUT ROWID), idx `expires_at`, idx `user_id` | `id_hash` = SHA-256(cookie value) |
| `repos` | UNIQUE `name`, partial idx `repos_anonymous (id) WHERE anonymous_read = 1` | `format` is any registered format name (no CHECK); `anonymous_read`; `config` JSON |
| `repo_permissions` | PK `(repo_id, user_id)`, idx `user_id` | `level` is `read` or `write` |
| `assets` | UNIQUE `(repo_id, path)`, idx `uploaded_by` | Every stored file; `kind` is `artifact`, `metadata` or `index` |
| `assets_fts` | FTS5 trigram on `path` | Kept in sync by triggers; `index` rows are not indexed |
| `settings` | PK `key` (WITHOUT ROWID), idx `updated_by` | JSON value, or base64(nonce‖AES-GCM ciphertext) when `secret=1`; `updated_by` |
| `schema_migrations` | PK `component` (WITHOUT ROWID) | Applied version per migration track |

Tables of the `debian` track:

| Table | Key / indexes | Notes |
|---|---|---|
| `deb_packages` | UNIQUE `(repo_id, distribution, component, architecture, package, version)`, idx `asset_id` | Also the streaming index for `Packages` |
| `deb_dirty` | PK `(repo_id, distribution, component, architecture)` (WITHOUT ROWID) | Durable rebuild queue; `marked_at` in unix ns |
| `deb_index_files` | PK `(repo_id, distribution, path)` (WITHOUT ROWID) | Sizes and hashes; `Release` is built from these rows |

Every query on a request path uses an index. Lists use keyset pagination (`?after=` and
`?limit=`), never `OFFSET` or `LIKE '%x%'`.

**Queries.** Core and each plugin keep their SQL in `queries/*.sql` next to a `sqlc.yaml`.
`go tool task gen` runs sqlc and writes the typed `dbq` package. Queries that sqlc cannot
express (FTS5 `MATCH`, streaming rows with `iter.Seq2`) are hand-written in `dbq/extra.go` and
listed in its `Extra` map.

**Plan guard.** `db.CheckPlans` runs `EXPLAIN QUERY PLAN` for every query of every `db.QuerySet`
(core and each `format.Queryable`) on a fresh database. A SCAN of a table and any TEMP B-TREE
fail. A SCAN of a partial index or a virtual table passes. A query that must scan goes in the
set's `Allow` map with a reason. An `Allow` entry for a query that is missing or has a clean plan
also fails. Core allows `ListSettings`, `HasUsers` and `ListPermissions`. Debian allows
`ListAllDirtyDists`, `MaxDirtyMark` and `ListAllIndexFiles` (startup and re-sign work).
`TestQueryPlans` runs the guard through the hidden `dbcheck` command.

## Storage layout

| Location | Contents |
|---|---|
| `$TR_DATA_DIR/telesphorepo.db` | SQLite database (WAL) |
| `$TR_DATA_DIR/master.key` | Master key (base64, mode 0600). Generated on first start unless `TR_MASTER_KEY` is set |
| `$TR_DATA_DIR/tmp/` | Upload spool, kept on the data volume rather than tmpfs |
| `$TR_DATA_DIR/blobs/` | FS blob root (default for the `fs.root` setting) |
| `$TR_DATA_DIR/signing.asc` | Legacy key from M1.1 only. It is imported once into `signing.key` and is no longer written |

Blob keys are the same on every backend: `repos/{repo}/{path}`, built with package `path`.
S3 keys get the optional `s3.prefix` in front. The FS backend writes a temp file and renames it
into place.
Debian pool path: `pool/{component}/{prefix}/{source}/{package}_{version-without-epoch}_{arch}.deb`.
The `{prefix}` is the first letter of the source name, or `lib` plus the 4th letter for `lib*`
sources.

## Settings model

| Layer | What | Where |
|---|---|---|
| Boot env | `TR_DATA_DIR` (`./data`, image `/data`), `TR_LISTEN` (`:8080`), `TR_MASTER_KEY` | `config.LoadBoot`. A change needs a restart |
| Bootstrap admin | `TR_BOOTSTRAP_ADMIN_USER` and `TR_BOOTSTRAP_ADMIN_PASSWORD`, set together | Used only when the users table is empty |
| DB settings | Everything else (registry in `internal/settings/registry.go`) | Edited in the admin **Settings** page or through `/api/settings` |
| Env override | `TR_<KEY>`: dots become `_`, upper-cased (e.g. `s3.bucket` → `TR_S3_BUCKET`) | Wins over the DB and shows as read-only "set by environment" in the UI |

Precedence is env override, then DB row, then default. A setting can carry a `Dev` value
(`settings.Def.Dev`). `telesphorepo dev` uses it instead of `Default`: `cookie.secure=false` in
the core and `deb.debounce=300ms` in the debian plugin.

A format can register its own settings group (interface `format.Configurable`). The group name is
the format name and the label is the format title. Plugin settings are registered once at startup,
before `settings.Load`.

| Group | Keys |
|---|---|
| general | `base_url`, `log.level`, `max_upload_bytes`, `max_json_bytes` |
| storage | `storage` (`fs`/`s3`, with `r2` as an alias for `s3`), `fs.root`, `s3.endpoint`, `s3.region`, `s3.bucket`, `s3.access_key_id`, `s3.secret_access_key`*, `s3.prefix`, `s3.path_style`, `s3.redirect`, `presign_ttl` |
| security | `cookie.secure`, `session_ttl`, `trusted_proxies` |
| sso | `oidc.issuer`, `oidc.client_id`, `oidc.client_secret`*, `oidc.redirect_url`, `oidc.scopes` (`openid profile email`), `oidc.admin_claim` (`groups`; dotted paths allowed), `oidc.admin_values`, `oidc.admin_emails`, `oidc.session_ttl` (`12h`), `admin_password_login` (`true`) |
| signing | `signing.key`* (only through its own endpoint), `gpg.key_file`, `gpg.passphrase_file` |
| debian (plugin) | `deb.debounce` (`2s`), `deb.max_delay` (`30s`) |

\* secret

- **Encryption.** Secrets use AES-256-GCM with key = HKDF-SHA256(master key, info
  `telesphorepo settings v1`). The AAD is the setting key and the nonce is a random 12 bytes. All
  of this is stdlib.
- **Secrets over the API.** The API never returns a secret; it reports `{"set": bool}`. Secrets
  are never logged.
- **Live changes.** The effective settings are an immutable snapshot behind an atomic pointer,
  rebuilt on every write. Request paths never query `settings`. Subscribers (`Service.Subscribe`)
  react to changes: the log level updates and storage is hot-swapped.
- **Storage changes.** A PATCH that changes a backend key runs the storage probe first
  (put/head/get/delete of `_telesphorepo/probe-<hex>`). Nothing is saved if the probe fails.
  Existing blobs are **not** moved.
- **SSO changes.** SSO is on iff `oidc.issuer` is set; it then needs `oidc.client_id`, `oidc.client_secret` (confidential client) and at least
  one of `oidc.admin_values` / `oidc.admin_emails`. The redirect URL is `oidc.redirect_url`, else
  `base_url` + `/api/auth/oidc/callback`; one of them must be set. `admin_password_login=false` is
  only accepted while the issuer is set. A PATCH that changes the issuer, client id/secret,
  redirect URL or scopes fetches the discovery document first; on failure nothing is saved and the
  error is under `fields["oidc.issuer"]`. The issuer is only whitespace-trimmed (trailing slashes
  must match the IdP). Incomplete SSO settings in the DB (e.g. undecryptable secret) are logged
  and turn SSO off at startup; invalid env values are a startup error.
- **Startup validation.** Env values must be valid at startup, or the server refuses to start;
  `TR_STORAGE=s3` without a bucket, endpoint or keys is a startup error. An invalid or
  undecryptable DB value is logged and ignored. A `TR_*` variable that matches no setting or boot
  variable (`TR_E2E_*` and `TR_ENGINE` excepted) is logged as a warning by name and ignored.
- **Signing key.** On first start the server generates an Ed25519 key, or imports the legacy
  `signing.asc`, and stores it encrypted in `signing.key`. Setting `gpg.key_file` /
  `TR_GPG_KEY_FILE` replaces it, and the file must then exist. Signing is a core service (`format.Host.Signer`). The
  debian plugin subscribes to settings changes. When the key changes, it marks one rebuild unit per
  dist, so each `Release` is rebuilt and `InRelease`/`Release.gpg` are signed again.
- **Lost master key.** Without the master key the secret settings cannot be decrypted and must be
  entered again.

## HTTP surface

Each format mounts its own routes through `Format.Register(mux, Host)`. The core names no format.
Format routes (package clients use HTTP Basic only: a password, or any username with a token as
the password):

| Route | Auth | Behaviour |
|---|---|---|
| `GET /healthz` | none | `ok` when the DB answers |
| `GET, HEAD, PUT /maven/{repo}/{path...}` | read / write | PUT: 201 new, 200 overwrite or identical retry, 409 changed release, 413 over `max_upload_bytes`. Mutable paths are `maven-metadata.xml*` and anything under a `-SNAPSHOT` dir. GET on S3 with `s3.redirect` answers 302 to a presigned URL. HEAD is never redirected |
| `PUT, POST /debian/{repo}/upload/{dist}/{component}` | write | Raw `.deb` body. 201 `{package,version,architecture,filename,sha256}`, 200 identical, 409 conflict |
| `GET, HEAD /debian/{repo}/{path...}` | read | `dists/...` and `pool/...` served from `assets` |
| `GET, HEAD, PUT /raw/{repo}/{path...}` | read / write | PUT: 201 new path; 200 identical bytes (any policy) or a replace when `overwrite` is `allow`; 409 different bytes when `overwrite` is `deny` (default); 413 over `max_upload_bytes`. Repo config: `{"overwrite":"deny"}` or `{"overwrite":"allow"}`. S3 redirect as for Maven |
| `GET /keys/debian.asc` | none | Public signing key |
| `/` | none | Embedded UI, hash-routed |

The repo decision table: anonymous read is allowed when `anonymous_read` is on. Otherwise no
credentials or bad credentials get 401 with a Basic challenge, valid credentials without
permission get 403, and an unknown repo gets 404 (401 for anonymous callers, so repo names don't
leak).

JSON API (`/api/`). Auth is a session cookie or HTTP Basic. Errors are `{"error": "..."}`, plus
`fields` for settings validation. Bodies are capped at `max_json_bytes`. Lists are
`{items, next}` with `?after=&limit=`.

| Method & path | Auth | Notes |
|---|---|---|
| `POST /api/auth/login` · `POST /api/auth/logout` | none · session | Login sets `tr_session` and returns the User. 401 for SSO users; 403 for a local admin when `admin_password_login=false`. Logout ends the local session only |
| `GET /api/auth/config` | none | `{password_login: true, oidc, admin_password_login}` |
| `GET /api/auth/oidc/login` | none | 404 when SSO is off, 503 when discovery fails. Stores state/nonce/PKCE verifier in memory (10 min, single use, keyed by SHA-256(state)), sets `tr_oidc` (HttpOnly, SameSite=Lax, Path `/api/auth/oidc`, 600 s), 302 to the IdP |
| `GET /api/auth/oidc/callback` | none | 400 for missing/mismatched/unknown/reused/expired state, state from before an issuer or client change, or missing `code`; 503 on discovery failure. Verifies the ID token (signature, issuer, audience, expiry, nonce) and the admin rule. Success: upsert user, session with `oidc.session_ttl`, 302 `/`. Failure: 302 `/#/login?error=` `not_authorized` \| `username_conflict` \| `invalid_username` \| `account_disabled` \| `sso_failed` |
| `GET /api/me` | any | Current user. User JSON includes `kind` (`local`/`oidc`) and `email` (`""` when unset) |
| `GET/POST /api/tokens`, `DELETE /api/tokens/{id}` | any | The token is shown once. A token cannot mint tokens. POST is 403 for SSO users |
| `GET/POST /api/users`, `PATCH /api/users/{username}` | admin (self may change own password) | The user list is paginated |
| `GET /api/formats` | none | `[{name, title, default_config, settings_group?}]` for every registered format |
| `GET/POST /api/repos`, `GET/PATCH /api/repos/{name}` | any/anon · admin | The list shows only repos the caller can see. `format` must be a registered name. `config` must be a JSON object that the format validates (400 otherwise; an unknown format lists the available ones). An empty config becomes the format default. A PATCH with `config` calls the format's `RepoConfigChanged` |
| `GET /api/repos/{name}/permissions`, `PUT/DELETE .../permissions/{username}` | admin | `{level: read\|write}` |
| `GET /api/repos/{name}/assets?prefix=&after=&limit=` | read | Ordered by path; the prefix is a range scan |
| `GET /api/search?q=&repo=&after=&limit=` | any/anon | FTS5 trigram, `q` ≥ 3 chars, readable repos only |
| `GET /api/repos/{name}/status` | read | The format's `Status` result, or `{}`. Debian: `{pending}`, the count of dirty rows |
| `GET /api/repos/{name}/usage` | read | `{base_url, snippets:[{title, lang, code}]}` from the format's `Usage`. `base_url` is the `base_url` setting, else the request scheme and host |
| `GET /api/settings` | admin | `{groups:[{name, label, settings:[Item]}]}`. An Item has key, env, group, label, help, type, options, secret, value, default, source (`db`/`env`/`default`), locked, read_only |
| `PATCH /api/settings` | admin | `{key: value}`. `null` resets a key; `""` for a secret means unchanged. Returns 400 `{error, fields}`, or 409 when an env-locked key gets a different value. Backend changes must pass the probe; SSO changes must pass discovery |
| `POST /api/settings/storage/test` | admin | Same body as the PATCH, merged over the current values and not saved. Returns `{ok:true}`, or 400 `{ok:false, error}` |
| `POST /api/settings/oidc/test` | admin | Same, for SSO discovery. Returns `{ok:true}`, or 400 `{ok:false, error, fields}` |
| `GET/POST /api/settings/signing-key` | admin | Returns `{public_key, fingerprint}`. POST `{armored?}`: an empty value generates a new key. 409 when the key comes from `gpg.key_file` |

## Client setup

The UI shows these snippets per repo (`/usage`), and `telesphorepo dev` prints them. `HOST` is the server URL. Credentials are a user and password, or any username with an API
token as the password.

| Client | Setup |
|---|---|
| Maven | URL `HOST/maven/{repo}` in `<distributionManagement>` / `<repositories>`; credentials go in `~/.m2/settings.xml` `<server>` with the same `<id>`. Maven 3.8.1+ blocks `http://` on non-localhost hosts |
| Gradle | `maven { url = uri("HOST/maven/{repo}"); credentials { username = ...; password = ... } }` in `publishing.repositories`, and in `pluginManagement.repositories` to consume plugins; `isAllowInsecureProtocol = true` only for plain http |
| apt | Key: `curl -fsSL HOST/keys/debian.asc -o /etc/apt/keyrings/telesphorepo.asc`. Source: `deb [signed-by=/etc/apt/keyrings/telesphorepo.asc] HOST/debian/{repo} {dist} {component}`. For a private repo add `/etc/apt/auth.conf.d/telesphorepo.conf` (`machine host login USER password TOKEN`, mode 600) |
| Upload `.deb` | `curl -u USER:TOKEN -T x.deb HOST/debian/{repo}/upload/{dist}/{component}`. Indexes rebuild after `deb.debounce`; poll `GET /api/repos/{repo}/status` for `pending: 0` |
| Raw | Upload: `curl -f -u USER:TOKEN -T file.tar.gz HOST/raw/{repo}/path/file.tar.gz`. Download: `curl -fL [-u USER:TOKEN] -O HOST/raw/{repo}/path/file.tar.gz` |

## Auth model

| Credential | Storage | Accepted on |
|---|---|---|
| Password | argon2id (64 MiB, t=3, p=2), at most 4 hashes at once; successful Basic checks are cached for 10 min under an HMAC key | Basic auth (formats + API), login |
| API token `trp_<12hex>_<secret>` | SHA-256(secret), looked up by id, constant-time compare | Basic password (any username), `/api/` |
| Session `tr_session` | SHA-256(id), absolute `session_ttl` | `/api/` only. HttpOnly, SameSite=Strict, Secure per `cookie.secure` |

- **Account kinds.** `local` users have a password, API tokens and Basic auth; publishers and the
  break-glass admin. `oidc` users are created on their first SSO login, keyed by
  `(oidc_issuer, oidc_subject)`, and are always admins. They have no usable password, cannot
  create tokens, get 401 on Basic auth and password login, and cannot have their password changed
  (403, also `user passwd`). Their session only comes from SSO.
- **SSO admin rule.** The `oidc.admin_claim` (ID token, falling back to UserInfo; string or array)
  must contain one of `oidc.admin_values`, or the email must be verified and in
  `oidc.admin_emails`. It is re-checked at every SSO login: a refused login creates no user and
  deletes an existing SSO user's sessions. The username is `preferred_username` lower-cased with
  chars outside `[a-z0-9._-]` mapped to `-`; if a different account owns it the login fails
  (`username_conflict`); accounts are never merged by name.
- **Roles.** Admins can do everything. Repo permission `write` includes `read`. A disabled user
  fails authentication.
- **CSRF.** `http.CrossOriginProtection` plus SameSite=Strict.
- **Rate limit.** Failed attempts are limited per IP and per username: burst 10, then 1 every
  6 s. Over the limit the response is 429 with `Retry-After`. The client IP comes from
  `X-Forwarded-For` only when the peer is in `trusted_proxies`.
- **First start.** On first start with no users, the server creates `admin` with a random
  password and prints it once to stderr, outside the JSON log stream.

## Debian indexing pipeline

1. **Upload.** The upload is spooled while MD5, SHA1 and SHA256 are computed. The `control.tar.*`
   is parsed (capped at 16 MiB decompressed, `control` at 1 MiB); `data.tar` is never unpacked.
2. **Validation.** `Package`, `Version` and `Architecture` are validated; the architecture must be
   `all` or one of the repo's `architectures` (default `amd64, arm64`). The debian plugin validates
   the repo config: a non-empty list, each name matching `^[a-z0-9-]+$`, no `all`, no duplicates.
   Other config keys are kept.
3. **Commit.** One transaction writes `assets` + `deb_packages` + `deb_dirty`. An `all` package
   marks one row per architecture.
4. **Debounce.** The indexer debounces per (repo, dist) using `deb.debounce` and `deb.max_delay`.
   On startup it recovers any rows left in `deb_dirty`.
5. **Package indexes.** For each dirty (component, arch), rows are streamed through the unique
   index into `Packages` + `Packages.gz`. The writes happen in order: `by-hash/SHA256/*` files,
   then the named files, then `deb_index_files`.
6. **Release.** `Release` is rebuilt from `deb_index_files` (`Acquire-By-Hash: yes`).
   `Release.gpg` and `InRelease` are written **last**.
7. **Dirty rows.** A dirty row is deleted only if it was not marked again during the rebuild.
8. **Architecture change.** A repo PATCH that changes `architectures` deletes the
   `deb_index_files` rows, `index` assets and blobs of removed architectures, and their pending
   rows. It marks the added architectures for rebuild, so each `Release` is rebuilt. A rebuild
   skips a pending unit whose architecture is no longer configured. Old `by-hash/` files stay.

## CLI

| Command | Does |
|---|---|
| `telesphorepo [serve]` | Runs the server from the boot env and DB settings |
| `telesphorepo dev` | Zero-config local server. Data goes in `./.telesphorepo-dev`. The first run creates `admin` and one anonymous-read demo repo per registered format, named after the format. The banner prints each format's usage snippets |
| `telesphorepo user add NAME [--admin]` | Asks for the password twice on a TTY, or reads one line from a pipe |
| `telesphorepo user passwd NAME` | Resets the password and signs out the user's sessions. Refused for SSO users |
| `telesphorepo keygen [FILE]` | Writes a new Ed25519 key (default `./signing.asc`; never overwrites) and prints the fingerprint |
| `telesphorepo version` / `help [cmd]` | |

Two hidden commands support testing and do not appear in the overview. `dbcheck` applies every
migration track to a temporary database and runs the plan guard. `seed N` fills the database in
`TR_DATA_DIR`. For every format that implements `format.Seeder`, it creates the repos
`seed-<format>-00` to `seed-<format>-09` (anonymous read, default config) and splits N rows
evenly over all of them in transactions of 10,000. Each format inserts its own rows: maven
and raw insert asset rows, debian inserts one package and its asset row per unit. Then it runs
`ANALYZE`. It is safe to run again.

## Tooling (`go tool task`, registered in `go.mod`)

| Command | Does |
|---|---|
| `go tool task dev` | Checks Go, bun/npm and Node (version in `.node-version`). Runs the backend on :8080 and rebuilds it on Go or migration changes. Runs Vite with HMR on **:5173**, proxying every path except the UI's own files to the backend |
| `go tool task build [image]` | Builds the UI, then `bin/telesphorepo` (`CGO_ENABLED=0`, version from git). `image` also builds the container image `telesphorepo:local` |
| `go tool task preview` | Builds, then runs `bin/telesphorepo dev` on :8080 |
| `go tool task test [go test args]` | Runs `go test -tags e2e ./e2e/... -count=1` |
| `go tool task test scale` | Runs `TestScale` (`-tags scale`, 60 min timeout) |
| `go tool task gen` | Runs `go tool sqlc generate` for every `internal/**/sqlc.yaml` |
| `go tool task doctor` | Shows the detected toolchain and container engine |

The web lockfile is `web/bun.lock`. bun is preferred and npm is the fallback. The Docker image is
distroless static, runs as uid 65532, and has `/data` as a volume.

## Testing

The suite is E2E only, with no unit tests and no mocks. `e2e/` builds the real image (skip the
build with `E2E_SKIP_BUILD=1`, or override the image with `E2E_IMAGE`). It runs real `mvn`,
`gradle` and `apt` clients in containers against the server. Each test runs on `fs` and on
SeaweedFS S3 (`E2E_SEAWEEDFS_IMAGE`). The engine
comes from `internal/containerengine`; `TR_ENGINE=docker|podman` forces one. `TestOIDC` runs a
Keycloak container (`E2E_KEYCLOAK_IMAGE`, realm `e2e/testdata/keycloak/realm-e2e.json`) and
drives the browser flow with a cookie-jar HTTP client. `TestRaw` uses a curl container.
`TestAPIFormats` covers `/api/formats`, `/status` and `/usage`. `TestQueryPlans` runs `dbcheck`
inside a server container.

`TestScale` (tag `scale`, `go tool task test scale`) seeds 100k and then 1M assets with `seed N`
on a named volume. It starts a server on the volume and measures the p95 latency over 400 runs of
each operation: Maven GET with `If-None-Match`, an asset list walk, a rare search, a common
search and token auth (`/api/me`). An operation fails when its p95 at 1M exceeds
`max(2 x p95 at 100k, p95 at 100k + 2 ms)`.

## Not yet built

See the milestones in the design doc (§17).

| Item | Milestone |
|---|---|
| `telesphorepo move` (copy blobs between backends), `import nexus` | M3 (§11, §12) |
| Full admin UI polish, deleting assets and deb packages, blob/by-hash GC | M4 |
| Publisher device keys, 2FA, short-lived tokens | Future (§7.6) |
| Restart-persistence e2e for settings (harness cannot restart a server yet) | open |
