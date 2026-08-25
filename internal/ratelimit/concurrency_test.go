package ratelimit

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestRedisLimiter_ConcurrencyRaceBoundary(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	key := fmt.Sprintf("test:ratelimit:concurrency:%s", uuid.New().String())

	// 0.1 RPS (negligible refill during test), Burst capacity = exactly 20 tokens
	const burstCapacity = 20.0
	const concurrentRequests = 100

	limiter, err := NewRedisTokenBucketLimiter(client, 0.1, burstCapacity, true)
	if err != nil {
		t.Fatalf("failed initializing limiter: %v", err)
	}

	var (
		wg           sync.WaitGroup
		startBarrier = make(chan struct{})
		allowedCount int64
		blockedCount int64
	)

	wg.Add(concurrentRequests)
	for i := 0; i < concurrentRequests; i++ {
		go func() {
			defer wg.Done()
			<-startBarrier // Synchronize simultaneous dispatch across all 100 goroutines

			if limiter.Allow(key) {
				atomic.AddInt64(&allowedCount, 1)
			} else {
				atomic.AddInt64(&blockedCount, 1)
			}
		}()
	}

	// Release all 100 goroutines simultaneously
	close(startBarrier)
	wg.Wait()

	t.Logf("=== REDIS CONCURRENT RATE LIMIT BOUNDARY RESULTS ===")
	t.Logf("Total Concurrent Requests: %d", concurrentRequests)
	t.Logf("Burst Limit Configured:    %d", int(burstCapacity))
	t.Logf("Actual Allowed:            %d", allowedCount)
	t.Logf("Actual Throttled (429):    %d", blockedCount)

	if allowedCount > int64(burstCapacity) {
		t.Fatalf("RACE CONDITION DETECTED: Allowed %d requests exceeding burst limit of %d (Over-limit leak: %d)",
			allowedCount, int(burstCapacity), allowedCount-int64(burstCapacity))
	}

	if allowedCount+blockedCount != int64(concurrentRequests) {
		t.Fatalf("unaccounted requests: allowed(%d) + blocked(%d) != %d", allowedCount, blockedCount, concurrentRequests)
	}
}
