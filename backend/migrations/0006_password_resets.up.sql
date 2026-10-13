-- Single-use password-reset tokens. The emailed link carries a random token;
-- only its SHA-256 is stored, like sessions.
CREATE TABLE password_resets (
    token_hash BYTEA PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX password_resets_user_id_idx ON password_resets (user_id);
CREATE INDEX password_resets_expires_at_idx ON password_resets (expires_at);
