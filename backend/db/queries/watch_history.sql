-- name: MarkWatched :exec
INSERT INTO watch_history (user_id, media_id)
VALUES ($1, $2)
ON CONFLICT (user_id, media_id) DO UPDATE SET watched_at = now();

-- name: ListWatchedMediaIDs :many
-- The subset of media_ids the user has watched.
SELECT media_id FROM watch_history
WHERE user_id = $1 AND media_id = ANY(sqlc.arg(media_ids)::uuid[]);

-- name: ListWatchHistory :many
-- The user's watch history, most recent first.
SELECT w.media_id, m.title, w.watched_at
FROM watch_history w
JOIN media m ON m.media_id = w.media_id
WHERE w.user_id = sqlc.arg(user_id)
ORDER BY w.watched_at DESC, w.media_id
LIMIT sqlc.arg(row_limit) OFFSET sqlc.arg(row_offset);

-- name: UnmarkWatched :execrows
DELETE FROM watch_history WHERE user_id = $1 AND media_id = $2;
