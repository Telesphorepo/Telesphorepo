# Group repositories

## Goal

A build points at one URL and gets the files of several Maven repositories: for example
`plex` serves `plex-releases`, `plex-snapshots`, `plex-modules` and a proxy of Maven Central.
The build lists one repository instead of four. The Nexus import creates a group here for each
Nexus group, as a convenience; it is not a promise that every Nexus URL keeps working.

Groups are for Maven. A format joins later by implementing the group interface below.

## Model

The project has no production data, so `0001_init.sql` changes in place.

- `repos` gains `kind TEXT NOT NULL DEFAULT 'hosted' CHECK (kind IN ('hosted','proxy','group'))`
  (proxy: see the proxy repositories spec).
- New table:

```sql
-- The members of a group repo, in the order a read tries them. A member is a hosted or proxy
-- repo of the group's format; a repo can be in several groups.
CREATE TABLE repo_group_members (
  group_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  member_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  position  INTEGER NOT NULL,
  PRIMARY KEY (group_id, member_id),
  UNIQUE (group_id, position)
) STRICT, WITHOUT ROWID;
CREATE INDEX repo_group_members_member ON repo_group_members(member_id);
```

Rules, checked by `repos.Service`:

- A member has the group's format and `kind` `hosted` or `proxy`. Groups do not nest.
- A proxy member fetches from its remote only when no member before it has the file. Put the
  hosted members first, so a remote cannot shadow your own artifacts; the UI orders a new
  group that way and warns when a proxy comes before a hosted member.
- A group holds 1 to 50 members.
- Deleting a member removes it from its groups (cascade). A group with no member left stays
  and serves nothing; its page says so.
- A group has no files, no Files tab and no uploads. Its own `anonymous_read`, people and IP
  rules work as on any repository.

The repos service keeps the members of each group in its in-memory snapshot, refreshed when a
group or a member changes, so a read through a group does no extra membership query.

## Reads through a group

`format.Grouper` (new optional interface) marks a format that serves groups. Maven implements
it. The core refuses `kind = 'group'` for a format without it.

`GET /maven/{group}/{path}`:

1. The reader needs read access to the group (`auth.RepoGate`, the group's IP rules).
2. The plugin tries the members in order. It skips a member that the reader cannot read: the
   reader's access and the member's IP rules both count. So a group never shows a private
   member's files to someone who cannot read that member, even when the group is public.
3. The first member that has the file serves it.
4. An artifact-level `maven-metadata.xml` merges across the readable members: the union of the
   versions, the newest `latest`, `release` and `lastUpdated`. Its `.md5`, `.sha1` and
   `.sha256` are computed from the merged bytes. A version-level `maven-metadata.xml` (a
   snapshot's) comes from the first member that has it. Nexus groups behave the same way.
5. No member has it: 404.

`PUT` to a group answers 405 "A group is read-only. Publish to one of its repositories."
Publish sessions, devices and CI pipelines never name a group.

## API

- `POST /api/repos` takes `kind` (`hosted` default, or `group`) and, for a group,
  `members: [names]`.
- `PUT /api/repos/{name}/members` `{"members": [names]}` (admin) replaces the members and their
  order. 400 for a member of another format, a group, or an unknown name.
- `GET /api/repos/{name}` returns `kind` and, for a group, `members` (name, format, public).
  For a hosted repository it returns `groups`: the groups it is in.
- Bulk actions apply to groups as to any repository (make public, private, delete).

## UI

- **New repository**: a first choice, "A repository" or "A group of repositories". A group asks
  for its name and its members (a pickable list of the hosted repositories of the format, with
  the order shown as numbers).
- **A group's settings**: an Overview with the usage snippet ("Use this one URL in your build")
  and the members in order, with "Move up", "Move down", "Remove" and "Add repositories". When
  the group is public and a member is private, the member row says "Only people with access to
  this repository get its files through the group."
- **A hosted repository's Overview** lists the groups it is in, as links.
- The repository list shows a group with the group icon and its member count in place of the
  file count.
- Search, the Files tab and uploads do not apply to a group; its page links to each member.

## Nexus import

The source page lists the Nexus groups under the repositories, in their own table "Groups".

- The member names come from `GET /service/rest/v1/repositories/maven/group/{name}`
  (`group.memberNames`). The implementation confirms that field against the Nexus container of
  the import zone first. If the source's account cannot read it (403), the row says "The Nexus
  account cannot read this group's members" and the admin picks the members here by hand.
- A group is pickable when at least one of its members is picked. The group here gets the
  picked members (hosted copies and proxies), in the Nexus order. A member that the admin did
  not pick is listed under the row: "maven-releases is not picked, so it is not in this group."
- "Who can download" comes from Nexus the same way as for repositories (an anonymous request).
- A group has no files, so syncs do not touch it. Its members change when the admin saves the
  page again; a group whose Nexus members changed shows "Nexus has different members now" with
  the difference.
- Switch-over does nothing more for groups: they serve from here already.

## Tests (e2e)

`e2e/groups_test.go`, on every storage backend:

- A group of a release repository and a snapshot repository: `mvn` and Gradle resolve a
  release and a snapshot through the group URL.
- The merged `maven-metadata.xml` lists the versions of both members, and its `.sha1` matches.
- The order decides when two members hold the same path.
- A public group with one private member: anonymous gets the public member's files and 404 for
  the private member's; a reader with access to the private member gets both.
- `PUT` to the group answers 405. Deleting a member removes it from the group.
- A group of another format's member, a nested group and an unknown member answer 400.

- A group of a hosted repository and a proxy: a file in both comes from the hosted one; a file
  only upstream comes through the proxy.

`e2e/nexus_test.go` (import zone): the fixture has a Maven group of two hosted repositories and
a proxy; the import creates the group with the three members in the Nexus order.

## Docs

`SPEC.md`: the `kind` column, the members table, the read rules, the routes, the import of
groups. `web/src/README.md`: the new repository choice. `docs/design/rationale.md`: an entry
"Group repositories" with screenshots of a group's page and the import's group table.
