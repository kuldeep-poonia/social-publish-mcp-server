// Package idempotency provides application-level idempotency protection,
// state machine transitions, stale crash recovery, and resumable session management.
package idempotency

import (
	"errors"
	"time"
)

// LockStatus represents the outcome of an idempotency lock acquisition attempt.
type LockStatus int

const (
	// LockAcquired indicates the caller won the lock and must execute the upstream API call.
	LockAcquired LockStatus = iota

	// LockCached indicates the post was already published and the cached platform ID is returned.
	LockCached

	// LockInFlight indicates another worker is actively processing the request (HTTP 409 Conflict).
	LockInFlight
)

// PostRecord represents the database row for an idempotent post.
type PostRecord struct {
	ID               string
	UserID           string
	Platform         string
	Status           string
	IdempotencyKey   string
	PlatformPostID   string
	UploadSessionURI string
	BytesUploaded    int64
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

// Common error definitions
var (
	ErrInFlightConflict = errors.New("idempotency: concurrent request in-flight for this key (HTTP 409)")
	ErrNotFound         = errors.New("idempotency: post record not found")
	ErrInvalidStatus    = errors.New("idempotency: invalid post status transition")
)
