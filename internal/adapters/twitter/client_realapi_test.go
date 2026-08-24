//go:build integration

package twitter

import (
	"context"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestTwitter_RealLiveAPI_50Attempts runs 50 real requests directly against the live Twitter API v2 endpoints.
// To run: go test -v -tags=integration -run TestTwitter_RealLiveAPI_50Attempts ./internal/adapters/twitter/
func TestTwitter_RealLiveAPI_50Attempts(t *testing.T) {
	bearerToken := os.Getenv("TWITTER_TEST_BEARER_TOKEN")
	accessToken := os.Getenv("TWITTER_TEST_ACCESS_TOKEN")

	tokenToUse := accessToken
	if tokenToUse == "" {
		tokenToUse = bearerToken
	}

	if tokenToUse == "" {
		t.Skip(`
================================================================================
  LIVE TWITTER API INTEGRATION TEST SKIPPED (NO CREDENTIALS CONFIGURED)
  To execute real live API calls against Twitter API v2:
  Set TWITTER_TEST_ACCESS_TOKEN or TWITTER_TEST_BEARER_TOKEN in your environment:
    $env:TWITTER_TEST_ACCESS_TOKEN="your_real_twitter_token"
    go test -v -tags=integration ./internal/adapters/twitter/
================================================================================`)
		return
	}

	client := NewClient(os.Getenv("TWITTER_CLIENT_ID"), os.Getenv("TWITTER_CLIENT_SECRET"))
	ctx := context.Background()

	const totalAttempts = 50
	latenciesMs := make([]float64, totalAttempts)

	var successCount int64
	var rateLimitCount int64
	var authErrorCount int64
	var networkErrorCount int64

	t.Logf("================================================================================")
	t.Logf("     LIVE TWITTER API V2 REAL TELEMETRY HARNESS (50 REAL REQUESTS)              ")
	t.Logf("     Target: https://api.twitter.com/2/tweets                                   ")
	t.Logf("================================================================================")

	var wg sync.WaitGroup
	startOverall := time.Now()

	for i := 0; i < totalAttempts; i++ {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()
			opStart := time.Now()

			// Query tweet or verify token validity on live API
			tweetReq := &TweetCreateRequest{
				Text: fmt.Sprintf("Automated validation probe #%d at %d [Social-MCP]", idx, time.Now().UnixNano()),
			}

			reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			_, err := client.PostTweet(reqCtx, tokenToUse, tweetReq)
			duration := time.Since(opStart)
			latenciesMs[idx] = float64(duration.Nanoseconds()) / 1_000_000.0

			if err == nil {
				atomic.AddInt64(&successCount, 1)
				return
			}

			var apiErr *TwitterAPIError
			if errorsAs(err, &apiErr) {
				if apiErr.StatusCode == 429 {
					atomic.AddInt64(&rateLimitCount, 1)
				} else if apiErr.StatusCode == 401 || apiErr.StatusCode == 403 {
					atomic.AddInt64(&authErrorCount, 1)
				} else {
					atomic.AddInt64(&networkErrorCount, 1)
				}
			} else {
				atomic.AddInt64(&networkErrorCount, 1)
			}
		}(i)

		// Throttle live dispatch slightly to avoid immediate burst ban
		time.Sleep(100 * time.Millisecond)
	}

	wg.Wait()
	totalElapsed := time.Since(startOverall)

	sortedLatencies := make([]float64, len(latenciesMs))
	copy(sortedLatencies, latenciesMs)
	sort.Float64s(sortedLatencies)

	n := len(sortedLatencies)
	p50 := sortedLatencies[int(float64(n)*0.50)]
	p95 := sortedLatencies[int(float64(n)*0.95)]
	p99 := sortedLatencies[int(float64(n)*0.99)]

	t.Logf("=== LIVE TWITTER API V2 TELEMETRY METRICS ===")
	t.Logf("Total Attempts Sent: %d | Total Elapsed: %v", totalAttempts, totalElapsed)
	t.Logf("Successes: %d | Rate-Limited (429): %d | Auth Errors: %d | Network/Other: %d",
		successCount, rateLimitCount, authErrorCount, networkErrorCount)
	t.Logf("Latency Distribution -> min: %.2fms | p50: %.2fms | p95: %.2fms | p99: %.2fms | max: %.2fms",
		sortedLatencies[0], p50, p95, p99, sortedLatencies[n-1])
	t.Logf("================================================================================")
}

func errorsAs(err error, target interface{}) bool {
	if err == nil {
		return false
	}
	t, ok := target.(**TwitterAPIError)
	if !ok {
		return false
	}
	apiErr, ok := err.(*TwitterAPIError)
	if ok {
		*t = apiErr
		return true
	}
	return false
}
