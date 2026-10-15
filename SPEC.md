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
| `cmd/telesphorepo` | CLI: `serve` (default), `dev`, `user add/passwd/reset-2fa`, `version`, `help`; hidden `dbcheck` and `seed` |
| `internal/server` | Wires everything together: logger, DB, settings, live storage, services, bootstrap admin, mux, access log |
| `internal/config` | Boot env (`LoadBoot`) and the effective `Config` struct; services read it through `config.Provider` |
| `internal/settings` | Settings registry, AES-256-GCM secrets, master key, atomic snapshot, env overrides, subscribers |
| `internal/db` | SQLite (modernc, no cgo), WAL, separate writer/reader pools, per-component migrations (`Migrate`), `PRAGMA optimize`, the plan guard (`CheckPlans`) |
| `internal/db/dbq` | sqlc-generated core queries (`internal/db/queries/*.sql`) plus hand-written FTS queries in `extra.go` |
| `internal/storage` | `Store` interface; `fs`, `s3`, and `Live`, a hot-swappable wrapper with `Open`, `SameBackend` and `Probe` |
| `internal/assets` | The `assets` table: spooled uploads, point lookups, downloads (stream or presigned redirect), listing, FTS search |
| `internal/move` | Storage move: copies every blob to a new backend in the background behind a `storage.Mirror` |
| `internal/nexus` | Nexus 3 REST client, sources, mapping, the sync scheduler and runner, and cut-over. Names no format; it calls each plugin's `format.Importer` |
| `internal/auth` | Users (kinds `local`/`oidc`), argon2id passwords, Basic-auth cache, API tokens, sessions, rate limiting, permission checks |
| `internal/oidc` | OIDC admin sign-on: `/api/auth/oidc/login` and `/callback` (go-oidc + x/oauth2, PKCE S256) |
| `internal/publish` | Publish sessions: device enrollment, nonces, trust policies, the CI token exchange, `Verify` for `trs_` tokens and the cleanup job |
| `internal/repos` | Repos and their per-format JSON config. A `Validator` (from `internal/format`) checks the config |
| `internal/format` | The plugin contract: `Format` and the optional interfaces, `Host`, and the registry (`Register`, `All`, `Get`, `ValidateRepoConfig`) |
| `internal/formats` | `all.go`: the one list of built-in formats. The only file that names them |
| `internal/maven` | Format `maven`: `/maven/{repo}/...` GET/HEAD/PUT and the overwrite rules |
| `internal/debian` | Format `debian`: `.deb` upload/parse, durable dirty queue, debounced indexer, `/debian/{repo}/key.asc`. Owns its tables, queries (`queries/`, `dbq/`), `deb.*` settings and architecture config |
| `internal/raw` | Format `raw`: plain files at `/raw/{repo}/{path...}` with an `overwrite` policy |
| `internal/signing` | OpenPGP: key `Generate` and `Parse`, the `Keys` service (create, import, delete, one signer per key), clearsign/detach-sign (ProtonMail go-crypto). See **Signing keys** |
| `internal/files` | File tree, deletes (files, folders, repos) and manual uploads. Calls the plugin interfaces `DeleteObserver` and `ManualUploader` |
| `internal/audit` | The audit log: `Record`, the list query and the retention job |
| `internal/netrules` | Client IP resolution behind trusted proxies, the IP lists (presets and custom lists) and the per-repo IP rules. See **Network rules** |
| `internal/api` | JSON API under `/api/`, wrapped in `http.CrossOriginProtection` |
| `internal/httpx` | JSON helpers, error shape, `Limit` (default 50, max 200) |
| `internal/containerengine` | Docker/Podman detection for tooling and e2e; not linked into the server |
| `web/` | Svelte 5 + Vite UI (Tailwind CSS v4, shadcn-svelte Maia style), embedded via `web/embed.go` (`go:embed all:dist`). See **Web UI** |
| `tools/task` | `go tool task`: dev, build, preview, test, gen and doctor |
| `e2e/` | End-to-end suite in four zones (tags `e2e`, `import`, `scale`, `clients`) using testcontainers |

## Data model (SQLite)

Core tables are in `internal/db/migrations/0001_init.sql`. Each format plugin that stores data has
its own track (for debian: `internal/debian/migrations/`). `schema_migrations` holds one row per
track (`component`, `version`). `db.Migrate` applies the files `NNNN_*.sql` of a track in order.
It refuses duplicate version numbers and a database newer than the binary. It also stores the SHA-256 of every applied file in `schema_checksums`. Until the first release the schema files change in place, so a file whose checksum differs (or a database without checksums) gives `db.ErrSchemaChanged`: `telesphorepo dev` deletes and recreates its own data dir, and `serve` refuses to start and names the file. Core runs first, then
each plugin in registration order. All tables are `STRICT`. Tables with a natural key are also
`WITHOUT ROWID`. Every foreign-key child column has an index. SQLite runs `PRAGMA optimize` at
close and every 6 hours.

| Table | Key / indexes | Notes |
|---|---|---|
| `users` | PK `id`, UNIQUE `username`, UNIQUE `users_oidc (oidc_issuer, oidc_subject)` where `oidc_subject` is set | argon2id PHC hash (`!` for SSO users, never verifies), `is_admin`, `disabled`, `password_changed_at`, `kind` (`local`/`oidc`), `email` |
| `api_tokens` | UNIQUE `public_id`, UNIQUE `(user_id, name)` | `secret_hash` = SHA-256(secret); optional `expires_at`; `last_used_at` written at most once per minute. These are **read tokens**: they never publish |
| `sessions` | PK `id_hash` (WITHOUT ROWID), idx `expires_at`, idx `user_id` | `id_hash` = SHA-256(cookie value) |
| `repos` | UNIQUE `name`, partial idx `repos_anonymous (id) WHERE anonymous_read = 1` | `format` is any registered format name (no CHECK); `description` (at most 500 characters); `anonymous_read`; `config` JSON; `locked_by` (NULL, or the text that a write answers with 409, set while the repo mirrors a Nexus source); the counters `file_count`, `total_size` and `last_published_at` (unix time, 0 for never) |
| `repo_permissions` | PK `(repo_id, user_id)`, idx `user_id` | `level` is `read` or `write` |
| `assets` | UNIQUE `(repo_id, path)`, idx `uploaded_by` | Every stored file; `kind` is `artifact`, `metadata` or `index`. Triggers keep the repo counters in the transaction of each write: an insert adds 1 and its size, a replace adds the size change, a delete subtracts. Every asset row counts (artifacts, metadata, checksums and Debian index files). `last_published_at` is the newest `updated_at` of an insert or replace that is not an `index` row; a delete never lowers it |
| `assets_fts` | FTS5 trigram on `path` | Kept in sync by triggers; `index` rows are not indexed |
| `settings` | PK `key` (WITHOUT ROWID), idx `updated_by` | JSON value, or base64(nonce‖AES-GCM ciphertext) when `secret=1`; `updated_by` |
| `schema_migrations` | PK `component` (WITHOUT ROWID) | Applied version per migration track |
| `storage_moves` | PK `id`, UNIQUE partial `storage_moves_copying (state) WHERE state = 'copying'`, idx `started_by` | `target` is the sealed JSON of the target storage settings (AAD `storage_moves:<id>`); `state` `copying`/`done`/`failed`/`cancelled`; `cursor` (last asset id done), `total`, `copied`, `skipped`, `failed`, `error` |
| `storage_move_failures` | PK `(move_id, asset_id)` (WITHOUT ROWID) | `key` and `reason` of each blob the copier could not copy |
| `nexus_sources` | PK `id`, UNIQUE `name`, idx `created_by`, partial idx `nexus_sources_due (last_sync_at) WHERE state = 'mirroring'` | `base_url`; `credentials` is the sealed JSON `{username, password}` (AAD `nexus_sources:<id>`), emptied by cut-over; `state` `mirroring`/`cut_over`; `last_sync_at` (unix time when the last sync ended, 0 for never); `last_error`; `cutover_sync_id` and `cutover_failed` (the cut-over sync and its failed file count, 0 before cut-over) |
| `nexus_source_repos` | PK `(source_id, source_repo)` (WITHOUT ROWID), idx `target_repo_id` | The mapping: `source_format` (Nexus format id) and the target repo of each Nexus repo. A target mirrors at most one source |
| `nexus_syncs` | PK `id`, UNIQUE partial `nexus_syncs_running (source_id) WHERE state = 'running'`, idx `source_id`, idx `started_by` | `state` `running`/`done`/`failed`/`cancelled`; `cutover` (0/1); `started_by` is NULL for the scheduler; `started_at`, `finished_at`, `error` |
| `nexus_sync_repos` | PK `(sync_id, source_repo)` (WITHOUT ROWID), idx `target_repo_id` | Copy of the mapping at sync start, with `continuation` (token of the next Nexus page), `imported`/`skipped`/`ignored`/`failed` and `state` `pending`/`running`/`done`/`failed`/`cancelled` |
| `nexus_sync_failures` | PK `id`, UNIQUE `(sync_id, source_repo, path)`, idx `sync_id` | One row per source file that a sync did not import |
| `user_totp` | PK `id`, idx `user_id` | `secret` is sealed (AAD `user_totp:<id>`); `last_step` is the last accepted 30 s step of this factor (0 for none) |
| `user_passkeys` | PK `id`, UNIQUE `credential_id`, idx `user_id` | `public_key`, `sign_count`, `credential` (JSON of the `webauthn.Credential`: flags, transports, AAGUID), `last_used_at` |
| `user_recovery_codes` | PK `(user_id, code_hash)` (WITHOUT ROWID) | `code_hash` is argon2id PHC; `used_at` is set when the code is used |
| `devices` | PK `id`, UNIQUE `public_key`, idx `user_id` | `kind` `gradle`/`maven`; `public_key` is the raw Ed25519 key; `expires_at`, `last_used_at`, `revoked_at` |
| `device_enrollments` | PK `device_code_hash` (WITHOUT ROWID), UNIQUE `user_code`, idx `expires_at`, idx `approved_by`, idx `device_id` | `polled_at` rate-limits the poll; `approved_by` and `device_id` are set on approval; a row is taken once by the poll |
| `publish_trusts` | PK `id`, UNIQUE `name`, partial idx `publish_trusts_issuer (issuer) WHERE disabled = 0`, idx `created_by` | `claims` is JSON `{claim: [allowed values]}`; `jwks` is `''` when the server fetches the issuer's keys; `audience` `''` means `base_url` |
| `publish_trust_repos` | PK `(trust_id, repo_id)` (WITHOUT ROWID), idx `repo_id` | The repos a policy may publish to |
| `publish_sessions` | PK `id`, UNIQUE `public_id`, idx `expires_at`, `repo_id`, `user_id`, `device_id`, `trust_id` | `secret_hash` = SHA-256(secret); `via` `device`/`oidc`/`manual`; `subject`; `closed_at`. `repo_id` cascades; `user_id`, `device_id` and `trust_id` are SET NULL |
| `auth_challenges` | PK `id_hash` (WITHOUT ROWID), idx `expires_at`, `user_id`, `device_id` | Short-lived single-use state: `kind` is `login`, `totp_setup`, `webauthn_register`, `webauthn_assert` or `publish_nonce`; `data` holds the payload |
| `audit_events` | PK `id`, idx `at`, idx `(repo_id, id)`, idx `(action, id)`, idx `actor_user_id` | `at` unix time; `actor` is a username, `cli` or the attempted username of a failed sign-in; `actor_user_id` and `repo_id` are SET NULL; `target`; `detail` is JSON and never holds a secret value; `ip` is the client address. This table is the only place that stores or shows an IP address. See **Audit log** |
| `publish_oidc_seen` | PK `id_hash` (WITHOUT ROWID), idx `expires_at` | SHA-256 of `<issuer>\n<jti>`, or of the whole token when it has no `jti`. A second insert means a replay |
| `ip_lists` | PK `id`, UNIQUE `name` | An IP list. `kind` is `preset` (the server fetches it) or `custom` (an admin edits it). `title`, `description`, `ranges` (JSON array of CIDRs), `source` (the fetch URLs of a preset), `fetched_at` (unix time), `error` (the last fetch failure, `''` after a success), `created_at` |
| `repo_ip_rules` | PK `(repo_id, scope, kind, value)` (WITHOUT ROWID), partial idx `repo_ip_rules_list (value, repo_id) WHERE kind = 'list'`; `repo_id` cascades with the repo | One allow-list entry. `scope` is `read` or `publish`, `kind` is `cidr` or `list` (then `value` is the name of an IP list), `note` is free text. A scope without rows is unrestricted |
| `signing_keys` | PK `id`, UNIQUE `name`; idx `repos_signing_key` on `repos(json_extract(config, '$.signing_key'))` | An OpenPGP signing key: `fingerprint`, armored `public_key`, `private_key` (armored, no passphrase, sealed with the master key, AAD `signing_keys:<id>`), `created_at`. A repo config names its key in `signing_key`. Two triggers on `repos` refuse a config that names a key that does not exist |

Tables of the `debian` track:

| Table | Key / indexes | Notes |
|---|---|---|
| `deb_packages` | UNIQUE `(repo_id, distribution, component, architecture, package, version)`, idx `asset_id`, idx `deb_packages_sha256 (repo_id, distribution, component, sha256)` | Also the streaming index for `Packages`. The SHA-256 index finds a package an import already holds |
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
also fails. Core allows `ListSettings`, `HasUsers`, `ListPermissions` and `CountAssets` (the
total of a storage move, once per move). Debian allows
`ListAllDirtyDists`, `MaxDirtyMark` and `ListAllIndexFiles` (startup and re-sign work).
`TestQueryPlans` runs the guard through the hidden `dbcheck` command.

## Storage layout

| Location | Contents |
|---|---|
| `$TR_DATA_DIR/telesphorepo.db` | SQLite database (WAL) |
| `$TR_DATA_DIR/master.key` | Master key (base64, mode 0600). Generated on first start unless `TR_MASTER_KEY` is set |
| `$TR_DATA_DIR/tmp/` | Upload spool, kept on the data volume rather than tmpfs |
| `$TR_DATA_DIR/blobs/` | FS blob root (default for the `fs.root` setting) |

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

Setting types are `string`, `bool`, `size`, `duration` and `list`. A `size` is stored as an integer
number of bytes. Input (API PATCH, env) is a number with an optional unit: `B`, `KiB`, `MiB`,
`GiB`, `TiB` (binary) or `KB`, `MB`, `GB`, `TB` (decimal), case-insensitive, with optional space
and decimals (`512MiB`, `1.5 GiB`, `500MB`). A bare number is bytes. A `duration` is stored in Go
syntax (`15m`, `2160h`). Input also accepts `15 minutes`, `90 days`, `365d`, `1h30m`; the units
are `ns`, `us`, `ms`, `s`, `m`, `h`, `d` and `w`, in short or long form. `GET /api/settings` gives
each size or duration item `display` (`2 GiB`, `90 days`, `1 hour 30 minutes`) and
`default_display`. A size item shows the largest binary unit that gives a whole number, else one
decimal.

A format can register its own settings group (interface `format.Configurable`). The group name is
the format name and the label is the format title. Plugin settings are registered once at startup,
before `settings.Load`.

| Group | Keys |
|---|---|
| general | `base_url`, `log.level`, `max_upload_size` (`2GiB`), `max_json_size` (`1MiB`), `nexus.sync_interval` (`15m`, minimum `1m`) |
| storage | `storage` (`fs`/`s3`, with `r2` as an alias for `s3`), `fs.root`, `s3.endpoint`, `s3.region`, `s3.bucket`, `s3.access_key_id`, `s3.secret_access_key`*, `s3.prefix`, `s3.path_style`, `s3.redirect`, `presign_ttl` |
| security | `cookie.secure`, `session_ttl`, `trusted_proxies`, `audit.retention` (`8760h`, which is 365 days) |
| publishing | `publish.session_ttl` (`15m`, maximum `1h`), `publish.device_ttl` (`2160h`, which is 90 days) |
| sso | `oidc.issuer`, `oidc.client_id`, `oidc.client_secret`*, `oidc.redirect_url`, `oidc.scopes` (`openid profile email`), `oidc.admin_claim` (`groups`; dotted paths allowed), `oidc.admin_values`, `oidc.admin_emails`, `oidc.session_ttl` (`12h`), `admin_password_login` (`true`) |
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
  When the new settings address another location and assets exist, the server moves the blobs
  first (see **Storage move**). A change of only credentials, region, path style or redirect,
  or a change on a server without assets, is saved and swapped at once.
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
  `TR_E2E_PRESET_BASE_URL` is a test variable (see **Network rules**).
- **Signing keys.** Debian repos sign their indexes with a named signing key from the table
  `signing_keys` (see **Signing keys**). No setting holds a key.
- **Lost master key.** Without the master key the secret settings cannot be decrypted and must be
  entered again.

## HTTP surface

Each format mounts its own routes through `Format.Register(mux, Host)`. The core names no format.
Format routes (package clients use HTTP Basic). A **read** takes a password, or any username
with a read token as the password. A GET or HEAD read also takes the UI session cookie, so a
signed-in browser downloads from a private repo without a Basic prompt. A **write** (PUT or upload) takes only a publish session token
(`trs_...`) as the password (see **Publishing**); the session cookie never writes. In the table, "write" means this rule:

| Route | Auth | Behaviour |
|---|---|---|
| `GET /healthz` | none | `ok` when the DB answers |
| `GET, HEAD, PUT /maven/{repo}/{path...}` | read / write | PUT: 201 new, 200 overwrite or identical retry, 409 changed release, 413 over `max_upload_size`. Mutable paths are `maven-metadata.xml*` and anything under a `-SNAPSHOT` dir. GET on S3 with `s3.redirect` answers 302 to a presigned URL. HEAD is never redirected |
| `PUT, POST /debian/{repo}/upload/{dist}/{component}` | write | Raw `.deb` body. 201 `{package,version,architecture,filename,sha256}`, 200 identical, 409 conflict |
| `GET, HEAD /debian/{repo}/{path...}` | read | `dists/...` and `pool/...` served from `assets` |
| `GET, HEAD, PUT /raw/{repo}/{path...}` | read / write | PUT: 201 new path; 200 identical bytes (any policy) or a replace when `overwrite` is `allow`; 409 different bytes when `overwrite` is `deny` (default); 413 over `max_upload_size`. Repo config: `{"overwrite":"deny"}` or `{"overwrite":"allow"}`. S3 redirect as for Maven |
| `GET, HEAD /debian/{repo}/key.asc` | read | The armored public signing key of the repo (`signing_key` of its config). Same read gate as the repo's other files |
| `/` | none | Embedded UI, hash-routed |

The repo decision table: anonymous read is allowed when `anonymous_read` is on. Otherwise no
credentials or bad credentials get 401 with a Basic challenge, valid credentials without
permission get 403, and an unknown repo gets 404 (401 for anonymous callers, so repo names don't
leak). A write to a repo with `locked_by` set gets 409 `{"error": <locked_by>}` after the
permission check. A mirrored repo is read only until cut-over. A write without an open publish
session for that repo gets 401 with a Basic challenge and
`{"error":"publishing needs a publish session: <base_url>/#/settings/tokens"}`. A device or manual
session writes only while its user still has `write` on the repo (else 403).

A 401 carries `WWW-Authenticate` only on a format route, so package clients get their prompt. It
never carries it for a path under `/api`, nor for a request with the header `X-Telesphorepo-UI: 1`
(the web UI sends it on every request). So a browser never opens its password dialog for the UI.

JSON API (`/api/`). Auth is a session cookie or HTTP Basic. Every route that changes state
(POST, PUT, PATCH, DELETE) and every admin route (GET too) needs the UI session cookie. HTTP
Basic with a password or a read token reaches only the GET routes that are not admin routes;
elsewhere it gets 401 `{"error":"sign in through the web UI for this action"}`, as does a request
with no credential. The routes that take no credential or their own token are the exceptions
(sign-in, `/api/auth/config`, `/api/formats`, and the publish routes marked "none" or "token" in
**Publishing**). A signed-in user without the right gets 403. A publish session token (`trs_...`,
as the Basic password or as `Authorization: Bearer trs_...`) reaches only `/api/publish/...`,
`POST /api/repos/{name}/upload` and `POST /api/repos/{name}/files/delete`, and only for its own
repo. Every other `/api` route answers it with 403. Errors are `{"error": "..."}`, plus
`fields` for settings validation. Bodies are capped at `max_json_size`. Lists are
`{items, next}` with `?after=&limit=`.

**Bulk actions.** A write route that changes a set takes a list, and one item is a list of one.
So each change has one route. A list holds 1 to 1000 items (400 outside that), and
`max_json_size` applies too. The server removes repeated items. Each route checks every item
before it changes any: an unknown name gives 404 with the name (`"repository x does not exist"`,
`"user x does not exist"`), and nothing changes. The changes to rows apply in one transaction, so
a failure leaves nothing half done. Each changed item gets its own audit event; an item that
already had the value gets none. The routes are `PATCH /api/repos`, `POST /api/repos/delete`,
`POST /api/repos/{name}/files/delete`, `PATCH /api/users`, `PUT /api/permissions` and
`POST /api/permissions/delete`. A repo delete removes blobs, so it is the one route that is not
one transaction (see the route).

| Method & path | Auth | Notes |
|---|---|---|
| `POST /api/auth/login` · `POST /api/auth/logout` | none · session | Login `{username, password}`. An admin with a second factor gets `{second_factor: true, methods: [...]}` and the cookie `tr_login`; an admin without one gets the User plus `second_factor_required: true` and a setup-only session. A non-admin user gets the User and a full session after the password alone (see **Auth model**). 401 for SSO users; 403 for a local admin when `admin_password_login=false`. Logout ends the local session only |
| `POST /api/auth/login/passkey` | `tr_login` cookie | Returns `{publicKey: {...}}`, the WebAuthn assertion options for the pending sign-in |
| `POST /api/auth/login/second` | `tr_login` cookie | `{totp_code}`, `{passkey: <assertion>}` or `{recovery_code}`. 200 sets `tr_session` and returns the User with `second_factor_required: false`. 401 for a wrong proof (the sign-in stays pending) or an expired sign-in; 429 with `Retry-After` after too many failures |
| `GET /api/security` | UI session | `{totp: [{id, name, created_at}], passkeys: [{id, name, created_at, last_used_at}], recovery_codes: <unused count>, second_factor: bool}` |
| `POST /api/security/totp/begin` · `.../totp/confirm` · `DELETE .../totp/{id}` | UI session | `{name}` returns `{setup_id, secret, otpauth_url}` (the secret is shown once; the setup lasts 10 min). `{setup_id, code}` returns 201 `{id, name, created_at}`. Delete is 204 |
| `POST /api/security/passkeys/begin` · `.../passkeys/finish` · `.../passkeys/assert` · `DELETE .../passkeys/{id}` | UI session | `begin` returns `{publicKey}` creation options. `finish` `{name, credential}` returns 201 `{id, name, created_at, last_used_at}`. `assert` returns `{publicKey}` options for a fresh check before an approval or a manual session. 409 when `base_url` is empty |
| `POST /api/security/recovery-codes` | UI session | 201 `{codes: [10 x "abcd-efgh-ijkl-mnop"]}`; replaces the old set. Shown once |
| `GET /api/auth/config` | none | `{password_login: true, oidc, admin_password_login, second_factor_optional}`. `second_factor_optional` is true only under `telesphorepo dev` |
| `GET /api/auth/oidc/login` | none | 404 when SSO is off, 503 when discovery fails. Stores state/nonce/PKCE verifier in memory (10 min, single use, keyed by SHA-256(state)), sets `tr_oidc` (HttpOnly, SameSite=Lax, Path `/api/auth/oidc`, 600 s), 302 to the IdP |
| `GET /api/auth/oidc/callback` | none | 400 for missing/mismatched/unknown/reused/expired state, state from before an issuer or client change, or missing `code`; 503 on discovery failure. Verifies the ID token (signature, issuer, audience, expiry, nonce) and the admin rule. Success: upsert user, session with `oidc.session_ttl`, 302 `/`. Failure: 302 `/#/login?error=` `not_authorized` \| `username_conflict` \| `invalid_username` \| `account_disabled` \| `sso_failed` |
| `GET /api/me` | any credential | Current user. User JSON includes `kind` (`local`/`oidc`), `email` (`""` when unset) and `second_factor_required` (true only for a setup-only UI session) |
| `GET /api/tokens` · `POST /api/tokens`, `DELETE /api/tokens/{id}` | any credential · UI session | **Read tokens.** The token is shown once. A token cannot mint tokens. POST is 403 for SSO users |
| `GET/POST /api/users`, `GET/PATCH /api/users/{username}` | admin (a UI session may change its own password) | The user list is paginated. `GET /api/users/{username}` returns one account (User JSON), 404 when no account has the name. PATCH takes `{password?, is_admin?}` |
| `PATCH /api/users` | admin | `{usernames: [...], disabled: bool}`. The one way to disable or enable people. 400 `You cannot disable your own account.` when the list holds the caller and `disabled` is true. A disabled person's sessions end, and their password and read tokens stop working. One `user.update` event per person whose value changed. 200 `{changed: n}` |
| `GET /api/formats` | none | `[{name, title, default_config, settings_group?, icon?, kind_icons?}]` for every registered format. `icon` is the format's own one-color SVG, inline. `kind_icons` maps an artifact kind to an SVG when it differs from the format (Maven: `gradle-plugin`). A format gets both by implementing `format.Iconic` |
| `GET /api/repos`, `GET /api/repos/{name}` · `POST /api/repos`, `PATCH /api/repos/{name}` | any/anon · admin | The list shows only repos the caller can see. Repo JSON has `file_count`, `total_size` and `last_published_at` (null before the first publish), read from the repo row (see `assets` in **Data model**). `format` must be a registered name. `description` (POST and PATCH) has at most 500 characters and no control characters except newline and tab. `config` must be a JSON object that the format validates (400 otherwise; an unknown format lists the available ones). An empty config becomes the format default. Repo JSON has `locked_by` (`""` when the repo is not locked). `PATCH /api/repos/{name}` takes `{description?, config?}`; a PATCH with `config` calls the format's `RepoConfigChanged` |
| `PATCH /api/repos` | admin | `{names: [...], anonymous_read: bool}`. The one way to make repos public or private. One `repo.update` event per repo whose value changed. 200 `{changed: n}` |
| `POST /api/repos/delete` | admin | `{names: [...]}`. Checks that every repo exists (404) and that none is locked (409 `<name>: <lock text>`); then nothing is deleted. Then it deletes the repos one after another with their files and blobs (see **Files, uploads and deletes**). The first failure stops the rest: 500 `{deleted: [names done], error}`. One `repo.delete` event per repo. 200 `{deleted: [names], files: n}` |
| `GET /api/repos/{name}/permissions` | admin | `[{username, level: read\|write}]` |
| `PUT /api/permissions` | admin | `{users: [...], repos: [...], level: "read"\|"write"}`. Sets the level of every pair of user and repo, at most 1000 pairs. One `permission.set` event per pair whose level changed. 200 `{changed: n}` |
| `POST /api/permissions/delete` | admin | `{users: [...], repos: [...]}`. Removes the level of every pair, at most 1000 pairs. One `permission.delete` event per pair that had one. 200 `{changed: n}` |
| `GET /api/repos/{name}/assets?prefix=&after=&limit=` | read | Ordered by path; the prefix is a range scan |
| `GET /api/repos/{name}/tree?path=&after=&limit=` | read | `{items: [{name, type: "dir"\|"file", path, size?, sha256?, updated_at?, uploaded_by?}], next}`. `path` is a folder (`""` or ending in `/`). A folder item's `path` and the `next` cursor end in `/`. `uploaded_by` is a username |
| `POST /api/repos/{name}/files/delete` | publish session of the repo, or admin UI session | `{paths?: [...], prefixes?: [...]}`: files and folders, 1 to 1000 items together. 400 with the first bad path or prefix; then nothing is deleted. 200 `{deleted: n}`, the number of files. A file that is gone already counts as done; an empty folder adds 0. 409 on a locked repo. 401 `publishing needs a publish session` for any other credential. One `delete` event per file and per folder that lost files |
| `POST /api/repos/{name}/upload` | publish session of the repo | Multipart. A file part may carry a relative path as its `filename` (folder upload). 201 `{files: [{path, status: "created"\|"replaced"\|"unchanged", size, sha256}]}`. 400 or 409 for a client error of the plugin |
| `POST /api/repos/{name}/upload/scan` | publish session of the repo | Same multipart body as the upload. Stores nothing. 200 `{found: [{kind: "maven"\|"debian"\|"file", title, files: [paths], problems: [text]}], ignored: [paths]}`. See **Files, uploads and deletes** |
| `GET, PUT /api/repos/{name}/ip-rules` | admin | `{read: [Rule], publish: [Rule]}` with `Rule` = `{kind: "cidr"\|"list", value, note}`. PUT replaces both lists and returns them in canonical form. 400 for an invalid rule or a `list` that does not exist. See **Network rules** |
| `GET /api/ip-lists` | admin | `[IPList]` without `ranges`: presets first, then by name. `IPList` = `{name, kind: "preset"\|"custom", title, description, count, ranges?, source, fetched_at\|null, error, created_at, used_by: {repos: [{repo, scope}], trusted_proxies}}` |
| `GET /api/ip-lists/{name}` | admin | The `IPList` with `ranges`. 404 when unknown |
| `POST /api/ip-lists` | admin | `{name, title?, description?, ranges?}`. 201 `IPList`. 400 for invalid input, 409 when the name is taken (preset names too) |
| `PUT /api/ip-lists/{name}` | admin | `{name?, title?, description?, ranges?}`; a missing field keeps its value. 200 `IPList`. 400 for a preset, 404 when unknown, 409 when the new name is taken or the rename would change `trusted_proxies` that the environment sets |
| `DELETE /api/ip-lists/{name}` | admin | 204. 409 `{error, used_by}` while in use, 400 for a preset |
| `POST /api/ip-lists/{name}/refresh` | admin | Fetches the preset now and returns the `IPList`. 400 for a custom list, 404 when unknown, 502 `{error}` when the fetch fails (the last good list stays) |
| `POST /api/ip-ranges/parse` | admin | `{text}` (at most 1 MiB). Stores nothing. 200 `{ranges: [CIDR], invalid: [{line, text, reason}], duplicates: n}`. 400 for longer text or more than 100,000 ranges. See **IP lists** |
| `GET /api/signing-keys` | admin | `[{name, fingerprint, public_key, created_at, error?, repos: [name]}]` by name |
| `POST /api/signing-keys` | admin | `{name}` creates an Ed25519 key. `{name, armored, passphrase?}` imports a key. 201 with the key. 400 for a bad name or key, 409 when the name is taken |
| `GET /api/signing-keys/{name}` | admin | The key with `repos`. 404 when unknown |
| `DELETE /api/signing-keys/{name}` | admin | 204. 409 `{error, repos}` while repos use the key |
| `GET /api/signing-keys/{name}/public.asc` | admin | `application/pgp-keys`, saved as `<name>.asc` |
| `GET /api/network/proxy-candidates` | admin | `[{address, last_seen, count, header}]`, newest first: the untrusted TCP peers that sent a forwarded header. See **Network rules** |
| `GET /api/audit?after=&limit=&action=&repo=` | admin | `{items: [{id, at, actor, actor_user_id\|null, action, repo, target, detail, ip}], next}`, newest first. `repo` is a repo name |
| `GET /api/search?q=&repo=&after=&limit=` | any/anon | FTS5 trigram, `q` ≥ 3 chars, readable repos only |
| `GET /api/repos/{name}/status` | read | The format's `Status` result, or `{}`. Debian: `{pending}`, the count of dirty rows |
| `GET /api/repos/{name}/usage` | read | `{base_url, snippets:[{title, lang, code}]}` from the format's `Usage`. `base_url` is the `base_url` setting, else the request scheme and host |
| `GET /api/settings` | admin | `{groups:[{name, label, settings:[Item]}]}`. An Item has key, env, group, label, help, type, options, secret, value, default, source (`db`/`env`/`default`), locked |
| `PATCH /api/settings` | admin | `{key: value}`. `null` resets a key; `""` for a secret means unchanged. Returns 400 `{error, fields}`, or 409 when an env-locked key gets a different value. Backend changes must pass the probe; SSO changes must pass discovery. A backend change that starts a storage move saves the other keys of the patch and returns 202 `{move: Move}`; 409 while a move runs |
| `GET /api/storage/moves/current` | admin | The newest Move, running or ended; 404 when there has been none |
| `GET /api/storage/moves/{id}` | admin | `Move` = `{id, state, total, copied, skipped, failed, error, started_at, finished_at, target: {storage, endpoint?, bucket?, prefix?, fs_root?}}`. Never holds secrets |
| `GET /api/storage/moves/{id}/failures?after=&limit=` | admin | `{items: [{asset_id, key, reason}], next}` in asset id order |
| `DELETE /api/storage/moves/{id}` | admin | Cancels the move and returns it; 409 when it has ended |
| `POST /api/nexus/sources` | admin | `{name, base_url, username, password}`; `name` matches the repo name pattern. Checks the connection. 201 `Source`. 400 for invalid input or refused credentials, 409 when the name exists, 502 when Nexus is unreachable |
| `GET /api/nexus/sources?after=<name>&limit=` | admin | `{items: [Source], next}` in name order |
| `GET /api/nexus/sources/{id}` | admin | `SourceDetail`. After cut-over, or when Nexus is unreachable, it lists only the mapped repos and `nexus_error` says why |
| `PATCH /api/nexus/sources/{id}` | admin | `{base_url?, username?, password?}`. Checks the connection. Returns the `Source`. 409 after cut-over |
| `PUT /api/nexus/sources/{id}/repos` | admin | `[{source_repo, target, create}]` replaces the whole mapping. Returns the `SourceDetail`. 400 for an invalid mapping, 409 while a sync runs, after cut-over, or when a target already mirrors another source |
| `POST /api/nexus/sources/{id}/sync` | admin | 202 `Sync` with `repos`. 400 without mapped repos, 409 while a sync runs or after cut-over |
| `POST /api/nexus/sources/{id}/cutover` | admin | 202 `Sync` with `cutover: true`. 409 while a sync runs or after cut-over |
| `GET /api/nexus/sources/{id}/syncs?after=<sync id>&limit=` | admin | `{items: [Sync without repos], next}`, newest first |
| `GET /api/nexus/syncs/{id}` | admin | `Sync` with `repos` |
| `GET /api/nexus/syncs/{id}/failures?after=&limit=` | admin | `{items: [Failure], next}` in failure order |
| `DELETE /api/nexus/syncs/{id}` | admin | Cancels the sync and returns it (200); 409 when it has ended |
| `DELETE /api/nexus/sources/{id}` | admin | 204. 409 while a sync runs or while the source mirrors mapped repos (allowed after cut-over, or without a mapping) |
| `POST /api/settings/storage/test` | admin | Same body as the PATCH, merged over the current values and not saved. Returns `{ok:true}`, or 400 `{ok:false, error}` |
| `POST /api/settings/oidc/test` | admin | Same, for SSO discovery. Returns `{ok:true}`, or 400 `{ok:false, error, fields}` |

"admin" in these tables means the UI session of an admin. "UI session" means the `tr_session`
cookie; an admin session exists only after the second factor (outside dev mode).

## Client setup

The UI shows these snippets per repo (`/usage`), and `telesphorepo dev` prints them. `HOST` is the server URL. For reads, credentials are a user and password, or any username with a
read token as the password. Uploads use a publish session token; the Maven and Gradle clients
(see **Publishing**) get it themselves.

| Client | Setup |
|---|---|
| Maven | URL `HOST/maven/{repo}` in `<distributionManagement>` / `<repositories>`. Reads of a private repo need `~/.m2/settings.xml` `<server>` with the same `<id>`. Deploys use the Telesphorepo extension. Maven 3.8.1+ blocks `http://` on non-localhost hosts |
| Gradle | `maven { url = uri("HOST/maven/{repo}") }` in `publishing.repositories`, and in `pluginManagement.repositories` to consume plugins; add `credentials { ... }` with a read token to read a private repo. The settings plugin adds the publish credentials. `isAllowInsecureProtocol = true` only for plain http |
| apt | Key: `curl -fsSL HOST/debian/{repo}/key.asc -o /etc/apt/keyrings/telesphorepo.asc`. Source: `deb [signed-by=/etc/apt/keyrings/telesphorepo.asc] HOST/debian/{repo} {dist} {component}`. For a private repo add `-u USER:READ_TOKEN` to the `curl` command, and add `/etc/apt/auth.conf.d/telesphorepo.conf` (`machine host login USER password TOKEN`, mode 600) |
| Upload `.deb` | `curl -u publish:SESSION_TOKEN -T x.deb HOST/debian/{repo}/upload/{dist}/{component}`. Indexes rebuild after `deb.debounce`; poll `GET /api/repos/{repo}/status` for `pending: 0` |
| Raw | Upload: `curl -f -u publish:SESSION_TOKEN -T file.tar.gz HOST/raw/{repo}/path/file.tar.gz`. Download: `curl -fL [-u USER:READ_TOKEN] -O HOST/raw/{repo}/path/file.tar.gz` |

## Auth model

| Credential | Storage | Accepted on |
|---|---|---|
| Password | argon2id (64 MiB, t=3, p=2), at most 4 hashes at once; successful Basic checks are cached for 10 min under an HMAC key | Sign-in (`POST /api/auth/login`). As Basic auth: format reads, and the `/api/` GET routes that are not admin routes |
| Read token `trp_<12hex>_<secret>` | SHA-256(secret), looked up by id, constant-time compare | Basic password (any username): format reads, and the `/api/` GET routes that are not admin routes |
| Session `tr_session` | SHA-256(id), absolute `session_ttl` | `/api/`, and format reads (GET and HEAD) for downloads in the browser. The only credential for admin routes and for routes that change state on `/api/`; it never writes to a format route. HttpOnly, SameSite=Strict, Secure per `cookie.secure`. An admin session may also delete files (see **Files, uploads and deletes**) |
| Publish session `trs_<12hex>_<secret>` | SHA-256(secret) in `publish_sessions`, constant-time compare | Basic password (any username) or `Authorization: Bearer` on format routes of its one repo, `/api/publish/...`, and the upload and delete routes of its repo. The only credential that writes to a format route |

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
- **Second factor.** A local user may register several TOTP factors (SHA-1, 6 digits, 30 s, +-1
  step; a step that a factor accepted is refused again), several passkeys (RP ID is the host of
  `base_url`, the one accepted origin is its scheme and host, a clone warning fails the check) and
  10 one-time recovery codes (80 bits each, argon2id-hashed). Recovery codes work only at
  sign-in. Passkeys need `base_url`; without it the routes answer 409. TOTP secrets are sealed
  with the settings key. Failed checks count against the `u:<username>` rate-limit key. SSO
  users have no second factor.
- **Sign-in.** An admin with a factor sends password, then a second step with the cookie
  `tr_login` (HttpOnly, SameSite=Strict, Path `/api/auth/login`, 5 min). The state is a row in
  `auth_challenges`, so it survives a restart. A wrong code keeps the sign-in pending. A
  non-admin user signs in with the password alone, also when the user has a factor.
- **Where a factor is required.** Admin sign-in, device approval and a manual publish session
  (the UI session that publishes or deletes) need a fresh factor. A non-admin user sets up a
  factor when first approving a device or creating a manual session.
- **Setup-only session.** A UI session of an admin with no factor reaches only `/api/me`,
  `/api/auth/logout` and `/api/security...`. Every other `/api` route answers 403
  `{"error":"second_factor_required"}`. The break-glass `admin` follows the same rule. The
  `/api/security` routes need a UI session, so Basic auth and tokens get 401. A password alone
  cannot add a factor and cannot publish.
- **Dev mode.** `telesphorepo dev` sets `server.Options.Dev`, which calls `auth.SetDevMode`. It is
  an in-process flag: not a setting, not an env var, and not reachable from `serve`. In dev mode
  an admin without a factor gets a full session, and a device approval or manual session without a
  factor passes. A factor that exists is still checked. `GET /api/auth/config` reports
  `second_factor_optional: true`, and the dev banner says "2FA not enforced (dev)". The e2e
  suite runs `serve`, so enforcement stays tested.
- **Where each credential works on the JSON API.** A route that changes state (POST, PUT, PATCH,
  DELETE) or an admin route (GET too) needs the UI session. HTTP Basic with a password or a read
  token reaches only the GET routes that are not admin routes: repo list and repo, tree, assets,
  search, status, usage, `/api/me`, own tokens, devices and publish sessions. Anything else gets
  401 `{"error":"sign in through the web UI for this action"}` without a Basic challenge. So a
  stolen admin password cannot change users, permissions, settings or trust policies: an admin
  session needs the second factor. `auth.Identify` refuses Basic on routes that change state, and
  `api.admin` refuses every credential except a UI session. The exceptions are the routes with no
  credential or their own token: sign-in, `/api/auth/config`, `/api/formats`, device enrollment
  and poll, the publish challenge, the device and OIDC session mints, closing a session with its
  token, and the upload and delete routes with a publish session token. Dev mode does not change
  this rule.
- **Lost factors.** `telesphorepo user reset-2fa NAME` removes the TOTP factors, passkeys and
  recovery codes of a user.
- **Roles.** Admins can do everything. Repo permission `write` includes `read`. A disabled user
  fails authentication.
- **CSRF.** `http.CrossOriginProtection` plus SameSite=Strict.
- **Rate limit.** Failed attempts are limited per IP and per username: burst 10, then 1 every
  6 s. Over the limit the response is 429 with `Retry-After`. The client IP is resolved as
  described in **Network rules**.
- **First start.** On first start with no users, the server creates `admin` with a random
  password and prints it once to stderr, outside the JSON log stream.

## Publishing

Every write on a format route needs a **publish session**: a random credential that the server
generates for one repo. It lives at most `publish.session_ttl`. A leaked password or read token
cannot publish. The Nexus sync and the storage move write in-process and do not pass this gate.
The server generates, stores (SHA-256), scopes, expires and revokes every session. A caller only
proves who it is. There are four proofs:

| Who | Proof | Route |
|---|---|---|
| Developer with Gradle or Maven | An enrolled device key (Ed25519) | `POST /api/publish/sessions` |
| CI (GitHub Actions, Jenkins, any OIDC issuer) | The CI identity token and an admin trust policy | `POST /api/publish/sessions/oidc` |
| Anyone, by hand | UI session plus a fresh second factor | `POST /api/publish/sessions/manual` |

Routes (`{items, next}` lists are keyset, `?after=&limit=`):

| Method & path | Auth | Notes |
|---|---|---|
| `POST /api/devices/enroll` | none | `{name, kind: "gradle"\|"maven", public_key}` (`public_key` is the raw 32-byte key, base64url, no padding). Returns `{device_code, user_code: "ABCD-EFGH", verification_url: "<base_url>/#/device", expires_in: 600, interval: 5}`. `verification_url` uses the request URL when `base_url` is empty. Each IP gets a burst of 10, then 1 per 30 s (429) |
| `POST /api/devices/enroll/poll` | none | `{device_code}`. 200 `{device_id}` once approved (single use). 400 `{"error": "authorization_pending" \| "slow_down" \| "expired_token"}`. `slow_down` answers a poll less than 4 s after the last. An unknown or used code gets `expired_token` |
| `GET /api/devices/enroll?user_code=` | UI session or password | `{user_code, name, kind, expires_at}`; 404 for an unknown or expired code |
| `POST /api/devices/approve` | UI session | `{user_code, totp_code \| passkey}`. 201 Device. 401 for a wrong second factor, 404 unknown or expired code, 409 used code, 403 `second_factor_required` |
| `GET /api/devices` | password, read token or UI session | Own devices; an admin sees all devices, with `username`. Newest first, `{items, next}` |
| `DELETE /api/devices/{id}` | UI session | 204. An admin may revoke any device. It ends the device's open sessions |
| `POST /api/publish/challenge` | none | `{device_id}` returns `{nonce}` (32 random bytes, base64url, single use, 60 s) |
| `POST /api/publish/sessions` | none | `{device_id, repo, nonce, signature}` returns 201 `{id, token: "trs_...", repo, expires_at}` |
| `POST /api/publish/sessions/manual` | UI session | `{repo, totp_code \| passkey}`; same answer. The proof is a fresh check, not recovery codes. 401 for a wrong proof, 429 with `Retry-After` when limited |
| `POST /api/publish/sessions/oidc` | none | `{token, repo}`; same answer |
| `GET /api/publish/sessions` | password, read token or UI session | The caller's own sessions |
| `DELETE /api/publish/sessions/{id}` | the session's own token | 204. The token is the Basic password or a Bearer token. Another token or none gets 401 |
| `GET /api/publish/log` | admin | Every session, newest first |
| `GET, POST /api/publish/trusts` · `GET, PATCH, DELETE /api/publish/trusts/{id}` | admin | Trust policies. The list is keyset by name |

Shapes:

```
Device       = {id, user_id, username?, name, kind, created_at, expires_at, last_used_at, revoked_at, state}
SessionItem  = {id, repo, via, subject, username?, device_id, device_name?, trust_id, trust_name?,
                created_at, expires_at, closed_at, state}
Trust input  = {name, issuer, audience, claims: {claim: [value...]}, jwks, repos: [name...], disabled}
Trust output = {id, name, issuer, audience, claims, jwks, repos, created_at, disabled}
```

PATCH changes only the fields that are set. Approve, manual session and the pending lookup
refuse read tokens and publish session tokens. A `trs_` token never reaches the admin routes,
also not for an admin user.

**Device flow** (RFC 8628 style):
1. The client creates an Ed25519 key pair, stores it in `~/.telesphorepo/<host>[_<port>]/device.json`
   (owner only) and calls `enroll`. It prints `Open <verification_url>?code=<USER-CODE> and
   approve code <USER-CODE>`.
2. The user opens the page, signs in with the second factor, checks name and kind, and approves
   with a fresh TOTP code or passkey check.
3. The client polls until it gets `device_id`. An enrollment code expires after 10 min.
4. A device lives `publish.device_ttl` unless revoked. The client then enrolls again.

**Minting from a device.** `signature` is the Ed25519 signature (base64url, no padding) over the
UTF-8 text `"telesphorepo publish v1\n" + nonce + "\n" + device_id + "\n" + repo` (`device_id` in
decimal). The server checks the signature and nonce, that the device is not revoked or expired,
and that its user has `write` on the repo now. The token is the Basic password for the upload, and
it also reads that repo.

**Trust policies and the CI exchange.**
- A policy needs at least one claim, and each claim at least one allowed value. A value is exact
  or a `*` glob; `*` matches any text, `/` included. Every listed claim must match.
- `audience` `""` means the current `base_url`, resolved at exchange time. A pasted `jwks` is
  checked when the policy is saved and replaces key discovery (for a Jenkins the server cannot
  reach). Signing algorithms are RS, ES and PS (256, 384, 512) and EdDSA. Issuer keys are cached.
- The server checks signature, issuer, audience, `exp`, and `iat` (at most 10 min old and 1 min
  in the future). It finds an enabled policy that matches the issuer, audience and claims and
  lists the repo. It refuses a token seen before (`publish_oidc_seen`, kept until `exp`). The
  replay row and the session are written in one transaction. `subject` is the token's `sub`.
- An edit of issuer, audience, claims, jwks or repos, a disable, and a delete end the policy's
  open sessions.
- A CI session writes outright. A device or manual session needs the user's `write` permission.

**Sessions.** An open session has `closed_at` empty and `expires_at` ahead. A device session also
needs a device that is not revoked or expired. An OIDC session needs an enabled policy. A bad,
unknown, closed or expired token counts toward the rate limit and answers 401. The job in
`internal/publish` runs every minute. In batches of 100 it deletes expired challenges,
enrollments and seen CI tokens, and sessions that expired more than 30 days ago (the audit
trail for the **Publish log**).

**Clients** (Java 17, JDK only). They live in their own repository,
[github.com/Telesphorepo/clients](https://github.com/Telesphorepo/clients), under the MIT license.
The group and plugin id are `com.telesphorepo`.
- **Gradle plugin** (`gradle/` in the clients repo): a settings plugin. `telesphorepo { url = "https://repo.acme.com"; oidcTokenEnv = "..." }`
  in `settings.gradle`. Each `PublishToMavenRepository` task whose repo is under `<url>/maven/<name>`
  gets credentials (user `telesphorepo`, the token as password) just before it runs. A session
  is minted once per repo and closed at build end by a build service. Proof order: GitHub
  (`ACTIONS_ID_TOKEN_REQUEST_URL`, audience = the server URL), the env var named by
  `oidcTokenEnv`, the device key. Tasks that publish to the server opt out of the configuration
  cache. Without a proof the build fails and names `./gradlew telesphorepoLogin`, the task that
  enrolls a device.
- **Maven** (`maven/` in the clients repo, parent `telesphorepo-maven-parent`): `telesphorepo-maven-client`
  (library), `telesphorepo-maven-extension` (declared in `.mvn/extensions.xml`) and
  `telesphorepo-maven-plugin` (goal `telesphorepo:login`). The server URL is the property
  `telesphorepo.url` (or env `TELESPHOREPO_URL`). `telesphorepo.oidcTokenEnv` names the env var
  with a CI token. `telesphorepo.deviceName` names the device. The extension mints a session when
  `maven-deploy-plugin` starts and closes it at the end. A `deploy` without proof stops with a
  message that names `mvn telesphorepo:login`. With `-DdeployAtEnd=true` a session older than
  `publish.session_ttl` at the last module fails.
- **Release.** The clients repo releases from its own workflow (`.github/workflows/release.yml`)
  on a `v*` tag. The server repo has no release job for the clients. The clients repo is
  MIT-licensed; the server is AGPL-3.0.
- **CI without a client.** The UI **Publishing** page and the server `README.md` show `curl`
  steps for GitHub Actions and Jenkins.

**UI pages.** The sign-in page (second step for admins), the approval page (`/#/device?code=`)
and, under Settings: **Sign-in and security** (TOTP with a QR code, passkeys, recovery codes),
**Devices**, **Tokens** (read tokens, and publish tokens with a new manual session), and for
admins **CI pipelines** (trust policies, started from GitHub Actions, Jenkins or another CI
system) and **Activity** (the publish log is its second tab). A setup-only session sees only Sign-in and security. See **Web UI**.

## Debian indexing pipeline

1. **Upload.** The upload is spooled while MD5, SHA1 and SHA256 are computed. The `control.tar.*`
   is parsed (capped at 16 MiB decompressed, `control` at 1 MiB); `data.tar` is never unpacked.
2. **Validation.** `Package`, `Version` and `Architecture` are validated; the architecture must be
   `all` or one of the repo's `architectures` (default `amd64, arm64`). The debian plugin validates
   the repo config: a non-empty list, each name matching `^[a-z0-9-]+$`, no `all`, no duplicates.
   The optional key `distribution` (a repo name pattern) is the distribution that the Nexus
   sync writes to. The key `signing_key` (default `default`) names the repo's signing key and must
   exist (400 otherwise). Other config keys are kept.
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
   skips a pending unit whose architecture is no longer configured. The by-hash files of a
   removed architecture are deleted too.
9. **By-hash cleanup.** After each `Packages` rebuild, the indexer keeps the current and the
   previous generation of `by-hash/` files for that index and deletes the rest.
10. **Delete.** The debian `DeleteObserver` deletes the `deb_packages` rows of each deleted pool
    file and marks the affected dists dirty. It marks nothing while the whole repo is deleted.

## Storage move

A settings PATCH that points the server at another blob location while assets exist starts a
move (`internal/move`). The source is never modified or deleted.

- **Start.** The PATCH probes the target, saves the non-storage keys of the patch, and stores
  the target storage settings in a `storage_moves` row, encrypted (AAD `storage_moves:<id>`).
  The storage settings do not change yet. One move runs at a time.
- **Mirror.** While the move copies, `Live` holds a `storage.Mirror`. Reads and listings use the
  source. `Put` and `Delete` go to the target first, then to the source; an error on either fails
  the call.
- **Copier.** It walks `assets` by `id > cursor` in pages of 500 and saves the cursor and
  counters after each page. For each asset it takes the asset lock that uploads take and reads
  the row again. It skips a deleted asset, and a target blob of the same size whose modification
  time is not before the asset's `updated_at`. It checks the source size and SHA-256 before it
  writes the target. A missing source blob or a mismatch is a `storage_move_failures` row.
- **Finish.** Without failures, one settings write makes the target the active storage, `Live`
  swaps to the target and the move is `done`. With failures the move is `failed` ("N blobs could
  not be copied"), and `Live` returns to the source. A later move skips the blobs already copied.
- **Cancel.** `DELETE` marks the move `cancelled`, stops the copier and returns `Live` to the
  source.
- **Restart.** A shutdown leaves the move `copying`. At startup, before the server serves
  requests, the move continues from its cursor. If the settings already point at the target, the
  move is marked `done`. If the target cannot be opened or probed, the move fails with that
  error.
- **UI.** The **Storage** page (`/#/settings/storage`) shows a progress bar while a move copies, a Cancel button, and the
  failure list of a failed move.

## Nexus sources

An admin adds a Sonatype Nexus 3 server as a **source** on the **Nexus** page
(`internal/nexus`). The server mirrors the mapped Nexus repos with repeated syncs. A mirrored
repo is read only. **Cut-over** ends the mirroring and makes the repos writable. Nexus is read
only through its REST API.

Shapes in the routes above:

```
Source       = {id, name, base_url, state: "mirroring"|"cut_over", created_at, last_error, last_sync: Sync|null,
                cutover_sync_id, cutover_failed}   (both 0 before cut-over)
SourceDetail = Source + {repos: [SourceRepo], nexus_error}
SourceRepo   = {name, format, type, supported, reason, target_format, target}
Sync         = {id, source_id, state: "running"|"done"|"failed"|"cancelled", cutover, started_at,
                finished_at|null, error, imported, skipped, ignored, failed, repos?: [SyncRepo]}
SyncRepo     = {source_repo, source_format, target, state: "pending"|"running"|"done"|"failed"|"cancelled",
                error, imported, skipped, ignored, failed}
Failure      = {source_repo, path, reason}
```

`type` is `""` for a mapped repo that Nexus did not list. `target` is `""` for a repo that is not
mapped. The `Sync` counters are the sums over its repos. Errors are `{"error"}` with 400 for invalid
input or refused credentials, 404, 409 (exists, running, not running, cut over, still mapped) and
502 when Nexus is unreachable.

- **Plugins.** A format that implements `format.Importer` lists the Nexus format ids it takes
  (`ImportFormats`) and imports one file at a time (`Import`). The outcome is `imported`,
  `skipped` (already present with the same bytes) or `ignored` (not a file of this format).
  - maven (`maven2`): every file at its path, with the PUT rules. Identical bytes are skipped; a
    different release file fails; `maven-metadata.xml*` and SNAPSHOT files are replaced.
  - raw (`raw`): every file at its path. Different bytes fail when the repo denies overwrite.
  - debian (`apt`): `pool/**/*.deb` through the upload pipeline into component `main` of the
    repo config's `distribution`. Other files are ignored. A target without `distribution` fails
    each package.
  - A plugin compares `SHA256` from Nexus with what it holds before it downloads. Imported files
    have no uploader. The importers write directly and do not pass the repo lock.
- **Repo list.** The client reads `/service/rest/v1/repositorySettings`, or
  `/service/rest/v1/repositories` when that is refused. Only hosted repos that a plugin takes are
  `supported`.
- **Source.** Create and PATCH check the connection first. The credentials are stored encrypted in
  the source row and are never returned or logged. Error messages name the request and the HTTP
  status only. A new source has `last_sync_at` set to its creation time.
- **Mapping.** `PUT .../repos` replaces the whole mapping. Every entry is checked first: the
  Nexus repo exists, is hosted and is listed once, and the target accepts the format or is created
  (`create: true`) by the first plugin that takes it. A target mirrors at most one source. A
  created target gets the plugin's default config plus the Nexus repo's `distribution` (apt); an
  existing Debian target without `distribution` gets the Nexus repo's. Each mapped target gets
  `locked_by` = `mirrors Nexus source <name>; cut over first`. A target that leaves the mapping is
  unlocked. Targets that the request creates stay when its transaction fails.
- **Sync.** `POST .../sync` and the scheduler start a sync. One sync runs per source (unique
  partial index). The sync copies the mapping into `nexus_sync_repos`. For each repo the runner
  pages `GET /service/rest/v1/assets?repository=X` from the saved continuation token and offers
  each asset to the plugin. A failed page is retried 3 times (2 s, 4 s, 8 s), except on 401/403.
  Downloads use the asset's `downloadUrl` only when it is on the base URL's host, else
  `{base}/repository/{repo}/{path}`. One transaction per page saves the failures, counters and
  next token. A file that is already present with the same bytes is skipped, so a later sync copies
  only new and changed files.
- **Scheduler.** It checks every `min(nexus.sync_interval, 1m)`. It reads the interval from the
  live settings. It starts a sync for each mirroring source whose `last_sync_at` is at or before
  now minus the interval, up to 100 per check. Every sync that ends sets `last_sync_at`.
- **Sync end.** The sync is `done` when every repo finishes, also when some files failed (they
  are in the failure list). It is `failed` when a whole repo failed and `cancelled` after
  `DELETE`. A shutdown leaves the sync `running`; it continues from the last saved page at the
  next start.
- **Cut-over.** `POST .../cutover` runs one final sync with `cutover: true`. It succeeds when
  no repo failed, also when some files failed. Then one transaction sets the sync `done`, sets the
  source `cut_over`, empties its credentials, clears `locked_by` on the mapped targets and saves
  `cutover_sync_id` (the sync) and `cutover_failed` (its failed file count). The failed files stay
  listed through `GET /api/nexus/syncs/{id}/failures`. If a repo failed (Nexus unreachable, a
  listing error), the sync is `failed`, the source stays `mirroring`, and `last_error` says
  `cut-over stopped: ...; the source stays mirroring`. A cancelled cut-over leaves the source mirroring. A restart
  continues an interrupted cut-over. After cut-over, PATCH, PUT repos, sync and cut-over answer
  409, and the sync history stays.
- **Delete.** `DELETE .../sources/{id}` removes the source, its mapping, syncs and failures. The
  repos and their files stay.
- **UI.** The admin-only **Nexus** pages (`/#/settings/nexus`, `/#/settings/nexus/{id}`) lists the sources with an add
  form. The source page shows the state, the repos table with a target name and a checkbox per
  repo, "Save mapping", "Sync now", "Cut over" with a confirm card, "Update credentials", the
  sync history with "Load more" and a sync detail (per-repo counters, Cancel, failures). It polls
  a running sync every 2 s. The "Cut over" confirm card shows how many files failed in the latest
  sync. A cut-over source with `cutover_failed` above 0 shows a warning with a link to the failure
  list of the cut-over sync. The repo list and the repo page show a "mirrors Nexus" badge when
  `locked_by` is set.

## Files, uploads and deletes

`internal/files` serves the file tree and runs deletes and manual uploads. The core names no
format; it calls the optional plugin interfaces of `internal/format`.

- **Tree.** `GET /api/repos/{name}/tree` returns the direct children of a folder in path order,
  folders and files mixed (the UI sorts folders first within a page). Each child is one indexed
  seek on `(repo_id, path)`. After a folder, the next seek starts just past every path under it,
  so a page costs `limit` seeks however large the repo is.
- **Delete credential.** `POST /api/repos/{name}/files/delete` takes a publish session of that repo
  (Basic password or `Authorization: Bearer trs_...`), which goes through the normal write gate:
  the user's `write` permission, the repo lock, and 401 for a session of another repo. It also
  takes the UI session cookie of an admin. Outside dev mode an admin session exists only after
  the second factor. Passwords, read tokens, admin Basic auth and non-admin UI sessions get 401
  `publishing needs a publish session`. A locked repo gets 409.
- **Delete steps.** A prefix is always a folder (a trailing `/` is added). The server checks
  every path and prefix first. The rows of the `paths` go in one transaction under the upload
  locks (taken in path order), then their blobs. Each folder then goes in batches of 500 the same
  way. `deleted` is the number of files.
- **Repo delete.** `POST /api/repos/delete` (admin UI session) deletes one repo after another.
  For each repo it sets the lock text `RepoDeleting`, so writes get 409. It deletes the files
  batch by batch, then the repo row. The name can be used again afterwards.
- **`format.DeleteObserver`.** `AssetsDeleted(ctx, tx, repo, paths)` runs in the delete
  transaction, before the rows go. Debian uses it (see **Debian indexing pipeline**). Maven
  regenerates nothing: a deleted version folder stays in `maven-metadata.xml` until the next
  publish, and the UI says so.
- **`format.ManualUploader`.** `Upload(ctx, repo, files, fields, userID)` stores the files of a
  manual upload. `Scan(ctx, repo, files)` returns the `[]format.Found` that a folder holds and
  stores nothing. `userID` is 0 for a CI session. `UploadFile` (with `Path`, the relative path),
  `Uploaded`, `UploadError`, `BadUpload`, `Found` and `InFolder` are the helper types.
  `Host.Audit` and `Host.RecordUpload` record the event.
- **Upload limits.** At most 500 files, 16 text fields of up to 1 KiB each, and all files together
  within `max_upload_size`. Fields come before the files in the body.
- **Relative paths.** The `filename` of a multipart file part may be a relative path
  (`folder/sub/file.jar`). The server reads it from the raw part header, because Go's
  `Part.FileName()` keeps only the base name. There is no separate paths field.
- **Folder scan.** `POST /api/repos/{name}/upload/scan` takes the same body as the upload and
  answers `{found, ignored}`. Each `found` item has a `kind` (`maven`, `debian` or `file`), a
  `title`, its `files` and its `problems`. `ignored` lists the files that no item uses. The scan
  stores nothing. The UI shows the result as a preview and uploads only the files of the items the
  user ticks.
  - **Maven folder.** A folder upload (any path with a `/`, or more than one pom) stores every
    artifact that the scan finds, and answers 400 when any of them has a problem. Each pom with
    the files next to it named `<artifactId>-<version>[-<classifier>].<ext>` is one artifact;
    `.module` files belong to it. Checksum and signature files (`.md5`, `.sha1`, `.sha256`,
    `.sha512`, `.asc`) are ignored, because the server writes the checksums. In a Maven-layout
    folder (`<group path>/<artifactId>/<version>/`) the pom's groupId, artifactId and version must
    match the path. A `pom.xml` outside a Maven layout is a project file and is ignored.
  - **Maven.** A single-pom upload: one pom (`*.pom` or `pom.xml`) plus files named
    `<artifactId>-<version>[-<classifier>].<ext>`. One file with any other name becomes the main
    artifact. The fields `groupId`, `artifactId`, `version` and `packaging` fill what the pom
    lacks. A lone artifact gets a generated pom. Every stored file gets `.md5`, `.sha1` and
    `.sha256`. `maven-metadata.xml` is created or merged, with the versions in Maven order, plus
    `latest`, `release` and `lastUpdated`.
  - **Debian.** `.deb` files with the fields `distribution` and `component` (default `main`),
    through the normal ingest. A folder upload uses only the `.deb` files; the scan reports each
    one that does not parse as a problem.
  - **Raw.** The field `folder`, under the repo's overwrite policy. A folder upload stores each
    file at `folder/<relative path>`.

## Network rules

`internal/netrules` resolves the client address, keeps the IP lists and checks the IP rules of
each repo. Request paths read in-memory snapshots only. The database holds the lists and the
rules.

**Client IP.** A middleware resolves the client address once per request and attaches it to the
request context. `httpx.ClientIP(r)` reads it. The rate limits, the audit log, the IP rules,
device enrollment and the access log all use it.

- If the TCP peer is not in `trusted_proxies`, the peer is the client. The server ignores
  `X-Forwarded-For` and `CF-Connecting-IP` from such a peer.
- If the peer is trusted, the server reads `X-Forwarded-For` from right to left and skips trusted
  addresses. The first untrusted address is the client. If every hop is trusted, the left-most
  hop is the client. An entry that does not parse stops the walk at the last address read.
- `CF-Connecting-IP` counts only when the walk ends at an address in the list `cloudflare` (the
  peer itself, or the last hop read), and `trusted_proxies` names `list:cloudflare`. So
  Cloudflare in front of Caddy needs no Caddy setting: Caddy puts the Cloudflare edge in
  `X-Forwarded-For` and passes `CF-Connecting-IP` on unchanged.
- `trusted_proxies` is a list. The default is empty: the server trusts no proxy. An entry is a
  CIDR, a plain address, `private` (loopback, RFC 1918, ULA and link-local) or `list:<name>`, the
  ranges of an IP list (for example `list:cloudflare`). A bare `cloudflare` is invalid. An invalid
  entry or an unknown list gives 400 `{error, fields: {trusted_proxies}}` on PATCH.
- The default trusts nothing because Docker's userland proxy and rootless Podman make every outside
  connection arrive from a private gateway address. With `private` trusted there, anyone who
  reaches the port directly could forge `X-Forwarded-For` and get around the IP rules.
- **Proxy candidates.** When a peer that is not trusted sends `X-Forwarded-For`, `Forwarded` or
  `CF-Connecting-IP`, the server remembers the peer in memory: the last time, the count and the
  header of the last request. It keeps at most 20 peers and drops the one seen longest ago. A
  peer that `trusted_proxies` covers leaves the list. Nothing is stored in the database, so a
  restart empties the list. `GET /api/network/proxy-candidates` returns it, and the UI offers
  "Trust this address", which adds the address to `trusted_proxies` with the settings PATCH.

**IP lists.** An IP list is a named set of CIDRs. Repo rules and `trusted_proxies` use lists by
name. The table is `ip_lists`.

- **Presets.** `cloudflare` (`https://www.cloudflare.com/ips-v4` and `/ips-v6`) and
  `github-actions` (the `actions` key of `https://api.github.com/meta`). They are read-only. The
  server creates them at start. It then refreshes each preset that was never fetched or is a day
  old, and it checks again every hour. The fetch client has timeouts. The body limit is 8 MiB and
  a list holds at most 100,000 ranges. A failed refresh keeps the last good list and stores the
  error text. An admin can refresh a preset at any time (`POST /api/ip-lists/{name}/refresh`). The
  test variable `TR_E2E_PRESET_BASE_URL` points every fetch at `<base>/ips-v4`, `<base>/ips-v6`
  and `<base>/meta`.
- **Custom lists.** An admin creates, renames, edits and deletes them. A name follows the repo name
  pattern. A title has at most 100 characters, a description at most 1,000, and a list at most
  10,000 ranges (CIDRs, or addresses that become `/32` or `/128`). An empty title becomes the name.
  No list may use the name of a preset. The list API checks its `ranges` strictly: each one must
  be a CIDR or an address, and a duplicate is a 400. Repo rules and `trusted_proxies` check theirs
  the same way.
- **Paste parsing.** `POST /api/ip-ranges/parse` (`netrules.ParseRanges`) takes text in any common
  layout and returns the ranges in it; the UI then saves them through the strict routes. The
  parser splits the text into lines and cuts each line at `#` or `//`. It splits a line on
  commas, semicolons, whitespace and `|` (CSV and table columns). It removes quotes and brackets
  around a token, trailing punctuation, and a label before `=` or `:` (`ip=1.2.3.4`,
  `runner-1:1.2.3.4`; a label before `:` needs a character that is not a hex digit). An address
  becomes a `/32` or `/128`. A CIDR is masked to its network address (`3.40.7.200/28` →
  `3.40.7.192/28`). An IPv4-mapped IPv6 address or range becomes IPv4. A zone is refused. Other
  tokens on a line with a range are ignored, and a token without a letter or digit (`---`) counts
  as nothing. A line with content and no range goes into `invalid` with its 1-based number and
  the reason "not an IP address or range". The first line with content is a header row, not
  invalid, when no token on it starts with a digit (`name,ip`). Duplicates are counted in
  `duplicates`, and the first one keeps its place. Limits: 1 MiB of text, 100,000 ranges, and the
  first 1,000 invalid lines, each clipped to 200 bytes.
- **Rename.** A rename updates the repo rules and `trusted_proxies` that name the list. It answers
  409 when the rename would change a `trusted_proxies` value that the environment sets.
- **Delete.** Refused with 409 `{error, used_by}` while a repo rule or `trusted_proxies` names the
  list. A preset cannot be deleted (400).
- **Use.** `used_by` is `{repos: [{repo, scope}], trusted_proxies: bool}`. A partial index on
  `repo_ip_rules` finds the rules that name a list.

**Repo IP rules.** Each repo has two allow-lists, `read` and `publish`. An entry is a CIDR or the
name of an IP list. An empty list is unrestricted (the default).

- **Save.** `PUT /api/repos/{name}/ip-rules` replaces both lists. A CIDR is masked and an address
  becomes `/32` or `/128`. At most 200 rules per scope and 200 characters per note. A duplicate, a
  bad CIDR or a `list` that does not exist gives 400. The change is audited as `repo.ip_rules`
  with the counts only.
- **Read rules** apply to every format read and to the repo read routes (`tree`, `assets`, `usage`,
  `status`), for every caller, admins included. Search leaves out the repos whose read rules
  refuse the caller. `GET /api/repos/{name}` is exempt (`auth.RepoMeta`), so an admin can still
  open the repo and change its rules.
- **Publish rules** apply to format writes, manual upload and scan, deletes (an admin UI session
  too) and to minting device, manual and OIDC publish sessions.
- **Refusal.** The answer is 403 `{"error":"your address is not allowed to read|publish this
  repository"}` and the audit event `ip.denied` with `detail.scope` and the client IP. The IP
  appears only in the audit log.

## Signing keys

`internal/signing` keeps the signing keys. It loads every key into memory at start, and request
paths and the indexers read that copy. The table is `signing_keys`.

- **Name.** 1 to 64 lower-case letters, digits, `.`, `_` or `-`, starting with a letter or digit.
- **First start.** If the table holds no key, the server creates the Ed25519 key `default`. If an
  admin deletes `default`, a repo created without `signing_key` gets 400 that names the missing
  key. The offline `seed` command needs a server that has started once, because the trigger needs
  the `default` row.
- **Storage.** The server stores the private key without a passphrase, sealed with the master key
  (AAD `signing_keys:<id>`). If the master key changes, the key shows `error`, and the repos that
  use it publish indexes without a signature.
- **Create or import.** An admin creates an Ed25519 key, or imports an armored private key. An
  encrypted key needs its passphrase. The stored copy has no passphrase.
- **Use.** A Debian repo config holds `signing_key` (default `default`). An unknown key gives 400.
  `PATCH /api/repos/{name}` replaces the whole config, so a client sends `signing_key` back.
- **Public key.** `GET|HEAD /debian/{repo}/key.asc` returns the armored public key of the repo's
  key. It follows the read rules of the repo: anyone for a public repo, credentials for a private
  repo.
- **Change of key.** When a repo changes its key, only that repo is signed again: every `Release`,
  `InRelease` and `Release.gpg` of the repo is written again.
- **Delete.** `DELETE /api/signing-keys/{name}` checks the use inside the write transaction. It
  answers 409 `{error, repos}` while a repo uses the key.
## Audit log

`audit_events` records who did what. `GET /api/audit` is admin only and lists newest first.
Actions:

| Group | Actions |
|---|---|
| Sign-in | `signin`, `signin.failed` |
| Publish sessions | `session.create`, `session.close` |
| Files | `upload`, `delete` |
| Repos | `repo.create`, `repo.update`, `repo.delete`, `permission.set`, `permission.delete` |
| Users and settings | `user.create`, `user.update`, `settings.update` (keys only, never values) |
| Network rules | `repo.ip_rules` (counts of read and publish rules only), `ip.denied` (`detail.scope` is `read` or `publish`), `ip_list.create`, `ip_list.update`, `ip_list.delete` |
| Signing keys | `signing_key.create` (created or imported), `signing_key.delete` |

- Changes made with the `telesphorepo user` commands are recorded with the actor `cli`.
- Client IP addresses appear only in this table and its API. The only other API with an address
  is `GET /api/network/proxy-candidates` (admin), which lists the addresses of untrusted proxies,
  not of clients. The publish log has none. The access log still records the IP.
- The setting `audit.retention` (default `8760h`) sets how long an event stays. A job deletes
  older events every hour.

## Web UI

Svelte 5 + Vite (no SvelteKit), Tailwind CSS v4, shadcn-svelte with the Maia style, TypeScript
and bun. `bun run build` runs `svelte-check` and `vite build` into `web/dist`, which the binary
embeds. Routing is hash-based (`/#/...`), so the URLs that the server and the clients print keep
working. The UI is only a client of the JSON API and has no tests of its own. The route table,
the folder layout, the components, the design rules and the steps to add a page are in
`web/src/README.md`. The principles are in `docs/design/principles.md`. The reason for each
decision is in `docs/design/rationale.md`.

- **Words and look.** The UI uses plain words, not jargon (the rename table is in
  `web/src/README.md`). `web/src/README.md` is binding for the UI code.
- **One shell, one width.** Every page sits in one centered container, 1200 px wide at most, with
  24 px side padding (16 px under 640 px). Browse and Settings use the same container. Tables use
  the full width of the container. The page reserves its scrollbar gutter.
- **Header.** The top bar is inside the container: logo and name (Home) on the left, a search
  field in the center (480 px at most), and on the right one **Sign in** button when the visitor is
  signed out (hidden on `/#/login`), or **Settings** and the avatar menu when the visitor is signed
  in. The search field holds the query only on the Search page. Sign out returns to Home. The page
  `/#/login` is the only place to sign in.
- **State on the thing.** No page opens with a status row or a summary banner. A switch shows that
  it is on. A method shows in its list. A private repository has a lock after its name, and a
  public repository has no mark. A badge marks the one row that needs attention.
- **Cards.** Each area of a page is a card (`Section`): a header with its title and its actions,
  then its content. Every table is in a card too. Cards are 24 px apart. A form card holds a
  two-column grid: the label above each control, every control the width of its cell, and a long
  field across both cells. The grid is one column under 640 px. A setup is a row of numbered cards
  (`Step`).
- **Type, spacing and states.** One sans-serif family for all text. Monospace is for code blocks
  and copy-paste snippets only. One spacing scale (4 px base) and one radius (8 px). Hover and
  selected states fill the whole row or item. There is no left-border accent. Every page has
  loading, empty and error states.
- **Loading.** The UI shows no skeletons. A loading area shows nothing for 300 ms, then a quiet
  spinner in the space the content will take. A reload keeps the content on screen.
- **Icons.** An icon marks a meaning (format, lock, state) and sits bare, never in a tinted square.
  Each format plugin supplies its own SVG (`format.Iconic`, returned by `GET /api/formats`):
  the Maven feather, the Gradle elephant for Gradle plugins, the Debian swirl and a file glyph for
  raw. The UI reserves the icon's space before the format list loads.
- **Dialogs and pages.** An action that does not need to stay on screen opens a dialog: change
  password, add a person, add a Nexus server, create a token, delete a repository (the dialog asks
  for the repository name). A card never holds a lone action button. A flow with several sections
  is a full page: create a repository, edit a CI pipeline, edit a network list, create or import a
  signing key, upload. The only side sheet is the navigation menu under 768 px. The details of a
  selected file show in a column inside the page.
- **Test where the connection is set.** A page that connects to another system has a **Test**
  action in the card or the step that sets the connection. The result shows beside the button, and
  only after a test. Sign-in options and Storage have it.
- **No browser password prompt.** The API client sends `X-Telesphorepo-UI: 1` on every request
  (fetch and upload). The server never sends `WWW-Authenticate` for such a request or for a path
  under `/api`. Plain download links of format routes carry no such header and keep the
  challenge.
- **Browse** (anyone; visitors see the anonymous-read repos). `/#/` is titled "Repositories", with
  no subtitle. A toolbar holds a filter box, a format filter and **New repository** (admins). A
  table shows the format icon, the name (with a lock when the repo is private), files, size and
  last publish (`file_count`, `total_size`, `last_published_at`). On a phone the format icon sits
  before the name. A click anywhere on a row opens the repo. Search is `/#/search?q=&repo=`: the
  top-bar field holds the query, and a select picks the repo.
- **Repository page** (`/#/repo/{name}/...`). The header shows the name with a lock for a private
  repo, the format (icon and title) and the description. **Upload** and **Settings** show for users
  who may use them. One page loads the repo once and switches between these views:
  - `files/<path>`: breadcrumbs and one of the views below, with a details column for the selected
    file or artifact (SHA-256, checksum links, Download, client snippets). Delete asks for a fresh
    second factor first, except for admins.
  - `use`: **Use this repository**. An address card first. A public repo then has one card, "Add it
    to your build". A private repo has two numbered steps: create a read token, then add it to the
    build; the snippets already hold the credentials.
  - `publish`: **Publish to this repository**, for users who can publish (others see a lock and
    "You cannot publish to X"). One card each for From your computer, From CI and By hand. The
    notice of a paused repo (a Nexus mirror) shows once, above the tabs.
  - `upload?folder=`: a full page. It starts with the fresh second-factor check, then one drop
    zone takes files or a folder, followed by the scan, the preview, the fields and the progress.
    The publish session lives in memory only. The limits and the folder filter are as in **Files,
    uploads and deletes**.
- **Views of the files.** A switcher offers **List**, **Tree** and **Columns** everywhere, and
  **Cover Flow** at an artifact or a snapshot that has several builds. The choice is remembered per
  user in the browser, for each place. Tree, Columns and Cover Flow fill a box of one height (at
  least 22 rem, at most 36 rem, otherwise the window height minus 24 rem). The list keeps that
  height as its minimum on wide screens, so a switch does not move the details column or the
  toolbar.
  - *Tree* shows the whole repo as an expandable tree. A folder loads its children from `/tree` the
    first time it opens, a page at a time. Up and Down move, Right expands or steps in, Left folds
    or steps out, Home and End jump, Enter opens. A version folder shows the files of its latest
    build and a row "N earlier builds".
  - *Columns* are Miller columns.
  - *Cover Flow* is a timeline of tickets for the versions of an artifact, or the builds of a
    snapshot. It replaces the list in the same box. The caption of a version has Open and Delete;
    the caption of a build has "Show N files", which switches to the list with that build chosen.
- **Settings.** The sidebar is 220 px wide inside the container. Under 768 px it is a menu. All
  paths are under `/#/settings/`.
  - *Your account:* Overview (`account`), Sign-in and security (`security`), Devices (`devices`)
    and Tokens (`tokens`: read tokens and publish tokens, two cards on one page).
  - *Repositories* (admins): All repositories (`repositories`, `repositories/new`,
    `repositories/{name}/{tab}`).
  - *People and CI* (admins): People (`people`, `people/{name}`), CI pipelines (`ci-pipelines`, `ci-pipelines/new`,
    `ci-pipelines/{id}`) and Networks (`networks`, `networks/new`, `networks/list/{name}`).
  - *Server* (admins): Sign-in options (`sign-in`), Signing keys (`signing-keys`,
    `signing-keys/new`), Storage (`storage`), Nexus migration (`nexus`, `nexus/{id}`), Network and
    proxy (`proxy`), General (`general`) and Activity (`activity?view=&action=&repo=`: the audit log
    and the publish log).
  - Breadcrumbs follow the hierarchy: Settings, the group, the page. A sub-page of the first
    item of a group does not repeat that item.
- **Repository settings** (admins). A page with the tabs Overview, Access, Format settings and
  Signing (Debian). The header shows the name with a lock for a private repo. **Delete** is an
  action at the end of the header, on every tab; it opens a dialog that asks for the repo name.
  - *Overview:* description and visibility.
  - *Access:* a Download card (the Public switch, people who can download and not publish, from where) and a Publish card (people who can publish, CI
    pipelines, from where), with thin dividers between the parts. **Add CI pipeline**, **Add
    network list** and **Add addresses** each open a short form in a dialog. **Add people** opens
    a dialog with a filter, a checkbox list of the people without access, Select all and Select
    none, and a choice of Download or Publish; it saves at once through `PUT /api/permissions`. The
    people tables take a pick, with **Remove access** (asks first) and **Change to Download** or
    **Change to Publish**; these act at once. The Public switch saves through `PATCH /api/repos`.
    Add CI pipeline adds existing pipelines, or links to
    `/#/settings/ci-pipelines/new?repo={name}&back=/settings/repositories/{name}/access`.
    Each person links to their page, where the same access is also edited.
  - *Signing:* the current key, the `key.asc` address, change the key or create a new one.
  - Create a repository at `/#/settings/repositories/new`: name, format (with the plugin icon),
    description, visibility. Debian adds architectures and the signing key. For a new key, the page
    calls `POST /api/signing-keys` and then `POST /api/repos`. If the second call fails, the page
    deletes the new key. **Create** is in the footer of the last card.
- **People.** The table shows each account with its avatar and role. The sign-in method column
  shows only when someone uses single sign-on. A row opens the person's page
  (`/#/settings/people/{name}`): an identity card (avatar, role, how the person signs in, a
  Disabled badge) with Make administrator or Remove administrator rights, Reset password and Disable
  account or Enable account; then the person's access to each repository. **Give access** opens a
  dialog with a filter, a checkbox list of the repositories the person cannot reach yet, Select all
  and Select none, and a choice of Download or Publish. The repository table takes a pick, with
  **Remove access** (asks first) and **Change to Download** or **Change to Publish**. Each action
  saves at once. An administrator's page says that administrators reach every repository. The
  People table takes a pick of every row except your own: **Disable** asks first ("Disable 4
  people? They are signed out and cannot sign in."), **Enable** acts at once. **New account**
  opens a short form; after Create, the new person's page opens.
- **Bulk actions.** All repositories (**Make public**, **Make private**, **Delete**), the List
  view of the Files tab (**Delete**, files and folders mixed), People, a person's repositories and
  a repository's people take a pick (`DataTable` `bind:picked`). While rows are picked, a bar in
  place of the header row shows "12 selected", the actions, "Select all 50" and "Clear". Every
  delete asks first with the count and the first five names; a delete of more than one repository
  asks for the count. A toast says what changed.
- **CI pipelines.** A table of the trust policies: the name, what the policy accepts (less what
  the name says), the CI system and the repositories it can publish to. Only a pipeline that is
  off has a badge ("Off"); the switch on its page shows the state.
- **CI pipeline page.** A new pipeline has four steps: where the job runs (GitHub Actions,
  Jenkins, another CI system), which runs can publish, the repositories, and the publish step for
  the workflow or Jenkinsfile. The page builds the claims from answers a developer knows.
  GitHub Actions: the repository `owner/name` (`repository`), branches and tags (`ref`, as
  `refs/heads/<b>` and `refs/tags/<t>`) and an optional workflow file (`workflow_ref`
  `<repo>/.github/workflows/<file>@*`); issuer `https://token.actions.githubusercontent.com`.
  Jenkins: the address (issuer `<address>/oidc`) and job names such as `team/app` (`sub`
  `<address>/job/team/job/app/`), with optional pasted keys. **Edit the token conditions** shows
  the claims, the audience and the keys instead; another CI system always uses them. **Use the
  simple form** reads the claims back, and asks first when they say more than the answers can.
  The publish step comes from `GET /api/repos/{name}/usage` (the CI snippets of the format),
  with the branches and tags as the workflow trigger. `?repo=<name>` starts with that repository
  chosen; `?back=<path>` (only a `/settings/` path) is where Create and Cancel return.
- **Signing keys page.** A table with the fingerprint and the repos that use each key (as links), a
  public key download and delete (turned off while a repo uses the key). The new-key page creates
  an Ed25519 key or imports an armored key with its passphrase.
- **Networks page.** Downloaded lists show size, last refresh, error and a Refresh button. Your own
  lists show size and what uses them. **New network list** is the action of the "Your lists" card.
  The list page edits name, title, description and addresses, shows where the list is used, and
  deletes. A downloaded list shows its ranges read-only.
- **Pasted addresses.** The network list editor, the Access tab (the Add addresses dialog of each rule) and
  the Network and proxy page take addresses through one paste box (`RangePaste`). The box sends the
  text to `POST /api/ip-ranges/parse` 400 ms after the last key, or at once on Check. It shows
  "8 ranges found · 0 invalid · 0 duplicates removed", the invalid lines with their numbers and
  reasons, and the ranges. **Add** appends the ranges that are not there yet; **Replace** puts them
  in place of the current addresses. Nothing is saved until the page's own save.
- **Network and proxy page.** It edits `trusted_proxies`: private networks, any network list (saved
  as `list:<name>`) and other addresses (a list with remove buttons and the paste box). For each
  proxy candidate it shows "Requests from <address> carry forwarded headers. Is this your reverse
  proxy?" and **Trust this address**. A Caddyfile hint takes its host from `base_url`. A setting
  that an environment variable sets shows a badge and cannot change.
- **General page.** One form with a card per settings group. **Storage** and **Sign-in options**
  have their own pages.
- **Storage page.** The location card and, for an S3 bucket, the bucket card, each with **Test** in
  its footer. A running storage move is a card with progress and **Cancel**.
- **Sign-in options page.** Three numbered cards: connect your provider (with **Test** in its
  footer), choose who becomes an administrator, sign-in options. The labels are plain words; each
  field also names its term at the provider (Issuer URL, Client ID). The page tests the saved
  settings when it opens and shows the result only when the test fails. **Test** checks the unsaved
  edits and marks the fields that are wrong.
- **Nexus migration.** A table of Nexus servers. Adding a server opens a dialog. A server's page
  shows its state as a badge next to its title, the repositories to copy, the syncs (a row opens its
  details), the connection, the switch over, and **Delete** in the header.
- **Your account.**
  - *Overview:* an Administrator badge by the title for an administrator. Then a card each for
    the repositories you can use, your devices and your recent publishes. The "You can" column of
    the repositories shows only when the rows differ.
  - *Sign-in and security:* a Sign-in card with one row, "Password" and **Change**; **Change**
    opens a dialog (a single sign-on account sees "Single sign-on" and a line that sends it to the
    identity provider). A Two-step verification card lists the methods: each app, each passkey and
    the recovery codes are rows, and a method that is not set up is one row with its Add action.
    The rows are plain rows in the card, with thin dividers between them.
    Without a second step, this card comes before the Sign-in card. A Recovery codes card shows
    when a second step exists.
  - *Devices:* a table in a card. An empty list is one row "Devices · None yet".
  - *Tokens:* read tokens and publish tokens, each in a card.
- **Ticket moments.** A new publish token prints as a felt ticket with the repository, the end
  time and its number; ending the token punches the ticket. The device approval page shows the
  device as a ticket with the code on its stub; approving tears the stub off. Each animation takes
  under 400 ms and does not play under `prefers-reduced-motion`.
- **Empty lists.** An empty list is one plain row (what is empty, why it is useful, one action),
  never a table without rows. Inside a titled card it does not repeat the card title and has no
  dashed frame. The header action of the card is hidden while the list is empty, so the row holds
  the only action.
- **Auth in the UI.** A 401 outside the sign-in routes signs the user out. A 403
  `second_factor_required` sets the setup-only state. Signing out returns to Home.

## CLI

| Command | Does |
|---|---|
| `telesphorepo [serve]` | Runs the server from the boot env and DB settings |
| `telesphorepo dev [--empty]` | Zero-config local server. Data goes in `./.telesphorepo-dev`. The first run creates `admin` and the demo data (see **Demo data**) before the server listens; with `--empty` it creates one empty anonymous-read repo per registered format instead, named after the format. The banner prints the demo users, each format's usage snippets and "2FA not enforced (dev)". Dev mode makes the second factor optional (see **Auth model**) |
| `telesphorepo user add NAME [--admin]` | Asks for the password twice on a TTY, or reads one line from a pipe. Every `user` command writes an audit event with the actor `cli` |
| `telesphorepo user passwd NAME` | Resets the password and signs out the user's sessions. Refused for SSO users |
| `telesphorepo user reset-2fa NAME` | Removes the TOTP factors, passkeys and recovery codes of a user, so the user sets up a new factor at the next sign-in |
| `telesphorepo version` / `help [cmd]` | |

**Demo data.** The first run of `telesphorepo dev` (without `--empty`) fills the dev instance
for work on browsing and search. Paths, file contents, users and roles are the same on every fresh
run. The times spread over the two years before the day of the run, and the Maven metadata
timestamps follow them.

| Repo | Access | Content |
|---|---|---|
| `maven` | anonymous read | About 150,000 files. 400 groupIds of 2 to 4 segments from word lists (e.g. `io.northwind.billing.cli`), 3 to 60 artifacts each (a few groups are large), 1 to 40 versions each (most have few). Each version has a valid pom, jar and sources jar (minimal zips), sometimes a javadoc jar and Gradle `.module`, and real `.md5`/`.sha1`/`.sha256` files. Each artifact has a `maven-metadata.xml` with `latest` and `release`. Some artifacts end in a `-SNAPSHOT` with timestamped files and version metadata. 30 Gradle plugins have their `<id>.gradle.plugin` marker artifacts |
| `internal` | private | About 2,000 files of the same shape. `lena` reads it; `jonas` writes it |
| `debian` | anonymous read | About 300 binary packages from about 100 sources, 1 to 5 versions each, as real `.deb` files for `amd64` and `arm64` or `all`. They go through the plugin's upload pipeline into `stable/main`, so the indexes and signed `Release` files are valid |
| `raw` | anonymous read | 20,000 files: `docs/<product>/<version>/` sites, `releases/<yyyy>/<mm>/<product>-<version>/` archives (valid `.tar.gz`/`.zip`) with `SHA256SUMS`, and `build-logs/<project>/<branch>/<build>/` logs and test reports |

The demo users are `priya` (admin), `jonas` (write on `maven` and `internal`), `mei` (write on
`maven` and `raw`), `tomas` (write on `debian` and `raw`) and `lena` (read on `internal`). Their
random passwords are in `<data>/demo-users.txt`. Each file's uploader is an admin or a writer of
its repo. Blobs go to the configured store through a pool of 32 writers, and asset rows go in
transactions of 5,000, so the assets triggers keep the repo counters and the search index. The
command prints its progress to stderr every 3 seconds, then runs `ANALYZE`. While it runs,
`<data>/demo-data.pending` exists; a dev start that finds that file deletes the data dir and
starts again.

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
| `go tool task test [go test args]` | Runs the core zone: `go test -tags e2e ./e2e/... -count=1 -v` |
| `go tool task test import [go test args]` | Runs the import zone: `-tags import`, 30 min timeout. It starts a Nexus container |
| `go tool task test scale [go test args]` | Runs `TestScale` (`-tags scale`, 60 min timeout) |
| `go tool task test clients [go test args]` | Runs the clients zone: `-tags clients`, 40 min timeout. It builds the Gradle plugin and the Maven extension from the clients repo (`E2E_CLIENTS_DIR`, or a shallow clone of `E2E_CLIENTS_REF`, default `main`) |
| `go tool task test all [go test args]` | Runs core, import, scale and clients one after the other. It runs every zone, lists the failed zones and exits non-zero if any failed |
| `go tool task release [version]` | Builds the UI once. Then it cross-compiles `bin/release/telesphorepo_<version>_<os>_<arch>` for linux, darwin and windows on amd64 and arm64 (`CGO_ENABLED=0`, `-trimpath`). Each binary goes into a `.tar.gz` (`.zip` on Windows) with `LICENSE` and `README.md`. The command writes `SHA256SUMS`. The version defaults to `git describe` |
| `go tool task gen` | Runs `go tool sqlc generate` for every `internal/**/sqlc.yaml` |
| `go tool task doctor` | Shows the detected toolchain and container engine |

CI runs on GitHub Actions with the built-in `GITHUB_TOKEN` and no repository secrets:

| Workflow | Runs | Does |
|---|---|---|
| `ci.yml` | Push to `main`, pull requests | `build` job: `go tool task build`, `go vet` for the `e2e`, `import`, `scale` and `clients` tags, `telesphorepo dbcheck`. `e2e-core` job: `go tool task test` (uploads the log on failure). `snapshot-image` job (push to `main` only, after `e2e-core` passes): multi-arch image with tags `main` and `sha-<short>`, version `main-<short sha>`. A newer push cancels the running pull request jobs |
| `e2e-extended.yml` | Manual (zone `import`, `scale` or `all`), weekly for `import` | `go tool task test <zone>` |
| `release.yml` | Push of a `v*` tag | `binaries` job: `go tool task release`, then a GitHub Release with the archives and `SHA256SUMS`. `image` job: multi-arch image (`linux/amd64`, `linux/arm64`) pushed to `ghcr.io/telesphorepo/telesphorepo` with tags `X.Y.Z`, `X.Y` and `latest` (no `latest` for pre-releases) |

The Dockerfile builds the UI and the Go binary on the build platform and cross-compiles with
`TARGETOS` and `TARGETARCH`. It takes `VERSION` as a build argument. A plain `docker build` or
`podman build` builds for the host.

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
inside a server container. `files_test.go` covers `TestTree`, `TestDelete` (who may delete, file, prefix and Bearer deletes, an admin UI session, blobs gone, repo delete and name reuse), `TestManualUploadMaven` (checksums, metadata order, `mvn` resolves the uploaded and the generated pom), `TestManualUploadRaw`, `TestRepoCounters` (`file_count`, `total_size` and `last_published_at` through uploads, a replace, a file delete and a folder delete) and `TestDebianDeleteAndUpload` (apt sees the package, then not; 4 by-hash files remain). `audit_test.go` covers `TestAudit`: events, filters, paging, no secret values, admin only, and no IP outside the audit log. `network_test.go` covers `TestClientIP` (with the default an `X-Forwarded-For` from the harness peer is ignored and the peer becomes a proxy candidate; trusting the candidate makes the header count and removes the candidate; `private`: a chain through a private hop, `CF-Connecting-IP` from a non-Cloudflare peer; an untrusted peer's header, an empty list, invalid entries, reset to the default). `TestIPRules` and `TestPresetIPLists` set `trusted_proxies` to `private` first, so `X-Forwarded-For` sets the client address, `TestIPRules` (validation; allowed and refused reads of the format route, tree, assets, usage and status; the repo record stays readable; search drops the repo; refused device and manual mints, PUT and admin delete, each audited with its IP and scope; empty lists lift the rule) and `TestPresetIPLists` (a second Telesphorepo server is the stub: startup fetch, a preset in read rules, a Cloudflare hop in the chain, a failed refresh that keeps the last good list, recovery, and stored lists after a restart without the stub) and `TestCustomIPLists` (create, edit, rename that carries the references, use in rules and in `trusted_proxies`, delete while in use gives 409) and `TestIPRangeParse` (the paste parser: one per line, comma, space, tab and `|` separated, CSV with a header row, tables, comments, labels, quotes, IPv6 and IPv4-mapped IPv6, an unmasked CIDR, duplicates, invalid lines with their numbers, the 1 MiB limit, admins only). `signing_keys_test.go` covers create, import of plain and encrypted keys, delete while in use gives 409, a per-repo key that `apt` installs from, a key change that signs again, and keys after a restart. `TestNoBrowserPasswordPrompt` (in `auth_test.go`) checks that a 401 under `/api` or with `X-Telesphorepo-UI` has no `WWW-Authenticate`. `folder_upload_test.go` covers `TestFolderUploadMaven` (a Maven-layout tree with checksum, signature and stray files; a flat folder with two poms; a bad tree that gives 400; scans store nothing; checksums and metadata) and `TestFolderUploadDebianAndRaw`. `TestStorageMove*` run each server on a named data volume: fs to
SeaweedFS and back with uploads during the move, a forced failure, a restart mid-move
(`Server.Restart`) and a cancel.

**Zones.** A build tag picks the zone, so a run starts only the containers it needs. The harness
and the client helpers (`harness_test.go`, `main_test.go`, `clients_test.go`) carry every tag.
| Zone | Tag | Command | Contents |
|---|---|---|---|
| core | `e2e` | `go tool task test` | Formats, auth, API, settings, storage move, query plans |
| import | `import` | `go tool task test import` | `TestNexus` |
| scale | `scale` | `go tool task test scale` | `TestScale` |
| clients | `clients` | `go tool task test clients` | `TestClients` (`e2e/publish_clients_test.go`). It needs `git` unless `E2E_CLIENTS_DIR` points at a checkout of the clients repo |
| all | all tags | `go tool task test all` | Every zone |

**Admin and user calls.** The API changes state only through a UI session, so the suite signs in
the way the web UI does. `s.Admin()` is the bootstrap admin's UI session: on first use the
harness enrolls TOTP factors through the setup-only session and signs in with the password and a
code. `s.SignIn(t, user, pass)` gives any user's UI session, and `s.AdminBasic()` is the admin
password for format reads. `TestAPISessionRule` checks where each credential works, including a UI session that downloads from a private Maven repo and cannot PUT there.

**Publishing tests.** Every publish in the suite uses a publish session. The harness helper
`PublishToken(t, s, user, repo)` enrolls a device the real way (the test computes the TOTP
codes), approves it and mints a session. The harness enrolls 3 TOTP factors per user, so tests
never wait 30 s for a new step. `publish_test.go` covers: `TestPublishGate` (a password or read
token is refused; a session for another repo; lost permission; closed and expired sessions;
replayed nonce; bad signature; revoked and expired devices; manual sessions),
`TestSecondFactorLogin` (including the setup-only session), `TestDeviceEnrollment`,
`TestDeviceEnrollmentExpiry` (ages the row with `editDB` on a data volume) and `TestPublishOIDC`
(Keycloak as the CI issuer through the client `ci-github` in `realm-e2e.json`, whose tokens carry
GitHub-shaped claims: wrong claim, wrong audience, replayed token, an unlisted repo, a disabled
policy, a pasted JWKS). The Keycloak fixture is in `keycloak_test.go` (tags `e2e` and `clients`).

`TestClients` builds the Gradle plugin and the Maven extension in containers, publishes them to
the test server, and then (1) runs `telesphorepoLogin` and `mvn telesphorepo:login`, approves the
code through the API, publishes a library and a Gradle plugin, and resolves them; (2) publishes as
"GitHub Actions" with a `jwebserver` container that serves a Keycloak token at
`ACTIONS_ID_TOKEN_REQUEST_URL`.

`TestNexus` runs a Nexus container (`sonatype/nexus3`) and starts the server with
`TR_NEXUS_SYNC_INTERVAL=12h`, so that no scheduled sync changes the counters. On each storage
backend it refuses a source with a wrong password, creates the source, maps a maven2, an apt and a
raw repo with `create: true`, and runs a first sync. It resolves the files with `mvn`,
`apt-get` and curl. It then adds new files to Nexus and runs a second sync: only the new files
count as imported, the others as skipped. While the repos mirror, a maven PUT, a raw PUT and a
Debian upload answer 409. After cut-over the same writes succeed, and sync answers 409. No
response holds the Nexus password.

`TestScale` (tag `scale`, `go tool task test scale`) seeds 100k and then 1M assets with `seed N`
on a named volume. It starts a server on the volume and measures the p95 latency over 400 runs of
each operation: Maven GET with `If-None-Match`, an asset list walk, a rare search, a common
search and token auth (`/api/me`). An operation fails when its p95 at 1M exceeds
`max(2 x p95 at 100k, p95 at 100k + 2 ms)`.

## Not yet built

See the milestones in the design doc (§17).

| Item | Milestone |
|---|---|
| Restart-persistence e2e for settings (`Server.Restart` exists) | open |
