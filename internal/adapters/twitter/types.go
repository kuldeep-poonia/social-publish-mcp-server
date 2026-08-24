// Package twitter provides the Twitter/X API v2 integration adapter for posting tweets, uploading media, and retrieving analytics.
package twitter

import (
	"encoding/json"
	"fmt"
	"time"
)

// Twitter API v2 Standard Endpoints
const (
	APIBaseURL          = "https://api.twitter.com/2"
	UploadBaseURL       = "https://upload.twitter.com/1.1"
	OAuthAuthorizeURL   = "https://twitter.com/i/oauth2/authorize"
	OAuthTokenURL       = "https://api.twitter.com/2/oauth2/token"
	OAuthRevokeURL      = "https://api.twitter.com/2/oauth2/revoke"
)

// Standard Twitter OAuth 2.0 Scopes required for MCP operation
var RequiredScopes = []string{
	"tweet.read",
	"tweet.write",
	"users.read",
	"offline.access",
}

// TweetCreateRequest defines the payload sent to POST /2/tweets.
type TweetCreateRequest struct {
	Text  string            `json:"text"`
	Media *TweetMediaObject `json:"media,omitempty"`
	Reply *TweetReplyObject `json:"reply,omitempty"`
}

// TweetMediaObject references uploaded media IDs.
type TweetMediaObject struct {
	MediaIDs []string `json:"media_ids"`
}

// TweetReplyObject allows posting in reply to an existing tweet.
type TweetReplyObject struct {
	InReplyToTweetID string `json:"in_reply_to_tweet_id"`
}

// TweetCreateResponse represents the successful response from POST /2/tweets.
type TweetCreateResponse struct {
	Data struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"data"`
}

// TweetPublicMetrics represents public engagement counts for a tweet.
type TweetPublicMetrics struct {
	ImpressionCount int `json:"impression_count"`
	LikeCount       int `json:"like_count"`
	RetweetCount    int `json:"retweet_count"`
	ReplyCount      int `json:"reply_count"`
	QuoteCount      int `json:"quote_count"`
	BookmarkCount   int `json:"bookmark_count"`
}

// TweetNonPublicMetrics represents private author analytics for a tweet.
type TweetNonPublicMetrics struct {
	ImpressionCount   int `json:"impression_count"`
	URLClicks         int `json:"url_link_clicks"`
	UserProfileClicks int `json:"user_profile_clicks"`
}

// TweetMetricsResponse represents the response from GET /2/tweets/:id.
type TweetMetricsResponse struct {
	Data struct {
		ID               string                 `json:"id"`
		Text             string                 `json:"text"`
		CreatedAt        time.Time              `json:"created_at"`
		PublicMetrics    TweetPublicMetrics     `json:"public_metrics"`
		NonPublicMetrics *TweetNonPublicMetrics `json:"non_public_metrics,omitempty"`
	} `json:"data"`
}

// OAuth2TokenResponse represents Twitter's token issuance response.
type OAuth2TokenResponse struct {
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	AccessToken  string `json:"access_token"`
	Scope        string `json:"scope"`
	RefreshToken string `json:"refresh_token"`
}

// TwitterAPIError encapsulates Twitter v2 structured error payloads.
type TwitterAPIError struct {
	Title      string          `json:"title"`
	Detail     string          `json:"detail"`
	Type       string          `json:"type"`
	Status     int             `json:"status"`
	Errors     json.RawMessage `json:"errors,omitempty"`
	StatusCode int             `json:"-"`
}

func (e *TwitterAPIError) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("twitter api error (%d): %s - %s", e.StatusCode, e.Title, e.Detail)
	}
	return fmt.Sprintf("twitter api error (%d): %s", e.StatusCode, e.Title)
}

// MediaUploadInitResponse represents response from POST upload.twitter.com INIT command.
type MediaUploadInitResponse struct {
	MediaID          int64  `json:"media_id"`
	MediaIDString    string `json:"media_id_string"`
	Size             int64  `json:"size"`
	ExpiresAfterSecs int    `json:"expires_after_secs"`
}

// MediaProcessingInfo represents asynchronous video processing status from Twitter.
type MediaProcessingInfo struct {
	State          string           `json:"state"` // "pending", "in_progress", "succeeded", "failed"
	CheckAfterSecs int              `json:"check_after_secs"`
	ProgressPct    int              `json:"progress_percent"`
	Error          *TwitterAPIError `json:"error,omitempty"`
}

// MediaUploadStatusResponse represents response from GET STATUS command during video upload.
type MediaUploadStatusResponse struct {
	MediaID          int64                `json:"media_id"`
	MediaIDString    string               `json:"media_id_string"`
	ProcessingInfo   *MediaProcessingInfo `json:"processing_info,omitempty"`
	ExpiresAfterSecs int                  `json:"expires_after_secs"`
}
