-- The counters of group repos: a group counts each file that it serves once, from the member that
-- a group read takes it from.
--
-- assets.unit is the part of the repo that a group serves from one member (format.Spec.GroupUnit):
-- a format whose group serves every file of a package from the member that owns the name (npm,
-- PyPI, Cargo, NuGet) names the package; '' for a file that a group serves path by path, from the
-- first member that has the path. NULL: not set yet (a row written before this column, or by the
-- seed command); the job core.group-counts sets it before it counts.
ALTER TABLE assets ADD COLUMN unit TEXT;
CREATE INDEX assets_unit ON assets(repo_id, unit) WHERE unit <> '';
CREATE INDEX assets_unit_unset ON assets(repo_id) WHERE unit IS NULL;

-- One row per group and set of members that a reader reads (members: their ids in read order,
-- comma-separated). The job core.group-counts counts a row in one transaction; the triggers below
-- keep it in the transaction of every asset write after that. A change of the members of a group
-- deletes its rows. last_published_at is the newest updated_at of a non-index file the group
-- serves; a delete never lowers it.
CREATE TABLE group_counts (
  id                INTEGER PRIMARY KEY,
  group_id          INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  members           TEXT NOT NULL,
  file_count        INTEGER NOT NULL DEFAULT 0,
  total_size        INTEGER NOT NULL DEFAULT 0,
  last_published_at INTEGER NOT NULL DEFAULT 0,
  UNIQUE (group_id, members)
) STRICT;

-- The members of a row of group_counts, in read order.
CREATE TABLE group_count_members (
  count_id  INTEGER NOT NULL REFERENCES group_counts(id) ON DELETE CASCADE,
  position  INTEGER NOT NULL,
  member_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  PRIMARY KEY (count_id, position)
) STRICT, WITHOUT ROWID;
CREATE INDEX group_count_members_member ON group_count_members(member_id, count_id);

CREATE TRIGGER repo_group_members_count_ai AFTER INSERT ON repo_group_members BEGIN
  DELETE FROM group_counts WHERE group_id = new.group_id;
END;
CREATE TRIGGER repo_group_members_count_au AFTER UPDATE ON repo_group_members BEGIN
  DELETE FROM group_counts WHERE group_id = new.group_id;
END;
CREATE TRIGGER repo_group_members_count_ad AFTER DELETE ON repo_group_members BEGIN
  DELETE FROM group_counts WHERE group_id = old.group_id;
END;

-- A file with unit '' is the group's when no earlier member of the row has its path. It takes
-- the place of the copy of the first later member that has the path, if any.
CREATE TRIGGER assets_group_path_ai AFTER INSERT ON assets WHEN new.unit = '' BEGIN
  UPDATE group_counts SET
    file_count = file_count + CASE WHEN EXISTS (
        SELECT 1 FROM group_count_members l JOIN assets a ON a.repo_id = l.member_id AND a.path = new.path
        WHERE l.count_id = group_counts.id AND l.position > (
          SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = new.repo_id))
      THEN 0 ELSE 1 END,
    total_size = total_size + new.size - coalesce((
        SELECT a.size FROM group_count_members l JOIN assets a ON a.repo_id = l.member_id AND a.path = new.path
        WHERE l.count_id = group_counts.id AND l.position > (
          SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = new.repo_id)
        ORDER BY l.position LIMIT 1), 0),
    last_published_at = CASE WHEN new.kind <> 'index' THEN max(last_published_at, new.updated_at) ELSE last_published_at END
  WHERE id IN (SELECT count_id FROM group_count_members WHERE member_id = new.repo_id)
    AND NOT EXISTS (
      SELECT 1 FROM group_count_members e JOIN assets a ON a.repo_id = e.member_id AND a.path = new.path
      WHERE e.count_id = group_counts.id AND e.position < (
        SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = new.repo_id));
END;

CREATE TRIGGER assets_group_path_au AFTER UPDATE OF size, updated_at ON assets WHEN new.unit = '' BEGIN
  UPDATE group_counts SET
    total_size = total_size + new.size - old.size,
    last_published_at = CASE WHEN new.kind <> 'index' THEN max(last_published_at, new.updated_at) ELSE last_published_at END
  WHERE id IN (SELECT count_id FROM group_count_members WHERE member_id = new.repo_id)
    AND NOT EXISTS (
      SELECT 1 FROM group_count_members e JOIN assets a ON a.repo_id = e.member_id AND a.path = new.path
      WHERE e.count_id = group_counts.id AND e.position < (
        SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = new.repo_id));
END;

-- A removed file of the group gives its place to the copy of the first later member that has the
-- path, if any.
CREATE TRIGGER assets_group_path_ad AFTER DELETE ON assets WHEN old.unit = '' BEGIN
  UPDATE group_counts SET
    file_count = file_count - CASE WHEN EXISTS (
        SELECT 1 FROM group_count_members l JOIN assets a ON a.repo_id = l.member_id AND a.path = old.path
        WHERE l.count_id = group_counts.id AND l.position > (
          SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = old.repo_id))
      THEN 0 ELSE 1 END,
    total_size = total_size - old.size + coalesce((
        SELECT a.size FROM group_count_members l JOIN assets a ON a.repo_id = l.member_id AND a.path = old.path
        WHERE l.count_id = group_counts.id AND l.position > (
          SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = old.repo_id)
        ORDER BY l.position LIMIT 1), 0),
    last_published_at = max(last_published_at, coalesce((
        SELECT CASE WHEN a.kind <> 'index' THEN a.updated_at ELSE 0 END
        FROM group_count_members l JOIN assets a ON a.repo_id = l.member_id AND a.path = old.path
        WHERE l.count_id = group_counts.id AND l.position > (
          SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = old.repo_id)
        ORDER BY l.position LIMIT 1), 0))
  WHERE id IN (SELECT count_id FROM group_count_members WHERE member_id = old.repo_id)
    AND NOT EXISTS (
      SELECT 1 FROM group_count_members e JOIN assets a ON a.repo_id = e.member_id AND a.path = old.path
      WHERE e.count_id = group_counts.id AND e.position < (
        SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = old.repo_id));
END;

-- A file with a unit is the group's when its member owns the unit: no earlier member of the row has
-- a file of the unit. A member that gets its first file of a unit takes the unit from the first
-- later member that has it: that member's files of the unit leave the counters.
CREATE TRIGGER assets_group_unit_ai AFTER INSERT ON assets WHEN new.unit <> '' BEGIN
  UPDATE group_counts SET
    file_count = file_count + 1 - CASE WHEN EXISTS (
        SELECT 1 FROM assets a WHERE a.repo_id = new.repo_id AND a.unit = new.unit AND a.unit <> '' AND a.id <> new.id)
      THEN 0 ELSE (
        SELECT count(*) FROM assets a WHERE a.unit = new.unit AND a.unit <> '' AND a.repo_id = (
          SELECT l.member_id FROM group_count_members l
          WHERE l.count_id = group_counts.id AND l.position > (
            SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = new.repo_id)
            AND EXISTS (SELECT 1 FROM assets b WHERE b.repo_id = l.member_id AND b.unit = new.unit AND b.unit <> '')
          ORDER BY l.position LIMIT 1)) END,
    total_size = total_size + new.size - CASE WHEN EXISTS (
        SELECT 1 FROM assets a WHERE a.repo_id = new.repo_id AND a.unit = new.unit AND a.unit <> '' AND a.id <> new.id)
      THEN 0 ELSE coalesce((
        SELECT sum(a.size) FROM assets a WHERE a.unit = new.unit AND a.unit <> '' AND a.repo_id = (
          SELECT l.member_id FROM group_count_members l
          WHERE l.count_id = group_counts.id AND l.position > (
            SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = new.repo_id)
            AND EXISTS (SELECT 1 FROM assets b WHERE b.repo_id = l.member_id AND b.unit = new.unit AND b.unit <> '')
          ORDER BY l.position LIMIT 1)), 0) END,
    last_published_at = CASE WHEN new.kind <> 'index' THEN max(last_published_at, new.updated_at) ELSE last_published_at END
  WHERE id IN (SELECT count_id FROM group_count_members WHERE member_id = new.repo_id)
    AND NOT EXISTS (
      SELECT 1 FROM group_count_members e JOIN assets a ON a.repo_id = e.member_id AND a.unit = new.unit AND a.unit <> ''
      WHERE e.count_id = group_counts.id AND e.position < (
        SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = new.repo_id));
END;

CREATE TRIGGER assets_group_unit_au AFTER UPDATE OF size, updated_at ON assets WHEN new.unit <> '' BEGIN
  UPDATE group_counts SET
    total_size = total_size + new.size - old.size,
    last_published_at = CASE WHEN new.kind <> 'index' THEN max(last_published_at, new.updated_at) ELSE last_published_at END
  WHERE id IN (SELECT count_id FROM group_count_members WHERE member_id = new.repo_id)
    AND NOT EXISTS (
      SELECT 1 FROM group_count_members e JOIN assets a ON a.repo_id = e.member_id AND a.unit = new.unit AND a.unit <> ''
      WHERE e.count_id = group_counts.id AND e.position < (
        SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = new.repo_id));
END;

-- A member that removes its last file of a unit it owns gives the unit to the first later member
-- that has it: that member's files of the unit join the counters.
CREATE TRIGGER assets_group_unit_ad AFTER DELETE ON assets WHEN old.unit <> '' BEGIN
  UPDATE group_counts SET
    file_count = file_count - 1 + CASE WHEN EXISTS (
        SELECT 1 FROM assets a WHERE a.repo_id = old.repo_id AND a.unit = old.unit AND a.unit <> '')
      THEN 0 ELSE (
        SELECT count(*) FROM assets a WHERE a.unit = old.unit AND a.unit <> '' AND a.repo_id = (
          SELECT l.member_id FROM group_count_members l
          WHERE l.count_id = group_counts.id AND l.position > (
            SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = old.repo_id)
            AND EXISTS (SELECT 1 FROM assets b WHERE b.repo_id = l.member_id AND b.unit = old.unit AND b.unit <> '')
          ORDER BY l.position LIMIT 1)) END,
    total_size = total_size - old.size + CASE WHEN EXISTS (
        SELECT 1 FROM assets a WHERE a.repo_id = old.repo_id AND a.unit = old.unit AND a.unit <> '')
      THEN 0 ELSE coalesce((
        SELECT sum(a.size) FROM assets a WHERE a.unit = old.unit AND a.unit <> '' AND a.repo_id = (
          SELECT l.member_id FROM group_count_members l
          WHERE l.count_id = group_counts.id AND l.position > (
            SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = old.repo_id)
            AND EXISTS (SELECT 1 FROM assets b WHERE b.repo_id = l.member_id AND b.unit = old.unit AND b.unit <> '')
          ORDER BY l.position LIMIT 1)), 0) END,
    last_published_at = CASE WHEN EXISTS (
        SELECT 1 FROM assets a WHERE a.repo_id = old.repo_id AND a.unit = old.unit AND a.unit <> '')
      THEN last_published_at ELSE max(last_published_at, coalesce((
        SELECT max(a.updated_at) FROM assets a WHERE a.unit = old.unit AND a.unit <> '' AND a.kind <> 'index' AND a.repo_id = (
          SELECT l.member_id FROM group_count_members l
          WHERE l.count_id = group_counts.id AND l.position > (
            SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = old.repo_id)
            AND EXISTS (SELECT 1 FROM assets b WHERE b.repo_id = l.member_id AND b.unit = old.unit AND b.unit <> '')
          ORDER BY l.position LIMIT 1)), 0)) END
  WHERE id IN (SELECT count_id FROM group_count_members WHERE member_id = old.repo_id)
    AND NOT EXISTS (
      SELECT 1 FROM group_count_members e JOIN assets a ON a.repo_id = e.member_id AND a.unit = old.unit AND a.unit <> ''
      WHERE e.count_id = group_counts.id AND e.position < (
        SELECT m.position FROM group_count_members m WHERE m.count_id = group_counts.id AND m.member_id = old.repo_id));
END;
