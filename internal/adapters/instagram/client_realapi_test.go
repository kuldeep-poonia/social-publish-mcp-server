//go:build integration
// +build integration

package instagram

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
)

// TestInstagramRealAPI_WANVerification runs live automated WAN integration against Meta Graph API v21.0.
func TestInstagramRealAPI_WANVerification(t *testing.T) {
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("failed loading config: %v", err)
	}

	clientID := cfg.InstagramClientID
	clientSecret := cfg.InstagramClientSecret
	accessToken := os.Getenv("INSTAGRAM_ACCESS_TOKEN")
	testMediaID := os.Getenv("INSTAGRAM_TEST_MEDIA_ID")
	if testMediaID == "" {
		testMediaID = "18106694998880108" // Newly published test post
	}

	if accessToken == "" {
		// Attempt to load decrypted token from Token Vault for test_user_1
		db, repo, _ := setupTestDB(t)
		if db != nil && repo != nil {
			defer db.Close()
			ctx := context.Background()
			user, uErr := repo.GetOrCreateUserByUsername(ctx, "test_user_1", "test_user_1@example.com")
			if uErr == nil && user != nil {
				decAccess, _, _, _, tErr := repo.GetDecryptedPlatformConnection(ctx, user.ID, "instagram")
				if tErr == nil && len(decAccess) > 0 {
					accessToken = string(decAccess)
				}
			}
		}
	}

	if clientID == "" || clientSecret == "" || accessToken == "" {
		t.Skip("Skipping live Meta Graph API integration test: INSTAGRAM_CLIENT_ID, INSTAGRAM_CLIENT_SECRET, or INSTAGRAM_ACCESS_TOKEN not available")
	}

	client := NewClient(clientID, clientSecret)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	t.Run("Live Discovery of Instagram Business Account", func(t *testing.T) {
		igAccount, pageToken, err := client.GetInstagramBusinessAccount(ctx, accessToken)
		if err != nil {
			t.Fatalf("Live business account discovery failed: %v", err)
		}
		if igAccount.ID == "" {
			t.Fatal("Discovered live Instagram Business account has empty ID")
		}
		t.Logf("Discovered Live Instagram Business Account: @%s (ID: %s, Page Token len: %d)",
			igAccount.Username, igAccount.ID, len(pageToken))
	})

	t.Run("Live Token Extension Check", func(t *testing.T) {
		tok, err := client.ExtendLongLivedToken(ctx, accessToken)
		if err != nil {
			t.Logf("Token extension notice (normal if already refreshed within 24h): %v", err)
		} else {
			t.Logf("Successfully verified live 60-day token extension (expires_in: %d seconds)", tok.ExpiresIn)
		}
	})

	if testMediaID != "" {
		t.Run("Live Media Insights Fetch", func(t *testing.T) {
			metrics, err := client.GetMediaInsights(ctx, testMediaID, accessToken)
			if err != nil {
				t.Fatalf("Live insights query failed: %v", err)
			}
			t.Logf("Live Media Metrics for ID %s: Impressions=%d, Reach=%d, Likes=%d, Comments=%d",
				testMediaID, metrics.Impressions, metrics.Reach, metrics.Likes, metrics.Comments)
		})
	}
}
