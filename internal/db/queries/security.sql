-- Second factors (TOTP, passkeys, recovery codes) and one-time challenges (SPEC.md "Auth model").

-- The secret is sealed with the row id as AAD, so the service inserts an empty secret and
-- sets it with SetTOTPSecret in the same transaction.
-- name: CreateTOTP :one
INSERT INTO user_totp (user_id, secret, name, created_at, last_step)
VALUES (?, X'', ?, ?, ?)
RETURNING id;

-- name: SetTOTPSecret :exec
UPDATE user_totp SET secret = ? WHERE id = ?;

-- A user's TOTP factors in creation order.
-- name: ListUserTOTP :many
SELECT id, secret, name, created_at, last_step FROM user_totp WHERE user_id = ? ORDER BY id;

-- Accepts a time step once: 0 rows when the step is not newer than the last accepted one.
-- name: UseTOTPStep :execrows
UPDATE user_totp SET last_step = sqlc.arg(step) WHERE id = sqlc.arg(id) AND last_step < sqlc.arg(step);

-- name: DeleteUserTOTP :execrows
DELETE FROM user_totp WHERE id = ? AND user_id = ?;

-- name: CreatePasskey :one
INSERT INTO user_passkeys (user_id, credential_id, credential, name, created_at)
VALUES (?, ?, ?, ?, ?)
RETURNING id;

-- A user's passkeys in creation order.
-- name: ListUserPasskeys :many
SELECT id, credential_id, credential, name, created_at, last_used_at
FROM user_passkeys WHERE user_id = ? ORDER BY id;

-- name: GetPasskeyByCredentialID :one
SELECT id, user_id, credential_id, credential, name, created_at, last_used_at
FROM user_passkeys WHERE credential_id = ?;

-- Saves the credential (sign count and flags) of a successful assertion.
-- name: UpdatePasskeyUse :exec
UPDATE user_passkeys SET credential = ?, last_used_at = ? WHERE id = ?;

-- name: DeleteUserPasskey :execrows
DELETE FROM user_passkeys WHERE id = ? AND user_id = ?;

-- Removes every TOTP factor of a user ("telesphorepo-server user reset-2fa").
-- name: DeleteAllUserTOTP :execrows
DELETE FROM user_totp WHERE user_id = ?;

-- Removes every passkey of a user ("telesphorepo-server user reset-2fa").
-- name: DeleteAllUserPasskeys :execrows
DELETE FROM user_passkeys WHERE user_id = ?;

-- Whether the user has a TOTP factor or a passkey (recovery codes alone do not count).
-- name: HasSecondFactor :one
SELECT CAST(EXISTS (SELECT 1 FROM user_totp t WHERE t.user_id = sqlc.arg(user_id))
         OR EXISTS (SELECT 1 FROM user_passkeys p WHERE p.user_id = sqlc.arg(user_id)) AS INTEGER) AS has;

-- name: DeleteUserRecoveryCodes :exec
DELETE FROM user_recovery_codes WHERE user_id = ?;

-- name: InsertRecoveryCode :exec
INSERT INTO user_recovery_codes (user_id, code_hash) VALUES (?, ?);

-- The unused codes of a user (at most 10), to verify a code against each hash.
-- name: ListUnusedRecoveryCodes :many
SELECT code_hash FROM user_recovery_codes WHERE user_id = ? AND used_at IS NULL;

-- Marks a code used: 0 rows when another request used it first.
-- name: UseRecoveryCode :execrows
UPDATE user_recovery_codes SET used_at = ? WHERE user_id = ? AND code_hash = ? AND used_at IS NULL;

-- name: CountUnusedRecoveryCodes :one
SELECT COUNT(*) FROM user_recovery_codes WHERE user_id = ? AND used_at IS NULL;

-- name: CreateChallenge :exec
INSERT INTO auth_challenges (id_hash, kind, user_id, data, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?);

-- Takes a challenge once: deletes it and returns it when it has the kind and has not expired.
-- No row: unknown, already taken, another kind or expired.
-- name: TakeChallenge :one
DELETE FROM auth_challenges
WHERE id_hash = sqlc.arg(id_hash) AND kind = sqlc.arg(kind) AND expires_at > sqlc.arg(now)
RETURNING user_id, data, created_at, expires_at;

-- Takes a challenge of the kind once, expired or not: deletes it and returns it. The caller
-- checks expires_at. No row: unknown, already taken or another kind.
-- name: TakeChallengeAnyAge :one
DELETE FROM auth_challenges
WHERE id_hash = sqlc.arg(id_hash) AND kind = sqlc.arg(kind)
RETURNING user_id, data, created_at, expires_at;

-- Bounded cleanup, served by auth_challenges_expires.
-- name: DeleteExpiredChallenges :execrows
DELETE FROM auth_challenges WHERE id_hash IN
  (SELECT x.id_hash FROM auth_challenges x WHERE x.expires_at <= sqlc.arg(now) LIMIT 100);
