ALTER TABLE posts ADD COLUMN IF NOT EXISTS image_prompt TEXT;
ALTER TABLE posts ADD COLUMN IF NOT EXISTS media_type VARCHAR(32);
ALTER TABLE posts ADD COLUMN IF NOT EXISTS metadata JSONB DEFAULT '{}'::jsonb;
ALTER TABLE posts ADD COLUMN IF NOT EXISTS media_path TEXT;

CREATE INDEX IF NOT EXISTS idx_posts_status_scheduled_at ON posts(status, scheduled_at) WHERE status = 'scheduled';
