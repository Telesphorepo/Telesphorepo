# Working in this repo (for coding agents)

Read [SPEC.md](SPEC.md) first. It describes the system as currently built. Design docs and
milestones are in `docs/superpowers/specs/`, mainly `2026-10-14-telesphorepo-mvp-design.md`.

## Commands

```sh
go tool task dev            # backend :8080 + Vite UI :5173 with live reload (open :5173)
go tool task build [image]  # bin/telesphorepo with UI embedded (+ image telesphorepo:local)
go tool task preview        # build, then run the production binary at :8080
go tool task test [args]    # core zone (tag e2e); args go to go test, e.g. -run TestMaven
go tool task test import    # import zone (tag import): TestNexus, starts a Nexus container (slow)
go tool task test scale     # scale zone (tag scale): 100k and 1M asset latency test (slow)
go tool task test clients   # clients zone (tag clients): builds clients/gradle and clients/maven in containers (slow)
go tool task test all       # core, import, scale and clients one after the other
go tool task release [ver]  # cross-compiled archives + SHA256SUMS in bin/release
go tool task gen            # run sqlc for every internal/**/sqlc.yaml
go tool task doctor         # show detected Go / bun / Node / Docker-Podman

go build ./...
go vet ./...
go vet -tags e2e ./e2e/...  # e2e files only compile with a tag; also vet -tags import, scale and clients
E2E_SKIP_BUILD=1 go tool task test -run TestX   # reuse the already-built image
```

Run the core zone for every change. Run the import zone when you touch internal/nexus, internal/api/nexus.go, an importer, the repo lock or the mirror gate. Run the scale zone when you touch a hot query or an index. Run the clients zone when you touch `clients/`, `internal/publish`, the write gate or the device and OIDC routes. Run all before a push that touches several zones. A subagent builds and vets with the tags; the main session runs the zones.

`go tool task` is registered with `tool` in `go.mod`. There is no root `package.json`, Makefile
or shell script, so it works the same on Windows, macOS and Linux. The UI build needs bun (npm is
the fallback) and Node 26+ (see `.node-version`). The e2e suite needs Docker or Podman.

## Hard rules

- **E2E tests only.** Don't write unit tests or mocks. Test behaviour through the real image and
  real clients (`mvn`, `gradle`, `apt`, curl) in `e2e/` (`//go:build e2e`). Each test should
  start its own server via the harness.
- **Writes need a publish session.** A password or read token never writes to a format route.
  The gate is `auth.RepoGate`. Tests get a session with `PublishToken(t, s, user, repo)` from
  `e2e/harness_test.go`, or `s.PublishAuth(t, user, repo)` for a Basic header. Never add a
  write path that accepts a password or a read token.
- **Never log second-factor material.** That covers TOTP secrets and codes, recovery codes,
  device keys, publish tokens and OIDC tokens. Compare secrets in constant time.
- **Storage keys and repo paths use package `path`, never `path/filepath`.** Keys are
  forward-slash on every OS. Use `filepath` only for local OS paths, such as the data dir.
- **Every query on a request path uses an index.** Lists use keyset pagination
  (`?after=&limit=`, `httpx.Limit`) with one lower bound per key column: pick the tighter of prefix and cursor in Go, because SQLite checks a second bound row by row. Never use `OFFSET` or `LIKE '%x%'`; substring search goes
  through `assets_fts` (trigram). Request paths read settings from the in-memory snapshot,
  never from the `settings` table.
- **Write queries in `queries/*.sql`.** Core and each format keep their SQL in `queries/*.sql`
  next to a `sqlc.yaml`. Run `go tool task gen` after you change a query or a migration, and
  commit the generated `dbq/` package. Write a query by hand (`dbq/extra.go`) only when sqlc
  cannot express it, such as FTS5 `MATCH` or a streaming `iter.Seq2` read. Register each
  hand-written query in the package `Extra` map so the plan guard (`db.CheckPlans`, run by
  `TestQueryPlans`) checks it. A query that must scan goes in the `Allow` map with a reason.
  Do not write inline SQL in services.
- **Keep dependencies few and mature.** Use the stdlib first: `crypto/*`, `crypto/hkdf`,
  `net/http` routing, `http.CrossOriginProtection` and `log/slog`. Explain any new module in the
  change, and don't run `go mod tidy` unless an import needs it.
- **Never log secrets.** That covers passwords, tokens, cookies, `Authorization` headers, query
  strings, secret setting values and private keys. The access log already leaves these out; keep
  it that way.
- **Keep CLIs free of flag slop.** Write the human invocation first. Use positional args for the
  obvious thing, TTY prompts for passwords and derived defaults. Add flags only for genuinely
  optional knobs, and give every command an example-led `--help`.
- **Keep the env minimal.** Put a new tunable in the settings registry
  (`internal/settings/registry.go`: key, type, default, group, label, help, validation, and
  `Secret` if sensitive) rather than in a new env var. It then gets a `TR_<KEY>` override, the UI
  and the API for free. The only env vars outside the registry are the boot env (`TR_DATA_DIR`,
  `TR_LISTEN`, `TR_MASTER_KEY`), the bootstrap admin, and test/tooling vars (`TR_ENGINE`,
  `E2E_*`).
- **Read configuration live.** Services take a `config.Provider` (`set.Config`) and call it per
  request. Don't keep a `*config.Config` around. To react to changes, use
  `settings.Service.Subscribe`.
- **Support Windows, macOS and Linux.** Don't depend on bash or make in tooling. Watch path
  separators and process signals (see `tools/task/proc_*.go`).
- **Use bun for the web app.** The lockfile is `web/bun.lock`. Don't commit a `package-lock.json`.
- **Keep the docs current.** Update `SPEC.md` in the same change whenever behaviour, routes,
  tables, settings or commands change. Keep `README.md` short and for humans (run locally, run in
  production, a few commands); details belong in SPEC.md. Don't add a `CLAUDE.md`.

## Where things live

| Need | Place |
|---|---|
| Wiring and startup order | `internal/server/server.go` |
| New core setting | `internal/settings/registry.go` (+ a field in `config.Config` if code reads it) |
| New format setting | `Settings()` of the format (`format.Configurable`) |
| Core schema change | New `internal/db/migrations/000N_name.sql` (never edit applied migrations) |
| Format schema change | New `internal/<format>/migrations/000N_name.sql` |
| Query | `queries/*.sql` of the owner (`internal/db` or the format), then `go tool task gen` |
| New repo format | `internal/<format>` + one line in `internal/formats/all.go` (see below) |
| Plugin contract | `internal/format/format.go` |
| JSON API route | `internal/api/api.go` (register) + handler file; errors via `httpx.Error` |
| Auth / permission checks | `internal/auth` (`Identify`, repo access and the write gate, second factor) |
| Publish sessions, devices, trust policies | `internal/publish`; routes in `internal/api/publish.go` and `internal/api/security.go` |
| Publisher clients (Gradle plugin, Maven extension) | `clients/gradle`, `clients/maven`; release in `.github/workflows/release-clients.yml` |
| Blob I/O | `internal/assets` (spool, store, serve) on top of `internal/storage` |
| UI page | `web/src/pages/*.tsx`, routes and nav in `web/src/App.tsx`, fetch helpers in `web/src/api.ts` |
| Storage move (FS ↔ S3) | `internal/move` (service, copier), `internal/storage/mirror.go`, `internal/api/moves.go`, `web/src/pages/MovePanel.tsx` |
| Nexus sources, syncs, cut-over | `internal/nexus` (client, service, `sync.go` scheduler and runner), `internal/api/nexus.go`, `web/src/pages/Nexus.tsx`. Per-format import code is the plugin's `Importer`. Repo lock: `repos.locked_by`, checked in `auth.RepoGate` |
| E2E harness | `e2e/harness_test.go` (servers, storage matrix `fs` + SeaweedFS), `e2e/clients_test.go` (Maven, Debian and curl helpers that several zones share), publish helpers at the end of `harness_test.go`, `e2e/keycloak_test.go` (Keycloak fixture), `e2e/publish_clients_test.go` (clients zone) |
| Dev tooling | `tools/task` |

## Adding a repo format

A format is one package that implements `format.Format` (`internal/format`). The core names no
format, so you edit no core file.

1. Create `internal/<format>` with `func New() format.Format`. The type implements `Name`,
   `Title`, `DefaultConfig`, `ValidateConfig` and `Register(mux, format.Host)`. The name is the URL
   segment, the `repos.format` value and the migration component.
2. In `Register`, keep the fields you need from `format.Host` (`Auth`, `Assets`, `Config`, ...).
   Authorize through `Host.Auth.RepoGate`, store and serve through `Host.Assets` (key
   `repos/{repo}/{path}`), and read limits from `Host.Config`.
3. Implement the optional interfaces you need: `Migrator` (own tables), `Queryable` (plan guard),
   `Configurable` (settings group), `Starter` (background work), `StatusReporter`,
   `ConfigObserver` and `Describer` (client snippets that the UI and `dev` show), and `Importer`
   (`ImportFormats` and `Import`: the Nexus import offers each source file to the plugin; share
   the code of your upload path with it, and return `imported`, `skipped` or `ignored`).
4. If the format has tables, add `migrations/NNNN_*.sql`, `queries/*.sql` and a `sqlc.yaml`
   (copy `internal/debian/sqlc.yaml`). Run `go tool task gen`.
5. Add one line to `internal/formats/all.go`: the import and the `format.Register(...)` argument.
6. Add `e2e/<format>_test.go` that uses the format's real client and runs on every storage backend.
7. Document the routes and tables in `SPEC.md`.

`raw` (`internal/raw`) is the smallest example. `debian` shows migrations, settings and a
background worker.

## How the maintainer likes to work

- **Design first, then build. No plan documents.** New features get a design spec in
  `docs/superpowers/specs/YYYY-MM-DD-<topic>-design.md`, written with the superpowers brainstorming
  skill and approved by the maintainer. After approval, implement straight from the spec; don't
  write a separate implementation plan.
- **Use subagents freely.** Large parallel swarms are welcome. Use Opus or Sonnet subagents, and
  Haiku only for trivial non-coding work. Never use Fable subagents.
- **Subagents only implement.** They don't build, vet, lint, test or run the app. After each
  milestone the main session runs the build, vet and the e2e suite once, and sends fix-up agents
  for failures.
- **Make decisions instead of asking a lot of questions.** Flag a real trade-off when there is one,
  then pick a sensible default.
- **Treat other tools as inspiration, not a requirement.** When the maintainer cites another tool
  ("like `bun run dev/build/preview`"), copy its shape using this project's own tooling.
- Commit in logical chunks and push to `origin` (github.com/Telesphorepo/Telesphorepo) once the
  suite is green.
