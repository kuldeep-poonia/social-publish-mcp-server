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
			Timeout: 120 * time.Second, // Adequate for streaming upload chunks
		},
		uploadBase: YouTubeUploadBaseURL,
		apiBase:    YouTubeAPIBaseURL,
		tokenURL:   OAuthTokenURL,
		revokeURL:  OAuthRevokeURL,
	}
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
