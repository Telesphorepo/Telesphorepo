# Web UI

Svelte 5 + Vite (no SvelteKit), Tailwind CSS v4, shadcn-svelte (Maia style), TypeScript, bun.
`bun run build` type-checks with `svelte-check` and writes `web/dist`, which the Go binary embeds
(`web/embed.go`). `bun run dev` serves the UI on :5173 and proxies every other path to the backend
on :8080. Run both with `go tool task dev`.

The UI is only a client of the JSON API (`/api/...`). It has no tests of its own: e2e tests cover
every API route it uses.

This file is binding. Every page follows the layout, the components, the interaction rules and
the checklist below. The sitemap and the Settings navigation come from
`docs/design/information-architecture.md`; the principles from `docs/design/principles.md`.

## Structure

| Path | What it holds |
|---|---|
| `main.ts`, `App.svelte` | Mount; the shell: loads the user and the formats, matches the route, checks access, renders the page in `AppShell`. |
| `modules/` | The UI of each server module (`<id>/module.ts`, one `ModuleUI`: pages with their sidebar items, redirects, repository settings tabs, repository field controls, format parts of the file details) and `index.ts`, the registry in the server's module start order. `types.ts` holds `PageDef`, `Access`, `RepoTabDef`, `RepoFieldDef`, `ModuleUI`. A module's own components sit next to its `module.ts` (the Signing tab, the signing key field, the Maven, Debian and Docker file parts), and so do the calls to a format module's own routes (`docker/api.ts`). |
| `app.css` | Design tokens: colors (light and dark), the type scale, fonts, the one radius, the width scale, the control widths, the scrollbars. |
| `pages/` | One component per route: `Home`, `Search`, `SignIn`, `DeviceApprove`; `repo/` (one page per repository), `settings/repos/` (a repository's settings: the shell, its tabs and the Access tab's parts), `account/`, `repositories/`, `server/` (sub-folders `ci/`, `ip-lists/`, `nexus/`, `people/` hold the parts of those pages). |
| `lib/api/` | The one API client: `client.ts` (fetch wrapper, `ApiError`, the `X-Telesphorepo-UI` header, the XHR upload with progress), `types.ts` (JSON shapes), `index.ts` (`api.*` for every route outside the routes of a format module). |
| `lib/auth.svelte.ts` | `auth`: current user, sign in (two steps), sign out (returns to Browse home); the permission checks `can(perm)`, `canRepo(repo, perm)`, `managesRepo(repo)`, `allows(access, params)`. |
| `lib/permissions.ts` | The permission registry of the server (`GET /api/permissions`, read by `auth.load()` before the first page): the server and repository permissions in registry order with their labels and help, the needs of each (`needsOf`), the picker groups, one per subsystem (`pickerGroups`), and the phrases of a set: `repoPhrase` ("Download, publish", "Everything") and `serverPhrase` ("Administrator", "Manage people, manage storage"). |
| `lib/router.svelte.ts` | Hash router: `router`, `href()`, `navigate()`, `go()`, `isActive()`, `encodePath()`, `repoPath()`, `filesPath()`, `repoSettingsPath()`; the navigation guard hook. |
| `lib/current.svelte.ts` | `current`: the matched page of the module registry and the pending redirect of the current address. |
| `lib/crumbs.ts` | `Crumb` and `settingsTrail()`: the breadcrumbs of a Settings page, from the sidebar items of the module registry. |
| `lib/unsaved.svelte.ts` | `unsaved` (the unsaved-changes guard) and `Draft` (a form's edits and what is saved). |
| `lib/view.svelte.ts` | `ViewChoice` and `viewLook`: List, Tree, Columns, Cover Flow, remembered per user. |
| `lib/load.svelte.ts` | `Resource` (one request), `Keyset` (a paged list), `listOf()` and `TableSource` (what `DataTable` reads). |
| `lib/format.ts` | `formatSize`, `formatRelative`, `formatDateTime`, `formatCount`. |
| `lib/words.ts` | `count` ("12 repositories"), `selectedText` ("12 selected"), `someNames` (the first five names "and 7 more"): the words of bulk actions. |
| `lib/utils.ts` | `cn()`: joins classes; it knows that `text-title` … `text-meta` are font sizes, not colors. |
| `lib/layouts/` | `AppShell` (top bar + container + Settings sidebar), `TopBar`, `AccountMenu`, `GlobalSearch`, `SettingsNav`, `UnsavedGuard` (the leave question). |
| `lib/components/` | The shared components (below). Pages build only from these and `ui/`. |
| `lib/components/ui/` | shadcn-svelte primitives. Add more with `bunx shadcn-svelte@latest add <name>`. |
| `lib/formats/` | `registry.svelte.ts` (`formats`: the UI descriptors of `GET /api/formats`, loaded once by `App.svelte`: titles, icons, config fields, upload rules, delete notes, file parts, remote presets, words) and `ConfigEditor` (a format's repo config as one field per entry of `config.fields`). Every format choice of the UI reads the descriptor; a format-specific component exists only as a named `formatParts` slot of the format's UI module. |
| `lib/glob.ts` | `globMatch`, `anyGlob`: the globs of the descriptors, matched like the server. |
| `lib/units.ts` | `splitSize`, `splitDuration`, `splitSeconds`: the number and unit that a form shows for a size or a duration; `joinSeconds` turns a number and a unit back into seconds. |
| `lib/remotes.ts` | `normalizeRemote`, `remoteError`: the remote address of a proxy. The well-known remotes are `remote_presets` of the descriptor. |
| `lib/network.ts` | `checkRange` and `rangeSize`: client-side checks of the addresses and CIDRs that network rules and trusted proxies hold. |
| `lib/webauthn.ts`, `lib/qr.ts` | Passkey create/get in the browser; QR encoder for authenticator setup. |

## Routes

URLs are hash URLs (`/#/path?query`), so the server never sees UI paths and the links that the
server and the clients print keep working: `/#/device?code=ABCD-EFGH`, `/#/settings/tokens`,
`/#/login?error=<code>`. A page gets `params` (from `:name` and `*rest` segments) and `query`
(a `URLSearchParams`) as props.

| Path | Page | Layout | Access | What it shows |
|---|---|---|---|---|
| `/` | `Home` | app | any | Repositories: a table with name (a lock after a private one, the kind icon after a group or a proxy), format, files (a group: its number of repositories), size, last publish. Filter box, format filter, New repository (All repositories). |
| `/search?q=&repo=` | `Search` | app | any | Files whose path contains `q` (3+ characters). The top bar's search field holds `q`. |
| `/login?next=&error=` | `SignIn` | app | any | The one place to sign in: single sign-on, password, then the second step. |
| `/device?code=&grant=&error=` | `DeviceApprove` | app | user | Look up a device code, check the device, approve it with a fresh second factor. A single sign-on account signs in again at its identity provider instead; the server returns with `grant` (the page approves at once) or `error`. |
| `/repo/:name` | `repo/RepoPage` | app | any | The repository header and the Files tab at the top folder. A group has no files and no tabs: the Use view and its members, each a link to the member's page. A proxy has Files (its cached files) and Use, and no publish or upload. |
| `/repo/:name/files/<path>?version=&build=` | `repo/RepoPage` | app | any | Files tab: a folder (`path` ends with `/`) or a selected file. `version` names a version of the artifact folder (a Debian package keeps every version in one folder); `build` a chosen build. The details column always shows on wide screens: the selected file, or else the open folder (repository, folder, artifact or version). |
| `/repo/:name/use` | `repo/RepoPage` | app | any | Use this repository: the address and the snippets for a build; a private repository adds a read-token step. |
| `/repo/:name/publish` | `repo/RepoPage` | app | any (Publish on the repository) | Publish to this repository: from your computer, from CI, by hand. The Set up a project card (`repo/ProjectSetup`) comes first: a fresh second factor, then the install and setup line for the OS (a single sign-on account returns with `?setup_grant=`), then `telesphorepo publish` or the native command (`publish` of the descriptor); the snippets stay behind "Set it up by hand". |
| `/setup/:code` | `Setup` | app | any | A setup link: run the install line in a terminal in the project folder. The code stays in the address fragment and the page calls no API. |
| `/repo/:name/upload?folder=` | `repo/RepoPage` | app | any (Publish on the repository, a password account) | Upload files or a folder. |
| **Settings › Your account** | | | | |
| `/settings/account` | `account/Overview` | settings | user | Overview: one identity card (avatar, name, role), then your repositories, your devices and your recent publishes. |
| `/settings/security` | `account/Security` | settings | user | Sign-in and security: password, authenticator apps, passkeys, recovery codes. The only page of a setup-only session. |
| `/settings/devices` | `account/Devices` | settings | user | Devices (holders of People see every user's). |
| `/settings/tokens` | `account/Tokens` | settings | user | Tokens: read tokens and publish tokens, one page with two sections. |
| **Settings › Repositories** | | | | |
| `/settings/repositories` | `repositories/AllRepositories` | settings | repositories | Repositories: every one for a holder of All repositories, else the ones where the person holds more than download. A row opens its settings (or its files when no tab opens). Picked rows (Edit settings): Make public, Make private; Delete (All repositories). |
| `/settings/repositories/new` | `repositories/NewRepository` | settings | repositories.all | First choice: a repository, a proxy of a remote repository, or a group of repositories; each offers the formats that take it (`kinds` of `GET /api/formats`). A repository: name, format, description, visibility, the format's options (`config.fields`), and a section per repository field of the format with the control of its UI module (the signing key). A proxy: the remote address (the `remote_presets` of the format as quick choices) and optional credentials; the server checks the remote, and "Create anyway" creates it when the remote does not answer. A group: its members, hosted repositories first, with the order as numbers. A proxy or a group then opens its settings Overview. |
| `/settings/repositories/:name/:tab` | `settings/repos/RepoSettings` | settings | repo | A repository's settings. Each tab shows only with its permission: Overview, Format settings and Remote need Edit settings, a module tab its `access` (the Signing tab: Edit settings); Access needs Manage access, Edit settings or CI pipelines, and shows People (Manage access), Public and From where (Edit settings), CI pipelines (CI pipelines). A tab the person cannot open goes to the first one they can. Hosted: `overview` (description, contents, its groups), `access`, `format` (the fields of `config.fields`), and the tabs of the UI modules that the format names in `repo_tabs` (`signing`). Proxy: `overview` (remote with Test the remote, use snippets, cache, its groups), `access` (download only), `remote` (address, credentials, the two times). Group: `overview` (use snippets, members with Move up, Move down, Remove and Add repositories, saved from the save bar), `access` (download only). Delete is a header action (All repositories); Open files is one too, except for a group. Breadcrumbs: Settings › Repositories › maven › Access. |
| **Settings › People and CI** | | | | |
| `/settings/people` | `server/People` | settings | people.manage | Every account with its role: Administrator (every server permission), the phrase of its server permissions, or nothing. New account opens a short form (name and password); the person's page then opens. A row opens the person. Picked rows (not your own): Disable, Enable. |
| `/settings/people/:name` | `server/people/Person` | settings | people.manage | One person: the identity card (avatar, role, sign-in) with the account actions; Server permissions (`PermissionPicker`, saved from the save bar after a confirm); the person's permissions on each repository. Give access opens a short form for many repositories; Change edits one; picked rows: Change permissions, Remove access. Each repository change applies at once. |
| `/settings/ci-pipelines` | `server/CIPublishing` | settings | ci.pipelines | CI pipelines: name, what each accepts, CI system, the repositories it can publish to. A row opens the pipeline. |
| `/settings/ci-pipelines/new?repo=&back=` | `server/CIPublishingRule` | settings | ci.pipelines | A new pipeline in four steps: where the job runs; which runs (GitHub repository, branches, tags, workflow file; or Jenkins address and jobs; the token conditions behind a secondary action); repositories; the workflow or Jenkinsfile step. `repo` starts chosen; Create and Cancel return to `back` when it is a `/settings/` path. |
| `/settings/ci-pipelines/:id` | `server/CIPublishingRule` | settings | ci.pipelines | Edit, turn off or delete a pipeline. |
| `/settings/networks` | `server/IPLists` | settings | network.manage | Networks (IP lists): downloaded lists and your own lists; each shows the repositories that use it. |
| `/settings/networks/new` | `server/IPListPage` | settings | network.manage | A new list: name, title, description, addresses pasted with `RangePaste`. |
| `/settings/networks/list/:name` | `server/IPListPage` | settings | network.manage | Edit, rename or delete your own list; a downloaded list's ranges and Refresh. |
| **Settings › Server** | | | | |
| `/settings/sign-in` | `server/SingleSignOn` | settings | signin.manage | Sign-in options in three steps: connect the provider (with Test), permissions from the provider (the group field and the groups, each with its server permissions in a `PermissionPicker`: `PermissionMapInput`), and how people sign in (password sign-in for everyone or for people who publish). |
| `/settings/signing-keys` | `server/SigningKeys` | settings | signing.manage | Signing keys: fingerprint, the repositories that use each key, public key download, delete. |
| `/settings/signing-keys/new` | `server/NewSigningKey` | settings | signing.manage | Create an Ed25519 key or import an armored key. |
| `/settings/storage` | `server/Storage` | settings | storage.manage | Storage settings, connection test, and the storage move with its progress. |
| `/settings/nexus` | `server/NexusMigration` | settings | nexus.manage | Nexus servers with status, repositories, last check and next check; Add a Nexus server. |
| `/settings/nexus/:id?sync=&repo=` | `server/NexusSource` | settings | nexus.manage | The status and its line; Sync now or Cancel the sync, Switch over, Delete. Repositories to copy (one pick table in set-up order: repositories and proxies, then groups with their members, then the ones this server cannot copy; each row with its state; picking a group picks its members; "Copy N repositories" saves and starts the sync); the running or chosen sync with its progress, its log (`repo` filters it) and its failed files; the syncs (a row opens its details); the connection. |
| `/settings/edge` | `server/EdgeDelivery` | settings | edge.manage | Edge delivery: the status (Worker version, last gap check, home bucket), the six R2 regions as a table (Serve from it, bucket, state, pointers waiting, last error) with the home region select, the backfill progress while a region fills, the on switch and the access lifetime, and the Deploy section with the generated `wrangler.jsonc` (`CodeBlock`) and the provision and deploy commands. One `Draft` and one `SaveBar`: Save checks the buckets first and asks before the server stops writing to one. |
| `/settings/proxy` | `server/ReverseProxy` | settings | network.manage | Network and proxy: the trusted proxies (none, this machine or network, the Cloudflare list, custom addresses) and the detected proxies. |
| `/settings/general` | `server/General` | settings | settings.general | The settings groups with no page of their own (public URL, upload limits, logs, index and upload timing, publishing lifetimes, retention, the Nexus sync interval): one form, a section per group, with the server's labels and help. |
| `/settings/activity?view=&action=&repo=` | `server/Activity` | settings | activity.read | Events (the audit log, filtered by what happened, from `GET /api/audit/actions` grouped by subsystem, and by repository) and Publish tokens (the publish log). |

A sub-page with a `:name` or `:id` keys its content by the parameter (`{#key}` in the page file),
so the form starts again when the parameter changes. A network list opens at
`/settings/networks/list/:name`, so no list name can collide with `/settings/networks/new`. A
repository's settings always name the tab (`/settings/repositories/new/overview` is the
repository called "new"), so no repository name collides with `/settings/repositories/new`. Build
those links with `repoSettingsPath(name, tab)`.

Redirects (`redirects` of a `ModuleUI`, paths like pages; the query is kept): `/settings` →
`/settings/account`.

Access is checked in `App.svelte` with `auth.allows`, never in a page: `user` sends a signed-out
visitor to `/login?next=<path>`; a permission ID (`people.manage`, `storage.manage`, ...; a repository
permission such as `ci.pipelines` on at least one repository) or `repositories`, `repo` (see `Access` in
`modules/types.ts`) shows "You cannot open this page" to a
person without it; and a setup-only session (no second factor yet) goes to the first page with `setup` (`/settings/security`).
The repository page is the one exception: it checks the caller's permissions on that repository
(`repo.permissions`), because only the repository knows them.

### Add a page

1. Create `pages/<area>/<Name>.svelte` with `let { params, query }: PageProps = $props();`.
2. Add one `PageDef` to the `pages` of the owning module's `modules/<id>/module.ts` (a new module
   UI also gets a line in `modules/index.ts`, in the server's start order): `path`, lazy `page`,
   `layout`, `access`, `title`, and `setup: true` for a page of a setup-only session.
3. For a Settings page, give it `nav` (its group, label, icon and order in the group). A sub-page
   (`/settings/networks/new`, `/settings/ci-pipelines/:id`) needs no nav item: its parent item
   stays open because `isActive` matches the prefix, and its breadcrumbs end with the item and
   the page title.
4. Add the route to the table above.
5. Call the API through `api.*` (`lib/api/index.ts`). A new server route gets a function
   and its types there first. A format module keeps the calls to its own routes in its own folder.

## Layout

- **One container.** `AppShell` puts every page in one centered container: `max-w-page`
  (1200px), side padding 24px (`sm:px-6`), 16px under 640px (`px-4`). Pages never set their own
  page width and never add side padding.
- **Top bar**, inside the same container: logo and name (Browse home) left; search centered (max
  480px); right: one **Sign in** button when signed out (hidden on `/login`), or **Settings** and
  the avatar menu (Your account, theme, Sign out) when signed in. Under 640px the search is an icon
  that opens `/search`.
- **Settings.** A 220px sidebar inside the container, 40px gap, then the content column. Four
  groups from the sitemap: "Your account", "Repositories", "People and CI", "Server". Each item
  shows only to a person with the permission of its route, and a group without items is hidden
  (`visibleNav` of the module registry). Each group has a heading; groups are 24px apart with no line; every
  item has an icon and is 32px tall. The sidebar sticks under the top bar and scrolls on its own
  when the window is short. Under 768px the sidebar is a menu that the top bar's menu button
  opens. Every Settings page keeps this shell, a repository's settings included.
- **One content width.** Every Settings page uses the whole content column next to the sidebar:
  the page header, sections, tables and forms start and end at the same edges on every page.
  Pages never set a `max-w-*` on a block of their own.
- **Logical containers** (principle 12). Each area of a page is a `Section`: a card with its
  title (and the area's actions at the header's end), 24px padding (20px on a phone), and 24px
  between cards. Inside a card, a form is a two-column `FormGrid`; a thin `<Separator />` may part
  sub-groups of one card. Never a line between cards.
- **Breadcrumbs** sit above the title of every Settings page (`PageHeader` builds them):
  Settings › group › item, then deeper steps on a sub-page (Settings › People and CI › CI
  pipelines › acme/app). The last step is the current page. They are the way up; there is no
  "back" link, and the browser's back button returns to the previous view (every view has its own
  address).
- **No near-miss lines.** The page header, tabs and sections are separated by space and type,
  never by a full-width rule: a rule in the content nearly aligns with something in the sidebar
  and looks wrong. Lines stay inside a component (a table header, a card, a dialog).
- **State lives where the thing is** (principle 1). No status row and no summary banner at the
  top of a page or a section: a switch shows that it is on, a method shows in its list, a private
  repository has a lock, the one row that needs attention has a badge.
- **Say it once** (principle 8). A fact shows in one place, as an icon or behavior where it can:
  a private repository has a lock and a credentials step, a public one has neither, and no text
  says that it is public.
- **The root reserves its scrollbar gutter** (`scrollbar-gutter: stable`), so a page with a
  scrollbar and one without have the same width.

## Tokens (`app.css`)

- **Type.** One sans family (Geist) for all text: titles, labels, repository names, paths,
  checksums, badges. One scale, as utilities from `app.css`: `text-title` (page title, 24/32
  semibold), `text-section` (section and step titles, 16/24 semibold), `text-label` (row labels,
  14/20 medium), `text-body` (body and tables, 14/20), `text-meta` (help, facts, timestamps, 13/18,
  always `text-muted-foreground`). Table headings and sidebar headings stay `text-xs font-medium
  text-muted-foreground`. Numbers in tables and checksums use `tabular-nums`. Sentence case
  everywhere; never all caps.
- **Contrast.** `--muted-foreground` (meta and help text) is at least 4.5:1 on `--background`,
  `--panel` and `--muted` in both themes (light 6.2 / 5.8 / 5.6, dark 7.8 / 7.4 / 6.3). So are
  `--success`, `--warning` and `--destructive` on the panel.
- **Monospace** (Geist Mono) only inside `CodeBlock` and `CodeSnippet`, and in the JSON editor
  of format settings (an editable code block). Nowhere else: not for names, paths, checksums,
  labels, badges, codes or input values.
- **Spacing.** Tailwind's 4px scale only. Breadcrumbs to title 12px; page header to content `mb-8`
  (32px, `PageHeader` does it); tabs to content 32px (`TabNav`). Between sections: 24px (`Section`
  does it). Inside a section: 24px padding (20px under 640px), 24px from the header to the content
  and from the content to the footer. Form grid: 24px between cells (`FormGrid`), 8px from a label
  to its control and from the control to its help (`FormRow`). Buttons in a row `gap-2`; toolbar
  to table `mb-4` (`Toolbar` does it). Do not add other margins between these blocks.
- **Control width.** Every text control fills its cell of the form grid, so every text control
  in a grid has one width. That covers an input, a select, a `ListInput`, a `SecretInput`, and a
  number with its unit (`UnitInput`: one control, the unit is a menu at its end). A field that
  needs the whole width spans both columns (`FormRow full`). Do not pass `width` and do not size a
  control in a page. Only a control that sizes itself sits at the start of its cell with
  `width="auto"`: a switch (`SwitchRow`), a checkbox, a button. The values `wide`, `medium` and
  `short` are deprecated and render the full width.
- **Rows.** A table row is one line, `h-row` (40px). Zebra stripes use `--stripe` (between the
  background and `--hover`), so a stripe and a hover never look the same.
- **Scrollbars.** The page and every inner scroll area use the themed scrollbar from `app.css`:
  a rounded thumb (`--scrollbar-thumb`) that darkens in light and lightens in dark under the
  pointer (`--scrollbar-thumb-hover`, `--scrollbar-thumb-active`), on a track that is the surface
  itself. Do not style scrollbars in a component.
- **Radius.** One radius, `--radius` (8px). Every `rounded-*` step resolves to it. `rounded-full`
  only for avatars, dots and progress bars.
- **Color.** Use the tokens: `bg-background`, `text-foreground`, `text-muted-foreground`,
  `border`, `bg-card` (the surface of a `Section`), `bg-panel` (quiet fill: details column, code,
  drop zone, help), `bg-hover`,
  `bg-stripe`, `bg-selected` + `text-selected-foreground`, `bg-primary` (brand yellow; main
  buttons), `text-link` (links in text), `text-destructive`, `text-success`, `text-warning`. No
  raw colors. Formats have no tint: their icon is the mark.
- **States.** Hover and selected fill the whole element: `bg-hover` under the pointer,
  `bg-selected` for the open or chosen element (sidebar item, tab, selected table row).
- **`cn()`** knows the type scale: `cn("text-meta", "text-muted-foreground")` keeps both classes.
- **Icons** (principle 9) mark meaning: a format, a lock, a state. An icon sits bare, in the text
  color of where it is, never in a colored or tinted square beside a heading or a row. A format's
  icon comes from its plugin (`FormatIcon`), never from Lucide.
- **Loading** (principle 11). No skeletons. `Loading` shows nothing for 300 ms, then a quiet
  spinner and "Loading" in the space the content will take. A reload keeps the current content on
  screen: test `loading && !data`. Switching views keeps the same area.

## Components (`lib/components/`)

| Component | Use |
|---|---|
| `PageHeader` | `title`, `description?` (one sentence), `crumbs?`, snippets `meta?` (badges next to the title), `actions?`. The page's only h1, with the breadcrumbs above it. On a Settings page the breadcrumbs come from the sidebar (Settings › group › item › title); `crumbs` replaces the steps after the group when the page sits deeper (`[{label: "maven", href}, {label: "Access"}]`). A Browse page shows breadcrumbs only when it passes `crumbs` (its whole trail). `back` is deprecated: use `crumbs`. |
| `Breadcrumbs` | `items: Crumb[]` (`{label, href?}`; the last is the current page). `PageHeader` renders it; use it directly only outside a page header (the Files tab's folder path). |
| `Toolbar` | The row above a table: filters as children, the main action in `end`. |
| `DataTable` | Every table, at the full content width: `source` (a `Keyset`, or `listOf(resource)`), `columns` (`{label, srLabel?, align?: "right", grow?, hideOnPhone?}`), `caption`, `key`, `href?` (whole-row click), `selected?`, snippets `row(item)` (the cells, as `Cell`s) and `empty?` (a `CompactEmpty`). Zebra stripes, one fixed row height (40px, one line), rounded row ends, a fill on the whole row under the pointer, the selected fill on the `selected` row. Loading (`Loading` in the space of four rows, after 300 ms; a reload keeps the current rows) and error (a row with Try again) show under the headings; an empty table is never drawn: `empty` shows in its place, without column headings. A `Keyset` gets "Load more". Bulk actions: `bind:picked` (a `Set` of row keys), `pickable?(item)`, `pickLabel?(item)` (the checkbox's name) and snippet `bulk` (the actions). Each row then gets a checkbox in a first column, a 40px hit target over the full row height; a row that is not pickable has an empty cell. The header checkbox picks every pickable loaded row and clears them on the next click ("Select the 50 rows shown" for a keyset). While rows are picked, a bar of the header's height lies over the headings: "12 selected", the `bulk` actions, "Select all 50", "Clear". Shift+click picks a range; Esc clears the pick when no dialog is open. `pickOnRow` (a table without `href`): a click anywhere on a pickable row, outside its controls, toggles its checkbox. `pickOff`: the checkboxes show, turned off. `selectAllLabel`: the text of the "Select all" button. `picked` is not `selected`. After an action, reload the table, empty `picked` and say what changed in a toast. |
| `Cell` | One cell of a `DataTable` row: `text?` (one line that truncates, full value in a tooltip), `copy?` (`true` or the value to copy: a copy button that shows on row hover), `copyLabel?`, `grow?` (the column that takes the free width, at least 10rem), `num?` (numbers, sizes, times: right-aligned, tabular numerals), `actions?` (the row's buttons, right-aligned), `strong?` (the row's name), `muted?`, `hideOnPhone?`. Children for anything else (a badge, a `RelativeTime`). Use `Table.Cell` only for a control that `Cell` does not cover, and keep it one line. |
| `Truncate` | `text`, `copy?`, `copyLabel?`, `class?`: one line that never wraps; "…" and a tooltip when it is cut (a path breaks after its slashes in the tooltip); an optional copy button. `Cell text` uses it; use it directly for a long value outside a table. |
| `RepoTable` | The repositories table (Browse home, All repositories): name, a lock after a private one, the kind icon after a group or a proxy, and the description on one line; format, files (a group: its number of repositories), size, last publish. On a phone the format icon, the name and the last publish. |
| `RepoKindIcon` | `kind`, `iconClass?`, `class?`. The mark of a group (layers) or a proxy (cloud download), with the word "Group" or "Proxy" for screen readers; nothing for a hosted repository. |
| `Section` | `title?`, `description?` (one sentence), snippets `actions?` (the area's actions at the header's end; on a phone they wrap under the title) and `footer?` (a submit row), `class?`. One logical area of a page on its own surface (a shadcn `Card`): 24px padding, 24px to the next section. A page with a table or a list puts it in a `Section` too. |
| `FormPage` | The body of a form page. It sets no width: every Settings page uses the full content column. |
| `FormSection` | `title?`, `description?`, snippets `actions?` and `footer?` (the submit row). A `Section` that holds a form: put one `FormGrid` in it, or two with a `<Separator />` between sub-groups. |
| `FormGrid` | The one form grid: holds `FormRow` and `SwitchRow` in two columns (`sm:grid-cols-2`), 24px apart; one column under 640px. Each label sits above its control and every control fills its cell, so there is no label column. |
| `FormRow` | `label`, `for`, `help?` (one sentence; id `{for}-help` for `aria-describedby`), snippet `more?` (longer help behind "Learn more"; it opens under the whole row), `aka?` + `akaLabel?` (the technical name: "In your provider: Client ID"), `full?` (span both columns), `width?` (leave it out: the whole cell; `"auto"` only for a switch, a checkbox or a button), `error?`, snippet `badge?`. The label above, the control, then the help, so the controls of a grid row line up. |
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
| `CompactEmpty` | `icon?`, `title?`, `state?`, `text`; children are the one action. An empty list or a feature that is not set up, as one row: a plain icon, "Passkey · Not set up", one sentence, one action. Pass it as `DataTable` `empty`. Inside a card it is a plain row; outside a card it has a dashed frame. Several rows in one card are parted by thin dividers, never a frame each. |
| `FeltTicket` | `top`, `bottom`, `serial`, `label`, `moment` (`none`, `print`, `punch`, `tear`). The brand felt ticket with printed lines, for the ticket moments only, at 320px wide or more. |
| `ConfirmDialog` | `bind:open`, `title` (a question), `description?`, `changes: string[]`, `confirmLabel`, `destructive?`, `typeToConfirm?` (a field; the confirm button stays off until it holds this text: a count or a name), `onConfirm` (async; errors stay in the dialog). A bulk delete names the count and the first five names (`someNames` in `lib/words.ts`): "Delete 12 repositories? oasis, plex, scissors, enginehub, worldedit and 7 more." |
| `ShortFormDialog` | `bind:open`, `title`, `description?`, `submitLabel`, `canSubmit?`, `onSubmit` (async); at most five `FormField`s as children. |
| `DetailsColumn` | `title`, `onClose`, snippet `actions?`: the details of the selected row in a 360px column right of the table. On the Files tab the column always shows (the selected file, or else the open folder); Close hides it until the person selects a file or clicks "Show details". |
| `TabNav` | Tabs that are pages (each tab has its own address): `label`, `items: {label, href, active, icon?}[]`. The open tab fills like a sidebar item; no line under the row; 32px to the content. |
| `CodeBlock` | `code`, `title?`: multi-line code with a copy button (monospace). |
| `CodeSnippet` | `value`, `label?`, `secret?`: one line to copy: a command, a token, an address (monospace). |
| `CopyValue` | `value`, `label?`: data in sans with a copy button (a checksum, a fingerprint). |
| `StatusBadge` | `tone` (`neutral`, `success`, `warning`, `danger`, `info`), `icon?`, the word as child. |
| `ViewSwitcher` | `bind:value`, `views` (`"list"`, `"tree"`, `"columns"`, `"coverflow"`), `label?`. The choice of view as one segmented control with icons and labels (icons only under 640px); arrow keys move. Hold the value in a `ViewChoice` (`lib/view.svelte.ts`), which remembers it per user. Offer only views that carry meaning: Tree (folders that expand in place) and Columns for deep trees, Cover Flow only for the versions and builds of one artifact (time is the axis). There is no Icons view. Every view renders in the same area: a switch never changes the layout around it. |
| `FormatIcon` | `format`, `kind?`, `label?`, `class?`. The format plugin's own SVG icon (from `GET /api/formats`), one color, 16px by default: the Maven feather, the Debian swirl, the raw file; `kind="gradle-plugin"` gives the Gradle elephant. Its space is reserved before the formats load. |
| `FormatBadge` | `format`, `title?`. The format's icon and title, inline, with no box and no tint. |
| `EmptyState` | `title`, `description?`, `icon?`; children are the action that fills it. A whole page or result that has nothing to show; a list uses `CompactEmpty`. |
| `ErrorState` | `error`, `title?`, `onRetry?`, `inline?` (one line in a form, a dialog or a table). |
| `Loading` | `rows?` (the height to reserve, in 40px rows; 4), `inline?` (one line in a form, a dialog or a row), `label?`. The one loading state: nothing for 300 ms, then a quiet spinner and "Loading" in the reserved space. Show it for `loading && !data`, so a reload keeps the current content. `DataTable` uses it. |
| `FreshFactor` | `actionLabel`, `onProof(proof)`: the fresh second-factor check, inline in a page. |
| `SecondFactorPrompt` | The same in a short dialog, before an action started from a button. |
| `SecondFactorForm` | The code or passkey form inside the two above and the sign-in page. |
| `SettingsGroupForm` | Registry settings rendered by type (a size or duration is a `UnitInput`, see `lib/units.ts`), saved from its `SaveBar`. When `effects` lists further effects of a save (a storage move), Save first asks in a confirm that lists every change. `sections` make it `FormSection`s (with `steps`, numbered `Step`s), so it is the whole form of a page (General, Storage, Sign-in options). Every field is a `FormRow` / `SwitchRow`; a list is a `ListInput`, a secret a `SecretInput`. `fields` gives a key a plain label, help, `aka` and placeholder; `errors` marks fields from a connection test; `before` / `extra` get the unsaved edits (`extra` renders under the last section: a connection test). |
| `OptionRow` | `id`, `label`, `description?`, snippet `control` (a `Checkbox` or a `RadioGroup.Item`), `children?` (a field or a note under the description): one choice in a list. A click anywhere on the control, the title or the description toggles it once; the title stays the one label of the control. `children` sit outside that hit target. The row fills when chosen. |
| `PermissionPicker` | `id`, `groups` (`{title, help?, items}[]`: `pickerGroups("server")` or `pickerGroups("repo")`), `bind:value`, `needs?` (`needsOf(scope)`: each permission and the permissions it needs), `held?` (the permissions the person can change; others are off), `columns?` (2 on a page), `onchange?`. Permissions as checkboxes in groups, one per subsystem, each with its title and help, All and None, and one line of help per checkbox. A check also checks what the permission needs, a clear also clears what needs it, and the help line says why. Always checkboxes, no presets. The grant dialog (`settings/repos/GrantDialog`) and the person page use it. |
| `VisibilityChoice` | `bind:anonymous`: Private or Public (new repository, repository Overview). |
| `SigningKeyChoice` | `keys`, `repoName`, `exclude?`, `bind:choice` (`{mode: "existing" \| "new", name}`): the signing key of a repository (the signing module's Signing tab and repo field). |
| `PermissionMapInput` | `id`, `value` (group → server permission IDs), `onchange`, `disabled?`: the groups of a permission map, each with Change and Remove, and Add a group; a short form with a `PermissionPicker` edits one. `SettingsGroupForm` uses it for a `map` setting with `permissionMap` in `fields`. |
| `RangePaste` | `id`, `label?`, `disabled?`, `onAdd?(ranges)`, `onReplace?(ranges)`, `buttons?`, `bind:found?`: paste addresses and ranges in any format. See below. |
| `FileSize`, `RelativeTime`, `QR`, `Logo`, `CopyButton` | Small parts. |

### Examples

A list page:

```svelte
<script lang="ts">
  const lists = new Resource(() => api.ipLists.list());
  const open = (l: IPList) => href(`/settings/networks/list/${encodeURIComponent(l.name)}`);
</script>

<PageHeader title="Networks" description="Named address ranges that repositories allow downloads and publishes from.">
  {#snippet actions()}<Button href={href("/settings/networks/new")}><PlusIcon />New network list</Button>{/snippet}
</PageHeader>

<DataTable source={listOf(lists)} caption="Network lists" key={(l) => l.name} href={open}
  columns={[{ label: "List", grow: true }, { label: "Used by", hideOnPhone: true }, { label: "Ranges", align: "right" }]}>
  {#snippet row(l)}
    <Cell grow strong text={l.title} />
    <Cell hideOnPhone muted text={l.repos.join(", ")} />
    <Cell num>{formatCount(l.count)}</Cell>
  {/snippet}
  {#snippet empty()}<CompactEmpty title="Network lists" state="None yet" text="A list names address ranges once, for many repositories." />{/snippet}
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
    <FormRow label="Description" for="desc" help="Shown in the list of repositories." full>
      <Input id="desc" bind:value={draft.value.description} aria-describedby="desc-help" />
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
  <DataTable source={listOf(passkeys)} ...>
    {#snippet empty()}
      <CompactEmpty icon={KeyRoundIcon} title="Passkey" state="Not set up" text="Sign in with your fingerprint." />
    {/snippet}
  </DataTable>
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
{#if view.value === "list"}<DataTable ... />{:else}<!-- the Cover Flow of felt tickets, built with the Browse pages -->{/if}
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

## Interaction rules

- **Navigation.** Every view has its own address, so the browser's back and forward buttons move
  between views. The way up is the breadcrumbs; a page never has a "back" link that goes
  somewhere else. Repository settings, like every Settings page, keep the Settings shell.
- **Unsaved changes.** A page that edits saved values holds them in a `Draft` and shows a
  `SaveBar`; it appears only with changes ("2 unsaved changes · Discard · Save"). While it shows,
  leaving the view asks "Leave this page?" (an in-app link, the back button, `navigate()`), and
  closing or reloading the tab gets the browser's prompt. After a save, `draft.commit()` so the
  bar goes away. Navigate after a save only once the draft is clean, or pass `{ force: true }`.
- **Centered dialogs only for confirmations (`ConfirmDialog`) and short forms (`ShortFormDialog`,
  at most five fields).** A longer flow is a page of its own, one level down in the breadcrumbs:
  create a repository, edit a CI pipeline, edit a network list, create or import a signing key,
  upload.
- **No side sheets.** The only sheet is the Settings menu under 768px.
- **Details of a selected row** show in a `DetailsColumn` inside the page, never in an overlay.
- **Rows are clickable anywhere** when they open something (`DataTable` `href`). Put the same link
  on the row's name for the keyboard. Buttons inside a row keep their own click.
- **One line per row.** A long value truncates (`Cell text`) with its full text in a tooltip and,
  for a value people paste elsewhere (a path, a checksum, an address), a copy button (`copy`).
  Never let a cell wrap to a second line, and never put a description under a name in a row: put
  it after the name, muted, on the same line.
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
- **A secret shows once**, in a `bg-panel` block above the table with `CodeSnippet secret` and a
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
- Subtitles for decoration. A description says what the page is for in one sentence, or is left
  out. No eyebrow labels, no all-caps labels.
- A second Sign in button.
- A page width, a container, a `max-w-*` on a page block, or side padding set by a page.
- A control sized by a page (`w-44`, `max-w-sm` on an input or a select in a form), two controls
  for one value (a number and a separate unit select), a bare checkbox for a yes/no setting.
- A table row of two lines, a table without `DataTable`, a number column aligned left.
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
- Unit tests and mocks.

## Checklist for every page

- [ ] The page is a `PageDef` of its module and in the table above; a Settings page has `nav` with an icon
      in its sitemap group, and its breadcrumbs read right (check a sub-page too).
- [ ] `PageHeader` with a title; a description only if it says something the title does not.
- [ ] No status row and no summary banner: state shows on the thing itself.
- [ ] Each area is a `Section` (a card); a connection has its `ConnectionTest` where it is
      configured.
- [ ] The page uses the whole content column; its left and right edges match every other
      Settings page (compare two screenshots).
- [ ] Tables use `DataTable` with `Cell`s: zebra stripes, one line per row, long values
      truncated with a tooltip (and `copy` where people paste them), numbers and times right
      with `num`, one `grow` column, secondary columns `hideOnPhone`. An empty list is a
      `CompactEmpty` row.
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
- [ ] Words follow the table below; buttons name the action; toasts reuse the verb.
- [ ] Screenshots at 1440 and 390 wide, dark and light, in `docs/design/screenshots/`
      (`<page>-<state>-<width>-<theme>.png`); it works at 390px with no sideways page scroll.

## Uploads and deletes

The repository page holds a publish token for the repository in memory only
(`pages/repo/session.ts`; never in storage or the URL), and drops it 30 seconds before it
expires or when the server answers 401.

- **Upload** (`pages/repo/UploadView.svelte`, `/repo/:name/upload`): first `FreshFactor`, which
  mints the token with `api.publish.manualSession`. Then one `DropZone` takes files or a folder,
  as the `upload` of the format's descriptor allows (a format without it takes no uploads).
  `folder.ts` reads a folder, at most 500 files, only the ones that the `keep` globs take.
  `api.repos.uploadPlan` sends the list of files and the first bytes of the files that a `reads`
  rule matches, and stores nothing; `PlanView` shows the plan (each item with the path of each
  file and its problems, the files left out), and the form shows the plan's fields (a value the
  plan read from the files is fixed). An edit of a field plans again after 400 ms. The ticked
  items then upload with `api.repos.upload` and progress, the fields first, each file with its
  path in the upload as its multipart filename.
- **Delete** (`pages/repo/FilesTab.svelte`): a person with Delete files deletes with the UI session,
  in a repository or in the cached files of a proxy.
  `delete/DeleteDialog.svelte` asks the server for the delete plan (`api.repos.deletePlan`)
  before the user confirms: the file count and the delete notes of the format (the versions that
  `maven-metadata.xml` stops listing, the packages that apt stops offering). In the
  List view, people who can delete pick rows (files and folders mix); the details column then
  shows "3 selected" and the size of the picked files, and Delete removes them all after one
  confirm. A version delete (`versionTarget` in `files/place.ts`) removes the version folder, or
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
| The permission labels and help of `GET /api/permissions` (Download, Publish, Delete files, Edit settings, Manage access, Manage CI pipelines; All repositories, Manage people, ...) and the phrases of a set ("Download, publish", "Everything") | Read / write permission, levels, roles other than Administrator |
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
