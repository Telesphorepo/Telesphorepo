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
| M4: Svelte UI (Browse and Settings), file tree, upload and delete, repo description, audit log, by-hash cleanup, admin second-factor rule, dev mode (§13) | done |
| M3.5: publisher credentials: publish sessions as the only way to write, second factor (TOTP, passkeys, recovery codes), device keys, CI trust policies (OIDC), Gradle plugin and Maven extension in the clients repo (§7.6) | done |

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

## M3.5 and M4: verified

All four e2e zones pass on Windows 11 + Podman 5.8: core (incl. the session rule: admin and
write routes need a signed-in UI session; passwords and read tokens only read), import, clients
and scale. The UI build runs svelte-check. CI on GitHub runs build, vet, the plan guard and the
core zone on every push, and publishes `ghcr.io/telesphorepo/telesphorepo:main` after a green push
to `main`.

Things to know:
- `index.html` has an inline script that sets the theme before the first paint. A future CSP needs a hash for it.
- The Publish page snippets use the browser address, not `base_url` from `/usage`.
- A whole-repo delete that races an in-flight upload or index rebuild can leave one orphan blob
  (its row write fails). A new repo with the same name overwrites it. Fixing it needs a
  repo-level drain of in-flight writes.

## Owner tasks before the first client release

The clients are published by a `v<version>` tag in the clients repo, for example `v0.1.0`. The
owner must do these steps first:

1. Add the GitHub repository secrets `NEXUS_USERNAME` and `NEXUS_PASSWORD` to the clients repo.
   The release goes to Nexus first.
2. Later, for the Gradle Plugin Portal and Maven Central: create the accounts, verify the
   namespace `com.telesphorepo`, create a GPG key, add the secrets listed in
   `.github/workflows/release.yml` of the clients repo, and enable the commented jobs there.
3. After the first release, replace the `VERSION` placeholders in `web/src/pages/settings/Publish.svelte`
   and `README.md`.

## Known gaps / small follow-ups

- During a repo delete, an index rebuild that already writes, or an upload that already passed the gate, can leave an orphan blob after the repo row is gone. The row upsert fails and the blob stays. A new repo with the same name overwrites a blob at the same key. A fix needs a repo-level drain mechanism.
- HTTP Basic with an admin password works on the JSON API with no second factor. The e2e suite depends on it. The new sign-in rule covers UI sign-in only.
- Downloads from a private repo ask the browser for a username and read token, because the format routes do not accept the UI session cookie.
- The upload formats (maven, debian, raw) are a list in `web/src/pages/browse/repo/RepoView.svelte`. `/api/formats` does not report which formats take uploads.
- `tools/task` prints its own dev banner. It lacks the line "2FA not enforced (dev)".
- Maven `-DdeployAtEnd=true` fails when more than `publish.session_ttl` passes between the first
  and the last module. The cause: `maven-deploy-plugin` fixes the credentials at each module's
  deploy step and uploads at the end. The client README states the limit.

## Suggested next step

1. Verify M3.5 (above), then commit it in logical chunks.
2. M3 decisions that stand: a cut-over stops on any failed file, so a file that can never import
   blocks it until it is fixed in Nexus or here. Sync history is kept (96 small rows per source per
   day at the 15m default).
3. Next milestone: **M5** (design doc §13.1).
