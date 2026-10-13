-- name: CreatePasswordReset :exec
INSERT INTO password_resets (token_hash, user_id, expires_at)
VALUES ($1, $2, $3);

-- name: ConsumePasswordReset :one
-- Atomically marks a valid token used and returns its user; no row if the
-- token is unknown, expired or already used.
UPDATE password_resets SET used_at = now()
WHERE token_hash = $1 AND used_at IS NULL AND expires_at > now()
RETURNING user_id;

-- name: InvalidateUserPasswordResets :exec
UPDATE password_resets SET used_at = now()
WHERE user_id = $1 AND used_at IS NULL;

-- name: DeleteExpiredPasswordResets :exec
DELETE FROM password_resets WHERE expires_at <= now() OR used_at IS NOT NULL;

-- name: UpdateUserPassword :exec
UPDATE users SET password_hash = $2, updated_at = now() WHERE id = $1;
