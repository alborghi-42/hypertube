-- name: ListCommentsByMedia :many
SELECT * FROM media_comments WHERE media_id = $1 ORDER BY created_at DESC;

-- name: GetComment :one
SELECT * FROM media_comments WHERE comment_id = $1;

-- name: CreateComment :one
INSERT INTO media_comments (comment_id, media_id, user_id, content)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateComment :one
UPDATE media_comments
SET content = $3, updated_at = now()
WHERE comment_id = $1 AND media_id = $2
RETURNING *;

-- name: DeleteComment :execrows
DELETE FROM media_comments WHERE comment_id = $1 AND media_id = $2;
