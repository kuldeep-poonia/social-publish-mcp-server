// Package youtube provides the HTTP client for Google OAuth 2.0 PKCE,
// YouTube Data API v3, chunked resumable video uploads, and analytics retrieval.
package youtube

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client communicates with Google OAuth and YouTube Data API v3 endpoints.
type Client struct {
	clientID     string
	clientSecret string
	httpClient   *http.Client
	uploadBase   string
	apiBase      string
	tokenURL     string
	revokeURL    string
}

// NewClient initializes a YouTube API v3 client.
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		clientID:     clientID,
		clientSecret: clientSecret,
		httpClient: &http.Client{
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		uploadBase: YouTubeUploadBaseURL,
		apiBase:    YouTubeAPIBaseURL,
		tokenURL:   OAuthTokenURL,
		revokeURL:  OAuthRevokeURL,
	}
}

// SetAPIBaseForTesting overrides API base URL for test mocking.
func (c *Client) SetAPIBaseForTesting(u string) {
	c.apiBase = u
}

// SetTokenURLForTesting overrides token URL for test mocking.
func (c *Client) SetTokenURLForTesting(u string) {
	c.tokenURL = u
}

// ExchangeOAuthToken exchanges an authorization code for access and refresh tokens.
func (c *Client) ExchangeOAuthToken(ctx context.Context, code, codeVerifier, redirectURI string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("client_id", c.clientID)
	data.Set("client_secret", c.clientSecret)
	data.Set("code", code)
	data.Set("code_verifier", codeVerifier)
	data.Set("grant_type", "authorization_code")
	data.Set("redirect_uri", redirectURI)

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
		return nil, &YouTubeAPIError{
			StatusCode: resp.StatusCode,
			Message:    string(body),
			Reason:     "TokenExchangeFailed",
		}
	}

	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("failed decoding token response: %w", err)
	}
	return &tok, nil
}

// RefreshToken obtains a new access token using a stored refresh token.
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	data := url.Values{}
	data.Set("client_id", c.clientID)
	data.Set("client_secret", c.clientSecret)
	data.Set("refresh_token", refreshToken)
	data.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed creating refresh request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed executing token refresh: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &YouTubeAPIError{
			StatusCode: resp.StatusCode,
			Message:    string(body),
			Reason:     "TokenRefreshFailed",
		}
	}

	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("failed decoding refresh response: %w", err)
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = refreshToken // Retain existing refresh token if Google does not rotate
	}
	return &tok, nil
}

// RevokeToken invalidates a token with Google OAuth.
func (c *Client) RevokeToken(ctx context.Context, token string) error {
	data := url.Values{}
	data.Set("token", token)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.revokeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return nil
}

// InitiateResumableUpload initiates a resumable video upload session and returns the upload session URI.
func (c *Client) InitiateResumableUpload(ctx context.Context, accessToken string, snippet *VideoSnippet, status *VideoStatus, mimeType string, totalBytes int64) (string, error) {
	initURL := fmt.Sprintf("%s?uploadType=resumable&part=snippet,status", c.uploadBase)

	metadata := map[string]interface{}{
		"snippet": snippet,
		"status":  status,
	}
	metaBytes, err := json.Marshal(metadata)
	if err != nil {
		return "", fmt.Errorf("failed serializing video metadata: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, bytes.NewReader(metaBytes))
	if err != nil {
		return "", fmt.Errorf("failed creating init upload request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Type", mimeType)
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(totalBytes, 10))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed initiating resumable upload: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return "", &YouTubeAPIError{
			StatusCode: resp.StatusCode,
			Message:    string(body),
			Reason:     "ResumableInitFailed",
		}
	}

	sessionURI := resp.Header.Get("Location")
	if sessionURI == "" {
		return "", errors.New("youtube client: missing Location header in resumable upload response")
	}

	return sessionURI, nil
}

// UploadChunk streams a single binary chunk to the resumable session URI.
// Returns (bytesReceived, videoID, isCompleted, error).
func (c *Client) UploadChunk(ctx context.Context, sessionURI string, chunkData io.Reader, startByte, endByte, totalBytes int64, mimeType string) (int64, string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURI, chunkData)
	if err != nil {
		return 0, "", false, fmt.Errorf("failed creating upload chunk request: %w", err)
	}

	contentRange := fmt.Sprintf("bytes %d-%d/%d", startByte, endByte, totalBytes)
	req.Header.Set("Content-Range", contentRange)
	req.Header.Set("Content-Type", mimeType)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, "", false, fmt.Errorf("failed streaming chunk: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	// HTTP 308 Resume Incomplete indicates chunk was received, more chunks needed
	if resp.StatusCode == 308 {
		rangeHeader := resp.Header.Get("Range")
		var bytesRecv int64 = endByte + 1
		if rangeHeader != "" {
			// Range header format: "bytes=0-1048575"
			parts := strings.Split(rangeHeader, "-")
			if len(parts) == 2 {
				if upper, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
					bytesRecv = upper + 1
				}
			}
		}
		return bytesRecv, "", false, nil
	}

	// HTTP 200 OK or 201 Created indicates upload is complete
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		var video VideoResource
		if err := json.Unmarshal(body, &video); err != nil {
			return totalBytes, "", true, fmt.Errorf("failed decoding completed video resource: %w", err)
		}
		return totalBytes, video.ID, true, nil
	}

	return 0, "", false, &YouTubeAPIError{
		StatusCode: resp.StatusCode,
		Message:    string(body),
		Reason:     "ChunkUploadFailed",
	}
}

// QueryResumableOffset queries the byte offset Google has already received for an interrupted upload session.
func (c *Client) QueryResumableOffset(ctx context.Context, sessionURI string, totalBytes int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURI, nil)
	if err != nil {
		return 0, err
	}

	req.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", totalBytes))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("failed querying upload status: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 308 {
		rangeHeader := resp.Header.Get("Range")
		if rangeHeader == "" {
			return 0, nil // 0 bytes received yet
		}
		parts := strings.Split(rangeHeader, "-")
		if len(parts) == 2 {
			if upper, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				return upper + 1, nil
			}
		}
		return 0, nil
	}

	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
		return totalBytes, nil
	}

	body, _ := io.ReadAll(resp.Body)
	return 0, &YouTubeAPIError{
		StatusCode: resp.StatusCode,
		Message:    string(body),
		Reason:     "OffsetQueryFailed",
	}
}

// GetVideoAnalytics retrieves statistics for a published YouTube video.
func (c *Client) GetVideoAnalytics(ctx context.Context, accessToken, videoID string) (*VideoAnalyticsMetrics, error) {
	apiURL := fmt.Sprintf("%s?part=snippet,statistics,status&id=%s", c.apiBase, url.QueryEscape(videoID))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed creating analytics request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed fetching video analytics: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &YouTubeAPIError{
			StatusCode: resp.StatusCode,
			Message:    string(body),
			Reason:     "AnalyticsFetchFailed",
		}
	}

	var listResp struct {
		Items []VideoResource `json:"items"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil {
		return nil, fmt.Errorf("failed parsing video response: %w", err)
	}

	if len(listResp.Items) == 0 {
		return nil, fmt.Errorf("youtube video '%s' not found", videoID)
	}

	item := listResp.Items[0]
	var views, likes, comments int64
	if item.Statistics != nil {
		views, _ = strconv.ParseInt(item.Statistics.ViewCount, 10, 64)
		likes, _ = strconv.ParseInt(item.Statistics.LikeCount, 10, 64)
		comments, _ = strconv.ParseInt(item.Statistics.CommentCount, 10, 64)
	}

	title := ""
	if item.Snippet != nil {
		title = item.Snippet.Title
	}

	privacy := "public"
	if item.Status != nil {
		privacy = item.Status.PrivacyStatus
	}

	return &VideoAnalyticsMetrics{
		VideoID:       videoID,
		Title:         title,
		ViewCount:     views,
		LikeCount:     likes,
		CommentCount:  comments,
		PrivacyStatus: privacy,
		RetrievedAt:   time.Now().UTC(),
	}, nil
}

// GetChannelInsights retrieves channel statistics and profile metadata.
func (c *Client) GetChannelInsights(ctx context.Context, accessToken string) (*YouTubeChannelInsights, error) {
	apiURL := "https://www.googleapis.com/youtube/v3/channels?part=snippet,statistics&mine=true"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed creating channel insights request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed fetching channel insights: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &YouTubeAPIError{
			StatusCode: resp.StatusCode,
			Message:    string(body),
			Reason:     "ChannelFetchFailed",
		}
	}

	var chanResp struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title       string `json:"title"`
				Description string `json:"description"`
				CustomURL   string `json:"customUrl"`
			} `json:"snippet"`
			Statistics struct {
				ViewCount       string `json:"viewCount"`
				SubscriberCount string `json:"subscriberCount"`
				VideoCount      string `json:"videoCount"`
			} `json:"statistics"`
		} `json:"items"`
	}

	if err := json.Unmarshal(body, &chanResp); err != nil {
		return nil, fmt.Errorf("failed parsing channel response: %w", err)
	}

	if len(chanResp.Items) == 0 {
		return nil, errors.New("no youtube channel found for authenticated user")
	}

	item := chanResp.Items[0]
	views, _ := strconv.ParseInt(item.Statistics.ViewCount, 10, 64)
	subs, _ := strconv.ParseInt(item.Statistics.SubscriberCount, 10, 64)
	vids, _ := strconv.ParseInt(item.Statistics.VideoCount, 10, 64)

	recentVideos, _ := c.GetRecentChannelVideos(ctx, accessToken, 5)

	diagnostics := map[string]interface{}{
		"total_subscribers":   subs,
		"total_videos":        vids,
		"total_views":         views,
		"avg_views_per_video": int64(0),
		"diagnostic_status":   "healthy",
		"recommendation":      "Optimize video titles and tags with high-search keywords and custom thumbnails.",
	}
	if vids > 0 {
		diagnostics["avg_views_per_video"] = views / vids
	}

	return &YouTubeChannelInsights{
		ChannelID:       item.ID,
		Title:           item.Snippet.Title,
		Description:     item.Snippet.Description,
		CustomURL:       item.Snippet.CustomURL,
		SubscriberCount: subs,
		VideoCount:      vids,
		ViewCount:       views,
		RecentVideos:    recentVideos,
		Diagnostics:     diagnostics,
		RetrievedAt:     time.Now().UTC(),
	}, nil
}

// GetRecentChannelVideos retrieves the latest uploaded videos and their metrics.
func (c *Client) GetRecentChannelVideos(ctx context.Context, accessToken string, limit int) ([]VideoAnalyticsMetrics, error) {
	if limit <= 0 || limit > 50 {
		limit = 5
	}
	apiURL := fmt.Sprintf("https://www.googleapis.com/youtube/v3/search?part=snippet&forMine=true&type=video&order=date&maxResults=%d", limit)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &YouTubeAPIError{
			StatusCode: resp.StatusCode,
			Message:    string(body),
			Reason:     "SearchFetchFailed",
		}
	}

	var searchResp struct {
		Items []struct {
			ID struct {
				VideoID string `json:"videoId"`
			} `json:"id"`
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return nil, err
	}

	var results []VideoAnalyticsMetrics
	for _, it := range searchResp.Items {
		if it.ID.VideoID != "" {
			if metrics, err := c.GetVideoAnalytics(ctx, accessToken, it.ID.VideoID); err == nil {
				results = append(results, *metrics)
			}
		}
	}

	return results, nil
}

// UpdateVideoMetadata updates title, description, and tags of an existing YouTube video.
func (c *Client) UpdateVideoMetadata(ctx context.Context, accessToken string, params *UpdateVideoMetadataParams) (*VideoAnalyticsMetrics, error) {
	if params.VideoID == "" {
		return nil, errors.New("video_id is required for metadata update")
	}

	// First fetch existing video snippet to preserve categoryId and status
	apiURL := fmt.Sprintf("%s?part=snippet,status&id=%s", c.apiBase, url.QueryEscape(params.VideoID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed fetching video details: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &YouTubeAPIError{StatusCode: resp.StatusCode, Message: string(body), Reason: "FetchFailed"}
	}

	var listResp struct {
		Items []VideoResource `json:"items"`
	}
	if err := json.Unmarshal(body, &listResp); err != nil || len(listResp.Items) == 0 {
		return nil, fmt.Errorf("video '%s' not found", params.VideoID)
	}

	existing := listResp.Items[0]
	if existing.Snippet == nil {
		existing.Snippet = &VideoSnippet{}
	}

	if params.Title != "" {
		existing.Snippet.Title = params.Title
	}
	if params.Description != "" {
		existing.Snippet.Description = params.Description
	}
	if len(params.Tags) > 0 {
		existing.Snippet.Tags = params.Tags
	}
	if params.CategoryID != "" {
		existing.Snippet.CategoryID = params.CategoryID
	}

	updateURL := fmt.Sprintf("%s?part=snippet", c.apiBase)
	updatePayload, _ := json.Marshal(existing)

	putReq, err := http.NewRequestWithContext(ctx, http.MethodPut, updateURL, bytes.NewReader(updatePayload))
	if err != nil {
		return nil, err
	}
	putReq.Header.Set("Authorization", "Bearer "+accessToken)
	putReq.Header.Set("Content-Type", "application/json")

	putResp, err := c.httpClient.Do(putReq)
	if err != nil {
		return nil, fmt.Errorf("failed updating video: %w", err)
	}
	defer putResp.Body.Close()

	putBody, _ := io.ReadAll(putResp.Body)
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		return nil, &YouTubeAPIError{StatusCode: putResp.StatusCode, Message: string(putBody), Reason: "UpdateFailed"}
	}

	return c.GetVideoAnalytics(ctx, accessToken, params.VideoID)
}
