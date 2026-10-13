-- Comments were posted with a placeholder user before authentication existed.
-- Drop any comment whose author is not a real user so the FK can be added.
DELETE FROM media_comments c
WHERE NOT EXISTS (SELECT 1 FROM users u WHERE u.id = c.user_id);

-- Deleting an account removes its comments.
ALTER TABLE media_comments
    ADD CONSTRAINT media_comments_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE;
