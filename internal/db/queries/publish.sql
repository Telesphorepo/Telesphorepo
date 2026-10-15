-- Devices, device enrollment, CI trust policies and publish sessions (spec section 7.6).

-- name: CreateDevice :one
INSERT INTO devices (user_id, name, kind, public_key, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING id;

-- A device with its owner, for minting a session from it.
-- name: GetDevice :one
SELECT d.id, d.user_id, d.name, d.kind, d.public_key, d.created_at, d.expires_at, d.last_used_at, d.revoked_at,
       u.username, u.disabled, u.kind AS user_kind,
       CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = u.id), '') AS TEXT) AS perms
FROM devices d JOIN users u ON u.id = d.user_id
WHERE d.id = ?;

-- name: TouchDevice :exec
UPDATE devices SET last_used_at = ? WHERE id = ?;

-- Keyset page of a user's devices, newest first: ids below before.
-- name: ListUserDevices :many
SELECT id, user_id, name, kind, created_at, expires_at, last_used_at, revoked_at
FROM devices WHERE user_id = sqlc.arg(user_id) AND id < sqlc.arg(before)
ORDER BY id DESC LIMIT sqlc.arg(limit);

-- Keyset page of every device with its owner, newest first: ids below before.
-- name: ListDevices :many
SELECT d.id, d.user_id, u.username, d.name, d.kind, d.created_at, d.expires_at, d.last_used_at, d.revoked_at
FROM devices d JOIN users u ON u.id = d.user_id
WHERE d.id < sqlc.arg(before)
ORDER BY d.id DESC LIMIT sqlc.arg(limit);

-- 0 rows: unknown or already revoked.
-- name: RevokeDevice :execrows
UPDATE devices SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL;

-- 0 rows: unknown, another user's or already revoked.
-- name: RevokeUserDevice :execrows
UPDATE devices SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL;

-- A user_code collision violates the UNIQUE constraint; the service retries with a new code.
-- name: CreateEnrollment :exec
INSERT INTO device_enrollments (device_code_hash, user_code, name, kind, public_key, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetEnrollmentByUserCode :one
SELECT device_code_hash, user_code, name, kind, public_key, created_at, expires_at, polled_at, approved_by, device_id
FROM device_enrollments WHERE user_code = ?;

-- name: GetEnrollmentByDeviceCode :one
SELECT device_code_hash, user_code, name, kind, public_key, created_at, expires_at, polled_at, approved_by, device_id
FROM device_enrollments WHERE device_code_hash = ?;

-- Records the approval. 0 rows: unknown, already approved or expired.
-- name: ApproveEnrollment :execrows
UPDATE device_enrollments SET approved_by = sqlc.arg(approved_by), device_id = sqlc.arg(device_id)
WHERE device_code_hash = sqlc.arg(device_code_hash) AND device_id IS NULL AND expires_at > sqlc.arg(now);

-- Records a poll when the last one was at or before min_polled_at. 0 rows: unknown, or the
-- client polls too fast (answer slow_down).
-- name: TouchEnrollmentPoll :execrows
UPDATE device_enrollments SET polled_at = sqlc.arg(now)
WHERE device_code_hash = sqlc.arg(device_code_hash) AND polled_at <= sqlc.arg(min_polled_at);

-- Takes an approved enrollment once: deletes it and returns the device id. No row: unknown,
-- not approved yet or already taken.
-- name: TakeApprovedEnrollment :one
DELETE FROM device_enrollments
WHERE device_code_hash = ? AND device_id IS NOT NULL
RETURNING device_id;

-- Bounded cleanup, served by device_enrollments_expires.
-- name: DeleteExpiredEnrollments :execrows
DELETE FROM device_enrollments WHERE device_code_hash IN
  (SELECT x.device_code_hash FROM device_enrollments x WHERE x.expires_at <= sqlc.arg(now) LIMIT 100);

-- name: CreateTrust :one
INSERT INTO publish_trusts (name, issuer, audience, claims, jwks, created_by, created_at, disabled)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- name: UpdateTrust :execrows
UPDATE publish_trusts SET name = ?, issuer = ?, audience = ?, claims = ?, jwks = ?, disabled = ?
WHERE id = ?;

-- name: GetTrust :one
SELECT id, name, issuer, audience, claims, jwks, created_by, created_at, disabled
FROM publish_trusts WHERE id = ?;

-- Keyset page by name (pass "" for the first page).
-- name: ListTrusts :many
SELECT id, name, issuer, audience, claims, jwks, created_by, created_at, disabled
FROM publish_trusts WHERE name > sqlc.arg(after) ORDER BY name LIMIT sqlc.arg(limit);

-- Deletes the policy and its repo list (ON DELETE CASCADE); its sessions keep trust_id NULL.
-- name: DeleteTrust :execrows
DELETE FROM publish_trusts WHERE id = ?;

-- name: DeleteTrustRepos :exec
DELETE FROM publish_trust_repos WHERE trust_id = ?;

-- name: InsertTrustRepo :exec
INSERT INTO publish_trust_repos (trust_id, repo_id) VALUES (?, ?);

-- The repos of a policy in repo id order (the primary key order).
-- name: ListTrustRepos :many
SELECT r.id, r.name FROM publish_trust_repos tr JOIN repos r ON r.id = tr.repo_id
WHERE tr.trust_id = ? ORDER BY tr.repo_id;

-- The enabled policies of an issuer that list the repo, through publish_trusts_issuer.
-- name: ListMatchingTrusts :many
SELECT t.id, t.name, t.issuer, t.audience, t.claims, t.jwks
FROM publish_trusts t JOIN publish_trust_repos tr ON tr.trust_id = t.id
WHERE t.issuer = sqlc.arg(issuer) AND t.disabled = 0 AND tr.repo_id = sqlc.arg(repo_id);

-- Records a CI token as used. 0 rows: the token was seen before (a replay).
-- name: InsertSeenOIDCToken :execrows
INSERT INTO publish_oidc_seen (id_hash, expires_at) VALUES (?, ?)
ON CONFLICT (id_hash) DO NOTHING;

-- Bounded cleanup, served by publish_oidc_seen_expires.
-- name: DeleteExpiredSeenOIDCTokens :execrows
DELETE FROM publish_oidc_seen WHERE id_hash IN
  (SELECT x.id_hash FROM publish_oidc_seen x WHERE x.expires_at <= sqlc.arg(now) LIMIT 100);

-- name: CreatePublishSession :one
INSERT INTO publish_sessions (public_id, secret_hash, repo_id, user_id, device_id, trust_id, via, subject, created_at, expires_at,
  ci_server, ci_repository, ci_sha, ci_ref, ci_run_id, ci_run_attempt, ci_workflow, ci_workflow_ref, ci_actor)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- A session by the public id of its token, with what the write gate checks: the repo, and the
-- state of the device or trust policy behind it. The gate reads the user through GetPrincipal.
-- name: GetPublishSessionAuth :one
SELECT s.id, s.secret_hash, s.repo_id, s.user_id, s.device_id, s.trust_id, s.via, s.subject,
       s.created_at, s.expires_at, s.closed_at, s.ci_repository,
       r.name AS repo, r.locked_by,
       d.expires_at AS device_expires_at, d.revoked_at AS device_revoked_at,
       t.disabled AS trust_disabled
FROM publish_sessions s
JOIN repos r ON r.id = s.repo_id
LEFT JOIN devices d ON d.id = s.device_id
LEFT JOIN publish_trusts t ON t.id = s.trust_id
WHERE s.public_id = ?;

-- Ends an open session. 0 rows: unknown or already closed.
-- name: ClosePublishSession :execrows
UPDATE publish_sessions SET closed_at = ? WHERE id = ? AND closed_at IS NULL;

-- name: CloseDevicePublishSessions :execrows
UPDATE publish_sessions SET closed_at = ? WHERE device_id = ? AND closed_at IS NULL;

-- name: CloseTrustPublishSessions :execrows
UPDATE publish_sessions SET closed_at = ? WHERE trust_id = ? AND closed_at IS NULL;

-- name: CloseUserPublishSessions :execrows
UPDATE publish_sessions SET closed_at = ? WHERE user_id = ? AND closed_at IS NULL;

-- Keyset page of the publish log, newest first: ids below before.
-- name: ListPublishLog :many
SELECT s.id, s.public_id, s.repo_id, r.name AS repo, s.via, s.subject,
       s.user_id, u.username, s.device_id, d.name AS device_name, s.trust_id, t.name AS trust_name,
       s.created_at, s.expires_at, s.closed_at
FROM publish_sessions s
JOIN repos r ON r.id = s.repo_id
LEFT JOIN users u ON u.id = s.user_id
LEFT JOIN devices d ON d.id = s.device_id
LEFT JOIN publish_trusts t ON t.id = s.trust_id
WHERE s.id < sqlc.arg(before)
ORDER BY s.id DESC LIMIT sqlc.arg(limit);

-- Keyset page of one user's publish sessions, newest first: ids below before.
-- name: ListUserPublishSessions :many
SELECT s.id, s.public_id, s.repo_id, r.name AS repo, s.via, s.subject,
       s.device_id, d.name AS device_name, s.created_at, s.expires_at, s.closed_at
FROM publish_sessions s
JOIN repos r ON r.id = s.repo_id
LEFT JOIN devices d ON d.id = s.device_id
WHERE s.user_id = sqlc.arg(user_id) AND s.id < sqlc.arg(before)
ORDER BY s.id DESC LIMIT sqlc.arg(limit);

-- Deletes ended sessions without provenance older than the cutoff (expires_at bounds every
-- session's end), in bounded batches through publish_sessions_expires.
-- name: DeleteOldPublishSessions :execrows
DELETE FROM publish_sessions WHERE id IN
  (SELECT x.id FROM publish_sessions x WHERE x.ci_repository = '' AND x.expires_at <= sqlc.arg(cutoff) LIMIT 100);

-- Keeps the ended provenance sessions older than the cutoff that wrote files: they are the
-- build records of those files. Bounded batches through publish_sessions_ci_ended.
-- name: KeepProvenanceSessions :execrows
UPDATE publish_sessions SET kept = 1 WHERE id IN
  (SELECT x.id FROM publish_sessions x
   WHERE x.ci_repository <> '' AND x.kept = 0 AND x.expires_at <= sqlc.arg(cutoff)
     AND EXISTS (SELECT 1 FROM assets a WHERE a.publish_session_id = x.id)
   LIMIT 100);

-- Deletes the ended provenance sessions older than the cutoff that wrote no file.
-- name: DeleteUnusedProvenanceSessions :execrows
DELETE FROM publish_sessions WHERE id IN
  (SELECT x.id FROM publish_sessions x
   WHERE x.ci_repository <> '' AND x.kept = 0 AND x.expires_at <= sqlc.arg(cutoff)
     AND NOT EXISTS (SELECT 1 FROM assets a WHERE a.publish_session_id = x.id)
   LIMIT 100);

-- The build provenance of a session ('' ci_repository: none).
-- name: GetSessionProvenance :one
SELECT ci_server, ci_repository, ci_sha, ci_ref, ci_run_id, ci_run_attempt, ci_workflow, ci_workflow_ref, ci_actor
FROM publish_sessions WHERE id = ?;
