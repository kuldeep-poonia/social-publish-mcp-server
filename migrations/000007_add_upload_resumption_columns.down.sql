-- Migration: 000007_add_upload_resumption_columns.down.sql
-- Reverts upload_session_uri and bytes_uploaded columns from posts table.

DROP INDEX IF EXISTS idx_posts_upload_session;

ALTER TABLE posts
    DROP COLUMN IF EXISTS bytes_uploaded,
    DROP COLUMN IF EXISTS upload_session_uri;
