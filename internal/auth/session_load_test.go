package auth

import (
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestSessionIssuance_LoadConcurrency(t *testing.T) {
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")

	// Target concurrency and rate scenarios: 100 req/s, 500 req/s, 1000 req/s
	scenarios := []struct {
		name          string
		targetRateRPS int
		totalRequests int
	}{
		{name: "Load_100_RPS", targetRateRPS: 100, totalRequests: 200},
		{name: "Load_500_RPS", targetRateRPS: 500, totalRequests: 500},
		{name: "Load_1000_RPS", targetRateRPS: 1000, totalRequests: 1000},
	}

	t.Logf("================================================================================")
	t.Logf("           HARDCORE SESSION ISSUANCE CONCURRENCY & LATENCY LOAD TEST             ")
	t.Logf("================================================================================")

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			latencies := make([]float64, sc.totalRequests)
			errorCount := 0
			var mu sync.Mutex

			intervalBetweenRequests := time.Second / time.Duration(sc.targetRateRPS)
			ticker := time.NewTicker(intervalBetweenRequests)
			defer ticker.Stop()

			var wg sync.WaitGroup
			startOverall := time.Now()

			for i := 0; i < sc.totalRequests; i++ {
				<-ticker.C
				wg.Add(1)

				go func(index int) {
					defer wg.Done()
					opStart := time.Now()

					userID := fmt.Sprintf("usr_load_%d", index)
					_, err := IssueSessionTokens(userID, "user", secret)
					duration := time.Since(opStart)
					durationMs := float64(duration.Microseconds()) / 1000.0

					mu.Lock()
					if err != nil {
						errorCount++
					} else {
						latencies[index] = durationMs
					}
					mu.Unlock()
				}(i)
			}

			wg.Wait()
			totalElapsed := time.Since(startOverall)

			// Calculate latency distribution (p50, p95, p99)
			validLatencies := make([]float64, 0, sc.totalRequests-errorCount)
			for _, l := range latencies {
				if l > 0 {
					validLatencies = append(validLatencies, l)
				}
			}

			sort.Float64s(validLatencies)
			var p50, p95, p99, maxLat float64
			if len(validLatencies) > 0 {
				p50 = validLatencies[int(float64(len(validLatencies))*0.50)]
				p95 = validLatencies[int(float64(len(validLatencies))*0.95)]
				p99 = validLatencies[int(float64(len(validLatencies))*0.99)]
				maxLat = validLatencies[len(validLatencies)-1]
			}

			actualRPS := float64(sc.totalRequests) / totalElapsed.Seconds()
			errorRate := (float64(errorCount) / float64(sc.totalRequests)) * 100.0

			t.Logf("[%s] Requests: %d | Elapsed: %v | Actual RPS: %.1f", sc.name, sc.totalRequests, totalElapsed, actualRPS)
			t.Logf("[%s] Errors: %d (Error Rate: %.2f%%)", sc.name, errorCount, errorRate)
			t.Logf("[%s] Latency Distribution -> p50: %.3f ms | p95: %.3f ms | p99: %.3f ms | max: %.3f ms", sc.name, p50, p95, p99, maxLat)

			if errorCount != 0 {
				t.Fatalf("expected 0 errors under %s, got %d", sc.name, errorCount)
			}
		})
	}
	t.Logf("================================================================================")
}
