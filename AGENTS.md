# Working in this repo

Telesphorepo is an artifact server (Go, SQLite, Svelte 5). [SPEC.md](SPEC.md) is the reference for
how it works now; search it for the part you change. Its last section, **Code map and recipes**,
says where things live and how to add a module, a format or a UI page.

## Commands

```sh
go tool task dev                  # backend :8080 + Vite UI :5173 with live reload
go tool task build                # bin/telesphorepo-server with the UI embedded
go tool task gen                  # sqlc, after changing a query or a migration
go tool task migration <dir> <name>  # the next migration file of a component
go build ./... && go vet ./...
go vet -tags e2e ./e2e/...        # also -tags import, scale, clients, macos
cd web && bun run build           # svelte-check, then the UI bundle
cd edge && bunx tsc --noEmit && bun run build
```

Tooling is `go tool task` (no Makefile, no shell scripts): it must work on Windows, macOS and
Linux. Use bun, not npm.

## Workflow

- Every change reaches `main` through a pull request. CI runs the build and the e2e zones on the
  pull request; a push to `main` publishes the `main` image, which production runs.
- Tests cost time locally and money in CI, so run each once. Locally, run only the tests a change
  touches (`go tool task test -run 'TestX|TestY'`), once, when the change is finished, never after
  every edit and never a whole zone. Do not run locally what the push you are about to make runs
  in CI. Read CI's log when a job fails. Start the import and scale zones by hand (`e2e-extended.yml`) for a change to the Nexus
  import, a hot query or an index.
- Update SPEC.md in the same change when behaviour, routes, tables or settings change.
- For architectural work, write a short design in `docs/superpowers/specs/` and get it approved
  first; delete it when done and keep what stays true in SPEC.md.

## Rules the code will not tell you

- **Migrations are append-only.** Production runs `main`, so never edit, rename or delete a
  migration file that `main` holds: add the next one. `serve` refuses a changed migration.
- **Tests are e2e only, against real systems.** No unit tests, mocks or fakes: run the real tool
  or service in a container. A test calls `t.Parallel()`, starts its own server, and never
  hardcodes a fact the code owns (versions, defaults, registry lists, limits): read it from the
  server or set it in the test.
- **Security model.** Every route is registered with a `route.Policy`; handlers never check
  credentials. Only a publish session writes to a format route; a password or read token only
  reads. API changes need a UI session (second factor). Read the client address only with
  `httpx.ClientIP`. Never log secrets or second-factor material; compare secrets in constant time.
- **Only the byte pipeline writes files** (`assets.Pipeline.Commit`, `CommitAll`, `Remove`); a
  format joins through its `format.Spec` hooks. Change a repo only through `repos.Service`.
- **Queries live in `queries/*.sql`** and every request-path query uses an index (keyset
  pagination, never `OFFSET` or `LIKE '%x%'`); the plan guard checks them.
- **Register what a module adds** (permissions, audit actions, settings groups, op types) in its
  `Register`. New tunables are settings of the module, not env vars; read them live per request.
- **Storage keys use package `path`**, never `path/filepath`.
- **An IP address shows only in the audit log** (`audit_events`, `GET /api/audit`).
- **The UI follows `web/src/README.md`**; shared UI code never names a format.
- **CLI commands lead with the human invocation**: positional args for the obvious thing, prompts
  for secrets, flags only for optional knobs, an example-led `--help`.
- **Keep dependencies few**: stdlib first; explain any new module.
- Add a `changes.toml` entry when a change breaks an older CLI or server (see SPEC.md).
