-- Devices, device enrollment, publish nonces, CI trust policies and publish sessions.

-- name: CreateDevice :one
INSERT INTO devices (user_id, name, public_key, created_at, expires_at)
VALUES (?, ?, ?, ?, ?)
RETURNING id;

-- A device with its owner, for minting a session from it.
-- name: GetDevice :one
SELECT d.id, d.user_id, d.name, d.public_key, d.created_at, d.expires_at, d.last_used_at, d.revoked_at,
       u.username, u.disabled, u.kind AS user_kind,
       CAST(coalesce((SELECT group_concat(perm) FROM user_permissions up WHERE up.user_id = u.id), '') AS TEXT) AS perms
FROM devices d JOIN users u ON u.id = d.user_id
WHERE d.id = ?;

-- name: TouchDevice :exec
UPDATE devices SET last_used_at = ? WHERE id = ?;

-- Keyset page of a user's devices, newest first: ids below before.
-- name: ListUserDevices :many
SELECT id, user_id, name, created_at, expires_at, last_used_at, revoked_at
FROM devices WHERE user_id = sqlc.arg(user_id) AND id < sqlc.arg(before)
ORDER BY id DESC LIMIT sqlc.arg(limit);

-- Keyset page of every device with its owner, newest first: ids below before.
-- name: ListDevices :many
SELECT d.id, d.user_id, u.username, d.name, d.created_at, d.expires_at, d.last_used_at, d.revoked_at
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
INSERT INTO device_enrollments (device_code_hash, user_code, name, public_key, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: GetEnrollmentByUserCode :one
SELECT device_code_hash, user_code, name, public_key, created_at, expires_at, polled_at, approved_by, device_id
FROM device_enrollments WHERE user_code = ?;

-- name: GetEnrollmentByDeviceCode :one
SELECT device_code_hash, user_code, name, public_key, created_at, expires_at, polled_at, approved_by, device_id
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

-- A keyset page by name of the policies whose repos all lie in a set of repos, at least one: the
-- policies that list a repo of the set, through publish_trust_repos_repo, kept when every repo they
-- list is in the set.
-- name: ListTrustsInRepos :many
SELECT id, name, issuer, audience, claims, jwks, created_by, created_at, disabled
FROM publish_trusts WHERE name > sqlc.arg(after) AND id IN (
  SELECT tr.trust_id FROM publish_trust_repos tr WHERE tr.repo_id IN (sqlc.slice(repo_ids))
  GROUP BY tr.trust_id
  HAVING count(*) = (SELECT count(*) FROM publish_trust_repos y WHERE y.trust_id = tr.trust_id)
) ORDER BY name LIMIT sqlc.arg(limit);
-- Deletes the policy and its repo list (ON DELETE CASCADE); its sessions keep trust_id NULL.
-- name: DeleteTrust :execrows
DELETE FROM publish_trusts WHERE id = ?;

-- name: DeleteTrustRepos :exec
DELETE FROM publish_trust_repos WHERE trust_id = ?;

-- name: InsertTrustRepo :exec
INSERT INTO publish_trust_repos (trust_id, repo_id) VALUES (?, ?);

-- A merge adds repo to_id to every policy that lists repo from_id, through
-- publish_trust_repos_repo. A policy that lists both keeps one row.
-- name: CopyTrustRepos :execrows
INSERT INTO publish_trust_repos (trust_id, repo_id)
SELECT tr.trust_id, sqlc.arg(to_id) FROM publish_trust_repos tr WHERE tr.repo_id = sqlc.arg(from_id)
ON CONFLICT (trust_id, repo_id) DO NOTHING;

-- The repos of a policy in repo id order (the primary key order).
-- name: ListTrustRepos :many
SELECT r.id, r.name FROM publish_trust_repos tr JOIN repos r ON r.id = tr.repo_id
WHERE tr.trust_id = ? ORDER BY tr.repo_id;

-- The enabled policies of an issuer that list the repo, through publish_trusts_issuer.
-- name: ListMatchingTrusts :many
SELECT t.id, t.name, t.issuer, t.audience, t.claims, t.jwks
FROM publish_trusts t JOIN publish_trust_repos tr ON tr.trust_id = t.id
WHERE t.issuer = sqlc.arg(issuer) AND t.disabled = 0 AND tr.repo_id = sqlc.arg(repo_id);

-- The enabled policies of an issuer, through publish_trusts_issuer (a check of a CI token without
-- a repo, e.g. a registry login).
-- name: ListIssuerTrusts :many
SELECT id, name, issuer, audience, claims, jwks
FROM publish_trusts WHERE issuer = ? AND disabled = 0;

-- name: CreateNonce :exec
INSERT INTO publish_nonces (id_hash, device_id, exchange, expires_at) VALUES (?, ?, ?, ?);

-- Takes a one-repo nonce once: deletes it and returns its device when it has not expired. No
-- row: unknown, already taken, expired or a token exchange nonce.
-- name: TakeNonce :one
DELETE FROM publish_nonces
WHERE id_hash = sqlc.arg(id_hash) AND exchange = 0 AND expires_at > sqlc.arg(now)
RETURNING device_id;

-- The device of a token exchange nonce that has not expired. The nonce stays.
-- name: GetExchangeNonce :one
SELECT device_id FROM publish_nonces
WHERE id_hash = sqlc.arg(id_hash) AND exchange = 1 AND expires_at > sqlc.arg(now);

-- The expiry of a token exchange nonce of a device that has not expired.
-- name: GetDeviceExchangeNonce :one
SELECT expires_at FROM publish_nonces
WHERE id_hash = sqlc.arg(id_hash) AND device_id = sqlc.arg(device_id) AND exchange = 1 AND expires_at > sqlc.arg(now);

-- The session that a token exchange nonce minted for a repo.
-- name: GetExchangeSession :one
SELECT e.session_id, s.public_id FROM publish_exchange_sessions e
JOIN publish_sessions s ON s.id = e.session_id
WHERE e.nonce_hash = ? AND e.repo_id = ?;

-- name: InsertExchangeSession :exec
INSERT INTO publish_exchange_sessions (nonce_hash, repo_id, session_id) VALUES (?, ?, ?);

-- Bounded cleanup, served by publish_nonces_expires.
-- name: DeleteExpiredNonces :execrows
DELETE FROM publish_nonces WHERE id_hash IN
  (SELECT x.id_hash FROM publish_nonces x WHERE x.expires_at <= sqlc.arg(now) LIMIT 100);

-- The CI rules (trust policies) that list one repo, through publish_trust_repos_repo. The caller
-- sorts them.
-- name: ListRepoTrusts :many
SELECT t.id, t.name, t.issuer, t.claims, t.disabled
FROM publish_trust_repos tr JOIN publish_trusts t ON t.id = tr.trust_id
WHERE tr.repo_id = ?;

-- Records a CI token as used for a repo. 0 rows: the token was seen for the repo before.
-- name: InsertSeenOIDCToken :execrows
INSERT INTO publish_oidc_seen (id_hash, repo_id, expires_at, session_id) VALUES (?, ?, ?, ?)
ON CONFLICT (id_hash, repo_id) DO NOTHING;

-- Whether a CI token was used for any repo, through the primary key.
-- name: SeenOIDCToken :one
SELECT EXISTS (SELECT 1 FROM publish_oidc_seen WHERE id_hash = ?);

-- The session that a CI token minted for a repo (NULL: none, or deleted).
-- name: GetSeenOIDCSession :one
SELECT session_id FROM publish_oidc_seen WHERE id_hash = ? AND repo_id = ?;

-- Bounded cleanup, served by publish_oidc_seen_expires.
-- name: DeleteExpiredSeenOIDCTokens :execrows
DELETE FROM publish_oidc_seen WHERE id_hash IN
  (SELECT x.id_hash FROM publish_oidc_seen x WHERE x.expires_at <= sqlc.arg(now) LIMIT 100);

-- name: CreatePublishSession :one
INSERT INTO publish_sessions (public_id, secret_hash, repo_id, user_id, device_id, trust_id, build_id, via, subject, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING id;

-- A session by the public id of its token, with what the access engine checks: the repo, and the
-- state of the device or trust policy behind it. The gate reads the user through GetPrincipal.
-- name: GetPublishSessionAuth :one
SELECT s.id, s.secret_hash, s.repo_id, s.user_id, s.device_id, s.trust_id, s.via, s.subject,
       s.created_at, s.expires_at, s.closed_at, s.build_id,
       r.name AS repo,
       d.expires_at AS device_expires_at, d.revoked_at AS device_revoked_at,
       t.disabled AS trust_disabled
FROM publish_sessions s
JOIN repos r ON r.id = s.repo_id
LEFT JOIN devices d ON d.id = s.device_id
LEFT JOIN publish_trusts t ON t.id = s.trust_id
WHERE s.public_id = ?;

-- The session behind a core bearer token, by its primary key.
-- name: GetPublishSessionAuthByID :one
SELECT s.id, s.repo_id, s.user_id, s.device_id, s.trust_id, s.via, s.subject,
       s.expires_at, s.closed_at, s.build_id,
       d.expires_at AS device_expires_at, d.revoked_at AS device_revoked_at,
       t.disabled AS trust_disabled
FROM publish_sessions s
LEFT JOIN devices d ON d.id = s.device_id
LEFT JOIN publish_trusts t ON t.id = s.trust_id
WHERE s.id = ?;

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

-- Deletes the sessions that ended before the cutoff (expires_at bounds every session's end), in
-- bounded batches through publish_sessions_expires.
-- name: DeleteOldPublishSessions :execrows
DELETE FROM publish_sessions WHERE id IN
  (SELECT x.id FROM publish_sessions x WHERE x.expires_at <= sqlc.arg(cutoff) LIMIT 100);
-- A repo delete empties these tables batch by batch before its row goes (repos.Service.OnPurge).
-- name: PurgePublishExchangeSessions :execrows
DELETE FROM publish_exchange_sessions WHERE publish_exchange_sessions.repo_id = sqlc.arg(repo_id) AND nonce_hash IN (SELECT x.nonce_hash FROM publish_exchange_sessions x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- name: PurgePublishOIDCSeen :execrows
DELETE FROM publish_oidc_seen WHERE publish_oidc_seen.repo_id = sqlc.arg(repo_id) AND id_hash IN (SELECT x.id_hash FROM publish_oidc_seen x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));

-- name: PurgePublishSessions :execrows
DELETE FROM publish_sessions WHERE id IN (SELECT x.id FROM publish_sessions x WHERE x.repo_id = sqlc.arg(repo_id) LIMIT sqlc.arg(limit));


-- Counts the policies up to a cap, for the Settings overview.
-- name: CountTrustsUpTo :one
SELECT count(*) FROM (SELECT 1 FROM publish_trusts LIMIT sqlc.arg(cap));

-- Counts the policies whose every repo is among ids: the candidates through
-- publish_trust_repos_repo, each with the count of its repos through the primary key.
-- name: CountTrustsWithin :one
SELECT count(*) FROM (SELECT tr.trust_id FROM publish_trust_repos tr WHERE tr.repo_id IN (sqlc.slice(ids))
  GROUP BY tr.trust_id
  HAVING count(*) = (SELECT count(*) FROM publish_trust_repos x WHERE x.trust_id = tr.trust_id));
