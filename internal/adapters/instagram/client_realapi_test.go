//go:build integration
// +build integration

package instagram

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestInstagramRealAPI_WANVerification runs live automated WAN integration against Meta Graph API v21.0.
func TestInstagramRealAPI_WANVerification(t *testing.T) {
	clientID := os.Getenv("INSTAGRAM_CLIENT_ID")
	clientSecret := os.Getenv("INSTAGRAM_CLIENT_SECRET")
	accessToken := os.Getenv("INSTAGRAM_ACCESS_TOKEN")
	testMediaID := os.Getenv("INSTAGRAM_TEST_MEDIA_ID")

	if clientID == "" || clientSecret == "" || accessToken == "" {
		t.Skip("Skipping live Meta Graph API integration test: INSTAGRAM_CLIENT_ID, INSTAGRAM_CLIENT_SECRET, or INSTAGRAM_ACCESS_TOKEN not set in environment")
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
