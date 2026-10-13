-- name: ListCommentsByMedia :many
SELECT c.comment_id, c.media_id, c.user_id, c.content, c.created_at, c.updated_at, u.username
FROM media_comments c
JOIN users u ON u.id = c.user_id
WHERE c.media_id = $1
ORDER BY c.created_at DESC;

-- name: GetComment :one
SELECT c.comment_id, c.media_id, c.user_id, c.content, c.created_at, c.updated_at, u.username
FROM media_comments c
JOIN users u ON u.id = c.user_id
WHERE c.comment_id = $1;

-- name: CreateComment :one
INSERT INTO media_comments (comment_id, media_id, user_id, content)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- Update/delete are also scoped to the author, so the write itself can never
-- touch someone else's comment whatever the handler checked before.
-- name: UpdateComment :one
UPDATE media_comments
SET content = $4, updated_at = now()
WHERE comment_id = $1 AND media_id = $2 AND user_id = $3
RETURNING *;

-- name: DeleteComment :execrows
DELETE FROM media_comments WHERE comment_id = $1 AND media_id = $2 AND user_id = $3;
