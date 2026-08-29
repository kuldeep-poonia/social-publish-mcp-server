package scheduler

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
)

func TestSchedulePost_Validation(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed creating sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(db, nil, nil, nil, nil, nil)
	ctx := context.Background()

	// 1. Missing user_id
	_, err = svc.SchedulePost(ctx, &SchedulePostRequest{
		Platform:    "instagram",
		Content:     "Hello",
		ScheduledAt: time.Now().Add(1 * time.Hour),
	})
	if err == nil {
		t.Errorf("expected error for missing user_id, got nil")
	}

	// 2. Unsupported platform
	_, err = svc.SchedulePost(ctx, &SchedulePostRequest{
		UserID:      uuid.New().String(),
		Platform:    "tiktok_invalid",
		Content:     "Hello",
		ScheduledAt: time.Now().Add(1 * time.Hour),
	})
	if err == nil {
		t.Errorf("expected error for invalid platform, got nil")
	}

	// 3. Scheduled time in past
	_, err = svc.SchedulePost(ctx, &SchedulePostRequest{
		UserID:      uuid.New().String(),
		Platform:    "instagram",
		Content:     "Hello",
		ScheduledAt: time.Now().Add(-10 * time.Minute),
	})
	if err == nil {
		t.Errorf("expected error for past scheduled time, got nil")
	}

	// 4. Empty content and empty media
	_, err = svc.SchedulePost(ctx, &SchedulePostRequest{
		UserID:      uuid.New().String(),
		Platform:    "instagram",
		Content:     "",
		ScheduledAt: time.Now().Add(1 * time.Hour),
	})
	if err == nil {
		t.Errorf("expected error for empty content/media, got nil")
	}
}

func TestSchedulePost_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed creating sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(db, nil, nil, nil, nil, nil)
	ctx := context.Background()

	testUserID := uuid.New().String()
	testPostID := uuid.New().String()
	schedTime := time.Now().UTC().Add(2 * time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO posts")).
		WithArgs(
			sqlmock.AnyArg(), testUserID, "instagram", "Awesome Scheduled Reel",
			sqlmock.AnyArg(), "", "REELS",
			"Cyberpunk neon", schedTime, sqlmock.AnyArg(), sqlmock.AnyArg(),
		).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "platform", "content", "media_urls", "media_path", "media_type",
			"image_prompt", "status", "scheduled_at", "idempotency_key", "metadata", "created_at", "updated_at",
		}).AddRow(
			testPostID, testUserID, "instagram", "Awesome Scheduled Reel", "{}", "", "REELS",
			"Cyberpunk neon", "scheduled", schedTime, "sched-key-1", []byte("{}"), time.Now(), time.Now(),
		))

	post, err := svc.SchedulePost(ctx, &SchedulePostRequest{
		UserID:         testUserID,
		Platform:       "instagram",
		Content:        "Awesome Scheduled Reel",
		ScheduledAt:    schedTime,
		MediaURLs:      []string{"https://images.unsplash.com/photo-sample"},
		MediaType:      "REELS",
		ImagePrompt:    "Cyberpunk neon",
		IdempotencyKey: "sched-key-1",
	})

	if err != nil {
		t.Fatalf("SchedulePost failed unexpectedly: %v", err)
	}
	if post.ID != testPostID || post.Status != "scheduled" || post.MediaType != "REELS" {
		t.Errorf("unexpected post fields: %+v", post)
	}
}

func TestSchedulePost_MediaPathValidation(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed creating sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(db, nil, nil, nil, nil, nil)
	ctx := context.Background()

	// Non-existent file path must be cleanly rejected
	_, err = svc.SchedulePost(ctx, &SchedulePostRequest{
		UserID:      uuid.New().String(),
		Platform:    "instagram",
		Content:     "Reel with missing local file",
		ScheduledAt: time.Now().UTC().Add(1 * time.Hour),
		MediaPath:   "C:/non_existent_folder/missing_video.mp4",
	})

	if err == nil {
		t.Fatalf("expected error for non-existent media_path, got nil")
	}
	if !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected helpful error message mentioning file does not exist, got: %v", err)
	}
}

func TestListScheduledPosts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed creating sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(db, nil, nil, nil, nil, nil)
	ctx := context.Background()
	testUserID := uuid.New().String()
	schedTime := time.Now().UTC().Add(1 * time.Hour)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, user_id, platform")).
		WithArgs(testUserID, 50).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "platform", "platform_post_id", "content",
			"media_urls", "media_path", "media_type",
			"image_prompt", "status", "scheduled_at", "published_at",
			"idempotency_key", "metadata", "created_at", "updated_at",
		}).AddRow(
			"post-1", testUserID, "twitter", "", "Scheduled tweet",
			"{}", "", "IMAGE", "", "scheduled", schedTime, nil,
			"key-1", []byte("{}"), time.Now(), time.Now(),
		))

	posts, err := svc.ListScheduledPosts(ctx, testUserID, 50)
	if err != nil {
		t.Fatalf("ListScheduledPosts failed: %v", err)
	}
	if len(posts) != 1 || posts[0].ID != "post-1" {
		t.Errorf("unexpected posts: %+v", posts)
	}
}

func TestCancelScheduledPost(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed creating sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(db, nil, nil, nil, nil, nil)
	ctx := context.Background()
	testUserID := uuid.New().String()
	testPostID := uuid.New().String()

	// 1. Success cancel
	mock.ExpectExec(regexp.QuoteMeta("UPDATE posts SET status = 'cancelled'")).
		WithArgs(testPostID, testUserID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = svc.CancelScheduledPost(ctx, testUserID, testPostID)
	if err != nil {
		t.Fatalf("CancelScheduledPost failed: %v", err)
	}

	// 2. Not found
	mock.ExpectExec(regexp.QuoteMeta("UPDATE posts SET status = 'cancelled'")).
		WithArgs("unknown-id", testUserID).
		WillReturnResult(sqlmock.NewResult(0, 0))

	err = svc.CancelScheduledPost(ctx, testUserID, "unknown-id")
	if err != ErrScheduledPostNotFound {
		t.Errorf("expected ErrScheduledPostNotFound, got: %v", err)
	}
}

func TestExecuteDuePosts_NoPosts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed creating sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(db, nil, nil, nil, nil, nil)
	ctx := context.Background()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, user_id, platform")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "platform", "content", "media_urls", "media_path", "media_type", "image_prompt", "idempotency_key",
		}))
	mock.ExpectCommit()

	report, err := svc.ExecuteDuePosts(ctx)
	if err != nil {
		t.Fatalf("ExecuteDuePosts failed: %v", err)
	}
	if report.ProcessedCount != 0 {
		t.Errorf("expected 0 processed posts, got %d", report.ProcessedCount)
	}
}

func TestExecuteDuePosts_FullDispatchAndFailureTracking(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed creating sqlmock: %v", err)
	}
	defer db.Close()

	svc := NewService(db, nil, nil, nil, nil, nil)
	ctx := context.Background()
	duePostID := uuid.New().String()
	userID := uuid.New().String()

	// 1. Transaction starts and selects 1 due post
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, user_id, platform")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "platform", "content", "media_urls", "media_path", "media_type", "image_prompt", "idempotency_key",
		}).AddRow(
			duePostID, userID, "instagram", "Scheduled post content", "{}", "", "IMAGE", "", "due-key-1",
		))
	mock.ExpectCommit()

	// 2. Platform service execution fails because adapter is nil in this test instance -> updates status to 'failed' with error metadata
	mock.ExpectExec(regexp.QuoteMeta("UPDATE posts SET status = 'failed'")).
		WithArgs(sqlmock.AnyArg(), duePostID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	report, err := svc.ExecuteDuePosts(ctx)
	if err != nil {
		t.Fatalf("ExecuteDuePosts failed: %v", err)
	}
	if report.ProcessedCount != 1 {
		t.Fatalf("expected 1 processed post, got %d", report.ProcessedCount)
	}
	if report.FailureCount != 1 {
		t.Errorf("expected 1 failure due to nil adapter, got %d", report.FailureCount)
	}
	if len(report.ExecutedPosts) != 1 || report.ExecutedPosts[0].Status != "failed" {
		t.Errorf("expected failed status in log, got %+v", report.ExecutedPosts)
	}
}
