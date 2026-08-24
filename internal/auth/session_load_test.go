package auth

import (
	"fmt"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkSessionIssuance measures the raw single-thread and parallel throughput of token generation.
func BenchmarkSessionIssuance_SingleThread(b *testing.B) {
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := IssueSessionTokens("usr_bench_123", "user", secret)
		if err != nil {
			b.Fatalf("benchmark failed: %v", err)
		}
	}
}

func BenchmarkSessionIssuance_Parallel(b *testing.B) {
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := IssueSessionTokens("usr_bench_parallel", "user", secret)
			if err != nil {
				b.Fatalf("parallel benchmark failed: %v", err)
			}
		}
	})
}

func TestSessionIssuance_LoadConcurrency(t *testing.T) {
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")

	// Warm up runtime, cryptographic buffers, and memory allocator
	for i := 0; i < 500; i++ {
		_, _ = IssueSessionTokens("usr_warmup", "user", secret)
	}
	runtime.GC()

	scenarios := []struct {
		name          string
		targetRateRPS int
		totalRequests int
	}{
		{name: "Load_100_RPS", targetRateRPS: 100, totalRequests: 300},
		{name: "Load_500_RPS", targetRateRPS: 500, totalRequests: 1000},
		{name: "Load_1000_RPS", targetRateRPS: 1000, totalRequests: 2000},
	}

	t.Logf("================================================================================")
	t.Logf("           HIGH-PRECISION SESSION ISSUANCE LOAD & CONCURRENCY TEST              ")
	t.Logf("================================================================================")

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			latenciesMs := make([]float64, sc.totalRequests)
			var errorCount int64

			interval := time.Second / time.Duration(sc.targetRateRPS)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			var wg sync.WaitGroup
			startOverall := time.Now()

			for i := 0; i < sc.totalRequests; i++ {
				<-ticker.C
				wg.Add(1)

				go func(idx int) {
					defer wg.Done()
					opStart := time.Now()

					userID := fmt.Sprintf("usr_load_%d", idx)
					_, err := IssueSessionTokens(userID, "user", secret)
					duration := time.Since(opStart)

					// High-precision nanosecond conversion to milliseconds float
					latenciesMs[idx] = float64(duration.Nanoseconds()) / 1_000_000.0

					if err != nil {
						atomic.AddInt64(&errorCount, 1)
					}
				}(i)
			}

			wg.Wait()
			totalElapsed := time.Since(startOverall)

			// Sort full latency distribution without dropping fast sub-microsecond requests
			sortedLatencies := make([]float64, len(latenciesMs))
			copy(sortedLatencies, latenciesMs)
			sort.Float64s(sortedLatencies)

			n := len(sortedLatencies)
			p50 := sortedLatencies[int(float64(n)*0.50)]
			p90 := sortedLatencies[int(float64(n)*0.90)]
			p95 := sortedLatencies[int(float64(n)*0.95)]
			p99 := sortedLatencies[int(float64(n)*0.99)]
			maxLat := sortedLatencies[n-1]
			minLat := sortedLatencies[0]

			actualRPS := float64(sc.totalRequests) / totalElapsed.Seconds()
			errorRate := (float64(errorCount) / float64(sc.totalRequests)) * 100.0

			t.Logf("[%s] Target: %d RPS | Total Requests: %d | Actual RPS: %.1f | Elapsed: %v",
				sc.name, sc.targetRateRPS, sc.totalRequests, actualRPS, totalElapsed)
			t.Logf("[%s] Errors: %d (Error Rate: %.2f%%)", sc.name, errorCount, errorRate)
			t.Logf("[%s] Latency -> min: %.3fms | p50: %.3fms | p90: %.3fms | p95: %.3fms | p99: %.3fms | max: %.3fms",
				sc.name, minLat, p50, p90, p95, p99, maxLat)

			if errorCount != 0 {
				t.Fatalf("expected 0 errors under %s, got %d", sc.name, errorCount)
			}
		})
	}
	t.Logf("================================================================================")
}
