# Telesphorepo: system as built

This file describes what the code does today. Update it in the same change whenever behaviour
changes. `AGENTS.md` holds the working rules for coding agents, and `README.md` is the short
guide for people who run the server.

## Purpose

A self-hosted repository for Maven repos (including Gradle plugins), Debian (apt) repos and raw
file repos. Each format is a module (see **Modules**), and a new format is one more module: the
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
| `cmd/telesphorepo` | CLI: `serve` (default), `dev`, `user add/passwd/reset-2fa`, `version`, `help`; hidden `dbcheck` and `seed` |
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
| `internal/db` | SQLite (modernc, no cgo), WAL, separate writer/reader pools, one schema per component with a fingerprint (`Init`), `PRAGMA optimize`, the plan guard (`CheckPlans`) |
| `internal/ci` | The CI provider registry (`Registry`: `Build`, `Describe`) and the build records (`Builds`: `Record`, `Upsert`, `Loader`, `Cleanup`). See **CI providers and build records** |
| `internal/clients` | The publisher client registry (`Registry`): the build tool plugins that enroll devices. See **Publisher clients** |
| `internal/db/dbq` | sqlc-generated core queries (`internal/db/queries/*.sql`) plus hand-written FTS queries in `extra.go` |
| `internal/storage` | `Store` interface; `fs`, `s3`, and `Live`, a hot-swappable wrapper with `Open`, `SameBackend` and `Probe`. Declares the storage setting keys and `ConfigView` (the store of a snapshot) |
| `internal/repopath` | The pure repo-path parser: `Parse` (a file path) and `Folder` (`""` or a folder ending in `/`). Routes, file operations, imports and uploads check every path with it |
| `internal/blobs` | Content-addressed blobs: `Spooled` bodies with their hashes, `Store` (`Key`, digest `Lock`, `Ensure`/`EnsureAll`, `Get`, `Open`) and the blob GC. See **Byte pipeline** |
| `internal/assets` | The byte pipeline: `Pipeline` (`Commit`, `CommitAll`, `Remove`, point lookups, listing, FTS search), `Reader` (the read path of hosted, proxy and group repos), `Uploads` (resumable uploads) and the asset hooks of a format (`Hooks`). See **Byte pipeline** |
| `internal/ops` | Repo operations (`repo_ops`): `Begin`, `Progress`, `SetState`, `Fail`, `End`, and the operation types that modules register (`delete`, `mirror`) |
| `internal/rebuild` | The durable rebuild queue of format indexes (`rebuilds`): `MarkTx`, `Pending`, and the debounced worker of the job `core.rebuild` |
| `internal/move` | Module `storage`: the storage move copies every blob to a new backend in the background behind a `storage.Mirror`; owner hooks of the `storage` settings group |
| `internal/nexus` | Module `nexus`: Nexus 3 REST client, sources, mapping, the sync scheduler and runner, and cut-over. Names no format; it calls the `Importer` of the target format (its `Plan` for the target repo, its `Run` for each file) |
| `internal/auth` | Users (kinds `local`/`oidc`), argon2id passwords, Basic-auth cache, read tokens, sessions, rate limiting, permission checks. A caller is a `Principal` with one credential kind (`Cred`: password, read token, session, setup session, publish session). `Identify` reads the credentials a route policy names. Token kinds are verifiers by prefix (`AddTokenKind`). Also core bearer tokens (`IssueBearer`), fresh second-factor proofs (`CheckProof`, `IssueGrant`) and the session view (`View`) |
| `internal/oidc` | Module `sso`: OIDC sign-in for any person at the provider, the server permissions from a claim (`oidc.permission_claim`, `oidc.permission_map`), and the SSO device approval: `/api/auth/oidc/login`, `/device` and `/callback` (go-oidc + x/oauth2, PKCE S256); owner hooks of the `sso` settings group |
| `internal/publish` | Module `publish`: device enrollment, nonces, trust policies, the CI token exchange, the CI providers `github-actions` and `jenkins`, the token kind `trs` of publish sessions, the publish API and the cleanup job |
| `internal/repos` | Repos, their kind, their active operation (`Repo.Op`) and per-format JSON config. `Create` and `Update` (a `Patch`) are the only changes of a repo record: each checks the whole new state first, then saves it in one transaction with the format's `OnRepoChange` and one audit event. A `Validator` (the format registry) checks the config and a `KindCheck` the kind. Modules drop what they keep about a deleted repo with `OnDelete`. A module adds repo values that it keeps in its own tables as fields (`repos.Field`, `AddField`): `Create` and `Update` validate and save them in the same transaction, and `Repo.Fields` holds them. Keeps the group members and the proxy remotes in memory (see **Group and proxy repositories**) |
| `internal/proxy` | Fetches the files of proxy repos from their remotes (the `assets.Fetcher` of the read path), stores them through `Commit` with source `proxy`, answers a remote's 401 challenge through the format's `RemoteAuth`, and remembers remote 404s |
| `internal/format` | The format contract: `Spec` (every optional capability is a field that is nil when the format lacks it), the `Registry` (`All`, `Get`, `Hooks`, `RebuildRun`, `OnRepoChange`, `ValidateConfig`, `SupportsKind`, `Importer`), `Kit` and `Standard` (the default routes), and `Importer.Run` (the shared import of one source file) |
| `internal/maven` | Module and format `maven`: the standard routes, release immutability (`Admit`), the content types, the merge of `maven-metadata.xml` in a group, the metadata after a delete (`AfterRemove`), web UI uploads with checksums and metadata |
| `internal/debian` | Module and format `debian`: `.deb` upload/parse through `Commit`, the package rows (`OnCommitted`, `OnRemoved`), the index rebuild (`Rebuild`), `/debian/{repo}/key.asc`, the plan of a Nexus apt import (distribution and signing key). Owns its tables, queries (`queries/`, `dbq/`) and architecture config; binds a signing key per repo (`SigningKey`) |
| `internal/raw` | Module and format `raw`: plain files at `/raw/{repo}/{path...}` (the standard routes) with an `overwrite` policy (`Admit`) |
| `internal/signing` | Module `signing`. OpenPGP: key `Generate` and `Parse`, the `Keys` service (create, import, delete, one signer per key, the key of each repo), the repo field `signing_key`, clearsign/detach-sign (ProtonMail go-crypto). See **Signing keys** |
| `internal/files` | The file tree and the details of one file. Writes and deletes go through the byte pipeline |
| `internal/audit` | The audit log: the action registry (`Actions`), the actor in the context (`WithActor`, `System`, `CLI`), `Record` and `RecordTx`, the list query and `Cleanup`. See **Audit log** |
| `internal/activity` | Module `audit`: `GET /api/audit`, `GET /api/audit/actions`, the permission `activity.read`, the settings group `activity` and the job `audit.retention` |
| `internal/netrules` | Module `network`: client IP resolution behind trusted proxies (the client address of every request), the IP lists (presets and custom lists) and the per-repo IP rules. See **Network rules** |
| `internal/api` | Module `core`: sign-in, the account, people, read tokens (token kind `trp`), repos, grants, files, uploads, search, the settings API and `/api/formats`. The JSON API of every module is under `/api/`, wrapped in `http.CrossOriginProtection` |
| `internal/httpx` | JSON helpers, error shape, `StatusError` (an error with its HTTP status), `RepoPath` (the URL check of a `{path...}`, then `repopath.Parse`), `Limit` (default 50, max 200) |
| `internal/demo` | The dev demo data (`telesphorepo dev`): it writes repos, users and files through the byte pipeline, with the demo data of each module (`Extender.Demo`). See **Demo data** |
| `internal/containerengine` | Docker/Podman detection for tooling and e2e; not linked into the server |
| `web/` | Svelte 5 + Vite UI (Tailwind CSS v4, shadcn-svelte Maia style), embedded via `web/embed.go` (`go:embed all:dist`). See **Web UI** |
| `tools/task` | `go tool task`: dev, build, preview, test, gen and doctor |
| `e2e/` | End-to-end suite in four zones (tags `e2e`, `import`, `scale`, `clients`) using testcontainers |

## Modules

Every repository format and every core feature with routes is a module (`internal/module`). A
module is `module.Module{ID, Title, Requires, Register}`. `Register` runs once per process,
before the database opens, and declares what the module adds; each `Extender` call adds one
entry to the module's manifest. The server reads the manifests and never finds a capability by
type assertion.

| Extender call | What it adds |
|---|---|
| `Schema(db.Component)` | The module's schema (`schema.sql`) and queries for the plan guard; the component is named after the module |
| `Settings(*settings.Group)` | A settings group that the module owns: its keys, the permission that edits them, the UI page, and the owner hooks `Validate`, `Test` and `Apply` (see **Settings model**) |
| `Permissions(perm.Subsystem, perm.Def...)` | A permission subsystem of the module and its permissions (see **Permissions**); one call per subsystem |
| `AuditActions(audit.ActionDef...)` | The audit actions the module records, each with a label and a subsystem (see **Audit log**) |
| `OpType(ops.Type)` | A type of repo operation that the module begins: its message, and `Resume` and `Clear` when it has them (see **Repo operations**) |
| `Init(fn)` | Startup code with the `Host`; an error stops startup |
| `Routes(fn)` | Routes on the module's `route.Router` |
| `RootPrefix(prefix)` | A path outside `/api/` and the format segments that the module owns, e.g. `/setup.sh` |
| `Job(jobs.Job)` | A job named `<module>.<name>` with `Start`, `Run` and `Tick` |
| `Format(format.Spec)` | A repository format; the module owns `/<name>/`. A nil `Routes` mounts `format.Standard` (see **Byte pipeline**) |
| `RepoField(repos.Field)` | A repo value that the module keeps in its own table, e.g. `signing_key`: `Applies` (by format and repo kind), `Validate`, `Default`, `Save` (in the repo transaction), `Committed`, `Load` (from memory). It serves once the module's `Init` ran |
| `RepoGuard(access.Guard)` | An extra rule of the access engine (the IP rules): `Name`, `Check`, `Describe` (the access report), `Refused` (search) |
| `CIProvider(ci.Provider)` | A CI system whose tokens make build records and whose trust policies the access report describes |
| `PublisherClient(clients.Client)` | A publisher client that enrolls devices (`gradle`, `maven`) |
| `ClientAddress(fn)` | The client address of every request; at most one module |
| `TokenKind(prefix, verifier)` | A token kind (`trp`, `trs`) |
| `Demo(module.DemoData)`, `Seed(module.SeedFunc)` | The dev demo data and the rows of `telesphorepo seed` of the module's one format |

`module.Host` holds the kernel services (DB, settings, the permission registry, live storage,
audit, jobs, `Blobs`, `Assets` (the pipeline), `Reader`, `Uploads`, `Rebuilds`, repos, `Ops`, auth,
access, the format registry, `CI` (the CI providers), `Builds` (the build records), `Clients` (the
publisher clients), `route.API`) and a log tagged with the module ID. An `Init` and every job of a module record audit events as the system actor of
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

| ID | Package | Requires | Owns |
|---|---|---|---|
| `core` | `internal/api` | | Sign-in, `/api/me`, users, read tokens (`trp`), repos, grants, files, upload plans and uploads, delete plans, resumable uploads, repo operations, search, the settings API, `/api/formats` (the UI descriptors), the snippets and CI files of each repo, `/api/permissions`, the access report; the subsystems `repositories`, `people`, `settings` and `repo`; the kernel's audit actions; the settings group `general`; the operation type `delete`; jobs `core.optimize` (`PRAGMA optimize` every 6 h), `core.blob-gc`, `core.upload-sessions` (every 10 min), `core.rebuild` and `core.ops` |
| `audit` | `internal/activity` | | `GET /api/audit`, `GET /api/audit/actions`, the subsystem `activity`, the settings group `activity`, job `audit.retention` (every hour) |
| `storage` | `internal/move` | | `/api/storage/moves/...`, the subsystem `storage`, the settings group `storage` (its `Validate`: the S3 rules; `Test`: the probe; `Apply`: refuse a change while a move runs, probe the new store, start the move), the `storage.move.*` actions, job `storage.resume` |
| `network` | `internal/netrules` | | The client address, IP lists, repo IP rules (a repo guard and the networks of the access report), the IP lists of the trusted proxies (`/api/network/trusted-proxies`), the subsystem `network`, the settings group `network` (`trusted_proxies`; its `Validate` refuses a `list:` entry in a change), the `network.*` actions, job `network.presets` (every hour) |
| `signing` | `internal/signing` | | Signing keys and `/api/signing-keys/...`, the repo field `signing_key` (for the hosted repos of the formats with `SigningKey`), the subsystem `signing`, the `signing.key.*` actions; provides `*signing.Keys` |
| `publish` | `internal/publish` | `audit` | Devices, publish sessions (`trs`), CI trust policies, the CI providers `github-actions` and `jenkins`, `/api/devices/...` and `/api/publish/...`, the subsystem `ci`, the settings group `publishing`, the `publish.session.*`, `device.*` and `ci.pipeline.*` actions, job `publish.cleanup` (every minute), `/setup.sh` and `/setup.ps1` (the frame of the project setup; see **Project setup**); provides `*publish.Service` and `publish.Minter`; loads the publish sessions of core bearer tokens and gives the access report its CI pipelines |
| `sso` | `internal/oidc` | `publish` | `/api/auth/oidc/...`, the subsystem `signin`, the settings group `sso` (its `Validate` with the startup guard and the claim map check, `Test` and `Apply`: the discovery check), the `sso.*` actions; tells auth when single sign-on manages permissions |
| `nexus` | `internal/nexus` | | `/api/nexus/...`, the subsystem `nexus`, the settings group `nexus`, the `nexus.*` actions, the operation type `mirror`, job `nexus.scheduler` (resumes running syncs, then the schedule loop) |
| `maven` | `internal/maven` | | Format `maven` with the tools `gradle` and `maven` (their CI steps and project setup fragments), the publisher clients `gradle` and `maven` |
| `debian` | `internal/debian` | `signing` | Format `debian` with its schema and `Rebuild` |
| `raw` | `internal/raw` | | Format `raw` |

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
| `SomeRepo` | A repository permission ID that passes in place of `Perm` when the caller holds it on at least one repo. The handler gets those repos in `Call.Managed` (`All`, or the repo IDs) |
| `Repo` | `route.RepoRule{Name, Format, Op, Perm, AnyPerm}`: the repo the route acts on (default: the path value `{repo}`, else `{name}`), the format it must be of (another format is 404), the operation (`access.Meta`, `Read`, `Write`, `Delete`, `Manage`, `Mint`) and the repository permission it needs (or one of `AnyPerm`; none with `Meta` is any) |
| `Errors` | The writer of a refusal; nil is `{"error": "..."}`. A format with its own error JSON sets it |
| `Challenge` | The `WWW-Authenticate` of a 401; nil is a Basic challenge outside `/api/` and none for a request with `X-Telesphorepo-UI: 1` |

Presets: `route.Public`, `route.UI` (a full UI session), `route.Setup` (a UI session, setup-only
too), `route.AnyUser` (password, read token, UI session), `route.UIPerm(id)`,
`route.UIRepo(op, ids...)`, `route.RepoRead()` (a JSON read of a repo with `repo.download`),
`route.FormatRead(format)` (anonymous, password, read token, UI session, publish session; `Read`
with `repo.download`) and `route.FormatWrite(format)` (a publish session; `Write` with
`repo.publish`).

The router panics at startup when a policy that accepts a password or a read token has a method
other than GET or HEAD, or a server permission (`Perm` or `SomeRepo`), and when a `Write` route
accepts a credential other than a publish session. A route with `Bearer` has no server permission,
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
   passes at the repo step only with the open publish session of its scope for the repo.
2. A request without a credential passes only when the policy is `Anonymous` or names a repo; else
   401 (`authentication required` where a password is accepted, else `sign in through the web UI
   for this action`).
3. The fresh second factor (`FreshFactor`): `X-Telesphorepo-Proof: totp <6 digits>`, `passkey
   <base64url of the assertion JSON>` (after `POST /api/security/passkeys/assert`) for a local
   account, `grant <grant>` for an SSO account (the grant of a fresh SSO sign-in, used once). No
   factor: 403 `second_factor_required` (in dev mode a person without a factor passes); a wrong
   proof: 401 `second factor did not verify`; no proof: 400; a missing or used grant: 401. Failures
   count against the username like failed passwords.
4. The server permission, or `SomeRepo` on at least one repo: 403 `permission required: <label>`
   (e.g. `permission required: Manage signing keys, or Edit settings on a repository`).
5. The repo: an unknown repo, or one of another format, is 404 (401 for anonymous on a private
   repo); then `access.Engine.Decide` (see **Access engine**).

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
   scopes (403 for a repo or an operation outside them).
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
group skips a member that the reader may not read). "May this caller write to this repo now" is
always `Decide(..., Write)` or `Decide(..., Mint)`.

**Core bearer tokens.** `auth.Service.IssueBearer(p, scopes, ttl)` returns
`trb.<base64url payload>.<base64url HMAC-SHA256>` for at most 15 minutes. The key is HKDF-SHA256
of the master key with the info `telesphorepo bearer v1`. The payload holds the credential kind
(none for anonymous), the user, the read token, the scopes (`{repo, ops, session}`) and the
expiry. The credential is a password, a read token or a publish session; a UI session gets no
bearer. A scope holds `Read`, `Write` and `Delete` only. `Write` and `Delete` stay only when the
credential is a publish session and the scope names that session, so a bearer from a password, a
read token or the anonymous caller carries `Read` only, and a bearer writes to one repo at most.
On each request the kernel loads the credential again (the user enabled, the read token present,
the publish session open; the publish module loads the session). It refuses the token when a
scope session is not the session of the credential, or when the scope names another repo than
the repo of that session. A route
reads bearer tokens only with `Policy.Bearer`; a format with its own token exchange (e.g. an OCI
registry, `GET /v2/token`) issues them.

**Minting a publish session.** The publish module provides `publish.Minter` (`module.Use`):
`Mint(ctx, r, MintCred, repo)` checks the credential, runs `Decide(..., Mint)` with `repo.publish`,
writes the session row and the `publish.session.create` event, and returns the open session without a
token. `MintCred` is one of: `Device` (a device signature over a nonce of
`POST /api/publish/challenge` for one repo, or over a nonce of `ExchangeChallenge` signed for the
repo `*`), `CIToken` (a CI identity token that
a trust policy of the repo allows; a token that minted a session for the repo before gets that
session back while it is open) or `Fresh` (a UI session whose request carried a fresh second
factor). The three mint routes use the same code and answer the token; there a CI token mints
once, and an exchange nonce does not mint. An exchange nonce lives as long as a publish session
(`publish.session_ttl`) and is not used up: a token exchange sends the same proof on each token
request of one push. It mints one session per repo (`publish_exchange_sessions`), and a later
mint for the same repo gets that session back while it is open, so upload sessions continue
across token requests. When that session ended, the proof mints no new one.

**Format UI descriptors.** `GET /api/formats` answers, for each format, everything the web UI
shows or chooses per format, so no shared UI code names a format. It comes from the
`format.Spec`: `Description`; `Config.Fields` (the repo config form: `{key, label, help, type:
string|bool|list|enum|secret, options, required, default, placeholder, change_note}`; a change of
a field with a `change_note` asks first and lists the note); `RepoTabs` and the module repo fields;
`Upload` (`single`, `folder`, `accept`, `hint`, `keep` (the globs of the files of a folder that the
browser sends), `one` and `many` (the words for an item), `note`, `ignored`, `empty`, `fields`,
`reads` (the globs and `max_bytes` of the files whose first bytes a plan reads)); `Tools` (the
build tools: `{id, label, setup, project?}`, and `setup` when a tool has a project setup);
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
lang, code, note}`. A format's `Usage` gives the use snippets and the publish snippets for a
computer and by hand; the core adds the CI files. `internal/ci/workflow.go` holds the one GitHub
Actions builder (`GitHubWorkflow`), the one Jenkinsfile builder (`Jenkinsfile`) and `Indent`. A
`format.Tool` gives `Publish(repo, baseURL, system) ci.PublishSteps` (`Toolchain` steps of GitHub
Actions, `Build`, `Publish`, `Token` (the job needs its identity token in `OIDC_TOKEN`), `Env`)
and an optional `CINote` and `Setup` fragment. `format.CISnippet` builds the file of a tool for a
system and a `ci.Trigger{Branches, Tags}`. A format without a client (Debian, Raw) has the tool
`curl` (`format.CurlTool`): its publish script exchanges `$OIDC_TOKEN` for a publish session at
`/api/publish/sessions/oidc`, uploads with the session token on curl's stdin and closes the
session in an exit trap.

**Lifecycle.** `server.RunWith` starts in this order: logger and data dirs; `db.Open`; every
`Register` once per process (then the permission registry is fixed, and the audit actions, the
settings groups and the format registry are built); the schema of the kernel and of each module
(`db.Init`, in start order); `settings.Load` with every group; the kernel services; each module's `Init` in start order, then the
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

Each schema component has exactly one schema file: `internal/db/schema.sql` for the kernel
(component `core`), and `internal/<module>/schema.sql` for each module with tables
(`Extender.Schema`; the component is named after the module: `storage`, `network`, `signing`,
`publish`, `nexus`, `debian`). A module schema may reference the kernel tables and the tables of
the modules it requires; the kernel schema references no module table. `db.Init` runs the
components in start order (the kernel first). The table `schema_components` (`component`,
`fingerprint`, `applied_at`) holds one row per component with the SHA-256 of its schema text, with CRLF line ends read as LF
(`.gitattributes` also checks out every `*.sql` with LF). A component without a row gets its schema and its row in one transaction. A row with another
fingerprint is `db.ErrSchemaChanged{Component}`: the schema files change in place, so
`telesphorepo dev` deletes and recreates its own data dir, and `serve` refuses to start and names
the component. All tables are `STRICT`. Tables with a natural key are also `WITHOUT ROWID`. Every
foreign-key child column has an index. SQLite runs `PRAGMA optimize` at close and every 6 hours
(job `core.optimize`).

Kernel tables (`internal/db/schema.sql`):

| Table | Key / indexes | Notes |
|---|---|---|
| `users` | PK `id`, UNIQUE `username`, UNIQUE `users_oidc (oidc_issuer, oidc_subject)` where `oidc_subject` is set | argon2id PHC hash (`!` for SSO users, never verifies), `disabled`, `password_changed_at`, `kind` (`local`/`oidc`), `email` |
| `api_tokens` | UNIQUE `public_id`, UNIQUE `(user_id, name)` | `secret_hash` = SHA-256(secret); optional `expires_at`; `last_used_at` written at most once per minute. These are **read tokens**: they never publish |
| `sessions` | PK `id_hash` (WITHOUT ROWID), idx `expires_at`, idx `user_id` | `id_hash` = SHA-256(cookie value) |
| `repos` | UNIQUE `name`, partial idx `repos_anonymous (id) WHERE anonymous_read = 1` | `format` is any registered format name (no CHECK); `kind` is `hosted` (default), `proxy` or `group`; `description` (at most 500 characters); `anonymous_read`; `config` JSON; the counters `file_count`, `total_size` and `last_published_at` (unix time, 0 for never). The values of module fields (`repos.Field`, e.g. `signing_key`) are rows of their module |
| `user_permissions` | PK `(user_id, perm)` (WITHOUT ROWID), idx `user_permissions_perm (perm, user_id)` | The server permissions of a person, as permission IDs (e.g. `people.manage`). No CHECK: every write checks the IDs against the permission registry, and a read skips an ID that no module registers. See **Permissions** |
| `repo_permissions` | PK `(repo_id, user_id, perm)` (WITHOUT ROWID), idx `(user_id, perm)` | The repository permissions of a person on a repo, as permission IDs (e.g. `repo.publish`). No CHECK, as above. See **Permissions** |
| `repo_group_members` | PK `(group_id, member_id)` (WITHOUT ROWID), UNIQUE `(group_id, position)`, idx `member_id` | The members of a group repo in read order (`position`). Both ids cascade with their repo, so a deleted member leaves its groups |
| `repo_proxies` | PK `repo_id` (cascades) | The remote of a proxy repo: `remote_url` (http or https, ends with `/`), `credentials` (sealed JSON `{username, password}`, AAD `repo_proxy:<repo id>`; empty for none), `metadata_ttl` (1800 s), `not_found_ttl` (300 s), `last_ok_at`, `last_error`, `last_error_at` |
| `blobs` | PK `id`, UNIQUE `sha256`, partial idx `blobs_unreferenced (unreferenced_at) WHERE refs = 0` | Content-addressed blobs, storage key `blobs/sha256/<first 2 hex>/<hex>`. `size`; `refs` (the asset rows that point here; the assets triggers keep it); `unreferenced_at` (`created_at` for a new row, the time `refs` reached 0, 0 while referenced); `created_at`. One blob backs any number of assets in any repos |
| `builds` | PK `id`, UNIQUE `(provider, key)`, idx `created_at` | A CI build record (see **CI providers and build records**): `provider` (a CI provider ID, e.g. `github-actions`), `key` (the provider's unique key of the run), `repository`, `revision`, `ref`, `ref_name`, `ref_type` (`branch`, `tag` or `''`), `run`, `workflow`, `actor`, `links` (JSON `[{rel, label, url}]` that only the provider makes), `created_at` (the time of the last mint for the run) |
| `assets` | UNIQUE `(repo_id, path)`, idx `uploaded_by`, idx `assets_blob (blob_id, repo_id)`, partial idx `assets_build (build_id) WHERE build_id IS NOT NULL` | Every stored file: a path that points to a blob (`blob_id`; `size` and `sha256` are the blob's, copied); `content_type` from the format's `Describe`; `kind` is `artifact`, `metadata` or `index`; `build_id` is the CI build record that wrote the file (SET NULL). Only the byte pipeline writes rows; triggers keep `blobs.refs` on every insert, delete and change of `blob_id`. A cached file of a proxy has `checked_at` (when the remote last confirmed it) and `remote_etag`; a hosted file keeps 0 and `''`. Triggers keep the repo counters in the transaction of each write: an insert adds 1 and its size, a replace adds the size change, a delete subtracts. Every asset row counts (artifacts, metadata, checksums and Debian index files). `last_published_at` is the newest `updated_at` of an insert or replace that is not an `index` row; a delete never lowers it |
| `assets_fts` | FTS5 trigram on `path` | Kept in sync by triggers; `index` rows are not indexed |
| `repo_ops` | PK `id`, UNIQUE `repo_id` (cascades), idx `started_by`, partial idx `repo_ops_running (type) WHERE state = 'running'` | At most one operation per repo; an ended operation's row is deleted. `type` (a registered type: `delete`, `mirror`), `owner` (the module), `ref` (the owner's reference, e.g. the Nexus source ID), `state` `running`/`held`/`failed`, `done`, `total`, `error`, `started_by`, `started_at`, `updated_at` |
| `upload_sessions` | PK `id` (WITHOUT ROWID), idx `repo_id`, `user_id`, `expires_at` | A resumable upload: `id` (32 lowercase hex), `repo_id` and `user_id` cascade, `publish_session_id` (the session that started it; no foreign key: the table belongs to the publish module), `received`, `created_at`, `updated_at`, `expires_at`. The bytes are in `$TR_DATA_DIR/tmp/uploads/<id>` |
| `rebuilds` | PK `(repo_id, grp, unit)` (WITHOUT ROWID); `repo_id` cascades | The durable rebuild queue: a unit of a group of a repo that its format builds again (Debian: `grp` is the distribution, `unit` is `component/arch`). `marked_at` in unix ns |
| `settings` | PK `key` (WITHOUT ROWID), idx `updated_by` | JSON value, or base64(nonce‖AES-GCM ciphertext) when the setting's definition is secret (`settings.Def.Secret`); `updated_by` |
| `user_totp` | PK `id`, idx `user_id` | `secret` is sealed (AAD `user_totp:<id>`); `last_step` is the last accepted 30 s step of this factor (0 for none) |
| `user_passkeys` | PK `id`, UNIQUE `credential_id`, idx `user_id` | `credential` is the JSON of the `webauthn.Credential` (the public key, the sign count, the flags, the transports, the AAGUID); `credential_id` is its lookup key; `name`, `created_at`, `last_used_at` |
| `user_recovery_codes` | PK `(user_id, code_hash)` (WITHOUT ROWID) | `code_hash` is argon2id PHC; `used_at` is set when the code is used |
| `auth_challenges` | PK `id_hash` (WITHOUT ROWID), idx `expires_at`, `user_id` | Short-lived single-use state: `kind` is `login`, `totp_setup`, `webauthn_register`, `webauthn_assert` or `device_grant` (an SSO approval grant: `user_id` set, `data` is the user code, 5 min); `data` holds the payload. A new challenge deletes up to 100 expired ones |
| `audit_events` | PK `id`, idx `at`, idx `(repo_id, id)`, idx `(action, id)`, idx `actor_user_id` | `at` unix time; `action` is a registered action ID; `actor` is a username, a CI subject, `cli`, the module ID of a job, `anonymous`, or the attempted username of a failed sign-in; `actor_user_id` and `repo_id` are SET NULL; `target`; `detail` is JSON and never holds a secret value; `ip` is the client address. This table is the only place that stores or shows an IP address. See **Audit log** |

Tables of the `storage` component (`internal/move/schema.sql`):

| Table | Key / indexes | Notes |
|---|---|---|
| `storage_moves` | PK `id`, UNIQUE partial `storage_moves_copying (state) WHERE state = 'copying'`, idx `started_by` | `target` is the sealed JSON of the target storage settings (AAD `storage_moves:<id>`); `state` `copying`/`done`/`failed`/`cancelled`; `cursor` (last blob id done), `total` (the blob rows when the move started), `copied`, `skipped`, `failed`, `error` |
| `storage_move_failures` | PK `(move_id, blob_id)` (WITHOUT ROWID) | `key` and `reason` of each blob the copier could not copy |

Tables of the `network` component (`internal/netrules/schema.sql`):

| Table | Key / indexes | Notes |
|---|---|---|
| `ip_lists` | PK `id`, UNIQUE `name` | An IP list. `kind` is `preset` (the server fetches it) or `custom` (an admin edits it). `title`, `description`, `ranges` (JSON array of CIDRs), `source` (the fetch URLs of a preset), `fetched_at` (unix time), `error` (the last fetch failure, `''` after a success), `created_at` |
| `repo_ip_rules` | PK `id`, UNIQUE partial `(repo_id, scope, cidr) WHERE cidr IS NOT NULL`, UNIQUE partial `(repo_id, scope, list_id) WHERE list_id IS NOT NULL`, idx `(repo_id, scope)`, idx `list_id` | One allow-list entry. `scope` is `read` or `publish`; a rule holds a `cidr` or the `list_id` of an IP list (exactly one; `ON DELETE RESTRICT`); `note` is free text. `repo_id` cascades. A scope without rows is unrestricted |
| `trusted_proxy_lists` | PK `list_id` (`ON DELETE RESTRICT`) | The IP lists that the trusted proxies include. The `list:<name>` entries of `TR_TRUSTED_PROXIES` are resolved at startup and are not rows |

Tables of the `signing` component (`internal/signing/schema.sql`):

| Table | Key / indexes | Notes |
|---|---|---|
| `signing_keys` | PK `id`, UNIQUE `name` | An OpenPGP signing key: `fingerprint`, armored `public_key`, `private_key` (armored, no passphrase, sealed with the master key, AAD `signing_keys:<id>`), `created_at` |
| `repo_signing_keys` | PK `repo_id` (cascades), idx `(key_id, repo_id)` | The key of a repo (the repo field `signing_key`). `key_id` is `ON DELETE RESTRICT`, so a key in use cannot be deleted |

Tables of the `publish` component (`internal/publish/schema.sql`):

| Table | Key / indexes | Notes |
|---|---|---|
| `devices` | PK `id`, UNIQUE `public_key`, idx `user_id` | `client` is a registered publisher client ID (no CHECK; see **Publisher clients**); `public_key` is the raw Ed25519 key; `expires_at`, `last_used_at`, `revoked_at` |
| `device_enrollments` | PK `device_code_hash` (WITHOUT ROWID), UNIQUE `user_code`, idx `expires_at`, idx `approved_by`, idx `device_id` | `client`; `polled_at` rate-limits the poll; `approved_by` and `device_id` are set on approval; a row is taken once by the poll |
| `publish_trusts` | PK `id`, UNIQUE `name`, partial idx `publish_trusts_issuer (issuer) WHERE disabled = 0`, idx `created_by` | `claims` is JSON `{claim: [allowed values]}`; `jwks` is `''` when the server fetches the issuer's keys; `audience` `''` means `base_url` |
| `publish_trust_repos` | PK `(trust_id, repo_id)` (WITHOUT ROWID), idx `repo_id` | The repos a policy may publish to |
| `publish_nonces` | PK `id_hash` (WITHOUT ROWID), idx `device_id`, idx `expires_at` | A nonce that a device signs: `id_hash` = SHA-256(nonce), `device_id` (cascades), `exchange`. With `exchange` 0 it is for one repo, lives 60 s and the mint deletes it. With `exchange` 1 it is for a format's token exchange, signed over the repo `*`, and mints until it expires (`publish.session_ttl`) |
| `publish_exchange_sessions` | PK `(nonce_hash, repo_id)` (WITHOUT ROWID), idx `repo_id`, `session_id` | The session that a token exchange nonce minted for each repo. Each column cascades |
| `publish_sessions` | PK `id`, UNIQUE `public_id`, idx `expires_at`, `repo_id`, `user_id`, `device_id`, `trust_id`, `build_id` | `secret_hash` = SHA-256(secret); `via` `device`/`oidc`/`manual`; `subject`; `build_id` (the CI build record of a CI session whose token a provider read); `closed_at`. `repo_id` cascades; `user_id`, `device_id`, `trust_id` and `build_id` are SET NULL |
| `publish_oidc_seen` | PK `(id_hash, repo_id)` (WITHOUT ROWID), idx `expires_at`, `session_id`, `repo_id` | One row per CI token and repo, with the session the token minted there. `id_hash` is SHA-256 of `<issuer>\n<jti>`, or of the whole token when it has no `jti`. A second insert for the repo means a replay |

Tables of the `nexus` component (`internal/nexus/schema.sql`):

| Table | Key / indexes | Notes |
|---|---|---|
| `nexus_sources` | PK `id`, UNIQUE `name`, idx `created_by`, partial idx `nexus_sources_due (last_sync_at) WHERE state = 'mirroring'` | `base_url`; `credentials` is the sealed JSON `{username, password}` (AAD `nexus_sources:<id>`), emptied by cut-over; `state` `mirroring`/`cut_over`; `last_sync_at` (unix time when the last sync ended, 0 for never); `last_error`; `cutover_sync_id` and `cutover_failed` (the cut-over sync and its failed file count, 0 before cut-over) |
| `nexus_source_repos` | PK `(source_id, source_repo)` (WITHOUT ROWID), idx `target_repo_id` | The mapping: `source_format` (Nexus format id), `kind` (`hosted`/`proxy`/`group`, the Nexus repo type) and the target repo of each Nexus repo. A target mirrors at most one source |
| `nexus_syncs` | PK `id`, UNIQUE partial `nexus_syncs_running (source_id) WHERE state = 'running'`, idx `source_id`, idx `started_by` | `state` `running`/`done`/`failed`/`cancelled`; `trigger` `save`/`manual`/`schedule`/`cutover`; `started_by` is NULL for the scheduler; `started_at`, `finished_at`, `error` |
| `nexus_sync_repos` | PK `(sync_id, source_repo)` (WITHOUT ROWID), idx `target_repo_id` | Copy of the hosted part of the mapping at sync start, with `continuation` (token of the next Nexus page), `checked` (files read from Nexus), `imported`/`skipped`/`ignored`/`failed`, `bytes` (bytes imported), `active_at` (when the runner saved the last page) and `state` `pending`/`running`/`done`/`failed`/`cancelled` |
| `nexus_sync_failures` | PK `id`, UNIQUE `(sync_id, source_repo, path)`, idx `sync_id` | One row per source file that a sync did not import |
| `nexus_sync_log` | PK `id`, idx `(sync_id, id)` | The log of a sync for the admin: `at`, `source_repo` (`''` for a line about the whole sync), `level` `info`/`warn`/`error`, `message` |

Tables of the `debian` component (`internal/debian/schema.sql`):

| Table | Key / indexes | Notes |
|---|---|---|
| `deb_packages` | UNIQUE `(repo_id, distribution, component, architecture, package, version)`, idx `asset_id`, idx `deb_packages_sha256 (repo_id, distribution, component, sha256)`, idx `deb_packages_package (repo_id, component, package)` | Also the streaming index for `Packages`. The SHA-256 index finds a package an import already holds. The package index reads the versions and builds of a package. `asset_id` cascades with the pool file |
| `deb_index_files` | PK `(repo_id, distribution, path)` (WITHOUT ROWID) | Sizes and hashes; `Release` is built from these rows |

Every query on a request path uses an index. Lists use keyset pagination (`?after=` and
`?limit=`), never `OFFSET` or `LIKE '%x%'`.

**Queries.** The kernel and each module with a schema keep their SQL in `queries/*.sql` next to a
`sqlc.yaml` (a module's config reads the kernel schema and its own `schema.sql`). `go tool task
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
`HasUsers` and `ListRebuildGroups` (the startup walk of the rebuild queue); `storage` allows
`CountBlobs` (the total of a storage move, once per move); `network` allows
`ListTrustedProxyLists` and `DeleteTrustedProxyLists` (one row per IP list at most).
`TestQueryPlans` runs the guard through the hidden `dbcheck` command.

## Storage layout

| Location | Contents |
|---|---|
| `$TR_DATA_DIR/telesphorepo.db` | SQLite database (WAL) |
| `$TR_DATA_DIR/master.key` | Master key (base64, mode 0600). Generated on first start unless `TR_MASTER_KEY` is set |
| `$TR_DATA_DIR/tmp/` | Upload spool, kept on the data volume rather than tmpfs |
| `$TR_DATA_DIR/tmp/uploads/<id>` | The bytes of a resumable upload |
| `$TR_DATA_DIR/blobs/` | FS blob root (default for the `fs.root` setting) |

Blob keys are the same on every backend: `blobs/sha256/<first 2 hex>/<sha256 hex>`, built with
package `path`. A blob holds the content of any number of files in any repos; the asset rows hold
the paths and the content types. S3 keys get the optional `s3.prefix` in front. The FS backend
writes a temp file and renames it into place.
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
(`settings.Def.Dev`). `telesphorepo dev` uses it instead of `Default`: `cookie.secure=false` and
`index.debounce=300ms` in the core.

Setting types are `string`, `bool`, `size`, `duration`, `list` and `map`. A `size` is stored as an
integer number of bytes. Input (API PATCH, env) is a number with an optional unit: `B`, `KiB`,
`MiB`, `GiB`, `TiB` (binary) or `KB`, `MB`, `GB`, `TB` (decimal), case-insensitive, with optional
space and decimals (`512MiB`, `1.5 GiB`, `500MB`). A bare number is bytes. A `duration` is stored
in Go syntax (`15m`, `2160h`). Input also accepts `15 minutes`, `90 days`, `365d`, `1h30m`; the
units are `ns`, `us`, `ms`, `s`, `m`, `h`, `d` and `w`, in short or long form. A `map` is a JSON
object of string to a list of strings (in the environment too). `GET /api/settings` gives each
size or duration item `display` (`2 GiB`, `90 days`, `1 hour 30 minutes`) and `default_display`. A
size item shows the largest binary unit that gives a whole number, else one decimal.

**Groups.** Each module owns its settings groups and registers them with `Extender.Settings`. A
`settings.Group` has an `ID`, a `Label`, the server permission `Perm` that reads and edits every
key of it, the UI `Page` that edits it (`""` shows it on Settings › General), its definitions
(`settings.Def`: key, label, help, type, default, `Dev`, `Secret`, options, `Normalize`,
`Validate`) and three owner hooks:

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
| general (core) | `settings.general` · General | `base_url`, `log.level`, `max_upload_size` (`2GiB`), `max_json_size` (`1MiB`), `cookie.secure`, `session_ttl` (`168h`), `index.debounce` (`2s`), `index.max_delay` (`30s`), `blobs.gc_grace` (`1h`), `upload.session_ttl` (`24h`) |
| activity (audit) | `settings.general` · General | `audit.retention` (`8760h`, which is 365 days) |
| storage (storage) | `storage.manage` · `/settings/storage` | `storage` (`fs`/`s3`), `fs.root` (default `blobs/` in the data dir), `s3.endpoint`, `s3.region`, `s3.bucket`, `s3.access_key_id`, `s3.secret_access_key`*, `s3.prefix`, `s3.path_style`, `s3.redirect`, `presign_ttl` |
| network (network) | `network.manage` · `/settings/proxy` | `trusted_proxies` (literal entries; the IP lists of the trusted proxies are rows, see **Network rules**) |
| publishing (publish) | `settings.general` · General | `publish.session_ttl` (`15m`, maximum `1h`), `publish.device_ttl` (`2160h`, which is 90 days) |
| sso (sso) | `signin.manage` · `/settings/sign-in` | `oidc.issuer`, `oidc.client_id`, `oidc.client_secret`*, `oidc.redirect_url`, `oidc.scopes` (`openid profile email`), `oidc.session_ttl` (`12h`), `oidc.permission_claim` (`""`; dotted paths allowed), `oidc.permission_map` (type `map`, `{}`), `signin.password_login` (`all` or `publishers`, default `all`) |
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
| `PUT, POST /debian/{repo}/upload/{dist}/{component}` | write | Raw `.deb` body. 201 `{package,version,architecture,filename,sha256}`, 200 identical, 409 conflict |
| `GET, HEAD /debian/{repo}/{path...}` | read | `dists/...` and `pool/...` through the read path |
| `GET, HEAD, PUT /raw/{repo}/{path...}` | read / write | PUT: 201 new path; 200 identical bytes (any policy) or a replace when `overwrite` is `allow`; 409 different bytes when `overwrite` is `deny` (default); 413 over `max_upload_size`. Repo config: `{"overwrite":"deny"}` or `{"overwrite":"allow"}`. S3 redirect as for Maven |
| `GET, HEAD /debian/{repo}/key.asc` | read | The armored public signing key of the repo (its repo field `signing_key`). Same read gate as the repo's other files |
| `GET /setup.sh`, `GET /setup.ps1` | none | The project setup script (see **Project setup**), `text/plain`. The publish module mounts them. 400 when the server address holds a quote, a space, a backslash or a control character |
| `/` | none | Embedded UI, hash-routed |

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
after the permission check. A mirrored repo is read only until cut-over. A write without an open publish
session for that repo gets 401 with a Basic challenge and
`{"error":"publishing needs a publish session: <base_url>/#/settings/tokens"}`. A device or manual
session writes only while its user still holds `repo.publish` on the repo (else 403).

A 401 carries `WWW-Authenticate` only on a format route, so package clients get their prompt. It
never carries it for a path under `/api`, nor for a request with the header `X-Telesphorepo-UI: 1`
(the web UI sends it on every request). So a browser never opens its password dialog for the UI.

JSON API (`/api/`). Auth is a session cookie or HTTP Basic; each route's policy says which (see
**Route policy**). Every route that changes state (POST, PUT, PATCH, DELETE) and every permission
route (GET too) needs the UI session cookie, and the router enforces it at startup. HTTP
Basic with a password or a read token reaches only the GET routes that need no permission;
elsewhere it gets 401 `{"error":"sign in through the web UI for this action"}`, as does a request
with no credential. The routes that take no credential or their own token are the exceptions
(sign-in, `/api/auth/config`, `/api/formats`, and the publish routes marked "none" or "token" in
**Publishing**). A signed-in user without the permission gets 403. A token of a registered kind (a read token
`trp_...` or a publish session token `trs_...`) is read from the Basic password or from
`Authorization: Bearer <token>`. A publish session token reaches only the routes whose policy
accepts it: `DELETE /api/publish/sessions/{id}`, `POST /api/repos/{name}/upload`,
`POST /api/repos/{name}/upload/plan`, the resumable upload routes `/api/repos/{name}/uploads...` and
`POST /api/repos/{name}/files/delete` and `.../files/delete/plan`, and only for its own repo. Every other `/api` route answers it with 403. Errors are `{"error": "..."}`, plus
`fields` for settings validation. Bodies are capped at `max_json_size`. Lists are
`{items, next}` with `?after=&limit=`.

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
| `POST /api/auth/login` · `POST /api/auth/logout` | none · session | Login `{username, password}`. A person who needs the second factor (see **Permissions**) and has one gets `{second_factor: true, methods: [...]}` and the cookie `tr_login`; one without a factor gets `{session: <session view>}` with `second_factor_required: true` and a setup-only session. Anyone else gets `{session: <session view>}` and a full session after the password alone (see **Auth model**). 401 for SSO users; 403 `password sign-in is off for people who manage; sign in with single sign-on` for a person who needs the second factor when `signin.password_login=publishers`. Logout ends the local session only |
| `POST /api/auth/login/passkey` | `tr_login` cookie | Returns `{publicKey: {...}}`, the WebAuthn assertion options for the pending sign-in |
| `POST /api/auth/login/second` | `tr_login` cookie | `{totp_code}`, `{passkey: <assertion>}` or `{recovery_code}`. 200 sets `tr_session` and returns `{session: <session view>}` with `second_factor_required: false`. 401 for a wrong proof (the sign-in stays pending) or an expired sign-in; 429 with `Retry-After` after too many failures |
| `GET /api/security` | UI session | `{totp: [{id, name, created_at}], passkeys: [{id, name, created_at, last_used_at}], recovery_codes: <unused count>, second_factor: bool}` |
| `POST /api/security/totp/begin` · `.../totp/confirm` · `DELETE .../totp/{id}` | UI session | `{name}` returns `{setup_id, secret, otpauth_url}` (the secret is shown once; the setup lasts 10 min). `{setup_id, code}` returns 201 `{id, name, created_at}`. Delete is 204 |
| `POST /api/security/passkeys/begin` · `.../passkeys/finish` · `.../passkeys/assert` · `DELETE .../passkeys/{id}` | UI session | `begin` returns `{publicKey}` creation options. `finish` `{name, credential}` returns 201 `{id, name, created_at, last_used_at}`. `assert` returns `{publicKey}` options for a fresh check before an approval or a manual session. 409 when `base_url` is empty |
| `POST /api/security/recovery-codes` | UI session | 201 `{codes: [10 x "abcd-efgh-ijkl-mnop"]}`; replaces the old set. Shown once |
| `GET /api/auth/config` | none | `{oidc, password_login: "all"\|"publishers", second_factor_optional}`. `second_factor_optional` is true only under `telesphorepo dev` |
| `GET /api/auth/oidc/login` | none | 404 when SSO is off, 503 when discovery fails. Stores state/nonce/PKCE verifier in memory (10 min, single use, keyed by SHA-256(state)), sets `tr_oidc` (HttpOnly, SameSite=Lax, Path `/api/auth/oidc`, 600 s), 302 to the IdP |
| `GET /api/auth/oidc/device?code=` | UI session of an SSO account | 404 when SSO is off. Starts a new sign-in that approves the device of `code` (see **Publishing**). It stores the state like `login`, plus the user id and the user code, and sends `prompt=login` and `max_age=0`. 302 `/#/device?code=` without an error when there is no UI session or the code is unknown, expired or used (the page says why), and with `error=not_sso` for a local account or `sso_failed` when discovery fails |
| `GET /api/auth/oidc/callback` | none | 400 for missing/mismatched/unknown/reused/expired state, state from before an issuer or client change, or missing `code`; 503 on discovery failure. Verifies the ID token (signature, issuer, audience, expiry, nonce). Success: upsert user, set its server permissions when `oidc.permission_claim` is set, session with `oidc.session_ttl`, 302 `/`. Failure: 302 `/#/login?error=` `username_conflict` \| `invalid_username` \| `account_disabled` \| `sso_failed`. For a sign-in that `device` started, it starts no session and checks the same token, then that the token's issuer and `sub` belong to the account that started it, and that `auth_time` is present and at most 5 minutes old. Success: 302 `/#/device?code=<code>&grant=<grant>`. Failure: 302 `/#/device?code=<code>&error=` `wrong_account` \| `no_auth_time` \| `stale_sign_in` (the provider ignored `prompt=login`) \| `account_disabled` \| `sso_failed` |
| `GET /api/me` | password, read token, UI session (setup-only too) | The session view, the same shape as `session` of a finished sign-in: `{id, username, kind, email, disabled, created_at, permissions, permissions_managed, all_repos, repo_permissions, second_factor_required}`. User JSON elsewhere is `{id, username, permissions, permissions_managed, disabled, kind, email, created_at}`: `permissions` lists the server permissions, `kind` is `local`/`oidc`, `email` is `""` when unset. `repo_permissions: {repo: [perms]}` is always present: every repo where the person has a grant, `repo.download` included (`{}` for none, and for a holder of `repositories.all`, whose `all_repos` is true). `second_factor_required` is true only for a setup-only UI session. `permissions_managed` is true for an SSO account while `oidc.permission_claim` is set: single sign-on sets its server permissions at each sign-in (see **Single sign-on**) |
| `GET /api/tokens` · `POST /api/tokens`, `DELETE /api/tokens/{id}` | any credential · UI session | **Read tokens.** The token is shown once. A token cannot mint tokens. POST is 403 for SSO users |
| `GET/POST /api/users`, `GET/PATCH /api/users/{username}` | `people.manage` (a UI session may change its own password) | The user list is paginated. `GET /api/users/{username}` returns one account (User JSON), 404 when no account has the name. POST takes `{username, password, permissions}`; the caller gives only server permissions they hold (403). PATCH takes `{password}`; the caller changes the password of another person only when they hold every permission of that person (403) |
| `PUT /api/users/{username}/permissions` | `people.manage` | `{permissions: [...]}` (server permission IDs) replaces the server permissions and returns the User. 400 for an unknown ID, a repository permission or a broken need. The caller adds or removes only permissions they hold (403). 409 when no enabled person would keep `people.manage`, and 409 `single sign-on manages the permissions of this person` for an account whose permissions single sign-on manages. One `user.permissions` event with `{old, new}` |
| `GET /api/people?q=&after=&limit=` | `people.manage`, or `repo.access` on a repo | `{items: [{username}], next}`: the enabled usernames that start with `q`, at most 20 per page. The UI draws the avatar from the username |
| `PATCH /api/users` | `people.manage` | `{usernames: [...], disabled: bool}`. The one way to disable or enable people. 400 `You cannot disable your own account.` when the list holds the caller and `disabled` is true. 403 for a person whose permissions the caller does not all hold. 409 when no enabled person would keep `people.manage`. A disabled person's sessions end, and their password and read tokens stop working. One `user.update` event per person whose value changed. 200 `{changed: n}` |
| `GET /api/permissions` | none | The permission registry: `{subsystems: [{id, scope: "server"|"repo", label, help, module, permissions: [{id, label, help, needs, second_factor, all_repos}]}]}`, subsystems in module start order and permissions in registration order. See **Permissions** |
| `GET /api/formats` | none | The UI descriptor of every format, in module order (see **Format UI descriptors**): `[{name, title, description, icon?, kind_icons?, kinds, config: {default, fields}, repo_fields, repo_tabs, upload, delete_notes, file_details, browse: {versions, artifact_snippets}, remote_presets, proxy_note?, public_key?, labels: {artifact_kinds, artifact_name, tools, install_heading?, build_files}, import_sources, tools: [{id, label, setup, project?}], setup}]` |
| `GET /api/repos`, `GET /api/repos/{name}` · `POST /api/repos` · `PATCH /api/repos/{name}` | any/anon · `repositories.all` · `repo.settings` | The list shows the repos where the caller holds a permission, and the public ones. Repo JSON has `permissions`, the caller's repository permissions on the repo. Repo JSON has `file_count`, `total_size` and `last_published_at` (null before the first publish), read from the repo row (see `assets` in **Data model**). `format` must be a registered name. `description` (POST and PATCH) has at most 500 characters and no control characters except newline and tab. `config` must be a JSON object that the format validates (400 otherwise; an unknown format lists the available ones). An empty config becomes the format default. Repo JSON has `operation`: `{id, type, owner, state, done, total, message, error}` of the repo's active operation, or null (see **Repo operations**). Repo JSON has `fields`: the values of the module fields of the repo (e.g. `{signing_key: "default"}`; `{}` for a format without fields). POST and PATCH take `fields` too; POST gives a field without a value its default, and a field that does not apply to the format and kind is 400. `PATCH /api/repos/{name}` takes `{description?, config?, proxy?, fields?}` and is one repo update (`repos.Service.Update`): the whole new state is checked first, then saved in one transaction with the field values, the format's `OnRepoChange` and one `repo.update` event with the changed fields. Repo JSON has `kind`, and a group has `member_count`. POST takes `kind` (`hosted` default, `proxy`, `group`), `members: [names]` for a group, `proxy: {remote_url, username?, password?, metadata_ttl?, not_found_ttl?}` (seconds) for a proxy, and `force`. A new proxy's remote is checked first (see **Group and proxy repositories**): 502 `{error}` when it does not answer, unless `force: true`. PATCH takes `proxy` with the same fields for a proxy; `username` and `password` together replace the credentials, `username: ""` removes them, and the password is never returned. `GET /api/repos/{name}` adds, for a group, `members: [{name, format, kind, public}]` (the members the caller can read, in order); for a hosted or proxy repo, `groups: [names]` (the groups it is in that the caller can read); for a proxy and a holder of `repo.settings`, `proxy: {remote_url, has_credentials, metadata_ttl, not_found_ttl, last_ok_at\|null, last_error, last_error_at\|null}` |
| `PUT /api/repos/{name}/members` | `repo.settings` | `{members: [names]}` replaces the members of a group and their order; returns the repo. 400 for 0 or more than 50 members, a member of another format, a group, a name listed twice, an unknown name, or a repo that is not a group |
| `POST /api/repos/{name}/proxy/test` | `repo.settings` | Checks the remote of a proxy now: 200 `{ok: true}` or 502 `{ok: false, error}`. It records the result as the proxy's last answer or last error |
| `PATCH /api/repos` | `repo.settings` on each repo | `{names: [...], anonymous_read: bool}`. The one way to make repos public or private: one repo update of every repo in one transaction. One `repo.update` event per repo whose value changed. 200 `{changed: n}` |
| `POST /api/repos/delete` | `repositories.all` | `{names: [...]}`. Checks that every repo exists (404) and that none has an operation (409 `<name>: <message>`); then nothing is deleted. Then it deletes the repos one after another, each under a `delete` operation (see **Repo operations**). The first failure stops the rest: 500 `{deleted: [names done], error}`, and the failed repo keeps a failed `delete` operation. One `repo.delete` event per repo. 200 `{deleted: [names], files: n}` |
| `POST /api/repos/{name}/operation/resume` · `DELETE /api/repos/{name}/operation` | `repositories.all` | Resume runs the failed operation of the repo again (a delete deletes the repo): 200 `{ok: true}`. Clear ends it, and the repo keeps the files that are left: 204. 409 when the repo has no operation, when it has not failed, or when its type cannot resume or clear here (a Nexus mirror ends at the cut-over). Records `repo.delete.resume` or `repo.delete.clear` |
| `GET /api/repos/{name}/grants` | `repo.access` | `[{username, perms: [...]}]` by username; `perms` are permission IDs in registry order |
| `GET /api/repos/{name}/access?admins=1` | a repository permission that manages the repo (one with a second-factor need: `repo.delete`, `repo.settings`, `repo.access`, `ci.pipelines`) | The access report, built from the parts of `access.Engine.Decide`: `{repo, format, kind, people, download: {public, people, admins, guards, sentence}, publish: {people, admins, ci, guards, blocked, sentence}}`. `people` are `{username, perms, via}` (`permission`; with `admins=1` the holders of `repositories.all` too, via `all_repos`). `guards` maps a guard name to `{restricted, summary, detail}`; the network guard's `detail` is `{any, lists: [{name, title}], cidrs: [{cidr, note}]}`. `blocked` is null, or `{code: "read_only_kind" \| "operation", message}`: a proxy or a group (it lists no publisher and no CI pipeline) or a locked repo. Each `sentence` says the answer in words, e.g. "2 people, only from 10.0.0.0/8." |
| `GET /api/users/{username}/access` · `GET /api/me/access` | `people.manage` · password, read token or UI session | `{username, permissions, disabled, kind, can_publish, items: [{repo, format, public, perms, via}], next}`, a keyset page by repo name. On a proxy or a group a person holds no `repo.publish` and no `ci.pipelines` |
| `PUT /api/grants` | `repo.access` on each repo | `{users: [...], repos: [...], perms: [...]}`. Sets the repository permission set of every pair of user and repo, at most 1000 pairs; `perms: []` removes the pairs. 400 for an unknown permission ID, a server permission, or a permission without one it needs (e.g. `repo.publish` without `repo.download`, `ci.pipelines` without `repo.publish`); 404 for an unknown user or repo. 403 when the caller adds or removes a permission they do not hold on that repo. The auth service records one `grant.set` event with `{old, new}` per pair whose set changed, in the transaction. 200 `{changed: n}` |
| `GET /api/repos/{name}/assets?prefix=&after=&limit=` | `repo.download` | Ordered by path; the prefix is a range scan |
| `GET /api/repos/{name}/tree?path=&after=&limit=` | `repo.download` | `{items: [{name, type: "dir"\|"file", path, size?, sha256?, updated_at?, uploaded_by?, build?}], next}`. `build` is the CI build record that wrote the file (see **CI providers and build records**). `path` is a folder (`""` or ending in `/`). A folder item's `path` and the `next` cursor end in `/`. `uploaded_by` is a username |
| `GET /api/repos/{name}/file?path=` | `repo.download` | One file with its details: `{name, path, size, sha256, content_type, kind, created_at, updated_at, uploaded_by, build}`. 404 when the path holds no file. `build` is the CI build record, or null |
| `POST /api/repos/{name}/files/delete/plan` | as the delete | `{paths?, prefixes?}` as the delete. Deletes nothing. 200 `{files, more, notes: [{text, count, one, many}]}`: the files that exist (a folder walk reads at most 100,000 files; `more` says it stopped), and for each `delete_notes` entry of the format the count of the files its glob matches (entries without a match are left out). It walks `assets` by the `(repo_id, path)` index |
| `POST /api/repos/{name}/files/delete` | `repo.delete`, with a UI session or a device or manual publish session of the repo | `{paths?: [...], prefixes?: [...]}`: files and folders, 1 to 1000 items together. 400 with the first bad path or prefix; then nothing is deleted. 200 `{deleted: n}`, the number of files. A file that is gone already counts as done; an empty folder adds 0. 409 on a repo with an operation. 401 for a password or a read token; 403 without `repo.delete` (a CI session never deletes). One `file.delete` event per file and per folder that lost files |
| `POST /api/repos/{name}/upload/plan` | publish session of the repo | What an upload of files would store; stores nothing. Multipart: the part `plan` first, `{fields, folder, files: [{path, size}]}` (1 to 500 files, paths relative to the drop), then one part per listed file that a `reads` rule of the format matches, its `filename` the path, holding at most the rule's `max_bytes` from the start of the file. 200 `Plan` = `{items: [{title, files: [{path, target}], facts?, problems}], ignored: [paths], fields: [{key, label, help?, type, ..., value, read?, problem?}]}`. 400 for an unknown field, a bad path, a head that no rule reads or that is longer than it allows. See **Files, uploads and deletes** |
| `POST /api/repos/{name}/upload` | publish session of the repo | Multipart: the fields first, then the files, each with its relative path as its `filename`. The server runs the format's `Upload.Plan` on the files (the first bytes of each file that a `reads` rule matches): 400 when a field or an item has a problem or no item takes a file. Then `Upload.Store` commits the files of the items through the byte pipeline (source `ui`; checksums and metadata with source `generated`). 201 `{files: [{path, status: "created"\|"replaced"\|"unchanged", size, sha256, generated?}]}`. 400 or 409 for a refusal of the format |
| `POST /api/repos/{name}/uploads` | publish session of the repo | Starts a resumable upload: 201 `{id, received: 0, expires_at}` |
| `PATCH /api/repos/{name}/uploads/{id}` | the publish session that started it | Appends the body at the offset of `Content-Range: bytes <from>-<to>/*` (without the header: at the end). 202 `{received}`; 416 `{error, received}` when `from` is not the bytes received; 413 above `max_upload_size`; 404 for an unknown, expired or foreign upload. The answers carry `Range: bytes=0-<received-1>` |
| `PUT /api/repos/{name}/uploads/{id}?path=&sha256=&size=` | the publish session that started it | Finishes the upload: the server reads the file once for its hashes, checks `sha256` (and `size` when given), then commits it at `path` with source `put`: the answers of a format PUT (201, 200, the format's refusal). 400 when the bytes do not match; the upload is deleted. A Debian repo refuses every path (400): its packages go through `/debian/{repo}/upload/{dist}/{component}` |
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
| `GET /api/audit?after=&limit=&action=&repo=` | `activity.read` | `{items: [{id, at, actor, actor_user_id\|null, action, repo, target, detail, ip}], next}`, newest first. `repo` is a repo name |
| `GET /api/audit/actions` | `activity.read` | `[{id, label, subsystem}]`: every action that a module registers, in module start order. The Activity filter reads it |
| `GET /api/search?q=&repo=&after=&limit=` | any/anon | FTS5 trigram, `q` ≥ 3 chars, only repos where the caller holds `repo.download` |
| `GET /api/repos/{name}/status` | `repo.download` | `{pending}`: the units of the repo that wait for a rebuild of its indexes (`rebuilds`), the ones being rebuilt included; 0 for a format without a `Rebuild` |
| `GET /api/repos/{name}/usage` | `repo.download` | `{base_url, tools: [{id, label}], snippets: [Snippet]}`: the format's `Usage` (use, a computer and by hand), then for a hosted repo the CI file of each of the format's tools for GitHub Actions and Jenkins (default trigger). A proxy or a group gets no publish snippets. `Snippet` = `{id, tool, purpose: "use"\|"publish", context: "repo"\|"artifact", via?: "computer"\|"ci"\|"hand", file?, title, lang, code, note?}`. `tools` names each tool of the snippets with its label (the format's `SnippetTools`, the CI provider titles). `base_url` is the `base_url` setting, else the request scheme and host |
| `GET /api/repos/{name}/ci?system=github-actions\|jenkins&tool=&branches=&tags=` | `repo.download` | The CI file that publishes to a hosted repo (one `Snippet`), built by `internal/ci/workflow.go` from the `PublishSteps` of the tool (a tool of the format; default the first). `branches` and `tags` are comma-separated names or globs (letters, digits and `. _ / * -`, at most 20 each): the GitHub Actions trigger (default: a push to `main`), and a `when { anyOf { branch; tag } }` on the Jenkins publish stage (none by default). 400 for another system, an unknown tool, a bad pattern, or a proxy or a group |
| `GET /api/repos/{name}/versions?path=&after=&limit=` | `repo.download` | The versions of the artifact folder `path`, newest first (`format.Spec.Versions`): `{artifact: {path, title, kind}\|null, items: [{version, kind, latest_build, latest_label, builds_count, published_at, build}], next}`. `artifact` is null for any other folder. `latest_label` is the format's name of the latest build for people (Maven: "Build 3"); the UI shows `latest_build` when it is absent. See **Versions and builds** |
| `GET /api/repos/{name}/builds?path=&version=&after=&limit=` | `repo.download` | The builds of one version of the artifact folder `path`, newest first: `{artifact, version, items: [{id, label, files: [{name, path, size, sha256, classifier, extension}], published_at, build}], next}`. `label` is the format's name of the build for people (Maven: "Build 3", "Build without a timestamp"); the UI shows `id` when it is absent. `artifact` and `version` are null when the artifact has no such version. 400 without `version` |
| `GET /api/repos/{name}/snippets?path=&version=&version_mode=pinned\|latest` | `repo.download` | The snippets that use one artifact (`format.Spec.ArtifactUsage`): `{base_url, artifact, version, version_mode, tools, snippets}`. `path` is an artifact folder (with an optional `version`), a version folder or a file |
| `GET /api/settings` | the permission of a settings group | `{groups: [{id, label, module, perm, page, settings: [Item]}]}`: the groups whose permission the caller holds, in module start order. An Item has key, env, group, label, help, type, options, secret (from the definition), value, default, display, default_display, source (`stored`/`env`/`default`), locked |
| `PATCH /api/settings` | the permission of each key's group | `{key: value}`. Each key needs the permission of its group (403 otherwise). `null` resets a key; `""` for a secret means unchanged. The server runs the `Validate` of every touched group, then each `Apply`, then saves (see **Settings model**). Returns 400 `{error, fields}`, or 409 when an env-locked key gets a different value. Location changes must pass the probe; SSO connection changes must pass discovery. A location change that starts a storage move saves the other keys of the patch and returns 202 `{move: Move}`; 409 while a move runs. The settings service records one `settings.update` event that names the saved keys, never the values; a patch that saves no key records none |
| `GET /api/storage/moves/current` | `storage.manage` | The newest Move, running or ended; 404 when there has been none |
| `GET /api/storage/moves/{id}` | `storage.manage` | `Move` = `{id, state, total, copied, skipped, failed, error, started_at, finished_at, target: {storage, endpoint?, bucket?, prefix?, fs_root?}}`. Never holds secrets |
| `GET /api/storage/moves/{id}/failures?after=&limit=` | `storage.manage` | `{items: [{asset_id, key, reason}], next}` in asset id order |
| `DELETE /api/storage/moves/{id}` | `storage.manage` | Cancels the move and returns it; 409 when it has ended |
| `POST /api/nexus/sources` | `nexus.manage` | `{name, base_url, username, password}`; `name` matches the repo name pattern. Checks the connection. 201 `Source`. 400 for invalid input or refused credentials, 409 when the name exists, 502 when Nexus is unreachable |
| `GET /api/nexus/sources?after=<name>&limit=` | `nexus.manage` | `{items: [Source], next}` in name order |
| `GET /api/nexus/sources/{id}` | `nexus.manage` | `SourceDetail`. After cut-over, or when Nexus is unreachable, it lists only the mapped repos and `nexus_error` says why |
| `PATCH /api/nexus/sources/{id}` | `nexus.manage` | `{base_url?, username?, password?}`. Checks the connection. Returns the `Source`. 409 after cut-over |
| `PUT /api/nexus/sources/{id}/repos` | `nexus.manage` | `{repos: [Mapping]}` replaces the whole mapping and, when it holds a repo, starts a sync (`trigger: save`). Returns the `SourceDetail` with `running_sync_id`. 400 for an invalid mapping, 409 while a sync runs, after cut-over, or when a target already mirrors another source |
| `POST /api/nexus/sources/{id}/sync` | `nexus.manage` | 202 `Sync` with its `repos` (`trigger: manual`). 400 without mapped repos, 409 while a sync runs or after cut-over |
| `POST /api/nexus/sources/{id}/cutover` | `nexus.manage` | 202 `Sync` with `trigger: cutover`. 409 while a sync runs or after cut-over |
| `GET /api/nexus/sources/{id}/syncs?after=<sync id>&limit=` | `nexus.manage` | `{items: [Sync without repos], next}`, newest first |
| `GET /api/nexus/syncs/{id}` | `nexus.manage` | `Sync` with its `repos` |
| `GET /api/nexus/syncs/{id}/failures?after=&limit=` | `nexus.manage` | `{items: [Failure], next}` in failure order |
| `GET /api/nexus/syncs/{id}/log?after=<line id>&limit=&repo=` | `nexus.manage` | `{items: [LogLine], next}`, oldest first. `repo` keeps the lines of one Nexus repo |
| `DELETE /api/nexus/syncs/{id}` | `nexus.manage` | Cancels the sync and returns it (200); 409 when it has ended |
| `DELETE /api/nexus/sources/{id}` | `nexus.manage` | 204. 409 while a sync runs or while the source mirrors mapped repos (allowed after cut-over, or without a mapping) |
| `POST /api/settings/{group}/test` | the permission of the group | Same body as the PATCH, merged over the current values and not saved. Runs the group's `Validate`, then its `Test` (`storage`: the probe of the store; `sso`: the discovery check). Returns `{ok:true}`, or 400 `{ok:false, error, fields?}`; 404 for an unknown group |

A permission in these tables means the UI session of a person who holds it (see
**Permissions**); `repositories.all` holds every repository permission. "UI session" means the `tr_session`
cookie; the session of a person who needs the second factor exists only after it (outside dev
mode). The publish routes are in **Publishing**.

## Client setup

The UI shows these snippets per repo (`/usage`), and `telesphorepo dev` prints them. `HOST` is the server URL. For reads, credentials are a user and password, or any username with a
read token as the password. Uploads use a publish session token; the Maven and Gradle clients
(see **Publishing**) get it themselves.

No snippet puts a token in a process argument. A curl snippet pipes a curl config
(`user = "publish:$TOKEN"`, `header = ...`) to `curl -K -` with the shell builtin `printf`, and
sends a JSON body that holds a token with `--data @-`. A snippet for a person reads a token at a
hidden prompt (`read -rs TOKEN`), so the token stays out of the shell history; wget uses
`--ask-password`. A GitHub Actions workflow that needs the identity token in `OIDC_TOKEN` masks
it with `::add-mask::` as soon as it has it. The curl publish script of a format without a client
turns tracing off with `set +x` before it holds the session token, never prints it, and closes
the session in an exit trap.
The apt credentials file gets mode 600 (`sudo install -m 600 /dev/null FILE`) before the token
is written to it with `sudo tee -a FILE > /dev/null`.

| Client | Setup |
|---|---|
| Maven | URL `HOST/maven/{repo}` in `<distributionManagement>` / `<repositories>`. Reads of a private repo need `~/.m2/settings.xml` `<server>` with the same `<id>`. Deploys use the Telesphorepo extension. Maven 3.8.1+ blocks `http://` on non-localhost hosts |
| Gradle | `maven { url = uri("HOST/maven/{repo}") }` in `repositories` of the build file to consume dependencies (Gradle ignores the repositories of the settings file when the build file declares its own), in `pluginManagement.repositories` of the settings file to consume plugins, and in `publishing.repositories` to publish; add `credentials(PasswordCredentials::class)` with a read token in `~/.gradle/gradle.properties` to read a private repo. The settings plugin adds the publish credentials. `isAllowInsecureProtocol = true` only for plain http |
| apt | Key: `curl -fsSL HOST/debian/{repo}/key.asc -o /etc/apt/keyrings/telesphorepo.asc`. Source: `deb [signed-by=/etc/apt/keyrings/telesphorepo.asc] HOST/debian/{repo} {dist} {component}`. For a private repo, the `curl` command reads `USER:READ_TOKEN` from a config on stdin, and `/etc/apt/auth.conf.d/{repo}.conf` holds `machine HOST/debian/{repo} login USER password TOKEN` (mode 600) |
| Upload `.deb` | `curl -K - -T x.deb HOST/debian/{repo}/upload/{dist}/{component}` with `user = "publish:SESSION_TOKEN"` on stdin. Indexes rebuild after `index.debounce`; poll `GET /api/repos/{repo}/status` for `pending: 0` |
| Raw | Upload: `curl -K - -T file.tar.gz HOST/raw/{repo}/path/file.tar.gz` with `user = "publish:SESSION_TOKEN"` on stdin. Download: `curl -fLO HOST/raw/{repo}/path/file.tar.gz`; a private repo adds `-K -` with `user = "USER:READ_TOKEN"` on stdin |

### Project setup

One command sets up a project to publish to a repo. The build tools of the formats with a
`format.SetupFragment` (Gradle and Maven) take part. The person runs it once in the project
folder and commits the files it changes:

```sh
curl -fsSL HOST/setup.sh | sh -s -- REPO [--ci none|github|jenkins-pipeline|jenkins-freestyle] [--yes]
```

```powershell
& ([scriptblock]::Create((irm HOST/setup.ps1))) REPO [-Ci none|github|jenkins-pipeline|jenkins-freestyle] [-Yes]
```

`setup.sh` is POSIX sh; `setup.ps1` runs on PowerShell 5.1 and later. The publish module holds
the frame (`internal/publish/setup.sh` and `setup.ps1`): the arguments, the questions, the staged
changes with the confirmation, the CI files and the next steps. Each tool with a setup fragment
adds its part (`internal/maven/setup_gradle.sh`, `setup_maven.sh` and their `.ps1`): the sh
functions `detect_<tool>`, `apply_<tool>`, `publish_cmd_<tool>`, `ci_edit_<tool>` and
`next_<tool>` (PowerShell: `Test-<Tool>`, `Set-<Tool>`, `Get-<Tool>PublishCommand`,
`Edit-<Tool>Ci`, `Write-<Tool>Next`), with the frame's helpers (`stage_file`, `stage_path`,
`stage_insert_after`, `stage_append`, `block_start`, `block_end`, `plan`, `manual`, `note`). The
server fills in `HOST` (the `base_url` setting, else the scheme and host of the request), the
tools in module order, the fragments (with the client version `clientsVersion`), the Jenkins
credential ID, and the CI files of each tool from `internal/ci/workflow.go` for the repo
placeholder `%REPO%`: the GitHub Actions workflow with the trigger `on: push: tags: ["v*"]`, the
Jenkinsfile, and the freestyle shell command (the Jenkins build and publish). The script fills in
the repo and runs each CI file through the tool's `ci_edit` (Gradle: `gradle` when the project
has no `gradlew`).

- **Questions.** When REPO or the CI choice is missing and a terminal is there (`/dev/tty` for
  sh, because stdin is the pipe; the console for PowerShell), the script asks for the repo and
  "Where will you publish from?" (1 Your computer only, 2 GitHub Actions, 3 Jenkins with a
  Jenkinsfile, 4 Jenkins freestyle job). `--ci none` (`-Ci none`) is Your computer only and asks
  no question. Without a terminal, a missing REPO stops the script and a missing CI choice means
  the computer only. A repo name must match `[a-z0-9][a-z0-9._-]*`.
- **Build tool.** The first tool whose detection finds its project: Gradle (`settings.gradle.kts`,
  else `settings.gradle`), then Maven (`pom.xml`). With none, the script stops.
- **Gradle.** The settings file gets the `com.telesphorepo.publish` plugin. With a top-level
  `plugins {` block, the script adds the id line to it. Without one, it adds a `plugins` block
  after the `pluginManagement` and `buildscript` blocks, or at the top after the comments and
  imports. At the end it adds `telesphorepo { url; repository }`, with
  `oidcTokenEnv = "OIDC_TOKEN"` for both Jenkins choices.
- **Maven.** `.mvn/extensions.xml` gets the extension (a new file, or a new `<extension>` before
  `</extensions>`). `.mvn/maven.config` gets `-Dtelesphorepo.url=HOST` unless it has a
  `-Dtelesphorepo.url` line. `pom.xml` gets `<distributionManagement>` with `<repository>` and
  `<snapshotRepository>` before `</project>`, with the indent of `<modelVersion>`, unless it has
  one.
- **CI.** `--ci github` writes `.github/workflows/publish.yml`. `--ci jenkins-pipeline` writes
  `Jenkinsfile`. `--ci jenkins-freestyle` writes no CI file and prints the freestyle job steps:
  Configure; under Environment tick "Use secret text(s) or file(s)"; under Bindings Add, then
  "Secret text", with Variable `OIDC_TOKEN` and Credentials `telesphorepo-id-token`; Add build
  step, then "Execute shell" ("Execute Windows batch command" on Windows agents) with
  `./gradlew build && ./gradlew publish` (or `gradle`) or
  `mvn -B verify && mvn -B deploy -DskipTests -Dtelesphorepo.oidcTokenEnv=OIDC_TOKEN`; Save, then
  Build Now. A CI file that
  exists with other content stays as it is.
- **Safety.** The script finds anchors with plain line matches and brace counts, never a
  rewrite of the whole file. A block that it cannot place safely (a `plugins` block on one line,
  braces that do not close, a second `</project>`) it prints to paste by hand, and it then exits
  1. It shows each change first. A change to a file that exists needs y at the prompt, or
  `--yes`; without a terminal and without `--yes` it stops and changes nothing. Line ends
  (LF or CRLF) and a UTF-8 BOM stay as they are. It never touches `~/.m2` or `~/.gradle` and
  writes no secret. A second run changes nothing.
- **End.** It prints the changed files, "Commit these files", the publish command with "click
  Approve in your browser the first time", and for CI "Ask an admin to add a CI pipeline on
  Settings › CI pipelines": for GitHub Actions the pipeline must allow tags `v*`; for Jenkins it
  adds the credential note ("OpenID Connect id token", ID `telesphorepo-id-token`, the server
  address as audience). For Gradle, when neither the root build file nor `gradle.properties`
  sets `group` and `version`, it asks the person to set them; it never edits the build file.

## Auth model

| Credential | Storage | Accepted on |
|---|---|---|
| Password | argon2id (64 MiB, t=3, p=2), at most 4 hashes at once; successful Basic checks are cached for 10 min under an HMAC key | Sign-in (`POST /api/auth/login`). As Basic auth: format reads, and the `/api/` GET routes that need no permission |
| Read token `trp_<12hex>_<secret>` | SHA-256(secret), looked up by id, constant-time compare | Basic password (any username) or `Authorization: Bearer`: format reads, and the `/api/` GET routes that need no permission |
| Session `tr_session` | SHA-256(id), absolute `session_ttl` | `/api/`, and format reads (GET and HEAD) for downloads in the browser. The only credential for permission routes and for routes that change state on `/api/`; it never writes to a format route. HttpOnly, SameSite=Strict, Secure per `cookie.secure`. The session of a person who holds `delete` may also delete files (see **Files, uploads and deletes**) |
| Publish session `trs_<12hex>_<secret>` | SHA-256(secret) in `publish_sessions`, constant-time compare | Basic password (any username) or `Authorization: Bearer` on format routes of its one repo, `/api/publish/...`, and the upload and delete routes of its repo. The only credential that writes to a format route |

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
  A change that would leave no enabled person with `people.manage` keeps the old permissions and is
  logged. Nobody can change managed permissions on the server (409); the session view and User
  JSON say `permissions_managed: true`, and the UI shows them read-only. Repo grants stay local for
  every account. With no claim, people manage the permissions on the server, and a sign-in never
  changes them.
- **Second factor.** A local user may register several TOTP factors (SHA-1, 6 digits, 30 s, +-1
  step; a step that a factor accepted is refused again), several passkeys (RP ID is the host of
  `base_url`, the one accepted origin is its scheme and host, a clone warning fails the check) and
  10 one-time recovery codes (80 bits each, argon2id-hashed). Recovery codes work only at
  sign-in. Passkeys need `base_url`; without it the routes answer 409. TOTP secrets are sealed
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
  `/api/auth/logout` and `/api/security...`. Every other route answers 403
  `{"error":"second_factor_required"}`. The break-glass `admin` follows the same rule. The
  `/api/security` routes need a UI session, so Basic auth and tokens get 401. A password alone
  cannot add a factor and cannot publish.
- **Dev mode.** `telesphorepo dev` sets `server.Options.Dev`, which calls `auth.SetDevMode`. It is
  an in-process flag: not a setting, not an env var, and not reachable from `serve`. In dev mode
  a person without a factor gets a full session, and a device approval or manual session without a
  factor passes. A factor that exists is still checked. `GET /api/auth/config` reports
  `second_factor_optional: true`, and the dev banner says "2FA not enforced (dev)". The e2e
  suite runs `serve`, so enforcement stays tested.
- **Where each credential works on the JSON API.** A route that changes state (POST, PUT, PATCH,
  DELETE) or a permission route (GET too) needs the UI session. HTTP Basic with a password or a
  read token reaches only the GET routes that need no permission: repo list and repo, tree, assets,
  search, status, usage, `/api/me`, own tokens, devices and publish sessions. Anything else gets
  401 `{"error":"sign in through the web UI for this action"}` without a Basic challenge. So a
  stolen password cannot change users, permissions, settings or trust policies: the session of a
  person who holds a permission for them needs the second factor. The route policies say it, and
  the router refuses at startup a policy that lets a password or a read token change state or reach
  a permission route (see **Route policy**). The exceptions are the routes with no
  credential or their own token: sign-in, `/api/auth/config`, `/api/formats`, device enrollment
  and poll, the publish challenge, the device and OIDC session mints, closing a session with its
  token, and the upload and delete routes with a publish session token. Dev mode does not change
  this rule.
- **Lost factors.** `telesphorepo user reset-2fa NAME` removes the TOTP factors, passkeys and
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
  | `activity` (server, audit) | `activity.read` | Read the audit log: Settings › Activity, the audit log and the publish log | | yes |
  | `storage` (server, storage) | `storage.manage` | Manage storage: Settings › Storage and the storage moves | | yes |
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
| Developer with Gradle or Maven | An enrolled device key (Ed25519) | `POST /api/publish/sessions` |
| CI (GitHub Actions, Jenkins, any OIDC issuer) | The CI identity token and a trust policy (CI pipeline) | `POST /api/publish/sessions/oidc` |
| Anyone, by hand | UI session plus a fresh second factor | `POST /api/publish/sessions/manual` |

Routes (`{items, next}` lists are keyset, `?after=&limit=`):

| Method & path | Auth | Notes |
|---|---|---|
| `POST /api/devices/enroll` | none | `{name, client, public_key}` (`client` is a registered publisher client ID, `gradle` or `maven`; another is 400 with the list) (`public_key` is the raw 32-byte key, base64url, no padding). Returns `{device_code, user_code: "ABCD-EFGH", verification_url: "<base_url>/#/device", expires_in: 600, interval: 5}`. `verification_url` uses the request URL when `base_url` is empty. Each IP gets a burst of 10, then 1 per 30 s (429) |
| `POST /api/devices/enroll/poll` | none | `{device_code}`. 200 `{device_id}` once approved (single use). 400 `{"error": "authorization_pending" \| "slow_down" \| "expired_token"}`. `slow_down` answers a poll less than 4 s after the last. An unknown or used code gets `expired_token` |
| `GET /api/devices/enroll?user_code=` | UI session or password | `{user_code, name, client, client_title, client_help, expires_at}`; 404 for an unknown or expired code |
| `POST /api/devices/approve` | UI session and a fresh factor (`X-Telesphorepo-Proof`) | `{user_code}`. A local account proves with `totp <code>` or `passkey <assertion>`, an SSO account with `grant <grant>` of a fresh SSO sign-in for this code. 201 Device. 401 for a wrong second factor, or for a missing, used, expired or other grant (another user or code), 404 unknown or expired code, 409 used code, 403 `second_factor_required` |
| `GET /api/devices` | password, read token or UI session | Own devices; a holder of `people.manage` sees all devices, with `username`. Newest first, `{items, next}`. A device is `{id, user_id, username?, name, client, client_title, created_at, expires_at, last_used_at, revoked_at, state}` |
| `GET /api/network/trusted-proxies` · `PUT /api/network/trusted-proxies` | `network.manage` | GET answers `{lists: [{id, name}], env_lists: [name]}`: the IP lists that the trusted proxies include (rows of `trusted_proxy_lists`), and those that `TR_TRUSTED_PROXIES` names. PUT takes `{lists: [id]}`, replaces the rows in one transaction, records `network.trusted.update` and answers like GET. 400 for an unknown id |
| `DELETE /api/devices/{id}` | UI session | 204. A holder of `people.manage` may revoke any device. It ends the device's open sessions |
| `POST /api/publish/challenge` | none | `{device_id}` returns `{nonce}` (32 random bytes, base64url, single use, 60 s) |
| `POST /api/publish/sessions` | none | `{device_id, repo, nonce, signature}` returns 201 `{id, token: "trs_...", repo, expires_at}` |
| `POST /api/publish/sessions/manual` | UI session and a fresh factor (`X-Telesphorepo-Proof`) | `{repo}`; same answer. The proof is `totp <code>` or `passkey <assertion>`, not a recovery code. 401 for a wrong proof, 403 `second_factor_required` without a factor, 429 with `Retry-After` when limited. `access.Engine.Decide` with `Mint` decides (405 for a proxy or a group, 409 for a locked repo, 403 for the network rules) |
| `POST /api/publish/sessions/oidc` | none | `{token, repo}`; same answer |
| `GET /api/publish/sessions` | password, read token or UI session | The caller's own sessions |
| `DELETE /api/publish/sessions/{id}` | the session's own token | 204. The token is the Basic password or a Bearer token. Another token, an ended session or none gets 401 |
| `GET /api/publish/log` | `activity.read` | Every session, newest first |
| `GET, POST /api/publish/trusts` · `GET, PATCH, DELETE /api/publish/trusts/{id}` | `ci.pipelines` on a repo | Trust policies (CI pipelines). A holder of `repositories.all` sees and changes every one. Anyone else sees and changes only the policies whose repos all hold `ci.pipelines` for them: the list leaves the others out, and a policy outside that scope answers 404. A new or changed `repos` list outside that scope answers 403. 403 when the caller holds `ci.pipelines` on no repo. A create, a change and a delete record `ci.pipeline.create`, `ci.pipeline.update` and `ci.pipeline.delete`. The list is keyset by name |

Shapes:

```
Device       = {id, user_id, username?, name, client, client_title, created_at, expires_at, last_used_at, revoked_at, state}
SessionItem  = {id, repo, via, subject, username?, device_id, device_name?, trust_id, trust_name?,
                created_at, expires_at, closed_at, state}
Trust input  = {name, issuer, audience, claims: {claim: [value...]}, jwks, repos: [name...], disabled}
Trust output = {id, name, issuer, audience, claims, jwks, repos, created_at, disabled}
```

PATCH changes only the fields that are set. Approve, manual session and the pending lookup
refuse read tokens and publish session tokens. A `trs_` token never reaches the admin routes,
also not for an admin user.

**Device flow** (RFC 8628 style):
1. The client creates an Ed25519 key pair, stores it in `~/.telesphorepo/<server>/device.json` (`<server>` is the host, with `_<port>` when the port is not the default, and `_<base path>` when the server has one)
   (owner only) and calls `enroll`. It prints `Open <verification_url>?code=<USER-CODE> and
   approve code <USER-CODE>`.
2. The user opens the page, signs in, checks the name and the client (its title and one line of
   help from the publisher client registry), and approves. A local account
   approves with a fresh TOTP code or passkey check. An SSO account selects **Sign in again with
   single sign-on to approve**: `GET /api/auth/oidc/device?code=` asks the provider for a new
   sign-in (`prompt=login`, `max_age=0`). The callback checks that the provider signed in the same
   account (issuer and `sub`) and that `auth_time` is at most 5 minutes old; a token without
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

**Publisher clients.** A publisher client (`clients.Client`, `Extender.PublisherClient`) is a build
tool plugin that enrolls a device: `{id, title, help}`. The maven module registers `gradle`
(Gradle plugin) and `maven` (Maven extension). `POST /api/devices/enroll` refuses another `client`
with 400 that lists the IDs; a device and an enrollment keep the ID in `client`.

**Clients** (Java 17, JDK only). They live in their own repository,
[github.com/Telesphorepo/clients](https://github.com/Telesphorepo/clients), under the MIT license.
The group is `com.telesphorepo` and the Gradle plugin id is `com.telesphorepo.publish`. Both
clients have the same version.
- **Gradle plugin** (`gradle/` in the clients repo): a settings plugin.
  `telesphorepo { url = "https://repo.acme.com"; repository = "releases"; oidcTokenEnv = "..." }`
  in `settings.gradle`. With `repository`, each project with `java` or `java-platform` gets
  `maven-publish`, the repository `telesphorepo` at `<url>/maven/<repository>` (unless the build
  file declares a repository on the server) and the publication `maven` of its component (unless
  it has a publication of its own; `java-gradle-plugin` keeps its own). Each
  `PublishToMavenRepository` task whose repo is under `<url>/maven/<name>` gets credentials (user
  `telesphorepo`, the token as password) just before it runs. A session is minted once per repo
  and closed at build end by a build service. Proof order: GitHub
  (`ACTIONS_ID_TOKEN_REQUEST_URL`, audience = the server URL), the env var named by
  `oidcTokenEnv`, the device key. The plugin checks a saved device once per build with
  `POST /api/publish/challenge`. With no device that the server accepts, a build outside CI
  enrolls one inline: it prints the approval link, opens the browser when it can, polls, saves the
  key and continues. In CI (`CI` set, `GITHUB_ACTIONS=true`, or `JENKINS_URL`/`JENKINS_HOME`) the
  build fails with what the job must add (`permissions: id-token: write`, or the Jenkins
  credential `telesphorepo-id-token` bound to `OIDC_TOKEN`). `./gradlew telesphorepoLogin`
  enrolls without a publish and keeps a device that the server accepts. A 404 from the session
  mint says that the repository does not exist. Tasks that publish to the server opt out of the
  configuration cache.
- **Maven** (`maven/` in the clients repo, parent `telesphorepo-maven-parent`): `telesphorepo-maven-client`
  (library), `telesphorepo-maven-extension` (declared in `.mvn/extensions.xml`) and
  `telesphorepo-maven-plugin` (goal `login`, optional). The server URL is the property
  `telesphorepo.url` (or env `TELESPHOREPO_URL`). `telesphorepo.oidcTokenEnv` names the env var
  with a CI token. `telesphorepo.deviceName` names the device. Before a `deploy` to the server, the
  extension checks the proof the same way as the Gradle plugin: outside CI it enrolls inline and
  waits, in CI it stops with what the job must add. The extension mints a session when
  `maven-deploy-plugin` starts and closes it at the end. With `-DdeployAtEnd=true` a session
  older than `publish.session_ttl` at the last module fails.
- **Release.** The clients repo releases from its own workflow (`.github/workflows/release.yml`)
  on a `v*` tag. The server repo has no release job for the clients. The clients repo is
  MIT-licensed; the server is AGPL-3.0.
- **CI without a client.** For a format without a client plugin (Debian, Raw), the tool `curl`
  builds the CI files (see **Snippets and CI files**): a GitHub Actions workflow and a Jenkinsfile
  that post the identity token to `/api/publish/sessions/oidc` with `curl`, upload, and close the
  session. The repo's **Publish to this repository** tab and the CI pipeline page show them.

**UI pages.** The sign-in page (second step for a person who needs it), the approval page (`/#/device?code=`)
and, under Settings: **Sign-in and security** (TOTP with a QR code, passkeys, recovery codes),
**Devices**, **Tokens** (read tokens, and publish tokens with a new manual session), and for
people who hold the permission **CI pipelines** (trust policies, started from GitHub Actions, Jenkins or another CI
system) and **Activity** (the publish log is its second tab). A setup-only session sees only Sign-in and security. The approval page shows when the code expires (10 minutes after the login command starts). A user with no second factor follows its link to
`/#/settings/security?back=/device?code=<code>`; once a method is on, that page shows **Go back**,
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
4. The digest locks; each new blob is written once (a few at a time), and its row is inserted.
5. One transaction: the asset rows, the format's `OnCommitted` (its own rows, rebuild marks), and
   one `file.upload` event per stored file whose source is `put`, `ui` or `import`. A failed
   transaction is tried again up to 3 times.
6. The digest locks, then the path locks are released; then `AfterCommit` runs.

A repo that is being deleted refuses every write with 409. The write runs detached from its
request (a client that goes away does not leave a blob without its row) and stops only when the
server shuts down. The result of each file is `created`, `replaced` or `unchanged`.

Every writer uses it: the format routes, the web UI upload, the resumable uploads, the Nexus
import, the proxy fetch, the Debian upload and index rebuild, the Maven checksums and metadata,
and the dev demo data (source `demo` writes blobs without the store's fsync when the store is the
filesystem). `telesphorepo seed` inserts rows only (`assets.SeedTx`).

**Delete (`Remove`).** A removal is `{repo, paths, prefixes (folders ending in "/"; "" is every
file), origin}`. It works in batches of 500 rows: the path locks in path order, then one
transaction with the format's `OnRemoved` (the rows are still readable; it may return follow-up
paths, removed as the next batches), the row deletes and one `file.delete` event per file of
`paths`; one event per folder of `prefixes` after its last batch. Then `AfterRemove`. A path that
does not exist is skipped. Blob objects are never deleted here: the blob GC removes a blob that no
file uses. While the repo has an active `delete` operation, `Remove` calls no `OnRemoved` and no
`AfterRemove` and records no `file.delete` event, and the rebuild queue skips the repo; the rows of
the format go with the repo row (`ON DELETE CASCADE`).

**Blob GC.** The job `core.blob-gc` waits `blobs.gc_grace` (at least a second, at most 10 minutes),
then deletes every blob with `refs = 0` for longer than the grace. Under its digest lock, which
every write that references the blob also holds, it reads the row again and checks that `refs` is
still 0 since before the cutoff. Then it deletes the object, then the row. When the object delete
fails, the row stays and the next round tries again.

**Read path (`Reader.Serve`).** A hosted repo serves the asset of the path, or 404. A proxy asks
the format's `Classify` (a refused path is 404), fetches the file through `proxy.Service.Ensure`
when the cached copy is missing or stale (see **Group and proxy repositories**), then serves it. A
group tries its members in read order that the caller may read (`access.Engine.Allows` with
`Read`); the first member with the file serves it, and a proxy member fetches only when no member
before it has the file. With a format `Merger` that matches the path and two or more members that
have its base file, the server merges the members' files and serves the merged bytes; a checksum
path (`md5`, `sha1`, `sha256`, `sha512`) of a merged file is the checksum of the merged bytes. No
member: 404, or 502 when a proxy member's remote failed.

One function sets the content headers from the asset row: `Content-Type`, `ETag` (`"<sha256>"`),
`Last-Modified`, `X-Content-Type-Options: nosniff`, `Content-Security-Policy: default-src 'none';
sandbox`, `Content-Disposition: attachment` except for `text/plain`; it answers 304 to a matching
`If-None-Match`, and the stream serves ranges. With S3 and `s3.redirect` on, a GET of a hosted or
proxy file answers 302 to a presigned URL that signs `response-content-type` and
`response-content-disposition` from the asset row, so each path of a shared blob keeps its own
content type; the 302 carries `ETag` and `Cache-Control: no-store`. HEAD, merged files and every
request without `s3.redirect` stream.

**Resumable uploads (`Uploads`).** A publish session starts an upload in a repo, appends chunks at
increasing offsets (`Append` needs the offset to equal the bytes received, else 416 with the bytes
received), and finishes it with the SHA-256 (and optionally the size) of the whole file. The bytes
are in `$TR_DATA_DIR/tmp/uploads/<id>`; the row saves the count after each append, so an upload
continues after a restart; no hash state is kept. `Finish` reads the file once for its MD5, SHA-1 and
SHA-256, deletes the upload on a mismatch (400), and returns the body for a `Commit` with source
`put`. Only the publish session that started an upload continues it. `max_upload_size` caps it, and
the job `core.upload-sessions` deletes uploads idle past `upload.session_ttl` (`24h`) with their files.
A repo delete or a user delete removes the rows of their uploads (`ON DELETE CASCADE`), so the job
also deletes each file in `tmp/uploads` that has no row and was not written for `upload.session_ttl`.

**Repo paths.** `repopath.Parse` checks a file path: valid UTF-8, no control character, no
backslash, no leading `/`, no empty, `.` or `..` segment, at most 1024 bytes and 255 bytes per
segment. `repopath.Folder` takes `""` or a folder and returns it with one trailing `/`. A format
route reads its path with `httpx.RepoPath`, which first refuses an encoded `/`, `\` or NUL and an
encoded `.` or `..` segment in the raw URL. Routes, the file tree, deletes, uploads and imports use
the same rules.

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
a source file, or ignored) and an optional `Store`. `Importer.Run` does the shared work once: the
target path, the skip of a file whose target holds the source's SHA-256 (it gets the source's
dates), the download under `max_upload_size` with the source checksum checked, then `Store` or a
`Commit` with source `import`. An `unchanged` result counts as skipped.

## Repo operations

A repo operation (`repo_ops`, `internal/ops`) is a long task that owns a repo: at most one per repo,
with an ID, a type, its owner module, the owner's reference, a state (`running`, `held`, `failed`),
progress (`done`, `total`) and the error of a failure. Its row is deleted when it ends. While a repo
has an operation, the access engine refuses `Write`, `Delete` and `Mint` with 409 and the message
of the operation type; repo JSON shows it as `operation`. A module registers its types with
`Extender.OpType` (`ID`, `Label`, `Message`, and `Resume` and `Clear` when it has them).

- **`delete`** (core). `POST /api/repos/delete` begins it (`running`) for each repo, removes the
  files with progress, then deletes the repo row (the operation goes with it) and records
  `repo.delete`. A failure sets `failed` with the error and keeps the operation; the job `core.ops`
  continues running deletes at startup. `POST /api/repos/{name}/operation/resume` runs a failed
  delete again; `DELETE /api/repos/{name}/operation` ends it and the repo keeps the files that are
  left. The repo page shows a failed operation with Resume and "Stop and keep the files" to a holder
  of `repositories.all`.
- **`mirror`** (nexus). `held` while a Nexus source mirrors a hosted repo; its reference is the
  source ID and its message is `mirrors Nexus source <name>; cut over first`. Saving the mapping
  begins it, and unmapping or the cut-over ends it. It cannot be resumed or cleared through the repo
  routes.

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
   and `Packages.gz`. One `CommitAll` (source `generated`) writes them with their
   `by-hash/SHA256/*` copies (the same blobs), then `deb_index_files` is updated.
6. **Release.** `Release` is rebuilt from `deb_index_files` (`Acquire-By-Hash: yes`); one
   `CommitAll` writes `Release`, `Release.gpg` and `InRelease`.
7. **Architecture change.** A repo update that changes `architectures` marks, for each
   (distribution, component) with packages, the added architectures (without one, the first
   configured one) and the removed ones. The rebuild builds the added ones and, for a unit whose
   architecture is no longer configured, removes its index files (with their by-hash files) and
   its `deb_index_files` rows; then each `Release` is rebuilt. A change of `signing_key` marks one
   unit of each distribution, so each `Release` is signed again.
8. **By-hash cleanup.** After each `Packages` rebuild, the server keeps the current and the
   previous generation of `by-hash/` files for that index and removes the rest (source
   `generated`).
9. **Delete.** The format's `OnRemoved` deletes the `deb_packages` rows of each removed pool file and
   marks the affected units. During a repo delete the package rows go with their pool files
   (`ON DELETE CASCADE`).

## Storage move

A settings PATCH that points the server at another blob location while blobs exist starts a move
(`internal/move`). The move copies; it changes and deletes nothing on the source.

- **Start.** The PATCH probes the target, saves the non-storage keys of the patch, and stores
  the target storage settings in a `storage_moves` row, encrypted (AAD `storage_moves:<id>`).
  The storage settings do not change yet. One move runs at a time.
- **Mirror.** While the move copies, `Live` holds a `storage.Mirror`. Reads and listings use the
  source. `Put` and `Delete` (new blobs, the blob GC) go to the target first, then to the source;
  an error on either fails the call.
- **Copier.** It walks `blobs` by `id > cursor` in pages of 500 and saves the cursor and counters
  after each page. For each blob it takes its digest lock (so the blob GC waits) and reads the row
  again. It skips a blob that the GC deleted, and one that the target holds with the same size (a
  blob never changes). It checks the source size and SHA-256 before it writes the target. A missing
  source blob or a mismatch is a `storage_move_failures` row.
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
                cutover_sync_id, cutover_failed,   (both 0 before cut-over)
                status: "no_repos"|"syncing"|"up_to_date"|"failed"|"cut_over",
                next_sync_at|null, running_sync_id (0: none), repo_count, sync_interval (seconds)}
SourceDetail = Source + {repos: [SourceRepo], nexus_error}
SourceRepo   = {name, format, type, supported, reason, offline, public, target_format, target,
                remote_url, remote_username,                 (a proxy)
                members, members_known, target_members,      (a group)
                questions: [Question], facts: {...}}         (from the plan of the target format)
Question     = {key, label, help?, type: "choice"|"secret", options?: [{value, label}], required, default?}
Mapping      = {source_repo, target, create, anonymous_read,
                answers?: {key: value},                             (answers to the questions; a new target)
                remote_username?, remote_password?,                 (a new proxy target)
                members?: [Nexus repo names]}                       (a group whose members Nexus did not give)
Sync         = {id, source_id, state: "running"|"done"|"failed"|"cancelled",
                trigger: "save"|"manual"|"schedule"|"cutover", started_by (user name, "" for the scheduler),
                started_at, finished_at|null, error, checked, imported, skipped, ignored, failed, bytes,
                repo_count, repos_done, current_repo, repos?: [SyncRepo]}
SyncRepo     = {source_repo, source_format, target, state: "pending"|"running"|"done"|"failed"|"cancelled",
                error, checked, imported, skipped, ignored, failed, bytes, active_at|null, previous_checked}
Failure      = {source_repo, path, reason}
LogLine      = {id, at, source_repo, level: "info"|"warn"|"error", message}
```

`type` is `""` for a mapped repo that Nexus did not list. `target` is `""` for a repo that is not
mapped. The `Sync` counters are the sums over its repos; `checked` is imported + skipped +
ignored + failed. `previous_checked` is the `checked` of the newest earlier walk of the same repo
that reached the end (0 when there is none); the UI divides by it to estimate the progress,
because Nexus gives no total count. Errors are `{"error"}` with 400 for invalid input or refused
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
    existing key, default `default`; `new`, a new key named after the repo; `source`, the key pair
    of the Nexus repo) and, when the Nexus key is encrypted, `signing_passphrase` (a secret).
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
  is `mirrors Nexus source <name>; cut over first`; a target with another operation cannot be
  mapped (400). A hosted target that leaves the mapping loses the operation and keeps its files.
  Targets are created and changed through `repos.Service.Create` and `Update`. Targets that the request creates stay when its
  transaction fails. After the mapping, the save starts a sync with `trigger: save`.
- **Sync.** A save, `POST .../sync` (`manual`), the scheduler (`schedule`) and the cut-over
  (`cutover`) start a sync. One sync runs per source (unique partial index). The sync copies the
  hosted part of the mapping into `nexus_sync_repos`; proxies and groups have no files to copy.
  At the start the runner lists the Nexus repos once and fails each mapped repo that is offline.
  For each other repo it pages `GET /service/rest/v1/assets?repository=X` from the saved
  continuation token and offers each asset to the importer of the target format. A failed page is retried 3 times (2 s,
  4 s, 8 s), except on 401/403. Downloads use the asset's `downloadUrl` only when it is on the
  base URL's host, else `{base}/repository/{repo}/{path}`. One transaction per page saves the
  failures, the counters (`checked`, `bytes` counts the bytes read for imported files),
  `active_at`, the next token and the page's log line. A file that is already present with the
  same bytes is skipped, so a later sync copies only new and changed files.
- **Log.** The runner writes a line for: the start ("Started after the repositories were
  saved.", "Started by sam.", "Scheduled sync started.", "Final sync before the switch-over
  started by sam."), the proxies it skips, each repo's start or resume ("Started.", "Resumed
  after a restart."), each page that copied a file ("Checked 100, copied 12 (3.4 MiB)."), each
  repo's end ("Done. Copied ..., skipped ..., failed ..." or "No new files. ..."), each retry of
  a page (warn), each failed repo (error), and the end ("Finished in 6 minutes. 34 new files (2.1
  MiB).", "Cancelled by sam.", "Failed: ..."). A line of one repo carries the repo in
  `source_repo`, and its message does not repeat it. A line holds counts, never a path, a URL, a
  password or a token. `GET .../log` reads the lines through `nexus_sync_log_sync`; the `repo`
  filter applies in Go.
- **Retention.** A new sync deletes the syncs of its source after the newest 100, with their
  repos, failures and log lines (cascade). The cut-over sync stays.
- **Scheduler.** It checks every `min(nexus.sync_interval, 1m)`. It reads the interval from the
  live settings. It starts a sync for each mirroring source whose `last_sync_at` is at or before
  now minus the interval, up to 100 per check; a source without a mapping does not start. Every
  sync that ends sets `last_sync_at`.
- **Sync end.** The sync is `done` when every repo finishes, also when some files failed (they
  are in the failure list). It is `failed` when a whole repo failed and `cancelled` after
  `DELETE`. A shutdown leaves the sync `running`; it continues from the last saved page at the
  next start.
- **Cut-over.** `POST .../cutover` runs one final sync with `trigger: cutover`. It succeeds when
  no repo failed, also when some files failed. Then one transaction sets the sync `done`, sets the
  source `cut_over`, empties its credentials, ends the `mirror` operation of the mapped hosted targets and
  saves `cutover_sync_id` (the sync) and `cutover_failed` (its failed file count). The failed
  files stay listed through `GET /api/nexus/syncs/{id}/failures`. If a repo failed (Nexus
  unreachable, a listing error), the sync is `failed`, the source stays `mirroring`, and
  `last_error` says `the switch-over stopped: ...; the source stays mirroring`. A cancelled
  cut-over leaves the source mirroring. A restart continues an interrupted cut-over. After
  cut-over, PATCH, PUT repos, sync and cut-over answer 409, and the sync history stays. Groups and
  proxies need nothing more: they serve from this server already.
- **Delete.** `DELETE .../sources/{id}` removes the source, its mapping, syncs, failures and
  log. The repos and their files stay.
- **UI.** The admin-only **Nexus** pages (`/#/settings/nexus`, `/#/settings/nexus/{id}`). The
  list shows Server, Status, Repositories, Last check and Next check; a syncing source shows its
  progress line. The source page has, top to bottom: the header (status badge and line, Sync now
  or Cancel the sync, Switch over, Delete), Repositories to copy (a pick table with a filter
  above 10 repos, Kind, Name here, Who can download, and the choice question of the target
  format (Debian: Signing key), with its secret questions below the table; one button "Copy N
  repositories" or "Save" saves and opens the sync), Groups (their own pick table), the sync
  detail (per repo "copied (size) of checked", last activity, a bar with "about N%" when an
  earlier walk exists, "No progress for N minutes" after two minutes; the log, which polls every
  2 s and filters by a clicked repo; the failed files), the sync history (Started, How, Took, New
  files, Size, Failed, State) and the connection with the save bar. The repo list and the repo
  page show a "mirrors Nexus" badge while the repo has the `mirror` operation.

## Group and proxy repositories

A format takes proxy repos when its `format.Spec.Kinds` lists `proxy` (Maven, Debian; it then
sets `Classify` and `ProxyProbe`) and group repos when it lists `group` (Maven). `repos.Service` refuses a kind that the format's
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
  failed.
- Maven's `Merger`: an artifact-level (or group-level) `maven-metadata.xml` merges over the
  readable members that have it: the union of the versions in Maven order, the newest `latest`,
  `release` and `lastUpdated`, and the union of the plugins by prefix. Its `.md5`, `.sha1`,
  `.sha256` and `.sha512` are computed from the merged bytes. With one such member, that member's
  own files serve (a checksum file it lacks is computed from its file). A version-level file (under
  a `-SNAPSHOT` folder) comes from the first member that has it.

**Proxies** (`internal/proxy`).

- The remote URL is http or https, has no user, query or fragment, and ends with `/`. A new proxy
  checks its remote once: a GET of the format's probe (`format.Spec.ProxyProbe`: the root for
  Maven, `dists/<distribution>/InRelease` for Debian, with `stable` when the config names no
  `distribution`). The root passes with any answer except 401, 403, 407 and 5xx; a probe file
  must exist. `force: true` skips the check.
- `format.Spec.Classify` sorts a path: Maven `maven-metadata.xml*` and files under a
  `-SNAPSHOT` folder change, the rest is immutable; Debian `pool/**` and `dists/**/by-hash/**` are
  immutable, the rest of `dists/**` changes, other paths are refused (404).
- A read goes through the usual gate. A cached file that is immutable, or changing and checked
  within `metadata_ttl`, serves from storage (with the presigned redirect on S3). Otherwise the
  server fetches `remote_url + path`, with `If-None-Match` (the stored ETag) and
  `If-Modified-Since` for a cached file; a 304 sets `checked_at`. A 401 with a challenge goes to the
  format's `RemoteAuth` once, when it has one, and the request is sent again with the headers it
  returns. One fetch per (repo, path) runs at a time (a fetch lock of its own); a second request
  waits and then serves the stored file.
- A 200 is spooled (`max_upload_size`, hashes) and committed through the byte pipeline with
  source `proxy`: no uploader, the remote's `Last-Modified` as `created_at` and `updated_at`,
  `checked_at` and the ETag; the bytes of a blob that exists already are not stored again. The
  client waits for the whole file.
- A remote 404 or 410 answers 404 and is remembered in memory for `not_found_ttl` (an LRU of
  10,000 paths per server). A cached file that the remote no longer has stays and serves.
- A remote error, a refusal (for example 401) or no answer headers within 30 s: a cached file
  serves although stale, with a warning in the log; with nothing cached the answer is 502 "The
  remote repository did not answer." `last_error` and `last_error_at` record it; a success sets
  `last_ok_at`. A fetch continues when the client goes away, for at most 30 minutes.
- The fetch follows at most 5 redirects. The credentials (HTTP Basic) go only to the remote's
  host: a redirect to another host drops them. No log line holds them.
- A Debian proxy serves the remote's files unchanged: no indexer, no signature of its own.
  Clients keep the remote archive's key. Its apt snippet has no key step and no `signed-by`.
- Deleting files of a proxy (Files tab, admin) works; the next request fetches them again.

## Files, uploads and deletes

`internal/files` serves the file tree; deletes and uploads go through the byte pipeline (see
**Byte pipeline**), and the core module reads manual uploads. The core names no format; it calls
the capabilities of the repo's `format.Spec` that are set.

- **Tree.** `GET /api/repos/{name}/tree` returns the direct children of a folder in path order,
  folders and files mixed (the UI sorts folders first within a page). Each child is one indexed
  seek on `(repo_id, path)`. After a folder, the next seek starts just past every path under it,
  so a page costs `limit` seeks however large the repo is.
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
- **Repo delete.** `POST /api/repos/delete` (`repositories.all`) deletes one repo after another,
  each under a `delete` operation (see **Repo operations**), so writes get 409. It removes the
  files batch by batch, then the repo row. The name can be used again afterwards.
- **`format.Spec.OnRemoved` and `AfterRemove`.** `OnRemoved(ctx, tx, repo, removed)` runs in the
  transaction of each batch, before the rows go; Debian uses it (see **Debian indexing
  pipeline**). Maven's `AfterRemove` writes the artifact's `maven-metadata.xml` again (and the
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
  files is fixed), plans again 400 ms after an edit of a field, and uploads the files of the items
  the person ticks. `POST /api/repos/{name}/upload` runs the same plan on the sent files and stores
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
rules.

**Client IP.** The network module resolves the client address once per request
(`Extender.ClientAddress`), and the server attaches it to the request context. `httpx.ClientIP(r)` reads it. The rate limits, the audit log, the IP rules,
device enrollment and the access log all use it.

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
- **Refusal.** The answer is 403 `{"error":"your address is not allowed to read|publish this
  repository"}` and the audit event `network.denied` with `detail.scope` and the client IP. The IP
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
permission subsystem; `GET /api/audit/actions` lists them, and the Activity filter groups them by
subsystem. `audit.Log.Record` refuses an action that no module registered (it logs and returns
the error). The route middleware puts the caller in the request context (`audit.WithActor`, with
the client address); services record their own events with that actor, inside the transaction of
the change when they have one (`RecordTx`). A job and an `Init` record as the system actor of its
module (`audit.System`), the `telesphorepo user` commands as `cli` (`audit.CLI`).

| Module | Subsystem | Actions |
|---|---|---|
| core | `people` | `signin.success` (`detail.method`), `signin.failed` (the actor is the tried username), `token.create`, `token.delete`, `user.create` (`detail.permissions`), `user.update` (password changed, disabled, second factors reset), `user.permissions` (`detail.old` and `detail.new`: the server permission sets; the actor kind is `sso` when single sign-on set them at a sign-in) |
| core | `repo` | `file.upload`, `file.delete`, `grant.set` (`detail.old` and `detail.new`: the repository permission sets) |
| core | `repositories` | `repo.create`, `repo.update`, `repo.delete`, `repo.delete.resume`, `repo.delete.clear` |
| core | `settings` | `settings.update` (keys only, never values) |
| storage | `storage` | `storage.move.start` (`detail.target` without secrets), `storage.move.cancel`, `storage.move.done`, `storage.move.failed` (`detail.error`) |
| network | `network` | `network.denied` (`detail.scope` is `read` or `publish`), `network.rules.update` (counts of read and publish rules only), `network.list.create`, `network.list.update` (`detail.renamed_from` after a rename), `network.list.delete`, `network.trusted.update` (`detail.lists`: the IP lists of the trusted proxies after a change) |
| signing | `signing` | `signing.key.create` (`detail.imported`), `signing.key.delete`, `signing.key.bind` (a repo got its key: the repo, `target` the key name) |
| publish | `repo` | `publish.session.create` (`detail.via`; the actor is the person or the CI subject of the session), `publish.session.close` |
| publish | `people` | `device.approve`, `device.revoke` |
| publish | `ci` | `ci.pipeline.create`, `ci.pipeline.update`, `ci.pipeline.delete` |
| sso | `signin` | `sso.signin`, `sso.signin.failed` (`detail.reason`) |
| nexus | `nexus` | `nexus.source.create`, `nexus.source.update`, `nexus.source.delete`, `nexus.mapping.save`, `nexus.sync.start` (a manual sync, or the sync after a mapping save), `nexus.sync.cancel`, `nexus.cutover` |

- Client IP addresses appear only in this table and its API. The only other API with an address
  is `GET /api/network/proxy-candidates` (`network.manage`), which lists the addresses of untrusted
  proxies, not of clients. The publish log has none. The access log still records the IP.
- The setting `audit.retention` (default `8760h`) sets how long an event stays. A job deletes
  older events every hour.

## Web UI

Svelte 5 + Vite (no SvelteKit), Tailwind CSS v4, shadcn-svelte with the Maia style, TypeScript
and bun. `bun run build` runs `svelte-check` and `vite build` into `web/dist`, which the binary
embeds. Routing is hash-based (`/#/...`), so the URLs that the server and the clients print keep
working. The UI is only a client of the JSON API and has no tests of its own. `web/index.html` holds one inline script that sets the theme before the first paint; a future Content-Security-Policy needs a hash for it. The page registry
(`web/src/modules`), the folder layout, the components, the design rules and the steps to add a
page are in `web/src/README.md`.

- **UI modules.** Each server module with UI has a `ModuleUI` in `web/src/modules/<id>/module.ts`
  (same ID), and `web/src/modules/index.ts` lists them in the server's start order. A `ModuleUI`
  has `pages` (`PageDef{path, title, layout, access, page, nav?, setup?}`), `redirects`,
  `repoTabs` (a repo settings tab that a format names in `repo_tabs`, e.g. the Signing tab of the
  signing module), `repoFields` (the control of a repo field on New repository, e.g.
  `signing_key`) and `formatParts` (a format's named parts of the file details: `maven-artifact`,
  `deb-package`). The router, the Settings sidebar (`nav`), the breadcrumbs and the pages of a
  setup-only session (`setup`) read the registry. `web/src/lib/api/` is the one API client. The principles are in `docs/design/principles.md`, and the reason for each decision is in `docs/design/rationale.md`.

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
- **Browse** (anyone; visitors see the anonymous-read repos). `/#/` is titled "Repositories", with
  no subtitle. A toolbar holds a filter box, a format filter and **New repository** (`repositories.all`). A
  table shows the format icon, the name (with a lock when the repo is private, and the mark of a
  proxy or a group), files (a group: its member count), size and last publish (`file_count`, `total_size`, `last_published_at`). On a phone the format icon sits
  before the name. A click anywhere on a row opens the repo. Search is `/#/search?q=&repo=`: the
  top-bar field holds the query, and a select picks the repo.
- **Repository page** (`/#/repo/{name}/...`). The header shows the name with a lock for a private
  repo, the format (icon and title) and the description. **Upload** and **Settings** show for users
  who may use them. A group has no Files view: its page shows the Use view and links to each
  member. A proxy shows its cached files and has no Upload and no Publish view. One page loads
  the repo once and switches between these views:
  - `files/<path>`: breadcrumbs and one of the views below, with a details column for the selected
    file or artifact (SHA-256, checksum links, the CI build, a curl and wget download, and the part
    of the format's UI module that the first matching `file_details` entry names: the artifact
    snippets of a pom or a jar, of a `.deb`). Delete shows to a person who holds `repo.delete` and
    acts with the UI session; its confirmation lists the delete plan (the file count and the
    delete notes of the format).
  - `use`: **Use this repository**. An address card first. A public repo then has one card, "Add it
    to your build". A private repo has two numbered steps: create a read token, then add it to the
    build; the snippets already hold the credentials.
  - `publish`: **Publish to this repository**, for users who can publish (others see a lock and
    "You cannot publish to X"). One card each for From your computer, From CI and By hand; the tabs
    are the tools of the snippets, with the labels of the `/usage` answer. A repo whose format has
    a project setup (`setup` of its descriptor: Maven) shows one **Set up your project** card in
    place of From your computer and From CI:
    numbered steps "Where will you publish from?" (Your computer only, GitHub Actions, Jenkins
    with a Jenkinsfile, Jenkins freestyle job), the OS (macOS or Linux, or Windows) and the
    setup command (see **Project setup**) with the repo, the server and the choice (`--ci none`, or `-Ci none`, for Your computer only) filled in and a copy
    button; for a CI choice, Add a CI pipeline; then Publish. The snippets below stay behind one
    **Set it up by hand** disclosure, with the CI snippets of the chosen CI system. The
    notice of a paused repo (a Nexus mirror) shows once, above the tabs. The snippets run in
    order to a first publish: for Gradle the settings file (the `com.telesphorepo.publish` plugin
    at the client version, `url`, `repository` and `oidcTokenEnv = "OIDC_TOKEN"`) and
    `./gradlew publish`; for Maven `.mvn/extensions.xml`, `.mvn/maven.config`, the
    `<distributionManagement>` (`<repository>` and `<snapshotRepository>`) and `mvn deploy`. The
    first publish on a computer asks for the device approval in the browser. The client version
    is one constant, `clientsVersion` in `internal/maven/usage.go`.
    From CI shows a full GitHub Actions workflow (`on: push` to `main`) and a full declarative
    Jenkinsfile. The Jenkinsfile builds in one stage and binds the credential
    `telesphorepo-id-token` to `OIDC_TOKEN` only in the publish stage, because Jenkins makes the
    token when `withCredentials` starts and the server takes a token for 10 minutes only.
    **New CI pipeline** (`ci.pipelines`) opens
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
    `-SNAPSHOT` version has timestamped builds (`20261014.103000-3`); a release has one build.
  - *Debian:* the format reads `deb_packages`, through the index `deb_packages_package`. An artifact
    folder is `pool/<component>/<prefix>/<package>/`; its versions are those of the package of that
    name in that component whose files lie in the folder. `Artifact.Kind` is `package`. A Debian
    version `<base>~SNAPSHOT<sep><stamp>` (`<sep>` one of `.`, `+`, `~`, `-`; `<stamp>` 8 to 14
    digits, `yyyyMMdd[HHmmss]`) is a build of the snapshot version `<base>~SNAPSHOT`; the build id is
    the whole Debian version. Any other version is a release with one build. The files of a build
    are its `.deb` files, one per architecture: `classifier` is the architecture, `extension` is
    `deb`. Versions sort by Debian version order (the `dpkg --compare-versions` rules: epoch,
    upstream, revision, and `~` before anything), so `3.0~SNAPSHOT` sorts before `3.0`. Builds sort
    by stamp, newest first. One request reads at most 20,000 rows of a package.
  - `published_at` is the newest update time of the build's files, and `build` is the CI build
    record of its main file (else of any file); null when none.
  - Snippets of a Debian package: the apt setup for the distribution and component of the package,
    then the install. A release and a build (a `.deb` file) are pinned
    (`apt-get install urn=3.0~SNAPSHOT.20261014211855`); a snapshot version and the latest mode
    install `urn` without a version, so apt takes the newest.
  - A delete of a version removes its folder when all its files lie in the folder named after the
    version (Maven). Else (Debian) it removes the files of all its builds.
- **Settings.** The sidebar is 220 px wide inside the container. Under 768 px it is a menu. All
  paths are under `/#/settings/`.
  - *Your account:* Overview (`account`), Sign-in and security (`security`), Devices (`devices`)
    and Tokens (`tokens`: read tokens and publish tokens, two cards on one page).
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
    proxy (`proxy`), General (`general`) and Activity (`activity?view=&action=&repo=`: the audit log
    and the publish log; the filter lists the actions of `GET /api/audit/actions`, grouped by
    subsystem).
  - Breadcrumbs follow the hierarchy: Settings, the group, the page. A sub-page of the first
    item of a group does not repeat that item.
- **Repository settings.** A page with the tabs Overview, Access, Format settings and Signing
  (Debian). Overview, Format settings, Signing and Remote need `repo.settings`; Access needs a permission
  that manages the repository (`repo.access`, `repo.settings`, `ci.pipelines`, ...). A person sees only the tabs they can open, and a link to a tab they
  cannot open goes to their first tab. The header shows the name with a lock for a private repo.
  **Delete** (`repositories.all`) is an action at the end of the header, on every tab; it opens a dialog that
  asks for the repo name.
  - *Overview:* description and visibility.
  - *Access:* a People part (`repo.access`): each person with a grant once, with the phrase of their
    set ("Download", "Download, publish", "Everything", or the list such as "Download, delete
    files"). **Give access** opens the grant dialog: people found through `GET /api/people`, then
    the permission picker. **Change** opens the picker for one person; the table takes a pick with
    **Remove access** (asks first). These save at once through `PUT /api/grants`. A Download
    part (the Public switch and from where) and a Publish part (CI pipelines and from where)
    follow: the Public switch and the network rules show to a holder of `repo.settings`, the CI
    pipelines to a holder of `ci.pipelines`, with thin dividers between the parts. **Add CI
    pipeline**, **Add network list** and **Add addresses** each open a short form in a dialog. The
    Public switch saves through `PATCH /api/repos`. Add CI pipeline adds existing pipelines, or
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
- **People** (`people.manage`). The table shows each account with its avatar and role: "Administrator"
  for a person with every server permission, else the phrase of the server permissions held, or
  nothing. The sign-in method column shows only when someone uses single sign-on. A row opens the
  person's page (`/#/settings/people/{name}`): an identity card (avatar, role, how the person
  signs in, a Disabled badge) with Reset password (only for a person whose permissions the caller
  all holds) and Disable account or Enable account; then the server permissions in the picker,
  saved from the save bar after a confirm through `PUT /api/users/{name}/permissions`; then the
  person's permissions on each repository. **Give access** opens the grant dialog with the
  repositories and the picker. The repository table takes a pick, with **Change permissions** and
  **Remove access** (asks first). Each repository action saves at once. A holder of `repositories.all`
  reaches every repository, and the page says so. When single sign-on manages the permissions of an
  account, the server permissions show read-only ("Single sign-on manages these permissions."). The
  People table takes a pick of every row except your own: **Disable** asks first ("Disable 4
  people? They are signed out and cannot sign in."), **Enable** acts at once. **New account**
  opens a short form; after Create, the new person's page opens, where the permissions are set.
- **Bulk actions.** Repositories (**Make public**, **Make private**, **Delete**), the List
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
  The workflow or Jenkinsfile comes from `GET /api/repos/{name}/ci` for the chosen repository and
  build tool, with the branches and tags of the pipeline as the trigger, and the workflow file of
  the pipeline as its name. `?repo=<name>` starts with that repository chosen; `?back=<path>`
  (only a `/settings/` or `/repo/` path) is where Create and Cancel return. When the `base_url`
  setting is empty and the pipeline has no audience of its own, the page warns that CI jobs
  cannot publish and links to General.
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
- **Network and proxy page.** It edits the literal entries of `trusted_proxies` (private networks
  and other addresses, a list with remove buttons and the paste box) through the settings PATCH,
  and the IP lists of the trusted proxies (Cloudflare, and any other list) through
  `PUT /api/network/trusted-proxies`. A list that `TR_TRUSTED_PROXIES` names shows checked and
  cannot change. For each
  proxy candidate it shows "Requests from <address> carry forwarded headers. Is this your reverse
  proxy?" and **Trust this address**. A Caddyfile hint takes its host from `base_url`. A setting
  that an environment variable sets shows a badge and cannot change.
- **General page.** One form with a section per settings group whose page is empty (see **Settings
  model**), with the labels and help of the server, for the groups whose permission the person
  holds. **Storage**, **Sign-in options** and **Network and proxy** edit their groups on their own
  pages; each group names its page.
- **Storage page.** The location card and, for an S3 bucket, the bucket card, each with **Test** in
  its footer. A running storage move is a card with progress and **Cancel**.
- **Sign-in options page.** Three numbered cards: connect your provider (with **Test** in its
  footer), permissions from the provider (the claim `oidc.permission_claim`, and the map `oidc.permission_map` as groups: each value of the claim with its server permissions in a permission picker, with Change, Remove and Add a group), and how people sign in (`oidc.session_ttl`,
  `signin.password_login`). The labels are plain words; each
  field also names its term at the provider (Issuer URL, Client ID). The page tests the saved
  settings when it opens and shows the result only when the test fails. **Test** checks the unsaved
  edits and marks the fields that are wrong.
- **Nexus migration.** A table of Nexus servers with their status, repositories, last check and
  next check. Adding a server opens a dialog. A server's page shows its status as a badge and a
  line next to its title, then the repositories to copy and the groups (pick tables; a click on a
  row picks it), the running or chosen sync with its progress and log, the syncs (a row opens its
  details) and the connection. Sync now or Cancel the sync, Switch over and **Delete** are in the
  header; while a sync runs, Delete says "Cancel the sync first".
- **Your account.**
  - *Overview:* the role ("Administrator" or the phrase of the server permissions) by the title. Then a card each for
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
| `debian` | anonymous read | About 300 binary packages from about 100 sources, 1 to 5 versions each, as real `.deb` files for `amd64` and `arm64` or `all`. They go through the format's upload pipeline into `stable/main`, so the indexes and signed `Release` files are valid |
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

Two hidden commands support testing and do not appear in the overview. `dbcheck` creates the
schema of every component on a temporary database and runs the plan guard. `seed N` fills the database in
`TR_DATA_DIR`. For the format of every module with a seed (`Extender.Seed`), it creates the repos
`seed-<format>-00` to `seed-<format>-09` (anonymous read, default config) and splits N rows
evenly over all of them in transactions of 10,000. Each format inserts its own rows: maven
and raw insert asset rows, debian inserts one package and its asset row per unit. Then it runs
`ANALYZE`. It is safe to run again.

## Tooling (`go tool task`, registered in `go.mod`)

| Command | Does |
|---|---|
| `go tool task dev` | Checks Go, bun/npm and Node (version in `.node-version`). Runs the backend on :8080 and rebuilds it on Go or `schema.sql` changes. Runs Vite with HMR on **:5173**, proxying every path except the UI's own files to the backend |
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
drives the browser flow with a cookie-jar HTTP client: a sign-in with the claim map sets the
permissions (409 on a change of them), a person outside the map signs in without permissions, a
map change replaces them at the next sign-in, and without a claim a sign-in changes nothing; a map
without a claim or with an unknown or repository permission is 400. `TestRaw` uses a curl container.
`TestAPIFormats` covers `/api/formats`, `/status` and `/usage`; `descriptor_test.go` covers the format descriptors (`TestFormatDescriptors`), the CI file route with branches and tags (`TestCIWorkflowRoute`) and the delete plan (`TestDeletePlan`). `TestQueryPlans` runs `dbcheck`
inside a server container. `files_test.go` covers `TestTree`, `TestDelete` (who may delete: `repo.delete` with a publish session or a UI session, file, prefix and Bearer deletes, blobs kept for the blob GC, repo delete and name reuse), `TestManualUploadMaven` (checksums, metadata order, `mvn` resolves the uploaded and the generated pom), `TestManualUploadRaw`, `TestRepoCounters` (`file_count`, `total_size` and `last_published_at` through uploads, a replace, a file delete and a folder delete) and `TestDebianDeleteAndUpload` (apt sees the package, then not; 4 by-hash files remain). `audit_test.go` covers `TestAudit`: events, filters, paging, no secret values, `activity.read` only, and no IP outside the audit log. `network_test.go` covers `TestClientIP` (with the default an `X-Forwarded-For` from the harness peer is ignored and the peer becomes a proxy candidate; trusting the candidate makes the header count and removes the candidate; `private`: a chain through a private hop, `CF-Connecting-IP` from a non-Cloudflare peer; an untrusted peer's header, an empty list, invalid entries, reset to the default). `TestIPRules` and `TestPresetIPLists` set `trusted_proxies` to `private` first, so `X-Forwarded-For` sets the client address, `TestIPRules` (validation; allowed and refused reads of the format route, tree, assets, usage and status; the repo record stays readable; search drops the repo; refused device and manual mints, PUT and admin delete, each audited with its IP and scope; empty lists lift the rule) and `TestPresetIPLists` (a second Telesphorepo server is the stub: startup fetch, a preset in read rules, a Cloudflare hop in the chain, a failed refresh that keeps the last good list, recovery, and stored lists after a restart without the stub) and `TestCustomIPLists` (create, edit, a rename that keeps the rules and the trusted proxies (by list id), use in rules and in the trusted proxies, delete while in use gives 409, an unknown list id gives 400) and `TestTrustedProxyEnvList` (`TR_TRUSTED_PROXIES` with `list:cloudflare` shows as `env_lists`) and `TestIPRangeParse` (the paste parser: one per line, comma, space, tab and `|` separated, CSV with a header row, tables, comments, labels, quotes, IPv6 and IPv4-mapped IPv6, an unmasked CIDR, duplicates, invalid lines with their numbers, the 1 MiB limit, `network.manage` only). `signing_keys_test.go` covers create, import of plain and encrypted keys, delete while in use gives 409, a per-repo key (the repo field `signing_key`; an unknown key, a key in the config and a key of a raw repo give 400) that `apt` installs from, a key change that signs again, and keys and bindings after a restart. `groups_test.go` covers `TestGroups`: a release and a snapshot member resolve through the group with `mvn` and Gradle, the merged `maven-metadata.xml` and its `.sha1`, the member order, a public group with a private member (anonymous and a reader), 405 for a PUT and 409 for a publish session, 400 for a member of another format, a nested group, an unknown name and no member, and a member delete. `proxy_test.go` covers `TestProxy` with a second Telesphorepo server as the remote: `mvn` through the proxy and no remote request on the second resolve, the `metadata_ttl` recheck with a 304 and a changed file, a remembered 404, 405 for a PUT, a group where the hosted member wins over the proxy, remote credentials (with them, 502 without them), an nginx container that redirects to another host and records that the credentials did not follow, `apt-get` through a Debian proxy with the remote's key, and a stopped remote (cached file served, 502, `last_error`). `TestNoBrowserPasswordPrompt` (in `auth_test.go`) checks that a 401 under `/api` or with `X-Telesphorepo-UI` has no `WWW-Authenticate`. `folder_upload_test.go` covers `TestFolderUploadMaven` (a Maven-layout tree with checksum, signature and stray files; a flat folder with two poms; a bad tree that gives 400; plans store nothing; checksums and metadata) and `TestFolderUploadDebianAndRaw`. `TestStorageMove*` run each server on a named data volume: fs to
SeaweedFS and back with uploads during the move, a forced failure, a restart mid-move
(`Server.Restart`) and a cancel. `blobs_test.go` covers `TestBlobDedupeAndGC` (the same bytes in two raw repos
and a Maven repo are one stored blob, each path keeps its own content type, also in an S3 redirect;
the blob stays while a file uses it and the blob GC removes it after `TR_BLOBS_GC_GRACE=1s`; an upload of the
same bytes stores them again), `TestUploadSessions` (chunks, a wrong offset, a restart between chunks, a
wrong digest, another publish session, finish into a raw and a Maven repo, an immutable release path,
cancel) and `TestMavenMetadataAfterDelete`. `bulk_test.go` checks the `mirror` operation of a mapped repo and
the 409 of resume and clear.

**Zones.** A build tag picks the zone, so a run starts only the containers it needs. The harness
and the client helpers (`harness_test.go`, `main_test.go`, `clients_test.go`) carry every tag.
Each test removes its containers when it ends. Every container and the run network carry the
label `telesphorepo.e2e`, and each run first removes what a killed run left: labelled containers
and networks, and the `telesphorepo-data-*` and `telesphorepo-scale-*` volumes.
| Zone | Tag | Command | Contents |
|---|---|---|---|
| core | `e2e` | `go tool task test` | Formats, auth, API, settings, storage move, query plans |
| import | `import` | `go tool task test import` | `TestNexus` |
| scale | `scale` | `go tool task test scale` | `TestScale` |
| clients | `clients` | `go tool task test clients` | `TestClients` (`e2e/publish_clients_test.go`) and `TestSetupPowerShell` (`e2e/setup_clients_test.go`). It needs `git` unless `E2E_CLIENTS_DIR` points at a checkout of the clients repo |
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
`permissions_test.go` covers `TestServerPermissions` (one subtest per server permission: the
holder reaches its routes, anyone else gets 403), `TestPipelinesPermission` (the CI pipeline
scope) and `TestPeoplePicker` (`GET /api/people`). `registries_test.go` covers
`TestPermissionRegistry` (the shape of `GET /api/permissions`, the server permissions in module
start order, 400 for an unknown ID or an ID of the other scope, grants read back as IDs),
`TestAuditActions` (`GET /api/audit/actions` and the events that services record: read tokens, CI
pipelines, the first-start signing key, the bind of a new Debian repo's key), `TestSettingsS3Endpoint` (an empty endpoint needs a
region, `r2` is refused, an unknown group's test is 404) and `TestSSOStartupGuard` (SSO settings
and `signin.password_login=publishers` saved with one master key; after a restart with another the
server starts with SSO off and password sign-in for all). `settings_test.go` has
`TestSettingsSurviveRestart` (a size, a duration and a secret setting come back from the database
after `Server.Restart`, the secret shows only as set, and the stored size limit applies).

**Publishing tests.** Every publish in the suite uses a publish session. The harness helper
`PublishToken(t, s, user, repo)` enrolls a device the real way (the test computes the TOTP
codes), approves it and mints a session. The harness enrolls 3 TOTP factors per user, so tests
never wait 30 s for a new step. `publish_test.go` covers: `TestPublishGate` (a password or read
token is refused; a session for another repo; lost permission; closed and expired sessions;
replayed nonce; bad signature; revoked and expired devices; manual sessions),
`TestSecondFactorLogin` (including the setup-only session), `TestDeviceEnrollment` (an unknown
publisher client is 400; the device names its client),
`TestDeviceEnrollmentExpiry` (ages the row with `editDB` on a data volume) and `TestPublishOIDC`
(Keycloak as the CI issuer through the client `ci-github` in `realm-e2e.json`, whose tokens carry
GitHub-shaped claims: wrong claim, wrong audience, replayed token, an unlisted repo, a disabled
policy, a pasted JWKS). The Keycloak fixture is in `keycloak_test.go` (tags `e2e` and `clients`).
`oidc_device_test.go` covers `TestOIDCDeviceApproval` (a person who signs in with single sign-on approves a device after a new
Keycloak sign-in, then publishes with it; the authorization request carries `prompt=login` and
`max_age=0`; a missing grant, a grant for another code and a used grant get 401) and
`TestOIDCDeviceApprovalStale`. That test signs in first, then calls `t.Parallel()`, so the wait
for a sign-in older than 5 minutes runs while the other tests run. The test then removes
`prompt` and `max_age` from the authorization request, as a provider that ignores them, and
checks that the callback returns `error=stale_sign_in` and that the code stays pending.

`TestClients` builds the Gradle plugin and the Maven extension in containers, publishes them to
the test server, and then (1) runs `gradle publish` and `mvn deploy` with no device, approves the
code that the inline enrollment prints through the API, and checks that the build continues and
publishes a library (auto publication, no publishing block) and a Gradle plugin, then resolves
them; it checks that `telesphorepoLogin` and the Maven `login` goal keep the enrolled device, and
that a deploy to a missing repository says that the repository does not exist; (2) checks that a
GitHub Actions build without `id-token: write` fails and names it, and publishes as "GitHub
Actions" with a `jwebserver` container that serves a Keycloak token at
`ACTIONS_ID_TOKEN_REQUEST_URL`; (3) fetches `/setup.sh`, sets its version to the clients of the
run, and runs it twice with `--yes` (the second run changes no file): in a Gradle Kotlin build
with a `pluginManagement` and a `plugins` block, with `--ci github` (the id line lands in the
`plugins` block, the workflow file), and in a fresh Maven project with `--ci none` (`.mvn/extensions.xml`,
`.mvn/maven.config`, `<distributionManagement>`); then it publishes each one with the files that
the script wrote, through the inline enrollment. `TestSetupPowerShell` (`e2e/setup_clients_test.go`)
runs `setup.ps1` through `irm` in a `mcr.microsoft.com/powershell` container twice, in a Gradle
Kotlin build with `-Ci jenkins-pipeline` and in a Maven project with `-Ci github`, and checks the
files. The core zone's `TestSetupScripts` (`e2e/setup_test.go`) checks that both scripts are
public and carry the server address and `clientsVersion`.

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
  answers 409. No response holds the Nexus password.

`TestScale` (tag `scale`, `go tool task test scale`) seeds 100k and then 1M assets with `seed N`
on a named volume. It starts a server on the volume and measures the p95 latency over 400 runs of
each operation: Maven GET with `If-None-Match`, an asset list walk, a rare search, a common
search and token auth (`/api/me`). An operation fails when its p95 at 1M exceeds
`max(2 x p95 at 100k, p95 at 100k + 2 ms)`.
