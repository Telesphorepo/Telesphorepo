# Handoff — 2026-10-14

Where the work stopped on the Mac, so it can continue on another machine. When picking up in a new
Claude Code session, point it at this file plus `AGENTS.md`.

## State

M1 to M2.5 are on `main` and pushed. The e2e suite is green on Windows 11 + Podman 5.8 (28 tests
incl. a real Keycloak sign-in and `TestQueryPlans`), and `go tool task test scale` is flat from
100k to 1M assets:

| Milestone | Status |
|---|---|
| M1: Maven (+ Gradle plugins) and Debian hosted repos, auth/tokens/permissions, FS + S3 storage, JSON API, UI shell, Dockerfile, e2e suite | done |
| M1.1: DX — `go tool task dev/build/preview/test/doctor`, human CLI (`dev`, `user add/passwd`, `keygen`, `version`), Docker/Podman auto-detection, bun + Node 26 | done |
| M1.2: in-app encrypted settings (env is only `TR_DATA_DIR`, `TR_LISTEN`, `TR_MASTER_KEY`), Settings page, SPEC.md, AGENTS.md, short README | done |
| M2: OIDC admin sign-on (Keycloak or any OIDC IdP; settings in the Settings page's SSO section), §7.5 | done |
| M2.5: format plugins (Maven, Debian and `raw` are plugins), per-owner sqlc queries and migrations, plan guard, scale test (§5.4, §5.5) | done |
| M3: `move` (FS ↔ S3) and `import nexus` | **next** |
| M4: full admin UI polish, deletes + blob/by-hash GC | designed |
| Future: publisher device keys + passkey/TOTP + short-lived minted tokens, Gradle plugin/CLI (§7.6) | designed |

Design spec (source of truth for what's next):
`docs/superpowers/specs/2026-10-14-telesphorepo-mvp-design.md`. System as built: `SPEC.md`.

## Getting going on the PC (Windows)

1. Install Go 1.27+, [bun](https://bun.sh), Node 26 (`winget install OpenJS.NodeJS`), and Docker
   Desktop or Podman Desktop (only for tests and images).
2. `git clone https://github.com/Telesphorepo/Telesphorepo` and `cd` into it.
3. `go tool task doctor` — confirms Go, bun, Node and the container engine.
4. `go tool task dev` — open http://localhost:5173 with the printed credentials.
5. `go tool task test` — full e2e suite (first run pulls Maven/Gradle/Debian/SeaweedFS/curl images).
6. `go tool task test scale` — optional 100k and 1M asset latency test.

## Not yet verified (do these early on the PC)

- **`go tool task dev` on Windows.** The e2e suite passes on Windows 11 + Podman 5.8 (rootless
  machine), but the dev loop (process-tree kill, colours) has not been run there.
- **The UI in a browser.** It builds and its API calls are covered by e2e, but no human has
  clicked through it yet (no UI tests, by design). M2.5 changed the repo create form, the
  config editor, the Usage panel and the Settings signing card.
- **Real R2.** The e2e suite no longer has R2 runs. R2 works through the generic S3 backend.

## Known gaps / small follow-ups

- Debian `by-hash/` files are never pruned (planned for M4).

## Suggested next step

**M3: `move` and `import nexus`** (design §11, §12). `import nexus` reads a Nexus export and
writes through each format's plugin. Keep Nexus-specific code out of the core. Add any new format
hook to `internal/format` as an optional interface.
