// Package instagram implements the Meta Graph API v21.0 adapter for Instagram Business & Creator publishing and analytics.
package instagram

import (
	"errors"
	"fmt"
	"time"
)

// Meta Graph API Endpoints & Configuration
const (
	MetaGraphAPIBaseURL   = "https://graph.facebook.com/v21.0"
	MetaOAuthDialogURL    = "https://www.facebook.com/v21.0/dialog/oauth"
	MetaTokenExchangeURL  = "https://graph.facebook.com/v21.0/oauth/access_token"

	// Container Polling Thresholds
	MaxContainerPollAttempts = 30
	ContainerPollInitialWait = 2 * time.Second
	ContainerPollMaxWait     = 10 * time.Second
	ContainerPollTimeout     = 120 * time.Second

	// Token Renewal Threshold (Extend 60-day token when <= 7 days remain)
	TokenProactiveRenewalThreshold = 7 * 24 * time.Hour
)

// Container Status Constants
const (
	ContainerStatusInProgress = "IN_PROGRESS"
	ContainerStatusFinished   = "FINISHED"
	ContainerStatusError      = "ERROR"
	ContainerStatusExpired    = "EXPIRED"
)

// Media Type Constants
const (
	MediaTypeImage = "IMAGE"
	MediaTypeReels = "REELS"
	MediaTypeVideo = "VIDEO"
)

// RequiredScopes defines minimal Meta permissions for Instagram publishing & insights.
var RequiredScopes = []string{
	"instagram_basic",
	"instagram_content_publish",
	"instagram_manage_insights",
	"pages_show_list",
	"pages_read_engagement",
	"business_management",
}

// Domain Errors
var (
	ErrPersonalAccountNotSupported = errors.New("instagram: connected account is not an Instagram Business or Creator account; Meta Graph API requires an Instagram Professional account linked to a Facebook Page")
	ErrInvalidMediaFormat          = errors.New("instagram: invalid media format or unsupported aspect ratio")
	ErrContainerProcessingFailed   = errors.New("instagram: media container transcoding/processing failed on Meta servers")
	ErrContainerExpired            = errors.New("instagram: media container has expired on Meta servers")
	ErrMediaPayloadExpired         = errors.New("instagram: container expired on Meta and staged media payload is no longer available; please retry publish")
	ErrReauthenticationRequired    = errors.New("instagram: access token has expired and cannot be refreshed silently; re-authentication required via /auth/instagram/connect")
	ErrInvalidSignature            = errors.New("instagram: webhook payload signature verification failed (invalid X-Hub-Signature-256)")
)

// InstagramAPIError represents an error response returned by Meta Graph API.
type InstagramAPIError struct {
	StatusCode   int    `json:"status_code"`
	Message      string `json:"message"`
	Type         string `json:"type"`
	Code         int    `json:"code"`
	ErrorSubcode int    `json:"error_subcode"`
	FBTraceID    string `json:"fbtrace_id"`
}

func (e *InstagramAPIError) Error() string {
	return fmt.Sprintf("meta graph api error (status %d, code %d, subcode %d): %s (trace_id: %s)",
		e.StatusCode, e.Code, e.ErrorSubcode, e.Message, e.FBTraceID)
}

// TokenResponse represents the short-lived or long-lived OAuth token response.
type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"` // Seconds until expiration
}

// PageAccountsResponse represents the list of Facebook Pages owned by the user.
type PageAccountsResponse struct {
	Data []PageAccount `json:"data"`
}

// PageAccount represents a single Facebook Page and its linked Instagram Business Account.
type PageAccount struct {
	ID                       string                    `json:"id"`
	Name                     string                    `json:"name"`
	AccessToken              string                    `json:"access_token"`
	InstagramBusinessAccount *InstagramBusinessAccount `json:"instagram_business_account,omitempty"`
}

// InstagramBusinessAccount holds the discovered Instagram Business/Creator account metadata.
type InstagramBusinessAccount struct {
	ID       string `json:"id"`
	Username string `json:"username,omitempty"`
	Name     string `json:"name,omitempty"`
}

// CreateContainerRequest contains parameters to initialize an Instagram media container.
type CreateContainerRequest struct {
	IGUserID    string
	AccessToken string
	Caption     string
	ImageURL    string
	VideoURL    string
	MediaType   string // IMAGE or REELS
	ShareToFeed bool
}

// ContainerResponse contains the created container ID (creation_id).
type ContainerResponse struct {
	ID string `json:"id"`
}

// ContainerStatusResponse represents the asynchronous transcoding/processing status of a container.
type ContainerStatusResponse struct {
	ID           string `json:"id"`
	StatusCode   string `json:"status_code"` // IN_PROGRESS, FINISHED, ERROR, EXPIRED
	Status       string `json:"status,omitempty"`
	ErrorMessage string `json:"error_message,omitempty"`
}

// PublishMediaResponse represents the final published post ID on Instagram.
type PublishMediaResponse struct {
	ID string `json:"id"` // instagram media id
}

// InstagramInsightsResponse represents engagement and impression metrics from Meta.
type InstagramInsightsResponse struct {
	Data []struct {
		Name        string `json:"name"`
		Period      string `json:"period"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Values      []struct {
			Value int64 `json:"value"`
		} `json:"values"`
		TotalValue *struct {
			Value int64 `json:"value"`
		} `json:"total_value,omitempty"`
	} `json:"data"`
}

// UnifiedInstagramMetrics represents formatted post performance statistics.
type UnifiedInstagramMetrics struct {
	MediaID           string    `json:"media_id"`
	Impressions       int64     `json:"impressions"`
	Reach             int64     `json:"reach"`
	Likes             int64     `json:"likes"`
	Comments          int64     `json:"comments"`
	Saved             int64     `json:"saved"`
	Shares            int64     `json:"shares"`
	Plays             int64     `json:"plays"`
	TotalInteractions int64     `json:"total_interactions"`
	RetrievedAt       time.Time `json:"retrieved_at"`
}

// InstagramUserProfile represents account metadata for an IG Business/Creator account.
type InstagramUserProfile struct {
	ID             string `json:"id"`
	Username       string `json:"username"`
	Name           string `json:"name"`
	Biography      string `json:"biography"`
	FollowersCount int64  `json:"followers_count"`
	FollowsCount   int64  `json:"follows_count"`
	MediaCount     int64  `json:"media_count"`
	Website        string `json:"website,omitempty"`
}

// InstagramRecentMediaItem represents high-level metrics for recently published media.
type InstagramRecentMediaItem struct {
	ID               string    `json:"id"`
	Caption          string    `json:"caption,omitempty"`
	MediaType        string    `json:"media_type"`
	MediaProductType string    `json:"media_product_type,omitempty"`
	LikeCount        int64     `json:"like_count"`
	CommentsCount    int64     `json:"comments_count"`
	Timestamp        time.Time `json:"timestamp"`
	Permalink        string    `json:"permalink,omitempty"`
}

// UnifiedInstagramAccountInsights aggregates account-level metrics, growth, and content comparisons.
type UnifiedInstagramAccountInsights struct {
	Profile           InstagramUserProfile       `json:"profile"`
	Period            string                     `json:"period"`
	TotalReach        int64                      `json:"total_reach"`
	TotalImpressions  int64                      `json:"total_impressions"`
	ProfileViews      int64                      `json:"profile_views"`
	WebsiteClicks     int64                      `json:"website_clicks"`
	TotalInteractions int64                      `json:"total_interactions"`
	EngagementRatePct float64                    `json:"engagement_rate_pct"`
	RecentMedia       []InstagramRecentMediaItem `json:"recent_media"`
	Diagnostics       map[string]interface{}     `json:"diagnostics"`
	RetrievedAt       time.Time                  `json:"retrieved_at"`
}

// WebhookPayload represents an incoming Meta webhook event notification.
type WebhookPayload struct {
	Object string         `json:"object"`
	Entry  []WebhookEntry `json:"entry"`
}

// WebhookEntry represents a single entry in a Meta webhook payload.
type WebhookEntry struct {
	ID      string          `json:"id"`
	Time    int64           `json:"time"`
	Changes []WebhookChange `json:"changes"`
}

// WebhookChange represents a change payload inside a webhook entry.
type WebhookChange struct {
	Field string                 `json:"field"`
	Value map[string]interface{} `json:"value"`
}
