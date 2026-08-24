package ratelimit

import (
	"testing"
	"time"
)

func TestTokenBucketLimiter_BurstAndRejection(t *testing.T) {
	// Rate: 10 tokens/sec, Burst: 5 tokens
	limiter := NewTokenBucketLimiter(10.0, 5.0)
	clientKey := "192.168.1.100"

	// 1. Initial 5 requests (within burst) must be allowed
	for i := 0; i < 5; i++ {
		if !limiter.Allow(clientKey) {
			t.Fatalf("request %d within burst was unexpectedly rejected", i+1)
		}
	}

	// 2. 6th immediate request must be rejected (fail-closed)
	if limiter.Allow(clientKey) {
		t.Fatal("request exceeding burst capacity was unexpectedly allowed!")
	}

	// 3. Different client key has independent bucket
	if !limiter.Allow("192.168.1.101") {
		t.Fatal("separate client key was unexpectedly throttled")
	}

	// 4. Wait for refill (0.2s = 2 tokens refilled at 10 tokens/sec)
	time.Sleep(250 * time.Millisecond)

	if !limiter.Allow(clientKey) {
		t.Fatal("request after token refill was unexpectedly rejected")
	}
}
