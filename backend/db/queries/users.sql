-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1;

-- name: GetUserByUsername :one
SELECT * FROM users WHERE username = $1;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1;

-- name: CreateUser :one
INSERT INTO users (username, email, first_name, last_name, password_hash)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: ListUsers :many
SELECT id, username, avatar_url FROM users ORDER BY username;

-- name: UpdateUserProfile :one
UPDATE users
SET username = $2, email = $3, first_name = $4, last_name = $5, language = $6, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateUserAvatar :exec
UPDATE users SET avatar_url = $2, updated_at = now() WHERE id = $1;
