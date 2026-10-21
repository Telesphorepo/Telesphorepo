-- Docker module schema: the rows of the OCI objects of each docker repo. Every object is an asset
-- of the repo (<image>/_blobs/sha256/<hex>, <image>/_manifests/sha256/<hex>, <image>/_tags/<tag>);
-- only the hooks of the format write these tables, in the transaction of the asset change. A repo
-- delete runs no hooks: the rows go with the repo. Digests are the hex of a SHA-256.

-- The images of a repo. full_name is "<repo name>/<image>" (repos have no rename); the catalog
-- walks it.
CREATE TABLE docker_images (
  repo_id   INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  image     TEXT NOT NULL,
  full_name TEXT NOT NULL,
  manifests INTEGER NOT NULL DEFAULT 0,  -- manifest rows of the image
  PRIMARY KEY (repo_id, image)
) STRICT, WITHOUT ROWID;
CREATE UNIQUE INDEX docker_images_full ON docker_images (full_name);

-- The manifests of an image. artifact_type is artifactType, else config.mediaType unless it is
-- the OCI empty config. subject is the hex of subject.digest ('' for none). total_size is the
-- manifest with its config and layers (an index: its own size). platforms is the JSON
-- [{digest, os, architecture, variant}] of an index.
CREATE TABLE docker_manifests (
  repo_id       INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  image         TEXT NOT NULL,
  digest        TEXT NOT NULL,
  media_type    TEXT NOT NULL,
  artifact_type TEXT NOT NULL DEFAULT '',
  config_type   TEXT NOT NULL DEFAULT '',
  subject       TEXT NOT NULL DEFAULT '',
  size          INTEGER NOT NULL,
  total_size    INTEGER NOT NULL,
  platforms     TEXT NOT NULL DEFAULT '[]',
  annotations   TEXT NOT NULL DEFAULT '{}',
  created_at    INTEGER NOT NULL,
  PRIMARY KEY (repo_id, image, digest)
) STRICT, WITHOUT ROWID;
CREATE INDEX docker_manifests_subject ON docker_manifests (repo_id, image, subject, digest);

-- What each manifest references: its config and layers (kind 'blob') or the manifests of an index
-- (kind 'manifest'), each distinct child once.
CREATE TABLE docker_refs (
  repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  image    TEXT NOT NULL,
  manifest TEXT NOT NULL,
  child    TEXT NOT NULL,
  kind     TEXT NOT NULL CHECK (kind IN ('blob', 'manifest')),
  PRIMARY KEY (repo_id, image, manifest, child)
) STRICT, WITHOUT ROWID;
CREATE INDEX docker_refs_child ON docker_refs (repo_id, image, child, kind);

-- One row per blob link of an image. refs counts the docker_refs rows of the image with this
-- child; unreferenced_at is when refs reached 0 (the insert time for a new row), 0 while the link
-- is used. The job docker.links removes a link unused for docker.link_grace.
CREATE TABLE docker_links (
  repo_id         INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  image           TEXT NOT NULL,
  digest          TEXT NOT NULL,
  size            INTEGER NOT NULL,
  refs            INTEGER NOT NULL,
  unreferenced_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, image, digest)
) STRICT, WITHOUT ROWID;
CREATE INDEX docker_links_digest ON docker_links (repo_id, digest);
CREATE INDEX docker_links_unreferenced ON docker_links (unreferenced_at) WHERE refs = 0;

-- The tags of an image: mutable references to a manifest digest.
CREATE TABLE docker_tags (
  repo_id    INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
  image      TEXT NOT NULL,
  tag        TEXT NOT NULL,
  digest     TEXT NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (repo_id, image, tag)
) STRICT, WITHOUT ROWID;
CREATE INDEX docker_tags_recent ON docker_tags (repo_id, image, updated_at, tag);
CREATE INDEX docker_tags_digest ON docker_tags (repo_id, image, digest);
