# Telesphorepo: system as built

This file describes what the code does today. Update it in the same change whenever behaviour
changes. `AGENTS.md` holds the working rules for coding agents, and `README.md` is the short
guide for people who run the server.

## Purpose

A self-hosted repository for Maven repos (including Gradle plugins), Debian (apt) repos, NuGet
repos and raw file repos. Each format is a module (see **Modules**), and a new format is one more module: the
core names no format. A repo is **hosted** (it stores what is published to it), a
**proxy** (Maven and Debian: it fetches files from a remote repository and keeps them) or a
**group** (Maven: one address for the files of several repos). It ships as one Go binary with the
web UI embedded. Metadata is in SQLite and blobs are on the local filesystem or in any
S3-compatible bucket (Cloudflare R2, SeaweedFS, AWS). It is not compatible with the Nexus API.

## Principles

- **The cost of a request does not grow with the size of a repo.** Every query on a request path
  uses an index. Lists use keyset pagination. There is no `OFFSET`, no `LIKE '%x%'` and no
  listing of storage in a handler. Work that grows with the contents of a repo (a Debian index)
  runs in a background job on the smallest affected unit.
- **One process per data volume.** SQLite, the in-process locks and the debounced workers need
  exactly one server on a data volume.
- **Light.** One static binary (`CGO_ENABLED=0`), a distroless image, one SQLite file, one blob
  store. Few and mature dependencies: the standard library first, then `modernc.org/sqlite`,
  `aws-sdk-go-v2`, `ProtonMail/go-crypto`, `pault.ag/go/debian`, `go-oidc`, `x/oauth2`,
  `go-webauthn`, `pquerna/otp` and `x/crypto/argon2`.
- **E2E tests only.** The suite runs the real image with real clients. There are no unit tests and
  no mocks.
- **Repo paths and storage keys use package `path`**, with forward slashes on every OS.

## Architecture

| Package | Role |
|---|---|
| `cmd/telesphorepo-server` | The server binary `telesphorepo-server`: `serve` (default), `dev`, `user add/passwd/reset-2fa`, `backup`, `restore`, `version`, `help`; hidden `dbcheck` and `seed`. `cmd/telesphorepo` is the name of the publishing client |
| `changes.go` (package `telesphorepo` at the repo root) | Embeds `changes.toml`, the change log (`ChangesTOML`). It imports nothing |
| `changelog` | Reads the change log (`Parse`, `Embedded`, `Since`, `Stamp`) and compares release versions (`ParseVersion`, `Version.Compare`). It imports only the stdlib, `github.com/BurntSushi/toml` and the root package, so the server and the CLI both use it. See **Change log** |
| `internal/server` | Builds the kernel and runs the module lifecycle (see **Modules**): logger, DB, schemas, settings, live storage, kernel services, module `Init`, routes, bootstrap admin, jobs, client address, access log |
| `internal/module` | The module contract: `Module`, `Extender` (the manifest calls), `Manifest`, `Host` with `Provide` and `Use` |
| `internal/modules` | `modules.go`: the one list of modules in start order. The only file that names them |
| `internal/jobs` | The job runner: one context and wait group per module, `Start`, `Run` and `Tick` of every job, on-demand tasks (`Go`), stop in reverse module order |
| `internal/route` | The route table: each module's `Router` accepts only the paths the module owns, and every route has a `Policy` that one middleware enforces (see **Route policy**); `route.API`, the helpers that JSON handlers share (JSON body limit, base URL, error answers, pages) |
| `internal/perm` | The permission registry: the subsystems and permissions that modules register (`perm.Subsystem`, `perm.Def`), fixed at startup; `perm.Set`, a bit mask over the registry. See **Permissions** |
| `internal/access` | The access engine: `Engine.Decide` for every repo operation, the guards of the modules, and the access report built from the same parts |
| `internal/token` | The token format of every token kind: `Mint`, `Parse`, `Hash`, `Check` |
| `internal/config` | Boot env (`LoadBoot`): `TR_DATA_DIR`, `TR_LISTEN`, `TR_MASTER_KEY`, the bootstrap admin. Everything else is a setting |
| `internal/settings` | The settings store: the groups that modules own (`settings.Group`), one immutable snapshot read through typed `Key`s and `View`s, AES-256-GCM secrets, master key, env overrides, subscribers. Declares the keys of the `general` group. See **Settings model** |
| `internal/db` | SQLite (modernc, no cgo), WAL, separate writer (1 connection) and reader (8 connections, all kept open) pools, a 64 MiB page cache and a 256 MiB memory map per connection, one schema per component with a fingerprint (`Init`), `PRAGMA optimize`, the plan guard (`CheckPlans`) |
| `internal/ci` | The CI provider registry (`Registry`: `Build`, `Describe`) and the build records (`Builds`: `Record`, `Upsert`, `Loader`, `Cleanup`). See **CI providers and build records** |
| `internal/db/dbq` | sqlc-generated core queries (`internal/db/queries/*.sql`) plus hand-written FTS queries in `extra.go` |
| `internal/storage` | `Store` interface (`Put` reads an `io.ReaderAt`; `DeleteMany`: one S3 `DeleteObjects` per 1000 keys, one delete per key on fs, and a `DeleteError` that names the keys left); `fs` (with `Adopt`), `s3` (multipart from 64 MiB), `Mirror`, and `Live`, a hot-swappable wrapper with `Open`, `SameBackend` and `Probe`. Declares the storage setting keys and `ConfigView` (the store of a snapshot) |
| `internal/repopath` | The pure repo-path parser: `Parse` (a file path) and `Folder` (`""` or a folder ending in `/`). Routes, file operations, imports and uploads check every path with it |
| `internal/blobs` | Content-addressed blobs: `Spooled` bodies with their hashes, `Store` (`Key`, digest `Lock`, `Ensure`/`EnsureAll`, `Get`, `Open`) and the blob GC. See **Byte pipeline** |
| `internal/assets` | The byte pipeline: `Pipeline` (`Commit`, `CommitAll`, `Remove`, point lookups, listing, FTS search), `Reader` (the read path of hosted, proxy and group repos), `Uploads` (resumable uploads), the asset hooks of a format (`Hooks`) and the watchers of every commit and remove (`Watcher`). See **Byte pipeline** |
| `internal/ops` | Repo operations (`repo_ops`): `Begin`, `Progress`, `SetState`, `Fail`, `End`, and the operation types that modules register (`delete`, `mirror`) |
| `internal/rebuild` | The durable rebuild queue of format indexes (`rebuilds`): `MarkTx`, `Pending`, and the debounced worker of the job `core.rebuild` |
| `internal/backup` | Module `backup`: a backup of the database (`VACUUM INTO`), the master key and, on fs, the blobs into a backup folder (`Run`), the list (`List`), the retention (`Prune`), the restore (`Restore`), the source of a backup from the command line (`Offline`); the settings group `backup` and its job. See **Backups** |
| `internal/move` | Module `storage`: the storage move copies every blob to a new backend in the background behind a `storage.Mirror`; owner hooks of the `storage` settings group |
| `internal/nexus` | Module `nexus`: Nexus 3 REST client, sources, mapping, the sync scheduler and runner, and cut-over. Names no format; it calls the `Importer` of the target format (its `Plan` for the target repo, its `Run` for each file) |
| `internal/auth` | Users (kinds `local`/`oidc`), argon2id passwords, Basic-auth cache, read tokens, sessions, rate limiting, permission checks. A caller is a `Principal` with one credential kind (`Cred`: password, read token, session, setup session, publish session, device). `Identify` reads the credentials a route policy names. Token kinds are verifiers by prefix (`AddTokenKind`). Also core bearer tokens (`IssueBearer`), fresh second-factor proofs (`CheckProof`, `IssueGrant`) and the session view (`View`) |
| `internal/oidc` | Module `sso`: OIDC sign-in for any person at the provider, the server permissions from a claim (`oidc.permission_claim`, `oidc.permission_map`), and the SSO confirmations of a device approval and of a setup code: `/api/auth/oidc/login`, `/device`, `/setup` and `/callback` (go-oidc + x/oauth2, PKCE S256); owner hooks of the `sso` settings group |
| `internal/publish` | Module `publish`: device enrollment, setup codes, nonces, trust policies, the CI token exchange, the CI providers `github-actions` and `jenkins`, the token kind `trs` of publish sessions, the device proof of format reads, the publish API, the cleanup job, and the install scripts and downloads of the Telesphorepo CLI |
| `internal/edge` | Module `edge`: the R2 objects that the edge Worker reads (pointers, access objects, `config.json`), the pipeline watcher and the outbox that keep them, the regions, `wrangler.jsonc`, the Worker download, `/api/edge/...`. See **Edge delivery** |
| `edge/` | The edge Worker (TypeScript, bun, wrangler at an exact version): `fetch` (serve or pass), `queue` (blob copies) and `scheduled` (the gap check); `scripts/provision.ts`; `test/` holds the fixture of the `edge` e2e zone. See **Edge delivery** |
| `internal/clidist` | The CLI archives that the server embeds (`go:embed all:dist`) and serves at `/cli/`: `Targets`, `Archive`, `FS`, `Sums`. `go tool task build`, `dev`, `cli` and `release` write them |
| `internal/edgedist` | The edge Worker folder that the server embeds (`go:embed all:dist`) and hands out at `GET /api/edge/worker.zip`: `telesphorepo-edge.zip` (the bundle, `package.json`, `bun.lock`, `README.md`, `scripts/provision.ts`, all under `telesphorepo-edge/`) and `VERSION`, read by `Zip`. `go tool task build`, `dev`, `edge` and `release` write them |
| `internal/repos` | Repos, their kind, their active operation (`Repo.Op`) and per-format JSON config. `Create` and `Update` (a `Patch`) are the only changes of a repo record: each checks the whole new state first, then saves it in one transaction with the format's `OnRepoChange` and one audit event. A `Validator` (the format registry) checks the config and a `KindCheck` the kind. Modules drop what they keep about a deleted repo with `OnDelete`. A module adds repo values that it keeps in its own tables as fields (`repos.Field`, `AddField`): `Create` and `Update` validate and save them in the same transaction, and `Repo.Fields` holds them. Keeps the group members and the proxy remotes in memory (see **Group and proxy repositories**) |
| `internal/proxy` | Fetches the files of proxy repos from their remotes (the `assets.Fetcher` of the read path), stores them through `Commit` with source `proxy`, answers a remote's 401 challenge through the format's `RemoteAuth`, and remembers remote 404s; `proxy.Answer` is the answer of the registry token protocol (also for the Nexus client), `proxy.Send` a request to a remote as a fetch sends it (e.g. a remote tags list) |
| `internal/format` | The format contract: `Spec` (every optional capability is a field that is nil when the format lacks it), the `Registry` (`All`, `Get`, `Hooks`, `RebuildRun`, `OnRepoChange`, `ValidateConfig`, `SupportsKind`, `Importer`), `Kit` and `Standard` (the default routes), `Importer.Run` (the shared import of one source file), and the rendered documents: `WriteDocument` (a `Document`, a body with its strong `ETag`, answered with 304 on `If-None-Match` and `Vary`; `NewDocument` gives an uncached body the SHA-256 of the body) and `DocumentCache` (an LRU of rendered documents, bounded in entries, in bytes and per entry, keyed by `DocumentKey` of what the body depends on: a revision of the rows or the SHA-256s of the sources, and the base URL). A cached document's ETag is the SHA-256 of its key, so `DocumentCache.Serve` answers a matching `If-None-Match` with 304 before it looks up or builds the body; absolute URLs in a document start at `route.API.BaseURL` |
| `internal/version` | The version schemes of the package formats, with no outside module: `semver.go` (SemVer 2.0 for npm and Cargo; `CleanSemVer` is npm's loose clean of a package.json version), `pep440.go` (PEP 440: normal form and order), `nuget.go` (NuGet: normal form, order, `IsSemVer2`). Each version has a `SortKey`, a string whose byte order is the scheme's order, for the `sort_key` columns that keyset paging reads |
| `internal/maven` | Module and format `maven`: the standard routes, release immutability (`Admit`), the content types, the version and build rows (`OnCommitted`, `OnRemoved`), the merge of `maven-metadata.xml` in a group, the metadata after a delete (`AfterRemove`), web UI uploads with checksums and metadata. Owns its tables and queries (`queries/`, `dbq/`) |
| `internal/debian` | Module and format `debian`: `.deb` upload/parse through `Commit`, the package rows (`OnCommitted`, `OnRemoved`), the index rebuild (`Rebuild`), `/debian/{repo}/key.asc`, the plan of a Nexus apt import (distribution and signing key). Owns its tables, queries (`queries/`, `dbq/`) and architecture config; binds a signing key per repo (`SigningKey`) |
| `internal/raw` | Module and format `raw`: plain files at `/raw/{repo}/{path...}` (the standard routes) with an `overwrite` policy (`Admit`) |
| `internal/cargo` | Module and format `cargo`: the Cargo sparse index (`index/`), the `.crate` files (`crates/`) and the publish and yank API (`api/v1/`) for hosted, proxy and group repos; owns its tables and queries. See **Cargo registry** |
| `internal/npm` | Module and format `npm`: the registry API at `/npm/{repo}/` (one route per method that parses the rest of the path), tarballs through `Commit`, the version rows (`OnCommitted`, `OnRemoved`), packuments rendered per request, the streaming publish body, the Nexus `npm` importer and web UI uploads of `.tgz` files. See **npm registry** |
| `internal/nuget` | Module and format `nuget`: the NuGet V3 protocol at `/nuget/{repo}/` (see **NuGet repositories**): push, unlist and relist, the flat container, the registrations and the search rendered per request, the proxy of a NuGet V3 remote and the group name rule. Owns its table and queries (`queries/`, `dbq/`) |
| `internal/pypi` | Module and format `pypi`: the simple repository API rendered per request (hosted from its rows, proxy from the cached upstream page, group from the owning member), the legacy upload, the file and project rows (`OnCommitted`, `OnRemoved`), the proxy fetches with the `files` base of the repo config. Owns its tables, queries (`queries/`, `dbq/`). See **PyPI** |
| `internal/signing` | Module `signing`. OpenPGP: key `Generate` and `Parse`, the `Keys` service (create, import, delete, one signer per key, the key of each repo), the repo field `signing_key`, clearsign/detach-sign (ProtonMail go-crypto). See **Signing keys** |
| `internal/files` | The file tree and the details of one file. Writes and deletes go through the byte pipeline |
| `internal/audit` | The audit log: the action registry (`Actions`), the actor in the context (`WithActor`, `System`, `CLI`), `Record` and `RecordTx`, the list query and `Cleanup`. See **Audit log** |
| `internal/activity` | Module `audit`: `GET /api/audit`, `GET /api/audit/actions`, `GET /api/audit/people`, `GET /api/stats`, the permission `activity.read`, the settings group `activity` and the jobs `audit.retention` and `audit.downloads` |
| `internal/netrules` | Module `network`: client IP resolution behind trusted proxies (the client address of every request), the IP lists (presets and custom lists) and the per-repo IP rules. See **Network rules** |
| `internal/api` | Module `core`: sign-in, the account, people, read tokens (token kind `trp`), repos, grants, files, uploads, search, the settings API and `/api/formats`. The JSON API of every module is under `/api/`, wrapped in `http.CrossOriginProtection` |
| `internal/httpx` | JSON helpers, the error answers (`Error`, the problem types of `DeclareProblem`, `WriteProblem`; see **Problem documents**), `StatusError` (an error with its HTTP status), `RepoPath` (the URL check of a `{path...}`, then `repopath.Parse`), `Limit` (default 50, max 200), `AcceptsEncoding` and `GzipJSON` (see **Compression** under the JSON API) |
| `internal/demo` | The dev demo data (`telesphorepo-server dev`): it writes repos, users and files through the byte pipeline, with the demo data of each module (`Extender.Demo`). See **Demo data** |
| `internal/containerengine` | Docker/Podman detection for tooling and e2e; not linked into the server |
| `web/` | Svelte 5 + Vite UI (Tailwind CSS v4, shadcn-svelte Maia style), embedded via `web/embed.go` (`go:embed all:dist`). See **Web UI** |
| `tools/task` | `go tool task`: dev, build, preview, test, cli, release, gen and doctor |
| `e2e/` | End-to-end suite in four zones (tags `e2e`, `import`, `scale`, `clients`) using testcontainers, and the macOS zone (tag `macos`) of `release.yml` |

## Modules

Every repository format and every core feature with routes is a module (`internal/module`). A
module is `module.Module{ID, Title, Requires, Register}`. `Register` runs once per process,
before the database opens, and declares what the module adds; each `Extender` call adds one
entry to the module's manifest. The server reads the manifests and never finds a capability by
type assertion.

| Extender call | What it adds |
|---|---|
| `Schema(db.Component)` | The module's migrations (`migrations/*.sql`) and queries for the plan guard; the component is named after the module |
| `Settings(*settings.Group)` | A settings group that the module owns: its keys, the permission that edits them, the UI page, and the owner hooks `Validate`, `Test` and `Apply` (see **Settings model**) |
| `Permissions(perm.Subsystem, perm.Def...)` | A permission subsystem of the module and its permissions (see **Permissions**); one call per subsystem |
| `AuditActions(audit.ActionDef...)` | The audit actions the module records, each with a label and a subsystem (see **Audit log**) |
| `OpType(ops.Type)` | A type of repo operation that the module begins: its message, and `Resume` and `Clear` when it has them (see **Repo operations**) |
| `Init(fn)` | Startup code with the `Host`; an error stops startup |
| `Routes(fn)` | Routes on the module's `route.Router` |
| `RootPrefix(prefix)` | A path outside `/api/` and the format segments that the module owns, e.g. `/install.sh` |
| `Job(jobs.Job)` | A job named `<module>.<name>` with `Start`, `Run` and `Tick` |
| `Format(format.Spec)` | A repository format; the module owns `/<name>/`. A nil `Routes` mounts `format.Standard` (see **Byte pipeline**) |
| `RepoField(repos.Field)` | A repo value that the module keeps in its own table, e.g. `signing_key`: `Applies` (by format and repo kind), `Validate`, `Default`, `Save` (in the repo transaction), `Committed`, `Load` (from memory). It serves once the module's `Init` ran |
| `RepoGuard(access.Guard)` | An extra rule of the access engine (the IP rules): `Name`, `Check`, `Describe` (the access report), `Refused` (search) |
| `CIProvider(ci.Provider)` | A CI system whose tokens make build records and whose trust policies the access report describes |
| `ResolveClient(fn)` | The client address and scheme of every request; at most one module |
| `TokenKind(prefix, verifier)` | A token kind (`trp`, `trs`) |
| `Demo(module.DemoData)`, `Seed(module.SeedFunc)` | The dev demo data and the rows of `telesphorepo-server seed` of the module's one format |

`module.Host` holds the kernel services (DB, settings, the permission registry, live storage,
audit, jobs, `Blobs`, `Assets` (the pipeline), `Reader`, `Uploads`, `Rebuilds`, repos, `Ops`, auth,
access, the format registry, `CI` (the CI providers), `Builds` (the build records), `route.API`) and a log tagged with the module ID. An `Init` and every job of a module record audit events as the system actor of
the module (`audit.System`). A
module gives a service to the modules that require it with `Host.Provide` (as its own type) or
`module.ProvideAs[T]` (as an interface, e.g. `publish.Minter`), and a module gets it with
`module.Use[T]`. `Use` panics when no required module provided a `T`.

`internal/modules/modules.go` lists the modules in start order. `module.Check` refuses a bad,
reserved (`api`, `healthz`, `assets`, `setup`) or repeated ID and a `Requires` entry that is not
earlier in the list.

**What a new format uses.** A format module edits no core file. It sets the `format.Spec` fields
it needs and leaves the others nil. The kernel gives it these parts:

- The routes: `format.Standard` (GET and HEAD through the read path, PUT through `Commit`), or its
  own `Routes`. A path outside `/<name>/` (for example `/v2/`) is a `RootPrefix`. A route policy
  can set `Errors` (the format's own error JSON), `Challenge` and `TokenHeader`.
- Repo names: `ValidName` adds a rule for the names of the format's repos (`repos.Validator`
  gets the format, the name and the config; e.g. a docker repo name is one OCI name segment).
- The bytes: `Commit` and `CommitAll` with the hooks `Describe`, `Admit`, `OnCommitted`,
  `AfterCommit`, `OnRemoved` and `AfterRemove`; `Remove`; the read path with `Classify`, `Merge`
  and `RemoteAuth`; resumable uploads; blob mounts (`Body.Blob`); the rebuild queue with
  `Rebuild`.
- Access: `route.FormatRead` and `route.FormatWrite`, `access.Engine.Decide`, core bearer tokens
  (`Policy.Bearer`, `auth.IssueBearer`) and `publish.Minter` for a token exchange, extra token
  kinds (`Extender.TokenKind`).
- Its own data: `Schema` with `queries/` and a `sqlc.yaml`, repo fields (`RepoField`), settings
  (`Settings`), permissions, audit actions and repo operation types.
- The web UI: the descriptor in `format.Spec` (see **Format UI descriptors**), and a named
  `formatParts` slot in the UI module of the format for a part that only the format can draw.
- Edge delivery: `Edge` (`format.EdgeSpec{Shape, Immutable, Mutable}`): the URL shape that the
  Worker parses (`standard` or `oci`) and the globs of the paths that never change. A format
  without it gets no Worker route (see **Edge delivery**).

| ID | Package | Requires | Owns |
|---|---|---|---|
| `core` | `internal/api` | | Sign-in, `/api/me`, users, read tokens (`trp`), repos, grants, files, upload plans and uploads, delete plans, resumable uploads, repo operations, search, the settings API, `/api/formats` (the UI descriptors), the server identity (`/api/identity`, the root prefix `/identity/`), the snippets and CI files of each repo, `/api/permissions`, the access report; the subsystems `repositories`, `people`, `settings` and `repo`; the kernel's audit actions; the settings group `general`; the operation type `delete`; jobs `core.optimize` (`PRAGMA optimize` every 6 h), `core.blob-gc`, `core.group-counts` (see **Group counters**), `core.upload-sessions` (every 10 min), `core.rebuild` and `core.ops` |
| `audit` | `internal/activity` | | `GET /api/audit`, `GET /api/audit/actions`, `GET /api/audit/people`, `GET /api/stats`, the subsystem `activity`, the settings group `activity`, jobs `audit.retention` (every hour: events and download days) and `audit.downloads` (writes the download counters every minute and at shutdown) |
| `storage` | `internal/move` | | `/api/storage/moves/...`, the subsystem `storage`, the settings group `storage` (its `Validate`: the S3 rules; `Test`: the probe; `Apply`: refuse a change while a move runs, probe the new store, start the move), the `storage.move.*` actions, job `storage.resume` |
| `backup` | `internal/backup` | | `GET /api/backup`, `POST /api/backup/run`, the subsystem `backup`, the settings group `backup` (its `Test`: the server writes a probe file into the folder), the `backup.*` actions, job `backup.schedule` (every minute: a backup when one is due). No table: the backup folder holds its state |
| `network` | `internal/netrules` | | The client address, IP lists, repo IP rules (a repo guard and the networks of the access report), the IP lists of the trusted proxies (`/api/network/trusted-proxies`), the subsystem `network`, the settings group `network` (`trusted_proxies`; its `Validate` refuses a `list:` entry in a change), the `network.*` actions, job `network.presets` (every hour) |
| `signing` | `internal/signing` | | Signing keys and `/api/signing-keys/...`, the repo field `signing_key` (for the hosted repos of the formats with `SigningKey`), the subsystem `signing`, the `signing.key.*` actions; provides `*signing.Keys` |
| `publish` | `internal/publish` | `audit` | Devices, publish sessions (`trs`), CI trust policies, the CI providers `github-actions` and `jenkins`, `/api/devices/...` and `/api/publish/...`, the subsystem `ci`, the settings group `publishing`, the `publish.session.*`, `device.*`, `setup_code.create` and `ci.pipeline.*` actions, job `publish.cleanup` (every minute), `/install.sh`, `/install.ps1` and `/cli/` (see **Install scripts and CLI downloads**); provides `*publish.Service` and `publish.Minter`; loads the publish sessions and devices of core bearer tokens and gives the access report its CI pipelines |
| `edge` | `internal/edge` | `storage`, `network`, `publish` | Edge delivery (see **Edge delivery**): the subsystem `edge` with `edge.manage`, the settings group `edge`, `GET /api/edge/status`, `POST /api/edge/check`, `GET` and `POST /api/edge/speedtest`, `GET /api/edge/worker.zip` and `GET /api/edge/wrangler.jsonc`, the root prefix `/.well-known/telesphorepo-edge/` (the server's answer on the reserved path of the check), the pipeline watcher, jobs `edge.outbox`, `edge.backfill`, `edge.reconcile` (every 5 min) and `edge.seen` (every minute), the line of the access report that says where downloads come from |
| `sso` | `internal/oidc` | `publish` | `/api/auth/oidc/...`, the subsystem `signin`, the settings group `sso` (its `Validate` with the startup guard and the claim map check, `Test` and `Apply`: the discovery check), the `sso.*` actions; tells auth when single sign-on manages permissions |
| `nexus` | `internal/nexus` | `publish` | `/api/nexus/...`, the subsystem `nexus`, the settings group `nexus`, the `nexus.*` actions, the operation types `mirror`, `empty` and `merge`, the repo fields `rehearsal_for` and `moves_into`, job `nexus.scheduler` (resumes running syncs, empties and merges, then the schedule loop); a merge gives the CI pipelines of `publish` to its target |
| `maven` | `internal/maven` | | Format `maven` (publish category `cli`) with the tools `gradle` and `maven` (their CI steps) |
| `debian` | `internal/debian` | `signing` | Format `debian` (publish category `cli`) with its schema, `Rebuild` and the tool `telesphorepo` |
| `raw` | `internal/raw` | | Format `raw` (publish category `cli`) with the tool `telesphorepo` |
| `nuget` | `internal/nuget` | | Format `nuget` (hosted, proxy and group repos; publish category `cli`) with its schema, the tool `dotnet`, the audit actions `nuget.unlist` and `nuget.relist`, the Nexus importer of `nuget`, demo data and seed |
| `docker` | `internal/docker` | `network`, `publish` | Format `docker` (an OCI registry: hosted, proxy and group repos) with its schema; the root prefix `/v2/`, `/docker/` (the token endpoint) and `GET /api/docker/{name}/manifest`; the settings group `docker`; job `docker.links`; the publish category `helper` (the native tools push through the credential helper of the CLI, see **Publishing**); the Nexus importer of `docker`; demo data and seed (see **Docker registry**) |
| `cargo` | `internal/cargo` | | Format `cargo` (publish category `cli`, hosted, proxy and group repos) with its schema, the tool `cargo` (its CI steps), the `cargo.yank` and `cargo.unyank` actions, the Nexus importer of `cargo`, demo data and seed (see **Cargo registry**) |
| `npm` | `internal/npm` | | Format `npm` (hosted, proxy and group; publish category `cli`) with its schema, the tools `npm`, `pnpm`, `yarn` and `bun` (their CI steps), the Nexus importer of `npm`, demo data and seed (see **npm registry**) |
| `pypi` | `internal/pypi` | | Format `pypi` (hosted, proxy and group repos; publish category `cli`) with its schema, the tools `uv`, `poetry` and `pip`, the Nexus importer of `pypi`, demo data and seed (see **PyPI**) |

**Routes.** The route table (`internal/route`) gives each module a `Router`. A module registers
under `/api/`, under `/<name>/` of each format it registers, and under its root prefixes. Any
other path, or a prefix that two modules claim, panics at startup. The kernel adds `GET /healthz`
and the web UI at `/`. Every route under `/api/`, and every route that accepts a UI session, is
wrapped in `http.CrossOriginProtection`.

**Route policy.** A module registers each route with `Router.Handle(pattern, route.Policy,
handler)`; the handler gets a `route.Call` with the caller (`P`), the repo (`Repo`, `RepoPerms`) and
the fresh second factor (`Proof`), and the repos of `SomeRepo` (`Managed`). No handler checks credentials itself. A `Policy` has:

| Field | Meaning |
|---|---|
| `Accept` | The credential kinds that pass (`auth.Cred` bits). 0 with `Anonymous` is a public route: the middleware reads no credential |
| `Anonymous` | A request without a credential passes to the repo step or the handler |
| `Bearer` | Core bearer tokens (`trb.`) are read on this route. A bearer passes when `Accept` holds its credential kind, or on a repo route that accepts a publish session when its scope of the repo has an open publish session |
| `TokenHeader` | A token of a registered kind is read from this header too (e.g. `X-NuGet-ApiKey`; `Authorization` for a raw token) |
| `FreshFactor` | The request carries a fresh second factor in `X-Telesphorepo-Proof` |
| `Perm`, `AnyPerm` | A server permission ID the caller holds, or one of several |
| `AllPerms` | Server permission IDs the caller holds every one of, on top of `Perm` and `AnyPerm` (`route.UIPerms(ids...)`: a UI session with all of them). `SomeRepo` does not stand in for them. A Nexus merge needs `nexus.manage` and `repositories.all` |
| `SomeRepo` | A repository permission ID that passes in place of `Perm` when the caller holds it on at least one repo. The handler gets those repos in `Call.Managed` (`All`, or the repo IDs) |
| `Repo` | `route.RepoRule{Name, Format, Op, Perm, AnyPerm, Ops, Pick}`: the repo the route acts on (default: the path value `{repo}`, else `{name}`), the format it must be of (another format is 404), the operation (`access.Meta`, `Read`, `Write`, `Delete`, `Manage`, `Mint`) and the repository permission it needs (or one of `AnyPerm`; none with `Meta` is any). With `Pick`, the operation and its permission are the entry of `Ops` (`route.RepoOp{Op, Perm}`) at the index `Pick(r)` returns, per request (an index outside `Ops` is 500); the router checks every entry at startup. The docker `DELETE /v2/...` route is the one user: an upload cancel is a `Write`, everything else a `Delete` |
| `Errors` | The writer of a refusal; nil is `route.Refuse`: a problem document under `/api/` (see **Problem documents**), `{"error": "..."}` on a format route, each with the refusal code of the decision as the member `code` (e.g. `session`, `publish_session`, `network`, `read_only_kind`). A format with its own error JSON sets it |
| `Challenge` | The `WWW-Authenticate` of a 401, from the refusal (`route.Challenger(w, r, decision)`); nil is a Basic challenge outside `/api/` and none for a request with `X-Telesphorepo-UI: 1` |

Presets: `route.Public`, `route.UI` (a full UI session), `route.Setup` (a UI session, setup-only
too), `route.AnyUser` (password, read token, UI session), `route.UIPerm(id)`,
`route.UIRepo(op, ids...)`, `route.RepoRead()` (a JSON read of a repo with `repo.download`),
`route.FormatRead(format)` (anonymous, password, read token, UI session, publish session, device;
`Read` with `repo.download`) and `route.FormatWrite(format)` (a publish session; `Write` with
`repo.publish`).

The router panics at startup when a policy that accepts a password, a read token or a device
(`auth.CredDevice`) has a method other than GET or HEAD, or a server permission (`Perm` or
`SomeRepo`), and when a `Write` route accepts a credential other than a publish session. A route with `Bearer` has no server permission,
and on a method other than GET or HEAD its repo rule is a `Write` or a `Delete`. So a password
never changes state and never reaches a permission route, also inside a bearer token.

The middleware runs in this order and writes the first refusal:

1. Identify the credentials of the policy (the Authorization header always; the session cookie
   when `Accept` has a UI session). Bad credentials: 401 `invalid credentials`; an ended publish
   session: 401 `publishing needs a publish session: <base_url>/#/settings/tokens`; too many
   failures: 429 with `Retry-After`. A credential kind the policy does not accept: 403
   `second_factor_required` for a setup-only session, 403 `a publish session token only publishes`
   for a publish token, and 401 `sign in through the web UI for this action` for a password or a
   read token. On a `Write` route a password or a read token goes on to the repo step, which
   answers that publishing needs a publish session. A core bearer token is checked by the kind
   of its credential; on a repo route that accepts a publish session, a bearer of another kind
   goes on to the repo step: a read passes only with the open publish session of its scope for
   the repo, and a write or a delete gets the answer of the access engine for its scope.
2. A request without a credential passes only when the policy is `Anonymous` or names a repo; else
   401 (`authentication required` where a password is accepted, else `sign in through the web UI
   for this action`).
3. The fresh second factor (`FreshFactor`): `X-Telesphorepo-Proof: totp <6 digits>`, `passkey
   <base64url of the assertion JSON>` (after `POST /api/security/passkeys/assert`) for a local
   account, `grant <grant>` for an SSO account (the grant of a fresh SSO sign-in, used once). No
   factor: 403 `second_factor_required` (in dev mode a person without a factor passes); a wrong
   proof: 401 `second factor did not verify`; no proof: 400; a missing or used grant: 401. Failures
   count against the username like failed passwords.
4. The server permission, or `SomeRepo` on at least one repo: 403 `permission required: <label>`; then every permission of `AllPerms`: 403 `permission required: <label> and <label>`
   (e.g. `permission required: Manage signing keys, or Edit settings on a repository`).
5. The repo: an unknown repo, or one of another format, is 404 (401 for anonymous on a private
   repo); then `access.Engine.Decide` (see **Access engine**). A format read (a `RepoRule` with
   `Format` and `Read`: every route of `FormatRead`) of a repo that not everyone reads
   (`access.Engine.Private`) sets `Cache-Control: private` before the handler runs. Such a repo
   has no anonymous read or has read IP rules, or it is a group with such a member. The IP rule
   check is `access.ReadRestricter`, an optional guard method that the network guard answers
   from its in-memory rules. So every answer of the read (file bytes, HEAD, 304, registry
   manifests, blobs and lists) tells a shared cache, such as a CDN, not to keep it. A public repo
   without read IP rules gets no `Cache-Control` from this step.

From step 1 on, the request context holds the audit actor of the caller with the client address
(`audit.WithActor`), so the handler, the services it calls and a guard refusal record events with
it.

**Access engine.** `access.Engine.Decide(ctx, r, p, repo, op, need)` is the one decision of repo
access. The route middleware, the publish-session mint, the group read path and the access report
use it. In order:

1. The permissions of `p` on the repo: its grants (every repository permission with a server
   permission that has `AllRepos`, `repositories.all`), `repo.download` for anyone on a repo with
   anonymous read. A publish session holds, on its own repo, `repo.download` and `repo.publish` (a
   CI session) or the user's current `repo.download`, `repo.publish` and `repo.delete` (a device or
   manual session); on another repo what anonymous holds. A core bearer token is limited to its
   scopes. When its scope of the repo lacks the operation (or it has none), a guard that refuses
   the caller answers first (403, recorded); then a token that reads the repo gets the answer of
   its credential: a `Write` or `Delete` on a proxy or a group is the read-only 405, a `Write`
   elsewhere is 401 `publishing needs a publish session: ...`; anything else is 401 with the code
   `scope` (the challenge of an OCI route adds `error="insufficient_scope"`). With an open publish
   session in its scope of the repo, the token acts as that session (`auth.Principal.For(repo)`;
   upload ownership and `format.OriginOf` use the same principal).
2. The repo kind: `Write` and `Mint` on a proxy or a group, and `Delete` on a group, answer 405
   with `Allow: GET, HEAD` and the read-only text to a caller who can download; anyone else gets
   the refusal of step 3. The cached files of a proxy can be deleted.
3. The permission: `Write` without an open publish session of this repo is 401 `publishing needs
   a publish session: ...`; anonymous is 401 `authentication required`; anyone else 403
   `permission required on <repo>: <labels>` (`forbidden` for `Meta`).
4. The operation: a repo with an active operation (`repo_ops`, see **Repo operations**) refuses
   `Write`, `Delete` and `Mint` with 409 and the operation's message.
5. The guards, in module start order, for `Read`, `Write`, `Delete` and `Mint` only: a refusal is
   403 and the guard's audit event. The network guard maps `Read` to the read rules and the other
   operations to the publish rules, and records `network.denied`.

Guards never see `Meta` or `Manage`, so a holder of `repo.settings` or `repo.access` can open the repo and
fix the rules that refuse them. `Engine.Allows` is `Decide` without the audit event of a guard (a
group skips a member that the reader may not read). `Engine.GroupMembers(ctx, r, p, group)` is the
one list of the members that a group read uses (the read path, the registry tags list and
referrers): each member in read order that `Allows` with `Read` and `repo.download`. A core bearer
token names the group in its scopes, not the members. So when its scope of the group holds `Read`,
the members are checked with `auth.Principal.InGroup(group)`: the credential behind the token
without its scopes, the same as that credential over HTTP Basic. Each member still needs the
credential's own `repo.download` and passes the guards. The copy exists only inside a group read
that the token passed, so the token reads no member directly. "May this caller write to this repo
now" is always `Decide(..., Write)` or `Decide(..., Mint)`.

**Core bearer tokens.** `auth.Service.IssueBearer(p, scopes, ttl)` returns
`trb.<payload>.<HMAC-SHA256>.<Ed25519>` (each part base64url without padding) for at most 15
minutes; `IssueUploadToken` signs the same way. Both signatures sign the ASCII bytes of the
payload part. The HMAC key is HKDF-SHA256 of the master key with the info `telesphorepo bearer
v1`. The server checks both signatures, so a token is valid in one spelling only (a token of
another number of parts is refused). The
Ed25519 key is derived from the master key with the info `telesphorepo edge v1` (the seed of the
key); the edge Worker checks that signature with the public key of `config.json`
(`auth.Service.EdgePublicKey`), so the HMAC key never leaves the server. The payload holds the
credential kind (none for anonymous), the user, the read token (its row ID `t` and its public ID
`tp`), the scopes (`{repo, ops, session}`) and the expiry. The credential is a password, a read token, a publish session or a device
(`auth.CredDevice` with `Principal.DeviceID`: the device of a format's token exchange or of a
device proof); a UI session gets no bearer. A scope holds `Read`, `Write` and `Delete` only. `Write` and `Delete` stay
only with a scope session of the credential: the publish session of a publish session credential,
or for a device an open session that the device minted for the scope's repo
(`auth.PublishSession.DeviceID`). So a bearer from a password, a read token or the anonymous
caller carries `Read` only, and a bearer writes to one repo at most. On each request the kernel
loads the credential again (the user enabled, the read token present, the publish session open,
the device not revoked or expired and its user enabled, with the user's current permissions; the
publish module loads sessions and devices through `SetSessionLoader` and `SetDeviceLoader`). It
refuses the token when a scope session is not a session of the credential (for a device: an open
session that the device minted), or when the scope names another repo than the repo of that
session. A device reads what its user reads: the access engine gives it the grants of its user,
and the router treats it like a password (GET and HEAD routes without a server permission only). A route
reads bearer tokens only with `Policy.Bearer`; a format with its own token exchange (the docker
format, `GET /docker/token`) issues them. `auth.Service.Authenticate(r, username, password)` checks
a password or a token of a registered kind that came outside the Authorization header (a token
endpoint form), with the failed-attempt limits of the client address. `auth.Service.Failed(r,
username)` counts a failed attempt of a credential that the auth service does not check itself (a
CI token: the client address only), and `auth.Service.Blocked(r)` says how long the address
waits. `auth.PublishSession.Expires` is when a session ends at the latest.

**Device proof on format reads.** A route whose policy accepts `auth.CredDevice` (every
`route.FormatRead`, and `GET /v2/_catalog`) also reads a device proof
`trx.<device id>.<nonce>.<signature>` (`auth.DeviceProofPrefix`) as the Basic password or as the
Bearer value. The publish module checks it (`auth.Service.SetDeviceProofVerifier`,
`publish.Service.VerifyDeviceProof`): the nonce is a token exchange nonce of the device that has
not expired, the device is usable, and the signature over `SignedMessage(nonce, device id, "*")`
verifies. The caller is then the `CredDevice` principal with the grants of the device's user. The
router allows `CredDevice` only on GET and HEAD routes without a server permission, so a device
proof never writes. A proof that does not verify counts as a failed attempt of the client address.

**Minting a publish session.** The publish module provides `publish.Minter` (`module.Use`):
`Mint(ctx, r, MintCred, repo)` checks the credential, runs `Decide(..., Mint)` with `repo.publish`,
writes the session row and the `publish.session.create` event, and returns the open session without a
token. `MintCred` is one of: `Device` (a device signature over a nonce of
`POST /api/publish/challenge` for one repo, or over a nonce of `ExchangeChallenge` (the same
route with `exchange: true`) signed for the repo `*`), `CIToken` (a CI identity token that
a trust policy of the repo allows; a token that minted a session for the repo before gets that
session back while it is open) or `Fresh` (a UI session whose request carried a fresh second
factor). The three mint routes use the same code and answer the token; there a CI token mints
once, and an exchange nonce does not mint. `POST /api/publish/sessions/exchange` mints with an
exchange nonce (`publish.Service.Exchange`), as the `Minter` does, and answers the token. The
secret of a session that an exchange nonce mints is HMAC-SHA256 of the public id, keyed with the
nonce, so a later exchange with the same nonce and repo answers the same token. Every mint answer
(`POST /api/publish/sessions`, `/manual`, `/oidc` and `/exchange`) is `{id, token, repo, format,
kind, expires_at}`. A refusal of `Decide(..., Mint)` at these routes and at
`POST /api/devices/setup` is written by `route.Refuse`, as the route policy writes its own: a
problem document with the refusal code as the member `code` (e.g. `network`, `permission`), and
`Allow: GET, HEAD` with the 405 of a proxy or a group. An exchange nonce lives as long as a publish session
(`publish.session_ttl`) and is not used up: a token exchange sends the same proof on each token
request of one push. It mints one session per repo (`publish_exchange_sessions`), and a later
mint for the same repo gets that session back while it is open, so upload sessions continue
across token requests. When that session ended, the proof mints no new one. Through the
`Minter` a CI token that minted a session for the repo gets it back after a check of the
signature, the issuer, the audience and the trust policy only, without the age of `iat` and
without `exp`: the open session bounds the time, so a token request in the middle of a long push
works. A first mint needs `iat` at most 10 minutes old and `exp` ahead. `Minter.Check(ctx, r,
MintCred)` verifies a credential without a repo and mints nothing: a CI token that an enabled
trust policy of its issuer allows (audience, claims, `iat`, `exp`), or a device proof over an
exchange nonce (nonce, signature, a usable device). It records no use of the token.

**Format UI descriptors.** `GET /api/formats` answers, for each format, everything the web UI
shows or chooses per format, so no shared UI code names a format. It comes from the
`format.Spec`: `Description`; `Config.Fields` (the repo config form: `{key, label, help, type:
string|bool|list|enum|secret, options, required, default, placeholder, change_note}`; a change of
a field with a `change_note` asks first and lists the note); `RepoTabs` and the module repo fields;
`Upload` (`single`, `folder`, `accept`, `hint`, `keep` (the globs of the files of a folder that the
browser sends), `one` and `many` (the words for an item), `note`, `ignored`, `empty`, `fields`,
`reads` (the globs and `max_bytes` of the files whose first bytes a plan reads)); `Tools` (the
build tools of the CI files: `{id, label}`); `Publish` (`publish`: `cli`, the format publishes
with `telesphorepo publish` in the project folder, or `helper`, the native command of the tool
through a credential helper of the CLI; `format.PublishCategory`);
`Importer.Sources` (`import_sources`, the Nexus formats it imports); `Versions` and
`ArtifactUsage` (`browse`); and `UI` (`format.UIDescriptor`): `delete_notes` (`{match, one, many,
text}`: the delete plan counts the files of a delete that the glob matches), `file_details`
(`{match, part}`: the part of the format's UI module that the details of a matching file show),
`remote_presets`, `proxy_note`, `public_key` (the path of the public signing key under a repo, e.g.
`key.asc`), and `labels` (`artifact_kinds`, `artifact_name`, `tools` (the format's
`SnippetTools`, then the titles of the CI providers), `install_heading`, `build_files` (`{extension,
classifier, label}`: the words for the files of a build, the first match wins; `*` is any, a
classifier `""` none)). A glob is a repo path pattern: `*` is any run of characters except `/`, `?`
one character except `/`, `**/` any number of folders; matching is case-sensitive
(`format.Glob`, and `lib/glob.ts` in the UI).

**Snippets and CI files.** A `format.Snippet` is `{id, tool, purpose, context, via, file, title,
lang, code, diff, note}`. `diff` is set on a GitHub Actions workflow and a Jenkinsfile: what a
project's existing file needs, as diff lines, each after its mark (`+` added, a space for
context). The workflow change adds `permissions` (`id-token: write`, `contents: read`), the
install step and the publish step, with the trigger, a job with the checkout and the toolchain,
and the build as context; the Jenkinsfile change adds the publish stage after a Build stage of
context (`ci.GitHubWorkflowDiff`, `ci.JenkinsfileDiff`). A build that belongs to the publish
(`PublishSteps.BuildAdded`: the Docker build, whose tag names the registry) is added too. A format's `Usage` gives the use snippets and the publish snippets for a
computer (the Telesphorepo CLI, `format.CLISnippet`) and by hand; the core adds the CI files.
`internal/ci/workflow.go` holds the one GitHub Actions builder (`GitHubWorkflow`), the one
Jenkinsfile builder (`Jenkinsfile`), the Jenkins freestyle builder (`JenkinsFreestyle`) and
`Indent`. A `format.Tool` gives `Publish(repo, baseURL, system) ci.PublishSteps` (`Toolchain`
steps of GitHub Actions, `Build`, `BuildPS` and `PublishPS` (the PowerShell forms of `Build` and
`Publish` for a Windows Jenkins agent; empty: `Build` and `Publish`), `Env`) and an optional
`CINote`. The Build stage of a Jenkinsfile runs `Build` with `sh` on a Unix agent and `BuildPS`
with `powershell` on a Windows agent (`isUnix()`). Every CI file installs
the CLI before the publish: GitHub Actions in a step of its own (`curl -fsSL <base>/install.sh |
sh`, which adds the CLI to `$GITHUB_PATH`) and keeps `permissions: id-token: write`; a Jenkinsfile
inside `withCredentials` of `OIDC_TOKEN`, with `install.sh` on a Unix agent and `install.ps1` on
a Windows agent (`isUnix()`); a freestyle step with `install.sh`. The CLI reads the identity token
of the job itself, so no CI file fetches or prints a token. The publish of the Maven tools and of
the tool `telesphorepo` (`format.CLITool`, Debian and Raw) is `telesphorepo publish`; Docker
pushes with `docker push` through a Docker config of its own that names the credential helper of
the CLI for the registry host, with `TELESPHOREPO_SERVER=<base_url>` in the env of the publish, so
the helper knows the server of the identity token. `format.CISnippet` builds the file of a tool for a system
(`github-actions`, `jenkins` or `jenkins-freestyle`, `ci.JenkinsFreestyleSystem`) and a
`ci.Trigger{Branches, Tags}`.

**Lifecycle.** `server.RunWith` starts in this order: logger and data dirs; `db.Open`; every
`Register` once per process (then the permission registry is fixed, and the audit actions, the
settings groups and the format registry are built); the migrations of the kernel and of each module
(`db.Init`, in start order; see **Schema migrations**); `settings.Load` with every group; the kernel services; each module's `Init` in start order, then the
token kinds, guards and jobs of every module; the routes; `Options.Init` of the dev command; the
bootstrap admin; the `Start` of every job in start order (an error stops startup); listen; the
`Run` and `Tick` loops of every job (`Tick` runs at once and then every `Every`). A module runs an
on-demand task (a storage move copier, a Nexus sync, a repo delete) with `Host.Go`; the rebuild
worker runs each index rebuild as a task of the core module.

At shutdown the server stops `http.Server` (30 s; a commit that runs detached from its request
finishes during that time, and a timeout ends the base context of the byte pipeline at once). Then
it stops each module in reverse start order: it cancels the module's context and waits for its
jobs and tasks. Then it waits for the `last_used_at` writes of read tokens, ends the base context,
and the database closes. No worker runs after the database closes.

## Data model (SQLite)

Each schema component has its own migrations: `internal/db/migrations` for the kernel
(component `core`), and `internal/<module>/migrations` for each module with tables
(`Extender.Schema`; the component is named after the module: `storage`, `network`, `signing`,
`publish`, `edge`, `nexus`, `debian`, `docker`, `cargo`, `npm`, `nuget`, `pypi`). A module schema
may reference the kernel tables and the tables of the modules it requires; the kernel schema
references no module table. All tables are `STRICT`. Tables with a natural key are also
`WITHOUT ROWID`. Every foreign-key child column has an index. SQLite runs `PRAGMA optimize` at
close and every 6 hours (job `core.optimize`).

**Schema migrations.** A migration is a SQL file `NNNN_name.sql` (4 digits from `0001` with no
gap, then `a-z`, `0-9` and `_`), embedded in the binary; `db.Component.Migrations` holds the
folder. The table `schema_migrations` (`component`, `version`, `name` (the file name), `checksum`,
`applied_at`; PK `(component, version)`, `STRICT`, `WITHOUT ROWID`) holds one row per applied
migration. The checksum is the hex SHA-256 of the file with CRLF line ends read as LF
(`.gitattributes` also checks out every `*.sql` with LF). `db.Init` creates the table and then,
per component in start order (the kernel first):

1. An applied version above the newest file is `db.ErrDatabaseNewer{Component, Version}`: "the
   database is from a newer Telesphorepo (core migration 0007); run that version or newer".
2. An applied migration whose file has another name or checksum is
   `db.ErrMigrationChanged{Component, Version, Name}`: the sentence names the component and the
   file and says that an applied migration must not change, so the file must be restored and the
   change made in the next migration.
3. Each file above the last applied version runs in order, each in its own transaction together
   with its `schema_migrations` row, and the server logs `applied migration` with the component
   and the file. The transaction runs on a connection of its own with `PRAGMA foreign_keys = OFF`,
   so a migration can rebuild a table (create the new table, copy the rows, drop the old one,
   rename the new one) without the drop deleting or cascading to the rows that reference it.
   `PRAGMA foreign_key_check` runs before the commit on the tables that the migration creates,
   alters, renames, drops or writes, and on the tables whose foreign keys point at one of them (so
   a migration costs what it touches, not the size of the database); a row left without its parent fails the
   migration, and the error names its table and rowid. The connection gets `foreign_keys = ON`
   back afterwards. A failed migration leaves no row, so the next start runs it again.

A database with the table `schema_components` and no migrations is `db.ErrDevDatabase`.
`db.IsIncompatible` covers all three errors: `serve` refuses to start and prints the sentence,
and `telesphorepo-server dev` deletes and recreates its own data dir. A new database applies
every migration from `0001`. `go tool task migration <folder> <name>` creates the next file.

Kernel tables (`internal/db/migrations`):

| Table | Key / indexes | Notes |
|---|---|---|
| `users` | PK `id`, UNIQUE `username`, UNIQUE `users_oidc (oidc_issuer, oidc_subject)` where `oidc_subject` is set | argon2id PHC hash (`!` for SSO users, never verifies), `disabled`, `password_changed_at`, `kind` (`local`/`oidc`), `email` |
| `api_tokens` | UNIQUE `public_id`, UNIQUE `(user_id, name)` | `secret_hash` = SHA-256(secret); optional `expires_at`; `last_used_at` written at most once per minute. These are **read tokens**: they never publish |
| `sessions` | PK `id_hash` (WITHOUT ROWID), idx `expires_at`, idx `user_id` | `id_hash` = SHA-256(cookie value) |
| `repos` | UNIQUE `name`, idx `(format, name)`, `(kind, name)`, `(last_published_at, id)`, `(total_size, id)`; partial idx `WHERE anonymous_read = 1`: `repos_anonymous (id)`, `(name)`, `(format, name)`, `(last_published_at, id)` | `format` is any registered format name (no CHECK); `kind` is `hosted` (default), `proxy` or `group`; `description` (at most 500 characters); `anonymous_read`; `config` JSON; the counters `file_count`, `total_size` and `last_published_at` (unix time, 0 for never). The values of module fields (`repos.Field`, e.g. `signing_key`) are rows of their module. Each list of `GET /api/repos` reads one of the indexes (see **Repository lists**) |
| `repo_totals` | PK `id` (always 1) | One row: `repos` (how many), `files` and `bytes` (the repo counters summed; a group's are 0) and `public` (the public repos that are not groups). Triggers on `repos` keep it in the transaction of every insert, delete and change of `file_count`, `total_size` or `anonymous_read`, so the counter updates of the assets triggers move it too. `GET /api/overview` reads it |
| `repo_redirects` | PK `name` (WITHOUT ROWID), idx `repo_redirects_repo (repo_id, name)`; `repo_id` cascades | A name that answers for the repo `repo_id` (see **Repository redirects**): `created_at`, `last_used_at` (unix time of the last request through it, 0 for never; written with the download counters). A name is a repo or a redirect, never both |
| `user_permissions` | PK `(user_id, perm)` (WITHOUT ROWID), idx `user_permissions_perm (perm, user_id)` | The server permissions of a person, as permission IDs (e.g. `people.manage`). No CHECK: every write checks the IDs against the permission registry, and a read skips an ID that no module registers. See **Permissions** |
| `repo_permissions` | PK `(repo_id, user_id, perm)` (WITHOUT ROWID), idx `(user_id, perm)` | The repository permissions of a person on a repo, as permission IDs (e.g. `repo.publish`). No CHECK, as above. See **Permissions** |
| `repo_group_members` | PK `(group_id, member_id)` (WITHOUT ROWID), UNIQUE `(group_id, position)`, idx `member_id` | The members of a group repo in read order (`position`). Both ids cascade with their repo, so a deleted member leaves its groups |
| `repo_proxies` | PK `repo_id` (cascades) | The remote of a proxy repo: `remote_url` (http or https, ends with `/`), `credentials` (sealed JSON `{username, password}`, AAD `repo_proxy:<repo id>`; empty for none), `metadata_ttl` (1800 s), `not_found_ttl` (300 s), `last_ok_at`, `last_error`, `last_error_at` |
| `blobs` | PK `id`, UNIQUE `sha256`, partial idx `blobs_unreferenced (unreferenced_at) WHERE refs = 0` | Content-addressed blobs, storage key `blobs/sha256/<first 2 hex>/<hex>`. `size`; `md5` and `sha1` (hex, from the spool of the first write; `''` for a synthetic row of the scale test); `refs` (the asset rows that point here; the assets triggers keep it); `unreferenced_at` (`created_at` for a new row, the time `refs` reached 0, 0 while referenced); `created_at`. One blob backs any number of assets in any repos |
| `builds` | PK `id`, UNIQUE `(provider, key)`, idx `created_at` | A CI build record (see **CI providers and build records**): `provider` (a CI provider ID, e.g. `github-actions`), `key` (the provider's unique key of the run), `repository`, `revision`, `ref`, `ref_name`, `ref_type` (`branch`, `tag` or `''`), `run`, `workflow`, `actor`, `links` (JSON `[{rel, label, url}]` that only the provider makes), `created_at` (the time of the last mint for the run) |
| `assets` | UNIQUE `(repo_id, path)`, idx `uploaded_by`, idx `assets_blob (blob_id, repo_id)`, partial idx `assets_build (build_id) WHERE build_id IS NOT NULL`, partial idx `assets_unit (repo_id, unit) WHERE unit <> ''`, partial idx `assets_unit_unset (repo_id) WHERE unit IS NULL` | Every stored file: a path that points to a blob (`blob_id`; `size`, `sha256`, `md5` and `sha1` are the blob's, copied, so a download reads no other row); `content_type` from the format's `Describe`; `kind` is `artifact`, `metadata` or `index`; `build_id` is the CI build record that wrote the file (SET NULL). Only the byte pipeline writes rows; triggers keep `blobs.refs` on every insert, delete and change of `blob_id`. A cached file of a proxy has `checked_at` (when the remote last confirmed it) and `remote_etag`; a hosted file keeps 0 and `''`. Triggers keep the repo counters in the transaction of each write: an insert adds 1 and its size, a replace adds the size change, a delete subtracts. Every asset row counts (artifacts, metadata, checksums and Debian index files). `last_published_at` is the newest `updated_at` of an insert or replace that is not an `index` row; a delete never lowers it. `unit` is the part of the repo that a group serves from one member (`format.Spec.GroupUnit`, set on insert): the package of a file of npm, PyPI, Cargo or NuGet that a group takes from the member that owns the name, `''` for a file that a group serves path by path; NULL until the job `core.group-counts` sets it (a row of the seed command) |
| `group_counts` | UNIQUE `(group_id, members)` | The counters of a group for one set of members that a reader reads: `members` is their ids in read order, comma-separated; `file_count`, `total_size`, `last_published_at`. See **Group counters** |
| `group_count_members` | PK `(count_id, position)` (WITHOUT ROWID), idx `(member_id, count_id)` | The members of a row of `group_counts` in read order; both ids cascade |
| `group_count_pending` | PK `count_id` | A row of `group_counts` that the job is still counting (cascades); a reader never reads such a row. See **Group counters** |
| `assets_search` | FTS5 trigram on `path`, contentless with deletes | The rowid is `repo_id << 40 \| id`, so the matches of one repo sit together in rowid order and a search page is one seek (see `GET /api/search`). Kept in sync by triggers; `index` rows are not indexed |
| `assets_search_vocab` | fts5vocab (row) of `assets_search` | How many paths hold each trigram: a search of a longer text picks its rarest trigrams from it |
| `repo_ops` | PK `id`, UNIQUE `repo_id` (cascades), idx `started_by`, partial idx `repo_ops_running (type) WHERE state = 'running'` | At most one operation per repo; an ended operation's row is deleted. `type` (a registered type: `delete`, `mirror`), `owner` (the module), `ref` (the owner's reference, e.g. the Nexus source ID), `state` `running`/`held`/`failed`, `done`, `total`, `error`, `started_by`, `started_at`, `updated_at` |
| `upload_sessions` | PK `id` (WITHOUT ROWID), idx `repo_id`, `user_id`, `expires_at` | A resumable upload: `id` (32 lowercase hex), `repo_id` and `user_id` cascade, `publish_session_id` (the session that started it; no foreign key: the table belongs to the publish module), `received`, `created_at`, `updated_at`, `expires_at`. The bytes are in `$TR_DATA_DIR/tmp/uploads/<id>` |
| `rebuilds` | PK `(repo_id, grp, unit)` (WITHOUT ROWID); `repo_id` cascades | The durable rebuild queue: a unit of a group of a repo that its format builds again (Debian: `grp` is the distribution, `unit` is `component/arch`). `marked_at` in unix ns |
| `settings` | PK `key` (WITHOUT ROWID), idx `updated_by` | JSON value, or base64(nonce‖AES-GCM ciphertext) when the setting's definition is secret (`settings.Def.Secret`); `updated_by` |
| `user_totp` | PK `id`, idx `user_id` | `secret` is sealed (AAD `user_totp:<id>`); `last_step` is the last accepted 30 s step of this factor (0 for none) |
| `user_passkeys` | PK `id`, UNIQUE `credential_id`, idx `user_id` | `credential` is the JSON of the `webauthn.Credential` (the public key, the sign count, the flags, the transports, the AAGUID); `credential_id` is its lookup key; `name`, `created_at`, `last_used_at` |
| `user_recovery_codes` | PK `(user_id, code_hash)` (WITHOUT ROWID) | `code_hash` is argon2id PHC; `used_at` is set when the code is used |
| `auth_challenges` | PK `id_hash` (WITHOUT ROWID), idx `expires_at`, `user_id` | Short-lived single-use state: `kind` is `login`, `totp_setup`, `webauthn_register`, `webauthn_assert` `device_grant` (an SSO grant: `user_id` set, `data` is the user code of a device approval or `setup:<repo>` for a setup code, 5 min) or `setup_code` (a setup code: `id_hash` is SHA-256 of `trsu_<32 random bytes, base64url>`, `user_id` the person who made it, `data` the repo id, 10 min; the first use deletes it); `data` holds the payload. A new challenge deletes up to 100 expired ones |
| `download_stats` | PK `(repo_id, day)` (WITHOUT ROWID), idx `day` | `day` is the Unix time of 00:00 UTC; `downloads`, `bytes`; `repo_id` CASCADE. See **Download statistics** |
| `audit_events` | idx `(repo_id, id)`, idx `(repo_id, action, id)`, idx `(action, id)`, idx `actor_user_id` | `at` unix time; `action` is a registered action ID; `actor` is a username, a CI subject, `cli`, the module ID of a job, `anonymous`, or the attempted username of a failed sign-in; `actor_user_id` and `repo_id` are SET NULL; `target`; `detail` is JSON and never holds a secret value; `ip` is the client address. This table is the only place that stores or shows an IP address. See **Audit log** |
| `download_window_state` | PK `id` (always 1) | `first_day`: the first UTC day of the download window (the last 30 days, today included). See **Download statistics** |
| `download_window_days` | PK `day` | The downloads and bytes of each day of the window, kept by the triggers of `download_stats` |
| `download_window_repos` | PK `repo_id`, idx `download_window_repos_top (downloads DESC, bytes DESC, repo_id)` | The downloads and bytes of each repo in the window, kept by the triggers of `download_stats`; `repo_id` CASCADE |

Tables of the `storage` component (`internal/move/migrations`):

| Table | Key / indexes | Notes |
|---|---|---|
| `storage_moves` | PK `id`, UNIQUE partial `storage_moves_copying (state) WHERE state = 'copying'`, idx `started_by` | `target` is the sealed JSON of the target storage settings (AAD `storage_moves:<id>`); `state` `copying`/`done`/`failed`/`cancelled`; `cursor` (last blob id done), `total` (the blob rows when the move started), `copied`, `skipped`, `failed`, `error`; `dismissed_at` (an admin put the ended move away) |
| `storage_move_failures` | PK `(move_id, blob_id)` (WITHOUT ROWID) | `key` and `reason` of each blob the copier could not copy |

Tables of the `network` component (`internal/netrules/migrations`):

| Table | Key / indexes | Notes |
|---|---|---|
| `ip_lists` | PK `id`, UNIQUE `name` | An IP list. `kind` is `preset` (the server fetches it) or `custom` (an admin edits it). `title`, `description`, `ranges` (JSON array of CIDRs), `source` (the fetch URLs of a preset), `fetched_at` (unix time), `error` (the last fetch failure, `''` after a success), `created_at` |
| `repo_ip_rules` | PK `id`, UNIQUE partial `(repo_id, scope, cidr) WHERE cidr IS NOT NULL`, UNIQUE partial `(repo_id, scope, list_id) WHERE list_id IS NOT NULL`, idx `(repo_id, scope)`, idx `list_id` | One allow-list entry. `scope` is `read` or `publish`; a rule holds a `cidr` or the `list_id` of an IP list (exactly one; `ON DELETE RESTRICT`); `note` is free text. `repo_id` cascades. A scope without rows is unrestricted |
| `trusted_proxy_lists` | PK `list_id` (`ON DELETE RESTRICT`) | The IP lists that the trusted proxies include. The `list:<name>` entries of `TR_TRUSTED_PROXIES` are resolved at startup and are not rows |

Tables of the `signing` component (`internal/signing/migrations`):

| Table | Key / indexes | Notes |
|---|---|---|
| `signing_keys` | PK `id`, UNIQUE `name` | An OpenPGP signing key: `fingerprint`, armored `public_key`, `private_key` (armored, no passphrase, sealed with the master key, AAD `signing_keys:<id>`), `created_at` |
| `repo_signing_keys` | PK `repo_id` (cascades), idx `(key_id, repo_id)` | The key of a repo (the repo field `signing_key`). `key_id` is `ON DELETE RESTRICT`, so a key in use cannot be deleted |

Tables of the `publish` component (`internal/publish/migrations`):

| Table | Key / indexes | Notes |
|---|---|---|
| `devices` | PK `id`, UNIQUE `public_key`, idx `user_id` | A device key of the Telesphorepo CLI, one per computer and server: `name`; `public_key` is the raw Ed25519 key; `expires_at`, `last_used_at`, `revoked_at` |
| `device_enrollments` | PK `device_code_hash` (WITHOUT ROWID), UNIQUE `user_code`, idx `expires_at`, idx `approved_by`, idx `device_id` | `name`, `public_key`; `polled_at` rate-limits the poll; `approved_by` and `device_id` are set on approval; a row is taken once by the poll |
| `publish_trusts` | PK `id`, UNIQUE `name`, partial idx `publish_trusts_issuer (issuer) WHERE disabled = 0`, idx `created_by` | `claims` is JSON `{claim: [allowed values]}`; `jwks` is `''` when the server fetches the issuer's keys; `audience` `''` means `base_url` |
| `publish_trust_repos` | PK `(trust_id, repo_id)` (WITHOUT ROWID), idx `repo_id` | The repos a policy may publish to |
| `publish_nonces` | PK `id_hash` (WITHOUT ROWID), idx `device_id`, idx `expires_at` | A nonce that a device signs: `id_hash` = SHA-256(nonce), `device_id` (cascades), `exchange`. With `exchange` 0 it is for one request (a mint for one repo, a project setup, or the revoke of the device), lives 60 s and that request deletes it. With `exchange` 1 it is for a format's token exchange, signed over the repo `*`, and mints until it expires (`publish.session_ttl`) |
| `publish_exchange_sessions` | PK `(nonce_hash, repo_id)` (WITHOUT ROWID), idx `repo_id`, `session_id` | The session that a token exchange nonce minted for each repo. Each column cascades |
| `publish_sessions` | PK `id`, UNIQUE `public_id`, idx `expires_at`, `repo_id`, `user_id`, `device_id`, `trust_id`, `build_id` | `secret_hash` = SHA-256(secret); `via` `device`/`oidc`/`manual`; `subject`; `build_id` (the CI build record of a CI session whose token a provider read); `closed_at`. `repo_id` cascades; `user_id`, `device_id`, `trust_id` and `build_id` are SET NULL |
| `publish_oidc_seen` | PK `(id_hash, repo_id)` (WITHOUT ROWID), idx `expires_at`, `session_id`, `repo_id` | One row per CI token and repo, with the session the token minted there. `id_hash` is SHA-256 of `<issuer>\n<jti>`, or of the whole token when it has no `jti`. A second insert for the repo means a replay |

Tables of the `edge` component (`internal/edge/migrations`). They reference no table, so an outbox
row outlives a deleted repo and still deletes its pointers (see **Edge delivery**):

| Table | Key / indexes | Notes |
|---|---|---|
| `edge_outbox` | PK `(repo_id, path)` (WITHOUT ROWID), idx `edge_outbox_due (due_at)`, partial idx `edge_outbox_backfill (repo_id) WHERE backfill = 1`, partial idx `edge_outbox_access (repo_id, due_at) WHERE repo_id = 0` (the access objects in due order) | One row per dirty object. `repo_id` > 0: `path` is an asset path whose pointer the outbox writes or deletes; `repo_id` 0: `path` is the key of an access object or `config.json`. `sha256` is the digest of the commit that wrote the row (`''`: apply the current state); `due_at` (unix ms), `attempts`, `seq` (raised by each new row of the key, so the worker drops only the row it read), `timed_out_at` (unix ms of a put with an unknown outcome, 0 for none), `error`, `backfill` (1: the backfill of every pointer queued the row; a later row of the key keeps it) |
| `edge_written` | PK `key` (WITHOUT ROWID) | The access objects and `config.json` in the home bucket: the key under `edge/v1/`, the hex SHA-256 of the JSON written, `written_at` |
| `edge_state` | PK `key` (WITHOUT ROWID) | JSON state: `home` (the location fingerprint, without the secret, that the objects were written for, the backfill cursor and counts, home ready, deploy needed and its reason, the gone regions with the end of their drain, and the drain that runs: its end, the old home location without its secret, its regional buckets and the cache time of its off switch), `region:<hint>` (bucket, ready, cleared, the backfill cursor, the blob check cursor and the blobs missing) and `ttl` (the `access_ttl` of the last `config.json` written, and until when a Worker can still hold an earlier one) |

Tables of the `nexus` component (`internal/nexus/migrations`):

| Table | Key / indexes | Notes |
|---|---|---|
| `nexus_sources` | PK `id`, UNIQUE `name`, idx `created_by`, partial idx `nexus_sources_due (last_sync_at) WHERE state = 'mirroring'` | `base_url`; `credentials` is the sealed JSON `{username, password}` (AAD `nexus_sources:<id>`), emptied by cut-over; `state` `mirroring`/`cut_over`; `last_sync_at` (unix time when the last sync ended, 0 for never); `last_error`; `cutover_sync_id` and `cutover_failed` (the cut-over sync and its failed file count, 0 before cut-over) |
| `nexus_source_repos` | PK `(source_id, source_repo)` (WITHOUT ROWID), idx `target_repo_id` | The mapping: `source_format` (Nexus format id), `kind` (`hosted`/`proxy`/`group`, the Nexus repo type) and the target repo of each Nexus repo. A target mirrors at most one source |
| `nexus_syncs` | PK `id`, UNIQUE partial `nexus_syncs_running (source_id) WHERE state = 'running'`, idx `source_id`, idx `started_by` | `state` `running`/`done`/`failed`/`cancelled`; `trigger` `save`/`manual`/`schedule`/`cutover`; `started_by` is NULL for the scheduler; `started_at`, `finished_at`, `error` |
| `nexus_sync_repos` | PK `(sync_id, source_repo)` (WITHOUT ROWID), idx `target_repo_id` | Copy of the hosted part of the mapping at sync start, with `continuation` (token of the next Nexus page), `checked` (files read from Nexus), `imported`/`skipped`/`ignored`/`failed`, `bytes` (bytes imported), `active_at` (when the runner saved the last page) and `state` `pending`/`running`/`done`/`failed`/`cancelled` |
| `nexus_sync_failures` | PK `id`, UNIQUE `(sync_id, source_repo, path)`, idx `sync_id` | One row per source file that a sync did not import |
| `nexus_sync_log` | PK `id`, idx `(sync_id, id)` | The log of a sync for the admin: `at`, `source_repo` (`''` for a line about the whole sync), `level` `info`/`warn`/`error`, `message` |
| `nexus_switch_overs` | PK `(source_id, source_repo)` (WITHOUT ROWID), FK to the mapping row (cascade) | A mapped hosted repo that switched over: `sync_id` (its final sync), `failed` (the files that sync failed to import), `switched_at`. A mapped hosted repo without a row still copies |
| `repo_rehearsals` | PK `repo_id`, idx `repo_rehearsals_for (for_repo_id)` | A rehearsal repo and the repo it rehearses (`for_repo_id`, a target of a Nexus source), `created_at`. Both cascade with their repo |
| `repo_moves` | PK `repo_id`, idx `repo_moves_into (into_repo_id)` | A moving repo (Move it project by project) and the repo it merges into (`into_repo_id`, a target of a Nexus source), `created_at`. Both cascade with their repo |
| `nexus_merges` | PK `id`, idx `nexus_merges_target (target_repo_id, id)`, idx `sync_id`, idx `started_by` | One merge of a source repo into a target (see **Nexus sources**, Gradual move): `source_repo_id` (no foreign key: the merge deletes it), `source_name` and `target_name` at the start, `keep` (`target` or `source`), `otherwise` (the choice of a conflict that no choice names), `state` (`running`, `failed`, `done`, `stopped`), `step`, `sync_id` (its final sync), `cursor` (the last source path copied), the counters `files`, `done`, `copied`, `skipped`, `rebuilt`, `kept`, `taken`, `error`, `started_by`, `started_at`, `finished_at`. Cascades with the target |
| `nexus_merge_choices` | PK `(merge_id, path)` (WITHOUT ROWID) | The choice of one listed conflict of a merge: `take` 1 deletes the target's file and takes the source's, 0 keeps the target's |

Tables of the `maven` component (`internal/maven/migrations`). Every `repo_id` cascades. Hosted
and proxy repos have rows (a proxy lists the versions it has cached); the hooks write them in the
transaction of the asset change (see **Versions and builds**):

| Table | Key / indexes | Notes |
|---|---|---|
| `maven_versions` | PK `(repo_id, folder, sort_key)` (WITHOUT ROWID), idx `maven_versions_latest (repo_id, folder, snapshot, sort_key)` | One row per version folder with a build: `folder` the artifact folder, `sort_key` the version's key (byte order is Maven order), `version`, `snapshot` (1 for a `-SNAPSHOT`), `builds` (its rows in `maven_builds`); the row goes with its last build |
| `maven_builds` | PK `(repo_id, folder, sort_key)` (WITHOUT ROWID) | One row per build of a version folder: `folder` the version folder, `sort_key` the build's key (byte order is build order), `build` the id (the version, `<yyyyMMdd.HHmmss>-<n>` or `SNAPSHOT`); the row goes with its last file |
| `maven_index_pending` | PK `one` | One row while the files that the Maven repos held before these tables are not indexed yet; the module's start indexes them and deletes it |

Tables of the `debian` component (`internal/debian/migrations`):

| Table | Key / indexes | Notes |
|---|---|---|
| `deb_packages` | UNIQUE `(repo_id, distribution, component, architecture, package, version)`, idx `asset_id`, idx `deb_packages_sha256 (repo_id, distribution, component, sha256)` | Also the streaming index for `Packages`. The SHA-256 index finds a package an import already holds. `asset_id` cascades with the pool file |
| `deb_package_order` | PK `package_id` (cascades with its `deb_packages` row), idx `deb_package_order_page (repo_id, folder, component, package, name_key, build_key)` | One row per `deb_packages` row, written with it: `folder` (of the file, e.g. `pool/main/h/hello/`) and two byte keys whose byte order is the order of the Versions and Builds pages (`internal/debian/order.go`): `name_key` the version the row is a build of in `dpkg --compare-versions` order, `build_key` the build by stamp, then in dpkg order. The SQL functions `deb_name_key` and `deb_build_key` compute them in a migration |
| `deb_index_files` | PK `(repo_id, distribution, path)` (WITHOUT ROWID) | Sizes and hashes; `Release` is built from these rows |

Tables of the `npm` component (`internal/npm/migrations`). Every `repo_id` cascades. Rows exist for
hosted repos only; the commit of a tarball writes them and its removal deletes them (see **npm
registry**):

| Table | Key / indexes | Notes |
|---|---|---|
| `npm_packages` | PK `(repo_id, name)` (WITHOUT ROWID) | `created`, `modified` (unix seconds) and `rev`, the `_rev` of the packument: `<n>-<hash of the versions, deprecations and tags>`; the row goes with the last version |
| `npm_versions` | PK `(repo_id, name, version)` (WITHOUT ROWID), idx `npm_versions_sort (repo_id, name, sort_key)`, UNIQUE `npm_versions_tarball (repo_id, tarball)` | `sort_key` (`version.SemVer.SortKey`), `tarball` (the asset path), `integrity` (`sha512-<base64>`), `shasum` (SHA-1 hex), `published_at`, `deprecated` (`''` for none), and last `manifest` (the version's package.json without `dist`), so the refresh of the package row after a change reads version, time and deprecation without the overflow pages of a large manifest |
| `npm_tags` | PK `(repo_id, name, tag)` (WITHOUT ROWID) | The dist-tags; a tag always names an existing version, and `latest` always exists |
Tables of the `nuget` component (`internal/nuget/migrations`):

| Table | Key / indexes | Notes |
|---|---|---|
| `nuget_versions` | UNIQUE `(repo_id, lower_id, sort_key)`, idx `nuget_versions_asset (asset_id)`, partial idx `nuget_versions_releases (repo_id, lower_id, sort_key) WHERE listed = 1 AND prerelease = 0` | One row per package version of a hosted repo: `package_id` as the package writes it, `version` (normalised, with metadata), `normalized` (the identity), `sort_key` (`version.NuGet.SortKey`), `semver2`, `prerelease`, `listed`, `meta` (JSON of the `.nuspec` fields), `nupkg_path`, `published_at`. Written in the commit of the `.nupkg`, deleted in its remove; `asset_id` cascades with the `.nupkg`. The UNIQUE index reads a package's versions in order; the partial index its listed releases |
| `nuget_packages` | PK `id`, UNIQUE `(repo_id, lower_id)` | One row per package of a hosted repo with a version: `lower_id` and `rev`, the revision of the package's documents: `max(rev + 1, unix ns)` on each push, removal, unlist and relist, so a package removed and pushed again never reuses one. The row goes with the last version. The UNIQUE index pages the package ids of the search |
| `nuget_package_latest` | PK `package` → `nuget_packages(id)` ON DELETE CASCADE | The `sort_key` of the newest listed version of the package per search variant, NULL when it has none: `release` (no prerelease, no SemVer 2.0), `release2` (no prerelease), `pre` (no SemVer 2.0), `pre2` (any). Written with each move of the package's `rev` |
| `nuget_packages_search` | FTS5 trigram on `lower_id`, contentless with deletes, rowid `repo_id << 40 \| id` | Kept in sync by triggers on `nuget_packages`; the substring search of package ids. A repo's packages are one rowid range |

Tables of the `pypi` component (`internal/pypi/migrations`). Only hosted repos have project and
file rows; the hooks write the rows in the transaction of the asset change (see **PyPI**):

| Table | Key / indexes | Notes |
|---|---|---|
| `pypi_projects` | PK `(repo_id, project)` (WITHOUT ROWID) | `project` is the normalised name, `display_name` the `Name` of the newest upload's metadata, `rev` the revision of the project page (`max(rev + 1, unix ns)` on each stored or removed file and removed `.metadata`); the row goes with the project's last file |
| `pypi_files` | PK `id`, UNIQUE `(repo_id, filename)`, idx `pypi_files_project (repo_id, project, sort_key)`, idx `asset_id` | One row per wheel or sdist: `version` (PEP 440 normal form), `sort_key` (`version.PEP440.SortKey`), `path`, `sha256`, `size`, `requires_python`, `metadata_sha256` (`''` without a `.metadata`), `uploaded_at`. `asset_id` cascades with the file |
| `pypi_index_revs` | PK `repo_id` (cascades) | `rev`, the revision of the project list of a hosted repo or a proxy (`max(rev + 1, unix ns)` when a project comes, goes or changes its display name, or a proxy caches its first page of a project or removes one); a repo without a row is at 0 |

Tables of the `docker` component (`internal/docker/migrations`). Every column `repo_id` cascades;
digests are the hex of a SHA-256. Only the hooks of the format write them, in the transaction of
the asset change (see **Docker registry**):

| Table | Key / indexes | Notes |
|---|---|---|
| `docker_images` | PK `(repo_id, image)` (WITHOUT ROWID), UNIQUE `docker_images_full (full_name)` | `full_name` is `<repo>/<image>` (the catalog); `manifests` counts the manifest rows of the image; the row goes with its last manifest |
| `docker_manifests` | PK `(repo_id, image, digest)` (WITHOUT ROWID), idx `docker_manifests_subject (repo_id, image, subject, digest)` | `media_type`, `artifact_type` (`artifactType`, else the config media type unless it is the OCI empty config), `config_type`, `subject` (`''` for none), `size`, `total_size` (with the config and the layers; an index: its own size), `platforms` (JSON of an index), `annotations` (JSON), `created_at` |
| `docker_refs` | PK `(repo_id, image, manifest, child)` (WITHOUT ROWID), idx `docker_refs_child (repo_id, image, child, kind)` | What a manifest references, each distinct child once: `kind` `blob` (config, layer) or `manifest` (a child of an index) |
| `docker_links` | PK `(repo_id, image, digest)` (WITHOUT ROWID), idx `docker_links_digest (repo_id, digest)`, partial idx `docker_links_unreferenced (unreferenced_at) WHERE refs = 0` | One row per blob link of an image: `size`, `refs` (the manifests of the image that use it), `unreferenced_at` (when `refs` reached 0, the insert time of a new row; 0 while used) |
| `docker_tags` | PK `(repo_id, image, tag)` (WITHOUT ROWID), idx `docker_tags_recent (repo_id, image, updated_at, tag)`, idx `docker_tags_digest (repo_id, image, digest)` | A tag and the manifest digest it names, `updated_at` |

Every query on a request path uses an index. Lists use keyset pagination (`?after=` and
`?limit=`), never `OFFSET` or `LIKE '%x%'`.

**Queries.** The kernel and each module with a schema keep their SQL in `queries/*.sql` next to a
`sqlc.yaml`. Its `schema` is the `migrations` folder of the component; a module's config lists the folder of the kernel (`../db/migrations`) and of each module whose tables it reads first (`edge` reads `../publish/migrations`), and sqlc applies the files of each folder in name order. `go tool task
gen` runs sqlc and writes the typed `dbq` package of each component. Queries that sqlc cannot
express (FTS5 `MATCH`, streaming rows with `iter.Seq2`) are hand-written in `dbq/extra.go` and
listed in its `Extra` map.

**Plan guard.** `db.CheckPlans` runs `EXPLAIN QUERY PLAN` for every query of every `db.Component`
(the kernel's `db.Core()` and the `Schema` of each module) on a database with every schema. A SCAN
of a table and any TEMP B-TREE fail. A SCAN of a partial index or a virtual table passes. A query
that must scan goes in the component's `Allow` map with a reason. An `Allow` entry for a query that
is missing or has a clean plan also fails. A query that writes (`INSERT`, `UPDATE`, `DELETE`,
`REPLACE`) a table outside its component's schema fails too: a module writes only its own tables,
and the kernel tables change only through kernel services. The kernel allows `ListSettings`,
`HasUsers`, `ListRebuildGroups` (the startup walk of the rebuild queue), and `CountRepoTotals` and
`ListMigrations` (hand-written, `schema_migrations`; both once per backup); `storage` allows
`CountBlobs` (the total of a storage move, once per move); `network` allows
`ListTrustedProxyLists` and `DeleteTrustedProxyLists` (one row per IP list at most); `edge` allows
`CountOutbox` (the outbox job counts its rows for the status page, every 5 seconds only while
the page polls it, never per request) and `SumFileCount` (the total of a backfill, once per backfill).
`TestQueryPlans` runs the guard through the hidden `dbcheck` command.

## Storage layout

| Location | Contents |
|---|---|
| `$TR_DATA_DIR/telesphorepo.db` | SQLite database (WAL) |
| `$TR_DATA_DIR/master.key` | Master key (base64, mode 0600). Generated on first start unless `TR_MASTER_KEY` is set |
| `$TR_DATA_DIR/tmp/` | Upload spool, kept on the data volume rather than tmpfs |
| `$TR_DATA_DIR/tmp/uploads/<id>` | The bytes of a resumable upload |
| `$TR_DATA_DIR/tmp/uploads/<id>.hash` | The MD5, SHA-1 and SHA-256 state of the bytes received (with a checksum of its own) |
| `$TR_DATA_DIR/tmp/plans/<heads>` | The first bytes that upload plans read, held for later plans (the index is in memory; the first held set after a start deletes the folder) |
| `$TR_DATA_DIR/blobs/` | FS blob root (default for the `fs.root` setting) |

Blob keys are the same on every backend: `blobs/sha256/<first 2 hex>/<sha256 hex>`, built with
package `path`. A blob holds the content of any number of files in any repos; the asset rows hold
the paths and the content types. S3 keys get the optional `s3.prefix` in front. The FS backend
writes a temp file, syncs it and renames it into place. A new blob whose spool (an upload in
`tmp/`) is on the volume of the FS root is not copied: the spool is synced and renamed into place
(`Adopt`); across volumes the blob is copied.

The S3 backend writes an object below 64 MiB with one `PutObject`, and from 64 MiB as a multipart
upload: parts of 16 MiB (larger for an object above 156 GiB, so it fits in 10,000 parts; every
part but the last has the same size, as R2 requires), 8 parts at once, each read from the spool at
its offset; a failed upload is aborted. Its HTTP client keeps up to 64 idle connections, fails a
request whose connection reads or writes nothing for a minute, and the retryer (3 attempts with a
backoff) has no shared retry quota, so an outage of the store does not make every later call fail
at once.
Debian pool path: `pool/{component}/{prefix}/{source}/{package}_{version-without-epoch}_{arch}.deb`.
The `{prefix}` is the first letter of the source name, or `lib` plus the 4th letter for `lib*`
sources.

## Settings model

| Layer | What | Where |
|---|---|---|
| Boot env | `TR_DATA_DIR` (`./data`, image `/data`), `TR_LISTEN` (`:8080`), `TR_MASTER_KEY` | `config.LoadBoot`. A change needs a restart |
| Bootstrap admin | `TR_BOOTSTRAP_ADMIN_USER` and `TR_BOOTSTRAP_ADMIN_PASSWORD`, set together | Used only when the users table is empty |
| Stored settings | Everything else: the settings groups of the modules | Edited in the **Settings** pages or through `/api/settings` |
| Env override | `TR_<KEY>`: dots become `_`, upper-cased (e.g. `s3.bucket` → `TR_S3_BUCKET`) | Wins over the stored value and shows as read-only "set by environment" in the UI |

Precedence is env override, then stored value, then default. A setting can carry a `Dev` value
(`settings.Def.Dev`). `telesphorepo-server dev` uses it instead of `Default`: `cookie.secure=false` and
`index.debounce=300ms` in the core.

Setting types are `string`, `bool`, `int` (a whole number, positive unless `Zero`), `size`, `duration`, `list` and `map`. A `size` is stored as an
integer number of bytes. Input (API PATCH, env) is a number with an optional unit: `B`, `KiB`,
`MiB`, `GiB`, `TiB` (binary) or `KB`, `MB`, `GB`, `TB` (decimal), case-insensitive, with optional
space and decimals (`512MiB`, `1.5 GiB`, `500MB`). A bare number is bytes. A `duration` is stored
in Go syntax (`15m`, `2160h`). Input also accepts `15 minutes`, `90 days`, `365d`, `1h30m`; the
units are `ns`, `us`, `ms`, `s`, `m`, `h`, `d` and `w`, in short or long form. A `map` is a JSON
object of string to a list of strings (in the environment too). An `image` is the bytes of an image
file, stored and set in the environment as standard base64; `GET /api/settings` gives it the value
`{set}` and never the bytes, like a secret. `GET /api/settings` gives each
size or duration item `display` (`2 GiB`, `90 days`, `1 hour 30 minutes`) and `default_display`. A
size item shows the largest binary unit that gives a whole number, else one decimal.

**Groups.** Each module owns its settings groups and registers them with `Extender.Settings`. A
`settings.Group` has an `ID`, a `Label`, the server permission `Perm` that reads and edits every
key of it, the UI `Page` that edits it (`""` shows it on Settings › General), its definitions
(`settings.Def`: key, label, help, type, default, `Dev`, `Secret`, options, `Normalize`,
`Validate`, and `Zero`, which lets a duration be 0) and three owner hooks:

- `Validate(draft, mode)`: the rules across keys. Field errors go through `draft.Fail`. It runs at
  startup on the stored values and the environment (`ModeStartup`) and on every PATCH and test
  (`ModeChange`). A stored secret that does not decrypt reaches it as `draft.Broken(key)`, with the
  default value in its place.
- `Test(ctx, draft)`: a check against the outside world (`POST /api/settings/{group}/test`).
- `Apply(ctx, call)`: runs before a PATCH of the group saves. It may take the change over: it holds
  keys back and answers the PATCH itself (the storage module answers 202 `{move}`).

The owner declares a typed handle next to each definition (`settings.String`, `Bool`, `Size`,
`Duration`, `List`, `Map`, giving a `settings.Key[T]`), and services read `Key.Get(snapshot)` or a
`settings.View` (e.g. `storage.ConfigView`, the store of a snapshot), computed once per snapshot.
The `settings` package holds no feature rules and declares only the keys of the `general` group.

| Group (owner) | Permission · page | Keys |
|---|---|---|
| general (core) | `settings.general` · General | `base_url`, `log.level`, `max_upload_size` (`2GiB`), `max_json_size` (`1MiB`), `cookie.secure`, `session_ttl` (`168h`), `index.debounce` (`2s`), `index.max_delay` (`30s`), `blobs.gc_grace` (`1h`), `upload.session_ttl` (`24h`), `tls.cert_file`, `tls.key_file` (its `Validate`: both or neither, and the pair loads), `server_name` (`""`: the UI says Telesphorepo; at most 64 characters, no control characters), `server_logo` (type `image`, none: a PNG, JPEG or WebP image, checked by its bytes, at most 256 KiB and 512 px on its long side; see **Server identity**) |
| docker (docker) | `settings.general` · General | `docker.direct_uploads` (`false`; "Expose the origin IP for Docker uploads", see **Upload host**), `docker.upload_url` (`""`; empty or an http(s) URL of a scheme and a host only: the DNS-only name or address of the server that takes the upload bytes; required when `docker.direct_uploads` is on), `docker.link_grace` (`24h`, minimum `1s`) |
| activity (audit) | `settings.general` · General | `audit.retention` (365 days), `stats.retention` (400 days) |
| storage (storage) | `storage.manage` · `/settings/storage` | `storage` (`fs`/`s3`), `fs.root` (default `blobs/` in the data dir), `s3.endpoint`, `s3.region`, `s3.bucket`, `s3.access_key_id`, `s3.secret_access_key`*, `s3.prefix`, `s3.path_style`, `s3.redirect`, `presign_ttl` |
| backup (backup) | `backup.manage` · `/settings/backups` | `backup.dir` (an absolute path; `""` turns backups off; the default is `""` in a plain build and `/backups` in the image, see **Backups**), `backup.interval` (`24h`, minimum `1m`), `backup.keep` (type `int`, `7`, at most 1000). See **Backups** |
| network (network) | `network.manage` · `/settings/proxy` | `trusted_proxies` (literal entries; the IP lists of the trusted proxies are rows, see **Network rules**) |
| edge (edge) | `edge.manage` · `/settings/edge` | `edge.enabled` (`false`; needs `storage=s3` and a `base_url` without a path), `edge.home_hint` (`""`; one of `wnam enam weur eeur apac oc`, required when edge delivery is on), `edge.regions` (`[]`; entries `<hint>=<bucket>`, each hint once, not the home hint, not the home bucket), `edge.access_ttl` (`30s`, 0 to 5 min). Its `Validate` checks the rules across keys and at startup turns edge delivery off instead of stopping the server; `Test` probes the home bucket and every regional bucket; `Apply` marks a save, which ends "deploy needed" after a storage change. See **Edge delivery** |
| publishing (publish) | `settings.general` · General | `publish.session_ttl` (`15m`, maximum `1h`), `publish.device_ttl` (90 days) |
| sso (sso) | `signin.manage` · `/settings/sign-in` | `oidc.issuer`, `oidc.client_id`, `oidc.client_secret`*, `oidc.redirect_url`, `oidc.scopes` (`openid profile email`), `oidc.session_ttl` (`12h`), `oidc.approval_max_age` (`5m`, 10 s to 1 h: the oldest sign-in that approves a device), `oidc.permission_claim` (`""`; dotted paths allowed), `oidc.permission_map` (type `map`, `{}`), `signin.password_login` (`all` or `publishers`, default `all`) |
| nexus (nexus) | `nexus.manage` · General | `nexus.sync_interval` (`15m`, minimum `1m`) |

\* secret

- **Encryption.** Secrets use AES-256-GCM with key = HKDF-SHA256(master key, info
  `telesphorepo settings v1`). The AAD is the setting key and the nonce is a random 12 bytes. All
  of this is stdlib. A definition says whether a setting is secret; the table has no flag.
- **Secrets over the API.** The API never returns a secret; it reports `{"set": bool}`. Secrets
  are never logged.
- **Live changes.** The effective settings are an immutable snapshot behind an atomic pointer,
  rebuilt on every write. Request paths never query `settings`. Subscribers (`Service.Subscribe`)
  react to changes: the log level updates and storage is hot-swapped.
- **A PATCH.** The server builds the whole new state and runs the `Validate` of every group that
  the patch names a key of, then their `Apply` hooks in group order, then saves the keys that no
  `Apply` held back, in one transaction.
- **base_url from the first sign-in.** While `base_url` holds its default (no stored value, no
  `TR_BASE_URL`) and is empty, the first full UI sign-in (`POST /api/auth/login` that starts a
  session that is not setup-only, or `POST /api/auth/login/second`) of a person who holds
  `settings.general` saves `base_url` as the origin of that request (`httpx.Origin`: the scheme
  from a trusted proxy's forwarded proto or the connection, and the `Host`). A loopback or
  unspecified host (`localhost`, `*.localhost`, `127.0.0.0/8`, `::1`, `0.0.0.0`, `::`) is never
  saved: a browser on the server or on an SSH tunnel does not use the public address.
  `settings.Service.Fill` saves it like a PATCH, under the writer lock and only while the key still
  holds its default, so a later sign-in from another address changes nothing. The settings.update
  event has that person as its actor and the detail `{keys: ["base_url"], via: "first sign-in"}`.
  A single sign-on sign-in does not set it. The web UI then offers the address in the browser when
  it differs (see **Web UI**).
- **Storage rules.** `storage` is `fs` or `s3`. With `s3` the bucket, the access key ID and the
  secret are required. An empty `s3.endpoint` means AWS: the store uses the AWS endpoint of
  `s3.region`, so the region is required then. A set endpoint must be an http(s) URL; with an
  endpoint and no region the store signs for the region `auto` (R2, SeaweedFS). A PATCH that changes
  where the blobs are runs the storage probe first (put/head/get/delete of
  `_telesphorepo/probe-<hex>`); nothing is saved if the probe fails, and a change while a move runs
  is 409. When the new settings address another location and assets exist, the server moves the
  blobs first (see **Storage move**). A change of only credentials, region, path style or redirect,
  or a change on a server without assets, is saved and swapped at once.
- **SSO rules.** SSO is on iff `oidc.issuer` is set; it then needs `oidc.client_id` and
  `oidc.client_secret` (confidential client). The redirect URL is `oidc.redirect_url`, else
  `base_url` + `/api/auth/oidc/callback`; one of them must be set. `oidc.permission_map` maps a
  value of the claim `oidc.permission_claim` to server permission IDs; a map without a claim, an
  unknown ID or a repository permission is refused. `signin.password_login=publishers` is only
  accepted while the issuer is set. A PATCH that changes the issuer, client id/secret, redirect URL or scopes
  fetches the discovery document first; on failure nothing is saved and the error is under
  `fields["oidc.issuer"]`. The issuer is only whitespace-trimmed (trailing slashes must match the
  IdP).
- **SSO startup guard.** When the stored SSO settings fail at startup (for example a client secret
  that does not decrypt after a change of the master key), the server still starts: the `sso`
  group's `Validate` returns an error that wraps `settings.ErrDegraded` and overrides
  `oidc.issuer` to `""` and `signin.password_login` to `all` in the snapshot, without saving them.
  The log says that SSO is off. The overrides stay until a PATCH of the group passes `Validate`. A
  failing value from the environment stops the server.
- **Startup validation.** Env values must be valid at startup, or the server refuses to start; a
  refusal of a group's `Validate` stops it too (e.g. `TR_STORAGE=s3` without a bucket). An invalid
  stored value is logged and ignored. A `TR_*` variable that matches no setting or boot variable
  (`TR_E2E_*` and `TR_ENGINE` excepted) is logged as a warning by name and ignored.
  `TR_E2E_PRESET_BASE_URL` is a test variable (see **Network rules**).
- **Signing keys.** Debian repos sign their indexes with the signing key of their repo field
  `signing_key` (see **Signing keys**). No setting holds a key.
- **Lost master key.** Without the master key the secret settings cannot be decrypted and must be
  entered again.

## HTTP surface

Each format mounts its routes through `format.Spec.Routes`, or the standard routes
(`format.Standard`) when it sets none: GET and HEAD `/<format>/{repo}/{path...}` through the read
path and PUT through `Commit` with source `put` (see **Byte pipeline**). The core names no format.
Format routes (package clients use HTTP Basic). A **read** takes a password, or any username
with a read token as the password. A GET or HEAD read also takes the UI session cookie, so a
signed-in browser downloads from a private repo without a Basic prompt. A **write** (PUT or upload) takes only a publish session token
(`trs_...`) as the password (see **Publishing**); the session cookie never writes. In the table, "write" means this rule:

| Route | Auth | Behaviour |
|---|---|---|
| `GET /healthz` | none | `ok` when the DB answers |
| `GET, HEAD, PUT /maven/{repo}/{path...}` | read / write | PUT: 201 new, 200 overwrite or identical retry, 409 changed release, 413 over `max_upload_size`. Mutable paths are `maven-metadata.xml*` and anything under a `-SNAPSHOT` dir. GET on S3 with `s3.redirect` answers 302 to a presigned URL. HEAD is never redirected |
| `PUT, POST /debian/{repo}/upload/{dist}/{component}` | write | Raw `.deb` body. 201 `{package,version,architecture,filename,sha256}`, 200 identical, 409 conflict. With `?upload=<id>&sha256=<hex>[&size=<n>]` and no body, it finishes the resumable upload `<id>` of the same publish session (`/api/repos/{name}/uploads`) and adds its file: the same answers, 404 for an unknown, expired or foreign upload, 400 when the bytes do not match (the upload is deleted) |
| `GET, HEAD /debian/{repo}/{path...}` | read | `dists/...` and `pool/...` through the read path |
| `GET, HEAD, PUT /raw/{repo}/{path...}` | read / write | PUT: 201 new path; 200 identical bytes (any policy) or a replace when `overwrite` is `allow`; 409 different bytes when `overwrite` is `deny` (default); 413 over `max_upload_size`. Repo config: `{"overwrite":"deny"}` or `{"overwrite":"allow"}`. S3 redirect as for Maven |
| `GET, HEAD /debian/{repo}/key.asc` | read | The armored public signing key of the repo (its repo field `signing_key`). Same read gate as the repo's other files |
| `/v2/...`, `GET, POST /docker/token`, `GET /docker/hosts` | see **Docker registry** | The OCI registry of the docker format |
| `GET, HEAD, PUT, DELETE /npm/{repo}/...` | see **npm registry** | The npm registry API of the npm format |
| `/nuget/{repo}/...` | see **NuGet repositories** | The NuGet V3 resources of the nuget format |
| `GET /api/docker/{name}/manifest?path=` | `repo.download` (password, read token, UI session, anonymous on a public repo) | The details of the manifest at a tag or manifest path of a docker repo (a group: of the first readable member that stores it): `{digest, media_type, artifact_type, size, total_size, config: {digest, media_type, size}\|null, layers: [{digest, media_type, size}], platforms: [{digest, os, architecture, variant}], subject\|null, tags: [first 100], referrers: n}`. A link path or another path is 400; a file that does not parse is 422 |
| `GET /install.sh`, `GET /install.ps1` | none | The install script of the Telesphorepo CLI (see **Install scripts and CLI downloads**), `text/plain`. The publish module mounts them. 404 problem `no-cli-downloads` on a build without CLI archives; 400 when the server address holds a quote, a space, a backslash or a control character |
| `GET /cli/telesphorepo_<os>_<arch>.<ext>`, `GET /cli/SHA256SUMS` | none | A CLI archive of the server's build (`.tar.gz`, `.zip` on Windows) and the SHA-256 of each, `Cache-Control: no-cache`. 404 problem `no-cli-downloads` on a build without CLI archives; another name is 404 |
| `/` | none | Embedded UI, hash-routed (see **Web UI**). `/assets/...` has `Cache-Control: public, max-age=31536000, immutable`; `index.html` and a path without a file extension (the SPA fallback to `index.html`) have `no-cache`; another path with an extension that is not a file is 404 |

A read of a proxy or a group repo on a format route follows **Group and proxy repositories**. A
write (Maven PUT, Debian upload, manual upload, upload plan) to a proxy or a group answers 405 with
`Allow: GET, HEAD` and "A proxy is read-only." or "A group is read-only. Publish to one of its
repositories." to a caller who can read the repo; any other caller gets the usual refusal.
`GET /debian/{proxy}/key.asc` answers 404: a Debian proxy has no key of its own.

The format routes use `route.FormatRead` and `route.FormatWrite` (see **Route policy** and
**Access engine**): anonymous read is allowed when `anonymous_read` is on. Otherwise no
credentials or bad credentials get 401 with a Basic challenge, valid credentials without
permission get 403, and an unknown repo gets 404 (401 for anonymous callers, so repo names don't
leak). A write to a repo with an active operation gets 409 `{"error": <the operation's message>}`
after the permission check. A mirrored repo is read only until its switch-over. A write without an open publish
session for that repo gets 401 with a Basic challenge and
`{"error":"publishing needs a publish session: <base_url>/#/settings/tokens"}`. A device or manual
session writes only while its user still holds `repo.publish` on the repo (else 403).

Every download (`assets.Reader.ServeAsset`, on every format route) carries `X-Checksum-SHA256`,
and `X-Checksum-SHA1` and `X-Checksum-MD5` when the asset row has them (a synthetic row of the
scale test has none); a merged group file carries all three of the merged bytes. Nothing hashes
on a request. A request for one byte range (`Range: bytes=a-b`, `a-` or `-n`, with a matching
`If-Range`) answers 206 with `Content-Range` on every store (`storage.Store.GetRange`: a seek on
fs, a `Range` request on S3); a range outside the file is 416; several ranges get the whole file.

The listener speaks HTTP/1.1 and HTTP/2: over TLS by ALPN, and on a plain listener with prior
knowledge (h2c, for a reverse proxy that speaks it; stdlib `http.Server.Protocols`). When
`tls.cert_file` and `tls.key_file` are set at startup, the listener serves TLS (1.2 or later); the
server loads the PEM pair for a handshake and keeps it until a path or the modification time of a
file changes, so a renewed certificate applies without a restart. Turning TLS on or off applies at
the next start. There is no HTTP/3.

A 401 carries `WWW-Authenticate` only on a format route, so package clients get their prompt. It
never carries it for a path under `/api`, nor for a request with the header `X-Telesphorepo-UI: 1`
(the web UI sends it on every request). So a browser never opens its password dialog for the UI.

JSON API (`/api/`). Auth is a session cookie or HTTP Basic; each route's policy says which (see
**Route policy**). Every route that changes state (POST, PUT, PATCH, DELETE) and every permission
route (GET too) needs the UI session cookie, and the router enforces it at startup. HTTP
Basic with a password or a read token reaches only the GET routes that need no permission;
elsewhere it gets 401 with the detail `sign in through the web UI for this action`, as does a request
with no credential. The routes that take no credential or their own token are the exceptions
(sign-in, `/api/auth/config`, `/api/formats`, and the publish routes marked "none" or "token" in
**Publishing**). A signed-in user without the permission gets 403. A token of a registered kind (a read token
`trp_...` or a publish session token `trs_...`) is read from the Basic password or from
`Authorization: Bearer <token>`. A publish session token reaches only the routes whose policy
accepts it: `DELETE /api/publish/sessions/{id}`, `POST /api/repos/{name}/upload`,
`POST /api/repos/{name}/upload/plan`, the resumable upload routes `/api/repos/{name}/uploads...` and
`POST /api/repos/{name}/files/delete` and `.../files/delete/plan`, and only for its own repo. Every other `/api` route answers it with 403. Errors are problem documents (see **Problem
documents**), with the extension member `fields` for settings validation. Bodies are capped at
`max_json_size`. Lists are `{items, next}` with `?after=&limit=`.

**Compression.** Every route under `/api/` answers with `Vary: Accept-Encoding`. A JSON answer
(`application/json` or a `+json` type such as a problem document) of 1 KiB or more goes out with
`Content-Encoding: gzip` (stdlib `compress/gzip`, without `Content-Length` and `Accept-Ranges`)
when `Accept-Encoding` accepts gzip with a q above 0 (`httpx.GzipJSON`, which the router puts
around each `/api/` route). The answer stays as it is for HEAD, for a request with `Range`, for
204 and 304, for an answer that sets `Content-Encoding` itself or has another content type (a
download), for a body below 1 KiB, and for an answer that flushes before its body reaches 1 KiB.
The access log records the status and the bytes sent, so a compressed answer logs its compressed
size.

**Problem documents.** Every error answer under `/api/` and `/cli/`, and of `/install.sh` and
`/install.ps1`, is an RFC 9457 problem document (`Content-Type: application/problem+json`,
`Cache-Control: no-store`): `{type, title, detail, status, remedy?}` and the extension members of
the answer (for example `fields`, `deleted`, `received`, `used_by`, `repos` or `ok`). A member of an
extension never replaces a standard member. `httpx.Error` and `route.Fail` write a document of type
`about:blank` with the status text as its title and the message as its detail. A problem type that
has a stable name is declared once with `httpx.DeclareProblem` (`Name`, `Status`, `Title`,
`Remedy`); its type is `urn:telesphorepo:problem:<name>`, and a name declared twice panics at
startup. `remedy` is `{action, url?, after?, file?, version?}`; `action` is one of `update_cli`,
`update_server`, `login`, `setup`, `retry`, `open_url` and `fix_project`. The declared types:

| Type | Status | Title | Remedy |
|---|---|---|---|
| `rate-limited` | 429 | Too many attempts | `retry`, `after` from `Retry-After`. Every 429 under `/api/` is this type |
| `setup-code-used` | 401 | This setup line is already used | `open_url` to the Publish tab of the repo of the code (`<base>/#/repo/<repo>`), or the server root when the code is unknown or used. `POST /api/devices/setup` answers it for a wrong, used or expired code |
| `no-cli-downloads` | 404 | This build has no CLI downloads | none. `/cli/`, `/install.sh` and `/install.ps1` of a build without CLI archives |

Format routes keep the error form of their protocol: `{"error": "..."}` with the same extension
members, or the OCI errors of Docker (`Policy.Errors`). The web client (`web/src/lib/api/client.ts`) shows `detail`, else `title`.

**Change log.** `changes.toml` at the repo root is the change log that the server and the CLI
both embed. Each `[[change]]` entry has `version` (a release version such as `v0.6.0`, or `dev`
before a release), `scope` (a command such as `publish`, a command and a format such as
`publish/npm`, or a helper such as `helper/docker`; a scope matches itself and every scope under
it), `needs` (`server` or `cli`: the side that must be at least `version`; an unknown value counts
as `cli`) and `summary` (one sentence for the person), and an optional `id` (a-z, 0-9 and `-`,
unique) that names the entry in the CLI's `olderPaths` and stays when the version is stamped. A
field that a reader does not know is ignored. A `dev` entry counts for nothing. A broken change log stops the server at startup.
`go tool task tag <version>` writes the release version into each `dev` entry, commits the change
log and creates the tag; `release` and `cli` refuse a release version while a `dev` entry is left.
A release version is `MAJOR.MINOR.PATCH` with an optional `v`, pre-release (`-rc.1`) and
build data (`+x`), compared as semantic versions.

**Server version.** Every answer carries the header `Telesphorepo-Version` with the version that
the build stamped into the server binary (`-X main.version`, `server.Options.Version`), or `dev`
for a build without a stamp.

**Bulk actions.** A write route that changes a set takes a list, and one item is a list of one.
So each change has one route. A list holds 1 to 1000 items (400 outside that), and
`max_json_size` applies too. The server removes repeated items. Each route checks every item
before it changes any: an unknown name gives 404 with the name (`"repository x does not exist"`,
`"user x does not exist"`), and nothing changes. The changes to rows apply in one transaction, so
a failure leaves nothing half done. Each changed item gets its own audit event; an item that
already had the value gets none. The routes are `PATCH /api/repos`, `POST /api/repos/delete`,
`POST /api/repos/{name}/files/delete`, `PATCH /api/users` and `PUT /api/grants`. A repo delete and a
file delete remove files in batches (see **Byte pipeline**), so they are the routes that are not one
transaction.

| Method & path | Permission | Notes |
|---|---|---|
| `POST /api/auth/login` · `POST /api/auth/logout` | none · session | Login `{username, password}`. A person who needs the second factor (see **Permissions**) and has one gets `{second_factor: true, methods: [...]}` and the cookie `tr_login`; one without a factor gets `{session: <session view>}` with `second_factor_required: true` and a setup-only session. Anyone else gets `{session: <session view>}` and a full session after the password alone (see **Auth model**). The first full session of a person who holds `settings.general` on a server without `base_url` saves it (see **Settings model**). 401 for SSO users; 403 `password sign-in is off for people who manage; sign in with single sign-on` for a person who needs the second factor when `signin.password_login=publishers`. Logout ends the local session only |
| `POST /api/auth/login/passkey` | `tr_login` cookie | Returns `{publicKey: {...}}`, the WebAuthn assertion options for the pending sign-in |
| `POST /api/auth/login/second` | `tr_login` cookie | `{totp_code}`, `{passkey: <assertion>}` or `{recovery_code}`. 200 sets `tr_session` and returns `{session: <session view>}` with `second_factor_required: false`. It may save `base_url` like the password step. 401 for a wrong proof (the sign-in stays pending) or an expired sign-in; 429 with `Retry-After` after too many failures |
| `GET /api/security` | UI session | `{totp: [{id, name, created_at}], passkeys: [{id, name, created_at, last_used_at}], recovery_codes: <unused count>, second_factor: bool}` |
| `POST /api/security/totp/begin` · `.../totp/confirm` · `DELETE .../totp/{id}` | UI session | `{name}` returns `{setup_id, secret, otpauth_url}` (the secret is shown once; the setup lasts 10 min). `{setup_id, code}` returns 201 `{id, name, created_at}`. Delete is 204 |
| `POST /api/security/passkeys/begin` · `.../passkeys/finish` · `.../passkeys/assert` · `DELETE .../passkeys/{id}` | UI session | `begin` returns `{publicKey}` creation options. `finish` `{name, credential}` returns 201 `{id, name, created_at, last_used_at}`. `assert` returns `{publicKey}` options for a fresh check before an approval or a manual session. 409 when `base_url` is empty |
| `POST /api/security/recovery-codes` | UI session | 201 `{codes: [10 x "abcd-efgh-ijkl-mnop"]}`; replaces the old set. Shown once |
| `GET /api/auth/config` | none | `{oidc, password_login: "all"\|"publishers", second_factor_optional}`. `second_factor_optional` is true only under `telesphorepo-server dev` |
| `GET /api/auth/oidc/login` | none | 404 when SSO is off, 503 when discovery fails. Stores state/nonce/PKCE verifier in memory (10 min, single use, keyed by SHA-256(state)), sets `tr_oidc` (HttpOnly, SameSite=Lax, Path `/api/auth/oidc`, 600 s), 302 to the IdP |
| `GET /api/auth/oidc/device?code=` | UI session of an SSO account | 404 when SSO is off. Starts a new sign-in that approves the device of `code` (see **Publishing**). It stores the state like `login`, plus the user id and the user code, and sends `prompt=login` and `max_age=0`. 302 `/#/device?code=` without an error when there is no UI session or the code is unknown, expired or used (the page says why), and with `error=not_sso` for a local account or `sso_failed` when discovery fails |
| `GET /api/auth/oidc/callback` | none | 400 for missing/mismatched/unknown/reused/expired state, state from before an issuer or client change, or missing `code`; 503 on discovery failure. Verifies the ID token (signature, issuer, audience, expiry, nonce). Success: upsert user, set its server permissions when `oidc.permission_claim` is set, session with `oidc.session_ttl`, 302 `/`. Failure: 302 `/#/login?error=` `username_conflict` \| `invalid_username` \| `account_disabled` \| `sso_failed`. For a sign-in that `device` started, it starts no session and checks the same token, then that the token's issuer and `sub` belong to the account that started it, and that `auth_time` is present and at most 5 minutes old. Success: 302 `/#/device?code=<code>&grant=<grant>`. Failure: 302 `/#/device?code=<code>&error=` `wrong_account` \| `no_auth_time` \| `stale_sign_in` (the provider ignored `prompt=login`) \| `account_disabled` \| `sso_failed` |
| `GET /api/me` | password, read token, UI session (setup-only too) | The session view, the same shape as `session` of a finished sign-in: `{id, username, kind, email, disabled, created_at, permissions, permissions_managed, all_repos, repo_permissions, second_factor_required}`. User JSON elsewhere is `{id, username, permissions, permissions_managed, disabled, kind, email, created_at}`: `permissions` lists the server permissions, `kind` is `local`/`oidc`, `email` is `""` when unset. `repo_permissions: {repo: [perms]}` is always present (one query of the person's grants joined to `repos`): every repo where the person has a grant, `repo.download` included (`{}` for none, and for a holder of `repositories.all`, whose `all_repos` is true). `second_factor_required` is true only for a setup-only UI session. `permissions_managed` is true for an SSO account while `oidc.permission_claim` is set: single sign-on sets its server permissions at each sign-in (see **Single sign-on**) |
| `GET /api/tokens` · `POST /api/tokens`, `DELETE /api/tokens/{id}` | any credential · UI session | **Read tokens.** The token is shown once. A token cannot mint tokens. POST is 403 for SSO users |
| `GET/POST /api/users`, `GET/PATCH /api/users/{username}` | `people.manage` (a UI session may change its own password) | The user list is paginated. `GET /api/users/{username}` returns one account (User JSON), 404 when no account has the name. POST takes `{username, password, permissions}`; the caller gives only server permissions they hold (403). PATCH takes `{password}`; the caller changes the password of another person only when they hold every permission of that person (403) |
| `GET /api/administrators` | `people.manage` | `{usernames: [...]}`: the enabled people who hold every server permission, sorted. The UI keeps the only one from losing the role or being disabled |
| `PUT /api/users/{username}/permissions` | `people.manage` | `{permissions: [...]}` (server permission IDs) replaces the server permissions and returns the User. 400 for an unknown ID, a repository permission or a broken need. The caller adds or removes only permissions they hold (403). 409 when no enabled person would keep `people.manage`, 409 when the change takes a server permission from the only enabled administrator (a server where nobody holds every server permission refuses nothing here), and 409 `single sign-on manages the permissions of this person` for an account whose permissions single sign-on manages. One `user.permissions` event with `{old, new}` |
| `GET /api/people?q=&after=&limit=` | `people.manage`, or `repo.access` on a repo | `{items: [{username}], next}`: the enabled usernames that start with `q`, at most 20 per page. The UI draws the avatar from the username |
| `PATCH /api/users` | `people.manage` | `{usernames: [...], disabled: bool}`. The one way to disable or enable people. 400 `You cannot disable your own account.` when the list holds the caller and `disabled` is true. 403 for a person whose permissions the caller does not all hold. 409 when no enabled person would keep `people.manage`, or when it disables the only enabled administrator. A disabled person's sessions end, and their password and read tokens stop working. One `user.update` event per person whose value changed. 200 `{changed: n}` |
| `GET /api/permissions` | none | The permission registry: `{subsystems: [{id, scope: "server"|"repo", label, help, module, permissions: [{id, label, help, needs, second_factor, all_repos}]}]}`, subsystems in module start order and permissions in registration order. See **Permissions** |
| `GET /api/changes?since=<version>` | none | `{version, changes: [{id?, version, scope, needs, summary}]}`: the version of the server, and the released entries of the embedded change log that are newer than `since`, in the order of the file. Without `since`, every released entry. 400 when `since` is not a release version (see **Change log**) |
| `GET /api/identity` | none | The identity of the server (see **Server identity**): `{name, logo, customized}`. `name` is `server_name`, or `Telesphorepo` when it is empty; `logo` is the path of the logo with its version (`/identity/logo?v=<hash>`), or null; `customized` is true when either is set |
| `GET /identity/logo` | none | The logo: its media type (`image/png`, `image/jpeg` or `image/webp`), the ETag of its SHA-256, `Cache-Control: public, no-cache`, `X-Content-Type-Options: nosniff` and a sandbox CSP; 304 for a matching `If-None-Match`; 404 without a logo |
| `PUT /api/identity/logo` · `DELETE /api/identity/logo` | `settings.general` | PUT takes the image file as the body and saves it as `server_logo`; the type comes from the bytes, never from a header or a name (an SVG is 400), a body over 256 KiB is 413, an image over 512 px on its long side is 400 with `fields`. DELETE removes it. Each saves like `PATCH /api/settings` (one `settings.update` event; 409 when `TR_SERVER_LOGO` sets it) and answers the identity |
| `GET /api/formats` | none | The UI descriptor of every format, in module order (see **Format UI descriptors**): `[{name, title, description, icon?, kind_icons?, kinds, config: {default, fields}, repo_fields, repo_tabs, upload, delete_notes, file_details, browse: {versions, artifact_snippets}, remote_presets, proxy_note?, public_key?, labels: {artifact_kinds, artifact_name, tools, install_heading?, build_files}, import_sources, tools: [{id, label, setup, project?}], setup}]` |
| `GET /api/repos`, `GET /api/repos/{name}` · `POST /api/repos` · `PATCH /api/repos/{name}` | any/anon · `repositories.all` · `repo.settings` | The list is `{items: [Repo], next}`, a keyset page by name (`?after=<name>&limit=`, 50 by default, at most 200) of the repos where the caller holds a permission and the public ones (see **Repository lists**). `?q=` keeps the names that start with it, `?format=` one format, `?kind=` one kind (400 for another word), `?perm=a,b` the repos where the caller holds one of those repository permissions (400 for an unknown one). `?sort=published` answers the `limit` repos (at most 50) published last, newest first, with `perm` only (400 with `after`, `q`, `format` or `kind`) and `next` null. `?name=a&name=b` (at most 200) answers those of the repos that the caller sees, by name, `next` null. A list item has no `config`. Repo JSON has `permissions`, the caller's repository permissions on the repo. Repo JSON has `file_count`, `total_size` and `last_published_at` (null before the first publish), read from the repo row (see `assets` in **Data model**); a group's counters count each file it serves to the caller once (see **Group counters**), with `counts_pending: true` and 0 and null while the server has not counted the caller's set of members yet. A list item has `downloads`: the downloads of the repo in the last 30 days, today included (UTC days, the window of the Overview's Downloads card), read for the repos of the page only (one seek of the `download_stats` key per repo); it lags the counters in memory by up to a minute (see **Download statistics**). `format` must be a registered name. `description` (POST and PATCH) has at most 500 characters and no control characters except newline and tab. `config` must be a JSON object that the format validates (400 otherwise; an unknown format lists the available ones). An empty config becomes the format default. Repo JSON has `operation`: `{id, type, owner, state, done, total, message, error}` of the repo's active operation, or null (see **Repo operations**). Repo JSON has `fields`: the values of the module fields of the repo (e.g. `{signing_key: "default"}`; `{}` for a format without fields). POST and PATCH take `fields` too; POST gives a field without a value its default, and a field that does not apply to the format and kind is 400. `PATCH /api/repos/{name}` takes `{description?, config?, proxy?, fields?}` and is one repo update (`repos.Service.Update`): the whole new state is checked first, then saved in one transaction with the field values, the format's `OnRepoChange` and one `repo.update` event with the changed fields. Repo JSON has `kind`, and a group has `member_count`. POST takes `kind` (`hosted` default, `proxy`, `group`), `members: [names]` for a group, `proxy: {remote_url, username?, password?, metadata_ttl?, not_found_ttl?}` (seconds) for a proxy, and `force`. A new proxy's remote is checked first (see **Group and proxy repositories**): a 502 problem document when it does not answer, unless `force: true`. PATCH takes `proxy` with the same fields for a proxy; `username` and `password` together replace the credentials, `username: ""` removes them, and the password is never returned. `GET /api/repos/{name}` adds, for a group, `members: [{name, format, kind, public, permissions}]` (the members the caller can read, in order, each with the caller's repository permissions on it); for a hosted or proxy repo, `groups: [names]` (the groups it is in that the caller can read); for a proxy and a holder of `repo.settings`, `proxy: {remote_url, has_credentials, metadata_ttl, not_found_ttl, last_ok_at\|null, last_error, last_error_at\|null}` |
| `GET /api/overview` | a UI session | The figures of the Settings overview, each a counter or a bounded query: `{repos: {all, count, files, size}, largest: [Repo], public_repos, people?: {count, more}}`. `repos` totals every repo for a holder of `repositories.all` (`all: true`, from `repo_totals`), else the repos where the caller holds a repository permission other than `repo.download` (one lookup per grant). `largest` is up to eight of those repos that store something, the largest first (`repos (total_size, id)`). `public_repos` counts the public repos that are not groups (`repo_totals`). `people` (a holder of `people.manage`) counts the accounts up to 10,000 (`more: true` past it) |
| `PUT /api/repos/{name}/members` | `repo.settings` | `{members: [names]}` replaces the members of a group and their order; returns the repo. 400 for 0 or more than 50 members, a member of another format, a group, a name listed twice, an unknown name, or a repo that is not a group |
| `POST /api/repos/{name}/proxy/test` | `repo.settings` | Checks the remote of a proxy now: 200 `{ok: true}`, or a 502 problem document with the extension member `ok: false` and the reason as its detail. It records the result as the proxy's last answer or last error |
| `PATCH /api/repos` | `repo.settings` on each repo | `{names: [...], anonymous_read: bool}`. The one way to make repos public or private: one repo update of every repo in one transaction. One `repo.update` event per repo whose value changed. 200 `{changed: n}` |
| `POST /api/repos/delete` | `repositories.all` | `{names: [...]}`. Checks that every repo exists (404) and that none has an operation (409 `You cannot delete <name> now. <message>`); then nothing is deleted. Then every repo gets its `delete` operation in one transaction (409 that names the repo when one got an operation meanwhile; then none starts), and each delete runs as a task of the core module (see **Repo operations**). 202 `{deleting: [{name, operation}]}` in the order of names, each with its running operation. `GET /api/repos/{name}` shows the progress in `operation` (`done` of `total` files) until it answers 404; a failed delete keeps a failed `delete` operation. One `repo.delete` event per repo, with the number of files |
| `POST /api/repos/{name}/rename` | `repositories.all` | `{name}` renames the repo (`repos.Service.Rename`) and returns the repo; `{name}` in the path may be the repo's own name or a redirect of it. The old name becomes a redirect (see **Repository redirects**). 400 for a name that `NameRE` or the format refuses, 409 for the name of another repo or of a redirect of another repo (the message names the repo it redirects to). A redirect of the same repo may be taken back: it goes, and the name the repo had becomes the redirect. One `repo.rename` event with `detail.renamed_from` |
| `GET /api/repos/{name}/redirects` · `DELETE /api/repos/{name}/redirects/{redirect}` | any repository permission · `repositories.all` | GET lists the redirects of the repo by name: `[{name, created_at, last_used_at\|null}]`; `last_used_at` includes the uses of the last minute that are still in memory. DELETE removes one: 204, 404 when the repo has no such redirect. The name is free again and answers as any unknown repo. One `repo.redirect.remove` event with `detail.redirect` |
| `POST /api/repos/{name}/operation/resume` · `DELETE /api/repos/{name}/operation` | `repositories.all` | Resume runs the failed operation of the repo again: 200 `{ok: true}` (a delete runs on as a task until the repo is gone). Clear ends it, and the repo keeps the files that are left: 204. 409 when the repo has no operation, when it has not failed, or when its type cannot resume or clear here (a Nexus mirror ends at the cut-over). Records `repo.delete.resume` or `repo.delete.clear` |
| `GET /api/repos/{name}/grants` | `repo.access` | `[{username, perms: [...]}]` by username; `perms` are permission IDs in registry order |
| `GET /api/repos/{name}/access?admins=1` | a repository permission that manages the repo (one with a second-factor need: `repo.delete`, `repo.settings`, `repo.access`, `ci.pipelines`) | The access report, built from the parts of `access.Engine.Decide`: `{repo, format, kind, people, download: {public, people, admins, guards, sentence, notes}, publish: {people, admins, ci, guards, blocked, sentence}}`. `people` are `{username, perms, via}` (`permission`; with `admins=1` the holders of `repositories.all` too, via `all_repos`). `guards` maps a guard name to `{restricted, summary, detail}`; the network guard's `detail` is `{any, lists: [{name, title}], cidrs: [{cidr, note}]}`. `blocked` is null, or `{code: "read_only_kind" \| "operation", message}`: a proxy or a group (it lists no publisher and no CI pipeline) or a locked repo. Each `sentence` says the answer in words, e.g. "2 people, only from 10.0.0.0/8." A blocked publish sentence is "nobody." followed by the message. `notes` are the sentences that modules add (`access.Engine.AddDownloadNote`): while edge delivery is on, "Downloads come from the edge." or "Downloads go to the server: read rules apply." |
| `GET /api/users/{username}/access` · `GET /api/me/access` | `people.manage` · password, read token or UI session | `{username, permissions, disabled, kind, can_publish, items: [{repo, format, public, perms, via}], next}`, a keyset page by repo name: with `repositories.all` of every repo, otherwise of the repos with a grant of the person and the public ones, read as one page of each side (`repos_anonymous_name` and the person's grants), never the repos in between. On a proxy or a group a person holds no `repo.publish` and no `ci.pipelines` |
| `PUT /api/grants` | `repo.access` on each repo | `{users: [...], repos: [...], perms: [...]}`. Sets the repository permission set of every pair of user and repo, at most 1000 pairs; `perms: []` removes the pairs. 400 for an unknown permission ID, a server permission, or a permission without one it needs (e.g. `repo.publish` without `repo.download`, `ci.pipelines` without `repo.publish`); 404 for an unknown user or repo. 403 when the caller adds or removes a permission they do not hold on that repo. The auth service records one `grant.set` event with `{old, new}` per pair whose set changed, in the transaction. 200 `{changed: n}` |
| `GET /api/repos/{name}/assets?prefix=&after=&limit=` | `repo.download` | `{items: [{path, size, sha256, content_type, kind, updated_at, repo?}], next}`, ordered by path; the prefix is a range scan. A group merges the pages of the members that the caller reads, each read from the same cursor: a path comes from the first member that has it, named in `repo` |
| `GET /api/repos/{name}/tree?path=&after=&limit=` | `repo.download` | `{items: [{name, type: "dir"\|"file", path, size?, sha256?, updated_at?, uploaded_by?, build?}], next}`. `build` is the CI build record that wrote the file (see **CI providers and build records**). `path` is a folder (`""` or ending in `/`). A folder item's `path` and the `next` cursor end in `/`. `uploaded_by` is a username. A group answers the merged listing of its members (see **Files, uploads and deletes**), and each file item has `repo`, the member that serves it |
| `GET /api/repos/{name}/file?path=` | `repo.download` | One file with its details: `{name, path, size, sha256, content_type, kind, created_at, updated_at, uploaded_by, build}`. 404 when the path holds no file. `build` is the CI build record, or null. A group answers the file of the first member in read order that the caller reads and that stores it, with `repo`, that member |
| `POST /api/repos/{name}/files/delete/plan` | as the delete | `{paths?, prefixes?}` as the delete. Deletes nothing. 200 `{files, more, notes: [{text, count, one, many}]}`: the files that exist (a folder walk reads at most 100,000 files; `more` says it stopped), and for each `delete_notes` entry of the format the count of the files its glob matches (entries without a match are left out). It walks `assets` by the `(repo_id, path)` index |
| `POST /api/repos/{name}/files/delete` | `repo.delete`, with a UI session or a device or manual publish session of the repo | `{paths?: [...], prefixes?: [...]}`: files and folders, 1 to 1000 items together. 400 with the first bad path or prefix; then nothing is deleted. 200 `{deleted: n}`, the number of files. A file that is gone already counts as done; an empty folder adds 0. 409 on a repo with an operation. 401 for a password or a read token; 403 without `repo.delete` (a CI session never deletes). One `file.delete` event per file and per folder that lost files |
| `POST /api/repos/{name}/upload/plan` | publish session of the repo | What an upload of files would store; stores nothing. Multipart: the part `plan` first, `{fields, folder, files: [{path, size}], heads?}` (1 to 500 files, paths relative to the drop), then one part per listed file that a `reads` rule of the format matches, its `filename` the path, holding at most the rule's `max_bytes` from the start of the file. 200 `Plan` = `{items: [{title, files: [{path, target}], facts?, problems}], ignored: [paths], fields: [{key, label, help?, type, ..., value, read?, problem?}], heads}`. The server holds the parts it read under `heads` (32 hex; `""` when it holds none) for 30 minutes after the last use, for the same publish session and repo, at most 1 GiB of every held set together (the least recently used set goes first): a plan that names `heads` needs no part of a file that the set holds at the same size, and the server adds its new parts to the set. Such a plan that lacks the first bytes of a file the format reads answers 409 `plan-heads-gone`, and the client sends them again. 400 for an unknown field, a bad path, a head that no rule reads or that is longer than it allows. See **Files, uploads and deletes** |
| `POST /api/repos/{name}/upload` | publish session of the repo | Multipart: the fields first, then the text part `uploads` (optional), a JSON list `[{path, id, size}]` of files that went through the resumable upload routes with the same publish session, then the files, each with its relative path as its `filename`. The server finishes each named upload with its size (no digest; 404 for an unknown upload, 400 for another size, which deletes it) and takes it as the file at its path. The files and the finished uploads together may not exceed `max_upload_size`. The server runs the format's `Upload.Plan` on the files (the first bytes of each file that a `reads` rule matches): 400 when a field or an item has a problem or no item takes a file. Then `Upload.Store` commits the files of the items through the byte pipeline (source `ui`; checksums and metadata with source `generated`). 201 `{files: [{path, status: "created"\|"replaced"\|"unchanged", size, sha256, generated?}]}`. 400 or 409 for a refusal of the format |
| `POST /api/repos/{name}/uploads` | publish session of the repo | Starts a resumable upload: 201 `{id, received: 0, expires_at}` |
| `PATCH /api/repos/{name}/uploads/{id}` | the publish session that started it | Appends the body at the offset of `Content-Range: bytes <from>-<to>/*` (without the header: at the end). 202 `{received}`; 416 `{error, received}` when `from` is not the bytes received; 413 above `max_upload_size`; 404 for an unknown, expired or foreign upload. The answers carry `Range: bytes=0-<received-1>` |
| `PUT /api/repos/{name}/uploads/{id}?path=&sha256=&size=` | the publish session that started it | Finishes the upload: the server reads the file once for its hashes, checks `sha256` (and `size` when given), then commits it at `path` with source `put`: the answers of a format PUT (201, 200, the format's refusal). 400 when the bytes do not match; the upload is deleted. A Debian repo refuses every path (400): `PUT /debian/{repo}/upload/{dist}/{component}?upload=<id>&sha256=` finishes the upload of a package |
| `DELETE /api/repos/{name}/uploads/{id}` | the publish session that started it | Deletes the upload and its bytes: 204 |
| `GET, PUT /api/repos/{name}/ip-rules` | `repo.settings` | `{read: [Rule], publish: [Rule]}` with `Rule` = `{kind: "cidr"\|"list", value, note}`. PUT replaces both lists and returns them in canonical form. 400 for an invalid rule or a `list` that does not exist. See **Network rules** |
| `GET /api/ip-lists` | `network.manage`, or `repo.settings` on a repo | `[IPList]` without `ranges`: presets first, then by name. `IPList` = `{id, name, kind: "preset"\|"custom", title, description, count, ranges?, source, fetched_at\|null, error, created_at, used_by: {repos: [{repo, scope}], trusted_proxies}}` |
| `GET /api/ip-lists/{name}` | `network.manage` | The `IPList` with `ranges`. 404 when unknown |
| `POST /api/ip-lists` | `network.manage` | `{name, title?, description?, ranges?}`. 201 `IPList`. 400 for invalid input, 409 when the name is taken (preset names too) |
| `PUT /api/ip-lists/{name}` | `network.manage` | `{name?, title?, description?, ranges?}`; a missing field keeps its value. 200 `IPList`. Rules and trusted proxies name the list by its id, so a rename keeps them. 400 for a preset, 404 when unknown, 409 when the new name is taken or `TR_TRUSTED_PROXIES` names the list |
| `DELETE /api/ip-lists/{name}` | `network.manage` | 204. 409 `{error, used_by}` while in use, 400 for a preset |
| `POST /api/ip-lists/{name}/refresh` | `network.manage` | Fetches the preset now and returns the `IPList`. 400 for a custom list, 404 when unknown, 502 `{error}` when the fetch fails (the last good list stays) |
| `POST /api/ip-ranges/parse` | `network.manage` | `{text}` (at most 1 MiB). Stores nothing. 200 `{ranges: [CIDR], invalid: [{line, text, reason}], duplicates: n}`. 400 for longer text or more than 100,000 ranges. See **IP lists** |
| `GET /api/signing-keys` | `signing.manage`, or `repo.settings` on a repo | `[{name, fingerprint, public_key, created_at, error?, repos: [name]}]` by name |
| `POST /api/signing-keys` | `signing.manage` | `{name}` creates an Ed25519 key. `{name, armored, passphrase?}` imports a key. 201 with the key. 400 for a bad name or key, 409 when the name is taken |
| `GET /api/signing-keys/{name}` | `signing.manage` | The key with its `repos`. 404 when unknown |
| `DELETE /api/signing-keys/{name}` | `signing.manage` | 204. 409 `{error, repos}` while repos use the key |
| `GET /api/signing-keys/{name}/public.asc` | `signing.manage` | `application/pgp-keys`, saved as `<name>.asc` |
| `GET /api/network/proxy-candidates` | `network.manage` | `[{address, last_seen, count, header}]`, newest first: the untrusted TCP peers that sent a forwarded header. See **Network rules** |
| `GET /api/audit?after=&limit=&action=&repo=&actor=&since=&until=` | `activity.read` | `{items: [{id, at, actor, actor_user_id\|null, action, repo, target, detail, ip}], next}`, newest first. `repo` is a repo name, `actor` a username, `since` and `until` RFC 3339 (see **Audit log**, Filters) |
| `GET /api/audit/actions` | `activity.read` | `[{id, label, subsystem}]`: every action that a module registers, in module start order. The Activity filter reads it |
| `GET /api/search?q=&repo=&after=&limit=` | any/anon | FTS5 trigram (`assets_search`), `q` ≥ 3 chars, only repos where the caller holds `repo.download` and that its guards let it read. `{items: [{repo, path, size, sha256, kind, updated_at}], next, repos?}`: the hits of one repo come together, in the order the repo stored them, and the repos in the order they were created. A page is one seek past `after` (the index key of the last hit), and a repo that the caller cannot read is one seek past its end, so every page is full until the end and costs the same at any size. The first page (no `after`) has `repos: [{name, format, matches, more}]`: each repo with matches, in the same order, its matches counted up to 1,000 (`more`: the count stopped there); the count seeks from one repo with matches to the next. `q` of 3 characters, or with a character outside ASCII, is one trigram phrase; a longer `q` matches its 3 rarest trigrams (their path counts from `assets_search_vocab`) and keeps the paths that hold `q`, so the cost of a search follows its rarest trigram, not its common ones |
| `GET /api/repos/{name}/status` | `repo.download` | `{pending}`: the units of the repo that wait for a rebuild of its indexes (`rebuilds`), the ones being rebuilt included; 0 for a format without a `Rebuild` |
| `GET /api/repos/{name}/usage` | `repo.download` | `{base_url, tools: [{id, label}], snippets: [Snippet]}`: the format's `Usage` (use, a computer and by hand), then for a hosted repo the CI file of each of the format's tools for GitHub Actions and Jenkins (default trigger). A proxy or a group gets no publish snippets. `Snippet` = `{id, tool, purpose: "use"\|"publish", context: "repo"\|"artifact", via?: "computer"\|"ci"\|"hand", file?, title, lang, code, note?}`. `tools` names each tool of the snippets with its label (the format's `SnippetTools`, the CI provider titles). `base_url` is the `base_url` setting, else the request scheme and host |
| `GET /api/repos/{name}/ci?system=github-actions\|jenkins&tool=&branches=&tags=` | `repo.download` | The CI file that publishes to a hosted repo (one `Snippet`; the whole file in `code`, and for `github-actions` and `jenkins` the change to an existing file in `diff`), built by `internal/ci/workflow.go` from the `PublishSteps` of the tool (a tool of the format; default the first). `branches` and `tags` are comma-separated names or globs (letters, digits and `. _ / * -`, at most 20 each): the GitHub Actions trigger (default: a push to `main`), and a `when { anyOf { branch; tag } }` on the Jenkins publish stage (none by default). 400 for another system, an unknown tool, a bad pattern, or a proxy or a group |
| `GET /api/repos/{name}/versions?path=&after=&limit=` | `repo.download` | The versions of the artifact folder `path`, newest first (`format.Spec.Versions`): `{artifact: {path, title, kind}\|null, items: [{version, kind, folder, latest_build, latest_label, builds_count, published_at, build, remote}], next}`. `artifact` is null for any other folder. `published_at` is null for a version without a date (a proxy's version whose remote document names none); `remote` is true for a proxy's version that its remote lists and whose files the proxy has not cached yet. `folder` is the folder that holds every file of the version and no file of another (Maven `com/acme/lib/1.0/`, NuGet `v3-flatcontainer/<id>/<version>/`); absent when the version shares its folder (Debian, npm, Cargo, PyPI, Docker). `version` of `/builds` has it too. `latest_label` is the format's name of the latest build for people (Maven: "Build 3"); the UI shows `latest_build` when it is absent. A group merges its members (see **Group versions**): each item has `repo`, the member that serves it, and `next` is a group cursor. 400 for a cursor that does not decode. See **Versions and builds** |
| `GET /api/repos/{name}/builds?path=&version=&after=&limit=&build=` | `repo.download` | The builds of one version of the artifact folder `path`, newest first: `{artifact, version, items: [{id, label, files: [{name, path, size, sha256, classifier, extension, repo, remote}], published_at, build}], next}`. In a group each file has `repo`, the member that serves it; `remote` marks a proxy's file that it has not cached yet (`size` 0 and `sha256` empty when the remote document does not name them). `label` is the format's name of the build for people (Maven: "Build 3", "Build without a timestamp"); the UI shows `id` when it is absent. `artifact` and `version` are null when the artifact has no such version. `build=<id>` answers that one build in place of a page: `items` holds it, or nothing when the version has no such build, and `next` is null (`after` and `limit` do not apply); `version.latest_build` tells whether it is the latest. A group answers the builds that a group read serves for the version, `version.repo` the first member that has it (see **Group versions**). 400 without `version` |
| `GET /api/repos/{name}/snippets?path=&version=&version_mode=pinned\|latest` | `repo.download` | The snippets that use one artifact (`format.Spec.ArtifactUsage`): `{base_url, artifact, version, version_mode, fixed_version, tools, snippets}`. `path` is an artifact folder (with an optional `version`), a version folder or a file. A group answers the snippets of the group, with the artifact read from the first member in read order that the caller reads and that answers for the path |
| `GET /api/settings` | the permission of a settings group | `{groups: [{id, label, module, perm, page, settings: [Item]}]}`: the groups whose permission the caller holds, in module start order. An Item has key, env, group, label, help, type, options, secret (from the definition), value, default, display, default_display, source (`stored`/`env`/`default`), locked |
| `PATCH /api/settings` | the permission of each key's group | `{key: value}`. Each key needs the permission of its group (403 otherwise). `null` resets a key; `""` for a secret means unchanged. The server runs the `Validate` of every touched group, then each `Apply`, then saves (see **Settings model**). Returns 400 `{error, fields}`, or 409 when an env-locked key gets a different value. Location changes must pass the probe; SSO connection changes must pass discovery. A location change that starts a storage move saves the other keys of the patch and returns 202 `{move: Move}`; 409 while a move runs. The settings service records one `settings.update` event that names the saved keys, never the values; a patch that saves no key records none |
| `GET /api/backup` | `backup.manage` | `{job: {running, started_at, progress: {step, blobs, copied} | null, last_at, last_error}, next_at, backups: [{name, created_at, complete, version, storage, size, database_size, files, blobs_included, master_key_included}], error}`: the folders of `backup.dir`, newest first (`next_at` and the list are empty while backups are off). `size` is the database and the blobs it uses; `database_size` is the database file, all that a backup of an s3 server holds. `job` is the state of this process: `last_*` are lost at a restart. `error` says why the folder could not be read |
| `POST /api/backup/run` | `backup.manage` | Starts a backup now: 202 with the `job`; 409 while one runs; 400 without `backup.dir` |
| `GET /api/storage/moves/current` | `storage.manage` | The newest Move, running or ended; 404 when there has been none or the newest one is dismissed |
| `GET /api/storage/moves/{id}` | `storage.manage` | `Move` = `{id, state, total, copied, skipped, failed, error, started_at, finished_at, dismissed_at, target: {storage, endpoint?, bucket?, prefix?, fs_root?}, target_settings, progress_at|null, save_every}`. `progress_at` is the copier's last save of the counters in this server process (null when no copier of this process runs the move); it saves at most every `save_every` seconds, when a file in id order is done, so a large file holds the counters until it is copied. `target_settings` are the storage settings the move switches to, by setting key (`storage`, `fs.root`, `s3.*` but `presign_ttl`), with `s3.secret_access_key` as `{set}`. Never holds secrets |
| `GET /api/storage/moves/{id}/failures?after=&limit=` | `storage.manage` | `{items: [{blob_id, key, reason}], next}` in blob id order |
| `DELETE /api/storage/moves/{id}` | `storage.manage` | Cancels the move and returns it; 409 when it has ended |
| `POST /api/storage/moves/{id}/retry` | `storage.manage` | Tries the newest move again when it failed or was cancelled (409 otherwise). The body `{key: value}` holds storage settings that change the target (`{}` keeps it; a secret left out or `""` keeps the target's secret; a key of another group is 400). The server builds the settings PATCH that makes the sealed target the active storage, puts the body on top and saves it like `PATCH /api/settings`: 202 `{move}` with the new move, or 200 `{move: null}` when the settings saved at once; a refusal answers like the PATCH. The old move is dismissed. Records `storage.move.retry` |
| `POST /api/storage/moves/{id}/test` | `storage.manage` | The same body as the retry; runs the storage `Validate` and the probe on the target a retry would move to, saves nothing. Answers like `POST /api/settings/storage/test` |
| `POST /api/storage/moves/{id}/dismiss` | `storage.manage` | Dismisses an ended move and returns it; storage stays where it is. 409 while it copies. Records `storage.move.dismiss` |
| `GET /api/edge/status` | `edge.manage` | `{enabled, mode: "on"\|"draining"\|"off", unfit, server_version, worker_version\|null, worker_outdated, gap_checked_at\|null, gap_check_every, blob_check_every, deploy_needed, deploy_reason, worker_missing, check_unfit, last_check\|null, home: {hint, bucket, outbox, pointers, last_error}, backfill: {running, queued, total}, regions: [{hint, area, bucket, state: "filling"\|"ready"\|"waiting", pointers_written, pointers_total, pointers_done, blobs_missing\|null, blobs_checked_at\|null, outbox, last_error}]}`. `gap_check_every` is the interval of the Worker's gap check (its cron, 600 s) and `blob_check_every` the interval of the server's count of the files of a filling region (120 s), in seconds; `blobs_checked_at` is the end of the region's last whole count. `home.outbox` counts every outbox row, `home.pointers` and each region's `outbox` the pointer rows; `pointers_done`: every pointer of the region is written, so the blob check runs. `check_unfit` says why `POST /api/edge/check` cannot run (`""` when it can); `last_check` is the answer of the last check since the server started. `backfill.running` holds until the outbox wrote every pointer of the backfill (`home.ready`). The Worker's build (`worker_version`), its features and the gap check come from `gap/state.json`; `worker_outdated` says why the deployed Worker sends every request to the server (`""` when it serves; see **The contract**), and then `deploy_needed` holds; `waiting`: the Worker of the last gap check has no binding for the region, or its binding is a bucket whose `bucket.json` names another bucket; `deploy_needed` also holds when the `bucket.json` of its `HOME` names another bucket than the home bucket. The outbox counts come from the count of the outbox job: every 5 s while the page asks for the status at least every 30 s; after a longer pause the request asks for a count and waits for it up to a second. `worker_missing` is the detail of the `no-edge-worker` problem of `GET /api/edge/worker.zip`, `""` when the build holds the Worker of the server's version. See **Edge delivery** |
| `POST /api/edge/check` | `edge.manage` | Check now: the deployed Worker runs its gap check at once (see **The check of the Worker** under **Edge delivery**). 200 `{at, outcome: "worker"\|"refused"\|"server"\|"other"\|"unreachable"\|"error", ok, message, worker_version?}`; `ok` when the Worker of the server's version answered and ran the check. 409 problem `edge-not-configured` without storage s3 or without a base URL without a path; 429 problem `edge-check-busy` (with `Retry-After`) while a check runs or within 10 s of the start of the last one. No audit event: the check changes nothing on the server, and the page runs it every 30 s while the Worker has not reported this server's version |
| `GET /.well-known/telesphorepo-edge/check?n=<nonce>` | public | The reserved path of the check. On the server it answers that the request reached the origin: `Telesphorepo-Edge-Check: server`, 200 `{answered_by: "server"}` for the nonce of the check that runs, else 403. The Worker answers the same path on its route (see **Edge delivery**) |
| `GET /api/edge/speedtest` | `edge.manage` | The files of the speed test (see **The speed test** under **Edge delivery**): `{base_url, unfit, files: [{repo, format, path, size, url}]}`. `base_url` is the scheme and host of the base URL (`""` when unset); `unfit` says why no pass can be issued (as `check_unfit`). `files`: the immutable files of the public, unguarded hosted and proxy repos whose format has `Edge` (the first 16 such repos, the first 500 files of each in path order), the two largest of each repo, at most 8, largest first; `url` is the download path in the URL shape of the format (`/<format>/<repo>/<path>`; on `oci` the blob or the manifest by digest) |
| `POST /api/edge/speedtest` | `edge.manage` | Issues a pass of the speed test: writes `speedtest/<hex SHA-256 of the token>.json` to the home bucket and answers 200 `{token, expires_at}` (a random 256-bit token, base64url; 10 minutes). 409 problem `edge-not-configured` as `POST /api/edge/check`; 502 when the home bucket does not take the write. It deletes the pass objects older than 11 minutes after the write. No audit event: a pass changes nothing on the server and grants no access |
| `GET /api/edge/worker.zip` | `edge.manage` | The edge Worker folder of the server's build, `application/zip`, `attachment; filename="telesphorepo-edge_<version>.zip"`, `no-store`: every file of `internal/edgedist` under `telesphorepo-edge/`, plus `telesphorepo-edge/wrangler.jsonc` for the current settings with `"main": "dist/index.js"` and `"no_bundle": true`. 409 problem `edge-not-configured` as below; 404 problem `no-edge-worker` when the build holds no Worker, or one of another version than the server |
| `GET /api/edge/wrangler.jsonc` | `edge.manage` | The `wrangler.jsonc` of the Worker for the current settings, for a checkout of `edge/` (`"main": "src/index.ts"` and the `define`), `text/plain`, `no-store`. 409 problem `edge-not-configured` when the settings cannot run edge delivery (storage fs, a base URL with a path, no home hint) |
| `POST /api/nexus/sources` | `nexus.manage` | `{name, base_url, username, password}`; `name` matches the repo name pattern. Checks the connection. 201 `Source`. 400 for invalid input or refused credentials, 409 when the name exists, 502 when Nexus is unreachable |
| `GET /api/nexus/sources?after=<name>&limit=` | `nexus.manage` | `{items: [Source], next}` in name order |
| `GET /api/nexus/sources/{id}` | `nexus.manage` | `SourceDetail`. After cut-over, or when Nexus is unreachable, it lists only the mapped repos and `nexus_error` says why |
| `PATCH /api/nexus/sources/{id}` | `nexus.manage` | `{base_url?, username?, password?}`. Checks the connection. Returns the `Source`. 409 after cut-over |
| `PUT /api/nexus/sources/{id}/repos` | `nexus.manage` | `{repos: [Mapping]}` replaces the whole mapping and, when it holds a repo, starts a sync (`trigger: save`). Returns the `SourceDetail` with `running_sync_id`. 400 for an invalid mapping, 409 while a sync runs, after cut-over, or when a target already mirrors another source |
| `POST /api/nexus/sources/{id}/sync` | `nexus.manage` | 202 `Sync` with its `repos` (`trigger: manual`). 400 without mapped repos, 409 while a sync runs or after cut-over |
| `POST /api/nexus/sources/{id}/cutover` | `nexus.manage` | Switch over everything: 202 `Sync` with `trigger: cutover` of every mapped hosted repo that still copies. 409 while a sync runs or after cut-over |
| `POST /api/nexus/sources/{id}/repos/{repo}/switch-over` | `nexus.manage` | Switches over the repo that Nexus repo `{repo}` copies into: 202 `Sync` with `trigger: cutover` of the Nexus repos that copy into it. 400 for a proxy or a group, 404 for a repo that is not mapped, 409 while a sync runs, after cut-over, or when it switched over already |
| `POST /api/nexus/sources/{id}/repos/{repo}/rehearsal` | `nexus.manage` | Creates the rehearsal repo of the repo that Nexus repo `{repo}` copies into: 201 `{name}`. 400 for a proxy or a group, 404 for a repo that is not mapped, 409 `{repo, rehearsal}` when the name is taken (`rehearsal` is true when that repo is its rehearsal already) |
| `POST /api/nexus/rehearsals/{name}/empty` | `nexus.manage` | Starts the `empty` operation of rehearsal repo `{name}`: 202 `{name, files}` (the file count when it started). 400 for a repo that rehearses nothing, 404, 409 while the repo has an operation |
| `POST /api/nexus/sources/{id}/repos/{repo}/move` | `nexus.manage` | `{name, group}`: creates the moving repo `name` of the repo that Nexus repo `{repo}` copies into, and with `group` the group `<target>-all`: 201 `{name, group}` (`group` "" for none). 400 for a proxy, a group, a bad name or `group` on a format without groups, 409 `{repo, rehearsal: false}` when a name is taken |
| `GET /api/nexus/merges/plan?from=&into=` | `nexus.manage` and `repositories.all` | The `MergePlan` of a merge of `from` into `into` (`into` empty: the repo that `from` moves into). It reads every file of `from` and writes nothing. 400 when the two cannot merge, 404 |
| `POST /api/nexus/merges` | `nexus.manage` and `repositories.all` | `{from, into, keep, choices, otherwise}`: starts a merge after the plan of now: 202 `Merge`. 400 for a blocked file or a conflict without a choice, 409 while an operation other than the target's `mirror` holds either repo |
| `GET /api/nexus/merges/{id}` | `nexus.manage` and `repositories.all` | One `Merge` |
| `GET /api/nexus/sources/{id}/syncs?after=<sync id>&limit=` | `nexus.manage` | `{items: [Sync without repos], next}`, newest first |
| `GET /api/nexus/syncs/{id}` | `nexus.manage` | `Sync` with its `repos` |
| `GET /api/nexus/syncs/{id}/failures?after=&limit=` | `nexus.manage` | `{items: [Failure], next}` in failure order |
| `GET /api/nexus/syncs/{id}/log?after=<line id>&limit=&repo=` | `nexus.manage` | `{items: [LogLine], next}`, oldest first. `repo` keeps the lines of one Nexus repo |
| `DELETE /api/nexus/syncs/{id}` | `nexus.manage` | Cancels the sync and returns it (200); 409 when it has ended |
| `DELETE /api/nexus/sources/{id}` | `nexus.manage` | 204. 409 while a sync runs or while the source mirrors mapped repos (allowed after cut-over, or without a mapping) |
| `POST /api/settings/{group}/test` | the permission of the group | Same body as the PATCH, merged over the current values and not saved. Runs the group's `Validate`, then its `Test` (`storage`: the probe of the store; `sso`: the discovery check; `edge`: the probe of the home bucket and of each regional bucket, a failed region under `fields["edge.regions.<hint>"]`). Returns `{ok:true}`, or 400 `{ok:false, error, fields?}`; 404 for an unknown group |

A permission in these tables means the UI session of a person who holds it (see
**Permissions**); `repositories.all` holds every repository permission. "UI session" means the `tr_session`
cookie; the session of a person who needs the second factor exists only after it (outside dev
mode). The publish routes are in **Publishing**.

## Client setup

The UI shows these snippets per repo (`/usage`), and `telesphorepo-server dev` prints them. `HOST` is the server URL. For reads, credentials are a user and password, or any username with a
read token as the password. Uploads use a publish session token; the Telesphorepo CLI (see
**Publishing**) gets it itself.

No snippet puts a token in a process argument. A curl snippet pipes a curl config
(`user = "publish:$TOKEN"`, `header = ...`) to `curl -K -` with the shell builtin `printf`, and
sends a JSON body that holds a token with `--data @-`. A snippet for a person reads a token at a
hidden prompt (`read -rs TOKEN`), so the token stays out of the shell history; wget uses
`--ask-password`. No CI file fetches, prints or passes a token: the CLI and its credential helper
read the identity token of the job themselves.
The apt credentials file gets mode 600 (`sudo install -m 600 /dev/null FILE`) before the token
is written to it with `sudo tee -a FILE > /dev/null`.

| Client | Setup |
|---|---|
| Maven | URL `HOST/maven/{repo}` in `<repositories>`. Reads of a private repo need `~/.m2/settings.xml` `<server>` with the same `<id>`. Deploys: `telesphorepo publish`. Maven 3.8.1+ blocks `http://` on non-localhost hosts |
| Gradle | `maven { url = uri("HOST/maven/{repo}") }` in `repositories` of the build file to consume dependencies (Gradle ignores the repositories of the settings file when the build file declares its own), and in `pluginManagement.repositories` of the settings file to consume plugins; add `credentials(PasswordCredentials::class)` with a read token in `~/.gradle/gradle.properties` to read a private repo. Publishes: `telesphorepo publish` (a project that applies `maven-publish`). `isAllowInsecureProtocol = true` only for plain http |
| apt | Key: `curl -fsSL HOST/debian/{repo}/key.asc -o /etc/apt/keyrings/telesphorepo.asc`. Source: `deb [signed-by=/etc/apt/keyrings/telesphorepo.asc] HOST/debian/{repo} {dist} {component}`. For a private repo, the `curl` command reads `USER:READ_TOKEN` from a config on stdin, and `/etc/apt/auth.conf.d/{repo}.conf` holds `machine HOST/debian/{repo} login USER password TOKEN` (mode 600) |
| Upload `.deb` | `telesphorepo publish x.deb` (with `dist` and `component` in `telesphorepo.toml`). Indexes rebuild after `index.debounce`; poll `GET /api/repos/{repo}/status` for `pending: 0` |
| Docker | Pull: `docker pull <host>/{repo}/IMAGE:TAG`; a private repo needs `docker login <host>` with a read token. Push: the setup line of the Publish tab adds the credential helper of the CLI to the Docker config once, then `docker push <host>/{repo}/IMAGE:TAG` with no `docker login`. Helm and ORAS use the same helper (see **Docker registry** and **Publishing**) |
| NuGet | Source `HOST/nuget/{repo}/index.json` in `nuget.config` (`allowInsecureConnections="true"` for plain http); a private repo adds `packageSourceCredentials` with the user and a read token as `ClearTextPassword` (dotnet sends them after the first 401). Publishes: `telesphorepo publish` in a .NET project, or `dotnet nuget push` with a publish session as the API key |
| pip, uv, poetry | Index `HOST/pypi/{repo}/simple/`: `index-url` in `pip.conf` (a private repo: `USER:READ_TOKEN@` in the URL; a server on http: `trusted-host`, since pip ignores an http index on any host but localhost), a `[[tool.uv.index]]` with `default = true` (a private repo: `UV_INDEX_<NAME>_USERNAME` and `_PASSWORD`), `poetry source add --priority=primary` (a private repo: `poetry config http-basic.<repo>`). A hosted repo holds only its own packages; a group of it and a PyPI proxy also serves their dependencies. Publish: `telesphorepo publish` |
| Raw | Upload: `telesphorepo publish file.tar.gz` (with `path` in `telesphorepo.toml`). Download: `curl -fLO HOST/raw/{repo}/path/file.tar.gz`; a private repo adds `-K -` with `user = "USER:READ_TOKEN"` on stdin |
| npm, pnpm, bun | `.npmrc` of the project: `registry=HOST/npm/{repo}/`; a private repo adds `//HOST-without-scheme/npm/{repo}/:_authToken=${NPM_TOKEN}` with a read token in `NPM_TOKEN`. Publish: `telesphorepo publish` |
| Yarn 2+ | `.yarnrc.yml`: `npmRegistryServer: "HOST/npm/{repo}/"`; a private repo adds `npmAlwaysAuth: true` and `npmAuthToken: "${NPM_TOKEN}"` (Yarn sends no token on reads without `npmAlwaysAuth`); a plain-HTTP server adds its host name to `unsafeHttpWhitelist`; a hosted repo adds `npmMinimalAgeGate: 0` (Yarn 4 installs a version only once it is a week old), and the note of a proxy or group says to list your own packages in `npmPreapprovedPackages` |

### Install scripts and CLI downloads

The server serves the Telesphorepo CLI of its own build. `go tool task build` (six targets),
`go tool task dev` (the target of the computer), `go tool task cli` and `go tool task release`
compile `./cmd/telesphorepo` into `internal/clidist/dist` before they compile the server, as
`telesphorepo_<os>_<arch>.tar.gz` (`.zip` on Windows) for linux, darwin and windows on amd64 and
arm64, with `SHA256SUMS`. The image build runs `go tool task cli` too. Without `cmd/telesphorepo`
the task writes no archives and says so; that server answers `/cli/`, `/install.sh` and
`/install.ps1` with the problem `no-cli-downloads` (404).

```sh
curl -fsSL HOST/install.sh | sh [-s -- CLI ARGS...]
```

```powershell
& ([scriptblock]::Create((irm HOST/install.ps1))) [CLI ARGS...]
```

The server writes `HOST` (the `base_url` setting, else the scheme and host of the request) and the
SHA-256 of each archive into the script (`internal/publish/install.sh`, `install.ps1`). The script
picks the archive for the OS and the CPU, downloads it from `HOST/cli/`, and refuses it, with no
change, when its SHA-256 differs. `install.sh` (POSIX sh, Linux and macOS) installs into
`~/.local/bin`; `install.ps1` (PowerShell 5.1 and later, and PowerShell on Linux and macOS)
installs into `%LOCALAPPDATA%\Programs\telesphorepo` and adds that folder to the user `PATH`
(`~/.local/bin` on Linux and macOS). Next to the CLI, the script makes
`docker-credential-telesphorepo`: a symbolic link on Linux and macOS, a hard link on Windows, a
copy when a link fails. On Windows a running `telesphorepo.exe` is renamed to
`telesphorepo.exe.old` first. In GitHub Actions the script appends the folder to `$GITHUB_PATH`.
Arguments after the script go to the CLI, which runs once the install is done; `install.sh` gives
it the terminal (`/dev/tty`) when there is one, because the script itself came on stdin.

**PATH.** Outside GitHub Actions, when `~/.local/bin` is not on `PATH`, `install.sh` picks the
start-up file of the shell that `$SHELL` names:

| `$SHELL` | File | Line |
|---|---|---|
| zsh | `~/.zprofile` on macOS, `~/.zshrc` on Linux | `export PATH="$HOME/.local/bin:$PATH"` |
| bash | `~/.bash_profile` on macOS, `~/.bashrc` on Linux | the same |
| fish | `~/.config/fish/config.fish` | `set -gx PATH $HOME/.local/bin $PATH` |
| any other, or unset | `~/.profile` | `export PATH="$HOME/.local/bin:$PATH"` |

A file that already holds the line gets nothing: the script says to open a new terminal or to
run the line now. On a terminal (`/dev/tty`) it asks `Add ~/.local/bin to your PATH in <file>?
[Y/n]`; yes (or Enter) appends the line, creating the file and its folder when needed, and the
script says to open a new terminal or to run the line now. No, or no terminal (CI, a pipe
without a terminal), prints `~/.local/bin is not on PATH. Add this line to <file>, then open a
new terminal:` and the line. `install.ps1` puts the folder on `$env:PATH` of the session, so the
PowerShell session that ran it can run `telesphorepo` at once. On Windows it adds the folder to
the user `PATH` and says that a new terminal is needed elsewhere; on Linux and macOS, when the
folder is not on `PATH` and there is no `$GITHUB_PATH`, it prints the line for the PowerShell
profile (`$env:PATH = "$HOME/.local/bin:$env:PATH"`).

The web UI page **Install the CLI** (`/#/install`, see **Web UI**) shows the install lines, the
archives with their SHA-256 and the commands to install one by hand.

## Auth model

| Credential | Storage | Accepted on |
|---|---|---|
| Password | argon2id (64 MiB, t=3, p=2), at most 4 hashes at once; successful Basic checks are cached for 10 min under an HMAC key | Sign-in (`POST /api/auth/login`). As Basic auth: format reads, and the `/api/` GET routes that need no permission |
| Read token `trp_<12hex>_<secret>` | SHA-256(secret), looked up by id, constant-time compare | Basic password (any username) or `Authorization: Bearer`: format reads, and the `/api/` GET routes that need no permission |
| Session `tr_session` | SHA-256(id), absolute `session_ttl` | `/api/`, and format reads (GET and HEAD) for downloads in the browser. The only credential for permission routes and for routes that change state on `/api/`; it never writes to a format route. HttpOnly, SameSite=Strict, Secure per `cookie.secure`. The session of a person who holds `delete` may also delete files (see **Files, uploads and deletes**) |
| Publish session `trs_<12hex>_<secret>` | SHA-256(secret) in `publish_sessions`, constant-time compare | Basic password (any username) or `Authorization: Bearer` on format routes of its one repo, `/api/publish/...`, and the upload and delete routes of its repo. The only credential that writes to a format route |
| Device (`auth.CredDevice`) | The enrolled device key; a device proof `trx.<device id>.<nonce>.<signature>` over a token exchange nonce, as the Basic password or the Bearer value of a format read, or inside a core bearer token of a format's token exchange (the Docker credential helper) | Format reads (GET and HEAD) with the grants of the device's user, and writes through the publish session that the device minted for the repo (the bearer scope) |

- **Account kinds.** `local` users have a password, API tokens and Basic auth; publishers and the
  break-glass admin. `oidc` users are created on their first SSO login, keyed by
  `(oidc_issuer, oidc_subject)` (see **Single sign-on**). They have no usable password, cannot
  create tokens, get 401 on Basic auth and password login, and cannot have their password changed
  (403, also `user passwd`). Their session only comes from SSO. They approve a device with a new
  SSO sign-in (see **Publishing**) and publish through that device or from CI; they have no manual
  publish session.
- **Single sign-on.** SSO is a sign-in method for any person at the provider; a first sign-in
  creates an `oidc` account with no server permission. The username is `preferred_username`
  lower-cased with chars outside `[a-z0-9._-]` mapped to `-`; if a different account owns it the
  login fails (`username_conflict`); accounts are never merged by name. A disabled account gets
  `account_disabled`. With `oidc.permission_claim` set, SSO **manages** the server permissions of
  every `oidc` account: each sign-in sets them to the union of `oidc.permission_map` over the
  claim's values (the ID token, then UserInfo; a string or a list; a dotted path such as
  `realm_access.roles`), and records `user.permissions` with the actor kind `sso` when they change.
  A change that would leave no enabled person with `people.manage`, or take a server permission
  from the only enabled administrator, keeps the old permissions and is
  logged. Nobody can change managed permissions on the server (409); the session view and User
  JSON say `permissions_managed: true`, and the UI shows them read-only. Repo grants stay local for
  every account. With no claim, people manage the permissions on the server, and a sign-in never
  changes them.
- **Second factor.** A local user may register several TOTP factors (SHA-1, 6 digits, 30 s, +-1
  step; a step that a factor accepted is refused again), several passkeys (RP ID is the host of
  `base_url`, the one accepted origin is its scheme and host; a sign count that does not go up
  passes, because a security key keeps one counter per protocol and a phone may use the other
  over NFC, and the stored count keeps its highest value; the server logs `passkey rejected` with
  go-webauthn's reason for every failed check) and
  10 one-time recovery codes (80 bits each, argon2id-hashed). Recovery codes work only at
  sign-in. While `base_url` is empty, passkeys take the scheme and host by which the browser reached
  the server (`httpx.Origin`; the trusted proxies decide the scheme), so the first admin of a new
  server can add one before setting `base_url`; a passkey stays bound to that host. TOTP secrets are sealed
  with the settings key. Failed checks count against the `u:<username>` rate-limit key. SSO
  users have no second factor.
- **Sign-in.** A person who needs the second factor (see **Permissions**) and has one sends the
  password, then a second step with the cookie `tr_login` (HttpOnly, SameSite=Strict, Path
  `/api/auth/login`, 5 min). The state is a row in `auth_challenges`, so it survives a restart. A
  wrong code keeps the sign-in pending. Anyone else signs in with the password alone, also when
  the user has a factor.
- **Where a factor is required.** The sign-in of a person who needs it, device approval and a
  manual publish session (the UI session that publishes) need a fresh factor. Anyone else sets up
  a factor when first approving a device or creating a manual session. An SSO user approves a
  device with a new sign-in at the provider instead.
- **Setup-only session.** A UI session of a local account that needs the second factor and has
  none reaches only the routes whose policy accepts it (`route.Setup`): `/api/me`,
  `/api/auth/logout` and `/api/security...`. Every other route answers 403 with the
  detail `second_factor_required`. The break-glass `admin` follows the same rule. The
  `/api/security` routes need a UI session, so Basic auth and tokens get 401. A password alone
  cannot add a factor and cannot publish.
- **Dev mode.** `telesphorepo-server dev` sets `server.Options.Dev`, which calls `auth.SetDevMode`. It is
  an in-process flag: not a setting, not an env var, and not reachable from `serve`. In dev mode
  a person without a factor gets a full session, and a device approval or manual session without a
  factor passes. A factor that exists is still checked. `GET /api/auth/config` reports
  `second_factor_optional: true`, and the dev banner says "2FA not enforced (dev)". The e2e
  suite runs `serve`, so enforcement stays tested.
- **Where each credential works on the JSON API.** A route that changes state (POST, PUT, PATCH,
  DELETE) or a permission route (GET too) needs the UI session. HTTP Basic with a password or a
  read token reaches only the GET routes that need no permission: repo list and repo, tree, assets,
  search, status, usage, `/api/me`, own tokens, devices and publish sessions. Anything else gets
  401 with the detail `sign in through the web UI for this action` without a Basic challenge. So a
  stolen password cannot change users, permissions, settings or trust policies: the session of a
  person who holds a permission for them needs the second factor. The route policies say it, and
  the router refuses at startup a policy that lets a password or a read token change state or reach
  a permission route (see **Route policy**). The exceptions are the routes with no
  credential or their own token: sign-in, `/api/auth/config`, `/api/formats`, device enrollment
  and poll, the publish challenge, the device and OIDC session mints, closing a session with its
  token, and the upload and delete routes with a publish session token. Dev mode does not change
  this rule.
- **Lost factors.** `telesphorepo-server user reset-2fa NAME` removes the TOTP factors, passkeys and
  recovery codes of a user.
- **Permissions.** The permission registry (`internal/perm`) holds every permission. Each module
  registers one or more **subsystems** (a part of the server, with a label and a one-line help)
  and their permissions (`Extender.Permissions`). A permission has an ID `<subsystem>.<name>`, a
  plain label, a one-line help that says what a holder can do, the permissions of the same scope
  it needs, whether holding it needs a second factor, and (server scope only) whether it gives
  every repository permission on every repository (`AllRepos`). A subsystem is of server scope (a
  person holds the permission, `user_permissions`) or of repo scope (a person holds it on one
  repository, `repo_permissions`). The registry is fixed at startup; the tables store the IDs, and
  every write checks them (400 for an unknown ID, an ID of the other scope or a broken need). A
  read skips an ID that no module registers. `GET /api/permissions` lists the registry, and the UI
  shows one group of checkboxes per subsystem with its help. There is no admin flag: an
  **administrator** is a person who holds every server permission, and the UI shows that word as
  the role. The first-start `admin` and `user add --admin` hold every server permission; nothing
  else grants them all. A disabled user fails authentication.

  | Subsystem (scope, module) | Permission | Label | Needs | Second factor |
  |---|---|---|---|---|
  | `repositories` (server, core) | `repositories.all` | All repositories: create and delete repositories, and hold every repository permission on every repository (`AllRepos`) | | yes |
  | `people` (server, core) | `people.manage` | Manage people: create, disable and enable accounts, reset passwords, and give server permissions, only the ones they hold. Reset the password of, or disable, only a person whose permissions they all hold. See every device | | yes |
  | `settings` (server, core) | `settings.general` | General settings: the groups on Settings › General | | yes |
  | `activity` (server, audit) | `activity.read` | Read the audit log: Settings › Activity, the audit log, the publish log and the download statistics | | yes |
  | `storage` (server, storage) | `storage.manage` | Manage storage: Settings › Storage and the storage moves | | yes |
  | `backup` (server, backup) | `backup.manage` | Manage backups: Settings › Backups, Back up now and the list of backups | | yes |
  | `network` (server, network) | `network.manage` | Manage networks and proxy: Networks (IP lists) and Network and proxy (`trusted_proxies`) | | yes |
  | `signing` (server, signing) | `signing.manage` | Manage signing keys | | yes |
  | `signin` (server, sso) | `signin.manage` | Manage sign-in options: Settings › Sign-in options | | yes |
  | `nexus` (server, nexus) | `nexus.manage` | Manage the Nexus migration | | yes |
  | `repo` (repo, core) | `repo.download` | Download: read the files of the repository | | no |
  | `repo` (repo, core) | `repo.publish` | Publish through a publish session | `repo.download` | no |
  | `repo` (repo, core) | `repo.delete` | Delete files and folders, with the UI session or a device or manual publish session | | yes |
  | `repo` (repo, core) | `repo.settings` | Edit settings: the repository settings, the members of a group, the remote of a proxy, the network rules and the public flag | | yes |
  | `repo` (repo, core) | `repo.access` | Manage access: give and remove this repository's permissions, only the ones they hold | | yes |
  | `ci` (repo, publish) | `ci.pipelines` | Manage CI pipelines that publish to this repository | `repo.publish` | yes |

  Each route's policy names the permission it needs (`Policy.Perm`/`AnyPerm` for a server
  permission, `RepoRule.Perm`/`AnyPerm` for a repository permission). The server refuses a change
  that leaves no enabled person with `people.manage` (409).
- **Second factor rule.** A person needs the second factor for a UI session when they hold any
  permission whose definition needs it (`SecondFactor`): every server permission, and every
  repository permission other than `repo.download` and `repo.publish`. `second_factor_required` in
  `GET /api/me` follows this rule. A grant that makes a person need the second factor ends their
  UI sessions, so a session that a password alone started never holds it.
  `signin.password_login=publishers` refuses the password sign-in (403) of a local account that
  needs the second factor by this rule (a person who manages); publishers sign in with a password.
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
proves who it is. There are three proofs:

| Who | Proof | Route |
|---|---|---|
| Developer with the Telesphorepo CLI | An enrolled device key (Ed25519) | `POST /api/publish/sessions` |
| Developer with Docker, Helm or ORAS | An enrolled device key of the CLI's credential helper, over a token exchange nonce | `GET, POST /docker/token` (see **Docker registry**), `POST /api/publish/sessions/exchange` |
| CI (GitHub Actions, Jenkins, any OIDC issuer) | The CI identity token and a trust policy (CI pipeline) | `POST /api/publish/sessions/oidc` |
| Anyone, by hand | UI session plus a fresh second factor | `POST /api/publish/sessions/manual` |

Routes (`{items, next}` lists are keyset, `?after=&limit=`):

| Method & path | Auth | Notes |
|---|---|---|
| `POST /api/devices/enroll` | none | `{name, public_key}` (`public_key` is the raw 32-byte key, base64url, no padding). Returns `{device_code, user_code: "ABCD-EFGH", verification_url: "<base_url>/#/device", expires_in: 600, interval: 5}`. `verification_url` uses the request URL when `base_url` is empty. Each IP gets a burst of 10, then 1 per 30 s (429) |
| `POST /api/devices/enroll/poll` | none | `{device_code}`. 200 `{device_id}` once approved (single use). 400, a problem document with the detail `authorization_pending`, `slow_down` or `expired_token`. `slow_down` answers a poll less than 4 s after the last. An unknown or used code gets `expired_token` |
| `GET /api/devices/enroll?user_code=` | UI session or password | `{user_code, name, expires_at}`; 404 for an unknown or expired code |
| `POST /api/devices/approve` | UI session and a fresh factor (`X-Telesphorepo-Proof`) | `{user_code}`. A local account proves with `totp <code>` or `passkey <assertion>`, an SSO account with `grant <grant>` of a fresh SSO sign-in for this code. 201 Device. 401 for a wrong second factor, or for a missing, used, expired or other grant (another user or code), 404 unknown or expired code, 409 used code, 403 `second_factor_required` |
| `GET /api/devices` | password, read token or UI session | Own devices; a holder of `people.manage` sees all devices, with `username`. Newest first, `{items, next}`. A device is `{id, user_id, username?, name, created_at, expires_at, last_used_at, revoked_at, state}` |
| `GET /api/network/trusted-proxies` · `PUT /api/network/trusted-proxies` | `network.manage` | GET answers `{lists: [{id, name}], env_lists: [name]}`: the IP lists that the trusted proxies include (rows of `trusted_proxy_lists`), and those that `TR_TRUSTED_PROXIES` names. PUT takes `{lists: [id]}`, replaces the rows in one transaction, records `network.trusted.update` and answers like GET. 400 for an unknown id |
| `DELETE /api/devices/{id}` | UI session | 204. A holder of `people.manage` may revoke any device. It ends the device's open sessions |
| `DELETE /api/devices/self` | none (the device signature) | `{device_id, nonce, signature}`; 204. See **Device logout**. 401 for a bad nonce or signature, or a device that is not usable |
| `POST /api/devices/setup` | none (the setup code) | `{code, name, public_key}` or `{code, device_id, nonce, signature}`; 200 `{device_id, user, repo, format, kind}`. See **Project setup**. 401 `setup-code-used`; 401 for a bad device proof; 409 for a device of another user or a key that is enrolled already; the refusal of `Decide(..., Mint)` |
| `POST /api/publish/setup-codes` | UI session, a fresh factor and `repo.publish` on the repo | `{repo}`; 201 `{code, link, expires_at}`. See **Project setup** |
| `POST /api/publish/challenge` | none | `{device_id, exchange, nonce, ts, signature}` returns `{nonce, expires_at}` (32 random bytes, base64url, and the expiry). Without `exchange`: single use, 60 s, for `POST /api/publish/sessions`, `POST /api/devices/setup` or `DELETE /api/devices/self`. With `exchange: true`: a token exchange nonce that lives `publish.session_ttl`, signed for the repo `*` and sent to a format's token endpoint (the Docker credential helper). With `exchange: true` and a `nonce` that is a token exchange nonce of the device that has not expired, the answer is that nonce and its expiry, and the server writes no row. Else a new token exchange nonce needs `ts` (Unix seconds, at most 5 minutes from the server time) and `signature`: the device's Ed25519 signature over `"telesphorepo challenge v1\n<device id>\n<ts>"` (base64url, no padding), so only the holder of the device key makes the server write a row that lives that long; a missing or bad signature or time is 401. A device that is unknown, revoked or expired, or whose user is disabled, gets 401 |
| `POST /api/publish/sessions` | none | `{device_id, repo, nonce, signature}` returns 201 `{id, token: "trs_...", repo, format, kind, expires_at}` (`format` and `kind` of the repo) |
| `POST /api/publish/sessions/manual` | UI session and a fresh factor (`X-Telesphorepo-Proof`) | `{repo}`; same answer. The proof is `totp <code>` or `passkey <assertion>`, not a recovery code. 401 for a wrong proof, 403 `second_factor_required` without a factor, 429 with `Retry-After` when limited. `access.Engine.Decide` with `Mint` decides (405 for a proxy or a group, 409 for a locked repo, 403 for the network rules) |
| `POST /api/publish/sessions/oidc` | none | `{token, repo, revision?}`; same answer. `revision` is the commit of the job (40 or 64 lower-case hex digits, else 400); the build record keeps it when the token names no commit |
| `POST /api/publish/sessions/exchange` | none (the device proof) | `{proof: "trx.<device id>.<nonce>.<signature>", repo}`; same answer, and the same session and token again for the same nonce and repo while the session is open. 401 for a bad or ended proof |
| `GET /api/publish/sessions` | password, read token or UI session | The caller's own sessions |
| `DELETE /api/publish/sessions/{id}` | the session's own token | 204. The token is the Basic password or a Bearer token. Another token, an ended session or none gets 401 |
| `GET /api/publish/log` | `activity.read` | Every session, newest first |
| `GET, POST /api/publish/trusts` · `GET, PATCH, DELETE /api/publish/trusts/{id}` | `ci.pipelines` on a repo | Trust policies (CI pipelines). A holder of `repositories.all` sees and changes every one. Anyone else sees and changes only the policies whose repos all hold `ci.pipelines` for them: the list leaves the others out, and a policy outside that scope answers 404. A new or changed `repos` list outside that scope answers 403. 403 when the caller holds `ci.pipelines` on no repo. A create, a change and a delete record `ci.pipeline.create`, `ci.pipeline.update` and `ci.pipeline.delete`. The list is keyset by name: one query of the policies that list a repo of the caller's scope and no repo outside it, never a walk of all policies |
| `GET /api/publish/trusts/count` | `ci.pipelines` on a repo | `{count, more}`: the CI pipelines in the caller's scope of the list above. A holder of `repositories.all` gets a bounded count of every one, which stops at 10,000 (`more: true`); anyone else the pipelines whose every repo they hold `ci.pipelines` on, counted from the pipelines of those repos (`publish_trust_repos_repo`). The Settings overview shows it |

Shapes:

```
Device       = {id, user_id, username?, name, created_at, expires_at, last_used_at, revoked_at, state}
SessionItem  = {id, repo, via, subject, username?, device_id, device_name?, trust_id, trust_name?,
                created_at, expires_at, closed_at, state}
Trust input  = {name, issuer, audience, claims: {claim: [value...]}, jwks, repos: [name...], disabled}
Trust output = {id, name, issuer, audience, claims, jwks, repos, created_at, disabled}
```

PATCH changes only the fields that are set. Approve, manual session and the pending lookup
refuse read tokens and publish session tokens. A `trs_` token never reaches the admin routes,
also not for an admin user.

**Device flow** (RFC 8628 style):
1. The CLI creates an Ed25519 key pair, stores it in `~/.telesphorepo/<server>/device.json` (`<server>` is the host, with `_<port>` when the port is not the default, and `_<base path>` when the server has one)
   (owner only) and calls `enroll`. It prints `Open <verification_url>?code=<USER-CODE> and
   approve code <USER-CODE>`.
2. The user opens the page, signs in, checks the name of the device of the Telesphorepo CLI, and
   approves. A local account
   approves with a fresh TOTP code or passkey check. An SSO account selects **Sign in again with
   single sign-on to approve**: `GET /api/auth/oidc/device?code=` asks the provider for a new
   sign-in (`prompt=login`, `max_age=0`). The callback checks that the provider signed in the same
   account (issuer and `sub`) and that `auth_time` is at most `oidc.approval_max_age` old (5 minutes by default); a token without
   `auth_time` is refused. It stores a grant in `auth_challenges` (kind `device_grant`, SHA-256 of
   the grant, the user id and the user code, single use, 5 min) and returns to
   `/#/device?code=<code>&grant=<grant>`. The page removes the grant from the address and sends
   it to `approve`, which takes it once and requires the same user and code. A provider that
   ignores `prompt=login` returns an old `auth_time`; the page then shows `stale_sign_in` and names
   that cause. The approval writes the same rows and audit events as the local path: `device.approve`
   for the approval, and `publish.session.create` when the device mints a session.
3. The client polls until it gets `device_id`. An enrollment code expires after 10 min.
4. A device lives `publish.device_ttl` unless revoked. The client then enrolls again.

**Minting from a device.** `signature` is the Ed25519 signature (base64url, no padding) over the
UTF-8 text `"telesphorepo publish v1\n" + nonce + "\n" + device_id + "\n" + repo` (`device_id` in
decimal). The server checks the signature and nonce, that the device is not revoked or expired,
that its user (local or SSO) is enabled, and that the user holds `repo.publish` on the repo now. The token is the Basic password for the upload, and
it also reads that repo.

**Trust policies and the CI exchange.**
- A policy needs at least one claim, and each claim at least one allowed value. A value is exact
  or a `*` glob; `*` matches any text, `/` included. Every listed claim must match.
- `audience` `""` means the current `base_url`, resolved at exchange time. When both are empty,
  the exchange refuses the token and the error says to set the public address on Settings ›
  General or an audience on the pipeline. Every refusal of a policy match names Settings › CI
  pipelines as the place that fixes it. A pasted `jwks` is
  checked when the policy is saved and replaces key discovery (for a Jenkins the server cannot
  reach). Signing algorithms are RS, ES and PS (256, 384, 512) and EdDSA. Issuer keys are cached.
- The server checks signature, issuer, audience, `exp`, and `iat` (at most 10 min old and 1 min
  in the future). It finds an enabled policy that matches the issuer, audience and claims and
  lists the repo. The mint route refuses a token seen before for any repo (`publish_oidc_seen`,
  kept until `exp`). The Minter, which a format's token exchange calls, mints once for each repo
  and gives a token its open session of that repo back. The replay row and the session are
  written in one transaction. `subject` is the token's `sub`.
- The CI provider that matches the token (see **CI providers and build records**) reads its claims
  into a build record. The build record and the session are written in one transaction; the
  session has `build_id`, and every file that it writes points at the build (`assets.build_id`). A
  token that no provider reads makes no build record.
- An edit of issuer, audience, claims, jwks or repos, a disable, and a delete end the policy's
  open sessions.
- The build record takes its commit from the token (GitHub Actions `sha`). A token that names
  none (Jenkins) takes the `revision` that the job sends with it: the `revision` of the mint
  route, or the user name `telesphorepo-ci@<commit>` at `/docker/token`. The job states it, and
  no signature covers it.
- A CI session writes outright. A device or manual session needs the user's `repo.publish` permission.

**Sessions.** An open session has `closed_at` empty and `expires_at` ahead. A device session also
needs a device that is not revoked or expired. An OIDC session needs an enabled policy. A bad,
unknown, closed or expired token counts toward the rate limit and answers 401. The job in
`internal/publish` runs every minute. In batches of 100 it deletes expired nonces
(`publish_nonces`), enrollments and seen CI tokens, sessions that expired more than 30 days ago
(the audit trail for the **Publish log**). Each run also reads one batch of the build records
older than 30 days, with a cursor that persists between runs, and deletes the ones that no file
references; a session that names a deleted build keeps its row with `build_id` NULL. A second
mint for the same run updates the record and its `created_at`, so a build in use is never old.

**CI providers and build records.** A CI provider (`ci.Provider`, `Extender.CIProvider`) is one CI
system: `Match` (does a token, or with no claims a trust policy, of this issuer belong to it),
`Build` (the build record of a verified token) and `Describe` (what a trust policy's allowed claims
let publish, in its words). The publish module registers two:

- `github-actions` (title GitHub Actions): the issuer `https://token.actions.githubusercontent.com`
  (or with `/<enterprise>`), or any issuer whose token carries `repository` and `workflow_ref`
  (GitHub Enterprise Server). A build record needs a well-formed `repository`, `sha`, `run_id` and a
  `workflow_ref` of that repository. The key is `<server>/<repository>/<run_id>/<run_attempt>`;
  `revision` is the commit, `ref`, `ref_name`, `ref_type` come from `ref`, `run` is `run_id`,
  `workflow` the workflow's name (else its file), `actor`. Links go to the repository, the commit,
  the run (with the attempt) and the workflow file at the commit, on `https://github.com` for the
  GitHub.com issuer and on the issuer's origin otherwise. Its `Describe` reads `repository`, `ref`
  (branches and tags), `workflow_ref`, `job_workflow_ref`, `workflow` and `sub`.
- `jenkins` (title Jenkins): the issuer `<jenkins>/oidc` of the Jenkins OpenID Connect provider
  plugin. The repository is the job path of `sub` (the job URL `<jenkins>/job/a/job/b/` gives
  `a/b`), `run` the `build_number` claim when the token has it. It links to the job, and to the
  build (`sub` + `<build_number>/`) only with a build number. A token whose `sub` is not a job URL
  of the issuer's Jenkins makes no record. Its `Describe` reads the job paths of exact `sub` values.

An issuer that no provider matches gives no build record and no link; the access report shows its
policies as system `other` with their subjects. The API shows a build record as `build: {id,
provider, provider_title, repository, revision, ref, ref_name, ref_type, run, workflow, actor,
links: [{rel, label, url}]}` on tree entries, file details, versions and builds; null when none.
The dev demo data writes its builds through the same provider (GitHub Actions claims), not as
sessions.

**Project setup.** One client, the Telesphorepo CLI (`cmd/telesphorepo`), enrolls devices and
publishes to every format; the approval page names it "Telesphorepo CLI". The Publish tab of a repo
makes a setup line: `POST /api/publish/setup-codes` with `{repo}` (a UI session, a fresh second
factor, and `Decide(..., Mint)` with `repo.publish` on the repo, the same bar as a device approval)
stores a setup code `trsu_<32 random bytes, base64url>` as an `auth_challenges` row of kind
`setup_code` (its SHA-256, the user, the repo id, 10 minutes), records `setup_code.create` and
answers `{code, link: "<base>/#/setup/<code>", expires_at}`. The code is in the fragment of the
link, so a browser that opens the link never sends it. An SSO account confirms with the grant of a
fresh SSO sign-in: `GET /api/auth/oidc/setup?repo=` asks the provider for a new sign-in, and the
callback stores a grant with the data `setup:<repo>` and returns to
`/#/repo/<repo>?setup_grant=<grant>`; the route refuses a grant of another repo. The CLI then calls
`POST /api/devices/setup` with the code and a new device key, or with the proof of the device it
has. The server first checks a device proof (a Challenge nonce, the signature over
`"telesphorepo setup v1\n" + nonce + "\n" + device_id`, a usable device), then takes the code (the
first use deletes it). It checks that the user of the code is enabled and may mint for the repo,
and that an existing device belongs to that user (409 otherwise). It creates the device (the
device lifetime is `publish.device_ttl`) or records its use, and records `device.setup` as that
user. A failure after the code was taken puts the code back, unless the code expired. A wrong,
used or expired code answers 401, the problem `setup-code-used`.

**Device logout.** `DELETE /api/devices/self` with `{device_id, nonce, signature}`, the signature
over `"telesphorepo revoke v1\n" + nonce + "\n" + device_id` and a Challenge nonce, revokes the
device, ends its open sessions and records `device.revoke` as the device's user.

**Token exchange for the helpers.** `POST /api/publish/sessions/exchange` with `{proof:
"trx.<device id>.<nonce>.<signature>", repo}` mints through the same code as the `Minter` of
`/docker/token`: one session per repo per token exchange nonce, and the open session and its token
again on a later call with the same nonce and repo (see **Minting a publish session**). The
credential helpers of the CLI use it.

**Telesphorepo CLI** (`cmd/telesphorepo`, Go, in this repository under the MIT license of its own
`LICENSE`). It imports no server package; outside the standard library it uses only
`golang.org/x/sys` (the owner-only ACL, the terminal check, the parent process),
`github.com/BurntSushi/toml` (`telesphorepo.toml`) and the root package `changelog`. Every
request carries `User-Agent: telesphorepo-cli/<version> (<os>/<arch>)`, goes only to the
canonical URL of the server and follows no redirect. No command takes a token as an argument, and
the CLI never prints a CI token, a session token or a setup code; in GitHub Actions it prints
`::add-mask::` for each token before a child starts. Every command has an example-led `--help`.

| Command | Does |
|---|---|
| `setup LINK [--name NAME]` | Reads the server from the link and the code from its fragment (`#/setup/trsu_...`). With a device that the server accepts (`POST /api/publish/challenge`), it sends `POST /api/devices/setup` with `{code, device_id, nonce, signature}`; else with `{code, name, public_key}` of a new Ed25519 key (name: the host name), and saves the device file. It refreshes `hosts.json` (`GET /docker/hosts`) and prints `publishing to <repo> (<format>) as <user>`. A format of the publish category: the folder must hold a marker of the format (else the CLI names the markers and stops); the CLI writes `telesphorepo.toml` there (a file with other values: it shows the change and asks y/N), with `kind` when the folder has several marker kinds (it asks on a terminal, else stops and lists them), `path` for raw (asked on a terminal) and `dist` and `component` for Debian (asked, default `stable` and `main`). Then `git check-ignore -v` warns when git ignores the file and names the matching line. The helper category (Docker): no project file; it shows the `credHelpers` entry of the server's Docker hosts, asks "Add the credential helper to `$DOCKER_CONFIG/config.json`? [y/N]", edits the file on yes (else prints the entry), and prints `docker push <host>/<repo>/app:1.0`. A Docker repo set up in a folder with `Chart.yaml` also gets the project file. On a terminal it then offers a CI file (none, GitHub Actions, Jenkinsfile, Jenkins freestyle): it mints a session, reads `GET /api/repos/{name}/ci?system=&tool=` with it, shows the file, writes it at the top of the git work tree (a file that exists and differs needs y), and closes the session. A freestyle job gets the build step to paste |
| `publish [FILE...]` | Walks up from the current folder to the nearest `telesphorepo.toml`. With none it stops: on a computer it names the Publish tab, in CI it says to commit the file. It mints one session for the repo: in CI (`CI`, `GITHUB_ACTIONS`, `JENKINS_URL` or `JENKINS_HOME`) with the identity token of the job (GitHub Actions `ACTIONS_ID_TOKEN_REQUEST_URL` and `_TOKEN` with the canonical server URL as the audience, then `OIDC_TOKEN`) through `POST /api/publish/sessions/oidc`, with `revision`: the commit of the job (`GIT_COMMIT`, then `GITHUB_SHA`, then `git rev-parse HEAD` in the project folder, when it is a commit hash). The CLI sends an `OIDC_TOKEN` only when its `aud` claim (read without a check of the signature) holds the canonical URL of the server of the project file, with or without a trailing slash; else it stops and names both URLs, because a project file of an unchecked branch could name another server; on a computer with the device proof through `POST /api/publish/challenge` and `POST /api/publish/sessions` (the challenge also shows whether the server accepts the device; a device that it refuses, or none, runs the device flow first, inline, then mints). FILE uploads (raw, Debian, PyPI and NuGet, also the files of a PyPI or .NET build) run 4 at a time; the `Uploaded` and `Pushed` lines come in the order of the files, the first failure cancels the uploads that run and starts no other, and the files after it that were uploaded get their line all the same. Two raw FILE arguments with one file name stop the command. It runs the row of the repo's format (the detection table below) and closes the session with `DELETE /api/publish/sessions/{id}` at exit, also on Ctrl-C. A child's exit code passes through; a child that outlives the session gets a line that names `publish.session_ttl`. A repo of the helper category stops with the native command and the hook |
| `login SERVER [--name NAME]` | The device flow alone: an Ed25519 key, `POST /api/devices/enroll`, `Open <verification_url>?code=<CODE> and approve code <CODE>` (the browser opens on a terminal), the poll at `interval` (`slow_down` adds 5 s). It saves `device.json` and `hosts.json`. A device that the server accepts stays |
| `logout [SERVER]` | Revokes the device with `DELETE /api/devices/self` (a Challenge nonce, the signature over `"telesphorepo revoke v1\n" + nonce + "\n" + device_id`), removes the `credHelpers` entries `"telesphorepo"` of the server's hosts and keeps every other entry, and deletes `~/.telesphorepo/<server>/`. Without SERVER: the server of the project when this computer has its device, else the one enrolled server |
| `status` | Each enrolled server: the device, whether the server accepts it, the `credHelpers` entries. The current folder: the project file and the command that `publish` runs. It prints no secret |
| `update [SERVER]` | Installs the CLI of the server (the server of the project, else SERVER, else the one enrolled server): `GET /cli/SHA256SUMS` and `GET /cli/telesphorepo_<os>_<arch>.<ext>`, the SHA-256 check, a new file next to the running binary, then a rename into place (on Windows the running file first becomes `telesphorepo.exe.old`; every start deletes such a `.old` file). It makes `docker-credential-telesphorepo` again: a symbolic link, a hard link on Windows, a copy when a link fails. A folder that is not writable gets its name and the install line. A CLI that is the same as or newer than the server's changes nothing |
| `version`, `help [command]` | The version (`-X main.version`); the help |
| `docker-credential-telesphorepo get\|store\|erase\|list` | The Docker helper protocol (below). The same binary under a second name, which the install script makes |

**Detection.** `publish` looks for the markers of the repo's format in the folder of the project
file. A row is one entry of a table in `cmd/telesphorepo/detect.go`; a new format adds a row.

| Format | Marker | Tool | `publish` runs | Token |
|---|---|---|---|---|
| Maven | `pom.xml` | `mvnw` (`mvnw.cmd` on Windows), else `mvn` | `mvn deploy -gs <tmp>/settings.xml -DaltDeploymentRepository=telesphorepo::<base>/maven/<repo>` | `settings.xml` has one `<server>` `telesphorepo` with `<password>${env.TELESPHOREPO_TOKEN}</password>`; the user's `~/.m2/settings.xml` stays in force |
| Maven (Gradle) | `settings.gradle(.kts)`, `build.gradle(.kts)` | `gradlew` (`gradlew.bat`), else `gradle` | `gradle publish --init-script <tmp>/telesphorepo.init.gradle.kts` | The init script adds the repository `telesphorepo` (URL from `TELESPHOREPO_MAVEN_URL`, password from `TELESPHOREPO_TOKEN`) to each project that applies `maven-publish` (with `allowInsecureProtocol` for an `http://` server); with none it stops the build with the plugin line |
| Docker (Helm chart) | `Chart.yaml` | `helm` | `helm package <dir> -d <tmp>/chart`, then `helm push <chart>.tgz oci://<host>/<repo> --registry-config <tmp>/config.json` (the upload host when the server has one; `--plain-http` for an `http://` server) | The registry config names `credHelpers` `telesphorepo` for the Docker hosts; the helper answers `TELESPHOREPO_TOKEN` for the hosts of `TELESPHOREPO_DOCKER_HOSTS` |
| raw | none | the CLI | `PUT /raw/<repo>/<path>/<file name>` for each FILE; a file of 64 MiB or more goes through `POST /api/repos/{name}/uploads`, `PATCH` chunks of 16 MiB (a failed chunk is sent again from the `received` of the server), and `PUT ...?path=&sha256=&size=` | Bearer session token |
| NuGet | `*.csproj`, `*.fsproj`, `*.sln`, `*.slnx` | `dotnet` | `dotnet pack -c Release -o <tmp>/nupkg` (no token in its env), then for each `.nupkg` but `*.symbols.nupkg` `PUT /nuget/<repo>/package` with the package as the first part of `multipart/form-data`; a package of 64 MiB or more goes through the resumable routes as for raw, and `PUT /nuget/<repo>/package?upload=<id>&sha256=&size=` finishes it (a server older than the change log entry `nuget-resumable-push` gets the one push). With FILE arguments: a push of each FILE, no build, also in a folder without a project | `X-NuGet-ApiKey: <session token>` |
| Debian | none | the CLI | `PUT /debian/<repo>/upload/<dist>/<component>` for each FILE; a file of 64 MiB or more goes through the resumable routes as for raw, and `PUT /debian/<repo>/upload/<dist>/<component>?upload=<id>&sha256=&size=` finishes it | Bearer session token |
| Cargo | `Cargo.toml` | `cargo` | `cargo publish --registry telesphorepo --config 'registries.telesphorepo.index="sparse+<base>/cargo/<repo>/index/"' --config 'registries.telesphorepo.credential-provider="cargo:token"'` | `CARGO_REGISTRIES_TELESPHOREPO_TOKEN` in the env of cargo; cargo sends it as a raw `Authorization` header |
| npm | `package.json` | by lockfile: `pnpm-lock.yaml` pnpm; `yarn.lock` with `.yarnrc.yml` yarn; `bun.lock` (or `bun.lockb`) bun; else npm, each on the `PATH` | `npm publish --registry <base>/npm/<repo>/`, `pnpm publish --registry ... --no-git-checks`, `yarn npm publish` or `bun publish --registry ...`; the package manager packs | npm and pnpm: `npm_config_userconfig=<tmp>/npmrc` with the registry and `//<host>/npm/<repo>/:_authToken=${TELESPHOREPO_TOKEN}`. bun: `NPM_CONFIG_TOKEN`. yarn: `YARN_NPM_PUBLISH_REGISTRY`, `YARN_NPM_AUTH_TOKEN` and, for an `http://` server, `YARN_UNSAFE_HTTP_WHITELIST`. `setup` offers the CI file of the tool of the lockfile |
| PyPI | `pyproject.toml`, `setup.py` | `poetry` (`[tool.poetry]` in `pyproject.toml`), `uv` (a `uv.lock`), else `python3`/`python` with the `build` package | `poetry build --output <tmp>/dist`, `uv build --out-dir <tmp>/dist` or `python -m build --outdir <tmp>/dist` (the build gets no token), then `POST /pypi/<repo>/legacy/` (the legacy upload form) for each wheel and sdist; a file of 64 MiB or more goes through the resumable routes and `POST /pypi/<repo>/legacy/?upload=<id>&sha256=&size=&filename=`. FILE arguments upload the named files without a build. The CI tool of setup follows the build tool (`poetry`, `uv` or `pip`) | Bearer session token |

A wrapper in the project comes before a tool on the `PATH` (`exec.LookPath`); a wrapper without
the execute bit runs through `sh`. The CLI prints each command before it runs it, without the
token. The token goes only into the env of the child, as `TELESPHOREPO_TOKEN`; the config files
in the temp folder (`0700`, an owner-only ACL on Windows, removed at exit) hold `${env}`
references. The child env has no `OIDC_TOKEN`, so a build step cannot mint for other repos with
it. Ctrl-C reaches the child from the terminal; a stop of the CLI itself stops the child too.

**Compatibility.** Before a command, the CLI takes the entries of the change log (see **Change
log**) between its version and the server's whose scope matches the command (`setup`, `login`,
`publish`, then `publish/<format>` once the mint names the format, `helper/docker`). Server newer:
it reads `GET /api/changes?since=<CLI version>`; an entry with `needs = "cli"` prints its summary
and, on a terminal, asks `Update now? [Y/n]`, updates and runs the command again with the new
binary; without a terminal it prints the install line and exits 1. CLI newer: its embedded
entries after the server's version; an entry with `needs = "server"` takes the older path when the
CLI lists the `id` of the entry (`olderPaths` in `compat.go`; the id stays when the version is stamped), else prints the summary and `This needs server <v>;
the server runs <v>. Ask the server admin to update.` and exits 1. A dev CLI or a dev server skips
the check. `~/.telesphorepo/<server>/changes.json` keeps `{server_version, since, changes,
drift_notice}`; the CLI fetches again only when the `Telesphorepo-Version` header of an answer
names another version. When the server is newer, the CLI asks after the command `The server has
CLI <v>, you have <v>. Update now? [Y/n]` at most once a day per server; without a terminal and in
the helper mode it prints one line on stderr instead.

**Errors.** A problem document prints its `title` and `detail` as they are, then its remedy:
`update_cli` names `telesphorepo update` (on a terminal it offers the update), `update_server`
names the server admin and `version`, `login` runs the device flow on a terminal and then the
command again, `setup` prints the Publish tab link of `url`, `retry` waits `after` seconds (at most
60) and sends the request once more, `open_url` prints `url` and opens it on a terminal,
`fix_project` names `file`. An unknown action prints `detail` and `url`; an unknown field is
ignored. A format route's `{"error"}` prints as its detail. Any other answer of 300 or more gets
`the server at <url> answered <status> with <content type>; a proxy in front of it may be down`.

**Docker credential helper mode.** Docker, buildx, Podman, skopeo, crane, Helm and ORAS run it
through `credHelpers` in `$DOCKER_CONFIG/config.json` (else `~/.docker/config.json`):

| Command | stdin | Answer |
|---|---|---|
| `get` | the registry host (`host[:port]`) | In this order: `{"ServerURL", "Username": "telesphorepo", "Secret": "<session token>"}` when `TELESPHOREPO_TOKEN` is set and the host is one of `TELESPHOREPO_DOCKER_HOSTS` (a child of `publish`); `{..., "Username": "telesphorepo-device", "Secret": "trx.<device id>.<nonce>.<signature>"}` for a host of an enrolled device: a nonce of `POST /api/publish/challenge` with `exchange: true` (the request carries `ts` and the device signature over it), and the device signature over the nonce for the repo `*` (base64url, no padding); in a CI job with no device, `{..., "Username": "telesphorepo-ci@<commit>", "Secret": "<identity token of the job>"}` (`telesphorepo-ci` when the job has no commit), which `/docker/token` mints from; the token endpoint takes the commit after `telesphorepo-ci@` as the revision of the mint. The server of the token is `TELESPHOREPO_SERVER` (the Docker CI files set it) when the host is its host or one of its `GET /docker/hosts` (another host: the reason on stderr and no credential), else the server of the project file when its host is the registry host, else `https://<host>`. GitHub Actions gets that server as the audience; an `OIDC_TOKEN` whose `aud` does not hold it gets no credential. Else, or on a 4xx from the challenge (a device that is unknown, revoked or expired): `credentials not found in native keychain` and exit 1, so Docker reads anonymously; the reason goes to stderr. Each `get` sends the challenge, so a revoked device stops at once. The process that runs the helper (its id and start time) keeps its nonce in `exchange.json` and sends it as `nonce`, so it gets the same nonce while the nonce is valid: Helm and ORAS run `get` for each token, and all token requests of one push get the same publish session |
| `store` | Docker's credentials | Exit 1: "docker-credential-telesphorepo keeps no passwords. Run: telesphorepo login <server URL>" |
| `erase` | the registry host | Keeps the device (`docker logout`) and says to run `telesphorepo logout` |
| `list` | `unused` | `{"<host>": "telesphorepo-device", ...}` for each host of each enrolled device |
| `version`, `help [command]` | | The version; the help |

- **Files.** `~/.telesphorepo/<server>/device.json` (one per computer and server) is
  `{"server": "<canonical URL>", "device_id": n, "private_key": "<base64 PKCS#8>", "public_key":
  "<base64 raw 32 bytes>", "created_at": "<RFC 3339>"}`, the only secret file. `hosts.json` next
  to it holds `{"hosts": [...]}` of `GET /docker/hosts`. `exchange.json` next to it holds the token
  exchange nonce of each process that ran `get`: `{"nonces": [{"pid": n, "start": "<start time of
  the process>", "nonce": "...", "expires_at": "<RFC 3339>"}]}`; a save drops the expired nonces.
  `changes.json` is the change log cache. A file that is missing or not valid is empty, and a
  failed write goes to stderr. The CLI writes each file through a temporary file and a rename: mode
  600 in a 700 folder on Linux and macOS, an ACL with the current user as the only entry on
  Windows. A device file that another user can open (a mode bit of group or others, an ACL entry of
  another user) stops the CLI. `<server>` is the host without IPv6 brackets, `_<port>` when the
  port is not the default of the scheme, and `_` with the base path (each `/` as `_`); each other
  character than `A-Z a-z 0-9 . _ -` becomes `_`.
- **Server identity.** The canonical URL is the scheme (http or https), the lower-case ASCII host,
  the effective port and the base path without a trailing slash; an internationalized host must be
  in its `xn--` form. `get` answers a device proof only when the host and port that Docker passes
  equal those of the server, or a host of `hosts.json` (a host without a port takes the default
  port of the server's scheme). Every request goes to the canonical URL of the device file or the
  project file, never to the host that Docker passed.
- **Token exchange.** Docker sends the secret as the password to `/docker/token`. The token
  endpoint mints one session per repo per nonce (`publish_exchange_sessions`); a pull checks the
  proof (`Minter.Check`) and mints nothing. The bearer carries the device (`auth.CredDevice`), so
  it reads what the device's user reads (a private base image too) and pushes through the session
  of its scope.

**UI pages.** The sign-in page (second step for a person who needs it: the passkey first, its prompt
opening by itself, then a code from the app, and a recovery code only behind a link), the approval page (`/#/device?code=`)
and, under Your account: **Sign-in and security** (TOTP with a QR code, passkeys, recovery codes),
**Devices**, **Tokens** (read tokens, and publish tokens with a new manual session), and under
Settings for people who hold the permission **CI pipelines** (trust policies, started from GitHub Actions, Jenkins or another CI
system) and **Activity** (the publish log is its second tab). A setup-only session sees only Sign-in and security. The approval page shows when the code expires (10 minutes after the login command starts). A user with no second factor follows its link to
`/#/account/security?back=/device?code=<code>`; once a method is on, that page shows **Go back**,
and the approval page looks the code up again. An SSO account sees **Sign in again with single
sign-on to approve** in place of the second-factor form; back from the provider, the page approves
at once, or shows the error. See **Web UI**.

## Byte pipeline

Every file of every repo goes through one write pipeline, one delete pipeline and one read path in
the kernel (`internal/assets`, `internal/blobs`). A format takes part through the hooks of its
`format.Spec`; no module writes the `assets` or `blobs` tables.

**Blobs.** The content of a file is a blob, stored by its SHA-256 at
`blobs/sha256/<first 2 hex>/<hex>`. One blob backs any number of files in any repos: the same bytes
uploaded twice, to one repo or to several, are stored once. A blob is stored with no content type;
the asset row of each path holds its own. `blobs.refs` counts the asset rows that point to a blob
(triggers in the transaction of every asset write). A blob without references stays for
`blobs.gc_grace` (`1h`): an upload of the same bytes in that time reuses it.

**Locks.** A write takes the path locks of its files (and its extra locks) in sorted order, then
the digest locks of the blobs it stores in sorted order; the blob GC takes digest locks only, and
the storage move copier takes the digest lock of the blob it copies. So no two holders wait on
each other in a cycle. The locks are in-process (one server process).

**Write (`Commit`, `CommitAll`).** A write is `{repo, path, body, content type, kind, origin, extra
locks, meta}`. The body is spooled bytes with their MD5, SHA-1 and SHA-256, or the digest of a blob
that exists (a mount; an unknown digest is 404 `blob unknown`). The origin is the source (`put`, `ui`,
`import`, `proxy`, `generated`, `demo`), the user, the CI build record of the publish session, the
dates (an import gives the source's dates; a proxy the remote's `Last-Modified`), and a proxy's ETag
and check time. `CommitAll` writes files of one repo in one transaction:

1. The path locks and the extra locks; then each existing asset.
2. `Describe` of the format gives an empty content type and kind (a format without it:
   `application/octet-stream` and `artifact`); a path that `Describe` refuses is 400.
3. `Admit` of the format decides each write: store, keep (the same bytes: status `unchanged`, no
   write) or a refusal with its status (400, 404, 409). Without `Admit`, the same bytes are kept and
   other bytes replace the file.
4. The digest locks; each new blob is written once (a few at a time; on the filesystem a spool on
   the store's volume is renamed into place), and its row is inserted.
5. One transaction: the asset rows, the format's `OnCommitted` (its own rows, rebuild marks), and
   one `file.upload` event per stored file whose source is `put`, `ui` or `import`. A failed
   transaction is tried again up to 3 times.
6. The digest locks, then the path locks are released; then `AfterCommit` runs with a result per
   write (`Write`, `Asset`, `Status`, and `Prev`, the asset that a replaced write replaced).

A repo that is being deleted refuses every write with 409. The write runs detached from its
request (a client that goes away does not leave a blob without its row) and stops only when the
server shuts down. The result of each file is `created`, `replaced` or `unchanged`.

Every writer uses it: the format routes, the web UI upload, the resumable uploads, the Nexus
import, the proxy fetch, the Debian upload and index rebuild, the Maven checksums and metadata,
and the dev demo data (source `demo` writes blobs without the store's fsync when the store is the
filesystem). `telesphorepo-server seed` inserts rows only (`assets.SeedTx`).

**Delete (`Remove`).** A removal is `{repo, paths, prefixes (folders ending in "/"; "" is every
file), origin}`. It works in batches of 500 rows: the path locks in path order, then one
transaction with the format's `OnRemoved` (the rows are still readable; it gets the folders of
the removal, whose other files go in this or a later batch, and it may return follow-up paths,
removed as the next batches), the row deletes and one `file.delete` event per file of
`paths`; one event per folder of `prefixes` after its last batch. Then `AfterRemove`. A path that
does not exist is skipped. Blob objects are never deleted here: the blob GC removes a blob that no
file uses. While the repo has an active `delete` operation, `Remove` calls no `OnRemoved` and no
`AfterRemove` and records no `file.delete` event, and the rebuild queue skips the repo; the rows of
the format go with the repo row (`ON DELETE CASCADE`).

**Blob GC.** The job `core.blob-gc` waits `blobs.gc_grace` (at least a second, at most 10 minutes),
then deletes every blob with `refs = 0` for longer than the grace, in rounds of up to 1000 blobs.
Under the digest locks of the round, which every write that references a blob also holds, one
query reads which blobs still have `refs` 0 since before the cutoff. One `DeleteMany` deletes
their objects (one `DeleteObjects` on S3), then one transaction deletes the rows of the objects
that are gone, also when the job stops meanwhile. A blob whose object delete failed keeps its
row, and the next run tries again.

**Read path (`Reader.Serve`).** A hosted repo serves the asset of the path, or 404. A proxy asks
the format's `Classify` (a refused path is 404), fetches the file through `proxy.Service.Ensure`
when the cached copy is missing or stale (see **Group and proxy repositories**), then serves it. A
group tries its members in read order that the caller may read (`access.Engine.Allows` with
`Read`); the first member with the file serves it, and a proxy member fetches only when no member
before it has the file (stored copies of a path that names its bytes come first; see **Groups**).
With a format `Merger` that matches the path and two or more members that
have its base file, the server merges the members' files and serves the merged bytes; a checksum
path (`md5`, `sha1`, `sha256`, `sha512`) of a merged file is the checksum of the merged bytes. No
member: 404, or 502 when a proxy member's remote failed. A format with its own answers uses the
parts: `Reader.Resolve(ctx, r, p, repo, path)` (the asset by the rules above, nil when none,
`ErrRemoteFailed` when a proxy failed and nothing else has it), `Reader.ResolveLive` (the same with
`Reader.Live(w, r, header)`: a proxy fetch of a file that is not cached streams to the client, and
the caller checks `live.Done()` before it answers; see **Proxies**), `Reader.Stored(ctx, r, p, repo,
path)` (a stored asset only: hosted, a proxy's cache, or the first readable group member that
stores it; it never contacts a remote) and `Reader.ServeAsset(w, r, asset, ServeOptions{NoRedirect})`
(the download of one asset; `NoRedirect` streams it even when the store presigns).

One function sets the content headers from the asset row: `Content-Type`, `ETag` (`"<sha256>"`),
`Last-Modified`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: default-src 'none';
sandbox`, `Content-Disposition: attachment` except for `text/plain`; it answers 304 to a matching
`If-None-Match`, and the stream serves ranges. With S3 and `s3.redirect` on, a GET of a hosted or
proxy file answers 302 to a presigned URL that signs `response-content-type` and
`response-content-disposition` from the asset row, so each path of a shared blob keeps its own
content type; the disposition (`attachment`, or `inline` for `text/plain`) carries the base name
of the path as `filename`, since the presigned URL ends in the SHA-256; the 302 carries `ETag` and `Cache-Control: no-store`. HEAD, merged files and every
request without `s3.redirect` stream. The route step sets `Cache-Control: private` on every format
read of a repo that not everyone reads (see **Route policy**); a public repo's
answers carry no `Cache-Control`.

**Resumable uploads (`Uploads`).** A publish session starts an upload in a repo, appends chunks at
increasing offsets (`Append` needs the offset to equal the bytes received, else 416 with the bytes
received; a refused append, 416 or 404, still reads the body up to `max_upload_size`, so the
client reads the answer before the connection closes), and finishes it with the SHA-256 (and optionally the size) of the whole file; the web
UI upload, which cannot hash a large file in parts, finishes with the size alone. The bytes
are in `$TR_DATA_DIR/tmp/uploads/<id>`; the row saves the count after each append, so an upload
continues after a restart. Each append hashes its bytes as they arrive and saves the MD5, SHA-1
and SHA-256 state in `<id>.hash` (with the byte count and a SHA-256 of the state); a state that
does not cover exactly the bytes received (an append that stopped before its row update, a damaged
file) is rebuilt from a read of those bytes. `Finish` drops bytes past the count, takes the hashes
from the state without reading the file again, deletes the upload on a mismatch (400), and returns
the body for a `Commit` with source `put`. Only the publish session that started an upload continues it (a core bearer token counts
with the publish session of its scope of the repo); `Status` returns the bytes received. `max_upload_size` caps it, and
the job `core.upload-sessions` deletes uploads idle past `upload.session_ttl` (`24h`) with their files.
A repo delete or a user delete removes the rows of their uploads (`ON DELETE CASCADE`), so the job
also deletes each file in `tmp/uploads` that has no row and was not written for `upload.session_ttl`.

**Repo paths.** `repopath.Parse` checks a file path: valid UTF-8, no control character, no
backslash, no leading `/`, no empty, `.` or `..` segment, at most 1024 bytes and 255 bytes per
segment. `repopath.Folder` takes `""` or a folder and returns it with one trailing `/`. A format
route reads its path with `httpx.RepoPath`, which first refuses an encoded `/`, `\` or NUL and an
encoded `.` or `..` segment in the raw URL. Routes, the file tree, deletes, uploads and imports use
the same rules.

**Watchers.** A module that must see every commit and remove of every format and every writer
registers an `assets.Watcher` with `Pipeline.Watch` in its `Init` (the edge module keeps its R2
pointers with it); several run in the order they were added. `Replacing(repo, prev)` runs in
`CommitAll` after `Admit` and before the blobs are ensured, under the path locks, with the assets
that the stored writes replace; `Creating(repo, paths)` runs at the same point with the paths that
they create. `Committed(tx, repo, stored)` runs in the commit transaction after `OnCommitted` (a
retried transaction calls it again). `Removing(repo, paths)` runs in each remove batch after the
path locks and before the transaction (also during a repo delete), and `Removed(tx, repo,
removed)` in the remove transaction after `OnRemoved`. `Touched(repo, paths)` runs after
`Pipeline.SetTimes` changed the dates of an asset. An error of `Replacing`, `Creating`,
`Committed`, `Removing` or `Removed` fails the commit or the batch. `Pipeline.Lock(repo, path)`
takes the path lock of a commit and a remove.

**Format hooks.** `format.Spec` holds the hooks: `Describe`, `Admit`, `OnCommitted`, `AfterCommit`,
`OnRemoved`, `AfterRemove`, `OnRepoChange` (in the transaction of every repo update), `Classify`
and `ProxyProbe` (a proxy), `Merge` (a group), `RemoteAuth` (the answer to a remote's 401 challenge,
e.g. a registry's token exchange; the proxy calls it once per fetch) and `Rebuild`. Each format
gives its content types through `Describe`; the kernel's only content type is the fallback
`application/octet-stream`.

**Rebuild queue.** A format marks a unit of a group of a repo with `rebuild.Queue.MarkTx` in the
transaction of the change (Debian: the distribution and `component/arch`). The job `core.rebuild`
debounces per (repo, group) with `index.debounce` (`2s`, dev `300ms`) and `index.max_delay`
(`30s`), calls the format's `Rebuild.Run` with the units marked before it started, and deletes
those units unless they were marked again; a failed rebuild is tried again with a backoff up to 5
minutes. At most two groups rebuild at a time. At startup it picks up every pending row. A repo that
is being deleted is skipped. `GET /api/repos/{name}/status` answers the pending count.

**Importers.** A format's `Importer` has `Sources` (source format IDs), `Target` (the target path of
a source file, or ignored), an optional `Store` and an optional `Header` (extra headers of every
download from the source, e.g. the `Accept` of the OCI manifest types). `Importer.Run` does the
shared work once: the target path, the skip of a file whose target holds the source's SHA-256 (it
gets the source's dates), the download under `max_upload_size` with the source checksum checked,
then `Store` or a `Commit` with source `import`. An `unchanged` result counts as skipped. An
`ImportItem` carries the source's `ContentType` of the file and `Fetch(ctx, sourcePath, header)`,
which downloads another path of the same source repo (e.g. a layer that a manifest names) and
answers the source's authentication challenges, and `Cache`, the `RunCache` of the import run:
`Do(ctx, key, fn)` calls `fn` once per key for the items of the run (callers of a key wait for
it), and drops a failed result so the next caller asks again. Items of a run import at the same
time, so `Run`, `Target` and `Store` are safe for concurrent use.

## Repo operations

A repo operation (`repo_ops`, `internal/ops`) is a long task that owns a repo: at most one per repo,
with an ID, a type, its owner module, the owner's reference, a state (`running`, `held`, `failed`),
progress (`done`, `total`) and the error of a failure. Its row is deleted when it ends. While a repo
has an operation, the access engine refuses `Write`, `Delete` and `Mint` with 409 and the message
of the operation type, in full sentences; repo JSON shows it as `operation`. A module registers its types with
`Extender.OpType` (`ID`, `Label`, `Message`, and `Resume` and `Clear` when it has them).

- **`delete`** (core). `POST /api/repos/delete` begins it (`running`) for each repo and answers
  202; each delete is a task of the core module, at most 4 at once (the others wait for a slot).
  It removes the files with progress (`done` and `total`), then `repos.Service.DeleteRow` empties
  what the row delete would cascade to in short writes: it sets `audit_events.repo_id` to NULL in
  batches of 1000 through `audit_events_repo`, and runs the `repos.Purge` that each module with
  rows of a repo registers (`Repos.OnPurge`; one batch of at most 1000 rows per statement, children
  first: docker, npm, pypi, debian, cargo, nuget, publish sessions and Nexus mappings) until it
  deletes nothing. Then it deletes the repo row (the operation and the few rows left go with it)
  and records `repo.delete`. A failure sets `failed` with the error and keeps the operation; a
  server stop leaves it `running`, and the job `core.ops` continues running deletes at startup.
  `POST /api/repos/{name}/operation/resume` starts a failed delete again; `DELETE /api/repos/{name}/operation` ends it and the repo keeps the files that are
  left. The repo page shows a failed operation with Resume and "Stop and keep the files" to a holder
  of `repositories.all`.
- **`mirror`** (nexus). `held` while a Nexus source mirrors a hosted repo; its reference is the
  source ID and its message is `Uploads and deletes start after the switch-over from Nexus server <name>.` Saving the mapping
  begins it, and unmapping or the switch-over of the repo ends it. It cannot be resumed or cleared through the repo
  routes.
- **`merge`** (nexus). `running` on both repos of a merge while it runs (see **Nexus sources**,
  Gradual move); its reference is the merge id, and `done` and `total` count the files of the
  copy. The target's message is `<source> is being merged into this repository. Uploads start again
  when the merge is done.`, the source's `This repository is being merged into <target>. Its name
  answers for <target> when the merge is done.` A failure sets both `failed`; Resume (on either
  repo) continues the merge, Stop and keep the files stops it. A server stop leaves them `running`,
  and the nexus job continues the merge at startup.
- **`empty`** (nexus). `running` while the files of a rehearsal repo are deleted (see **Nexus
  sources**, Rehearsal repositories); `done` and `total` count them. The repo stays. A server
  stop leaves it `running`, and the nexus job continues it at startup. Resume and Stop and keep
  the files work as for a delete.

## Repository redirects

A redirect is a repository name that answers for another repo (`repo_redirects`, kept by
`repos.Service`). Every request to it, from every client and every route, is served by its repo
exactly as if it named the repo, publishes included. It is not an HTTP redirect: Maven and Gradle
follow one for downloads, but uploads, `npm publish`, `docker push` and apt do not.

- **Rename.** `POST /api/repos/{name}/rename` (`repositories.all`, `repos.Service.Rename`) changes
  `repos.name`, adds the old name as a redirect and runs the format's `OnRepoChange` with the old
  and the new name (docker gives every image its new `full_name`, so the catalog lists it under
  the new name), with one `repo.rename` event, in one transaction. So the bad first name of a
  repository can stay as a redirect while everyone moves to the better one.
- **Merge.** `repos.Service.Retire` gives a repo's names to another repo: it renames the repo to a
  name nobody uses, its name and its redirects become redirects of the other repo, the format's
  `OnRepoChange` and one `repo.rename` event (`detail.redirect_to`) run, and the caller's rows go in
  the same transaction. A Nexus merge retires its source this way before the source's delete.
- **One resolution.** `repos.Service.ByName` reads `repos` by its UNIQUE name first, then
  `repo_redirects` by its key, then the repo by id. Every request that names a repo goes through
  it: the repo step of every route policy (format routes, `/api/repos/{name}/...`), the mint routes
  and the token exchange, the Docker token endpoint, grants, CI pipeline repositories, group
  members, network rules (by the repo of the route) and the Activity filter. `ByOwnName` skips the
  redirects (the edge objects); `RedirectTarget` reads one redirect without noting a use.
- **Keys.** Everything the server keys by repo uses the repo id (assets, pointers, grants, rules,
  sessions). The names that a credential carries resolve through `ByName` and match by id: each
  scope of a core bearer token gets the id of the repo its name names when the token is verified
  (`auth.Scope.RepoID`; the Worker reads the repo object of the scope's name), and the grant of a
  setup code resolves its repo name, so a token issued before a rename keeps working through both
  names. The edge object of a redirect names its
  repo (see **Edge delivery**).
- **Reserved.** A name is a repo or a redirect, never both: create and rename refuse the name of a
  redirect of another repo (409, the message names its repo). A repo may take back a redirect of
  its own: that redirect goes, and the name it had becomes one.
- **Never a chain.** A redirect holds the repo id, so a later rename of its repo keeps it answering
  for the repo under the new name.
- **Permissions, network rules and visibility** are the repo's. **Activity records the name the
  request used:** the route step, the mint routes and the Docker token endpoint put a redirect
  that a request named in the context (`audit.WithRepoName`), and an event of that repo records it
  as `repo`, with the repo's own name in `detail.redirect_to`.
- **Last use.** `ByName` notes each use in memory and the download counters write it once a minute
  (see **Download statistics**). `GET /api/repos/{name}/redirects` shows it.
- **Remove the redirect.** `DELETE /api/repos/{name}/redirects/{redirect}` (`repositories.all`):
  the name answers 404 from then on and is free for a new repo. A repo delete removes its
  redirects.
- **Debian.** apt refuses the update of a source whose `Release` changed its `Origin` or `Label`,
  so a rebuild of a `Release` keeps the `Origin` and the `Label` of the current one; a new
  distribution gets the repo's name.

## Debian indexing pipeline

1. **Upload.** The upload is spooled while MD5, SHA1 and SHA256 are computed. The `control.tar.*`
   is parsed (capped at 16 MiB decompressed, `control` at 1 MiB); `data.tar` is never unpacked.
2. **Validation.** `Package`, `Version` and `Architecture` are validated; the architecture must be
   `all` or one of the repo's `architectures` (default `amd64, arm64`). The debian module validates
   the repo config: a non-empty list, each name matching `^[a-z0-9-]+$`, no `all`, no duplicates.
   The optional key `distribution` (a repo name pattern) is the distribution that the Nexus
   sync writes to. The key `signing_key` is 400: the signing key of a repo is the repo field `signing_key` (default
   `default`, see **Signing keys**). Other config keys are kept.
3. **Commit.** The pool file goes through `Commit` with the package identity
   (dist/component/package/version/arch) as an extra lock and the control data as `Meta`. The
   format's `Admit` keeps a package version that exists with the same bytes (200), and refuses one
   with other bytes or a pool file that holds other bytes (409); a pool file with the same bytes is
   stored again, so the package goes into another distribution. `OnCommitted` writes the
   `deb_packages` row and marks the units (distribution, `component/arch`) for a rebuild in the same
   transaction. An `all` package marks every configured architecture.
4. **Debounce.** The kernel's rebuild queue debounces per (repo, distribution) with
   `index.debounce` and `index.max_delay` and calls the format's `Rebuild.Run` (see **Byte
   pipeline**).
5. **Package indexes.** For each unit, rows are streamed through the unique index into `Packages`
   and `Packages.gz` (gzip at the default level). One `CommitAll` (source `generated`) writes them with their
   `by-hash/SHA256/*` copies (the same blobs), then `deb_index_files` is updated.
6. **Release.** `Release` is rebuilt from `deb_index_files` (`Acquire-By-Hash: yes`); one
   `CommitAll` writes `Release`, `Release.gpg` and `InRelease`.
7. **Architecture change.** A repo update that changes `architectures` marks, for each
   (distribution, component) with packages, the added architectures (without one, the first
   configured one) and the removed ones. The rebuild builds the added ones and, for a unit whose
   architecture is no longer configured, removes its index files (with their by-hash files) and
   its `deb_index_files` rows; then each `Release` is rebuilt. A change of `signing_key` marks one
   unit of each distribution, so each `Release` is signed again. The marks are written in the
   transaction of the repo update; the (distribution, component) pairs come from one seek of the
   UNIQUE index of `deb_packages` per pair (`ListPackageComponents`), so the update costs the
   pairs, not the packages.
8. **By-hash cleanup.** After each `Packages` rebuild, the server keeps the current and the
   previous generation of `by-hash/` files for that index and removes the rest (source
   `generated`).
9. **Delete.** The format's `OnRemoved` deletes the `deb_packages` rows of each removed pool file and
   marks the affected units. During a repo delete the module's `repos.Purge` deletes the package
   and index rows in batches before the repo row goes.

## Storage move

A settings PATCH that points the server at another blob location while blobs exist starts a move
(`internal/move`). The move copies; it changes and deletes nothing on the source.

- **Start.** The PATCH probes the target, saves the non-storage keys of the patch, and stores
  the target storage settings in a `storage_moves` row, encrypted (AAD `storage_moves:<id>`).
  The storage settings do not change yet. One move runs at a time.
- **Mirror.** While the move copies, `Live` holds a `storage.Mirror`. Reads and listings use the
  source. `Put`, `Delete` and `DeleteMany` (new blobs, the blob GC) go to the target and the
  source at once, each store reading the spool on its own; an error on either fails the call.
- **Copier.** It walks `blobs` by `id > cursor` in pages of 500 and saves the cursor and counters
  after each page. For each blob it takes its digest lock (so the blob GC waits) and reads the row
  again. It skips a blob that the GC deleted, and one that the target holds with the same size (a
  blob never changes). It checks the source size and SHA-256 before it writes the target. A missing
  source blob or a mismatch is a `storage_move_failures` row. The copy of a blob has a deadline of
  5 minutes plus a second per MiB; a copy past it is a failure row, and the copier goes on with the
  next blobs.
- **Finish.** Without failures, one settings write makes the target the active storage, `Live`
  swaps to the target and the move is `done`. With failures the move is `failed` ("N blobs could
  not be copied"), and `Live` returns to the source. A later move skips the blobs already copied.
- **Cancel.** `DELETE` marks the move `cancelled`, stops the copier and returns `Live` to the
  source.
- **Retry.** `POST .../retry` starts a new move to the sealed target of a failed or cancelled
  move, with changed settings on top, through the same settings save as a PATCH (`Validate`,
  then the `Apply` hook: probe, start). The new move skips the blobs the target already holds.
  The old move is dismissed.
- **Dismiss.** `POST .../dismiss` sets `dismissed_at` of an ended move; `current` then answers
  404. Storage does not change.
- **Restart.** A shutdown leaves the move `copying`. At startup, before the server serves
  requests, the move continues from its cursor. If the settings already point at the target, the
  move is marked `done`. If the target cannot be opened or probed, the move fails with that
  error.
- **UI.** The **Storage** page (`/#/settings/storage`) is in one state at a time. Without a
  move it shows the storage form. While a move copies it shows where the files go (the folder,
  or the bucket with its folder or its root), that storage stays on the current place until
  every file is copied, the progress and Cancel, and no form. A failed or cancelled move shows
  that storage is still on the current place and nothing was lost, why it ended, the counts and
  the failure list, with Try again (the retry with `{}`), Change the target (the form filled with
  `target_settings`; the secret shows as set and stays the target's unless replaced; Save sends
  the retry with the edits, Test connection the retry test) and Keep (dismiss). A done move shows
  one success line, with Dismiss, over the form. The dialog of a save that changes the place
  says where the files go, and when only the folder in the same bucket changes, that the files
  at the old folder stay there.

## Backups

The image has a folder `/backups` that belongs to the server's user (65532) and is a `VOLUME`, like `/data`, and its build sets the default of `backup.dir` to it (`-X internal/backup.defaultDir=/backups`). So a container backs up from its first start, and a new volume mounted at `/backups` takes the folder's owner from the image, with no setup. A plain build (a downloaded binary) has the default `""`: backups are off until a folder is set.

Module `backup` (`internal/backup`). With `backup.dir` set, the job `backup.schedule` checks every
minute whether a backup is due: when the folder has no complete backup, or the newest is older than
`backup.interval`. After a failed run it waits `backup.interval`, at most an hour, before it tries
again. `POST /api/backup/run` starts one at once; one runs at a time. The state is the folder
itself: the module has no table.

- **Folder.** A backup is `<backup.dir>/<UTC start time>/`, named `20261020T201500.123Z` (sortable).
  It holds `telesphorepo.db`, `master.key` and `backup.json`. A folder without a readable
  `backup.json` is incomplete: the list shows it, restore refuses it and the next run removes it.
- **Database.** `VACUUM INTO` from a reader of the live database writes a consistent copy while the
  server writes (`db.VacuumInto`); the copy is in rollback journal mode, so it opens read-only
  without side files. The file is owner-only (0600).
- **Master key.** `master.key` of the data dir is copied with mode 0600. With `TR_MASTER_KEY` set
  the backup has none, and `backup.json` says `master_key_source: "env"`.
- **Blobs (fs).** The blobs that an asset uses (`refs > 0`) go into `<backup.dir>/blobs/`, the same
  layout as the store (`blobs/sha256/<2 hex>/<hex>`), shared by every backup of the folder. A blob
  that the folder holds with its size is not copied again (a blob never changes); a copy goes to a
  temp name, its size and SHA-256 are checked, then it is renamed. Each page of 500 blobs is copied
  12 at a time. The run copies the blobs of the live database first, then writes the database copy,
  then copies the blobs of the copy that the first pass missed (uploads in between): the second
  pass does not look again at a blob that the first pass found or copied (by id and SHA-256), and
  only sets its mtime when the run has taken more than half an hour. In the server each copy holds the digest lock of the
  blob, so the blob GC waits; a blob that the copy uses and the store no longer holds fails the
  run.
- **Blobs (S3).** No blob is copied. `backup.json` names the bucket, the prefix and the endpoint
  (never a credential): back up the bucket with the tools of its provider.
- **`backup.json`** (written last, through a temp file): `format` (1), `version` of the server,
  `created_at`, `storage` (`fs`/`s3`), `s3`, `blobs_included`, `migrations` (the rows of
  `schema_migrations` of the database: `component`, `version`, `name`, `checksum`), `counts`
  (`repos`, `files`, `file_bytes`, `blobs`, `blob_bytes`), `database` (`file`, `size`, `sha256`),
  `master_key_included`, `master_key_source`.
- **Retention.** After a run, the newest `backup.keep` complete backups stay; every other backup
  folder is removed. Then each file of `blobs/` that no kept backup uses is removed: the module
  reads the blob list of each kept backup's database, read-only. When one of them cannot be read,
  no blob is removed. Retention leaves alone what may still be written: an incomplete folder, and a
  file of `blobs/` (a temp file too), while its mtime is less than an hour old. A running backup
  touches its folder after each page of blobs and each blob it uses that is already there, so it
  stays young however long it takes. So a `telesphorepo-server backup`
  into the folder of the server's backups is safe while the server runs retention.
- **Log and audit.** One log line per run (`backup written` with the name, the counts and the
  duration, or `backup failed` with the error) and one audit event (`backup.done` or
  `backup.failed`). Neither holds a secret.
- **Restore.** `telesphorepo-server restore FOLDER` reads `backup.json`, refuses a data dir that is
  not empty, checks the SHA-256 of the database, then copies, with fs storage, every blob that the
  database uses from the `blobs/` folder next to the backup into the fs store of the data dir
  (`TR_FS_ROOT`, else the stored `fs.root`, else `blobs/` in the data dir; it first checks that the
  folder holds all of them), then `master.key` (0600), and the database last. The restored database
  is byte for byte the backed-up one. With S3 the restored server reads the blobs from the bucket,
  which must still hold them. It then compares the `migrations` of `backup.json` with the
  migration files of the binary and says which holds: the same migrations; older, so the server
  applies the newer migrations at its start; or from a newer Telesphorepo (or with a changed
  migration), so this binary refuses to start on it, and the line names the version of the backup
  to run. The files are restored in every case.
- **UI.** Settings › Backups (`/#/settings/backups`): the list with Back up now, the progress of a
  running backup or the time of the next one, the error of a failed run, and the settings form with
  Test the folder.

## Edge delivery

Clients far from the server download from Cloudflare's edge: R2 buckets, a Queue and a Worker
(`edge/`), kept by the module `edge` (`internal/edge`). The Worker answers a GET or HEAD only when
it can prove that the server would give the same answer, and passes every other request to the
server. It never refuses: every 401, 403, 404, 405, 409, 416 and 429 comes from the server, so the
server keeps the audit events, the rate limits and the error bodies. It answers 502 itself only
when it cannot reach the server. Cloudflare is the only provider (R2, Queues and Workers on the
Workers Paid plan), and client URLs do not change: the Worker runs on routes of the base URL host.

Two rules shape the design:

- **Consistency before latency.** A path that can change (Maven metadata and snapshots, Debian
  `dists/` outside `by-hash/`, Docker tags, every raw path) has its pointer in the home bucket only,
  so every region reads the same current version. An immutable path has a pointer and a blob in
  every region.
- **Warm everywhere from the first request.** The server writes every pointer, and the copy Worker
  copies every blob, when the file is stored, before any client asks for it. No region gets a file
  only after a first miss.

Workers Cache and Sippy are not the delivery path. Workers Cache evicts, so a far region is not
always warm. Sippy copies a file only on the first miss, so the first client of each region pays for
the far read. R2 has no bucket replication and its Workers binding has no copy method, so the copy
Worker streams the home `get().body` into the regional `put` (or `uploadPart`), never a cross-bucket
`CopyObject`.

**Buckets.** The home bucket is the S3 storage of the server (`storage=s3` on R2); blobs keep
their keys `[s3.prefix/]blobs/sha256/<2>/<hex>`. Each chosen region has a bucket of its own in the
same account (`edge.regions`, `<hint>=<bucket>`); the module opens one `storage.Store` per region
with the storage settings and the bucket of the region, so the R2 token of the storage settings
covers every edge bucket. The server writes pointers to the regional buckets; the copy Worker
writes the blobs there. A path that can change has its pointer in the home bucket only, so every
region reads the same version; an immutable path has a pointer and a blob in every region.

**Objects** (JSON, under `[s3.prefix/]edge/v1/`):

| Key | Content | Buckets |
|---|---|---|
| `config.json` | `{server_version, requires: [features], enabled, access_ttl (seconds), edge_key (base64url of the Ed25519 public key), formats: {name: {shape, prefix, immutable, mutable}}, home: {hint, bucket, ready}, regions: [{hint, binding, bucket, ready}]}`; `home.ready`: the outbox wrote every pointer of the backfill; `bucket`: the bucket that the `bucket.json` of the binding must name | home |
| `bucket.json` | `{hint, bucket}` (`hint` is `home` or the region): the bucket names itself, so the Worker sees a binding to another bucket. The controller writes it into each bucket it uses (again after the wipe of a first start, and once per process start); a region becomes ready only after its `bucket.json` is written | home and each region |
| `p/<repo id>/<sha256 hex of the path>` | `{repo, path, sha256, sha1, md5, size, ct, mod (unix), kind}`, or `{repo, path, pending: true}` while a write runs | home; also each region for an immutable path |
| `a/repo/<name>` | `{id, format, kind, anon, private, guarded, members: [names]}`: `private` is `access.Engine.Private`, `guarded` is `access.Engine.Guarded` (a read IP rule, or a guard that is not a `ReadRestricter`). For a redirect name, `{redirect: <repo name>}`: the Worker reads the object of that repo next (one more read on a cache miss) and serves the request as one to that repo, whose name the scopes of a core bearer token carry; a redirect object that names another redirect sends the request to the server. A rename marks the object of the old name, the new name and every redirect of the repo; a repo delete and a removed redirect mark the redirect names | home |
| `a/user/<id>` | `{enabled, local, all, read: [repo ids]}`: `all` is a server permission with `AllRepos`, `read` the repos with `repo.download` | home |
| `a/token/<public id>` | `{id, user, hash (hex SHA-256 of the secret), exp (unix, 0 never)}`; an expired token has none | home |
| `a/device/<id>` | `{user, exp}`; a revoked or expired device has none | home |
| `seen/<isolate>-<unix ms>.json` | The Worker: `{version, tokens: [{id, at}]}`, the read tokens that one isolate checked itself, written 20 seconds after the first of them (at most one file per isolate in 20 seconds, each file a new key) | home |
| `gap/state.json` | The Worker's gap check: `{version (the Worker's build), features, checked, last_run, bindings: [hints], markers: {home, regions: {hint: bucket}}, pass, last_pass: {started, finished}, regions}`; `markers` holds the bucket that the `bucket.json` of each binding names (`""` for none); every run reads them and writes the file with `checked`, the time of the run (one small put per run), so the status's last gap check is the last run, with or without a copy pass. A gap pass skips a region whose binding names another bucket | home |
| `gap/request.json` | The server asks for a gap pass: `{at, regions}` | home |
| `check.json` | The server's check of the Worker: `{hash (hex SHA-256 of the one-time nonce), exp (unix, 30 s after the write), pass}`; `pass`: run a copy pass now (edge delivery is on and a region is not ready). The Worker deletes it when it takes it; the server deletes it after the check | home |
| `speedtest/<hex>.json` | A pass of the speed test: `{hash, exp}`; `hash` is the hex SHA-256 of the token (the `<hex>` of the key), `exp` unix seconds, 10 minutes after the write. The server writes it (`POST /api/edge/speedtest`) and deletes it a minute after `exp`, at a later pass | home |
| `copy/<hint>/<hex>.json` | The copy Worker: the multipart upload of a large copy, so a retried message resumes it | home |

The pointer key hashes the path, so a 1024-byte path fits the R2 key limit. `repos.id` has
`AUTOINCREMENT`, so the ID of a deleted repo never comes back. A repo delete removes every file
through `Remove` before the repo row goes, the Worker checks that a pointer's `repo` is the `id` of
the repo object, and a repo delete marks every user object. User and device IDs are reused.

**Formats.** A format opts in with `format.Spec.Edge`: the shape (`standard`:
`/<format>/<repo>/<path>`; `oci`: `/v2/<repo>/<image>/manifests/<ref>` and
`/v2/<repo>/<image>/blobs/sha256:<hex>`) and `format.Glob` patterns. A path is immutable when it
matches an `Immutable` glob and no `Mutable` glob. maven: immutable `**/*`, mutable
`**/maven-metadata.xml*` and `**/*-SNAPSHOT/*`; debian: immutable `pool/**/*` and
`dists/**/by-hash/**/*`; docker (`oci`): immutable `**/_manifests/sha256/*` and
`**/_blobs/sha256/*` (tags are mutable); npm: immutable `**/-/*.tgz` (packuments are rendered
per request and have no pointer, so the Worker sends them to the server); nuget: immutable
`v3-flatcontainer/*/*/*.nupkg` and `v3-flatcontainer/*/*/*.nuspec` (the documents are rendered per
request and have no pointer); raw: every path is mutable. A proxy gets a pointer only for a path whose `Classify` class is not `Changing`. A group has no pointers of its own.

**The pointers.** The module's pipeline watcher (see **Byte pipeline**) and the outbox
(`edge_outbox`) keep them:

1. `Replacing` writes a dirty outbox row (empty digest) in its own transaction, then puts
   `{pending}` in every bucket for an immutable path, or deletes the home pointer of a mutable one.
   `Creating` does the same for a new immutable path of a repo that is a member of a group: the
   Worker skips a group member without a pointer, so until the new pointer lands the group read
   goes to the server and never serves a later member's copy.
2. `Removing` writes dirty rows, then deletes the pointer keys of the batch with one `DeleteMany`
   per bucket.
3. `Committed` upserts the final rows in the commit transaction. `Removed`, in the remove
   transaction, deletes the dirty row of each removed path whose pointers `Removing` deleted in
   every bucket of the view that is still current (a row with a put of unknown outcome stays);
   any other removed path gets a row with the current state. When the remove transaction fails,
   the dirty rows stay and the outbox puts the pointers of the files that are still there.
4. The job `edge.outbox` runs 32 workers, each writing one row at a time, so one slow put holds
   only its own worker. One reader hands out the due rows (pages of 512, oldest first): the
   access objects and `config.json` first (`edge_outbox_access`), then the pointers; a newly
   marked access object goes out before the next pointer row, so a revocation never waits behind
   a large import. A row is never handed out twice at once. A worker takes `Pipeline.Lock(repo,
   path)`, reads the asset row again, and puts the home pointer and then the regional ones
   (immutable paths), deletes the pointer in every bucket when the asset is gone, or drops the row
   when the asset has another digest. A row is deleted only after every write of it succeeded,
   in transactions of up to 128 rows, at most 200 ms after its writes; a row whose delete did not
   commit is written again, which leaves the same state. A failed row retries with a backoff up
   to 5 minutes. Writes to one key are spaced by a second (R2 takes one write per second per key),
   the wait outside the lock; a 429 of a pre-step retries for up to 2 s. A put that timed out has
   an unknown outcome: the row stays and the state is applied once more a minute later.

A failed pre-step fails the commit or the remove batch. `Touched` (after `SetTimes`) marks the
path dirty. Until a pointer lands, the Worker finds none, or a pending one, and passes.

**Access objects.** The change notices of the kernel mark the keys of the affected objects:
`auth.Service.AfterChange` (tokens, users, permissions, grants), `repos.Service.AfterChange`
(create, update, members, delete; a repo marks its groups too, since a group is private when a
member is), `netrules.Service.AfterRulesChange` (IP rules) and `publish.Service.AfterDeviceChange`
(approve, setup, revoke). The outbox writes the marked keys within a second, before its pointer
rows; `edge_written` keeps the hash of each object written. The job `edge.reconcile` computes
every access object and `config.json` from the database every 5 minutes (the users in pages of
500, with one read of the permissions and one of the grants per page) and marks in the outbox each key whose hash
differs from `edge_written`, that was never written, or whose row is gone; the outbox computes the
body again when it writes, so a reconcile never puts an older body over a newer write. It covers
a lost notice. Device expiry needs no notice: the object carries `exp`. Every write of
`config.json` (the controller's and the outbox's) runs under one lock and builds the body from the
mode of that moment, so an `enabled: true` never lands after the off switch.

**The controller** (job `edge.backfill`). It wakes on a settings change, a storage move and every
10 s, and works at most about 5 s per step.

- **On.** `edge.enabled` with settings that can run it (`storage=s3`, a `base_url` without a path,
  `edge.home_hint`). A first start (edge turned on, or the first save of the edge settings after a
  "deploy needed") deletes `edge/v1/` in the home bucket and in every region, writes
  `config.json` with `home.ready: false`, queues the pointer of every asset (`assets` by
  `id > cursor`, pages of 500, the cursor in `edge_state`, each row with `backfill = 1`) and runs a
  reconcile. When every row is queued and no row with `backfill = 1` is left (the outbox wrote
  each one), the home bucket is ready and `config.json` says `home.ready: true`. Until then a
  missing pointer may be one not written yet, so the Worker answers nothing. A restart resumes
  without a wipe: at module init, before any commit, the module turns on in the buckets of the
  saved state when the settings still run it (the first controller step then follows the
  settings), and it queues `config.json`, so a new server version reaches it.
- **Regions.** A region is `filling` or `ready` (`config.json` lists it with `ready`). A region that
  exists at a first start gets its pointers from the backfill of every pointer (done when the
  home bucket is ready). A region added
  later first loses every stale pointer of its bucket (list and `DeleteMany`), then gets the
  pointer of every immutable path, under the path lock, and the server writes `gap/request.json`
  so the Worker's gap check copies the blobs. When the pointers are done, the server compares the
  home `blobs/` and the regional `blobs/` in key order (a resumable cursor, up to 50 pages of
  1000 keys per step, every 2 minutes); a pass that finds every blob marks the region ready and
  rewrites `config.json`. Missing blobs ask for a gap pass again, at most every 30 minutes. A
  removed region that was never ready leaves `config.json` and gets no writes from then on. A
  removed ready region (a hint taken out of `edge.regions`, or the old bucket of a hint that got
  a new one) is gone: `config.json` is written without it first, then the pre-steps (`{pending}`
  and deletes, never the outbox) keep changing its bucket until the drain end (below); then
  nothing writes it. A region added again during its drain waits for the drain end before it
  clears its bucket. A hint moved to a new bucket before the Worker is deployed again is safe: the
  old Worker's binding reads the old bucket, whose `bucket.json` names the old bucket, so the
  Worker does not read the region.
- **Draining.** Edge turned off, a change of the home location (a key of `storage.LocationKeys`
  other than `s3.redirect`) or a storage move (`move.Service.OnStart`) writes `config.json` with
  `enabled: false` to the home bucket and marks "deploy needed" on a location change or a move.
  The pre-steps keep changing the old buckets (gone regions included) until the drain end, so no
  Worker with a cached `config.json` serves a file that changed; then the outbox is dropped and
  nothing is written. The drain starts when the off switch is written (the controller writes it
  again until a put succeeds). The drain end is the latest time a Worker can still hold a
  `config.json` written before, plus 5 s: each write of `config.json` supersedes the one before,
  which a Worker holds for at most that one's `access_ttl` (the row `ttl` of `edge_state` keeps
  the latest such time), so lowering `edge.access_ttl` does not shorten a drain. The drain is
  saved in the row `home` (its end, the old home location without the secret, its buckets), so
  a restart during it builds the draining view at module init and the pre-steps go on until the
  saved end; a removed region's drain is saved the same way (`gone`). The old home opens with the
  secret of the storage settings, so this needs the same access key ID there; without it the
  module logs that the drain cannot go on and drops it. A move to
  `storage=fs` turns edge delivery off.
- **The Worker's state.** The controller reads `gap/state.json` at most once a minute: the Worker's
  build and features (one that lacks a feature of `requires` is outdated:
  see **The contract**), the regions that the
  Worker has a binding for (a chosen region without one is `waiting`), the start of the copy pass
  that runs and the copies that each region's last pass queued. While edge delivery is not on it
  reads the file from the S3 bucket of the storage settings once `edge.home_hint` is set, so the
  page sees a deployed Worker before edge delivery is turned on. A check of the Worker (below)
  reads it again at once. The blob check keeps when the count of missing blobs last changed.

**The check of the Worker** (`POST /api/edge/check`, the page's **Check now**). It runs one at a
time, at most one every 10 s (429 `edge-check-busy` otherwise), and needs storage s3 and a base URL
without a path. The server writes `check.json` with the SHA-256 of a fresh 256-bit nonce to the
home bucket, then sends `GET <base URL>/.well-known/telesphorepo-edge/check?n=<nonce>` (60 s
timeout, no redirects). The request goes through Cloudflare, so the Worker on the route of the
reserved path (`<host>/.well-known/telesphorepo-edge/*`, in every `wrangler.jsonc` the server
writes) answers it. The Worker answers that path itself and never passes it to the server: only
for the nonce of `check.json` (a constant-time compare of its SHA-256) before `exp` it deletes
`check.json`, so the nonce works once, runs the gap check (the cron's function, with at most 1,000
subrequests and 20 s of listing; a pass when `pass` is set, else when one is due; a pass that does
not end goes on at the next cron run), and answers 200 `{answered_by: "worker", version, features, ran: true,
pass: "done"|"partial"|"none"|"off"}`; any other request there is 403 `{answered_by: "worker",
version, features, ran: false, error}`. Both carry `Telesphorepo-Edge-Check: worker`. When the request
reaches the server, its own handler of the path answers with `Telesphorepo-Edge-Check: server`
(200 only for the nonce of the check that runs, else 403), and the check says "The request reached
the server, not the Worker: the Worker is not deployed on <host>, or not on this route". Another
answer is "other" (something in front of the server, such as Cloudflare Access), no answer is
"unreachable". After the request the server deletes `check.json` and reads `gap/state.json`. The
last result is kept in memory for the status. The check changes no server state and records no
audit event. A Worker deployed with a `wrangler.jsonc` without that route is not on the path: the
check says the request reached the server until the Worker is deployed again.
**The speed test** (the page's **Speed test**). It shows the edge working: the browser downloads
one file through the Worker from a bucket that the administrator picks and times it.
`POST /api/edge/speedtest` (UI session, `edge.manage`, the conditions of the check) writes
`speedtest/<hex>.json` to the home bucket with the SHA-256 of a random 256-bit token and an
expiry 10 minutes ahead, and answers the token. A GET or HEAD with
`Telesphorepo-Edge-Region: <hint>|home|origin` and `Telesphorepo-Edge-Pass: <token>` then goes
through the usual steps of **What the Worker answers**. Only once the request passes every one of
them does the Worker look at the choice:

- The pass is valid when the SHA-256 of the token names a pass object of the home bucket (read in
  the same round as the repo object), whose `hash` equals it (a constant-time compare) and whose
  `exp` has not passed. The isolate keeps a valid pass in memory for at most a minute, never past
  `exp`; an unknown or expired token is read again each time.
- `origin` sends the request to the server. A hint reads the pointer of an immutable path and the
  blob from that region when it is ready, has a binding and its `bucket.json` names its bucket;
  `home` from the home bucket. A mutable path still reads its pointer in the home bucket, and a
  blob the region lacks still comes from the home bucket, so `Telesphorepo-Edge: hit;
  region=<hint>` names the bucket that served the bytes.
- An edge answer of a choice that the Worker took carries `Server-Timing: pointer;dur=<ms>;desc="<hint>",
  blob;dur=<ms>;desc="<hint>"`: the time of each bucket read to its headers.
- Every answer to a request with either header carries `Telesphorepo-Edge-Override`:
  `applied; region=<choice>`; `refused; reason=pass` (no valid pass) or `refused; reason=region`
  (the Worker does not read that region), both served as without the headers; or `unused` (the
  Worker passed the request to the server for its own reasons: a check failed, no pointer).
- The Worker drops both headers when it forwards, so the server never sees a pass.

Why this is safe: the choice comes after the checks of the credential, the repo and the pointer,
and every bucket holds the same bytes under the same pointer, so the pass changes which copy
answers, never whether an answer is allowed or what it holds. `origin` is the answer that any
request gets by carrying a `tr_session` cookie. The pass only keeps the headers from being a knob
for everyone: without it a client could pin the far bucket or the server for its own downloads. It
lives 10 minutes, the server keeps only its hash, and the Worker never logs it. Issuing a pass
changes no server state, so it records no audit event.

The page needs the address it runs on to be the base URL, because the browser downloads from its
own origin (`credentials: "omit"`, so the Worker sees an anonymous request, and `cache:
"no-store"`) and the Worker runs only on the base URL host. Times come from the browser's resource
timing of the request (time to first byte, total, and the bytes over the time from the first byte
to the last).

The job `edge.seen` lists `seen/` every minute, reads up to 16 files at once, sets
`last_used_at` of those read tokens (`auth.Service.TouchTokens`, which never moves it back) and
deletes the files. A token that the Worker serves goes into the pending set of its isolate. When a
request notes a token and the isolate has no flush scheduled, the request schedules one
(`waitUntil`): it waits 20 seconds, then writes every pending token in one file,
`seen/<isolate>-<unix ms>.json`, a new key each time. A write that succeeds takes its tokens out of
the set; a failed one leaves them for the next flush. So an isolate writes at most about three
files a minute, and a token is lost only when the runtime ends the `waitUntil` before the write.

**What the Worker answers.** A GET or HEAD where all of these hold; anything else goes to the
server:

1. `config.json` says `enabled` and `home.ready`, the `bucket.json` of `HOME` names
   `home.bucket`, and the Worker implements every feature of `requires` (see **The contract**).
2. The URL parses with the format's shape and the repopath rules; a path with an encoded `/`, `\`
   or NUL, or with an empty, `.` or `..` segment, goes to the server, and so do uploads, tags
   lists, referrers, `/v2/`, the catalog, `key.asc` and a request with the query parameter
   `_state`.
3. The repo object exists, has the route's format and is not `guarded`.
4. The caller may read the repo: no credential on an anonymous-read repo; a read token `trp_` (the
   Basic password with any username, or `Bearer trp_...`: the token object, a constant-time compare
   of the SHA-256 of the secret, `exp`, the user `enabled` and `local`, then the repo in `read`, or
   `all`, or `anon`); on the `oci` shape a core bearer of an anonymous, password, read token or
   device credential without a session scope and without an upload binding (its Ed25519 signature,
   `exp`, a scope of the repo with `Read`, then the rows of its credential). A `tr_session`
   cookie, a password, a publish session, a device proof, any other token or `Authorization`, and
   a credential that fails a check go to the server.
5. A pointer exists and is not pending, and its `repo` and `path` match.

A group read goes to the server for a mutable path (every `Merger` path is mutable) and when any
member is `guarded`. The Worker walks the members in order: it skips a member the caller may not
read (checked without the bearer's scopes, as `Principal.InGroup`), serves the first member with
a pointer, skips a hosted member without one, and passes to the server at a pending pointer or at
a proxy member without a pointer. It reads member pointers 6 at a time. After `config.json` it
reads the `bucket.json` of `HOME`, the repo object, the objects of the credential and the
`bucket.json` of the nearer regions at once (a read token's user object follows its token object,
whose `user` it names; a bearer's token or device object and its user object are read at once),
so a cache expiry costs about one far read, not one per object. It reads access objects
and `config.json` through isolate memory and the Cache API for `edge.access_ttl` (0: every
request reads the home bucket), so a revocation reaches the edge within that time plus the outbox
write. The Cache API does not work behind Cloudflare Access, so the base URL host must not be
behind it.

**Which bucket.** The nearest ready region with a binding whose `bucket.json` names the region's
`bucket` (read through isolate memory and the Cache API like `config.json`), or the home bucket, by great-circle
distance from `request.cf` to a fixed point per hint (`wnam` San Francisco, `enam` Ashburn,
`weur` London, `eeur` Warsaw, `apac` Singapore, `oc` Sydney; the home bucket at
`edge.home_hint`); without coordinates the home bucket. An immutable path reads its pointer in
that bucket, a mutable path in the home bucket; the blob is read in that bucket, then in the home
bucket.

**The answer** sets what `Reader.ServeAsset` sets on a streamed answer, from the pointer: the
checksum headers, `Content-Type`, `ETag`, `Last-Modified`, nosniff, the sandbox CSP,
`Content-Disposition: attachment` except for `text/plain`, `Accept-Ranges`,
`Telesphorepo-Version` (`server_version` of `config.json`), `Cache-Control: private` when the repo object is
private, and on `oci` `Docker-Content-Digest` and `Docker-Distribution-API-Version`. A matching
`If-None-Match` is 304; HEAD answers from the pointer; one range (`If-Range` absent or the exact
ETag) is 206 from a ranged R2 read; several ranges or another `If-Range` serve the whole file; a
range that the server answers with 416 goes to the server. `Telesphorepo-Edge: hit; region=<hint>`
marks an edge answer. With `s3.redirect` the server answers a 302 to the store; the edge streams
the same bytes with 200.

**Forwarding.** The Worker forwards with `fetch()` (`redirect: manual`, the body streamed) and
sets `x-real-ip` and `X-Forwarded-For` to the incoming `CF-Connecting-IP`, never a value from the
client; with `list:cloudflare` in the trusted proxies the server sees the real client. It drops
the hop-by-hop headers (`Connection`, `Keep-Alive`, `Proxy-Connection`, `TE`, `Trailer`,
`Transfer-Encoding`, `Upgrade`) and `Expect`, so a client's `Expect: 100-continue` (mvn sends it
on each PUT) stays on the client's hop. A deployed
Worker runs on routes of the zone of the base URL host and forwards with a same-zone fetch. A GET
or HEAD also calls `passThroughOnException`.

**Copies.** An R2 event notification rule of the home bucket (prefix `[s3.prefix/]blobs/`,
`object-create` and `object-delete`) sends to the queue `<home bucket>-edge-copy`. The consumer
acts on the current state: it HEADs the home key and, for every region at once, copies to a
region that lacks the blob or holds another size (a put with the `sha256` option; above 4.9 GiB a multipart upload with parts of
the larger of 100 MiB and the size / 10,000, its state in `copy/<hint>/<hex>.json` after each
part), or deletes the regional copies when the home blob is gone. A region that fails retries the
message after every other region finished; a region that has the blob skips it then. The cron (every 10 minutes) runs
a gap pass when `gap/request.json` is at least as new as the start of the last pass or the last
pass is older than 24 hours: it merges the key-ordered lists of the home `blobs/` and each region
(1000 keys per list), queues copies and deletes 100 per batch, keeps its cursor in
`gap/state.json` and stops before 5,000 subrequests. A pass runs only while `config.json` says
`enabled`, requires no feature that the Worker lacks, and the Worker has its queue, so a region keeps its missing
files while edge delivery is off. Every run (and every check) writes the Worker's build and features, bindings
and markers to `gap/state.json` when they changed.

**Deploy.** The server writes the Worker configuration for the settings: the routes
`<base URL host><prefix>*` of each format with `Edge` and `<base URL host>/.well-known/telesphorepo-edge/*`
(the check), the bindings `HOME` and `R2_<HINT>` of
each chosen region, the queue (producer `COPY`, consumer batches of 100, 10 retries), the cron,
the var `PREFIX` (`s3.prefix`) and `observability` on (Workers Logs of the fetch, cron and queue
runs in the Cloudflare dashboard). `GET /api/edge/worker.zip` (the download of the Edge delivery
page) is the Worker folder, ready to run: the bundle `dist/index.js` that the build of the server
made with `--define TELESPHOREPO_VERSION:"<server version>"` (`internal/edgedist`, see
**Tooling**), `package.json` (wrangler at an exact version, the scripts `provision` and
`deploy`), `bun.lock`, `scripts/provision.ts`, the README and the `wrangler.jsonc` with
`"main": "dist/index.js"` and `"no_bundle": true`. The server refuses the download when its build
holds no Worker, or one built with another server version, so a deploy from it always has every
feature that the server uses. `GET /api/edge/wrangler.jsonc` is the configuration for a checkout of `edge/`:
`"main": "src/index.ts"` and the `define` of `TELESPHOREPO_VERSION` to the server version, the
build that the Worker reports.

**The contract.** The Worker serves by the features it implements, never by the server version,
so a server release needs no Worker deploy unless it uses a feature that the deployed Worker
lacks. The contract is the objects under `edge/v1/` and what their fields mean:

- **Tolerant readers.** Both sides ignore a field they do not know. A field added later has a
  default that means the behavior from before it.
- **Features.** A feature is a name for one thing that a Worker must implement to read what the
  server writes. The Worker's build lists those it implements (`edge/src/features.ts`); the list
  only grows. The server's are `workerFeature`s (`internal/edge/features.go`), each with the text
  that ends "this server needs it to ...". There are none yet.
- **`requires`** of `config.json` names the features that the server uses now and that every
  request needs. A Worker that lacks one passes every request to the server and runs no copy
  pass.
- **`needs`** of an object (an access object, a pointer, a format of `config.json`) names the
  features that reading it takes. A Worker that lacks one passes the requests that read the
  object to the server (a pointer counts as pending, a format as one the Worker does not serve)
  and serves the rest. A format of a shape that the Worker does not know counts the same. A field
  that restricts access, which an older Worker would ignore and so serve what it must not, is
  always a need, so an older Worker fails closed.
- **A change** to what the server writes for the Worker, or to how the Worker reads it: additive
  with a default that keeps the old behavior needs nothing else; a meaning an older Worker would
  get wrong is a new feature, named in `requires` or in the objects' `needs` only while the server
  uses it; renamed keys, moved paths or a field whose old meaning changes move the objects to
  `edge/v2/`, where an older Worker finds no `config.json` and passes every request to the
  server.
- **Outdated.** The status's `worker_outdated` says why the deployed Worker sends every request to
  the server: it lacks a feature of `requires`. Then `deploy_needed` is true, the check says so, and Settings › Overview shows
  the notice for a person who can open Edge delivery.

In the folder the operator runs `bun install`, `bunx wrangler login` (once), `bun run provision`
and `bun run deploy`. Wrangler's OAuth login asks for its default scopes, which include
`workers:write`, `workers_scripts:write`, `workers_routes:write`, `zone:read` and `queues:write`;
R2 has no scope of its own there, and Cloudflare's R2 guides run `r2 bucket create`, `r2 bucket
notification create` and `queues create` after a plain `wrangler login`. In CI,
`CLOUDFLARE_API_TOKEN` (Workers Scripts and Routes edit, R2 edit, Queues edit) takes the place of
the login; wrangler reads it itself. `scripts/provision.ts` first runs `wrangler whoami --json`:
without a login or a token it stops with "Sign in first: bunx wrangler login". Provision runs
wrangler without a terminal, and wrangler cannot ask which account to use, so when the login
reaches several accounts and neither `CLOUDFLARE_ACCOUNT_ID` nor `account_id` in
`wrangler.jsonc` names one, provision stops, names `CLOUDFLARE_ACCOUNT_ID` and lists the accounts;
an account ID that the login cannot reach stops it too. Then it creates each regional bucket with
its location hint, the queue and the notification rule, skipping what exists; it refuses a config
with a custom domain, routes on several hosts or a route outside the zone it names.
`go tool task release` adds the same folder as `telesphorepo-edge_<version>.tar.gz`.

**Revocation speed.** The Cache API keeps an object per data center and `cache.delete` purges only
the local one, so `edge.access_ttl` (default 30 s, 0 to 5 min) is the bound.

| Change | Reaches the edge | What a stale edge can serve meanwhile |
|---|---|---|
| Read token deleted or expired; user disabled; grant removed; repo made private; device revoked; IP read rule added | within `edge.access_ttl` plus the write (the outbox writes within a second); within 5 minutes plus the TTL after a server crash in that second | Downloads of that repo to that credential, from a data center that cached the object |
| File deleted or replaced | at once (the pointer is deleted or pending before the commit) | nothing |
| Edge turned off | within `edge.access_ttl` | downloads that the server would also allow |

**Other rules.**

- *Repo kinds and locks.* The edge only reads, so the 405 for writes to proxies and groups, the 409
  of a repo operation and the mirror lock stay with the server (they refuse `Write`, `Delete` and
  `Mint`, never a read).
- *Formats.* maven: releases from the regions, `maven-metadata.xml*` and snapshots through the home
  pointer; checksum files are assets and serve like any file. debian: `pool/**` and `by-hash/**`
  from the regions, `Release`, `InRelease` and `Packages*` through the home pointer (apt with
  `Acquire-By-Hash` reads the indexes by hash, so most bytes come from the region). docker:
  manifests by digest and blobs from the regions, manifests by tag through the home pointer; a pull
  still gets its token from the server (`/v2/` and `/docker/token`) once, and a CI identity pull
  carries an anonymous bearer, so the edge serves it only on an anonymous-read repo. A cached proxy
  file that is not `Changing` serves from the edge; a miss goes to the server, which fetches and
  commits it and so writes the pointer for the next client.
- *Audit and logs.* The server records no download events, so none are lost at the edge. Guard
  refusals (`network.denied`) stay at the server because a guarded repo is never answered by the
  edge. Edge downloads are absent from the server's access log. The Worker never logs
  `Authorization`, cookies or query strings.
- *Uploads.* Every method other than GET and HEAD goes to the server with its body streamed.
  Cloudflare limits a request body to 100 MB on Free and Pro and 200 MB on Business, with or without
  a Worker; Maven, raw, Debian, PyPI and NuGet uploads of the CLI and the large files of a web UI upload use the resumable upload routes in chunks,
  and Docker pushes use `docker.direct_uploads` (the upload `Location` points at the DNS-only host
  of `docker.upload_url`, which has no Worker route).
- *Pass-through errors.* A request with a body never relies on `passThroughOnException`: when the
  server fetch throws after the body was read, the Worker answers the 502 problem document itself.
  `passThroughOnException` does not cover a CPU or memory overrun (error 1102), so the Worker keeps
  its CPU per request small. On Workers Paid there is no request cap: cost grows with traffic and
  the edge does not stop.

**Known difference.** The server refuses even a valid token from an address that it blocks after
failed attempts (`authenticate` checks `limits.blocked` first); that block lives in server memory
and changes every few seconds, and copying it to the edge would cost a write per change against the
one-write-per-second key limit. The edge serves a valid read token from such an address. A valid
256-bit token is not a guess, and every wrong guess still reaches the server and counts.

**Operator flow.** In **Edge delivery** the admin picks the home hint and the regions and saves; the
page probes every bucket first. The admin downloads the edge Worker from the Deploy section,
unpacks it, and in its `telesphorepo-edge` folder runs `bun install`, `bunx wrangler login`,
`bun run provision` and `bun run deploy`, then turns `edge.enabled` on (the page keeps its switch off
until the deployed Worker has reported its build; the page runs the check of the Worker when it
opens and every 30 s until then or while the Worker is outdated, and **Check now** runs it at once). After each region change
the admin downloads the Worker again (its `wrangler.jsonc` has the new bindings) and runs
provision and deploy; after a server release that the Worker is outdated for (Settings › Overview
says so), downloads it again and runs deploy. The server backfills the pointers and the gap check copies the blobs; a region
serves when the page says ready. A removed region needs a deploy and a manual bucket delete in
Cloudflare. Download snippets for private repos use a read token, never a password.

**Costs and limits** (Cloudflare prices and limits as of October 2026; the monthly figure is an
estimate, not a measurement).

- Workers Paid: $5 a month with 10 M requests and 30 M CPU ms, then $0.30 per million requests and
  $0.02 per million CPU ms; subrequests are not billed. Limits: 30 s CPU per request by default
  (5 min at most), 128 MB per isolate, 10,000 subrequests per invocation by default, 6 connections
  waiting for response headers at once, 1,000 Cache API calls per request.
- R2 Standard: $0.015 per GB-month; class A $4.50 per million (put, copy, list, multipart and its
  parts); class B $0.36 per million (get, head); `DeleteObject` free (`DeleteObjects` is counted as
  free, an estimate); egress free; the free tier is 10 GB, 1 M class A and 10 M class B a month.
  A single put takes up to 4.995 GiB, a multipart upload 10,000 parts of equal size, one write per
  second per key (429 above); reads and lists are strongly consistent. Event notifications: 100
  rules per bucket, no overlapping rules.
- Queues: 1 M operations a month included, then $0.40 per million; a message costs a write, a read
  and a delete per 64 KB, and each retry a read; 5,000 messages per second per queue, batches of
  100, 15 minutes wall time per consumer run.
- Operations: a download reads one pointer (one per member for a group), one blob, and 0 to 3
  access objects on a Cache API miss. A stored file puts one home pointer, and one per region for
  an immutable path (a replacement adds a `{pending}` put per bucket). A new blob costs one queue
  message and one put per region. The idle cron costs one class B read per run. A far-bucket read
  takes roughly 100 to 300 ms against a few ms from a near bucket (an estimate).
- Example estimate: 10 M downloads and 12 M Worker requests a month, 200 GB stored, 4 regions,
  100,000 new files and 50,000 new blobs come to about $28 a month: $5 Workers base, $0.60 for
  requests above 10 M, $0 CPU, $7.20 class B, $0 class A and Queues, $14.85 storage (1,000 GB with
  the home copy and 4 regional copies, minus 10 GB free) and free egress. Storage of the regional
  copies is the largest cost, so fewer regions cost less.

## Nexus sources

An admin adds a Sonatype Nexus 3 server as a **source** on the **Nexus** page
(`internal/nexus`). The server mirrors the mapped Nexus repos with repeated syncs. A mirrored
repo is read only. A **switch-over** of one repo ends its mirroring and makes it writable; the
other repos keep copying. When every repo has switched over, the source is **cut over**. Nexus
is read only through its REST API.

Shapes in the routes above:

```
Source       = {id, name, base_url, state: "mirroring"|"cut_over", created_at, last_error, last_sync: Sync|null,
                cutover_sync_id, cutover_failed,   (both 0 before cut-over)
                status: "no_repos"|"syncing"|"up_to_date"|"failed"|"cut_over",
                next_sync_at|null, running_sync_id (0: none), repo_count, sync_interval (seconds),
                copying, switched_over}                      (mapped hosted repos that copy, that switched over)
SourceDetail = Source + {repos: [SourceRepo], nexus_error}
SourceRepo   = {name, format, type, supported, reason, offline, public, target_format, target,
                remote_url, remote_username,                 (a proxy)
                members, members_known, target_members,      (a group)
                questions: [Question], facts: {...},         (from the plan of the target format)
                switch_over: SwitchOver|null,                (a mapped hosted repo; null while it copies)
                rehearsals: [repo name],                     (the rehearsal repos of the target)
                moving: [repo name], merge_id}               (its moving repos; a merge into it that runs or failed, 0: none)
SwitchOver   = {sync_id, failed, switched_at}
MergePlan    = {from, into, format, source_id, source_name, copying, files, copy, skip, rebuild, labels,
                conflicts: [MergeConflict] (the first 1000 by path), conflict_count,
                blocked: [{path, reason}] (the first 100), blocked_count,
                people, pipelines, groups, from_redirects, into_redirects, busy, merge_id}
MergeConflict = {kind: "file"|"label", title, value, target_value,    (a label: an npm dist-tag)
                path (the key of its choice: a file's path, or "label:<key>"),
                size, sha256, updated_at, target_size, target_sha256, target_updated_at}
Merge        = {id, from, into, name (the merged repo now; "" when gone), redirect, keep, format,
                source_id, source_name, state, step, error,
                sync_id|null (the final sync; null when the target copied no more),
                files, done, copied, skipped,
                rebuilt, kept, taken, groups, started_by, started_at, finished_at|null}
Question     = {key, label, help?, type: "choice"|"secret", options?: [{value, label}], required, default?}
Mapping      = {source_repo, target, create, anonymous_read,
                answers?: {key: value},                             (answers to the questions; a new target)
                remote_username?, remote_password?,                 (a new proxy target)
                members?: [Nexus repo names]}                       (a group whose members Nexus did not give)
Sync         = {id, source_id, state: "running"|"done"|"failed"|"cancelled",
                trigger: "save"|"manual"|"schedule"|"cutover", started_by (user name, "" for the scheduler),
                started_at, finished_at|null, error, checked, imported, skipped, ignored, failed, bytes,
                repo_count, repos_done, current_repo, repos?: [SyncRepo],
                merge_id, merge_from, merge_into}   (the merge whose final sync it is: 0 and "" for none)
SyncRepo     = {source_repo, source_format, target, state: "pending"|"running"|"done"|"failed"|"cancelled",
                error, checked, imported, skipped, ignored, failed, bytes, active_at|null, previous_checked}
Failure      = {source_repo, path, reason}
LogLine      = {id, at, source_repo, level: "info"|"warn"|"error", message}
```

`type` is `""` for a mapped repo that Nexus did not list. `target` is `""` for a repo that is not
mapped. The `Sync` counters are the sums over its repos; `checked` is imported + skipped +
ignored + failed. `previous_checked` is the `checked` of the newest earlier walk of the same repo
that reached the end (0 when there is none); the UI divides by it to estimate the progress,
because Nexus gives no total count. Errors are problem documents with 400 for invalid input or refused
credentials, 404, 409 (exists, running, not running, cut over, still mapped) and 502 when Nexus
is unreachable.

- **Status.** The server derives `status`: `cut_over` after cut-over, `syncing` while a sync
  runs, `no_repos` without a mapping, `failed` when the last sync failed (`last_error`), else
  `up_to_date`. `next_sync_at` is `last_sync_at` plus `nexus.sync_interval` (now for a source
  that never synced); it is null without a mapping, after cut-over and while a sync runs.
- **Planning hook.** An `Importer` may have `Plan(ctx, src, target, answers)`: `src` is the Nexus
  repo (`format.SourceRepo{Name, Format, Kind, Attributes}`, `Attributes` the raw
  `repositorySettings` item; it can hold the apt key pair and is never logged or shown), `target`
  the target repo (nil in the source detail, ID 0 for a target that the save creates). It answers
  a `TargetPlan`: the config of a new target or the merged config of an existing one, repo field
  values (`Fields`), the `Questions` the UI asks, the `Facts` the detail shows, and `Prepare`, run
  before the repo save. The detail calls it with nil answers; the save calls it with the mapping's
  `answers`, checks every plan before it creates any repo, then saves through `repos.Create` or
  `repos.Update`. Nexus names no format and parses no format setting.
  - debian (`apt`): `facts` `{distribution, key_in_source, key_encrypted}`. A new target gets the
    default config with the Nexus `apt.distribution`; an existing hosted target without a
    distribution gets it (a repo update). A new hosted target asks `signing_key` (a choice: an
    existing key; `new`, a new key named after the repo; `source`, the key pair of the Nexus
    repo). The default, and the answer when the save names none, is `source` when Nexus gives
    the key pair, so apt clients that trust the Nexus key keep trusting the repo, else `default`.
    When the Nexus key is encrypted it also asks `signing_passphrase` (a secret, `show_when`
    `signing_key` = `source`: a question's `show_when {key, value}` asks it only while that other
    answer holds, and the UI neither shows nor sends it otherwise). A passphrase with another
    answer than `source` is 400, so it is never dropped without a word.
    `source` opens the key pair with the passphrase (400 when it does not) and `Prepare` imports it
    as `nexus-<target>` (a key of that name with the same fingerprint is used again; another one
    is 400). The answer becomes the repo field `signing_key`.
- **Importers.** A format with a `format.Spec.Importer` lists the Nexus format ids it takes
  (`Sources`). The sync offers each file to `Importer.Run` (see **Byte pipeline**: the target
  path, the skip check, the download with the size limit and the checksum check, then `Store` or a
  `Commit` with source `import`). The outcome is `imported`, `skipped` (already present with the
  same bytes) or `ignored` (not a file of this format).
  - maven (`maven2`): every file at its path, under the format's `Admit`. Identical bytes are
    skipped; a different release file fails; `maven-metadata.xml*` and SNAPSHOT files are replaced.
  - raw (`raw`): every file at its path. Different bytes fail when the repo denies overwrite.
  - debian (`apt`): `pool/**/*.deb` through the Debian commit into component `main` of the repo
    config's `distribution`; the target of a package that the distribution holds already is its
    pool file, so it is skipped without a download. Other files are ignored. A target without
    `distribution` fails each package.
  - npm (`npm`): each `<name>/-/<file>.tgz` at its path, through the commit of a version with the
    manifest read from the tarball's `package/package.json` (the version cleaned as npm cleans
    it). The dist-tags of the package at the source (`-/package/<name>/dist-tags`, read once per
    package and run through `ImportItem.Cache`) that name the version are set; without them
    `latest` is the highest release. Nexus's packuments are ignored.
    A version that exists with the same tarball is skipped, with other bytes it fails.
  - docker (`docker`): `v2/<image>/manifests/<tag>` to the tag path and
    `v2/<image>/manifests/sha256:<hex>` to the manifest path; the blobs (`v2/-/blobs/...`) are
    ignored, because each manifest brings what it names. `Store` parses the manifest (the media
    type of `ContentType`, else the JSON `mediaType`), fetches through `Fetch` each blob
    (`v2/<image>/blobs/sha256:<hex>`, spooled under `max_upload_size`; a blob that another image of
    the repo links is linked without a download; 6 blobs of a manifest download at once) and each
    child manifest of an index (`v2/<image>/manifests/sha256:<hex>`, recursively, one after the
    other) that the image lacks, checks each digest, and
    commits the links, the child manifests, the manifest and the tag in one `CommitAll` with source
    `import`. So the page order of Nexus does not matter. Every download asks for the four manifest
    types (`Importer.Header`), so Nexus sends a manifest as it stores it. A Nexus Docker repo with
    the Docker Bearer Token Realm answers each download with a Bearer challenge; the client
    answers it with `proxy.Answer` and the source's credentials and keeps the newest token of each
    Nexus repo while it lives. A Nexus docker proxy becomes a docker proxy with the Nexus remote
    URL.
  - nuget (`nuget`): an asset `<id>/<version>` (a Nexus nuget hosted repo) or
    `<id>/<version>/<id>.<version>.nupkg` to the flat-container path of the `.nupkg`; `Store`
    pushes it through the push of the format (its `.nuspec` gives the id and the version). Other
    files are ignored.
  - Imported files have no uploader. The import is the owner of the repo's `mirror` operation, so
    it writes while uploads answer 409; each imported file records `file.upload` with the actor
    `nexus`.
- **File dates.** `format.ImportItem` carries the Nexus blob dates of each asset: `CreatedAt`
  from `blobCreated`, `UpdatedAt` from `blobUpdated` (`lastModified` when it is missing). The
  commit stores the file with them (the origin's `Created` and `Updated`); an insert writes
  `created_at` and `updated_at`, an update writes `updated_at`. A skipped file whose dates differ
  gets the Nexus dates (`assets.Pipeline.SetTimes`), so a later sync corrects files that an older
  sync stamped with the import time. Files a format generates (Debian indexes) keep the time they
  are written. `lastDownloaded` and `uploader` are not kept.
- **Repo list.** The client reads `/service/rest/v1/repositorySettings`, or
  `/service/rest/v1/repositories` when that is refused (then group members and the apt key pair
  are unknown). A repo is `supported` when the importer of a format takes its format and this server has its
  kind: hosted for every format, proxy and group where `format.Spec.Kinds` lists them. A repo that Nexus marks `"online": false` is not supported, with the
  reason "Offline in Nexus. Bring it online in Nexus to copy it." A sync fails a mapped repo that
  went offline with the same words.
- **What Nexus gives** (checked against `sonatype/nexus3:3.96.4` with an admin account).
  `repositorySettings` returns every repo with its settings in one call, so the server makes one
  call and no call per repo.
  - apt hosted: `aptSigning.keypair` is the armored private key. Nexus does not give its
    passphrase. The Debian plan reads it (`facts.key_encrypted` says whether it needs one).
  - group: `group.memberNames` in Nexus order.
  - proxy: `proxy.remoteUrl`, `proxy.metadataMaxAge` and `negativeCache.timeToLive` (minutes,
    -1 for forever) and `httpClient.authentication.username`. Nexus does not give the password.
  - asset list: `blobCreated`, `blobUpdated`, `lastModified` per asset.
  - An offline repo still lists its assets, but each download answers 503.
- **Who can download.** For each supported repo, the source detail asks Nexus without
  credentials: it takes the first asset of the repo (with the source's account) and sends a
  `HEAD` of its download URL; 200 is `public: true`, 401 or 403 `false`. A repo with no asset
  sends a `HEAD` of a path that does not exist: Nexus answers 404 to an anonymous reader that may
  read the repo and 401 to one that may not. 8 checks run at a time, and a result is kept in
  memory for 5 minutes. A failed check counts as private.
- **Source.** Create and PATCH check the connection first. The credentials are stored encrypted in
  the source row and are never returned or logged. Error messages name the request and the HTTP
  status only. A new source has `last_sync_at` 0 and no mapping, so the scheduler skips it.
- **Mapping.** `PUT .../repos` replaces the whole mapping. Every entry is checked first: the
  Nexus repo exists, is supported and is listed once, and the target is of the same kind and
  accepts the format, or is created (`create: true`) in the first format (in module order) that takes it. A target
  mirrors at most one source. `anonymous_read` applies to a target that the save creates; an
  existing target keeps its setting. A created target:
  - hosted: the config and the field values of the target format's plan (the format's default
    config without a plan). The plan's `Prepare` runs first.
  - proxy: the Nexus remote and times, and the credentials `remote_username` and
    `remote_password` (sealed) when the mapping gives them.
  - group: the targets of its picked members in Nexus order (`members` when Nexus did not give
    them). A group needs one picked member. A save again replaces the members of an existing
    group.

  An existing target gets the config and the field values that its plan gives (a repo update). Each
  hosted target gets the operation `mirror` (state `held`, see **Repo operations**), whose message
  is `Uploads and deletes start after the switch-over from Nexus server <name>.`; a target with another operation cannot be
  mapped (400). A hosted target that leaves the mapping loses the operation and keeps its files.
  A repo that switched over keeps its mapping whatever the save holds, so it never copies
  again: an entry for it must name its target (else 400), and no other Nexus repo can map to that
  target (400).
  Targets are created and changed through `repos.Service.Create` and `Update`. Targets that the request creates stay when its
  transaction fails. After the mapping, the save starts a sync with `trigger: save`.
- **Sync.** A save, `POST .../sync` (`manual`), the scheduler (`schedule`) and the cut-over
  (`cutover`) start a sync. One sync runs per source (unique partial index). The sync copies the
  hosted part of the mapping that has not switched over into `nexus_sync_repos` (the final sync
  of one repo's switch-over: only the Nexus repos that copy into that repo); proxies and groups
  have no files to copy.
  At the start the runner lists the Nexus repos once and fails each mapped repo that is offline.
  The repos sync one after the other. For each other repo it pages
  `GET /service/rest/v1/assets?repository=X` from the saved continuation token and offers each
  asset to the importer of the target format. Nexus sets the page size and takes no page size
  parameter (10 assets before 3.74, larger pages since). A reader fetches the pages one after the
  other, up to 8 pages ahead of the oldest page not saved yet, and 16 workers import the assets of
  the repo at the same time. A failed page is retried 3 times (2 s, 4 s, 8 s), except on 401/403.
  Downloads use the asset's `downloadUrl` only when it is on the base URL's host, else
  `{base}/repository/{repo}/{path}`. A download whose body sends no bytes for 1 minute fails the
  asset. The runner takes the results in page order: one transaction per page saves the
  failures, the counters (`checked`, `bytes` counts the bytes read for imported files),
  `active_at`, the next token and the page's log line, once every asset of that page and of the
  pages before it is done. So the saved token always follows a done prefix, and a resume redoes
  the pages after it. A file that is already present with the same bytes is skipped, so a later
  sync copies only new and changed files. Imports at the same time are safe because the commit
  takes the path locks and the extra locks of the format (the package of npm, Debian, Cargo and
  NuGet), and no importer depends on the order of the assets. Each repo's run keeps a
  `format.RunCache` (`ImportItem.Cache`) for what its items share.
- **Log.** The runner writes a line for: the start ("Started after the repositories were
  saved.", "Started by sam.", "Scheduled sync started.", "Final sync before the switch-over
  started by sam.", "Final sync before the switch-over of maven-releases started by sam."), the
  proxies it skips, each repo's start or resume ("Started.", "Resumed
  after a restart."), each page that copied a file ("Checked 100, copied 12 (3.4 MiB)."), each
  repo's end ("Done. Copied ..., skipped ..., failed ..." or "No new files. ..."), each retry of
  a page (warn), each failed repo (error), and the end ("Finished in 6 minutes. 34 new files (2.1
  MiB).", "Cancelled by sam.", "Failed: ..."). A line of one repo carries the repo in
  `source_repo`, and its message does not repeat it. A line holds counts, never a path, a URL, a
  password or a token. `GET .../log` reads the lines through `nexus_sync_log_sync`; the `repo`
  filter applies in Go.
- **Retention.** A new sync deletes the syncs of its source after the newest 100, with their
  repos, failures and log lines (cascade). The switch-over syncs (`trigger: cutover`) stay.
- **Scheduler.** It checks every `min(nexus.sync_interval, 1m)`. It reads the interval from the
  live settings. It starts a sync for each mirroring source whose `last_sync_at` is at or before
  now minus the interval, up to 100 per check; a source without a mapping does not start. Every
  sync that ends sets `last_sync_at`.
- **Sync end.** The sync is `done` when every repo finishes, also when some files failed (they
  are in the failure list). It is `failed` when a whole repo failed and `cancelled` after
  `DELETE`. A shutdown leaves the sync `running`; it continues from the last saved page at the
  next start.
- **Switch-over.** Each mapped hosted repo copies from Nexus until it switches over
  (`nexus_switch_overs`; the source's `copying` and `switched_over` and each repo's
  `switch_over` derive from it). `POST .../repos/{repo}/switch-over` runs a final sync with
  `trigger: cutover` of the Nexus repos that copy into the target of `{repo}`; `POST .../cutover`
  (Switch over everything) runs one of every hosted repo that still copies. The sync succeeds when
  no repo failed, also when some files failed. Then one transaction sets the sync `done`, gives
  each of its repos a switch-over row (the sync and the repo's failed file count) and ends the
  `mirror` operation of its target, so publishes start there and later syncs skip it. When no
  mapped hosted repo copies any more, the same transaction sets the source `cut_over`, empties
  its credentials and saves `cutover_sync_id` (the sync) and `cutover_failed` (its failed file
  count). The failed files stay listed through `GET /api/nexus/syncs/{id}/failures`. If a repo
  failed (Nexus unreachable, a listing error), the sync is `failed`, nothing switches over, and
  `last_error` says `the switch-over stopped: ...; nothing switched over, and they keep copying
  from Nexus`. A cancelled switch-over changes nothing. A restart continues an interrupted
  switch-over. A switched-over repo cannot copy again. After cut-over, PATCH, PUT repos, sync and
  the switch-overs answer 409, and the sync history stays. Groups and proxies need no
  switch-over: they serve from this server already.
- **Rehearsal repositories.** `POST .../repos/{repo}/rehearsal` creates `<target>-rehearsal`
  through `repos.Service.Create`: a private hosted repo of the target's format with its config
  and field values, the description "Publish here first, to try the builds before the
  switch-over.", and the repo field `rehearsal_for` (the target's name; `repo_rehearsals` holds
  the ids, the module keeps them in memory). Only this route sets the field: a create or an update
  through the API that names it is 400. Each person with `repo.publish` granted on the target
  gets `repo.download` and `repo.publish` on the rehearsal. A taken name is 409 with `{repo,
  rehearsal}`. **Empty it** (`POST /api/nexus/rehearsals/{name}/empty`) begins the operation
  `empty` and runs the removal of the repo delete (`assets.Pipeline.Remove` with the prefix `""`,
  the format's hooks included) as a task of the module, without the row delete: the settings,
  the CI pipelines and the grants stay. **Delete** is the repository delete (`POST
  /api/repos/delete`). The rehearsal's row goes with either repo (purge).
- **Gradual move.** For a repo that many projects publish to: the projects move one at a time to a
  new repo while builds read both, and the new repo then merges into the Nexus copy.
  - **Move it project by project** (`POST .../repos/{repo}/move`) creates the moving repo through
    `repos.Service.Create`: a hosted repo of the target's format with its config, field values and
    visibility, the description "Projects publish here while they move from <target>, one at a time.
    Merge it into <target> when the last one has moved.", and the repo field `moves_into` (the
    target's name; `repo_moves` holds the ids, the module keeps them in memory; only this route
    sets it). Each person with `repo.publish` on the target gets `repo.download` and `repo.publish`
    on it. With `group`, the group `<target>-all` reads the new repo first, then the target, with
    the target's visibility; each person with a grant on the target gets `repo.download` on it.
    Both names are checked before anything is created.
  - **Plan.** `GET /api/nexus/merges/plan` walks every file of the source by path (keyset pages of
    `assets`) and looks up the target's file at the same path (point lookups); it writes nothing.
    A merge takes two hosted repos of one format; the target is a target of a Nexus source
    (copying or switched over), the source is not. Each file of the source is **copy** (the target
    has none, or the format's `Admit` takes the replace, e.g. a SNAPSHOT file), **skip** (the same
    SHA-256), **conflict** (other bytes that `Admit` refuses with 409 or 403, e.g. a Maven release;
    for a format whose `Importer` has `Store`, any other bytes), **rebuild** (a metadata or index
    file: a format with `Importer.Store` builds its own indexes; for another format a file that its
    group `Merge` matches is merged with the target's and its checksum files are written from the
    result, and another metadata file is decided as an artifact), or **blocked** (with
    `Importer.Store`, a path that `Target` does not map to itself; or another refusal of `Admit`).
    A format's `format.Spec.Carry` changes this: with `Prepare`, its files go by their digests
    even when its importer has `Store`, `Unit` keeps the files of one unit in one commit and
    `Prepare` sets their `Meta`, `Pinned` makes a path that both hold with other bytes a conflict
    although `Admit` replaces it, and `Labels`/`SetLabel` carry named values that are not files.
    docker: each image commits as one unit (its links, manifests and tags, each marked an object of
    that commit, so a manifest finds its blobs and child manifests and a tag its manifest), by
    digest, so no bytes are copied; a tag that both hold with other digests is a conflict. npm: the
    tarballs go through the import, and each dist-tag of the source is a label `<name> <tag>`:
    for a package that the target does not have, or a tag that it lacks, the source's version
    wins; a tag of a package in both with another version is a conflict (`label:<name> <tag>`).
    Before the first file goes in, the merge saves which labels the target takes (as choices of
    take), because the copy changes the target; after the copy (step `access`) it sets them
    (`SetLabel`, through the format's package change, so the packument follows).
    A merge with a blocked file does not start. The plan also lists the people with a grant on the
    source, its CI pipelines, the groups that read it, the redirects of both, why it cannot start
    now (`busy`: an operation other than the target's `mirror`) and a merge into the target that
    runs or failed.
  - **Start** (`POST /api/nexus/merges`) takes the plan of now again: every listed conflict needs a
    choice in `choices` (`keep` the target's file; `take`: delete it there and take the source's),
    or `otherwise` decides each conflict that `choices` does not name. One transaction stores the
    merge (`nexus_merges`, `nexus_merge_choices`), ends the target's `mirror` operation and begins
    the `merge` operation on both repos, so publishes to both answer 409 until it ends.
  - **Steps.** Each step is saved before the next one starts, so a restart continues at the step
    it was at:
    1. `sync`: a final sync of the target (`trigger: cutover`, the Nexus repos that copy into it),
       after a running sync of the source ends. The transaction that creates the sync saves its id
       in the merge, and the runner ends that sync `done` without the switch-over ("... The merge
       goes on."). Its first log line is "Final sync of the merge of <source> into <target>.", and every
       sync of the API names the merge it belongs to (`merge_id`, `merge_from`, `merge_into`, through
       `nexus_merges_sync`), so the Nexus page shows it as "Final sync of the merge into <target>",
       never as a switch-over. A target that no longer copies skips the step.
    2. `copy`: the files of the source after `cursor`, in path order: 100 files per `CommitAll` by
       their digest (`Body.Blob`, source `import`, the source's dates, uploader and CI build), so no
       bytes are copied; a format with `Importer.Store` takes each file through `Importer.Run` (it
       reads the blob); a merged metadata file and its checksums are written one by one; a conflict
       as its choice says (take: a `Remove` of the target's file first). The cursor and the counters
       are saved after each commit and at least every second. While a merge is past its final sync,
       the syncs of the source skip its target. A conflict that the plan did not list and that no
       choice decides fails the merge.
    3. `access`: each person's grants on the source are added to their grants on the target
       (`auth.Service.SetPermissions`), each CI pipeline of the source also publishes to the target
       (`publish.Service.AddPipelinesOf`, one `ci.pipeline.update` each), and each group that reads
       the source but not the target reads the target in its place.
    4. `switch`: one transaction gives each Nexus repo that copies into the target its switch-over
       (the final sync and its failed file count) and cuts the source over when no mapped hosted
       repo copies any more, as a switch-over does; its line goes to the final sync's log.
    5. `retire`: `repos.Service.Retire` renames the source to `<name>.merged-<id>`, makes its name
       and its redirects redirects of the target, and in the same transaction ends the source's
       `merge` operation and begins its `delete` operation (owner `core`).
    6. `rename` (keep `source`): `repos.Service.Rename` gives the target the source's name; the
       target's old name becomes the redirect.
    7. `done`: the target's operation ends and the merge is `done`; the run that retired the source
       then deletes it as the repo delete does (after a restart, `core.ops` does). The group
       `<target>-all` loses the source with its delete and keeps reading the target.
  - **Failure.** A failed step sets the merge and both operations `failed` with the error. Before
    the switch-over the target keeps copying from Nexus (its syncs include it again) and the source
    keeps its files. **Resume** (`POST /api/repos/{name}/operation/resume` on either repo) runs the
    merge again from its step; a merge that failed in `sync` or `copy` while the target still copies
    runs a new final sync first, then copies the files after its cursor. **Stop**
    (`DELETE /api/repos/{name}/operation`) ends both operations, gives a target that still copies
    its `mirror` operation back and sets the merge `stopped`; the files copied so far stay.
- **Delete.** `DELETE .../sources/{id}` removes the source, its mapping, syncs, failures and
  log. The repos and their files stay.
- **UI.** The admin-only **Nexus** pages (`/#/settings/nexus`, `/#/settings/nexus/{id}`). The
  list is a card per server: its name, status badge, address and number of repositories, and the
  next check; a syncing source shows its progress line instead. The source page has, top to bottom: the header (status badge and line, Sync now
  or Cancel the sync, Switch over everything, Delete), Repositories to copy (one pick table of every Nexus repo
  in the order a save sets them up: repos and proxies, then groups, then the repos this server
  cannot copy, muted with their reason; a filter above 10 repos; Kind, State (Copying, Switched
  over, Copied for a proxy or a group, New repository, New proxy, New group, Stops copying,
  Members change, or Not copied), Name here (with its rehearsal repos under the name), Who
  can download, and the choice question of the target format (Debian: Signing key); the row
  actions of a copied repository: Switch over and Rehearse publishing, and of each rehearsal
  repo: Empty it and Delete, each behind a confirm that lists its effects; a switched-over row
  cannot be unpicked; a proxy
  shows its remote and a group the members it reads and leaves out, and picking a group picks its
  members; the secret questions and the remote credentials of the new repos below the table; one
  button "Copy N repositories" or "Save", with a sentence of what the save does, saves and opens
  the sync), the sync
  detail (per repo "copied (size) of checked", last activity, a bar with "about N%" when an
  earlier walk exists, "No progress for N minutes" after two minutes; the log, a list of its
  lines (the message, then the time and the repo), which reads its new lines at each refresh of the sync
  (every 2 s) and filters by a clicked repo; the failed files as a list: the path with a copy
  button, then the repo and the reason), the sync history (a list: when it started, then how, how
  long it took, the new files and their size; the failed count as a badge when there are any; the
  state at the end) and the connection with the save bar. While a repo has the `mirror`
  operation, its page shows the operation's message under the header to the people who could upload or delete.
  The row actions also hold **Move it project by project** (a short form: the name, `<target>-new`
  by default, and the group `<target>-all` when the format has groups), and per moving repo Open it and Merge into <target>…;
  the moving repos show as a "moving" link after the target's name, a merge as a "merge" link. The
  State column shows on a phone too, in the row's meta line. The **merge plan**
  (`/#/settings/nexus/merge?from=&into=`): the counts of the files, the files the format cannot
  take, the conflicts as a list with Keep or Take per row and Keep all, Take all; which name stays
  (each choice says who changes what), what else changes, and Start the merge behind a confirm.
  The **merge** (`/#/settings/nexus/merges/{id}`): its steps with the copy's progress, refreshed
  every 2 s while it runs; a failed one with Resume and Stop; a done one says what changed and
  gives the setup line of the merged repo (the Set up a project card). A moving repo's header says
  "Moving into <target>" (a link) and, for a holder of `nexus.manage`, has Merge into <target>….
  A rehearsal repo's header says what it rehearses ("Rehearsal for maven-releases", a link). A new
  CI pipeline that starts from a repo that still copies from Nexus (`?repo=`) has its rehearsal
  repo chosen in place of it, and the pipeline's repository list shows each rehearsal repo first,
  right before the repo it rehearses.

## Group and proxy repositories

A format takes proxy repos when its `format.Spec.Kinds` lists `proxy` (Maven, Debian, Docker, npm,
PyPI, Cargo, NuGet; it then sets `Classify` and `ProxyProbe`) and group repos when it lists `group`
(Maven, Docker, npm, PyPI, Cargo, NuGet). npm, PyPI, Cargo and NuGet render their group documents
themselves: the first member that has a package owns its name (`assets.Reader.Owner`, see each
format's section). `repos.Service` refuses a kind that the format's
`format.Spec.Kinds` does not list (400). Neither kind takes uploads, publish sessions or CI pipelines:
minting a session for one answers 409 with the read-only text, and a CI pipeline that lists one
answers 400. A Nexus mapping maps a Nexus hosted, proxy or group repo to a target of the same kind.

**In memory.** `repos.Service` keeps the members of each group and the remote of each proxy
(credentials decrypted) in memory. It loads them at start (`Load`) and changes them after each
write: a group create or member change, a proxy create or change, and a repo delete (the member
leaves its groups through the cascade). Request paths never read `repo_group_members` or
`repo_proxies`. A proxy's last answer is written to `last_ok_at` at most once a minute; an
error is written each time. Credentials that do not decrypt at start (a changed master key)
are dropped from memory, and the server logs the repo ids.

**Groups.**

- A group holds 1 to 50 members, each a hosted or proxy repo of the group's format; groups do
  not nest. A repo can be in several groups. A group with no member left (its members were
  deleted) stays and serves nothing.
- A read of a group (e.g. `GET /maven/{group}/{path}`) needs read access to the group (the read
  gate and the group's IP rules). The core read path (see **Byte pipeline**) serves every format
  that takes groups. It tries the members in order and skips a member that the reader cannot read
  (`access.Engine.Allows` with `Read`: the reader's access and the guards, the member's read rules,
  both count; no audit event). The first member that has the file serves it. A proxy member fetches from its remote
  only when no member before it has the file. No member: 404, or 502 when a proxy member's remote
  failed. The members and the reader's grants on them are read once per request (`repos.Remember`
  for the members, `auth.Service.WithGrants` for the grants, one query).
- Stored copies first (`Reader.ResolveLive`): before a proxy member asks its remote, the read
  checks the stored rows of the members in read order, as far as the answer is the same as the
  read in order. A hosted member without the file and a proxy member that refuses the path do not
  serve it. A proxy member without a stored copy of a path that names its bytes
  (`assets.Class.Addressed`: OCI blobs and manifests by digest) would serve the same bytes as any
  stored copy, so the pass goes on past it, and a later member's stored copy serves without a
  remote request. The pass stops at a proxy member that would fetch a changing file or an
  immutable file that does not name its bytes (a Maven release: a later member may hold other
  bytes at the same path, and the first member wins); the read in order goes on from there.
- A merged file asks its members and reads their files at the same time, and merges in member
  order. The merged bytes stay in memory (1,024 files, 128 MiB) under the group, the base path and
  each member file's SHA-256, so its checksum files and the next request reuse them until a member
  file changes.
- Maven's `Merger`: an artifact-level (or group-level) `maven-metadata.xml` merges over the
  readable members that have it, each read in the charset its XML declaration names (the Gradle
  Plugin Portal declares US-ASCII; a member file that does not parse is left out): the union of the versions in Maven order, the newest `latest`,
  `release` and `lastUpdated`, and the union of the plugins by prefix. Its `.md5`, `.sha1`,
  `.sha256` and `.sha512` are computed from the merged bytes. With one such member, that member's
  own files serve (a checksum file it lacks is computed from its file). A version-level file (under
  a `-SNAPSHOT` folder) comes from the first member that has it.
- A format that renders its metadata documents from the members of a group picks one member per
  package name with `assets.Reader.Owner`: the first member in read order that the reader can
  read and that has the name (the format's own check). That member serves every version of the
  name, so a later member cannot shadow it. A member whose check fails is skipped with a warning
  in the log; `Owner` answers `ErrRemoteFailed` when no member has the name and a proxy member's
  remote failed.

**Proxies** (`internal/proxy`).

- The remote URL is http or https, has no user, query or fragment, and ends with `/`. A new proxy
  checks its remote once: a GET of the format's probe (`format.Spec.ProxyProbe`: the root for
  Maven, `dists/<distribution>/InRelease` for Debian, with `stable` when the config names no
  `distribution`, `/v2/` for Docker). The root passes with any answer except 401, 403, 407 and 5xx;
  a probe file must exist. A format with `RemoteAuth` answers a 401 challenge of the check too; a
  token realm that refuses the credentials fails the check ("the remote refused the username and
  password"). `force: true` skips the check (`proxy.Check`).
- `format.Spec.Classify(repo, remote, path)` sorts a path of a proxy repo (its config in
  `repo.Config`, its remote in `remote`) into an `assets.Class`: `Changing` (checked
  again after `metadata_ttl`), `Remote` (the path at the remote: relative to `remote_url`, or from
  the root of its host when it starts with `/`; `""` is the local path), `Files` (an absolute
  http or https base URL that `Remote` is relative to instead of `remote_url`, for a registry
  that serves its package files from another host; a format takes it only from its repo config,
  checked with `proxy.FilesURL`, never from a remote document), `Header` (extra request
  headers, e.g. `Accept`), `KeepType` (the remote's `Content-Type` becomes the file's content type)
  and `Unchanged` (nil: a conditional GET; set: a HEAD with the same headers, and a 200 answer for
  which `Unchanged(head, cached)` is true only sets `checked_at`, else a GET). Maven
  `maven-metadata.xml*` and files under a `-SNAPSHOT` folder change, the rest is immutable;
  Debian `pool/**` and `dists/**/by-hash/**` are immutable, the rest of `dists/**` changes, other
  paths are refused (404). Docker: see **Docker registry**.
- A read goes through the usual gate. A cached file that is immutable, or changing and checked
  within `metadata_ttl`, serves from storage (with the presigned redirect on S3). Otherwise the
  server fetches the remote path, with `If-None-Match` (the stored ETag) and
  `If-Modified-Since` for a cached file; a 304 sets `checked_at`. A format without `RemoteAuth`
  sends the remote's credentials as HTTP Basic on every request. A format with `RemoteAuth`
  sends them only in the answer to a challenge: a 401 goes to `RemoteAuth` once, and the request
  is sent again with the headers it returns. `proxy.Answer(ctx, client, challenge, user, pass)`
  is the answer of the registry token protocol: for `Bearer realm,service,scope` it GETs
  `realm?service=&scope=` with the credentials as Basic and returns `Authorization: Bearer
  <token>` with its life (`expires_in`, 60 s without it); for `Basic` it returns the Basic header.
  One fetch per (repo, path) runs at a time (a fetch lock of its own). A request for a file that
  is not cached waits for the fetch that runs and then serves the stored file (or the failure of
  that fetch). A request with a stale cached copy does not wait: while another request checks the
  file again, the stale copy serves at once, so only that request waits for the remote.
- A 200 is spooled (`max_upload_size`, hashes) and committed through the byte pipeline with
  source `proxy`: no uploader, the remote's `Last-Modified` as `created_at` and `updated_at`,
  `checked_at` and the ETag; the bytes of a blob that exists already are not stored again. A file
  that the format refuses (a `*assets.Refusal`, e.g. bytes that do not have the digest of their
  path) is a remote failure: "the remote sent a file that the format refuses: <message>"; a body
  that the remote breaks off is one too.
- The request that fetches an immutable file that is not cached gets the remote's bytes while they
  spool (`assets.Live`, through `Reader.Serve` and the Docker blob route): a GET without `Range`
  or a condition. The answer is 200 with the content type of the format's `Describe`, the remote's
  `Last-Modified` and its length (chunked when the remote sent none), and the headers the route
  adds (Docker: `Docker-Content-Digest` of the path); it has no `ETag` and no checksum headers,
  since the hashes are known only at the end. The last block of the body goes out after the
  commit: a body that breaks off, a file above `max_upload_size` or a file that the format refuses
  aborts the connection and is not stored, so the client never takes a short body for the file. A
  client that takes no bytes for 30 s is dropped, and the fetch goes on. A changing file, a file
  that keeps the remote's content type (`KeepType`, e.g. an OCI manifest, whose `Admit` sets the
  media type), a HEAD and a request with `Range` or a condition serve from storage after the
  commit. Other requests for the same path wait for the fetch and serve the stored file.
- A remote 404 or 410 answers 404 and is remembered in memory for `not_found_ttl` (an LRU of
  10,000 paths per server). A cached file that the remote no longer has stays and serves. A 401
  or 403 to the request with the token of a Bearer challenge counts as a 404 (`proxy.ErrDenied`):
  Docker Hub, GHCR and Quay answer so for a name that does not exist or that the credentials
  cannot read. A refusal at the token realm stays a refusal of the credentials.
- A remote error, a refusal (for example 401) or no answer headers within 30 s: a cached file
  serves although stale, with a warning in the log; with nothing cached the answer is 502 "The
  remote repository did not answer." `last_error` and `last_error_at` record it; a success sets
  `last_ok_at`. The failure is remembered in memory for 30 s per (repo, path) (an LRU of 10,000
  paths per server): in that time a stale cached copy serves without a remote request, and the
  requests that waited for the failed fetch get its answer. A new request for a file that is not
  cached asks the remote again, and an answer of the remote forgets the failure. A fetch continues
  when the client goes away, for at most 30 minutes. The remote client keeps up to 64 idle
  connections per host (256 in all) and uses HTTP/2 where the remote offers it.
- The fetch follows at most 5 redirects. The credentials (HTTP Basic or a token) go only to the
  remote's host: a redirect to another host drops them, and a fetch from a `Files` base with
  another scheme or host sends none and does not call `RemoteAuth`. The not-found memory, the
  conditional GET and the stale copy on a failure work the same for a `Files` base. No log line
  holds the credentials.
- A Debian proxy serves the remote's files unchanged: no indexer, no signature of its own.
  Clients keep the remote archive's key. Its apt snippet has no key step and no `signed-by`.
- Deleting files of a proxy (Files tab, admin) works; the next request fetches them again.

## PyPI

The module `pypi` (`internal/pypi`) is the format `pypi`: Python packages that pip, uv and poetry
install and that twine, `uv publish`, `poetry publish` and `telesphorepo publish` upload. The index
URL is `HOST/pypi/{repo}/simple/`, the upload URL `HOST/pypi/{repo}/` (or `.../legacy/`). Hosted,
proxy and group repos.

**Files.** A hosted repo stores each wheel and sdist at `packages/<project>/<file name>` and, for a
wheel, the exact `*.dist-info/METADATA` at the same path plus `.metadata` (PEP 658), in one commit.
`<project>` is the normalised name (PEP 503: lower case, each run of `-`, `_`, `.` one `-`). The
files are immutable: the same bytes are kept, other bytes under a file name that exists are 409
`File already exists`. A write of a path outside this pipeline (a core upload session) is 400. A
proxy stores its upstream pages at `.simple/<project>` and its files at `files/<rest>` and
`remote/<rest>` (below). `GET /pypi/{repo}/packages/{path...}`, `/files/{path...}` and
`/remote/{path...}` serve them (`Range`, S3 redirects and the edge as every asset).

**Pages.** `GET /pypi/{repo}/simple/` (the project list) and `GET /pypi/{repo}/simple/{project}/`
are rendered documents; they are never stored at their URL. A name that is not normalised, or
that lacks the trailing slash, is 301 to the normalised page. The `Accept` header picks the form
(PEP 691): `application/vnd.pypi.simple.v1+json` (also `...latest+json`), `...v1+html` (also
`...latest+html`) or `text/html`, the highest `q` winning and the server order (JSON, HTML,
`text/html`) breaking a tie; no `Accept` is `text/html`; none of them is 406. Every page answers
through `format.WriteDocument` with `Vary: Accept`, a strong `ETag` and 304. Both forms render
from one model:

- JSON: `meta.api-version` 1.1 (1.0 when a proxied HTML page gives no sizes), `name`, `versions`,
  and per file `filename`, `url`, `hashes.sha256`, `requires-python`, `yanked` (`false` for a hosted
  file), `size`, `upload-time` (`yyyy-mm-ddThh:mm:ss.ffffffZ`) and for a wheel `core-metadata`
  and `dist-info-metadata` (`{"sha256": ...}`).
- HTML: `<meta name="pypi:repository-version">`, one anchor per file with `#sha256=` on the href,
  `data-requires-python` (escaped), `data-core-metadata` and `data-dist-info-metadata`
  (`sha256=...`) and `data-yanked`.
- Every URL is relative to the page (`../../packages/...`, `../../files/...`), so a group serves
  the page of its owning member unchanged and the client follows each file to the repo it asked.

**Upload.** `POST /pypi/{repo}/` and `POST /pypi/{repo}/legacy/` (`route.FormatWrite`) take the
legacy upload form: the file in the field `content` goes to the spool; `:action` other than
`file_upload` and `protocol_version` other than `1` are 400; `sha256_digest` and `md5_digest` must
match; any other field is ignored. The project and the version come from the file's own metadata
(a wheel's `METADATA`, the `PKG-INFO` in the top folder of an sdist) and must match the file name
(wheel: PEP 427; sdist `.tar.gz` or `.zip`); a form `name` or `version` that disagrees is 400. The
answer is 201 `{project, version, filename, sha256}` for a new file, 200 for the same bytes, 409
for other bytes. With `?upload=<id>&sha256=&size=&filename=` and no body the route finishes a
resumable upload of the same publish session. An error answers in text with the message in the
body (twine prints it with `--verbose`, uv always); a client that asks for JSON gets `{"error"}`.
twine, uv and poetry send the session token as Basic with user `__token__`.

**Rows.** `OnCommitted` writes `pypi_projects` and `pypi_files` for the stored file; `OnRemoved`
deletes the file row of a removed file (its `.metadata` follows), stops advertising a removed
`.metadata`, and deletes a project without files. So a file delete in the web UI drops the file from
the page, and the last file drops the project. Each change moves the project's `rev` on; the
rendered pages of a hosted project are kept in the module's `format.DocumentCache` (1,024
entries, 64 MiB, 8 MiB each) keyed by the form, the repo, the project and `rev`, so a page renders
once per change and a new or removed file shows at once.

**Project list.** The rendered list is kept in a second `format.DocumentCache` (256 entries,
64 MiB, 32 MiB each) keyed by the form and the `pypi_index_revs` revision of the repo; a group's
key holds the ids and revisions of the members the caller reads, in member order. A request costs a
point lookup per repo, and an `If-None-Match` that names the key's `ETag` is 304 without a build.
A build reads a hosted repo's `pypi_projects` in key order, a proxy's `.simple/` assets in path
order, and merges a group's sorted member lists (a name once, from the first member that has it).

**Proxy** (default remote `https://pypi.org/`). The config field `files` (default
`https://files.pythonhosted.org/`, checked with `proxy.FilesURL`) is the host the remote serves its
package files from. The upstream page `simple/<project>/` is fetched with a JSON-first `Accept`
and cached at `.simple/<project>` (`Changing`, rechecked after `metadata_ttl`, its content type
kept); a remote that answers HTML is read as PEP 503 anchors. Each file URL, resolved against the
upstream page, maps to `files/<rest>` under the `files` base or to `remote/<rest>` under the remote
URL; a file at any other address is left out of the page, so the server never fetches an address
that a remote document names. A file's `.metadata` follows it. The rendered pages are kept in the
same `format.DocumentCache`, keyed by the SHA-256 of the cached page, the form, the remote and the
`files` base. The module fetches through `assets.Reader.Resolve`; `Classify` puts the `files`
base of the repo config in `Files` for a `files/` path. The remote's credentials go only to the
remote's host.
A proxy's project list is the projects it has cached; the upstream list is not proxied.

**Group.** A group asks its members in order for a project (`assets.Reader.Owner`): the first member
that has it (a hosted repo with the project row, a proxy whose remote has the page) renders the
page, and no later member is asked, so a public package cannot shadow a private one. A
`packages/` file goes to that member; a `files/` or `remote/` file to the first proxy member that
has it. The project list is the union of the members' lists.

**Versions.** The artifact folder of a hosted repo is `packages/<project>/`. Versions are newest
first by `sort_key` (keyset on the version string), one build per version with its files (the
sdist, then the wheels with their tags as the classifier).

**Edge.** `Immutable: packages/**/*, files/**/*, remote/**/*`. The pages are rendered, so the
Worker finds no pointer for them and sends them to the server.

**Import.** `Sources: ["pypi"]`: each wheel and sdist of a Nexus pypi repo
(`packages/<name>/<version>/<file>`) goes through the upload code to `packages/<project>/<file>`;
every other file is ignored.

**Web UI upload.** Wheels and sdists, chosen or a dropped `dist` folder; the plan reads the project
and the version from the file names, the upload checks the metadata.

**Snippets and CI.** pip (`pip.conf`), uv (`[[tool.uv.index]]`) and poetry (`poetry source add`);
the artifact snippets install one project (`pip install "<name>==<v>"`, `uv add`, `poetry add`).
CI tools `uv` (`astral-sh/setup-uv`), `poetry` (`setup-python`, `pipx install poetry`) and `pip`
(`setup-python`, `pip install build`), each then `telesphorepo publish`.

## Docker registry

The module `docker` (`internal/docker`) is the format `docker`: an OCI registry that serves the
OCI Distribution Spec 1.1 for hosted repos, proxies of other registries and groups. It holds container images, Helm charts and any other
OCI artifact (ORAS). The repo is the first segment of an OCI repository name: an image is
`<host>/<repo>/<image>[:tag|@digest]`, and the image has one or more segments (`team/api`). A
docker repo name is one OCI name segment (`[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*`; `a..b` and `a-` are
400). Its config is `{}`. Digests are `sha256` only (another algorithm is 400 `DIGEST_INVALID`). A
name is at most 255 bytes; a tag is `[A-Za-z0-9_][A-Za-z0-9._-]{0,127}`. The registry serves at
the root of an origin (`/v2/`, `/docker/`); a `base_url` with a path does not move it.

**Objects.** Every OCI object is an asset of the repo, and its blob is a core blob shared across
repos:

| Path | Body | Kind |
|---|---|---|
| `<image>/_blobs/sha256/<hex>` (a link) | the blob | `index` (not searched) |
| `<image>/_manifests/sha256/<hex>` | the manifest bytes, with its media type | `metadata` |
| `<image>/_tags/<tag>` | the blob of its manifest, with its media type | `artifact` |

`Describe` refuses every other path. `Admit` refuses a write of the generic upload and file routes
("push with an OCI client (docker push, helm push, oras push)"), a link or a manifest whose bytes do
not have the digest of its path, a pushed manifest that references a blob or a child manifest
that is not in the image or in the same commit (400 `MANIFEST_BLOB_UNKNOWN` with the digest;
foreign and non-distributable layers excepted), and a tag whose manifest is not in the image. The
same bytes at a path are kept. `OnCommitted` writes the rows of **Data model** (a link counts the
manifests of the image that use it; a manifest adds one use of each blob it references, each
distinct blob once; a tag records its digest). `OnRemoved`: a tag drops its row; a manifest that
an index outside the removal lists is 409 "an index uses this manifest; delete the index first",
else it gives back one use of each blob and its tags follow; a link that a manifest outside the
removal uses is 409 "a manifest uses this layer". The removal is the files of the batch and, when
a folder of the delete (`Removal.Prefixes`) holds `<image>/_manifests/sha256/`, every manifest of
the image: `Remove` reads a folder in path order, 500 files per batch, so the links (`_blobs/`
sorts first) go in an earlier batch than the manifests that use them, and a child manifest can go
in an earlier batch than its index. A manifest of a later batch gives back no use of a link that
is gone already. In one batch the tags go first, then the manifests, then the links.

**Routes.** ServeMux cannot match a name in the middle of a path, so the format registers method
catch-alls and parses the raw path (an encoded `/`, `\` or NUL is 400). Every `/v2/` answer has
`Docker-Distribution-API-Version: registry/2.0`; every refusal is the OCI error JSON
`{"errors":[{"code","message","detail"}]}`.

| Route | Policy | Serves |
|---|---|---|
| `GET /v2/` | none (the handler reads the credential) | No `Authorization`: 401 with the challenge. A credential that `Identify` accepts (a core bearer too): 200 `{}`; a bad one: 401 |
| `GET /v2/_catalog?n=&last=` | password, read token, publish session, device, bearer, anonymous | The images (`{"repositories": [...]}`, by full name) of the repos that the credential behind the request downloads, without the repos a guard refuses. Bearer scopes do not limit it: anonymous and a bearer without a credential see the public repos, a publish session also its own repo, a password, a read token or a device the grants of its user. A full page adds `Link: </v2/_catalog?n=&last=>; rel="next"`. A page reads only rows the caller lists: it walks the caller's repos that hold an image in name order (`ReposWithImages`) and reads each one's images through the primary key, one seek per repo it lists from; a holder of all repositories walks `docker_images_full` and seeks past each repo a guard refuses |
| `GET, HEAD /v2/<name>/manifests/<tag\|digest>` | `FormatRead` + bearer | The stored bytes and media type (`Accept` is ignored), `Docker-Content-Digest`, `Content-Length`, `ETag`; never redirected. 404 `MANIFEST_UNKNOWN`, also for a reference that is neither a digest nor a valid tag |
| `GET, HEAD /v2/<name>/blobs/<digest>` | `FormatRead` + bearer | The link of the image, with ranges; a GET may redirect to S3. 404 `BLOB_UNKNOWN` |
| `GET /v2/<name>/tags/list?n=&last=` | `FormatRead` + bearer | `{"name","tags"}` in tag order after `last`; `n` absent is 1000, above 10000 is 10000, 0 is an empty list; a bad `n` is 400 `PAGINATION_NUMBER_INVALID`. A full page adds `Link`. An image without rows is 404 `NAME_UNKNOWN`. A proxy lists the remote's tags; a group merges the tags of its readable members (see **Proxies and groups**) |
| `GET /v2/<name>/referrers/<digest>?artifactType=` | `FormatRead` + bearer | Always 200 `application/vnd.oci.image.index.v1+json` with one descriptor per manifest of the image whose subject is the digest, in digest order, 500 per page (`?last=<hex>` and a `Link`). The filter sets `OCI-Filters-Applied: artifactType`. A proxy lists the manifests it has cached; a group merges its readable members by digest |
| `GET /v2/<name>/blobs/uploads/<id>` | `FormatRead` + bearer | The upload: 204, `Location`, `Range`, `Docker-Upload-UUID` |
| `POST /v2/<name>/blobs/uploads/` | `FormatWrite` + bearer | `?mount=<digest>&from=<name>`: 201 when the caller reads the source repo and the source stores the link (a mount never contacts a remote); any miss starts an upload (202), never 401 or 403. `?digest=` with the body: a monolithic upload, 201. Else a new upload session: 202 with `Location: /v2/<name>/blobs/uploads/<id>`, `Docker-Upload-UUID`, `Range: 0-0` |
| `PATCH /v2/<name>/blobs/uploads/<id>` | `FormatWrite` + bearer | A chunk: with `Content-Range: <from>-<to>` (and a `Content-Length` of that size) at `from`, else streamed at the end; 202 with `Range: 0-<last byte>`. `from` other than the bytes received: 416 `BLOB_UPLOAD_INVALID` with `Range` and `Location` |
| `PUT /v2/<name>/blobs/uploads/<id>?digest=` | `FormatWrite` + bearer | An optional last chunk, then the end: 201 with `Location: /v2/<name>/blobs/sha256:<hex>` and `Docker-Content-Digest`; a mismatch is 400 `DIGEST_INVALID` and the upload is gone |
| `PUT /v2/<name>/manifests/<tag\|digest>` | `FormatWrite` + bearer | At most 4 MiB (413 `SIZE_INVALID`). The OCI image manifest and index and the Docker schema 2 manifest and manifest list; a JSON `mediaType` that differs from the `Content-Type` is 400, schema 1 is 400 `MANIFEST_INVALID`. A digest reference must be the body's digest; a reference that is neither a digest nor a valid tag is 400 `TAG_INVALID`. A blob that the image lacks and that another image of the repo links gets a link in the same commit. One commit: those links, the manifest and the tag (`Locks`: the links and child manifests it references). 201 with `Location`, `Docker-Content-Digest`, and `OCI-Subject` when the manifest has a subject (the subject need not exist). A push of a tag to another digest moves the tag; the old manifest stays, untagged, until deleted |
| `DELETE /v2/<name>/manifests/<digest\|tag>` | publish session + bearer; `Delete` with `repo.delete` | By digest the manifest and its tags; by tag the tag only. 202; 404 `MANIFEST_UNKNOWN` (also for a reference that is neither a digest nor a valid tag) |
| `DELETE /v2/<name>/blobs/uploads/<id>` | publish session + bearer; `Write` with `repo.publish` | Cancels the upload: 204 |
| `DELETE /v2/<name>/blobs/<digest>` | as a manifest delete | 405 `UNSUPPORTED` "delete the manifests; the server removes layers that no manifest uses" |
| `GET, POST /docker/token` | none (the handler reads the credential) | The token endpoint (below) |
| `GET /docker/hosts` | none | `{"registry": "<host>", "upload": "<host>"}`: the host of `base_url` (else of the request), and the host of `docker.upload_url` when `docker.direct_uploads` is on (else no `upload`) |

A name of one segment (no image) is 404 `NAME_UNKNOWN` "name an image inside the repository:
<host>/<repo>/<image>". The refusals map to OCI codes: an engine `anonymous`, `credentials`,
`publish_session` or `scope` refusal is 401 `UNAUTHORIZED`; `permission` and `network` are 403
`DENIED`; an unknown repo (or one of another format) is 404 `NAME_UNKNOWN`; a write to a proxy or
group is 405 `UNSUPPORTED`; a repo operation is 409 `DENIED`; a rate limit is 429 `TOOMANYREQUESTS`.
The handlers add: a bad name or path is 400 `NAME_INVALID`; a malformed upload body or
`Content-Range` is 400 `BLOB_UPLOAD_INVALID`; an upload that does not exist, expired, or belongs to
another repo or credential is 404 `BLOB_UPLOAD_UNKNOWN`; a blob over `max_upload_size` is 413
`SIZE_INVALID`; a 409 refusal of a hook (a used layer, a manifest that an index uses) is
`DENIED`; a failed proxy remote is 502 `UNKNOWN` "The remote repository did not answer."; any other
error is 500 `UNKNOWN` "internal error".

**Challenge.** A 401 carries `WWW-Authenticate: Bearer realm="<scheme>://<host>/docker/token",
service="<host>",scope="repository:<name>:<actions>"` with `pull` on GET and HEAD, `pull,push`
on PUT, POST and PATCH and `delete` on DELETE (`registry:catalog:*` on the catalog, none on
`/v2/`). The scheme and the host are those of `docker.upload_url` when direct uploads are on and
the request came to its host (the `Host` header; a default port counts as none), else those of
`base_url`, else the scheme of the client (`httpx.ClientScheme`, see **Client IP and scheme**) and
the host of the request.
A refusal with the code `scope` adds `error="insufficient_scope"`.

**Token endpoint.** `GET /docker/token?service=&scope=...` with Basic credentials, or `POST` with
a form (`grant_type=password` with `username` and `password`; `grant_type=refresh_token` with
`refresh_token`, read as a password; `scope` space-separated). The answer is 200 `{token,
access_token, expires_in, issued_at}` with one core bearer token in both fields; it never returns
a refresh token. A blocked client address gets 429 first. The password is a device proof of the
credential helper when it is `trx.<device id>.<nonce>.<signature>` (see **Publishing**), a CI
identity token when it is a JWT (three parts, `eyJ...`), else a password or a token of a
registered kind (`trp_`, `trs_`) checked with `auth.Service.Authenticate` (a bad one is 401). Scopes are
`repository:<name>:<actions>` (`pull`, `push`, `delete`, `*`); each maps to its repo, and an
unknown repo or a repo of another format is dropped. The answer grants a subset:

- Pull: `access.Engine.Allows(Read, repo.download)` for the credential (the guards apply). A
  publish session reads what anonymous reads outside its repo; a CI token reads what anonymous
  reads; a device proof reads what the device's user reads (`Minter.Check`, then the
  `auth.CredDevice` principal of the device; no session for a pull).
- Push and delete: one repo per token, the first in request order that asks. A publish session
  token (`trs_`) gets them on its own repo, with its session. A CI token mints a session for the
  repo through `publish.Minter.Mint` (the bearer then carries that session; push only). A device
  proof mints a session through `Minter.Mint` too (one per repo per nonce; push and delete, the
  engine checks `repo.delete`); the bearer carries the device with that session in its scope. A
  refused mint is 403 `DENIED` with the minter's message. A mint on a proxy or a group does not happen; the
  pull stays. A password or a read token gets no push; its write answers that publishing needs a
  publish session.
- Without scopes (`docker login`): a password or token that verifies gets 200; a CI token or a
  device proof passes `Minter.Check` (a failed check, or a proof that does not parse, is 401 and
  counts as a failed attempt of the address).

`expires_in` is 15 minutes, or the rest of the session when it is shorter, at least 60 s. A push
ends within `publish.session_ttl`. A mount from a private repo with a CI or `trs_` credential
falls back to an upload, because a publish session reads only what anonymous reads outside its
repo; with a device proof the device's user reads the source, so the mount works.

**Unused layers.** A manifest delete gives back one use of each blob it references; a link at 0
uses gets `unreferenced_at`. The job `docker.links` runs every `min(docker.link_grace, 1 min)` and
at once after a settings change: it reads up to 1000 links unused for `docker.link_grace` (`24h`,
minimum `1s`) at a time and removes the links of each repo with one `Remove` (source
`generated`, no audit event). When the hooks refuse the group (a manifest took one of its links
meanwhile), it removes the group link by link and the refused link stays; the row of a link
whose asset is gone already is dropped. Then the core blob GC deletes the blob after `blobs.gc_grace` when
no asset uses it. The grace also keeps a layer that a client pushed before its manifest, so it
must outlast the slowest push. Untagged manifests of a hosted repo stay until deleted; a proxy
drops the manifest of a tag that moved (see **Proxy**).

**Uploads.** Uploads are core upload sessions (`assets.Uploads`): the bytes stream to
`tmp/uploads/<id>`, nothing holds a blob in memory, `max_upload_size` caps one blob, and
`upload.session_ttl` expires them. The registry sets no body limit of its own and sends no
`OCI-Chunk-Min-Length`.

**Proxies and groups.** A docker proxy caches any OCI registry; the UI offers Docker Hub
(`https://registry-1.docker.io/`), the GitHub Container Registry and Quay as presets, and says
"Images keep the digests of the remote registry." A path in the remote URL is a name prefix:
`https://ghcr.io/acme/` proxies the images of `acme`, and `http://other:8080/team/` the images of
the repo `team` of another server. On `registry-1.docker.io` without a prefix, a one-segment image
is `library/<image>`. `Classify`:

| Local path | Remote path | Class |
|---|---|---|
| `<image>/_tags/<tag>` | `/v2/<remote image>/manifests/<tag>` | changing, `Accept` of the four manifest types, `KeepType`, `Unchanged` (the `Docker-Content-Digest` of a HEAD is the cached SHA-256) |
| `<image>/_manifests/sha256/<hex>` | `/v2/<remote image>/manifests/sha256:<hex>` | immutable, `Accept`, `KeepType` |
| `<image>/_blobs/sha256/<hex>` | `/v2/<remote image>/blobs/sha256:<hex>` | immutable |

`ProxyProbe` is `/v2/`. The proxy sends the remote's credentials only through `RemoteAuth`: a
Bearer challenge gets a token from the remote's realm (`proxy.Answer` with the credentials), a
Basic challenge the Basic header. The module keeps each token by remote, credentials and scope
until 30 s (at most half its life) before it expires, and a Basic answer for an hour, and sends
it with the next request of that image, so a fetch costs no token request while a token lives. A
check of a stale tag sends a HEAD (Docker Hub counts only GETs against its pull limit): the same
digest only sets `checked_at`; another digest fetches the tag. `Admit` refuses a fetched tag or
manifest that does not parse and a fetched file without the digest of its path (a remote
failure); a fetched tag needs no manifest in the image, and `AfterCommit` stores the manifest of a
fetched tag at its digest path. When the fetched tag replaced another digest, `AfterCommit` then
removes the old manifest when no tag names it and no index lists it, and each manifest of its
index that no tag names and that no index lists but the ones removed with it, all in one `Remove`
(source `generated`, one transaction for an index and its platform manifests). Their links lose
those uses, so the job `docker.links` and the blob GC reclaim the layers that only the old version
used; a client that asks for the old digest gets it from the remote again. The children of a
fetched manifest come when a client asks for them. A mount never contacts a remote. The first GET of an uncached layer gets the remote's bytes while
the proxy stores them, with `Docker-Content-Digest` and the remote's length (see **Proxies**); a
pull that asks for the same layer meanwhile waits until the proxy holds it. A group serves a link
or a manifest by digest from any member that stores it before a proxy member asks its remote
(`Addressed`). The tags list of a proxy is the remote's tags list (with `n` and `last`;
the `Link` of the next page names the local name); a remote that denies the name (404, or
`proxy.ErrDenied`) gives 404 `NAME_UNKNOWN`; a remote that fails gives the cached tags.
The catalog lists the images that a proxy has cached. A group reads its members in order
(manifests, blobs, a mount source); its tags list merges the tags of the readable members by tag
(a proxy member gives its cached tags) and its referrers merge by digest. A push to a proxy or a
group is 405 `UNSUPPORTED` with the read-only text ("A proxy is read-only.", "A group is
read-only. Publish to one of its repositories.").

**Upload host (the 100 MB request limit).** Behind Cloudflare (Free and Pro plans), each image
layer must be under 100 MB; pulls have no limit. Docker 29, containerd 2 (nerdctl, BuildKit and
buildx), Podman, skopeo, crane and ORAS send each layer in one request body: one PUT, or one PATCH
and an empty PUT. No client reads a maximum chunk size, and none splits a layer after a 413.
`docker.direct_uploads` ("Expose the origin IP for Docker uploads", off by default) lifts the
limit: the layer bytes go to `docker.upload_url`, the DNS-only name or address of the server. This
makes the IP address of the server public, and the admin opens that port in the firewall of the
server for the clients that push. The group's `Validate` refuses the toggle without an address.
Not behind Cloudflare, nothing is needed. Clients name only the main host. With the toggle on,
every answer that names an upload session (the 202 of `POST .../blobs/uploads/`, of a PATCH and of
a mount that falls back to an upload, the 204 of a status GET and the 416 of a wrong offset) has
an absolute `Location`: `<docker.upload_url>/v2/<name>/blobs/uploads/<id>?_state=<upload token>`.
A request that came to the upload host gets the relative path, as with the toggle off. The upload
token is a core bearer token of the caller's publish session for the repo (read and write) that
`auth.Service.IssueUploadToken` binds to the upload id. It lives 15 minutes, or the rest of the
session when that is shorter, at least 60 s, and each answer carries a new one. The `/v2/` routes
read it (`route.Policy.URLToken`, `auth.Read.URLToken`) only from the `_state` parameter of the
URL of that upload, and only when the `Authorization` header gives no credential. It never
verifies from a header, on another path or for another upload; a changed or expired token is 401.
The access log never writes query strings. The mount answer 201 and the monolithic
`POST ...?digest=` keep their relative `Location`. The clients follow the `Location` this way:

| Client | Credential on the upload host | Bytes |
|---|---|---|
| containerd 2 pusher (Docker 29 with the containerd image store, BuildKit, buildx, nerdctl) | none: it drops its authorizer on a host change | one PUT |
| Docker 29 with a graph driver (the distribution client) | none: it has no challenge of that host | one PATCH, then an empty PUT |
| containers/image (Podman, skopeo) | the bearer token of the main host | one PATCH, then an empty PUT |
| go-containerregistry (crane, ko) | none; it refuses a private or loopback IP literal as the host | one PATCH, then an empty PUT |
| oras-go v2.6.1 and later (ORAS 1.3, Helm 4) | it refuses a `Location` on another host (GHSA-jxpm-75mh-9fp7); it pushes to the upload host by name, where the `Location` stays on that host and the challenge names the token endpoint of that host | one PUT |

The registry supports the full chunked protocol for clients that use it (oras-go v3 with a chunk
size).

**Snippets and CI.** `HOST` is the host of `base_url`. With direct uploads off and the Cloudflare
IP list among the trusted proxies (`netrules.Service.TrustsList`, an in-memory read), the Docker push
snippets add "Behind Cloudflare (Free and Pro plans), each image layer must be under 100 MB. Pulls
have no limit." Use: `docker login HOST` with a read token for a private repo, `docker pull
HOST/<repo>/IMAGE:TAG`, `helm pull oci://HOST/<repo>/CHART --version X`. Publish (hosted repos):
from a computer, the setup line of the Publish tab once, then `docker push
HOST/<repo>/IMAGE:TAG`, `helm push chart.tgz oci://OCI/<repo>` and `oras push
OCI/<repo>/ARTIFACT:TAG FILE`, where `OCI` is the host of `docker.upload_url` when direct uploads are
on (with the note "ORAS and Helm 4 push to the upload address <host>, because they send layers only
to the host of the push."), else `HOST`; by hand, a publish token at
a hidden prompt, `docker login HOST -u publish --password-stdin`, the push and `docker logout`.
An http `base_url` adds "Add HOST to insecure-registries in /etc/docker/daemon.json."; a
`base_url` with a path adds that Docker reaches a registry at the root of a host. The artifact
snippets of an image (a folder, a tag or a manifest file) pull the digest that the tag names
(pinned) or the tag (latest); a manifest without a tag pulls by digest; a Helm chart pulls with
`helm pull --version`, another OCI artifact with `oras pull`; a private repo adds the login. The
CI tool `docker` builds `HOST/<repo>/app` with the commit as the tag (`$GITHUB_SHA`, Jenkins
`${GIT_COMMIT:-$BUILD_NUMBER}`), then pushes with a Docker config of its own (`DOCKER_CONFIG`, a
temp folder) whose `credHelpers` name the credential helper of the CLI for the registry host. The
helper mints the publish session from the job's identity token (`TELESPHOREPO_SERVER` names the
server of its audience), so the job runs no `docker login`. Its note:
"Change app to the name of your image. The job needs Docker. The OIDC token pulls public images
only: to build from a private image, log in with a read token before the build."

**Versions and builds.** An artifact is an image folder `<image>/` with a `docker_images` row.
Its kind is `helm-chart` when the newest tag's config is `application/vnd.cncf.helm.config.v1+json`,
`image` for an image config or an index, else `artifact`. Versions are the tags, newest first
(`docker_tags_recent`; `after` is a tag), each a release with one build: the short digest, and
the CI build of the tag asset. The files of the build are the manifest (`manifest`), the config
(`config`) and the layers (`layer`), or for an index its platform manifests with the classifier
`<os>/<arch>[/<variant>]`.

**Web UI.** The descriptor has the kinds Container image, Helm chart and OCI artifact, the
snippet tools Docker, Helm and ORAS, "Pull it", the delete notes of tags, manifests and layers,
and the file part `manifest` for tags and manifests: `web/src/modules/docker/ManifestPart.svelte`
shows `GET /api/docker/{name}/manifest` (digest, kind, size, tags, subject, referrers, the
platforms or the layers) and the artifact snippets. No repo tab and no config fields.

**Demo and seed.** The demo repo `docker` (public) holds 20 images with 1 to 8 tags each: two
multi-platform indexes, one Helm chart, the rest image manifests of small random layers. The seed
inserts per number a link, a manifest and a tag with their rows, 100 tags per image.

## Cargo registry

The module `cargo` (`internal/cargo`) is the format `cargo`: a Cargo registry with the sparse
index protocol (Cargo 1.68 or later; a private repo needs 1.74 or later) for hosted repos, proxies
of crates.io (or another sparse registry) and groups. The registry of a repo is
`sparse+<base>/cargo/<repo>/index/`. Cargo sends its token as a raw `Authorization: <token>`
header, so every Cargo route sets `Policy.TokenHeader = "Authorization"` (a read token or a
publish session token; HTTP Basic works too). Every refusal and error is a Cargo error
`{"errors": [{"detail": "..."}]}` (`Policy.Errors`).

**Routes.**

| Route | Policy | What |
|---|---|---|
| `GET, HEAD /cargo/{repo}/index/config.json` | read | `{"dl": "<base>/cargo/<repo>/crates/{crate}/{crate}-{version}.crate", "api": "<base>/cargo/<repo>"}`, with `"auth-required": true` when the repo has no anonymous read. Without a credential such a repo answers 401 with `WWW-Authenticate: Cargo login_url="<base>/#/repo/<repo>"`, so Cargo sends its token on every request of the registry |
| `GET, HEAD /cargo/{repo}/index/{path...}` | read | The index file of a crate, one JSON line per version: `1/{n}`, `2/{n}`, `3/{c}/{n}` or `{ab}/{cd}/{n}` of the lowercased name (another path is 404). A rendered document (`format.DocumentCache.Serve`: strong ETag of the revision, 304 to `If-None-Match`, which Cargo sends for each file) |
| `GET, HEAD /cargo/{repo}/crates/{name}/{name}-{version}.crate` | read | The `.crate`, an asset at that path (`Range`, S3 redirects and edge delivery as for every asset) |
| `PUT /cargo/{repo}/api/v1/crates/new` | write | `cargo publish`: a u32 LE length, the JSON of the crate, a u32 LE length, the `.crate` (spooled, at most `max_upload_size`; the JSON at most `max_json_size`). The name and version of the JSON must be those of the `Cargo.toml` in the crate. 200 `{"warnings": {...}}`; 409 for a version that exists (ignoring build metadata) or a name that clashes |
| `DELETE /cargo/{repo}/api/v1/crates/{name}/{version}/yank`, `PUT .../unyank` | write | `cargo yank` and `cargo yank --undo`: `{"ok": true}`; 404 for a version the repo lacks. Audit `cargo.yank`, `cargo.unyank` |
| `GET /cargo/{repo}/api/v1/crates` and `/cargo/{repo}/api/v1/crates/{name}/owners` | public | 404 with a Cargo error that names what to do instead (`cargo search` and `cargo owner` are not served) |

**Hosted repos.** A `.crate` is stored through `Commit` with its index entry as `Write.Meta` and
the folded name (lowercase, `-` as `_`) as an extra lock. `Admit` refuses (409) a crate whose
name folds to the name of another crate of the repo (as on crates.io), and a version that the
repo has ignoring build metadata; the same bytes at the same path are kept (an upload or an
import is then unchanged, a publish answers 409). A write without an entry (a core upload
session) is 400. `OnCommitted` writes the rows; `OnRemoved` drops the version of each deleted
`.crate` and the name of a crate with no version left, so a file delete in the web UI removes the
version from the index at once. The index file is rendered from the rows: `yanked` from the row,
the rest as stored. Each publish, removal and yank moves the `rev` of the crate's `cargo_names`
row on (`max(rev + 1, unix ns)`, so a crate removed and published again never reuses one), and the
rendered file is kept in the module's `format.DocumentCache` (1,024 entries, 64 MiB, 16 MiB each)
keyed by the repo, the crate and `rev`. Its ETag comes from that key: a request costs one point
read of the name row, and an `If-None-Match` that names the current revision answers 304 without
the file. A proxy's file is kept the same way, keyed by the SHA-256 of the cached remote file.

The entry follows the Cargo book: `name`, `vers`, `deps` (`name`, `req`, `features`, `optional`,
`default_features`, `target`, `kind`, `registry`, and `package` for a renamed dependency),
`cksum` (the SHA-256 of the `.crate`, computed by the server), `features`, `yanked`, `links`,
`rust_version`. Features whose values use `dep:` or `?/` go to `features2` with `"v": 2`, as
crates.io writes them. Dependencies are sorted by name, kind and target. An entry comes from:

- **a publish:** the JSON of the request with the Cargo book's conversion: `version_req` is
  `req`; a dependency renamed in `Cargo.toml` has the new name (`explicit_name_in_toml`) in
  `name` and the package name in `package`; `registry` is as Cargo sent it (null: this registry);
- **an upload or an import:** the normalised `Cargo.toml` in the `.crate` (BurntSushi/toml),
  converted as Cargo converts it for a publish: a requirement prints as the semver crate prints
  it (`1.2` is `^1.2`, comparators joined by `, `), a `cfg()` target with Cargo's spacing, a
  dependency without `registry-index` names crates.io (`https://github.com/rust-lang/crates.io-index`),
  and one whose `registry-index` is this repo's own index (`.../cargo/<repo>/index/`) names none.
  The two ways give byte-equal entries for one crate.

**Proxies.** The remote URL is the remote's sparse index (default `https://index.crates.io/`;
the check fetches its `config.json`). An index file is cached at `.index/<index path>`
(`Changing`, checked again after `metadata_ttl`) and served unchanged: its `cksum` holds for the
crate whatever the `dl`. Only `config.json` is the repo's own. A `.crate` comes from the repo
config's `download` template (`{"download": "<template>"}`, default
`https://static.crates.io/crates/{crate}/{crate}-{version}.crate`; markers `{crate}`,
`{version}`, `{prefix}`, `{lowerprefix}`; a template without a marker gets
`/{crate}/{version}/download` as Cargo does; `{sha256-checksum}` is refused because the proxy
fetches a crate before it knows its checksum) through `assets.Class.Files`: the remote's
credentials go there only on the remote's own scheme and host. `Classify` reads the template
from the repo config.

**Groups.** For an index file, the first member in read order that has the crate owns the name
(`assets.Reader.Owner`): its file serves, and a later member (the crates.io proxy) cannot shadow
a hosted crate with higher versions. A `.crate` of a group serves from the member that owns the
name: a hosted member that has the name row, a proxy that has the remote's index file of the
crate (the asset, not its body). `config.json` points `dl` at the group. Use a proxy or a group as crates.io through source
replacement (`[source.crates-io] replace-with = "<registry>"`).

**Tables** (`internal/cargo/migrations`):

| Table | Keys | What |
|---|---|---|
| `cargo_versions` | UNIQUE `(repo_id, lower_name, base_key)`, idx `cargo_versions_sort (repo_id, lower_name, sort_key)`, idx `cargo_versions_asset (asset_id)`; `repo_id` and `asset_id` cascade | One version of a hosted crate: `name` as published, `lower_name` (the index file), `version`, `sort_key` (`version.SemVer.SortKey`), `base_key` (the key without build metadata), `entry` (the index line with `yanked` false), `asset_id` and `path` of the `.crate`, `yanked`, `created_at` |
| `cargo_names` | PK `(repo_id, folded_name)` (WITHOUT ROWID); `repo_id` cascades | The crate names of a repo, folded, with the name that holds each and `rev`, the revision of the crate's index file |

**Versions and builds.** The artifact folder of a crate is `crates/<name>/` (kind `crate`). Its
versions are the rows of a hosted repo, newest first by `sort_key` (keyset on the sort key of
the cursor's version); each version is a release with one build, its `.crate`. Proxies and groups
have no rows and list no versions.

**Snippets.** `.cargo/config.toml` with `[registries.<repo>] index = "sparse+..."` (a repo name
with characters other than letters, digits, `-` and `_` gets `-` for them), plus
`credential-provider = "cargo:token"` for a private repo (Cargo refuses a registry with
`auth-required` that has no credential provider) and `[source.crates-io] replace-with` for a
proxy or a group; `~/.cargo/credentials.toml` with
`token = "YOUR_READ_TOKEN"` for a private repo (or `CARGO_REGISTRIES_<NAME>_TOKEN` in CI); a
`Cargo.toml` dependency with `registry = "<repo>"`; `cargo add`. The CLI tool `cargo` publishes
with `telesphorepo publish` (GitHub Actions adds `dtolnay/rust-toolchain@stable`; Jenkins runs
`cargo test` first).

**Uploads and imports.** The web UI takes `.crate` files (a folder keeps `**/*.crate`): the plan
reads the first MiB of each for its `Cargo.toml` and shows `<name> <version>`, with a problem for
a crate that does not parse, a name that clashes or a version the repo has. The Nexus importer
takes the source format `cargo`: a file `<name>-<version>.crate`, or a path that ends in
`<name>/<version>[/download]`, is stored with the entry of its `Cargo.toml`; index files are
ignored.

**Edge.** `Immutable: ["crates/*/*.crate"]`. The index files are rendered, so the Worker finds no
pointer for them and passes them to the server.

**Demo and seed.** The demo repo `crates` (anonymous read) holds 8 crates with 2 to 5 versions
each, stored as real `.crate` files through the upload path, with `dep:` features, a crates.io
dependency and dependencies between them. The seed inserts `seedcrate<i % 1000>` version
`<i / 1000>.0.0` with its asset row.

**Testing.** `cargo_test.go` (core zone, the real `cargo` of the `rust` image): `TestCargo` on
both stores publishes with `cargo publish` to a private repo (401 with the Cargo challenge
without a token, `auth-required`), refuses a second publish of a version and a clashing name,
checks the entry (renamed, crates.io, target and `dep:` dependencies), yanks and unyanks with
`cargo yank`, refuses a yank with a read token, builds a binary with a read token, deletes a
version and uploads its `.crate` through the web UI path, and requires the byte-equal entry.
`TestCargoProxyGroup` builds through a crates.io proxy by source replacement and through a
private group, where a hosted `itoa` 0.0.1 is the only `itoa` version the group lists.
`cli_cargo_test.go` (clients zone): `telesphorepo setup` and `telesphorepo publish` in the `rust`
image, then a build from the repo, with the secret scan. `nexus_cargo_test.go` (import zone,
part of `TestNexus`): `cargo publish` to a Nexus cargo hosted repo, the import, a build from the
target and a second sync with nothing new.

## npm registry

The npm module (`internal/npm`) serves the registry `HOST/npm/{repo}/` for npm, pnpm, Yarn 2+ and
Bun, with hosted, proxy and group repos. Tarballs (`<name>/-/<unscoped>-<version>.tgz`, the paths
of registry.npmjs.org) are assets; packuments are rendered per request and never stored at their
own URL.

- **Routes.** A scoped name comes as one segment with an encoded slash (`@scope%2fname`) or as two
  segments; `httpx.RepoPath` refuses `%2f`, so one route per method (`/npm/{repo}/{rest...}`) reads
  the escaped path itself and checks the name (URL-safe characters, an optional `@scope/`, at most
  214 bytes, no leading `.` or `_`; a name that a version adds must be lower case).
  - `GET <name>`: the packument. `Accept: application/vnd.npm.install-v1+json` gets the abbreviated
    document with that content type (the install fields of npm's package-metadata.md), else the
    full one (`_id`, `_rev`, `dist-tags`, `versions`, `time`, and `description`, `readme` and the
    like of the latest version). Both send `Vary: Accept`, a strong `ETag` and 304
    (`format.WriteDocument`); `?write=true` is the same. `dist.tarball` is absolute under
    `route.API.BaseURL` (the base_url setting, else the request's scheme and host).
  - `GET <name>/<version or dist-tag>`: the manifest of that version as the full packument lists
    it (`application/json`, strong `ETag`, 304), 404 for a version the package does not have; a
    dist-tag wins over a version of the same name. It reads only that version: a hosted package
    its tag and version rows, a proxy its cached full packument as a stream up to that version
    (a ref that is a SemVer version skips the tags, since npm refuses such a tag; a second read
    when the versions come before the tags). The manifest is kept in the document cache, keyed by
    the package `rev` or the SHA-256 of the cached packument, the ref and the base URL. pnpm 11+
    run through corepack fetches its binary package (`@pnpm/exe.<os>-<arch>`) this way from the
    registry of the project's `.npmrc`, without credentials on plain HTTP.
  - `GET <name>/-/<file>`: the tarball through the read path.
  - `PUT <name>` (`repo.publish`): with a `.tgz` attachment, a publish. The body is read as a
    stream: each field whole (at most 64 MiB), the base64 attachment decoded straight into the
    spool (`max_upload_size`). The body names exactly one version, strict SemVer, whose manifest
    names the package and the version. The server computes `integrity` (sha512) and `shasum`
    (SHA-1) and writes `dist`; a `.sigstore` attachment (provenance) is stored next to the tarball
    and not advertised. A version that exists is 403 "version 1.2.0 of x already exists". The
    dist-tags of the body that name the version are set, then `latest` names the highest release
    when no latest is left. 201 `{"ok":true}`. Without an attachment it is a metadata update: a
    changed `deprecated` of a version is applied (`npm deprecate`); versions missing from the body
    stay.
  - `PUT <name>/-rev/<rev>` (`repo.delete`): the step of `npm unpublish` that drops versions: every
    version missing from the body is removed with its tarball. `DELETE <name>/-/<file>/-rev/<rev>`
    (`repo.delete`) removes a tarball (200 when the step before removed it), `DELETE
    <name>/-rev/<rev>` every version. A `_rev` that is not current is 409. The `RepoRule` picks
    the operation from the URL (`Ops` and `Pick`), so a CI session cannot unpublish.
  - `GET /-/package/<name>/dist-tags`, `PUT .../dist-tags/<tag>` (body: the version as a JSON
    string, `repo.publish`, 201) and `DELETE .../dist-tags/<tag>` (`repo.publish`; `latest` is
    refused with 400). A tag is up to 64 URL-safe characters and not a version. The `GET` reads
    a hosted package's `npm_tags` rows, a proxy's cached abbreviated packument as a stream up to
    its `dist-tags`.
  - `GET /-/whoami` (`{"username"}` of the credential, 401 without one) and `GET /-/ping` (`{}`).
  - `POST /-/v1/login` and `PUT /-/user/<user>` answer 401 "npm login is not used with
    Telesphorepo. To install, put a read token in .npmrc (see the repo page). To publish, run
    telesphorepo publish."; `GET /-/v1/search` and `/-/npm/v1/security/...` answer 404. Errors are
    `{"error": "..."}`.
- **Credentials.** npm, pnpm and Bun send `Authorization: Bearer <token>`, Yarn the same with
  `npmAlwaysAuth`; a read token reads, a publish session writes.
- **Rows.** The commit of a tarball whose write carries the version (`Write.Meta`) inserts its
  `npm_versions` row, its tags and the package row in the transaction of the commit; `OnRemoved`
  deletes the version of a removed tarball, drops the tags that named it, moves `latest` to the
  highest release when it went, deletes a package without versions, and has the `.sigstore` of the
  version removed next. So a file delete in the web UI unpublishes the version. Every change moves
  `_rev` on; the refresh reads the version, time and deprecation of each version, not its
  manifest. Tag changes and deprecations run in one transaction under the package lock.
  `Admit` refuses a write without a version (a core upload) with 400.
- **Proxy** (preset `https://registry.npmjs.org/`). The remote's abbreviated and full packuments are
  cached as `Changing` assets at `.packument/abbreviated/<name>` and `.packument/full/<name>`, each
  fetched only when a client asks for that form (with the matching `Accept`). Rendering rewrites
  each `dist.tarball` to `<repo URL>/<name>/-/<file name of the remote URL>` and keeps
  `integrity`, `shasum` and `signatures`; `dist.attestations` goes, and a version without a `.tgz`
  tarball URL goes. A tarball is fetched from the same path at the remote and never changes.
- **Group.** The first member in read order that has the name owns it (`assets.Reader.Owner`): a
  hosted member with a package row, a proxy whose remote has the packument. The group renders the
  owner's packument with the group's URLs and serves a tarball only from the owner, so a public
  package in a later proxy never shadows a private name. A member that fails is skipped; with none
  left and a failed proxy the answer is 502.
- **Cache.** Rendered packuments stay in a `format.DocumentCache` (2,000 entries, 256 MiB, 64 MiB
  each, so the full packument of a big remote package is rewritten once per change of the cached
  copy) keyed by the form, the name, the package `_rev` or the SHA-256 of the cached remote
  document, and the base URL. The ETag comes from that key, so a matching `If-None-Match` answers
  304 without the body.
- **Versions.** An artifact folder is `<name>/`. Versions page newest first by `sort_key`
  (`npm_versions_sort`); each is a release with one build, its tarball (`extension` `tgz`).
  Proxies and groups have no versions.
- **Upload.** The web UI takes `.tgz` files; the plan reads the first MiB for `package/package.json`
  (name, version cleaned as npm does). A version that exists is a problem. An upload moves
  `latest` to the version when it is a release above the current one.
- **Snippets.** `.npmrc` (npm, pnpm, bun) and `.yarnrc.yml`, then `npm install` and `yarn add` of a
  package (pinned, or by the latest tag). CI tools `npm`, `pnpm`, `yarn` and `bun`, each with its
  install step before `telesphorepo publish`.

## NuGet repositories

The module `nuget` (`internal/nuget`) is the format `nuget`: the NuGet V3 protocol that dotnet,
nuget.exe and Visual Studio speak, with hosted, proxy and group repos. The source is
`<base>/nuget/{repo}/index.json`; every resource is under `/nuget/{repo}/`, so dotnet sends the
credentials of the source to each of them once the first 401 (`WWW-Authenticate: Basic`) asked.

**Stored files and rendered documents.** A package is two assets: the `.nupkg` at
`v3-flatcontainer/<id>/<version>/<id>.<version>.nupkg` and its root `.nuspec` (the exact bytes from
the package) at `v3-flatcontainer/<id>/<version>/<id>.nuspec`, with the id lowered and the version
normalised and lowered (`version.NuGet`: leading zeros dropped, a fourth part of 0 dropped, no build
metadata). The bytes of a `.nupkg` are never changed, so a signed package keeps its signature. The
service index, the version lists, the registrations, the search and the vulnerability index are
rendered (strong ETag, 304) from `nuget_versions` in a hosted repo, from the remote's cached
documents in a proxy, and from the member that owns the package id in a group. The version lists
and the registrations are kept in a `format.DocumentCache` (1,024 entries, 64 MiB, 8 MiB each)
keyed by the URL of the repo, the document, and the `rev` of the package's `nuget_packages` row
(hosted) or the SHA-256s of the cached service index, registration index and pages (proxy). A
request reads one package row, or resolves the cached remote documents without reading them, and
renders only when the key is new. A hosted render reads only what the document holds: an index of
up to 128 versions those versions, a paged index the version strings for its page bounds, a page
its versions, a leaf its version; an `If-None-Match` of the key's ETag answers 304 at once. The
proxy parses the service index once per change of its cached copy.

**Routes** (`route.FormatRead` for reads; push, unlist and relist `route.FormatWrite` with
`Policy.TokenHeader = "X-NuGet-ApiKey"`, so the API key is a publish session; that header comes
before the `Authorization` of the source's read credentials):

| Method & path | Answer |
|---|---|
| `GET /nuget/{repo}/index.json` | The service index `{"version": "3.0.0", "resources": [...]}`: `PackageBaseAddress/3.0.0` (`v3-flatcontainer/`), `RegistrationsBaseUrl/3.6.0` (`registration/`), the plain `RegistrationsBaseUrl` and its `3.0.0-beta`, `3.0.0-rc` and `3.4.0` (`registration-semver1/`, without SemVer 2.0 versions), `SearchQueryService` with `3.0.0-beta`, `3.0.0-rc` and `3.5.0` (`query`), `PackagePublish/2.0.0` (`package`), `VulnerabilityInfo/6.7.0` (`vulnerabilities/index.json`) |
| `GET /nuget/{repo}/vulnerabilities/index.json`, `.../base.json` | One page `base` of vulnerability data, and that page: `{}`. Restore finds the source audited and warns nothing |
| `GET /nuget/{repo}/v3-flatcontainer/{id}/index.json` | `{"versions": [...]}`: every version, unlisted ones too, lowered and normalised, oldest first. A proxy serves its remote's list as the remote wrote it |
| `GET /nuget/{repo}/v3-flatcontainer/{id}/{version}/{file}` | The `.nupkg` or the `.nuspec` (`Reader.ServeAsset`, S3 redirect as for every file) |
| `GET /nuget/{repo}/registration/{id}/index.json` (and `registration-semver1/`) | The registration index: up to 128 versions inlined in one page (`#page/<lower>/<upper>`, with `parent`), more in pages of 64 that the index names. Each leaf has the catalog entry inline (`id`, `version`, `listed`, `published` (`1900-01-01T00:00:00+00:00` when unlisted), `packageContent`, `dependencyGroups` with each dependency's `registration`, and the `.nuspec` fields), `packageContent` of this repo's flat container and `registration` |
| `GET /nuget/{repo}/registration/{id}/page/{lower}/{upper}.json` | One page of a paged registration; a hosted repo answers the versions from `lower` through `upper`, up to 128 (more: 404) |
| `GET /nuget/{repo}/registration/{id}/{version}.json` | One registration leaf |
| `GET /nuget/{repo}/query?q=&skip=&take=&prerelease=&semVerLevel=` | The packages of a hosted repo (a group: its hosted members that the caller reads, in order, each leaving out the ids that a member before it has) whose id holds `q` (3 or more characters: the trigram index `nuget_packages_search`, in the order of the index, so a repo's packages in the order they first came in; 1 or 2 characters: a prefix of the id, in id order; empty: every id, in id order), `skip` at most 3,000 and `take` at most 1,000 (20 by default). A package shows with its newest listed version that the client takes (`nuget_package_latest`: prerelease versions only with `prerelease=true`, SemVer 2.0 versions only with `semVerLevel=2.0.0`); a package without one does not count. A page reads at most `skip + take` packages; `totalHits` counts up to 10,000. The metadata of the newest version and the list of visible versions are read only for the packages of the answered page (the releases through `nuget_versions_releases`). A proxy answers no package |
| `PUT /nuget/{repo}/package` (and `.../package/`) | Push: the first part of `multipart/form-data` (or a body that is the `.nupkg`) is the package; the id and the version come from its root `.nuspec`, matched by local name. With `?upload=<id>&sha256=<hex>[&size=<n>]` and no body, the file of a resumable upload of the same publish session (`/api/repos/{name}/uploads`) is the package, and the push finishes the upload: 404 for an unknown upload, 400 when the bytes do not match (the upload is deleted). 201; 409 for a version that the repo has (also with the same bytes, so `--skip-duplicate` skips it); 400 for a package that does not read (no zip, no or several root `.nuspec`, a bad id or version); 413 above `max_upload_size` |
| `DELETE /nuget/{repo}/package/{id}/{version}` · `POST` the same | Unlist and relist (`listed` of the row, audit `nuget.unlist` and `nuget.relist`); 200, 404 for a version the repo lacks. dotnet restores an unlisted version by its exact version |

The push, the web UI upload (`.nupkg` files; a plan reads up to 16 MiB of each, a larger one is
read at the upload), the Nexus import and the demo data share one ingest: a `CommitAll` of the
`.nupkg` and the `.nuspec` with the package version as an extra lock. `Admit` keeps the same bytes
and refuses other bytes at a package path with 409; a write without a parsed package from a source
other than a proxy (a core upload session) is 400. `OnCommitted` writes the `nuget_versions` row
and moves the package's `rev` on; `OnRemoved` deletes the row of a removed `.nupkg`, moves the
`rev` on (or deletes the package row with its last version) and removes the `.nuspec` next.
Unlist and relist move the `rev` on in their transaction. Errors are the
`{"error"}` JSON of format routes; dotnet prints the status line.

**Proxy.** The remote URL is the service index (`https://api.nuget.org/v3/index.json`; stored with
a trailing `/` like every remote, which the format drops again) or the folder that holds
`index.json`. The proxy caches what it reads from the remote under `.upstream/`, a folder no client
asks for: the service index (`.upstream/index.json`), the version list of a package
(`.upstream/flat/<id>/index.json`), the registration index (`.upstream/registration/<id>/index.json`,
from `RegistrationsBaseUrl/3.6.0`, else `3.4.0`, `3.0.0-rc` or the plain one) and the pages that the
index does not inline (`.upstream/page/<path on the remote host>`), each `Changing` with the repo's
`metadata_ttl`. Packages are stored at their own flat-container paths and fetched once. The proxy
follows only resources of the service index on the remote's scheme and host, and a page only under
the registration base. Its registration is rendered from the remote's leaves with this repo's URLs:
every value of a catalog entry that points at the remote's host (`@id`, `iconUrl`, catalog
references) is dropped, and `packageContent` and the dependencies' `registration` point here. A
remote without a usable service index, or with nothing cached while it is down, answers 502.

**Group.** A group answers each package id from the member that owns it (`assets.Reader.Owner`:
the first readable member that has the id; a hosted member through its `nuget_packages` row, a
proxy member through the cached asset of the remote's version list), so a public package cannot shadow a private one with
the same id and a higher version. The registration and the version list are rendered from that
member with the group's URLs, and the packages download from it.

**Snippets and tools.** `Usage`: a `nuget.config` with the source (and `allowInsecureConnections`
on plain http, `packageSourceCredentials` with `YOUR_USERNAME` and `YOUR_READ_TOKEN` for a private
repo), `dotnet nuget add source` and, for a hosted repo, `telesphorepo publish`. `ArtifactUsage`
(hosted): `dotnet add package <id> --version <v>` and a `PackageReference`; the latest mode drops the
version (`Version="*"`). The tool `dotnet` builds the CI files (`actions/setup-dotnet@v5` with
`10.0.x`, then `telesphorepo publish`). `Versions` and `Builds` list the versions of
`v3-flatcontainer/<id>/` newest first (keyset on `sort_key`); a version has one build, its `.nupkg`
and `.nuspec`.

## Files, uploads and deletes

`internal/files` serves the file tree; deletes and uploads go through the byte pipeline (see
**Byte pipeline**), and the core module reads manual uploads. The core names no format; it calls
the capabilities of the repo's `format.Spec` that are set.

- **Tree.** `GET /api/repos/{name}/tree` returns the direct children of a folder in path order,
  folders and files mixed (the UI sorts folders first within a page). Each child is one indexed
  seek on `(repo_id, path)`. After a folder, the next seek starts just past every path under it,
  so a page costs `limit` seeks however large the repo is.
- **Group tree.** The tree of a group is the union of the trees of the members that the caller
  reads, in read order (`access.Engine.GroupMembers`, as a group read; a proxy member gives its
  cached files). Each member is read with the same `after` and `limit`, and the pages merge by
  path: a folder that several members hold is one item, and a file that several members hold is
  the first member's, the one a group read serves; each file item names it in `repo`. The first
  `limit` items of the merge are the first `limit` of the union, because a member's page holds all
  its items before its `limit`-th. `next` is the last item's cursor while any item follows. A page
  costs at most `limit` + 1 seeks per member. A group has no deletes (405), no publish sessions
  (409) and no uploads.
- **Group versions** (`internal/format/group.go`). The versions, builds and artifact snippets of a
  group come from the members that the caller reads, in read order (`access.Engine.GroupMembers`),
  so a group's Files tab has every view of a hosted repo of its format. `format.Spec.Versions`,
  `Builds` and `ArtifactUsage` take the repo that the answer names (the group: its name, its URLs)
  and `from`, the repo whose files and rows they read (the repo itself, or a member).
  `format.Spec.GroupVersions` says how a group lists versions:
  - `each` (Maven, Docker; a group read serves each path or tag from the first member that has
    it): a k-way merge of the members' pages in `format.Spec.VersionOrder`. Each member reads one
    page of `limit` versions from its own cursor; the group cursor is base64url JSON of the
    member cursors by repo id (the last version taken from each member, also from one that has
    no more) and the members that have no more. A version comes out while every member that has
    more still holds a version it read, so no unread version sorts before it. A version that an
    earlier member has comes from that member; the later member's copy is left out. With
    `format.Spec.VersionsByName` (Maven: the order is that of the version strings, so a version
    sorts at one place in every member) the pages and cursors say which members have a version:
    an earlier member holds it in its page or took it last on an earlier page, a later member
    holds it in its page. Without it (Docker orders tags by date) a member that does not hold the
    version in its page is asked by a lookup of its `Builds` (limit 1). A page costs one
    `Versions` call per member that has more, and without `VersionsByName` at most one `Builds`
    lookup per other member and version. A version with a `folder` (Maven: its files serve path
    by path) that later members hold too is the union of their builds (below), so its
    `builds_count`, `latest_build` and `published_at` are the union's; the union reads every
    build of the version in each member that holds it.
  - `owner` (npm, PyPI, Cargo, NuGet; a group read serves every version of a package from the
    member that owns the name): the versions of the owner, with its own cursor. The owner is the
    member that `assets.Reader.Owner` picks for a group read: the first member whose `Versions`
    answers an artifact for the folder, skipping a member whose `Versions` fails (502 when no
    member has it and a proxy's remote failed). A proxy member's `Versions` reads the document
    that its owner check reads, through the read path, so it fetches the document from the remote
    when a download would: npm the abbreviated packument, PyPI the project page, Cargo the index
    file, NuGet the flat container version list. It lists every version the document names, in
    the order of a hosted repo, each with `remote: true` while the proxy has not cached its files
    (one indexed lookup per version of the page). `published_at` is the document's date when it
    has one (npm reads it from a cached full packument only; NuGet's version list and Cargo's
    index file without `pubtime` have none: null). A parsed document stays in a small cache under
    the SHA-256 of the cached copy.
  - The builds of a version are what a group read serves for it, and each file names in `repo`
    the member that serves it: in an `owner` group the owner's builds (a proxy's files not cached
    yet have `remote: true`); in an `each` group, for a version with a `folder`, the page of the
    first member that has the version when no later member has it (a `Builds` lookup in each),
    else the union of the builds of every member that has it: a build id once, a path once from
    the first member that has it, sorted in `format.Spec.BuildOrder`, a build's `published_at`
    the newest of its files; for a version without a `folder` (a Docker tag, which one path names), the
    builds of the first member that has it, each file named by the first member that stores its
    path. The artifact snippets are those of the owner (`owner`), else of the first member that
    answers for the path, built for the group.
  - In an `each` group a proxy member gives what it has cached: a version that only its remote
    holds shows once the proxy has cached it.
- **Repository lists** (`internal/api/repolist.go`, `internal/repos/list.go`, migration
  `0005_performance.sql`). A request reads only its answer. A holder of `repositories.all` sees
  every repo: a page is one range of one index (`name`, `(format, name)` or `(kind, name)`, the
  cursor and the `q` prefix as `[from, to)`). Anyone else sees the public repos (the partial
  indexes `WHERE anonymous_read = 1`) and the repos of their grants (one rowid lookup per grant,
  the grants that `auth.Service.WithGrants` reads once per request); `perm` keeps the grants that
  hold one of its permissions, and the public repos only when it names `repo.download`. Each list
  reads up to `limit + 1` repos and the merge keeps the first `limit`, a repo once, so it holds
  every name before the next page. `sort=published` merges the same lists from
  `(last_published_at, id)`. Per page the server reads the downloads of the page's repos, and a
  group's counters (its members in one query of their ids); a list item has no config.
- **Group counters** (`internal/api/groupcounts.go`, migrations `0003_group_counts.sql` and
  `0005_performance.sql`). A group's
  `file_count`, `total_size` and `last_published_at` (`GET /api/repos/{name}`, `GET /api/repos`)
  count each file it serves to the caller once, from the members the caller reads
  (`access.Engine.GroupMembers`): a file whose `unit` is `''` from the first member that has its
  path; a file of a unit (`format.Spec.GroupUnit`: the package of npm, PyPI, Cargo, NuGet) from
  the member that owns the unit, the first one that stores a file of it, as a group read takes
  every file of a package from its owner. A file that the group merges (Maven
  `maven-metadata.xml` and its checksums) counts once with the size of the first member's copy:
  the merged bytes exist only for a read. `last_published_at` is the newest `updated_at` of a
  non-index file it serves; a delete never lowers it.
  - Each group keeps one row of `group_counts` per set of members that a reader asked for (the
    key is the member ids in read order). A request reads its row with one indexed lookup; the
    list of repositories does that once per group. A set without a counted row answers
    `counts_pending: true` (0 and null) and asks the job for it.
  - The job `core.group-counts` first sets the `unit` of every asset row that has none (500 per
    transaction) and deletes the rows of the groups of those repos; then it counts the full set
    of members of every group and every set that a reader asked for, each that has no counted
    row yet. The count never holds SQLite's writer for longer than a few small statements, at
    any number of files:
    1. One write transaction inserts the row, its members and its `group_count_pending` entry.
       From here on the triggers add the change of every asset write to the row.
    2. One write transaction sets the row's counters to zero and, before it commits, opens a read
       snapshot on a reader connection (its first read). No write commits between the two, so
       from then on the row holds exactly the changes of the writes that the snapshot does not
       see.
    3. Two queries over the members' files (`CountGroupPaths`, `CountGroupUnits`) count the
       snapshot, holding no writer; publishes go on meanwhile.
    4. One write transaction adds the count to the row (`last_published_at` takes the larger)
       and deletes the pending entry: the row counts the files as they are now.
    A change of the members on the way deletes the row and ends the count; a row left pending (an
    error, a stop) is deleted and counted again from the start. It runs at start, after every repo change (`repos.Service.AfterChange`), when a reader asks
    for a set, and every 5 minutes.
  - Triggers on `assets` keep every counted row in the transaction of each insert, replace and
    delete of a member's file, with indexed lookups only (`(repo_id, path)`, `assets_unit`,
    `group_count_members`): a file that no earlier member of the row has takes the place of the
    copy of the first later member that has it; a removed one gives its place back. A member's
    first file of a unit takes the unit from the first later member that has it (its files of the
    unit leave the counters, read by `assets_unit`); its last file gives it back.
  - A change of a group's members (an insert, update or delete of `repo_group_members`, a member's
    repo delete included) deletes the group's rows; the job counts the new set again.
- **Delete credential.** `POST /api/repos/{name}/files/delete` needs the `repo.delete` permission on
  the repo. It takes the UI session cookie of a person who holds it; that session exists only
  after the second factor (outside dev mode). It also takes a device or manual publish session of
  that repo (Basic password or `Authorization: Bearer <token>`) whose user holds `repo.delete` now,
  through `access.Engine.Decide` with `Delete`: the repo's operation, the publish rules, and 403
  for a session of another repo. A CI session gets 403. Passwords and read tokens get 401. A repo
  with an operation gets 409. A group gets 405.
- **Delete steps.** A prefix is always a folder (a trailing `/` is added). The server checks
  every path and prefix first, then calls `Remove` (see **Byte pipeline**): the `paths`, then each
  folder, in batches of 500. `deleted` is the number of files. The blobs stay until the blob GC
  removes those that no file uses.
- **Repo delete.** `POST /api/repos/delete` (`repositories.all`) starts a `delete` operation per
  repo and answers 202; the deletes run as tasks (see **Repo operations**), and writes get 409
  meanwhile. Each removes the files batch by batch, empties the rows of the repo in batches, then
  deletes the repo row. The UI waits for each repo to answer 404 and shows the progress in a toast. The name can be used again afterwards.
- **`format.Spec.OnRemoved` and `AfterRemove`.** `OnRemoved(ctx, tx, repo, removed, prefixes)` runs in
  the transaction of each batch, before the rows go, with the folders of the removal; Debian,
  Docker and Maven use it (see **Debian indexing
  pipeline**, **Versions and builds**). Maven's `AfterRemove` writes the artifact's `maven-metadata.xml` again (and the
  checksum files it has), with the version folders that still hold files, so a deleted version
  leaves the metadata. An artifact without a `maven-metadata.xml` stays without one.
- **`format.Spec.Upload`.** `Plan(ctx, repo, PlanInput{Files, Fields, Folder})` works out what an
  upload stores and stores nothing: `Plan{Items: [{Title, Files: [{Path, Target}], Facts,
  Problems}], Ignored, Fields: [{Field, Value, Read, Problem}]}`. A `PlanFile` has its path, its size
  and `Head`, the first bytes of a file that a `Reads` rule matches. `Folder` is the open folder of
  the Files view, which gives the first values of fields. `Store(ctx, repo, plan, files, origin)`
  stores the files of the plan's items through `Commit`/`CommitAll` (the uploaded files with
  source `ui`, the checksums and the metadata the format writes with source `generated`) and
  returns the results. `Uploaded` (with `generated`), `UploadedOf`, `BadUpload` (a 400 refusal),
  `CheckFields` and `InFolder` are the helpers. The pipeline records one `file.upload` event per
  uploaded file.
- **Upload limits.** At most 500 files, 16 text fields of up to 1 KiB each, and all files together
  within `max_upload_size` (the heads of a plan too). Fields come before the files in the body.
- **Relative paths.** The `filename` of a multipart file part may be a relative path
  (`folder/sub/file.jar`). The server reads it from the raw part header, because Go's
  `Part.FileName()` keeps only the base name. There is no separate paths field.
- **Upload plan.** `POST /api/repos/{name}/upload/plan` takes the list of files and the heads
  of the files the format reads, and answers the `Plan`. The UI shows it (each item with the path
  of each file, its problems, the files left out, and the fields; a value the plan read from the
  files is fixed), plans again 400 ms after an edit of a field (it aborts a plan in flight, names
  the `heads` of the last plan and sends only the first bytes of files added since), and uploads
  the files of the items the person ticks. Past 90 MB of files, the largest files go first through
  the resumable upload routes in 16 MiB chunks (a failed chunk is sent again from the `received`
  of the server), until the rest fit in one request of at most 90 MB, so no request passes the
  100 MB body limit of Cloudflare; the upload names them in its part `uploads`. A cancel or a
  failure deletes the resumable uploads that the server did not finish. `POST /api/repos/{name}/upload` runs the same plan on the sent files and stores
  only when no field and no item has a problem.
  - **Maven folder.** A folder upload (any path with a `/`, or more than one pom) is one item per
    artifact that the plan finds, and takes no fields. The plan reads the poms (`**/*.pom`,
    `**/pom.xml`, 1 MiB). Each pom with
    the files next to it named `<artifactId>-<version>[-<classifier>].<ext>` is one artifact;
    `.module` files belong to it. Checksum and signature files (`.md5`, `.sha1`, `.sha256`,
    `.sha512`, `.asc`) are ignored, because the server writes the checksums. In a Maven-layout
    folder (`<group path>/<artifactId>/<version>/`) the pom's groupId, artifactId and version must
    match the path. A `pom.xml` outside a Maven layout is a project file and is ignored.
  - **Maven.** A single-pom upload: one pom (`*.pom` or `pom.xml`) plus files named
    `<artifactId>-<version>[-<classifier>].<ext>`. One file with any other name becomes the main
    artifact. The fields `groupId`, `artifactId`, `version` and `packaging` fill what the pom
    lacks; the plan lists the fields that apply, with the values it read from the pom fixed, and a
    coordinate problem on its field. A lone artifact gets a generated pom. Every stored file gets `.md5`, `.sha1` and
    `.sha256`. `maven-metadata.xml` is created or merged, with the versions in Maven order, plus
    `latest`, `release` and `lastUpdated`.
  - **Debian.** `.deb` files with the fields `distribution` (first value: the open folder
    `dists/<d>/`, else the repo config's `distribution`) and `component` (`pool/<c>/`, else
    `main`), through the normal ingest. The plan reads the first 1 MiB of each `.deb` for its
    control data and names its pool path; a package whose control data lies past it is checked
    when the whole file arrives. Other files are left out; one that does not parse is a problem.
  - **Raw.** The field `folder` (first value: the open folder), under the repo's overwrite
    policy. Each file is one item, stored at `folder/<relative path>`.

## Network rules

`internal/netrules` resolves the client address, keeps the IP lists and checks the IP rules of
each repo. Request paths read in-memory snapshots only. The database holds the lists and the
rules. The ranges of each IP list, the CIDRs of each rule scope and the trusted proxies are
compiled into sorted, merged address ranges when they change, so checking an address is a binary
search at any list size.

**Client IP and scheme.** The network module resolves the client once per request
(`Extender.ResolveClient`): its address and its scheme. The server attaches them to the request
context. `httpx.ClientIP(r)` reads the address. The rate limits, the audit log, the IP rules,
device enrollment and the access log all use it. `httpx.ClientScheme(r)` reads the scheme.
The URLs that the server builds from the request when `base_url` is empty use it: the realm of
the registry challenge, the base URL of the snippets and of `/install.sh`, and the device
verification URL. Without the network module, the address is the TCP peer and the scheme is that
of the connection.

- The trusted proxies are the union of the literal entries of the setting `trusted_proxies`, the
  ranges of the IP lists of `trusted_proxy_lists` and the ranges of the lists that
  `TR_TRUSTED_PROXIES` names. The snapshot compiles again when the setting, a list or a row
  changes.
- If the TCP peer is not a trusted proxy, the peer is the client. The server ignores
  `X-Forwarded-For` and `CF-Connecting-IP` from such a peer.
- If the peer is trusted, the server reads `X-Forwarded-For` from right to left and skips trusted
  addresses. The first untrusted address is the client. If every hop is trusted, the left-most
  hop is the client. An entry that does not parse stops the walk at the last address read.
- `CF-Connecting-IP` counts only when the walk ends at an address in the list `cloudflare` (the
  peer itself, or the last hop read), and the trusted proxies include the list `cloudflare`. So
  Cloudflare in front of Caddy needs no Caddy setting: Caddy puts the Cloudflare edge in
  `X-Forwarded-For` and passes `CF-Connecting-IP` on unchanged.
- `trusted_proxies` is a list. The default is empty: the server trusts no proxy. An entry is a
  CIDR, a plain address or `private` (loopback, RFC 1918, ULA and link-local). A bare `cloudflare`
  is invalid. A PATCH refuses a `list:<name>` entry with 400 `add an IP list on the Reverse proxy
  page` in `fields.trusted_proxies`: an IP list joins the trusted proxies as a row of
  `trusted_proxy_lists` (`PUT /api/network/trusted-proxies {lists: [id]}`, one transaction,
  `network.trusted.update`). Only `TR_TRUSTED_PROXIES` may name lists as `list:<name>`; the
  network module's `Init` resolves each name to its list once, and an unknown name stops startup.
- The default trusts nothing because Docker's userland proxy and rootless Podman make every outside
  connection arrive from a private gateway address. With `private` trusted there, anyone who
  reaches the port directly could forge `X-Forwarded-For` and get around the IP rules.
- **Scheme.** The scheme is `https` when the connection is TLS. If the peer is trusted, the
  forwarded proto gives the scheme: the first entry of `X-Forwarded-Proto`, else the `proto` of
  the first element of `Forwarded`. A value other than `http` or `https` does not count. From a
  peer that is not trusted, a forwarded proto of `https` also makes the scheme `https`. The
  scheme only shapes the URLs that the server sends back to the same client, so such a peer can
  only make its own client use https. A TLS-terminating proxy that is not trusted yet so never
  makes the registry send credentials to an `http` token realm.
- **Proxy candidates.** When a peer that is not trusted sends `X-Forwarded-For`, `Forwarded` or
  `CF-Connecting-IP`, the server remembers the peer in memory: the last time, the count and the
  header of the last request. It keeps at most 20 peers and drops the one seen longest ago. A
  peer that the trusted proxies cover leaves the list. Nothing is stored in the database, so a
  restart empties the list. `GET /api/network/proxy-candidates` returns it, and the UI offers
  "Trust this address", which adds the address to `trusted_proxies` with the settings PATCH.

**IP lists.** An IP list is a named set of CIDRs. Repo rules and the trusted proxies refer to a
list by its id (the API shows its name). The table is `ip_lists`.

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
- **Rename.** A rename changes one row: the rules and the trusted proxies keep the list by its id.
  It answers 409 when `TR_TRUSTED_PROXIES` names the list, because the next start resolves the
  old name.
- **Delete.** Refused with 409 `{error, used_by}` while a repo rule or the trusted proxies (a row
  or `TR_TRUSTED_PROXIES`) use the list; the foreign keys (`ON DELETE RESTRICT`) refuse it too. A
  preset cannot be deleted (400).
- **Use.** `used_by` is `{repos: [{repo, scope}], trusted_proxies: bool}`. The index
  `repo_ip_rules_list_id` finds the rules of a list.

**Repo IP rules.** Each repo has two allow-lists, `read` and `publish`. An entry is a CIDR or an
IP list (the API names it; the row keeps its id). An empty list is unrestricted (the default).

- **Save.** `PUT /api/repos/{name}/ip-rules` replaces both lists. A CIDR is masked and an address
  becomes `/32` or `/128`. At most 200 rules per scope and 200 characters per note. A duplicate, a
  bad CIDR or a `list` that does not exist gives 400. The change is audited as
  `network.rules.update` with the counts only.
- **Read rules** apply to every format read and to the repo read routes (`tree`, `assets`, `usage`,
  `status`), for every caller, admins included. Search leaves out the repos whose read rules
  refuse the caller. `GET /api/repos/{name}` is exempt (`access.Meta`: guards never see it), so a holder of `repo.settings` can
  still open the repo and change its rules.
- **Publish rules** apply to format writes, manual upload and upload plan, deletes (a UI session
  too) and to minting device, manual and OIDC publish sessions. The network module is a guard of
  the access engine (`access.Guard` named `network`): it maps `Read` to the read rules and
  `Write`, `Delete` and `Mint` to the publish rules.
- **Refusal.** The answer is 403 with the message `your address is not allowed to read|publish this
  repository` and the code `network` (a problem document under `/api/`, `{"error"}` on a format route) and the audit event `network.denied` with `detail.scope` and the client IP. The IP
  appears only in the audit log.

## Signing keys

`internal/signing` keeps the signing keys and the key of each repo. It loads every key and every
binding into memory at start, and request paths and the indexers read that copy. The tables are
`signing_keys` and `repo_signing_keys`.

- **Name.** 1 to 64 lower-case letters, digits, `.`, `_` or `-`, starting with a letter or digit.
- **First start.** If the table holds no key, the server creates the Ed25519 key `default`. If an
  admin deletes `default`, a repo created without `signing_key` gets 400 that names the missing
  key. The offline `seed` command creates its repos without a key; their indexes have no
  signature.
- **Storage.** The server stores the private key without a passphrase, sealed with the master key
  (AAD `signing_keys:<id>`). If the master key changes, the key shows `error`, and the repos that
  use it publish indexes without a signature.
- **Create or import.** An admin creates an Ed25519 key, or imports an armored private key. An
  encrypted key needs its passphrase. The stored copy has no passphrase.
- **Use.** A format with `format.Spec.SigningKey` (its module requires `signing`; Debian) binds
  each of its hosted repos to a key: the repo field `signing_key` (a `repos.Field`, default
  `default`), a row of `repo_signing_keys` with a foreign key to the key. A proxy and a group
  sign nothing, so they have no `signing_key`; one in `fields` gives 400. `POST /api/repos` and
  `PATCH /api/repos/{name}` take it in `fields`; an unknown key gives 400. A bind records
  `signing.key.bind`. The key is not a config key: a config with `signing_key` gives 400.
- **Public key.** `GET|HEAD /debian/{repo}/key.asc` returns the armored public key of the repo's
  key. It follows the read rules of the repo: anyone for a public repo, credentials for a private
  repo.
- **Change of key.** When a repo changes its key, only that repo is signed again: every `Release`,
  `InRelease` and `Release.gpg` of the repo is written again.
- **Delete.** `DELETE /api/signing-keys/{name}` checks the use inside the write transaction. It
  answers 409 `{error, repos}` while a repo uses the key; the foreign key (`ON DELETE RESTRICT`)
  refuses it too.

## Audit log

`audit_events` records who did what. `GET /api/audit` needs `activity.read` and lists newest
first. Every action is registered by its module (`Extender.AuditActions`) with a label and a
permission subsystem; `GET /api/audit/actions` lists them, and the Activity filter picks the
subsystem (its area) first, then one action of it. `audit.Log.Record` refuses an action that no module registered (it logs and returns
the error). The route middleware puts the caller in the request context (`audit.WithActor`, with
the client address); services record their own events with that actor, inside the transaction of
the change when they have one (`RecordTx`). A job and an `Init` record as the system actor of its
module (`audit.System`), the `telesphorepo-server user` commands as `cli` (`audit.CLI`).

| Module | Subsystem | Actions |
|---|---|---|
| core | `people` | `signin.success` (`detail.method`), `signin.failed` (the actor is the tried username), `token.create`, `token.delete`, `user.create` (`detail.permissions`), `user.update` (password changed, disabled, second factors reset), `user.permissions` (`detail.old` and `detail.new`: the server permission sets; the actor kind is `sso` when single sign-on set them at a sign-in) |
| core | `repo` | `file.upload`, `file.delete`, `grant.set` (`detail.old` and `detail.new`: the repository permission sets) |
| core | `repositories` | `repo.create`, `repo.update`, `repo.delete`, `repo.delete.resume`, `repo.delete.clear`, `repo.rename` (`detail.renamed_from`), `repo.redirect.remove` (`detail.redirect`) |
| core | `settings` | `settings.update` (keys only, never values; `via: "first sign-in"` when the sign-in set `base_url`) |
| storage | `storage` | `storage.move.start` (`detail.target` without secrets), `storage.move.cancel`, `storage.move.done`, `storage.move.failed` (`detail.error`), `storage.move.retry` (`detail.changed`: the keys of the body; `detail.move`: the new move), `storage.move.dismiss` |
| backup | `backup` | `backup.done` (`target` the backup name; `detail`: `dir`, `storage`, `blobs`, `copied`, `size`, `master_key_included`, `removed` (the folders retention removed), `removed_blobs`), `backup.failed` (`detail.error`). A scheduled run has the module as actor, Back up now the person |
| network | `network` | `network.denied` (`detail.scope` is `read` or `publish`), `network.rules.update` (counts of read and publish rules only), `network.list.create`, `network.list.update` (`detail.renamed_from` after a rename), `network.list.delete`, `network.trusted.update` (`detail.lists`: the IP lists of the trusted proxies after a change) |
| signing | `signing` | `signing.key.create` (`detail.imported`), `signing.key.delete`, `signing.key.bind` (a repo got its key: the repo, `target` the key name) |
| publish | `repo` | `publish.session.create` (`detail.via`; the actor is the person or the CI subject of the session), `publish.session.close`, `setup_code.create` (a setup line of the repo), `device.setup` (a project setup with a setup code; the actor is the person of the code, `detail.device_id` and `detail.new`) |
| publish | `people` | `device.approve`, `device.revoke` (by a person in the UI, or by the device itself with `detail.by` `device`) |
| publish | `ci` | `ci.pipeline.create`, `ci.pipeline.update`, `ci.pipeline.delete` |
| sso | `signin` | `sso.signin`, `sso.signin.failed` (`detail.reason`) |
| nexus | `nexus` | `nexus.source.create`, `nexus.source.update`, `nexus.source.delete`, `nexus.mapping.save`, `nexus.sync.start` (a manual sync, or the sync after a mapping save), `nexus.sync.cancel`, `nexus.cutover` (switch over everything), `nexus.repo.switchover` (`detail.source_repo`), `nexus.rehearsal.create` (`detail.for`, `detail.people`), `nexus.rehearsal.empty` (`detail.count`), `nexus.move.create` (`detail.into`, `detail.group`, `detail.people`), `nexus.merge.start`, `nexus.merge.done` (the counters), `nexus.merge.fail` (`detail.step`, `detail.error`), `nexus.merge.resume`, `nexus.merge.stop` (each with `detail.merge_id`) |

- Client IP addresses appear only in this table and its API. The only other API with an address
  is `GET /api/network/proxy-candidates` (`network.manage`), which lists the addresses of untrusted
  proxies, not of clients. The publish log has none. The access log still records the IP.
- The setting `audit.retention` (default 365 days) sets how long an event stays. A job deletes
  older events every hour.
- **Filters.** `GET /api/audit` filters by `action`, `repo` (a name), `actor` (a username: it
  filters on `actor_user_id`, so it finds people, not CI subjects or the names of failed sign-ins;
  an unknown name gives an empty page) and `since` / `until` (RFC 3339: the events at or after
  `since` and before `until`; another form is 400). A time becomes an id bound: the id of the first
  event at or after it (`FirstAuditAt`, through `audit_events_at`), so every query keeps its keyset
  order on `id` and gains only `id >= since`, and `until` lowers the `before` cursor. Each of the
  eight combinations of actor, repo and action is its own query on its own index.
  `audit_events_actor` is on `actor_user_id` and holds the id as its rowid, so it serves
  `(actor_user_id, id)`. `GET /api/audit/people?q=` (`activity.read`) lists the enabled usernames
  that start with `q`, for the person picker.

## Download statistics

The server counts the downloads of each repo per day (`internal/stats`, table `download_stats`).

- **What counts.** One download per GET that gets a file's bytes from the read path: a 200 or a
  206 of `Reader.ServeAsset` (on every format route; a 206 counts the bytes of its range), an S3
  redirect (302, the file's size), a live proxy stream once it completed (`Live.End`), and a
  merged group file (on the group). HEAD, 304, 404, refused and failed answers do not count, and
  neither do downloads that the edge Worker answers from R2 without the server. A file counts on
  the repo that stores it: a group member's file read through the group counts on the member.
- **Cost.** The read path adds to a map under a mutex (`stats.Counter.Add`, keyed by repo and UTC
  day). The job `audit.downloads` writes the counters every minute in one transaction, one upsert
  per repo and day (a repo deleted since is skipped), and once more at shutdown; a failed write
  keeps the counts for the next one. A crash loses at most the last minute.
- **Redirects.** The same counter keeps the last use of each repository redirect in memory
  (`stats.Counter.UseRedirect`, which `repos.Service.ByName` calls through `OnRedirectUse`) and
  the same flush writes it, one update of `repo_redirects.last_used_at` per used redirect, which
  never moves back.
- **Retention.** The setting `stats.retention` (default 400 days) of the group `activity`; the hourly
  job `audit.retention` deletes older days. A repo delete takes its rows (`ON DELETE CASCADE`).
- **The window.** The last 30 UTC days, today included (`stats.RecentDays`), are kept as totals:
  `download_window_days` (a row per day), `download_window_repos` (a row per repo, index
  `download_window_repos_top (downloads DESC, bytes DESC, repo_id)`) and `download_window_state`
  (the first day of the window). Triggers of `download_stats` add each written row of the window to
  both totals and subtract a deleted one, in the transaction of the write, so the totals are exact
  across restarts and crashes, and a repo delete or a retention delete leaves them. The flush moves
  the window to today (`stats.rollWindow`, in its own transaction): for each day that left it, it
  reads only that day's rows through `download_stats_day`, subtracts them from the repo totals and
  deletes the day rows before the window.
- **`GET /api/stats`** (`activity.read`) writes the pending counters first, then answers
  `{days: [{day, downloads, bytes}], repos: [{name, format, downloads, bytes}], downloads, bytes}`
  for the window: every UTC day of it (today last, `day` as `2026-10-22`; 30 rows read) and the ten
  repos with the most downloads (the first ten rows of `download_window_repos_top`, joined to
  `repos`). It takes no parameters.
- **`GET /api/repos`** gives each repo of its page its `downloads` of the window:
  one seek of `download_window_repos` per repo (`stats.Counter.Recent(ctx, ids)`), so a download
  shows there after the next flush.

## Web UI

Svelte 5 + Vite (no SvelteKit), Tailwind CSS v4, shadcn-svelte with the Maia style, TypeScript
and bun. `bun run build` runs `svelte-check` and `vite build` into `web/dist`, which the binary
embeds. The build's `precompress-dist` plugin (`web/vite.config.ts`, `node:zlib`) writes a `.br`
(quality 11) and a `.gz` (level 9) next to each text file of 1 KiB or more (`.html`, `.js`, `.mjs`,
`.css`, `.svg`, `.json`, `.webmanifest`, `.map`, `.txt`, `.xml`), each only when it is smaller
than the file; images and fonts get none. `web/embed.go` sends the `.br`, else the `.gz`, to a
client whose `Accept-Encoding` accepts that coding, with `Content-Encoding`, the `Content-Type` of
the original file and its `Cache-Control`; the SPA fallback to `index.html` too. A request with
`Range` gets the plain file. Every answer of a file that has a precompressed file carries
`Vary: Accept-Encoding`. Routing is hash-based (`/#/...`), so the URLs that the server and the clients print keep
working. The UI is only a client of the JSON API and has no tests of its own. `web/index.html` holds one inline script that sets the theme before the first paint; a future Content-Security-Policy needs a hash for it. The page registry
(`web/src/modules`), the folder layout, the components, the design rules and the steps to add a
page are in `web/src/README.md`.

- **UI modules.** Each server module with UI has a `ModuleUI` in `web/src/modules/<id>/module.ts`
  (same ID), and `web/src/modules/index.ts` lists them in the server's start order. A `ModuleUI`
  has `pages` (`PageDef{path, title, layout, access, page, nav?, setup?}`), `redirects`,
  `repoTabs` (a repo settings tab that a format names in `repo_tabs`, e.g. the Signing tab of the
  signing module), `repoFields` (the control of a repo field on New repository, e.g.
  `signing_key`) and `formatParts` (a format's named parts of the file details: `maven-artifact`,
  `deb-package`). The router, the sidebars of Settings and Your account (`nav`), the way back of a page and the pages of a
  setup-only session (`setup`) read the registry. `web/src/lib/api/` is the one API client.

- **Words and look.** The UI uses plain words, not jargon (the rename table is in
  `web/src/README.md`). `web/src/README.md` is binding for the UI code.
- **One shell, one width.** Every page sits in one centered container, 1440 px wide at most, with
  24 px side padding (16 px under 640 px). Browse and Settings use the same container, the
  repository page included. Collections use the full width of the container (of the content
  column in Settings). The page reserves its scrollbar gutter.
- **Collections.** A collection is a list by default (`ItemList`): rows in one card, parted by thin
  lines, each with the name and its badges, a muted line of one or two facts, and the state or
  the actions at the end. A handful of things that each have more to say is a grid of cards
  (`CardGrid`, columns of 300 px or more). A table (`DataTable`, one line per row, striped) is
  only where people sort, compare columns or scan hundreds of rows. A row or a card shows the name,
  the state, one or two facts and the action; the rest is on the thing's own page.
- **Server identity.** The UI loads `GET /api/identity` once at start, with the formats. The
  name shows in the top bar, on the sign-in page (its heading and the felt ticket), in the browser
  tab (`<page> · <name>`), in Home's welcome and as the title of All settings; the logo replaces
  the brand's mark in the top bar and heads the sign-in form at every width. Without a logo the
  brand's mark shows. Under the container every page has one quiet line, "Powered by
  Telesphorepo", whatever the identity; no setting turns it off. Settings › General has the
  section Server identity first: the name saves with the form; the logo has its own control (a
  preview, Upload logo or Replace logo, and Remove after a confirm), which applies at once through
  `PUT` and `DELETE /api/identity/logo`.
- **Header.** The top bar is inside the container: the server's mark and name (Home) on the left, a search
  field in the center (480 px at most), and on the right one **Sign in** button when the visitor is
  signed out (hidden on `/#/login`), or **Settings** (for a person who can open an admin page)
  and the avatar menu (Your account, Install the CLI, the theme, Sign out) when the visitor is
  signed in. The search shows on the Browse pages only, not on Home (its own large field) and not
  on the Settings and account pages; under 640 px it is an icon that opens the Search page. The
  search field holds the query only on the Search page. Sign out returns to Home. The page
  `/#/login` is the only place to sign in.
- **State on the thing.** No page opens with a status row or a summary banner. A switch shows that
  it is on. A method shows in its list. A private repository has a lock after its name, and a
  public repository has no mark. A badge marks the one row that needs attention.
- **Address notice.** The one notice of the whole app sits at the top of the container: a person
  who holds `settings.general` (not a setup-only session) opened the app at an origin that is not
  `base_url`. It says "This server's address is set to <base_url>, but you opened it at
  <origin>. Clients and snippets use the set address." with **Use <origin>** (saves `base_url`,
  with a toast) and **Keep** (hides it for that origin in this browser, in `localStorage`). It
  never shows under the Vite dev server, at a loopback origin, or when `TR_BASE_URL` sets
  `base_url`. It reads `GET /api/settings` when the person signs in and after they leave
  Settings › General.
- **Cards.** Each area of a page is a card (`Section`): a header with its title and its actions,
  then its content. Every list, grid and table is in a card too. Cards are 32 px apart on Browse pages and 28 px in Settings. A form card holds a
  two-column grid: the label above each control, every control the width of its cell, and a long
  field across both cells. The grid is one column under 640 px. A setup is a row of numbered cards
  (`Step`).
- **Type, spacing and states.** One sans-serif family for all text. Monospace is for code blocks
  and copy-paste snippets only. Body text is 15 px, help and facts 14 px, section titles 17 px and
  the page title 26 px. One spacing scale (4 px base) and one radius (8 px). A table row is 48 px
  on Browse pages and 44 px in Settings, which are a little denser; a list row is at least as
  tall. Hover and
  selected states fill the whole row or item. There is no left-border accent. Every page has
  loading, empty and error states.
- **Loading.** The UI shows no skeletons. A loading area shows nothing for 300 ms, then a quiet
  spinner in the space the content will take. A reload keeps the content on screen.
- **Live data.** A page that follows a process on the server (a backup, a storage move, a Nexus
  sync, a region that fills) refreshes on a timer through one `Live` (`web/src/lib/live.svelte.ts`):
  it pauses while the tab is hidden or the browser is offline and refreshes at once on return,
  never runs two requests at once, and waits longer after each failure. A `LiveStatus` says
  "Updated 4 s ago" with how often it refreshes, and says plainly when updates stop. A slower
  process outside the page shows its cadence where its value is, from the server that owns it
  (the Worker's gap check, the count of a region's files, the move's progress saves, the next
  Nexus sync). A repository delete follows its operation in a toast the same way.
- **Icons.** An icon marks a meaning (format, lock, state) and sits bare, never in a tinted square.
  Each format supplies its own SVG (`format.Spec.Icon`, returned by `GET /api/formats`):
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
- **Home** (`/#/`, anyone). A short page centered in the container: one line of welcome
  ("Welcome to <server name>") and a large search field (the top bar's search: files by path, with the `/` shortcut; the top bar has no search on Home), then
  the doors, when at least two would show: large cards side by side (stacked on a phone), each a
  bare icon, a title, one sentence and the whole card a link. **Publish** opens `/#/publish`, for a
  signed-in person who holds `repo.publish` somewhere (from the grants of `GET /api/me`). **Browse** opens `/#/browse`. **Administer** opens
  `/#/settings/overview`, only for a person who would see a card there. Each card is as wide as one
  column of a three-column row, so two cards keep that width, centered. A visitor or a reader sees
  no doors (the top bar has Sign in). Then, for everyone, the six repositories published last
  (`GET /api/repos?sort=published&limit=6`): a heading with "See all" (to `/#/browse`) at its end over up to six repository cards
  (three on a phone), with no card around them. A card holds the format icon, the name (with a
  lock when the repo is private), the description up to two lines, and at the bottom the format,
  with the kind of a proxy or a group ("Maven / Gradle · Group of 3 repositories"). **Your repositories** are the repos where the
  person holds a permission beyond `repo.download` (`perm=` every other repository permission); a
  person with none sees **Repositories**, the repos they can see (a visitor: the anonymous-read
  ones), a second call. With no repo at all, the empty state of Browse.
- **Browse** (`/#/browse`, anyone; visitors see the anonymous-read repos). A toolbar on one row
  holds a filter box (the start of the name, `q`, 250 ms after the last key), a format select ("All
  formats" first, then each format of the server with its icon; `format`) and **New repository**
  (`repositories.all`) at the end; each asks the server again. The repos are one compact table
  sorted by name, 100 a page of `GET /api/repos`, the next page loading as the end comes into view, a row each and the whole row a link: the format icon and the name (with a lock
  when the repo is private), the format (only when the server has more than one), Proxy or Group (a
  column once one is listed), and the description on one line. On a phone a row stacks: the
  name, then the kind and the description. Files, size and last publish show in
  Settings › Repositories and in the details of a repo's top folder; a group shows the files it
  serves to the reader (see **Group counters**), and "Counting…" until the server has counted
  them. In a group's Files tab a proxy's version or file that is not cached yet has a "Not cached"
  badge, and every file of a build names the member that serves it. The whole card opens the repo.
- **Publish** (`/#/publish`, signed in). The hosted repos the person holds `repo.publish` on
  (`GET /api/repos?perm=repo.publish&kind=hosted`, 200 a page with Load more; a group is not
  listed, since its publishes go to a hosted member) as the same cards, each opening its Publish view, a card per format when more than
  one is present. With exactly one (and no next page), the page replaces the history entry with `/#/repo/{name}/publish`; with none, it says to
  ask an administrator for publish access.
- **Search** is `/#/search?q=&repo=`: the top-bar field holds the query, and a repository picker
  picks the repo (`RepoPicker`: the server's repos whose name starts with what you type, 20 at a
  time, groups left out). The hits are grouped in the browser: a card per repo, in it a row per folder
  that holds hits (the folders under one parent fold into the parent's row, so an artifact is one
  row with its version folders), each row with its folders and files as links; the checksum and
  signature files (`.md5`, `.sha1`, `.sha256`, `.sha512`, `.asc`, `.sig`) of a hit fold into it.
  The matched text is marked in each path, and a count says how many files the loaded pages hold
  ("350+ files" while more pages wait). A row opens its folder, a link its folder or file.
- **Repository page** (`/#/repo/{name}/...`). The header shows the name with a lock for a private
  repo, the format (icon and title) and the description. **Upload** and **Settings** show for users
  who may use them. A proxy shows its cached files and has no Upload and no Publish view. A group
  has the same tabs, read-only: where Upload would be, the header says "Read-only: this group serves
  its members' files". Files is the Files tab of a hosted repo of its format, every view, the
  artifact and version pages and the file details included (see **Group versions**): each file and
  version shows the member that serves it muted after its name, and the details of a file or a
  version name it ("Served by"). Use this repository lists the members first, and Publish to this
  repository shows to a person who holds `repo.publish` on one of its hosted members: a select of
  those members (the first by default, the choice in `?to=<member>`), then that member's Publish
  view. One page loads the repo once and switches between these views:
  - `files/<path>`: the folder path and one of the views below, with a details column for the selected
    file or artifact (SHA-256, checksum links, the CI build, a curl and wget download, and the part
    of the format's UI module that the first matching `file_details` entry names: the artifact
    snippets of a pom or a jar, of a `.deb`). Delete shows to a person who holds `repo.delete` and
    acts with the UI session; its confirmation lists the delete plan (the file count and the
    delete notes of the format).
  - `use`: **Use this repository**. An address card first. A public repo then has one card, "Add it
    to your build". A private repo has two numbered steps: create a read token, then add it to the
    build; the snippets already hold the credentials.
  - `publish`: **Publish to this repository**, for users who can publish (others see a lock and
    "You cannot publish to X"). The **Set up a project** card comes first. Its button asks for a
    fresh second factor (a single sign-on account signs in again at its identity provider through
    `GET /api/auth/oidc/setup?repo=` and returns to the repo page with `?setup_grant=`, which the
    page uses at once and removes from the address). Then `POST /api/publish/setup-codes` gives the
    setup line for the OS of the browser (macOS or Linux, or Windows, on a toggle) with the time at
    which it ends (10 minutes). The line is `curl -fsSL <base>/install.sh | sh -s -- setup '<link>'`
    or `& ([scriptblock]::Create((irm <base>/install.ps1))) setup '<link>'`, and the link is
    `<base>/#/setup/<code>`. Below the line, a format of the `cli` publish category (`publish` of
    its descriptor) shows `telesphorepo publish`; a format of the `helper` category shows its
    native commands. The snippets of the format stay behind one **Set it up by hand** disclosure.
    Then From CI (a full GitHub Actions workflow, `on: push` to `main`, and a full declarative
    Jenkinsfile that binds the credential `telesphorepo-id-token` to `OIDC_TOKEN` only in the
    publish stage, because Jenkins makes the token when `withCredentials` starts and the server
    takes a token for 10 minutes only) and By hand. The tabs are the tools of the snippets, with
    the labels of the `/usage` answer. A CI file opens on its `diff`, the change to the reader's
    own workflow or Jenkinsfile (**Add to a workflow**), with **New workflow** (the whole file)
    beside it; the choice is remembered in the browser. Snippets of one tab with the same file,
    code, change and note show once, titled with every tool (`.github/workflows/publish.yml
    (Gradle and Maven)`). The notice of a paused repo (a Nexus mirror) shows once,
    above the tabs.
  - `/#/setup/<code>`: the page of a setup link. It says to run the line in a terminal in the
    project folder and shows the line for the OS of the browser. It makes no API call, and the
    code stays in the fragment.
  - `/#/install?os=&cpu=`: **Install the CLI** (anyone). The account menu and the header of the
    Set up a project card link to it. One tab per OS (Linux, macOS, Windows; without `os` the
    OS of the browser). **Install**: `curl -fsSL <base>/install.sh | sh` or
    `irm <base>/install.ps1 | iex` and what the script does with `PATH`. **Install by hand**: the
    archives of the OS from `GET /cli/SHA256SUMS` as a list, each row the archive name, its CPU,
    the start of its SHA-256 with a copy button, and a download link; a row picks `cpu` (default
    `arm64` on macOS, else `amd64`), and below the list the
    commands that download that archive, check its SHA-256 (`sha256sum -c`, `shasum -a 256 -c`
    or `Get-FileHash`), unpack it into `~/.local/bin` or `%LOCALAPPDATA%\Programs\telesphorepo`
    and make the helper name. **Update**: `telesphorepo update`. A build without CLI archives
    (404 `no-cli-downloads`) shows one empty state in place of the cards.
  - The devices lists and the approval page name no client. They show "Telesphorepo CLI".
  - **New CI pipeline** (`ci.pipelines`) on the Publish tab opens
    `/#/settings/ci-pipelines/new?repo={name}&back=/repo/{name}/publish`.
  - `upload?folder=`: a full page. It starts with the fresh second-factor check, then one drop
    zone takes files or a folder (as the format's `upload` allows), followed by the upload plan of
    the server (the items, the files left out, the fields), the upload and the progress.
    The publish session lives in memory only. The limits and the folder filter are as in **Files,
    uploads and deletes**.
- **Views of the files.** A switcher offers **List**, **Tree** and **Columns** everywhere, and
  **Cover Flow** at an artifact or a snapshot that has several builds. The choice is remembered per
  user in the browser, for each place. Tree, Columns and Cover Flow fill a box of one height (at
  least 22 rem, at most 36 rem, otherwise the window height minus 24 rem). The list keeps that
  height as its minimum on wide screens, so a switch does not move the details column or the
  toolbar.
  - *List* shows a folder as a table (name, size, published, and who published it while the
    details column is closed). The versions of an artifact are a list: each row the version with
    Latest or Snapshot, then when it was published, its commit and its builds. A snapshot's
    earlier builds are a list the same way, and the files of a build a table (name, kind, size).
  - *Tree* shows the whole repo as an expandable tree. A folder loads its children from `/tree` the
    first time it opens, a page at a time. Up and Down move, Right expands or steps in, Left folds
    or steps out, Home and End jump, Enter opens. A version folder shows the files of its latest
    build and a row "N earlier builds".
  - *Columns* are Miller columns.
  - *Cover Flow* is a timeline of tickets for the versions of an artifact, or the builds of a
    snapshot. It replaces the list in the same box. The caption of a version has Open and Delete;
    the caption of a build has "Show N files", which switches to the list with that build chosen.
- **Versions and builds.** A format that sets `format.Spec.Versions` and `Builds` groups the files of an
  artifact into versions and the files of a version into builds. The artifact folder and the
  version string name a version: `#/repo/x/files?path=pool/main/u/urn/&version=3.0~SNAPSHOT`.
  For Maven, the version folder (`com/acme/lib/1.0/`) also opens that version: its parent is the
  artifact folder and its name the version. `?build=` keeps a chosen build in the address.
  - *Maven:* an artifact folder is `<group path>/<artifactId>/` with one folder per version. A
    `-SNAPSHOT` version has timestamped builds (`20261014.103000-3`) and the build `SNAPSHOT` of
    its files without a timestamp; a release has one build. The format reads `maven_versions` and
    `maven_builds`, which its hooks keep in the transaction of every write and remove: a file
    `<artifactId>-<version>[-<classifier>].<ext>` (or the timestamped name of a snapshot build;
    not a checksum or a signature) records its build and its version, and the remove of the last
    file of a build deletes the build, and of the last build its version. A page of versions or
    builds is one seek past its cursor, plus one range read of the files of each build on it (the
    newest build of each version), at any number of versions, builds or files. Versions sort in
    Maven order by a stored key: numbers numerically; trailing zeros and `ga`, `final`, `release`
    count as nothing (`1.0.Final` is `1`); `alpha`/`a`, `beta`/`b`, `milestone`/`m`, `rc`/`cr`
    and `snapshot` sort before the end of a version, `sp` and other qualifiers (alphabetically)
    after it, numbers after every qualifier; versions equal in that order sort by their strings
    (`1` before `1.0`). Builds sort by timestamp and number, the build `SNAPSHOT` last. The
    snippets of an artifact folder without a version use its newest release, else its newest
    snapshot: one seek of `maven_versions_latest` each. On the first start with these tables,
    the module's start indexes the files that the Maven repos hold already, then serves.
  - *Debian:* the format reads `deb_packages` through the order keys of `deb_package_order`. An artifact
    folder is `pool/<component>/<prefix>/<package>/`; its versions are those of the package of that
    name in that component whose files lie in the folder. `Artifact.Kind` is `package`. A Debian
    version `<base>~SNAPSHOT<sep><stamp>` (`<sep>` one of `.`, `+`, `~`, `-`; `<stamp>` 8 to 14
    digits, `yyyyMMdd[HHmmss]`) is a build of the snapshot version `<base>~SNAPSHOT`; the build id is
    the whole Debian version. Any other version is a release with one build. The files of a build
    are its `.deb` files, one per architecture: `classifier` is the architecture, `extension` is
    `deb`. Versions sort by Debian version order (the `dpkg --compare-versions` rules: epoch,
    upstream, revision, and `~` before anything), so `3.0~SNAPSHOT` sorts before `3.0`. Builds sort
    by stamp, newest first; a build without a stamp sorts below the stamped builds of its version.
    A page of versions seeks from one version to the next through `deb_package_order_page` and
    reads the files of each version's newest build and counts its builds (index entries only); a
    page of builds reads the rows of its builds and stops after the page.
  - `published_at` is the newest update time of the build's files, and `build` is the CI build
    record of its main file (else of any file); null when none.
  - Snippets of a Debian package: the apt setup for the distribution and component of the package,
    then the install. A release and a build (a `.deb` file) are pinned
    (`apt-get install urn=3.0~SNAPSHOT.20261014211855`); a snapshot version and the latest mode
    install `urn` without a version, so apt takes the newest.
  - A delete of a version removes its `folder` when it has one (Maven, NuGet), with no request for
    its builds. Else (Debian) it removes the files of all its builds, read 200 builds a page.
  - A `?build=` in the address that the loaded pages of builds lack is read with `build=<id>` of
    `GET /api/repos/{name}/builds`, alongside the first page.
- **Settings and Your account.** Two areas with the same shell: a sidebar 240 px wide inside the
  container, a menu under 768 px. Settings is the admin panel; its paths are under
  `/#/settings/`, and `/#/settings` opens the overview. A person who can open no admin page sees
  no Settings in the top bar.
  - *All settings* (`overview`), the first item, above the groups: about this server. The name of
    the server, its version (`GET /api/changes`: a release as its tag, a build of `main` as Canary
    and its commit, a local build as Development) and, for a holder of `settings.general`, its
    public address; New repository, New account and New CI pipeline as doors for a person who
    may. Then one row of stat tiles: from `GET /api/overview` the repositories, the files, the
    stored size and the number of people, from `GET /api/publish/trusts/count` the CI pipelines,
    and the signing keys, each for a person who can open its page and a link to it; a count that
    stopped reads "10,000+". Every repository for a holder
    of `repositories.all`, else "Your repositories" (more than download). Then the eight largest
    repositories (`largest` of `GET /api/overview`) as size bars with their format's icon; for a holder of `activity.read` the downloads of the
    last 30 days (`GET /api/stats`: the total, the bytes served, a column per UTC day and the five
    most downloaded repositories) and the last five events of Activity. A person who can open no Settings page goes on to Your account.
  - *Your account* (`/#/account`, every signed-in person, from the avatar menu) has its own
    sidebar with one group, Your account: Overview (`/#/account`), Sign-in and security
    (`/#/account/security`), Devices (`/#/account/devices`) and Tokens (`/#/account/tokens`: read
    tokens and publish tokens, two cards on one page). `/#/settings/account`,
    `/#/settings/security`, `/#/settings/devices` and `/#/settings/tokens` open the same pages (the
    server prints `/#/settings/tokens`).
  - The navigation shows each page only to a person who holds its permission (see **Auth
    model**, Permissions). A path the person cannot open shows "You cannot open this page".
  - *Repositories*: Repositories (`repositories`, `repositories/new`, `repositories/{name}/{tab}`):
    every repository for a holder of `repositories.all`, else the repositories where the person
    holds a permission that manages them. New repository and Delete need `repositories.all`; Make
    public and Make private need `repo.settings`.
  - *People and CI*: People (`people`, `people/{name}`), CI pipelines (`ci-pipelines`, `ci-pipelines/new`,
    `ci-pipelines/{id}`) and Networks (`networks`, `networks/new`, `networks/list/{name}`).
  - *Server*: Sign-in options (`sign-in`), Signing keys (`signing-keys`,
    `signing-keys/new`), Storage (`storage`), Nexus migration (`nexus`, `nexus/{id}`), Network and
    proxy (`proxy`), General (`general`) and Activity
    (`activity?view=&area=&action=&repo=&actor=&when=&from=&to=`: the audit log and the publish
    log; the audit log filters by one action of `GET /api/audit/actions`, picked by its area (the
    subsystem) first, by a repository, by a person and by a range of days in the reader's time
    zone (Today, Last 7 days, Last 30 days, or Custom from one day to another); the events show by
    day).
  - A page one level down has one link above its title, "← <parent>", to the page one level up
    (CI pipelines over a CI pipeline, Repositories over a repository's settings). A page of the
    sidebar has none: the sidebar is the way to it. There are no breadcrumbs.
- **Repository settings.** A page with the tabs Overview, Access, Format settings and Signing
  (Debian). Overview, Format settings, Signing and Remote need `repo.settings`; Access needs a permission
  that manages the repository (`repo.access`, `repo.settings`, `ci.pipelines`, ...). A person sees only the tabs they can open, and a link to a tab they
  cannot open goes to their first tab. The header shows the name with a lock for a private repo.
  **Delete** (`repositories.all`) is an action at the end of the header, on every tab; it opens a dialog that
  asks for the repo name.
  - *Overview:* description and visibility; for a holder of `repositories.all` the name, whose
    change asks first and says that the old name keeps working as a redirect; and Redirects, the
    old names with their last use and Remove after a confirm that names what stops working (see
    **Repository redirects**). An old name in the address opens the settings under the new name.
  - *Access:* two cards, Download and Publish, that answer from
    `GET /api/repos/{name}/access?admins=1` and the unsaved edits. Each person shows once, in the
    card of their highest access, with what they hold beyond it as badges (Delete files, Edit
    settings, Manage access, Manage CI pipelines). **Download**: the Public switch and **Give
    access** in its header; the people whose grant has no Publish (while private, or when someone
    holds such a grant on a public repository); then from where ("From any network", "Only from
    GitHub Actions"). **Publish**: the people with Publish, then the holders of All repositories
    as one read-only row ("2 administrators") that opens to their names; the CI pipelines; then
    from where. While nobody can publish (`publish.blocked`: a group, a proxy or an operation),
    the card says why in place of the CI pipelines and the networks. **Give access**
    opens the grant dialog: people found through `GET /api/people`, then the permission picker,
    side by side in a wide dialog. **Change** opens the picker for one person, **Remove** and a
    pick with **Remove access** ask first. These save at once through `PUT /api/grants`. The
    Public switch (in the download card's header) and **Change** of from where show to a holder
    of `repo.settings`, Add CI pipeline and removing one to a holder of `ci.pipelines`. **Add CI
    pipeline**, **Add network list** and **Add addresses** each open a short form in a dialog.
    These edits save together from the save bar; the Public switch saves through
    `PATCH /api/repos`. Add CI pipeline adds existing pipelines, or
    links to `/#/settings/ci-pipelines/new?repo={name}&back=/settings/repositories/{name}/access`.
    Each person links to their page, where the same access is also edited.
  - **Permission picker.** The grant dialog (Access tab, person page, bulk Give access) and the
    person page use one picker, read from `GET /api/permissions`: one group of checkboxes per
    subsystem, each with the subsystem's label and help, **All** and **None**, and one help line
    per checkbox. Checking a permission checks what it needs (`repo.publish` checks
    `repo.download`, `ci.pipelines` checks `repo.publish`), and clearing one clears what needs it;
    the help line says why. A permission the caller does not hold is turned off. There are no
    presets. The phrases of a set ("Download, publish", "Everything", "Administrator") come from
    the registry's labels.
  - *Signing:* the current key, the `key.asc` address, change the key or create a new one.
  - A proxy has the tabs Overview (the remote, when it last answered or its last error, **Test
    the remote**, the address for a build, the cache size, the groups it is in), Access and Remote
    (address, credentials, the two times). A group has Overview (the address for a build and the
    members in order with Move up, Move down, Remove and Add repositories, saved from the save
    bar) and Access. A hosted repository's Overview lists the groups it is in.
  - Create a repository at `/#/settings/repositories/new`. The first choice is a repository, a
    proxy of a remote repository or a group of repositories; each lists only the formats that take
    it. A proxy asks for the remote address (with the well-known remotes as choices) and optional
    credentials, and offers **Create anyway** when the check of the remote fails. A group picks its
    members, hosted first. A repository asks for name, format (with the format icon),
    description, visibility. Debian adds architectures and the signing key. For a new key, the page
    calls `POST /api/signing-keys` and then `POST /api/repos`. If the second call fails, the page
    deletes the new key. **Create** is in the footer of the last card.
- **People** (`people.manage`). A list shows each account with its avatar, its name and its role as
  a badge: "Administrator" for a person with every server permission, else the phrase of the
  server permissions held, or none. Under the name: the email, the sign-in method (only when
  someone uses single sign-on) and when the account was added. A row opens the
  person's page (`/#/settings/people/{name}`): an identity card (avatar, how the person signs in,
  the email, a Disabled badge; the role shows only under Server permissions) with Reset password (only for a person whose permissions the caller
  all holds) and Disable account or Enable account; then the server permissions as a role:
  Member (none), Administrator (every one) or Custom, which lists one switch per server permission
  under the Settings sidebar group of its page. A permission that another switched-on one needs is
  locked on, and one the caller does not hold cannot change. The role is saved from the save bar
  after a confirm through `PUT /api/users/{name}/permissions`; then the
  person's permissions on each repository. **Give access** opens the grant dialog with the
  repositories and the picker. The repository list shows each repository where the person holds a
  grant, with the phrase of the person's permissions under its name, and one line under it counts
  the public repositories they can also download from ("Can also download from the 8 public
  repositories.", a link to Browse) and **Change** and **Remove** (asks first) on its row; it
  takes a pick, with **Change permissions** and **Remove access** (asks first). Each repository action saves at once. A holder of `repositories.all`
  reaches every repository, and the page says so. When single sign-on manages the permissions of an
  account, the role and its permissions show read-only ("Single sign-on manages these
  permissions"), with Open sign-in options for a holder of `signin.manage`. The
  People list takes a pick of every row except your own: **Disable** asks first ("Disable 4
  people? They are signed out and cannot sign in."), **Enable** acts at once. **New account**
  opens a short form; after Create, the new person's page opens, where the permissions are set.
- **Bulk actions.** Repositories (**Make public**, **Make private**, **Delete**), the List
  view of the Files tab (**Delete**, files and folders mixed), People, a person's repositories and
  a repository's people take a pick (`bind:picked` of `DataTable` or `ItemList`). While rows are picked, a bar in
  place of the header row shows "12 selected", the actions, "Select all 50" and "Clear". Every
  delete asks first with the count and the first five names; a delete of more than one repository
  asks for the count. A toast says what changed.
- **CI pipelines.** A card per trust policy: the name, the CI system with the GitHub repository or
  Jenkins host (less what the name says), its conditions (branches, tags, the workflow file, jobs)
  as ticket badges, and up to three of the repositories it can publish to ("and N more").
  The card opens the pipeline. Only a pipeline that is off has a badge ("Off"); the switch on its
  page shows the state.
- **CI pipeline page.** A new pipeline has four steps: where the job runs (GitHub Actions,
  Jenkins, another CI system), which runs can publish, the repositories, and the publish step for
  the workflow or Jenkinsfile. An admission ticket beside the steps (above Create on a narrow
  window) fills in as the form is filled: the CI system and the jobs it admits, and the
  repositories it admits them to. The page builds the claims from answers a developer knows.
  GitHub Actions: the repository `owner/name` (`repository`), branches and tags (`ref`, as
  `refs/heads/<b>` and `refs/tags/<t>`) and an optional workflow file (`workflow_ref`
  `<repo>/.github/workflows/<file>@*`); issuer `https://token.actions.githubusercontent.com`.
  Jenkins: the address (issuer `<address>/oidc`) and job names such as `team/app` (`sub`
  `<address>/job/team/job/app/`), with optional pasted keys. **Edit the token conditions** shows
  the claims, the audience and the keys instead; another CI system always uses them. **Use the
  simple form** reads the claims back, and asks first when they say more than the answers can.
  The GitHub repository and workflow file fields take what people copy from GitHub: owner/name,
  the address of the repository (with or without `.git`, or the clone address), of a file in it
  (`/blob/`, `raw.githubusercontent.com`) or of the workflow's Actions page
  (`/actions/workflows/<file>`); on a paste or when the field is left, the field shows what the
  address names, and a file's address also fills the repository. The Jenkins address and the
  jobs take a job's address (`<address>/job/team/job/app/` becomes `team/app`). The placeholders
  are full addresses. The workflow or Jenkinsfile comes from `GET /api/repos/{name}/ci` for the
  chosen repository and each build tool of its format, with the branches and tags of the
  pipeline as the trigger, and the workflow file of the pipeline as its name; tools that give the
  same file show as one, and the build tool choice shows only when the files differ. `?repo=<name>` starts with that repository chosen; `?back=<path>`
  (only a `/settings/` or `/repo/` path) is where Create and Cancel return. When the `base_url`
  setting is empty and the pipeline has no audience of its own, the page warns that CI jobs
  cannot publish and links to General.
- **Signing keys page.** A card per key with the end of its fingerprint (copy takes all of it),
  the repos that use it (as links), when it was added, a public key download and delete (turned
  off while a repo uses the key). The new-key page creates
  an Ed25519 key or imports an armored key with its passphrase.
- **Networks page.** A card per list, in two grids. A downloaded list shows its number of ranges,
  what uses it, its last refresh, a badge when the last refresh failed, and a Refresh button. Your
  own lists show their number of ranges, what uses them and when they were added. **New network
  list** is the action of the "Your lists" section. The list page edits name, title, description
  and addresses (a list of one range per row, 200 at a time), lists where the list is used, and
  deletes. A downloaded list shows its ranges read-only.
- **Pasted addresses.** The network list editor, the Access tab (the Add addresses dialog of each rule) and
  the Network and proxy page take addresses through one paste box (`RangePaste`). The box sends the
  text to `POST /api/ip-ranges/parse` 400 ms after the last key, or at once on Check. It shows
  "8 ranges found · 0 invalid · 0 duplicates removed", the invalid lines with their numbers and
  reasons, and the ranges. **Add** appends the ranges that are not there yet; **Replace** puts them
  in place of the current addresses. Nothing is saved until the page's own save.
- **Network and proxy page.** It edits the literal entries of `trusted_proxies` (private networks
  and other addresses, a list with remove buttons and the paste box) through the settings PATCH,
  and the IP lists of the trusted proxies (Cloudflare, and any other list) through
  `PUT /api/network/trusted-proxies`. A list that `TR_TRUSTED_PROXIES` names shows checked and
  cannot change. The proxy candidates are a list of detected proxies: each the address, then the
  header, the number of requests and when it was last seen, and **Trust**. A Caddyfile hint takes its host from `base_url`. A setting
  that an environment variable sets shows a badge and cannot change.
- **General page.** One form with a section per settings group whose page is empty (see **Settings
  model**), with the labels and help of the server, for the groups whose permission the person
  holds. **Storage**, **Sign-in options**, **Network and proxy** and **Edge delivery** edit their
  groups on their own pages; each group names its page.
- **Storage page.** The location card and, for an S3 bucket, the bucket card, each with **Test** in
  its footer. A running storage move is a card with progress, when the copier last saved it (`progress_at`)
  and **Cancel**. An ended move with failed files lists them: the path with a copy button, then
  the reason.
- **Edge delivery page** (`/#/settings/edge`, `edge.manage`, Server group). Status: on, stopping
  or off (and why the settings cannot run it), the Worker build with an Outdated badge when the Worker lacks a feature of the
  server (before the first report: the Worker reports at its first gap check),
  the last gap check with its cadence (`gap_check_every`: the next check, or how late it is), the home bucket, and the last check of the Worker (a badge of its outcome,
  when, and its message). **Check now** in the header runs `POST /api/edge/check`; the page runs it
  by itself when it opens and every 30 seconds while the Worker has not reported the server's
  version (a 429 of another tab is silent). Regions: a card for each of the six R2 location
  hints (the select of the home region is in Settings, with the on switch and the access lifetime): its area, the state as a badge (not used, filling,
  copying files, ready, waiting for a deploy), the bucket (an input while the region serves,
  default `<home bucket>-<hint>`), the pointers waiting in the outbox when there are any, the last
  error, and a "Serve from it" switch at the bottom; the home region's card shows "Home bucket"
  and has no switch. Save
  runs the group's **Test** first; a failed probe shows on its card and nothing is saved. Clearing a
  saved region asks first ("The server stops writing to the bucket ... Deploy the Worker again,
  then delete the bucket in Cloudflare"). The card of a filling region shows its progress,
  refreshed every 3 seconds (pointers written of the total with a bar, the files missing), and
  what it waits for: edge delivery off (the Worker copies files only
  while it is on), pointers being written (with the count waiting), the Worker's first gap check,
  the Worker of the server version, the first count of its files, or "Copying files" with the
  files missing, when that count last changed, and the copy pass that runs (or what the last pass
  queued); under the cards the cadence of the count (`blob_check_every`) and of the Worker's copy
  (`gap_check_every`). Settings: `edge.enabled` and `edge.access_ttl`. Deploy: numbered
  steps: download the edge Worker (the one button, `GET /api/edge/worker.zip`; in its place the
  `worker_missing` sentence of the status when the build has none), unpack it and open a terminal
  in `telesphorepo-edge`, then `bun install`, `bunx wrangler login` (with `CLOUDFLARE_API_TOKEN`
  for CI), `bun run provision` (with `CLOUDFLARE_ACCOUNT_ID` for a login with several accounts)
  and `bun run deploy`, each a `CodeSnippet`; when to repeat them; "Deploy needed" after a
  storage, region or version change; and behind a `LearnMore` the `wrangler.jsonc` of a checkout
  in a `CodeBlock` with its download. Speed test (between Regions and Settings, its own
  component `pages/server/edge/SpeedTest.svelte`): a select of the files of
  `GET /api/edge/speedtest` (repo, file name and size; "Another file" takes a path or a whole
  address on this origin) and a select of the bucket: the home bucket and each ready region;
  **Run** downloads the file once from that bucket, **Run all** once from each bucket in turn. A
  run first sends a HEAD without a choice to learn the bucket the Worker picks for this browser.
  The results are a list with one row per bucket, ranked fastest total first (the runs that went
  elsewhere, failed or still run after them): the bucket, the time to the first byte (its tooltip
  the `Server-Timing` of the Worker) and the total time under it, and the speed at the end, in
  tabular figures; the row of the bucket the Worker picks is marked Nearest and the fastest Fastest, and a row says what
  happened instead when the Worker did not read that bucket (served from another bucket, pass
  refused, or went to the server). A line under it
  says the times are from this browser's location and names the Cloudflare data center of
  `CF-Ray`. The page takes a pass at the first run and keeps it in memory until 30 seconds before
  it expires. Opened at another address than the base URL, the section says to open the page at
  the base URL and offers no run; without a Worker report, with an outdated Worker, or
  with edge delivery off it says first what the downloads will do. The access
  tab of a repo shows the download notes under the Download section.
- **Sign-in options page.** Three numbered cards: connect your provider (with **Test** in its
  footer), permissions from the provider (the claim `oidc.permission_claim`, and the map `oidc.permission_map` as groups: each value of the claim with its server permissions as a role (Member, Administrator or Custom), with Change, Remove and Add a group), and how people sign in (`oidc.session_ttl`,
  `signin.password_login`). The labels are plain words; each
  field also names its term at the provider (Issuer URL, Client ID). The page tests the saved
  settings when it opens and shows the result only when the test fails. **Test** checks the unsaved
  edits and marks the fields that are wrong.
- **Nexus migration.** A card for each Nexus server with its status, address, repositories and
  next check. Adding a server opens a dialog. A server's page shows its status as a badge and a
  line next to its title, then the repositories to copy (one pick table: repositories and
  proxies, then the groups with their members, then what cannot be copied; a click on a row picks
  it, and each row says its state after a save), the running or chosen sync with its progress and log, the syncs (a row opens its
  details) and the connection. Sync now or Cancel the sync, Switch over and **Delete** are in the
  header; while a sync runs, Delete says "Cancel the sync first".
- **Your account.**
  - *Overview:* an identity card with the avatar, the name and the role ("Administrator", the
    phrase of the server permissions, or "Member"). Then two columns (one under 1024px): a card
    with the repositories you can use, and a card each for your devices and your recent publishes.
    Each card lists at most five rows, with **See all** to the full list (Browse, Devices,
    Tokens). What you can do in a repository shows under its name only when the rows differ.
  - *Sign-in and security:* a Sign-in card with one row, "Password" and **Change**; **Change**
    opens a dialog (a single sign-on account sees "Single sign-on" and a line that sends it to the
    identity provider). A Two-step verification card lists the methods: each app, each passkey and
    the recovery codes are rows, and a method that is not set up is one row with its Add action.
    The rows are plain rows in the card, with thin dividers between them.
    Without a second step, this card comes before the Sign-in card. A Recovery codes card shows
    when a second step exists.
  - *Devices:* a list in a card: each device's name, then the client, when it was last used and
    when it expires; its state as a badge and **Revoke**. A holder of `people.manage` sees every
    person's devices, each with the person's avatar and name. An empty list is one row.
  - *Tokens:* read tokens and publish tokens, each a list in a card: a read token's name, when it
    expires and when it was last used, and **Revoke**; a publish token's repository, where it came
    from, when it started and ends, and its state as a badge.
- **Ticket moments.** A new publish token prints as a felt ticket with the repository, the end
  time and its number; ending the token punches the ticket. The device approval page shows the
  device as a ticket with the code on its stub; approving tears the stub off. Each animation takes
  under 400 ms and does not play under `prefers-reduced-motion`.
- **Empty lists.** An empty list is one plain row (what is empty, why it is useful, one action),
  never a table without rows. Inside a titled card it does not repeat the card title and has no
  dashed frame. The header action of the card is hidden while the list is empty, so the row holds
  the only action.
- **Auth in the UI.** A 401 outside the sign-in routes signs the user out. A 403
  `second_factor_required` sets the setup-only state; the UI shows its message as a sentence, never
  the code. A setup-only session goes from the sign-in, and from every page but the sign-in, to
  Sign-in and security, which says why and, once a method is added, offers the way on. Signing out
  returns to Home.

## CLI

| Command | Does |
|---|---|
| `telesphorepo-server [serve]` | Runs the server from the boot env and DB settings |
| `telesphorepo-server dev [--empty]` | Zero-config local server. Data goes in `./.telesphorepo-dev`. The first run creates `admin` and the demo data (see **Demo data**) before the server listens; with `--empty` it creates one empty anonymous-read repo per registered format instead, named after the format. The banner prints the demo users, each format's usage snippets and "2FA not enforced (dev)". Dev mode makes the second factor optional (see **Auth model**) |
| `telesphorepo-server user add NAME [--admin]` | Asks for the password twice on a TTY, or reads one line from a pipe. Every `user` command writes an audit event with the actor `cli` |
| `telesphorepo-server user passwd NAME` | Resets the password and signs out the user's sessions. Refused for SSO users |
| `telesphorepo-server user reset-2fa NAME` | Removes the TOTP factors, passkeys and recovery codes of a user, so the user sets up a new factor at the next sign-in |
| `telesphorepo-server backup DIR` | One backup of the data dir (`TR_DATA_DIR`, else `./data`) into `DIR`, as the job writes it (see **Backups**). It opens the database read-only, so a server may run on the data dir; it reads the storage settings from the `TR_*` environment, else the stored rows. It applies no retention and writes no audit event |
| `telesphorepo-server restore FOLDER [--data-dir DIR]` | Restores a backup folder into `DIR`, else `TR_DATA_DIR`, else `./data`, which must be empty or missing (see **Backups**). It prints what it wrote, and how the migrations of the backup fit this binary: the same, older (applied at the next start) or newer (this binary refuses to start; it names the version to run) |
| `telesphorepo-server version` / `help [cmd]` | |

**Demo data.** The first run of `telesphorepo-server dev` (without `--empty`) fills the dev instance
for work on browsing and search. Paths, file contents, users and roles are the same on every fresh
run. The times spread over the two years before the day of the run, and the Maven metadata
timestamps follow them.

| Repo | Access | Content |
|---|---|---|
| `maven` | anonymous read | About 150,000 files. 400 groupIds of 2 to 4 segments from word lists (e.g. `io.northwind.billing.cli`), 3 to 60 artifacts each (a few groups are large), 1 to 40 versions each (most have few). Each version has a valid pom, jar and sources jar (minimal zips), sometimes a javadoc jar and Gradle `.module`, and real `.md5`/`.sha1`/`.sha256` files. Each artifact has a `maven-metadata.xml` with `latest` and `release`. Some artifacts end in a `-SNAPSHOT` with timestamped files and version metadata. 30 Gradle plugins have their `<id>.gradle.plugin` marker artifacts |
| `internal` | private | About 2,000 files of the same shape. `lena` reads it; `jonas` writes it |
| `debian` | anonymous read | About 300 binary packages from about 100 sources, 1 to 5 versions each, as real `.deb` files for `amd64` and `arm64` or `all`. They go through the format's upload pipeline into `stable/main`, so the indexes and signed `Release` files are valid |
| `npm` | anonymous read | About 40 packages (a third under `@acme`), 1 to 6 versions each, some ending in a `-beta.1`, as real tarballs through the format's upload step |
| `nuget` | anonymous read | About 120 `.nupkg` files: 20 `Contoso.*` packages, some with `.Abstractions`, `.Testing` and `.AspNetCore` packages that depend on them, 1 to 6 versions each (some prereleases), pushed through the format's push |
| `pypi` | anonymous read | 40 projects (`<company>-<word>`), 1 to 6 versions each, each version an sdist and a pure-Python wheel built by the module, stored through the upload pipeline with their `.metadata` |
| `raw` | anonymous read | 20,000 files: `docs/<product>/<version>/` sites, `releases/<yyyy>/<mm>/<product>-<version>/` archives (valid `.tar.gz`/`.zip`) with `SHA256SUMS`, and `build-logs/<project>/<branch>/<build>/` logs and test reports |

The demo users are `priya` (administrator), `jonas` (download and publish on `maven` and
`internal`), `mei` (download and publish on `maven` and `raw`), `tomas` (download and publish
on `debian` and `raw`) and `lena` (download on `internal`). Their random passwords are in
`<data>/demo-users.txt`. Each file's uploader is an administrator or a publisher of its repo. The files go through the byte
pipeline with source `demo` in commits of 1,000 files, so the triggers keep the repo counters, the blob
references and the search index; on the filesystem store the blobs are written without fsync. The
command prints its progress to stderr every 3 seconds, then runs `ANALYZE`. While it runs,
`<data>/demo-data.pending` exists; a dev start that finds that file deletes the data dir and
starts again.

Two hidden commands support testing and do not appear in the overview. `dbcheck` applies the
migrations of every component to a temporary database and runs the plan guard. `seed N` fills the database in
`TR_DATA_DIR`. For the format of every module with a seed (`Extender.Seed`), it creates the repos
`seed-<format>-00` to `seed-<format>-09` (anonymous read, default config) and splits N rows
evenly over all of them in transactions of 10,000. Each format inserts its own rows: maven
and raw insert asset rows, debian inserts one package and its asset row per unit, pypi one sdist with its
project and file rows per unit. Then it runs
`ANALYZE`. It is safe to run again.

## Tooling (`go tool task`, registered in `go.mod`)

| Command | Does |
|---|---|
| `go tool task dev` | Checks Go, bun/npm and Node (version in `.node-version`). Builds the CLI archive of this computer and the edge Worker folder once (see **Install scripts and CLI downloads**). Runs the backend on :8080 and rebuilds it on Go or `internal/**/migrations/*.sql` changes. Runs Vite with HMR on **:5173**, proxying every path except the UI's own files to the backend |
| `go tool task build [image]` | Builds the UI, the six CLI archives (`internal/clidist/dist`) and the edge Worker folder (`internal/edgedist/dist`, as `go tool task edge`; without bun it writes none and says so), then `bin/telesphorepo-server` with them embedded (`CGO_ENABLED=0`, version from git). `image` also builds the container image `telesphorepo:local` |
| `go tool task cli [version]` | Builds the six CLI archives of `./cmd/telesphorepo` and `SHA256SUMS` into `internal/clidist/dist` (version from git by default). A given release version needs a `changes.toml` with no `dev` entry. Without `cmd/telesphorepo` it writes none and says so |
| `go tool task edge [version]` | Runs `bun install --frozen-lockfile` in `edge/`, bundles the Worker (`wrangler deploy src/index.ts --dry-run --outdir` with `--define TELESPHOREPO_VERSION:"<version>"`; version from git by default) and packs the folder that `GET /api/edge/worker.zip` hands out into `internal/edgedist/dist`: `telesphorepo-edge.zip` with `dist/`, `package.json` without the scripts `build` and `typecheck` (its dependencies unchanged, so `bun.lock` matches it), `bun.lock`, `README.md` and `scripts/provision.ts`, and `VERSION`. It needs bun. The image build runs it with Node and the bun of the web stage. `dev` runs it with the version `dev` (the dev backend's) and goes on without it when it fails |
| `go tool task preview` | Builds, then runs `bin/telesphorepo-server dev` on :8080 |
| `go tool task test [go test args]` | Runs the core zone: `go test -tags e2e ./e2e/... -count=1 -v`, 60 min timeout |
| `go tool task test import [go test args]` | Runs the import zone: `-tags import`, 45 min timeout. It starts a Nexus container |
| `go tool task test scale [go test args]` | Runs `TestScale` (`-tags scale`, 60 min timeout) |
| `go tool task test clients [go test args]` | Runs the clients zone: `-tags clients`, 120 min timeout. It builds the CLI of this repo on the host and runs it in containers with the real tools |
| `go tool task test [zone] --shard i/n [go test args]` | Runs every n-th top-level test of the zone, by name, from the i-th (`go test -list`, then one `-run`); `TestMain` skips its setup while listing | Runs core, import, scale and clients one after the other. It runs every zone, lists the failed zones and exits non-zero if any failed |
| `go tool task release [version]` | Builds the UI and the six CLI archives that the server embeds once. Then it cross-compiles each program whose package exists: the CLI (`./cmd/telesphorepo`, with its own `LICENSE`; the install scripts make its second name `docker-credential-telesphorepo`) and the server (`./cmd/telesphorepo-server`) for linux, darwin and windows on amd64 and arm64 (`CGO_ENABLED=0`, `-trimpath`, `-X main.version`). Each binary goes into `bin/release/<program>_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows) with `LICENSE` and `README.md`. With `edge/`, it bundles the edge Worker folder once (as `go tool task edge`; it needs bun), embeds it in the server, and packs the same folder into `telesphorepo-edge_<version>.tar.gz`: `dist/`, `package.json`, `bun.lock`, `README.md` and `scripts/provision.ts`. The command writes `SHA256SUMS` of every archive. The version defaults to `git describe`. A version given as the argument must be a release version, and `changes.toml` must have no `dev` entry (see **Change log**) |
| `go tool task tag <version>` | Makes a release. It needs a clean work tree and a new tag. It writes `v<version>` into each `dev` entry of `changes.toml`, commits the file as `Release v<version>`, and creates the annotated tag `v<version>`. It pushes nothing. The tagged commit holds the stamped change log, so `release.yml` and every image build embed the same entries |
| `go tool task gen` | Runs `go tool sqlc generate` for every `internal/**/sqlc.yaml` |
| `go tool task migration <folder> <name>` | Creates the next migration `internal/<folder>/migrations/NNNN_<name>.sql` of a component (`db`, or `core`, for the kernel; the folder of the module otherwise, e.g. `move` for `storage`) with a header comment. The name holds only `a-z`, `0-9` and `_` |
| `go tool task doctor` | Shows the detected toolchain and container engine |

CI runs on GitHub Actions with the built-in `GITHUB_TOKEN` and reads no repository secrets. The Linux jobs run on Blacksmith runners (x64; 8 vCPU for the e2e zones, 4 for the rest) and use its caches: the Go build cache (so `setup-go` caches nothing), the images that a job pulls, a sticky disk for bun's package cache in `build`, and `useblacksmith/setup-docker-builder` for every Docker build (the e2e harness's too: it passes `--load` to Docker), whose BuildKit state stays on a disk under the key `telesphorepo-image`. The `macos` job runs on GitHub's `macos-latest`:

| Workflow | Runs | Does |
|---|---|---|
| `ci.yml` | Push to `main`, pull requests | `build` job: `go tool task build`, `go vet` for the `e2e`, `import`, `scale`, `clients` and `macos` tags, `go vet` and `go build` of `./cmd/telesphorepo` for `GOOS=darwin` and `GOOS=windows`, `bunx tsc --noEmit` and `bun run build` in `edge/`, `telesphorepo-server dbcheck`. The e2e jobs run on pull requests only, and only when the `changes` job finds a file outside `web/src/` that is not Markdown (the e2e tests drive the API and real clients, never the UI): `e2e-core` on four machines at once, each `go tool task test --shard i/4`; `e2e-clients`, `go tool task test clients`; `e2e-github-actions` (pull requests of this repository; `id-token: write`), `TestCLIGitHubActions` with the job's real identity token. Each uploads its log on failure. `snapshot-image` job (push to `main` only, after `build`): multi-arch image with tags `main` and `sha-<short>`, version `main-<short sha>`. A newer push cancels the running pull request jobs |
| `e2e-extended.yml` | Manual only (zone `import`, `scale` or `all`); the zones run locally before a push (AGENTS.md) | `go tool task test <zone>` |
| `release.yml` | Push of a `v*` tag | `binaries` job: `go tool task release`, then a GitHub Release with the archives and `SHA256SUMS`. `image` job: multi-arch image (`linux/amd64`, `linux/arm64`) pushed to `ghcr.io/telesphorepo/telesphorepo` with tags `X.Y.Z`, `X.Y` and `latest` (no `latest` for pre-releases). `macos` job on `macos-latest`: `go tool task build`, then the macOS zone (`go test -tags macos -run TestMacOS ./e2e/` with `E2E_SERVER_BIN`) |

The Dockerfile builds the UI, the six CLI archives (`go tool task cli`) and the Go binary on the build platform and cross-compiles with
`TARGETOS` and `TARGETARCH`. It takes `VERSION` as a build argument. A plain `docker build` or
`podman build` builds for the host.

The web lockfile is `web/bun.lock`. bun is preferred and npm is the fallback. The Docker image is
distroless static, runs as uid 65532, and has `/data` as a volume.

## Testing

The suite is E2E only, with no unit tests and no mocks. Every test calls `t.Parallel()` and
starts its own server, so the tests of a zone run at the same time (`go test` runs as many as the
host has CPUs), and `ForEachStorage` runs its two stores at the same time. `TestNexus` runs its
stores one after the other (`ForEachStorageInTurn`), because both change the one Nexus. The
harness starts each container that publishes a port with `runContainer`: rootless Podman can
give two containers that start at the same moment one host port, and `runContainer` starts the
second again. The containers that share the Maven cache volume lock it with files
(`MAVEN_OPTS` with `aether.syncContext.named.factory=file-lock`). `e2e/` builds the real image (skip the
build with `E2E_SKIP_BUILD=1`, or override the image with `E2E_IMAGE`). It runs real `mvn`,
`gradle` and `apt` clients in containers against the server. A test whose subject is the store
runs on `fs` and on SeaweedFS S3 (`ForEachStorage`, `E2E_SEAWEEDFS_IMAGE`): the byte path of each
format (`TestMaven`, `TestRaw`, `TestDebian`, `TestDockerRegistry`, `TestNPM`), the presigned redirects and
their cache headers (`TestCacheControl`, `TestBulkRepos`, `TestBulkFiles`), deletes (`TestDelete`),
the blob GC (`TestBlobDedupeAndGC`, `TestDockerGC`), the uploads of the CLI (`TestCLIUpload`) and
the Nexus import. Every other test runs once, on `fs` (`OnLocalDisk`). The engine
comes from `internal/containerengine`; `TR_ENGINE=docker|podman` forces one. The images of the
tools that people bring float (`e2e/images_test.go`): Maven (`maven:3`), Gradle, Debian
(`stable`), curl, Helm, Docker (`docker:dind`), crane, skopeo, the reference registry, Keycloak,
Nexus, the install images (`buildpack-deps:curl`, Alpine), Node (`node:lts`, with pnpm and Yarn 4
through corepack), Bun (`oven/bun`) and the newest ORAS release. The npm tests: `TestNPM` (npm
publish of a plain and a scoped package, installs with npm, pnpm and Yarn, both stores),
`TestNPMRegistry` (a registry.npmjs.org proxy and a group, the pnpm binary that corepack's pnpm
fetches through the proxy, the name rule, dist-tags, deprecate,
unpublish with a device session and refused with a CI session minted from a Keycloak service
account), `TestCLINPM` (clients zone: setup and publish with each package manager) and the npm part
of `TestNexus`. Each file registers its images with `floatingImage`, and each run pulls the images of
its zone again before the tests and prints the version of each. `TestInstall` runs the newest PowerShell release from GitHub on Debian, because Microsoft's PowerShell images no longer follow the releases. `TestCLIMaven` takes the newest
Maven 4 from Maven Central, and `TestCLIJenkins` builds its Jenkins with `--pull` (Jenkins `lts`)
and fetches the newest plugins once a day. The infrastructure of the tests stays pinned:
SeaweedFS, Caddy, the OCI conformance suite and the curl of the volume tool. `TestOIDC` runs a
Keycloak container (realm `e2e/testdata/keycloak/realm-e2e.json`) and
drives the browser flow with a cookie-jar HTTP client: a sign-in with the claim map sets the
permissions (409 on a change of them), a person outside the map signs in without permissions, a
map change replaces them at the next sign-in, and without a claim a sign-in changes nothing; a map
without a claim or with an unknown or repository permission is 400. `TestRaw` uses a curl container.
`TestAPIFormats` covers `/api/formats`, `/status` and `/usage`; `descriptor_test.go` covers the format descriptors (each one well formed) and the CI file route with branches and tags (`TestCIWorkflowRoute`), and the delete plan (`TestDeletePlan`). `TestQueryPlans` runs `dbcheck`
inside a server container. `migrations_test.go` covers `TestMigrations`, a real upgrade with three builds of the server (`BuildServer`, `BuildServerWith` with an overlay): the tree writes a private raw repo, a user with `repo.download` on it and a file; a build with one more core migration (`0002_e2e_probe.sql`, a new table filled from `repos` and `users`) starts on the same data volume, logs `applied migration` for that file only, and the user reads the file and its grant back; the exported database holds both core rows of `schema_migrations` and the probe rows of the older data; a build whose `0001_init.sql` has another content refuses to start with the sentence that names `core` and the file; the tree refuses the database of the newer build ("core migration 0002"); then the newer build starts on it again and applies nothing. `files_test.go` covers `TestTree`, `TestDelete` (who may delete: `repo.delete` with a publish session or a UI session, file, prefix and Bearer deletes, blobs kept for the blob GC, repo delete and name reuse), `TestManualUploadMaven` (checksums, metadata order, `mvn` resolves the uploaded and the generated pom), `TestManualUploadRaw`, `TestRepoCounters` (`file_count`, `total_size` and `last_published_at` through uploads, a replace, a file delete and a folder delete) and `TestDebianDeleteAndUpload` (the plan of a folder of `.deb` files: a broken one has problems, another file is ignored, the open folder gives the distribution, a bad distribution is a problem of its field; apt sees the package, then not; 4 by-hash files remain). `audit_test.go` covers `TestAudit`: events, filters, paging, no secret values, `activity.read` only, and no IP outside the audit log. `network_test.go` covers `TestClientIP` (with the default an `X-Forwarded-For` from the harness peer is ignored and the peer becomes a proxy candidate; trusting the candidate makes the header count and removes the candidate; `private`: a chain through a private hop, `CF-Connecting-IP` from a non-Cloudflare peer; an untrusted peer's header, an empty list, invalid entries, reset to the default). `TestIPRules` and `TestPresetIPLists` set `trusted_proxies` to `private` first, so `X-Forwarded-For` sets the client address, `TestIPRules` (validation; allowed and refused reads of the format route, tree, assets, usage and status; the repo record stays readable; search drops the repo; refused device and manual mints, PUT and admin delete, each audited with its IP and scope; empty lists lift the rule) and `TestPresetIPLists` (a second Telesphorepo server is the stub: startup fetch, a preset in read rules, a Cloudflare hop in the chain, a failed refresh that keeps the last good list, recovery, and stored lists after a restart without the stub) and `TestCustomIPLists` (create, edit, a rename that keeps the rules and the trusted proxies (by list id), use in rules and in the trusted proxies, delete while in use gives 409, an unknown list id gives 400) and `TestTrustedProxyEnvList` (`TR_TRUSTED_PROXIES` with `list:cloudflare` shows as `env_lists`) and `TestIPRangeParse` (the paste parser: one per line, comma, space, tab and `|` separated, CSV with a header row, tables, comments, labels, quotes, IPv6 and IPv4-mapped IPv6, an unmasked CIDR, duplicates, invalid lines with their numbers, the 1 MiB limit, `network.manage` only). `signing_keys_test.go` covers create, import of plain and encrypted keys, delete while in use gives 409, a per-repo key (the repo field `signing_key`; an unknown key, a key in the config and a key of a raw repo give 400) that `apt` installs from, a key change that signs again, and keys and bindings after a restart. `redirects_test.go` covers `TestRepoRedirects`: a Maven, an npm and a Docker repository renamed, then `mvn deploy`, `npm publish` and `docker push` (Docker in Docker) through the old name with a publish token minted through it, each landing in the renamed repository; the upload event records the old name with `redirect_to`; the redirect lists a last use; create and rename to a redirect's name or a repo's name give 409; a rename back takes the old name again; the catalog lists the images of before the rename under the new name; after Remove the redirect the old name answers 404 and a new repository takes it. `groups_test.go` covers `TestGroups`: a release and a snapshot member resolve through the group with `mvn` and Gradle, the merged `maven-metadata.xml` and its `.sha1`, the member order, a public group with a private member (anonymous and a reader), 405 for a PUT and 409 for a publish session, 400 for a member of another format, a nested group, an unknown name and no member, and a member delete. `groups_files_test.go` covers `TestGroupFiles`: a Maven group of a repository, a proxy of a second Telesphorepo server and a private repository, with a path in both the repository and the proxy; the group tree equals the union of the member trees at three folders and pages through it with limits 1, 3 and 200 without a gap or a duplicate, folders merge, the shared path comes from the repository with its `repo` and checksum (tree and file details), anonymous readers do not see the private member, a bad cursor is 400, and a delete (405), a publish session (409) and a PUT (405) are refused. `cache_control_test.go` covers
`TestCacheControl`: HEAD, a 304 and a GET of a private Maven repo, of a public group with a
private member (also for the public member's file) carry `Cache-Control: private`; a public repo
and a group of public members carry none (an S3 redirect carries `no-store`); after a read IP rule
that allows every address, the public repo and the group of it carry `private`. `proxy_test.go` covers `TestProxy` with a second Telesphorepo server as the remote: `mvn` through the proxy and no remote request on the second resolve, the `metadata_ttl` recheck with a 304 and a changed file, a remembered 404, 405 for a PUT, a group where the hosted member wins over the proxy, remote credentials (with them, 502 without them), a Caddy container that redirects to another host and refuses a request that carries credentials, `apt-get` through a Debian proxy with the remote's key, and a stopped remote (cached file served, 502, `last_error`, and a stale `maven-metadata.xml` that serves twice with one remote attempt in the log). `TestProxyStream` (both stores) checks that the first GET of an uncached 24 MiB file streams (the remote's length, no `ETag` yet) with the remote's bytes, that a later GET serves the stored file, and that three concurrent GETs of another uncached file get its bytes with one remote GET. `TestNoBrowserPasswordPrompt` (in `auth_test.go`) checks that a 401 under `/api` or with `X-Telesphorepo-UI` has no `WWW-Authenticate`. `folder_upload_test.go` covers `TestFolderUploadMaven` (a Maven-layout tree with checksum, signature and stray files; a flat folder with two poms; a bad tree that gives 400; plans store nothing; checksums and metadata) and `TestUploadHeadsAndChunks` (a plan with held heads and no parts; 409 for unknown heads, the heads of another session and another size; an upload whose part `uploads` names a resumable upload: 404 unknown, 400 another size, 201 with a file of the request). `TestStorageMove*` run each server on a named data volume: fs to
SeaweedFS and back with uploads during the move, a forced failure, a restart mid-move
(`Server.Restart`) and a cancel. The restart and the cancel hold the move on a SeaweedFS of their own
(`StartOwnSeaweedFS`): once the move has copied a blob, the test pauses that container, so the
restart (the target runs again before the server starts) and the cancel come while the move copies. `backup_test.go` covers `TestBackupRestore`: on fs, a server with Maven, Debian and raw repos backs up through the API while raw uploads run; the folder holds the database (its SHA-256 in `backup.json`), `master.key` (0600) and `backup.json`, and the blobs folder every blob; `telesphorepo-server restore` of the image restores it into a new volume and refuses a volume that is not empty; a server on that volume reads every file back byte for byte (the uploads during the backup that it has too), `mvn` resolves and `apt` installs with the same signing key; with `backup.keep=2` two more backups remove the first one and the blob only it used (made an hour old first), while a fresh unused file of the blobs folder stays; an incomplete folder is listed, refused by restore and removed by the next run. On SeaweedFS the backup has no blobs folder, `backup.json` names the bucket, and the restored server reads the files from the bucket. `blobs_test.go` covers `TestBlobDedupeAndGC` (the same bytes in two raw repos
and a Maven repo are one stored blob, each path keeps its own content type, also in an S3 redirect,
whose disposition names the file of the path, a name with a space too;
the blob stays while a file uses it and the blob GC removes it after `TR_BLOBS_GC_GRACE=1s`; an upload of the
same bytes stores them again), `TestUploadSessions` (chunks, a wrong offset, a restart between chunks, a
wrong digest, another publish session, finish into a raw and a Maven repo, an immutable release path,
cancel, a Debian repo that refuses a path and finishes a `.deb` through its upload route, a NuGet push that finishes a `.nupkg` with the session as the API key), `TestLargeUploadAndRepoGC`
(on both stores: an 80 MiB resumable upload in three chunks, one sent again at a wrong offset, whose checksum headers
are the hashes of the whole file; on SeaweedFS a multipart object of 5 parts; the file reads back byte for byte; after a
repo delete the blob GC removes every object of the repo) and `TestMavenMetadataAfterDelete`. `bulk_test.go` checks the `mirror` operation of a mapped repo and
the 409 of resume and clear.

`pypi_test.go` covers PyPI with the official `python` image (twine, build and poetry installed
with pip) and uv's image: `TestPyPI` (both stores: `python -m build` and twine upload, the JSON
page and the `.metadata` bytes, `pip install --require-hashes`),
`TestPyPIProtocol` (negotiation, 406, 304, the 301s, the project list, the upload refusals, the
version browser, deletes), `TestPyPIClients` (`uv publish`, `poetry publish`, `uv pip compile`
reads the `.metadata` and not the wheel, uv and poetry install) and `TestPyPIProxyGroup` (a
pypi.org proxy for pip, uv and poetry, and a group where a hosted `iniconfig` owns the name, though
pypi.org has higher versions). `cli_pypi_test.go` (`TestCLIPyPI`, clients zone) publishes with uv, Poetry and
`python -m build` and a FILE upload; `nexus_pypi_test.go` imports a Nexus pypi repo (import zone).

`docker_test.go` covers the registry. `TestDockerChallengeScheme`: with
no `base_url`, the realm is http over plain HTTP; `X-Forwarded-Proto` or `Forwarded` of https
from an untrusted peer makes it https, http does not; through a trusted proxy (`private`) the
first forwarded proto gives the scheme, and no forwarded proto leaves http. `TestDockerRegistry`: docker repo
names that are not one OCI segment are 400; `GET /v2/` answers the challenge and the API version;
a token grants the subset (an anonymous bearer reads the public repo and gets 401 with
`error="insufficient_scope"` and the scope on the private one); a bad password is 401
`UNAUTHORIZED`; a
one-segment name is `NAME_UNKNOWN`. Then the Docker CLI in Docker in Docker (`docker:dind`,
privileged, `StartDind`, `DockerScratchImage`: images FROM scratch, no registry pull): a push of
three layers (one of 20 MiB) with a device publish session, `rmi`, pull and a compare of the file
hashes; a password and a read token pull but cannot push ("publishing needs a publish session");
an anonymous pull of a public repo; a tag move (the old digest still pulls); a multi-platform
index (`docker build` of two platforms and `docker push`; two platforms in the index, a pull of
one); a read IP rule that refuses
the pull with "not allowed to read" and records `network.denied`. `TestDockerConformance` runs the
OCI conformance suite (`conformance:v1.1.1`: pull, push, content discovery, content management).
`TestDockerClients`: `crane copy` mounts a layer from a public repo (201), a mount from a repo
that the token does not read starts an upload (202); `skopeo copy` of an OCI layout; ORAS push,
attach, discover (also with `--artifact-type`) and pull, the referrers filter header, an empty
referrers index, `OCI-Subject` on a manifest PUT;
the catalog of a reader, of a publish session bearer and of anonymous, its `Link` and a bad `n`.
`TestDockerGC`: a used layer cannot be deleted (409); after a delete by digest the tags go, and
after `docker.link_grace=1s` and `TR_BLOBS_GC_GRACE=1s` the blobs of the deleted image leave the
store while the shared layer and the other image stay; a folder delete (`prefixes`) of an image of
522 files (499 links, a two-platform index with its children, 20 tags), where the first batch holds
the links and a child of the index, deletes every file and the image. `docker_proxy_test.go`: `TestDockerProxy`
proxies two remotes, a second server with a private docker repo (its Bearer challenge runs through
`RemoteAuth`; the remote's log shows the token request) and the reference registry (`registry:latest`) with htpasswd Basic
auth. For each: the check passes, and wrong credentials fail it with 502; Docker in Docker pulls an
image of real tar layers (one of 30 MiB) through the proxy and the layer assets exist; a second
pull sends no blob GET to the remote; after `metadata_ttl` (1 s) a HEAD of the tag finds the moved
tag and the proxy serves the new digest, the old manifest leaves the cache and, after
`docker.link_grace=1s`, its config and layers too (the old digest still serves from the remote); a missing tag is
`MANIFEST_UNKNOWN`; on the second server, a proxy of a repo that the user may not read answers a
tag with `MANIFEST_UNKNOWN` (the second GET sends nothing to the remote) and the tags list with
`NAME_UNKNOWN`, and the proxy keeps no `last_error`; a mount from the proxy of a layer it has not cached is 202 and sends nothing
to the remote; a push is 405 "A proxy is read-only."; a group of a hosted repo and the proxy
serves the hosted tag on a shared tag and the proxy's tag otherwise, merges the tags list, and a
push is 405 with the group's text; the proxy's tags list is the remote's; a stopped remote serves
a cached tag and answers 502 `UNKNOWN` for an uncached one. `TestDockerGroupBearer`
(`docker_group_test.go`): a private group of two private hosted repos, where the user holds
`repo.download` on the group and on the second member only; the Docker CLI after `docker login`,
crane and a bearer token of the token endpoint pull through the group and get the second
member's image; the first member's tags are 404 and absent from the tags list, while the admin
gets them; the group's token on a member is 401 `insufficient_scope`; manifests and blobs carry
`Cache-Control: private`; an anonymous bearer reads a public group of a public repo.
`TestDockerUploadHost`
(`docker_upload_test.go`): Caddy with `request_body max_size 1MB` in front of the main host
(`base_url`); `docker.direct_uploads` without an address is 400; with the address set and the toggle
off, `/docker/hosts` names the main host only and a crane push of a 5 MB layer through the proxy is
413; with the toggle on, POST answers an absolute `Location` on the upload host (the server's own
alias) with `_state`, the token writes to its own upload only (no token, a changed token, the token
of another upload and the token on another path are 401), and Docker (the containerd image store and
the `vfs` graph driver), crane and skopeo push the 5 MB layer through the proxy hostname and pull it
back; `/docker/hosts` names the upload host, a POST on the upload host gets a relative `Location`
and the challenge there names its own realm, and ORAS (5 MB) and Helm 4 push to the upload host by
name and pull through the proxy hostname. `TestDockerVersions` (`versions_test.go`): the versions (tags, newest first, keyset pages),
the kinds image, helm-chart, the builds of an image and of an index (platform classifiers), the pinned and latest artifact snippets, the snippets of
a tag file and of a chart, and `GET /api/docker/{name}/manifest`.
`TestCIWorkflowRoute` checks the Docker CI files (build,
the install step, the Docker config that names the credential helper, and the push in that order,
with no `docker login`) and the usage snippets. It also checks the install step
and `telesphorepo publish` of the GitHub Actions, Jenkinsfile and Jenkins freestyle files, and
that a publish session of the repo reads its CI file. The shared OCI helpers
(`oci_test.go`, tags `e2e` and `import`): `tarImage` (an image of tar layers that Docker unpacks),
`ociLayout` and `ociIndexLayout` (OCI image layouts for skopeo) and `registryClient` (a push over
the registry API to any registry). `protocols_test.go` covers
`TestServerProtocols` (fs): curl gets HTTP/2 with prior knowledge and HTTP/1.1, a server with
`TR_TLS_CERT_FILE` and `TR_TLS_KEY_FILE` (`ServerOpts.TLS`) answers HTTPS with HTTP/2, and a Maven
GET and HEAD carry `X-Checksum-SHA1`, `-MD5` and `-SHA256` of the file. The
harness has `ClientOpts.Privileged` and `NoShell` (the image's own entrypoint),
`ContainerFile.HostPath`, `ServerOpts.Aliases`, `ServerOpts.TLS` and `Dind.CopyFile`.
`build_test.go` (tags `e2e` and `clients`) builds programs of this repo on the host for Linux on
the CPU of the test (`CGO_ENABLED=0`): `BuildCLI` (a version, a change log and extra Go files of
package main), `BuildServer` (a version, a change log, and the CLI archive of the test platform
or no CLI archives) and `CLIArchive`. They read their changes through `go build -overlay`, so the
tree on disk stays as it is. `ServerBinary` puts such a server in place of the image's.

**Zones.** A build tag picks the zone, so a run starts only the containers it needs. The harness
and the client helpers (`harness_test.go`, `main_test.go`, `clients_test.go`) carry every tag.
Each test removes its containers when it ends. Every container and the run network carry the
label `telesphorepo.e2e`, and each run first removes what a killed run left: labelled containers
and networks, and the `telesphorepo-data-*` and `telesphorepo-scale-*` volumes.
| Zone | Tag | Command | Contents |
|---|---|---|---|
| core | `e2e` | `go tool task test` | Formats (the Docker registry with Docker in Docker too), auth, API, settings, storage move, query plans, the listener protocols |
| import | `import` | `go tool task test import` | `TestNexus`, with the import of a Nexus docker hosted repo (`nexus_docker_test.go`: the Docker Bearer Token Realm on and `forceBasicAuth` off, so every download answers a Bearer challenge; skopeo pushes an image and a multi-platform index; the sync imports both tags and ignores the blobs; crane pulls both by tag and by digest; a second sync imports nothing) and of a Nexus nuget hosted repo (`nexus_nuget_test.go`: a package of `dotnet pack` uploaded to Nexus, imported, then restored and run with dotnet) |
| scale | `scale` | `go tool task test scale` | `TestScale` |
| clients | `clients` | `go tool task test clients` | The Telesphorepo CLI with the real tools: `TestCLISetup`, `TestCLILogin`, `TestCLIDocker`, `TestCLIMaven`, `TestCLIGradle`, `TestCLIJenkins`, `TestCLIGitHubActions`, `TestCLIUpload`, `TestCLINuGet`, `TestCLICompat` and `TestInstall` (`e2e/cli_*_test.go`). It needs Go on the host to build the CLI and the internet for Maven Central, Gradle, the Maven 4 download and the Jenkins plugins |
| all | all tags | `go tool task test all` | Every zone |

**Admin and user calls.** The API changes state only through a UI session, so the suite signs in
the way the web UI does. `s.Admin()` is the bootstrap admin's UI session: on first use the
harness enrolls TOTP factors through the setup-only session and signs in with the password and a
code. `s.SignIn(t, user, pass)` gives any user's UI session, and `s.AdminBasic()` is the admin
password for format reads. `s.CreateUser(t, name, pass, perms...)` creates a person with server
permissions (`AllServerPerms` for an administrator), `s.SetPerms(t, repo, user, perms...)` sets a
repository permission set, and `s.SetServerPerms` sets server permissions. A person who needs the
second factor gets a UI session through `EnrollTOTP` and `LoginTOTP`. `TestAPISessionRule` checks where each credential works, including a UI session that downloads from a private Maven repo and cannot PUT there.

**Permission tests.** `access_test.go` covers `TestEffectiveAccess`, `TestRepoSettingsPermission`
(`repo.settings` on one repo edits it and gets 403 on another), `TestAccessPermission` (`repo.access` gives
and removes only held permissions; 400 for a set that breaks the dependency rules),
`TestLastPeople` (409 when the last enabled holder of `people.manage` would lose it; `people.manage` acts only
on people it covers) and `TestSecondFactorRule` (who signs in with the password alone, the
setup-only session, the second step, and a grant that ends a password-only session).
`edge_test.go` covers `TestEdgeWorkerDownload`: on SeaweedFS with a home region and one region,
`GET /api/edge/worker.zip` holds `dist/index.js`, `package.json` (wrangler at an exact version,
`provision` and `deploy`), `bun.lock`, `README.md`, `scripts/provision.ts` and a
`wrangler.jsonc` with `"main": "dist/index.js"`, `"no_bundle": true`, the routes and the buckets
of the settings; the `wrangler.jsonc` of a checkout has the same routes with `src/index.ts` and
the define; a server on the local disk answers 409 `edge-not-configured`. No Cloudflare account is
needed.
`permissions_test.go` covers `TestServerPermissions` (one subtest per server permission: the
holder reaches its routes, anyone else gets 403), `TestPipelinesPermission` (the CI pipeline
scope) and `TestPeoplePicker` (`GET /api/people`). `registries_test.go` covers
`TestPermissionRegistry` (the shape of `GET /api/permissions`, 400 for an unknown ID or an ID of
the other scope, grants read back as IDs),
`TestAuditFilters` (`actor`, `since` and `until` alone and with `action` and `repo`, keyset order, a bad time is 400, `GET /api/audit/people`), `TestDownloadStats` (what counts: 200, 206 with its range bytes; not HEAD, 304, 404 or 401; the totals, the days and the top repos of `GET /api/stats` after its flush; a deleted repo leaves; `days` 400; `activity.read` and a UI session only), `TestAuditActions` (`GET /api/audit/actions` and the events that services record: read tokens, CI
pipelines, the first-start signing key, the bind of a new Debian repo's key) and `TestSSOStartupGuard` (SSO settings
and `signin.password_login=publishers` saved with one master key; after a restart with another the
server starts with SSO off and password sign-in for all). `settings_test.go` has
`TestSettingsStorageBogusS3Refused` (the test of an S3 store that does not answer is 400 and a
switch to it saves nothing; an empty endpoint needs a region; an endpoint with a path is refused),
`TestSettingsSurviveRestart` (a size, a duration and a secret setting come back from the database
after `Server.Restart`, the secret shows only as set, and the stored size limit applies) and
`TestBaseURLFromFirstSignIn` (no `base_url`: sign-ins at `localhost`, `127.0.0.1` and `[::1]` and
of a person without `settings.general` leave it unset; the admin's sign-in with `Host:
repo.example.com` and `X-Forwarded-Proto: https` behind a trusted proxy saves
`https://repo.example.com` with one settings.update event of the admin with `via: "first
sign-in"`; a later sign-in at another host changes nothing).

**Publishing tests.** Every publish in the suite uses a publish session. The harness helper
`PublishToken(t, s, user, repo)` enrolls a device the real way (the test computes the TOTP
codes), approves it and mints a session. The harness enrolls 3 TOTP factors per user, so tests
never wait 30 s for a new step. `publish_test.go` covers: `TestPublishGate` (a password or read
token is refused; a session for another repo; lost permission; closed and expired sessions;
replayed nonce; bad signature; revoked and expired devices; manual sessions),
`TestSecondFactorLogin` (including the setup-only session), `TestDeviceEnrollment` (the device
has its name),
and `TestDeviceEnrollmentExpiry` (ages the row with `editDB` on a data volume). The CI token
exchange runs with the real CI systems in the clients zone (`TestCLIJenkins`,
`TestCLIGitHubActions`). The Keycloak fixture of single sign-on is in `keycloak_test.go`.
`oidc_device_test.go` covers `TestOIDCDeviceApproval` (a person who signs in with single sign-on approves a device after a new
Keycloak sign-in, then publishes with it; the authorization request carries `prompt=login` and
`max_age=0`; a missing grant, a grant for another code and a used grant get 401). The test sets
`oidc.approval_max_age` to 20 s and first signs in once more in a browser of its own; at the end it
waits until that sign-in is 25 s old, removes `prompt` and `max_age` from that browser's
authorization request, as a provider that ignores them, and checks that the callback returns
`error=stale_sign_in` and that the code stays pending.

`cli_server_test.go` covers the server side of the CLI with no external tool:
`TestSetupCodes` (a setup code needs the fresh second factor and `repo.publish`; it enrolls a new
key or takes the proof of the person's device and answers the repo with its format and kind; a
used, unknown or expired code answers the problem `setup-code-used` with its `open_url` link; the
device of another person gets 409 and leaves the code), `TestDeviceSelfRevoke` (`DELETE
/api/devices/self` with the device signature, a replayed nonce, the end of the sessions and the
audit event `by: device`), `TestExchangeSession` (the token exchange nonce needs a recent device
signature and is kept on request; one session per repo per proof, the same session again; a
format read takes the `trx.` proof as the Basic password or the Bearer value, a write does not)
and `TestCLIDownloads` (the six archives, `SHA256SUMS`, the URL and sums in both install scripts,
Windows line ends in `install.ps1`).

**Clients zone.** Each test builds the CLI of this repo (`BuildCLI`), copies it into client
containers at `/usr/local/bin/telesphorepo` with `docker-credential-telesphorepo` next to it, and
runs on `fs`; `TestCLIUpload` runs on both stores. The harness (`cli_harness_test.go`) passes setup links and CI tokens in the
env and, after each run, lists each file of the container (and of its volumes other than the
build caches) that holds `trs_` or `trsu_` with 40 or more characters or the CI token of the test;
a match fails the test. A terminal comes from `script -qec`, which also types the answers.

| Test | Proves |
|---|---|
| `TestCLISetup` | In a git work tree that ignores the file: the setup line writes `telesphorepo.toml` and warns with the `.gitignore` rule; the line again prints `This setup line is already used`; on a terminal the answer `2` writes the GitHub Actions file; a Docker repo gets no project file, `y` adds the `credHelpers` entry and `n` keeps the Docker config file |
| `TestCLILogin` | A teammate's `publish` with no device prints the code, which the test approves, and publishes; `status` names the server, the device, the hook and the project; `device.json` is 600 in a 700 folder; `logout` revokes the device, removes only the server's `credHelpers` entry and deletes the server folder |
| `TestCLIDocker` | Docker in Docker after setup (the printed entry added to `config.json`) pushes and pulls with a device session; `get` answers only the server's hosts, `list` and `store`; `publish` of a Helm chart (`alpine/helm`); ORAS push and pull through the helper |
| `TestCLIMaven` | `publish` with a pom that names another deployment target, with the newest Maven 3 and the newest Maven 4 (from the metadata of Maven Central); the mirror of the person's `settings.xml` (a proxy repo of Central) serves the plugins; `mvn` resolves both jars |
| `TestCLIGradle` | `publish` of a library (Kotlin DSL) and a Gradle plugin (Groovy DSL); the build's own repository on another host (a Caddy that refuses credentials) gets none; a clean build applies the plugin and loads the library; a build with no `maven-publish` gets the message |
| `TestCLIJenkins` | A real Jenkins (`testdata/jenkins`: Jenkins LTS with the OpenID Connect Provider plugin, Pipeline, Credentials Binding, Git, Maven and the Docker CLI; built once per run) runs, from local git repositories, the Jenkinsfiles that `GET /api/repos/{name}/ci` writes. The Maven job installs the CLI with the server's `install.sh` and publishes with `mvn`; the antrun plugin shows that Maven has no `OIDC_TOKEN`. The Docker job builds on a Docker in Docker engine (`DOCKER_HOST`) and pushes through the helper. The server checks the plugin's tokens with the keys that Jenkins serves; the build records name the job, the build number and the `GIT_COMMIT` of the checkout. A project file of another server gets no token, the helper gives none for a host of another server, and no file of Jenkins holds a session token. The server has the alias that the Jenkins credential names as its audience |
| `TestCLIGitHubActions` | Runs in the `e2e-github-actions` job of `ci.yml` (`id-token: write`, `E2E_GITHUB_ACTIONS` set) and skips elsewhere: the CLI in a Maven container asks GitHub for the job's identity token and publishes with `mvn`; the server checks it with GitHub's keys; the build record names the repository, the run and the signed `sha`, also with another `GIT_COMMIT`; each session token is masked; a job without an identity token names `id-token: write` |
| `TestCLIUpload` | `publish FILE` into the `path` folder of a raw repo (a 65 MiB file through the resumable routes) and of `.deb` packages to the `dist` of the project file (a 65 MiB package through the resumable routes); `apt` installs them |
| `TestCLICompat` | Programs built at three versions with test entries: the server v0.9.0 (with the CLI archive v0.9.0), the CLI v0.8.0 and the CLI v1.0.0 (with an older path for one entry). The version header; the drift notice once a day; a `needs = "cli"` entry stops the older CLI with the install line, and on a terminal it updates and runs the command again; a `needs = "server"` entry stops the newer CLI with the summary, or takes the older path; an unknown field of an entry is ignored; the server v0.9.0 is built with a remedy action for a used setup line that no CLI knows, and the CLI prints the problem's text and link; a Caddy proxy whose upstream does not run answers 502, which gets the generic message; `update` through a Caddy proxy in front of two server versions (`SHA256SUMS` of one, the archive of the other) refuses the archive, makes the helper name again, and changes nothing for a newer CLI |
| `TestInstall` | `install.sh` with curl (`buildpack-deps`) and with BusyBox wget (Alpine) and `install.ps1` with PowerShell on Linux install a CLI that runs, make the helper name a symbolic link, write `$GITHUB_PATH`, pass the arguments to the CLI, and install nothing with a wrong hash |

**macOS zone** (tag `macos`, the `macos` job of `release.yml` only). `TestMacOS` needs no container
engine: it starts the server binary of `E2E_SERVER_BIN` on 127.0.0.1, installs the CLI with
`install.sh` into a new `HOME`, and runs setup and publish for a Maven project (`mvn deploy` of a
jar) and a raw project. Agents never run it.

`TestNexus` runs a Nexus container (`sonatype/nexus3`) and starts the server with
`TR_NEXUS_SYNC_INTERVAL=12h`, so that no scheduled sync changes the counters. The fixture has two
maven2 hosted repos (anonymous read in Nexus for one of them only), an apt repo with an encrypted
key pair, a raw repo, an npm repo, an offline raw repo, a maven2 proxy with remote credentials and
a maven2 group of the two hosted repos and the proxy. On each storage backend the test:

- refuses a source with a wrong password; a new source is `no_repos` with no next sync;
- checks the repo list: format, type, support, `public`, the offline reason, the facts and
  questions of the Debian plan (the Nexus key pair with a passphrase), the
  proxy remote and username, the group members;
- saves the repos (the apt repo with the answers `signing_key: source` and its passphrase, the
  proxy with its password) and waits for the
  sync that the save starts (`trigger: save`); the source is then `up_to_date` with the next sync
  one interval after the end; the targets have the kind and the public setting of the save, the
  proxy has the Nexus remote, credentials and times, and the group has the three members in the
  Nexus order;
- checks the counters (`checked`, `bytes`), the log lines, the Nexus blob dates of a Maven file
  and a Debian package, and that `Release.gpg` verifies with the Nexus public key;
- resolves the files with `mvn`, `apt-get` and curl, adds new files to Nexus and runs a second
  sync (only the new files count as imported, `previous_checked` is the first sync's `checked`),
  then a third sync ("no new files" for each repo) and pages and filters its log;
- checks that the mirrored repos refuse publish sessions, unpicks the raw repo and uploads to it,
  finds no password in any log line, cuts over and writes to the repos. After cut-over, sync
  answers 409. No response holds the Nexus password;
- in the subtest `switch-over`, copies two raw repos, rehearses publishing to the first (a
  publisher of the repo publishes to its rehearsal, empties it, publishes again; the rehearsal
  is deleted at the end), switches over the first alone (uploads work there, the second still
  refuses publish sessions, a new Nexus file reaches only the second, a save cannot move or drop
  the switched-over repo), then switches over everything, which cuts the source over;
- in the subtest `gradual-move`, moves a raw repo project by project (the moving repo and the group
  `<target>-all`; a file published to the moving repo reads through the group), merges it with a
  skipped file and a conflict taken from the moving repo, keeping the target's name (the old name
  redirects, a publish through it lands in the target, the group reads the target); then moves a
  second raw repo, whose merge fails while its Nexus repo is offline (the final sync fails),
  resumes once the repo is online again and keeps the moving repo's name (the target's old name
  redirects);
- in the subtests `merge-docker` and `merge-npm`, merges a moving docker repo (skopeo pushes v1, a
  tag that the copy holds with another digest, and v2; the plan lists the tag as the one conflict,
  taken; crane pulls both tags through both names and the copy's old image by digest) and a moving
  npm repo (`npm publish --tag beta`; the plan lists `latest` as a conflict, kept; after the merge
  the packument through both names has `beta` and the copy's `latest`); each merge's final sync
  names the merge.

`TestScale` (tag `scale`, `go tool task test scale`) seeds 100k and then 1M assets with `seed N`
on a named volume. It starts a server on the volume and measures the p95 latency over 400 runs of
each operation: Maven GET with `If-None-Match`, an asset list walk, a rare search, a common
search and token auth (`/api/me`). An operation fails when its p95 at 1M exceeds
`max(2 x p95 at 100k, p95 at 100k + 2 ms)`.

## Code map and recipes

Where each part of the code lives, and the steps to add a module, a repo format or a UI page.

### Where things live

| Need | Place |
|---|---|
| Wiring and startup order | `internal/server/server.go` (kernel, module lifecycle); the module list in `internal/modules/modules.go` |
| Module contract | `internal/module` (`Module`, `Extender`, `Host`, `Use`); jobs in `internal/jobs`; routes and policies in `internal/route` |
| Core setting | `internal/settings/general.go` (the `general` group) |
| Module setting | `x.Settings(&settings.Group{...})` in the module's `Register`; the group owns its keys, its permission, its page and its `Validate`, `Test` and `Apply` hooks |
| Permission | `x.Permissions(perm.Subsystem{...}, perm.Def{...})` in the module's `Register`; the registry is `internal/perm` |
| Audit action | `x.AuditActions(...)`; record with `h.Audit.Record(ctx, ...)` or `RecordTx`; the actor comes from the context (`internal/audit`) |
| Core schema change | The next file of `internal/db/migrations`; the runner is `internal/db/migrate.go` |
| Module schema change | The next file of `internal/<module>/migrations` |
| Query | `queries/*.sql` of the owner (`internal/db` or the module), then `go tool task gen` |
| New module | `internal/<module>` + one line in `internal/modules/modules.go` (see below) |
| Format contract | `internal/format/format.go` (`format.Spec`), `kit.go` (the standard routes) |
| JSON API route | `x.Routes` of the owning module (core: `internal/api/api.go`) + handler; shared helpers in `route.API` (`h.API`); errors via `httpx.Error` or `route.Fail` (RFC 9457 problem documents under `/api/` and `/cli/`); declare a named problem type once with `httpx.DeclareProblem` (`internal/httpx/problem.go`) |
| Credentials, permission checks | `internal/auth` (`Identify`, sessions, tokens, second factor, bearer tokens), `internal/route/policy.go` (the policy), `internal/access/engine.go` (`Decide`, the access report) |
| Publish sessions, devices, trust policies, CI providers | `internal/publish` (module `publish`, routes in `api.go`, `mint.go`, `providers.go`); `internal/ci` (provider registry, build records); setup codes, `/api/devices/setup`, `/api/devices/self` and `/api/publish/sessions/exchange` in `setup.go`; the install scripts and CLI downloads in `install.go`, `install.sh`, `install.ps1` with the archives of `internal/clidist` (built by `tools/task/cli.go`); sign-in and second factor routes in `internal/api/security.go` |
| Byte pipeline | `internal/assets` (`Commit`, `Remove`, `Reader`, `Uploads`), `internal/blobs` (content-addressed blobs, GC), `internal/repopath`, `internal/ops` (repo operations), `internal/rebuild` (index rebuild queue), on top of `internal/storage` |
| UI page | `web/src/pages/**/*.svelte` (`repo/`, `account/`, `repositories/`, `server/`, `settings/repos/`); its `PageDef` (path, access, Settings nav) in the owning module's `web/src/modules/<id>/module.ts`; API calls in `web/src/lib/api/` (a format module's own routes in its own folder). See `web/src/README.md` |
| UI of a format | The descriptor (`format.Spec` fields and `UI`, `GET /api/formats`; `web/src/lib/formats/registry.svelte.ts`); a format-specific component only as a named `formatParts` slot in `web/src/modules/<format>/module.ts` |
| File tree, deletes, manual upload | `internal/files` (tree), `internal/api/files.go`, `internal/api/pipeline.go` (repo delete, resumable uploads), `format.Spec.Upload`; UI in `web/src/pages/repo/` (`FilesTab`, `upload/`, `delete/`) |
| Repository lists, Settings overview figures | `internal/api/repolist.go` (`GET /api/repos`, `GET /api/overview`), `internal/repos/list.go` (one index per list), the grant lists in `internal/db/dbq/extra.go`, `repo_totals` in migration `0005_performance.sql`; UI: `RepoPicker`, `chain` and `RepoFormats` in `web/src/lib/load.svelte.ts` |
| Client IP, IP lists, repo IP rules | `internal/netrules` (module `network`: `lists.go`, `rules.go`, `clientip.go`, `module.go`), `web/src/pages/settings/repos/` (Access tab), `web/src/pages/server/IPLists.svelte`, `IPListPage.svelte`, `ReverseProxy.svelte` |
| Docker / OCI registry | `internal/docker` (module `docker`: `routes.go` at `/v2/`, `token.go` the token endpoint `/docker/token`, `manifests.go`, `blobs.go`, `lists.go`, `referrers.go`, `hooks.go` (the rows of `migrations/`), `links.go` the job `docker.links`, `proxy.go` the proxy (`Classify`, `RemoteAuth`, tags list) and group tags, `usage.go` and `tools.go` the snippets and the CI tool, `versions.go`, `api.go` `/api/docker/{name}/manifest`, `importer.go` the Nexus import, `demo.go`, `seed.go`); UI part `web/src/modules/docker/`; e2e in `e2e/docker_test.go`, `e2e/docker_proxy_test.go`, `e2e/nexus_docker_test.go` and `e2e/cli_docker_test.go` (clients zone), with `StartDind` and `Dind.CopyFile` in `e2e/clients_test.go` and the OCI helpers in `e2e/oci_test.go` |
| Telesphorepo CLI and its Docker credential helper | `cmd/telesphorepo` (MIT, its own `LICENSE`; stdlib, `golang.org/x/sys`, `BurntSushi/toml` and the root package `changelog` only; imports no server package): `main.go` the commands and the error handling, `setup.go`, `publish.go`, `detect.go` the detection table (a new format adds a row), `upload.go` raw and Debian uploads, `login.go` login, logout and status, `update.go`, `compat.go` the change log check, `problem.go` problem documents and remedies, `client.go` the HTTP client, the device flow and the mints, `ci.go` the CI token, `project.go` `telesphorepo.toml`, `docker.go` the helper mode (program name `docker-credential-telesphorepo`), `identity.go`, `device.go`, `exchange.go`, `dockerconfig.go`, `acl_*.go`, `parent_*.go`, `tty_*.go`; built and embedded by `tools/task/cli.go`, released by `tools/task/release.go` |
| Signing keys | `internal/signing/keys.go`, `internal/signing/module.go` (the repo field `signing_key`), `GET /debian/{repo}/key.asc` in `internal/debian`; UI in `web/src/pages/server/SigningKeys.svelte` and `web/src/modules/signing/` |
| Upload plan and upload | `Plan` and `Store` of the format (`format.Spec.Upload`), e.g. `internal/maven/plan.go`; routes in `internal/api/files.go`; UI in `web/src/pages/repo/UploadView.svelte` and `upload/` |
| Snippets, CI files | `Usage` and `Tools` of the format (`format.CLITool`, `format.CLISnippet`); `internal/ci/workflow.go` (the GitHub Actions, Jenkinsfile and Jenkins freestyle builders, each with the install step of the CLI) |
| Audit log | `internal/audit` (registry, `Record`), `internal/activity` (module `audit`: `GET /api/audit`), `web/src/pages/server/Activity.svelte` (its time ranges in `activity/range.ts`) |
| Download statistics | `internal/stats` (the counters), `assets.Reader.SetDownloads` and the count calls of the read path, `internal/activity` (`GET /api/stats`, the jobs), `web/src/pages/settings/overview/DownloadBars.svelte` |
| Edge delivery | `internal/edge` (module `edge`: `module.go` buckets and routes, `settings.go` the group `edge`, `objects.go` the R2 objects, `watcher.go` the pipeline watcher, `outbox.go` the job `edge.outbox`, `access.go` the access objects, the change notices and `edge.reconcile`, `control.go` the job `edge.backfill` (on, off, draining, regions, blob check), `gap.go` and `seen.go` what the Worker writes, `speedtest.go` the passes and files of the speed test, `wrangler.go`, `api.go` with the download `worker.zip`); `format.Spec.Edge` of each format; the Worker in `edge/` (`src/index.ts` fetch, `shapes.ts`, `glob.ts` the port of `format.Glob`, `access.ts`, `serve.ts`, `copy.ts` queue, `gap.ts` cron, `speedtest.ts` the pass of the speed test, `scripts/provision.ts`); the Worker folder that the server embeds (`internal/edgedist`) and the release archive, both from `tools/task/edge.go`; UI in `web/src/pages/server/EdgeDelivery.svelte` and `web/src/modules/edge/`; e2e of the Worker download in `e2e/edge_test.go`, none of the serving (local R2 differs from R2 in what the edge relies on) |
| Backups and restore | `internal/backup` (module `backup`: `backup.go` one backup, `list.go` the list and the retention, `restore.go`, `offline.go` the source of the CLI, `module.go` settings, job and routes), the core queries in `internal/db/queries/backup.sql`, `db.VacuumInto` and `db.OpenReadOnly`, the commands in `cmd/telesphorepo-server/backup.go`, `web/src/pages/server/Backups.svelte` and `web/src/modules/backup/`; e2e in `e2e/backup_test.go` |
| Storage move (FS ↔ S3) | `internal/move` (module `storage`: service, copier, `module.go`), `internal/storage/mirror.go`, `web/src/pages/server/Storage.svelte` |
| Single sign-on | `internal/oidc` (module `sso`: sign-in, the permission map, device approval), `web/src/pages/server/SingleSignOn.svelte` |
| Nexus sources, syncs, cut-over | `internal/nexus` (module `nexus`: client, service, `sync.go` scheduler and runner, `api.go`), `web/src/pages/server/NexusMigration.svelte`, `NexusSource.svelte`. Per-format import code is the `Importer` of the format (`Plan` for the target repo, `Target`/`Store` per file). Repo lock: the repo operation `mirror` (`repo_ops`), checked in `access.Engine.Decide` |
| Demo data and seed | `internal/demo`, and `Demo` and `Seed` of each module; `cmd/telesphorepo-server/demo.go` |
| E2E harness | `e2e/harness_test.go` (servers, `ForEachStorage` for `fs` + SeaweedFS, `OnLocalDisk`), `e2e/clients_test.go` (Maven, Debian and curl helpers that several zones share), publish helpers at the end of `harness_test.go`, `e2e/keycloak_test.go` (Keycloak fixture), `e2e/build_test.go` (`BuildCLI`, `BuildServer`: programs of this repo built on the host with `go build -overlay`), `e2e/cli_server_test.go` (the CLI routes of the server, core zone), `e2e/cli_harness_test.go` and `e2e/cli_*_test.go` (clients zone: the CLI in containers, the secret scan), `e2e/cli_jenkins_test.go` with `e2e/testdata/jenkins` (the real Jenkins of the clients zone) |
| Change log and server version | `changes.toml` at the repo root (embedded by `changes.go`, package `telesphorepo`), the parser in `changelog/` (stdlib and toml only, shared with the CLI), `GET /api/changes` in `internal/api/changes.go`, the `Telesphorepo-Version` header in `internal/server/server.go`; `go tool task tag <version>` stamps the `dev` entries |
| Dev tooling | `tools/task` |

### Adding a module

Every format and every core feature with routes is a module (`internal/module`). A module is
`module.Module{ID, Title, Requires, Register}`. `Register` runs once, before the database opens.
It calls the `x.*` methods of the `Extender` to declare what the module adds: `Schema`,
`Settings`, `Permissions`, `AuditActions`, `OpType`, `Init`, `Routes`, `RootPrefix`, `Job`,
`Format`, `RepoField`, `RepoGuard`, `CIProvider`, `ResolveClient`,
`TokenKind`, `Demo` and `Seed`. A module edits no core file.

1. Create `internal/<module>` with `func Module() module.Module`. The module ID is also the
   schema component. List the modules you use in `Requires`; each must be earlier in the list.
2. Add one line to `internal/modules/modules.go`, in start order.
3. In `x.Init`, keep the `*module.Host`. It holds the kernel services (`DB`, `Settings`,
   `Audit`, `Assets`, `Reader`, `Uploads`, `Rebuilds`, `Repos`, `Ops`, `Auth`, `Access`,
   `Formats`, `CI`, `Builds`, `API`) and a log tagged with the module ID. Give a
   service to other modules with `h.Provide` or `module.ProvideAs[T]`, and get one from a
   required module with `module.Use[T](h)`. Run background work as `x.Job` or `h.Go`.
4. Register each route with a policy in `x.Routes`. A module registers only under `/api/`, under
   `/<format>/` of its formats and under its `RootPrefix`es.
5. If the module has tables, add `migrations/0001_init.sql`, `queries/*.sql` and a `sqlc.yaml` (copy
   `internal/debian/sqlc.yaml`), and declare them with `x.Schema`. Run `go tool task gen`.
6. Add `e2e/<module>_test.go` that uses a real client. Run its byte path on both stores (`ForEachStorage`) and the rest on the local disk (`OnLocalDisk`).
7. Document the routes, tables and settings in `SPEC.md`.

#### Adding a repo format

A format is a module that passes a `format.Spec` to `x.Format`. A nil field is a capability that
the format lacks.

- **Required:** `Name` (the URL segment and the `repos.format` value), `Config.Validate` and
  `Kinds` (it holds `hosted`). Set `Title`, `Description`, `Config.Default` and `Config.Fields`
  for the web UI. Set `Icon` too: embed an `icon.svg` of one color (`currentColor`, 24x24 view
  box).
- **Routes:** a nil `Routes` mounts `format.Standard` (GET and HEAD through the read path, PUT
  through `Commit`). Set `Routes` for a protocol of its own, and register each route with
  `route.FormatRead` or `route.FormatWrite`.
- **Bytes:** `Describe` (content type and kind of a path), `Admit` (store, keep or refuse a
  write), `OnCommitted` and `OnRemoved` (your own rows, in the same transaction),
  `AfterCommit`, `AfterRemove`, `OnRepoChange`, and `Rebuild` for an index that a background
  job builds.
- **Proxy and group:** `Kinds` with `proxy` needs `Classify` and `ProxyProbe`; `RemoteAuth`
  answers a remote's token challenge. `Kinds` with `group` can set `Merge`, and with `Versions`
  needs `GroupVersions` (`each` with `VersionOrder`, `VersionsByName` when that order reads only the version string, and, for versions with a folder, `BuildOrder`; or `owner` with `GroupUnit`, the package of a path, and `Versions` and `Builds` that answer for a proxy from its remote's document).
- **Web UI:** `Description`, `Config.Fields`, `UI` (the descriptor), `Versions` and `Builds`,
  `Usage`, `ArtifactUsage` and `Tools` (snippets and CI steps), `Publish` (`cli` or `helper`), `Upload` (`Plan` and
  `Store`), `SigningKey` and `RepoTabs`. The web UI reads the descriptor and names no format.
- **Import:** `Importer` (`Sources`, `Target`, `Store`, `Plan`): the Nexus import offers each
  source file to the format.

`raw` (`internal/raw`) is the smallest example. `debian` shows a schema, a repo field, `Rebuild`
and an importer plan. `maven` shows proxy, group, `Merge`, `Upload` and `Tools`.

### Adding a UI page

1. Create `web/src/pages/<area>/<Name>.svelte` with `let { params, query }: PageProps = $props();`.
2. Add one `PageDef` to the owning module's `web/src/modules/<id>/module.ts`: `path`, lazy
   `page`, `layout` (`app`, `settings` or `account`), `access` (`any`, `user`, `repositories`, `repo` or a
   permission ID), `title`, and `setup` for a page of a setup-only session.
3. For a Settings or account page, give it `nav` (group, label, icon, order).
4. Add the route to the table in `web/src/README.md`.
5. Call the API through `api.*`. For a new server route, add a function and its type in
   `web/src/lib/api/`. A format module keeps the calls to its own routes in its own folder.
6. Use `Resource` or `Keyset` (`lib/load.svelte.ts`) for requests, and show loading, error and
   empty states. Use `ConfirmDialog` for every destructive action.

