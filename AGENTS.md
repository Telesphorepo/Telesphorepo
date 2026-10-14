# Working in this repo (for coding agents)

Read [SPEC.md](SPEC.md) first. It describes the system as currently built. Design docs and
milestones are in `docs/superpowers/specs/`, mainly `2026-10-14-telesphorepo-mvp-design.md`.

## Commands

```sh
go tool task dev            # backend :8080 + Vite UI :5173 with live reload (open :5173)
go tool task build [image]  # bin/telesphorepo with UI embedded (+ image telesphorepo:local)
go tool task preview        # build, then run the production binary at :8080
go tool task test [args]    # e2e suite; args go to go test, e.g. -run TestMaven
go tool task doctor         # show detected Go / bun / Node / Docker-Podman

go build ./...
go vet ./...
go vet -tags e2e ./e2e/...  # e2e files only compile with the tag
E2E_SKIP_BUILD=1 go tool task test -run TestX   # reuse the already-built image
```

`go tool task` is registered with `tool` in `go.mod`. There is no root `package.json`, Makefile
or shell script, so it works the same on Windows, macOS and Linux. The UI build needs bun (npm is
the fallback) and Node 26+ (see `.node-version`). The e2e suite needs Docker or Podman.

## Hard rules

- **E2E tests only.** Don't write unit tests or mocks. Test behaviour through the real image and
  real clients (`mvn`, `gradle`, `apt`, curl) in `e2e/` (`//go:build e2e`). Each test should
  start its own server via the harness.
- **Storage keys and repo paths use package `path`, never `path/filepath`.** Keys are
  forward-slash on every OS. Use `filepath` only for local OS paths, such as the data dir.
- **Every query on a request path uses an index.** Lists use keyset pagination
  (`?after=&limit=`, `httpx.Limit`). Never use `OFFSET` or `LIKE '%x%'`; substring search goes
  through `assets_fts` (trigram). Request paths read settings from the in-memory snapshot,
  never from the `settings` table.
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
  `E2E_*`, `TR_E2E_R2_*`).
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
| New setting | `internal/settings/registry.go` (+ a field in `config.Config` if code reads it) |
| Schema change | New `internal/db/migrations/000N_name.sql` (never edit applied migrations) |
| JSON API route | `internal/api/api.go` (register) + handler file; errors via `httpx.Error` |
| Auth / permission checks | `internal/auth` (`Identify`, repo access) |
| Blob I/O | `internal/assets` (spool, store, serve) on top of `internal/storage` |
| UI page | `web/src/pages/*.tsx`, routes and nav in `web/src/App.tsx`, fetch helpers in `web/src/api.ts` |
| E2E harness | `e2e/harness_test.go` (servers, storage matrix, clients) |
| Dev tooling | `tools/task` |

## Adding a repo format

1. Add a migration that widens the `repos.format` CHECK. SQLite needs a table rebuild for this;
   do it in a new migration file.
2. Add the format constant and any config validation in `internal/repos`.
3. Create `internal/<format>` with a `Handler` that has `Register(mux)`. Authorize through
   `internal/auth`, store and serve through `internal/assets` (key `repos/{repo}/{path}`), and
   read limits from `config.Provider`.
4. Wire it in `internal/server/server.go`, add it to the Vite proxy list in `web/vite.config.ts`,
   and expose it in the UI.
5. Add e2e tests that use the format's real client and run on every storage backend.
6. Document the routes and tables in `SPEC.md`, and add a short client snippet to `README.md`
   only if the format is common.

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
