CREATE TABLE IF NOT EXISTS posts (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    platform VARCHAR(32) NOT NULL,
    platform_post_id VARCHAR(255),
    content TEXT NOT NULL,
    media_urls TEXT[] NOT NULL DEFAULT '{}',
    status VARCHAR(32) NOT NULL DEFAULT 'draft',
    scheduled_at TIMESTAMPTZ,
    published_at TIMESTAMPTZ,
    idempotency_key VARCHAR(128) UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC'),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC')
);

CREATE INDEX IF NOT EXISTS idx_posts_user_platform ON posts(user_id, platform);
CREATE INDEX IF NOT EXISTS idx_posts_status_scheduled ON posts(status, scheduled_at);
CREATE INDEX IF NOT EXISTS idx_posts_idempotency ON posts(idempotency_key);
