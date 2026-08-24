// Package twitter provides the resilient, idempotent social publishing orchestration service for Twitter/X.
package twitter

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

const (
	// StaleProcessingLockThreshold defines when an in-flight 'processing' row is considered crashed (60 seconds).
	StaleProcessingLockThreshold = 60 * time.Second
)

var (
	// ErrPostProcessingInProgress is returned when an identical request is actively being processed.
	ErrPostProcessingInProgress = errors.New("idempotency: post request is currently processing in-flight (409 conflict)")
	// ErrPlatformNotConnected is returned when user has not authorized Twitter via OAuth 2.0.
	ErrPlatformNotConnected = errors.New("twitter publish: no active Twitter connection found for user")
)

// PublishTweetRequest contains parameters for creating a new tweet.
type PublishTweetRequest struct {
	UserID         string
	Content        string
	MediaURLs      []string
	IdempotencyKey string
}

// PublishTweetResponse contains the result of a published tweet.
type PublishTweetResponse struct {
	PostID             string `json:"post_id"`
	PlatformPostID     string `json:"platform_post_id"`
	Status             string `json:"status"`
	IsIdempotentReplay bool   `json:"is_idempotent_replay"`
}

// Service coordinates Twitter publishing, idempotency locks, media validation, and vault retrieval.
type Service struct {
	db     *sql.DB
	repo   *database.Repository
	client *Client
}

// NewService initializes a Twitter publishing service.
func NewService(db *sql.DB, repo *database.Repository, client *Client) *Service {
	return &Service{
		db:     db,
		repo:   repo,
		client: client,
	}
}

// PublishTweet orchestrates atomic idempotency acquisition, token retrieval, 401 refresh recovery, and Twitter API posting.
func (s *Service) PublishTweet(ctx context.Context, req *PublishTweetRequest) (*PublishTweetResponse, error) {
	if strings.TrimSpace(req.UserID) == "" {
		return nil, errors.New("user_id is required")
	}

	// 1. Generate or validate idempotency key
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		// Deterministic SHA-256 hash based on user, platform, and content
		h := sha256.New()
		h.Write([]byte(req.UserID))
		h.Write([]byte("twitter"))
		h.Write([]byte(req.Content))
		for _, u := range req.MediaURLs {
			h.Write([]byte(u))
		}
		idempotencyKey = hex.EncodeToString(h.Sum(nil))
	}

	// 2. Validate tweet text constraints before DB acquisition
	if _, err := ValidateTweetText(req.Content); err != nil {
		return nil, err
	}

	// 3. Acquire Idempotency Lock with Stale Crashed Worker Recovery
	postRecord, lockAcquired, err := s.acquireIdempotencyLock(ctx, req.UserID, idempotencyKey, req.Content, req.MediaURLs)
	if err != nil {
		return nil, err
	}

	// If already published, return cached result immediately (0 Twitter API calls)
	if !lockAcquired && (postRecord.Status == "published" || postRecord.Status == "posted") {
		return &PublishTweetResponse{
			PostID:             postRecord.ID,
			PlatformPostID:     postRecord.PlatformPostID,
			Status:             "published",
			IsIdempotentReplay: true,
		}, nil
	}

	if !lockAcquired {
		return nil, ErrPostProcessingInProgress
	}

	// 4. Retrieve User's Twitter Connection from Audited Token Vault
	accessBytes, refreshBytes, _, scopes, err := s.repo.GetDecryptedPlatformConnection(ctx, req.UserID, "twitter")
	if err != nil {
		s.markPostFailed(ctx, idempotencyKey)
		if errors.Is(err, database.ErrNotFound) {
			return nil, ErrPlatformNotConnected
		}
		return nil, fmt.Errorf("failed retrieving platform credentials: %w", err)
	}

	accessToken := string(accessBytes)
	refreshToken := string(refreshBytes)

	// 5. Execute Twitter API v2 Post with Auto-Refresh Recovery
	tweetCreateReq := &TweetCreateRequest{
		Text: req.Content,
	}

	tweetResp, err := s.postTweetWithTokenRecovery(ctx, req.UserID, accessToken, refreshToken, scopes, tweetCreateReq)
	if err != nil {
		s.markPostFailed(ctx, idempotencyKey)
		return nil, fmt.Errorf("twitter api posting failed: %w", err)
	}

	// 6. Mark post as successfully published
	if err := s.markPostPublished(ctx, idempotencyKey, tweetResp.Data.ID); err != nil {
		return nil, fmt.Errorf("failed marking post published in database: %w", err)
	}

	return &PublishTweetResponse{
		PostID:             postRecord.ID,
		PlatformPostID:     tweetResp.Data.ID,
		Status:             "published",
		IsIdempotentReplay: false,
	}, nil
}

func (s *Service) acquireIdempotencyLock(ctx context.Context, userID, idempotencyKey, content string, mediaURLs []string) (*models.Post, bool, error) {
	staleCutoff := time.Now().UTC().Add(-StaleProcessingLockThreshold)

	// Step A: Check if row already exists
	var existing models.Post
	var mediaArr models.StringArray
	var platformPostID sql.NullString
	var scheduledAt, publishedAt sql.NullTime

	query := `SELECT id, user_id, platform, platform_post_id, content, media_urls, status, scheduled_at, published_at, idempotency_key, created_at, updated_at 
	          FROM posts WHERE idempotency_key = $1`

	err := s.db.QueryRowContext(ctx, query, idempotencyKey).Scan(
		&existing.ID, &existing.UserID, &existing.Platform, &platformPostID,
		&existing.Content, &mediaArr, &existing.Status, &scheduledAt, &publishedAt,
		&existing.IdempotencyKey, &existing.CreatedAt, &existing.UpdatedAt,
	)

	if err == nil {
		existing.PlatformPostID = platformPostID.String
		existing.MediaURLs = []string(mediaArr)

		if existing.Status == "published" || existing.Status == "posted" {
			return &existing, false, nil
		}

		if existing.Status == "processing" && existing.UpdatedAt.After(staleCutoff) {
			// Active in-flight attempt
			return &existing, false, ErrPostProcessingInProgress
		}

		// Row is stale processing or failed: attempt atomic conditional reclaim
		reclaimQuery := `UPDATE posts 
		                 SET status = 'processing', updated_at = (NOW() AT TIME ZONE 'UTC') 
		                 WHERE idempotency_key = $1 AND (status = 'failed' OR (status = 'processing' AND updated_at < $2))`
		res, execErr := s.db.ExecContext(ctx, reclaimQuery, idempotencyKey, staleCutoff)
		if execErr != nil {
			return nil, false, fmt.Errorf("failed executing lock reclaim: %w", execErr)
		}

		rows, _ := res.RowsAffected()
		if rows == 1 {
			// Winner of reclaim race
			return &existing, true, nil
		}

		// Lost race to concurrent worker
		return &existing, false, ErrPostProcessingInProgress
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("database query error during idempotency check: %w", err)
	}

	// Step B: No existing row -> Attempt initial INSERT with status='processing'
	insertQuery := `INSERT INTO posts (user_id, platform, content, media_urls, status, idempotency_key, created_at, updated_at)
	                VALUES ($1, 'twitter', $2, $3, 'processing', $4, (NOW() AT TIME ZONE 'UTC'), (NOW() AT TIME ZONE 'UTC'))
	                RETURNING id, created_at, updated_at`

	var newPost models.Post
	newPost.UserID = userID
	newPost.Platform = "twitter"
	newPost.Content = content
	newPost.MediaURLs = mediaURLs
	newPost.Status = "processing"
	newPost.IdempotencyKey = idempotencyKey

	insertErr := s.db.QueryRowContext(ctx, insertQuery, userID, content, models.StringArray(mediaURLs), idempotencyKey).Scan(
		&newPost.ID, &newPost.CreatedAt, &newPost.UpdatedAt,
	)

	if insertErr != nil {
		// Catch PostgreSQL Unique Constraint Violation (SQLSTATE 23505) gracefully
		var pgErr *pgconn.PgError
		if errors.As(insertErr, &pgErr) && pgErr.Code == "23505" {
			return nil, false, ErrPostProcessingInProgress
		}
		return nil, false, fmt.Errorf("failed inserting initial idempotency row: %w", insertErr)
	}

	return &newPost, true, nil
}

func (s *Service) postTweetWithTokenRecovery(ctx context.Context, userID, accessToken, refreshToken string, scopes []string, req *TweetCreateRequest) (*TweetCreateResponse, error) {
	// First attempt
	resp, err := s.client.PostTweet(ctx, accessToken, req)
	if err == nil {
		return resp, nil
	}

	// Inspect if error is 401 Unauthorized requiring token refresh
	var apiErr *TwitterAPIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 401 && refreshToken != "" {
		// Attempt automatic token refresh
		refreshResp, refreshErr := s.client.RefreshToken(ctx, refreshToken)
		if refreshErr != nil {
			return nil, fmt.Errorf("token refresh failed after 401: %w", refreshErr)
		}

		// Update refreshed tokens in encrypted database vault
		expiresAt := time.Now().UTC().Add(time.Duration(refreshResp.ExpiresIn) * time.Second)
		_ = s.repo.SavePlatformConnection(ctx, userID, "twitter", []byte(refreshResp.AccessToken), []byte(refreshResp.RefreshToken), expiresAt, scopes)

		// Retry tweet post with new token
		return s.client.PostTweet(ctx, refreshResp.AccessToken, req)
	}

	return nil, err
}

func (s *Service) markPostPublished(ctx context.Context, idempotencyKey, platformPostID string) error {
	query := `UPDATE posts 
	          SET status = 'published', platform_post_id = $1, published_at = (NOW() AT TIME ZONE 'UTC'), updated_at = (NOW() AT TIME ZONE 'UTC')
	          WHERE idempotency_key = $2`
	_, err := s.db.ExecContext(ctx, query, platformPostID, idempotencyKey)
	return err
}

func (s *Service) markPostFailed(ctx context.Context, idempotencyKey string) {
	query := `UPDATE posts 
	          SET status = 'failed', updated_at = (NOW() AT TIME ZONE 'UTC')
	          WHERE idempotency_key = $1`
	_, _ = s.db.ExecContext(ctx, query, idempotencyKey)
}
