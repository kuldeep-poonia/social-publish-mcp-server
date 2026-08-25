package ratelimit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func getTestRedisClient(t *testing.T) *redis.Client {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping Redis rate limiter test: local Redis at localhost:6379 unreachable: %v", err)
	}
	return client
}

func TestRedisLimiter_BasicBurstAndRefill(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	key := fmt.Sprintf("test:ratelimit:basic:%s", uuid.New().String())

	// 5 RPS, burst of 3
	limiter, err := NewRedisTokenBucketLimiter(client, 5.0, 3.0, true)
	if err != nil {
		t.Fatalf("failed initializing limiter: %v", err)
	}

	// 1. Consume full burst capacity of 3
	for i := 0; i < 3; i++ {
		if !limiter.Allow(key) {
			t.Fatalf("expected request %d within burst to be allowed", i+1)
		}
	}

	// 2. 4th request immediately should be throttled (burst exhausted)
	if limiter.Allow(key) {
		t.Fatal("expected 4th immediate request to be throttled")
	}

	// 3. Wait 250ms (at 5 RPS, refills ~1.25 tokens)
	time.Sleep(250 * time.Millisecond)

	if !limiter.Allow(key) {
		t.Fatal("expected request after refill window to be allowed")
	}
}

func TestRedisLimiter_KeyIsolation(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	userA := FormatUserPlatformKey("user_A", "twitter")
	userB := FormatUserPlatformKey("user_B", "twitter")

	// 1 RPS, burst of 2
	limiter, err := NewRedisTokenBucketLimiter(client, 1.0, 2.0, true)
	if err != nil {
		t.Fatalf("failed initializing limiter: %v", err)
	}

	// Exhaust User A's bucket
	limiter.Allow(userA)
	limiter.Allow(userA)
	if limiter.Allow(userA) {
		t.Fatal("expected User A to be throttled")
	}

	// User B must still have full burst capacity
	if !limiter.Allow(userB) || !limiter.Allow(userB) {
		t.Fatal("expected User B to remain unaffected by User A's exhaustion (Cross-tenant isolation violation)")
	}
}
