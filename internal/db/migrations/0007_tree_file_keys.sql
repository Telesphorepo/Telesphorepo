-- 0007_tree_file_keys.sql. db.Init runs this file once, in one transaction with foreign keys off
-- and checked before the commit. Every statement can run twice. Once a release has applied it,
-- never change it: add the next migration instead.

-- A file row of tree_entries is keyed as a folder row is, by repopath.NameKey(name): the names
-- that start with an ASCII digit first in version order ("16.json" < "91.json" < "100.json"),
-- then the other names by their bytes. tree_file_key is repopath.NameKey. The new keys start with
-- the byte 0x01 or 0x02 and a name never does, so no new key meets an old key of the same folder.
UPDATE tree_entries SET sort_key = tree_file_key(name) WHERE kind = 1;
