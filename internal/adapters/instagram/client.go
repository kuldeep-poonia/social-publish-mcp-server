// Package instagram implements the Meta Graph API client for Instagram Business/Creator publishing and insights.
package instagram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client communicates with Meta Graph API v21.0 endpoints.
type Client struct {
	clientID     string
	clientSecret string
	apiBase      string
	tokenURL     string
	httpClient   *http.Client
}

// NewClient initializes an Instagram Graph API client.
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		apiBase:      MetaGraphAPIBaseURL,
		tokenURL:     MetaTokenExchangeURL,
		httpClient: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// ExchangeShortLivedToken exchanges an OAuth authorization code for a short-lived user access token.
func (c *Client) ExchangeShortLivedToken(ctx context.Context, code, redirectURI string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("client_id", c.clientID)
	data.Set("client_secret", c.clientSecret)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", redirectURI)
	data.Set("code", code)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed creating token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed executing token exchange: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseMetaAPIError(resp.StatusCode, body)
	}

	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("failed decoding token response: %w", err)
	}
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 3600 // 1 hour default
	}
	return &tok, nil
}

// ExchangeLongLivedToken upgrades a short-lived user access token to a 60-day long-lived access token.
func (c *Client) ExchangeLongLivedToken(ctx context.Context, shortLivedToken string) (*TokenResponse, error) {
	endpoint := fmt.Sprintf("%s?grant_type=fb_exchange_token&client_id=%s&client_secret=%s&fb_exchange_token=%s",
		c.tokenURL, url.QueryEscape(c.clientID), url.QueryEscape(c.clientSecret), url.QueryEscape(shortLivedToken))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed creating long-lived token request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed executing long-lived token exchange: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseMetaAPIError(resp.StatusCode, body)
	}

	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("failed decoding long-lived token response: %w", err)
	}
	if tok.ExpiresIn <= 0 {
		tok.ExpiresIn = 60 * 24 * 3600 // 60 days default (5,184,000 seconds)
	}
	return &tok, nil
}

// ExtendLongLivedToken re-exchanges a valid long-lived token for a fresh 60-day token.
func (c *Client) ExtendLongLivedToken(ctx context.Context, currentToken string) (*TokenResponse, error) {
	return c.ExchangeLongLivedToken(ctx, currentToken)
}

// GetInstagramBusinessAccount discovers the linked Instagram Business/Creator Account from the user's Facebook Pages or direct /me.
func (c *Client) GetInstagramBusinessAccount(ctx context.Context, userAccessToken string) (*InstagramBusinessAccount, string, error) {
	// 1. Primary Discovery: Facebook Pages (/me/accounts)
	endpoint := fmt.Sprintf("%s/me/accounts?fields=id,name,access_token,instagram_business_account{id,username,name}&access_token=%s",
		c.apiBase, url.QueryEscape(userAccessToken))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err == nil {
		resp, doErr := c.httpClient.Do(req)
		if doErr == nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				var pagesResp PageAccountsResponse
				if jErr := json.Unmarshal(body, &pagesResp); jErr == nil && len(pagesResp.Data) > 0 {
					for _, page := range pagesResp.Data {
						if page.InstagramBusinessAccount != nil && page.InstagramBusinessAccount.ID != "" {
							pageAccessToken := page.AccessToken
							if pageAccessToken == "" {
								pageAccessToken = userAccessToken
							}
							return page.InstagramBusinessAccount, pageAccessToken, nil
						}
					}
				}
			}
		}
	}

	// 2. Secondary Fallback: Direct Instagram Profile (/me)
	directEndpoint := fmt.Sprintf("%s/me?fields=id,username,name,account_type&access_token=%s",
		c.apiBase, url.QueryEscape(userAccessToken))
	reqDirect, err := http.NewRequestWithContext(ctx, http.MethodGet, directEndpoint, nil)
	if err == nil {
		respDirect, doErr := c.httpClient.Do(reqDirect)
		if doErr == nil {
			defer respDirect.Body.Close()
			body, _ := io.ReadAll(respDirect.Body)
			if respDirect.StatusCode >= 200 && respDirect.StatusCode < 300 {
				var directAccount struct {
					ID          string `json:"id"`
					Username    string `json:"username"`
					Name        string `json:"name"`
					AccountType string `json:"account_type"`
				}
				if jErr := json.Unmarshal(body, &directAccount); jErr == nil && directAccount.ID != "" {
					return &InstagramBusinessAccount{
						ID:       directAccount.ID,
						Username: directAccount.Username,
						Name:     directAccount.Name,
					}, userAccessToken, nil
				}
			}
		}
	}

	return nil, "", fmt.Errorf("%w: please verify in Meta/Instagram that your Instagram account is set to Professional (Creator or Business) and linked to a Facebook Page", ErrPersonalAccountNotSupported)
}

// CreateMediaContainer initializes a media container on Meta's servers (Step 1).
func (c *Client) CreateMediaContainer(ctx context.Context, req *CreateContainerRequest) (string, error) {
	if req.IGUserID == "" {
		return "", errors.New("missing instagram user id (ig_user_id)")
	}
	if req.AccessToken == "" {
		return "", errors.New("missing access token")
	}

	endpoint := fmt.Sprintf("%s/%s/media", c.apiBase, req.IGUserID)
	data := url.Values{}
	data.Set("access_token", req.AccessToken)
	if req.Caption != "" {
		data.Set("caption", req.Caption)
	}

	if req.MediaType == MediaTypeReels || req.VideoURL != "" {
		data.Set("media_type", "REELS")
		data.Set("video_url", req.VideoURL)
		if req.ShareToFeed {
			data.Set("share_to_feed", "true")
		}
	} else {
		data.Set("image_url", req.ImageURL)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed creating media container request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("failed executing create media container: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", parseMetaAPIError(resp.StatusCode, body)
	}

	var containerResp ContainerResponse
	if err := json.Unmarshal(body, &containerResp); err != nil {
		return "", fmt.Errorf("failed decoding media container response: %w", err)
	}

	if containerResp.ID == "" {
		return "", errors.New("meta returned empty container ID")
	}

	return containerResp.ID, nil
}

// PollContainerStatus checks container status with a bounded 4-state state machine until terminal state.
func (c *Client) PollContainerStatus(ctx context.Context, creationID, accessToken string) (*ContainerStatusResponse, error) {
	endpoint := fmt.Sprintf("%s/%s?fields=id,status_code,status&access_token=%s",
		c.apiBase, creationID, url.QueryEscape(accessToken))

	waitInterval := ContainerPollInitialWait
	deadline := time.Now().Add(ContainerPollTimeout)

	for attempt := 1; attempt <= MaxContainerPollAttempts; attempt++ {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%w: container processing timed out after %v", ErrContainerProcessingFailed, ContainerPollTimeout)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("failed creating status query request: %w", err)
		}

		resp, err := c.httpClient.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("failed querying container status: %w", err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, parseMetaAPIError(resp.StatusCode, body)
		}

		var statusResp ContainerStatusResponse
		if err := json.Unmarshal(body, &statusResp); err != nil {
			return nil, fmt.Errorf("failed decoding status response: %w", err)
		}

		switch statusResp.StatusCode {
		case ContainerStatusFinished:
			return &statusResp, nil

		case ContainerStatusError:
			return nil, fmt.Errorf("%w: %s (status: %s)", ErrContainerProcessingFailed, statusResp.ErrorMessage, statusResp.Status)

		case ContainerStatusExpired:
			return nil, ErrContainerExpired

		case ContainerStatusInProgress, "":
			// Continue polling with exponential backoff
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(waitInterval):
			}

			waitInterval *= 2
			if waitInterval > ContainerPollMaxWait {
				waitInterval = ContainerPollMaxWait
			}

		default:
			// Unrecognized state, continue polling cautiously
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(waitInterval):
			}
		}
	}

	return nil, fmt.Errorf("%w: max polling attempts (%d) reached without reaching terminal state", ErrContainerProcessingFailed, MaxContainerPollAttempts)
}

// PublishMedia publishes a finished container to the user's Instagram feed (Step 2).
func (c *Client) PublishMedia(ctx context.Context, igUserID, creationID, accessToken string) (string, error) {
	endpoint := fmt.Sprintf("%s/%s/media_publish", c.apiBase, igUserID)
	data := url.Values{}
	data.Set("creation_id", creationID)
	data.Set("access_token", accessToken)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(data.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed creating media publish request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("failed executing media publish: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", parseMetaAPIError(resp.StatusCode, body)
	}

	var pubResp PublishMediaResponse
	if err := json.Unmarshal(body, &pubResp); err != nil {
		return "", fmt.Errorf("failed decoding publish response: %w", err)
	}

	if pubResp.ID == "" {
		return "", errors.New("meta returned empty published media ID")
	}

	return pubResp.ID, nil
}

// GetMediaInsights retrieves performance and engagement metrics for a published Instagram post or reel.
func (c *Client) GetMediaInsights(ctx context.Context, mediaID, accessToken string) (*UnifiedInstagramMetrics, error) {
	endpoint := fmt.Sprintf("%s/%s/insights?metric=views,reach,likes,comments,saved,shares,total_interactions&access_token=%s",
		c.apiBase, mediaID, url.QueryEscape(accessToken))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed creating insights request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed querying instagram insights: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseMetaAPIError(resp.StatusCode, body)
	}

	var rawInsights InstagramInsightsResponse
	if err := json.Unmarshal(body, &rawInsights); err != nil {
		return nil, fmt.Errorf("failed decoding insights response: %w", err)
	}

	metrics := &UnifiedInstagramMetrics{
		MediaID:     mediaID,
		RetrievedAt: time.Now().UTC(),
	}

	for _, metric := range rawInsights.Data {
		var val int64
		if len(metric.Values) > 0 {
			val = metric.Values[0].Value
		} else if metric.TotalValue != nil {
			val = metric.TotalValue.Value
		}

		switch metric.Name {
		case "impressions":
			metrics.Impressions = val
		case "views":
			metrics.Plays = val
			if metrics.Impressions == 0 {
				metrics.Impressions = val
			}
		case "plays":
			metrics.Plays = val
		case "reach":
			metrics.Reach = val
		case "likes":
			metrics.Likes = val
		case "comments":
			metrics.Comments = val
		case "saved":
			metrics.Saved = val
		case "shares":
			metrics.Shares = val
		case "total_interactions":
			metrics.TotalInteractions = val
		}
	}

	return metrics, nil
}

// GetAccountProfile queries high-level profile statistics for an Instagram business/creator account.
func (c *Client) GetAccountProfile(ctx context.Context, igUserID, accessToken string) (*InstagramUserProfile, error) {
	endpoint := fmt.Sprintf("%s/%s?fields=id,username,name,biography,followers_count,follows_count,media_count,website&access_token=%s",
		c.apiBase, igUserID, url.QueryEscape(accessToken))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed creating profile request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed querying profile: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseMetaAPIError(resp.StatusCode, body)
	}

	var profile InstagramUserProfile
	if err := json.Unmarshal(body, &profile); err != nil {
		return nil, fmt.Errorf("failed decoding profile response: %w", err)
	}

	return &profile, nil
}

// GetRecentMedia retrieves recent media posts and high-level engagement counts for comparison.
func (c *Client) GetRecentMedia(ctx context.Context, igUserID, accessToken string, limit int) ([]InstagramRecentMediaItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	endpoint := fmt.Sprintf("%s/%s/media?fields=id,caption,media_type,media_product_type,like_count,comments_count,timestamp,permalink&limit=%d&access_token=%s",
		c.apiBase, igUserID, limit, url.QueryEscape(accessToken))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed creating recent media request: %w", err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("failed querying recent media: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseMetaAPIError(resp.StatusCode, body)
	}

	var mediaResp struct {
		Data []InstagramRecentMediaItem `json:"data"`
	}
	if err := json.Unmarshal(body, &mediaResp); err != nil {
		return nil, fmt.Errorf("failed decoding recent media response: %w", err)
	}

	return mediaResp.Data, nil
}

// GetAggregatedAccountInsights combines profile data, account insights, recent post trends, and diagnostic ratios.
func (c *Client) GetAggregatedAccountInsights(ctx context.Context, igUserID, accessToken, period string) (*UnifiedInstagramAccountInsights, error) {
	if period == "" {
		period = "days_28"
	}

	profile, err := c.GetAccountProfile(ctx, igUserID, accessToken)
	if err != nil {
		return nil, fmt.Errorf("failed retrieving account profile: %w", err)
	}

	recentMedia, _ := c.GetRecentMedia(ctx, igUserID, accessToken, 10)

	// Fetch account-level metrics
	insightsEndpoint := fmt.Sprintf("%s/%s/insights?metric=impressions,reach,profile_views,website_clicks,total_interactions&period=%s&access_token=%s",
		c.apiBase, igUserID, url.QueryEscape(period), url.QueryEscape(accessToken))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, insightsEndpoint, nil)
	var totalReach, totalImpressions, profileViews, websiteClicks, totalInteractions int64

	if err == nil {
		resp, doErr := c.httpClient.Do(httpReq)
		if doErr == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				var rawInsights InstagramInsightsResponse
				if json.Unmarshal(body, &rawInsights) == nil {
					for _, metric := range rawInsights.Data {
						var val int64
						if len(metric.Values) > 0 {
							val = metric.Values[0].Value
						} else if metric.TotalValue != nil {
							val = metric.TotalValue.Value
						}
						switch metric.Name {
						case "reach":
							totalReach = val
						case "impressions":
							totalImpressions = val
						case "profile_views":
							profileViews = val
						case "website_clicks":
							websiteClicks = val
						case "total_interactions":
							totalInteractions = val
						}
					}
				}
			}
		}
	}

	// Calculate engagement rate
	var totalRecentLikes, totalRecentComments int64
	for _, m := range recentMedia {
		totalRecentLikes += m.LikeCount
		totalRecentComments += m.CommentsCount
	}

	var engagementRate float64
	if profile.FollowersCount > 0 && len(recentMedia) > 0 {
		avgInteractionsPerPost := float64(totalRecentLikes+totalRecentComments) / float64(len(recentMedia))
		engagementRate = (avgInteractionsPerPost / float64(profile.FollowersCount)) * 100.0
	}

	diagnostics := map[string]interface{}{
		"followers_count":       profile.FollowersCount,
		"recent_posts_analyzed": len(recentMedia),
		"avg_likes_per_post":    0.0,
		"avg_comments_per_post": 0.0,
		"diagnostic_status":     "healthy",
		"recommendation":        "Maintain regular posting schedule and test high-retention Reels.",
	}
	if len(recentMedia) > 0 {
		diagnostics["avg_likes_per_post"] = float64(totalRecentLikes) / float64(len(recentMedia))
		diagnostics["avg_comments_per_post"] = float64(totalRecentComments) / float64(len(recentMedia))
	}
	if profile.FollowersCount > 0 && totalReach > 0 {
		diagnostics["reach_to_follower_ratio"] = float64(totalReach) / float64(profile.FollowersCount)
	}
	if engagementRate < 1.5 && profile.FollowersCount > 100 {
		diagnostics["diagnostic_status"] = "low_engagement"
		diagnostics["recommendation"] = "Engagement rate is below standard benchmark (1.5%-3%). Focus on strong hook in first 3 seconds of Reels, interactive carousel slides, and targeted niche hashtags."
	}

	return &UnifiedInstagramAccountInsights{
		Profile:           *profile,
		Period:            period,
		TotalReach:        totalReach,
		TotalImpressions:  totalImpressions,
		ProfileViews:      profileViews,
		WebsiteClicks:     websiteClicks,
		TotalInteractions: totalInteractions,
		EngagementRatePct: engagementRate,
		RecentMedia:       recentMedia,
		Diagnostics:       diagnostics,
		RetrievedAt:       time.Now().UTC(),
	}, nil
}

// parseMetaAPIError maps raw HTTP error payloads into structured InstagramAPIError.
func parseMetaAPIError(statusCode int, body []byte) error {
	type rawMetaErr struct {
		Error struct {
			Message      string `json:"message"`
			Type         string `json:"type"`
			Code         int    `json:"code"`
			ErrorSubcode int    `json:"error_subcode"`
			FBTraceID    string `json:"fbtrace_id"`
		} `json:"error"`
	}

	var parsed rawMetaErr
	if err := json.Unmarshal(body, &parsed); err == nil && parsed.Error.Message != "" {
		// Detect expired token error codes (Error 190 / Subcode 463/467)
		if parsed.Error.Code == 190 || parsed.Error.ErrorSubcode == 463 || parsed.Error.ErrorSubcode == 467 {
			return fmt.Errorf("%w: %s", ErrReauthenticationRequired, parsed.Error.Message)
		}

		return &InstagramAPIError{
			StatusCode:   statusCode,
			Message:      parsed.Error.Message,
			Type:         parsed.Error.Type,
			Code:         parsed.Error.Code,
			ErrorSubcode: parsed.Error.ErrorSubcode,
			FBTraceID:    parsed.Error.FBTraceID,
		}
	}

	return &InstagramAPIError{
		StatusCode: statusCode,
		Message:    string(bytes.TrimSpace(body)),
		Type:       "GenericMetaError",
	}
}
