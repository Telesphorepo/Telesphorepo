-- Name search (internal/namesearch) over repo_name_grams and user_name_grams (migration
-- 0008_name_search.sql). A list reads the rows of one gram in one range [from, to) of the names,
-- in name order, and keeps the names that hold the text (instr on the gram row, no other row
-- read); the text '' keeps every name. The repos of a caller's grants are in dbq/extra.go
-- (ListReposIn), one lookup per grant.

-- name: InsertRepoGram :exec
INSERT OR REPLACE INTO repo_name_grams (gram, name, format, kind, anonymous) VALUES (?, ?, ?, ?, ?);

-- The rows of a name: a seek of the primary key per gram of the name.
-- name: DeleteRepoGrams :exec
DELETE FROM repo_name_grams WHERE name = sqlc.arg(name) AND gram IN (sqlc.slice(grams));

-- name: SetRepoGramsAnonymous :exec
UPDATE repo_name_grams SET anonymous = sqlc.arg(anonymous) WHERE name = sqlc.arg(name) AND gram IN (sqlc.slice(grams));

-- The probe of a gram: its rows, up to limit.
-- name: CountRepoGram :one
SELECT count(*) FROM (SELECT 1 FROM repo_name_grams WHERE gram = sqlc.arg(gram) LIMIT sqlc.arg(limit));

-- name: ListRepoGrams :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repo_name_grams g JOIN repos r ON r.name = g.name LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE g.gram = sqlc.arg(gram) AND g.name >= sqlc.arg(from) AND g.name < sqlc.arg(to)
  AND instr(g.name, sqlc.arg(text)) > 0
ORDER BY g.name LIMIT sqlc.arg(limit);

-- Through repo_name_grams_format; kind '' matches every repo.
-- name: ListRepoGramsOfFormat :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repo_name_grams g JOIN repos r ON r.name = g.name LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE g.gram = sqlc.arg(gram) AND g.format = sqlc.arg(format) AND g.name >= sqlc.arg(from) AND g.name < sqlc.arg(to)
  AND instr(g.name, sqlc.arg(text)) > 0 AND (CAST(sqlc.arg(kind) AS TEXT) = '' OR r.kind = sqlc.arg(kind))
ORDER BY g.name LIMIT sqlc.arg(limit);

-- Through repo_name_grams_kind.
-- name: ListRepoGramsOfKind :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repo_name_grams g JOIN repos r ON r.name = g.name LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE g.gram = sqlc.arg(gram) AND g.kind = sqlc.arg(kind) AND g.name >= sqlc.arg(from) AND g.name < sqlc.arg(to)
  AND instr(g.name, sqlc.arg(text)) > 0
ORDER BY g.name LIMIT sqlc.arg(limit);

-- The public repos, through repo_name_grams_anonymous.
-- name: ListAnonymousRepoGrams :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repo_name_grams g JOIN repos r ON r.name = g.name LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE g.anonymous = 1 AND g.gram = sqlc.arg(gram) AND g.name >= sqlc.arg(from) AND g.name < sqlc.arg(to)
  AND instr(g.name, sqlc.arg(text)) > 0 AND (CAST(sqlc.arg(kind) AS TEXT) = '' OR r.kind = sqlc.arg(kind))
ORDER BY g.name LIMIT sqlc.arg(limit);

-- Through repo_name_grams_anonymous_format.
-- name: ListAnonymousRepoGramsOfFormat :many
SELECT r.id, r.name, r.format, r.kind, r.anonymous_read, r.config, r.created_at, r.description,
  r.file_count, r.total_size, r.last_published_at,
  o.id AS op_id, o.type AS op_type, o.owner AS op_owner, o.ref AS op_ref, o.state AS op_state, o.done AS op_done,
  o.total AS op_total, o.error AS op_error, o.started_by AS op_started_by, o.started_at AS op_started_at,
  o.updated_at AS op_updated_at
FROM repo_name_grams g JOIN repos r ON r.name = g.name LEFT JOIN repo_ops o ON o.repo_id = r.id
WHERE g.anonymous = 1 AND g.gram = sqlc.arg(gram) AND g.format = sqlc.arg(format)
  AND g.name >= sqlc.arg(from) AND g.name < sqlc.arg(to)
  AND instr(g.name, sqlc.arg(text)) > 0 AND (CAST(sqlc.arg(kind) AS TEXT) = '' OR r.kind = sqlc.arg(kind))
ORDER BY g.name LIMIT sqlc.arg(limit);

-- name: InsertUserGram :exec
INSERT OR REPLACE INTO user_name_grams (gram, name, user_id) VALUES (?, ?, ?);

-- name: CountUserGram :one
SELECT count(*) FROM (SELECT 1 FROM user_name_grams WHERE gram = sqlc.arg(gram) LIMIT sqlc.arg(limit));

-- The accounts (the People list), as ListUsers.
-- name: ListUserGrams :many
SELECT u.id, u.username, u.disabled, u.kind, u.email, u.created_at,
  CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = u.id), '') AS TEXT) AS perms
FROM user_name_grams g JOIN users u ON u.id = g.user_id
WHERE g.gram = sqlc.arg(gram) AND g.name >= sqlc.arg(from) AND g.name < sqlc.arg(to)
  AND instr(g.name, sqlc.arg(text)) > 0
ORDER BY g.name LIMIT sqlc.arg(limit);

-- The enabled people, for a person picker, as ListPeople.
-- name: ListPeopleGrams :many
SELECT u.username FROM user_name_grams g JOIN users u ON u.id = g.user_id
WHERE g.gram = sqlc.arg(gram) AND g.name >= sqlc.arg(from) AND g.name < sqlc.arg(to)
  AND instr(g.name, sqlc.arg(text)) > 0 AND u.disabled = 0
ORDER BY g.name LIMIT sqlc.arg(limit);
