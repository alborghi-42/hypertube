-- 42-18: media unwatched for a month are deleted from disk. last_watched_at
-- is bumped on /api/media/play and on stream requests (throttled).
ALTER TABLE media ADD COLUMN last_watched_at TIMESTAMPTZ;

-- Existing downloads (or leftovers) count as watched at their last update,
-- so the first cleanup does not wipe everything at once.
UPDATE media SET last_watched_at = updated_at
WHERE download_status IN ('downloaded', 'downloading', 'failed');

CREATE INDEX idx_media_last_watched_at ON media (last_watched_at);
