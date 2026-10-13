-- Films each user has started watching (42-13): one row per user and media,
-- refreshed on every /api/media/play.
CREATE TABLE watch_history (
    user_id    UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    media_id   UUID NOT NULL REFERENCES media (media_id) ON DELETE CASCADE,
    watched_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, media_id)
);

CREATE INDEX idx_watch_history_media_id ON watch_history (media_id);
