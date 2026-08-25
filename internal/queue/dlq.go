// Package queue provides dead-letter queue management and structured failure diagnostics.
package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/crypto"
	"github.com/redis/go-redis/v9"
)

// DLQManager provides inspection and diagnostic querying for permanently failed jobs.
type DLQManager struct {
	client        *redis.Client
	encryptionKey []byte
}

// NewDLQManager initializes a Dead-Letter Queue manager.
func NewDLQManager(client *redis.Client, queueEncryptionKey []byte) *DLQManager {
	return &DLQManager{
		client:        client,
		encryptionKey: queueEncryptionKey,
	}
}

// ListRecentDeadLetters retrieves the most recent permanently failed dead-letter records.
func (d *DLQManager) ListRecentDeadLetters(ctx context.Context, count int64) ([]*DeadLetterRecord, error) {
	if d.client == nil {
		return nil, errors.New("redis client cannot be nil")
	}
	if count <= 0 {
		count = 50
	}

	messages, err := d.client.XRevRangeN(ctx, StreamDLQ, "+", "-", count).Result()
	if err != nil {
		return nil, fmt.Errorf("failed querying dead letter stream: %w", err)
	}

	var records []*DeadLetterRecord
	for _, msg := range messages {
		record, decErr := d.decryptRecord(msg.Values)
		if decErr != nil {
			continue
		}
		records = append(records, record)
	}

	return records, nil
}

// GetDeadLetterByPostID searches the dead letter queue for a record matching the post ID.
func (d *DLQManager) GetDeadLetterByPostID(ctx context.Context, postID string) (*DeadLetterRecord, error) {
	if postID == "" {
		return nil, errors.New("postID cannot be empty")
	}

	records, err := d.ListRecentDeadLetters(ctx, 100)
	if err != nil {
		return nil, err
	}

	for _, rec := range records {
		if rec.PostID == postID {
			return rec, nil
		}
	}

	return nil, ErrJobNotFound
}

func (d *DLQManager) decryptRecord(values map[string]interface{}) (*DeadLetterRecord, error) {
	rawCiphertext, ok := values["payload"].(string)
	if !ok || rawCiphertext == "" {
		return nil, errors.New("missing encrypted payload in dead letter message")
	}

	plaintext, err := crypto.DecryptAESGCM([]byte(rawCiphertext), d.encryptionKey)
	if err != nil {
		return nil, fmt.Errorf("failed decrypting dead letter record: %w", err)
	}

	var record DeadLetterRecord
	if err := json.Unmarshal(plaintext, &record); err != nil {
		return nil, fmt.Errorf("failed unmarshaling dead letter json: %w", err)
	}

	return &record, nil
}
