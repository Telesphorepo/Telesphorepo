# Handoff — 2026-10-14

Where the work stopped on the Mac, so it can continue on another machine. When picking up in a new
Claude Code session, point it at this file plus `AGENTS.md`.

## State

All of this is on `main`, pushed, and the e2e suite is green (25 tests incl. a real Keycloak
sign-in; R2 runs skipped without credentials):

| Milestone | Status |
|---|---|
| M1: Maven (+ Gradle plugins) and Debian hosted repos, auth/tokens/permissions, FS + S3 storage, JSON API, UI shell, Dockerfile, e2e suite | done |
| M1.1: DX — `go tool task dev/build/preview/test/doctor`, human CLI (`dev`, `user add/passwd`, `keygen`, `version`), Docker/Podman auto-detection, bun + Node 26 | done |
| M1.2: in-app encrypted settings (env is only `TR_DATA_DIR`, `TR_LISTEN`, `TR_MASTER_KEY`), Settings page, SPEC.md, AGENTS.md, short README | done |
| M2: OIDC admin sign-on (Keycloak or any OIDC IdP; settings in the Settings page's SSO section), §7.5 | done |
| M2.5: format plugins — make Maven/Debian ordinary plugins, add `raw` as the proof (§5.4) | **next** |
| M3: `move` (FS ↔ S3) and `import nexus` | designed |
| M4: full admin UI polish, deletes + blob/by-hash GC, scaling test | designed |
| Future: publisher device keys + passkey/TOTP + short-lived minted tokens, Gradle plugin/CLI (§7.6) | designed |

Design spec (source of truth for what's next):
`docs/superpowers/specs/2026-10-14-telesphorepo-mvp-design.md`. System as built: `SPEC.md`.

## Getting going on the PC (Windows)

1. Install Go 1.27+, [bun](https://bun.sh), Node 26 (`winget install OpenJS.NodeJS`), and Docker
   Desktop or Podman Desktop (only for tests and images).
2. `git clone https://github.com/Telesphorepo/Telesphorepo` and `cd` into it.
3. `go tool task doctor` — confirms Go, bun, Node and the container engine.
4. `go tool task dev` — open http://localhost:5173 with the printed credentials.
5. `go tool task test` — full e2e suite (first run pulls Maven/Gradle/Debian/SeaweedFS images).

## Not yet verified (do these early on the PC)

- **`go tool task dev` on Windows.** The e2e suite passes on Windows 11 + Podman 5.8 (rootless
  machine), but the dev loop (process-tree kill, colours) has not been run there.
- **Real R2.** Set `TR_E2E_R2_ENDPOINT`, `TR_E2E_R2_BUCKET`, `TR_E2E_R2_ACCESS_KEY_ID`,
  `TR_E2E_R2_SECRET_ACCESS_KEY` and run `go tool task test` to include the `r2` subtests.
- **The UI in a browser.** It builds and its API calls are covered by e2e, but no human has
  clicked through it yet (no UI tests, by design).

## Known gaps / small follow-ups

- Debian `by-hash/` files are never pruned (planned for M4).
- Changing a Debian repo's architecture list marks missing arches dirty, but removed arches keep
  their old index files.
- The Settings page's SSO hint shows `<browser origin>/api/auth/oidc/callback`, which is wrong
  when `oidc.redirect_url` or `base_url` is set to something else.

## Suggested next step

**M2.5: format plugins** (design §5.4). The goal is that adding Cargo/npm/PyPI/Yum later means one
new package plus one registration line. Today the core still names Maven/Debian in these places,
which M2.5 removes:

| Where | Coupling |
|---|---|
| `internal/db/migrations/0001_init.sql` | `CHECK (format IN ('maven','debian'))` |
| `internal/repos` | format constants; validates Debian config itself |
| `internal/api` | `/api/repos/{name}/debian/status`, `DebianStatus` interface, Debian config hook |
| `internal/server` | hand-wires `maven.New` / `debian.New` |
| `internal/settings/registry.go` | Debian settings group lives in core |
| `cmd/telesphorepo/dev.go` | creates maven/debian demo repos and prints their snippets |
| `web/src` (api.ts, Browser, Admin, Settings) | per-format snippets/config/status |
| `web/vite.config.ts` | per-format proxy list |

Acceptance: add the `raw` format touching only `internal/raw/`, `internal/formats/all.go` and
`e2e/raw_test.go`. If anything else needs editing, the abstraction isn't done. Do this before M3,
because `import nexus` would otherwise add more format-specific code to the core.
