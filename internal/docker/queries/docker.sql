-- The rows of the OCI objects of docker repos (migrations/). Only the hooks write them.

-- name: GetImage :one
SELECT repo_id, image, full_name, manifests FROM docker_images WHERE repo_id = ? AND image = ?;

-- name: UpsertImage :exec
INSERT INTO docker_images (repo_id, image, full_name, manifests) VALUES (?, ?, ?, 0)
ON CONFLICT (repo_id, image) DO NOTHING;

-- name: AddImageManifests :exec
UPDATE docker_images SET manifests = manifests + sqlc.arg(delta) WHERE repo_id = sqlc.arg(repo_id) AND image = sqlc.arg(image);

-- name: DeleteEmptyImage :exec
DELETE FROM docker_images WHERE docker_images.repo_id = ? AND image = ? AND manifests <= 0;

-- A keyset page of every image by full name, through docker_images_full.
-- name: ListCatalog :many
SELECT repo_id, full_name FROM docker_images WHERE full_name > sqlc.arg(after) ORDER BY full_name LIMIT sqlc.arg(limit);

-- name: GetManifest :one
SELECT repo_id, image, digest, media_type, artifact_type, config_type, subject, size, total_size, platforms, annotations, created_at
FROM docker_manifests WHERE repo_id = ? AND image = ? AND digest = ?;

-- name: InsertManifest :exec
INSERT INTO docker_manifests (repo_id, image, digest, media_type, artifact_type, config_type, subject, size, total_size, platforms, annotations, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: DeleteManifest :exec
DELETE FROM docker_manifests WHERE docker_manifests.repo_id = ? AND image = ? AND digest = ?;

-- The manifests whose subject is a digest, in digest order after a cursor, through
-- docker_manifests_subject.
-- name: ListReferrers :many
SELECT digest, media_type, artifact_type, size, annotations FROM docker_manifests
WHERE repo_id = sqlc.arg(repo_id) AND image = sqlc.arg(image) AND subject = sqlc.arg(subject) AND digest > sqlc.arg(after)
ORDER BY digest LIMIT sqlc.arg(limit);

-- ListReferrers of one artifact type.
-- name: ListReferrersOfType :many
SELECT digest, media_type, artifact_type, size, annotations FROM docker_manifests
WHERE repo_id = sqlc.arg(repo_id) AND image = sqlc.arg(image) AND subject = sqlc.arg(subject) AND digest > sqlc.arg(after)
  AND artifact_type = sqlc.arg(artifact_type)
ORDER BY digest LIMIT sqlc.arg(limit);

-- name: InsertRef :exec
INSERT INTO docker_refs (repo_id, image, manifest, child, kind) VALUES (?, ?, ?, ?, ?);

-- The children of a manifest, through the primary key.
-- name: ListRefs :many
SELECT child, kind FROM docker_refs WHERE repo_id = ? AND image = ? AND manifest = ?;

-- name: DeleteRefs :exec
DELETE FROM docker_refs WHERE docker_refs.repo_id = ? AND image = ? AND manifest = ?;

-- How many manifests of the image use a blob, through docker_refs_child.
-- name: CountChildRefs :one
SELECT count(*) FROM docker_refs WHERE repo_id = ? AND image = ? AND child = ? AND kind = 'blob';

-- The indexes of the image that list a manifest, through docker_refs_child.
-- name: ParentIndexes :many
SELECT manifest FROM docker_refs WHERE repo_id = ? AND image = ? AND child = ? AND kind = 'manifest' LIMIT ?;

-- name: GetLink :one
SELECT repo_id, image, digest, size, refs, unreferenced_at FROM docker_links WHERE repo_id = ? AND image = ? AND digest = ?;

-- name: UpsertLink :exec
INSERT INTO docker_links (repo_id, image, digest, size, refs, unreferenced_at) VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (repo_id, image, digest) DO UPDATE SET size = excluded.size, refs = excluded.refs, unreferenced_at = excluded.unreferenced_at;

-- Changes the uses of an existing link by delta; a link that reaches 0 gets unreferenced_at now.
-- name: AddLinkRefs :exec
UPDATE docker_links SET refs = refs + sqlc.arg(delta),
  unreferenced_at = CASE WHEN refs + sqlc.arg(delta) <= 0 THEN sqlc.arg(now) ELSE 0 END
WHERE repo_id = sqlc.arg(repo_id) AND image = sqlc.arg(image) AND digest = sqlc.arg(digest);

-- name: DeleteLink :exec
DELETE FROM docker_links WHERE docker_links.repo_id = ? AND image = ? AND digest = ?;

-- Deletes the row of a link whose asset is gone, while it is still unused since before cutoff.
-- name: DeleteStaleLink :exec
DELETE FROM docker_links WHERE docker_links.repo_id = ? AND image = ? AND digest = ? AND refs = 0 AND unreferenced_at < ?;

-- An image of the repo that links a blob, through docker_links_digest.
-- name: RepoLinkOf :one
SELECT image FROM docker_links WHERE repo_id = ? AND digest = ? LIMIT 1;

-- The links that no manifest used since before a cutoff, oldest first, through the partial
-- docker_links_unreferenced.
-- name: ListUnreferencedLinks :many
SELECT l.repo_id, l.image, l.digest FROM docker_links l
WHERE l.refs = 0 AND l.unreferenced_at < sqlc.arg(cutoff)
ORDER BY l.unreferenced_at LIMIT sqlc.arg(limit);

-- name: UpsertTag :exec
INSERT INTO docker_tags (repo_id, image, tag, digest, updated_at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT (repo_id, image, tag) DO UPDATE SET digest = excluded.digest, updated_at = excluded.updated_at;

-- name: DeleteTag :exec
DELETE FROM docker_tags WHERE docker_tags.repo_id = ? AND image = ? AND tag = ?;

-- name: GetTag :one
SELECT tag, digest, updated_at FROM docker_tags WHERE repo_id = ? AND image = ? AND tag = ?;

-- A keyset page of the tags of an image by name, through the primary key.
-- name: ListTags :many
SELECT tag FROM docker_tags WHERE repo_id = sqlc.arg(repo_id) AND image = sqlc.arg(image) AND tag > sqlc.arg(after)
ORDER BY tag LIMIT sqlc.arg(limit);

-- A keyset page of the tags of an image, newest first, through docker_tags_recent with one
-- row-value bound.
-- name: ListTagsRecent :many
SELECT tag, digest, updated_at FROM docker_tags
WHERE repo_id = sqlc.arg(repo_id) AND image = sqlc.arg(image) AND (updated_at, tag) < (sqlc.arg(before_at), CAST(sqlc.arg(before_tag) AS TEXT))
ORDER BY updated_at DESC, tag DESC LIMIT sqlc.arg(limit);

-- The tags of a manifest digest, through docker_tags_digest.
-- name: TagsOfDigest :many
SELECT tag FROM docker_tags WHERE repo_id = ? AND image = ? AND digest = ?;

-- At most limit tags of a manifest digest, through docker_tags_digest.
-- name: SomeTagsOfDigest :many
SELECT tag FROM docker_tags WHERE repo_id = ? AND image = ? AND digest = ? LIMIT ?;

-- How many manifests of the image name a subject, through docker_manifests_subject.
-- name: CountReferrers :one
SELECT count(*) FROM docker_manifests WHERE repo_id = ? AND image = ? AND subject = ?;

-- A repo delete empties these tables batch by batch before its row goes (repos.Service.OnPurge).
-- name: PurgeDockerTags :execrows
DELETE FROM docker_tags WHERE docker_tags.repo_id = sqlc.arg(repo_id) AND (image, tag) IN (SELECT x.image, x.tag FROM docker_tags x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- name: PurgeDockerRefs :execrows
DELETE FROM docker_refs WHERE docker_refs.repo_id = sqlc.arg(repo_id) AND (image, manifest, child) IN (SELECT x.image, x.manifest, x.child FROM docker_refs x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- name: PurgeDockerLinks :execrows
DELETE FROM docker_links WHERE docker_links.repo_id = sqlc.arg(repo_id) AND (image, digest) IN (SELECT x.image, x.digest FROM docker_links x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- name: PurgeDockerManifests :execrows
DELETE FROM docker_manifests WHERE docker_manifests.repo_id = sqlc.arg(repo_id) AND (image, digest) IN (SELECT x.image, x.digest FROM docker_manifests x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- name: PurgeDockerImages :execrows
DELETE FROM docker_images WHERE docker_images.repo_id = sqlc.arg(repo_id) AND image IN (SELECT x.image FROM docker_images x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

