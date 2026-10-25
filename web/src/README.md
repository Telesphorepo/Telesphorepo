# Web UI

Svelte 5 + Vite (no SvelteKit), Tailwind CSS v4, shadcn-svelte (Maia style), TypeScript, bun.
`bun run build` type-checks with `svelte-check` and writes `web/dist`, which the Go binary embeds
(`web/embed.go`). The build also writes a `.br` and a `.gz` next to each text file of 1 KiB or more,
which the server sends to a browser that accepts them (see **Web UI** in SPEC.md). `bun run dev` serves the UI on :5173 and proxies every other path to the backend
on :8080. Run both with `go tool task dev`.

The UI is only a client of the JSON API (`/api/...`). It has no tests of its own: e2e tests cover
every API route it uses.

This file is binding. Every page follows the layout, the components, the interaction rules and
the checklist below.

## Structure

| Path | What it holds |
|---|---|
| `main.ts`, `App.svelte` | Mount; the shell: loads the user, the formats and the server identity (`lib/brand.svelte.ts`: `brand.name`, `brand.logo` from `GET /api/identity`; every place that names the server reads it), matches the route, checks access, renders the page in `AppShell`. |
| `modules/` | The UI of each server module (`<id>/module.ts`, one `ModuleUI`: pages with their sidebar items, redirects, repository settings tabs, repository field controls, format parts of the file details) and `index.ts`, the registry in the server's module start order. `types.ts` holds `PageDef`, `Access`, `RepoTabDef`, `RepoFieldDef`, `ModuleUI`. A module's own components sit next to its `module.ts` (the Signing tab, the signing key field, the Maven, Debian and Docker file parts), and so do the calls to a format module's own routes (`docker/api.ts`). |
| `app.css` | Design tokens: colors (light and dark), the type scale, fonts, the one radius, the width scale, the control widths, the scrollbars. |
| `pages/` | One component per route: `Home`, `Browse`, `Publish`, `Search`, `SignIn`, `DeviceApprove`; `repo/` (one page per repository), `settings/Overview` (the Settings overview; its parts `Stat`, `SizeBars` and `DownloadBars` in `settings/overview/`), `settings/repos/` (a repository's settings: the shell, its tabs and the Access tab's parts), `account/`, `repositories/`, `server/` (sub-folders `activity/`, `ci/`, `ip-lists/`, `nexus/`, `people/` hold the parts of those pages). |
| `lib/api/` | The one API client: `client.ts` (fetch wrapper, `ApiError`, the `X-Telesphorepo-UI` header, the XHR upload with progress), `types.ts` (JSON shapes), `index.ts` (`api.*` for every route outside the routes of a format module). |
| `lib/auth.svelte.ts` | `auth`: current user, sign in (two steps), sign out (returns to Home); the permission checks `can(perm)`, `canRepo(repo, perm)`, `managesRepo(repo)`, `allows(access, params)`. |
| `lib/permissions.ts` | The permission registry of the server (`GET /api/permissions`, read by `auth.load()` before the first page): the server and repository permissions in registry order with their labels and help, the needs of each (`needsOf`), the picker groups, one per subsystem (`pickerGroups`), the role of a server set (`serverRole`: member, administrator or custom), and the phrases of a set: `repoPhrase` ("Download, publish", "Everything"), `serverPhrase` ("Administrator", "Manage people, manage storage") and `listPhrase` ("download, publish and delete files"). |
| `lib/router.svelte.ts` | Hash router: `router`, `href()`, `navigate()`, `go()`, `isActive()`, `encodePath()`, `repoPath()`, `filesPath()`, `repoSettingsPath()`; the navigation guard hook. |
| `lib/current.svelte.ts` | `current`: the matched page of the module registry and the pending redirect of the current address. |
| `lib/crumbs.ts` | `Crumb` and `sidebarTrail()`: the trail of a Settings or account page that the way back of `PageHeader` reads its parent from, from the sidebar items of the module registry (an item named like its group is that one step: Settings › Repositories; an account page starts at its group: Your account › Devices). |
| `lib/fade.ts` | `fadeEdges`: the action of a row that scrolls sideways (the tabs), which fades the end where items are cut off. |
| `lib/unsaved.svelte.ts` | `unsaved` (the unsaved-changes guard) and `Draft` (a form's edits and what is saved). |
| `lib/view.svelte.ts` | `ViewChoice` and `viewLook`: List, Tree, Columns, Cover Flow, remembered per user. |
| `lib/load.svelte.ts` | `Resource` (one request), `Keyset` (a paged list), `chain()` (two keysets as one source: a format's hosted repositories, then its proxies), `RepoFormats` (the format of each repository a page names, read for those names only: `GET /api/repos?name=`), `listOf()` and `TableSource` (what `DataTable`, `ItemList` and `SourceGrid` read). `fetch()` of a `Resource` or a `Keyset` reloads and throws on an error: the `run` of a `Live`. |
| `lib/pick.svelte.ts` | `Pick`: the bulk pick of `DataTable` and `ItemList` (the picked keys, the header checkbox, Shift+click ranges, Esc); `openRow` and `rowClick`: a click on a row that opens it, outside its controls. |
| `lib/live.svelte.ts` | `Live` (a refresh on a timer: see **Live data**), `clock` (the time now, reactive each second), `pause` (a wait that holds while the tab is hidden or offline), `flash` (the action of `LiveValue`). |
| `lib/format.ts` | `formatSize`, `formatRelative`, `formatDateTime`, `formatCount`, `formatDuration` ("6 minutes"), `formatSpan` ("6 min", for a value that ticks), `formatEvery` (an interval after "every": "3 seconds", "10 minutes"). |
| `lib/words.ts` | `count` ("12 repositories"), `selectedText` ("12 selected"), `someNames` (the first five names "and 7 more"): the words of bulk actions; `kindPhrase` (the kind of a repository: "Hosted", "Proxy of a remote repository", "Group of 3 repositories"; a `RepoCard` shows it only for a proxy or a group). |
| `lib/brand.ts` | `serverName`: the name of the server as the UI shows it (the heading of the Settings overview). |
| `lib/utils.ts` | `cn()`: joins classes; it knows that `text-title` … `text-meta` are font sizes, not colors. |
| `lib/layouts/` | `AppShell` (top bar + container + the sidebar of Settings or Your account), `AddressNotice` (the server's address differs from the one in the browser), `TopBar`, `AccountMenu`, `GlobalSearch`, `SettingsNav` (the sidebar of either area), `UnsavedGuard` (the leave question). |
| `lib/components/` | The shared components (below). Pages build only from these and `ui/`. |
| `lib/components/ui/` | shadcn-svelte primitives. Add more with `bunx shadcn-svelte@latest add <name>`. |
| `lib/formats/` | `registry.svelte.ts` (`formats`: the UI descriptors of `GET /api/formats`, loaded once by `App.svelte`: titles, icons, config fields, upload rules, delete notes, file parts, remote presets, words) and `ConfigEditor` (a format's repo config as one field per entry of `config.fields`). Every format choice of the UI reads the descriptor; a format-specific component exists only as a named `formatParts` slot of the format's UI module. |
| `lib/glob.ts` | `globMatch`, `anyGlob`: the globs of the descriptors, matched like the server. |
| `lib/units.ts` | `splitSize`, `splitDuration`, `splitSeconds`: the number and unit that a form shows for a size or a duration; `joinSeconds` turns a number and a unit back into seconds. |
| `lib/remotes.ts` | `normalizeRemote`, `remoteError`: the remote address of a proxy. The well-known remotes are `remote_presets` of the descriptor. |
| `lib/network.ts` | `checkRange` and `rangeSize`: client-side checks of the addresses and CIDRs that network rules and trusted proxies hold. |
| `lib/webauthn.ts`, `lib/qr.ts` | Passkey create/get in the browser; the QR module matrix for authenticator setup (qrcode-generator). |

## Routes

URLs are hash URLs (`/#/path?query`), so the server never sees UI paths and the links that the
server and the clients print keep working: `/#/device?code=ABCD-EFGH`, `/#/settings/tokens`,
`/#/login?error=<code>`. A page gets `params` (from `:name` and `*rest` segments) and `query`
(a `URLSearchParams`) as props.

| Path | Page | Layout | Access | What it shows |
|---|---|---|---|---|
| `/` | `Home` | app | any | The front door, a short page centered in the container: one line of welcome ("Welcome to <server name>"), the search field large (the top bar's search: files by path, 48px tall, with the "/" shortcut; the top bar shows no search field on Home), then, when at least two would show, the doors: large cards side by side (stacked on a phone), each a 32px bare icon, a title, one sentence, and the whole card a link. **Publish** ("Set up a project to publish to this server.") opens `/publish`, for a signed-in person who can publish somewhere. **Browse** ("Find a package and see how to use it.") opens `/browse`. **Administer** ("People, repositories, storage and the rest of the server.") opens `/settings/overview`, only for a person who would see at least one card there (`adminNav` of the module registry). Each card is as wide as one column of a three-column row, so two cards keep that width, centered. The Publish door shows from the grants (`auth.canSomeRepo`). A visitor or a reader sees no doors: Browse alone says no more than the repositories under it, and the top bar has Sign in. Then the six repositories published last (`api.repos.recent`, for everyone) as a `plain` `Section` (a heading with "See all" at its end, opening `/browse`, over the cards): up to six `RepoCard`s (three on a phone). **Your repositories** are the ones where the person holds a permission beyond download (`perm`); a person with none sees **Repositories**, the ones they can see (a visitor: the public ones), from a second call. Loading, error and the empty states of Browse as any list. |
| `/browse` | `Browse` | app | any | Repositories, made for scanning: a toolbar on one row: the filter box (the start of the name, sent as `q` 250 ms after the last key), a format select ("All formats" first, then each format of the server with its icon; only with two or more formats), and New repository (All repositories) at the end; the repositories as one `DataTable` in a `Section`, 100 a page of `GET /api/repos`, the next page loading on its own as the end comes into view (`SourceMore auto`), to scan down: the format icon and the name (a lock after a private one), Format (only with two or more formats), Kind ("Proxy" or "Group"; once one is listed, a hosted repository's cell empty), and the description, muted on one line. A row opens the repository. On a phone a row is the icon and the name over the kind and the description. The same empty and error states as any list. |
| `/publish` | `Publish` | app | user | The first step of publishing: the hosted repositories the person can publish to (`GET /api/repos?perm=repo.publish&kind=hosted`, 200 a page with Load more; a group is not listed, since publishing to a group is publishing to one of its hosted members) as `RepoCard`s that open their Publish tab, a `Section` per format when more than one is present. With exactly one (and no next page), the page replaces the history entry with its Publish tab (`/repo/:name/publish`). With none, an `EmptyState` says to ask an administrator for publish access. |
| `/search?q=&repo=` | `Search` | app | any | Files whose path contains `q` (3+ characters), 200 hits a page, a repository's hits together (the repositories in the order they were created), grouped in the browser (`search/hits.ts`): a toolbar with a `RepoPicker` (`repo`) and the count of the matches from the first page ("5,444 files in 3 repositories", "10,000+" when a repository's count stopped there); with several repositories a line under it names each with its format icon and count, a button that searches only it; then a card per repository (its format icon, name and count of matches, and "Search only <name>" when several show), in it a row per folder that holds hits, parted by thin lines. The folders under one parent fold into the parent's row when there are two of them, or one and files of the parent's own, so an artifact is one row: the folder path (parents muted, the last step strong), its folders and files as links on the line under it (16 folders and 6 files, then "N more"; a file's size and its checksums in its tooltip), and "N folders · N files · modified" at the end. The checksum and signature files of a hit (`.md5`, `.sha1`, `.sha256`, `.sha512`, `.asc`, `.sig`) fold into it. The matched text is marked (`search/Highlight`) in every path. A row opens its folder in the Files tab; a link its folder or file. The next page loads on its own once the end of the results comes within a screen of the view (`SourceMore auto`). The top bar's search field holds `q`. |
| `/login?next=&error=` | `SignIn` | app | any | The one place to sign in: single sign-on, password, then the second step. From 1024px the felt ticket ("Admit one") stands left of the form; under it the flat mark heads the form. |
| `/device?code=&grant=&error=` | `DeviceApprove` | app | user | Look up a device code, check the device, approve it with a fresh second factor. A single sign-on account signs in again at its identity provider instead; the server returns with `grant` (the page approves at once) or `error`. |
| `/repo/:name` | `repo/RepoPage` | app | any | The repository header and the Files tab at the top folder. A rehearsal repository (the repo field `rehearsal_for`) says "Rehearsal for <name>" after its format, the name a link. A moving repository (the repo field `moves_into`) says "Moving into <name>", and for a holder of `nexus.manage` has Merge into <name>… in the header actions (it opens the merge plan). A group has the tabs too, read-only (the header says "Read-only: this group serves its members' files" where Upload would be): Files is the Files tab of a hosted repository of its format, with every view, the artifact and version pages and the file details, because the server answers the tree, the versions, the builds, the files and the snippets of a group from its members; a file or a version shows the member that serves it (`files/MemberTag` after its name, "Served by" in the details), Use lists its members first, and Publish shows to a person who publishes to one of its hosted members. While an operation holds the repository, the people who could upload or delete see its sentence as one muted line under the header (`PageHeader` `status`). A proxy has Files (its cached files) and Use, and no publish or upload. |
| `/repo/:name/files/<path>?version=&build=` | `repo/RepoPage` | app | any | Files tab: one panel (a card) with a toolbar (the folder path, Show details, the view) over the open folder. A folder (`path` ends with `/`) or a selected file. `version` names a version of the artifact folder (a Debian package keeps every version in one folder); `build` a chosen build, read by its id (`api.repos.build`) when the loaded pages of builds lack it. The first page of a plain folder is asked for together with its place (`placeOf` in `files/place.ts`), and the List view and the details column share it. The details column always shows on wide screens: the selected file, or else the open folder (repository, folder, artifact or version). |
| `/repo/:name/use` | `repo/RepoPage` | app | any | Use this repository: the address and the snippets for a build; a private repository adds a read-token step. |
| `/repo/:name/publish?to=` | `repo/RepoPage` | app | any (Publish on the repository; a group: on one of its hosted members) | Publish to this repository: from your computer, from CI, by hand. A group (`repo/GroupPublish`) first has a select of the hosted members the person publishes to (`to`, the first by default), then that member's Publish view. The Set up a project card (`repo/ProjectSetup`, with Install the CLI in its header) comes first: a fresh second factor, then the install and setup line for the OS (a single sign-on account returns with `?setup_grant=`), then `telesphorepo publish` or the native command (`publish` of the descriptor); the snippets stay behind "Set it up by hand". |
| `/setup/:code` | `Setup` | app | any | A setup link: run the install line in a terminal in the project folder. The code stays in the address fragment and the page calls no API. |
| `/install?os=&cpu=` | `InstallCLI` | app | any | Install the CLI: a tab per operating system (`os`: `linux`, `darwin`, `windows`; the browser's own when absent). Install (the one-line install of `/install.sh` or `/install.ps1`, and what it does with `PATH`), Install by hand (the archives of the system from `GET /cli/SHA256SUMS` as an `ItemList`: the archive name, then CPU · the start of the SHA-256 with copy, and Download; a row picks `cpu`, and the commands that download, check and unpack that archive follow it), Update (`telesphorepo update`). A build without CLI archives shows one empty state. The account menu and the Set up a project card of the Publish tab link to it. |
| `/repo/:name/upload?folder=` | `repo/RepoPage` | app | any (Publish on the repository, a password account) | Upload files or a folder. |
| `/settings/overview` | `settings/Overview` | settings | user | All settings, the administrator's home and the sidebar's first item, above the groups: about this server, not a second sidebar. The header: the server's name (`serverName`), its version as a badge (`GET /api/changes`), the public address under it (`base_url`, for a holder of `settings.general`), and, under the header, the actions people take most as doors (`DoorCard`), each only for a person who may: New repository, New account (the short form of People), New CI pipeline. Then one row of stat tiles (`overview/Stat`: each its own small card, centered across, the value in the title size, the label under it and one muted phrase, centered in the tile both ways; 6 columns from 1280px, 3 from 768px, else 2): Repositories (a link to Settings › Repositories), Files, Stored and People from `GET /api/overview` (counters the server keeps), CI pipelines (`GET /api/publish/trusts/count`) and Signing keys, each only for a person who can open its page and a link to it; a count that stopped reads "10,000+". A person without All repositories sees their repositories (more than download) under the heading "Your repositories". Then Largest repositories (`overview/SizeBars`: the eight largest, `largest` of `GET /api/overview`, each its format icon, name, a bar against the largest in `--chart-bar` on a muted track and the size; a tooltip with the share, the files and the last publish; a row opens the repository's settings, or its files without a settings tab). Then, for a holder of `activity.read`, Downloads ("The last 30 days, by day in UTC.", `GET /api/stats`): two `Stat`s (Downloads, Served), `overview/DownloadBars` (a column per day in `--chart-bar`, at most 24px wide with a 2px gap, round at the top; a hairline baseline and one at the top tick; a tooltip of the day under the pointer or the arrow keys; a hidden table for screen readers) and beside it from 1024px Most downloaded (an `ItemList` of five: format icon, name, bytes served, the count); no downloads is one `CompactEmpty`. Then Recent activity (an `ItemList` of the last five events, no address; See all opens Activity). Loading, error with Try again and the empty state per card. With no Settings page to open, the page replaces the address with `/account`. |
| **Your account** | | | | |
| `/account` | `account/Overview` | account | user | Overview (its sidebar label, under the group Your account): one identity card (avatar, name, role), then two columns (one under 1024px): your repositories, and your devices over your recent publishes, each a `Section` with an `ItemList` of at most five rows and "See all" (`/browse`, Devices, Tokens). |
| `/account/security?back=` | `account/Security` | account | user | Sign-in and security: password; the second-step methods as one `ItemList` (icon by type, name, added · last used, Remove); recovery codes. The only page of a setup-only session. |
| `/account/devices` | `account/Devices` | account | user | Devices as an `ItemList`: name, last used · expires, state badge, Revoke (the page description names the CLI, so no row repeats it). Holders of People see every user's, each with the person's avatar and name. |
| `/account/tokens` | `account/Tokens` | account | user | Tokens: read tokens and publish tokens, one page with two sections, each an `ItemList` (a read token: name, expires · last used, Revoke; a publish token: repository, from · started · ends, state badge). |
| **Settings › Repositories** | | | | |
| `/settings/repositories` | `repositories/AllRepositories` | settings | repositories | Repositories: every one for a holder of All repositories, else the ones where the person holds a permission that manages it (`GET /api/repos?perm=`, 100 a page with Load more). A row opens its settings (or its files when no tab opens). Picked rows (Edit settings): Make public, Make private; Delete (All repositories). |
| `/settings/repositories/new` | `repositories/NewRepository` | settings | repositories.all | First choice: a repository, a proxy of a remote repository, or a group of repositories; each offers the formats that take it (`kinds` of `GET /api/formats`). A repository: name, format, description, visibility, the format's options (`config.fields`), and a section per repository field of the format with the control of its UI module (the signing key). A proxy: the remote address (the `remote_presets` of the format as quick choices) and optional credentials; the server checks the remote, and "Create anyway" creates it when the remote does not answer. A group: its members, hosted repositories first, with the order as numbers. A proxy or a group then opens its settings Overview. |
| `/settings/repositories/:name/:tab` | `settings/repos/RepoSettings` | settings | repo | A repository's settings. Each tab shows only with its permission: Overview, Format settings and Remote need Edit settings, a module tab its `access` (the Signing tab: Edit settings); Access needs Manage access, Edit settings or CI pipelines, and answers from the saved state and the unsaved edits: two cards, each person in one of them only, by their highest access, with what they hold beyond it as badges (Delete files, Edit settings, Manage access): **Download** (the Public switch and Give access (Manage access) in its header; the people whose grant has no Publish, with Change, Remove from 640px and the pick, while private or when someone holds such a grant; then from where: "From any network" or "Only from …", Change opens the networks; the switch and Change need Edit settings) and **Publish** (the people with Publish the same way, then the holders of All repositories as one read-only row, "2 administrators", that opens to their names; the CI pipelines, each row the CI system and its repository, and from 768px the conditions as `TicketBadge`s, with Add CI pipeline and remove for CI pipelines; then from where; while nothing can publish, why, in place of the CI pipelines and the networks). A tab the person cannot open goes to the first one they can. Every kind's `overview` starts with the description, and for a holder of All repositories the name: a new name asks first and says that the old name keeps working as a redirect; it ends with Redirects (`RedirectsCard`, only while the repository has old names: an `ItemList` of each old name with "Used 3 minutes ago" or "Not used for 40 days", and Remove (All repositories) after a confirm that names what stops working and the last use). An old name in the address opens the settings under the repository's name. Hosted: `overview` (description, contents, its groups), `access`, `format` (the fields of `config.fields`), and the tabs of the UI modules that the format names in `repo_tabs` (`signing`). Proxy: `overview` (remote with Test the remote, use snippets, cache, its groups), `access` (download only), `remote` (address, credentials, the two times). Group: `overview` (use snippets, members with Move up, Move down, Remove and Add repositories, saved from the save bar), `access` (download only). Delete is a header action (All repositories); Open files is one too, except for a group. The way back: Repositories, on every tab. |
| **Settings › People and CI** | | | | |
| `/settings/people` | `server/People` | settings | people.manage | Every account as an `ItemList` row: avatar, name, its role as a badge (Administrator for every server permission, the phrase of its server permissions, or none), meta: email · signs in with (when it differs) · added. New account opens a short form (name and password); the person's page then opens. A row opens the person. Picked rows (not your own): Disable, Enable. |
| `/settings/people/:name` | `server/people/Person` | settings | people.manage | One person: the identity card (avatar, sign-in, email; the role shows once, in Server permissions) with the account actions; Server permissions as a role (`ServerPermissionPicker`: Member, Administrator, or Custom with one switch per permission; saved from the save bar after a confirm; read-only with Open sign-in options when single sign-on manages them); the repositories where the person holds a grant as an `ItemList` (format icon, name, the permission phrase as meta, Change and Remove), then one muted line, "Can also download from the N public repositories." (a link to Browse). Give access opens a short form for many repositories; Change edits one; picked rows: Change permissions, Remove access. Each repository change applies at once. |
| `/settings/ci-pipelines` | `server/CIPublishing` | settings | ci.pipelines | CI pipelines as a `CardGrid`: each card the name (Off when turned off), the CI system with the GitHub repository or Jenkins host (when the name does not say it), the conditions as `TicketBadge`s (`ci/ConditionBadges`: branches, tags, the workflow file, jobs; four, then "and N more"; "Every branch and tag" in the warning color), and up to three of the repositories it publishes to ("and N more"). The card opens the pipeline. |
| `/settings/ci-pipelines/new?repo=&back=` | `server/CIPublishingRule` | settings | ci.pipelines | A new pipeline in four steps: where the job runs; which runs (GitHub repository, branches, tags, workflow file; or Jenkins address and jobs; the token conditions behind a secondary action); repositories (each with its `FormatIcon`; a rehearsal repository right before the repository it rehearses, "Rehearsal for <name>: publish here first"); the workflow or Jenkinsfile step. The admission ticket (`ci/AdmissionTicket`) fills in as the form is filled: from 1280px it sticks beside the steps, under it it sits above Create. `repo` starts chosen (a repository that still copies from Nexus: its rehearsal repository in its place); Create and Cancel return to `back` when it is a `/settings/` path. |
| `/settings/ci-pipelines/:id` | `server/CIPublishingRule` | settings | ci.pipelines | Edit, turn off or delete a pipeline, with the same admission ticket (after the form under 1280px). |
| `/settings/networks` | `server/IPLists` | settings | network.manage | Networks (IP lists): two `CardGrid`s, downloaded lists (with Refresh) and your own lists; each card the title, "N ranges · used by …" and when it was refreshed or added, and opens the list. |
| `/settings/networks/new` | `server/IPListPage` | settings | network.manage | A new list: name, title, description, addresses pasted with `RangePaste`. |
| `/settings/networks/list/:name` | `server/IPListPage` | settings | network.manage | Edit, rename or delete your own list; a downloaded list's ranges and Refresh. The ranges are a paged `ItemList` of one line each (200 at a time, with copy, and Remove on your own list); "Used by" is an `ItemList` of what uses the list. |
| **Settings › Server** | | | | |
| `/settings/sign-in` | `server/SingleSignOn` | settings | signin.manage | Sign-in options in three steps: connect the provider (with Test), permissions from the provider (the group field and the groups, each with its server permissions as a role in a `ServerPermissionPicker`: `PermissionMapInput`), and how people sign in (password sign-in for everyone or for people who publish). |
| `/settings/signing-keys` | `server/SigningKeys` | settings | signing.manage | Signing keys as a `CardGrid`, with two keys or more a repository select over it ("All repositories", then each repository that uses a key, with its `FormatIcon`) that keeps the keys of that repository: each card the name, the last 16 characters of the fingerprint (copy takes all of it), the repositories that use the key, when it was added, Public key (download) and Delete. |
| `/settings/signing-keys/new` | `server/NewSigningKey` | settings | signing.manage | Create an Ed25519 key or import an armored key. |
| `/settings/storage` | `server/Storage` | settings | storage.manage | One state at a time: the storage settings with the connection test; a running move (where the files go, where storage is now, the progress and when the server last saved it, Cancel; no form); a failed or cancelled move (storage is still where it was, why the move ended, the failed files as an `ItemList` (the path with a copy button, the reason), Try again, Change the target with the form filled from the target, Keep); or a done move's success line over the form. |
| `/settings/backups` | `server/Backups` | settings | backup.manage | Backups: the backups in the backup folder, newest first, as `ItemCard`s in a `SourceGrid` (an `ItemList` when there are more than 12): the time, what it holds (with a folder store the files and their size, with S3 "Database" and the size of the database), the version, and the badges Incomplete and No master key; Back up now, the progress of a running backup or the time of the next one, the error of a failed one, and one sentence on what a backup holds; then the folder, the interval and how many to keep (`SettingsGroupForm`) with Test the folder. |
| `/settings/nexus` | `server/NexusMigration` | settings | nexus.manage | Nexus servers as `ItemCard`s in a `SourceGrid` (each opens its page): the name, the status badge, the address, the repositories, and the next check or, while it syncs, its progress line; Add a Nexus server. |
| `/settings/nexus/:id?sync=&repo=` | `server/NexusSource` | settings | nexus.manage | The status and its line; Sync now or Cancel the sync, Switch over everything, Delete. Repositories to copy (one pick table in set-up order: repositories and proxies, then groups with their members, then the ones this server cannot copy; each row with its state, which shows on a phone too (in the stacked row's meta line): a copied repository is Copying or Switched over, with the files its final sync did not copy as a link to that sync; its rehearsal repositories as a "rehearsal" link after its name, its moving repositories as a "moving" link, a merge into it as a "merge" link; a row actions menu (Switch over, Rehearse publishing, Move it project by project, per rehearsal Open it, Empty it, Delete it, and per moving repository Open it and Merge into <name>…, which shows only to a holder of All repositories too; a sync that is the final sync of a merge reads "Final sync of the merge into <name>" in the history and its details), each behind a confirm that lists its effects (Move it project by project is a short form: the name and the group `<name>-all`); a switched-over row cannot be unpicked; picking a group picks its members; "Copy N repositories" saves and starts the sync); the running or chosen sync with its progress (its repositories stay a `DataTable`), its log (an `ItemList` of the lines; `repo` filters it) and its failed files (an `ItemList`); the syncs (an `ItemList`: when it started, how · took · new files · size, a warning badge with the failed count, the state; a row opens its details); the connection. |
| `/settings/nexus/merge?from=&into=` | `server/NexusMergePlan` (`nexus/MergePlanView`) | settings | nexus.manage and repositories.all | The plan of a merge of `from` into `into` (`into` empty: the repository `from` moves into), one level under the Nexus server (Nexus migration › the server › Merge `from`): the counts of the files (copied by reference, skipped, rebuilt by the format, conflicts, blocked); the files the format cannot take (an `ItemList`; Start stays off); the conflicts as an `ItemList` (a label, an npm dist-tag, shows its title and both values; a file the path with copy, both sizes, dates and checksum starts, a compact `Segmented` Keep or Take per row; Keep all and Take all in the header, which also decide the conflicts past the first 1000); which name stays (two `OptionRow`s, each saying who changes what); what else changes (the final sync and the switch-over, the people, CI pipelines and groups that move, the delete of `from`, uploads waiting meanwhile); Start the merge, behind a confirm, opens the merge. The page is listed before `/settings/nexus/:id`. |
| `/settings/nexus/merges/:id` | `server/NexusMerge` (`nexus/MergeView`) | settings | nexus.manage and repositories.all | One merge: the state badge and who started it, a `LiveStatus` (every 2 s while it runs); its steps as an `ItemList` (done, running, failed or later; the copy with its counters and a progress bar); a failed one with Resume and Stop (All repositories; the repository operation routes); a done one says what changed (the merged repository, the redirect, the groups) and gives the setup line of the merged repository (`nexus/MergeSetup`, the Set up a project card of `repo/ProjectSetup`). |
| `/settings/edge` | `server/EdgeDelivery` | settings | edge.manage | Edge delivery: the status (On, Stopping or Off as the badge after its title; the Worker version, the last gap check with the next one, the home bucket, the last check of the Worker) with **Check now** (`POST /api/edge/check`; the page runs it on open and every 30 s while the Worker has not reported the server's version), the six R2 regions as `ItemCard`s in a `CardGrid` under a `plain` heading (the area, the state badge, the bucket (when it has one), pointers waiting when there are any, the last error, the Serve from it switch at the bottom; a filling region's card holds its progress and what it waits for in words, and the cadences sit under the grid), the Speed test (`edge/SpeedTest.svelte`: a file of `GET /api/edge/speedtest` or a pasted path, and where to read from: the nearest bucket, a ready region, the home bucket or the server; Run and Run all download it with a pass of `POST /api/edge/speedtest` and the region headers, and a ranked `ItemList`, fastest first, shows each bucket with whether the Worker took the choice, first byte · total, and the speed, the nearest and the fastest marked; only at the base URL), the settings (the on switch, the home region select and the access lifetime), and the Deploy section as numbered steps: the download of the edge Worker (`/api/edge/worker.zip`), unpack, then `bun install`, `bunx wrangler login`, `bun run provision` and `bun run deploy` (`CodeSnippet`s), when to repeat them, and the `wrangler.jsonc` of a checkout (`CodeBlock`) behind a `LearnMore`. One `Draft` and one `SaveBar`: Save checks the buckets first and asks before the server stops writing to one. |
| `/settings/proxy` | `server/ReverseProxy` | settings | network.manage | Network and proxy: the trusted proxies (none, this machine or network, the Cloudflare list, specific addresses, an `ItemList` of the other trusted proxies) and the detected proxies (an `ItemList`: the address, then header · requests · last seen, and Trust). |
| `/settings/general` | `server/General` | settings | settings.general | The settings groups with no page of their own (public URL, upload limits, logs, index and upload timing, publishing lifetimes, retention, the Nexus sync interval): one form, a section per group, with the server's labels and help. Server identity comes first: the name saves with the form; the logo (`general/LogoField`, the `control` of its field) is its own action that applies at once: a preview, Upload logo or Replace logo, and Remove after a confirm. |
| `/settings/activity?view=&area=&action=&repo=&actor=&when=&from=&to=` | `server/Activity` | settings | activity.read | Events (the audit log) and Publish tokens (the publish log: a table of Who, Repository, State, Started). Events: a toolbar with the area (`area`, a subsystem of `GET /api/audit/actions`, with the sidebar icon of the Settings page its permissions open), then, once an area is picked, what happened in it (`action`, its short list), a `RepoPicker` (`repo`), a `PersonPicker` (`actor`, from `GET /api/audit/people`) and the time (`when`: Any time, Today, Last 7 days, Last 30 days, Custom; Custom adds two date fields, `from` and `to`, both days included, and starts at the last 7 days; `activity/range.ts` turns them into `since` and `until` in the reader's time zone). The log filters by one action, so an area alone shows its actions as buttons in place of the events. The active filters show under the toolbar as `FilterChip`s with Clear filters. The events show by day (Today, Yesterday, then the date), each day an `ItemList`: the person's `UserAvatar`, what happened (a refusal with a Refused badge), then the person · the repository with its format icon · the facts in words (`activity/details.ts`: "with a password", "Public", "from Download to Everything", "12 files"; a key without words shows as its name and value), and at the end the `RelativeTime` (the full time in its tooltip) over the address (hidden under 640px). |

A sub-page with a `:name` or `:id` keys its content by the parameter (`{#key}` in the page file),
so the form starts again when the parameter changes. A network list opens at
`/settings/networks/list/:name`, so no list name can collide with `/settings/networks/new`. A
repository's settings always name the tab (`/settings/repositories/new/overview` is the
repository called "new"), so no repository name collides with `/settings/repositories/new`. Build
those links with `repoSettingsPath(name, tab)`.

Redirects (`redirects` of a `ModuleUI`, paths like pages; the query is kept): `/settings` →
`/settings/overview` (which goes on to `/account` for a person who sees no card there);
`/settings/account`, `/settings/security`, `/settings/devices` and `/settings/tokens` → the same
page under `/account` (the server prints `/#/settings/tokens` when a publish needs a publish session).

Access is checked in `App.svelte` with `auth.allows`, never in a page: `user` sends a signed-out
visitor to `/login?next=<path>`; a permission ID (`people.manage`, `storage.manage`, ...; a repository
permission such as `ci.pipelines` on at least one repository; a list of them needs every one, as the merge pages need `nexus.manage` and `repositories.all`) or `repositories`, `repo` (see `Access` in
`modules/types.ts`) shows "You cannot open this page" to a
person without it; and a setup-only session (no second factor yet) goes to the first page with `setup` (`/account/security`).
The repository page is the one exception: it checks the caller's permissions on that repository
(`repo.permissions`), because only the repository knows them.

### Add a page

1. Create `pages/<area>/<Name>.svelte` with `let { params, query }: PageProps = $props();`.
2. Add one `PageDef` to the `pages` of the owning module's `modules/<id>/module.ts` (a new module
   UI also gets a line in `modules/index.ts`, in the server's start order): `path`, lazy `page`,
   `layout` (`app`, `settings` for an admin page, `account` for a page of Your account), `access`,
   `title`, and `setup: true` for a page of a setup-only session.
3. For a Settings or account page, give it `nav` (its group, `account` for an account page; label, icon and order in the group). A sub-page
   (`/settings/networks/new`, `/settings/ci-pipelines/:id`) needs no nav item: its parent item
   stays open because `isActive` matches the prefix, and its way back is the item.
4. Add the route to the table above.
5. Call the API through `api.*` (`lib/api/index.ts`). A new server route gets a function
   and its types there first. A format module keeps the calls to its own routes in its own folder.

## Layout

- **One container.** `AppShell` puts every page in one centered container: `max-w-page`
  (1440px, `--container-page`), side padding 24px (`sm:px-6`), 16px under 640px (`px-4`). Every
  page uses it, the repository page included; the Browse pages (Home, Browse, a repository) use
  the whole container. Pages never set their own page width and never add side padding.
- **Top bar**, inside the same container: the server's mark (`ServerMark`: its logo, or the brand's mark) and name (Home) left; search centered (max
  480px); right: one **Sign in** button when signed out (hidden on `/login`), or **Settings** (for
  a person who can open an admin page: `adminNav` is not empty) and the avatar menu (Your account,
  Install the CLI, theme, Sign out) when signed in. Under 640px the search is an icon that opens
  `/search`. The search shows on Browse pages only: Home has its own large search field, and the
  Settings and account pages have none.
- **Settings and Your account.** Two areas with the same shell: a 240px sidebar inside the
  container, 40px gap, then the content column (about 1110px on a wide window). Settings is the
  admin panel: "All settings" is the first item, above the groups "Repositories", "People and
  CI", "Server". Your account (`/account`) has its own sidebar with one group, "Your account"
  (Overview, Sign-in and security, Devices, Tokens). Each item shows only to a person with the
  permission of its route, and a group without items is hidden (`visibleNav` of the module
  registry). Each group has a heading; groups are 24px apart with no line; every item has an icon
  and is 32px tall. The sidebar sticks under the top bar and scrolls on its own when the window is
  short. Under 768px the sidebar is a menu that the top bar's menu button opens. Every Settings
  and account page keeps this shell, a repository's settings included.
- **One content width.** Every Settings and account page uses the whole content column next to the sidebar:
  the page header, sections, tables and forms start and end at the same edges on every page.
  Pages never set a `max-w-*` on a block of their own. A page whose content is a few cards does
  not stretch one card across the column: the cards flow in a `CardGrid`.
- **Collections.** A collection is an `ItemList` by default: rows of two lines (the name with its
  badges, then one or two facts) in its `Section`'s card. A handful of things that each have a
  little more to say (keys, pipelines, regions, backups) is a `CardGrid` of `ItemCard`s. A
  `DataTable` only where people sort, compare columns, or scan rows of the same shape for the one
  they want (the Files tab, Browse, Settings › Repositories, the publish log). A row or a card shows what decides and acts
  (the name, the state, one or two facts, the action), not every field the API returns; the rest
  is on the thing's own page.
- **Logical containers** (principle 12). Each area of a page is a `Section`: a card with its
  title (and the area's actions at the header's end), 24px padding (20px on a phone), and 32px
  between cards on a Browse page, 28px in Settings. Inside a card, a form is a two-column `FormGrid`; a thin `<Separator />` may part
  sub-groups of one card. Never a line between cards.
- **Footer.** Under the container, every page has one quiet line, "Powered by Telesphorepo"
  (`text-meta`, centered), with the default identity and a custom one alike.
- **The way back** sits above the title of every page one level down (`PageHeader` builds it):
  one link, "← <parent>", to the page one level up (CI pipelines over the title acme/app,
  Repositories over a repository). A page of the sidebar has none: the sidebar is the way
  to it. There are no breadcrumbs, and the browser's back button returns to the previous view
  (every view has its own address).
- **No near-miss lines.** The page header, tabs and sections are separated by space and type,
  never by a full-width rule: a rule in the content nearly aligns with something in the sidebar
  and looks wrong. Lines stay inside a component (a table header, a card, a dialog).
- **State lives where the thing is** (principle 1). No status row and no summary banner at the
  top of a page or a section: a switch shows that it is on, a method shows in its list, a private
  repository has a lock, the one row that needs attention has a badge. A state of the whole
  repository that holds back its uploads (an operation) is one muted line in the header
  (`PageHeader` `status`), never an alert. The app has two notices. `EdgeNotice` sits under the
  header of Settings › Overview while the edge Worker needs a deploy, for a person who can open
  Edge delivery: it says why and links to the page. `AddressNotice` sits at the top of the container: a person who may change the general settings opened the app at an address
  that is not `base_url` (not under the Vite dev server, not at a localhost address, not when an
  environment variable sets it). "Use <address>" saves it; "Keep" hides it for that address in this
  browser.
- **Say it once** (principle 8). A fact shows in one place, as an icon or behavior where it can:
  a private repository has a lock and a credentials step, a public one has neither, and no text
  says that it is public.
- **The root reserves its scrollbar gutter** (`scrollbar-gutter: stable`), so a page with a
  scrollbar and one without have the same width.

## Tokens (`app.css`)

- **Type.** One sans family (Geist) for all text: titles, labels, repository names, paths,
  checksums, badges. One scale, as utilities from `app.css`: `text-title` (page title, 26/34
  semibold), `text-section` (section and step titles, 17/26 semibold), `text-label` (row labels,
  15/22 medium), `text-body` (body and tables, 15/22), `text-meta` (help, facts, timestamps, 14/20,
  always `text-muted-foreground`). The shadcn primitives' `text-sm` is the body size (15/22) and
  `text-xs` is 13/18, so buttons, inputs and menus follow the scale. Table headings and sidebar
  headings stay `text-xs font-medium text-muted-foreground`. Code in `CodeBlock` and `CodeSnippet`
  is 14px. Numbers in tables and checksums use `tabular-nums`. Sentence case everywhere; never all
  caps.
- **Contrast.** `--muted-foreground` (meta and help text) is at least 4.5:1 on `--background`,
  `--panel` and `--muted` in both themes (light 6.27 / 6.37 / 5.95, dark 8.69 / 8.35 / 6.59; on a
  card 6.84 light, 7.98 dark). So are `--success`, `--warning` and `--destructive` on the panel
  (light 4.80 / 5.05 / 4.78, dark 8.55 / 9.50 / 6.21). The ink of a `TicketBadge` is at least 8.7:1
  on both tones and the initials of a `UserAvatar` at least 6:1 on every hue.
- **Canvas.** The page background (`--background`) is a quiet warm tint of the brand's Paper in
  light and a deep navy from Carbon in dark; text is a Carbon ink in light and Paper in dark. A
  card (`--card`) is a clean surface that lifts off it: white in light, a lighter navy in dark.
  Every quiet fill (`--hover`, `--stripe`, `--panel`, `--muted`, `--border`, `--input`, the
  scrollbars) takes the hue of its canvas. See **Canvas colors** in `branding/README.md`.
- **Monospace** (Geist Mono) only inside `CodeBlock` and `CodeSnippet`, and in the JSON editor
  of format settings (an editable code block). Nowhere else: not for names, paths, checksums,
  labels, badges, codes or input values.
- **Spacing.** Tailwind's 4px scale only. Browse pages (home, a repository, search) are airier
  than Settings pages, which stay a little denser: `AppShell` sets `data-layout` on the container
  and `app.css` sets the variables below for each layout (Browse / Settings). The way back to title
  12px; page header to content `--header-gap` (40 / 32px, `PageHeader` does it); tabs to content
  `--tabs-gap` (32 / 24px, `TabNav`). Between sections: `--section-gap` (32 / 28px, `Section` does
  it). Inside a section: 24px padding (20px under 640px), 24px from the header to the content and
  from the content to the footer. Form grid: 24px between cells (`FormGrid`), 8px from a label to
  its control and from the control to its help (`FormRow`). Buttons in a row `gap-2`; toolbar to
  table `--toolbar-gap` (20 / 16px, `Toolbar` does it). Do not add other margins between these
  blocks.
- **Edges.** Inside a card every line of text and every control starts and ends on the content
  edge, the title's edge (`app.css`, Edges). Rows that fill on hover or when chosen (`ItemList`,
  `DataTable`, a list of `OptionRow`s) reach 12px into the card's padding, so their text stays on
  that edge and their fill sits 12px inside the card on every side. A ghost or link button at the
  end of a header (`Section`, `Step`, `PageHeader` actions) or a row, or at the start of an
  `ItemCard`'s actions, reaches out by its 13px inset, so its label meets the edge. A grid of
  cards sits in a `plain` `Section`, never in a card. Pages never add padding or margins to line
  things up: the shared parts do it.
- **Control width.** Every text control fills its cell of the form grid, so every text control
  in a grid has one width. That covers an input, a select, a `ListInput`, a `SecretInput`, and a
  number with its unit (`UnitInput`: one control, the unit is a menu at its end). A field that
  needs the whole width spans both columns (`FormRow full`). Do not pass `width` and do not size a
  control in a page. Only a control that sizes itself sits at the start of its cell with
  `width="auto"`: a switch (`SwitchRow`), a checkbox, a button.
- **Rows.** A `DataTable` row is one line, `h-row` (`--row`: 48px on a Browse page, 44px in
  Settings); its heading row is `--row-head` (40 / 36px). Zebra stripes use `--stripe` (between
  the background and `--hover`), so a stripe and a hover never look the same. An `ItemList` row is
  at least `h-row`, has two lines when it has a meta line, and has no stripes: thin lines part
  the rows.
- **Scrollbars.** The page and every inner scroll area use the themed scrollbar from `app.css`:
  a rounded thumb (`--scrollbar-thumb`) that darkens in light and lightens in dark under the
  pointer (`--scrollbar-thumb-hover`, `--scrollbar-thumb-active`), on a track that is the surface
  itself. Do not style scrollbars in a component.
- **Radius.** One radius, `--radius` (8px). Every `rounded-*` step resolves to it. `rounded-full`
  only for avatars, dots and progress bars. The one other shape is the ticket's: `TicketBadge`
  has the corners and notches of the mark at its 24px height (3px corners, 4px notches).
- **Color.** Use the tokens: `bg-background`, `text-foreground`, `text-muted-foreground`,
  `border`, `bg-card` (the surface of a `Section`), `bg-panel` (quiet fill: details column, code,
  drop zone, help), `bg-hover`,
  `bg-stripe`, `bg-selected` + `text-selected-foreground`, `bg-primary` (brand yellow; main
  buttons), `text-link` (links in text), `text-destructive`, `text-success`, `text-warning`, `bg-chart-bar` (the one series of a bar chart: Gold Deep in light, a gold in the chart lightness band in dark, at least 3:1 on the card). The
  ticket parts have their own: `bg-ticket` + `text-ticket-foreground` and `bg-ticket-gold` +
  `text-ticket-gold-foreground` (`TicketBadge`), `bg-ticket-paper` and `--ticket-shadow` (the admission ticket), and
  `--avatar-1..6` with `--avatar-ink` (`UserAvatar`); only those components use them. No raw
  colors. Formats have no tint: their icon is the mark.
- **Ticket motifs.** A ticket shape (a `TicketBadge`, a `Perforation`, the admission ticket) marks
  only what admits or is admitted: CI pipelines and their conditions (a branch, a tag, a workflow
  file, a job, the CI system), publish tokens. Nowhere else, and never as decoration: a state is a
  `StatusBadge`, sub-groups part with a `Separator`, a list is not a row of tickets. The brand
  illustrations follow their own rules: the outlined ticket of `EmptyState`, the stub of a
  `UserAvatar`, the `FeltTicket` of the ticket moments and the sign-in page.
- **States.** Hover and selected fill the whole element: `bg-hover` under the pointer,
  `bg-selected` for the open or chosen element (sidebar item, tab, selected table row).
- **`cn()`** knows the type scale: `cn("text-meta", "text-muted-foreground")` keeps both classes.
- **Icons** (principle 9) mark meaning: a format, a lock, a state. An icon sits bare, in the text
  color of where it is, never in a colored or tinted square beside a heading or a row. A format's
  icon comes from its plugin (`FormatIcon`), never from Lucide. Sizes: 20px at the start of an
  `ItemList` row and in an `ItemCard`, 24px for the format of a `RepoCard`, 32px for the doors of
  Home.
- **Icons in a select.** A choice of a format or a repository shows its `FormatIcon`, a choice of
  a person a small `UserAvatar`, and a state its meaning icon where the UI has one (the lock of
  Private); plain values (units, numbers, words) and the "All …" row show none. The icon goes in
  the `icon` snippet of `Select.Item` (a 16px slot before the label), and in the `icon` snippet of
  `Select.Trigger` for the chosen item, at the same place. In a list whose items have icons, a row
  without one (`All formats`, `Another file`, `Public`) passes `inset`, so every label starts on
  one line. A trigger with no chosen icon shows the label alone.

  ```svelte
  {#snippet chosenIcon()}<FormatIcon {format} />{/snippet}
  <Select.Trigger aria-label="Format" icon={format ? chosenIcon : undefined}>…</Select.Trigger>
  <Select.Content>
    <Select.Item value="" label="All formats" inset />
    {#each present as f (f)}
      <Select.Item value={f} label={formats.title(f)}>{#snippet icon()}<FormatIcon format={f} />{/snippet}</Select.Item>
    {/each}
  </Select.Content>
  ```
- **Loading** (principle 11). No skeletons. `Loading` shows nothing for 300 ms, then a quiet
  spinner and "Loading" in the space the content will take. A reload keeps the current content on
  screen: test `loading && !data`. Switching views keeps the same area.

## Components (`lib/components/`)

| Component | Use |
|---|---|
| `PageHeader` | `title`, `description?` (one sentence), `crumbs?`, snippets `meta?` (badges next to the title), `status?` (one muted line under the title: an icon and one sentence, for a state with no other place on the page; the repository page uses it while an operation holds the repository), `actions?`. The page's only h1, with the way back above it: "← <parent>", the last step of the page's trail that links elsewhere. On a Settings page the trail comes from the sidebar (Settings › group › item); `crumbs` replaces the steps after the group when the page sits deeper (`[{label: "maven", href}, {label: "Access"}]`). A Browse page passes `crumbs` (its whole trail). A page of the sidebar shows no way back. |
| `Breadcrumbs` | `items: Crumb[]` (`{label, href?}`; the last is the current folder), `label?` (the accessible name). A place inside a repository: the Files tab's folder path, in the toolbar of its panel: body text after a folder icon, slashes between the steps. |
| `Toolbar` | The row above a collection: filters as children, the main action in `end`. |
| `ItemList` | The default for a collection: `source` (a `Keyset`, or `listOf(resource)`), `caption`, `key`, `href?` (whole-row click; the title is the link), `selected?`, snippets `leading?(item)` (a 20px icon or an avatar), `title(item)` (the name: one line that truncates; put no link in it), `badges?(item)` (after the title), `meta?(item)` (the muted line under it: one or two facts joined by " · "), `trailing?(item)` (a state badge, a switch, the row's actions), `empty?` (a `CompactEmpty`), `emptyText?`. Rows in the `Section`'s card, parted by thin lines, no column headings, no stripes, at least `h-row` tall. Loading, error with Try again, empty and "Load more" as `DataTable`. Bulk actions with the API of `DataTable` (`bind:picked`, `pickable?`, `pickLabel?`, `pickOnRow?`, `pickOff?`, `selectAllLabel?`, snippet `bulk`): a checkbox at the start of each row, and a head row with the checkbox that picks every loaded row ("Select all 5 rows"; none for a single pickable row), over which the selection bar lies while rows are picked. Inside a card the rows reach 12px into its padding (see Edges). |
| `CardGrid` | `label?` (accessible name), `max?` (the most columns), `class?`; `ItemCard`s as children. A grid whose columns are at least 300px, 16px apart, one column on a phone. For a handful of things that each have a little more to say than a row. |
| `ItemCard` | `title` (up to two lines), `href?` (the whole card is the link), `titleClass?`, snippets `icon?` (bare, 20px), `badges?` (after the title), `actions?` (the row at the bottom: the card's actions, or a closing fact); up to three fact lines as children, each a `<p>` with a short phrase (never a label and a value). Its own frame, so it reads inside a `Section` too; cards of a row are as tall as the tallest, their bottom rows aligned. Buttons in `actions` keep their own click. |
| `SourceGrid` | `source`, `label`, `key`, `max?`, snippets `card(item)` (an `ItemCard`) and `empty?`, `emptyText?`, `class?`. A `CardGrid` over a source, with the loading, error, empty and "Load more" states of `ItemList`; the empty state sits in a card, since the grid's `plain` Section has none. |
| `PickBar`, `PickBox`, `SourceError`, `SourceMore` | The shared parts of `DataTable` and `ItemList` (the selection bar, a pick checkbox, the error with Try again, the trailing error and "Load more", or with `auto` the next page on its own as the end comes into view: Search). Pages use the collections, not these. |
| `DataTable` | A table, for rows of one shape that people sort, compare by column or scan by the hundred, at the full content width: `source` (a `Keyset`, or `listOf(resource)`), `columns` (`{label, srLabel?, align?: "right", grow?, hideOnPhone?}`), `caption`, `key`, `href?` (whole-row click), `selected?`, snippets `row(item)` (the cells, as `Cell`s) and `empty?` (a `CompactEmpty`). Zebra stripes, one fixed row height (`h-row`, one line), rounded row ends, a fill on the whole row under the pointer, the selected fill on the `selected` row. Under 640px a row stacks (`app.css`): the `grow` cell (the name) on the first line, the other cells that show on a phone as one muted meta line parted by " · ", and no column headings, so nothing is cut off or overlaps. Loading (`Loading` in the space of four rows, after 300 ms; a reload keeps the current rows) and error (a row with Try again) show under the headings; an empty table is never drawn: `empty` shows in its place, without column headings. A `Keyset` gets "Load more". Bulk actions: `bind:picked` (a `Set` of row keys), `pickable?(item)`, `pickLabel?(item)` (the checkbox's name) and snippet `bulk` (the actions). Each row then gets a checkbox in a first column, a 40px hit target over the full row height; a row that is not pickable has an empty cell, and a collection with no pickable row has no checkbox column. The header checkbox picks every pickable loaded row and clears them on the next click ("Select all 5 rows", or "Select the 50 rows shown" for a keyset with more to load; a table with one pickable row has none). While rows are picked, a bar of the header's height lies over the headings: "12 selected", the `bulk` actions, "Select all 50", "Clear". Shift+click picks a range; Esc clears the pick when no dialog is open. `pickOnRow` (a table without `href`): a click anywhere on a pickable row, outside its controls, toggles its checkbox. `pickOff`: the checkboxes show, turned off. `selectAllLabel`: the text of the "Select all" button. `picked` is not `selected`. After an action, reload the table, empty `picked` and say what changed in a toast. |
| `Cell` | One cell of a `DataTable` row: `text?` (one line that truncates, full value in a tooltip), `copy?` (`true` or the value to copy: a copy button that shows on row hover), `copyLabel?`, `grow?` (the column that takes the free width, at least 10rem), `num?` (numbers, sizes, times: right-aligned, tabular numerals), `actions?` (the row's buttons, right-aligned), `strong?` (the row's name), `muted?`, `hideOnPhone?`. Children for anything else (a badge, a `RelativeTime`). Use `Table.Cell` only for a control that `Cell` does not cover, and keep it one line. |
| `Truncate` | `text`, `copy?`, `copyLabel?`, `class?`: one line that never wraps; "…" and a tooltip when it is cut (a path breaks after its slashes in the tooltip); an optional copy button. `Cell text` uses it; use it directly for a long value outside a table. |
| `DoorCard`, `DoorRow` | `DoorCard`: `title`, `text` (one sentence the title does not say), `icon`, and `href` (a link) or `onclick` (a button). A door: a large card with a 32px icon in the mark's gold (`--mark`). `DoorRow` (`label`) holds them, each one column of a three-column row, centered, stacked on a phone. The places to start: Home's Publish, Browse and Administer, and the admin home's New repository, New account and New CI pipeline. A page's own action stays a button in its header. |
| `RepoCard` | `repo`, `href`. A repository as an `ItemCard` (Home, Publish), the same fields in the same places for every kind: the format icon (24px), the name (`text-section`) with a lock after a private one, the description up to two lines, and at the bottom the kind of a proxy or a group in plain words (`kindPhrase` in `lib/words.ts`; a hosted repository, the default, has no such line). The whole card is the link. No counts, sizes or times. Put them in a `CardGrid`. |
| `RepoTable` | The repositories table for the people who manage them (Settings › Repositories): name, a lock after a private one, the kind icon after a group or a proxy, and the description on one line where at least 8rem of it fits; format, last publish (files and size are on the repository's Overview). On a phone the format icon and the name, with the last publish on the meta line. |
| `RepoKindIcon` | `kind`, `iconClass?`, `class?`. The mark of a group (layers) or a proxy (cloud download), with the word "Group" or "Proxy" for screen readers; nothing for a hosted repository. |
| `Section` | `title?`, `description?` (one sentence), snippets `badges?` (after the title: the state of the whole area, such as On or Off on Edge delivery's Status), `actions?` (the area's actions at the header's end; on a phone they wrap under the title, except one short button such as "See all", which stays on the title's line) and `footer?` (a submit row), `class?`. One logical area of a page on its own surface (a shadcn `Card`): 24px padding, `--section-gap` to the next section. A page with a table or a list puts it in a `Section` too: the section stays the card. `plain` drops the card for an area whose content is a `CardGrid` (Home's repositories, the regions of Edge delivery), so no card sits inside a card: the heading with its actions at the end on one line (also on a phone), 16px over the grid, the same `--section-gap`. |
| `FormPage` | The body of a form page. It sets no width: every Settings page uses the full content column. |
| `FormSection` | `title?`, `description?`, snippets `actions?` and `footer?` (the submit row). A `Section` that holds a form: put one `FormGrid` in it, or two with a `<Separator />` between sub-groups. |
| `FormGrid` | The one form grid: holds `FormRow` and `SwitchRow` in two columns (`sm:grid-cols-2`), 24px apart; one column under 640px. Each label sits above its control and every control fills its cell, so there is no label column. |
| `FormRow` | `label`, `for`, `help?` (one sentence; id `{for}-help` for `aria-describedby`), snippet `more?` (longer help behind "Learn more"; it opens under the whole row), `aka?` + `akaLabel?` (the technical name: "In your provider: Client ID"), `full?` (span both columns), `width?` (leave it out: the whole cell; `"auto"` only for a switch, a checkbox or a button), `error?`, snippet `badge?`, `length?` + `max?` (a text control with a limit: "12 of 500" at the end of the help line, id `{for}-count`; point `aria-describedby` at it too). The label above, the control, then the help, so the controls of a grid row line up. |
| `UnitInput` | `id`, `bind:num`, `bind:unit`, `units` (`SIZE_UNITS`, `DURATION_UNITS` or `{value, label}[]`), `label?` (names the unit menu), `placeholder?`, `disabled?`, `invalid?`, `describedby?`, `onchange?`. A number and its unit as one control of the full control width; the unit is a menu at its end. Never two controls side by side. |
| `LearnMore` | `label?` ("Learn more"), `bind:open?`; the longer help is the child. A quiet disclosure for help that does not fit one sentence, anywhere outside a `FormRow` (which has `more`). The panel takes the full width of its column, like the controls around it. |
| `SaveBar` | `count` (unsaved changes; 0 hides it), `onSave` (async; an error shows in the bar), `onDiscard`, `saveLabel?`. "2 unsaved changes · Discard · Save", sticky at the bottom of the window over the content column, only while there are changes. Ctrl+S (⌘S) saves. While it shows, leaving the page asks first. Put it last on the page. Pair it with a `Draft`. |
| `SwitchRow` | `id`, `label`, `help?`, `bind:checked` or `checked` + `onCheckedChange`, `disabled?`, `onText?` / `offText?`, snippet `badge?`. A yes/no setting: the switch and its state word in the control column. Never a bare checkbox. |
| `ListInput` | `id`, `values`, `onchange(values)`, `placeholder?`, `normalize?`, `invalid?`, `describedby?`, `label?`. Short values as chips in one box: Enter or a comma adds, a paste adds every line, Backspace removes the last. Never a "one per line" textarea. |
| `SecretInput` | `id`, `set` (a value is saved), `change` (undefined, a new string, or null to clear), `onchange`, `disabled?`, `canClear?`, `invalid?`, `describedby?`. A box with the look of an input: "Set · hidden" or "Not set", with Replace, Clear or Set. |
| `CredentialsInput` | `id`, `set`, `change` (undefined, `{username, password}`, or null to remove), `onchange`, `disabled?`, `invalid?`, `describedby?`. A saved username and password for another system (the remote of a proxy), with the look of `SecretInput`: "Set · hidden" or "Not set", with Replace (a username field and a password field), Clear or Set. |
| `FormField` | `label`, `for`, `hint?`, `error?`; the control is the child. Only inside a dialog (`ShortFormDialog`), where the form grid does not fit; a page uses `FormGrid`. |
| `ConnectionTest` | `onTest`, `testing?`, `result?` (`{ok, text}`), `label?` ("Test connection"), `disabled?`. The **Test** action of a connection to another system, where the connection is configured (the header or the footer of its `Section` or `Step`). The result shows beside the button, only after a test: a check and what answered, or the error and the next step. |
| `Step` | `n`, `title`, `description?`, `done?`, snippets `actions?` and `footer?`. One numbered step of a setup, as a `Section` whose title starts with its number; a done step shows a check in place of the number. |
| `CompactEmpty` | `icon?`, `title?`, `state?`, `text`; children are the one action. An empty list or a feature that is not set up, as one row: a plain icon, "Passkey · Not set up", one sentence, one action. Pass it as the `empty` of an `ItemList`, a `SourceGrid` or a `DataTable`. Inside a card it is a plain row; outside a card it has a dashed frame. Several rows in one card are parted by thin dividers, never a frame each. |
| `FeltTicket` | `top`, `bottom`, `serial`, `label`, `moment` (`none`, `print`, `punch`, `tear`). The brand felt ticket with printed lines, for the ticket moments only, at 320px wide or more. |
| `ConfirmDialog` | `bind:open`, `title` (a question), `description?`, `changes: string[]`, `confirmLabel`, `destructive?`, `typeToConfirm?` (a field; the confirm button stays off until it holds this text: the name of the one thing, or the action of a bulk delete, "delete 12 repositories"; never a bare count), `onConfirm` (async; errors stay in the dialog). A bulk delete names the count and the first five names (`someNames` in `lib/words.ts`): "Delete 12 repositories? oasis, plex, scissors, enginehub, worldedit and 7 more." |
| `ShortFormDialog` | `bind:open`, `title`, `description?`, `submitLabel`, `canSubmit?`, `onSubmit` (async), `wide?` (768px in place of 512px, for lists of choices that each have a line of help: the grant dialog, whose finder and permissions stand side by side from 768px); at most five `FormField`s as children. |
| `DetailsColumn` | `title`, `onClose`, snippet `actions?`: the details of the selected row in a 360px column right of the table. On the Files tab the column always shows (the selected file, or else the open folder); Close hides it until the person selects a file or clicks "Show details". |
| `TabNav` | Tabs that are pages (each tab has its own address): `label`, `items: {label, href, active, icon?}[]`. The open tab fills like a sidebar item; no line under the row; `--tabs-gap` to the content. On a narrow screen the row scrolls sideways with no scrollbar, the open tab in view, and the end where tabs are cut off fades (`fadeEdges` in `lib/fade.ts`). |
| `CodeBlock` | `code`, `title?`, `diff?`, snippet `actions?` (at the end of the title row): multi-line code with a copy button (monospace). With `diff` (diff lines, each after its mark: `+` added, a space for context) it shows the change to a file the reader has: added lines marked and filled, context muted; copy takes the added lines only, and a selection never takes the marks. `repo/snippets/SnippetBlock` shows a CI file this way, with Add to a workflow / New workflow. |
| `Segmented` | `bind:value`, `options` (`{value, label, title?}[]`), `label`, `compact?`: two or three short choices as one segmented control (a radio group); arrow keys move. It fills its cell; `compact` is as wide as its labels, for the title row of a `CodeBlock`. |
| `CodeSnippet` | `value`, `label?`, `secret?`: one line to copy: a command, a token, an address (monospace). |
| `CopyValue` | `value`, `label?`: data in sans with a copy button (a checksum, a fingerprint). |
| `StatusBadge` | `tone` (`neutral`, `success`, `warning`, `danger`, `info`), `icon?`, the word as child. |
| `ViewSwitcher` | `bind:value`, `views` (`"list"`, `"tree"`, `"columns"`, `"coverflow"`), `label?`. The choice of view as one segmented control with icons and labels (icons only under 640px); arrow keys move. Hold the value in a `ViewChoice` (`lib/view.svelte.ts`), which remembers it per user. Offer only views that carry meaning: Tree (folders that expand in place) and Columns for deep trees, Cover Flow only for the versions and builds of one artifact (time is the axis). There is no Icons view. Every view renders in the same area: a switch never changes the layout around it. |
| `FormatIcon` | `format`, `kind?`, `label?`, `class?`. The format plugin's own SVG icon (from `GET /api/formats`), one color, 16px by default: the Maven feather, the Debian swirl, the raw file; `kind="gradle-plugin"` gives the Gradle elephant. Its space is reserved before the formats load. |
| `FormatBadge` | `format`, `title?`. The format's icon and title, inline, with no box and no tint. |
| `RepoPicker` | `value` ("" for all), `onchange(name)`, `filter?` (`RepoFilter`: what the server keeps, e.g. `perm`, `kind`), `keep?(repo)` (what the menu shows of the server's answer), `format?` (the chosen one's, before a search found it), `all?` (false: a required choice with no "All repositories" row), `id?`, `label?`, `allLabel?` ("All repositories"), `class?`. One repository, or all of them for a filter (Search, Activity, the repository of a publish token): a field with the chosen repository's `FormatIcon` that asks the server for the repositories whose name starts with what you type (`GET /api/repos?q=`, 20 at a time; a bits-ui `Combobox` with the `Select` items), each repository with its icon. A name from the address that the search does not return still shows as the choice. |
| `PersonPicker` | `search(q)` (a page of `{username}`: `api.auditPeople`), `value` ("" for everyone), `onchange(name)`, `label?`, `allLabel?` ("Everyone"), `class?`. One person or everyone for a filter (Activity): the `RepoPicker` field with the chosen person's `UserAvatar`, which asks the server for the names that start with what you type; each person in the menu with a small `UserAvatar`. A name from the address that the search does not return still shows as the choice. | `label`, `kind?` (the muted word before it: "Repository"), snippet `icon?`, `onremove`. One active filter of a list as a small removable chip; a row of them sits under the filter controls, with Clear filters at its end. |
| `EmptyState` | `title`, `description?`, `icon?`; children are the action that fills it. A whole page or result that has nothing to show; a list uses `CompactEmpty`. Its picture is the brand's empty ticket: flat, in outline, in the muted ink, its stub torn off along the perforation; `icon` (24px, what is missing) sits where the T would be. |
| `TicketBadge` | `tone?` (`neutral`, `gold`), `icon?` (14px, the kind of value), `stub?` (a word or a number after the perforation), `title?`, the label as child. A short value on a small flat ticket: notched ends, the label, a dashed perforation and the stub (blank without `stub`), 24px tall like a `StatusBadge`. Only for what admits or is admitted (see Ticket motifs): conditions are neutral, the CI system on the admission ticket is gold. |
| `Perforation` | `vertical?`, `class?` (the color of the slots; the canvas by default, so they read as holes). The mark's slot column as a row (or a column) of rounded slots: the line where a ticket shape tears in two. Only inside a ticket shape. |
| `UserAvatar` | `name`, `size?` (`sm` 24px, `md` 32px for the rows of a list and the account menu, `lg` 40px for an identity card). The avatar generated from the username: one of six hues, a corner cut off at 45 degrees in a darker shade, the initials; the same name gives the same avatar everywhere. Hidden from screen readers: the name is always beside it. |
| `ErrorState` | `error`, `title?`, `onRetry?`, `inline?` (one line in a form, a dialog or a table). |
| `Loading` | `rows?` (the height to reserve, in table rows of `--row`; 4), `inline?` (one line in a form, a dialog or a row), `label?`. The one loading state: nothing for 300 ms, then a quiet spinner and "Loading" in the reserved space. Show it for `loading && !data`, so a reload keeps the current content. `DataTable`, `ItemList` and `SourceGrid` use it. |
| `FreshFactor` | `actionLabel`, `onProof(proof)`: the fresh second-factor check, inline in a page. |
| `SecondFactorPrompt` | The same in a short dialog, before an action started from a button. |
| `SecondFactorForm` | The code or passkey form inside the two above and the sign-in page. |
| `SettingsGroupForm` | Registry settings rendered by type (a size or duration is a `UnitInput`, see `lib/units.ts`), saved from its `SaveBar`. When `effects` lists further effects of a save (a storage move), Save first asks in a confirm that lists every change. `sections` make it `FormSection`s (with `steps`, numbered `Step`s), so it is the whole form of a page (General, Storage, Sign-in options). Every field is a `FormRow` / `SwitchRow`; a list is a `ListInput`, a secret a `SecretInput`. `fields` gives a key a plain label, help, `aka` and placeholder; `errors` marks fields from a connection test; `before` / `extra` get the unsaved edits (`extra` renders under the last section: a connection test). |
| `OptionRow` | `id`, `label`, `description?`, snippet `control` (a `Checkbox` or a `RadioGroup.Item`), snippet `icon?` (a bare 16px icon before the label: a repository's `FormatIcon`), `children?` (a field or a note under the description): one choice in a list. A click anywhere on the control, the title or the description toggles it once; the title stays the one label of the control. `children` sit outside that hit target. Four looks, so a click shows its result at once under the pointer: at rest no fill; under the pointer `--hover`; chosen (`data-state="checked"` on the control) `--selected` with the check, a little deeper under the pointer; a disabled control muted, with no fill, no hover and the not-allowed cursor (say why in `description`). |
| `PermissionPicker` | `id`, `groups` (`{title, help?, items}[]`: `pickerGroups("server")` or `pickerGroups("repo")`), `bind:value`, `needs?` (`needsOf(scope)`: each permission and the permissions it needs), `held?` (the permissions the person can change; others are off), `onchange?`. Permissions as checkboxes in groups, one per subsystem, each with its title and help, All and None, and one line of help per checkbox. A check also checks what the permission needs, a clear also clears what needs it, and the help line says why. Always checkboxes, no presets. The grant dialog (`settings/repos/GrantDialog`) uses it for repository permissions. |
| `ServerPermissionPicker` | `id`, `bind:value` (server permission IDs), `held?` (the permissions the person can change), `readonly?`, `onchange?`. The server permissions as a role: three radio cards, Member (none), Administrator (every one) and Custom (side by side from a 36rem container, stacked in a dialog or on a phone). Member and Administrator set the value at once. Custom lists one row per permission (its sidebar icon, label, one line of help, a switch at the right) under the Settings sidebar group of the page it opens ("Other" for one without a page); All repositories also names the repository permissions it holds. A permission that a switched-on one needs is on and locked ("Needed by …"); one outside `held` is off with why, and so are Administrator and Member when they would change such a permission. `readonly` shows the role and the permissions held. The person page and `PermissionMapInput` use it. |
| `VisibilityChoice` | `bind:anonymous`: Private or Public (new repository, repository Overview). |
| `SigningKeyChoice` | `keys`, `repoName`, `exclude?`, `bind:choice` (`{mode: "existing" \| "new", name}`): the signing key of a repository (the signing module's Signing tab and repo field). |
| `PermissionMapInput` | `id`, `value` (group → server permission IDs), `onchange`, `disabled?`: the groups of a permission map, each with the phrase of its permissions (or Member), Change and Remove, and Add a group; a short form with a `ServerPermissionPicker` edits one. `SettingsGroupForm` uses it for a `map` setting with `permissionMap` in `fields`. |
| `RangePaste` | `id`, `label?`, `disabled?`, `onAdd?(ranges)`, `onReplace?(ranges)`, `buttons?`, `bind:found?`: paste addresses and ranges in any format. See below. |
| `LiveStatus` | `live`, `verb?` ("Updated"), `cadence?` (the sentence of how often; "Refreshes every 3 seconds" by default). The state of a `Live`: a small dot and "Updated 4 s ago" (ticking); the dot pulses while a refresh runs (not under reduced motion). The tooltip says how often it refreshes and why the last refresh failed; a click refreshes now. When refreshes fail or the browser is offline it says "Not updated for 2 min · trying again in 20 s" in the warning color. It shows only while the `Live` is active. See **Live data**. |
| `LiveValue` | `value`; the text is the child (or `value`). A number that a `Live` refreshes: a brief faint brand tint behind it when it changes, nothing under reduced motion. |
| `Cadence` | `noun` ("gap check"), `every` (seconds, from the server), `last?`, `next?`, `class?`. When a slower process outside the page runs next, as a meta value where the value it moves is: "Next gap check in about 7 min", the interval in its tooltip ("Every 10 minutes"); before a first run, "Every 10 minutes". A next time more than one interval past reads "Gap check 14 min late" in the warning color. |
| `ServerMark` | `size?`: the server's logo at that height (at most four times as wide), or the brand's `Logo` without one. |
| `FileSize`, `RelativeTime`, `QR`, `Logo`, `CopyButton` | Small parts. |

### Examples

A list page: the collection is an `ItemList` in its `Section`:

```svelte
<script lang="ts">
  const devices = new Keyset((p) => api.devices.list(p));
</script>

<PageHeader title="Devices" description="The computers and CI jobs that are signed in as you." />

<Section title="Devices">
  <ItemList source={devices} caption="Devices" key={(d) => d.id}>
    {#snippet leading(d)}<LaptopIcon class="size-5" />{/snippet}
    {#snippet title(d)}{d.name}{/snippet}
    {#snippet meta(d)}{clientOf(d)} · used <RelativeTime at={d.last_used_at} />{/snippet}
    {#snippet trailing(d)}<Button variant="ghost" size="sm" onclick={() => revoke(d)}>Revoke</Button>{/snippet}
    {#snippet empty()}<CompactEmpty icon={LaptopIcon} title="Devices" state="None yet" text="A device signs in with telesphorepo login." />{/snippet}
  </ItemList>
</Section>
```

A handful of rich things: a `CardGrid` (or a `SourceGrid` over a source) of `ItemCard`s:

```svelte
<Section plain title="Signing keys">
  {#snippet actions()}<Button href={href("/settings/signing-keys/new")}><PlusIcon />New signing key</Button>{/snippet}
  <SourceGrid source={listOf(keys)} label="Signing keys" key={(k) => k.name}>
    {#snippet card(k)}
      <ItemCard title={k.name}>
        {#snippet icon()}<KeyRoundIcon class="size-5" />{/snippet}
        <p>…{k.fingerprint.slice(-16)}</p>
        <p>Used by {k.repos.join(", ")}</p>
        {#snippet actions()}<Button variant="ghost" size="sm" onclick={() => remove(k)}>Delete</Button>{/snippet}
      </ItemCard>
    {/snippet}
  </SourceGrid>
</Section>
```

A table, where people compare and sort (Settings › Repositories, the publish log):

```svelte
<DataTable source={events} caption="Events" key={(e) => e.id}
  columns={[{ label: "What", grow: true }, { label: "Who" }, { label: "When", align: "right" }]}>
  {#snippet row(e)}
    <Cell grow strong text={e.title} />
    <Cell muted text={e.actor} />
    <Cell num muted><RelativeTime at={e.at} /></Cell>
  {/snippet}
</DataTable>
```

A settings form: one card per area, a `Draft` holds the edits and the `SaveBar` saves them.
Every text control fills its cell; a size is one `UnitInput`; a long value spans both columns:

```svelte
<script lang="ts">
  const draft = new Draft({ description: repo.description, max: "2", unit: "GiB", public: repo.anonymous_read });
  async function save() {
    const saved = await api.repos.update(repo.name, { description: draft.value.description });
    if (draft.value.public !== draft.saved.public) await api.repos.setPublic([repo.name], draft.value.public);
    draft.commit({ ...draft.value, description: saved.description });
    toast.success(`Saved ${repo.name}`);
  }
</script>

<FormSection title="Repository">
  <FormGrid>
    <FormRow label="Description" for="desc" help="One sentence on what it holds, for people who look for it." full
      length={draft.value.description.length} max={500}>
      <Textarea id="desc" rows={3} maxlength={500} bind:value={draft.value.description} aria-describedby="desc-help desc-count" />
    </FormRow>
    <FormRow label="Largest upload" for="max" help="Uploads above this size are refused.">
      <UnitInput id="max" bind:num={draft.value.max} bind:unit={draft.value.unit} units={SIZE_UNITS} describedby="max-help" />
      {#snippet more()}The server checks the size before it stores anything, so a refused upload costs no space.{/snippet}
    </FormRow>
    <SwitchRow id="public" label="Public" help="Anyone can download without signing in." bind:checked={draft.value.public} />
  </FormGrid>
</FormSection>
<SaveBar count={draft.count} onDiscard={draft.reset} onSave={save} />
```

A form that creates something (a new list, a new repository) is its own confirmation: it ends
with its submit button in the last section's `footer`, and it has no `SaveBar`.

A thing that is not set up is one row with its action, where it would be listed. The state
shows on the thing itself, never in a summary above it:

```svelte
<Section title="Passkeys">
  {#snippet actions()}<Button variant="outline" onclick={add}><PlusIcon />Add passkey</Button>{/snippet}
  <ItemList source={listOf(passkeys)} ...>
    {#snippet empty()}
      <CompactEmpty icon={KeyRoundIcon} title="Passkey" state="Not set up" text="Sign in with your fingerprint." />
    {/snippet}
  </ItemList>
</Section>
```

An action that is not on screen all the time (change password, add a person) is a button in the
header of its section that opens a `ShortFormDialog`, never a lone button in a section.

A setup: numbered steps, with **Test** where the connection is configured (`SettingsGroupForm`
with `steps` does the steps for registry settings):

```svelte
<Step n={1} title="Connect your provider" done={connected}>
  <FormGrid>...</FormGrid>
  {#snippet footer()}<ConnectionTest {testing} {result} onTest={test} />{/snippet}
</Step>
```

A list with a choice of view (Browse):

```svelte
<script lang="ts">
  const view = new ViewChoice("versions", ["list", "coverflow"]);
</script>
<Toolbar>
  {#snippet end()}<ViewSwitcher bind:value={view.value} views={view.views} label="Show versions as" />{/snippet}
</Toolbar>
{#if view.value === "list"}<ItemList ... />{:else}<!-- the Cover Flow of felt tickets, built with the Browse pages -->{/if}
```

`ViewChoice` stores the choice in this browser under the signed-in user's name, per place
("files", "versions"): each person on a shared computer keeps their own view.

### Address ranges

Every place that takes addresses or ranges (the network-list editor, the repository Access tab's
"From where", Network and proxy) takes them through `RangePaste`. Never parse pasted ranges in
the browser. The component sends the text to `POST /api/ip-ranges/parse` 400 ms after the last
key, or at once on Check. It shows "8 ranges found · 0 invalid · 0 duplicates removed", the
invalid lines with their numbers and reasons, and the ranges found (sans, `tabular-nums`). Add
gives the ranges to the parent, and Replace (only with `onReplace`) gives them in place of the
current ones. Both empty the box. The parent drops the ranges that it already holds and saves
through its own form. In a dialog, pass `buttons={false}` and `bind:found`: the dialog's own Add
button takes the ranges of the checked text (Add addresses on the Access tab).

```svelte
<RangePaste
  id="{id}-paste"
  onAdd={(r) => (ranges = [...ranges, ...r.filter((x) => !ranges.includes(x))])}
  onReplace={(r) => (ranges = r)}
/>
```

## Live data

A page that follows a process on the server (a backup, a storage move, a Nexus sync, a region
that fills) shows that it is live, how often it refreshes, when it last did, and whether a value
that does not move waits for a slower process. Every such page does it the same way.

- **One `Live` per refreshed area** (`lib/live.svelte.ts`). It owns the timer: `run` asks for the
  data and throws on an error (`resource.fetch`), `every` is the wait in ms, `while` the condition
  (a running backup, a copying move), `now` refreshes at once when it starts. It pauses while the
  tab is hidden or the browser is offline and refreshes at once on return, never runs two
  requests at once, and waits twice as long after each failure (up to a minute). A page never
  writes its own `setInterval` or `setTimeout` loop for data.
- **One `LiveStatus`, where the data is.** In the actions of the `Section` whose data refreshes,
  first, before the section's buttons; in the `PageHeader` actions when the whole page refreshes
  (Edge delivery, Nexus migration). One per area: an area that refreshes two things (a sync and
  its log) refreshes them in one `Live`. While a refresh fails, the area's own error row stays
  quiet: `LiveStatus` says it.
- **Keep the intervals of the processes.** 2 s for a backup, a move and a Nexus sync (its log reads
  its new lines at each refresh), 3 s for the Nexus servers and a filling region, 30 s for the
  check of the Worker. A Nexus server's page reads the server again only when the next sync is due
  (reading it asks Nexus), and shows no `LiveStatus`: its status line names the next check.
- **Say the cadence of a slower process where its value is** (`Cadence`), with the interval from
  the server that owns it, never a number written into the page: the Worker's gap check
  (`gap_check_every` of the edge status), the count of a region's files (`blob_check_every`), the
  move's progress saves (`progress_at`, `save_every`), the Nexus sync (`sync_interval`,
  `next_sync_at`). A value that waits for such a process then reads as waiting, and a process that
  is late says so.
- **A changed number may flash** (`LiveValue`): the count that a refresh moves (files copied,
  pointers written, files missing), not every value of the area.
- **Outside a page** (a repository delete in a toast), loop with `pause` from `lib/live.svelte.ts`
  and say in the toast since when it is not updated when requests fail.

| Say | Not |
|---|---|
| Updated 4 s ago; Updated just now | Last refreshed at 14:02:11; Live |
| Refreshes every 3 seconds (tooltip); Click to refresh now | Polling, auto-refresh on |
| Not updated for 2 min · trying again in 20 s; Offline · not updated for 2 min | Stale, connection lost, error icon alone |
| Next gap check in about 7 min (tooltip: Every 10 minutes) | The Worker checks every 10 minutes; cron, next tick, ETA |
| Progress saved 12 s ago | Heartbeat, last write |

## Interaction rules

- **Navigation.** Every view has its own address, so the browser's back and forward buttons move
  between views. The way up is the way back of `PageHeader`, to the page one level up; it never
  goes somewhere else. Repository settings, like every Settings page, keep the Settings shell.
- **Unsaved changes.** A page that edits saved values holds them in a `Draft` and shows a
  `SaveBar`; it appears only with changes ("2 unsaved changes · Discard · Save"). While it shows,
  leaving the view asks "Leave this page?" (an in-app link, the back button, `navigate()`), and
  closing or reloading the tab gets the browser's prompt. After a save, `draft.commit()` so the
  bar goes away. Navigate after a save only once the draft is clean, or pass `{ force: true }`.
- **Centered dialogs only for confirmations (`ConfirmDialog`) and short forms (`ShortFormDialog`,
  at most five fields).** A longer flow is a page of its own, one level down, with its way back:
  create a repository, edit a CI pipeline, edit a network list, create or import a signing key,
  upload.
- **No side sheets.** The only sheet is the Settings menu under 768px.
- **Details of a selected row** show in a `DetailsColumn` inside the page, never in an overlay.
- **Rows and cards are clickable anywhere** when they open something (`href` of a `DataTable`, an
  `ItemList` or an `ItemCard`). `ItemList` and `ItemCard` make the title the link for the keyboard;
  in a `DataTable`, put the same link on the row's name. Buttons inside keep their own click.
- **One line per table row.** In a `DataTable`, a long value truncates (`Cell text`) with its full
  text in a tooltip (on a phone the row stacks into the name and a meta line) and, for a value people paste elsewhere (a path, a checksum, an address), a
  copy button (`copy`). Never let a cell wrap to a second line, and never put a description under
  a name in a table row: put it after the name, muted, on the same line. An `ItemList` row has two
  lines by design: the title, then its meta line, each one line that truncates.
- **Confirm what changes.** Every action that removes data or changes access asks with
  `ConfirmDialog` and lists the effects, one line each. A form the user fills in and submits is its
  own confirmation. The confirm button names the action ("Delete 14 files"); the toast uses the
  same verb ("Deleted 14 files"). The confirm button uses `variant="destructive"`. A row action
  that opens the confirm (Remove, Revoke, End it now) is a quiet ghost button that turns red under
  the pointer, so a list is not a column of red buttons.
- **Help** is one sentence under the label. Anything longer goes behind "Learn more" (`FormRow`
  `more`, or `LearnMore`).
- **Ticket moments** (principle 6). Creating a publish token prints a `FeltTicket`; ending it
  punches it; approving a device tears its stub. Each takes under 400 ms, never blocks work, and
  shows its end state at once under `prefers-reduced-motion`.
- **A secret shows once**, in a `bg-panel` block above the list with `CodeSnippet secret` and a
  Done button.
- **Sign in** happens only on `/login`. Other pages never show their own Sign in button: a page
  that needs a user has `access: "user"` and the shell sends the visitor to `/login?next=`.
- **Keyboard.** Every action is a `button` or a link; dialogs trap focus; `/` focuses the top-bar
  search; Ctrl+S (⌘S) saves a page with a `SaveBar`; arrow keys move in a `ViewSwitcher`; focus
  rings stay visible.

## Forbidden

- Monospace outside `CodeBlock`, `CodeSnippet` and the JSON editor (`font-mono` anywhere else).
- Side sheets (`Sheet`) for anything but the mobile Settings menu.
- Left, right or bottom border accents for a state: active, selected or hover is a fill.
- Subtitles for decoration, and text that states the obvious: what the title already says, what
  anyone sees by looking (a log is newest first, People lists people), or what everyone knows. A
  description tells the reader one thing they could not see or would not know (a format cannot
  change later; approve only if the code matches), or the page has none. No eyebrow labels, no
  all-caps labels.
- A second Sign in button.
- A page width, a container, a `max-w-*` on a page block, or side padding set by a page.
- A control sized by a page (`w-44`, `max-w-sm` on an input or a select in a form), two controls
  for one value (a number and a separate unit select), a bare checkbox for a yes/no setting.
- A table row of two lines, a table without `DataTable`, a number column aligned left.
- A `DataTable` for a collection that nobody sorts or compares by column; a list or a grid of cards
  built by hand instead of `ItemList` or `CardGrid`.
- A card that is a table in disguise (label and value pairs), and a card or a row that shows every
  field the API returns.
- A "dashboard" of numbers nobody acts on.
- A "back" link, and a Save button at the end of a long settings form (the `SaveBar` saves).
- Raw colors, other radii, spacing outside the 4px scale, margins between blocks other than the
  ones in Tokens.
- A status row or a summary banner at the top of a page or a section, and text that describes
  what the screen shows ("this repository is public").
- The same fact twice on one screen (a "Public" pill and a banner that says it again).
- An icon in a colored or tinted square beside a heading, a row or an empty state; a Lucide icon
  for a format.
- Skeletons, and a loading state that replaces content that is already on screen.
- A lone action button in a section: open the action in a dialog from the section header.
- A timer in a page that asks the server again (`setInterval`, a `setTimeout` loop): use a `Live`.
  An interval or a schedule that the server owns, written into the UI: read it from the API.
- Unit tests and mocks.

## Checklist for every page

- [ ] The page is a `PageDef` of its module and in the table above; a Settings page has `nav` with an icon
      in its sitemap group, and its way back reads right (check a sub-page too).
- [ ] `PageHeader` with a title; a description only if it tells the reader something they could
      not see on the page or would not already know.
- [ ] No status row and no summary banner: state shows on the thing itself.
- [ ] Each area is a `Section` (a card); a connection has its `ConnectionTest` where it is
      configured.
- [ ] The page uses the whole content column; its left and right edges match every other
      Settings page (compare two screenshots).
- [ ] Each collection has the right shape: an `ItemList` by default (the name and its badges,
      one meta line of one or two facts, the state or the actions at the end); a `CardGrid` of
      `ItemCard`s for a handful of rich things (up to three short fact lines, no label and value
      pairs); a `DataTable` only where people sort, compare columns or scan rows for the one they want.
- [ ] A `DataTable` uses `Cell`s: zebra stripes, one line per row, long values truncated with a
      tooltip (and `copy` where people paste them), numbers and times right with `num`, one
      `grow` column, secondary columns `hideOnPhone`.
- [ ] An empty collection is a `CompactEmpty` row (the `empty` of the collection).
- [ ] Forms use `FormSection` + `FormGrid` with `FormRow` / `SwitchRow`; labels sit above their
      controls and every text control fills its cell (no `width`, no size classes); a number with a unit is a
      `UnitInput`; lists are `ListInput`; secrets are `SecretInput`; help is one sentence, more
      goes in `more`.
- [ ] Edits of saved values use a `Draft` and a `SaveBar`; leaving with changes asks first.
- [ ] No full-width rule under the page header, under tabs or between sections.
- [ ] Every request shows loading (`Loading`, no skeletons; a reload keeps the content), error
      (with Try again for reads) and empty states.
- [ ] Icons are bare and mean something (`FormatIcon` for a format); none sits in a tinted square.
- [ ] Dialogs are only `ConfirmDialog` or `ShortFormDialog` (five fields or fewer); a longer flow
      is a page one level down.
- [ ] No `font-mono`, no `Sheet`, no `Skeleton`, no border accents, no all caps, no `max-w-` on
      a page block (`grep` before you finish).
- [ ] Data that the server changes on its own refreshes through a `Live` with a `LiveStatus` (no
      `setInterval` in a page); a slower outside process shows its `Cadence` from the server.
- [ ] Words follow the table below; buttons name the action; toasts reuse the verb.
- [ ] It works at 390px with no sideways page scroll.

## Uploads and deletes

The repository page holds a publish token for the repository in memory only
(`pages/repo/session.ts`; never in storage or the URL), and drops it 30 seconds before it
expires or when the server answers 401.

- **Upload** (`pages/repo/UploadView.svelte`, `/repo/:name/upload`): first `FreshFactor`, which
  mints the token with `api.publish.manualSession`. Then one `DropZone` takes files or a folder,
  as the `upload` of the format's descriptor allows (a format without it takes no uploads).
  `folder.ts` reads a folder, at most 500 files, only the ones that the `keep` globs take. It skips dot-folders and `node_modules`, and folders that no `keep` glob can match below, and reads sibling folders 8 at a time.
  `api.repos.uploadPlan` sends the list of files and the first bytes of the files that a `reads`
  rule matches, and stores nothing; a new plan aborts the one in flight, and names the `heads` of
  the last plan, so it sends only the first bytes of files added since (a 409 sends them all
  again). `PlanView` shows the plan (each item with the path of each
  file and its problems, the files left out), and the form shows the plan's fields (a value the
  plan read from the files is fixed). An edit of a field plans again after 400 ms. The ticked
  items then upload with progress: past 90 MB, `upload/send.ts` sends the largest files through
  resumable uploads in 16 MiB chunks (`api.repos.startUpload`, `appendUpload`) until the rest fit
  in one request; then `api.repos.upload` sends the fields first, the part `uploads` that names the
  finished uploads, and each other file with its path in the upload as its multipart filename.
- **Delete** (`pages/repo/FilesTab.svelte`): a person with Delete files deletes with the UI session,
  in a repository or in the cached files of a proxy.
  `delete/DeleteDialog.svelte` asks the server for the delete plan (`api.repos.deletePlan`)
  before the user confirms: the file count and the delete notes of the format (the versions that
  `maven-metadata.xml` stops listing, the packages that apt stops offering). In the
  List view, people who can delete pick rows (files and folders mix); the details column then
  shows "3 selected" and the size of the picked files, and Delete removes them all after one
  confirm. A version delete (`versionTarget` in `files/place.ts`) removes the version's `folder`, with no request, or
  the files of all its builds when the version has no folder of its own (Debian). Every delete
  calls `POST /api/repos/{name}/files/delete` (`api.repos.deleteFiles`), at most 1000 paths and
  prefixes in one request.

## Words

Write for someone who has never used a package server. Name things by what they do for the
reader, and keep one word for one thing on every page. API field names and URL paths stay
unchanged.

| Say | Not |
|---|---|
| Public / Private | Anonymous read; anyone / people with access |
| The permission labels and help of `GET /api/permissions` (Download, Publish, Delete files, Edit settings, Manage access, Manage CI pipelines; All repositories, Manage people, ...) and the phrases of a set ("Download, publish", "Everything"); the roles of server permissions: Member, Administrator, Custom | Read / write permission, levels, other role names |
| Make public, Make private, Delete, Disable, Enable, Give access, Remove access, Change permissions | Other verbs for the same bulk actions (Turn off, Revoke, Grant) |
| 1 selected / 12 selected (`selectedText` in `lib/words.ts`) | 12 rows checked, 12 items chosen |
| Read token | Download token, API token |
| Publish token | Publish session, temporary publish password |
| Sign-in and security; two-step verification; second step | Second factor, MFA |
| CI pipeline; conditions; CI system address | CI publishing rule, trust policy; claims; issuer |
| Activity | Audit log; actor, target |
| Sign-in options; single sign-on | SSO (the sign-in button says "Continue with single sign-on") |
| Networks; network list; from where (any network / these networks) | IP list, preset, allow-list, CIDR list |
| Nexus migration; copy; switch over | Nexus sources; mirror; cut over |
| Network and proxy; trusted proxy | Reverse proxy settings |
| Edge delivery; region; Worker | CDN, PoP, edge node |
| Administrator | Admin (in sentences) |
| Repository (hosted, when it needs a kind) | Hosted repository (in the UI) |
| Proxy; remote (the remote repository that a proxy keeps the files of); Test the remote | Mirror, upstream |
| Group; members (the repositories a group reads, in order) | Virtual repository, aggregate |
