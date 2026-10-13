-- name: GetOAuthIdentityUser :one
SELECT u.* FROM oauth_identities i
JOIN users u ON u.id = i.user_id
WHERE i.provider = $1 AND i.provider_user_id = $2;

-- name: CreateOAuthIdentity :exec
INSERT INTO oauth_identities (provider, provider_user_id, user_id)
VALUES ($1, $2, $3);

-- name: CreateOAuthUser :one
INSERT INTO users (username, email, first_name, last_name, avatar_url)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;
