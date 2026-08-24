-- Migration: 000007_add_upload_resumption_columns.up.sql
-- Adds upload_session_uri and bytes_uploaded to posts table for zero-quota-waste resumable upload crash recovery.

ALTER TABLE posts
    ADD COLUMN IF NOT EXISTS upload_session_uri TEXT,
    ADD COLUMN IF NOT EXISTS bytes_uploaded BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_posts_upload_session ON posts(upload_session_uri) WHERE upload_session_uri IS NOT NULL;
