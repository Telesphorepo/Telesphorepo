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
| M1.1: DX — `go tool task dev/build/preview/test/doctor`, human CLI (`dev`, `user add/passwd`, `version`), Docker/Podman auto-detection, bun + Node 26 | done |
| M1.2: in-app encrypted settings (env is only `TR_DATA_DIR`, `TR_LISTEN`, `TR_MASTER_KEY`), Settings page, SPEC.md, AGENTS.md, short README | done |
| M2: OIDC admin sign-on (Keycloak or any OIDC IdP; settings in the Settings page's SSO section), §7.5 | done |
| M2.5: format plugins (Maven, Debian and `raw` are plugins), per-owner sqlc queries and migrations, plan guard, scale test (§5.4, §5.5) | done |
| M3: storage move (FS ↔ S3), Nexus sources with mirroring syncs and cut-over (§12) | done |
| M4: Svelte UI (Browse and Settings), file tree, upload and delete, repo description, audit log, by-hash cleanup, admin second-factor rule, dev mode (§13) | done |
| M3.5: publisher credentials: publish sessions as the only way to write, second factor (TOTP, passkeys, recovery codes), device keys, CI trust policies (OIDC), Gradle plugin and Maven extension in the clients repo (§7.6) | done |
| M5: plain-words UI pass, folder upload with a scan preview, per-repo IP rules (read and publish), client IP behind trusted proxies, IP range presets (§13.1) | done; the follow-ups below are not yet verified |
| M5.5: UI reorganisation (spec §13.3): one shell and one width, signing keys and IP lists as resources, Debian key per repo at `/debian/{repo}/key.asc`, no browser password popup, every page rebuilt | written; **not yet verified** (see below) |
| UI redesign (`docs/design/`): cards, no status rows, format icons from the plugins, Tree view, Use and Publish tabs, new Settings sitemap | built; `bun run build` is clean; **not committed, not clicked through by a person** (see below) |

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
3. After the first release, replace the `VERSION` placeholders in the publish-token snippets (`web/src/pages/account/Tokens.svelte`)
   and `README.md`.

## Known gaps / small follow-ups

- During a repo delete, an index rebuild that already writes, or an upload that already passed the gate, can leave an orphan blob after the repo row is gone. The row upsert fails and the blob stays. A new repo with the same name overwrites a blob at the same key. A fix needs a repo-level drain mechanism.
- The upload formats (maven, debian, raw) are a list in `web/src/pages/repo/upload/formats.ts`. `/api/formats` does not report which formats take uploads.
- Maven `-DdeployAtEnd=true` fails when more than `publish.session_ttl` passes between the first
  and the last module. The cause: `maven-deploy-plugin` fixes the credentials at each module's
  deploy step and uploads at the end. The client README states the limit.

## M5: verified

The M5 verification run passed. The changes below came after it.

### Added after the M5 verification run (not yet verified)

These changes came after the run. Nobody has compiled or run them:

1. **Trusted proxies.** The default `trusted_proxies` is empty. An untrusted peer that sends
   forwarded headers becomes a proxy candidate (in memory, at most 20).
   `GET /api/network/proxy-candidates` lists them, and the Reverse proxy page has "Trust this
   address". Tests: `TestClientIP`, `TestIPRules`, `TestPresets`.
2. **Browser downloads.** Format routes take the UI session cookie for GET and HEAD. Test: the
   subtest "a UI session on format routes" of `TestAPISessionRule`.
3. **Repo counters.** `repos.file_count`, `total_size` and `last_published_at`, kept by triggers on
   `assets` (edited into `0001_init.sql`). `GET /api/repos` returns them and Home shows them.
   Test: `TestRepoCounters`. `telesphorepo dev` recreates its data dir when a schema file changed; `serve` refuses to start with an old database.
4. **Folder upload filter.** For Maven and Debian the UI sends only candidate files to the scan
   and counts the rest in the preview's "left out" section.
5. **A folder on "Upload files".** The dialog says "That is a folder — use Upload a folder", and
   its button opens the folder upload with the dropped folder.

Verify in this order: `go build ./...`, `go vet ./...`, `go vet -tags e2e ./e2e/...`,
`telesphorepo dbcheck`, `bun run build`, the tests named above, then the whole core zone. Click
through the Reverse proxy page (behind Caddy), a download from a private repo, Home, "Upload a
folder" on a Maven build output, and a folder dropped on "Upload files".

Things to know:
- A search page can hold fewer items than `limit` when the read rules drop repos. Paging stays
  correct.
- The repo list shows only the caller's own access, because the API returns no other publisher.

## M5.5: written, pending verification

M5.5 replaces the M4 and M5 UI layout and the global signing key. Nobody has compiled or run it. The
design is §13.3 of the design spec. The system as built is in `SPEC.md` (**Signing keys**, **Network
rules**, **Web UI**). The route table and the UI rules are in `web/src/README.md`.

What changed:

- **Signing keys** are a table (`signing_keys`) and an API (`/api/signing-keys`). A Debian repo
  config names its key (`signing_key`, default `default`). `GET /debian/{repo}/key.asc` replaces
  `/keys/debian.asc`. The settings `signing.key`, `gpg.key_file` and `gpg.passphrase_file` and the
  CLI `keygen` are gone. Import through the API covers them.
- **IP lists** are a table (`ip_lists`) and an API (`/api/ip-lists`). The table `preset_ranges` and
  the rule kind `preset` are gone. Rules use `list`. `trusted_proxies` uses `list:<name>`, for
  example `list:cloudflare`. There are no list templates. `POST /api/ip-ranges/parse` reads
  pasted addresses, and the UI's `RangePaste` uses it.
- **No browser password popup.** The UI sends `X-Telesphorepo-UI: 1`. The server sends no
  `WWW-Authenticate` under `/api` or for such a request.
- **UI.** Every page uses one shell and one 1200 px container. The pages live in `pages/repo`,
  `pages/account`, `pages/repositories` and `pages/server`.

Verify in this order: `go build ./...`, `go vet ./...`, `go vet -tags e2e ./e2e/...`,
`telesphorepo dbcheck` (the plans of `ListRulesOfList` and `ListReposUsingSigningKey` are not yet
proven), `bun run build` in `web` (expect compile fixes), then the e2e core zone. The new tests are
`TestPresetIPLists`, `TestCustomIPLists`, `TestNoBrowserPasswordPrompt` and `signing_keys_test.go`.
Then click through every page in a browser, in light and dark mode and at phone width.

Known behaviour and open points:

- A plain download link on a private repo can still open the browser prompt after a session
  expires. Such a link cannot send `X-Telesphorepo-UI`, and `wantsChallenge` in
  `internal/auth/identify.go` then sends the challenge. A fix is to skip the challenge when the
  request has the session cookie or `Sec-Fetch-Mode: navigate`.
- The help text of the `storage` setting (`internal/settings/registry.go`) still uses technical
  words (`fs`, `s3`) that the Storage page shows.
- `default` is created only when the key table is empty. If an admin deletes it, a repo created
  without `signing_key` gets 400. The offline `seed` command needs a server that has started once.

## UI redesign: built, pending commit and verification

The redesign follows `docs/design/principles.md` (12 principles) and the sitemap in
`docs/design/information-architecture.md`. `docs/design/rationale.md` records each decision with
screenshots. `web/src/README.md` is the binding guide for UI code. `SPEC.md` (**Web UI**) describes
the result.

What changed in the code:

- **No status rows.** `StatusSummary`, `ConnectionCard`, `LoadingRows` and `ui/skeleton` are
  deleted. The components that replace them are `Section`, `Step`, `ConnectionTest` and `Loading`.
- **Format icons.** A format plugin implements `format.Iconic` and embeds an `icon.svg`.
  `GET /api/formats` returns `icon` and `kind_icons`. `FormatIcon` draws them.
- **Repository settings.** The Delete tab is gone; `DeleteRepo.svelte` is a dialog opened from the
  header.
- **Browse.** `TreeView.svelte` and `files/area.ts` (one box height for Tree, Columns and Cover
  Flow). The Use and Publish tabs are rebuilt.
- **Account.** `Profile.svelte`, `ReadTokens.svelte` and `PublishTokens.svelte` are gone. `Tokens`
  holds both token areas.

To do, in this order:

1. Run `go build ./...`, `go vet ./...`, `go vet -tags e2e ./e2e/...`, `bun run build` in `web`,
   then the core e2e zone. `TestAPIFormats` should check the `icon` field.
2. Click through every page in light and dark mode at 1440 and 390 px. The agents that built the
   pages saved shots only for some pages (see the "After" links in `rationale.md`). Pages with
   no saved shot: Storage, People (light and phone), Signing keys, Nexus migration, Activity, New
   repository, New CI pipeline.
3. Check Cover Flow for the builds of a snapshot. The demo data has no snapshot with several
   builds, so only `svelte-check` has covered that path.
4. Check the breadcrumbs of a sub-page of the first item of a group (New repository). Only
   `svelte-check` has covered them.
5. Add `password_changed_at` to `/api/me`, then show "changed N days ago" on the password row of
   `pages/account/Security.svelte`.
6. `AGENTS.md`, "Adding a repo format": add the step "implement `format.Iconic` and embed an
   `icon.svg`".
7. `web/src/README.md`: the routes table lists `/settings/tokens` as `PlannedPage` (the page is
   built), and the `SettingsGroupForm` row should name the `extraAt` prop.

## Suggested next step

1. Verify M5.5 and the UI redesign (above) and the changes added after the M5 run, then commit in logical chunks.
2. Next milestone: **M6**, single sign-on for developers (design doc §13.2). The questions to
   decide first are in that section: provisioning, device enrollment through SSO, and offboarding.

3. M3 decisions that stand: a cut-over stops on any failed file, so a file that can never import
   blocks it until it is fixed in Nexus or here. Sync history is kept (96 small rows per source per
   day at the 15m default).
