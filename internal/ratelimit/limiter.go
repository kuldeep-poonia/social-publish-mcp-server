// Package ratelimit provides distributed and in-memory token-bucket rate limiting.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter defines the rate limiter interface.
type Limiter interface {
	Allow(key string) bool
}

// TokenBucketLimiter implements an in-memory sliding token bucket per client key.
type TokenBucketLimiter struct {
	mu         sync.Mutex
	rate       float64 // Tokens added per second
	burst      float64 // Maximum bucket capacity
	buckets    map[string]*bucket
	lastClean  time.Time
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// NewTokenBucketLimiter initializes a token bucket limiter with specified refill rate (RPS) and burst capacity.
func NewTokenBucketLimiter(ratePerSecond, burstCapacity float64) *TokenBucketLimiter {
	return &TokenBucketLimiter{
		rate:      ratePerSecond,
		burst:     burstCapacity,
		buckets:   make(map[string]*bucket),
		lastClean: time.Now().UTC(),
	}
}

// Allow evaluates whether a request from the given key is permitted. Fails closed (returns false) if over budget.
func (l *TokenBucketLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now().UTC()

	// Periodic cleanup of stale keys every 5 minutes
	if now.Sub(l.lastClean) > 5*time.Minute {
		for k, b := range l.buckets {
			if now.Sub(b.lastRefill) > 10*time.Minute {
				delete(l.buckets, k)
			}
		}
		l.lastClean = now
	}

	b, exists := l.buckets[key]
	if !exists {
		l.buckets[key] = &bucket{
			tokens:     l.burst - 1.0,
			lastRefill: now,
		}
		return true
	}

	// Refill tokens based on elapsed time
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.lastRefill = now

	if b.tokens >= 1.0 {
		b.tokens -= 1.0
		return true
	}

	// Rate limit exceeded — fail closed
	return false
}
