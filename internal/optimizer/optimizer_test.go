package optimizer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/youtube"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/crypto"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

func TestOptimizer_CTRVariationsAndSanitization(t *testing.T) {
	svc := NewService(nil, nil, nil, "", nil, nil)

	req := &UpdateMetadataRequest{
		PostID:         "yt_test_123",
		Platform:       "youtube",
		Objective:      "ctr_boost",
		Niche:          "ai_tech",
		TargetAudience: "Software Developers",
		AutoOptimizeAI: true,
		ApplyLive:      false,
	}

	report, err := svc.UpdatePostMetadata(context.Background(), req)
	if err != nil {
		t.Fatalf("UpdatePostMetadata failed: %v", err)
	}

	if report.OptimizedTitle == "" {
		t.Errorf("expected non-empty optimized title")
	}

	if len(report.TitleCTRVariations) < 3 {
		t.Errorf("expected at least 3 CTR title variations, got %d", len(report.TitleCTRVariations))
	}

	if len(report.OptimizedTags) == 0 {
		t.Errorf("expected non-empty optimized tags")
	}

	// Verify all tags are valid (no hyphens or spaces)
	for _, tag := range report.OptimizedTags {
		if regexp.MustCompile(`[\s-]`).MatchString(tag) {
			t.Errorf("tag '%s' contains invalid whitespace or hyphen", tag)
		}
	}

	if report.PredictedImpact == "" {
		t.Errorf("expected predicted impact explanation")
	}

	// Verify UUID is never leaked into title
	if strings.Contains(report.OptimizedTitle, "yt_test_123") {
		t.Errorf("ID leaked into optimized title: %s", report.OptimizedTitle)
	}
}

func TestOptimizer_NoUUIDInTitleWhenExtractingFromDBOrEmpty(t *testing.T) {
	svc := NewService(nil, nil, nil, "", nil, nil)

	testUUID := "34c9624f-22f5-4ffe-a12e-943687c577b2"
	report, err := svc.UpdatePostMetadata(context.Background(), &UpdateMetadataRequest{
		PostID:         testUUID,
		Platform:       "instagram",
		Objective:      "viral_rehook",
		Niche:          "fitness",
		AutoOptimizeAI: true,
		ApplyLive:      false,
	})
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	if strings.Contains(report.OptimizedTitle, testUUID) {
		t.Fatalf("CRITICAL BUG: Raw database UUID leaked into title! Got: %s", report.OptimizedTitle)
	}

	for _, v := range report.TitleCTRVariations {
		if strings.Contains(v, testUUID) {
			t.Fatalf("CRITICAL BUG: Raw database UUID leaked into variation! Got: %s", v)
		}
	}
}

func TestOptimizer_DatabaseRecordUpdate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed initializing sqlmock: %v", err)
	}
	defer db.Close()

	testUserID := uuid.New().String()
	testPostID := uuid.New().String()

	svc := NewService(db, nil, nil, "", nil, nil)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE posts SET content = $1, metadata = $2, updated_at = $3 WHERE id = $4 AND user_id = $5")).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), testPostID, testUserID).
		WillReturnResult(sqlmock.NewResult(1, 1))

	report, err := svc.UpdatePostMetadata(context.Background(), &UpdateMetadataRequest{
		UserID:         testUserID,
		PostID:         testPostID,
		Platform:       "instagram",
		Objective:      "viral_rehook",
		Niche:          "fitness",
		AutoOptimizeAI: true,
		ApplyLive:      false,
	})

	if err != nil {
		t.Fatalf("UpdatePostMetadata failed: %v", err)
	}

	if !report.DatabaseRecordUpdated {
		t.Errorf("expected DatabaseRecordUpdated to be true")
	}
}

func TestOptimizer_YouTubeLiveUpdateFlow(t *testing.T) {
	testVideoID := "yt_vid_999"
	testUserID := uuid.New().String()

	// Mock YouTube API Server
	ytMockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/videos" {
			// Video details response
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"items": []map[string]interface{}{
					{
						"id": testVideoID,
						"snippet": map[string]interface{}{
							"title":       "Original Title",
							"description": "Original Description",
							"categoryId":  "28",
						},
						"statistics": map[string]interface{}{
							"viewCount": "1200",
							"likeCount": "50",
						},
					},
				},
			})
			return
		}
		if r.Method == http.MethodPut && r.URL.Path == "/videos" {
			// Video update response
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id": testVideoID,
				"snippet": map[string]interface{}{
					"title":       "Updated CTR Title",
					"description": "Updated Description",
					"categoryId":  "28",
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer ytMockServer.Close()

	ytClient := youtube.NewClient("yt_client_id", "yt_client_secret")
	ytClient.SetAPIBaseForTesting(ytMockServer.URL + "/videos")
	ytClient.SetTokenURLForTesting(ytMockServer.URL + "/token")

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock failed: %v", err)
	}
	defer db.Close()

	encKey := []byte("01234567890123456789012345678901")
	repo := database.NewRepository(db, encKey, nil)

	// Mock GetDecryptedPlatformConnection
	encAccess, _ := crypto.EncryptOAuthToken([]byte("mock_access_token"), encKey)
	encRefresh, _ := crypto.EncryptOAuthToken([]byte("mock_refresh_token"), encKey)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT encrypted_access_token, encrypted_refresh_token, token_expires_at, scopes, is_active FROM platform_connections")).
		WithArgs(testUserID, "youtube").
		WillReturnRows(sqlmock.NewRows([]string{"encrypted_access_token", "encrypted_refresh_token", "token_expires_at", "scopes", "is_active"}).
			AddRow(encAccess, encRefresh, time.Now().Add(time.Hour), models.StringArray{"https://www.googleapis.com/auth/youtube.upload"}, true))

	svc := NewService(db, repo, ytClient, "", nil, nil)

	report, err := svc.UpdatePostMetadata(context.Background(), &UpdateMetadataRequest{
		UserID:         testUserID,
		PostID:         testVideoID,
		Platform:       "youtube",
		Objective:      "ctr_boost",
		Niche:          "ai_tech",
		AutoOptimizeAI: true,
		ApplyLive:      true,
	})

	if err != nil {
		t.Fatalf("live YouTube update failed: %v", err)
	}

	if !report.OptimizationApplied {
		t.Errorf("expected OptimizationApplied to be true")
	}

	if report.LiveVideoAnalytics == nil || report.LiveVideoAnalytics.VideoID != testVideoID {
		t.Errorf("expected live video analytics for %s, got %+v", testVideoID, report.LiveVideoAnalytics)
	}
}
