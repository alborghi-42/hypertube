-- OAuth2 client-credentials clients for the public REST API (/api/v1).
-- A client belongs to a user and acts on their behalf. The secret is a
-- random 256-bit value shown once; only its SHA-256 is stored (like
-- sessions: high-entropy secrets do not need a slow hash).
CREATE TABLE api_clients (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name        TEXT NOT NULL,
    secret_hash BYTEA NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX api_clients_user_id_idx ON api_clients (user_id);

-- Opaque bearer tokens issued by POST /api/v1/oauth/token; SHA-256 only.
CREATE TABLE api_tokens (
    token_hash BYTEA PRIMARY KEY,
    client_id  UUID NOT NULL REFERENCES api_clients(id) ON DELETE CASCADE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX api_tokens_client_id_idx ON api_tokens (client_id);
CREATE INDEX api_tokens_expires_at_idx ON api_tokens (expires_at);
