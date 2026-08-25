// Package instagram provides resilient, idempotent Instagram publishing orchestration and token lifecycle management.
package instagram

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/idempotency"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/security"
)

const (
	// StaleProcessingLockThreshold defines when an in-flight 'processing' row is considered crashed (60 seconds).
	StaleProcessingLockThreshold = 60 * time.Second
)

var (
	// ErrPostProcessingInProgress is returned when an identical request is actively being processed.
	ErrPostProcessingInProgress = errors.New("idempotency: instagram publish is currently processing in-flight (409 conflict)")
	// ErrPlatformNotConnected is returned when user has not authorized Instagram via Meta OAuth 2.0.
	ErrPlatformNotConnected = errors.New("instagram publish: no active Instagram connection found for user")
)

// PublishPostRequest contains parameters for creating an Instagram post or reel.
type PublishPostRequest struct {
	UserID         string
	Caption        string
	MediaURLs      []string
	MediaPath      string
	MediaData      []byte
	MediaType      string // IMAGE or REELS
	IdempotencyKey string
}

// PublishPostResponse contains the result of a published Instagram post.
type PublishPostResponse struct {
	PostID             string `json:"post_id"`
	PlatformPostID     string `json:"platform_post_id"`
	Status             string `json:"status"`
	IsIdempotentReplay bool   `json:"is_idempotent_replay"`
}

// Service coordinates Instagram publishing, idempotency locks, media validation, and token vault management.
type Service struct {
	db        *sql.DB
	repo      *database.Repository
	client    *Client
	validator *MediaValidator
	stager    *MediaStager
	engine    *idempotency.Engine
}

// NewService initializes an Instagram publishing service.
func NewService(db *sql.DB, repo *database.Repository, client *Client, stager *MediaStager) *Service {
	if stager == nil {
		stager, _ = NewMediaStager("", "http://localhost:8080")
	}
	return &Service{
		db:        db,
		repo:      repo,
		client:    client,
		validator: NewMediaValidator(),
		stager:    stager,
		engine:    idempotency.NewEngine(db, StaleProcessingLockThreshold),
	}
}

// EnsureFreshToken checks if user's Meta 60-day token is within the 7-day renewal window and extends it proactively.
func (s *Service) EnsureFreshToken(ctx context.Context, userID string) (accessToken string, expiresAt time.Time, scopes []string, err error) {
	accessBytes, refreshBytes, exp, scps, err := s.repo.GetDecryptedPlatformConnection(ctx, userID, "instagram")
	if err != nil {
		if errors.Is(err, database.ErrNotFound) {
			return "", time.Time{}, nil, ErrPlatformNotConnected
		}
		return "", time.Time{}, nil, fmt.Errorf("failed retrieving instagram credentials: %w", err)
	}

	accessToken = string(accessBytes)
	expiresAt = exp
	scopes = scps

	if accessToken == "" {
		return "", time.Time{}, nil, ErrPlatformNotConnected
	}

	now := time.Now().UTC()
	if !expiresAt.IsZero() && now.After(expiresAt) {
		return "", time.Time{}, nil, fmt.Errorf("%w: token expired at %v", ErrReauthenticationRequired, expiresAt)
	}

	// Proactive Rolling Renewal Window (<= 7 days before expiry)
	if !expiresAt.IsZero() && expiresAt.Sub(now) <= TokenProactiveRenewalThreshold {
		refreshedTok, extErr := s.client.ExtendLongLivedToken(ctx, accessToken)
		if extErr == nil && refreshedTok != nil && refreshedTok.AccessToken != "" {
			newExpiry := now.Add(time.Duration(refreshedTok.ExpiresIn) * time.Second)
			accessToken = refreshedTok.AccessToken
			expiresAt = newExpiry

			// Persist fresh encrypted token into Token Vault
			if saveErr := s.repo.SavePlatformConnection(ctx, userID, "instagram", []byte(accessToken), refreshBytes, newExpiry, scopes); saveErr != nil {
				fmt.Printf("[instagram] warning: failed persisting refreshed token to vault: %v\n", saveErr)
			}
		}
	}

	return accessToken, expiresAt, scopes, nil
}

// Publish executes idempotent, crash-resilient 2-step Instagram media publishing.
func (s *Service) Publish(ctx context.Context, req *PublishPostRequest) (*PublishPostResponse, error) {
	if strings.TrimSpace(req.UserID) == "" {
		return nil, errors.New("user_id is required")
	}

	// 1. Prepare Media Payload
	var mediaBytes []byte
	var mediaURL string

	if len(req.MediaData) > 0 {
		mediaBytes = req.MediaData
	} else if req.MediaPath != "" {
		readBytes, readErr := os.ReadFile(req.MediaPath)
		if readErr != nil {
			return nil, fmt.Errorf("failed reading local media file: %w", readErr)
		}
		mediaBytes = readBytes
	} else if len(req.MediaURLs) > 0 && req.MediaURLs[0] != "" {
		if _, valErr := security.ValidateMediaURL(req.MediaURLs[0]); valErr != nil {
			return nil, fmt.Errorf("instagram media_urls validation failed: %w", valErr)
		}
		mediaURL = req.MediaURLs[0]
	} else {
		return nil, errors.New("instagram publish: missing required media (must provide media_path, media_data, or media_urls)")
	}

	// 2. Pre-Validate Media if raw bytes are available
	var valResult *MediaValidationResult
	var valErr error
	if len(mediaBytes) > 0 {
		valResult, valErr = s.validator.SniffAndValidate(bytes.NewReader(mediaBytes), int64(len(mediaBytes)))
		if valErr != nil {
			return nil, fmt.Errorf("media validation failed: %w", valErr)
		}

		// Meta Graph API strictly requires JPEG for Instagram feed photos. Auto-convert PNG -> JPEG.
		if valResult.Extension == "png" {
			img, _, decErr := image.Decode(bytes.NewReader(mediaBytes))
			if decErr == nil && img != nil {
				var buf bytes.Buffer
				if encErr := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); encErr == nil {
					mediaBytes = buf.Bytes()
					valResult.Extension = "jpg"
					valResult.MimeType = "image/jpeg"
				}
			}
		}
	}

	// 3. Compute Idempotency Key
	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		hasher := sha256.New()
		hasher.Write([]byte(req.UserID))
		hasher.Write([]byte("instagram"))
		hasher.Write([]byte(req.Caption))
		if len(mediaBytes) > 0 {
			hasher.Write(mediaBytes)
		} else {
			hasher.Write([]byte(mediaURL))
		}
		idempotencyKey = hex.EncodeToString(hasher.Sum(nil))
	}

	// 4. Acquire Idempotency Lock
	record, lockStatus, err := s.engine.AcquireLockWithContent(ctx, req.UserID, "instagram", idempotencyKey, req.Caption, req.MediaURLs)
	if err != nil && (lockStatus == idempotency.LockInFlight || errors.Is(err, idempotency.ErrInFlightConflict)) {
		return nil, ErrPostProcessingInProgress
	}
	if err != nil {
		return nil, err
	}

	// If already published, return cached result immediately
	if lockStatus == idempotency.LockCached {
		return &PublishPostResponse{
			PostID:             record.ID,
			PlatformPostID:     record.PlatformPostID,
			Status:             "published",
			IsIdempotentReplay: true,
		}, nil
	}

	if lockStatus != idempotency.LockAcquired {
		return nil, ErrPostProcessingInProgress
	}

	// 5. Ensure Valid / Proactively Refreshed Credentials
	accessToken, _, _, err := s.EnsureFreshToken(ctx, req.UserID)
	if err != nil {
		_ = s.engine.MarkFailed(ctx, record.ID, err.Error())
		return nil, err
	}

	// 6. Discover Instagram Business Account
	igAccount, _, err := s.client.GetInstagramBusinessAccount(ctx, accessToken)
	if err != nil {
		_ = s.engine.MarkFailed(ctx, record.ID, err.Error())
		return nil, err
	}

	// 7. Execute 2-Step Publishing with Crash-Recovery and Expired Container Fallback
	var cleanupFunc func()
	defer func() {
		if cleanupFunc != nil {
			cleanupFunc()
		}
	}()

	var creationID string

	// Step A: Check if resuming from existing container in upload_session_uri
	if record.UploadSessionURI != "" {
		storedID := record.UploadSessionURI
		statusResp, pollErr := s.client.PollContainerStatus(ctx, storedID, accessToken)
		if pollErr == nil && statusResp != nil && statusResp.StatusCode == ContainerStatusFinished {
			// Container is FINISHED -> Proceed directly to Step 2
			creationID = storedID
		} else if errors.Is(pollErr, ErrContainerExpired) {
			// Container expired on Meta (>24h delay). Reset session and re-create if media available.
			if len(mediaBytes) == 0 && mediaURL == "" {
				_ = s.engine.MarkFailed(ctx, record.ID, ErrMediaPayloadExpired.Error())
				return nil, ErrMediaPayloadExpired
			}
			_ = s.engine.UpdateResumableSession(ctx, record.ID, "", 0)
		} else if pollErr != nil {
			_ = s.engine.MarkFailed(ctx, record.ID, pollErr.Error())
			return nil, fmt.Errorf("failed verifying existing container status: %w", pollErr)
		}
	}

	// Step B: Create Container if not already created
	if creationID == "" {
		stageURL := mediaURL
		if len(mediaBytes) > 0 {
			ext := "jpg"
			mime := "image/jpeg"
			if valResult != nil {
				ext = valResult.Extension
				mime = valResult.MimeType
			} else if req.MediaPath != "" {
				ext = filepath.Ext(req.MediaPath)
			}

			var stageErr error
			stageURL, _, cleanupFunc, stageErr = s.stager.StageMedia(mediaBytes, ext, mime)
			if stageErr != nil {
				_ = s.engine.MarkFailed(ctx, record.ID, stageErr.Error())
				return nil, fmt.Errorf("failed staging media for Meta crawler: %w", stageErr)
			}
		}

		mediaType := MediaTypeImage
		if valResult != nil && valResult.MediaType == MediaTypeReels {
			mediaType = MediaTypeReels
		} else if strings.EqualFold(req.MediaType, "REELS") || strings.EqualFold(req.MediaType, "VIDEO") {
			mediaType = MediaTypeReels
		}

		createReq := &CreateContainerRequest{
			IGUserID:    igAccount.ID,
			AccessToken: accessToken,
			Caption:     req.Caption,
			MediaType:   mediaType,
			ShareToFeed: true,
		}
		if mediaType == MediaTypeReels {
			createReq.VideoURL = stageURL
		} else {
			createReq.ImageURL = stageURL
		}

		var createErr error
		creationID, createErr = s.client.CreateMediaContainer(ctx, createReq)
		if createErr != nil {
			_ = s.engine.MarkFailed(ctx, record.ID, createErr.Error())
			return nil, fmt.Errorf("meta container creation failed: %w", createErr)
		}

		// Persist creationID in posts.upload_session_uri for zero-orphan crash resumption
		_ = s.engine.UpdateResumableSession(ctx, record.ID, creationID, 0)

		// Poll Container Status until FINISHED
		statusResp, pollErr := s.client.PollContainerStatus(ctx, creationID, accessToken)
		if pollErr != nil {
			_ = s.engine.MarkFailed(ctx, record.ID, pollErr.Error())
			return nil, fmt.Errorf("meta container processing failed: %w", pollErr)
		}
		if statusResp.StatusCode != ContainerStatusFinished {
			err = fmt.Errorf("%w: status %s", ErrContainerProcessingFailed, statusResp.StatusCode)
			_ = s.engine.MarkFailed(ctx, record.ID, err.Error())
			return nil, err
		}
	}

	// Step C: Publish Media Container (Step 2)
	publishedID, pubErr := s.client.PublishMedia(ctx, igAccount.ID, creationID, accessToken)
	if pubErr != nil {
		_ = s.engine.MarkFailed(ctx, record.ID, pubErr.Error())
		return nil, fmt.Errorf("meta media publish failed: %w", pubErr)
	}

	// Mark Published
	if err := s.engine.MarkPublished(ctx, record.ID, publishedID, nil); err != nil {
		return nil, fmt.Errorf("failed marking post published in database: %w", err)
	}

	return &PublishPostResponse{
		PostID:             record.ID,
		PlatformPostID:     publishedID,
		Status:             "published",
		IsIdempotentReplay: false,
	}, nil
}

// GetAnalytics retrieves Instagram post insights with universal proactive token renewal on the read path.
func (s *Service) GetAnalytics(ctx context.Context, userID, mediaID string) (*UnifiedInstagramMetrics, error) {
	accessToken, _, _, err := s.EnsureFreshToken(ctx, userID)
	if err != nil {
		return nil, err
	}

	return s.client.GetMediaInsights(ctx, mediaID, accessToken)
}
