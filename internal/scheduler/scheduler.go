// Package scheduler manages autonomous content scheduling, cron triggers,
// background execution workers, and multi-platform publishing pipelines.
package scheduler

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/instagram"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/twitter"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/youtube"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/security"
	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

var (
	// ErrInvalidScheduledTime is returned when scheduled time is in the past or exceeds max horizon.
	ErrInvalidScheduledTime = errors.New("scheduler: scheduled time must be at least 10 seconds in the future")
	// ErrScheduledPostNotFound is returned when trying to modify a non-existent scheduled post.
	ErrScheduledPostNotFound = errors.New("scheduler: scheduled post not found or not in pending state")
)

// SchedulePostRequest encapsulates inputs for scheduling a future social media publication.
type SchedulePostRequest struct {
	UserID         string    `json:"user_id"`
	Platform       string    `json:"platform"`
	Content        string    `json:"content"`
	ScheduledAt    time.Time `json:"scheduled_at"`
	MediaURLs      []string  `json:"media_urls,omitempty"`
	MediaPath      string    `json:"media_path,omitempty"`
	MediaData      []byte    `json:"-"`
	MediaType      string    `json:"media_type,omitempty"`
	ImagePrompt    string    `json:"image_prompt,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
}

// ExecutionReport summarizes batch cron execution metrics.
type ExecutionReport struct {
	ProcessedCount int                `json:"processed_count"`
	SuccessCount   int                `json:"success_count"`
	FailureCount   int                `json:"failure_count"`
	ExecutedPosts  []PostExecutionLog `json:"executed_posts"`
}

// PostExecutionLog contains execution results for an individual scheduled post.
type PostExecutionLog struct {
	PostID         string `json:"post_id"`
	UserID         string `json:"user_id"`
	Platform       string `json:"platform"`
	Status         string `json:"status"`
	PlatformPostID string `json:"platform_post_id,omitempty"`
	Error          string `json:"error,omitempty"`
}

// Service manages the lifecycle and execution of scheduled social posts.
type Service struct {
	db               *sql.DB
	instagramService *instagram.Service
	twitterService   *twitter.Service
	youtubeService   *youtube.PublishService
	stager           *instagram.MediaStager
	repo             *database.Repository
	workerCancel     context.CancelFunc
	mu               sync.Mutex
}

// NewService instantiates a SchedulerService.
func NewService(
	db *sql.DB,
	instagramService *instagram.Service,
	twitterService *twitter.Service,
	youtubeService *youtube.PublishService,
	stager *instagram.MediaStager,
	repo *database.Repository,
) *Service {
	return &Service{
		db:               db,
		instagramService: instagramService,
		twitterService:   twitterService,
		youtubeService:   youtubeService,
		stager:           stager,
		repo:             repo,
	}
}

// SchedulePost validates and stores a future social media post into the database.
func (s *Service) SchedulePost(ctx context.Context, req *SchedulePostRequest) (*models.Post, error) {
	if req.UserID == "" {
		return nil, errors.New("scheduler: user_id is required")
	}
	platform := strings.ToLower(strings.TrimSpace(req.Platform))
	if !models.IsValidPlatform(platform) {
		return nil, fmt.Errorf("%w: %s", models.ErrInvalidPlatform, req.Platform)
	}

	now := time.Now().UTC()
	if req.ScheduledAt.Before(now.Add(10 * time.Second)) {
		return nil, ErrInvalidScheduledTime
	}

	if strings.TrimSpace(req.Content) == "" && strings.TrimSpace(req.ImagePrompt) == "" && len(req.MediaURLs) == 0 && req.MediaPath == "" && len(req.MediaData) == 0 {
		return nil, errors.New("scheduler: content, image_prompt, or media must be provided")
	}

	if req.MediaPath != "" {
		fileInfo, statErr := os.Stat(req.MediaPath)
		if statErr != nil {
			return nil, fmt.Errorf("scheduler: local media file '%s' does not exist or is not accessible on the server. In cloud environments (e.g. Render) or remote MCP clients, local client filesystem paths cannot be reached. Please supply a public HTTPS URL via 'media_urls' or upload Base64 binary via 'media_data' / upload endpoint: %w", req.MediaPath, statErr)
		}
		if fileInfo.IsDir() {
			return nil, fmt.Errorf("scheduler: media_path '%s' is a directory, expected a media file", req.MediaPath)
		}
	}

	idempotencyKey := strings.TrimSpace(req.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = fmt.Sprintf("sched-%s-%s-%d", platform, req.UserID, req.ScheduledAt.Unix())
	}

	mediaType := strings.ToUpper(strings.TrimSpace(req.MediaType))
	if mediaType == "" {
		if platform == models.PlatformYouTube {
			mediaType = "VIDEO"
		} else {
			mediaType = "IMAGE"
		}
	}

	postID := uuid.New().String()
	mediaURLsArray := models.StringArray(req.MediaURLs)

	query := `
		INSERT INTO posts (
			id, user_id, platform, content, media_urls, media_path, media_type,
			image_prompt, status, scheduled_at, idempotency_key, metadata, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, 'scheduled', $9, $10, '{}'::jsonb, $11, $11
		) RETURNING id, user_id, platform, content, media_urls, media_path, media_type,
		            image_prompt, status, scheduled_at, idempotency_key, metadata, created_at, updated_at;
	`

	var post models.Post
	var rawMeta []byte
	err := s.db.QueryRowContext(
		ctx, query,
		postID, req.UserID, platform, req.Content, mediaURLsArray, req.MediaPath, mediaType,
		req.ImagePrompt, req.ScheduledAt.UTC(), idempotencyKey, now,
	).Scan(
		&post.ID, &post.UserID, &post.Platform, &post.Content, &post.MediaURLs, &post.MediaPath,
		&post.MediaType, &post.ImagePrompt, &post.Status, &post.ScheduledAt, &post.IdempotencyKey,
		&rawMeta, &post.CreatedAt, &post.UpdatedAt,
	)

	if err != nil {
		return nil, fmt.Errorf("scheduler: failed inserting scheduled post: %w", err)
	}
	post.Metadata = json.RawMessage(rawMeta)

	log.Printf("[Scheduler] Scheduled post created: ID=%s, UserID=%s, Platform=%s, ScheduledAt=%s",
		post.ID, post.UserID, post.Platform, post.ScheduledAt.Format(time.RFC3339))
	return &post, nil
}

// ListScheduledPosts returns all upcoming scheduled posts for a user.
func (s *Service) ListScheduledPosts(ctx context.Context, userID string, limit int) ([]models.Post, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	query := `
		SELECT id, user_id, platform, COALESCE(platform_post_id, ''), content,
		       media_urls, COALESCE(media_path, ''), COALESCE(media_type, ''),
		       COALESCE(image_prompt, ''), status, scheduled_at, published_at,
		       idempotency_key, metadata, created_at, updated_at
		FROM posts
		WHERE user_id = $1 AND status = 'scheduled'
		ORDER BY scheduled_at ASC
		LIMIT $2;
	`

	rows, err := s.db.QueryContext(ctx, query, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("scheduler: failed querying scheduled posts: %w", err)
	}
	defer rows.Close()

	var posts []models.Post
	for rows.Next() {
		var p models.Post
		var platPostID, mediaPath, mediaType, imagePrompt string
		var rawMeta []byte
		if err := rows.Scan(
			&p.ID, &p.UserID, &p.Platform, &platPostID, &p.Content,
			&p.MediaURLs, &mediaPath, &mediaType,
			&imagePrompt, &p.Status, &p.ScheduledAt, &p.PublishedAt,
			&p.IdempotencyKey, &rawMeta, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scheduler: failed scanning scheduled post: %w", err)
		}
		p.PlatformPostID = platPostID
		p.MediaPath = mediaPath
		p.MediaType = mediaType
		p.ImagePrompt = imagePrompt
		p.Metadata = json.RawMessage(rawMeta)
		posts = append(posts, p)
	}

	return posts, nil
}

// CancelScheduledPost marks a pending scheduled post as cancelled.
func (s *Service) CancelScheduledPost(ctx context.Context, userID, postID string) error {
	query := `
		UPDATE posts
		SET status = 'cancelled', updated_at = NOW()
		WHERE id = $1 AND user_id = $2 AND status = 'scheduled';
	`
	res, err := s.db.ExecContext(ctx, query, postID, userID)
	if err != nil {
		return fmt.Errorf("scheduler: failed cancelling post: %w", err)
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return ErrScheduledPostNotFound
	}
	log.Printf("[Scheduler] Post cancelled: ID=%s, UserID=%s", postID, userID)
	return nil
}

// ExecuteDuePosts queries and publishes all scheduled posts that are due (scheduled_at <= NOW()).
func (s *Service) ExecuteDuePosts(ctx context.Context) (*ExecutionReport, error) {
	now := time.Now().UTC()
	report := &ExecutionReport{
		ExecutedPosts: make([]PostExecutionLog, 0),
	}

	// 1. Fetch & lock due posts atomically
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, fmt.Errorf("scheduler: failed beginning tx: %w", err)
	}

	query := `
		SELECT id, user_id, platform, content, media_urls, COALESCE(media_path, ''),
		       COALESCE(media_type, ''), COALESCE(image_prompt, ''), idempotency_key
		FROM posts
		WHERE status = 'scheduled' AND scheduled_at <= $1
		ORDER BY scheduled_at ASC
		LIMIT 20
		FOR UPDATE SKIP LOCKED;
	`

	rows, err := tx.QueryContext(ctx, query, now)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("scheduler: failed querying due posts: %w", err)
	}

	type duePost struct {
		id             string
		userID         string
		platform       string
		content        string
		mediaURLs      models.StringArray
		mediaPath      string
		mediaType      string
		imagePrompt    string
		idempotencyKey string
	}

	var batch []duePost
	for rows.Next() {
		var dp duePost
		if err := rows.Scan(
			&dp.id, &dp.userID, &dp.platform, &dp.content, &dp.mediaURLs,
			&dp.mediaPath, &dp.mediaType, &dp.imagePrompt, &dp.idempotencyKey,
		); err == nil {
			batch = append(batch, dp)
		}
	}
	rows.Close()

	if len(batch) == 0 {
		_ = tx.Commit()
		return report, nil
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("scheduler: failed committing due posts tx: %w", err)
	}

	// 2. Execute publishing for each post
	for _, dp := range batch {
		report.ProcessedCount++
		log.Printf("[Scheduler] Executing scheduled post: ID=%s, UserID=%s, Platform=%s", dp.id, dp.userID, dp.platform)

		execLog := PostExecutionLog{
			PostID:   dp.id,
			UserID:   dp.userID,
			Platform: dp.platform,
		}

		publishedID, pubErr := s.publishSinglePost(ctx, dp.userID, dp.platform, dp.content, []string(dp.mediaURLs), dp.mediaPath, dp.mediaType, dp.imagePrompt, dp.idempotencyKey)

		if pubErr != nil {
			report.FailureCount++
			execLog.Status = "failed"
			execLog.Error = pubErr.Error()

			metaJSON, _ := json.Marshal(map[string]interface{}{
				"last_error": pubErr.Error(),
				"failed_at":  time.Now().UTC().Format(time.RFC3339),
			})

			_, _ = s.db.ExecContext(
				ctx,
				"UPDATE posts SET status = 'failed', metadata = metadata || $1::jsonb, updated_at = NOW() WHERE id = $2",
				metaJSON, dp.id,
			)
			log.Printf("[Scheduler] Scheduled post execution FAILED: ID=%s, Error=%v", dp.id, pubErr)
		} else {
			report.SuccessCount++
			execLog.Status = "published"
			execLog.PlatformPostID = publishedID

			_, _ = s.db.ExecContext(
				ctx,
				"UPDATE posts SET status = 'published', published_at = NOW(), platform_post_id = $1, updated_at = NOW() WHERE id = $2",
				publishedID, dp.id,
			)
			log.Printf("[Scheduler] Scheduled post execution SUCCESS: ID=%s, PlatformPostID=%s", dp.id, publishedID)
		}

		report.ExecutedPosts = append(report.ExecutedPosts, execLog)
	}

	return report, nil
}

func (s *Service) publishSinglePost(
	ctx context.Context,
	userID, platform, content string,
	mediaURLs []string,
	mediaPath, mediaType, imagePrompt, idempotencyKey string,
) (string, error) {
	// Set actor context for database isolation
	execCtx := database.WithActor(ctx, database.ActorContext{
		ActorID:   userID,
		IPAddress: "scheduler-internal-cron",
	})

	switch platform {
	case models.PlatformInstagram:
		if s.instagramService == nil {
			return "", errors.New("instagram service is uninitialized")
		}
		resp, err := s.instagramService.Publish(execCtx, &instagram.PublishPostRequest{
			UserID:         userID,
			Caption:        content,
			MediaURLs:      mediaURLs,
			MediaPath:      mediaPath,
			MediaType:      mediaType,
			ImagePrompt:    imagePrompt,
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			return "", err
		}
		return resp.PlatformPostID, nil

	case models.PlatformTwitter:
		if s.twitterService == nil {
			return "", errors.New("twitter service is uninitialized")
		}
		resp, err := s.twitterService.PublishTweet(execCtx, &twitter.PublishTweetRequest{
			UserID:         userID,
			Content:        content,
			MediaURLs:      mediaURLs,
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			return "", err
		}
		return resp.PlatformPostID, nil

	case models.PlatformYouTube:
		if s.youtubeService == nil {
			return "", errors.New("youtube service is uninitialized")
		}

		var videoBytes []byte
		if len(mediaURLs) > 0 && mediaURLs[0] != "" {
			if strings.HasPrefix(strings.ToLower(mediaURLs[0]), "http://") || strings.HasPrefix(strings.ToLower(mediaURLs[0]), "https://") {
				fetchedBytes, _, fetchErr := security.FetchMediaWithSSRFProtection(ctx, mediaURLs[0], 500*1024*1024)
				if fetchErr != nil {
					return "", fmt.Errorf("failed fetching remote video URL: %w", fetchErr)
				}
				videoBytes = fetchedBytes
			} else {
				data, readErr := os.ReadFile(mediaURLs[0])
				if readErr == nil {
					videoBytes = data
				}
			}
		} else if mediaPath != "" {
			readBytes, readErr := os.ReadFile(mediaPath)
			if readErr != nil {
				return "", fmt.Errorf("failed reading local media file: %w", readErr)
			}
			videoBytes = readBytes
		}

		if len(videoBytes) == 0 {
			return "", errors.New("missing valid video media for scheduled youtube publication")
		}

		resp, err := s.youtubeService.PublishVideo(execCtx, &youtube.PublishVideoRequest{
			UserID:         userID,
			Title:          content,
			Description:    content,
			PrivacyStatus:  "public",
			VideoReader:    bytes.NewReader(videoBytes),
			TotalBytes:     int64(len(videoBytes)),
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			return "", err
		}
		return resp.PlatformPostID, nil

	default:
		return "", fmt.Errorf("unsupported platform: %s", platform)
	}
}

// StartWorker launches an asynchronous ticker to execute due posts on regular intervals.
func (s *Service) StartWorker(ctx context.Context, interval time.Duration) {
	s.mu.Lock()
	if s.workerCancel != nil {
		s.mu.Unlock()
		return
	}

	workerCtx, cancel := context.WithCancel(ctx)
	s.workerCancel = cancel
	s.mu.Unlock()

	if interval <= 0 {
		interval = 30 * time.Second
	}

	log.Printf("[Scheduler Worker] Started autonomous background worker (interval: %v)", interval)

	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-workerCtx.Done():
				log.Printf("[Scheduler Worker] Stopped background worker")
				return
			case <-ticker.C:
				rep, err := s.ExecuteDuePosts(workerCtx)
				if err != nil {
					log.Printf("[Scheduler Worker] Error executing due posts: %v", err)
				} else if rep != nil && rep.ProcessedCount > 0 {
					log.Printf("[Scheduler Worker] Executed %d due posts (%d succeeded, %d failed)",
						rep.ProcessedCount, rep.SuccessCount, rep.FailureCount)
				}
			}
		}
	}()
}

// StopWorker terminates the background worker loop.
func (s *Service) StopWorker() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.workerCancel != nil {
		s.workerCancel()
		s.workerCancel = nil
	}
}
