# Handoff — 2026-10-14

Where the work stopped on the Mac, so it can continue on another machine. When picking up in a new
Claude Code session, point it at this file plus `AGENTS.md`.

## State

All of this is on `main`, pushed, and the e2e suite is green (24 tests; R2 runs skipped without
credentials):

| Milestone | Status |
|---|---|
| M1: Maven (+ Gradle plugins) and Debian hosted repos, auth/tokens/permissions, FS + S3 storage, JSON API, UI shell, Dockerfile, e2e suite | done |
| M1.1: DX — `go tool task dev/build/preview/test/doctor`, human CLI (`dev`, `user add/passwd`, `keygen`, `version`), Docker/Podman auto-detection, bun + Node 26 | done |
| M1.2: in-app encrypted settings (env is only `TR_DATA_DIR`, `TR_LISTEN`, `TR_MASTER_KEY`), Settings page, SPEC.md, AGENTS.md, short README | done |
| M2: OIDC admin sign-on (Keycloak or any OIDC IdP), §7.5 of the design | **next** |
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

- **Windows itself.** Everything was built and tested on macOS. `tools/task` (process-tree kill,
  colours), `internal/containerengine` (Podman named pipe) and the e2e harness compile for Windows
  but have never run there.
- **Podman.** Detection code exists but only Docker was available here.
- **Real R2.** Set `TR_E2E_R2_ENDPOINT`, `TR_E2E_R2_BUCKET`, `TR_E2E_R2_ACCESS_KEY_ID`,
  `TR_E2E_R2_SECRET_ACCESS_KEY` and run `go tool task test` to include the `r2` subtests.
- **The UI in a browser.** It builds and its API calls are covered by e2e, but no human has
  clicked through it yet (no UI tests, by design).

## Known gaps / small follow-ups

- Changing the Debian signing key doesn't re-sign existing `InRelease`/`Release.gpg` until that
  distribution next rebuilds. Fix: subscribe to signer changes and mark all Debian dists dirty.
- `internal/api` keeps its own copy of which setting keys are "storage backend" keys, because
  `settings.Def.storage` is unexported. Export it and delete the copy.
- Debian `by-hash/` files are never pruned (planned for M4).
- Changing a Debian repo's architecture list marks missing arches dirty, but removed arches keep
  their old index files.

## Suggested next step

Start M2 (OIDC admin sign-on). §7.5 has the full design: generic OIDC with PKCE via
`golang.org/x/oauth2` + `github.com/coreos/go-oidc/v3`, admins-only SSO, publishers stay local
users. Per §3, its settings (issuer, client id, client secret, admin claim/values) go into the
settings registry and the Settings page's SSO section, not env vars. E2E uses a Keycloak container.
