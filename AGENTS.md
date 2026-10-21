# Working in this repo (for coding agents)

Read [SPEC.md](SPEC.md) first. It describes the system as built now: the modules, the route
policy, the registries, the byte pipeline, the schema, the routes and the settings.

## Commands

```sh
go tool task dev            # backend :8080 + Vite UI :5173 with live reload (open :5173)
go tool task build [image]  # bin/telesphorepo-server with UI embedded (+ image telesphorepo:local)
go tool task preview        # build, then run the production binary at :8080
go tool task test [args]    # core zone (tag e2e); args go to go test, e.g. -run TestMaven
go tool task test import    # import zone (tag import): TestNexus, starts a Nexus container (slow)
go tool task test scale     # scale zone (tag scale): 100k and 1M asset latency test (slow)
go tool task test clients   # clients zone (tag clients): the CLI of cmd/telesphorepo with the real tools in containers (slow)
go tool task test all       # core, import, scale and clients one after the other
go tool task test --shard 2/4  # every 4th core test from the 2nd (CI runs the core zone on 4 machines)
go tool task tag <ver>      # stamp the dev entries of changes.toml, commit, tag v<ver> (push it yourself)
go tool task release [ver]  # cross-compiled archives + SHA256SUMS in bin/release
go tool task edge [ver]     # bundle the edge Worker download into internal/edgedist/dist (needs bun)
go tool task gen            # run sqlc for every internal/**/sqlc.yaml
go tool task migration db add_x  # the next migration internal/db/migrations/NNNN_add_x.sql (folder of the component)
go tool task doctor         # show detected Go / bun / Node / Docker-Podman

go build ./...
go vet ./...
go vet -tags e2e ./e2e/...  # e2e files only compile with a tag; also vet -tags import, scale, clients and macos
E2E_SKIP_BUILD=1 go tool task test -run TestX   # reuse the already-built images
cd edge && bunx tsc --noEmit && bun run build   # type-check and bundle the edge Worker
```

`go tool task` is registered with `tool` in `go.mod`. There is no root `package.json`, Makefile
or shell script, so it works the same on Windows, macOS and Linux. The UI build needs bun (npm is
the fallback) and Node 26+ (see `.node-version`); so does the edge Worker in `edge/` (lockfile
`edge/bun.lock`). The e2e suite needs Docker or Podman.

**Who runs what.** A subagent builds and vets: `go build ./...`, `go vet ./...`, `go vet` with
each of the five tags (`e2e`, `import`, `scale`, `clients`, `macos`), `go vet` of
`./cmd/telesphorepo` with `GOOS=darwin` and `GOOS=windows`, `go tool task gen`, `bun run build` in
`web/`, and `bunx tsc --noEmit` and `bun run build` in `edge/`. A subagent never runs a test, a
dev server, a preview or `wrangler dev`, and never touches a Cloudflare account. The main session
runs the zones. Only the `macos` job of `release.yml` runs the macOS zone (tag `macos`).

**Which zone.** Run the core zone for every change. Run the import zone when you touch
`internal/nexus`, an importer or the `mirror` operation. Run the scale zone when you touch a hot
query or an index. Run the clients zone when you touch `cmd/telesphorepo`, `internal/publish`, the
write gate, the device and OIDC routes or the change log. Run all zones before a push that touches several zones.

## Hard rules

- **E2E tests only.** Don't write unit tests or mocks. Test behaviour through the real image and
  real clients (`mvn`, `gradle`, `apt`, curl) in `e2e/` (`//go:build e2e`). Each test starts its
  own server through the harness and calls `t.Parallel()` first. A test never changes the
  process env, and it shares no outside system with another test.
- **No fakes.** Never write a stand-in for another system: no fake build tool, no fake token
  endpoint, no stub server that answers what the real one would. Run the real system in a
  container (Jenkins with its plugins in `TestCLIJenkins`), or run the test where the real
  system is (GitHub Actions tokens only in the `e2e-github-actions` job of `ci.yml`). Another
  version of this server is a real build of it (`BuildServer`, `BuildServerWith`).
- **Every route has a policy.** Register a route with `Router.Handle(pattern, route.Policy,
  handler)`. One middleware checks the credential, the second factor, the permission and the
  repo access. A handler never checks credentials itself. The router panics at startup on a
  policy that lets a password or a read token change state. See **Route policy** in SPEC.md.
- **Writes need a publish session.** A password or read token never writes to a format route.
  The one "may write now" decision is `access.Engine.Decide(..., Write)` (or `Mint`), reached
  through `route.FormatWrite`. Tests get a session with `PublishToken(t, s, user, repo)` from
  `e2e/harness_test.go`, or `s.PublishAuth(t, user, repo)` for a Basic header. Never add a
  write path that accepts a password or a read token.
- **API changes and permission routes need a UI session.** HTTP Basic (a password or a read
  token) reaches only the GET routes of `/api` that need no permission. So an admin password
  cannot go around the second factor. Tests use `s.Admin()` (the admin's UI session) or
  `s.SignIn(t, user, pass)`; `s.AdminBasic()` is for format reads.
- **Only the byte pipeline writes files.** `assets.Pipeline.Commit`, `CommitAll` and `Remove` are
  the only code that writes the `assets` and `blobs` tables. A format takes part through the
  hooks of its `format.Spec`. The plan guard fails a query that writes a table outside the schema
  of its own component.
- **Change a repo through `repos.Service`.** `Create` and `Update` check the whole new state and
  save it in one transaction. A module that keeps a value for a repo registers a `repos.Field`.
- **Register what you add.** A permission (`x.Permissions`), an audit action (`x.AuditActions`),
  a settings group (`x.Settings`), a repo operation type (`x.OpType`) and a CI provider are all
  registered in the module's `Register`. The kernel refuses to record
  an unregistered audit action and to save an unregistered permission ID.
- **Never log second-factor material.** That covers TOTP secrets and codes, recovery codes,
  device keys, publish tokens and OIDC tokens. Compare secrets in constant time.
- **Storage keys and repo paths use package `path`, never `path/filepath`.** Keys are
  forward-slash on every OS. Use `filepath` only for local OS paths, such as the data dir. Check
  a repo path with `repopath.Parse`.
- **Every query on a request path uses an index.** Lists use keyset pagination
  (`?after=&limit=`, `httpx.Limit`) with one lower bound per key column: pick the tighter of prefix and cursor in Go, because SQLite checks a second bound row by row. Never use `OFFSET` or `LIKE '%x%'`; substring search goes
  through `assets_fts` (trigram). Request paths read settings from the in-memory snapshot,
  never from the `settings` table.
- **Write queries in `queries/*.sql`.** The kernel and each module with a schema keep their SQL
  in `queries/*.sql` next to a `sqlc.yaml`. Run `go tool task gen` after you change a query or a
  migration, and commit the generated `dbq/` package. Write a query by hand (`dbq/extra.go`)
  only when sqlc cannot express it, such as FTS5 `MATCH` or a streaming `iter.Seq2` read.
  Register each hand-written query in the package `Extra` map so the plan guard
  (`db.CheckPlans`, run by `TestQueryPlans`) checks it. A query that must scan goes in the
  `Allow` map with a reason. Do not write inline SQL in services.
- **Change a schema with a migration.** Each component keeps its schema in
  `migrations/NNNN_name.sql` (4 digits from `0001`, no gap; `go tool task migration <folder>
  <name>` creates the next file). A migration is SQL only. `db.Init` runs each file once, in its
  own transaction with foreign keys off and `PRAGMA foreign_key_check` before the commit, so it
  may rebuild a table (create the new table, copy, drop the old one, rename; then recreate its
  indexes and triggers). Production servers run every push to `main` (the `main` image), so
  never edit, rename or delete a migration that `main` holds: add the next file. `serve` refuses
  a database whose applied migration changed, or that a newer build migrated.
- **Keep dependencies few and mature.** Use the stdlib first: `crypto/*`, `crypto/hkdf`,
  `net/http` routing, `http.CrossOriginProtection` and `log/slog`. Explain any new module in the
  change, and don't run `go mod tidy` unless an import needs it.
- **Never log secrets.** That covers passwords, tokens, cookies, `Authorization` headers, query
  strings, secret setting values and private keys. The access log already leaves these out; keep
  it that way.
- **Keep CLIs free of flag slop.** Write the human invocation first. Use positional args for the
  obvious thing, TTY prompts for passwords and derived defaults. Add flags only for genuinely
  optional knobs, and give every command an example-led `--help`.
- **Keep the env minimal.** Put a new tunable in a settings group of its module (`x.Settings`):
  key, type, default, label, help, validation, and `Secret` if sensitive. The `general` keys are
  in `internal/settings/general.go`. A setting gets a `TR_<KEY>` override, the UI and the API
  for free. The only env vars outside the settings are the boot env (`TR_DATA_DIR`, `TR_LISTEN`,
  `TR_MASTER_KEY`), the bootstrap admin, and test and tooling vars (`TR_ENGINE`, `E2E_*`,
  `TR_E2E_PRESET_BASE_URL`).
- **Read configuration live.** A service takes `settings.Provider` (`set.Current`) and reads a
  typed `settings.Key` or `settings.View` per request. Don't keep a copy of a setting. To react
  to changes, use `settings.Service.Subscribe`, or the `Apply` hook of the settings group.
- **Support Windows, macOS and Linux.** Don't depend on bash or make in tooling. Watch path
  separators and process signals (see `tools/task/proc_*.go`).
- **Use bun for the web app.** The lockfile is `web/bun.lock`. Don't commit a `package-lock.json`.
  The web stack is Svelte 5 + Vite (no SvelteKit), Tailwind CSS v4, shadcn-svelte (Maia style) and
  TypeScript. Add a shadcn component with `bunx shadcn-svelte@latest add <name>`. A format module keeps the
  calls to its own routes in its own folder. Everything else calls the API through `api.*` in
  `web/src/lib/api/`. `bun run build` runs `svelte-check` first.
- **Follow the UI rules of `web/src/README.md`.** Every page sits in the one centered 1200 px
  container. The API client sends `X-Telesphorepo-UI: 1` on every request, so the server never
  sends a browser password prompt. Use monospace only in `CodeBlock`, `CodeSnippet` and the JSON
  editor. A dialog is for a confirmation or a short form only. No shared UI code names a format:
  read the format descriptor (`GET /api/formats`).
- **Never show an IP address outside the audit log.** `audit_events` is the only table and
  `GET /api/audit` the only API that returns one.
- **Read the client address with `httpx.ClientIP(r)` only.** The network module resolves it
  once per request. Never read `X-Forwarded-For` or `CF-Connecting-IP` in a handler, because
  the server trusts them only from a trusted proxy. A route that acts on a repo names the repo in
  its policy (`route.RepoRule`), so the access engine runs the IP rule guard.
- **Request paths use in-memory snapshots of IP lists, IP rules and signing keys.** They never query
  `ip_lists`, `repo_ip_rules` or `signing_keys`. The service refreshes the snapshot when the data changes.
- **Keep the docs current.** Update `SPEC.md` in the same change whenever behaviour, routes,
  tables, settings or commands change. Keep `README.md` short and for humans (run locally, run in
  production, a few commands); details belong in SPEC.md. Don't add a `CLAUDE.md`.
- **Add a `changes.toml` entry when an older side would fail.** Add an entry with `version = "dev"`
  when a change makes an older CLI fail against a newer server (`needs = "cli"`), or a newer CLI
  fail against an older server (`needs = "server"`) in a command, a format or a helper. Most
  changes need none. When the CLI keeps the older path of a `needs = "server"` entry, give the
  entry an `id` and list the id in `olderPaths` (`cmd/telesphorepo/compat.go`), never the
  version. `go tool task tag <version>` writes the release version into each `dev` entry,
  commits it and tags the release. See **Change log** in SPEC.md.

## Where things live

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
| Client IP, IP lists, repo IP rules | `internal/netrules` (module `network`: `lists.go`, `rules.go`, `clientip.go`, `module.go`), `web/src/pages/settings/repos/` (Access tab), `web/src/pages/server/IPLists.svelte`, `IPListPage.svelte`, `ReverseProxy.svelte` |
| Docker / OCI registry | `internal/docker` (module `docker`: `routes.go` at `/v2/`, `token.go` the token endpoint `/docker/token`, `manifests.go`, `blobs.go`, `lists.go`, `referrers.go`, `hooks.go` (the rows of `migrations/`), `links.go` the job `docker.links`, `proxy.go` the proxy (`Classify`, `RemoteAuth`, tags list) and group tags, `usage.go` and `tools.go` the snippets and the CI tool, `versions.go`, `api.go` `/api/docker/{name}/manifest`, `importer.go` the Nexus import, `demo.go`, `seed.go`); UI part `web/src/modules/docker/`; e2e in `e2e/docker_test.go`, `e2e/docker_proxy_test.go`, `e2e/nexus_docker_test.go` and `e2e/cli_docker_test.go` (clients zone), with `StartDind` and `Dind.CopyFile` in `e2e/clients_test.go` and the OCI helpers in `e2e/oci_test.go` |
| Telesphorepo CLI and its Docker credential helper | `cmd/telesphorepo` (MIT, its own `LICENSE`; stdlib, `golang.org/x/sys`, `BurntSushi/toml` and the root package `changelog` only; imports no server package): `main.go` the commands and the error handling, `setup.go`, `publish.go`, `detect.go` the detection table (a new format adds a row), `upload.go` raw and Debian uploads, `login.go` login, logout and status, `update.go`, `compat.go` the change log check, `problem.go` problem documents and remedies, `client.go` the HTTP client, the device flow and the mints, `ci.go` the CI token, `project.go` `telesphorepo.toml`, `docker.go` the helper mode (program name `docker-credential-telesphorepo`), `identity.go`, `device.go`, `exchange.go`, `dockerconfig.go`, `acl_*.go`, `parent_*.go`, `tty_*.go`; built and embedded by `tools/task/cli.go`, released by `tools/task/release.go` |
| Signing keys | `internal/signing/keys.go`, `internal/signing/module.go` (the repo field `signing_key`), `GET /debian/{repo}/key.asc` in `internal/debian`; UI in `web/src/pages/server/SigningKeys.svelte` and `web/src/modules/signing/` |
| Upload plan and upload | `Plan` and `Store` of the format (`format.Spec.Upload`), e.g. `internal/maven/plan.go`; routes in `internal/api/files.go`; UI in `web/src/pages/repo/UploadView.svelte` and `upload/` |
| Snippets, CI files | `Usage` and `Tools` of the format (`format.CLITool`, `format.CLISnippet`); `internal/ci/workflow.go` (the GitHub Actions, Jenkinsfile and Jenkins freestyle builders, each with the install step of the CLI) |
| Audit log | `internal/audit` (registry, `Record`), `internal/activity` (module `audit`: `GET /api/audit`), `web/src/pages/server/Activity.svelte` |
| Edge delivery | `internal/edge` (module `edge`: `module.go` buckets and routes, `settings.go` the group `edge`, `objects.go` the R2 objects, `watcher.go` the pipeline watcher, `outbox.go` the job `edge.outbox`, `access.go` the access objects, the change notices and `edge.reconcile`, `control.go` the job `edge.backfill` (on, off, draining, regions, blob check), `gap.go` and `seen.go` what the Worker writes, `wrangler.go`, `api.go` with the download `worker.zip`); `format.Spec.Edge` of each format; the Worker in `edge/` (`src/index.ts` fetch, `shapes.ts`, `glob.ts` the port of `format.Glob`, `access.ts`, `serve.ts`, `copy.ts` queue, `gap.ts` cron, `scripts/provision.ts`); the Worker folder that the server embeds (`internal/edgedist`) and the release archive, both from `tools/task/edge.go`; UI in `web/src/pages/server/EdgeDelivery.svelte` and `web/src/modules/edge/`; e2e of the Worker download in `e2e/edge_test.go`, none of the serving (local R2 differs from R2 in what the edge relies on) |
| Backups and restore | `internal/backup` (module `backup`: `backup.go` one backup, `list.go` the list and the retention, `restore.go`, `offline.go` the source of the CLI, `module.go` settings, job and routes), the core queries in `internal/db/queries/backup.sql`, `db.VacuumInto` and `db.OpenReadOnly`, the commands in `cmd/telesphorepo-server/backup.go`, `web/src/pages/server/Backups.svelte` and `web/src/modules/backup/`; e2e in `e2e/backup_test.go` |
| Storage move (FS ↔ S3) | `internal/move` (module `storage`: service, copier, `module.go`), `internal/storage/mirror.go`, `web/src/pages/server/Storage.svelte` |
| Single sign-on | `internal/oidc` (module `sso`: sign-in, the permission map, device approval), `web/src/pages/server/SingleSignOn.svelte` |
| Nexus sources, syncs, cut-over | `internal/nexus` (module `nexus`: client, service, `sync.go` scheduler and runner, `api.go`), `web/src/pages/server/NexusMigration.svelte`, `NexusSource.svelte`. Per-format import code is the `Importer` of the format (`Plan` for the target repo, `Target`/`Store` per file). Repo lock: the repo operation `mirror` (`repo_ops`), checked in `access.Engine.Decide` |
| Demo data and seed | `internal/demo`, and `Demo` and `Seed` of each module; `cmd/telesphorepo-server/demo.go` |
| E2E harness | `e2e/harness_test.go` (servers, `ForEachStorage` for `fs` + SeaweedFS, `OnLocalDisk`), `e2e/clients_test.go` (Maven, Debian and curl helpers that several zones share), publish helpers at the end of `harness_test.go`, `e2e/keycloak_test.go` (Keycloak fixture), `e2e/build_test.go` (`BuildCLI`, `BuildServer`: programs of this repo built on the host with `go build -overlay`), `e2e/cli_server_test.go` (the CLI routes of the server, core zone), `e2e/cli_harness_test.go` and `e2e/cli_*_test.go` (clients zone: the CLI in containers, the secret scan), `e2e/cli_jenkins_test.go` with `e2e/testdata/jenkins` (the real Jenkins of the clients zone) |
| Change log and server version | `changes.toml` at the repo root (embedded by `changes.go`, package `telesphorepo`), the parser in `changelog/` (stdlib and toml only, shared with the CLI), `GET /api/changes` in `internal/api/changes.go`, the `Telesphorepo-Version` header in `internal/server/server.go`; `go tool task tag <version>` stamps the `dev` entries |
| Dev tooling | `tools/task` |

## Adding a module

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

### Adding a repo format

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
  answers a remote's token challenge. `Kinds` with `group` can set `Merge`.
- **Web UI:** `Description`, `Config.Fields`, `UI` (the descriptor), `Versions` and `Builds`,
  `Usage`, `ArtifactUsage` and `Tools` (snippets and CI steps), `Publish` (`cli` or `helper`), `Upload` (`Plan` and
  `Store`), `SigningKey` and `RepoTabs`. The web UI reads the descriptor and names no format.
- **Import:** `Importer` (`Sources`, `Target`, `Store`, `Plan`): the Nexus import offers each
  source file to the format.

`raw` (`internal/raw`) is the smallest example. `debian` shows a schema, a repo field, `Rebuild`
and an importer plan. `maven` shows proxy, group, `Merge`, `Upload` and `Tools`.

## Adding a UI page

1. Create `web/src/pages/<area>/<Name>.svelte` with `let { params, query }: PageProps = $props();`.
2. Add one `PageDef` to the owning module's `web/src/modules/<id>/module.ts`: `path`, lazy
   `page`, `layout` (`app` or `settings`), `access` (`any`, `user`, `repositories`, `repo` or a
   permission ID), `title`, and `setup` for a page of a setup-only session.
3. For a Settings page, give it `nav` (group, label, icon, order).
4. Add the route to the table in `web/src/README.md`.
5. Call the API through `api.*`. For a new server route, add a function and its type in
   `web/src/lib/api/`. A format module keeps the calls to its own routes in its own folder.
6. Use `Resource` or `Keyset` (`lib/load.svelte.ts`) for requests, and show loading, error and
   empty states. Use `ConfirmDialog` for every destructive action.

## How the maintainer likes to work

- **Design first, then build. No plan documents.** For architectural work, write a design spec in
  `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md` and get the maintainer to approve it.
  After approval, implement straight from the spec. When the work is done, delete the spec and
  move what stays true into `SPEC.md`.
- **Use subagents freely.** Large parallel swarms are welcome. Use Opus or Sonnet subagents, and
  Haiku only for read-only work. Never use Fable subagents.
- **Subagents build and vet, but never test.** They do not run tests, dev servers or the app.
  After each milestone the main session runs the zones once and sends fix-up agents for failures.
- **Make decisions instead of asking a lot of questions.** Flag a real trade-off when there is one,
  then pick a sensible default.
- **Treat other tools as inspiration, not a requirement.** When the maintainer cites another tool
  ("like `bun run dev/build/preview`"), copy its shape using this project's own tooling.
- Commit in logical chunks and push to `origin` (github.com/Telesphorepo/Telesphorepo) once the
  suite is green.
