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
- **Tests run locally; CI runs once.** CI runs on Blacksmith, which bills every run, so a pull
  request's CI is the last check of a finished branch, not a place to find bugs. The e2e tests run
  here on Podman (start it with `podman machine start` when it is stopped; it may keep running).
- **A test run follows a commit.** Finish the change, commit it, then run the tests it touches,
  once (`go tool task test -run 'TestX|TestY'`). A failure: fix it, commit, and run the failed
  tests once more. Never run tests after each edit, and never in a loop while code changes.
- **Before the push, the zones the branch touches pass here:** the core zone for a change to the
  server or the UI (`go tool task test`), the clients zone for the CLI, the scale zone
  (`go tool task test scale`) for a hot query, an index or a request path, the import zone
  (`go tool task test import`) for the Nexus import. Never start `e2e-extended.yml` on GitHub.
- **Push a pull request once, when it is done.** Every push to an open pull request runs its whole
  CI again: batch the fixes into one push. When CI fails anyway, read its log, reproduce the
  failure here, and push the fix only after it passes here.
- Update SPEC.md in the same change when behaviour, routes, tables or settings change.
- For architectural work, write a short design in `docs/superpowers/specs/` and get it approved
  first; delete it when done and keep what stays true in SPEC.md.

## Performance is a feature

A request costs the same at 1,000,000 files, thousands of repositories and hundreds of people as
it does on an empty server. That was the promise from the first commit (the scale test: search
3.38 ms at 100k assets, 3.39 ms at 1M), and every change keeps it.

- **The cost of a request follows its answer, never the data around it.** A page of 200 costs a
  page of 200; a count is a maintained counter or a bounded one ("10,000+"). Never read every row
  of a table, a repository or a package to answer one page, sort or filter a whole set in Go or in
  the browser, loop one query per row (N+1), or skip rows the caller cannot see one query at a
  time. Shape the index so the answer is a seek: search keys its trigram index by repository
  (`repo_id << 40 | id`) for exactly this.
- **The plan guard is the floor, not the bar.** It catches a full scan or a temp B-tree in SQL; it
  cannot see a Go loop over pages, a per-row lookup, or a UI that walks every page of a list.
  Check those by reading the code path, and measure: time it on the dev server's data
  (`.telesphorepo-dev`; `admin-password.txt` signs in) before and after, and say the numbers.
- **Design from the property, not the query.** When a feature seems to need a whole set (counts,
  ordering, grouping), find the index or the counter that makes it a seek before writing it; a
  design that grows with the data needs the user's explicit yes, with the measured cost.
- **A change to a hot query, an index or a request path runs the scale zone here**
  (`go tool task test scale`) before it merges, and a new request path that a client hits often
  gets an operation in `e2e/scale_test.go`.

## Rules the code will not tell you

- **Migrations are append-only.** Production runs `main`, so never edit, rename or delete a
  migration file that `main` holds: add the next one. `serve` refuses a changed migration.
- **One migration per subsystem per branch, and every migration is idempotent.** While a branch
  is in work it may add small migrations where they fit. Before it merges, they are rolled into
  one file per component whose schema the branch changes (the kernel's `internal/db/migrations`,
  a module's own `migrations`): a module writes only the tables its own migrations create.
  Rolling them up changes migrations a dev database has applied, so delete the dev data folder
  (`.telesphorepo-dev`) after it. Each statement can run twice: `CREATE TABLE IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`,
  `DROP ... IF EXISTS`, `INSERT OR IGNORE`. SQLite has no conditional `ADD COLUMN` and a
  migration is plain SQL (`internal/db/migrate.go`), so new fields go in a new table keyed by the
  row they extend, never in an `ALTER TABLE ... ADD COLUMN`.
- **Tests are e2e only, against real systems.** No unit tests, mocks or fakes: run the real tool
  or service in a container. A test calls `t.Parallel()`, starts its own server, and never
  hardcodes a fact the code owns (versions, defaults, registry lists, limits): read it from the
  server or set it in the test.
- **A test earns its place in the suite.** It checks behaviour a user or client relies on, or a
  deliberate rule a later change could undo. A test written to chase a bug stays only when it
  pins such a rule; otherwise delete it once the fix is in. Never test a library, the standard
  library or a client tool's own behaviour, and look for a test that already covers the case
  before adding one: extend it rather than start another server.
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
