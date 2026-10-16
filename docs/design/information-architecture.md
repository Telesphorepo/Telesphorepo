# Information architecture

Status: built (2026-10-14). The sitemap, the access model and the navigation rules below are the UI
as it ships. The section
"As built: deviations" at the end lists what differs from the proposal. The reason for each page's
design is in [rationale.md](rationale.md).

## Why the current organisation fails

Owner review of the round-1 UI found problems no page polish can fix:

- **Access is split across three unlinked pages.** Public/private is on the repo page, people and
  their levels on the People page, network limits on the IP lists page. Nothing shows the final
  answer to "who can download this repo, and who can publish to it?". You must visit every page and
  hope it adds up.
- **Two unrelated systems look like one.** Trusted proxies (how the server finds the visitor's
  address behind Caddy or Cloudflare: server plumbing, set once) sits beside repo network limits
  and offers the same IP lists (e.g. Blacksmith), which makes no sense for a proxy.
- **Account shows trivia** ("Member since 49 minutes ago", "Signs in with a password") instead of
  what a person needs: which repos they can use and how.
- **Navigation is not navigation.** Repo settings leave the Settings shell (the sidebar disappears)
  and its back link goes to the repo's file view instead of back to where you came from.
- **No visual system for density.** Controls of different widths on one page and between pages,
  table rows of one or two lines, no zebra stripes: hard to read at a glance.

## The user's tasks (what the architecture is organised around)

| Person | Top tasks |
|---|---|
| Developer (reader/publisher) | Find an artifact and copy its dependency line · Set up publishing from my laptop or CI · See which repos I can use |
| Admin | Create a repo and decide who can download and publish · Add people · Connect CI · Answer "who can access X?" · Keep the server healthy (storage, keys, proxy) · Move off Nexus |

## The access model (the core of the IA)

Every repository answers exactly two questions, and each answer has the same three layers:

| | Download | Publish |
|---|---|---|
| **Who** | Anyone (public), or people with access (admins always) | People with publish access (from a device or the browser), and CI pipelines (CI rules that list the repo) |
| **From where** | Any network, or only the listed networks | Any network, or only the listed networks |
| **Blocks** | — | Mirrored from Nexus (temporarily read-only) |

The UI always shows the **effective** answer as a sentence before any controls, e.g.

> **Download:** anyone on the internet.
> **Publish:** 3 people and 1 CI pipeline (acme/app, branch main), only from GitHub Actions.

## Sitemap

```
Browse (everyone)
├─ Repositories                      the list
│  └─ <repo>                         Files · Use this repository · Publish to this repository
└─ Search

Settings (signed in)
├─ Your account
│  ├─ Overview                       your repos and what you can do in each; your devices; recent publishes
│  ├─ Sign-in and security           password (changed in a dialog) + two-step verification + recovery codes
│  ├─ Devices
│  └─ Tokens                         read tokens · publish tokens (one page, two sections)
├─ Repositories (admins)
│  ├─ All repositories               every repository; a row opens its settings
│  └─ <repo>                         Overview · Access · Format settings · Signing (Debian), inside the Settings shell; Delete is an action in the header
├─ People and CI (admins)
│  ├─ People                         every account; a row opens the person
│  │  └─ <person>                    role, sign-in, account actions; access to each repo (add, change, remove)
│  ├─ CI pipelines                   CI rules; each shows the repos it can publish to (links)
│  └─ Networks                       IP lists; each shows the repos that use it (links)
└─ Server (admins)
   ├─ Sign-in options                single sign-on, password sign-in for admins
   ├─ Signing keys
   ├─ Storage
   ├─ Nexus migration
   ├─ Network and proxy              trusted proxies only: none / proxy on this machine or network /
   │                                 Cloudflare / custom addresses; detected proxies; NOT IP lists
   ├─ General                        public URL, upload limits, logs, retention
   └─ Activity
```

**Cross-links are part of the architecture**: a person's page links to each repo they can access and edits that access in place; a CI
pipeline and a network list link to the repos that use them; a repo's Access tab links to each
person, pipeline and list it uses, and edits them in place where the change is repo-specific
(adding a person to this repo, choosing networks for this repo). Each task has every path a
person might start from (principle 13): access to a repository is granted from the repository's
Access tab or from the person's page, and a CI pipeline is added from the CI pipelines page or
from the repository's Access tab.

## The repo Access tab (one place)

1. **Download**: Public switch; then "People who can download" (a table of the people who can
   download and not publish, so each person shows once; Add person and remove in place); then "From where" (any network / these networks:
   Add network list and Add addresses each open a short form in a dialog).
3. **Publish**: "People who can publish" (same table, publish level); "CI pipelines" (rules that
   list this repo; Add CI pipeline opens a dialog that adds existing rules or links to New CI
   pipeline, `/settings/ci-pipelines/new?repo=<repo>&back=<this tab>`, which comes back here
   after Create); "From where".
4. A lock notice when the repo mirrors Nexus.

## The repo page: using vs publishing (two tasks, never one "Setup" tab)

"Setup" mixed two jobs with different people and different moments. They become two tabs, named
by the task:

- **Use this repository** (every developer, first visit): add the repo to a build. Tabs per client,
  remembered per browser: **Gradle (Kotlin)**, **Gradle (Groovy)**, **Maven**, plus credentials when
  the repo is private (settings.xml / gradle.properties / environment variables). Debian: apt
  source line and key. Raw: curl/wget.
- **Publish to this repository** (publishers): three short paths, each its own section, chosen by
  where you publish from: **From your computer** (the setup line of the Telesphorepo CLI, one-time
  device approval), **From CI** (GitHub Actions or Jenkins: the CI pipeline rule to create, and the
  workflow snippet), **By hand** (upload in the browser, or a publish token with curl).

**Per-artifact snippets** live in the artifact view (Browse), not in repo setup: the dependency for
*this* artifact, with the same client tabs, and a version choice:
- **This version** (pinned, e.g. `1.4.2`), the default for releases;
- **Always the newest** (Gradle `latest.release` / a dynamic version; Maven `RELEASE` or a range),
  with one line on the trade-off (builds stop being reproducible);
- the repository block it needs, so the snippet works when pasted (Nexus offers a coordinate only).
Gradle plugins get a `plugins { id("…") version "…" }` block plus `pluginManagement` repository,
never an `implementation(...)` line.

All snippets come from the format plugin (`format.Describer` returns structured snippets: client,
variant, title, code), so a new format brings its own instructions; the UI only renders tabs.

## Navigation rules

- Settings pages, repo settings included, always keep the Settings shell (top bar + sidebar).
- Every nested page has a breadcrumb that reflects the hierarchy (Settings › Repositories › maven ›
  Access); the browser back button returns to the previous view; no "back" link that goes
  somewhere else.
- Leaving a page with unsaved changes asks first.

## Layout and density rules (the form rule is replaced by principle 12; see the deviations)

- **One content width** for every Settings page: the full content column next to the sidebar.
  Tables use the full column.
- **Forms**: a fixed 14rem label column and one control column; **every text control in every
  form has the same width** (the full control column, max 32rem). A number with a unit is one
  control of that same total width. Switches and checkboxes sit at the start of the control column.
  Help is one line under the label; longer help goes behind a "Learn more" disclosure.
- **Tables**: zebra stripes, one fixed row height (single line; long values truncate with a
  tooltip and a copy affordance), numeric and time columns right-aligned with tabular numerals,
  whole-row hover fill.
- Save: a sticky bar appears only with unsaved changes ("2 unsaved changes · Discard · Save").

## What moves where (from today)

| Today | Becomes |
|---|---|
| Profile and password (member since, signs in with password) | Overview (your access) + password moves to Sign-in and security |
| Read tokens, Publish tokens | Tokens (one page) |
| Repo Network tab + IP lists + trusted proxies sharing lists | Repo Access tab "From where" + Networks (IP lists) + Network and proxy (proxies only, no lists) |
| Visibility on repo Overview, permissions on People | Repo Access tab (both), and each person's page edits that person's access to every repo |
| Single sign-on | Sign-in options |
| General + Reverse proxy | General · Network and proxy |
| Repo "Setup" tab (consume + CI + upload mixed) | "Use this repository" and "Publish to this repository"; per-artifact snippets in Browse |

## As built: deviations

These differ from the proposal above. Each one follows from a principle in
[principles.md](principles.md).

| Proposal | As built | Why |
|---|---|---|
| Forms: a fixed 14 rem label column and one control column. | A two-column card grid. The label sits above each control, every control fills its cell, and a long field spans both columns. | Principle 12. A label column left gaps and made controls look lost. |
| Pages open with a sentence or a summary about their state. | No page has a status row. | Principle 1. |
| An Access overview page shows every repository with its download and publish answers. | No such page. Each repository's Access tab and each person's page hold the answers and edit them; a page that only links to another tab does not exist. | Principles 13 and 15. |
| People shows each person's access read-only, and a row opens a details column. | Each person has a page, `/settings/people/:name`. It edits the account and the person's access to each repository. | Principle 13. |
| Repo settings tabs: Overview · Access · Format settings · Delete. | Overview · Access · Format settings · Signing (Debian). **Delete** is an action in the header that opens a type-the-name dialog. The old `/delete` address is gone. | Principle 10. A page section never holds a lone action button. |
| The password is a form on Sign-in and security. | A Sign-in card with one row, "Password" and **Change**, which opens a dialog. | Principle 10. |
| Public or private shows as a property of the repo. | A lock after the name for a private repo, and no mark for a public one. No Visibility column, no pill. | Principle 8. |
| Create a CI pipeline and a network list from the list page. | Both open their own page from **New**, because each has several sections. A person, a Nexus server and a token open a dialog. | Principle 10. A long, multi-step flow gets its own page. |
| The Use tab shows credentials when the repo is private. | A private repo's Use tab has two numbered steps (create a read token, add it to the build). A public repo's has one card. The Publish tab shows only to people who can publish. | Principles 2 and 8. |
| Finder-style views are List and Columns. | List, Tree, Columns and Cover Flow share one area, so a switch does not move the layout. Cover Flow shows only where an artifact or a snapshot has several builds. | Owner decision in [ideas.md](ideas.md); principle 11. |
| Network and proxy, IP lists, Single sign-on and CI publishing keep their names. | Network and proxy is `/settings/proxy`, Networks is `/settings/networks`, Sign-in options is `/settings/sign-in`, CI pipelines is `/settings/ci-pipelines`. The older addresses redirect. | Navigation rules. |
| Overview lists your repos, devices and recent publishes. | The same, in one card each. The "You can" column shows only when its values differ. | Principle 1. |

Not built:

- The password row does not show "changed N days ago". The API does not send `password_changed_at`
  (the column exists in `users`). Add it to `/api/me`, then show a `RelativeTime` after the word
  "Password".
