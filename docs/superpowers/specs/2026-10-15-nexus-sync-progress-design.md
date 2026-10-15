# Nexus import: true status, progress, a log, and a page that works

## Goal

An admin who imports from Nexus can see at a glance:

1. Whether a sync runs now, or the source waits for the next one.
2. How far the running sync is, and that it still moves.
3. That the mirror stays healthy: scheduled syncs run and pick up new files.

The admin can pick 40 repositories in two clicks, choose who can download them, and start the
first copy with the same click that saves the choice.

## What is wrong now

- The page shows "Copying" for every source that is not cut over. A source with no sync and no
  repositories also shows "Copying".
- Saving the repository choice starts no sync. The scheduler runs the first sync one
  `nexus.sync_interval` after the source was created, because `last_sync_at` starts at the
  creation time. Until then the page says "Copying" and nothing copies.
- The repository list has no "select all" and no filter. A repository that copies already
  cannot be taken out, although `SetRepos` supports that.
- The repository choice saves from the same SaveBar as the connection form, and the save
  does not say that a sync is the next step.
- New repositories are always private.
- A running sync shows file counts only: no bytes, no sign of activity, no history.

## Status of a source

The server derives one status and returns it as `status`. The UI shows it in the source page
header and in the Nexus list. The word "Copying" goes away.

| `status` | Badge | Line under the title |
|---|---|---|
| `no_repos` | Choose repositories | "Pick the Nexus repositories to copy." |
| `syncing` | Syncing | "Copying oasis-releases (4 of 11). 1,240 files, 84 MB so far." |
| `up_to_date` | Up to date | "Checked 4 minutes ago, 3 new files. Next check in 11 minutes." |
| `failed` | Last sync failed | The error, and "Sync now". |
| `cut_over` | Switched over | "Uploads go to this server." |

There is no "waiting for the first sync" status: saving repositories starts a sync (below).

## Schema (edit `internal/db/migrations/0001_init.sql`)

The project has no production data, so the initial migration changes in place.

- `nexus_sources.last_sync_at` defaults to 0 and means "never synced"; the creation time no
  longer goes in it. The scheduler treats 0 as due.
- `nexus_syncs` gains `trigger TEXT NOT NULL CHECK (trigger IN ('save','manual','schedule','cutover'))`.
  It replaces the `cutover` column (`cutover` is `trigger = 'cutover'`). `started_by` stays.
- `nexus_sync_repos` gains `checked`, `bytes` and `active_at` (`INTEGER NOT NULL DEFAULT 0`).
  `checked` counts the files read from Nexus (copied + skipped + ignored + failed). `bytes`
  counts the bytes copied. `active_at` is the time the runner finished the last page.
- New table:

```sql
-- A short log per sync, for the admin: one line per event, oldest first by id.
CREATE TABLE nexus_sync_log (
  id          INTEGER PRIMARY KEY,
  sync_id     INTEGER NOT NULL REFERENCES nexus_syncs(id) ON DELETE CASCADE,
  at          INTEGER NOT NULL,
  source_repo TEXT NOT NULL DEFAULT '',   -- '' for a line about the whole sync
  level       TEXT NOT NULL CHECK (level IN ('info','warn','error')),
  message     TEXT NOT NULL
) STRICT;
CREATE INDEX nexus_sync_log_sync ON nexus_sync_log(sync_id, id);
```

Queries go in `internal/db/queries`, then `go tool task gen`.

## Saving the repositories starts the copy

`PUT /api/nexus/sources/{id}/repos` takes `{"repos": [mappings]}`; each mapping carries
`anonymous_read` and, for apt, the signing key choice (below). When the saved set holds a repository, the route starts a sync with
`trigger = 'save'` in the same request and answers the source detail with `running_sync_id`.

A save while a sync runs answers 409, as now: the sync writes into the targets, so the set
cannot change under it. The UI says why (below).

## File dates come from Nexus

Now every imported file gets the time of the import as its upload date and its change date,
because `assets.UpsertTx` stamps `time.Now()` and the importer reads only the path, the size and
the checksum.

The Nexus asset list (`GET /service/rest/v1/assets`) returns the dates of each file. A real
answer from a Nexus server (October 2026):

```json
"lastModified": "2023-03-31T19:28:22.647-04:00",
"blobCreated":  "2023-03-31T19:28:22.647-04:00",
"blobUpdated":  "2023-03-31T19:28:22.647-04:00",
"lastDownloaded": "2026-10-15T00:55:57.151-04:00"
```

- `nexus.Asset` reads `blobCreated` and `blobUpdated` (falling back to `lastModified` when
  `blobUpdated` is missing).
- `format.ImportItem` gains `CreatedAt` and `UpdatedAt` (`time.Time`; zero means "now").
- `assets.Asset` gains the same two inputs. `UpsertTx` uses them when they are set and `now`
  otherwise. On an insert it writes both. On an update it writes `updated_at` from the input.
  `created_at` never changes on an update, as now.
- Each plugin's `Import` (maven, debian, raw) passes the item's dates into the asset it stores.
  A file that the plugin generates itself (a Debian index, a checksum it writes) keeps "now".
- A skipped file (same bytes already here) whose dates differ from Nexus gets the Nexus dates.
  So a second sync corrects the files that an earlier sync stamped with the import time.

`lastDownloaded` is not kept: this server does not record downloads. The Nexus `uploader` is a
Nexus account name with no user here, so it is not kept either.

## Who can download comes from Nexus

Now every repository that the import creates is private. When the source page lists the Nexus
repositories, the server checks each supported one without credentials: it asks Nexus for the
first page of the repository's asset list, and when there is an asset, it sends a `HEAD` for
that asset's download URL. A 200 means anyone can download it in Nexus. A 401 or 403 means
only people with access can. An empty repository counts as private.

`SourceRepo` gains `public` (bool). The "Name here" column gets a "Who can download" column
with a per-row choice, Anyone or Only people you give access, set to what Nexus does. The
single choice under the table goes away: the per-row value is the default from Nexus, and the
admin changes a row only to differ from Nexus. The pick bar offers "Make the picked ones
public" and "Make the picked ones private". `PUT .../repos` takes `anonymous_read` per mapping
in place of one for the save. It applies to repositories the save creates; an existing target
keeps its setting.

## The Debian signing key

Now an imported apt repository signs its indexes with a new key here, so after the switch-over
every machine that installed the Nexus key fails `apt update` until it trusts the new key.

For a picked apt repository, the row has "Signing key" with two choices: "A new key" (the
default signing key here, as now) and "The Nexus key".

Whether Nexus gives the key pair over its API is not checked yet. The implementation checks it
first against the Nexus container of the import zone: `GET /service/rest/v1/repositories/apt/hosted/{name}`
with the source's account, which must have the repository admin privilege.

- If the answer holds the armored private key (`apt.signing` or similar), the import reads it
  and stores it as a signing key here ("nexus-<repo>"), sealed like any signing key, and
  assigns it to the new repository. The passphrase comes the same way or from the admin.
- If it does not, "The Nexus key" opens the existing import dialog of Signing keys (armored
  private key and passphrase), then assigns the imported key to the new repository. The help
  text names the place in the Nexus UI where the key pair is, as the container shows it.

The e2e test creates the Nexus apt repository with a known key pair, imports it, and checks
that `Release.gpg` here verifies with the Nexus public key.

## Proxies and groups

The import also takes Nexus proxy and group repositories of a supported format. The proxy and
group specs (`2026-10-15-proxy-repositories-design.md`, `2026-10-15-group-repositories-design.md`)
describe how. The reason "only hosted repos can be mirrored" goes away.

## Repositories that are offline in Nexus

Nexus marks a repository `online: false`. Its files cannot download, so a sync of it fails. The
list shows it as not supported, with the reason "Offline in Nexus. Bring it online in Nexus to
copy it." A mapped repository that goes offline fails its part of the sync with the same
words in the log.

## Progress

Nexus gives no total count for a repository: its asset list is pages of 100 with a continuation
token. So progress is counts and activity, with an estimate when an earlier sync gives one.

`GET /api/nexus/syncs/{id}` returns, per repository: `state`, `checked`, `imported`, `skipped`,
`ignored`, `failed`, `bytes`, `active_at`, and `previous_checked` (the `checked` of the newest
earlier `done` sync of this source for this repository, 0 when there is none). The sync adds the
totals.

The sync detail shows, per repository row:

- "1,240 copied (84 MB) of 12,400 checked".
- "Last activity 3 seconds ago" while it runs.
- With `previous_checked > 0`: a bar and "about 40%" (`checked / previous_checked`, capped at
  99% until the repository is done). A first sync shows no bar, because there is nothing to
  divide by.
- "No progress for 3 minutes" in the warning tone when `active_at` is older than two minutes
  while the repository runs. A Nexus request has a 2-minute limit and the runner retries a
  failed page with back-off (`sync.go`), so a long wait is possible without a failure.

The failed files list shows while the sync runs too, and reloads with the progress.

## The log

The runner writes lines in the same transaction as the counters where it can:

- "Started after the repositories were saved." / "Started by sam." / "Scheduled sync started."
  / "Final sync before the switch-over started by sam."
- "oasis-releases: started." / "oasis-releases: resumed after a restart."
- One line per page that copied something: "oasis-releases: checked 100, copied 12 (3.4 MB)."
- "oasis-releases: no new files." when a repository copied nothing.
- "oasis-releases: done. Copied 1,240 (84 MB), skipped 11,160, failed 0."
- A warning for each retry of a Nexus page, with the error text; an error for each failed
  repository.
- "Finished in 6 minutes. 34 new files (2.1 MB)." / "Cancelled by sam." / "Failed: ..."

A line names a repository and counts, never a path, a URL with credentials, a password or a
token. Failed files stay in `nexus_sync_failures`.

Retention: when a sync starts, the runner deletes the syncs of the source after the newest 100,
with their rows in the other tables (cascade). The cut-over sync stays.

`GET /api/nexus/syncs/{id}/log?after=&limit=&repo=` (admin) returns lines oldest first, keyset
on `id`. The `repo` filter reads the sync's lines through the index and filters in Go; a sync
has a few thousand lines at most.

## The source page

Top to bottom:

1. **Header.** Name, address, the status badge and its line. Actions: "Sync now" (when no sync
   runs) or "Cancel the sync" (while one runs), "Switch over", "Delete". "Delete" while a sync
   runs says "Cancel the sync first" in its tooltip instead of being greyed out with no reason.

2. **Repositories to copy.** A `DataTable` with the pick pattern of the bulk-actions spec:
   - The header checkbox picks every supported repository, and clears them on the next click.
     The bar offers "Select all supported" and "Clear".
   - A filter field above the table, when there are more than 10 repositories.
   - Supported repositories first, then the unsupported ones with their reason and no checkbox.
   - Columns: Nexus repository, Kind ("Maven, hosted"; the words come from `words.ts`, not the
     raw Nexus ids), Name here. A repository that copies already shows its name here as a
     link and stays picked; unpicking it stops the copy and keeps its files here.
   - "Who can download" per row, set from Nexus; "Signing key" per apt row (sections above).
   - One button under the table: "Copy 11 repositories" (or "Save" when the set only shrinks).
     It saves and starts the sync, and the page opens the sync. This part leaves the SaveBar.
   - While a sync runs, the checkboxes are off and a line says "You can change the
     repositories when this sync ends."

3. **The running or chosen sync.** The sync detail with its progress and its log (below).

4. **Syncs.** The history: Started, How (Saved / Manual by sam / Scheduled / Final), Took,
   New files, Size, Failed, State. A run of scheduled syncs with 0 new files and the state Done
   shows a healthy mirror with no new uploads. The help text: "This server checks Nexus for
   new files every 15 minutes." with a link to the interval setting.

5. **Connection.** Address, username, password, saved with the SaveBar as now.

## The sync detail

- The repositories table with the progress columns above. A click on a row filters the log to
  that repository; "All repositories" clears the filter.
- "Log": rows of time, repository, message, with the warning and error tones. While the sync
  runs, the UI asks for new lines every two seconds (`after` = the last id). It keeps the view
  at the newest line unless the admin scrolled up.
- "Files that failed to copy", also while the sync runs.

## The Nexus list

Columns: Server, Status (the badge), Repositories ("11"), Last check (relative time), Next
check. A syncing source shows its progress line in place of the last check.

## API summary

- `GET /api/nexus/sources`, `GET /api/nexus/sources/{id}`: add `status`, `next_sync_at` (0
  when none is due: no repositories, or cut over), `running_sync_id` (0 when none).
- `GET /api/nexus/sources/{id}`: each repository adds `public` (what Nexus does) and
  `offline`.
- `PUT /api/nexus/sources/{id}/repos`: body `{repos}` with `anonymous_read` and the apt
  signing key choice per mapping; starts the sync.
- `GET /api/nexus/syncs/{id}`: the progress fields; `trigger` replaces `cutover`.
- `GET /api/nexus/syncs/{id}/log` (new).

## Tests (e2e, import zone, `e2e/nexus_test.go`)

- A new source has `status = no_repos` and `next_sync_at = 0`.
- The fixture gives anonymous read to one hosted repository and not to another. The source
  detail reports `public` true and false for them. A repository set offline in Nexus shows as
  not supported with the offline reason.
- Saving repositories with `anonymous_read: true` creates public repositories, starts a sync
  with `trigger = save`, and the status is `syncing`. After it, the status is `up_to_date` and
  `next_sync_at` is one interval after its end.
- After a sync, each repository has `checked` = copied + skipped + ignored + failed, and `bytes`
  = the sum of the sizes of the files it copied.
- The log has the start line, lines per repository and the finish line. A second sync with no
  new files writes "no new files" for each repository and shows `previous_checked`. The `repo`
  filter and `after` paging work.
- Unpicking a repository unlocks it: an upload with a publish session succeeds.
- No log line contains the Nexus password.
- An imported file's `created_at` and `updated_at` (from the files API) equal the Nexus
  `blobCreated` and `blobUpdated` of the source asset, to the second. A Maven file and a Debian
  package both check this. A file that Nexus did not date (none in the test data) gets the time
  of the import.
- The existing tests move from `cutover` to `trigger`.

## Docs

`SPEC.md`: the statuses, the fields, the log route and table, the changed `PUT .../repos`, and
that saving starts a sync. `docs/design/rationale.md`: an entry "Nexus import" with
screenshots of the repository pick, a running sync with its log, and the history.
