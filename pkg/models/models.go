// Package models defines core domain entities and data transfer objects for the MCP server.
package models

import (
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrInvalidPlatform is returned when an unsupported platform is specified.
	ErrInvalidPlatform = errors.New("models: unsupported social platform")
	// ErrEmptyContent is returned when a post has no content or media.
	ErrEmptyContent = errors.New("models: post content or media cannot be empty")
)

// Supported social platforms
const (
	PlatformTwitter   = "twitter"
	PlatformYouTube   = "youtube"
	PlatformInstagram = "instagram"
)

// Post statuses
const (
	PostStatusDraft     = "draft"
	PostStatusScheduled = "scheduled"
	PostStatusPublished = "published"
	PostStatusFailed    = "failed"
)

// StringArray implements database/sql driver.Valuer and sql.Scanner for PostgreSQL TEXT[] columns.
type StringArray []string

// Value serializes StringArray into a PostgreSQL array literal.
func (a StringArray) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "{}", nil
	}
	escaped := make([]string, len(a))
	for i, s := range a {
		escaped[i] = fmt.Sprintf(`"%s"`, strings.ReplaceAll(s, `"`, `\"`))
	}
	return fmt.Sprintf("{%s}", strings.Join(escaped, ",")), nil
}

// Scan deserializes PostgreSQL array literals into StringArray.
func (a *StringArray) Scan(src interface{}) error {
	if src == nil {
		*a = []string{}
		return nil
	}

	var str string
	switch v := src.(type) {
	case []byte:
		str = string(v)
	case string:
		str = v
	default:
		return fmt.Errorf("unsupported Scan type for StringArray: %T", src)
	}

	str = strings.Trim(str, "{}")
	if str == "" {
		*a = []string{}
		return nil
	}

	parts := strings.Split(str, ",")
	res := make([]string, len(parts))
	for i, p := range parts {
		res[i] = strings.Trim(p, `"`)
	}
	*a = res
	return nil
}

// User represents a registered user account.
type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Username  string    `json:"username"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// PlatformConnection represents an OAuth credential link to a social platform with encrypted tokens at rest.
type PlatformConnection struct {
	ID                    string      `json:"id"`
	UserID                string      `json:"user_id"`
	Platform              string      `json:"platform"`
	EncryptedAccessToken  []byte      `json:"-"` // Never serialized to JSON
	EncryptedRefreshToken []byte      `json:"-"` // Never serialized to JSON
	TokenExpiresAt        time.Time   `json:"token_expires_at"`
	Scopes                StringArray `json:"scopes"`
	IsActive              bool        `json:"is_active"`
	CreatedAt             time.Time   `json:"created_at"`
	UpdatedAt             time.Time   `json:"updated_at"`
}

// Post represents a social media post across supported platforms.
type Post struct {
	ID             string          `json:"id"`
	UserID         string          `json:"user_id"`
	Platform       string          `json:"platform"`
	PlatformPostID string          `json:"platform_post_id,omitempty"`
	Content        string          `json:"content"`
	MediaURLs      StringArray     `json:"media_urls,omitempty"`
	MediaPath      string          `json:"media_path,omitempty"`
	MediaType      string          `json:"media_type,omitempty"`
	ImagePrompt    string          `json:"image_prompt,omitempty"`
	Status         string          `json:"status"`
	ScheduledAt    *time.Time      `json:"scheduled_at,omitempty"`
	PublishedAt    *time.Time      `json:"published_at,omitempty"`
	IdempotencyKey string          `json:"idempotency_key"`
	Metadata       json.RawMessage `json:"metadata,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

// AnalyticsSnapshot represents a point-in-time metrics capture for a published post.
type AnalyticsSnapshot struct {
	ID         string          `json:"id"`
	PostID     string          `json:"post_id"`
	UserID     string          `json:"user_id"`
	Platform   string          `json:"platform"`
	Metrics    json.RawMessage `json:"metrics"`
	CapturedAt time.Time       `json:"captured_at"`
}

// AuditLog represents a structured, immutable security and operational audit trail entry.
type AuditLog struct {
	ID           string          `json:"id"`
	UserID       string          `json:"user_id"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	Metadata     json.RawMessage `json:"metadata"`
	IPAddress    string          `json:"ip_address"`
	CreatedAt    time.Time       `json:"created_at"`
}

// IsValidPlatform verifies if the platform identifier matches supported platforms.
func IsValidPlatform(platform string) bool {
	switch strings.ToLower(platform) {
	case PlatformTwitter, PlatformYouTube, PlatformInstagram:
		return true
	default:
		return false
	}
}
