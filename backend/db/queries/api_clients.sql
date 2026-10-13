-- name: CreateAPIClient :one
INSERT INTO api_clients (user_id, name, secret_hash)
VALUES ($1, $2, $3)
RETURNING id, user_id, name, created_at;

-- name: ListAPIClientsByUser :many
SELECT id, user_id, name, created_at FROM api_clients
WHERE user_id = $1
ORDER BY created_at DESC;

-- name: GetAPIClientSecret :one
SELECT id, user_id, secret_hash FROM api_clients WHERE id = $1;

-- name: DeleteAPIClient :execrows
DELETE FROM api_clients WHERE id = $1 AND user_id = $2;

-- name: CreateAPIToken :exec
INSERT INTO api_tokens (token_hash, client_id, expires_at)
VALUES ($1, $2, $3);

-- name: GetAPITokenUser :one
-- The owner of the client the token was issued to, if the token is live.
SELECT users.* FROM api_tokens
JOIN api_clients ON api_clients.id = api_tokens.client_id
JOIN users ON users.id = api_clients.user_id
WHERE api_tokens.token_hash = $1 AND api_tokens.expires_at > now();

-- name: DeleteExpiredAPITokens :exec
DELETE FROM api_tokens WHERE expires_at <= now();
