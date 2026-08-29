// Package idempotency provides application-level idempotency protection,
// state machine transitions, stale crash recovery, and resumable session management.
package idempotency

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

// Engine encapsulates state-machine operations for idempotent social publishing.
type Engine struct {
	db             *sql.DB
	staleThreshold time.Duration
}

// NewEngine creates an IdempotencyEngine with the specified database connection and stale timeout.
func NewEngine(db *sql.DB, staleThreshold time.Duration) *Engine {
	if staleThreshold <= 0 {
		staleThreshold = 60 * time.Second
	}
	return &Engine{
		db:             db,
		staleThreshold: staleThreshold,
	}
}

// AcquireLock attempts to acquire the processing lock for an idempotency key.
func (e *Engine) AcquireLock(ctx context.Context, userID, platform, idempotencyKey string) (*PostRecord, LockStatus, error) {
	return e.AcquireLockWithContent(ctx, userID, platform, idempotencyKey, "", nil)
}

// AcquireLockWithContent attempts to acquire the processing lock with optional content & media URLs.
func (e *Engine) AcquireLockWithContent(ctx context.Context, userID, platform, idempotencyKey, content string, mediaURLs []string) (*PostRecord, LockStatus, error) {
	now := time.Now().UTC()
	staleCutoff := now.Add(-e.staleThreshold)

	// Step 1: Check existing post by idempotency key
	query := `
		SELECT id, user_id, platform, status, idempotency_key, 
		       COALESCE(platform_post_id, ''), COALESCE(upload_session_uri, ''), 
		       bytes_uploaded, created_at, updated_at
		FROM posts
		WHERE idempotency_key = $1;
	`
	row := e.db.QueryRowContext(ctx, query, idempotencyKey)

	var record PostRecord
	err := row.Scan(
		&record.ID, &record.UserID, &record.Platform, &record.Status, &record.IdempotencyKey,
		&record.PlatformPostID, &record.UploadSessionURI, &record.BytesUploaded,
		&record.CreatedAt, &record.UpdatedAt,
	)

	if err == nil {
		// Existing record found — evaluate state
		if record.Status == "published" || record.Status == "posted" {
			return &record, LockCached, nil
		}

		if record.Status == "processing" && record.UpdatedAt.After(staleCutoff) {
			return &record, LockInFlight, ErrInFlightConflict
		}

		// State is scheduled, failed OR stale processing lock -> attempt atomic reclaim
		reclaimQuery := `
			UPDATE posts
			SET status = 'processing', updated_at = $1
			WHERE id = $2 AND (status = 'scheduled' OR status = 'failed' OR (status = 'processing' AND updated_at < $3));
		`
		res, execErr := e.db.ExecContext(ctx, reclaimQuery, now, record.ID, staleCutoff)
		if execErr != nil {
			return nil, LockInFlight, fmt.Errorf("failed executing stale lock recovery: %w", execErr)
		}

		rows, _ := res.RowsAffected()
		if rows == 1 {
			// Won the reclaim race
			record.Status = "processing"
			record.UpdatedAt = now
			return &record, LockAcquired, nil
		}

		// Lost the reclaim race — another worker took it or completed it
		return e.evaluateLostRace(ctx, record.ID)
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, LockInFlight, fmt.Errorf("failed querying idempotency record: %w", err)
	}

	// Step 2: Fresh post insertion
	newID := uuid.New().String()
	if mediaURLs == nil {
		mediaURLs = []string{}
	}
	insertQuery := `
		INSERT INTO posts (id, user_id, platform, content, media_urls, status, idempotency_key, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 'processing', $6, $7, $8);
	`
	_, insertErr := e.db.ExecContext(ctx, insertQuery, newID, userID, platform, content, models.StringArray(mediaURLs), idempotencyKey, now, now)
	if insertErr == nil {
		// Successfully acquired fresh lock
		return &PostRecord{
			ID:             newID,
			UserID:         userID,
			Platform:       platform,
			Status:         "processing",
			IdempotencyKey: idempotencyKey,
			CreatedAt:      now,
			UpdatedAt:      now,
		}, LockAcquired, nil
	}

	// Handle race condition on concurrent fresh inserts (PostgreSQL 23505)
	var pgErr *pgconn.PgError
	if (errors.As(insertErr, &pgErr) && pgErr.Code == "23505") || strings.Contains(insertErr.Error(), "duplicate key") || strings.Contains(insertErr.Error(), "23505") {
		// Re-query after race
		var racedRecord PostRecord
		row := e.db.QueryRowContext(ctx, query, idempotencyKey)
		if scanErr := row.Scan(
			&racedRecord.ID, &racedRecord.UserID, &racedRecord.Platform, &racedRecord.Status, &racedRecord.IdempotencyKey,
			&racedRecord.PlatformPostID, &racedRecord.UploadSessionURI, &racedRecord.BytesUploaded,
			&racedRecord.CreatedAt, &racedRecord.UpdatedAt,
		); scanErr == nil {
			if racedRecord.Status == "published" || racedRecord.Status == "posted" {
				return &racedRecord, LockCached, nil
			}
		}
		return nil, LockInFlight, ErrInFlightConflict
	}

	return nil, LockInFlight, fmt.Errorf("failed inserting idempotency record: %w", insertErr)
}

func (e *Engine) evaluateLostRace(ctx context.Context, postID string) (*PostRecord, LockStatus, error) {
	query := `
		SELECT id, user_id, platform, status, idempotency_key, 
		       COALESCE(platform_post_id, ''), COALESCE(upload_session_uri, ''), 
		       bytes_uploaded, created_at, updated_at
		FROM posts
		WHERE id = $1;
	`
	var r PostRecord
	if err := e.db.QueryRowContext(ctx, query, postID).Scan(
		&r.ID, &r.UserID, &r.Platform, &r.Status, &r.IdempotencyKey,
		&r.PlatformPostID, &r.UploadSessionURI, &r.BytesUploaded,
		&r.CreatedAt, &r.UpdatedAt,
	); err == nil && (r.Status == "published" || r.Status == "posted") {
		return &r, LockCached, nil
	}
	return &r, LockInFlight, ErrInFlightConflict
}

// MarkPublished transitions post status to published and stores the platform post ID.
func (e *Engine) MarkPublished(ctx context.Context, postID, platformPostID string, metadata map[string]interface{}) error {
	now := time.Now().UTC()
	query := `
		UPDATE posts
		SET status = 'published', platform_post_id = $1, published_at = $2, updated_at = $3
		WHERE id = $4;
	`
	_, err := e.db.ExecContext(ctx, query, platformPostID, now, now, postID)
	if err != nil {
		return fmt.Errorf("failed marking post published: %w", err)
	}
	return nil
}

// MarkFailed transitions post status to failed for retryability.
func (e *Engine) MarkFailed(ctx context.Context, postID, errorMessage string) error {
	now := time.Now().UTC()
	query := `
		UPDATE posts
		SET status = 'failed', updated_at = $1
		WHERE id = $2;
	`
	_, err := e.db.ExecContext(ctx, query, now, postID)
	if err != nil {
		return fmt.Errorf("failed marking post failed: %w", err)
	}
	return nil
}

// UpdateResumableSession updates the upload session URI and bytes uploaded for crash recovery.
func (e *Engine) UpdateResumableSession(ctx context.Context, postID, sessionURI string, bytesUploaded int64) error {
	now := time.Now().UTC()
	query := `
		UPDATE posts
		SET upload_session_uri = $1, bytes_uploaded = $2, updated_at = $3
		WHERE id = $4;
	`
	_, err := e.db.ExecContext(ctx, query, sessionURI, bytesUploaded, now, postID)
	if err != nil {
		return fmt.Errorf("failed updating resumable session: %w", err)
	}
	return nil
}
