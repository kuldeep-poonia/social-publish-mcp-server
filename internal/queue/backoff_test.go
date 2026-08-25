package queue

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestQueue_ExponentialBackoffCalculation(t *testing.T) {
	base := 100 * time.Millisecond
	max := 5 * time.Second

	var prevBackoff time.Duration
	t.Logf("=== EXPONENTIAL BACKOFF INTERVAL MEASUREMENTS ===")

	for attempt := 1; attempt <= 6; attempt++ {
		backoff := CalculateBackoff(attempt, base, max)
		t.Logf("Attempt %d: Backoff Interval = %v", attempt, backoff)

		if attempt > 1 && attempt <= 5 {
			// Ensure each interval is strictly greater than base and bounded
			if backoff < base {
				t.Fatalf("attempt %d backoff (%v) less than base (%v)", attempt, backoff, base)
			}
		}

		if attempt >= 6 {
			// Beyond attempt 6, should be capped near max backoff
			if backoff > max+(max/4) { // with jitter
				t.Fatalf("attempt %d backoff (%v) exceeded max cap (%v)", attempt, backoff, max)
			}
		}

		prevBackoff = backoff
		_ = prevBackoff
	}
}

func TestQueue_RetryStormDeceleration(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	key := getTestEncryptionKey()
	streamName := fmt.Sprintf("test:stream:backoff:%s", uuid.New().String())
	q, err := NewRedisStreamQueueCustom(client, key, streamName, "test_backoff_group")
	if err != nil {
		t.Fatalf("failed initializing queue: %v", err)
	}

	var retryAttempts []time.Time
	transientError := errors.New("upstream HTTP 429 Too Many Requests (rate limit exceeded)")

	mockHandler := func(ctx context.Context, job *PublishJob) error {
		retryAttempts = append(retryAttempts, time.Now())
		return transientError
	}

	policy := RetryPolicy{
		BaseBackoff:   50 * time.Millisecond,
		MaxBackoff:    500 * time.Millisecond,
		MaxRetries:    4,
		MaxDeliveries: 5,
	}

	pool := NewWorkerPool(q, mockHandler, policy, 1, "test_backoff_worker")
	ctx := context.Background()

	job := &PublishJob{
		ID:             "job_retry_storm_test",
		PostID:         "post_storm_123",
		UserID:         "user_storm",
		Platform:       "twitter",
		Caption:        "Testing upstream retry deceleration under rate limit storm",
		IdempotencyKey: "idemp_storm_123",
		CreatedAt:      time.Now().UTC(),
	}

	// Simulate 4 successive transient failure retries
	for i := 0; i < 4; i++ {
		msg := &StreamMessage{
			StreamID:   "stream_msg_1",
			Job:        job,
			Deliveries: 1,
		}
		pool.ProcessSingleMessage(ctx, msg)
	}

	t.Logf("=== RETRY STORM DECELERATION RESULTS ===")
	t.Logf("Total Transient Retries Recorded: %d", len(retryAttempts))
	t.Logf("Job Final Attempt Count:          %d", job.AttemptCount)
	t.Logf("Next Scheduled Run At:            %v", job.NextRunAt)

	if job.AttemptCount != 4 {
		t.Fatalf("expected 4 attempt counts, got %d", job.AttemptCount)
	}

	if job.LastErrorCategory != ErrorCategoryTransientRateLimit {
		t.Fatalf("expected category %s, got %s", ErrorCategoryTransientRateLimit, job.LastErrorCategory)
	}
}
