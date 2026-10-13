DROP INDEX IF EXISTS idx_media_last_watched_at;
ALTER TABLE media DROP COLUMN IF EXISTS last_watched_at;
