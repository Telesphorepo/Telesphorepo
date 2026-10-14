# Web UI

Svelte 5 + Vite (no SvelteKit), Tailwind CSS v4, shadcn-svelte (Maia style), TypeScript, bun.
`bun run build` type-checks with `svelte-check` and writes `web/dist`, which the Go binary embeds
(`web/embed.go`). `bun run dev` serves the UI on :5173 and proxies every other path to the backend
on :8080. Run both with `go tool task dev`.

The UI is only a client of the JSON API (`/api/...`). It has no tests of its own: e2e tests cover
every API route it uses.

## Structure

| Path | What it holds |
|---|---|
| `main.ts`, `App.svelte` | Mount; the shell: loads the user, matches the route, checks access, picks the layout. |
| `routes.ts` | The route table (below) and the redirects. |
| `app.css` | Tailwind, shadcn theme tokens (light and dark), brand color, fonts, width scale. |
| `pages/` | One component per route. `pages/browse/` for Browse, `pages/settings/` for Settings. |
| `lib/api/` | `client.ts` (fetch wrapper, `ApiError`), `types.ts` (JSON shapes), `index.ts` (`api.*` for every route). |
| `lib/auth.svelte.ts` | `auth`: current user, sign in (two steps), sign out. |
| `lib/router.svelte.ts` | Hash router: `router`, `href()`, `navigate()`, `isActive()`, `encodePath()`. |
| `lib/load.svelte.ts` | `Resource` (one request) and `Keyset` (a paged list) with loading and error state. |
| `lib/format.ts` | `formatSize`, `formatRelative`, `formatDateTime`, `formatCount`. |
| `lib/nav.ts` | The Settings sidebar: categories and their pages. |
| `lib/layouts/` | `BrowseLayout`, `SettingsLayout`, `BareLayout`, and the header parts (`AppHeader` holds the Repositories and Settings links). |
| `lib/components/` | Shared building blocks (below). |
| `lib/components/ui/` | shadcn-svelte components. Add more with `bunx shadcn-svelte@latest add <name>`. |
| `lib/formats/` | Format UI registry: the icon and hue of each format (`formatLook`), custom repo-config editors (`ConfigEditor`), JSON editor fallback. |
| `lib/browse/` | The Upload dialog (`upload/`, one fields component per format) and the Delete dialog (`delete/`) of the repo page. |
| `lib/webauthn.ts`, `lib/qr.ts` | Passkey create/get in the browser; QR encoder for TOTP setup. |
| `lib/network.ts` | `checkRange` (an address or CIDR, with a message for the reader) and `rangeSize`, for IP rules and trusted proxies. |

## Routes

URLs are hash URLs (`/#/path?query`), so the server never sees UI paths and the links that the
server and the clients print keep working: `/#/device?code=ABCD-EFGH`, `/#/publishing`,
`/#/login?error=<code>`. A page gets `params` (from `:name` and `*rest` segments) and `query`
(a `URLSearchParams`) as props.

| Path | Page | Layout | Access | What it shows |
|---|---|---|---|---|
| `/` | `browse/Home` | browse | any | The repository list: name, format, description, who can download, the visitor's access, last publish, size and file count. A filter box and format buttons narrow it. Admins create a repository here. |
| `/repo/:name/*path` | `browse/Repo` | browse | any | File tree of a repo. A folder `path` ends with `/` (`""` is the root); a file path opens the file panel: checksums, download, client snippet. "How to use" opens the client setup. Upload files, Upload a folder and Delete for write access. |
| `/search?q=&repo=` | `browse/Search` | browse | any | Search results (q needs 3+ characters). |
| `/login?next=&error=` | `SignIn` | bare | any | Sign in: SSO, password, then the second factor. |
| `/device?code=` | `DeviceApprove` | bare | any | Look up a device code, check the device, approve it with a fresh second factor (sign in first). |
| `/settings/profile` | `settings/Profile` | settings | user | Profile and password. |
| `/settings/security` | `settings/Security` | settings | user | Two-step verification: TOTP, passkeys, recovery codes. The only page of a setup-only session. |
| `/settings/devices` | `settings/Devices` | settings | user | Devices (admins see every user's). |
| `/settings/tokens` | `settings/Tokens` | settings | user | Download tokens (read tokens). |
| `/settings/publish` | `settings/Publish` | settings | user | Publish packages: build tool setup, a temporary publish password (manual publish session), CI setup, the caller's publish history. |
| `/settings/repos` | `settings/Repos` | settings | admin | List and create repositories. |
| `/settings/repos/:name[/:tab]` | `settings/RepoSettings` | settings | admin | Tabs `general` "Details" (description, anyone can download), `permissions` "Who has access", `network` "Network access" (IP rules: "Who can download" and "Who can publish"), `config` "Format options", `status` "How to use" (client snippets, format status), `danger` "Delete". |
| `/settings/users` | `settings/Users` | settings | admin | Users. |
| `/settings/sso` | `settings/SSO` | settings | admin | Single sign-on settings (group `sso`) and the discovery test. |
| `/settings/trusts` | `settings/Trusts` | settings | admin | CI publishing rules (trust policies), GitHub Actions and Jenkins presets. |
| `/settings/publish-log` | `settings/PublishLog` | settings | admin | Publish history: every publish session. |
| `/settings/storage` | `settings/Storage` | settings | admin | Storage settings, storage test, moves and their failures. |
| `/settings/nexus` | `settings/Nexus` | settings | admin | Import from Nexus: the Nexus servers (sources). |
| `/settings/nexus/:id` | `settings/NexusSource` | settings | admin | One Nexus server: the repositories to copy, syncs, failures, switch-over (cut-over). |
| `/settings/system` | `settings/System` | settings | admin | Server settings (base URL, limits, log level, signing key, sync interval, ...), "Reverse proxy" (`trusted_proxies`, with a Caddyfile and "Trust this address" for each proxy candidate) and "Network lists" (the fetched IP range presets, with Refresh). |
| `/settings/audit?action=&repo=` | `settings/Audit` | settings | admin | Activity log (the audit log). |

Redirects: `/publishing` → `/settings/publish` (the write-gate error prints it), `/settings` →
`/settings/profile`.

The header (`AppHeader`) always shows "Repositories" and, when signed in, "Settings", with the
open area filled. Signed out, it shows "Sign in"; signed-out visitors browse the repositories
that anyone can download from.

Access is checked in `App.svelte`, never in a page: `user` sends a signed-out visitor to
`/login?next=<path>`, `admin` shows "Admins only" to other users, and a setup-only session (no
second factor yet) goes to `/settings/security`.

Settings that come from the settings registry (`GET /api/settings`) render generically by
group with `SettingsGroupForm`; a Settings page picks the groups of its category (System:
`general`, `security`, `publishing`, `signing` and format groups; Storage: `storage`; SSO: `sso`).

## Add a page

1. Create `pages/<area>/<Name>.svelte` with `let { params, query }: PageProps = $props();`.
2. Add one entry to `routes.ts`: pattern, lazy `page`, `layout`, `access`, `title`.
3. For a Settings page, add it to its category in `lib/nav.ts`.
4. Add the route to the table above.
5. Call the API only through `api.*` (`lib/api/index.ts`). A new server route gets a function
   and its types there first.

## Design rules (spec §13)

**Width scale.** One scale, set in `app.css`:
- `max-w-page` (72rem): the content width. The layouts apply it; pages do not set a width.
- `max-w-form` (36rem): forms, form sections (`<Section narrow>`), side sheets with a form, and
  the column of the bare layout. The sign-in form inside it is `max-w-sm`.
- Tables use the full content width (`KeysetTable` does this).
- Dialogs: `sm:max-w-md` (shadcn default) for short ones, `sm:max-w-lg` for forms and
  `ConfirmDialog`, `sm:max-w-2xl` for Upload. Every dialog scrolls inside
  `max-h-[calc(100dvh-2rem)]`.
- Side sheets: override the shadcn width with the same variant, e.g.
  `data-[side=right]:w-full data-[side=right]:sm:max-w-xl`; a plain `sm:max-w-xl` loses to it.
  Content inside a sheet uses `px-6`, like its header.

**Spacing.** Page header to content `mb-8` (`PageHeader`); between sections `mb-10` (`Section`);
inside a section a heading to content `mb-4`; label to field `gap-2`; between fields `gap-4`;
buttons in a row `gap-2`. Do not add other margins between these blocks.

**Inputs sized to their content.** A field is as wide as the values it takes, not the column:
codes and ports `w-24`–`w-44`, names and usernames `max-w-sm`, URLs and paths `max-w-form`
(full column), JSON and PEM `Textarea` at full column width. Machine values (paths, checksums,
coordinates, tokens, snippets) use `font-mono`.

**Every request has three states.** Loading (`LoadingRows`, or a `Spinner` in the button that
started it), error (`ErrorState`, with `onRetry` for reads), and empty (`EmptyState` with the
action that fills it). Use `Resource` and `Keyset` from `lib/load.svelte.ts`.

**Confirm what changes.** Every action that changes or removes data asks with `ConfirmDialog`
and lists the effects, one line each. That includes one-click changes (disable, revoke, a new
access level), access changes ("Anyone can download") and server settings, which affect every user. A
form the user fills in and submits for one object (create a user, edit a description, upload
files) is its own confirmation. The confirm button names the action ("Delete 14
files"), and the toast after it uses the same verb ("Deleted 14 files"). A destructive action
in a table row is a `variant="destructive" size="sm"` button.

**Keyboard.** Every action is a `button` or a link; dialogs trap focus (shadcn does this); `/`
focuses the header search; visible focus rings stay on.

**Theme.** Light and dark through the tokens in `app.css` (`bg-background`, `text-foreground`,
`text-muted-foreground`, `border`, ...). Do not use raw colors. The brand yellow is
`bg-primary` (with `text-primary-foreground`); for links on the page use `text-link` (yellow
text is unreadable on white). Format icons and their tints come from `formatLook` in
`lib/formats`.

**Hover and selection fill the whole element.** Never mark a state with a left (inline-start)
border, an underline bar or any other edge accent: not in the sidebar, tabs, tables, cards or
callouts. Use the fill tokens:
- `bg-hover`: the element under the pointer (list rows, table rows, header links).
- `bg-selected` with `text-selected-foreground`: the open or chosen element (the sidebar page,
  the header area, a pressed toggle, the open file row, a checked row).
- A whole-row link (the repository list) is one `<a>` with the fill on the link itself.
- A danger section (`<Section tone="danger">`) is a light destructive fill, not an outline.
The shadcn primitives in `lib/components/ui/` (sidebar menu button, tabs, toggle, table row)
already use these tokens; keep them when you add or update a component.

**Text.** Sentence case. Active voice; name the action for the reader ("Copy the token",
"Sign in to approve"). Errors say what happened and what to do. Never show a client IP address
outside the activity log (the "Reverse proxy" card shows the addresses of proxy candidates), and
never a secret after the one time the server returns it.

**Plain words.** Write for someone who has never used a package server. Name things by what
they do for the reader, and keep one word for one thing on every page. When the technical term
helps (search, docs, logs), put it second, in muted text or in parentheses. API field names
and URL paths stay unchanged.

| Say | Not (technical term) |
|---|---|
| Anyone can download / People with access | Anonymous read, public / private |
| Can download / Can publish | Read / write permission |
| Download token | Read token |
| Temporary publish password | Publish session, session token |
| Two-step verification | Second factor |
| CI publishing rule; conditions; CI system address | Trust policy; claims; issuer |
| Signing keys (JWKS) | Pasted JWKS |
| Publish history | Publish log |
| Activity log | Audit log; actor, action, target |
| Who has access, Format options, How to use, Delete | Permissions, Config, Status, Danger zone |
| Import from Nexus; copy; switch over; final sync | Nexus sources; mirror, mapping; cut over; cut-over sync |
| Server settings; Where files are stored | General settings; Storage backend |
| Single sign-on | SSO (except the "Sign in with SSO" button) |
| Network access: who can download, who can publish; network lists | IP rules, read / publish allow-lists; IP range presets |

## Building blocks (`lib/components/`)

| Component | Use |
|---|---|
| `PageHeader` | `title`, `description?`, snippets `breadcrumbs?`, `actions?`, `children?`. The page's h1. |
| `Section` | `title?`, `description?`, `actions?`, `narrow?` (form column), `tone="danger"`. |
| `EmptyState` | `title`, `description?`, `icon?`; children are the action. |
| `ErrorState` | `error`, `title?`, `onRetry?`, `inline?` (one line under a form or in a dialog). |
| `LoadingRows` | `rows?`; `columns` inside a `Table.Body`. |
| `ConfirmDialog` | `bind:open`, `title`, `changes: string[]`, `confirmLabel`, `destructive?`, `onConfirm` (async; errors stay in the dialog). |
| `KeysetTable` | `list: Keyset<T>`, `columns`, snippets `head`, `row(item, i)`, `empty?`. Includes `LoadMore`. |
| `LoadMore` | "Load more" footer for a `Keyset` outside a table. |
| `CopyField` | `value`, `label?`, `block?` (code block), `secret?` (shown once). Copies to the clipboard. |
| `SecondFactorPrompt` | `bind:open`, `title`, `actionLabel`, `onProof(proof)`: fresh TOTP or passkey before a sensitive action (publish session, upload, delete). Under `telesphorepo dev` (`second_factor_optional` in `/api/auth/config`) a user without a factor can go on without one. |
| `SettingsGroupForm` | `settings`, `label`, `onSaved`, `visible?`, `sections?` (h3 groups), `effects?` (extra confirm lines), snippet `extra(patchBody)` (connection tests). Saves after a confirm that lists every changed setting. |
| `TrustSheet` | `bind:open`, `trust` (null creates), `repos`, `onSaved`. Wrap it in `{#key}`. |
| `NexusSyncDetail` | `id`, `onChanged`: one Nexus sync with live progress and its failed files. Wrap it in `{#key id}`. |
| `SecondFactorForm` | The form inside the prompt and the sign-in page: `methods`, `actionLabel`, `passkeyOptions`, `onProof`. |
| `FormatBadge` | `format`, `title?`. Icon and title of a format. |
| `FormatIcon` | `format`, `class?`. The tinted square tile in front of a repository name. |
| `AccessBadge` | `anyone` (the repo's `anonymous_read`): "Anyone" or "People with access". |
| `FileSize` | `bytes`. |
| `RelativeTime` | `at` (ISO time or null), `empty?`. |
| `QR` | `text`, `px?`. |
| `Logo` | `size?`. |

## Uploads and deletes

The repo page (`pages/browse/repo/RepoView.svelte`) holds the publish session. Upload needs a
publish session for the repo; delete needs one too, except for an admin, who deletes with the
UI session. Without a live session the page opens `SecondFactorPrompt`, mints one with
`api.publish.manualSession(repo, proof)` and keeps its token in memory only
(`pages/browse/repo/session.ts`; never in storage or the URL). It drops the token 30 seconds
before it expires, or when the server answers 401.

- `lib/browse/upload/UploadDialog.svelte` sends with `api.repos.upload(name, form, token,
  onProgress)`, which reports the progress of the body and returns `{done, abort}`. The format's
  fields go before the files in the multipart body.
- `lib/browse/upload/FolderUploadDialog.svelte` takes a whole folder: `FolderDropZone` reads a
  dropped folder (`webkitGetAsEntry`) or a chosen one (`webkitdirectory`) with each file's path
  inside it (`folder.ts`), at most 500 files. For Maven and Debian it keeps only the files that
  can be part of an upload (`folderKeep`) and counts the rest as skipped. The dialog sends them to
  `api.repos.scanUpload`, which stores nothing, and shows the answer in `ScanPreview`: each
  artifact or package with its files and problems, a checkbox per item (an item with problems
  cannot be ticked), and the files left out (the scan's and the skipped ones), folded. It then uploads the files of the ticked
  items with `api.repos.upload`; each file part's filename is its path in the folder. Debian
  and raw reuse `DebianFields` and `RawFields`; a Maven folder takes no fields.
- A folder dropped on `DropZone` ("Upload files") is not added: `UploadDialog` says "That is a
  folder — use Upload a folder", and its button hands the drop to `FolderUploadDialog`
  (`dropped`), which reads it when it opens.
- `lib/browse/delete/DeleteDialog.svelte` counts the files of a folder before the user confirms,
  and calls `api.repos.deleteFiles`. Deleting a Maven version folder leaves
  `maven-metadata.xml` listing it until the next publish; the dialog says so.
- The formats that take uploads (`maven`, `debian`, `raw`) are listed in `RepoView.svelte`.
