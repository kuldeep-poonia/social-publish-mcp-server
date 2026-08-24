//go:build integration
// +build integration

package youtube

import (
	"context"
	"errors"
	"os"
	"sort"
	"testing"
	"time"
)

func TestYouTubeRealAPI_LiveWANTelemetryHarness(t *testing.T) {
	testAccessToken := os.Getenv("YOUTUBE_TEST_ACCESS_TOKEN")
	if testAccessToken == "" {
		t.Skip("Skipping live YouTube API integration harness: YOUTUBE_TEST_ACCESS_TOKEN not set.")
	}

	client := NewClient("live_client_id", "live_client_secret")
	ctx := context.Background()

	const numAttempts = 10
	latencies := make([]time.Duration, 0, numAttempts)
	var authErrors, notFoundErrors, successCount int

	startTotal := time.Now()

	for i := 0; i < numAttempts; i++ {
		startReq := time.Now()
		_, err := client.GetVideoAnalytics(ctx, testAccessToken, "dQw4w9WgXcQ") // Rick Astley - Never Gonna Give You Up
		duration := time.Since(startReq)
		latencies = append(latencies, duration)

		if err != nil {
			var apiErr *YouTubeAPIError
			if errors.As(err, &apiErr) && apiErr.StatusCode == 401 {
				authErrors++
			} else {
				notFoundErrors++
			}
		} else {
			successCount++
		}
	}

	totalElapsed := time.Since(startTotal)

	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	minLat := latencies[0]
	maxLat := latencies[len(latencies)-1]
	p50Lat := latencies[len(latencies)*50/100]
	p95Lat := latencies[len(latencies)*95/100]

	t.Logf("================================================================================")
	t.Logf("     REAL LIVE YOUTUBE API WAN TELEMETRY HARNESS (%d Real HTTP Calls)          ", numAttempts)
	t.Logf("================================================================================")
	t.Logf("Target Endpoint:            %s", YouTubeAPIBaseURL)
	t.Logf("Total Real Requests Sent:   %d", numAttempts)
	t.Logf("Total Elapsed Duration:     %v", totalElapsed)
	t.Logf("Successes (200 OK):         %d", successCount)
	t.Logf("Auth Errors (401):          %d", authErrors)
	t.Logf("Live WAN Latency — Min:     %.2f ms", float64(minLat.Microseconds())/1000.0)
	t.Logf("Live WAN Latency — p50:     %.2f ms", float64(p50Lat.Microseconds())/1000.0)
	t.Logf("Live WAN Latency — p95:     %.2f ms", float64(p95Lat.Microseconds())/1000.0)
	t.Logf("Live WAN Latency — Max:     %.2f ms", float64(maxLat.Microseconds())/1000.0)
	t.Logf("================================================================================")
}
