// Package twitter provides the Twitter/X API v2 HTTP client implementation with automatic 429 backoff handling and chunked media uploads.
package twitter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client coordinates HTTP operations against Twitter API v2.
type Client struct {
	httpClient   *http.Client
	baseURL      string
	uploadURL    string
	clientID     string
	clientSecret string
}

// NewClient initializes a standard Twitter API v2 client.
func NewClient(clientID, clientSecret string) *Client {
	return &Client{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		baseURL:      APIBaseURL,
		uploadURL:    UploadBaseURL,
		clientID:     clientID,
		clientSecret: clientSecret,
	}
}

// NewCustomClient initializes a Twitter client with custom URLs (for mock servers and testing).
func NewCustomClient(httpClient *http.Client, baseURL, uploadURL, clientID, clientSecret string) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		httpClient:   httpClient,
		baseURL:      baseURL,
		uploadURL:    uploadURL,
		clientID:     clientID,
		clientSecret: clientSecret,
	}
}

// ExchangeOAuthToken exchanges a PKCE authorization code for Twitter access and refresh tokens.
func (c *Client) ExchangeOAuthToken(ctx context.Context, code, codeVerifier, redirectURI string) (*OAuth2TokenResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "authorization_code")
	data.Set("code", code)
	data.Set("code_verifier", codeVerifier)
	data.Set("redirect_uri", redirectURI)
	data.Set("client_id", c.clientID)

	tokenURL := fmt.Sprintf("%s/oauth2/token", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed creating token request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.clientSecret != "" {
		req.SetBasicAuth(c.clientID, c.clientSecret)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed executing token exchange: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseAPIError(resp)
	}

	var tokenResp OAuth2TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed decoding token response: %w", err)
	}

	return &tokenResp, nil
}

// RefreshToken obtains a refreshed access/refresh token pair using an existing refresh token.
func (c *Client) RefreshToken(ctx context.Context, refreshToken string) (*OAuth2TokenResponse, error) {
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", refreshToken)
	data.Set("client_id", c.clientID)

	tokenURL := fmt.Sprintf("%s/oauth2/token", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("failed creating refresh request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.clientSecret != "" {
		req.SetBasicAuth(c.clientID, c.clientSecret)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed executing token refresh: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseAPIError(resp)
	}

	var tokenResp OAuth2TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("failed decoding refreshed token response: %w", err)
	}

	return &tokenResp, nil
}

// RevokeToken triggers upstream revocation at Twitter's endpoint.
func (c *Client) RevokeToken(ctx context.Context, token string) error {
	data := url.Values{}
	data.Set("token", token)
	data.Set("token_type_hint", "access_token")
	data.Set("client_id", c.clientID)

	revokeURL := fmt.Sprintf("%s/oauth2/revoke", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, revokeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return fmt.Errorf("failed creating revoke request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.clientSecret != "" {
		req.SetBasicAuth(c.clientID, c.clientSecret)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed executing token revoke: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
		return c.parseAPIError(resp)
	}
	return nil
}

// PostTweet creates a new tweet on Twitter API v2.
func (c *Client) PostTweet(ctx context.Context, accessToken string, tweetReq *TweetCreateRequest) (*TweetCreateResponse, error) {
	if _, err := ValidateTweetText(tweetReq.Text); err != nil {
		return nil, err
	}

	bodyBytes, err := json.Marshal(tweetReq)
	if err != nil {
		return nil, fmt.Errorf("failed encoding tweet JSON: %w", err)
	}

	tweetURL := fmt.Sprintf("%s/tweets", c.baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tweetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed creating tweet request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.executeWithBackoff(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, c.parseAPIError(resp)
	}

	var tweetResp TweetCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&tweetResp); err != nil {
		return nil, fmt.Errorf("failed decoding tweet response: %w", err)
	}

	return &tweetResp, nil
}

// GetTweetAnalytics retrieves public and non-public metrics for a tweet by ID.
func (c *Client) GetTweetAnalytics(ctx context.Context, accessToken, tweetID string) (*TweetMetricsResponse, error) {
	metricsURL := fmt.Sprintf("%s/tweets/%s?tweet.fields=public_metrics,non_public_metrics,created_at", c.baseURL, tweetID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed creating metrics request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := c.executeWithBackoff(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, c.parseAPIError(resp)
	}

	var metricsResp TweetMetricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&metricsResp); err != nil {
		return nil, fmt.Errorf("failed decoding metrics response: %w", err)
	}

	return &metricsResp, nil
}

// UploadMediaChunked performs chunked upload (INIT -> APPEND -> FINALIZE) against upload.twitter.com.
func (c *Client) UploadMediaChunked(ctx context.Context, accessToken string, mediaReader io.ReaderAt, size int64, mediaType MediaType) (string, error) {
	// Step 1: INIT Command
	initData := url.Values{}
	initData.Set("command", "INIT")
	initData.Set("total_bytes", strconv.FormatInt(size, 10))
	initData.Set("media_type", string(mediaType))

	if mediaType == MediaTypeMP4 {
		initData.Set("media_category", "tweet_video")
	} else if mediaType == MediaTypeGIF {
		initData.Set("media_category", "tweet_gif")
	} else {
		initData.Set("media_category", "tweet_image")
	}

	initURL := fmt.Sprintf("%s/media/upload.json", c.uploadURL)
	initReq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, strings.NewReader(initData.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed creating media INIT request: %w", err)
	}

	initReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	initReq.Header.Set("Authorization", "Bearer "+accessToken)

	initResp, err := c.httpClient.Do(initReq)
	if err != nil {
		return "", fmt.Errorf("failed executing media INIT: %w", err)
	}
	defer initResp.Body.Close()

	if initResp.StatusCode != http.StatusOK && initResp.StatusCode != http.StatusAccepted {
		return "", c.parseAPIError(initResp)
	}

	var initResult MediaUploadInitResponse
	if err := json.NewDecoder(initResp.Body).Decode(&initResult); err != nil {
		return "", fmt.Errorf("failed decoding media INIT response: %w", err)
	}
	mediaID := initResult.MediaIDString
	if mediaID == "" && initResult.MediaID != 0 {
		mediaID = strconv.FormatInt(initResult.MediaID, 10)
	}

	// Step 2: APPEND Command in 4MB chunks
	const chunkSize = int64(4 * 1024 * 1024)
	var segmentIndex int
	var offset int64

	for offset < size {
		bytesToRead := chunkSize
		if offset+bytesToRead > size {
			bytesToRead = size - offset
		}

		chunkBuf := make([]byte, bytesToRead)
		if _, err := mediaReader.ReadAt(chunkBuf, offset); err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("failed reading media chunk at offset %d: %w", offset, err)
		}

		body := &bytes.Buffer{}
		writer := multipart.NewWriter(body)
		_ = writer.WriteField("command", "APPEND")
		_ = writer.WriteField("media_id", mediaID)
		_ = writer.WriteField("segment_index", strconv.Itoa(segmentIndex))

		part, err := writer.CreateFormFile("media", "blob")
		if err != nil {
			return "", fmt.Errorf("failed creating multipart form: %w", err)
		}
		if _, err := part.Write(chunkBuf); err != nil {
			return "", fmt.Errorf("failed writing multipart chunk: %w", err)
		}
		_ = writer.Close()

		appendReq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, body)
		if err != nil {
			return "", fmt.Errorf("failed creating media APPEND request: %w", err)
		}

		appendReq.Header.Set("Content-Type", writer.FormDataContentType())
		appendReq.Header.Set("Authorization", "Bearer "+accessToken)

		appendResp, err := c.httpClient.Do(appendReq)
		if err != nil {
			return "", fmt.Errorf("failed executing media APPEND: %w", err)
		}
		appendResp.Body.Close()

		if appendResp.StatusCode != http.StatusOK && appendResp.StatusCode != http.StatusNoContent {
			return "", c.parseAPIError(appendResp)
		}

		offset += bytesToRead
		segmentIndex++
	}

	// Step 3: FINALIZE Command
	finalData := url.Values{}
	finalData.Set("command", "FINALIZE")
	finalData.Set("media_id", mediaID)

	finalReq, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, strings.NewReader(finalData.Encode()))
	if err != nil {
		return "", fmt.Errorf("failed creating media FINALIZE request: %w", err)
	}

	finalReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	finalReq.Header.Set("Authorization", "Bearer "+accessToken)

	finalResp, err := c.httpClient.Do(finalReq)
	if err != nil {
		return "", fmt.Errorf("failed executing media FINALIZE: %w", err)
	}
	defer finalResp.Body.Close()

	if finalResp.StatusCode != http.StatusOK && finalResp.StatusCode != http.StatusCreated {
		return "", c.parseAPIError(finalResp)
	}

	return mediaID, nil
}

// executeWithBackoff executes an HTTP request, automatically parsing 429 rate limits and applying backoff.
func (c *Client) executeWithBackoff(req *http.Request) (*http.Response, error) {
	const maxRetries = 2
	var resp *http.Response
	var err error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 && req.GetBody != nil {
			newBody, getErr := req.GetBody()
			if getErr != nil {
				return nil, getErr
			}
			req.Body = newBody
		}

		resp, err = c.httpClient.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}

		// Handle 429 Rate Limit
		resetHeader := resp.Header.Get("x-rate-limit-reset")
		resp.Body.Close()

		if attempt == maxRetries {
			return nil, &TwitterAPIError{
				Title:      "Rate Limit Exceeded",
				Detail:     "Twitter API v2 rate limit threshold reached after maximum retry attempts",
				StatusCode: http.StatusTooManyRequests,
			}
		}

		sleepDuration := time.Duration(1<<attempt) * time.Second
		if resetHeader != "" {
			if resetEpoch, parseErr := strconv.ParseInt(resetHeader, 10, 64); parseErr == nil {
				waitSec := time.Until(time.Unix(resetEpoch, 0))
				if waitSec > 0 && waitSec < 30*time.Second {
					sleepDuration = waitSec
				}
			}
		}

		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(sleepDuration):
		}
	}

	return resp, err
}

func (c *Client) parseAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var apiErr TwitterAPIError
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Title != "" {
		apiErr.StatusCode = resp.StatusCode
		return &apiErr
	}

	return &TwitterAPIError{
		Title:      http.StatusText(resp.StatusCode),
		Detail:     string(body),
		StatusCode: resp.StatusCode,
	}
}
