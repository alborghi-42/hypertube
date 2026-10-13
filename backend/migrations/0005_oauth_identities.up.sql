-- External login identities (42 intra, GitHub). One user may have several;
-- a provider account maps to exactly one user.
CREATE TABLE oauth_identities (
    provider         TEXT NOT NULL,
    provider_user_id TEXT NOT NULL,
    user_id          UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, provider_user_id)
);

CREATE INDEX oauth_identities_user_id_idx ON oauth_identities (user_id);
