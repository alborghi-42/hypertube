-- name: MarkWatched :exec
INSERT INTO watch_history (user_id, media_id)
VALUES ($1, $2)
ON CONFLICT (user_id, media_id) DO UPDATE SET watched_at = now();

-- name: ListWatchedMediaIDs :many
-- The subset of media_ids the user has watched.
SELECT media_id FROM watch_history
WHERE user_id = $1 AND media_id = ANY(sqlc.arg(media_ids)::uuid[]);
