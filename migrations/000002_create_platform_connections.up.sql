CREATE TABLE IF NOT EXISTS platform_connections (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    platform VARCHAR(32) NOT NULL,
    encrypted_access_token BYTEA NOT NULL,
    encrypted_refresh_token BYTEA,
    token_expires_at TIMESTAMPTZ,
    scopes TEXT[] NOT NULL DEFAULT '{}',
    is_active BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC'),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC'),
    CONSTRAINT uq_user_platform UNIQUE (user_id, platform)
);

CREATE INDEX IF NOT EXISTS idx_platform_connections_user_id ON platform_connections(user_id);
