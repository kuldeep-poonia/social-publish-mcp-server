// Package queue provides an encrypted, resilient Redis Stream message queue with exponential backoff and dead-letter handling.
package queue

import (
	"errors"
	"time"
)

// JobStatus represents the state of a queued publish job.
type JobStatus string

const (
	StatusPending    JobStatus = "pending"
	StatusProcessing JobStatus = "processing"
	StatusPublished  JobStatus = "published"
	StatusDeadLetter JobStatus = "dead_letter"
	StatusFailed     JobStatus = "failed"
)

// ErrorCategory classifies failures for intelligent retry vs. dead-letter routing.
type ErrorCategory string

const (
	ErrorCategoryTransientRateLimit       ErrorCategory = "transient_rate_limit"
	ErrorCategoryTransientServerError     ErrorCategory = "transient_server_error"
	ErrorCategoryTransientNetworkTimeout  ErrorCategory = "transient_network_timeout"
	ErrorCategoryPermanentClientError     ErrorCategory = "permanent_client_error"
	ErrorCategoryRetryLimitExhausted      ErrorCategory = "retry_limit_exhausted"
	ErrorCategoryPoisonMessageDeliveryCap ErrorCategory = "poison_message_delivery_exceeded"
)

// Common queue domain errors.
var (
	ErrQueueClosed                  = errors.New("queue is closed")
	ErrJobNotFound                 = errors.New("job not found in queue")
	ErrMaxDeliveryAttemptsExceeded = errors.New("max delivery attempts exceeded (poison message)")
	ErrMaxRetriesExhausted         = errors.New("maximum retry attempts exhausted")
)

// PublishJob represents a queued publishing task.
type PublishJob struct {
	ID                string        `json:"id"`
	PostID            string        `json:"post_id"`
	UserID            string        `json:"user_id"`
	Platform          string        `json:"platform"`
	Caption           string        `json:"caption"`
	MediaURLs         []string      `json:"media_urls,omitempty"`
	MediaPath         string        `json:"media_path,omitempty"`
	MediaData         []byte        `json:"media_data,omitempty"`
	MediaType         string        `json:"media_type,omitempty"`
	PrivacyStatus     string        `json:"privacy_status,omitempty"`
	IdempotencyKey    string        `json:"idempotency_key"`
	AttemptCount      int           `json:"attempt_count"`
	MaxRetries        int           `json:"max_retries"`
	NextRunAt         time.Time     `json:"next_run_at"`
	CreatedAt         time.Time     `json:"created_at"`
	LastErrorMessage  string        `json:"last_error_message,omitempty"`
	LastErrorCategory ErrorCategory `json:"last_error_category,omitempty"`
}

// RetryPolicy defines backoff parameters and delivery bounds.
type RetryPolicy struct {
	BaseBackoff   time.Duration // e.g. 500ms
	MaxBackoff    time.Duration // e.g. 30s
	MaxRetries    int           // e.g. 5
	MaxDeliveries int           // e.g. 5 (Poison message threshold)
}

// DefaultRetryPolicy returns standard production retry configuration.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		BaseBackoff:   500 * time.Millisecond,
		MaxBackoff:    30 * time.Second,
		MaxRetries:    5,
		MaxDeliveries: 5,
	}
}

// DeadLetterRecord holds structured diagnostics for a permanently failed job.
type DeadLetterRecord struct {
	JobID           string        `json:"job_id"`
	PostID          string        `json:"post_id"`
	UserID          string        `json:"user_id"`
	Platform        string        `json:"platform"`
	TotalAttempts   int           `json:"total_attempts"`
	FirstAttemptAt  time.Time     `json:"first_attempt_at"`
	FailedAt        time.Time     `json:"failed_at"`
	ErrorCategory   ErrorCategory `json:"error_category"`
	DiagnosticTrace string        `json:"diagnostic_trace"`
}
