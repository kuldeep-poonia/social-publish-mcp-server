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

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/idempotency"
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
	engine *idempotency.Engine
}

// NewService initializes a Twitter publishing service.
func NewService(db *sql.DB, repo *database.Repository, client *Client) *Service {
	return &Service{
		db:     db,
		repo:   repo,
		client: client,
		engine: idempotency.NewEngine(db, StaleProcessingLockThreshold),
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

	// 3. Acquire Idempotency Lock via shared Idempotency Engine
	record, lockStatus, err := s.engine.AcquireLock(ctx, req.UserID, "twitter", idempotencyKey)
	if err != nil && (lockStatus == idempotency.LockInFlight || errors.Is(err, idempotency.ErrInFlightConflict)) {
		return nil, ErrPostProcessingInProgress
	}
	if err != nil {
		return nil, err
	}

	// If already published, return cached result immediately (0 Twitter API calls)
	if lockStatus == idempotency.LockCached {
		return &PublishTweetResponse{
			PostID:             record.ID,
			PlatformPostID:     record.PlatformPostID,
			Status:             "published",
			IsIdempotentReplay: true,
		}, nil
	}

	if lockStatus != idempotency.LockAcquired {
		return nil, ErrPostProcessingInProgress
	}

	// 4. Retrieve User's Twitter Connection from Audited Token Vault
	accessBytes, refreshBytes, _, scopes, err := s.repo.GetDecryptedPlatformConnection(ctx, req.UserID, "twitter")
	if err != nil {
		_ = s.engine.MarkFailed(ctx, record.ID, err.Error())
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
		_ = s.engine.MarkFailed(ctx, record.ID, err.Error())
		return nil, fmt.Errorf("twitter api posting failed: %w", err)
	}

	// 6. Mark post as successfully published via shared engine
	if err := s.engine.MarkPublished(ctx, record.ID, tweetResp.Data.ID, nil); err != nil {
		return nil, fmt.Errorf("failed marking post published in database: %w", err)
	}

	return &PublishTweetResponse{
		PostID:             record.ID,
		PlatformPostID:     tweetResp.Data.ID,
		Status:             "published",
		IsIdempotentReplay: false,
	}, nil
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

