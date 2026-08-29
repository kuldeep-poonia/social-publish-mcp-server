package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/instagram"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/twitter"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/youtube"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/mcp"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/queue"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/security"
)

func (s *HTTPServer) isAuthOrExpiredError(err error) bool {
	if err == nil {
		return false
	}
	// Do NOT treat account type or Facebook Page linking errors as an expired auth loop
	if errors.Is(err, instagram.ErrPersonalAccountNotSupported) || strings.Contains(strings.ToLower(err.Error()), "not an instagram business") {
		return false
	}
	msg := strings.ToLower(err.Error())
	return errors.Is(err, instagram.ErrPlatformNotConnected) ||
		errors.Is(err, instagram.ErrReauthenticationRequired) ||
		errors.Is(err, database.ErrNotFound) ||
		strings.Contains(msg, "token expired") ||
		strings.Contains(msg, "re-authentication required") ||
		strings.Contains(msg, "platform connection not found") ||
		strings.Contains(msg, "credentials not found")
}

func (s *HTTPServer) makeAuthRequiredToolResult(platform, userID string, rawErr error) *mcp.CallToolResult {
	baseURL := strings.TrimRight(s.cfg.PublicBaseURL, "/")
	if baseURL == "" {
		baseURL = "https://social-mcp.duckdns.org"
	}
	connectURL := fmt.Sprintf("%s/auth/%s/connect?user_id=%s", baseURL, platform, userID)
	payload := map[string]interface{}{
		"status":      "action_required",
		"error_type":  "reauthentication_required",
		"platform":    platform,
		"message":     fmt.Sprintf("Your %s account is not connected or the login session has expired.", platform),
		"connect_url": connectURL,
		"instruction": fmt.Sprintf("IMPORTANT FOR AI: You MUST tell the user to click this exact link to connect their %s account: [Click Here to Connect %s](%s) — Once they authorize, retry the publish/insights request.", strings.Title(platform), strings.Title(platform), connectURL),
	}
	b, _ := json.Marshal(payload)
	return &mcp.CallToolResult{
		Content: []mcp.ToolContent{
			{Type: "text", Text: string(b)},
		},
		IsError: false,
	}
}

func (s *HTTPServer) registerMCPToolHandlers() {
	publishHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
		actor := database.GetActor(ctx)
		if actor.ActorID == "" || actor.ActorID == "anonymous" {
			return nil, errors.New("unauthorized: authenticated user session required to publish")
		}

		actualUserID := actor.ActorID
		if _, err := uuid.Parse(actualUserID); err != nil && s.repo != nil {
			user, userErr := s.repo.GetOrCreateUserByUsername(ctx, actualUserID, fmt.Sprintf("%s@example.com", actualUserID))
			if userErr == nil && user != nil {
				actualUserID = user.ID
			}
		}
		ctx = database.WithActor(ctx, database.ActorContext{ActorID: actualUserID, IPAddress: actor.IPAddress})

		platform, _ := args["platform"].(string)
		content, _ := args["content"].(string)
		idempotencyKey, _ := args["idempotency_key"].(string)

		var mediaURLs []string
		if rawURLs, ok := args["media_urls"].([]interface{}); ok {
			for _, u := range rawURLs {
				if str, ok := u.(string); ok && strings.TrimSpace(str) != "" {
					if strings.HasPrefix(strings.ToLower(str), "http://") || strings.HasPrefix(strings.ToLower(str), "https://") {
						if _, valErr := security.ValidateMediaURL(str); valErr != nil {
							return nil, fmt.Errorf("invalid or blocked media URL: %w", valErr)
						}
					}
					mediaURLs = append(mediaURLs, str)
				}
			}
		}

		switch platform {
		case "twitter":
			if s.twitterService == nil {
				return nil, errors.New("twitter service is not initialized")
			}

			resp, err := s.twitterService.PublishTweet(ctx, &twitter.PublishTweetRequest{
				UserID:         actualUserID,
				Content:        content,
				MediaURLs:      mediaURLs,
				IdempotencyKey: idempotencyKey,
			})
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("twitter", actualUserID, err), nil
				}
				if isTransient, cat := queue.ClassifyError(err); isTransient && s.streamQueue != nil {
					_ = s.streamQueue.Enqueue(ctx, &queue.PublishJob{
						ID:             uuid.New().String(),
						UserID:         actualUserID,
						Platform:       "twitter",
						Caption:        content,
						MediaURLs:      mediaURLs,
						IdempotencyKey: idempotencyKey,
						AttemptCount:   1,
						MaxRetries:     s.cfg.QueueMaxRetries,
						CreatedAt:      time.Now().UTC(),
					})
					retryResp := map[string]interface{}{
						"status":          "queued_for_retry",
						"platform":        "twitter",
						"idempotency_key": idempotencyKey,
						"reason":          err.Error(),
						"error_category":  string(cat),
						"retry_attempt":   1,
						"instruction":     "Initial attempt met transient upstream resistance. Background worker is retrying automatically with exponential backoff.",
					}
					b, _ := json.Marshal(retryResp)
					return &mcp.CallToolResult{
						Content: []mcp.ToolContent{
							{Type: "text", Text: string(b)},
						},
						IsError: false,
					}, nil
				}
				return nil, err
			}

			resultJSON, _ := json.Marshal(resp)
			return &mcp.CallToolResult{
				Content: []mcp.ToolContent{
					{Type: "text", Text: string(resultJSON)},
				},
				IsError: false,
			}, nil

		case "youtube":
			if s.youtubeService == nil {
				return nil, errors.New("youtube service is not initialized")
			}

			title, _ := args["title"].(string)
			if title == "" {
				title = content
			}
			description, _ := args["description"].(string)
			if description == "" {
				description = content
			}
			privacyStatus, _ := args["privacy_status"].(string)
			if privacyStatus == "" {
				privacyStatus = "public"
			}
			mediaPath, _ := args["media_path"].(string)

			var videoBytes []byte
			if rawData, ok := args["media_data"].(string); ok && len(rawData) > 0 {
				decoded, decErr := base64.StdEncoding.DecodeString(rawData)
				if decErr != nil {
					return nil, fmt.Errorf("invalid base64 encoding in media_data: %w", decErr)
				}
				videoBytes = decoded
			} else if len(mediaURLs) > 0 && mediaURLs[0] != "" {
				if strings.HasPrefix(strings.ToLower(mediaURLs[0]), "http://") || strings.HasPrefix(strings.ToLower(mediaURLs[0]), "https://") {
					fetchedBytes, _, fetchErr := security.FetchMediaWithSSRFProtection(ctx, mediaURLs[0], 500*1024*1024)
					if fetchErr != nil {
						return nil, fmt.Errorf("failed fetching remote video URL with SSRF protection: %w", fetchErr)
					}
					videoBytes = fetchedBytes
				} else {
					data, readErr := os.ReadFile(mediaURLs[0])
					if readErr == nil {
						videoBytes = data
					}
				}
			} else if mediaPath != "" {
				readBytes, readErr := os.ReadFile(mediaPath)
				if readErr != nil {
					return nil, fmt.Errorf("failed reading local media file: %w", readErr)
				}
				videoBytes = readBytes
			}

			if len(videoBytes) == 0 {
				return nil, errors.New("missing valid video media (must provide media_urls, media_data, or media_path)")
			}

			resp, err := s.youtubeService.PublishVideo(ctx, &youtube.PublishVideoRequest{
				UserID:         actualUserID,
				Title:          title,
				Description:    description,
				PrivacyStatus:  privacyStatus,
				VideoReader:    bytes.NewReader(videoBytes),
				TotalBytes:     int64(len(videoBytes)),
				IdempotencyKey: idempotencyKey,
			})
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("youtube", actualUserID, err), nil
				}
				if isTransient, cat := queue.ClassifyError(err); isTransient && s.streamQueue != nil {
					_ = s.streamQueue.Enqueue(ctx, &queue.PublishJob{
						ID:             uuid.New().String(),
						UserID:         actualUserID,
						Platform:       "youtube",
						Caption:        title,
						MediaPath:      mediaPath,
						MediaData:      videoBytes,
						PrivacyStatus:  privacyStatus,
						IdempotencyKey: idempotencyKey,
						AttemptCount:   1,
						MaxRetries:     s.cfg.QueueMaxRetries,
						CreatedAt:      time.Now().UTC(),
					})
					retryResp := map[string]interface{}{
						"status":          "queued_for_retry",
						"platform":        "youtube",
						"idempotency_key": idempotencyKey,
						"reason":          err.Error(),
						"error_category":  string(cat),
						"retry_attempt":   1,
						"instruction":     "Initial attempt met transient upstream resistance. Background worker is retrying automatically with exponential backoff.",
					}
					b, _ := json.Marshal(retryResp)
					return &mcp.CallToolResult{
						Content: []mcp.ToolContent{
							{Type: "text", Text: string(b)},
						},
						IsError: false,
					}, nil
				}
				return nil, err
			}

			resultJSON, _ := json.Marshal(resp)
			return &mcp.CallToolResult{
				Content: []mcp.ToolContent{
					{Type: "text", Text: string(resultJSON)},
				},
				IsError: false,
			}, nil

		case "instagram":
			if s.instagramService == nil {
				return nil, errors.New("instagram service is not initialized")
			}

			caption, _ := args["caption"].(string)
			if caption == "" {
				caption = content
			}
			mediaType, _ := args["media_type"].(string)
			mediaPath, _ := args["media_path"].(string)
			imagePrompt, _ := args["image_prompt"].(string)

			var mediaData []byte
			if rawData, ok := args["media_data"].(string); ok && len(rawData) > 0 {
				decoded, decErr := base64.StdEncoding.DecodeString(rawData)
				if decErr != nil {
					return nil, fmt.Errorf("invalid base64 encoding in media_data: %w", decErr)
				}
				mediaData = decoded
			}

			resp, err := s.instagramService.Publish(ctx, &instagram.PublishPostRequest{
				UserID:         actualUserID,
				Caption:        caption,
				MediaURLs:      mediaURLs,
				MediaPath:      mediaPath,
				MediaData:      mediaData,
				MediaType:      mediaType,
				ImagePrompt:    imagePrompt,
				IdempotencyKey: idempotencyKey,
			})
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("instagram", actualUserID, err), nil
				}
				if isTransient, cat := queue.ClassifyError(err); isTransient && s.streamQueue != nil {
					_ = s.streamQueue.Enqueue(ctx, &queue.PublishJob{
						ID:             uuid.New().String(),
						UserID:         actualUserID,
						Platform:       "instagram",
						Caption:        caption,
						MediaURLs:      mediaURLs,
						MediaPath:      mediaPath,
						MediaData:      mediaData,
						MediaType:      mediaType,
						IdempotencyKey: idempotencyKey,
						AttemptCount:   1,
						MaxRetries:     s.cfg.QueueMaxRetries,
						CreatedAt:      time.Now().UTC(),
					})
					retryResp := map[string]interface{}{
						"status":          "queued_for_retry",
						"platform":        "instagram",
						"idempotency_key": idempotencyKey,
						"reason":          err.Error(),
						"error_category":  string(cat),
						"retry_attempt":   1,
						"instruction":     "Initial attempt met transient upstream resistance. Background worker is retrying automatically with exponential backoff.",
					}
					b, _ := json.Marshal(retryResp)
					return &mcp.CallToolResult{
						Content: []mcp.ToolContent{
							{Type: "text", Text: string(b)},
						},
						IsError: false,
					}, nil
				}
				return nil, err
			}

			resultJSON, _ := json.Marshal(resp)
			return &mcp.CallToolResult{
				Content: []mcp.ToolContent{
					{Type: "text", Text: string(resultJSON)},
				},
				IsError: false,
			}, nil

		default:
			return nil, fmt.Errorf("platform '%s' is not supported in current release (supported: 'twitter', 'youtube', 'instagram')", platform)
		}
	}

	analyticsHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
		actor := database.GetActor(ctx)
		if actor.ActorID == "" || actor.ActorID == "anonymous" {
			return nil, errors.New("unauthorized: authenticated user session required for analytics")
		}

		actualUserID := actor.ActorID
		if _, err := uuid.Parse(actualUserID); err != nil && s.repo != nil {
			user, userErr := s.repo.GetOrCreateUserByUsername(ctx, actualUserID, fmt.Sprintf("%s@example.com", actualUserID))
			if userErr == nil && user != nil {
				actualUserID = user.ID
			}
		}
		ctx = database.WithActor(ctx, database.ActorContext{ActorID: actualUserID, IPAddress: actor.IPAddress})

		platform, _ := args["platform"].(string)
		postID, _ := args["post_id"].(string)

		switch platform {
		case "twitter":
			accessBytes, refreshBytes, _, scopes, err := s.repo.GetDecryptedPlatformConnection(ctx, actualUserID, "twitter")
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("twitter", actualUserID, err), nil
				}
				return nil, fmt.Errorf("failed retrieving Twitter credentials: %w", err)
			}

			metrics, err := s.twitterClient.GetTweetAnalytics(ctx, string(accessBytes), postID)
			if err != nil && len(refreshBytes) > 0 && s.twitterClient != nil {
				if newTok, refErr := s.twitterClient.RefreshToken(ctx, string(refreshBytes)); refErr == nil {
					_ = s.repo.SavePlatformConnection(ctx, actualUserID, "twitter", []byte(newTok.AccessToken), []byte(newTok.RefreshToken), time.Now().Add(time.Duration(newTok.ExpiresIn)*time.Second), scopes)
					metrics, err = s.twitterClient.GetTweetAnalytics(ctx, newTok.AccessToken, postID)
				}
			}
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("twitter", actualUserID, err), nil
				}
				return nil, fmt.Errorf("failed retrieving Twitter metrics: %w", err)
			}

			resultJSON, _ := json.Marshal(metrics)
			return &mcp.CallToolResult{
				Content: []mcp.ToolContent{
					{Type: "text", Text: string(resultJSON)},
				},
				IsError: false,
			}, nil

		case "youtube":
			accessBytes, refreshBytes, _, scopes, err := s.repo.GetDecryptedPlatformConnection(ctx, actualUserID, "youtube")
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("youtube", actualUserID, err), nil
				}
				return nil, fmt.Errorf("failed retrieving YouTube credentials: %w", err)
			}

			metrics, err := s.youtubeClient.GetVideoAnalytics(ctx, string(accessBytes), postID)
			if err != nil && len(refreshBytes) > 0 && s.youtubeClient != nil {
				if newTokens, refErr := s.youtubeClient.RefreshToken(ctx, string(refreshBytes)); refErr == nil {
					_ = s.repo.SavePlatformConnection(ctx, actualUserID, "youtube", []byte(newTokens.AccessToken), []byte(newTokens.RefreshToken), time.Now().Add(time.Duration(newTokens.ExpiresIn)*time.Second), scopes)
					metrics, err = s.youtubeClient.GetVideoAnalytics(ctx, newTokens.AccessToken, postID)
				}
			}
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("youtube", actualUserID, err), nil
				}
				return nil, fmt.Errorf("failed retrieving YouTube video analytics: %w", err)
			}

			resultJSON, _ := json.Marshal(metrics)
			return &mcp.CallToolResult{
				Content: []mcp.ToolContent{
					{Type: "text", Text: string(resultJSON)},
				},
				IsError: false,
			}, nil

		case "instagram":
			if s.instagramService == nil {
				return nil, errors.New("instagram service is not initialized")
			}

			metrics, err := s.instagramService.GetAnalytics(ctx, actualUserID, postID)
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("instagram", actualUserID, err), nil
				}
				return nil, fmt.Errorf("failed retrieving Instagram insights: %w", err)
			}

			resultJSON, _ := json.Marshal(metrics)
			return &mcp.CallToolResult{
				Content: []mcp.ToolContent{
					{Type: "text", Text: string(resultJSON)},
				},
				IsError: false,
			}, nil

		default:
			return nil, fmt.Errorf("platform '%s' is not supported in current release (supported: 'twitter', 'youtube', 'instagram')", platform)
		}
	}

	connectHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
		platform, _ := args["platform"].(string)
		actor := database.GetActor(ctx)
		userID := actor.ActorID
		if userID == "" || userID == "anonymous" {
			userID = "test_user_1"
		}

		getConnectURL := func(plt, uid string) string {
			baseURL := strings.TrimRight(s.cfg.PublicBaseURL, "/")
			if baseURL == "" {
				baseURL = fmt.Sprintf("http://localhost:%d", s.cfg.ServerPort)
			}
			return fmt.Sprintf("%s/auth/%s/connect?user_id=%s", baseURL, plt, uid)
		}

		switch platform {
		case "twitter":
			connectURL := getConnectURL("twitter", userID)
			payload := map[string]string{
				"platform":    "twitter",
				"connect_url": connectURL,
				"status":      "action_required",
				"instruction": "Open connect_url in your web browser to authenticate Twitter and save tokens into vault",
			}
			b, _ := json.Marshal(payload)
			return &mcp.CallToolResult{
				Content: []mcp.ToolContent{
					{Type: "text", Text: string(b)},
				},
				IsError: false,
			}, nil

		case "youtube":
			connectURL := getConnectURL("youtube", userID)
			payload := map[string]string{
				"platform":    "youtube",
				"connect_url": connectURL,
				"status":      "action_required",
				"instruction": "Open connect_url in your web browser to authenticate Google YouTube and save tokens into vault",
			}
			b, _ := json.Marshal(payload)
			return &mcp.CallToolResult{
				Content: []mcp.ToolContent{
					{Type: "text", Text: string(b)},
				},
				IsError: false,
			}, nil

		case "instagram":
			connectURL := getConnectURL("instagram", userID)
			payload := map[string]string{
				"platform":    "instagram",
				"connect_url": connectURL,
				"status":      "action_required",
				"instruction": "Open connect_url in your web browser to authenticate Meta Instagram Business and save tokens into vault",
			}
			b, _ := json.Marshal(payload)
			return &mcp.CallToolResult{
				Content: []mcp.ToolContent{
					{Type: "text", Text: string(b)},
				},
				IsError: false,
			}, nil

		default:
			return nil, fmt.Errorf("platform '%s' connection is not supported yet (supported: 'twitter', 'youtube', 'instagram')", platform)
		}
	}

	accountInsightsHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
		actor := database.GetActor(ctx)
		if actor.ActorID == "" || actor.ActorID == "anonymous" {
			return nil, errors.New("unauthorized: authenticated user session required for account insights")
		}

		actualUserID := actor.ActorID
		if _, err := uuid.Parse(actualUserID); err != nil && s.repo != nil {
			user, userErr := s.repo.GetOrCreateUserByUsername(ctx, actualUserID, fmt.Sprintf("%s@example.com", actualUserID))
			if userErr == nil && user != nil {
				actualUserID = user.ID
			}
		}
		ctx = database.WithActor(ctx, database.ActorContext{ActorID: actualUserID, IPAddress: actor.IPAddress})

		platform, _ := args["platform"].(string)
		period, _ := args["time_period"].(string)
		if period == "" {
			period = "days_28"
		}

		if s.repo == nil {
			return nil, errors.New("database repository is not initialized")
		}

		switch platform {
		case "instagram":
			accessBytes, _, _, _, err := s.repo.GetDecryptedPlatformConnection(ctx, actualUserID, "instagram")
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("instagram", actualUserID, err), nil
				}
				return nil, fmt.Errorf("failed retrieving Instagram connection: %w (please connect Instagram via connect_platform tool first)", err)
			}

			// Retrieve linked IG User ID
			igAccount, pageAccessToken, accErr := s.instagramClient.GetInstagramBusinessAccount(ctx, string(accessBytes))
			if accErr != nil || igAccount == nil {
				if s.isAuthOrExpiredError(accErr) {
					return s.makeAuthRequiredToolResult("instagram", actualUserID, accErr), nil
				}
				return nil, fmt.Errorf("failed retrieving Instagram business account: %v", accErr)
			}
			igUserID := igAccount.ID
			tokenToUse := string(accessBytes)
			if pageAccessToken != "" {
				tokenToUse = pageAccessToken
			}

			insights, err := s.instagramClient.GetAggregatedAccountInsights(ctx, igUserID, tokenToUse, period)
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("instagram", actualUserID, err), nil
				}
				return nil, fmt.Errorf("failed fetching Instagram account insights: %w", err)
			}

			resultJSON, _ := json.Marshal(insights)
			return &mcp.CallToolResult{
				Content: []mcp.ToolContent{
					{Type: "text", Text: string(resultJSON)},
				},
				IsError: false,
			}, nil

		case "youtube":
			accessBytes, refreshBytes, _, scopes, err := s.repo.GetDecryptedPlatformConnection(ctx, actualUserID, "youtube")
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("youtube", actualUserID, err), nil
				}
				return nil, fmt.Errorf("failed retrieving YouTube connection: %w (please connect YouTube via connect_platform tool first)", err)
			}

			insights, err := s.youtubeClient.GetChannelInsights(ctx, string(accessBytes))
			if err != nil && len(refreshBytes) > 0 && s.youtubeClient != nil {
				if newTok, refErr := s.youtubeClient.RefreshToken(ctx, string(refreshBytes)); refErr == nil {
					_ = s.repo.SavePlatformConnection(ctx, actualUserID, "youtube", []byte(newTok.AccessToken), []byte(newTok.RefreshToken), time.Now().Add(time.Duration(newTok.ExpiresIn)*time.Second), scopes)
					insights, err = s.youtubeClient.GetChannelInsights(ctx, newTok.AccessToken)
				}
			}
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("youtube", actualUserID, err), nil
				}
				return nil, fmt.Errorf("failed fetching YouTube channel insights: %w", err)
			}

			resultJSON, _ := json.Marshal(insights)
			return &mcp.CallToolResult{
				Content: []mcp.ToolContent{
					{Type: "text", Text: string(resultJSON)},
				},
				IsError: false,
			}, nil

		case "twitter":
			accessBytes, refreshBytes, _, scopes, err := s.repo.GetDecryptedPlatformConnection(ctx, actualUserID, "twitter")
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("twitter", actualUserID, err), nil
				}
				return nil, fmt.Errorf("failed retrieving Twitter connection: %w (please connect Twitter via connect_platform tool first)", err)
			}

			insights, err := s.twitterClient.GetAccountInsights(ctx, string(accessBytes))
			if err != nil && len(refreshBytes) > 0 && s.twitterClient != nil {
				if newTok, refErr := s.twitterClient.RefreshToken(ctx, string(refreshBytes)); refErr == nil {
					_ = s.repo.SavePlatformConnection(ctx, actualUserID, "twitter", []byte(newTok.AccessToken), []byte(newTok.RefreshToken), time.Now().Add(time.Duration(newTok.ExpiresIn)*time.Second), scopes)
					insights, err = s.twitterClient.GetAccountInsights(ctx, newTok.AccessToken)
				}
			}
			if err != nil {
				if s.isAuthOrExpiredError(err) {
					return s.makeAuthRequiredToolResult("twitter", actualUserID, err), nil
				}
				return nil, fmt.Errorf("failed fetching Twitter account insights: %w", err)
			}

			resultJSON, _ := json.Marshal(insights)
			return &mcp.CallToolResult{
				Content: []mcp.ToolContent{
					{Type: "text", Text: string(resultJSON)},
				},
				IsError: false,
			}, nil

		default:
			return nil, fmt.Errorf("platform '%s' is not supported for account insights (supported: 'instagram', 'youtube', 'twitter')", platform)
		}
	}

	optimizeContentHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
		actor := database.GetActor(ctx)
		if actor.ActorID == "" || actor.ActorID == "anonymous" {
			return nil, errors.New("unauthorized: authenticated user session required for content optimization")
		}

		actualUserID := actor.ActorID
		if _, err := uuid.Parse(actualUserID); err != nil && s.repo != nil {
			user, userErr := s.repo.GetOrCreateUserByUsername(ctx, actualUserID, fmt.Sprintf("%s@example.com", actualUserID))
			if userErr == nil && user != nil {
				actualUserID = user.ID
			}
		}

		platform, _ := args["platform"].(string)
		targetType, _ := args["target_type"].(string)
		topicOrDraft, _ := args["topic_or_draft"].(string)
		niche, _ := args["niche"].(string)
		audience, _ := args["target_audience"].(string)
		postID, _ := args["post_id"].(string)
		applyUpdate, _ := args["apply_update"].(bool)

		if platform == "" || topicOrDraft == "" {
			return nil, errors.New("platform and topic_or_draft are required parameters")
		}

		hooks := []string{
			fmt.Sprintf("Stop making this %s mistake right now (here is what works in 2026):", niche),
			fmt.Sprintf("The secret %s framework nobody talks about:", niche),
			fmt.Sprintf("How I scaled %s reach by 300%% in 28 days:", niche),
		}

		seoOptimization := map[string]interface{}{
			"platform":                    platform,
			"target_type":                 targetType,
			"viral_hook_variations":       hooks,
			"high_ranking_tags":           []string{"growth", "algorithm2026", "contentstrategy", strings.ToLower(niche), "viral"},
			"optimized_description":       fmt.Sprintf("%s\n\n🎯 Target Audience: %s\n📈 Pro-tip: Focus on high-retention storytelling.", topicOrDraft, audience),
			"recommended_hashtags":        fmt.Sprintf("#%s #growth #creator #trending #viral #%s", strings.ToLower(platform), strings.ToLower(strings.ReplaceAll(niche, " ", ""))),
			"posting_time_recommendation": "Best posting windows: 8:00-9:30 AM (commute) or 6:00-8:30 PM (evening peak local time).",
			"optimization_applied":        false,
		}

		if platform == "youtube" && postID != "" && applyUpdate {
			accessBytes, refreshBytes, _, scopes, err := s.repo.GetDecryptedPlatformConnection(ctx, actualUserID, "youtube")
			if err != nil {
				return nil, fmt.Errorf("failed retrieving YouTube credentials for update: %w", err)
			}

			updateParams := &youtube.UpdateVideoMetadataParams{
				VideoID:     postID,
				Title:       topicOrDraft,
				Description: fmt.Sprintf("%s\n\nFollow for more updates!\n%s", topicOrDraft, seoOptimization["recommended_hashtags"]),
				Tags:        []string{"growth", "trending", "viral", strings.ToLower(niche)},
			}

			updatedMetrics, err := s.youtubeClient.UpdateVideoMetadata(ctx, string(accessBytes), updateParams)
			if err != nil && len(refreshBytes) > 0 && s.youtubeClient != nil {
				if newTok, refErr := s.youtubeClient.RefreshToken(ctx, string(refreshBytes)); refErr == nil {
					_ = s.repo.SavePlatformConnection(ctx, actualUserID, "youtube", []byte(newTok.AccessToken), []byte(newTok.RefreshToken), time.Now().Add(time.Duration(newTok.ExpiresIn)*time.Second), scopes)
					updatedMetrics, err = s.youtubeClient.UpdateVideoMetadata(ctx, newTok.AccessToken, updateParams)
				}
			}
			if err != nil {
				return nil, fmt.Errorf("failed applying live YouTube metadata update: %w", err)
			}

			seoOptimization["optimization_applied"] = true
			seoOptimization["live_updated_video"] = updatedMetrics
		}

		resultJSON, _ := json.Marshal(seoOptimization)
		return &mcp.CallToolResult{
			Content: []mcp.ToolContent{
				{Type: "text", Text: string(resultJSON)},
			},
			IsError: false,
		}, nil
	}

	uploadHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
		rawBase64, _ := args["media_data"].(string)
		if rawBase64 == "" {
			return nil, errors.New("media_data (base64 string) is required")
		}
		fileName, _ := args["file_name"].(string)
		ext := filepath.Ext(fileName)
		if ext == "" {
			ext = "jpg"
		}
		decoded, err := base64.StdEncoding.DecodeString(rawBase64)
		if err != nil {
			return nil, fmt.Errorf("invalid base64 encoding: %w", err)
		}
		if s.mediaStager == nil {
			return nil, errors.New("media stager is not initialized")
		}
		publicURL, token, _, stageErr := s.mediaStager.StageMedia(decoded, ext, "image/jpeg")
		if stageErr != nil {
			return nil, fmt.Errorf("failed staging media: %w", stageErr)
		}
		resp := map[string]interface{}{
			"status":      "staged",
			"public_url":  publicURL,
			"token":       token,
			"instruction": "Pass this public_url into publish_post media_urls to publish directly to Instagram/Twitter/YouTube",
		}
		b, _ := json.Marshal(resp)
		return &mcp.CallToolResult{
			Content: []mcp.ToolContent{
				{Type: "text", Text: string(b)},
			},
			IsError: false,
		}, nil
	}

	s.mcpServer.RegisterSocialTools(publishHandler, analyticsHandler, connectHandler, uploadHandler)
	s.mcpServer.RegisterInsightsAndOptimizationTools(accountInsightsHandler, optimizeContentHandler)
}

func (s *HTTPServer) handleBackgroundPublishRetry(ctx context.Context, job *queue.PublishJob) error {
	if job == nil {
		return errors.New("job cannot be nil")
	}

	switch job.Platform {
	case "twitter":
		if s.twitterService == nil {
			return errors.New("twitter service not initialized")
		}
		_, err := s.twitterService.PublishTweet(ctx, &twitter.PublishTweetRequest{
			UserID:         job.UserID,
			Content:        job.Caption,
			MediaURLs:      job.MediaURLs,
			IdempotencyKey: job.IdempotencyKey,
		})
		return err

	case "youtube":
		if s.youtubeService == nil {
			return errors.New("youtube service not initialized")
		}
		var videoBytes []byte
		if len(job.MediaData) > 0 {
			videoBytes = job.MediaData
		} else if job.MediaPath != "" {
			data, readErr := os.ReadFile(job.MediaPath)
			if readErr != nil {
				return fmt.Errorf("failed reading video file from media_path '%s': %w", job.MediaPath, readErr)
			}
			videoBytes = data
		}

		if len(videoBytes) == 0 {
			return errors.New("missing valid video payload for youtube background retry")
		}

		_, err := s.youtubeService.PublishVideo(ctx, &youtube.PublishVideoRequest{
			UserID:         job.UserID,
			Title:          job.Caption,
			Description:    job.Caption,
			PrivacyStatus:  job.PrivacyStatus,
			VideoReader:    bytes.NewReader(videoBytes),
			TotalBytes:     int64(len(videoBytes)),
			IdempotencyKey: job.IdempotencyKey,
		})
		return err

	case "instagram":
		if s.instagramService == nil {
			return errors.New("instagram service not initialized")
		}
		_, err := s.instagramService.Publish(ctx, &instagram.PublishPostRequest{
			UserID:         job.UserID,
			Caption:        job.Caption,
			MediaURLs:      job.MediaURLs,
			MediaPath:      job.MediaPath,
			MediaData:      job.MediaData,
			MediaType:      job.MediaType,
			IdempotencyKey: job.IdempotencyKey,
		})
		return err

	default:
		return fmt.Errorf("unsupported platform for retry: %s", job.Platform)
	}
}
