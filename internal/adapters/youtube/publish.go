// Package youtube provides the resilient, quota-preserving YouTube publishing orchestration service.
package youtube

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/idempotency"
)

var (
	// ErrPostProcessingInProgress is returned when an identical request is actively being processed.
	ErrPostProcessingInProgress = errors.New("idempotency: video upload is currently processing in-flight (409 conflict)")
	// ErrPlatformNotConnected is returned when user has not authorized YouTube via Google OAuth 2.0.
	ErrPlatformNotConnected = errors.New("youtube publish: no active YouTube connection found for user")
)

// PublishVideoRequest contains parameters for uploading a new video.
type PublishVideoRequest struct {
	UserID         string
	Title          string
	Description    string
	Tags           []string
	PrivacyStatus  string // "public", "private", "unlisted"
	VideoReader    io.ReadSeeker
	TotalBytes     int64
	IdempotencyKey string
}

// PublishVideoResponse contains the result of an uploaded video.
type PublishVideoResponse struct {
	PostID             string `json:"post_id"`
	PlatformPostID     string `json:"platform_post_id"`
	Status             string `json:"status"`
	IsIdempotentReplay bool   `json:"is_idempotent_replay"`
}

// PublishService coordinates YouTube publishing, resumable uploads, quota preservation, and token retrieval.
type PublishService struct {
	db           *sql.DB
	repo         *database.Repository
	client       *Client
	quotaManager *QuotaManager
	engine       *idempotency.Engine
}

// NewPublishService initializes a YouTube publishing service.
func NewPublishService(db *sql.DB, repo *database.Repository, client *Client, quotaManager *QuotaManager) *PublishService {
	if quotaManager == nil {
		quotaManager = NewQuotaManager(QuotaDailyBudget)
	}
	return &PublishService{
		db:           db,
		repo:         repo,
		client:       client,
		quotaManager: quotaManager,
		engine:       idempotency.NewEngine(db, 120*time.Second), // 2 min threshold for video uploads
	}
}

// PublishVideo orchestrates pre-flight validation, quota reservation, crash-recovered resumable upload, and database status.
func (s *PublishService) PublishVideo(ctx context.Context, req *PublishVideoRequest) (*PublishVideoResponse, error) {
	if strings.TrimSpace(req.UserID) == "" {
		return nil, errors.New("user_id is required")
	}

	// 1. Generate or validate idempotency key
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		h := sha256.New()
		h.Write([]byte(req.UserID))
		h.Write([]byte("youtube"))
		h.Write([]byte(req.Title))
		h.Write([]byte(req.Description))
		idempotencyKey = hex.EncodeToString(h.Sum(nil))
	}

	// 2. Pre-flight Metadata Validation
	if err := ValidateVideoMetadata(req.Title, req.Description, req.PrivacyStatus); err != nil {
		return nil, err
	}

	// 3. Pre-flight Video Binary Sniffing
	headerBuf := make([]byte, 32)
	n, err := req.VideoReader.Read(headerBuf)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("failed reading video header: %w", err)
	}
	mimeType, err := ValidateVideoHeader(headerBuf[:n])
	if err != nil {
		return nil, err
	}
	_, _ = req.VideoReader.Seek(0, io.SeekStart) // Rewind stream

	// 4. Acquire Idempotency Lock via shared Idempotency Engine
	record, lockStatus, err := s.engine.AcquireLockWithContent(ctx, req.UserID, "youtube", idempotencyKey, req.Title, nil)
	if err != nil && (lockStatus == idempotency.LockInFlight || errors.Is(err, idempotency.ErrInFlightConflict)) {
		return nil, ErrPostProcessingInProgress
	}
	if err != nil {
		return nil, err
	}

	// If already published, return cached result immediately with 0 quota burned
	if lockStatus == idempotency.LockCached {
		return &PublishVideoResponse{
			PostID:             record.ID,
			PlatformPostID:     record.PlatformPostID,
			Status:             "published",
			IsIdempotentReplay: true,
		}, nil
	}

	if lockStatus != idempotency.LockAcquired {
		return nil, ErrPostProcessingInProgress
	}

	// 5. Pre-flight Quota Reservation (1,600 units for winner only)
	if err := s.quotaManager.ReserveQuota(ctx, req.UserID, QuotaUploadCost); err != nil {
		_ = s.engine.MarkFailed(ctx, record.ID, err.Error())
		return nil, err
	}

	// 6. Retrieve User's Google YouTube Connection from Vault
	accessBytes, refreshBytes, _, scopes, err := s.repo.GetDecryptedPlatformConnection(ctx, req.UserID, "youtube")
	if err != nil {
		s.quotaManager.ReleaseQuota(ctx, req.UserID, QuotaUploadCost)
		_ = s.engine.MarkFailed(ctx, record.ID, err.Error())
		if errors.Is(err, database.ErrNotFound) {
			return nil, ErrPlatformNotConnected
		}
		return nil, fmt.Errorf("failed retrieving platform credentials: %w", err)
	}

	accessToken := string(accessBytes)
	refreshToken := string(refreshBytes)

	// 7. Execute Resumable Upload with Zero-Quota-Waste Crash Recovery
	videoID, err := s.executeResumableUpload(ctx, req.UserID, accessToken, refreshToken, scopes, record, req, mimeType)
	if err != nil {
		_ = s.engine.MarkFailed(ctx, record.ID, err.Error())
		return nil, fmt.Errorf("youtube video upload failed: %w", err)
	}

	// 8. Mark post as successfully published
	if err := s.engine.MarkPublished(ctx, record.ID, videoID, nil); err != nil {
		return nil, fmt.Errorf("failed marking post published in database: %w", err)
	}

	return &PublishVideoResponse{
		PostID:             record.ID,
		PlatformPostID:     videoID,
		Status:             "published",
		IsIdempotentReplay: false,
	}, nil
}

func (s *PublishService) executeResumableUpload(
	ctx context.Context,
	userID, accessToken, refreshToken string,
	scopes []string,
	record *idempotency.PostRecord,
	req *PublishVideoRequest,
	mimeType string,
) (string, error) {
	sessionURI := record.UploadSessionURI
	var currentOffset int64 = 0

	// Step A: Determine if resuming an existing upload session or creating a new one
	if sessionURI != "" {
		// Existing session found from a crashed worker -> query byte offset (0 duplicate quota burned)
		offset, err := s.client.QueryResumableOffset(ctx, sessionURI, req.TotalBytes)
		if err == nil {
			currentOffset = offset
		} else {
			sessionURI = "" // If existing session expired at Google, restart session
		}
	}

	if sessionURI == "" {
		// New session initiation
		snippet := &VideoSnippet{
			Title:       req.Title,
			Description: req.Description,
			Tags:        req.Tags,
		}
		status := &VideoStatus{
			PrivacyStatus: req.PrivacyStatus,
		}

		newSessionURI, err := s.client.InitiateResumableUpload(ctx, accessToken, snippet, status, mimeType, req.TotalBytes)
		if err != nil {
			// Check if 401 token refresh is required
			var apiErr *YouTubeAPIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == 401 && refreshToken != "" {
				refreshResp, refreshErr := s.client.RefreshToken(ctx, refreshToken)
				if refreshErr != nil {
					return "", fmt.Errorf("token refresh failed after 401: %w", refreshErr)
				}
				expiresAt := time.Now().UTC().Add(time.Duration(refreshResp.ExpiresIn) * time.Second)
				_ = s.repo.SavePlatformConnection(ctx, userID, "youtube", []byte(refreshResp.AccessToken), []byte(refreshResp.RefreshToken), expiresAt, scopes)
				accessToken = refreshResp.AccessToken
				newSessionURI, err = s.client.InitiateResumableUpload(ctx, accessToken, snippet, status, mimeType, req.TotalBytes)
			}
		}
		if err != nil {
			return "", err
		}

		sessionURI = newSessionURI
		_ = s.engine.UpdateResumableSession(ctx, record.ID, sessionURI, 0)
	}

	// Step B: Stream chunks starting from currentOffset
	chunkSize := int64(UploadChunkSize) // 8MB

	for currentOffset < req.TotalBytes {
		endByte := currentOffset + chunkSize - 1
		if endByte >= req.TotalBytes {
			endByte = req.TotalBytes - 1
		}
		currentChunkLen := endByte - currentOffset + 1

		_, err := req.VideoReader.Seek(currentOffset, io.SeekStart)
		if err != nil {
			return "", fmt.Errorf("failed seeking video stream to offset %d: %w", currentOffset, err)
		}

		chunkReader := io.LimitReader(req.VideoReader, currentChunkLen)

		bytesRecv, videoID, isCompleted, err := s.client.UploadChunk(
			ctx, sessionURI, chunkReader, currentOffset, endByte, req.TotalBytes, mimeType,
		)
		if err != nil {
			return "", err
		}

		if isCompleted {
			_ = s.engine.UpdateResumableSession(ctx, record.ID, sessionURI, req.TotalBytes)
			return videoID, nil
		}

		currentOffset = bytesRecv
		_ = s.engine.UpdateResumableSession(ctx, record.ID, sessionURI, currentOffset)
	}

	return "", errors.New("youtube publish: upload stream ended without completion confirmation")
}
