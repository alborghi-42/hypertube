-- name: GetMediaByID :one
SELECT * FROM media WHERE media_id = $1;

-- name: CreateMedia :one
INSERT INTO media (
    media_id, external_id, provider, title, description, creator, date,
    thumbnail_url, duration_seconds, download_status, local_path, public_url,
    file_size, mime_type, imdb_rating, actors
)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
RETURNING *;

-- name: UpdateMedia :one
UPDATE media
SET external_id = $2, provider = $3, title = $4, description = $5, creator = $6,
    date = $7, thumbnail_url = $8, duration_seconds = $9, download_status = $10,
    local_path = $11, public_url = $12, file_size = $13, mime_type = $14,
    imdb_rating = $15, actors = $16, updated_at = now()
WHERE media_id = $1
RETURNING *;

-- name: TouchMediaWatched :exec
UPDATE media SET last_watched_at = now() WHERE media_id = $1;

-- name: ListExpiredMedia :many
-- Downloads (or failed/partial leftovers) not watched since cutoff. Rows
-- without last_watched_at fall back to updated_at.
SELECT * FROM media
WHERE download_status IN ('downloaded', 'downloading', 'failed')
  AND COALESCE(last_watched_at, updated_at) < sqlc.arg(cutoff)::timestamptz
ORDER BY COALESCE(last_watched_at, updated_at);

-- name: ResetExpiredMedia :execrows
-- Back to the never-downloaded state (the app stores '' for fresh rows), unless watched again since cutoff.
UPDATE media
SET download_status = '', local_path = NULL, public_url = NULL,
    file_size = NULL, mime_type = NULL, updated_at = now()
WHERE media_id = sqlc.arg(media_id)
  AND download_status IN ('downloaded', 'downloading', 'failed')
  AND COALESCE(last_watched_at, updated_at) < sqlc.arg(cutoff)::timestamptz;

-- name: SetMediaDuration :exec
-- Fills the runtime once (from OMDB) without touching download state.
UPDATE media SET duration_seconds = $2
WHERE media_id = $1 AND COALESCE(duration_seconds, 0) <= 0;

-- name: DeleteMediaUnlessOthersCommented :execrows
-- Public API DELETE /movies/:id: the row (comments and watch history cascade)
-- goes only if no other user has commented on it.
DELETE FROM media m
WHERE m.media_id = sqlc.arg(media_id)
  AND NOT EXISTS (
    SELECT 1 FROM media_comments c
    WHERE c.media_id = m.media_id AND c.user_id <> sqlc.arg(user_id)
  );
