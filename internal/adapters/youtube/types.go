// Package youtube provides the Google YouTube Data API v3 client,
// resumable chunked video upload engine, daily quota tracking, and publishing service.
package youtube

import (
	"fmt"
	"time"
)

const (
	// Google OAuth 2.0 PKCE Endpoints
	OAuthAuthorizeURL = "https://accounts.google.com/o/oauth2/v2/auth"
	OAuthTokenURL     = "https://oauth2.googleapis.com/token"
	OAuthRevokeURL    = "https://oauth2.googleapis.com/revoke"

	// YouTube Data API v3 Endpoints
	YouTubeUploadBaseURL = "https://www.googleapis.com/upload/youtube/v3/videos"
	YouTubeAPIBaseURL    = "https://www.googleapis.com/youtube/v3/videos"

	// Quota Cost Constants (Google YouTube Data API default daily budget: 10,000 units)
	QuotaDailyBudget = 10000
	QuotaUploadCost  = 1600
	QuotaReadCost    = 1
	QuotaUpdateCost  = 50

	// Resumable Upload Chunk Settings (Google requires multiples of 256 KB)
	UploadChunkSize = 8 * 1024 * 1024 // 8 MB default chunk
)

// RequiredScopes defines minimal required Google scopes for YouTube video publishing & analytics.
var RequiredScopes = []string{
	"https://www.googleapis.com/auth/youtube.upload",
	"https://www.googleapis.com/auth/youtube.readonly",
	"https://www.googleapis.com/auth/userinfo.profile",
}

// TokenResponse represents the OAuth 2.0 token response from Google.
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Scope        string `json:"scope"`
}

// VideoSnippet defines YouTube video metadata.
type VideoSnippet struct {
	Title       string   `json:"title"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	CategoryID  string   `json:"categoryId,omitempty"`
}

// VideoStatus defines video privacy and licensing state.
type VideoStatus struct {
	PrivacyStatus           string `json:"privacyStatus"` // "public", "private", "unlisted"
	SelfDeclaredMadeForKids bool   `json:"selfDeclaredMadeForKids,omitempty"`
	Embeddable              bool   `json:"embeddable,omitempty"`
}

// VideoResource represents the complete YouTube video object for creation/retrieval.
type VideoResource struct {
	ID         string           `json:"id,omitempty"`
	Snippet    *VideoSnippet    `json:"snippet,omitempty"`
	Status     *VideoStatus     `json:"status,omitempty"`
	Statistics *VideoStatistics `json:"statistics,omitempty"`
}

// VideoStatistics holds engagement metrics returned by YouTube Data API.
type VideoStatistics struct {
	ViewCount    string `json:"viewCount"`
	LikeCount    string `json:"likeCount"`
	CommentCount string `json:"commentCount"`
}

// VideoAnalyticsMetrics represents parsed numerical engagement metrics.
type VideoAnalyticsMetrics struct {
	VideoID       string    `json:"video_id"`
	Title         string    `json:"title"`
	ViewCount     int64     `json:"view_count"`
	LikeCount     int64     `json:"like_count"`
	CommentCount  int64     `json:"comment_count"`
	PrivacyStatus string    `json:"privacy_status"`
	RetrievedAt   time.Time `json:"retrieved_at"`
}

// ResumableUploadSession tracks the state of an active resumable upload.
type ResumableUploadSession struct {
	SessionURI    string
	TotalBytes    int64
	BytesUploaded int64
	IsCompleted   bool
	VideoID       string
}

// YouTubeAPIError encapsulates structured error responses from Google APIs.
type YouTubeAPIError struct {
	StatusCode int
	Message    string
	Reason     string
}

func (e *YouTubeAPIError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("youtube api error (%d): %s (reason: %s)", e.StatusCode, e.Message, e.Reason)
	}
	return fmt.Sprintf("youtube api error (%d): %s", e.StatusCode, e.Message)
}
