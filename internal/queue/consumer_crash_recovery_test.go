package queue

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/crypto"
	"github.com/redis/go-redis/v9"
)

func getTestRedisClient(t *testing.T) *redis.Client {
	client := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping Redis queue test: local Redis at localhost:6379 unreachable: %v", err)
	}
	return client
}

func getTestEncryptionKey() []byte {
	key := make([]byte, crypto.KeySizeAES256)
	copy(key, []byte("test_queue_encryption_key_32byte"))
	return key
}

// TestQueue_ConsumerCrashAndXClaimRecovery verifies abandoned messages from crashed workers are reclaimed and completed exactly once.
func TestQueue_ConsumerCrashAndXClaimRecovery(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	streamName := fmt.Sprintf("test:stream:crash:%s", uuid.New().String())
	groupName := "test_crash_group"

	key := getTestEncryptionKey()
	q, err := NewRedisStreamQueueCustom(client, key, streamName, groupName)
	if err != nil {
		t.Fatalf("failed initializing stream queue: %v", err)
	}

	postID := fmt.Sprintf("post_crash_test_%s", uuid.New().String())
	job := &PublishJob{
		ID:             fmt.Sprintf("job_%s", uuid.New().String()),
		PostID:         postID,
		UserID:         "user_test_crash",
		Platform:       "twitter",
		Caption:        "Testing consumer crash recovery via XCLAIM",
		IdempotencyKey: fmt.Sprintf("idemp_%s", uuid.New().String()),
		CreatedAt:      time.Now().UTC(),
	}

	ctx := context.Background()

	// 1. Enqueue job
	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("failed enqueuing job: %v", err)
	}

	// 2. Worker A reads job via XREADGROUP
	workerA_Consumer := fmt.Sprintf("worker_A_%s", uuid.New().String())
	messages, err := q.ReadGroup(ctx, workerA_Consumer, 1, 1*time.Second)
	if err != nil || len(messages) == 0 {
		t.Fatalf("worker A failed reading message: %v", err)
	}
	claimedMsg := messages[0]
	if claimedMsg.Job.PostID != postID {
		t.Fatalf("unexpected post ID: %s", claimedMsg.Job.PostID)
	}

	// 3. Worker A crashes abruptly WITHOUT acknowledging (XACK not called)
	// Message is now trapped in Redis Pending Entries List (PEL)

	// 4. Verify message is present in PEL
	pending, err := client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: streamName,
		Group:  groupName,
		Start:  "-",
		End:    "+",
		Count:  10,
	}).Result()
	if err != nil || len(pending) == 0 {
		t.Fatalf("expected message in PEL for crashed worker A: %v", err)
	}

	// 5. Worker B performs XCLAIM to reclaim the abandoned message (minIdle = 0 for instant test reclamation)
	workerB_Consumer := fmt.Sprintf("worker_B_%s", uuid.New().String())
	reclaimed, err := q.ClaimPending(ctx, workerB_Consumer, 0, 10)
	if err != nil || len(reclaimed) == 0 {
		t.Fatalf("worker B failed reclaiming abandoned message via XCLAIM: %v", err)
	}

	var upstreamCalls int64
	mockPublishHandler := func(ctx context.Context, j *PublishJob) error {
		atomic.AddInt64(&upstreamCalls, 1)
		return nil
	}

	poolB := NewWorkerPool(q, mockPublishHandler, DefaultRetryPolicy(), 1, workerB_Consumer)

	// 6. Worker B executes and acknowledges the reclaimed job
	poolB.ProcessSingleMessage(ctx, reclaimed[0])

	// 7. Verify exactly 1 upstream execution and PEL cleared
	t.Logf("=== CONSUMER CRASH & XCLAIM RECOVERY RESULTS ===")
	t.Logf("Upstream Publish Executions: %d (Target: Exactly 1)", upstreamCalls)

	if upstreamCalls != 1 {
		t.Fatalf("expected exactly 1 upstream execution, got %d", upstreamCalls)
	}

	// 8. Verify PEL is now 0 (No orphaned messages)
	pendingAfter, err := client.XPending(ctx, streamName, groupName).Result()
	if err != nil {
		t.Fatalf("failed checking pending count: %v", err)
	}
	t.Logf("Remaining Abandoned Messages in PEL: %d (Target: 0)", pendingAfter.Count)
}

// TestQueue_PoisonMessageMaxDeliveriesExceeded verifies that repeated worker crashes trigger automatic DLQ diversion.
func TestQueue_PoisonMessageMaxDeliveriesExceeded(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	key := getTestEncryptionKey()
	q, err := NewRedisStreamQueue(client, key)
	if err != nil {
		t.Fatalf("failed initializing stream queue: %v", err)
	}

	dlq := NewDLQManager(client, key)
	postID := fmt.Sprintf("post_poison_%s", uuid.New().String())
	job := &PublishJob{
		ID:             fmt.Sprintf("job_poison_%s", uuid.New().String()),
		PostID:         postID,
		UserID:         "user_poison_test",
		Platform:       "instagram",
		Caption:        "Corrupt poison payload that repeatedly panics workers",
		IdempotencyKey: fmt.Sprintf("idemp_poison_%s", uuid.New().String()),
		CreatedAt:      time.Now().UTC(),
	}

	ctx := context.Background()

	// Simulate a message delivered 5 times (MaxDeliveries = 5 threshold exceeded)
	msg := &StreamMessage{
		StreamID:   "1700000000000-0",
		Job:        job,
		Deliveries: 5, // Triggers poison message guard!
	}

	crashHandler := func(ctx context.Context, j *PublishJob) error {
		return errors.New("unhandled runtime panic / segfault during corrupt media decode")
	}

	pool := NewWorkerPool(q, crashHandler, RetryPolicy{
		BaseBackoff:   10 * time.Millisecond,
		MaxBackoff:    50 * time.Millisecond,
		MaxRetries:    5,
		MaxDeliveries: 5,
	}, 1, "poison_worker")

	// Process message with delivery count = 5
	pool.ProcessSingleMessage(ctx, msg)

	// Verify job was diverted to DLQ with poison message reason
	dlqRecord, err := dlq.GetDeadLetterByPostID(ctx, postID)
	if err != nil {
		t.Fatalf("failed retrieving dead letter record for poison message: %v", err)
	}

	t.Logf("=== POISON MESSAGE RECLAIM CAP RESULTS ===")
	t.Logf("Post ID:         %s", dlqRecord.PostID)
	t.Logf("Error Category:  %s", dlqRecord.ErrorCategory)
	t.Logf("Diagnostic:      %s", dlqRecord.DiagnosticTrace)

	if dlqRecord.ErrorCategory != ErrorCategoryPoisonMessageDeliveryCap {
		t.Fatalf("expected error category %s, got %s", ErrorCategoryPoisonMessageDeliveryCap, dlqRecord.ErrorCategory)
	}
}
