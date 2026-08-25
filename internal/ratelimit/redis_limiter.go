// Package ratelimit provides distributed Redis-backed and in-memory token-bucket rate limiting.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/redis/go-redis/v9"
)

// ErrRateLimitServiceUnavailable is returned when Redis is unreachable and fail-closed is active.
var ErrRateLimitServiceUnavailable = errors.New("rate limiter service unavailable (failed closed)")

// RedisTokenBucketLimiter implements distributed atomic rate limiting via Redis Lua scripting.
type RedisTokenBucketLimiter struct {
	client     *redis.Client
	rate       float64 // Tokens replenished per second
	burst      float64 // Maximum token bucket capacity
	failClosed bool    // If true, reject requests when Redis is unreachable
	luaSHA     string  // Cached SHA1 hash for Redis EVALSHA
}

// TokenBucketLuaScript executes atomic sliding-window token replenishment and deduction in Redis.
const TokenBucketLuaScript = `
local key = KEYS[1]
local rate = tonumber(ARGV[1])
local capacity = tonumber(ARGV[2])
local now = tonumber(ARGV[3])
local requested = tonumber(ARGV[4])
local ttl = tonumber(ARGV[5])

local data = redis.call('HMGET', key, 'tokens', 'last_updated')
local tokens = tonumber(data[1])
local last_updated = tonumber(data[2])

if not tokens or not last_updated then
    tokens = capacity - requested
    last_updated = now
    redis.call('HMSET', key, 'tokens', tokens, 'last_updated', last_updated)
    redis.call('EXPIRE', key, ttl)
    return {1, string.format("%.4f", tokens)}
end

local elapsed = (now - last_updated) / 1000000.0
if elapsed < 0 then elapsed = 0 end

tokens = math.min(capacity, tokens + (elapsed * rate))
last_updated = now

if tokens >= requested then
    tokens = tokens - requested
    redis.call('HMSET', key, 'tokens', tokens, 'last_updated', last_updated)
    redis.call('EXPIRE', key, ttl)
    return {1, string.format("%.4f", tokens)}
else
    redis.call('HMSET', key, 'tokens', tokens, 'last_updated', last_updated)
    redis.call('EXPIRE', key, ttl)
    return {0, string.format("%.4f", tokens)}
end
`

// NewRedisTokenBucketLimiter initializes a distributed Redis rate limiter.
func NewRedisTokenBucketLimiter(client *redis.Client, ratePerSecond, burstCapacity float64, failClosed bool) (*RedisTokenBucketLimiter, error) {
	if client == nil {
		return nil, errors.New("redis client cannot be nil")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	sha, err := client.ScriptLoad(ctx, TokenBucketLuaScript).Result()
	if err != nil {
		// If Redis is unreachable during startup and fail-closed is active, return wrapped error
		if failClosed {
			return nil, fmt.Errorf("failed loading rate limit Lua script into Redis: %w", err)
		}
	}

	return &RedisTokenBucketLimiter{
		client:     client,
		rate:       ratePerSecond,
		burst:      burstCapacity,
		failClosed: failClosed,
		luaSHA:     sha,
	}, nil
}

// Allow checks if 1 token is available for the given key. Satisfies the Limiter interface.
func (r *RedisTokenBucketLimiter) Allow(key string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()

	allowed, err := r.AllowN(ctx, key, 1.0)
	if err != nil {
		return !r.failClosed
	}
	return allowed
}

// AllowN evaluates whether n tokens are available under the configured rate and burst capacity.
func (r *RedisTokenBucketLimiter) AllowN(ctx context.Context, key string, tokens float64) (bool, error) {
	if r.client == nil {
		if r.failClosed {
			return false, ErrRateLimitServiceUnavailable
		}
		return true, nil
	}

	nowMicros := time.Now().UTC().UnixNano() / 1000
	ttlSeconds := int(math.Ceil(r.burst/r.rate)) + 60
	if ttlSeconds < 60 {
		ttlSeconds = 60
	}

	var res interface{}
	var err error

	// Try EVALSHA first for speed; fallback to EVAL if script flushed
	if r.luaSHA != "" {
		res, err = r.client.EvalSha(ctx, r.luaSHA, []string{key}, r.rate, r.burst, nowMicros, tokens, ttlSeconds).Result()
	}
	if err != nil || r.luaSHA == "" {
		res, err = r.client.Eval(ctx, TokenBucketLuaScript, []string{key}, r.rate, r.burst, nowMicros, tokens, ttlSeconds).Result()
	}

	if err != nil {
		if r.failClosed {
			return false, fmt.Errorf("%w: %v", ErrRateLimitServiceUnavailable, err)
		}
		// In fail-open mode, permit request on error
		return true, nil
	}

	slice, ok := res.([]interface{})
	if !ok || len(slice) < 1 {
		if r.failClosed {
			return false, errors.New("invalid rate limit script response from redis")
		}
		return true, nil
	}

	allowedVal, ok := slice[0].(int64)
	if !ok {
		return false, nil
	}

	return allowedVal == 1, nil
}

// Key formatters for hierarchical distributed rate limiting
func FormatUserPlatformKey(userID, platform string) string {
	return fmt.Sprintf("ratelimit:user:%s:%s", userID, platform)
}

func FormatGlobalPlatformKey(platform string) string {
	return fmt.Sprintf("ratelimit:platform:%s", platform)
}

func FormatClientIPKey(ip string) string {
	return fmt.Sprintf("ratelimit:ip:%s", ip)
}
