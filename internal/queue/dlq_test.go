package queue

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestQueue_DeadLetterQueueAndPayloadEncryption(t *testing.T) {
	client := getTestRedisClient(t)
	defer client.Close()

	key := getTestEncryptionKey()
	q, err := NewRedisStreamQueue(client, key)
	if err != nil {
		t.Fatalf("failed initializing queue: %v", err)
	}

	dlq := NewDLQManager(client, key)
	postID := fmt.Sprintf("post_dlq_%s", uuid.New().String())
	jobID := fmt.Sprintf("job_dlq_%s", uuid.New().String())

	// 1. Permanent Client Error (HTTP 400 Bad Dimensions)
	permanentErr := errors.New("meta graph api error 400: image aspect ratio 3.50 is outside supported range")

	job := &PublishJob{
		ID:             jobID,
		PostID:         postID,
		UserID:         "user_dlq_test",
		Platform:       "instagram",
		Caption:        "Post with invalid dimensions that must be routed directly to DLQ",
		IdempotencyKey: fmt.Sprintf("idemp_dlq_%s", uuid.New().String()),
		CreatedAt:      time.Now().UTC(),
	}

	failHandler := func(ctx context.Context, j *PublishJob) error {
		return permanentErr
	}

	pool := NewWorkerPool(q, failHandler, DefaultRetryPolicy(), 1, "dlq_worker")
	ctx := context.Background()

	// Process message — permanent error should immediately route to DLQ
	msg := &StreamMessage{
		StreamID:   "stream_msg_dlq_1",
		Job:        job,
		Deliveries: 1,
	}
	pool.ProcessSingleMessage(ctx, msg)

	// 2. Query Raw Redis Stream to verify AES-256-GCM encryption at rest
	rawMessages, err := client.XRevRangeN(ctx, StreamDLQ, "+", "-", 1).Result()
	if err != nil || len(rawMessages) == 0 {
		t.Fatalf("expected message in Redis DLQ stream: %v", err)
	}

	rawValues := rawMessages[0].Values
	rawCiphertext, ok := rawValues["payload"].(string)
	if !ok || rawCiphertext == "" {
		t.Fatal("expected payload field in raw Redis DLQ message")
	}

	// Verify raw ciphertext is NOT plaintext JSON
	if len(rawCiphertext) < 16 || rawCiphertext[0] == '{' {
		t.Fatalf("SECURITY VIOLATION: Queue payload is stored unencrypted in Redis: %s", rawCiphertext)
	}

	t.Logf("=== QUEUE ENCRYPTED PAYLOAD AT REST VERIFICATION ===")
	t.Logf("Raw Ciphertext (AES-256-GCM): %s (len: %d bytes)", rawCiphertext[:20]+"...", len(rawCiphertext))

	// 3. Retrieve and Decrypt via DLQManager
	record, err := dlq.GetDeadLetterByPostID(ctx, postID)
	if err != nil {
		t.Fatalf("failed retrieving dead letter record: %v", err)
	}

	t.Logf("=== DEAD-LETTER QUEUE DIAGNOSTIC RETRIEVAL ===")
	t.Logf("Job ID:          %s", record.JobID)
	t.Logf("Post ID:         %s", record.PostID)
	t.Logf("Platform:        %s", record.Platform)
	t.Logf("Error Category:  %s", record.ErrorCategory)
	t.Logf("Diagnostic:      %s", record.DiagnosticTrace)
	t.Logf("Total Attempts:  %d", record.TotalAttempts)

	if record.ErrorCategory != ErrorCategoryPermanentClientError {
		t.Errorf("expected ErrorCategory %s, got %s", ErrorCategoryPermanentClientError, record.ErrorCategory)
	}
	if record.PostID != postID {
		t.Errorf("expected post ID %s, got %s", postID, record.PostID)
	}
}
