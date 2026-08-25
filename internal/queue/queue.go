// Package queue provides an encrypted, resilient Redis Stream message queue.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/crypto"
	"github.com/redis/go-redis/v9"
)

const (
	StreamPublish = "social_mcp:stream:publish"
	StreamDLQ     = "social_mcp:stream:dlq"
	ConsumerGroup = "social_mcp_worker_group"
)

// StreamMessage represents a decrypted job read from a Redis Stream.
type StreamMessage struct {
	StreamID   string
	Job        *PublishJob
	Deliveries int64
}

// RedisStreamQueue manages encrypted Redis Stream queue operations.
type RedisStreamQueue struct {
	client        *redis.Client
	encryptionKey []byte
	streamName    string
	consumerGroup string
}

// NewRedisStreamQueue initializes a RedisStreamQueue with default stream and consumer group names.
func NewRedisStreamQueue(client *redis.Client, queueEncryptionKey []byte) (*RedisStreamQueue, error) {
	return NewRedisStreamQueueCustom(client, queueEncryptionKey, StreamPublish, ConsumerGroup)
}

// NewRedisStreamQueueCustom initializes a RedisStreamQueue with custom stream and consumer group names.
func NewRedisStreamQueueCustom(client *redis.Client, queueEncryptionKey []byte, streamName, consumerGroup string) (*RedisStreamQueue, error) {
	if client == nil {
		return nil, errors.New("redis client cannot be nil")
	}
	if len(queueEncryptionKey) != crypto.KeySizeAES256 {
		return nil, fmt.Errorf("queueEncryptionKey must be exactly %d bytes for AES-256-GCM, got %d", crypto.KeySizeAES256, len(queueEncryptionKey))
	}
	if streamName == "" {
		streamName = StreamPublish
	}
	if consumerGroup == "" {
		consumerGroup = ConsumerGroup
	}

	q := &RedisStreamQueue{
		client:        client,
		encryptionKey: queueEncryptionKey,
		streamName:    streamName,
		consumerGroup: consumerGroup,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Ensure stream and consumer group exist (MKSTREAM flag creates stream if absent)
	err := client.XGroupCreateMkStream(ctx, streamName, consumerGroup, "$").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return nil, fmt.Errorf("failed creating consumer group %s on %s: %w", consumerGroup, streamName, err)
	}

	return q, nil
}

// Enqueue encrypts and writes a PublishJob into the Redis Stream.
func (q *RedisStreamQueue) Enqueue(ctx context.Context, job *PublishJob) error {
	if job == nil {
		return errors.New("job cannot be nil")
	}

	jobBytes, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed serializing publish job: %w", err)
	}

	// Encrypt payload at rest with AES-256-GCM
	ciphertext, err := crypto.EncryptAESGCM(jobBytes, q.encryptionKey)
	if err != nil {
		return fmt.Errorf("failed encrypting queue payload: %w", err)
	}

	values := map[string]interface{}{
		"post_id":   job.PostID,
		"user_id":   job.UserID,
		"platform":  job.Platform,
		"payload":   ciphertext,
		"queued_at": time.Now().UTC().Unix(),
	}

	_, err = q.client.XAdd(ctx, &redis.XAddArgs{
		Stream: q.streamName,
		Values: values,
	}).Result()
	if err != nil {
		return fmt.Errorf("failed writing job to Redis Stream: %w", err)
	}

	return nil
}

// ReadGroup reads unread messages for a specific consumer in the consumer group.
func (q *RedisStreamQueue) ReadGroup(ctx context.Context, consumerName string, count int64, block time.Duration) ([]*StreamMessage, error) {
	streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    q.consumerGroup,
		Consumer: consumerName,
		Streams:  []string{q.streamName, ">"},
		Count:    count,
		Block:    block,
	}).Result()

	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, nil
		}
		return nil, err
	}

	var results []*StreamMessage
	for _, stream := range streams {
		for _, msg := range stream.Messages {
			job, decErr := q.decryptJob(msg.Values)
			if decErr != nil {
				continue
			}
			results = append(results, &StreamMessage{
				StreamID:   msg.ID,
				Job:        job,
				Deliveries: 1,
			})
		}
	}

	return results, nil
}

// ClaimPending inspects Pending Entries List (PEL) and claims abandoned messages from crashed workers.
func (q *RedisStreamQueue) ClaimPending(ctx context.Context, consumerName string, minIdle time.Duration, count int64) ([]*StreamMessage, error) {
	// 1. Inspect Pending Entries List
	pending, err := q.client.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: q.streamName,
		Group:  q.consumerGroup,
		Start:  "-",
		End:    "+",
		Count:  count,
	}).Result()

	if err != nil || len(pending) == 0 {
		return nil, err
	}

	var messageIDsToClaim []string
	deliveryCounts := make(map[string]int64)

	for _, p := range pending {
		if p.Idle >= minIdle {
			messageIDsToClaim = append(messageIDsToClaim, p.ID)
			deliveryCounts[p.ID] = p.RetryCount
		}
	}

	if len(messageIDsToClaim) == 0 {
		return nil, nil
	}

	// 2. Claim pending messages for consumerName
	claimed, err := q.client.XClaim(ctx, &redis.XClaimArgs{
		Stream:   q.streamName,
		Group:    q.consumerGroup,
		Consumer: consumerName,
		MinIdle:  minIdle,
		Messages: messageIDsToClaim,
	}).Result()

	if err != nil {
		return nil, fmt.Errorf("failed claiming pending messages: %w", err)
	}

	var results []*StreamMessage
	for _, msg := range claimed {
		job, decErr := q.decryptJob(msg.Values)
		if decErr != nil {
			continue
		}
		results = append(results, &StreamMessage{
			StreamID:   msg.ID,
			Job:        job,
			Deliveries: deliveryCounts[msg.ID],
		})
	}

	return results, nil
}

// Acknowledge acknowledges completion of a message and removes it from the stream.
func (q *RedisStreamQueue) Acknowledge(ctx context.Context, streamID string) error {
	pipe := q.client.TxPipeline()
	pipe.XAck(ctx, q.streamName, q.consumerGroup, streamID)
	pipe.XDel(ctx, q.streamName, streamID)
	_, err := pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("failed acknowledging stream message %s: %w", streamID, err)
	}
	return nil
}

// EnqueueDeadLetter encrypts and pushes a permanently failed job into the Dead-Letter Queue.
func (q *RedisStreamQueue) EnqueueDeadLetter(ctx context.Context, record *DeadLetterRecord) error {
	if record == nil {
		return errors.New("dead letter record cannot be nil")
	}

	recordBytes, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("failed serializing dead letter record: %w", err)
	}

	ciphertext, err := crypto.EncryptAESGCM(recordBytes, q.encryptionKey)
	if err != nil {
		return fmt.Errorf("failed encrypting dead letter record: %w", err)
	}

	values := map[string]interface{}{
		"job_id":         record.JobID,
		"post_id":        record.PostID,
		"user_id":        record.UserID,
		"platform":       record.Platform,
		"error_category": string(record.ErrorCategory),
		"payload":        ciphertext,
		"failed_at":      time.Now().UTC().Unix(),
	}

	_, err = q.client.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamDLQ,
		Values: values,
	}).Result()
	if err != nil {
		return fmt.Errorf("failed writing to dead letter queue: %w", err)
	}

	return nil
}

func (q *RedisStreamQueue) decryptJob(values map[string]interface{}) (*PublishJob, error) {
	rawCiphertext, ok := values["payload"].(string)
	if !ok || rawCiphertext == "" {
		return nil, errors.New("missing encrypted payload in stream message")
	}

	plaintext, err := crypto.DecryptAESGCM([]byte(rawCiphertext), q.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("failed decrypting queue payload: %w", err)
	}

	var job PublishJob
	if err := json.Unmarshal(plaintext, &job); err != nil {
		return nil, fmt.Errorf("failed unmarshaling job json: %w", err)
	}

	return &job, nil
}
