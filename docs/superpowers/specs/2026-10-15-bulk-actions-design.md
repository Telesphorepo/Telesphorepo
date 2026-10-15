# Bulk actions

## Goal

An admin changes many things in one step. After a Nexus import of 40 private repositories, the
admin selects all 40 and makes them public with one click.

Write routes that change a set take a list. One item is a list of one, so each change has one
route and the single-item routes that it replaces go away. Nothing keeps an old route alive.

## Where bulk actions go

| Page | Actions |
|---|---|
| All repositories | Make public, Make private, Delete |
| Files tab (List view) | Delete (files and folders mixed) |
| People | Disable, Enable |
| A person's page | Give access to repositories, Remove access |
| A repository's Access tab | Give people access, Remove access |
| Nexus import, repositories to copy | Pick for copying (see the Nexus spec) |

Tokens, devices, IP lists, signing keys and CI pipelines stay one at a time. A person has few of
each, so a selection costs more than it saves.

## The selection pattern (UI)

The pattern lives in `DataTable`, so each page only names its actions.

- `DataTable` takes `bind:picked` (a `Set` of row keys), `pickable?: (row) => boolean` and a
  `bulk` snippet. With them, each row gets a checkbox in a first column. A row that is not
  pickable has an empty cell there.
- The header checkbox picks every pickable row that is loaded, and clears them on the next
  click. It shows the mixed state when some are picked. A keyset table picks the rows on
  screen, and its label says so: "Select the 50 rows shown".
- While rows are picked, a bar takes the place of the header row: "12 selected", the `bulk`
  actions, then "Select all 50" (when not all are picked) and "Clear". It has the height of the
  header row, so nothing below it moves.
- The checkbox cell is a 40 px hit target over the full row height. A click on the rest of the
  row opens the row, as now. Shift+click on a checkbox picks the range from the last checkbox
  clicked. Esc clears the pick when no dialog is open.
- `picked` and the existing `selected` (the row that the details column shows) are different
  things and keep different names.
- After an action, the table reloads and the pick empties. A toast says what changed: "12
  repositories are now public."
- Every delete opens `ConfirmDialog` with the count and the first five names ("Delete 12
  repositories? oasis, plex, scissors, enginehub, worldedit and 7 more."). A delete of more
  than one repository asks the admin to type the count.

## Routes

Every list route checks every item before it changes any. Changes to rows apply in one
transaction, so a failure leaves nothing half done. Each changed item gets its own audit
event. A list holds 1 to 1000 items (400 outside that); `MaxJSONBytes` applies too.

### Repositories (admin, UI session)

`PATCH /api/repos` `{"names": [...], "anonymous_read": bool}`
: The one way to make repositories public or private. 404 with the first unknown name, and
  nothing changes. One `repo.update` event per repository whose value changed. Answer
  `200 {"changed": n}`.

`PATCH /api/repos/{name}` keeps `description` and `config` only. `anonymous_read` leaves it.
The visibility control on the repository's settings page calls `PATCH /api/repos` with one
name.

`POST /api/repos/delete` `{"names": [...]}`
: Replaces `DELETE /api/repos/{name}`. It checks that every name exists and that none is
  locked (409 with the name and the lock, nothing deleted), then deletes the repositories one
  after another with `files.DeleteRepo`. A delete removes blobs, so the set cannot be one
  transaction. On the first failure it stops and answers 500 with
  `{"deleted": [names done], "error": "..."}`. Answer `200 {"deleted": [names], "files": n}`.
  One `repo.delete` event per repository; `rules.Forget` per repository.

### Files (publish session of the repo, or an admin UI session)

`POST /api/repos/{name}/files/delete` `{"paths": [...], "prefixes": [...]}`
: Replaces `DELETE /api/repos/{name}/files?path=|prefix=`. Same gate (`auth.DeleteGate`).
  `repoWriteRoute` (`internal/auth/identify.go`) matches `POST .../files/delete` in place
  of `DELETE .../files`. 400 with the first bad path or prefix, nothing deleted. The paths go
  in one `deleteBatch`; each prefix then goes through `DeletePrefix`. A path that is gone
  already counts as done. Answer `200 {"deleted": n}`. One `file.delete` event per path and per
  prefix.

### People (admin, UI session)

`PATCH /api/users` `{"usernames": [...], "disabled": bool}`
: The one way to disable or enable people. 404 for an unknown name, nothing changed. 400 "You
  cannot disable your own account." when the list holds the caller and `disabled` is true. A
  disabled person's sessions end. One `user.update` event per person whose value changed.
  Answer `200 {"changed": n}`.

`PATCH /api/users/{username}` keeps `password` and `is_admin`. `disabled` leaves it.

### Access (admin, UI session)

`PUT /api/permissions` `{"users": [...], "repos": [...], "level": "read" | "write"}`
: Sets the level for every pair of user and repository. A person's page sends one user and
  many repositories; the Access tab sends many users and one repository. 404 for an unknown
  user or repository, nothing changed. One `permission.set` event per pair. Answer
  `200 {"changed": n}`.

`POST /api/permissions/delete` `{"users": [...], "repos": [...]}`
: Removes the level of every pair. One `permission.delete` event per pair that had one.

These two replace `PUT` and `DELETE /api/repos/{name}/permissions/{username}`.
`GET /api/repos/{name}/permissions` stays. `auth.Service` gets `SetPermissions` and
`DeletePermissions`, which take the lists; `SetPermission` goes.

## Pages

**All repositories** (`AllRepositories.svelte`, `RepoTable`)
: Every row is pickable. "Make public" and "Make private" run with no dialog, because the
  other one undoes them. "Delete" asks first.

**Files tab** (`FilesTab.svelte`, `FolderList`)
: The List view's rows are pickable when the person can delete (`canWrite`). Files and folders
  mix in one pick. While rows are picked, the details column shows "3 selected" and the total
  size of the picked files. The single delete in the Tree, Columns and Cover Flow views calls
  the same route with one item.

**People** (`People.svelte`)
: Rows are pickable except the admin's own. "Disable" asks first ("Disable 4 people? They are
  signed out and cannot sign in."). "Enable" runs at once.

**A person's page** (`PersonView.svelte`)
: "Give access" opens a dialog: a filter field, a checkbox list of every repository the person
  cannot reach yet with "Select all" and "Select none", and a choice of Download or Publish.
  The person's repository table is pickable, with "Remove access" and "Change to Download" /
  "Change to Publish".

**A repository's Access tab** (`AccessTab.svelte`)
: "Add people" picks several people at once in the same kind of dialog. The people tables are
  pickable, with "Remove access" and the level change.

## Words

The same verbs everywhere: "Make public", "Make private", "Delete", "Disable", "Enable", "Give
access", "Remove access". The bar says "1 selected" / "12 selected". `words.ts` holds the
plurals.

## Tests (e2e)

`e2e/bulk_test.go`, on every storage backend where files are involved:

- repos: make 3 public in one call and download from each without credentials; a list with one
  unknown name changes nothing; delete 2 and check that both answer 404 and their blobs are
  gone; a locked repository in the list deletes nothing.
- files: delete two files and one folder in one call; a bad path deletes nothing; a read token
  gets 401; a publish session for another repository gets 403.
- people: disable 2, check that their sessions end and that HTTP Basic fails for them; a list
  with the caller gets 400 and changes nothing.
- access: give one person read on 3 repositories, then 3 people write on one repository, and
  check each with a download or a publish; one unknown name changes nothing; remove access and
  check 403.
- every route: a password over HTTP Basic is refused, because these routes need a UI session.

The existing tests that call the removed routes (`e2e/files_test.go`, `e2e/network_test.go`,
`e2e/api_test.go`, `SetPermission` in `harness_test.go`) move to the new routes.

## Docs

`SPEC.md`: the new routes, the removed ones gone from the route table, the self-disable rule.
`web/src/README.md`: the `DataTable` pick props. `docs/design/rationale.md`: an entry "Bulk
actions" with screenshots of the repository list with rows picked.
