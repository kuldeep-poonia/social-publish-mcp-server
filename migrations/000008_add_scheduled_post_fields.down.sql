DROP INDEX IF EXISTS idx_posts_status_scheduled_at;

ALTER TABLE posts DROP COLUMN IF EXISTS media_path;
ALTER TABLE posts DROP COLUMN IF EXISTS metadata;
ALTER TABLE posts DROP COLUMN IF EXISTS media_type;
ALTER TABLE posts DROP COLUMN IF EXISTS image_prompt;
