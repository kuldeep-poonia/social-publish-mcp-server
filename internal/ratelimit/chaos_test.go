package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestRedisLimiter_ChaosDisconnectionFailClosed(t *testing.T) {
	// 1. Initialize limiter with a broken/disconnected Redis target (unreachable port)
	deadClient := redis.NewClient(&redis.Options{
		Addr:        "127.0.0.1:59999", // Deliberately dead port
		DialTimeout: 50 * time.Millisecond,
		ReadTimeout: 50 * time.Millisecond,
	})
	defer deadClient.Close()

	failClosedLimiter := &RedisTokenBucketLimiter{
		client:     deadClient,
		rate:       10.0,
		burst:      20.0,
		failClosed: true,
		luaSHA:     "",
	}

	const loadCount = 100
	var (
		wg           sync.WaitGroup
		blockedCount int64
		leakCount    int64
	)

	start := time.Now()

	wg.Add(loadCount)
	for i := 0; i < loadCount; i++ {
		go func(idx int) {
			defer wg.Done()
			key := fmt.Sprintf("chaos:test:%d", idx)

			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			allowed, err := failClosedLimiter.AllowN(ctx, key, 1.0)
			if !allowed && err != nil {
				atomic.AddInt64(&blockedCount, 1)
			} else if allowed {
				atomic.AddInt64(&leakCount, 1)
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	t.Logf("=== REDIS CHAOS / DISCONNECTION FAIL-CLOSED RESULTS ===")
	t.Logf("Total Requests Dispatched During Redis Outage: %d", loadCount)
	t.Logf("Total Blocked (Fail-Closed Enforcement):      %d/%d (100.00%%)", blockedCount, loadCount)
	t.Logf("Unmetered Requests Leaked (Fail-Open Leaks):  %d", leakCount)
	t.Logf("Total Outage Handling Time:                    %v", duration)

	if leakCount != 0 {
		t.Fatalf("SECURITY VIOLATION: %d unmetered requests leaked through during Redis outage (Fail-Closed requirement failed)", leakCount)
	}

	if blockedCount != int64(loadCount) {
		t.Fatalf("expected 100%% fail-closed rejection (%d), got %d", loadCount, blockedCount)
	}

	// 2. Verify Reconnection Recovery: Once connected to healthy Redis, limiter recovers
	healthyClient := getTestRedisClient(t)
	defer healthyClient.Close()

	recoveryLimiter, err := NewRedisTokenBucketLimiter(healthyClient, 10.0, 10.0, true)
	if err != nil {
		t.Fatalf("failed initializing healthy limiter: %v", err)
	}

	recoveryStart := time.Now()
	recoveryKey := fmt.Sprintf("chaos:recovery:%s", uuid.New().String())
	if !recoveryLimiter.Allow(recoveryKey) {
		t.Fatal("expected limiter to allow valid request after Redis connection restored")
	}
	recoveryLatency := time.Since(recoveryStart)
	t.Logf("Healthy Service Recovery Latency:              %v", recoveryLatency)
}
