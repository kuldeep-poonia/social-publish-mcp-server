package telemetry

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestTelemetry_MockUpstreamLoadBenchmark executes 500 concurrent simulated publish operations
// against isolated mock HTTP upstream servers, recording p50/p95/p99 latency, error rates, and memory telemetry.
func TestTelemetry_MockUpstreamLoadBenchmark(t *testing.T) {
	// 1. Initialize Mock Upstream Servers for Twitter, YouTube, and Instagram
	var twitterCalls, youtubeCalls, instagramCalls int64

	mockTwitter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&twitterCalls, 1)
		time.Sleep(time.Duration(5+rand.Intn(15)) * time.Millisecond) // Simulated upstream latency
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":{"id":"1234567890","text":"mock tweet"}}`))
	}))
	defer mockTwitter.Close()

	mockYouTube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&youtubeCalls, 1)
		time.Sleep(time.Duration(10+rand.Intn(20)) * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"yt_mock_video_id","status":{"uploadStatus":"uploaded"}}`))
	}))
	defer mockYouTube.Close()

	mockInstagram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&instagramCalls, 1)
		time.Sleep(time.Duration(8+rand.Intn(18)) * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"ig_mock_container_123"}`))
	}))
	defer mockInstagram.Close()

	registry := NewTelemetryRegistry()

	const totalRequests = 500
	const maxConcurrency = 50

	var (
		wg           sync.WaitGroup
		semaphore    = make(chan struct{}, maxConcurrency)
		latenciesMu  sync.Mutex
		latencies    []time.Duration
		successCount int64
		errorCount   int64
	)

	// Capture initial memory profile
	var memBefore runtime.MemStats
	runtime.ReadMemStats(&memBefore)
	start := time.Now()

	t.Logf("=== STARTING 500-REQUEST HIGH-CONCURRENCY MOCK LOAD BENCHMARK ===")
	t.Logf("Target Mock Endpoints: Twitter (%s), YouTube (%s), Instagram (%s)", mockTwitter.URL, mockYouTube.URL, mockInstagram.URL)

	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			reqStart := time.Now()
			platform := ""
			var targetURL string

			switch idx % 3 {
			case 0:
				platform = "twitter"
				targetURL = mockTwitter.URL
			case 1:
				platform = "youtube"
				targetURL = mockYouTube.URL
			case 2:
				platform = "instagram"
				targetURL = mockInstagram.URL
			}

			// Execute HTTP request to mock upstream
			req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, targetURL, nil)
			resp, err := http.DefaultClient.Do(req)
			reqDuration := time.Since(reqStart)

			latenciesMu.Lock()
			latencies = append(latencies, reqDuration)
			latenciesMu.Unlock()

			statusCode := "500"
			if err == nil && resp != nil {
				statusCode = fmt.Sprintf("%d", resp.StatusCode)
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					atomic.AddInt64(&successCount, 1)
				} else {
					atomic.AddInt64(&errorCount, 1)
				}
			} else {
				atomic.AddInt64(&errorCount, 1)
			}

			// Record Prometheus Metric
			registry.ObserveRequest("POST", platform, statusCode, "load_test_client", reqDuration)
		}(i)
	}

	wg.Wait()
	totalDuration := time.Since(start)

	// Capture final memory profile
	var memAfter runtime.MemStats
	runtime.ReadMemStats(&memAfter)
	allocMB := float64(memAfter.Alloc-memBefore.Alloc) / (1024 * 1024)

	// Calculate Percentiles
	sort.Slice(latencies, func(i, j int) bool {
		return latencies[i] < latencies[j]
	})

	p50 := latencies[int(float64(len(latencies))*0.50)]
	p90 := latencies[int(float64(len(latencies))*0.90)]
	p95 := latencies[int(float64(len(latencies))*0.95)]
	p99 := latencies[int(float64(len(latencies))*0.99)]

	rps := float64(totalRequests) / totalDuration.Seconds()

	t.Logf("=== 500-REQUEST CONCURRENT BENCHMARK RESULTS ===")
	t.Logf("Total Requests Dispatched:   %d", totalRequests)
	t.Logf("Total Success (200 OK):      %d (%.2f%%)", successCount, float64(successCount)/totalRequests*100)
	t.Logf("Total Errors:                %d", errorCount)
	t.Logf("Total Benchmark Duration:    %v", totalDuration)
	t.Logf("Throughput (RPS):            %.2f req/s", rps)
	t.Logf("Latency p50 (Median):        %v", p50)
	t.Logf("Latency p90:                 %v", p90)
	t.Logf("Latency p95:                 %v", p95)
	t.Logf("Latency p99:                 %v", p99)
	t.Logf("Heap Memory Delta:           %.2f MB", allocMB)
	t.Logf("Mock Upstream Breakdown:     Twitter=%d, YouTube=%d, Instagram=%d",
		atomic.LoadInt64(&twitterCalls), atomic.LoadInt64(&youtubeCalls), atomic.LoadInt64(&instagramCalls))

	if successCount != int64(totalRequests) {
		t.Fatalf("expected 100%% success on mock benchmark (%d), got %d", totalRequests, successCount)
	}

	if p99 > 500*time.Millisecond {
		t.Fatalf("p99 latency (%v) exceeded 500ms benchmark threshold", p99)
	}
}
