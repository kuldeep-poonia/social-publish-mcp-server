package twitter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

func TestTwitterClient_FullLifecycleAndRecovery(t *testing.T) {
	var tokenExchangeCalls int64
	var refreshCalls int64
	var tweetPostCalls int64
	var analyticsCalls int64
	var revokeCalls int64
	var rateLimitedCalls int64

	// Mock Twitter API v2 Server
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/oauth2/token" && r.Method == http.MethodPost:
			_ = r.ParseForm()
			grantType := r.FormValue("grant_type")
			if grantType == "authorization_code" {
				atomic.AddInt64(&tokenExchangeCalls, 1)
				_ = json.NewEncoder(w).Encode(OAuth2TokenResponse{
					TokenType:    "bearer",
					ExpiresIn:    7200,
					AccessToken:  "tw_access_token_initial",
					RefreshToken: "tw_refresh_token_initial",
					Scope:        "tweet.read tweet.write users.read offline.access",
				})
				return
			} else if grantType == "refresh_token" {
				atomic.AddInt64(&refreshCalls, 1)
				_ = json.NewEncoder(w).Encode(OAuth2TokenResponse{
					TokenType:    "bearer",
					ExpiresIn:    7200,
					AccessToken:  "tw_access_token_refreshed",
					RefreshToken: "tw_refresh_token_new",
					Scope:        "tweet.read tweet.write users.read offline.access",
				})
				return
			}

		case r.URL.Path == "/oauth2/revoke" && r.Method == http.MethodPost:
			atomic.AddInt64(&revokeCalls, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"revoked":true}`))
			return

		case r.URL.Path == "/tweets" && r.Method == http.MethodPost:
			authHeader := r.Header.Get("Authorization")
			atomic.AddInt64(&tweetPostCalls, 1)

			// Simulate 401 on initial expired token
			if authHeader == "Bearer tw_access_token_expired" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(TwitterAPIError{
					Title:  "Unauthorized",
					Detail: "Access token is expired",
					Status: 401,
				})
				return
			}

			// Simulate 429 on rate-limited token on first try
			if authHeader == "Bearer tw_access_token_ratelimited" {
				calls := atomic.AddInt64(&rateLimitedCalls, 1)
				if calls == 1 {
					w.Header().Set("x-rate-limit-reset", strconv.FormatInt(time.Now().Add(1*time.Second).Unix(), 10))
					w.WriteHeader(http.StatusTooManyRequests)
					_ = json.NewEncoder(w).Encode(TwitterAPIError{
						Title:  "Too Many Requests",
						Detail: "Rate limit exceeded",
						Status: 429,
					})
					return
				}
			}

			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(TweetCreateResponse{
				Data: struct {
					ID   string `json:"id"`
					Text string `json:"text"`
				}{
					ID:   "tweet_1234567890",
					Text: "Verified tweet content",
				},
			})
			return

		case stringsContains(r.URL.Path, "/tweets/tweet_1234567890") && r.Method == http.MethodGet:
			atomic.AddInt64(&analyticsCalls, 1)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(TweetMetricsResponse{
				Data: struct {
					ID               string                 `json:"id"`
					Text             string                 `json:"text"`
					CreatedAt        time.Time              `json:"created_at"`
					PublicMetrics    TweetPublicMetrics     `json:"public_metrics"`
					NonPublicMetrics *TweetNonPublicMetrics `json:"non_public_metrics,omitempty"`
				}{
					ID:        "tweet_1234567890",
					Text:      "Verified tweet content",
					CreatedAt: time.Now().UTC(),
					PublicMetrics: TweetPublicMetrics{
						ImpressionCount: 15420,
						LikeCount:       432,
						RetweetCount:    89,
						ReplyCount:      24,
						QuoteCount:      12,
						BookmarkCount:   58,
					},
				},
			})
			return
		}

		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	client := NewCustomClient(mockServer.Client(), mockServer.URL, mockServer.URL, "client_id_123", "client_secret_xyz")
	ctx := context.Background()

	t.Logf("================================================================================")
	t.Logf("     TWITTER CLIENT FULL LIFECYCLE & ERROR RECOVERY TEST SUITE                  ")
	t.Logf("================================================================================")

	// 1. OAuth 2.0 Token Exchange
	tokenResp, err := client.ExchangeOAuthToken(ctx, "auth_code_sample", "code_verifier_sample", "http://localhost/callback")
	if err != nil || tokenResp.AccessToken != "tw_access_token_initial" {
		t.Fatalf("OAuth token exchange failed: %v", err)
	}
	t.Logf("[1/5] OAuth 2.0 PKCE Token Exchange: PASS (Access Token: %s)", tokenResp.AccessToken)

	// 2. Tweet Creation
	postResp, err := client.PostTweet(ctx, tokenResp.AccessToken, &TweetCreateRequest{
		Text: "Hello from Social Publish MCP Server! 🚀",
	})
	if err != nil || postResp.Data.ID != "tweet_1234567890" {
		t.Fatalf("PostTweet failed: %v", err)
	}
	t.Logf("[2/5] Tweet Creation (POST /2/tweets): PASS (Tweet ID: %s)", postResp.Data.ID)

	// 3. Analytics Retrieval
	metricsResp, err := client.GetTweetAnalytics(ctx, tokenResp.AccessToken, "tweet_1234567890")
	if err != nil || metricsResp.Data.PublicMetrics.ImpressionCount != 15420 {
		t.Fatalf("GetTweetAnalytics failed: %v", err)
	}
	t.Logf("[3/5] Analytics Retrieval (GET /2/tweets/:id): PASS (Impressions: %d, Likes: %d)",
		metricsResp.Data.PublicMetrics.ImpressionCount, metricsResp.Data.PublicMetrics.LikeCount)

	// 4. Token Refresh on Expiry Simulation
	refreshResp, err := client.RefreshToken(ctx, tokenResp.RefreshToken)
	if err != nil || refreshResp.AccessToken != "tw_access_token_refreshed" {
		t.Fatalf("RefreshToken failed: %v", err)
	}
	t.Logf("[4/5] Token Refresh Flow: PASS (New Access Token: %s)", refreshResp.AccessToken)

	// 5. Upstream Revoke Flow
	err = client.RevokeToken(ctx, refreshResp.AccessToken)
	if err != nil {
		t.Fatalf("RevokeToken failed: %v", err)
	}
	t.Logf("[5/5] Token Revocation Flow: PASS (Revoked: true)")
	t.Logf("================================================================================")
}

func TestTwitterClient_RateLimit429Backoff(t *testing.T) {
	var attemptCount int64

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt64(&attemptCount, 1)
		w.Header().Set("Content-Type", "application/json")

		if count == 1 {
			// First attempt returns 429 with reset in 1 second
			w.Header().Set("x-rate-limit-reset", strconv.FormatInt(time.Now().Add(1*time.Second).Unix(), 10))
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(TwitterAPIError{
				Title:  "Too Many Requests",
				Detail: "Twitter rate limit threshold exceeded",
				Status: 429,
			})
			return
		}

		// Second attempt succeeds
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(TweetCreateResponse{
			Data: struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			}{
				ID:   "tweet_rate_limit_recovered",
				Text: "Recovered tweet content",
			},
		})
	}))
	defer mockServer.Close()

	client := NewCustomClient(mockServer.Client(), mockServer.URL, mockServer.URL, "test_client", "test_secret")
	start := time.Now()

	resp, err := client.PostTweet(context.Background(), "sample_token", &TweetCreateRequest{
		Text: "Rate limit backoff test",
	})
	if err != nil {
		t.Fatalf("PostTweet with backoff failed: %v", err)
	}

	elapsed := time.Since(start)
	t.Logf("=== 429 RATE LIMIT BACKOFF & RECOVERY RESULTS ===")
	t.Logf("Total Attempts Executed: %d", atomic.LoadInt64(&attemptCount))
	t.Logf("Elapsed Backoff Recovery Time: %v", elapsed)
	t.Logf("Created Tweet ID after backoff: %s", resp.Data.ID)

	if atomic.LoadInt64(&attemptCount) != 2 {
		t.Fatalf("expected exactly 2 attempts (1 failure + 1 backoff retry), got %d", attemptCount)
	}
}
