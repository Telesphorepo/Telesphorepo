# Telesphorepo: system as built

This file describes what the code does today and what the code cannot say: how the parts fit,
the rules and their reasons, and the contracts with outside clients. It points to the code for
routes, columns and settings rather than copying them. `AGENTS.md` says when and how to update it,
and holds the working rules for coding agents; `README.md` is the short guide for people who run
and develop the server.

## Purpose

A self-hosted repository for Maven repos (including Gradle plugins), Debian (apt), Arch Linux
(pacman), RPM (dnf), NuGet and raw file repos. Each format is a module (see **Modules**); a new
format is one more module, and the core names no format. A repo is **hosted** (it stores what is
published to it), a **proxy** (Maven, Debian, Arch and RPM: it fetches files from a remote and
keeps them) or a **group** (Maven: one address for the files of several repos). It ships as one
Go binary with the web UI embedded. Metadata is in SQLite; blobs are on the local filesystem or in
any S3-compatible bucket (Cloudflare R2, SeaweedFS, AWS). It is not compatible with the Nexus API.

## Principles

- **The cost of a request does not grow with the size of a repo.** Every request-path query uses
  an index; lists use keyset pagination. No `OFFSET`, no `LIKE '%x%'`, no listing of storage in a
  handler. Work that grows with a repo's contents (a Debian index) runs in a background job on the
  smallest affected unit.
- **One process per data volume.** SQLite, the in-process locks and the debounced workers need
  exactly one server on a data volume.
- **Light.** One static binary (`CGO_ENABLED=0`), a distroless image, one SQLite file, one blob
  store. Few, mature dependencies, the standard library first (`go.mod`).
- **E2E tests only.** The suite runs the real image with real clients. No unit tests, no mocks.
- **Repo paths and storage keys use package `path`**, with forward slashes on every OS.

## Architecture

Each package's doc says what it holds. The parts that fit together:

- `cmd/telesphorepo-server` is the server, `cmd/telesphorepo` the publishing client. `changelog`
  reads the embedded `changes.toml` and imports only the stdlib, `BurntSushi/toml` and the root
  package, so the server and the CLI both use it (see **Change log**).
- The kernel: `internal/server` (lifecycle, client address, access log, host check),
  `internal/module` (the module contract), `internal/jobs`, `internal/route` (the route table and
  the one policy middleware), `internal/perm`, `internal/auth`, `internal/access` (the one repo
  access decision), `internal/settings` (everything but the boot env of `internal/config`),
  `internal/db` (SQLite in WAL, one writer connection, one schema per component, the plan guard).
- Bytes: `internal/storage` (fs or S3, hot-swappable behind `Live`), `internal/repopath` (the
  parser every route, file operation, import and upload checks paths with), `internal/blobs`,
  `internal/assets` (the byte pipeline and the read path, see **Byte pipeline**),
  `internal/proxy`, `internal/files`.
- `internal/repos`: `Create` and `Update` are the only changes of a repo record. Each checks the
  whole new state and saves it in one transaction with the format's `OnRepoChange`, the modules'
  repo fields (`repos.Field`) and one audit event.
- `internal/format` is the format contract: `Spec`, where each optional capability is a field
  that is nil when the format lacks it. A cached rendered document's ETag is the SHA-256 of its
  cache key (a revision of its rows or its sources' digests, plus the base URL), so a matching
  `If-None-Match` is a 304 before the body is built. `internal/version` gives each version scheme
  a `SortKey` whose byte order is the scheme's order, for keyset paging.
- `internal/modules/modules.go` is the one list of modules and the only file that names them.
  `internal/nexus` names no format; it calls the target format's `Importer`.
- `internal/containerengine` serves tooling and e2e and is not linked into the server.
- `web/` (embedded, see **Web UI**), `edge/` (the Worker; no e2e zone runs it), and
  `internal/clidist` and `internal/edgedist`, which embed the CLI archives and the Worker bundle
  that `go tool task` (`tools/task`) writes. `e2e/` holds the testcontainers zones, the scale zone
  and the macOS zone; the last two run the server as a local process.

## Modules

Every repository format and every core feature with routes is a module (`internal/module`).
`Register` runs once per process, before the database opens; each `Extender` call adds one entry
to the module's manifest. The server reads the manifests and never finds a capability by type
assertion. At most one module sets `ResolveClient`, and a `RepoField` serves only once its
module's `Init` ran.

A module's `Init` and jobs record audit events as the module's system actor. A module gives a
service to the modules that require it with `Host.Provide` or `module.ProvideAs[T]`; another gets
it with `module.Use[T]`, which panics when no required module provided a `T`. `module.Check`
refuses a bad, reserved or repeated ID, and a `Requires` entry that is not earlier in the list.

**What a new format uses.** A format module edits no core file. It sets the `format.Spec` fields
it needs and leaves the others nil: routes (`format.Standard`, or its own, plus a `RootPrefix`
outside `/<name>/`), a repo name rule, the byte pipeline hooks, `Rebuild`, the format route
policies and bearer tokens, its own schema, settings, permissions and repo fields, the UI
descriptor (see **Format UI descriptors**) with a `formatParts` slot for a part only it can draw,
and `Edge` (a format without it gets no Worker route, see **Edge delivery**).

Start order and dependencies (`internal/modules/modules.go`):

| ID | Requires | Notes |
|---|---|---|
| `core` (`internal/api`) | | The kernel's routes and jobs (see **Group counters**) |
| `audit` (`internal/activity`) | | See **Audit log** |
| `storage` (`internal/move`) | | Refuses a storage change while a move runs |
| `backup` | | No table: the backup folder holds its state |
| `network` (`internal/netrules`) | | Sets the engine's `access.Networks`; `network.restrict` makes restricted a public repo that has allowed networks |
| `signing` | | Provides `*signing.Keys` |
| `publish` | `audit` | Provides `publish.Minter`; loads the sessions and devices behind bearer tokens |
| `edge` | `storage`, `network`, `publish` | |
| `sso` (`internal/oidc`) | `publish` | |
| `nexus` | `publish` | A merge gives `publish`'s CI pipelines to its target |
| formats | `debian`, `arch`, `rpm`: `signing`; `docker`: `network`, `publish` | Docker publishes through the CLI's credential helper; the others with `telesphorepo publish` |

**Routes.** A module registers only under `/api/`, under `/<name>/` of each format it registers,
and under its root prefixes. Any other path, or a prefix two modules claim, panics at startup.
Every route under `/api/`, and every route that accepts a UI session, is wrapped in
`http.CrossOriginProtection`. `Mux.RepoRooted` notes the formats a repo domain can serve.

**Hosts and repo domains.** The handler chain is the version header, the client address, the
access log, the host check (`internal/server/hosts.go`), then the route table, so the access log
records a refused request too. The host check reads `r.Host` (never `X-Forwarded-Host`) against
the settings snapshot, so it costs a map lookup. A host of `repo_domains` is served in repo-domain
mode, unless it is the base URL host, a module host or a loopback name: such a line never takes
the host over, wherever it came from (`TR_REPO_DOMAINS` or a later change of `base_url` can make
one that a save would refuse); the server answers as itself and warns. With `base_url` empty every
host is served (setup needs it); else the base URL host, module hosts, loopback names and
`/healthz` are served, and anything else is 421.

Repo-domain mode only reads (405 otherwise). A path `P` becomes `/<format>/<repo>P` on the same
route and policy with its escaping kept, so `httpx.RepoPath` checks it as on the base URL. Its
policy reads no session cookie, a 401 always carries the Basic challenge, and no web UI page is
served there.

**Route policy.** Each route is registered with a `route.Policy` (`internal/route/policy.go`), and
no handler checks credentials. `route.FormatRead` accepts anyone who can read, including devices
and signed download links; `route.FormatWrite` accepts only a publish session. `SomeRepo` (a
repository permission on at least one repo) stands in for `Perm`, never for `AllPerms`.

The router (`internal/route/route.go`) panics at startup when:

- a policy that accepts a password, a read token, a device (`auth.CredDevice`) or a signed
  download link (`auth.CredLink`) has a method other than GET or HEAD, or a server permission
  (`Perm` or `SomeRepo`);
- a policy that accepts a link has no repo rule or an operation other than `Read`;
- a `Write` route accepts a credential other than a publish session;
- a `Bearer` route has a server permission, or, on a method other than GET or HEAD, a repo rule
  other than `Write` or `Delete`.

So a password never changes state and never reaches a permission route, also inside a bearer
token.

The middleware runs in this order and writes the first refusal: identify the credentials (the
session cookie only when the policy accepts a UI session); require one unless the route is
anonymous or names a repo; the fresh second factor (`X-Telesphorepo-Proof`; failures count against
the username like failed passwords); the server permissions; the repo and `access.Engine.Decide`.
On a `Write` route a password or a read token goes on to the repo step, so its answer is that
publishing needs a publish session. A format read of a repo that not everyone reads sets
`Cache-Control: private` before the handler runs, so no shared cache such as a CDN keeps any
answer of it. From the first step on, the context holds the caller's audit actor with the client
address.

**Access engine.** `access.Engine.Decide` is the one decision of repo access: the middleware, the
publish-session mint, the group read path and the access report all use it, and "may this caller
write here now" is always `Decide(..., Write)` or `Decide(..., Mint)`. In order:

1. The caller's permissions on the repo: grants, `repo.download` on a public repo, and
   `repo.download` when the client address is in the repo's allowed networks (for every
   credential and anonymous; an address only grants, never takes away). A publish session holds
   on its own repo `repo.download` and `repo.publish` (CI), or its user's current repo
   permissions (device or manual); elsewhere what anonymous holds. A bearer token is limited to
   its scopes first, and with an open publish session in its scope it acts as that session.
2. The repo kind: `Write` and `Mint` on a proxy or group, and `Delete` on a group, are 405 to a
   caller who can download. A proxy's cached files can be deleted; an API file delete on a group
   decides `Delete` on each member (see **Group deletes**).
3. The permission: `Write` without an open publish session of this repo is 401.
4. An active repo operation (see **Repo operations**) refuses `Write`, `Delete` and `Mint` with 409.

`Engine.GroupMembers` is the one member list of a group read: each member the caller may read.
The group's allowed networks reach every member; a member's own networks grant reads of it
directly and through any group. A bearer token names the group, not the members, so the members
are checked with the credential behind it without its scopes (`auth.Principal.InGroup`). That
copy exists only inside a group read the token passed, so the token reads no member directly.

**Core bearer tokens.** `trb.<payload>.<HMAC-SHA256>.<Ed25519>`, valid at most 15 minutes
(`internal/auth/bearer.go`). Both keys are HKDF-derived from the master key. The edge Worker
checks the Ed25519 signature with the public key in its `config.json`, so the HMAC key never
leaves the server; the server checks both, so a token is valid in one spelling only. A UI session
gets no bearer. A scope keeps `Write` or `Delete` only with a publish session of the credential,
so a bearer from a password, a read token or anonymous only reads, and a bearer writes to one repo
at most. Each request loads the credential again (user enabled, token present, session open,
device not revoked, current permissions), so revoking it ends its bearers at once. A device reads
what its user reads and is treated like a password.

**Device proof on format reads.** A `FormatRead` route also accepts a device proof
`trx.<device id>.<nonce>.<signature>` as the Basic password or Bearer value, signed over an
unexpired exchange nonce of the device (`publish.Service.VerifyDeviceProof`). Since devices pass
only GET and HEAD routes without a server permission, a proof never writes. A failed proof counts
against the client address. A signed download link is read the same way from the query (see
**Signed download links**).

**Minting a publish session.** `publish.Minter.Mint` (`internal/publish/mint.go`) checks the
credential (a device signature over a challenge nonce, a CI identity token a trust policy of the
repo allows, or a UI session with a fresh second factor), runs `Decide(..., Mint)`, and records
the session. An exchange nonce lives as long as a publish session and is not used up: a token
exchange sends the same proof on each token request of one push, so it mints one session per
repo and later requests get that session back while it is open (its secret is derived from the
nonce, so the token is the same). A CI token that minted a session likewise gets it back without the `iat` age
or `exp` check, since the open session bounds the time and a long push requests tokens midway. A
first mint needs `iat` at most 10 minutes old and `exp` ahead.

**Format UI descriptors.** `GET /api/formats` answers, per format, everything the web UI shows or
chooses per format, so no shared UI code names a format. `format.UIDescriptor`
(`internal/format/ui.go`) lists the fields. In a `rules` config field, the first rule whose glob
matches a path decides. Globs are case-sensitive, and three copies implement them that must
agree: `format.Glob`, `web/src/lib/glob.ts` and `edge/src/glob.ts`.

**Snippets and CI files.** A format's `Usage` gives the use and publish snippets; the core adds
the CI files, one builder per kind in `internal/ci/workflow.go`. Every CI file installs the CLI
first, and the CLI reads the job's identity token itself, so no CI file fetches or prints a token.
Docker pushes with `docker push` through a Docker config that names the CLI's credential helper,
with `TELESPHOREPO_SERVER=<base_url>` so the helper knows the server.

**Lifecycle.** `server.RunWith` starts in this order:

1. logger and data dirs; `db.Open`;
2. every `Register` (then the permission registry is fixed and the registries are built);
3. the migrations of the kernel and of each module, in start order (see **Schema migrations**);
4. `settings.Load`; the kernel services;
5. each module's `Init` in start order, then the token kinds and jobs;
6. the routes; the dev command's `Options.Init`; the bootstrap admin;
7. every job's `Start` in start order (an error stops startup);
8. listen; every job's `Run` and `Tick` loops.

On-demand tasks (a storage move, a Nexus sync, a repo delete, each index rebuild) run with
`Host.Go` under their module. At shutdown the server stops `http.Server` (30 s, so a detached
commit can finish), then stops each module in reverse start order and waits for its jobs and
tasks, then the read tokens' `last_used_at` writes, and closes the database. No worker runs after
the database closes.

## Data model (SQLite)

Each schema component has its own migrations: `internal/db/migrations` for the kernel (component
`core`), and `internal/<module>/migrations` for each module with tables (`Extender.Schema`; the
component is named after the module, e.g. `storage` in `internal/move`, `network` in
`internal/netrules`). The migrations define every column, key and index; this section says what
each table is for and the rules its keys carry. A module schema may reference the kernel tables
and those of the modules it requires; the kernel references no module table. All tables are
`STRICT`; tables with a natural key are also `WITHOUT ROWID`. Every foreign-key child column has
an index. SQLite runs `PRAGMA optimize` at close and every 6 hours (job `core.optimize`).

**Schema migrations.** A migration is an embedded SQL file `NNNN_name.sql` (from `0001`, no gap).
`schema_migrations` holds one row per applied migration with the file's SHA-256 (CRLF read as LF;
`.gitattributes` checks out `*.sql` with LF). `db.Init` (`internal/db/migrate.go`) runs per
component in start order, the kernel first:

1. An applied version above the newest file is `db.ErrDatabaseNewer`: run that version or newer.
2. An applied migration whose file has another name or checksum is `db.ErrMigrationChanged`:
   restore the file and make the change in the next migration.
3. Each newer file runs in order, in its own transaction with its `schema_migrations` row. The
   transaction runs with `PRAGMA foreign_keys = OFF`, so a migration can rebuild a table (create,
   copy, drop, rename) without the drop cascading. Before the commit, `PRAGMA foreign_key_check`
   runs on the tables the migration touches and those whose foreign keys point at them (a
   migration costs what it touches, not the database size); an orphaned row fails it. A failed
   migration leaves no row, so the next start runs it again.

On these errors and `db.ErrDevDatabase` (`db.IsIncompatible`) `serve` refuses to start;
`telesphorepo-server dev` deletes and recreates its data dir.

Kernel tables (`internal/db/migrations`):

- People and sign-in: `users` (argon2id hash; `!` for SSO users, which never verifies),
  `sessions` and `api_tokens` (both store SHA-256 of the secret; `api_tokens` are **read tokens**:
  they never publish; `last_used_at` is written at most once per minute), the second-factor tables,
  and `auth_challenges` (short-lived single-use state; a new challenge deletes up to 100 expired
  ones). Sealed values use AAD `<table>:<id>`.
- Permissions: `user_permissions` (server permissions) and `repo_permissions` (per repo). No
  CHECK on the ID: writes check it against the permission registry; reads skip an ID no module
  registers. See **Permissions**.
- Repositories: `repos` holds the counters `file_count`, `total_size`, `last_published_at`;
  each list of `GET /api/repos` reads one of its indexes, with partial indexes for
  `anonymous_read = 1` (see **Repository lists**). Module repo fields (`repos.Field`, e.g.
  `signing_key`) are rows in a table of their module keyed by the repo. `repo_totals` is one row
  of summed counters, kept by triggers on `repos` in the same transaction, so `GET /api/overview`
  reads one row. `repo_redirects` holds old names (a name is a repo or a redirect, never both; see
  **Repository redirects**). `repo_group_members` lists a group's members in read order; a deleted
  member leaves its groups. `repo_proxies` is the remote of a proxy repo (credentials sealed).
- Blobs and assets: `blobs` are content-addressed (one blob backs any number of assets in any
  repos); `refs` counts the asset rows pointing at it, and the partial index on `refs = 0` feeds
  blob GC after `blobs.gc_grace`. `assets` is every stored file: a path pointing to a blob, with
  size and hashes copied from the blob so a download reads no other row. Only the byte pipeline
  writes assets. Triggers keep `blobs.refs` and the repo counters in the transaction of each write;
  `last_published_at` moves only on a non-`index` insert or replace, and a delete never lowers it.
  `assets.unit` (`format.Spec.GroupUnit`) is the part of a repo a group serves from one member (the
  package for npm, PyPI, Cargo, NuGet; `''` path by path; NULL until job `core.group-counts` sets
  it). `builds` are CI build records (see **CI providers and build records**); an asset's
  `build_id` is SET NULL when the build goes.
- Group counters: `group_counts`, `group_count_members`, `group_count_pending`: a group's
  counters per set of members a reader reads; a reader never reads a pending row. See **Group
  counters**.
- Search: `assets_search` (FTS5 trigram) and `assets_segments` (FTS5 unicode61 on path
  segments) are contentless, kept by triggers, and skip `index` rows. Their rowid is
  `repo_id << 40 | id`, so one repo's matches are contiguous and a search page is one seek (see
  `GET /api/search`).
- Tree: `tree_entries` holds one row per direct child of each folder for
  `GET /api/repos/{name}/tree`, ordered by `repopath.NameKey` (`internal/repopath/sortkey.go`). A
  folder row counts its files and goes with the last of them. The byte pipeline writes rows in the
  transaction of each asset insert and delete; a replace changes nothing.
- Operations and queues: `repo_ops`: at most one operation per repo (UNIQUE `repo_id`);
  an ended operation's row is deleted. `upload_sessions`: resumable uploads, bytes in
  `$TR_DATA_DIR/tmp/uploads/<id>`; `publish_session_id` has no foreign key because that table
  belongs to the publish module. `rebuilds`: the durable rebuild queue of units a format builds
  again (Debian, Arch, RPM).
- Settings: `settings` (see **Encryption** under **Settings model**).
- Activity: `audit_events` is the only table that stores or shows an IP address; `detail` is
  never a secret (see **Audit log**). `download_stats` (per repo and UTC day) and the window tables
  `download_window_state`, `download_window_days`, `download_window_repos` (last 30 days, kept by
  triggers on `download_stats`, with an index for the top repos) are described in **Download
  statistics**.

Module tables (each in `internal/<dir>/migrations`):

- `storage` (`internal/move`): `storage_moves` (a UNIQUE partial index allows one copying move at a
  time; the target settings are sealed) and `storage_move_failures`. See **Storage move**.
- `network` (`internal/netrules`): `ip_lists`, `repo_networks` (a repo without rows has no allowed
  networks; a list in use is `ON DELETE RESTRICT`), `trusted_proxy_lists`. `list:<name>` entries of
  `TR_TRUSTED_PROXIES` resolve at startup and are not rows. See **Network rules**.
- `signing`: `signing_keys` (private key sealed, no passphrase) and `repo_signing_keys` (the repo
  field `signing_key`; `ON DELETE RESTRICT`, so a key in use cannot be deleted).
- `publish`: CLI devices and their enrollments, CI trust policies, publish nonces and sessions.
  Secrets and nonces are stored as SHA-256. `publish_oidc_seen` keys a CI token by
  `<issuer>\n<jti>` (the whole token without a `jti`) and repo, so a second insert is a replay.
- `edge`: `edge_outbox`, `edge_written`, `edge_paths`, `edge_reclass`, `edge_state`. They reference
  no table, so an outbox row outlives a deleted repo and still deletes its pointers. An outbox
  row's `seq` rises with each new row of the key, so the worker drops only the row it read. See
  **Edge delivery**.
- `nexus`: sources, the repo mapping, syncs, switch-overs, rehearsals, moves and merges
  (`nexus_merges.source_repo_id` has no foreign key: the merge deletes that repo). See **Nexus
  sources**.
- Format modules (`maven`, `debian`, `arch`, `rpm`, `npm`, `nuget`, `pypi`, `docker`, `cargo`)
  keep the index rows their protocol serves, kept by the format's hooks (see each format's
  section) and cascading with the repo or asset. A version order is a byte key (Debian,
  Arch and RPM in their `order.go`), so a page of versions is an index seek. Arch and RPM keep the
  SHA-256 state after the signed part of a package, so the server signs it again without reading
  the file. NuGet, PyPI and npm revisions are `max(rev + 1, unix ns)`, so a package removed and
  pushed again never reuses one. NuGet's package search keys its FTS5 rowid like `assets_search`.

Every query on a request path uses an index. Lists use keyset pagination (`?after=` and
`?limit=`), never `OFFSET` or `LIKE '%x%'`.

**Queries.** The kernel and each module with a schema keep their SQL in `queries/*.sql` next to a
`sqlc.yaml` whose `schema` lists its own migrations, the kernel's, and those of modules whose
tables it reads. `go tool task gen` writes each component's typed `dbq` package. Queries sqlc
cannot express (FTS5 `MATCH`, streaming rows with `iter.Seq2`) are hand-written in `dbq/extra.go`
and listed in its `Extra` map.

**Plan guard.** `db.CheckPlans` (`internal/db/plan.go`) runs `EXPLAIN QUERY PLAN` for every query
of every component on a database with every schema. A SCAN of a table and any TEMP B-TREE fail; a
SCAN of a partial index or a virtual table passes. A query that must scan goes in the component's
`Allow` map with its reason (each is off the request path: startup, once per backup or move, a
status poll); an `Allow` entry for a missing query or a clean plan also fails. A query that writes
a table outside its component's schema fails: a module writes only its own tables, and kernel
tables change only through kernel services. `TestQueryPlans` runs the guard through the hidden
`dbcheck` command.

## Storage layout

| Location | Contents |
|---|---|
| `$TR_DATA_DIR/telesphorepo.db` | SQLite database (WAL) |
| `$TR_DATA_DIR/master.key` | Master key (mode 0600), generated on first start unless `TR_MASTER_KEY` is set |
| `$TR_DATA_DIR/tmp/` | Upload spool, on the data volume rather than tmpfs; resumable uploads and their hash state in `tmp/uploads/`, held plan heads in `tmp/plans/` |
| `$TR_DATA_DIR/blobs/` | FS blob root (default of `fs.root`) |

Blob keys are the same on every backend: `blobs/sha256/<first 2 hex>/<sha256 hex>`, built with
package `path`; S3 keys get the optional `s3.prefix` in front. Asset rows hold the paths and
content types. The FS backend writes a temp file, syncs it and renames it into place. When the
spool is on the FS root's volume, the spool itself is synced and renamed into place (`Adopt`), not
copied.

The S3 backend (`internal/storage/s3.go`) uploads from 64 MiB in parts of equal size but the last,
as R2 requires, and aborts a failed upload. Its retryer has no shared retry quota, so a store
outage does not make every later call fail at once.

Debian pool path: `pool/{component}/{prefix}/{source}/{package}_{version-without-epoch}_{arch}.deb`,
where `{prefix}` is the first letter of the source, or `lib` plus the 4th letter for `lib*`.
Arch pool path: `pool/{name}/{name}-{version}-{arch}.pkg.tar.{zst|xz|gz}` with the server's
signature at `<path>.sig`; an architecture's indexes are `{arch}/index.db` and `index.files`, each
with `.sig`. RPM package path: `Packages/{name}/{name}-{version}-{release}.{arch}.rpm` (signed by
the server); repodata is `repodata/repomd.xml`, `repomd.xml.asc` and
`repodata/<sha256>-{primary,filelists,other}.xml.gz`.

## Settings model

| Layer | What | Where |
|---|---|---|
| Boot env | `TR_DATA_DIR`, `TR_LISTEN`, `TR_MASTER_KEY` | `config.LoadBoot`. A change needs a restart |
| Bootstrap admin | `TR_BOOTSTRAP_ADMIN_USER` and `TR_BOOTSTRAP_ADMIN_PASSWORD`, set together | Used only when the users table is empty |
| Stored settings | Everything else: the modules' settings groups | Edited in the **Settings** pages or through `/api/settings` |
| Env override | `TR_<KEY>`: dots become `_`, upper-cased (`s3.bucket` → `TR_S3_BUCKET`) | Wins over the stored value; read-only "set by environment" in the UI |

Precedence: env override, stored value, default. `telesphorepo-server dev` uses a setting's `Dev`
value instead of `Default`. Setting types and their input syntax (sizes with binary or decimal
units, durations such as `90 days`) are in `internal/settings/units.go` and `image.go`. An `image`
setting, like a secret, reads back as `{set}`, never the bytes.

**Groups.** Each module owns its settings groups and registers them with `Extender.Settings`; the
`settings` package holds no feature rules and declares only the `general` group
(`internal/settings/general.go`). A `settings.Group` names the server permission that reads and
edits all its keys, the UI page that edits it, its definitions (`settings.Def`, with defaults and
per-key validation) and owner hooks:

- `Validate(draft, mode)`: rules across keys. Runs at startup on stored values and the environment
  (`ModeStartup`) and on every PATCH and test (`ModeChange`). A stored secret that does not decrypt
  arrives as `draft.Broken(key)`, with the default in its place.
- `Test(ctx, draft)`: a check against the outside world (`POST /api/settings/{group}/test`).
- `Apply(ctx, call)`: runs before a PATCH of the group saves. It may take the change over: hold
  keys back and answer the PATCH itself (the storage module answers 202 with the move).
- `Service.AddCheck(group, fn)`: a check of a change that needs services outside the owner (the
  server checks the repos of `repo_domains`). Runs after `Validate` on every PATCH and test, never
  at startup.

Services read a typed handle (`settings.Key[T]`) or a `settings.View` computed once per snapshot.
Each module's `Register` names its groups (e.g. `internal/move/module.go`,
`internal/oidc/settings.go`); the sections of those features give the reasons for their rules.

- **Encryption.** Secrets use AES-256-GCM with key = HKDF-SHA256(master key, info
  `telesphorepo settings v1`), AAD the setting key, a random 12-byte nonce. The definition says
  whether a setting is secret; the table has no flag.
- **Secrets over the API.** The API never returns a secret; it reports `{"set": bool}`. Secrets
  are never logged.
- **Live changes.** The effective settings are an immutable snapshot behind an atomic pointer,
  rebuilt on every write. Request paths never query `settings`. Subscribers (`Service.Subscribe`)
  react to changes: the log level updates and storage is hot-swapped.
- **A PATCH.** The server builds the whole new state, runs the `Validate` of every group the patch
  names a key of, then their `Apply` hooks in group order, then saves the keys no `Apply` held back
  in one transaction.
- **base_url from the first sign-in.** While `base_url` holds its default and is empty, the first
  full UI sign-in (not a setup-only session) of a person holding `settings.general` saves
  `base_url` as the request's origin (`httpx.Origin`: scheme from a trusted proxy's forwarded proto
  or the connection, and the `Host`). A loopback or unspecified host is never saved: a browser on
  the server or an SSH tunnel does not use the public address. `settings.Service.Fill` saves it
  like a PATCH, under the writer lock and only while the key holds its default, so a later sign-in
  from another address changes nothing; the audit event names that person and `via: "first
  sign-in"`. A single sign-on sign-in does not set it. The web UI then offers the browser's address
  when it differs (see **Web UI**). Once set, a request to another host gets 421 (see **Hosts and
  repo domains**).
- **Repo domains.** `repo_domains` lists `<domain>=<repo>` lines; a domain must be a DNS name of
  two or more labels, once. The server's check refuses a domain the server answers for itself
  (base URL host, a module host, a loopback name) and, for each line the change adds or repoints, a
  repo that does not exist (a redirect name counts) or whose format has no GET route under
  `/<format>/{repo}/` (Docker: `/v2/`). Unchanged lines are not checked again, so a deleted repo
  does not block other general settings; its domain answers 404. `settings.DomainsOf(snapshot)` is
  the map domain → repo name.
- **Storage rules.** An empty `s3.endpoint` means AWS (so the region is required); an endpoint
  without a region signs for `auto` (R2, SeaweedFS). A PATCH that changes where the blobs are runs the storage probe first; nothing is
  saved if it fails, and a change while a move runs is 409. When the new settings address another
  location and assets exist, the server moves the blobs first (see **Storage move**). A change of
  only credentials, region, path style or redirect, or on a server without assets, is saved and
  swapped at once.
- **SSO rules.** SSO is on iff `oidc.issuer` is set; it then needs a confidential client. The
  redirect URL is `oidc.redirect_url`, else `base_url` + `/api/auth/oidc/callback`.
  `oidc.permission_map` maps values of the claim `oidc.permission_claim` to server permission IDs;
  a repository permission is refused. `signin.password_login=publishers` needs the issuer. A PATCH
  changing the provider fetches the discovery document first; on failure nothing is saved. The
  issuer is only whitespace-trimmed (trailing slashes must match the IdP).
- **SSO startup guard.** When the stored SSO settings fail at startup (e.g. a client secret that
  does not decrypt after a master key change), the server still starts: the `sso` group's
  `Validate` wraps `settings.ErrDegraded` and overrides `oidc.issuer` to `""` and
  `signin.password_login` to `all` in the snapshot, without saving, until a PATCH of the group
  passes `Validate`. A failing value from the environment stops the server.
- **Startup validation.** Env values must be valid at startup, or the server refuses to start; a
  refusal of a group's `Validate` stops it too. An invalid stored value is logged and ignored. A
  `TR_*` variable that matches no setting or boot variable (`TR_E2E_*` and `TR_ENGINE` excepted) is
  logged as a warning and ignored.
- **Signing keys.** Debian, Arch and RPM repos sign with the key of their repo field
  `signing_key` (see **Signing keys**). No setting holds a key.
- **Lost master key.** Without the master key the secret settings cannot be decrypted and must be
  entered again.

## HTTP surface

Each format mounts its routes through `format.Spec.Routes`, or the standard routes
(`format.Standard`, `internal/format/kit.go`) when it sets none: GET and HEAD
`/<format>/{repo}/{path...}` through the read path and PUT through `Commit` with source `put` (see
**Byte pipeline**). The core names no format. The format routes, their answers and their
protocol quirks are in each format's section; the routes of the JSON API are in each module's
`routes` (`internal/api/api.go` for the core, and the module packages: `internal/oidc`,
`internal/publish`, `internal/links`, `internal/netrules`, `internal/signing`,
`internal/activity`, `internal/backup`, `internal/move`, `internal/edge`, `internal/nexus`, and
the format packages for `/api/<format>/...`).

**Format routes.** Package clients use HTTP Basic. A **read** takes a password, or any username
with a read token as the password. A GET or HEAD read also takes the UI session cookie, so a
signed-in browser downloads from a private repo without a Basic prompt. A **write** (PUT or
upload) takes only a publish session token (`trs_...`) as the password (see **Publishing**); the
session cookie never writes. The routes use `route.FormatRead` and `route.FormatWrite` (see
**Route policy** and **Access engine**):

- Anonymous read is allowed when `anonymous_read` is on. Otherwise no credentials or bad
  credentials get 401 with a Basic challenge, valid credentials without permission get 403, and
  an unknown repo gets 404, but 401 for an anonymous caller, so repo names don't leak.
- A write to a repo with an active operation gets 409 `{"error": <the operation's message>}`
  after the permission check. A mirrored repo is read only until its switch-over.
- A write without an open publish session for that repo gets 401 with a Basic challenge and
  `{"error":"publishing needs a publish session: <base_url>/#/settings/tokens"}`. A device or
  manual session writes only while its user still holds `repo.publish` on the repo (else 403).
- A read of a proxy or a group follows **Group and proxy repositories**. A write (format PUT or
  upload, manual upload, upload plan) to a proxy or a group answers 405 with `Allow: GET, HEAD`
  and "A proxy is read-only." or "A group is read-only. Publish to one of its repositories." to a
  caller who can read the repo; any other caller gets the usual refusal. A proxy has no signing
  key, so its `key.asc` is 404.
- A format PUT answers 201 for a new path, 200 for an identical retry or an allowed overwrite,
  409 for different bytes where overwrite is refused, 413 over `max_upload_size`, and
  `ETag: "<sha256>"` when stored. `If-None-Match: *`, `If-Match: *` and `If-Match: "<sha256>"`
  make it conditional (412 when false; see **Preconditions** under **Byte pipeline**); the
  precondition comes before the overwrite policy. Raw's per-path overwrite rules are in its
  section; a delete is never an overwrite, so `deny` never stops a person with `repo.delete`.
- The package uploads (Debian, Arch, RPM: `PUT, POST /<format>/{repo}/upload...`) take the
  package as the body, or, with `?upload=<id>&sha256=<hex>[&size=<n>]` and no body, finish a
  resumable upload of the same publish session (404 for an unknown, expired or foreign upload;
  400 and the upload is deleted when the bytes do not match).
- A GET on S3 with `s3.redirect` answers 302 to a presigned URL. HEAD is never redirected.

Every download (`assets.Reader.ServeAsset`) carries `X-Checksum-SHA256`, and `X-Checksum-SHA1`
and `X-Checksum-MD5` when the asset row has them; a merged group file carries all three of the
merged bytes. Nothing hashes on a request. One byte range (`Range: bytes=a-b`, `a-` or `-n`, with
a matching `If-Range`) answers 206 with `Content-Range` on every store (`storage.Store.GetRange`:
a seek on fs, a `Range` request on S3); a range outside the file is 416; several ranges get the
whole file.

**Unauthenticated routes.** `GET /healthz` answers `ok` when the DB answers, on any host (see
**Hosts and repo domains**). `/install.sh`, `/install.ps1` and `/cli/...` (mounted by the publish
module; see **Install scripts and CLI downloads**) answer the problem `no-cli-downloads` on a
build without CLI archives; an install script refuses (400) a server address that holds a quote,
a space, a backslash or a control character, because the scripts quote it in single quotes.
`/` serves the embedded UI (see **Web UI**): `/assets/...` is `immutable` for a year, while
`index.html` and the SPA fallback (a path without a file extension) are `no-cache`; another path
with an extension that is not a file is 404. `GET /.well-known/telesphorepo-edge/check` is the
reserved path of the edge check (see **Edge delivery**).

**Listener.** HTTP/1.1 and HTTP/2: over TLS by ALPN, and on a plain listener with prior knowledge
(h2c, for a reverse proxy that speaks it). With `tls.cert_file` and `tls.key_file` set at startup
the listener serves TLS 1.2 or later; it reloads the PEM pair when a path or a file's modification
time changes, so a renewed certificate applies without a restart. Turning TLS on or off applies at
the next start. There is no HTTP/3.

**Basic challenge.** A 401 carries `WWW-Authenticate` only on a format route, so package clients
get their prompt. It never carries it under `/api`, nor for a request with `X-Telesphorepo-UI: 1`
(the web UI sends it on every request), so a browser never opens its password dialog for the UI.

**JSON API (`/api/`).** Auth is a session cookie or HTTP Basic; each route's policy says which
(see **Route policy**). The rules:

- Every route that changes state (POST, PUT, PATCH, DELETE) and every permission route (GET too)
  needs the UI session cookie (`tr_session`), and the router enforces it at startup. The session
  of a person who needs the second factor exists only after it (outside dev mode). A permission
  in a policy means the UI session of a person who holds it; `repositories.all` holds every
  repository permission.
- HTTP Basic with a password or a read token reaches only the GET routes that need no
  permission; elsewhere it gets 401 with the detail `sign in through the web UI for this
  action`, as does a request with no credential. The exceptions take no credential or their own
  token: sign-in, `/api/auth/config`, `/api/formats`, and the publish routes marked "none" or "token" in **Publishing**.
- A token of a registered kind (read token `trp_...`, publish session token `trs_...`) is read
  from the Basic password or from `Authorization: Bearer <token>`. A publish session token
  reaches only the routes whose policy accepts it, and only for its own repo:
  `DELETE /api/publish/sessions/{id}`, the upload routes (`/api/repos/{name}/upload`,
  `.../upload/plan`, the resumable `.../uploads...`) and the file delete routes
  (`.../files/delete`, `.../files/delete/plan`, `GET .../files/delete/{id}`). Every other `/api`
  route answers it with 403.
- A signed-in user without the permission gets 403.
- Bodies are capped at `max_json_size`. Lists are `{items, next}` with `?after=&limit=`: keyset
  pages, never an offset.

**Compression.** Every route under `/api/` answers with `Vary: Accept-Encoding`. A JSON answer
(`application/json` or a `+json` type) of 1 KiB or more goes out gzip-compressed, without
`Content-Length` and `Accept-Ranges`, when `Accept-Encoding` accepts gzip with a q above 0
(`httpx.GzipJSON`, `internal/httpx/compress.go`, which the router puts around each `/api/`
route). HEAD, `Range`, 204 and 304, an answer that sets `Content-Encoding` itself or has another
content type (a download), and an answer that flushes before 1 KiB stay as they are. The access
log records the compressed size.

**Problem documents.** Every error answer under `/api/` and `/cli/`, and of `/install.sh` and
`/install.ps1`, is an RFC 9457 problem document (`application/problem+json`,
`Cache-Control: no-store`): `{type, title, detail, status, remedy?}` plus the extension members of
the answer (such as `fields` for settings validation, `etag`, `used_by` or `ok`). An extension
member never replaces a standard member. `httpx.Error` and `route.Fail` write type `about:blank`
with the status text as title and the message as detail; an error that carries its own document
(`httpx.ProblemError`, e.g. `*assets.PreconditionError`) gets that document from `route.Fail`. A
problem with a stable name is declared once with `httpx.DeclareProblem`
(`internal/httpx/problem.go`); its type is `urn:telesphorepo:problem:<name>`, and a name declared
twice panics at startup. `remedy` is `{action, url?, after?, file?, version?}`, with `action` one
of `update_cli`, `update_server`, `login`, `setup`, `retry`, `open_url` and `fix_project`: the
CLI acts on it, so the actions are a contract across versions. Every 429 under `/api/` is
`rate-limited` with `remedy.after` from `Retry-After`. `precondition-failed` (412) carries `code`
(`precondition_failed`) and `etag` (the file's ETag now, or null); a format route answers the
same members with `error`. Grep `DeclareProblem(` for the rest.

Format routes keep the error form of their protocol: `{"error": "..."}` with the same extension
members, or the OCI errors of Docker (`Policy.Errors`). The web client
(`web/src/lib/api/client.ts`) shows `detail`, else `title`.

**Change log.** `changes.toml` at the repo root is the change log that the server and the CLI
both embed, so each side can tell the person what the other side lacks. Each `[[change]]` entry
has `version` (a release version such as `v0.6.0`, or `dev` before a release), `scope` (a command
such as `publish`, a command and a format such as `publish/npm`, or a helper such as
`helper/docker`; a scope matches itself and every scope under it), `needs` (`server` or `cli`: the
side that must be at least `version`; an unknown value counts as `cli`), `summary` (one sentence
for the person), and an optional `id` (a-z, 0-9 and `-`, unique) that names the entry in the CLI's
`olderPaths` and stays when the version is stamped. A reader ignores a field it does not know. A
`dev` entry counts for nothing. A broken change log stops the server at startup.
`go tool task tag <version>` writes the release version into each `dev` entry, commits the change
log and creates the tag; `release` and `cli` refuse a release version while a `dev` entry is left.
Release versions are semantic versions with an optional `v`. `GET /api/changes?since=` answers
the released entries newer than `since`.

**Server version.** Every answer carries `Telesphorepo-Version` with the version stamped into the
server binary (`-X main.version`), or `dev` for a build without a stamp.

**Bulk actions.** A write route that changes a set takes a list, and one item is a list of one, so
each change has one route: `PATCH /api/repos` (public or restricted), `POST /api/repos/delete`,
`POST /api/repos/{name}/files/delete`, `PATCH /api/users` (disable or enable) and
`PUT /api/grants`. A list holds 1 to 1000 items (400 outside that), and `max_json_size` applies
too. The server removes repeated items. Each route checks every item before it changes any: an
unknown name gives 404 with the name, and nothing changes. The row changes apply in one
transaction, so a failure leaves nothing half done. Each changed item gets its own audit event;
an item that already had the value gets none. A repo delete and a file delete remove files in
batches (see **Byte pipeline**), so they are the ones that are not one transaction.

**Route groups.** The groups below give the policy and the behaviour that a handler does not explain on its own.
Shapes and status details are in the handlers.

- **Sign-in** (`/api/auth/...`, `internal/api/api.go`, `internal/oidc/oidc.go`; none or the
  `tr_login` cookie). The password step gives a person who needs the second factor and has one
  the `tr_login` cookie and the list of methods; one without a factor gets a setup-only session;
  anyone else a full session (see **Auth model**). `POST /api/auth/login/second` takes a TOTP
  code, a passkey assertion or a recovery code; a wrong proof keeps the sign-in pending, and too
  many failures get 429. The first full session of a holder of `settings.general` on a server
  without `base_url` saves it (see **Settings model**). SSO users cannot sign in with a password
  (401); with `signin.password_login=publishers` a person who needs the second factor gets 403
  and must use SSO. Logout ends the local session only. `/api/auth/config` says which sign-ins
  exist; `second_factor_optional` is true only under `telesphorepo-server dev`.
- **Single sign-on** (`/api/auth/oidc/login`, `/callback`). The state, nonce and PKCE verifier
  live in memory for 10 minutes, single use, keyed by SHA-256(state), with the `tr_oidc` cookie
  (HttpOnly, SameSite=Lax, Path `/api/auth/oidc`). The callback verifies the ID token (signature,
  issuer, audience, expiry, nonce) and refuses state from before an issuer or client change. It
  upserts the user and, with `oidc.permission_claim`, sets their server permissions. Failures
  redirect to `/#/login?error=<code>`. `GET /api/auth/oidc/device?code=` (UI session of an SSO
  account) starts a fresh sign-in with `prompt=login` and `max_age=0` that approves a device (see
  **Publishing**): the callback starts no session, checks that the token's issuer and `sub`
  belong to the account that started it and that `auth_time` is present and at most 5 minutes
  old, then redirects to `/#/device?code=&grant=` or `/#/device?code=&error=<code>`
  (`stale_sign_in` when the provider ignored `prompt=login`).
- **Second factors** (`/api/security/...`, `internal/api/security.go`; UI session). TOTP setup
  shows the secret once and lasts 10 minutes; recovery codes (10) replace the old set and show
  once. `passkeys/assert` gives a fresh check before an approval or a manual session. Passkeys
  need `base_url` (409 without it).
- **The caller** (`GET /api/me`, `GET /api/me/access`; password, read token or UI session,
  setup-only too). The session view is the same shape as `session` of a finished sign-in.
  `repo_permissions` holds every repo where the person has a grant, `repo.download` included,
  read in one query of the grants joined to `repos`; a holder of `repositories.all` gets `{}` and
  `all_repos: true`. `permissions_managed` is true for an SSO account while
  `oidc.permission_claim` is set.
- **Read tokens.** `/api/tokens` (list: any credential; create and delete: UI session). The token
  shows once. A token cannot mint tokens. SSO users cannot create one (403).
- **People** (`/api/users`, `/api/administrators`, `/api/people`; `people.manage`, a UI session
  may change its own password; `/api/people` also `repo.access` on a repo). The rule throughout:
  a caller gives, takes or touches only permissions they hold (403), and changes another person's
  password or disabled state only when they hold every permission of that person. The server
  refuses (409) a change that leaves no enabled holder of `people.manage`, or that takes a server
  permission from, or disables, the only enabled administrator (a person who holds every server
  permission; a server without one refuses nothing here). Nobody disables their own account
  (400). Single sign-on owns the permissions of an account it manages (409). A disabled person's
  sessions end and their password and read tokens stop working. The user list and
  `/api/people` are keyset by username, `?q=` one range of the username index; `/api/people`
  answers at most 20 enabled usernames per page.
- **Registry and identity** (none): `GET /api/permissions` (the permission registry, see
  **Permissions**), `GET /api/formats` (the UI descriptors, see **Format UI descriptors**),
  `GET /api/identity` and `GET /identity/logo` (see **Server identity**). The logo answers with
  `X-Content-Type-Options: nosniff` and a sandbox CSP. `PUT /api/identity/logo`
  (`settings.general`) takes the type from the bytes, never from a header or a name, and refuses
  SVG; it saves like `PATCH /api/settings`.
- **Repositories** (`/api/repos...`, `internal/api/api.go`, `repolist.go`, `repokinds.go`,
  `redirects.go`). Read: any credential or anonymous, each repo filtered by what the caller may
  see; create, delete, rename and redirects: `repositories.all`; settings, members, proxy test,
  network rules: `repo.settings`. The list is a keyset page by name of the repos where the caller
  holds a permission plus the public ones (see **Repository lists**); `?sort=published` is
  bounded (at most 50) and takes no cursor or filter but `perm`. A list item's `downloads` (last
  30 UTC days) is one seek of `download_stats` per repo of the page and lags memory by up to a
  minute (see **Download statistics**). `file_count`, `total_size` and `last_published_at` come
  from the repo row; a group's counters follow **Group counters** (`counts_pending` until
  counted). A repo update (`PATCH /api/repos/{name}`, `repos.Service.Update`) checks the whole
  new state first, then saves it in one transaction with the field values, the format's
  `OnRepoChange` and one `repo.update` event. A new proxy's remote is checked first (502 unless
  `force`). A proxy's password is never returned. A group has 1 to 50 members of its own format.
  `PATCH /api/repos` refuses to make public a repo that has allowed networks. `POST
  /api/repos/delete` checks every repo and its operation, gives every repo its `delete`
  operation in one transaction, answers 202, and runs each delete as a task of the core module
  (see **Repo operations**); the repo shows progress in `operation` until it answers 404.
  `operation/resume` and `DELETE .../operation` resume or clear a failed operation. Rename
  (`repos.Service.Rename`) leaves the old name as a redirect (see **Repository redirects**).
- **Overview** (`GET /api/overview`; UI session). Every figure is a counter or a bounded query:
  totals from `repo_totals` for a holder of `repositories.all`, else one lookup per grant; the
  largest eight by the `(total_size, id)` index; people counted up to 10,000.
- **Access** (`internal/api/access.go`). `GET /api/repos/{name}/grants` (`repo.access`);
  `GET /api/repos/{name}/access` (a repository permission that manages the repo: one with a
  second-factor need) is the access report built from the parts of `access.Engine.Decide`, with
  a sentence for each answer and the notes that modules add (`access.Engine.AddDownloadNote`).
  `GET /api/users/{username}/access` (`people.manage`) and `/api/me/access` are keyset pages by
  repo name, read as one page of each side (the public repos' index and the person's grants),
  never the repos in between. On a proxy or a group nobody holds `repo.publish` or
  `ci.pipelines`. `PUT /api/grants` (`repo.access` on each repo) sets the permission set of every
  user and repo pair, several groups of pairs in one transaction, at most 1000 pairs; it refuses
  a server permission, a permission without one it needs, and a change to a permission the
  caller does not hold on that repo. The auth service records one `grant.set` per changed pair in
  the transaction.
- **Files** (`internal/api/files.go`, `groupfiles.go`; `repo.download` to read). Listings
  (`assets`, `tree`, `file`) are keyset by path and a prefix is a range scan; a group merges the
  pages of the members the caller reads, each from the same cursor, and names the serving member
  in `repo` (see **Files, uploads and deletes**). `POST .../links` (`repo.download`, UI session)
  makes a signed link (see **Signed download links**, `internal/links`). `POST .../files/holders`
  says which members of a group hold an item and whether the caller may delete there (see
  **Group deletes**). `POST .../files/delete/plan` deletes nothing and walks at most 100,000 files
  by the `(repo_id, path)` index. `POST .../files/delete` (`repo.delete`, with a UI session or a
  device or manual publish session of the repo; a CI session never deletes) answers 200 when it
  ends within `delete.wait`, else 202 with an id and goes on in the background; a repo runs at
  most 4 deletes of files at once (429). The state of a 202 delete lives in memory: gone 10
  minutes after it ends and at every restart. A server stop ends a running delete between two
  remove batches (each one transaction), so each file is deleted or kept.
- **Uploads** (publish session of the repo; see **Files, uploads and deletes**).
  `POST .../upload/plan` stores nothing: the client sends the first bytes of the files that a
  `reads` rule of the format matches, and the server holds them under `heads` for 30 minutes
  after the last use, for the same session and repo, at most 1 GiB in all, least recently used
  first out (`internal/api/planheads.go`); a plan that names `heads` sends only new parts, and
  409 `plan-heads-gone` tells the client to send them again. `POST .../upload` runs the format's
  `Upload.Plan` then `Upload.Store` through the byte pipeline (source `ui`). The resumable
  upload (`/api/repos/{name}/uploads`, only the session that started it) appends at the offset of
  `Content-Range` (416 with `received` otherwise) and finishes with `PUT ...?path=&sha256=`, which
  reads the file once for its hashes and commits it like a format PUT, precondition included; a
  failed precondition keeps the upload for 15 minutes to finish again. A Debian repo finishes a
  resumable upload through its own upload route.
- **Networks and signing keys** (`internal/netrules`, `internal/signing`; `network.manage` or
  `signing.manage`, with read access for `repo.settings` on a repo). See **Network rules**,
  **IP lists** and **Signing keys**. A list or key in use is not deleted (409 naming its
  users). `GET /api/network/whoami` (anyone) echoes only the caller's own address, so it may set
  `Access-Control-Allow-Origin` to the base URL's origin: the Access tab reads it from each probe
  host with a plain cross-origin GET without credentials.
- **Activity** (`internal/activity`; `activity.read`): the audit log, newest first, and the
  registered actions (see **Audit log**).
- **Search** (`GET /api/search`; anyone). See **Search** for how a query picks the segment index,
  the trigram index or a phrase. Only repos where the caller holds `repo.download` (a grant, a
  public repo, or allowed networks holding the client address, one lookup of the network
  module's index). A page is one seek past `after`, and a repo the caller cannot read is one seek
  past its end, so every page is full until the end and costs the same at any size. The first
  page counts each repo's matches up to 1,000, seeking from one repo with matches to the next.
- **Repo details** (`repo.download`): `status` (pending index rebuilds), `usage` and `ci` (the
  snippets and CI files, built by `internal/ci/workflow.go`; none to publish for a proxy or a
  group), `versions`, `builds` and `snippets` (see **Versions and builds** and **Group
  versions**), and the format detail routes `/api/arch|rpm/{name}/package` and
  `/api/docker/{name}/manifest`.
- **Settings** (`/api/settings`; the permission of each group). The server runs the `Validate`
  of every touched group, then each `Apply`, then saves (see **Settings model**). `null` resets a
  key; `""` for a secret means unchanged. An env-locked key with a different value is 409. A
  location change must pass the probe and an SSO connection change must pass discovery; a
  location change that starts a storage move answers 202. One `settings.update` event names the
  saved keys, never the values. `POST /api/settings/{group}/test` validates and tests merged
  values and saves nothing.
- **Backup** (`internal/backup`; `backup.manage`). The job state is of this process, so `last_*`
  is lost at a restart.
- **Storage moves** (`internal/move`; `storage.manage`). A move never shows a secret. A retry
  rebuilds the settings PATCH that makes the sealed target the active storage and saves it like
  `PATCH /api/settings`; the old move is dismissed. See **Storage move**.
- **Edge** (`internal/edge`; `edge.manage`). Status, check, deploy, speed test and the Worker
  downloads; see **Edge delivery**. A check and a speed-test pass record no audit event: they
  change nothing on the server and grant no access. A check runs at most once per 10 seconds
  (429 `edge-check-busy`).
- **Nexus** (`internal/nexus`; `nexus.manage`, and `repositories.all` for merges). Sources,
  mappings, syncs, cut-over, rehearsals, moves and merges; see **Nexus sources**. A mapping, a
  sync or a cut-over is refused (409) while a sync runs or after cut-over; a source is deleted
  only while no sync runs and it mirrors no mapped repo (or after cut-over).

## Client setup

The UI shows a setup snippet per client on each repo (`/usage`), and `telesphorepo-server dev`
prints them; each format writes its own in `internal/<format>/usage.go`, and the CLI and CI
snippets come from `internal/format/snippets.go`. Reads take a user and password, or any username
with a read token as the password. Writes take a publish session, which the Telesphorepo CLI gets
itself (see **Publishing**): the publish snippets run `telesphorepo publish` (NuGet also allows
`dotnet nuget push` with a publish session as the API key), and Docker, Helm and ORAS push through
the CLI's credential helper, which the setup line of the Publish tab adds to the Docker config
once.

No snippet puts a token in a process argument. curl reads a config from stdin (`printf ... | curl
-K -`; `printf` is a shell builtin) and sends a JSON body that holds a token with `--data @-`. A
snippet for a person reads a token at a hidden prompt (`read -rs TOKEN`), so it stays out of the
shell history; wget uses `--ask-password`. No CI file fetches, prints or passes a token: the CLI
and its helper read the job's identity token themselves. A file that holds a credential has mode
600; the apt file gets it before the token is written (`sudo install -m 600 /dev/null FILE`, then
`sudo tee -a`).

Client behaviours the snippets work around:

- **Maven** 3.8.1+ blocks `http://` on non-localhost hosts. **Gradle** ignores the settings
  file's repositories when the build file declares its own, so dependencies go in the build file
  and plugins in `pluginManagement.repositories`.
- **pacman** sends the user information of a `Server = https://USER:READ_TOKEN@host/...` URL as
  HTTP Basic, so a private repo's section includes a mode-600 file with that line.
- **dnf** gets `metadata_expire=1h`, so a release reaches `dnf upgrade` within the hour (the
  default is 48 hours). dnf5 sends no credentials for a `gpgkey` URL, so a private repo's key is
  fetched with curl and named by `gpgkey=file://`.
- **apt**: indexes rebuild after `index.debounce`; a script polls `GET /api/repos/{repo}/status`
  for `pending: 0`.
- **NuGet**: dotnet sends `packageSourceCredentials` only after the first 401.
- **pip** ignores an http index on any host but localhost without `trusted-host`. A hosted PyPI
  repo holds only its own packages; a group of it and a PyPI proxy also serves their dependencies.
- **Raw**: a private repo's snippet leads with `telesphorepo get`, which reads as the account of
  `telesphorepo login`.
- **Yarn 2+** sends no token on reads without `npmAlwaysAuth: true`, and Yarn 4 installs a
  version only once it is a week old, so a hosted repo sets `npmMinimalAgeGate: 0` and the note of
  a proxy or group says to list your own packages in `npmPreapprovedPackages`.

### Install scripts and CLI downloads

The server serves the Telesphorepo CLI of its own build. The `build`, `dev`, `cli` and `release`
tasks (and the image build) compile `./cmd/telesphorepo` into `internal/clidist/dist` before the
server, one archive per OS and CPU with `SHA256SUMS`. Without `cmd/telesphorepo` there are no
archives, and `/cli/`, `/install.sh` and `/install.ps1` answer the problem `no-cli-downloads`
(404).

```sh
curl -fsSL HOST/install.sh | sh [-s -- CLI ARGS...]
```

```powershell
& ([scriptblock]::Create((irm HOST/install.ps1))) [CLI ARGS...]
```

The server writes `HOST` (the `base_url` setting, else the scheme and host of the request) and the
SHA-256 of each archive into the script (`internal/publish/install.sh`, `install.ps1`). The script
downloads the archive for the OS and CPU from `HOST/cli/` and refuses it, changing nothing, when
its SHA-256 differs. It installs into `~/.local/bin` (Windows: `%LOCALAPPDATA%\Programs\telesphorepo`,
added to the user `PATH`) and makes `docker-credential-telesphorepo` next to it: a symbolic link,
a hard link on Windows, a copy when a link fails. On Windows a running `telesphorepo.exe` is
renamed to `.old` first. In GitHub Actions the script appends the folder to `$GITHUB_PATH`.
Arguments after the script go to the CLI, which runs once the install is done; `install.sh` gives
it the terminal (`/dev/tty`) when there is one, because the script itself came on stdin.

**PATH.** When `~/.local/bin` is not on `PATH` (outside GitHub Actions), `install.sh` picks the
start-up file of the shell that `$SHELL` names. On a terminal it asks before it appends the line;
with no, or no terminal, it prints the line to add. `install.ps1` also puts the folder on the
session's `$env:PATH`, so that session runs `telesphorepo` at once.

The web UI page **Install the CLI** (`/#/install`, see **Web UI**) shows the install lines, the
archives with their SHA-256 and the commands to install one by hand.

## Auth model

| Credential | Storage | Accepted on |
|---|---|---|
| Password | argon2id (`internal/auth/password.go`), at most 4 hashes at once; successful Basic checks are cached for 10 min under an HMAC key | Sign-in (`POST /api/auth/login`). As Basic auth: format reads, and the `/api/` GET routes that need no permission |
| Read token `trp_<12hex>_<secret>` | SHA-256(secret), looked up by id, constant-time compare | Basic password (any username) or Bearer: format reads, and the `/api/` GET routes that need no permission |
| Session `tr_session` | SHA-256(id), absolute `session_ttl` | `/api/`, and format reads (GET and HEAD) for downloads in the browser; never on a repo domain, where the policy does not read the cookie (see **Hosts and repo domains**). The only credential for permission routes and for routes that change state on `/api/`; it never writes to a format route. HttpOnly, SameSite=Strict, Secure per `cookie.secure`. The session of a person who holds `delete` may also delete files (see **Files, uploads and deletes**) |
| Publish session `trs_<12hex>_<secret>` | SHA-256(secret), constant-time compare | Basic password (any username) or Bearer on format routes of its one repo, `/api/publish/...`, and the upload and delete routes of its repo. The only credential that writes to a format route |
| Device (`auth.CredDevice`) | The enrolled device key; a proof `trx.<device id>.<nonce>.<signature>` over a token exchange nonce, as the Basic password or Bearer value of a format read, or inside a core bearer token of a format's token exchange | Format reads (GET and HEAD) with the grants of the device's user, and writes through the publish session that the device minted for the repo (the bearer scope) |
| Signed download link (`auth.CredLink`) | Nothing stored: `?texp=&tsig=<user id>.<repo id>.<mac>` | Format reads (GET and HEAD) without an `Authorization` header, of exactly the signed path of the signed repo, until it expires, with the current grants of its maker. See **Signed download links** |

- **Account kinds.** `local` users have a password, API tokens and Basic auth: publishers and the
  break-glass admin. `oidc` users are created on their first SSO login, keyed by
  `(oidc_issuer, oidc_subject)`. They have no usable password, cannot create tokens, get 401 on
  Basic auth and password login, and cannot have their password changed (403, also `user passwd`).
  Their session only comes from SSO. They approve a device with a new SSO sign-in and publish
  through that device or from CI; they have no manual publish session.
- **Single sign-on.** A first SSO sign-in creates an `oidc` account with no server permission. The
  username is `preferred_username` lower-cased with chars outside `[a-z0-9._-]` mapped to `-`; if
  a different account owns it the login fails (`username_conflict`): accounts are never merged by
  name. A disabled account gets `account_disabled`. With `oidc.permission_claim` set, SSO
  **manages** the server permissions of every `oidc` account: each sign-in sets them to the union
  of `oidc.permission_map` over the claim's values (ID token, then UserInfo; a dotted path such as
  `realm_access.roles`) and records `user.permissions` with the actor kind `sso` when they change.
  A change that would leave no enabled person with `people.manage`, or take a server permission
  from the only enabled administrator, keeps the old permissions and is logged. Nobody can change
  managed permissions on the server (409; `permissions_managed: true`, read-only in the UI). Repo
  grants stay local for every account. With no claim, people manage the permissions on the server
  and a sign-in never changes them.
- **Second factor.** A local user may register several TOTP factors, several passkeys and 10
  one-time recovery codes (argon2id-hashed); `internal/auth/secondfactor.go` holds the parameters.
  A TOTP step that a factor accepted is refused again. A passkey's RP ID is the host of
  `base_url`, and the one accepted origin is its scheme and host. A sign count that does not go up
  passes, because a security key keeps one counter per protocol and a phone may use the other over
  NFC; the stored count keeps its highest value. The server requests no WebAuthn extensions and
  ignores the extension outputs a browser returns anyway (Safari on iOS adds `appid`), and logs
  `passkey rejected` with go-webauthn's reason for every failed check. Recovery codes work only at
  sign-in. While `base_url` is empty, passkeys take the scheme and host by which the browser
  reached the server (`httpx.Origin`), so the first admin of a new server can add one before
  setting `base_url`; the passkey stays bound to that host. TOTP secrets are sealed with the
  settings key. Failed checks count against the `u:<username>` rate-limit key. SSO users have no
  second factor.
- **Sign-in.** A person who needs the second factor (see **Permissions**) and has one sends the
  password, then a second step with the cookie `tr_login` (HttpOnly, SameSite=Strict, 5 min). The
  state is a row in `auth_challenges`, so it survives a restart. A wrong code keeps the sign-in
  pending. Anyone else signs in with the password alone, also when they have a factor.
- **Where a factor is required.** The sign-in of a person who needs it, device approval, a setup
  code and a manual publish session need a fresh factor. Anyone else sets up a factor when first
  approving a device or creating a manual session. An SSO user signs in again at the provider
  instead.
- **Setup-only session.** A UI session of a local account that needs the second factor and has
  none reaches only the routes whose policy is `route.Setup` (`/api/me`, `/api/auth/logout`,
  `/api/security...`); every other route answers 403 `second_factor_required`. The break-glass
  `admin` follows the same rule. The `/api/security` routes need a UI session, so a password alone
  cannot add a factor, and cannot publish.
- **Dev mode.** `telesphorepo-server dev` calls `auth.SetDevMode`, an in-process flag: not a
  setting, not an env var, and not reachable from `serve`. In dev mode a person without a factor
  gets a full session, and a device approval or manual session without a factor passes; a factor
  that exists is still checked. `GET /api/auth/config` reports `second_factor_optional: true`. The
  e2e suite runs `serve`, so enforcement stays tested.
- **Where each credential works on the JSON API.** A route that changes state (POST, PUT, PATCH,
  DELETE) or a permission route (GET too) needs the UI session. HTTP Basic with a password or a
  read token reaches only the GET routes that need no permission (repos, tree, assets, search,
  status, usage, `/api/me`, own tokens, devices and publish sessions); anything else gets 401
  `sign in through the web UI for this action` without a Basic challenge. So a stolen password
  cannot change users, permissions, settings or trust policies: the session of a person who holds
  a permission for them needs the second factor. The router refuses at startup a policy that lets
  a password or a read token change state or reach a permission route (see **Route policy**). The
  exceptions are the routes with no credential or their own token: sign-in, `/api/auth/config`,
  `/api/formats`, device enrollment and poll, the publish challenge, the device and OIDC mints,
  closing a session with its token, and the upload and delete routes with a publish session. Dev
  mode does not change this rule.
- **Lost factors.** `telesphorepo-server user reset-2fa NAME` removes a user's TOTP factors,
  passkeys and recovery codes.
- **Permissions.** The registry (`internal/perm`) holds every permission; each module registers
  its **subsystems** and their permissions (`Extender.Permissions`). A permission `<subsystem>.<name>`
  declares the permissions of the same scope it needs, whether holding it needs a second factor,
  and (server scope) whether it gives every repository permission on every repository
  (`AllRepos`). A server-scope permission is held by a person (`user_permissions`), a repo-scope
  one on one repository (`repo_permissions`). The registry is fixed at startup; every write checks
  the IDs (400 for an unknown ID, the other scope or a broken need), and a read skips an ID that no
  module registers. There is no admin flag: an **administrator** holds every server permission.
  The first-start `admin` and `user add --admin` hold them all; nothing else grants them all. A
  disabled user fails authentication.

  Every server permission needs the second factor: `repositories.all` (create and delete
  repositories, and `AllRepos`), `people.manage`, `settings.general`, `activity.read`,
  `storage.manage`, `backup.manage`, `network.manage`, `signing.manage`, `signin.manage`,
  `nexus.manage`. A holder of `people.manage` gives only the server permissions they hold, and
  resets the password of, or disables, only a person whose permissions they all hold; they see
  every device. Repository permissions: `repo.download`, `repo.publish` (through a publish
  session; needs `repo.download`), `repo.delete` (with the UI session or a device or manual
  publish session), `repo.settings` (settings, group members, a proxy's remote, allowed networks,
  the public flag), `repo.access` (give and remove this repository's permissions, only ones the
  holder holds), and `ci.pipelines` (needs `repo.publish`). All but `repo.download` and
  `repo.publish` need the second factor.

  Each route's policy names the permission it needs (`Policy.Perm`/`AnyPerm`,
  `RepoRule.Perm`/`AnyPerm`). The server refuses a change that leaves no enabled person with
  `people.manage` (409).
- **Second factor rule.** A person needs the second factor for a UI session when they hold any
  permission that needs it (`SecondFactor`); `second_factor_required` in `GET /api/me` follows
  this rule. A grant that makes a person need it ends their UI sessions, so a session that a
  password alone started never holds such a permission. `signin.password_login=publishers` refuses
  the password sign-in (403) of a local account that needs the second factor; publishers sign in
  with a password.
- **CSRF.** `http.CrossOriginProtection` plus SameSite=Strict.
- **Rate limit.** Failed attempts are limited per IP and per username: burst 10, then 1 every
  6 s; over it, 429 with `Retry-After`. The client IP is resolved as in **Network rules**.
- **First start.** With no users, the server creates `admin` with a random password and prints it
  once to stderr, outside the JSON log stream.

### Signed download links

A person copies a direct URL to one file of a repository that not everyone reads and pastes it
into a tool that takes a plain URL (a game panel that downloads a server jar). The module `links`
(`internal/links`) owns it; it has no table.

- **Making one.** `POST /api/repos/{name}/links` needs a UI session and `repo.download` on the
  repo (ttl at most `links.max_ttl`). 409 for a format whose files have no URL under
  `/<format>/{repo}/` (Docker). The answer is the file's format URL (the one the Download button
  uses) on `base_url`, with `texp` and `tsig`; the file name stays the last path segment. The MAC
  is HMAC-SHA256 under `links.secret` over the repo id, user id, expiry and the unescaped server
  path. Each link records `file.link.create`.
- **The key.** `links.secret` (a secret setting) is made at the first link, saved like a change of
  the setting by that person. A new value or a reset of the setting ends every link.
- **Reading with one.** The router allows `auth.CredLink` only on GET and HEAD routes that read
  one repo and need no server permission. `Identify` reads it when the request has `tsig` and no
  `Authorization` header (it then reads no session cookie). The verifier checks the expiry (in the
  future, and at most `links.max_ttl` from now, so lowering the setting shortens every link), then
  the MAC over the request's `URL.Path`, then the user: gone or disabled is refused. The route's
  repo step refuses the link on another repo of that name (one deleted and created again). The
  access engine then decides as for any read: the user's current grants, plus the repo's allowed
  networks for the downloader's address. A failed link is 401 and counts against the client
  address like a failed password. A write route reads no link, so a link never writes. The query
  never reaches a log: the server logs paths only.
- **Edge.** The Worker passes every request with `tsig` to the server (see **Edge delivery**), so
  only the server checks links.

## Publishing

Every write on a format route needs a **publish session**: a random credential that the server
generates for one repo and that lives at most `publish.session_ttl`. A leaked password or read
token cannot publish. The Nexus sync and the storage move write in-process and do not pass this
gate. The server generates, stores (SHA-256), scopes, expires and revokes every session; a caller
only proves who it is:

| Who | Proof | Route |
|---|---|---|
| Developer with the Telesphorepo CLI | An enrolled device key (Ed25519) | `POST /api/publish/sessions` |
| Developer with Docker, Helm or ORAS | The device key, through the CLI's credential helper, over a token exchange nonce | `/docker/token` (see **Docker registry**), `POST /api/publish/sessions/exchange` |
| CI (GitHub Actions, Jenkins, any OIDC issuer) | The CI identity token and a trust policy (CI pipeline) | `POST /api/publish/sessions/oidc` |
| Anyone, by hand | UI session plus a fresh second factor | `POST /api/publish/sessions/manual` |

The routes are in `internal/publish/api.go`. Their policies:

- **No credential, a proof in the body:** device enroll and poll, the publish challenge, the
  device, OIDC and exchange mints, `POST /api/devices/setup` (a setup code) and
  `DELETE /api/devices/self` (a device signature).
- **Password, read token or UI session:** own devices and own publish sessions. The
  pending-enrollment lookup takes a password or a UI session, never a token. A holder of `people.manage` sees every device.
- **UI session and a fresh factor** (`X-Telesphorepo-Proof`: `totp <code>` or `passkey
  <assertion>`, never a recovery code; an SSO account sends `grant <grant>` of a fresh SSO
  sign-in): device approval, a manual session, and a setup code (which also needs `repo.publish`).
  These refuse read tokens and publish session tokens. A `trs_` token never reaches the admin
  routes, also not for an admin user.
- **UI session:** `DELETE /api/devices/{id}` revokes the caller's device (any device for a holder
  of `people.manage`) and ends its open sessions.
- **The session's own token:** `DELETE /api/publish/sessions/{id}`.
- **`activity.read`:** `GET /api/publish/log`.
- **`ci.pipelines` on a repo:** the trust policies (`/api/publish/trusts`). A holder of
  `repositories.all` sees and changes every one. Anyone else sees and changes only the policies
  whose repos all hold `ci.pipelines` for them: the list leaves the others out, such a policy
  answers 404, a `repos` list outside the scope answers 403, and a caller with `ci.pipelines` on
  no repo gets 403. The list is one keyset query of the policies with a repo in the caller's scope
  and none outside it, never a walk of all policies; the count of a `repositories.all` holder
  stops at 10,000, anyone else's is counted from their repos' pipelines.

The trusted-proxy lists (`/api/network/trusted-proxies`, `network.manage`) live in
`internal/netrules`.

Rules of these routes:

- **Enroll** is limited per IP: a burst of 10, then 1 per 30 s. The code expires after 10 min.
- **Poll** answers `{device_id, user}` once after approval; only the holder of the device code
  learns the user name, and the CLI names its account after it. A poll less than 4 s after the
  last gets `slow_down`; an unknown or used code `expired_token`.
- **Challenge.** A plain nonce is single use and lives 60 s, for a device mint, a setup or a
  device logout. With `exchange: true` it is a token exchange nonce that lives
  `publish.session_ttl`, signed for the repo `*`. Sending an unexpired exchange nonce of the device
  back returns it and writes no row. A new exchange nonce needs `ts` (within 5 minutes of server
  time) and the device's signature over it, so only the holder of the device key makes the server
  write a row that lives that long. An unknown, revoked or expired device, or a disabled user, gets
  401.
- **Mints.** `access.Engine.Decide` with `Mint` decides (405 for a proxy or a group, 409 for a
  locked repo). The OIDC mint takes an optional `revision` (a 40 or 64 hex commit). The exchange
  mint returns the same session and token for the same nonce and repo while it is open.

**Device flow** (RFC 8628 style):
1. The CLI creates an Ed25519 key pair, calls `enroll`, and prints the verification URL and user
   code. Once approved it stores the key owner-only (see **Files**).
2. The user opens the page, signs in, checks the device's name, and approves. The device belongs
   to the account that approves it: the page names that account and offers to sign in as someone
   else. A local account approves with a fresh TOTP code or passkey check. An SSO account signs in
   again: `GET /api/auth/oidc/device?code=` asks the provider for a new sign-in (`prompt=login`,
   `max_age=0`). The callback checks that the provider signed in the same account (issuer and
   `sub`) and that `auth_time` is at most `oidc.approval_max_age` old; a token without
   `auth_time` is refused. It stores a grant in `auth_challenges` (kind `device_grant`, hashed,
   bound to the user and the user code, single use, 5 min) and returns it in the page address,
   which the page removes before it sends it to `approve`. A provider that ignores `prompt=login`
   returns an old `auth_time`, and the page shows `stale_sign_in` with that cause. Both paths
   record `device.approve`, and `publish.session.create` when the device mints.
3. The client polls until it gets `device_id` and `user`.
4. A device lives `publish.device_ttl` unless revoked; the client then enrolls again.

**Minting from a device.** The signature is Ed25519 (base64url, no padding) over
`"telesphorepo publish v1\n" + nonce + "\n" + device_id + "\n" + repo` (`device_id` in decimal).
The server checks the signature and nonce, that the device is not revoked or expired, that its
user (local or SSO) is enabled, and that the user holds `repo.publish` on the repo now. The token
also reads that repo.

**Trust policies and the CI exchange.**
- A policy needs at least one claim, and each claim at least one allowed value. A value is exact
  or a `*` glob; `*` matches any text, `/` included. Every listed claim must match.
- `audience` `""` means the current `base_url`, resolved at exchange time. When both are empty,
  the exchange refuses the token and says to set the public address or an audience. Every refusal
  of a policy match names Settings › CI pipelines as the place that fixes it. A pasted `jwks` is
  checked when the policy is saved and replaces key discovery (for a Jenkins the server cannot
  reach). Signing algorithms are RS, ES and PS (256, 384, 512) and EdDSA. Issuer keys are cached.
- The server checks signature, issuer, audience, `exp`, and `iat` (at most 10 min old and 1 min
  in the future), then finds an enabled policy that matches the issuer, audience and claims and
  lists the repo. The mint route refuses a token seen before for any repo (`publish_oidc_seen`,
  kept until `exp`). The Minter, which a format's token exchange calls, mints once per repo and
  gives a token its open session of that repo back. The replay row and the session are written in
  one transaction. `subject` is the token's `sub`.
- The CI provider that matches the token reads its claims into a build record, written in the
  same transaction as the session; the session has `build_id`, and every file it writes points at
  the build (`assets.build_id`).
- An edit of issuer, audience, claims, jwks or repos, a disable, and a delete end the policy's
  open sessions.
- The build record takes its commit from the token (GitHub Actions `sha`). A token that names none
  (Jenkins) takes the `revision` that the job sends: the mint route's `revision`, or the user name
  `telesphorepo-ci@<commit>` at `/docker/token`. The job states it, and no signature covers it.
- A CI session writes outright. A device or manual session needs the user's `repo.publish`.

**Sessions.** An open session has `closed_at` empty and `expires_at` ahead; a device session also
needs a usable device, an OIDC session an enabled policy. A bad, unknown, closed or expired token
counts toward the rate limit and answers 401. A job in `internal/publish` runs every minute and
deletes, in batches of 100, expired nonces, enrollments and seen CI tokens, and sessions that
expired more than 30 days ago (the audit trail for the **Publish log**). Each run also reads one
batch of the build records older than 30 days, with a cursor that persists between runs, and
deletes those no file references; a session that names a deleted build keeps its row with
`build_id` NULL. A second mint for the same run updates the record and its `created_at`, so a
build in use is never old.

**CI providers and build records.** A CI provider (`ci.Provider` in `internal/ci`,
`Extender.CIProvider`) is one CI system: `Match` (does a token, or a trust policy, of this issuer
belong to it), `Build` (the build record of a verified token) and `Describe` (what a trust
policy's allowed claims let publish, in its words). The publish module registers `github-actions`
and `jenkins` (`internal/publish/providers.go`). GitHub Actions matches the GitHub.com issuer (or
`/<enterprise>`), or any issuer whose token carries `repository` and `workflow_ref` (GitHub
Enterprise Server); a build record needs a well-formed `repository`, `sha`, `run_id` and a
`workflow_ref` of that repository. Jenkins matches the `<jenkins>/oidc` issuer of its OpenID
Connect provider plugin; the repository is the job path of `sub`, and a `sub` that is not a job
URL of the issuer's Jenkins makes no record. An issuer that no provider matches gives no build
record and no link; the access report shows its policies as system `other`. The dev demo data
writes its builds through the same provider, not as sessions.

**Project setup.** One client, the Telesphorepo CLI (`cmd/telesphorepo`), enrolls devices and
publishes to every format. The Publish tab of a repo makes a setup line:
`POST /api/publish/setup-codes` (a UI session, a fresh second factor, and `Decide(..., Mint)` with
`repo.publish`, the same bar as a device approval) stores a setup code `trsu_...` as an
`auth_challenges` row (hashed, the user, the repo id, 10 minutes), records `setup_code.create`
and answers a link `<base>/#/setup/<code>`. The code is in the fragment, so a browser that opens
the link never sends it. An SSO account confirms with the grant of a fresh SSO sign-in
(`GET /api/auth/oidc/setup?repo=`); the route refuses a grant of another repo. The CLI then calls
`POST /api/devices/setup` with the code and a new device key, or with the proof of the device it
has. The server first checks a device proof (a Challenge nonce, the signature over
`"telesphorepo setup v1\n" + nonce + "\n" + device_id`, a usable device), then takes the code
(the first use deletes it). It checks that the code's user is enabled and may mint for the repo,
and that an existing device belongs to that user (409 otherwise, and for a key enrolled already).
It creates the device or records its use, and records `device.setup` as that user. A failure
after the code was taken puts the code back, unless it expired. A wrong, used or expired code
answers 401 `setup-code-used`.

**Device logout.** `DELETE /api/devices/self` with a Challenge nonce and the signature over
`"telesphorepo revoke v1\n" + nonce + "\n" + device_id` revokes the device, ends its open sessions
and records `device.revoke` as the device's user.

**Token exchange for the helpers.** `POST /api/publish/sessions/exchange` with a device proof and
a repo mints through the same code as the `Minter` of `/docker/token`: one session per repo per
token exchange nonce, returned again for the same nonce and repo while it is open (see **Minting a
publish session**). The CLI's credential helpers use it.

**Telesphorepo CLI** (`cmd/telesphorepo`, Go, MIT license of its own `LICENSE`). It imports no
server package; outside the standard library it uses only `golang.org/x/sys`,
`github.com/BurntSushi/toml`, `github.com/klauspost/compress/zstd`, `github.com/xi2/xz` and the
root package `changelog`. Every request carries
`User-Agent: telesphorepo-cli/<version> (<os>/<arch>)`, goes only to the canonical URL of the
server and follows no redirect; only the download of `get` follows redirects, and it drops the
device proof on a redirect to another origin (a presigned URL of the store). No command takes a
token as an argument, and the CLI never prints a CI token, a session token or a setup code; in
GitHub Actions it prints `::add-mask::` for each token before a child starts.

**Output.** The CLI talks to the person on stderr and prints results on stdout; a build tool's
output passes through untouched. On a terminal it styles both streams
(`cmd/telesphorepo/style.go`; `NO_COLOR` or `TERM=dumb` turns style off, `FORCE_COLOR` or
`CLICOLOR_FORCE` on). Without style the text is the same word for word, so pipes, CI logs and
scripts read the same lines. The credential helper mode prints nothing but its protocol on stdout.

The commands, each in its own file of `cmd/telesphorepo`, keep these rules:

- **`setup LINK`** reuses a device that the server accepts, else enrolls a new key with the code.
  A publish-category format needs a marker of the format in the folder, where the CLI writes
  `telesphorepo.toml` (it shows a change to an existing file and asks; its `[[when]]` rules stay)
  and warns when git ignores it. Docker asks before it adds the `credHelpers` entry of the
  server's Docker hosts to the Docker config. On a terminal it then offers a CI file, read from
  `GET /api/repos/{name}/ci` with a session it mints and closes.
- **`publish [--version VERSION] [FILE...]`** walks up to the nearest `telesphorepo.toml`, picks
  the repo by version when it has rules (see **Rules by version**), and mints one session per
  repo. In CI (`CI`, `GITHUB_ACTIONS`, `JENKINS_URL` or `JENKINS_HOME`) it mints with the job's
  identity token (GitHub Actions with the canonical server URL as the audience, else
  `OIDC_TOKEN`) and sends the commit as `revision`. It sends an `OIDC_TOKEN` only when its `aud`
  (read without a signature check) holds the canonical URL of the project file's server, because
  a project file of an unchecked branch could name another server. On a computer it mints with the
  device proof, running the device flow first when the server refuses the device. FILE uploads run
  4 at a time and the first failure cancels the rest. It runs the format's row of **Detection** and
  closes the session at exit, also on Ctrl-C; a child's exit code passes through.
- **`get URL...`** downloads through a temporary file that a rename puts in place, so a failed
  download leaves no file. A URL under the canonical URL of a server of this computer (the longest
  base path wins) reads as that server's active account, with the device proof over the repo `*`
  as the Bearer value; a refused device, or another URL, reads anonymously and writes nothing
  under `~/.telesphorepo`.
- **`login [SERVER]`** runs the device flow and saves the device as the account of the poll's
  user, made active. A user who has an account already gets the new device in its place, and the
  CLI revokes the old one. On a terminal with several accounts it offers to keep, switch or add
  one.
- **`logout [SERVER]`** revokes the active account's device and deletes its folder; the last
  account also removes the server's `credHelpers` entries (every other entry stays).
- **`status`** prints accounts, device state, `credHelpers` entries and what `publish` would do,
  and no secret.
- **`update [SERVER]`** installs the server's CLI after the SHA-256 check, through a new file and a
  rename next to the running binary, and makes the helper link again.
- **`docker-credential-telesphorepo`** is the same binary under a second name (see **Docker
  credential helper mode**).

**Rules by version.** `telesphorepo.toml` holds `server`, `repo`, optionally `kind`, `path`,
`dist`, `component`, and an ordered list of `[[when]]` rules. A rule has `version`, a glob (`*`
is any run of characters, everything else literal, matched against the whole version), and at
least one of `repo`, `path`, `dist` and `component`; the first rule that matches overrides those
keys, and a version that matches none keeps the top-level ones. Reading the file names a bad rule
by its number from 1.

```toml
server = "https://repo.example.com"
repo = "releases"

[[when]]
version = "*-SNAPSHOT"
repo = "snapshots"
```

Only a project file with rules makes `publish` read a version: `--version`, else the metadata of
each FILE (FILEs that pick different repos publish one repo after the other), else the project's
build tool or manifest (`cmd/telesphorepo/rules.go`). The CLI prints the decision for each version
before it talks to the server; with no version it mints no session and stops.

**Detection.** `publish` looks for the markers of the repo's format in the folder of the project
file. Each format is one row of the table in `cmd/telesphorepo/detect.go` (marker, tool, command,
how the token reaches it); a new format adds a row. Build formats run their own tool, preferring a
wrapper in the project over the `PATH`; raw, Arch, RPM and Debian have no marker, and the CLI
uploads the FILEs itself. A file of 64 MiB or more goes through the resumable upload routes, and a
failed chunk is sent again from the server's `received`. Raw `--if-new` and `--if-match` send
`If-None-Match: *` or `If-Match`. The CLI prints each command before it runs it, without the token.
The token goes only into the child's env; the config files in the temp folder (`0700`, an
owner-only ACL on Windows, removed at exit) hold `${env}` references. A build step that only packs
gets no token. The child env has no `OIDC_TOKEN`, so a build step cannot mint for other repos with
it. A stop of the CLI stops the child too.

**Compatibility.** Before a command, the CLI takes the entries of the change log (see **Change
log**) between its version and the server's whose scope matches the command (`setup`, `login`,
`publish`, then `publish/<format>` once the mint names the format, `helper/docker`). Server newer:
it reads `GET /api/changes?since=<CLI version>`; an entry with `needs = "cli"` offers the update
on a terminal and runs the command again with the new binary, else prints the install line and
exits 1. CLI newer: an embedded entry with `needs = "server"` takes the older path when the CLI
lists its `id` (`olderPaths` in `compat.go`), else says the server admin must update and exits 1.
A dev CLI or a dev server skips the check. The CLI caches the entries and fetches again only when
the `Telesphorepo-Version` header of an answer names another version.

**Errors.** A problem document prints its `title` and `detail`, then its remedy action
(`cmd/telesphorepo/problem.go`; `retry` waits at most 60 s); an unknown action prints `detail` and
`url`, and an unknown field is ignored. A 412 of a raw publish exits with code 3, so a script
reads the file again and loops; usage errors exit 2, other failures 1. A raw publish with
`--if-match` or `--if-new` against a server older than the change log entry `conditional-put`
stops, since that server would store the file without the check.

**Docker credential helper mode.** Docker, buildx, Podman, skopeo, crane, Helm and ORAS run it
through `credHelpers` in the Docker config (`cmd/telesphorepo/docker.go`). `get` answers, in this
order:
1. Inside `publish` (`TELESPHOREPO_TOKEN` set and the host one of `TELESPHOREPO_DOCKER_HOSTS`):
   the session token.
2. A host of an enrolled device: user `telesphorepo-device` and the proof
   `trx.<device id>.<nonce>.<signature>` over a token exchange nonce for the repo `*`.
3. A CI job with no device: user `telesphorepo-ci@<commit>` and the job's identity token, which
   `/docker/token` mints from.

The token's server is `TELESPHOREPO_SERVER` when the host is its host or one of its
`GET /docker/hosts` (another host gets no credential), else the project file's server when its
host is the registry host, else `https://<host>`. An `OIDC_TOKEN` whose `aud` does not hold that
server gets no credential. Else, or on a 4xx from the challenge, `get` answers `credentials not
found in native keychain` and exits 1, so Docker reads anonymously. Each `get` sends the
challenge, so a revoked device stops at once. The process that runs the helper (its id and start
time) keeps its nonce in `exchange.json` and sends it back, so Helm and ORAS, which run `get` for
each token, get one publish session per push. `store` refuses (the helper keeps no passwords);
`erase` keeps the device and says to run `telesphorepo logout`.

- **Files.** `~/.telesphorepo/<server>/` holds the computer's accounts on that server, each the
  device of one user: `accounts/<user>/device.json` is the only secret file, and `active` names
  the active account (else the first by name), which every signing command uses. A folder of an
  older CLI holds one device in `<server>/device.json`: that is the one account; a second account
  first moves it into `accounts/` with a rename, so a crash never loses the key. `hosts.json`,
  `exchange.json` and `changes.json` are caches. The CLI writes each file through a temporary file
  and a rename: mode 600 in a 700 folder, or an ACL with the current user as the only entry on
  Windows. A device file that another user can open stops the CLI. `<server>` is the host, port
  and base path with every character outside `A-Z a-z 0-9 . _ -` as `_` (`cmd/telesphorepo/device.go`).
- **Server identity.** The canonical URL is the scheme, the lower-case ASCII host (an
  internationalized host in its `xn--` form), the effective port and the base path without a
  trailing slash. `get` answers a device proof only when the host and port that Docker passes
  equal those of the server, or a host of `hosts.json`. Every request goes to the canonical URL of
  the device file or the project file, never to the host that Docker passed.
- **Token exchange.** Docker sends the secret as the password to `/docker/token`. The token
  endpoint mints one session per repo per nonce (`publish_exchange_sessions`); a pull checks the
  proof (`Minter.Check`) and mints nothing. The bearer carries the device (`auth.CredDevice`), so
  it reads what the device's user reads (a private base image too) and pushes through the session
  of its scope.

**UI pages.** The sign-in page (the second step offers the passkey first, a recovery code only
behind a link), the approval page (`/#/device?code=`), and under Your account **Sign-in and
security**, **Devices** and **Tokens**; under Settings, for holders of the permission, **CI
pipelines** and **Activity** (the publish log is its second tab). A setup-only session sees only
Sign-in and security. A user with no second factor goes from the approval page to set one up and
back. See **Web UI**.

## Byte pipeline

Every file of every repo goes through one write pipeline, one delete pipeline and one read path in
the kernel (`internal/assets`, `internal/blobs`). A format takes part through its `format.Spec`
hooks (`internal/format/format.go`); no module writes the `assets` or `blobs` tables.

**Blobs.** A file's content is a blob at `blobs/sha256/<first 2 hex>/<hex>`, stored once for any
number of paths and repos; each path's asset row holds its own content type. `blobs.refs` counts
the asset rows that point to it. An unreferenced blob stays for `blobs.gc_grace`, so an upload of
the same bytes reuses it.

**Locks.** A write takes its path locks (and extra locks), sorted, then the digest locks of the
blobs it stores, sorted. The blob GC takes digest locks only, the storage move copier the digest
lock of the blob it copies, so no lock cycle forms. The locks are in-process (one server process).

**Write (`Commit`, `CommitAll`).** `CommitAll` writes files of one repo in one transaction, in this
order: path locks and the existing assets; `Describe` (content type, kind); preconditions (412);
`Admit` per write (store, keep the same bytes as `unchanged`, or refuse); digest locks and each new
blob written once; one transaction, retried up to 3 times, with the asset and `tree_entries` rows,
`OnCommitted` (the format's rows, rebuild marks) and the `file.upload` events; then the locks are
released and `AfterCommit` runs. A write runs detached from its request (a client that leaves does
not orphan a blob) and stops only at shutdown. A repo being deleted refuses writes with 409. Every
writer uses it, imports, proxy fetches and generated indexes included; `telesphorepo-server seed`
inserts rows only (`assets.SeedTx`).

**Preconditions** (`assets.Precondition`, RFC 9110 §13, strong comparison with the ETag
`"<sha256>"`). A condition the server does not support is 400: a write never ignores a condition.
The check runs under the path locks, so of two writes from the same ETag one stores and the other
gets 412; it precedes `Admit`, so an `If-Match` naming a file still gets the overwrite policy's
409. A failure is a `precondition-failed` problem document (see **Problem documents**) with the
current ETag. Writes answer the `ETag` the path holds after the write, so a client chains writes
without a GET.

**Delete (`Remove`).** In batches of 500 rows: path locks, then one transaction with the format's
`OnRemoved` (rows still readable; it may return follow-up paths), the row deletes and the
`file.delete` events; then `AfterRemove`. Blob objects are never deleted here. While the repo has
an active `delete` operation, `Remove` skips the format hooks and events, the rebuild queue skips
the repo, and the format's rows go with the repo row.

**Blob GC.** The job `core.blob-gc` deletes blobs unreferenced for longer than `blobs.gc_grace`,
up to 1000 a round. Under the round's digest locks it rereads which are still unreferenced, deletes
their objects, then the rows of those gone. A blob whose object delete failed keeps its row for the
next run.

**Read path (`Reader.Serve`).** Hosted: the path's asset, or 404. Proxy: the format's `Classify`,
then a fetch through `proxy.Service.Ensure` when the cache is missing or stale (see **Group and
proxy repositories**). Group: the members the caller may read, in read order; the first with the
file serves it, and a proxy member fetches only when no earlier member has it (see **Groups**). A
format `Merger` merges a base file two or more members hold. No member: 404, or 502 when a proxy
member's remote failed. `ResolveLive` streams an uncached proxy fetch to the client (see
**Proxies**); `Stored` never reaches a remote.

One function sets the headers from the asset row (ETag, `nosniff`, a sandboxing CSP, `attachment`
except for `text/plain`). With S3 and `s3.redirect`, a GET is a 302 to a presigned URL that signs
the content type and disposition from the asset row, so each path of a shared blob keeps its own.
HEAD, merged files and a GET with `If-Modified-Since` stream instead: the store would judge by the
blob's date and could answer 304 to a changed file. Format reads of a repo not everyone reads carry
`Cache-Control: private` (see **Route policy**).

**Resumable uploads (`Uploads`).** Only the publish session that started an upload continues it (a
core bearer token counts as the publish session of its scope). It finishes with the SHA-256 of the
whole file, or the size alone from the web UI, which cannot hash a large file in parts. Bytes and
hash state live in `$TR_DATA_DIR/tmp/uploads/`, so an upload survives a restart. A refused append
still reads the body, so the client reads the answer before the connection closes. After a 412 the
upload stays for `assets.PreconditionHold`, so the client can finish again with new headers.
`core.upload-sessions` deletes idle uploads and orphaned files.

**Repo paths.** `repopath.Parse` and `httpx.RepoPath` define a valid repo path for every route
and writer.

**Watchers.** A module that must see every commit and remove (the edge module's R2 pointers)
registers an `assets.Watcher` (`internal/assets/hooks.go`); it runs under the path locks and in the
commit and remove transactions, repo deletes included, and its error fails them.

**Format hooks.** Content types come from `Describe`; the kernel's only one is the fallback
`application/octet-stream`. `OnRepoChange` and `OnRepoCreate` run in the repo write's
transaction.

**Rebuild queue.** A format marks a unit of a repo's index with `rebuild.Queue.MarkTx` in the
change's transaction. The job `core.rebuild` debounces per (repo, group) (`index.debounce`,
`index.max_delay`), rebuilds the units marked before it started, and keeps those marked again; a
failure retries with backoff. At most two groups rebuild at once; pending marks survive a restart.

**Importers.** `format.Importer` (`internal/format/importer.go`) skips a file whose target already
holds the source's SHA-256, checks the source checksum, and commits with source `import`. Items
import concurrently, so its hooks must be concurrency-safe.

## Repo operations

A repo operation (`repo_ops`, `internal/ops`) is a long task that owns a repo, at most one per
repo. Meanwhile the access engine refuses `Write`, `Delete` and `Mint` with 409 and the type's
message. Modules register types with `Extender.OpType`.

- **`delete`** (core). Removes the files, then empties in short writes what the row delete would
  cascade to (each module's `repos.Purge`, children first), then deletes the row with `repo.delete`
  in the same transaction, so a repo that answers 404 has its event. A stop leaves it running and
  `core.ops` continues it at startup; a failed one can be resumed or stopped, keeping the files.
- **`mirror`** (nexus). Held while a Nexus source mirrors a hosted repo, until the switch-over.
- **`merge`** (nexus). On both repos of a merge (see **Nexus sources**, Gradual move).
- **`empty`** (nexus). While a rehearsal repo's files are deleted; the repo stays.

## Repository redirects

A redirect is a name that answers for another repo (`repo_redirects`, kept by `repos.Service`):
every request to it, from every client and route, publishes included, is served exactly as if it
named the repo. It is not an HTTP redirect, which uploads, `npm publish`, `docker push` and apt do
not follow.

- **Rename.** Adds the old name as a redirect. **Merge.** `repos.Service.Retire` gives a repo's
  names to another (a Nexus merge retires its source). **Add a redirect.** Any taken name is 409.
- **One resolution.** Everything that names a repo goes through `repos.Service.ByName`, which reads
  the repo, then the redirect. Edge objects use `ByOwnName`, which skips redirects.
- **Keys.** Everything keyed by repo uses the repo id, and names in credentials resolve to it, so
  tokens issued before a rename work through both names. A redirect's edge object names its repo
  (see **Edge delivery**).
- **Reserved.** A name is a repo or a redirect, never both. A repo may take back its own redirect.
- **Never a chain.** A redirect holds the repo id, so it survives later renames.
- **Permissions, allowed networks and visibility** are the repo's. **Activity records the name the
  request used**, with the repo's own name in `detail.redirect_to`.
- **Last use.** Noted in memory, written once a minute (see **Download statistics**).
- **Remove the redirect.** The name answers 404 and is free for a new repo.
- **Debian.** apt refuses an update whose `Release` changed `Origin` or `Label`, so a rebuild keeps
  the current ones; a new distribution gets the repo's name.

## Debian indexing pipeline

1. **Upload.** Only `control.tar.*` is parsed, with size caps; `data.tar` is never unpacked.
2. **Validation.** `internal/debian/parse.go`, `config.go`; `signing_key` is the repo field (see
   **Signing keys**).
3. **Commit.** The package identity is an extra lock. The same version with the same bytes is kept
   (200), other bytes are 409; the same pool file stored again adds the package to another
   distribution. `OnCommitted` marks the units (distribution, `component/arch`).
4. **Debounce.** Per (repo, distribution) (see **Byte pipeline**).
5. **Package indexes.** Per unit, rows stream through the unique index into `Packages` and
   `Packages.gz`, written with their `by-hash` copies in one commit.
6. **Release.** Rebuilt from `deb_index_files` and signed in one commit.
7. **Architecture change.** Marks the added and removed architectures per (distribution, component)
   with one seek per pair of the UNIQUE index of `deb_packages`, so the update costs the pairs, not
   the packages. A `signing_key` change marks one unit per distribution, so each `Release` is
   re-signed.
8. **By-hash cleanup.** The current and previous `by-hash/` generations stay; older ones go.
9. **Delete.** `OnRemoved` deletes the rows and marks the units; a repo delete purges in batches.

## Arch Linux (pacman)

`internal/arch`, format `arch`, hosted and proxy. pacman uses `Server = HOST/arch/$repo/$arch`
under `[<repo>]`.

1. **Upload.** Read once: the raw bytes feed a SHA-256 whose state the row keeps, so a key change
   re-signs without rereading the file; the decompressed tar yields `.PKGINFO` and the file list,
   with the caps in `pkg.go`.
2. **Validation.** `pkg.go`, `config.go`.
3. **Commit.** `pool/<name>/<file>` and its `.sig` in one commit. Same bytes are kept (200), other
   bytes are 409. `OnCommitted` updates the per-architecture index entry of the name (newest
   version; architecture-specific over `any`) and marks changed architectures.
4. **Indexes.** `index.db` and `index.files` with detached signatures. `desc` follows repo-add,
   without `%PGPSIG%` (pacman fetches each `.sig`). A rebuild costs one architecture's package
   names, not versions (10,000 names: 40 ms db, 83 ms files).
5. **URLs.** `{arch}/<seg>.db` maps to `{arch}/index.db`, `<seg>` being the name the request used,
   so a section named before a rename works through the redirect; package files map to `pool/`.
   `arch.ServedPath` maps on the server, `pacmanPath` (`edge/src/shapes.ts`) in the edge Worker.
6. **Architectures.** An added one is filled from its and `any` packages; a removed one's index
   goes, its packages stay.
7. **Signing.** A key change rewrites every `.sig` from the saved hash states.
8. **Delete.** The name's entry passes to its newest remaining package. Deleting a `.sig` while its
   package stays is 400. Every version stays until deleted; older ones install with `pacman -U`.
9. **No groups.** pacman.conf lists several repositories and takes a name from the first that has
   it; a group would need a merged index re-signed on every member's change.

## RPM (dnf)

`internal/rpm`, format `rpm`, hosted and proxy. dnf uses `baseurl=HOST/rpm/<repo>/`: one repodata
holds every architecture and dnf picks what its machine installs, so there is no `$basearch` and no
architecture config.

1. **Upload.** Reads the lead and headers (rpm's `lib/header.cc` layout, `header.go`), never the
   payload, with rpm's limits. Header buffers grow as bytes arrive, so a false size claim costs
   only the file.
2. **Validation.** `pkg.go`. 400 without `PAYLOADSHA256` (rpm 4.14+ and nfpm write it): a
   header-only signature covers the payload through it. Source packages are refused.
3. **Signing.** The stored file gets a new signature header: the old signatures go, and a
   header-only OpenPGP v4 signature by the repo's key is made from the header's saved SHA-256
   state, as rpmsign does (`sign.go`). rpm 4.16 and 4.14 read only the first signature tag, so an
   rpm 6 package carries it twice. Without a usable v4 key the package is stored unsigned (logged).
4. **Commit.** The same uploaded bytes answer 200 without re-signing; other bytes are 409, as is a
   file name held by another epoch. Package rows follow createrepo_c's choices
   (`src/parsehdr.c`).
5. **Repodata.** One stream through `rpm_packages_order` writes `primary`, `filelists` and `other`
   (every version of every name), then `repomd.xml` and its signature, in one commit. The previous
   `repomd.xml`'s data files stay, so a client that read it still finds them. The old signature
   stays too, so a repo whose key became unusable keeps a signature that no longer matches, and dnf
   refuses its repodata. 10,000 packages rebuild in 0.89 s, 100,000 in 8.5 s; one gzip member per package
   halved that but made primary 2.8 times larger, so the stream stays one.
6. **New repo.** A hosted repo marks its repodata at create (`OnRepoCreate`): dnf
   (`skip_if_unavailable` false) fails every command on a 404 `repomd.xml`, and accepts an empty
   signed one.
7. **Key change.** Re-signs every package in bounded commits, skipping one changed since read or
   already signed. It costs the repo's bytes, and edge delivery recopies each package, so the
   Signing tab names the repo's size in its confirmation.
8. **Delete.** Removing a package, `repomd.xml` or a file it names marks the repodata, so it returns
   within the debounce.
9. **Version order.** rpmvercmp on epoch, version, release, as dnf and libsolv use it
   (`1.0~rc1` < `1.0` < `1.0^git1` < `1.0a` < `1.0.1`); `vkey` encodes it as bytes that sort the
   same way.
10. **Clients.** Fedora 44 (rpm 6, dnf5), Rocky 9 (rpm 4.16, dnf4) and Rocky 8 (rpm 4.14) install
    RSA-signed packages; Rocky 8 cannot read Ed25519 keys (the default), so EL8 repos need an RSA
    key. dnf4 sends the repo's credentials for a `gpgkey` URL; dnf5 does not.
11. **No groups.** A `.repo` file lists several repos and dnf merges them by `priority=` and
    `cost=`; a group would need merged repodata re-signed on every member's change.

## Storage move

A settings PATCH that points the server at another blob location while blobs exist starts a move
(`internal/move`). The move copies; it changes and deletes nothing on the source. One move at a
time; storage settings change only when it finishes.

- **Mirror.** While copying, reads use the source; writes and deletes go to both, and an error on
  either fails the call.
- **Copier.** It walks `blobs` by id with a saved cursor. Per blob it holds the digest lock (the GC
  waits), skips a blob the target already holds, and checks size and SHA-256. A failed blob is
  recorded and the copy goes on.
- **Finish.** No failures: the target becomes active. Failures: the move fails, reads stay on the
  source, and a retry skips copied blobs. Cancel returns to the source.
- **Restart.** At startup, before serving, a move continues from its cursor.

## Backups

The image's `/backups` is a `VOLUME` owned by the server's user, and the build sets `backup.dir`'s
default to it (`-X internal/backup.defaultDir=/backups`), so a container backs up from its first
start. A plain build is off until a folder is set. Module `backup` (`internal/backup`) has no
table; the folder is the state.

- **Folder.** One folder per backup with the database, `master.key` and `backup.json`
  (`backup.Manifest`, written last, never a secret). Without a readable `backup.json` it is
  incomplete: refused by restore, removed by the next run.
- **Database.** `VACUUM INTO` from a reader copies consistently while the server writes, in rollback
  journal mode, so it opens read-only without side files.
- **Blobs.** With fs storage, used blobs go to one `blobs/` folder shared by all backups, each
  checked and copied once. The run copies the live database's blobs, writes the database copy, then
  the blobs the copy names that the first pass missed. Each copy holds the blob's digest lock. With
  S3 nothing is copied; back up the bucket with the provider's tools.
- **Retention.** The newest `backup.keep` complete backups stay; then blobs no kept backup uses go,
  unless a kept database cannot be read. Folders and blob files younger than an hour stay, and a
  running backup keeps its folder young, so `telesphorepo-server backup` into the server's folder is
  safe during retention.
- **Restore.** `telesphorepo-server restore FOLDER` refuses a non-empty data dir, checks the
  database's SHA-256 and that every used blob is there, copies blobs, then `master.key`, then the
  database last. It then says whether the backup's migrations match the binary's, are older
  (applied at start), or are newer or changed (this binary refuses to start; it names the version
  to run).

## Edge delivery

Far clients download from Cloudflare's edge: R2 buckets, a Queue and a Worker (`edge/`), kept by
the module `edge` (`internal/edge`). The Worker answers a GET or HEAD only when it can prove the
server would give the same answer, and passes everything else to the server. It never refuses:
every 401, 403, 404, 405, 409, 416 and 429 comes from the server, which keeps audit events, rate
limits and error bodies. It answers 502 only when it cannot reach the server. Cloudflare is the
only provider (R2, Queues, Workers Paid). Client URLs do not change: the Worker runs on routes of
the base URL host.

- **Consistency before latency.** A path that can change (Maven metadata and snapshots, Debian
  `dists/` outside `by-hash/`, Docker tags, a raw path under `allow`) has its pointer in the home
  bucket only, so every region reads the same version. An immutable path has a pointer and a blob
  in every region.
- **Warm everywhere from the first request.** The server writes every pointer, and the copy Worker
  copies every blob, when the file is stored, not after a first miss.

So Workers Cache (evicts) and Sippy (copies on first miss) are not the delivery path. R2 has no
replication and its binding no copy method, so the copy Worker streams the home `get().body` into
the regional `put`, never a cross-bucket `CopyObject`.

**Buckets.** Home is the server's S3 storage (`storage=s3` on R2), blobs under their usual keys.
Each region (`edge.regions`, `<hint>=<bucket>`) has its own bucket in the same account, opened with
the storage settings' credentials. The server writes regional pointers; the copy Worker writes
regional blobs.

**Objects.** The server and the Worker talk only through JSON objects under
`[s3.prefix/]edge/v1/`, defined in `internal/edge/objects.go` and read in `edge/src/`: `config.json`
(mode, `requires`, `access_ttl`, the public key for core bearers, formats, buckets and readiness,
repo domains, `pointer_cache`); `bucket.json` in every bucket (the bucket names itself, so the
Worker detects a binding to another bucket; a region is ready only after it lands); pointers
`p/<repo id>/<sha256 of path>` (hashed so a 1024-byte path fits R2's key limit; `{pending}` while a
write runs); access objects `a/repo/`, `a/user/`, `a/token/`, `a/device/`; the Worker's `seen/` and
`gap/` state; one-time proofs `check.json`, `probe/`, `speedtest/` (each holds a SHA-256, never the
secret); `copy/` multipart state, so a retried copy resumes.

The repo object carries `private` (`access.Engine.Private`) and `guarded` (`access.Engine.Guarded`:
allowed networks make anonymous reads depend on the address, so they go to the server; a group is
`guarded` when any member is). A redirect name points to its repo; a redirect to a redirect goes to
the server. `repos.id` is `AUTOINCREMENT`, so a deleted repo's ID never returns, and the Worker
checks a pointer's `repo` against the repo object's `id`; user and device IDs are reused, so a repo
delete marks every user object.

**Formats.** A format opts in with `format.Spec.Edge`: a shape (`standard`, `oci`, `pacman`, whose
path map in `edge/src/shapes.ts` follows `arch.ServedPath`) and `Immutable`/`Mutable` globs. A path
is immutable when it matches an `Immutable` glob and no `Mutable` one. Signatures (arch `*.sig`,
rpm `repomd.xml.asc`) are mutable because a key change re-signs. Documents rendered per request
(npm packuments, NuGet documents) have no pointer. Raw uses `Paths`: a path is immutable when its
overwrite policy is `deny`, so a repo under the default `deny` serves everything from the regions.
A proxy gets a pointer only for a path whose `Classify` class is not `Changing`. A group has no
pointers of its own.

**Path classes** (`internal/edge/paths.go`). A format with `EdgeSpec.Paths` classifies each repo by
its config, and the repo object carries the rules (an older Worker ignores them and reads every
pointer from home, which holds them all). A Worker can hold an old repo object for up to
`edge.access_ttl`, and a mutable replace changes only the home pointer, so the module keeps one
invariant with no time bound: a non-pending regional pointer holds the asset's current bytes.

- Every regional pointer write first records its rule list in `edge_paths`, under the path lock it
  holds through the put.
- The pre-steps (`Replacing`, `Creating`, `Removing`) treat a path as immutable when the current
  rules or any recorded list say so: a replace puts `{pending}` in every bucket, a remove deletes
  in every bucket, and both purge cached copies.
- The outbox writes regional pointers by the current rules only, and deletes regional pointers of a
  mutable path that a recorded list makes immutable.

So a Worker with any repo object reads the current pointer, a pending one, or none (the last two go
to the server). A group read whose member has `paths` goes to the server. When a repo's rules
change, the job `edge.reclass` walks it under each path lock, adding and deleting regional
pointers; it drops a recorded list only when no write used it since the walk began, and not while
a put of unknown outcome may still land.

**The pointers.** The pipeline watcher (see **Byte pipeline**) and the outbox (`edge_outbox`) keep
them. `Replacing` writes a dirty outbox row in its own transaction, then makes the pointer pending
(immutable) or deletes the home pointer (mutable), then purges cached copies; `Creating` does the
same for a new immutable path of a group member, since the Worker skips a member without a pointer
and would serve a later member's copy. `Removing` deletes the batch's pointers everywhere first.
`Committed` and `Removed` settle the rows in the commit or remove transaction; if it fails, the
dirty rows stay and the outbox puts the pointers of files still there. A failed pre-step fails the
batch. Until a pointer lands, the Worker finds none or a pending one and passes.

The job `edge.outbox` writes one row per worker, so a slow put holds only its worker, and access
objects and `config.json` first, so a revocation never waits behind an import. A worker takes
`Pipeline.Lock(repo, path)` and rereads the asset before it writes. A row goes only after all its
writes succeed. Writes to one key are 1 s apart (R2's limit). A timed-out put has an unknown
outcome: its row stays, marked, and is applied again; while one remains, no region becomes ready
and no reclass walk of its repo ends.

**Access objects.** Change notices of `auth`, `repos` (a repo marks its groups too, since a group
is private when a member is), `netrules` and `publish` devices mark affected keys; the outbox writes
them within a second and recomputes the body when it writes, so an older body never lands over a
newer one. The job `edge.reconcile` (every 5 minutes) compares every object with the hashes in
`edge_written`, covering a lost notice. Every `config.json` write runs under one lock and builds
from the current mode, so an `enabled: true` never lands after the off switch.

**The controller** (job `edge.backfill`).

- **On.** `edge.enabled` with `storage=s3`, a `base_url` without a path and `edge.home_hint`. A
  first start wipes `edge/v1/` in every bucket, writes `config.json` with `home.ready: false` and
  queues every asset's pointer; until home is ready a missing pointer may be unwritten, so the
  Worker answers nothing. A restart resumes without a wipe: at init, before any commit, the module
  turns on in the saved state's buckets and rewrites `config.json`.
- **Regions.** A region added later loses its stale pointers, gets every immutable pointer, and
  the server asks for a gap pass; it is ready when a key-ordered comparison of home and regional
  `blobs/` finds every blob, with no unknown-outcome row left. A removed ready region leaves
  `config.json` first; the pre-steps keep changing its bucket until the drain end. A hint moved to
  a new bucket before a redeploy is safe: the old binding's `bucket.json` names the old bucket, so
  the Worker does not read the region.
- **Draining.** Edge off, a home location change (a `storage.LocationKeys` key other than
  `s3.redirect`) or a storage move writes `config.json` with `enabled: false` (retried until it
  lands) and marks "deploy needed" for a location change. The pre-steps keep changing the old
  buckets until the drain end, so no Worker with a cached `config.json` serves a changed file. The
  drain end is the latest time a Worker can hold an earlier `config.json`, plus 5 s, so lowering
  `edge.access_ttl` does not shorten a drain. A drain survives a restart; the old home opens with
  the storage settings' secret, so it needs the same access key ID, else the drain is dropped with
  a log line. A move to `storage=fs` turns edge off.
- **The Worker's state.** The controller reads `gap/state.json` (build, features, bindings) at most
  once a minute, even while edge is off once `edge.home_hint` is set, so the page sees a deployed
  Worker before edge is on.

**The check of the Worker** (`POST /api/edge/check`, **Check now**). The server writes `check.json`
with the SHA-256 of a fresh nonce and sends `GET <base URL>/.well-known/telesphorepo-edge/check?n=<nonce>`
through Cloudflare. That reserved path has a Worker route in every `wrangler.jsonc` the server
writes, and the Worker answers it first, on every host: for the right nonce before `exp` it deletes
`check.json` (one use), runs its gap check and answers 200; otherwise 403; both with
`Telesphorepo-Edge-Check: worker`, its version and features. The server's own handler answers with
`Telesphorepo-Edge-Check: server`, which means the Worker is not on that host or route; any other
answer is "other" (something in front, such as Cloudflare Access). The check changes no server
state and records no audit event.

**The speed test** (**Speed test**). The browser times one download through the Worker from a
bucket the admin picks. `POST /api/edge/speedtest` (UI session, `edge.manage`) issues a 10-minute
pass token. A GET or HEAD with `Telesphorepo-Edge-Region: <hint>|home|origin` and
`Telesphorepo-Edge-Pass` goes through every step of **What the Worker answers**; only then does the
Worker apply the choice, and it reports the outcome in `Telesphorepo-Edge-Override` and timings in
`Server-Timing`. The Worker drops both headers when it forwards, so the server never sees a pass.

Why this is safe: the choice comes after the credential, repo and pointer checks, and every bucket
holds the same bytes under the same pointer, so a pass changes which copy answers, never whether or
what. `origin` is what any request with a `tr_session` cookie gets. The pass only stops anyone from
pinning the far bucket or the server. It lives 10 minutes, the server keeps only its hash, the
Worker never logs it, and issuing one records no audit event. The page must run on the base URL:
the browser fetches from its own origin (`credentials: "omit"`, so the Worker sees an anonymous
request), and the Worker runs only on that host.

**Token last use.** The Worker batches the read tokens it served into `seen/` files (at most one
per isolate per 20 s, via `waitUntil`); the job `edge.seen` applies them with
`auth.Service.TouchTokens`, never moving `last_used_at` back.

**What the Worker answers.** A GET or HEAD where all of these hold; anything else goes to the
server:

1. `config.json` says `enabled` and `home.ready`, the `bucket.json` of `HOME` names the home
   bucket, and the Worker implements every `requires` feature. On a repo domain host, it maps the
   path to that repo's `standard` URL; `/`, a missing repo and another format go to the server.
2. The URL parses with the format's shape and the repopath rules. To the server: an encoded `/`,
   `\` or NUL; an empty, `.` or `..` segment; uploads, tags lists, referrers, `/v2/`, the catalog,
   `key.asc`; query parameter `_state` or `tsig` (a signed link, which the server checks).
3. The repo object exists, has the route's format and is not `guarded`.
4. The caller may read the repo:
   - no credential on an anonymous-read repo;
   - a read token `trp_` (Basic password with any username, or `Bearer trp_...`): token object,
     constant-time compare of the secret's SHA-256, `exp`, user `enabled` and `local`, then the repo
     in `read`, or `all`, or `anon`;
   - on `oci`, a core bearer of an anonymous, password, read token or device credential with no
     session scope and no upload binding: Ed25519 signature, `exp`, a scope of the repo with
     `Read`, then its credential's rows.

   A `tr_session` cookie, password, publish session, device proof, any other token or
   `Authorization`, or a failed check goes to the server.
5. A pointer exists, is not pending, and its `repo` and `path` match.

A group read goes to the server for a mutable path (every `Merger` path is mutable) or when any
member is `guarded`. The Worker walks members in order: skips one the caller may not read (without
the bearer's scopes, as `Principal.InGroup`), serves the first with a pointer, skips a hosted member
without one, and passes at a pending pointer or a proxy member without one.

Access objects and `config.json` go through isolate memory and the Cache API for `edge.access_ttl`
(0: read home every request), so a revocation reaches the edge within that time plus the outbox
write. The Cache API does not work behind Cloudflare Access, so the base URL host must not be
behind it.

**Which bucket.** The nearest ready region whose binding's `bucket.json` names its bucket, by
distance from `request.cf` to a fixed point per hint; else home. An immutable path reads its
pointer there, a mutable one in home; the blob is read there, then in home.

**The pointer cache** (`internal/edge/purge.go`). While `config.json` carries `pointer_cache`, the
Worker caches immutable single-repo pointers in the Cache API with a `Cache-Tag`, since Cloudflare
purges Cache API entries by tag, not URL. It caches only a serving pointer (never missing or
pending, never in isolate memory, which a purge cannot reach), and only when its put ended within
1 s of the R2 read's start.

The server keeps the **Path classes** invariant for the copies: every write that makes an immutable
pointer unusable or deletes it purges its tags 2 s after the change (covering the Worker's 1 s
window), on every zone of the live generations, before the commit or remove goes on. A failed purge
fails the step like a failed put. One queue sends all purges, since Cloudflare limits tag purges
per account; a 429 pauses it and fails no step. Assumed: a purge answered 200 is applied in every
data center by the time the commit goes on. A new pointer needs no purge: it replaces a pending or
missing one, which no copy holds. A generation (TTL, prefix, hosts, buckets) stays live, and
purged, until no Worker can hold its `config.json`, and is never reused, so a host or bucket that
returns never serves an older copy.

The server turns `pointer_cache` on only while its proof passes: with `edge.cloudflare_token`, it
purges an unused tag per zone, then per host has the Worker put two entries (through a one-time
`probe/` object on the check's route), purges one, and checks after 5 s that only that one is gone.
A purge Cloudflare refuses turns the cache off, and until the earlier generations end a replace or
remove of an immutable file fails. An import's date change (`Touched`) rewrites a pointer unpurged,
so a copy can show the earlier `Last-Modified` for up to the TTL.

**The answer** sets what `Reader.ServeAsset` sets on a streamed answer, from the pointer, and
follows the server on conditional and range requests; a range the server would answer 416 goes to
the server. `Telesphorepo-Edge: hit; region=<hint>` marks an edge answer. Where the server answers
`s3.redirect` with a 302, the edge streams the same bytes with 200.

**Forwarding.** `fetch()` with `redirect: manual` and the body streamed; `x-real-ip` and
`X-Forwarded-For` are set to the incoming `CF-Connecting-IP`, never a client value (with
`list:cloudflare` in the trusted proxies the server sees the real client). It drops the hop-by-hop
headers and `Expect`, so mvn's `Expect: 100-continue` stays on the client's hop. The forward is a
same-zone fetch of the request URL, so a repo-domain request reaches the server with its own host.

**Copies.** An R2 event notification rule on home's `blobs/` feeds the copy queue. The consumer
acts on current state, not the event: it HEADs the home key, then copies to regions that lack the
blob or deletes regional copies when home's is gone. A cron gap pass, when the server asks or daily,
lists home and regional `blobs/` in key order and queues what differs. A pass runs only while
`config.json` says `enabled` and requires nothing the Worker lacks.

**Deploy.** The server writes the Worker configuration (`internal/edge/wrangler.go`): routes per
edge format on the base URL host, the check route, `<domain>/*` per repo domain, a bucket binding
per region, the copy queue and the cron. `GET /api/edge/worker.zip` is the ready-to-run Worker
folder (`internal/edgedist`, see **Tooling**), refused when the build holds no Worker or one of
another server version, so a deploy from it has every feature the server uses;
`GET /api/edge/wrangler.jsonc` is for a checkout of `edge/`. Routes must be on zones of the
Worker's account: a repo domain zoned elsewhere gets no edge (the server serves it).

**Deploy from the server** (`internal/edge/deploy.go`, `cloudflare.go`). With
`edge.cloudflare_token` set, the server creates what `wrangler.jsonc` and `provision.ts` describe
through the Cloudflare v4 API. The account is the subdomain of `s3.endpoint`
(`<account>.r2.cloudflarestorage.com`, or a jurisdiction host). Another endpoint, or a base URL
host with a port (a route has none), refuses the deploy with that reason.

The token is an account API token (Manage account › Account API tokens, Custom), permissions named
as Cloudflare's token form names them:

- Entire Account: Developer Platform › Workers: Admin (Editor cannot create a Worker, which the
  first deploy does), Workers R2 Storage: Edit, Queues: Edit.
- All Domains, or each route host's domain: Developer Platform › Workers Routes: Edit, DNS & Zones
  › Zone: Read (Cloudflare's API reference calls Edit "Write").
- Not needed by the deploy: Cache & Performance › Cache: Purge (zone) for the pointer cache;
  Account & Billing › Account API Tokens: Read in the Entire Account policy (User › API Tokens:
  Read for a user token), so the token check can read the token's own policies.

The UI, the setting's help and permission refusals use these names. Each deploy step reads first
and changes only what differs. It never creates the home bucket, and refuses before any change a
route or queue consumer held by another Worker. A failure names the step, the call (never the
token), Cloudflare's error and the missing permission. The job `edge.deploy` redeploys on its own
(`edge.auto_deploy`) when the wanted state's hash differs from the deployed one or the Worker is
outdated. A removed region's bucket stays (delete it in Cloudflare). Every deploy records
`edge.deploy`.

**The check of the token** (`internal/edge/token.go`) shows each permission as present, missing or
unproven. It reads the token's own policies and matches permission groups by ID (Cloudflare calls
names cosmetic), deny before allow; without that read it probes with harmless calls, where a read
that went through only proves read access (`unproven`).

**The contract.** The Worker serves by the features it implements, never by server version, so a
release needs no Worker deploy unless it uses a feature the deployed Worker lacks. The contract is
the objects under `edge/v1/` and their fields' meaning:

- **Tolerant readers.** Both sides ignore unknown fields. A later field's default means the earlier
  behavior.
- **Features.** A feature names one thing a Worker must implement to read what the server writes.
  The Worker lists its own in `edge/src/features.ts` (the list only grows); the server's are
  `workerFeature`s in `internal/edge/features.go`. `repo-domains` is required while a repo domain is
  listed, since an older Worker on a domain route would read `/raw/x/...` as repo `x`.
- **`requires`** of `config.json`: features the server uses now that every request needs. A Worker
  lacking one passes every request to the server and runs no copy pass.
- **`needs`** of an object (access object, pointer, format of `config.json`): features needed to
  read it. A Worker lacking one passes requests that read it and serves the rest; a format of an
  unknown shape counts the same. A field that restricts access, which an older Worker would ignore
  and so over-serve, is always a need, so an older Worker fails closed.
- **A change**: additive with an old-behavior default needs nothing; a meaning an older Worker
  would get wrong is a new feature, in `requires` or `needs` only while used; renamed keys, moved
  paths or a changed meaning move to `edge/v2/`, where an older Worker finds no `config.json` and
  passes everything.
- **Outdated.** When the Worker lacks a `requires` feature, a deploy is needed and Settings ›
  Overview shows a notice to those who can open Edge delivery.

**Manual deploy.** In the Worker folder: `bun install`, `bunx wrangler login` (once), `bun run
provision`, `bun run deploy`. Wrangler's OAuth login has no R2 scope, yet Cloudflare's R2 guides
create buckets and notifications after a plain login. In CI, `CLOUDFLARE_API_TOKEN` (the server
deploy's permissions) replaces the login. `go tool task release` ships the same folder.

**Revocation speed.** The Cache API keeps an object per data center and `cache.delete` purges only
the local one, so `edge.access_ttl` (at most 5 minutes) is the bound.

| Change | Reaches the edge | A stale edge can serve meanwhile |
|---|---|---|
| Read token deleted or expired; user disabled; grant removed; repo made restricted; device revoked | within `edge.access_ttl` plus the outbox write (a second); 5 minutes plus the TTL after a server crash in that second | that repo to that credential, from a data center that cached the object |
| File deleted or replaced | at once (pointer deleted or pending, copies purged, before the commit) | nothing |
| Edge turned off | within `edge.access_ttl` | downloads the server would also allow |

**Other rules.**

- *Repo kinds and locks.* The edge only reads, so 405s for writes to proxies and groups, 409s and
  the mirror lock stay with the server.
- *Formats.* Apt with `Acquire-By-Hash` reads indexes by hash, so most Debian bytes come from
  regions. A docker pull still gets its token from the server once; a CI identity pull carries an
  anonymous bearer, so the edge serves it only on an anonymous-read repo. A proxy miss goes to the
  server, whose commit writes the pointer.
- *Audit and logs.* The server records no download events, so none are lost; edge downloads are
  absent from the server's access log. The Worker never logs `Authorization`, cookies or query
  strings.
- *Uploads.* Every other method goes to the server, body streamed. Cloudflare caps request bodies
  (100 MB Free and Pro, 200 MB Business, Worker or not), so CLI and large web UI uploads use the
  resumable upload routes in chunks, and Docker pushes use `docker.direct_uploads` (a DNS-only
  `docker.upload_url` host with no Worker route).
- *Pass-through errors.* A request with a body never relies on `passThroughOnException`: when the
  server fetch throws after the body was read, the Worker answers the 502 itself. It does not cover
  a CPU or memory overrun (error 1102), so the Worker keeps per-request CPU small. Workers Paid has
  no request cap: cost grows with traffic; the edge does not stop.

**Known difference.** The server refuses even a valid token from an address it blocks after failed
attempts (`authenticate` checks `limits.blocked` first). That block lives in server memory and
changes every few seconds; mirroring it would cost a write per change against R2's one write per
second per key. So the edge serves a valid read token from such an address: a valid 256-bit token
is not a guess, and every wrong guess still reaches the server and counts.

**Operator flow.** In **Edge delivery** the admin picks the home hint and regions. With a
Cloudflare API token the server deploys; without one, the admin downloads the Worker and runs
**Manual deploy**, again after a region change or a release that outdates it. `edge.enabled` stays
off until the Worker reported its build. A region serves once the page says ready. Download
snippets for private repos use a read token, never a password.

**Costs and limits** (Cloudflare, as of October 2026). The design leans on these: R2 allows one
write per second per key and one put up to about 5 GiB (copies above it are multipart); its reads
and lists are strongly consistent; deletes and egress are free. A gap pass stops before 5,000 subrequests (a Worker's default limit is
10,000 per invocation). Tag purges are rate-limited per account (5 calls a
minute on Free), so a large remove on Free takes minutes per thousand files. A download reads one
pointer, one blob and up to 3 access objects on a cache miss; a stored file puts one home pointer,
plus one per region if immutable; a new blob costs one queue message and one put per region. A
far-bucket read takes roughly 100 to 300 ms, a near one a few ms. Regional copies cost most, so
fewer regions cost less.

## Nexus sources

An admin adds a Sonatype Nexus 3 server as a **source** on the **Nexus** page
(`internal/nexus`). The server mirrors the mapped Nexus repos with repeated syncs. A mirrored
repo is read only. A **switch-over** of one repo ends its mirroring and makes it writable; the
other repos keep copying. When every repo has switched over, the source is **cut over**. Nexus
is read only through its REST API (`internal/nexus/client.go`). The API types are in
`internal/nexus/service.go`, `sync.go` and `merge.go`; a sync repo's `previous_checked` (the
`checked` of the last complete walk) exists because Nexus gives no total count, and the UI
estimates progress from it.

- **Status.** The server derives a source's `status` and `next_sync_at` (`last_sync_at` plus
  `nexus.sync_interval`).
- **Planning hook.** An `Importer` may have `Plan` (`internal/format/importer.go`): the config
  and field values of the target, the `Questions` the UI asks, the `Facts` it shows, and
  `Prepare`, run before the repo save. The save checks every plan before it creates any repo.
  Nexus names no format and parses no format setting. The raw `repositorySettings` item can hold
  the apt key pair and is never logged or shown. debian: a new hosted target asks `signing_key`,
  whose default is `source` (the Nexus key pair, imported as `nexus-<target>`) when Nexus gives
  it, so apt clients that trust the Nexus key keep trusting the repo. An encrypted key also asks
  its passphrase; a passphrase with another answer is 400, so it is never dropped without a word.
- **Importers.** A format's `format.Spec.Importer` lists the Nexus format ids it takes
  (`Sources`). The sync offers each file to `Importer.Run` (see **Byte pipeline**): `imported`,
  `skipped` (same bytes present) or `ignored` (not a file of this format). The non-obvious rules:
  - maven: a different release file fails; `maven-metadata.xml*` and SNAPSHOT files are replaced.
  - debian: `.deb` files go into component `main` of the target's `distribution`; a package the
    distribution holds is skipped without a download.
  - npm: Nexus's packuments are ignored; each tarball commits with its own `package.json`, and the
    source's dist-tags that name the version are set (else `latest` is the highest release).
  - docker: only manifests are offered; blobs are ignored because each manifest brings what it
    names, and `Store` commits an image's links, child manifests, manifest and tag in one
    `CommitAll`, so Nexus's page order does not matter. Every download asks for the four manifest
    types (`Importer.Header`), so Nexus sends a manifest as it stores it. A Nexus repo with the
    Docker Bearer Token Realm challenges each download; the client answers with `proxy.Answer`.
  - rpm: packages go through the RPM upload and are re-signed, so the target holds other bytes
    than Nexus. `Importer.Present` finds a package by its uploaded bytes (`upload_sha256`) from
    the file name and skips it without a download. Several Nexus repodata trees
    (`repodataDepth` > 0) go into the one repodata of the target.
  - Imported files have no uploader. The import owns the repo's `mirror` operation, so it writes
    while uploads answer 409; each file records `file.upload` with the actor `nexus`.
- **File dates.** Files keep the Nexus blob dates. A skipped file whose dates differ gets them
  (`assets.Pipeline.SetTimes`), so a later sync corrects files an older sync stamped with the
  import time.
- **Repo list.** The client reads `/service/rest/v1/repositorySettings`, or
  `/service/rest/v1/repositories` when that is refused (then group members and the apt key pair
  are unknown). A repo is supported when an importer takes its format and this server has its
  kind. A mapped repo that Nexus takes offline keeps its mapping and is frozen, since Nexus takes
  no uploads to it: every sync skips it, and a switch-over or a merge goes on with the files
  copied so far.
- **What Nexus gives** (checked against `sonatype/nexus3:3.96.4` with an admin account).
  `repositorySettings` returns every repo with its settings in one call, so the server makes no
  call per repo. apt hosted: `aptSigning.keypair`, the armored private key, without its
  passphrase. yum hosted: no signing key pair, not even for a repo created with one (checked
  against 3.97.2). group: `group.memberNames` in Nexus order. proxy: the remote, its times and
  the username; never the password. An offline repo still lists its assets, but each download
  answers 503.
- **Who can download.** The detail sends Nexus an anonymous `HEAD` of a repo's first asset (200
  public, 401 or 403 private). For an empty repo it sends one of a missing path: Nexus answers
  404 to an anonymous reader that may read the repo and 401 to one that may not. A failed check
  counts as private.
- **Source.** Create and PATCH check the connection first. The credentials are stored encrypted
  and are never returned or logged; error messages name the request and the HTTP status only.
- **Mapping.** `PUT .../repos` replaces the whole mapping, checked entry by entry first. A target
  is of the same kind and format, or created; it mirrors at most one source. A created proxy takes
  its password from the mapping, since Nexus does not give it. Each hosted target gets the
  operation `mirror` (state `held`, see **Repo operations**); a target with another operation
  cannot be mapped. A target that leaves the mapping loses the operation and keeps its files. A
  repo that switched over keeps its mapping whatever the save holds, so it never copies again.
  Targets change only through `repos.Service`. The save then starts a sync.
- **Sync.** One sync runs per source (a unique partial index). It copies the hosted repos that
  have not switched over, one after the other; proxies and groups have no files to copy. It pages
  `GET /service/rest/v1/assets?repository=X` from the saved continuation token. Nexus sets the
  page size and takes no page size parameter (10 assets before 3.74). A reader fetches pages
  ahead while workers import in parallel. A download uses the asset's `downloadUrl` only when it
  is on the base URL's host, else `{base}/repository/{repo}/{path}`. The runner saves each page
  (failures, counters, next token, log line) in one transaction only once that page and every
  page before it is done, so the saved token always follows a done prefix and a resume redoes the
  pages after it. Parallel imports are safe because the commit takes the path locks and the
  format's extra locks, and no importer depends on asset order.
- **Log.** A log line holds counts, never a path, a URL, a password or a token.
- **Retention.** A new sync deletes its source's syncs after the newest 100. The switch-over
  syncs stay.
- **Scheduler.** It reads `nexus.sync_interval` live and starts a sync for each due mirroring
  source with a mapping.
- **Sync end.** A sync is `done` when every repo finishes, also when some files failed (they are
  in the failure list), and `failed` when a whole repo failed. A shutdown leaves it `running`; it
  continues from the last saved page at the next start.
- **Switch-over.** `POST .../repos/{repo}/switch-over` runs a final sync (`trigger: cutover`) of
  the Nexus repos that copy into that target; `POST .../cutover` runs one of every hosted repo
  that still copies. When no repo failed, one transaction sets the sync `done`, records each
  repo's switch-over (`nexus_switch_overs`) and ends the target's `mirror` operation, so
  publishes start there; when no mapped hosted repo copies any more, it also cuts the source over
  and empties its credentials. If a repo failed, nothing switches over and they keep copying. A
  cancelled switch-over changes nothing; a restart continues an interrupted one. After cut-over
  the source's writes answer 409 and the history stays. Groups and proxies need no switch-over:
  they serve from this server already.
- **Rehearsal repositories.** `POST .../repos/{repo}/rehearsal` creates `<target>-rehearsal`, a
  private hosted repo like the target, where its publishers try their builds before the
  switch-over. Its repo field `rehearsal_for` is set only by this route (an API create or update
  that names it is 400). **Empty it** runs the removal of a repo delete without the row delete:
  settings, CI pipelines and grants stay.
- **Gradual move.** For a repo that many projects publish to: the projects move one at a time to a
  new repo while builds read both, and the new repo then merges into the Nexus copy
  (`internal/nexus/move.go`, `merge.go`).
  - **Move it project by project** (`POST .../repos/{repo}/move`) creates the moving repo (repo
    field `moves_into`, set only by this route) and optionally the group `<target>-all` that
    reads it first, then the target.
  - **Plan.** `GET /api/nexus/merges/plan` walks the source's files by path (keyset pages) with a
    point lookup of the target's file at each path; it writes nothing. Each file is **copy**,
    **skip** (same SHA-256), **conflict** (other bytes that `Admit` refuses; for a `Store`
    format, any other bytes), **rebuild** (a metadata or index file, merged or rebuilt) or
    **blocked**; a blocked file stops the merge from starting. `format.Spec.Carry`
    (`internal/format/carry.go`) adapts this per format: docker commits each image as one unit by
    digest; npm carries dist-tags as labels. The merge saves which labels the target takes before
    the first file goes in, because the copy changes the target.
  - **Start** (`POST /api/nexus/merges`) takes the plan of now again; every conflict needs a
    choice (`keep` or `take`). One transaction stores the merge, ends the target's `mirror`
    operation and begins the `merge` operation on both repos, so publishes to both answer 409.
  - **Steps.** Each step is saved before the next starts, so a restart continues at its step:
    1. `sync`: a final sync of the target; it ends without the switch-over.
    2. `copy`: the source's files after `cursor`, in path order, committed by digest (no bytes
       copied). While a merge is past its final sync, the source's syncs skip its target.
    3. `access`: the source's grants, CI pipelines and group memberships also apply to the target.
    4. `switch`: the switch-over, as above.
    5. `retire`: `repos.Service.Retire` renames the source to `<name>.merged-<id>`, makes its name
       a redirect to the target and begins its `delete` operation, in one transaction.
    6. `rename` (keep `source`): the target takes the source's name; its old name redirects.
    7. `done`: the retired source is deleted as a repo delete (after a restart, by `core.ops`).
  - **Failure.** A failed step sets the merge and both operations `failed`. Before the
    switch-over the target keeps copying from Nexus and the source keeps its files. **Resume**
    (`POST /api/repos/{name}/operation/resume`) runs from the step, with a new final sync first
    when it failed in `sync` or `copy` while the target still copies. **Stop**
    (`DELETE /api/repos/{name}/operation`) gives a target that still copies its `mirror`
    operation back; copied files stay.
- **Delete.** `DELETE .../sources/{id}` removes the source and its history. The repos and their
  files stay.
- **UI.** The admin-only **Nexus** pages (`/#/settings/nexus`, the **merge plan** and the
  **merge**): `web/src/pages/server/NexusSource.svelte` and `web/src/pages/server/nexus/`. While a
  repo has the `mirror` operation, its page shows the operation's message to the people who could
  upload or delete. A new CI pipeline that starts from a repo that still copies from Nexus gets its
  rehearsal repo chosen in its place.

## Group and proxy repositories

A format takes proxy repos when its `format.Spec.Kinds` lists `proxy` (it then sets `Classify`
and `ProxyProbe`) and group repos when it lists `group`; `repos.Service` refuses another kind
(400). npm, PyPI, Cargo and NuGet render their group documents themselves: the first member that
has a package owns its name (`assets.Reader.Owner`). Neither kind takes uploads, publish sessions
or CI pipelines: minting a session for one answers 409 with the read-only text, and a CI pipeline
that lists one answers 400.

**In memory.** `repos.Service` keeps the members of each group and the remote of each proxy
(credentials decrypted) in memory, loaded at start and changed after each write. Request paths
never read `repo_group_members` or `repo_proxies`. A proxy's success is written to `last_ok_at`
at most once a minute; an error is written each time. Credentials that do not decrypt at start (a
changed master key) are dropped, and the server logs the repo ids.

**Groups.**

- A group holds 1 to 50 hosted or proxy members of its format; groups do not nest. A group whose
  members were all deleted stays and serves nothing.
- A read of a group needs read access to the group. The core read path (see **Byte pipeline**)
  tries the members in order and skips a member the reader cannot read
  (`access.Engine.GroupMembers`; when the client address is in the group's allowed networks, every
  member is read). The first member that has the file serves it. A proxy member fetches from its
  remote only when no member before it has the file. No member: 404, or 502 when a proxy member's
  remote failed. Members and the reader's grants are read once per request (`repos.Remember`,
  `auth.Service.WithGrants`).
- Stored copies first (`Reader.ResolveLive`): before a proxy member asks its remote, the read
  checks the members' stored rows as far as the answer is the same as the read in order. A path
  that names its bytes (`assets.Class.Addressed`: OCI blobs and manifests by digest) serves the
  same bytes from any stored copy, so the pass goes on past a proxy member without one. It stops
  at a proxy member that would fetch a changing file or an immutable file that does not name its
  bytes (a Maven release: a later member may hold other bytes at that path, and the first member
  wins).
- A merged file reads its members at once and merges in member order. The merged bytes are kept
  in memory under the group, the path and each member file's SHA-256, so its checksum files and
  the next request reuse them until a member file changes.
- Maven's `Merger`: an artifact-level or group-level `maven-metadata.xml` merges the readable
  members that have it, each read in the charset its XML declaration names (the Gradle Plugin
  Portal declares US-ASCII; a member file that does not parse is left out). Its checksum files
  are computed from the merged bytes. With one such member, that member's own files serve. A
  version-level file (under `-SNAPSHOT`) comes from the first member that has it.
- `assets.Reader.Owner` picks, per package name, the first member in read order that the reader
  can read and that has the name. That member serves every version of the name, so a later
  member cannot shadow it. A member whose check fails is skipped with a warning; `Owner` answers
  `ErrRemoteFailed` when no member has the name and a proxy member's remote failed.

**Proxies** (`internal/proxy`).

- A new proxy checks its remote once with the format's `ProxyProbe` (`force: true` skips it,
  `proxy.Check`). A token realm that refuses the credentials fails the check.
- `format.Spec.Classify(repo, remote, path)` sorts a path into an `assets.Class`
  (`internal/assets`): `Changing` (checked again after `metadata_ttl`), `Remote` (the remote path;
  a leading `/` is from the root of the remote's host), `Files` (another base URL, for a registry
  that serves its files from another host; taken only from the repo config and checked with
  `proxy.FilesURL`, never from a remote document), `Header`, `KeepType` (keep the remote's
  content type) and `Unchanged` (check with a HEAD instead of a conditional GET). Each format's
  `Classify` says which of its paths change (metadata, indexes, tags) and which are immutable.
- A cached file that is immutable, or changing and checked within `metadata_ttl`, serves from
  storage. Otherwise the server fetches with `If-None-Match` and `If-Modified-Since`; a 304 sets
  `checked_at`. A format without `RemoteAuth` sends the remote's credentials as HTTP Basic on every
  request; a format with it sends them only to answer a 401 challenge, once, then resends.
  `proxy.Answer` is the registry token protocol (`Bearer realm,service,scope` → a GET of the realm
  with Basic credentials; a token without `expires_in` lives 60 s).
- One fetch per (repo, path) runs at a time. A request for an uncached file waits for that fetch
  and serves its result. A request with a stale cached copy does not wait: the stale copy serves
  at once, so only the request that checks waits for the remote.
- A 200 is spooled (`max_upload_size`) and committed with source `proxy` (no uploader, the
  remote's `Last-Modified` as the dates). A file the format refuses
  (`*assets.Refusal`, e.g. bytes without the digest of their path) or a body that breaks off is a
  remote failure.
- The request that fetches an uncached immutable file gets the bytes while they spool
  (`assets.Live`): a GET without `Range` or a condition, 200 with no `ETag` or checksum headers,
  since the hashes are known only at the end. The last block goes out after the commit: a body
  that breaks off, is too large or is refused aborts the connection, so the client never takes a
  short body for the file. A client that takes no bytes for 30 s is dropped and the fetch goes
  on. A changing file, a `KeepType` file (an OCI manifest, whose `Admit` sets the media type), a
  HEAD and a request with `Range` or a condition serve from storage after the commit.
- A remote 404 or 410 answers 404 and is remembered for `not_found_ttl` in memory. A cached file
  the remote no longer has stays and serves. A 401 or 403 to a request with a Bearer token counts
  as 404 (`proxy.ErrDenied`): Docker Hub, GHCR and Quay answer so for a name that does not exist
  or that the credentials cannot read. A refusal at the token realm stays a refusal of the
  credentials.
- A remote error, a refusal or no answer headers within 30 s: a stale cached file serves with a
  warning in the log; with nothing cached the answer is 502 "The remote repository did not
  answer." The failure is remembered for 30 s per (repo, path), so a stale copy serves without a
  remote request and the waiting requests get the same answer; a new request for an uncached file
  asks again. A fetch continues when the client goes away, for at most 30 minutes.
- The fetch follows at most 5 redirects. Credentials go only to the remote's host: a redirect to
  another host drops them, and a fetch from a `Files` base on another scheme or host sends none.
  No log line holds the credentials.
- A Debian proxy serves the remote's files unchanged: no indexer, no signature of its own.
  Clients keep the remote archive's key, so its apt snippet has no key step and no `signed-by`.
- An Arch proxy serves the remote's paths unchanged: a proxy of an Arch mirror is
  `Server = HOST/arch/<proxy>/$repo/os/$arch`, a proxy of a Telesphorepo Arch repo
  `Server = HOST/arch/<proxy>/$arch` under the remote repo's section name. pacman checks the
  remote's signatures with the keys it trusts for the remote.
- An RPM proxy serves the remote's paths unchanged (`baseurl` is the proxy plus the remote's
  layout), and dnf checks packages with the remote packagers' keys. A remote with
  `--simple-md-filenames` repodata can serve a cached `primary.xml.gz` older than a fresh
  `repomd.xml` for up to `metadata_ttl` (dnf reports a checksum mismatch until then); a remote
  that re-signs packages at the same paths needs its cached `Packages/` deleted. dnf5 asks parts
  of a `.zck` file with several ranges; the read path answers the whole file, and librepo then
  asks one range per request.
- Deleting files of a proxy works; the next request fetches them again.

## PyPI

The module `pypi` (`internal/pypi`) is the format `pypi`: Python packages that pip, uv and poetry
install and that twine, `uv publish`, `poetry publish` and `telesphorepo publish` upload. The index
URL is `HOST/pypi/{repo}/simple/`, the upload URL `HOST/pypi/{repo}/` (or `.../legacy/`). Hosted,
proxy and group repos.

**Files.** A hosted repo stores each wheel and sdist at `packages/<project>/<file name>` and, for a
wheel, its exact `*.dist-info/METADATA` at the same path plus `.metadata` (PEP 658), in one
commit. `<project>` is the PEP 503 normalised name. The files are immutable: the same bytes are
kept, other bytes under an existing name are 409 `File already exists`. A write outside this
pipeline (a core upload session) is 400. A proxy stores upstream pages at `.simple/<project>` and
files at `files/<rest>` and `remote/<rest>`.

**Pages.** The project list and project pages are rendered, never stored at their URL
(`internal/pypi/simple.go`). A name that is not normalised, or lacks the trailing slash, is 301 to
the normalised page. `Accept` picks the form (PEP 691): JSON v1, HTML v1 or `text/html`, highest
`q` winning and the server order (JSON, HTML, `text/html`) breaking a tie; no `Accept` is
`text/html`; none acceptable is 406. Every page answers through `format.WriteDocument` with `Vary:
Accept`, a strong `ETag` and 304. Both forms render from one model; JSON is `api-version` 1.1 (1.0
when a proxied HTML page gives no sizes). Every URL is relative to the page
(`../../packages/...`), so a group serves the page of its owning member unchanged and the client
follows each file to the repo it asked.

**Upload.** `POST /pypi/{repo}/` and `.../legacy/` (`route.FormatWrite`) take the legacy upload
form: the file in `content`; `:action` other than `file_upload` or `protocol_version` other than
`1` is 400; `sha256_digest` and `md5_digest` must match; other fields are ignored. The project and
version come from the file's own metadata and must match the file name; a disagreeing form
`name` or `version` is 400. 201 for a new file, 200 for the same bytes, 409 for other bytes. With
`?upload=<id>&sha256=&size=&filename=` and no body the route finishes a resumable upload of the
same publish session. An error answers in text with the message in the body (twine prints it with
`--verbose`, uv always); a client that asks for JSON gets `{"error"}`. twine, uv and poetry send
the session token as Basic with user `__token__`.

**Rows.** `OnCommitted` and `OnRemoved` keep `pypi_projects` and `pypi_files`, so a file delete in
the web UI drops the file from the page and the last file drops the project. Each change moves the
project's `rev`; rendered pages are kept in a `format.DocumentCache` keyed by form, repo, project
and `rev`, so a page renders once per change and a change shows at once.

**Project list.** Kept in a second `format.DocumentCache` keyed by the form and the repo's
`pypi_index_revs` revision; a group's key holds the revisions of the members the caller reads. A
request costs a point lookup per repo, and an `If-None-Match` that names the key's `ETag` is 304
without a build. A build reads `pypi_projects` (hosted) or `.simple/` assets (proxy) in key order
and merges a group's sorted member lists.

**Proxy** (default remote `https://pypi.org/`). The config field `files` (default
`https://files.pythonhosted.org/`, checked with `proxy.FilesURL`) is the host the remote serves
package files from. The upstream page is fetched with a JSON-first `Accept` and cached at
`.simple/<project>` (`Changing`); an HTML answer is read as PEP 503 anchors. Each file URL maps to
`files/<rest>` under the `files` base or `remote/<rest>` under the remote; a file at any other
address is left out of the page, so the server never fetches an address a remote document names.
The remote's credentials go only to the remote's host. A proxy's project list is the projects it
has cached; the upstream list is not proxied.

**Group.** The first member that has a project (`assets.Reader.Owner`) renders its page and no
later member is asked, so a public package cannot shadow a private one. A `packages/` file goes to
that member; a `files/` or `remote/` file to the first proxy member that has it. The project list
is the union of the members' lists.

**Edge.** `Immutable: packages/**/*, files/**/*, remote/**/*`. The pages are rendered, so the
Worker finds no pointer for them and sends them to the server.

**Import.** `Sources: ["pypi"]`: each wheel and sdist of a Nexus pypi repo goes through the upload
code; every other file is ignored.

Versions, web UI upload, snippets and CI tools: `internal/pypi/versions.go`, `webupload.go`,
`usage.go`.

## Docker registry

The module `docker` (`internal/docker`) is the format `docker`: an OCI registry that serves the
OCI Distribution Spec 1.1 for hosted repos, proxies of other registries and groups. It holds
container images, Helm charts and any other OCI artifact (ORAS). The repo is the first segment of
an OCI repository name: an image is `<host>/<repo>/<image>[:tag|@digest]`, and the image has one
or more segments. Digests are `sha256` only. The registry serves at the root of an origin
(`/v2/`, `/docker/`); a `base_url` with a path does not move it. Name and tag rules:
`internal/docker/names.go`.

**Objects.** Every OCI object is an asset of the repo, and its blob is a core blob shared across
repos: a link `<image>/_blobs/sha256/<hex>`, a manifest `<image>/_manifests/sha256/<hex>` and a
tag `<image>/_tags/<tag>` (the blob of its manifest). `Admit` refuses the generic upload routes
("push with an OCI client"), bytes without the digest of their path, a pushed manifest that
references a blob or child manifest not in the image or the same commit (400
`MANIFEST_BLOB_UNKNOWN`; foreign layers excepted), and a tag whose manifest is not in the image.
Each link keeps a use count of the manifests that reference it. A delete of a manifest that an
index outside the removal lists, or of a link that a manifest outside the removal uses, is 409.
`Remove` takes a folder in path order and batches, so links (`_blobs/` sorts first) go in an
earlier batch than the manifests that use them; a manifest of a later batch gives back no use of a
link that is gone. In one batch tags go first, then manifests, then links.

**Routes.** ServeMux cannot match a name in the middle of a path, so the format registers method
catch-alls and parses the raw path (`internal/docker/routes.go`; an encoded `/`, `\` or NUL is
400). Every `/v2/` answer has `Docker-Distribution-API-Version: registry/2.0`; every refusal is OCI
error JSON, with engine refusals mapped to OCI codes in `internal/docker/errors.go` (a write to a
proxy or group is 405 `UNSUPPORTED`). The policies:

- `GET /v2/` and `/docker/token` take no policy; the handler reads the credential. `/v2/` without
  `Authorization` is 401 with the challenge, which is how clients find the token endpoint.
- Reads take `FormatRead` + bearer, writes `FormatWrite` + bearer, deletes a publish session +
  bearer (with `repo.delete` for a manifest). Manifests serve the stored bytes and media type
  (`Accept` is ignored) and are never redirected; blobs may redirect to S3.
- A mount (`?mount=&from=`) needs read access to the source repo, and any miss starts an upload
  (202), never 401 or 403. A mount never contacts a remote.
- A push of a tag to another digest moves the tag; the old manifest stays, untagged, until
  deleted. A blob delete is 405 "delete the manifests; the server removes layers that no manifest
  uses".
- `GET /v2/_catalog` lists the images of the repos the credential downloads from the client
  address; bearer scopes do not limit it. A page reads only rows the caller lists: it walks the
  caller's repos that hold an image in name order (`ReposWithImages`), one seek per repo, and a
  holder of all repositories reads one range of `docker_images_full`.

**Challenge.** A 401 carries `WWW-Authenticate: Bearer realm="<scheme>://<host>/docker/token",
service="<host>",scope="repository:<name>:<actions>"` (`pull` on GET and HEAD, `pull,push` on
PUT, POST and PATCH, `delete` on DELETE). The scheme and host are those of `docker.upload_url`
when direct uploads are on and the request came to its host, else of `base_url`, else the client's
scheme (`httpx.ClientScheme`) and the request host. A `scope` refusal adds
`error="insufficient_scope"`.

**Token endpoint.** `GET /docker/token` with Basic credentials, or `POST` with a form
(`grant_type=password`, or `refresh_token` read as a password). The answer holds one core bearer
token in `token` and `access_token`; it never returns a refresh token. A blocked client address
gets 429 first. The password is a device proof of the credential helper
(`trx.<device id>.<nonce>.<signature>`, see **Publishing**), a CI identity token (a JWT), else a
password or token checked with `auth.Service.Authenticate`. The answer grants a subset of the
scopes:

- Pull: `access.Engine.Allows(Read, repo.download)` (the token request's address counts for
  allowed networks). A publish session and a CI token read what anonymous reads outside the
  session's repo; a device proof reads what the device's user reads.
- Push and delete: one repo per token. A `trs_` token gets them on its own repo. A CI token
  mints a session through `publish.Minter.Mint` (push only); a device proof mints one too (push
  and delete). A proxy or group gets no mint. A password or read token gets no push: publishing
  needs a publish session.
- Without scopes (`docker login`): a credential that verifies gets 200; a CI token or device proof
  passes `Minter.Check`, and a failure counts as a failed attempt of the address.

A token lives 15 minutes, or the rest of the session when shorter, at least 60 s. A mount from a
private repo with a CI or `trs_` credential falls back to an upload, because a publish session
reads only what anonymous reads outside its repo; with a device proof the mount works.

**Unused layers.** A link at 0 uses gets `unreferenced_at`. The job `docker.links`
(`internal/docker/links.go`) removes links unused for `docker.link_grace` (source `generated`, no
audit event); a link a manifest took meanwhile stays. The core blob GC then deletes the blob after
`blobs.gc_grace`. The grace also keeps a layer that a client pushed before its manifest, so it
must outlast the slowest push. Untagged manifests of a hosted repo stay until deleted.

**Uploads.** Uploads are core upload sessions (`assets.Uploads`): no blob is held in memory, and
`max_upload_size` caps one blob. The registry sets no body limit of its own and sends no
`OCI-Chunk-Min-Length`.

**Proxies and groups.** A docker proxy caches any OCI registry. A path in the remote URL is a name
prefix: `https://ghcr.io/acme/` proxies the images of `acme`. On `registry-1.docker.io` without a
prefix, a one-segment image is `library/<image>`. `Classify` (`internal/docker/proxy.go`) makes a
tag changing and a manifest by digest or a blob immutable; a tag asks with the four manifest types
in `Accept` and keeps the remote's content type. Credentials go only through `RemoteAuth`, and the
module keeps each token by remote, credentials and scope until shortly before it expires, so a
fetch costs no token request while a token lives. A check of a stale tag sends a HEAD, because
Docker Hub counts only GETs against its pull limit: the same `Docker-Content-Digest` only sets
`checked_at`. When a fetched tag moved, `AfterCommit` removes the old manifest (and the platform
manifests of an old index) that no tag names and no index lists, so `docker.links` and the blob GC
reclaim the layers only the old version used; a client that asks for the old digest gets it from
the remote again. A group serves a link or manifest by digest from any member that stores it
before a proxy member asks its remote (`Addressed`). A proxy's tags list is the remote's; a remote
that denies the name gives 404 `NAME_UNKNOWN`, one that fails gives the cached tags. The catalog
lists what a proxy has cached. A group merges its readable members' tags by tag and referrers by
digest.

**Upload host (the 100 MB request limit).** Behind Cloudflare (Free and Pro plans), each image
layer must be under 100 MB; pulls have no limit. Docker 29, containerd 2 (nerdctl, BuildKit and
buildx), Podman, skopeo, crane and ORAS send each layer in one request body: one PUT, or one PATCH
and an empty PUT. No client reads a maximum chunk size, and none splits a layer after a 413.
`docker.direct_uploads` ("Expose the origin IP for Docker uploads", off by default) lifts the
limit: the layer bytes go to `docker.upload_url`, the DNS-only name or address of the server. This
makes the IP address of the server public, and the admin opens that port in the firewall for the
clients that push. The setting group's `Validate` refuses the toggle without an address. Not
behind Cloudflare, nothing is needed. Clients name only the main host. With the toggle on, every
answer that names an upload session has an absolute `Location`:
`<docker.upload_url>/v2/<name>/blobs/uploads/<id>?_state=<upload token>`. A request that came to
the upload host gets the relative path. The upload token is a core bearer token of the caller's
publish session for the repo that `auth.Service.IssueUploadToken` binds to the upload id; each
answer carries a new one. The `/v2/` routes read it (`route.Policy.URLToken`) only from the
`_state` parameter of that upload's URL, and only when `Authorization` gives no credential; it
never verifies from a header, on another path or for another upload. The access log never writes
query strings. The mount 201 and the monolithic `POST ...?digest=` keep a relative `Location`.
The clients follow the `Location` this way:

| Client | Credential on the upload host | Bytes |
|---|---|---|
| containerd 2 pusher (Docker 29 with the containerd image store, BuildKit, buildx, nerdctl) | none: it drops its authorizer on a host change | one PUT |
| Docker 29 with a graph driver (the distribution client) | none: it has no challenge of that host | one PATCH, then an empty PUT |
| containers/image (Podman, skopeo) | the bearer token of the main host | one PATCH, then an empty PUT |
| go-containerregistry (crane, ko) | none; it refuses a private or loopback IP literal as the host | one PATCH, then an empty PUT |
| oras-go v2.6.1 and later (ORAS 1.3, Helm 4) | it refuses a `Location` on another host (GHSA-jxpm-75mh-9fp7); it pushes to the upload host by name, where the `Location` stays on that host and the challenge names the token endpoint of that host | one PUT |

The registry supports the full chunked protocol for clients that use it (oras-go v3 with a chunk
size).

**Snippets and CI.** `internal/docker/usage.go` and `tools.go`. The push snippets note the 100 MB
layer limit when direct uploads are off and the Cloudflare IP list is among the trusted proxies
(`netrules.Service.TrustsList`, an in-memory read). ORAS and Helm push to the upload host when
direct uploads are on, because they send layers only to the host of the push. The CI tool `docker`
pushes with a Docker config of its own whose `credHelpers` name the CLI's credential helper; the
helper mints the publish session from the job's identity token, so the job runs no `docker
login`. The OIDC token pulls public images only, so a build from a private image logs in with a
read token first.

**Versions and builds.** An artifact is an image folder `<image>/` with a `docker_images` row; its
kind (Helm chart, image, artifact) follows the newest tag's config media type. Versions are the
tags, newest first (`docker_tags_recent`); an index's files are its platform manifests with the
classifier `<os>/<arch>[/<variant>]`.

**Web UI.** `web/src/modules/docker/module.ts` and `ManifestPart.svelte` (which reads
`GET /api/docker/{name}/manifest`). No repo tab and no config fields.

**Demo and seed.** `internal/docker/demo.go` and `seed.go`.

## Cargo registry

The module `cargo` (`internal/cargo`) is a Cargo registry with the sparse index protocol (Cargo
1.68 or later; a private repo needs 1.74 or later): hosted repos, proxies of crates.io (or another
sparse registry) and groups, at `sparse+<base>/cargo/<repo>/index/`. Cargo sends its token as a raw
`Authorization: <token>` header, so every Cargo route sets `Policy.TokenHeader = "Authorization"`
(a read token or a publish session; Basic works too). Every refusal is a Cargo error
`{"errors": [{"detail": "..."}]}` (`Policy.Errors`).

**Routes** (`internal/cargo/api.go`, `index.go`). Reads: `index/config.json`, the index files and
the `.crate` assets. Writes (publish session): `cargo publish` and `cargo yank` / `--undo`.
`cargo search` and `cargo owner` answer 404 with a Cargo error that says what to do instead.
`config.json` has `"auth-required": true` when the repo has no anonymous read, and such a repo
answers a request without credential with 401 and `WWW-Authenticate: Cargo login_url=...`: that
challenge makes Cargo send its token on every request. Cargo sends `If-None-Match` for each index
file, so index files are rendered with a strong ETag of the revision and answer 304.

**Hosted repos.** A `.crate` is stored through `Commit` with its index entry as `Write.Meta` and
the folded name (lowercase, `-` as `_`) as an extra lock. `Admit` refuses (409) a name that folds
to another crate's (as on crates.io) and a version the repo has ignoring build metadata; the same
bytes at the same path are kept, though a publish of them answers 409. A write without an entry (a
core upload session) is 400. `OnRemoved` drops the version of a deleted `.crate`, so a file delete
in the web UI removes it from the index at once. Each publish, removal and yank moves the crate's
`rev` on (`max(rev + 1, unix ns)`, so a crate removed and published again never reuses one); the
rendered file is cached by repo, crate and `rev`, so a request costs one point read of the name
row and a matching `If-None-Match` answers 304 without rendering.

The entry follows the Cargo book; the server computes `cksum`, and features with `dep:` or `?/` go
to `features2` with `"v": 2`, as crates.io writes them. A publish converts the request JSON as the
Cargo book says; an upload or import converts the `Cargo.toml` inside the `.crate` as Cargo does
for a publish. The two ways must give byte-equal entries for one crate.

**Proxies.** The remote is a sparse index (default `https://index.crates.io/`). Index files are
cached `Changing` under `.index/` and served unchanged: `cksum` holds whatever the `dl`, and only
`config.json` is the repo's own. A `.crate` comes from the repo config's `download` template;
`{sha256-checksum}` is refused because the proxy fetches a crate before it knows its checksum. The
remote's credentials go only to its own scheme and host.

**Groups.** The first member in read order that has the crate owns the name
(`assets.Reader.Owner`) for the index file and the `.crate`, so a later crates.io proxy cannot
shadow a hosted crate with higher versions. A proxy or group replaces crates.io through
`[source.crates-io] replace-with`.

**Tables** (`internal/cargo/migrations`): `cargo_versions`, the versions of hosted crates, unique
without build metadata; `cargo_names`, the folded names of a repo with `rev`, the revision of the
crate's index file.

**Versions and builds.** The artifact folder is `crates/<name>/`; a hosted repo's versions page by
`sort_key`, one build each. Proxies and groups have no rows.

**Snippets** (`usage.go`). A private repo needs `credential-provider = "cargo:token"`, because
Cargo refuses an `auth-required` registry without a credential provider.

**Uploads and imports.** The upload plan reads the first MiB of each `.crate`. The Nexus importer
takes `<name>-<version>.crate` or a path ending in `<name>/<version>[/download]`, and ignores index
files.

**Edge.** `Immutable: ["crates/*/*.crate"]`. Index files are rendered, so the Worker passes them to
the server.

## npm registry

The npm module (`internal/npm`, routes in `routes.go`) serves `HOST/npm/{repo}/` for npm, pnpm,
Yarn 2+ and Bun, with hosted, proxy and group repos. Tarballs (at the paths of registry.npmjs.org)
are assets; packuments are rendered per request and never stored at their own URL.

- **Routes.** A scoped name comes as `@scope%2fname` or as two segments; `httpx.RepoPath` refuses
  `%2f`, so one route per method (`/npm/{repo}/{rest...}`) reads the escaped path itself.
  - `GET <name>`: `Accept: application/vnd.npm.install-v1+json` gets the abbreviated packument,
    else the full one, with `Vary: Accept`, a strong `ETag` and 304.
  - `GET <name>/<version or dist-tag>` reads only that version; a dist-tag wins over a version of
    the same name. pnpm 11+ run through corepack fetches its binary package this way, without
    credentials on plain HTTP.
  - `PUT <name>` with a `.tgz` attachment publishes one strict-SemVer version, read as a stream.
    The server computes `integrity` and `shasum`; a `.sigstore` attachment is stored but not
    advertised. An existing version is 403. Without an attachment it is a metadata update
    (`npm deprecate`).
  - The steps of `npm unpublish` (`-rev/<rev>` routes) need `repo.delete`; a stale `_rev` is 409.
    The `RepoRule` picks the operation from the URL (`Ops`, `Pick`), so a CI session cannot
    unpublish. Deleting the `latest` dist-tag is 400.
  - `npm login` routes answer 401 with a message pointing to a read token in `.npmrc` and to
    `telesphorepo publish`.
- **Credentials.** npm, pnpm and Bun send `Authorization: Bearer <token>`, Yarn too with
  `npmAlwaysAuth`; a read token reads, a publish session writes.
- **Rows.** A tarball commit whose write carries the version (`Write.Meta`) inserts its rows in the
  commit's transaction. `OnRemoved` deletes the version, its tags and its `.sigstore`, and moves
  `latest` to the highest release, so a file delete in the web UI unpublishes. Every change moves
  `_rev` on. `Admit` refuses a write without a version (a core upload) with 400.
- **Proxy.** The abbreviated and full packuments are cached separately (`Changing`), each fetched
  only when a client asks for that form. Rendering points `dist.tarball` here and keeps
  `integrity`, `shasum` and `signatures`; `dist.attestations` goes, and so does a version without
  a `.tgz` tarball.
- **Group.** The first member that has the name owns it (`assets.Reader.Owner`) and serves its
  packument and tarballs, so a public package in a later proxy never shadows a private name. A
  failing member is skipped; with none left and a failed proxy, 502.
- **Cache.** Rendered documents are cached keyed by form, name, the package `_rev` or the SHA-256
  of the cached remote document, and the base URL, so a big remote packument is rewritten once per
  change of the cached copy, and a matching `If-None-Match` answers 304 without the body.
- **Versions.** `<name>/`, newest first by `sort_key`, one build each. Proxies and groups have none.
- **Upload.** An upload moves `latest` to the version when it is a release above the current one.

## NuGet repositories

The module `nuget` (`internal/nuget`) speaks NuGet V3 to dotnet, nuget.exe and Visual Studio, with
hosted, proxy and group repos. The source is `<base>/nuget/{repo}/index.json`; every resource is
under `/nuget/{repo}/`, so dotnet sends the source's credentials to each once the first 401
(`WWW-Authenticate: Basic`) asked.

**Stored files and rendered documents.** A package is two assets under
`v3-flatcontainer/<id>/<version>/`: the `.nupkg` and its root `.nuspec`, id lowered and version
normalised (`version.NuGet`). A `.nupkg` is never changed, so a signed package keeps its signature.
Every other document is rendered (strong ETag, 304) and cached keyed by repo URL, document and the
package's `rev` (hosted) or the SHA-256s of the cached remote documents (proxy): a request reads one
package row and renders only when the key is new, and a hosted render reads only what the document
holds.

**Routes** (`nuget.go`). Push, unlist and relist set `Policy.TokenHeader = "X-NuGet-ApiKey"`, so the
API key is a publish session; that header comes before the `Authorization` of the source's read
credentials.

- `vulnerabilities/index.json` names one empty page, so restore finds the source audited and warns
  nothing.
- **Search** (`search.go`) answers hosted packages only (a group: its hosted members, each leaving
  out ids an earlier one has). `q` of 3 or more characters uses the trigram index
  `nuget_packages_search`, so results come in index order; shorter `q` is an id prefix. A page reads
  at most `skip + take` packages, `totalHits` counts up to 10,000, and version metadata is read only
  for the answered page.
- **Push** finishes a resumable upload of the same session when given `?upload=<id>&sha256=<hex>`
  and no body. A version the repo has is 409 even with the same bytes, so `--skip-duplicate`
  skips it.
- **Unlist** keeps the version restorable by its exact version, as dotnet expects.

The push, upload, Nexus import and demo share one ingest: a `CommitAll` of the `.nupkg` and the
`.nuspec` with the package version as an extra lock. `Admit` refuses other bytes at a package path
with 409, and a write without a parsed package from a non-proxy source (a core upload) with 400.
Every change moves the package's `rev` on. dotnet prints only the status line of an error.

**Proxy** (`upstream.go`). The remote is a service index (default nuget.org) or its folder. Remote
documents are cached under `.upstream/`, a folder no client asks for; packages are fetched once.
The proxy follows only resources on the remote's scheme and host, and drops every catalog value
pointing at the remote's host when it renders registrations with this repo's URLs. No usable
service index, or nothing cached while the remote is down: 502.

**Group.** Each package id comes from the member that owns it (`assets.Reader.Owner`), so a public
package cannot shadow a private one with the same id and a higher version.

**Snippets and tools.** `usage.go`; a plain-http source needs `allowInsecureConnections`.

## Files, uploads and deletes

`internal/files` serves the file tree; deletes and uploads go through the byte pipeline (see
**Byte pipeline**), and the core module reads manual uploads. The core names no format; it calls
the capabilities of the repo's `format.Spec` that are set.

- **Tree.** `GET /api/repos/{name}/tree` lists one folder's children from `tree_entries`: folders,
  then files, names starting with a digit first in version order (`internal/files/files.go`). A
  kind whose every name starts with a digit comes newest first; one seek decides it. A checksum or
  signature next to its base file has `side_of` (one seek per such file) and the Files views hide
  it. A page is one range of the primary key per kind, whatever the folder's size.
- **Group tree.** The union of the trees of the members the caller reads
  (`access.Engine.GroupMembers`). Each member reads `limit` + 1 items from the same cursor and the
  pages merge; a file is the first member's, the one a group read serves. The first `limit` of the
  merge are the first `limit` of the union, because a member's page holds all its items before its
  `limit`-th. A group has no publish sessions (409) and no uploads; deletes go to its members
  (**Group deletes**).
- **Group versions** (`internal/format/group.go`). A group's versions, builds and snippets come from
  the members the caller reads; `format.Spec.GroupVersions` says how:
  - `each` (Maven, Docker; a group read serves each path or tag from the first member that has
    it): a k-way merge of the members' pages, the cursor holding each member's. A version comes out
    only while every member with more still holds an unread one, so none sorts before it. Without
    `VersionsByName` (Docker orders tags by date) a member is asked by a `Builds` lookup. A
    version with a `folder` held by several members is the union of their builds. A proxy member
    gives only what it has cached.
  - `owner` (npm, PyPI, Cargo, NuGet; a group read serves a whole package from its owner): the
    owner's versions. A proxy member's `Versions` reads its owner-check document through the read
    path, so it fetches from the remote when a download would; uncached versions have
    `remote: true`.
- **Repository lists** (`internal/api/repolist.go`, `internal/repos/list.go`). A request reads only
  its answer: a holder of `repositories.all` reads one index range; anyone else gets the public repos
  (partial indexes on `anonymous_read`) merged with one rowid lookup per grant, each list read to
  `limit + 1`.
- **Group counters** (`internal/api/groupcounts.go`). A group's file count, size and last publish
  count each file it serves to the caller once: by path, or by unit (`format.Spec.GroupUnit`, the
  package) from the unit's owner. A merged file counts once with the first member's size, because
  merged bytes exist only for a read. A delete never lowers `last_published_at`.
  - `group_counts` holds one row per group and member set a reader asked for, read with one lookup;
    a set without a row answers `counts_pending: true` and asks the job.
  - The job `core.group-counts` never holds SQLite's writer for more than a few small statements:
    1. A write transaction inserts the row and its pending entry; from here triggers add every
       asset write to the row.
    2. A write transaction zeroes the counters and, before it commits, opens a read snapshot. No
       write commits between the two, so the row holds exactly what the snapshot does not see.
    3. Queries count the snapshot holding no writer; publishes go on.
    4. A write transaction adds the count and clears the pending entry.
    A member change on the way deletes the row; a row left pending is counted again. The job runs
    at start, after each repo change, on request and every 5 minutes.
  - Triggers on `assets` keep counted rows current with indexed lookups only. A change of
    `repo_group_members` deletes the group's rows.
- **Delete credential.** `POST /api/repos/{name}/files/delete` needs `repo.delete`. It takes a UI
  session (which exists only after the second factor) or a device or manual publish session of that
  repo whose user holds `repo.delete` now. A CI session gets 403; passwords and read tokens 401; a
  repo with an operation 409.
- **Group deletes** (`internal/api/groupfiles.go`). A group holds no files, so a delete there goes
  to the members, each deciding with its own `Delete` and `repo.delete`; the group needs only
  `repo.download`. The access engine still answers a `Delete` on a group with 405, so a client's
  format DELETE through a group (an npm unpublish) stays read-only. `files/holders` says which
  members hold each item; `files/delete` names the members, lists refusing ones in `kept`, and
  runs each member's delete as a direct one (its audit, its limit). The UI deletes from all holders
  or only from the serving one, after which the group serves the next member's copy.
- **Delete steps.** Every path and prefix is checked first, then `Remove` runs in batches of 500.
  Blobs stay until the blob GC.
- **Repo delete.** `POST /api/repos/delete` starts a `delete` operation per repo (202; see **Repo
  operations**); writes get 409 meanwhile. The name can be used again afterwards.
- **`format.Spec.OnRemoved` and `AfterRemove`.** `OnRemoved` runs in each batch's transaction
  before the rows go (see **Debian indexing pipeline**, **Versions and builds**). Maven's
  `AfterRemove` rewrites `maven-metadata.xml` without the emptied versions; an artifact without one
  stays without one.
- **`format.Spec.Upload`** (`internal/format/upload.go`). `Plan` stores nothing; `Store` commits
  the plan's items (source `ui`, or `generated` for files the format writes). One `file.upload`
  event per uploaded file.
- **Upload limits.** 500 files, 16 short fields, all within `max_upload_size`; fields come first.
- **Relative paths.** A part's `filename` may be a relative path, read from the raw part header,
  because Go's `Part.FileName()` keeps only the base name.
- **Upload plan.** `POST /api/repos/{name}/upload/plan` takes the file list and the heads the
  format reads. Past 90 MB the UI sends the largest files through the resumable upload routes in
  16 MiB chunks, so no request passes Cloudflare's 100 MB body limit. `POST .../upload` stores only
  when the plan has no problem. Each format's plan lives in its `upload.go`; what needs a reason:
  - **Maven folder.** Checksums and signatures are ignored because the server writes them; a
    `pom.xml` outside a Maven layout is a project file.
  - **Maven.** Fields fill only what the pom lacks; a lone artifact gets a generated pom.
  - **Debian.** A package whose control data lies past the 1 MiB head is checked on arrival.
  - **Arch.** nfpm writes `.PKGINFO` after the files, so such a package is checked on arrival.
  - **RPM.** A header past the 4 MiB head is checked on arrival.
  - **Raw.** Each path follows its overwrite policy.

## Network rules

`internal/netrules` resolves the client address and keeps the IP lists and each repo's allowed
networks. Request paths read in-memory snapshots compiled into sorted, merged ranges, so checking
an address is a binary search at any list size.

**Client IP and scheme.** The module resolves the client once per request
(`Extender.ResolveClient`); `httpx.ClientIP(r)` and `httpx.ClientScheme(r)` read it. The scheme
shapes the URLs built from the request when `base_url` is empty (registry realm, snippets,
`/install.sh`, device verification). Without the module, it is the TCP peer and connection.

- An untrusted peer is the client; its `X-Forwarded-For` and `CF-Connecting-IP` are ignored. From a
  trusted peer `X-Forwarded-For` is read right to left past trusted addresses.
- `CF-Connecting-IP` counts only when the walk ends in the list `cloudflare` and the trusted
  proxies include it. So Cloudflare in front of Caddy needs no Caddy setting.
- Only `TR_TRUSTED_PROXIES` names IP lists in text; the API joins a list by id
  (`trusted_proxy_lists`).
- The default trusts nothing because Docker's userland proxy and rootless Podman make every outside
  connection arrive from a private gateway address. With `private` trusted there, anyone who
  reaches the port directly could forge `X-Forwarded-For` and read the repos of any allowed network.
- **Scheme.** An untrusted peer's forwarded `https` also counts: the scheme only shapes URLs sent
  back to the same client, and it keeps a not-yet-trusted TLS proxy from making the registry send
  credentials to an `http` token realm.
- **Proxy candidates.** Untrusted peers that send forwarding headers are remembered in memory only,
  for the UI to offer trusting them.

**IP lists.** Named sets of CIDRs (`ip_lists`), referred to by id.

- **Presets.** `cloudflare` and `github-actions`, read-only and refreshed daily; a failed refresh
  keeps the last good list. `TR_E2E_PRESET_BASE_URL` points the fetches at a test server.
- **Custom lists.** Ranges are checked strictly by every saving route.
- **Paste parsing.** `POST /api/ip-ranges/parse` (`parse.go`) pulls ranges out of free text; the UI
  then saves through the strict routes. It needs `network.manage`, or `repo.settings` on a repo.
- **Rename.** 409 when `TR_TRUSTED_PROXIES` names the list, because the next start resolves the
  old name.
- **Delete.** 409 `{error, used_by}` while in use; `ON DELETE RESTRICT` refuses it too.
- **Use.** `repo_networks_list_id` finds a list's repos.

**Allowed networks.** A repo is **public** (`anonymous_read`) or **restricted**. A caller downloads
from a restricted repo with a credential that grants `repo.download`, or from an address in the
repo's allowed networks (`repo_networks`; see **Access engine**). An address never takes access
away and grants `repo.download` only: publishing always needs a publish session.

- **Save.** `PUT /api/repos/{name}/ip-rules`, audited with the count only.
- **Public and allowed networks do not mix.** Both directions are checked in the write transaction
  (`repos.Service.CheckChange` for a repo update). At start `network.restrict` makes restricted,
  through `repos.Service.Update`, any public repo with networks, so edge objects and counters
  follow.
- **What an allowed address reads.** Every format and repo read, and the repo in lists, search and
  the registry catalog. A group's networks reach its members through the group.
- **The address index** (`reach.go`). One in-memory index from address to repo ids; `Reach(addr)`
  is one binary search plus one per named IP list, whatever the number of repos. Lists, search and
  the catalog merge it like grants.
- **Add my address.** A browser reaches the server over one family, so `network.probe_hosts` names
  an IPv4-only and an IPv6-only host, and the Access tab asks `whoami` on both.
- **Edge.** A repo with networks is `guarded`: the Worker passes its anonymous reads to the server,
  which answers with `Cache-Control: private`.

## Signing keys

`internal/signing` keeps the keys and each repo's binding, loaded into memory at start for request
paths and indexers.

- **First start.** With no key, the server creates the Ed25519 key `default`; the offline `seed`
  creates repos without a key. Without `default`, a repo created without `signing_key` is 400.
- **Storage.** Keys are sealed with the master key (AAD `signing_keys:<id>`), without passphrase. A
  changed master key shows the key as `error`, and its repos publish unsigned indexes.
- **Use.** Formats with `format.Spec.SigningKey` (Debian, Arch, RPM) bind each hosted repo through
  the repo field `signing_key`. Proxies and groups sign nothing, so the field is 400 there.
- **Change of key.** Only that repo is signed again: Arch from saved hash states without reading
  packages, RPM by rewriting every package.
- **RPM.** The signature is inside each package (see **RPM (dnf)**), so the UI warns with the repo's
  size; EL8 clients need an RSA key.
- **Arch.** Packages are signed from the SHA-256 state saved at upload (`Signer.SignSHA256State`,
  OpenPGP v4). A v6 key cannot (its signature hashes a salt first), and pacman's GnuPG reads no v6
  signature, so a repo bound to one leaves its packages unsigned.
- **Delete.** 409 while a repo uses the key, checked in the write transaction; `ON DELETE RESTRICT`
  refuses it too.

## Audit log

`audit_events` records who did what; `GET /api/audit` needs `activity.read`. Each module registers
its actions (`Extender.AuditActions`; core in `internal/audit/audit.go`), and `audit.Log.Record`
refuses an unregistered one. The middleware sets the actor (`audit.WithActor`); services record
inside the change's transaction (`RecordTx`); jobs record as `audit.System`, CLI user commands as
`cli`. Events never hold secrets: `settings.update` records keys only, `file.link.create` never the
signature.

- Client IP addresses appear only here. `proxy-candidates` lists untrusted proxies, not clients;
  the access log still records the IP.
- **Filters.** `actor` matches `actor_user_id`, so it finds people only. `since` / `until` become id
  bounds (`audit_events_at`), keeping keyset order on `id`; each combination of actor, repo and
  action has its own index.

## Download statistics

`internal/stats` counts each repo's downloads per UTC day (`download_stats`).

- **What counts.** A GET that serves a file's bytes: `Reader.ServeAsset` 200/206, an S3 redirect, a
  completed live proxy stream, a merged group file. Not HEAD, 304, errors, or R2 hits at the edge.
  A file counts on the repo that stores it.
- **Cost.** The read path adds to an in-memory map; `audit.downloads` flushes every minute and at
  shutdown. A crash loses at most a minute.
- **Redirects.** The same flush writes `repo_redirects.last_used_at`, which never moves back.
- **Retention.** `stats.retention`; a repo delete cascades.
- **The window.** The last 30 days are kept as per-day and per-repo totals, maintained by triggers
  on `download_stats` in the same transaction, so they survive crashes; the flush rolls the
  window, reading only the rows of the days that leave it.
- **`GET /api/stats`** flushes first, then reads 30 day rows and the top ten repos from an index.
- **`GET /api/repos`** shows each repo's window downloads with one seek per repo, after the next
  flush.

## Web UI

Svelte 5 + Vite (no SvelteKit), Tailwind CSS v4, shadcn-svelte, TypeScript and bun.
`bun run build` runs `svelte-check` and `vite build` into `web/dist`, which the binary embeds.
`web/src/README.md` is binding for the UI code: the page registry and routes, folder layout,
components, design rules, words, the per-page detail and the steps to add a page are there, and
the Svelte code under `web/src/pages` and `web/src/modules` owns each page. This section keeps how
the UI meets the server and the rules that need a reason.

- **Delivery.** `web/embed.go` sends the `.br` or `.gz` copy that the build's `precompress-dist`
  plugin (`web/vite.config.ts`) wrote when `Accept-Encoding` accepts it, with the original file's
  headers (the SPA fallback to `index.html` too); a `Range` request gets the plain file, and every
  answer of a file with a precompressed copy carries `Vary: Accept-Encoding`.
  Routing is hash-based (`/#/...`), so the server never sees UI paths and the URLs that the server
  and the clients print keep working. `web/index.html` holds one inline script that sets the theme
  before the first paint; a future Content-Security-Policy needs a hash for it.
- **Only a client.** The UI uses the JSON API and nothing else, through one client
  (`web/src/lib/api/`). It has no tests of its own: the e2e tests cover every route it uses.
- **UI modules.** Each server module with UI has a `ModuleUI` in `web/src/modules/<id>/module.ts`
  (same ID); `web/src/modules/index.ts` lists them in the server's start order. A module adds
  pages, repository settings tabs (named by a format's `repo_tabs`), repository field controls and
  format parts of the file details (named by its `file_details`). The router and the sidebars read
  that registry, so shared UI code never names a format. Each format's icon is its own SVG
  (`format.Spec.Icon`, from `GET /api/formats`).
- **Server identity.** The UI loads `GET /api/identity` once at start, with the formats; the name
  and logo show wherever the UI names the server. Every page ends with "Powered by Telesphorepo"
  whatever the identity; no setting turns it off.
- **No browser password prompt.** The API client sends `X-Telesphorepo-UI: 1` on every request
  (fetch and upload). The server never sends `WWW-Authenticate` for such a request or for a path
  under `/api`. Plain download links of format routes carry no such header and keep the challenge.
- **Auth in the UI.** A 401 outside the sign-in routes signs the user out. A 403
  `second_factor_required` sets the setup-only state; the UI shows its message, never the code. A
  setup-only session goes from every page but the sign-in to Sign-in and security, which offers
  the way on once a method is added. `/#/login` is the only place to sign in; signing out returns
  to Home.
- **Address notice.** The app's one notice: a person who holds `settings.general` (not a
  setup-only session) opened the app at an origin that is not `base_url`. **Use <origin>** saves
  `base_url`; **Keep** hides it for that origin in this browser (`localStorage`). It never shows
  under the Vite dev server, at a loopback origin, or when `TR_BASE_URL` sets `base_url`. It reads
  `GET /api/settings` at sign-in and after the person leaves Settings › General.
- **Cost follows the page.** Every list asks the server for a page by keyset (a name prefix, a
  filter, the next page as its end comes into view) and never filters, sorts or counts a whole set
  in the browser. Counts come from the server's counters or are bounded ("10,000+"); a count of
  search hits is of the loaded pages ("350+ files"). Search groups hits in the browser only within
  the loaded pages (a card per repo, a row per artifact folder, checksum and signature files folded
  into their file). The grant dialog, the `RepoPicker` and the People list search by name prefix, a
  page at a time, so they cost the same on any server.
- **Live data.** A page that follows a server process (a backup, a storage move, a Nexus sync, a
  filling region, a repository delete in a toast) refreshes through one `Live`
  (`web/src/lib/live.svelte.ts`): paused while the tab is hidden or offline, never two requests at
  once, longer waits after each failure. A slower process shows its cadence from the server that
  owns it.

**Pages and the API they use.** Each page's layout and copy are in `web/src/README.md`.

- **Home** (`/#/`, anyone): the doors from the grants of `GET /api/me`, the repositories
  published last (`GET /api/repos?sort=published&limit=6`), and **Your repositories** (`perm=`
  every repository permission beyond `repo.download`).
- **Browse** (`/#/browse`, anyone; a visitor sees the anonymous-read repos): `GET /api/repos` by
  name prefix and format, 100 a page. A group shows the files it serves to the reader (see
  **Group counters**), "Counting…" until counted.
- **Publish** (`/#/publish`, signed in): the hosted repos of
  `GET /api/repos?perm=repo.publish&kind=hosted` (a group is left out: its publishes go to a hosted
  member). With exactly one, it replaces the history entry with that repo's Publish view.
- **Search** (`/#/search?q=&repo=`): a `RepoPicker` leaves groups out.
- **Repository page** (`/#/repo/{name}/...`). One page loads the repo once and switches between
  Files, Use this repository, Publish to this repository, Upload and Settings, each shown to who
  may use it. A proxy has no Upload or Publish view. A group's views are read-only, naming the
  member that serves each file and version (see **Group versions**); its Publish view shows to a
  holder of `repo.publish` on a hosted member, over that member's view (`?to=<member>`).
  - *Files* shows a folder's children in the order of `/tree`, without the files `/tree` marks
    with `side_of`, a page at a time; versions and builds come from `/versions` and `/builds`, and
    a `?build=` that the loaded pages lack is read with `build=<id>` alongside the first page.
    Copy link copies a public file's URL under `base_url`, and for a private file a signed link
    from `POST /api/repos/{name}/links` (see **Signed download links**). Delete (with `repo.delete`
    and the UI session) confirms the server's delete plan. A delete of a version removes its
    `folder` when it has one (Maven, NuGet), with no request for its builds; else (Debian) it
    removes the files of all its builds, read 200 builds a page.
  - *Publish to this repository*: **Set up a project** asks for a fresh second factor (a single
    sign-on account signs in again through `GET /api/auth/oidc/setup?repo=` and returns with
    `?setup_grant=`, which the page uses at once and removes from the address), then
    `POST /api/publish/setup-codes` gives a setup line for the browser's OS, valid 10 minutes,
    whose link is `<base>/#/setup/<code>`. The Jenkinsfile of From CI binds the credential
    `telesphorepo-id-token` to `OIDC_TOKEN` only in the publish stage, because Jenkins makes the
    token when `withCredentials` starts and the server takes a token for 10 minutes only.
  - *Upload* (`upload?folder=`): the fresh second-factor check, then the server's upload plan,
    then the upload. The publish session lives in memory only. Limits and the folder filter are as
    in **Files, uploads and deletes**.
  - `/#/setup/<code>` makes no API call, and the code stays in the fragment, so it never reaches
    the server.
  - `/#/install` (**Install the CLI**, anyone) lists the archives of `GET /cli/SHA256SUMS`; a build
    without CLI archives (404 `no-cli-downloads`) shows an empty state.
- **Settings** (`/#/settings/...`, the admin pages) and **Your account** (`/#/account/...`, every
  signed-in person) share one shell. The navigation shows each page only to a holder of its
  permission (see **Auth model**); a path the person cannot open shows "You cannot open this
  page". `/#/settings/account`, `/#/settings/security`, `/#/settings/devices` and
  `/#/settings/tokens` open the account pages, because the server prints `/#/settings/tokens`.
  The General page is one form for the settings groups without a page of their own (see
  **Settings model**); a setting that an environment variable sets cannot change there.
- **Repository settings.** A person sees only the tabs they can open: Overview, Format settings,
  Signing and Remote need `repo.settings`, Access a permission that manages the repository. A
  rename says the old name keeps working (see **Repository redirects**). Creating a repository
  with a new signing key calls `POST /api/signing-keys`, then `POST /api/repos`; if the second
  call fails, the page deletes the key.
  - *Access* reads `GET /api/repos/{name}/access?admins=1` and applies each change at once. Each
    network change is one `PUT /api/repos/{name}/ip-rules`, one at a time, each on the networks
    the last one left. Turning Public on while the list holds networks asks first and removes
    them; the grants stay. While nobody can publish (`publish.blocked`: a group, a proxy or an
    operation), the Publish card says why and offers no CI pipeline. **Add my address** adds the
    `cidr` of `GET /api/network/whoami` (an IPv6 address as its `/64`) and, with
    `network.probe_hosts` set, the `cidr` of each probe host's whoami (a cross-origin `GET` without
    credentials), so both address families of the network go in.
  - **Grant dialog.** Give access (Access tab, person page) is one dialog: it searches people or
    repositories by name prefix, a page at a time, groups the added ones by identical permission
    sets and saves them in one `PUT /api/grants` with `groups`. On the Access tab it also adds IP
    lists, addresses (each masked through `POST /api/ip-ranges/parse`) and CI pipelines.
  - **Permission picker.** One picker, read from `GET /api/permissions`. Checking a permission
    checks what it needs and clearing one clears what needs it; a permission the caller does not
    hold is turned off. There are no presets: the phrases of a set ("Download, publish",
    "Administrator") come from the registry's labels.
- **People** (`people.manage`). Reset password shows only for a person whose permissions the
  caller all holds, and a permission the caller does not hold cannot change. When single sign-on
  manages an account's permissions, they are read-only.
- **Bulk actions.** Repositories, the List view of the Files tab, People, a person's repositories
  and a repository's people take a pick. Every delete asks first with the count and the first five
  names; a delete of more than one repository asks for the count.
- **CI pipeline page.** The page builds the trust policy's claims from answers a developer knows
  (GitHub: repository, branches and tags, an optional workflow file, issuer
  `https://token.actions.githubusercontent.com`; Jenkins: the address, issuer `<address>/oidc`, and
  job names as `sub`); **Edit the token conditions** shows the raw claims. The workflow or
  Jenkinsfile comes from `GET /api/repos/{name}/ci`. `?back=` accepts only a `/settings/` or
  `/repo/` path. When `base_url` is empty and the pipeline has no audience of its own, the page
  warns that CI jobs cannot publish.
- **Pasted addresses.** Address fields parse through `POST /api/ip-ranges/parse` (`RangePaste`);
  nothing is saved until the page's own save. The Network and proxy page saves the trusted IP lists
  through `PUT /api/network/trusted-proxies`; a list that `TR_TRUSTED_PROXIES` names cannot change.
- **Edge delivery page** (`/#/settings/edge`, `edge.manage`). It runs `POST /api/edge/check` on
  open and every 30 seconds while the Worker has not reported the server's version (a 429 from
  another tab is silent). Saving regions runs the group's **Test** first and saves nothing when a
  probe fails; clearing a region asks first, since the server stops writing to its bucket. Deploy
  is **From this server** (`POST /api/edge/deploy`, with a saved Cloudflare token) or **From a
  computer** (`GET /api/edge/worker.zip`, deployed with wrangler). The speed test
  (`web/src/pages/server/edge/SpeedTest.svelte`) offers no run at an address other than
  `base_url`.
- **Sign-in options, Storage, Nexus.** A page that connects to another system has **Test** where
  the connection is set. Sign-in options tests the saved settings on open and shows the result only
  when it fails. A Nexus server's Delete refuses while a sync runs.

**Versions and builds.** A format that sets `format.Spec.Versions` and `Builds` groups an
artifact's files into versions and a version's files into builds; the UI reads them through
`/versions` and `/builds`. A format keeps its own index tables in the transaction of every write
and remove through its hooks, so a page of versions or builds is one seek past its cursor plus a
range read of the files of each build on it, at any number of versions, builds or files. Each
format's order is a stored byte key (see the format's `order.go` or index code).

- *Maven* (`internal/maven/index.go`, `versions.go`): an artifact folder is
  `<group path>/<artifactId>/`. A file of a version (not a checksum or signature) records its
  build and version in `maven_versions` and `maven_builds`; removing the last file of a build
  deletes the build, and of the last build its version. A `-SNAPSHOT` version has timestamped
  builds and the build `SNAPSHOT` of its untimestamped files. Versions sort in Maven order
  (`versionKey`). The snippets of a folder without a version use its newest release, else its
  newest snapshot: one seek of `maven_versions_latest` each. On the first start with these tables,
  the module indexes the files the Maven repos already hold, then serves.
- *Debian* (`internal/debian/order.go`): `deb_packages` through `deb_package_order`. An artifact
  folder is `pool/<component>/<prefix>/<package>/`. A version `<base>~SNAPSHOT<sep><stamp>`
  (`<sep>` one of `.`, `+`, `~`, `-`; `<stamp>` 8 to 14 digits) is a build of the snapshot version
  `<base>~SNAPSHOT`; any other version is a release with one build. Versions sort by
  `dpkg --compare-versions` rules, so `3.0~SNAPSHOT` sorts before `3.0`. A snapshot version
  installs unpinned, so apt takes the newest build.
- *Arch* (`internal/arch/order.go`): `arch_packages` through `arch_packages_order`; an artifact
  folder is `pool/<name>/`, a version the full pacman version with one build. Versions sort in
  `vercmp` order; the byte key disagrees with `vercmp` only where `vercmp` is not transitive (`1a.`
  against `1a.b`).
- *RPM* (`internal/rpm/order.go`): `rpm_packages` through `rpm_packages_order`; an artifact folder
  is `Packages/<name>/`, a version `[epoch:]version-release` with one build, in rpm's order (see
  **RPM (dnf)**).

## CLI

The server binary's commands (`cmd/telesphorepo-server/main.go`; `help [cmd]` prints each one's
usage):

- `serve` (the default) runs the server from the boot env and the DB settings.
- `dev [--empty]` runs a zero-config local server with data in `./.telesphorepo-dev`. Its first
  run creates `admin` and the demo data before listening; `--empty` instead creates one empty
  anonymous-read repo per registered format, named after the format. Dev mode makes the second
  factor optional (see **Auth model**).
- `user add NAME [--admin]`, `user passwd NAME`, `user reset-2fa NAME` manage an account from the
  host. A password comes from a TTY prompt (asked twice) or one line of a pipe. `passwd` signs out
  the user's sessions and is refused for SSO users; `reset-2fa` removes every factor, so the user
  sets up a new one at the next sign-in. Each writes an audit event with the actor `cli`.
- `backup DIR` writes one backup as the job does (see **Backups**). It opens the database
  read-only, so a server may run on the data dir, and writes no audit event and no retention.
- `restore FOLDER [--data-dir DIR]` restores into an empty or missing data dir and says how the
  backup's migrations fit this binary: the same, older (applied at the next start) or newer (this
  binary refuses to start, and it names the version to run).

**Demo data.** The first run of `telesphorepo-server dev` (without `--empty`) fills the instance
for work on browsing and search: about 150,000 Maven files in `maven`, a private `internal`, and
the demo repos that each format module declares (`module.DemoRepo` in its `demo.go`;
`cmd/telesphorepo-server/demo.go` drives them). Paths, contents, users and roles
are the same on every fresh run. The Debian, Arch and RPM packages go through the upload
pipeline, so their indexes and signatures are valid. The demo users are `priya` (administrator),
`jonas` (download and publish on `maven` and `internal`), `mei` (`maven` and `raw`), `tomas`
(`debian` and `raw`) and `lena` (download on `internal`); their passwords are in
`<data>/demo-users.txt`.
The files go through the byte pipeline with source `demo` in commits of 1,000 files, so the
triggers keep the counters, the blob references and the search index. While it runs,
`<data>/demo-data.pending` exists; a dev start that finds that file deletes the data dir and
starts again.

Two hidden commands support testing. `dbcheck` applies every component's migrations to a
temporary database and runs the plan guard. `seed N` fills the database in `TR_DATA_DIR` for the
scale test: for each module with a seed (`Extender.Seed`) it creates `seed-<format>-00` to
`seed-<format>-09` and splits N rows evenly over them in transactions of 10,000, then runs
`ANALYZE`. It is safe to run again.

## Tooling (`go tool task`, registered in `go.mod`)

The tasks live in `tools/task`; they are Go so they work on Windows, macOS and Linux.

| Command | Does |
|---|---|
| `go tool task dev` | The backend on :8080 (rebuilt on changes to Go or migrations) and Vite with HMR on **:5173**, which proxies every path but the UI's own files to the backend. Builds this computer's CLI archive and the Worker folder once first; a failed Worker build does not stop it |
| `go tool task build [image]` | `bin/telesphorepo-server` with the UI, the CLI archives and the Worker folder (skipped without bun) embedded. `image` also builds `telesphorepo:local` |
| `go tool task cli`, `edge` | The CLI archives (`internal/clidist`), and the Worker folder that `GET /api/edge/worker.zip` serves (`internal/edgedist`; needs bun) |
| `go tool task test [zones] [--now]` | Runs the named e2e zones (`core` by default, or `all`) at the same time (see **The run of the e2e tests**); `--shard i/n` runs every n-th test of one zone |
| `go tool task test scale` | Runs `TestScale` and prints its verdict: `PASS` exits 0, `FAIL` as the failed test, `NOISY` 3, `INCONCLUSIVE` 4 |
| `go tool task release [version]` | The CLI and server archives for linux, darwin and windows on amd64 and arm64, the Worker archive and `SHA256SUMS` in `bin/release/`. A given version must be a release version and `changes.toml` must have no `dev` entry (see **Change log**) |
| `go tool task tag <version>` | Stamps `v<version>` into each `dev` entry of `changes.toml`, commits `Release v<version>` and creates the annotated tag. Pushes nothing. The tagged commit holds the stamped change log, so `release.yml` and every image build embed the same entries |
| `go tool task gen`, `migration`, `doctor` | `sqlc generate`; the next migration file of a folder (`db` or `core` for the kernel, else the module's folder, e.g. `move` for `storage`); the detected toolchain and engine |

CI runs on GitHub Actions with the built-in `GITHUB_TOKEN` and reads no repository secrets. Linux
jobs run on Blacksmith and use its caches; Blacksmith bills every run, so CI is the last check of a
finished branch. The `macos` job runs on GitHub's `macos-latest`. The workflows
(`.github/workflows`):

- `ci.yml` (push to `main`, pull requests): `build` builds and vets everything, and runs
  `telesphorepo-server dbcheck`. The e2e jobs run on pull requests only, and only when a
  non-Markdown file outside `web/src/` changed (the e2e tests drive the API and real clients, never
  the UI): core on four shards, clients, and
  `TestCLIGitHubActions` with the job's real identity token. A push to `main` publishes the
  multi-arch image `main`, which production runs.
- `e2e-extended.yml`: manual only; the zones run locally before a push (AGENTS.md). The scale zone
  never runs in CI: a shared runner is not a quiet machine.
- `release.yml` (push of a `v*` tag): the release archives and a GitHub Release, the image on
  `ghcr.io/telesphorepo/telesphorepo` (`X.Y.Z`, `X.Y`, and `latest` for non-pre-releases), and the
  macOS zone.

The Dockerfile builds on the build platform and cross-compiles with `TARGETOS` and `TARGETARCH`. The
image is distroless static, runs as uid 65532, and has `/data` as a volume.

## Testing

The suite is e2e only: no unit tests, no mocks. It runs real clients (`mvn`, `gradle`, `apt` and
the rest) in containers against the real server image, which `go tool task test` builds once per
run (`E2E_IMAGE` names another and skips the build). The engine comes from
`internal/containerengine`; `TR_ENGINE=docker|podman` forces one.

- **Parallel.** Every test calls `t.Parallel()` and starts its own server, so a zone's tests run
  at once (as many as the host has CPUs).
- **Stores.** A test whose subject is the store runs on `fs` and on SeaweedFS S3
  (`ForEachStorage`, at the same time): each format's byte path, presigned redirects, deletes, the
  blob GC, the CLI's uploads and the Nexus import. Every other test runs once, on `fs`
  (`OnLocalDisk`). `TestNexus` runs its stores one after the other (`ForEachStorageInTurn`) because
  both change the one Nexus.
- **Containers.** Start each container that publishes a port with `runContainer`: rootless Podman
  can give two containers started at the same moment one host port, and `runContainer` starts the
  second again.
- **Images.** The images of the tools people bring float (`floatingImage`, listed in
  `e2e/images_test.go`): each run pulls its zone's images again and prints each version.
  A Docker Hub image whose local copy has the tag's current digest is not pulled (a manifest HEAD
  does not count against the anonymous pull limit). The newest PowerShell comes from GitHub,
  because Microsoft's images do not follow its releases. The test infrastructure stays pinned
  (SeaweedFS, Caddy, the OCI conformance suite).

The shared fixtures are in the files the **E2E harness** row of the code map names. `BuildCLI` and
`BuildServer` change code through `go build -overlay`, so the tree on disk stays as it is.
`TestEdgeDeployRefusedToken` reaches the real Cloudflare API. There is no Cloudflare test account:
the owner tests the deploy in production, and no e2e test covers the Worker's serving (local R2
differs from R2 in what the edge relies on).

**Zones.** A build tag picks the zone, so a run starts only the containers it needs. The harness
and client helpers carry the tags of the container zones (`e2e`, `import`, `clients`); the scale
and macOS zones run the server binary as a process of the computer (`native_test.go`). Every
container and the run network carry the label `telesphorepo.e2e`, so `go tool task test` first
removes what a killed run left (with the `telesphorepo-data-*` volumes). Every container logs to a
file (`json-file`, Podman's `k8s-file`): journald, Podman's default, drops a unit's lines past
10,000 in 30 s, and the tests read the server's access log.

| Zone | Tag | Command | Contents |
|---|---|---|---|
| core | `e2e` | `go tool task test` | Formats (the Docker registry with Docker in Docker too), auth, API, settings, storage move, query plans, the listener protocols |
| import | `import` | `go tool task test import` | `TestNexus` with a Nexus container, and the Nexus imports of the formats (`e2e/nexus_*_test.go`) |
| scale | `scale` | `go tool task test scale` | `TestScale`, with no container engine, alone on a quiet machine |
| clients | `clients` | `go tool task test clients` | The CLI of this repo, built on the host, in containers with the real tools (`e2e/cli_*_test.go`). Needs Go on the host and the internet |
| all | `e2e`, `import`, `clients` | `go tool task test all` | Every zone but scale, at the same time |

**The run of the e2e tests** (`internal/e2erun`, `tools/task/testrun.go`). Only `go tool task test`
starts the e2e zones (core, import, clients); the scale and macOS zones are outside it.

- **Budget.** A zone's tests, from the first to the last, finish within a budget: 4 minutes
  (`e2erun.ReferenceBudget`) on CI's e2e runner. The setup before them (image build, pulls, Maven
  warm-up) does not count. The zones run at the same time, as CI runs them, so the whole suite
  fits the budget. `TestMain` sets it as go test's own timeout, whose clock starts in `m.Run`; past
  it, the binary panics and names the tests still running. It prints the time the tests took.
- **Calibration.** Before the zones start, the run times a fixed piece of the tests' work on its
  engine: two rounds of six servers of the run's image started at once, each until `/healthz` and
  a sign-in answer, then removed; then four runs at once of the amd64 Rocky Linux 9 image, which an
  engine of another architecture emulates, as it does the RHEL-family clients. The budget is
  `ReferenceBudget × calibration / e2erun.ReferenceCalibration` (the calibration on CI's runner),
  kept within 0.5× and 3×. The run prints both.
- **Gate.** One run at a time on the computer: `running.json` in the user cache folder
  (`telesphorepo/e2e`), created exclusively, holds the run's process, its random ticket and its
  budget; the file of a process that is gone is taken over. Each checkout (by path) keeps the time
  of its last run and a hash of its tree (the commit, `git diff HEAD` and the untracked files). A
  run is refused when it starts less than 5 minutes (`e2erun.Cooldown`) after the checkout's last
  run, and the refusal starts the 5 minutes again; it tells the agent to batch its work and go on
  without waiting, and does not say how long is left. A run whose tree has not changed since the
  last run is refused too. A run's start counts as its last run until its end replaces it, so a
  killed run keeps its cooldown. `--now` skips both checks after the person at the terminal types
  `now`; without a terminal it refuses. The ticket goes to the test binaries in `E2E_RUN_TICKET`,
  and `TestMain` refuses to run without the ticket of the run in progress, so a plain `go test` of
  the zones does not start, while `go test -list` still does.

**Admin and user calls.** The API changes state only through a UI session, so the suite signs in
the way the web UI does, with the password and a TOTP code (`s.Admin()`, `s.SignIn`);
`s.AdminBasic()` is the admin password for format reads. The helpers are in `harness_test.go`.

**Permission tests.** `access_test.go`, `permissions_test.go` (one subtest per server permission:
the holder reaches its routes, anyone else gets 403) and `registries_test.go`.

**Publishing tests.** Every publish in the suite uses a publish session. `PublishToken` (at the end
of `harness_test.go`) enrolls a device the real way (the test computes the TOTP codes), approves it
and mints a session. The harness enrolls 3 TOTP factors per user, so tests never wait 30 s for a
new step. The CI token exchange runs with the real CI systems in the clients zone
(`TestCLIJenkins`, `TestCLIGitHubActions`); `cli_server_test.go` covers the server side of the CLI
in the core zone.

**Clients zone.** After each run the harness (`cli_harness_test.go`) scans every file of the container
and its volumes (but the build caches) for `trs_` or `trsu_` secrets of 40 or more characters or
the test's CI token; a match fails the test, so the CLI never leaves a credential on disk.
`TestCLIJenkins` runs a real Jenkins (`testdata/jenkins`) on the Jenkinsfiles that
`GET /api/repos/{name}/ci` writes. `TestCLIGitHubActions` runs only in CI's `e2e-github-actions`
job (`E2E_GITHUB_ACTIONS` set), where a real identity token exists.

**macOS zone** (tag `macos`, the `macos` job of `release.yml` only). `TestMacOS` needs no container
engine: it starts the server binary of `E2E_SERVER_BIN`, installs the CLI with `install.sh` into a
new `HOME`, and runs setup and publish for a Maven project and a raw project. Agents never run it.

`TestNexus` starts the server with `TR_NEXUS_SYNC_INTERVAL=12h`, so no scheduled sync changes the
counters. Its fixture covers hosted, proxy, group, apt, raw and npm repos, and its subtests cover
the switch-over, the gradual move and the merges.

`TestScale` (`e2e/scale_test.go`) needs no container engine and refuses to start while containers
labelled `telesphorepo.e2e` or `telesphorepo-server` processes run. It makes two data folders with
`seed` (100k and 1M assets). `seed` numbers each repo's rows from 0 and makes each path from its
number, so the 100k paths of a repo are paths of the same repo at 1M and both sizes answer the same
request. A copy of the 100k folder is the A/A control: a server of its own on a file of its own
(its own SQLite page caches and OS file cache pages, as the other two have). The three servers run
at once.

- **CPUs.** On Windows and Linux the servers run pinned to the CPUs of the L3 cache domain with the
  largest cache, and the client on every other CPU (`e2e/scale_cpu_*_test.go`; `E2E_SCALE_CPUS`
  overrides). macOS has no CPU affinity API: the test runs unpinned.
- **Clock.** On Windows requests are timed with `QueryPerformanceCounter`, because Go's monotonic
  clock there moves once per timer tick (0.5 ms to 15.6 ms).
- **Operations.** The hot request paths: a Maven conditional GET (also through a repo domain, so
  the host check runs), an asset list walk, rare, common and shared searches, token auth, the repo
  list, the versions of an RPM package and a tree walk. A new request path that clients hit often
  gets an operation here.
- **Warm-up and measurement.** Each operation warms up until block medians settle. Then rounds
  send every operation in a random order, and each operation to the three servers in a random
  order; per block the ratio of medians 1M/100k and A/A/100k, per operation the median of the
  block ratios with a 95% bootstrap interval.
- **Verdicts.** The limit is `max(2, (median at 100k + 2 ms) / median at 100k)`. FAIL: the 1M/100k
  interval lies above the limit; PASS: below it; INCONCLUSIVE: it holds the limit (a FAIL whose
  warm-up did not settle is INCONCLUSIVE). When an A/A interval leaves 0.8 to 1.25 the run is NOISY
  and judges nothing. The run is NOISY, else FAIL when an operation fails, else INCONCLUSIVE when
  one is, else PASS. Only FAIL fails the test.

## Code map and recipes

Where each part of the code lives, and the steps to add a module, a repo format or a UI page.

### Where things live

| Need | Place |
|---|---|
| Wiring and startup order | `internal/server/server.go` (kernel, module lifecycle); the module list in `internal/modules/modules.go` |
| Module contract | `internal/module` (`Module`, `Extender`, `Host`, `Use`); jobs in `internal/jobs`; routes and policies in `internal/route` |
| Core setting | `internal/settings/general.go` |
| Host check, repo domains, 421 | `internal/server/hosts.go`; `route.WithRepoDomain` and the cookie rule in `internal/route`; a module's extra host is `x.Host(...)` |
| Module setting, permission, audit action | `x.Settings`, `x.Permissions`, `x.AuditActions` in the module's `Register` (`internal/settings`, `internal/perm`, `internal/audit`); the audit actor comes from the context |
| Schema change | The next file of `internal/db/migrations` (kernel) or `internal/<module>/migrations`; the runner is `internal/db/migrate.go` |
| Query | `queries/*.sql` of the owner (`internal/db` or the module), then `go tool task gen` |
| New module | `internal/<module>` + one line in `internal/modules/modules.go` (see below) |
| Format contract | `internal/format/format.go` (`format.Spec`), `kit.go` (the standard routes) |
| JSON API route | `x.Routes` of the owning module (core: `internal/api/api.go`) + handler; helpers in `route.API`; errors via `httpx.Error` or `route.Fail` (RFC 9457 problem documents under `/api/` and `/cli/`; named types in `internal/httpx/problem.go`) |
| Signed download links | `internal/links`, `internal/auth/link.go` |
| Credentials, permission checks | `internal/auth` (`Identify`, sessions, tokens, second factor), `internal/route/policy.go`, `internal/access/engine.go` (`Decide`) |
| Publish sessions, devices, trust policies, CI providers | `internal/publish` (install scripts and CLI downloads in `install.go`); `internal/ci`; sign-in and second factor routes in `internal/api/security.go` |
| Byte pipeline | `internal/assets` (`Commit`, `Remove`, `Reader`, `Uploads`), `internal/blobs` (content-addressed blobs, GC), `internal/repopath`, `internal/ops` (repo operations), `internal/rebuild` (index rebuild queue), on top of `internal/storage` |
| UI page | `web/src/pages/**/*.svelte`; its `PageDef` in the owning module's `web/src/modules/<id>/module.ts`; API calls in `web/src/lib/api/`. See `web/src/README.md` |
| UI of a format | The descriptor (`format.Spec`, `GET /api/formats`; `web/src/lib/formats/registry.svelte.ts`); a format-specific component only as a named `formatParts` slot in `web/src/modules/<format>/module.ts` |
| File tree, deletes, manual upload | `internal/files`, `internal/api/files.go`, `pipeline.go` (resumable uploads), `format.Spec.Upload`; UI in `web/src/pages/repo/` |
| Repository lists, Settings overview figures | `internal/api/repolist.go`, `internal/repos/list.go` (one index per list), the maintained counters `repo_totals` |
| Client IP, IP lists, allowed networks | `internal/netrules` (module `network`) |
| Docker / OCI registry | `internal/docker`; UI in `web/src/modules/docker/` |
| Telesphorepo CLI and its Docker credential helper | `cmd/telesphorepo` (MIT, its own `LICENSE`; imports no server package; a new format adds a row to `detect.go`) |
| Signing keys | `internal/signing` |
| Snippets, CI files | `Usage` and `Tools` of the format; `internal/ci/workflow.go` |
| Audit log, download statistics | `internal/audit`, `internal/stats` (the counters), `internal/activity` (module `audit`: the routes and jobs) |
| A format | `internal/<format>`, UI in `web/src/modules/<format>/`, e2e in `e2e/<format>*_test.go`. Arch's URL rule is also in the Worker (`pacmanPath` in `edge/src/shapes.ts`) |
| Edge delivery | `internal/edge`; `format.Spec.Edge` of each format; the Worker in `edge/` (`src/glob.ts` ports `format.Glob`); the embedded Worker folder `internal/edgedist` |
| Backups and restore | `internal/backup`, `cmd/telesphorepo-server/backup.go` |
| Storage move (FS ↔ S3) | `internal/move` (module `storage`), `internal/storage/mirror.go` |
| Single sign-on | `internal/oidc` (module `sso`) |
| Nexus sources, syncs, cut-over | `internal/nexus`. Per-format import code is the format's `Importer`. Repo lock: the repo operation `mirror`, checked in `access.Engine.Decide` |
| Demo data and seed | `internal/demo`, and `Demo` and `Seed` of each module; `cmd/telesphorepo-server/demo.go` |
| E2E harness | `e2e/harness_test.go` (servers, `ForEachStorage`, `OnLocalDisk`, publish helpers at the end), `e2e/clients_test.go` (shared client helpers, Docker in Docker), `e2e/oci_test.go`, `e2e/images_test.go` (floating images), `e2e/keycloak_test.go`, `e2e/build_test.go` (`BuildCLI`, `BuildServer`), `e2e/cli_harness_test.go` (clients zone, the secret scan), `e2e/native_test.go` |
| Change log and server version | `changes.toml` (embedded by `changes.go`), the parser in `changelog/` (shared with the CLI), `internal/api/changes.go`, the `Telesphorepo-Version` header in `internal/server/server.go` |
| Dev tooling | `tools/task` |

### Adding a module

Every format and every core feature with routes is a module (`internal/module`):
`module.Module{ID, Title, Requires, Register}`. `Register` runs once, before the database opens,
and calls the `Extender`'s `x.*` methods to declare what the module adds (schema, settings,
permissions, audit actions, routes, jobs, formats and the rest; see `internal/module`). A module
edits no core file.

1. Create `internal/<module>` with `func Module() module.Module`. The module ID is also the schema
   component. List the modules you use in `Requires`; each must be earlier in the list.
2. Add one line to `internal/modules/modules.go`, in start order.
3. In `x.Init`, keep the `*module.Host`: the kernel services and a log tagged with the module ID.
   Offer a service with `h.Provide` or `module.ProvideAs[T]`; get one from a required module with
   `module.Use[T](h)`. Run background work as `x.Job` or `h.Go`.
4. Register each route with a policy in `x.Routes`. A module registers only under `/api/`, under
   `/<format>/` of its formats and under its `RootPrefix`es.
5. For tables, add `migrations/0001_init.sql`, `queries/*.sql` and a `sqlc.yaml` (copy
   `internal/debian/sqlc.yaml`), declare them with `x.Schema`, and run `go tool task gen`.
6. Add `e2e/<module>_test.go` with a real client: the byte path on both stores (`ForEachStorage`),
   the rest on the local disk (`OnLocalDisk`).
7. Add a section to `SPEC.md`: what the module does, its rules and their reasons, and its contracts with clients; point to its code for routes, tables and settings.

#### Adding a repo format

A format is a module that passes a `format.Spec` to `x.Format`. A nil field is a capability the
format lacks; `internal/format/format.go` documents each field.

- **Required:** `Name` (the URL segment and the `repos.format` value), `Config.Validate` and
  `Kinds` (holding `hosted`).
- **Routes:** a nil `Routes` mounts `format.Standard` (GET and HEAD through the read path, PUT
  through `Commit`). A protocol of its own registers each route with `route.FormatRead` or
  `route.FormatWrite`, and a write route passes `format.PreconditionOf(r)` as its write's
  `Precondition`.
- **Bytes:** hooks around the pipeline; `OnCommitted` and `OnRemoved` write the format's own rows
  in the same transaction, `Rebuild` builds an index in a background job.
- **Proxy and group:** `proxy` needs `Classify` and `ProxyProbe`; `group` can set `Merge` and, with
  `Versions`, needs `GroupVersions`.
- **Web UI:** the descriptor fields. The web UI reads the descriptor and names no format.
- **Import:** `Importer`: the Nexus import offers each source file to the format.

`raw` (`internal/raw`) is the smallest example. `arch` maps pacman's URLs to stored paths in a read
route, with the same rule in the edge Worker. `debian` shows a schema, a repo field, `Rebuild` and
an importer plan. `maven` shows proxy, group, `Merge`, `Upload` and `Tools`.

### Adding a UI page

1. Create `web/src/pages/<area>/<Name>.svelte` with `let { params, query }: PageProps = $props();`.
2. Add one `PageDef` to the owning module's `web/src/modules/<id>/module.ts` (path, lazy page,
   layout, access, title; `nav` for a Settings or account page).
3. Add the route to the table in `web/src/README.md`.
4. Call the API through `api.*`. For a new server route, add a function and its type in
   `web/src/lib/api/`. A format module keeps the calls to its own routes in its own folder.
5. Use `Resource` or `Keyset` (`lib/load.svelte.ts`) for requests, and show loading, error and
   empty states. Use `ConfirmDialog` for every destructive action.
