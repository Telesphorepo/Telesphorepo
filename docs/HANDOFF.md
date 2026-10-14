# Handoff — 2026-10-14

Where the work stopped on the Mac, so it can continue on another machine. When picking up in a new
Claude Code session, point it at this file plus `AGENTS.md`.

## State

M1 to M3 are on `main` and pushed. All three e2e zones are green on Windows 11 + Podman 5.8:
core (incl. a real Keycloak sign-in, `TestQueryPlans` and the storage-move tests), import (a real
Nexus 3 container) and scale (flat from 100k to 1M assets).

| Milestone | Status |
|---|---|
| M1: Maven (+ Gradle plugins) and Debian hosted repos, auth/tokens/permissions, FS + S3 storage, JSON API, UI shell, Dockerfile, e2e suite | done |
| M1.1: DX — `go tool task dev/build/preview/test/doctor`, human CLI (`dev`, `user add/passwd`, `keygen`, `version`), Docker/Podman auto-detection, bun + Node 26 | done |
| M1.2: in-app encrypted settings (env is only `TR_DATA_DIR`, `TR_LISTEN`, `TR_MASTER_KEY`), Settings page, SPEC.md, AGENTS.md, short README | done |
| M2: OIDC admin sign-on (Keycloak or any OIDC IdP; settings in the Settings page's SSO section), §7.5 | done |
| M2.5: format plugins (Maven, Debian and `raw` are plugins), per-owner sqlc queries and migrations, plan guard, scale test (§5.4, §5.5) | done |
| M3: storage move (FS ↔ S3), Nexus sources with mirroring syncs and cut-over (§12) | done |
| M4: full admin UI polish, deletes + blob/by-hash GC | designed |
| M3.5: publisher credentials: publish sessions as the only way to write, second factor (TOTP, passkeys, recovery codes), device keys, CI trust policies (OIDC), Gradle plugin and Maven extension in `clients/` (§7.6) | built, pending verification |

Design spec (source of truth for what's next):
`docs/superpowers/specs/2026-10-14-telesphorepo-mvp-design.md`. System as built: `SPEC.md`.

## Getting going on the PC (Windows)

1. Install Go 1.27+, [bun](https://bun.sh), Node 26 (`winget install OpenJS.NodeJS`), and Docker
   Desktop or Podman Desktop (only for tests and images).
2. `git clone https://github.com/Telesphorepo/Telesphorepo` and `cd` into it.
3. `go tool task doctor` — confirms Go, bun, Node and the container engine.
4. `go tool task dev` — open http://localhost:5173 with the printed credentials.
5. `go tool task test` — full e2e suite (first run pulls Maven/Gradle/Debian/SeaweedFS/curl images).
6. `go tool task test import` — Nexus source, sync and cut-over against a real Nexus container (slow).
7. `go tool task test scale` — optional 100k and 1M asset latency test.

## Not yet verified (do these early on the PC)

- **`go tool task dev` on Windows.** The e2e suite passes on Windows 11 + Podman 5.8 (rootless
  machine), but the dev loop (process-tree kill, colours) has not been run there.
- **The UI in a browser.** It builds and its API calls are covered by e2e, but no human has
  clicked through it yet (no UI tests, by design). M2.5 changed the repo create form, the
  config editor, the Usage panel and the Settings signing card.
- **Real R2.** The e2e suite no longer has R2 runs. R2 works through the generic S3 backend.
- **The M3 UI pages** (the storage move panel and the Nexus page) have not been clicked through.

## M3.5 status: written, not yet verified

The code, UI, clients, e2e tests and docs for M3.5 are in the working tree. Nobody has built,
vetted or run them yet. The orchestrator must do these checks in order:

1. `go build ./...`, `go vet ./...`, and `go vet -tags e2e|import|scale|clients ./e2e/...`.
2. `cd web && bun install && bun run build` (the UI and the QR encoder in `web/src/qr.tsx` have
   never compiled; scan one QR code with a real authenticator app).
3. `go tool task test` (core zone, including the publish tests and `TestQueryPlans`), then
   `go tool task test import`, then `go tool task test clients`.
4. Open the UI and click through Security, Devices, the approval page, Publishing, Trust
   policies and Publish log.

Things to know when the first run fails:
- The clients zone downloads `com.gradle.plugin-publish` from the Gradle Plugin Portal. The
  container needs network access. There is no Gradle wrapper, so the zone uses a Gradle image.
- The clients zone assumes that `jwebserver` is on the PATH of the Maven image.
- Passkeys need `base_url`. `telesphorepo dev` does not set it, so passkeys answer 409 there.
  Under `go tool task dev` the page origin is `:5173`, so the WebAuthn origin check would fail
  anyway.
- The design doc §7.6 still says `90d`. The built value is `2160h`. SPEC.md has the built values.
- `clients/` is MIT (`clients/LICENSE`). The server is AGPL-3.0 (`LICENSE`).

## Owner tasks before the first client release

The clients are published by a `clients-v<version>` tag, for example `clients-v0.1.0`. The owner
must do these steps first:

1. Add these GitHub repository secrets: `NEXUS_MAVEN_URL` (full URL of the hosted release
   repository), `NEXUS_USERNAME`, `NEXUS_PASSWORD`. The release goes to Nexus first.
2. Later, for the Gradle Plugin Portal and Maven Central: create the accounts, verify the
   namespace `com.telesphorepo`, create a GPG key, add the secrets listed in
   `.github/workflows/release-clients.yml`, and enable the commented jobs there.
3. After the first release, replace the `VERSION` placeholders in `web/src/pages/Publishing.tsx`,
   `README.md` and `clients/README.md`.

## Known gaps / small follow-ups

- Debian `by-hash/` files are never pruned (planned for M4).
- Maven `-DdeployAtEnd=true` fails when more than `publish.session_ttl` passes between the first
  and the last module. The cause: `maven-deploy-plugin` fixes the credentials at each module's
  deploy step and uploads at the end. The client README states the limit.

## Suggested next step

1. Verify M3.5 (above), then commit it in logical chunks.
2. M3 decisions that stand: a cut-over stops on any failed file, so a file that can never import
   blocks it until it is fixed in Nexus or here. Sync history is kept (96 small rows per source per
   day at the 15m default).
3. Start **M4**: full admin UI polish, deleting assets and deb packages, blob and by-hash GC.
