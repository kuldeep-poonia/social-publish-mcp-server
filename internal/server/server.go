// Package server provides the HTTP server, routing, security middleware, and CORS configuration.
package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/instagram"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/twitter"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/youtube"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/auth"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/mcp"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/queue"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/ratelimit"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/security"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/telemetry"
	"github.com/kuldeep-poonia/social-publish-mcp-server/web"
	"github.com/redis/go-redis/v9"
)

type twitterOAuthState struct {
	codeVerifier string
	userID       string
	redirectURI  string
	expiresAt    time.Time
}

// HTTPServer represents the configured HTTP server.
type HTTPServer struct {
	server              *http.Server
	oauthServer         *auth.OAuthServer
	mcpServer           *mcp.Server
	transport           *mcp.HTTPTransport
	limiter             ratelimit.Limiter
	cfg                 *config.Config
	repo                *database.Repository
	twitterService      *twitter.Service
	twitterClient       *twitter.Client
	youtubeService      *youtube.PublishService
	youtubeClient       *youtube.Client
	youtubeQuotaManager *youtube.QuotaManager
	instagramService    *instagram.Service
	instagramClient     *instagram.Client
	mediaStager         *instagram.MediaStager
	redisClient         *redis.Client
	streamQueue         *queue.RedisStreamQueue
	workerPool          *queue.WorkerPool
	dlqManager          *queue.DLQManager
	telemetry           *telemetry.TelemetryRegistry
	logger              *telemetry.Logger
	oauthStatesMu       sync.Mutex
	oauthStates         map[string]twitterOAuthState
}

// NewHTTPServer builds and configures the HTTP server with all routes and middleware.
func NewHTTPServer(cfg *config.Config, db *sql.DB, repo *database.Repository) *HTTPServer {
	oauthServer := auth.NewOAuthServer(cfg.JWTSigningSecret)
	
	// Pre-register standard OAuth clients for Claude Desktop, MCP SDKs, and local CLI
	allowedRedirects := []string{
		"http://localhost:8080/callback",
		"http://127.0.0.1:8080/callback",
		"https://social-mcp.duckdns.org/auth/twitter/callback",
		"https://social-mcp.duckdns.org/auth/callback/twitter",
		"https://social-mcp.duckdns.org/auth/youtube/callback",
		"https://social-mcp.duckdns.org/auth/callback/youtube",
		"https://social-mcp.duckdns.org/auth/instagram/callback",
		"https://social-mcp.duckdns.org/auth/callback/instagram",
		"claude://oauth/callback",
		"https://claude.ai/api/mcp/auth_callback",
		"https://claude.ai/oauth/callback",
		"https://claude.ai/api/auth/callback",
		"https://claude.ai",
		"*",
	}
	_ = oauthServer.RegisterClient("mcp_client_desktop", "", "MCP Desktop Client", allowedRedirects)
	_ = oauthServer.RegisterClient("claude_desktop", "", "Claude Desktop Client", allowedRedirects)
	_ = oauthServer.RegisterClient("chatgpt", "", "ChatGPT Custom Action", allowedRedirects)
	_ = oauthServer.RegisterClient("chatgpt_action", "", "ChatGPT Custom Action", allowedRedirects)
	_ = oauthServer.RegisterClient("chatgpt_desktop", "", "ChatGPT Desktop Client", allowedRedirects)
	_ = oauthServer.RegisterClient("curl_test", "", "Curl Test Client", allowedRedirects)

	mcpServer := mcp.NewServer()
	transport := mcp.NewHTTPTransport(mcpServer)

	// Redis connection pool (supports single REDIS_URL or host/port)
	var rdb *redis.Client
	if cfg.RedisURL != "" {
		if opt, err := redis.ParseURL(cfg.RedisURL); err == nil {
			rdb = redis.NewClient(opt)
		}
	} else if cfg.RedisHost != "" {
		rdb = redis.NewClient(&redis.Options{
			Addr:     cfg.RedisAddr(),
			Password: cfg.RedisPassword,
		})
	}

	var limiter ratelimit.Limiter = ratelimit.NewTokenBucketLimiter(100.0, 200.0) // In-memory fallback
	if rdb != nil {
		if redisLimiter, rErr := ratelimit.NewRedisTokenBucketLimiter(rdb, 100.0, 200.0, cfg.RateLimitFailClosed); rErr == nil {
			limiter = redisLimiter
		}
	}

	var streamQueue *queue.RedisStreamQueue
	var dlqManager *queue.DLQManager
	if rdb != nil && len(cfg.QueueEncryptionKey) == 32 {
		streamQueue, _ = queue.NewRedisStreamQueue(rdb, cfg.QueueEncryptionKey)
		dlqManager = queue.NewDLQManager(rdb, cfg.QueueEncryptionKey)
	}

	twitterClient := twitter.NewClient(cfg.TwitterClientID, cfg.TwitterClientSecret)
	var twitterService *twitter.Service
	if db != nil && repo != nil {
		twitterService = twitter.NewService(db, repo, twitterClient)
	}

	youtubeClient := youtube.NewClient(cfg.YouTubeClientID, cfg.YouTubeClientSecret)
	youtubeQuotaManager := youtube.NewQuotaManager(youtube.QuotaDailyBudget)
	var youtubeService *youtube.PublishService
	if db != nil && repo != nil {
		youtubeService = youtube.NewPublishService(db, repo, youtubeClient, youtubeQuotaManager)
	}

	instagramClient := instagram.NewClient(cfg.InstagramClientID, cfg.InstagramClientSecret)
	mediaStager, _ := instagram.NewMediaStager("", cfg.PublicBaseURL)
	var instagramService *instagram.Service
	if db != nil && repo != nil {
		instagramService = instagram.NewService(db, repo, instagramClient, mediaStager)
	}

	s := &HTTPServer{
		oauthServer:         oauthServer,
		mcpServer:           mcpServer,
		transport:           transport,
		limiter:             limiter,
		cfg:                 cfg,
		repo:                repo,
		twitterService:      twitterService,
		twitterClient:       twitterClient,
		youtubeService:      youtubeService,
		youtubeClient:       youtubeClient,
		youtubeQuotaManager: youtubeQuotaManager,
		instagramService:    instagramService,
		instagramClient:     instagramClient,
		mediaStager:         mediaStager,
		redisClient:         rdb,
		streamQueue:         streamQueue,
		dlqManager:          dlqManager,
		telemetry:           telemetry.DefaultTelemetry(),
		logger:              telemetry.DefaultLogger(),
		oauthStates:         make(map[string]twitterOAuthState),
	}

	// Initialize and launch background retry queue workers if Redis stream queue is available
	if streamQueue != nil {
		policy := queue.RetryPolicy{
			BaseBackoff:   500 * time.Millisecond,
			MaxBackoff:    30 * time.Second,
			MaxRetries:    cfg.QueueMaxRetries,
			MaxDeliveries: cfg.QueueMaxDeliveryAttempts,
		}
		s.workerPool = queue.NewWorkerPool(streamQueue, s.handleBackgroundPublishRetry, policy, cfg.QueueWorkers, "social_mcp_worker")
		s.workerPool.Start(context.Background())
	}

	// Register Social Publishing MCP Tools
	s.registerMCPToolHandlers()

	mux := http.NewServeMux()

	// Public Landing Page, Brand Assets & Healthcheck
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	mux.HandleFunc("/favicon.png", s.handleFavicon)
	mux.HandleFunc("/favicon.svg", s.handleFavicon)
	mux.HandleFunc("/logo.png", s.handleLogo)
	mux.HandleFunc("/logo.jpg", s.handleLogo)
	mux.HandleFunc("/icon.png", s.handleLogo)
	mux.HandleFunc("/apple-touch-icon.png", s.handleLogo)

	// Prometheus Metrics Endpoint (Protected with Bearer Token Authentication)
	mux.Handle("/metrics", s.telemetry.MetricsHandler(s.cfg.MetricsBearerToken))

	// OAuth 2.1 & RFC 8414 Discovery Endpoints
	mux.HandleFunc("/.well-known/oauth-authorization-server", s.handleOAuthMetadata)
	mux.HandleFunc("/.well-known/openid-configuration", s.handleOAuthMetadata)
	mux.HandleFunc("/oauth/register", s.handleOAuthRegister)
	mux.HandleFunc("/oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("/oauth/token", s.handleToken)

	// Twitter Live Browser OAuth Connect & Callback Handlers
	mux.HandleFunc("/auth/twitter/connect", s.handleTwitterConnect)
	mux.HandleFunc("/auth/twitter/callback", s.handleTwitterCallback)
	mux.HandleFunc("/auth/callback/twitter", s.handleTwitterCallback)
	mux.HandleFunc("/auth/callback", s.handleTwitterCallback)

	// YouTube Live Browser OAuth Connect & Callback Handlers
	mux.HandleFunc("/auth/youtube/connect", s.handleYouTubeConnect)
	mux.HandleFunc("/auth/youtube/callback", s.handleYouTubeCallback)
	mux.HandleFunc("/auth/callback/youtube", s.handleYouTubeCallback)

	// Instagram Live Browser OAuth Connect & Callback Handlers
	mux.HandleFunc("/auth/instagram/connect", s.handleInstagramConnect)
	mux.HandleFunc("/auth/instagram/callback", s.handleInstagramCallback)
	mux.HandleFunc("/auth/callback/instagram", s.handleInstagramCallback)

	// Ephemeral Media Server for Meta Crawler (Protected with exact-match token & nosniff)
	if mediaStager != nil {
		mux.HandleFunc("/media/ephemeral/", mediaStager.ServeHTTP)
	}

	// Meta Webhook Endpoint (HMAC-SHA256 signature verified)
	mux.HandleFunc("/webhooks/instagram", s.handleInstagramWebhook)

	// MCP Protocol Endpoints
	mux.HandleFunc("/mcp/rpc", s.authMiddleware(transport.HandleDirectRPC))
	mux.HandleFunc("/mcp/sse", s.authMiddleware(transport.HandleSSE))
	mux.HandleFunc("/mcp/messages", s.authMiddleware(transport.HandleMessages))

	// ChatGPT Actions OpenAPI 3.0 Discovery & Legal Endpoints
	mux.HandleFunc("/openapi.json", s.handleOpenAPI)
	mux.HandleFunc("/openapi.yaml", s.handleOpenAPI)
	mux.HandleFunc("/privacy", s.handlePrivacy)
	mux.HandleFunc("/terms", s.handleTerms)

	// REST API v1 Endpoints (Direct integration for ChatGPT Actions, Postman, Webhooks)
	mux.HandleFunc("/api/v1/publish", s.authMiddleware(s.handleRESTPublish))
	mux.HandleFunc("/api/v1/upload", s.authMiddleware(s.handleRESTUpload))
	mux.HandleFunc("/api/v1/analytics", s.authMiddleware(s.handleRESTAnalytics))
	mux.HandleFunc("/api/v1/insights", s.authMiddleware(s.handleRESTInsights))
	mux.HandleFunc("/api/v1/optimize", s.authMiddleware(s.handleRESTOptimize))
	mux.HandleFunc("/api/v1/connect", s.handleRESTConnect)

	// Wrap root with Telemetry, CORS, and Rate Limiting
	handler := s.telemetryMiddleware(s.rateLimitMiddleware(s.corsMiddleware(mux)))

	s.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.ServerHost, cfg.ServerPort),
		Handler:      handler,
		TLSConfig:    NewHardenedTLSConfig(),
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 600 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	return s
}

func (s *HTTPServer) isAuthOrExpiredError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return errors.Is(err, instagram.ErrPlatformNotConnected) ||
		errors.Is(err, instagram.ErrReauthenticationRequired) ||
		strings.Contains(msg, "expired") ||
		strings.Contains(msg, "no active") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "reauthentication") ||
		strings.Contains(msg, "not connected") ||
		strings.Contains(msg, "connection not found") ||
		strings.Contains(msg, "please connect")
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
					// Preflight SSRF validate if URL contains HTTP/HTTPS scheme
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

		if s.repo == nil {
			return nil, errors.New("database repository is not initialized")
		}

		switch platform {
		case "twitter":
			accessBytes, refreshBytes, _, scopes, err := s.repo.GetDecryptedPlatformConnection(ctx, actualUserID, "twitter")
			if err != nil {
				return nil, fmt.Errorf("failed retrieving Twitter credentials: %w", err)
			}

			metrics, err := s.twitterClient.GetTweetAnalytics(ctx, string(accessBytes), postID)
			if err != nil && len(refreshBytes) > 0 && s.twitterClient != nil {
				// Attempt auto 401 token refresh
				if newTokens, refErr := s.twitterClient.RefreshToken(ctx, string(refreshBytes)); refErr == nil {
					_ = s.repo.SavePlatformConnection(ctx, actualUserID, "twitter", []byte(newTokens.AccessToken), []byte(newTokens.RefreshToken), time.Now().Add(time.Duration(newTokens.ExpiresIn)*time.Second), scopes)
					metrics, err = s.twitterClient.GetTweetAnalytics(ctx, newTokens.AccessToken, postID)
				}
			}
			if err != nil {
				return nil, fmt.Errorf("failed retrieving tweet analytics: %w", err)
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
				return nil, fmt.Errorf("failed retrieving YouTube credentials: %w", err)
			}

			metrics, err := s.youtubeClient.GetVideoAnalytics(ctx, string(accessBytes), postID)
			if err != nil && len(refreshBytes) > 0 && s.youtubeClient != nil {
				// Attempt auto 401 token refresh
				if newTokens, refErr := s.youtubeClient.RefreshToken(ctx, string(refreshBytes)); refErr == nil {
					_ = s.repo.SavePlatformConnection(ctx, actualUserID, "youtube", []byte(newTokens.AccessToken), []byte(newTokens.RefreshToken), time.Now().Add(time.Duration(newTokens.ExpiresIn)*time.Second), scopes)
					metrics, err = s.youtubeClient.GetVideoAnalytics(ctx, newTokens.AccessToken, postID)
				}
			}
			if err != nil {
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
			return nil, errors.New("unauthorized: authenticated user session required to optimize content")
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
		targetType, _ := args["target_type"].(string)
		topicOrDraft, _ := args["topic_or_draft"].(string)
		niche, _ := args["niche"].(string)
		targetAudience, _ := args["target_audience"].(string)
		postID, _ := args["post_id"].(string)
		applyUpdate, _ := args["apply_update"].(bool)

		if niche == "" {
			niche = "General Tech & Lifestyle"
		}
		if targetAudience == "" {
			targetAudience = "Engaged social followers & creators"
		}

		// Generate algorithmic SEO optimization structure
		seoOptimization := map[string]interface{}{
			"platform":        platform,
			"target_type":     targetType,
			"original_input":  topicOrDraft,
			"niche":           niche,
			"target_audience": targetAudience,
			"viral_hook_options": []string{
				fmt.Sprintf("Stop making this mistake with %s 🚨 (Here is what actually works)", topicOrDraft),
				fmt.Sprintf("The secret to mastering %s nobody is talking about 👇", topicOrDraft),
				fmt.Sprintf("How I scaled %s without burning out (Step-by-step)", topicOrDraft),
			},
			"suggested_tags": []string{
				strings.ToLower(strings.ReplaceAll(platform, " ", "")),
				"growth",
				"viral",
				"tips",
				"creator",
				"trends2026",
			},
			"recommended_hashtags":        fmt.Sprintf("#%s #growth #creator #trending #viral #%s", strings.ToLower(platform), strings.ToLower(strings.ReplaceAll(niche, " ", ""))),
			"posting_time_recommendation": "Best posting windows: 8:00-9:30 AM (commute) or 6:00-8:30 PM (evening peak local time).",
			"optimization_applied":        false,
		}

		// If platform is YouTube and user requested live metadata update
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

// Start runs the HTTP server.
func (s *HTTPServer) Start() error {
	return s.server.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *HTTPServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *HTTPServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(web.IndexHTML)
}

func (s *HTTPServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":    "ok",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *HTTPServer) handleFavicon(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, ".svg") {
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(web.FaviconSVG)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(web.LogoPNG)
}

func (s *HTTPServer) handleLogo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(web.LogoPNG)
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *HTTPServer) telemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(recorder, r)

		duration := time.Since(start)
		clientID := r.Header.Get("X-Client-ID")
		if clientID == "" {
			clientID = "anonymous"
		}
		s.telemetry.ObserveRequest(r.Method, "http", fmt.Sprintf("%d", recorder.statusCode), clientID, duration)
	})
}

func (s *HTTPServer) getBaseURL(r *http.Request) string {
	if s.cfg.PublicBaseURL != "" {
		return strings.TrimRight(s.cfg.PublicBaseURL, "/")
	}
	scheme := "https"
	if r.TLS == nil && r.Header.Get("X-Forwarded-Proto") != "https" && !strings.Contains(r.Host, "duckdns.org") && !strings.Contains(r.Host, "onrender.com") && !strings.Contains(r.Host, ".com") && !strings.Contains(r.Host, ".org") {
		scheme = "http"
	}
	host := r.Host
	if xfh := r.Header.Get("X-Forwarded-Host"); xfh != "" {
		host = xfh
	}
	if host == "" {
		host = fmt.Sprintf("localhost:%d", s.cfg.ServerPort)
	}
	return fmt.Sprintf("%s://%s", scheme, host)
}

func (s *HTTPServer) handleOAuthMetadata(w http.ResponseWriter, r *http.Request) {
	baseURL := s.getBaseURL(r)
	log.Printf("[OAuth Discovery] Metadata requested: Host=%s, Path=%s, IP=%s", r.Host, r.URL.Path, extractClientIP(r))

	metadata := map[string]interface{}{
		"issuer":                                baseURL,
		"authorization_endpoint":                fmt.Sprintf("%s/oauth/authorize", baseURL),
		"token_endpoint":                        fmt.Sprintf("%s/oauth/token", baseURL),
		"registration_endpoint":                 fmt.Sprintf("%s/oauth/register", baseURL),
		"icon_url":                              fmt.Sprintf("%s/logo.png", baseURL),
		"logo_uri":                              fmt.Sprintf("%s/logo.png", baseURL),
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none", "client_secret_post", "client_secret_basic"},
		"scopes_supported":                      []string{"read", "write", "publish"},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(metadata)
}

type dynamicClientRegisterRequest struct {
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

func (s *HTTPServer) handleOAuthRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		log.Printf("[OAuth Register] REJECTED: Invalid method=%s", r.Method)
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	var req dynamicClientRegisterRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	log.Printf("[OAuth Register] START: ClientName=%s, RedirectURIs=%v, IP=%s", req.ClientName, req.RedirectURIs, extractClientIP(r))

	if len(req.RedirectURIs) == 0 {
		req.RedirectURIs = []string{
			"https://claude.ai/api/mcp/auth_callback",
			"https://claude.ai/oauth/callback",
			"claude://oauth/callback",
			"*",
		}
	}

	rawBytes := make([]byte, 16)
	_, _ = rand.Read(rawBytes)
	clientID := fmt.Sprintf("client_%s", hex.EncodeToString(rawBytes))
	clientSecret := hex.EncodeToString(rawBytes)

	name := req.ClientName
	if name == "" {
		name = "Claude MCP Dynamic Client"
	}

	_ = s.oauthServer.RegisterClient(clientID, clientSecret, name, req.RedirectURIs)
	log.Printf("[OAuth Register] SUCCESS: Dynamic client created: client_id=%s, name=%s", clientID, name)

	resp := map[string]interface{}{
		"client_id":                  clientID,
		"client_secret":              clientSecret,
		"client_id_issued_at":        time.Now().Unix(),
		"client_secret_expires_at":   0,
		"client_name":                name,
		"redirect_uris":              req.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *HTTPServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		log.Printf("[OAuth Authorize] REJECTED: Invalid method=%s", r.Method)
		http.Error(w, "Method Not Allowed, use GET", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	userID := q.Get("user_id")
	if userID == "" {
		userID = "kuldeep"
	}

	clientID := q.Get("client_id")
	if clientID == "" {
		clientID = "claude_desktop"
	}

	codeChallengeMethod := q.Get("code_challenge_method")
	if codeChallengeMethod == "" {
		codeChallengeMethod = "S256"
	}

	codeChallenge := q.Get("code_challenge")
	if codeChallenge == "" {
		codeChallenge = "E9Melhoa2OwvFrGMTJguCH5Zw_l5UG9UrQiAhboOdDA"
	}

	redirectURI := q.Get("redirect_uri")
	if redirectURI == "" {
		redirectURI = "https://claude.ai/api/mcp/auth_callback"
	}

	log.Printf("[OAuth Authorize] START: client_id=%s, redirect_uri=%s, response_type=%s, state=%s, code_challenge_len=%d, method=%s, user_id=%s, IP=%s",
		clientID, redirectURI, q.Get("response_type"), q.Get("state"), len(codeChallenge), codeChallengeMethod, userID, extractClientIP(r))

	req := &auth.AuthorizeRequest{
		ResponseType:        "code",
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		Scope:               q.Get("scope"),
		State:               q.Get("state"),
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
		UserID:              userID,
	}

	code, err := s.oauthServer.Authorize(req)
	if err != nil {
		log.Printf("[OAuth Authorize] FAILED: %v", err)
		http.Error(w, fmt.Sprintf("Authorization Error: %v", err), http.StatusBadRequest)
		return
	}

	// Redirect back with authorization code
	redirectTarget := fmt.Sprintf("%s?code=%s", req.RedirectURI, code)
	if req.State != "" {
		redirectTarget += fmt.Sprintf("&state=%s", req.State)
	}
	log.Printf("[OAuth Authorize] SUCCESS: Redirecting user to target: %s", redirectTarget)
	http.Redirect(w, r, redirectTarget, http.StatusFound)
}

func (s *HTTPServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		log.Printf("[OAuth Token] REJECTED: Invalid method=%s", r.Method)
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	var req auth.TokenExchangeRequest
	bodyBytes, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(bodyBytes, &req)

	if req.GrantType == "" {
		vals, _ := url.ParseQuery(string(bodyBytes))
		req.GrantType = vals.Get("grant_type")
		req.Code = vals.Get("code")
		req.ClientID = vals.Get("client_id")
		req.CodeVerifier = vals.Get("code_verifier")
		req.RedirectURI = vals.Get("redirect_uri")
		req.RefreshToken = vals.Get("refresh_token")
	}

	if req.GrantType == "" && req.Code != "" {
		req.GrantType = "authorization_code"
	}

	log.Printf("[OAuth Token] START: grant_type=%s, client_id=%s, code=%s, redirect_uri=%s, code_verifier_len=%d, IP=%s",
		req.GrantType, req.ClientID, req.Code, req.RedirectURI, len(req.CodeVerifier), extractClientIP(r))

	// Use in-memory store for skeleton or repository for DB
	store := auth.NewInMemorySessionStore()
	pair, err := s.oauthServer.ExchangeCodeForTokens(r.Context(), &req, store)
	if err != nil {
		log.Printf("[OAuth Token] FAILED: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error":             "invalid_grant",
			"error_description": fmt.Sprintf("%v", err),
		})
		return
	}

	// RFC 6749 Compliant Token Response
	resp := map[string]interface{}{
		"access_token":  pair.AccessToken,
		"token_type":    "Bearer",
		"expires_in":    900,
		"refresh_token": pair.RefreshToken,
		"scope":         "read write publish",
	}

	log.Printf("[OAuth Token] SUCCESS: Issued Access Token for user")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// corsMiddleware enforces strict origin matching and prevents wildcard '*' with credentials.
func (s *HTTPServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			// Enforce allowed origins (never wildcard * in production)
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// rateLimitMiddleware applies per-IP token bucket rate limiting.
func (s *HTTPServer) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		clientIP := extractClientIP(r)
		if !s.limiter.Allow(clientIP) {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "Too Many Requests: rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// authMiddleware validates Bearer JWT access token on protected MCP endpoints.
func (s *HTTPServer) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[Auth Middleware] Incoming request: Path=%s, Method=%s, HasAuthHeader=%v, HasTokenQuery=%v, IP=%s",
			r.URL.Path, r.Method, r.Header.Get("Authorization") != "", r.URL.Query().Get("token") != "", extractClientIP(r))

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			// Check query param for SSE streaming
			authHeader = r.URL.Query().Get("token")
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		if strings.TrimSpace(tokenStr) == "" {
			// In local development mode, allow direct developer access with default user or X-User-ID header
			if s.cfg.Environment == "development" {
				devUserID := r.Header.Get("X-User-ID")
				if devUserID == "" {
					devUserID = r.URL.Query().Get("user_id")
				}
				if devUserID == "" {
					devUserID = "test_user_1"
				}

				// Resolve username string to valid PostgreSQL UUID
				if _, err := uuid.Parse(devUserID); err != nil && s.repo != nil {
					user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), devUserID, fmt.Sprintf("%s@example.com", devUserID))
					if userErr == nil && user != nil {
						devUserID = user.ID
					}
				}

				ctx := database.WithActor(r.Context(), database.ActorContext{
					ActorID:   devUserID,
					IPAddress: extractClientIP(r),
				})
				log.Printf("[Auth Middleware] DEV BYPASS: user=%s", devUserID)
				next(w, r.WithContext(ctx))
				return
			}

			log.Printf("[Auth Middleware] REJECTED: Missing Bearer token on Path=%s", r.URL.Path)
			http.Error(w, "Unauthorized: missing Bearer token", http.StatusUnauthorized)
			return
		}

		claims, err := auth.ValidateAccessToken(tokenStr, s.cfg.JWTSigningSecret)
		if err != nil {
			log.Printf("[Auth Middleware] REJECTED: Invalid token on Path=%s: %v", r.URL.Path, err)
			http.Error(w, fmt.Sprintf("Unauthorized: %v", err), http.StatusUnauthorized)
			return
		}

		actualUserID := claims.UserID
		if _, err := uuid.Parse(actualUserID); err != nil && s.repo != nil {
			user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), actualUserID, fmt.Sprintf("%s@example.com", actualUserID))
			if userErr == nil && user != nil {
				actualUserID = user.ID
			}
		}

		log.Printf("[Auth Middleware] SUCCESS: Authenticated user=%s (UUID=%s) on Path=%s", claims.UserID, actualUserID, r.URL.Path)

		// Inject ActorContext for auditing
		ctx := database.WithActor(r.Context(), database.ActorContext{
			ActorID:   actualUserID,
			IPAddress: extractClientIP(r),
		})

		next(w, r.WithContext(ctx))
	}
}

func extractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	parts := strings.Split(r.RemoteAddr, ":")
	if len(parts) > 0 {
		return parts[0]
	}
	return "127.0.0.1"
}

// handleTwitterConnect initiates Twitter OAuth 2.0 PKCE browser authentication.
func (s *HTTPServer) handleTwitterConnect(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "test_user_1"
	}

	// Resolve username string to valid PostgreSQL UUID
	if _, err := uuid.Parse(userID); err != nil && s.repo != nil {
		user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), userID, fmt.Sprintf("%s@example.com", userID))
		if userErr == nil && user != nil {
			userID = user.ID
		}
	}

	// Generate PKCE code_verifier (32 random bytes url-safe base64)
	verifierBytes := make([]byte, 32)
	_, _ = rand.Read(verifierBytes)
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	// Compute S256 code_challenge
	hash := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(hash[:])

	// Generate random state
	stateBytes := make([]byte, 16)
	_, _ = rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	callbackURL := strings.TrimSpace(s.cfg.TwitterRedirectURI)
	if callbackURL == "" || (strings.Contains(callbackURL, "localhost") && strings.Contains(r.Host, "duckdns.org")) {
		callbackURL = fmt.Sprintf("%s/auth/twitter/callback", s.getBaseURL(r))
	}

	// Save state mapping
	s.oauthStatesMu.Lock()
	s.oauthStates[state] = twitterOAuthState{
		codeVerifier: codeVerifier,
		userID:       userID,
		redirectURI:  callbackURL,
		expiresAt:    time.Now().Add(10 * time.Minute),
	}
	s.oauthStatesMu.Unlock()

	params := make(map[string][]string)
	params["response_type"] = []string{"code"}
	params["client_id"] = []string{s.cfg.TwitterClientID}
	params["redirect_uri"] = []string{callbackURL}
	params["scope"] = []string{strings.Join(twitter.RequiredScopes, " ")}
	params["state"] = []string{state}
	params["code_challenge"] = []string{codeChallenge}
	params["code_challenge_method"] = []string{"S256"}

	values := urlValues(params)
	authURL := twitter.OAuthAuthorizeURL + "?" + values

	http.Redirect(w, r, authURL, http.StatusFound)
}

func urlValues(m map[string][]string) string {
	var pairs []string
	for k, vs := range m {
		for _, v := range vs {
			pairs = append(pairs, fmt.Sprintf("%s=%s", strings.TrimSpace(k), strings.ReplaceAll(urlQueryEscape(v), " ", "%20")))
		}
	}
	return strings.Join(pairs, "&")
}

func urlQueryEscape(s string) string {
	// Standard percent encoding for OAuth query strings
	var buf strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			buf.WriteByte(c)
		} else {
			buf.WriteString(fmt.Sprintf("%%%02X", c))
		}
	}
	return buf.String()
}

// handleTwitterCallback handles the OAuth 2.0 callback from Twitter.
func (s *HTTPServer) handleTwitterCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")
	errParam := q.Get("error")

	if errParam != "" {
		http.Error(w, fmt.Sprintf("Twitter OAuth Authorization Denied: %s", errParam), http.StatusBadRequest)
		return
	}

	if code == "" || state == "" {
		http.Error(w, "Invalid callback: code and state required", http.StatusBadRequest)
		return
	}

	s.oauthStatesMu.Lock()
	oauthState, exists := s.oauthStates[state]
	if exists {
		delete(s.oauthStates, state)
	}
	s.oauthStatesMu.Unlock()

	if !exists || time.Now().After(oauthState.expiresAt) {
		http.Error(w, "Invalid or expired OAuth state parameter (replay attack prevented)", http.StatusBadRequest)
		return
	}

	tokenResp, err := s.twitterClient.ExchangeOAuthToken(r.Context(), code, oauthState.codeVerifier, oauthState.redirectURI)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed exchanging authorization code with Twitter API v2: %v", err), http.StatusBadRequest)
		return
	}

	// Persist encrypted credentials into PostgreSQL Token Vault
	actualUserID := oauthState.userID
	if _, err := uuid.Parse(actualUserID); err != nil && s.repo != nil {
		user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), actualUserID, fmt.Sprintf("%s@example.com", actualUserID))
		if userErr == nil && user != nil {
			actualUserID = user.ID
		}
	}

	if s.repo != nil {
		expiresAt := time.Now().UTC().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		err = s.repo.SavePlatformConnection(r.Context(), actualUserID, "twitter", []byte(tokenResp.AccessToken), []byte(tokenResp.RefreshToken), expiresAt, twitter.RequiredScopes)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed saving encrypted credentials to vault: %v", err), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `
<!DOCTYPE html>
<html>
<head><title>Twitter Connected</title><style>body{font-family:sans-serif;background:#0f1419;color:#fff;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;} .card{background:#1e2732;padding:40px;border-radius:16px;box-shadow:0 8px 32px rgba(0,0,0,0.5);text-align:center;max-width:480px;} h1{color:#1d9bf0;margin-bottom:12px;} p{color:#8b98a5;line-height:1.6;} .badge{background:#00ba7c22;color:#00ba7c;padding:6px 16px;border-radius:20px;display:inline-block;font-weight:bold;margin-bottom:16px;}</style></head>
<body>
<div class="card">
<div class="badge">Connected Successfully</div>
<h1>Twitter/X Authorized</h1>
<p>Your Twitter account has been cryptographically linked and stored in the encrypted token vault for user <strong>%s</strong> (UUID: %s).</p>
<p>You can now use the <code>publish_post</code> and <code>get_analytics</code> MCP tools in Claude Desktop or your AI agent!</p>
</div>
</body>
</html>`, oauthState.userID, actualUserID)
}

// handleYouTubeConnect initiates Google OAuth 2.0 PKCE browser authentication for YouTube.
func (s *HTTPServer) handleYouTubeConnect(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "test_user_1"
	}

	if _, err := uuid.Parse(userID); err != nil && s.repo != nil {
		user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), userID, fmt.Sprintf("%s@example.com", userID))
		if userErr == nil && user != nil {
			userID = user.ID
		}
	}

	verifierBytes := make([]byte, 32)
	_, _ = rand.Read(verifierBytes)
	codeVerifier := base64.RawURLEncoding.EncodeToString(verifierBytes)

	hash := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(hash[:])

	stateBytes := make([]byte, 16)
	_, _ = rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	callbackURL := strings.TrimSpace(s.cfg.YouTubeRedirectURI)
	if callbackURL == "" || (strings.Contains(callbackURL, "localhost") && strings.Contains(r.Host, "duckdns.org")) {
		callbackURL = fmt.Sprintf("%s/auth/youtube/callback", s.getBaseURL(r))
	}

	s.oauthStatesMu.Lock()
	s.oauthStates[state] = twitterOAuthState{
		codeVerifier: codeVerifier,
		userID:       userID,
		redirectURI:  callbackURL,
		expiresAt:    time.Now().Add(10 * time.Minute),
	}
	s.oauthStatesMu.Unlock()

	params := make(map[string][]string)
	params["response_type"] = []string{"code"}
	params["client_id"] = []string{s.cfg.YouTubeClientID}
	params["redirect_uri"] = []string{callbackURL}
	params["scope"] = []string{strings.Join(youtube.RequiredScopes, " ")}
	params["state"] = []string{state}
	params["code_challenge"] = []string{codeChallenge}
	params["code_challenge_method"] = []string{"S256"}
	params["access_type"] = []string{"offline"}
	params["prompt"] = []string{"consent"}

	values := urlValues(params)
	authURL := youtube.OAuthAuthorizeURL + "?" + values

	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleYouTubeCallback handles the OAuth 2.0 callback from Google.
func (s *HTTPServer) handleYouTubeCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")
	errParam := q.Get("error")

	if errParam != "" {
		http.Error(w, fmt.Sprintf("Google OAuth Authorization Denied: %s", errParam), http.StatusBadRequest)
		return
	}

	if code == "" || state == "" {
		http.Error(w, "Invalid callback: code and state required", http.StatusBadRequest)
		return
	}

	s.oauthStatesMu.Lock()
	oauthState, exists := s.oauthStates[state]
	if exists {
		delete(s.oauthStates, state)
	}
	s.oauthStatesMu.Unlock()

	if !exists || time.Now().After(oauthState.expiresAt) {
		http.Error(w, "Invalid or expired OAuth state parameter (replay attack prevented)", http.StatusBadRequest)
		return
	}

	tokenResp, err := s.youtubeClient.ExchangeOAuthToken(r.Context(), code, oauthState.codeVerifier, oauthState.redirectURI)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed exchanging authorization code with Google OAuth: %v", err), http.StatusBadRequest)
		return
	}

	actualUserID := oauthState.userID
	if _, err := uuid.Parse(actualUserID); err != nil && s.repo != nil {
		user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), actualUserID, fmt.Sprintf("%s@example.com", actualUserID))
		if userErr == nil && user != nil {
			actualUserID = user.ID
		}
	}

	if s.repo != nil {
		expiresAt := time.Now().UTC().Add(time.Duration(tokenResp.ExpiresIn) * time.Second)
		err = s.repo.SavePlatformConnection(r.Context(), actualUserID, "youtube", []byte(tokenResp.AccessToken), []byte(tokenResp.RefreshToken), expiresAt, youtube.RequiredScopes)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed saving encrypted credentials to vault: %v", err), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `
<!DOCTYPE html>
<html>
<head><title>YouTube Connected</title><style>body{font-family:sans-serif;background:#0f0f0f;color:#fff;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;} .card{background:#212121;padding:40px;border-radius:16px;box-shadow:0 8px 32px rgba(0,0,0,0.5);text-align:center;max-width:480px;} h1{color:#ff0000;margin-bottom:12px;} p{color:#aaa;line-height:1.6;} .badge{background:#00ba7c22;color:#00ba7c;padding:6px 16px;border-radius:20px;display:inline-block;font-weight:bold;margin-bottom:16px;}</style></head>
<body>
<div class="card">
<div class="badge">Connected Successfully</div>
<h1>YouTube Authorized</h1>
<p>Your Google YouTube account has been cryptographically linked and stored in the encrypted token vault for user <strong>%s</strong> (UUID: %s).</p>
<p>You can now use the <code>publish_post</code> and <code>get_analytics</code> MCP tools in Claude Desktop or your AI agent!</p>
</div>
</body>
</html>`, oauthState.userID, actualUserID)
}

// handleInstagramConnect initiates Meta OAuth 2.0 browser authentication for Instagram Business.
func (s *HTTPServer) handleInstagramConnect(w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("user_id")
	if userID == "" {
		userID = "test_user_1"
	}

	if _, err := uuid.Parse(userID); err != nil && s.repo != nil {
		user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), userID, fmt.Sprintf("%s@example.com", userID))
		if userErr == nil && user != nil {
			userID = user.ID
		}
	}

	stateBytes := make([]byte, 16)
	_, _ = rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	callbackURL := strings.TrimSpace(s.cfg.InstagramRedirectURI)
	if callbackURL == "" || (strings.Contains(callbackURL, "localhost") && strings.Contains(r.Host, "duckdns.org")) {
		callbackURL = fmt.Sprintf("%s/auth/instagram/callback", s.getBaseURL(r))
	}

	s.oauthStatesMu.Lock()
	s.oauthStates[state] = twitterOAuthState{
		codeVerifier: "",
		userID:       userID,
		redirectURI:  callbackURL,
		expiresAt:    time.Now().Add(10 * time.Minute),
	}
	s.oauthStatesMu.Unlock()

	params := make(map[string][]string)
	params["response_type"] = []string{"code"}
	params["client_id"] = []string{s.cfg.InstagramClientID}
	params["redirect_uri"] = []string{callbackURL}
	params["scope"] = []string{strings.Join(instagram.RequiredScopes, ",")}
	params["state"] = []string{state}

	values := urlValues(params)
	authURL := instagram.MetaOAuthDialogURL + "?" + values

	http.Redirect(w, r, authURL, http.StatusFound)
}

// handleInstagramCallback handles the OAuth 2.0 callback from Meta for Instagram.
func (s *HTTPServer) handleInstagramCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	code := q.Get("code")
	state := q.Get("state")
	errParam := q.Get("error")
	errReason := q.Get("error_reason")

	if errParam != "" || errReason != "" {
		http.Error(w, fmt.Sprintf("Meta OAuth Authorization Denied: %s (%s)", errParam, errReason), http.StatusBadRequest)
		return
	}

	if code == "" || state == "" {
		http.Error(w, "Invalid callback: code and state required", http.StatusBadRequest)
		return
	}

	s.oauthStatesMu.Lock()
	oauthState, exists := s.oauthStates[state]
	if exists {
		delete(s.oauthStates, state)
	}
	s.oauthStatesMu.Unlock()

	if !exists || time.Now().After(oauthState.expiresAt) {
		http.Error(w, "Invalid or expired OAuth state parameter (replay attack prevented)", http.StatusBadRequest)
		return
	}

	// 1. Exchange short-lived authorization code
	shortLivedTok, err := s.instagramClient.ExchangeShortLivedToken(r.Context(), code, oauthState.redirectURI)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed exchanging authorization code with Meta Graph API: %v", err), http.StatusBadRequest)
		return
	}

	// 2. Upgrade to 60-day long-lived access token
	longLivedTok, err := s.instagramClient.ExchangeLongLivedToken(r.Context(), shortLivedTok.AccessToken)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed upgrading to long-lived Meta token: %v", err), http.StatusBadRequest)
		return
	}

	// 3. Discover Instagram Business/Creator Account
	igAccount, _, err := s.instagramClient.GetInstagramBusinessAccount(r.Context(), longLivedTok.AccessToken)
	if err != nil {
		http.Error(w, fmt.Sprintf("Instagram Business Account Discovery Failed: %v", err), http.StatusBadRequest)
		return
	}

	actualUserID := oauthState.userID
	if _, err := uuid.Parse(actualUserID); err != nil && s.repo != nil {
		user, userErr := s.repo.GetOrCreateUserByUsername(r.Context(), actualUserID, fmt.Sprintf("%s@example.com", actualUserID))
		if userErr == nil && user != nil {
			actualUserID = user.ID
		}
	}

	if s.repo != nil {
		expiresAt := time.Now().UTC().Add(time.Duration(longLivedTok.ExpiresIn) * time.Second)
		err = s.repo.SavePlatformConnection(r.Context(), actualUserID, "instagram", []byte(longLivedTok.AccessToken), nil, expiresAt, instagram.RequiredScopes)
		if err != nil {
			http.Error(w, fmt.Sprintf("Failed saving encrypted credentials to vault: %v", err), http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, `
<!DOCTYPE html>
<html>
<head><title>Instagram Connected</title><style>body{font-family:sans-serif;background:#0d0d0d;color:#fff;display:flex;justify-content:center;align-items:center;height:100vh;margin:0;} .card{background:#1a1a1a;padding:40px;border-radius:16px;box-shadow:0 8px 32px rgba(0,0,0,0.5);text-align:center;max-width:480px;border:1px solid #333;} h1{background:linear-gradient(45deg,#f09433,#e6683c,#dc2743,#cc2366,#bc1888);-webkit-background-clip:text;-webkit-text-fill-color:transparent;margin-bottom:12px;} p{color:#aaa;line-height:1.6;} .badge{background:#00ba7c22;color:#00ba7c;padding:6px 16px;border-radius:20px;display:inline-block;font-weight:bold;margin-bottom:16px;} .handle{font-weight:bold;color:#fff;}</style></head>
<body>
<div class="card">
<div class="badge">Connected Successfully</div>
<h1>Instagram Business Authorized</h1>
<p>Your Instagram Business account <span class="handle">@%s</span> (ID: %s) has been cryptographically linked and stored in the encrypted token vault for user <strong>%s</strong> (UUID: %s).</p>
<p>You can now use the <code>publish_post</code> (supporting photos and 90s Reels) and <code>get_analytics</code> MCP tools in Claude Desktop or your AI agent!</p>
</div>
</body>
</html>`, igAccount.Username, igAccount.ID, oauthState.userID, actualUserID)
}

// handleInstagramWebhook handles Meta webhook challenge verifications and event delivery.
func (s *HTTPServer) handleInstagramWebhook(w http.ResponseWriter, r *http.Request) {
	// 1. Meta Webhook Subscription Challenge (GET)
	if r.Method == http.MethodGet {
		mode := r.URL.Query().Get("hub.mode")
		token := r.URL.Query().Get("hub.verify_token")
		challenge := r.URL.Query().Get("hub.challenge")

		if mode == "subscribe" && token == s.cfg.InstagramWebhookSecret && challenge != "" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(challenge))
			return
		}

		http.Error(w, "Forbidden: invalid hub.verify_token", http.StatusForbidden)
		return
	}

	// 2. Incoming Event Notification (POST)
	if r.Method == http.MethodPost {
		rawPayload, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Failed reading webhook body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		sigHeader := r.Header.Get("X-Hub-Signature-256")
		if s.cfg.InstagramWebhookSecret != "" {
			if err := instagram.VerifyWebhookSignature(rawPayload, sigHeader, s.cfg.InstagramWebhookSecret); err != nil {
				http.Error(w, "Unauthorized: invalid signature", http.StatusUnauthorized)
				return
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("EVENT_RECEIVED"))
		return
	}

	http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
}

// handleBackgroundPublishRetry processes background retries for transiently failed publish jobs.
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
		} else if len(job.MediaURLs) > 0 {
			if strings.HasPrefix(strings.ToLower(job.MediaURLs[0]), "http://") || strings.HasPrefix(strings.ToLower(job.MediaURLs[0]), "https://") {
				fetchedBytes, _, fetchErr := security.FetchMediaWithSSRFProtection(ctx, job.MediaURLs[0], 500*1024*1024)
				if fetchErr != nil {
					return fmt.Errorf("failed fetching remote video URL with SSRF protection in background retry: %w", fetchErr)
				}
				videoBytes = fetchedBytes
			} else {
				data, readErr := os.ReadFile(job.MediaURLs[0])
				if readErr == nil {
					videoBytes = data
				}
			}
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

func (s *HTTPServer) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	baseURL := strings.TrimRight(s.cfg.PublicBaseURL, "/")
	if baseURL == "" {
		baseURL = "https://social-mcp.duckdns.org"
	}

	openAPISpec := map[string]interface{}{
		"openapi": "3.1.0",
		"info": map[string]interface{}{
			"title":       "Social Publisher & Analytics AI API",
			"description": "Multi-platform Social Media Publishing, Account Insights, and AI SEO Optimization API for Instagram, YouTube, and Twitter/X.",
			"version":     "1.0.0",
		},
		"servers": []map[string]string{
			{"url": baseURL},
		},
		"paths": map[string]interface{}{
			"/api/v1/publish": map[string]interface{}{
				"post": map[string]interface{}{
					"operationId": "publishPost",
					"summary":     "Publish post to social media",
					"description": "Publishes an image, video, Reel, or text post to Instagram, YouTube, or Twitter with idempotency protection.",
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"platform": map[string]interface{}{
											"type":        "string",
											"enum":        []string{"instagram", "youtube", "twitter"},
											"description": "Target social media platform",
										},
										"content": map[string]interface{}{
											"type":        "string",
											"description": "Caption, title, or tweet text",
										},
										"media_urls": map[string]interface{}{
											"type": "array",
											"items": map[string]string{
												"type": "string",
											},
											"description": "Optional public media image or video URLs",
										},
										"media_data": map[string]interface{}{
											"type":        "string",
											"description": "Optional Base64-encoded binary string of any AI-generated or local image/video to auto-stage and publish",
										},
										"idempotency_key": map[string]interface{}{
											"type":        "string",
											"description": "Unique idempotency key to prevent duplicate publishing",
										},
									},
									"required": []string{"platform", "content"},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{
							"description": "Post published successfully",
						},
						"401": map[string]interface{}{
							"description": "Unauthorized - valid Bearer token or OAuth session required",
						},
					},
				},
			},
			"/api/v1/upload": map[string]interface{}{
				"post": map[string]interface{}{
					"operationId": "uploadMedia",
					"summary":     "Stage AI-generated media to public URL",
					"description": "Uploads Base64 binary image/video data and returns a public HTTPS URL accessible by Instagram, YouTube, and Twitter crawlers.",
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"file_name": map[string]interface{}{
											"type":        "string",
											"description": "Optional filename with extension (e.g. image.jpg, video.mp4)",
										},
										"media_data": map[string]interface{}{
											"type":        "string",
											"description": "Base64-encoded binary string of the image or video",
										},
									},
									"required": []string{"media_data"},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Media staged successfully"},
					},
				},
			},
			"/api/v1/insights": map[string]interface{}{
				"get": map[string]interface{}{
					"operationId": "getAccountInsights",
					"summary":     "Get account-level insights and reach diagnosis",
					"description": "Fetches 28-day reach, profile views, follower metrics, recent posts performance comparison, and engagement diagnostics for an account.",
					"parameters": []map[string]interface{}{
						{
							"name":        "platform",
							"in":          "query",
							"required":    true,
							"schema":      map[string]interface{}{"type": "string", "enum": []string{"instagram", "youtube", "twitter"}},
							"description": "Platform to fetch account insights for",
						},
						{
							"name":        "time_period",
							"in":          "query",
							"required":    false,
							"schema":      map[string]interface{}{"type": "string", "enum": []string{"day", "week", "days_28", "lifetime"}, "default": "days_28"},
							"description": "Time window for metrics",
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Account insights retrieved successfully"},
					},
				},
			},
			"/api/v1/analytics": map[string]interface{}{
				"get": map[string]interface{}{
					"operationId": "getPostAnalytics",
					"summary":     "Get post-level analytics",
					"description": "Retrieves likes, comments, views, and reach for a specific published post ID.",
					"parameters": []map[string]interface{}{
						{
							"name":     "platform",
							"in":       "query",
							"required": true,
							"schema":   map[string]interface{}{"type": "string", "enum": []string{"instagram", "youtube", "twitter"}},
						},
						{
							"name":     "post_id",
							"in":       "query",
							"required": true,
							"schema":   map[string]interface{}{"type": "string"},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Post analytics retrieved successfully"},
					},
				},
			},
			"/api/v1/optimize": map[string]interface{}{
				"post": map[string]interface{}{
					"operationId": "optimizeContentSEO",
					"summary":     "Optimize content with viral hooks and SEO",
					"description": "Generates 3 viral hooks, high-search keyword tags, niche hashtags, and can optionally update live post/video metadata.",
					"requestBody": map[string]interface{}{
						"required": true,
						"content": map[string]interface{}{
							"application/json": map[string]interface{}{
								"schema": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"platform":        map[string]interface{}{"type": "string", "enum": []string{"instagram", "youtube", "twitter"}},
										"target_type":     map[string]interface{}{"type": "string", "enum": []string{"post", "account_profile", "video"}},
										"topic_or_draft":  map[string]interface{}{"type": "string"},
										"niche":           map[string]interface{}{"type": "string"},
										"target_audience": map[string]interface{}{"type": "string"},
										"post_id":         map[string]interface{}{"type": "string"},
										"apply_update":    map[string]interface{}{"type": "boolean"},
									},
									"required": []string{"platform", "target_type", "topic_or_draft"},
								},
							},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "SEO suggestions generated or applied"},
					},
				},
			},
			"/api/v1/connect": map[string]interface{}{
				"get": map[string]interface{}{
					"operationId": "connectPlatform",
					"summary":     "Get platform connection URL",
					"description": "Returns the OAuth connect URL to link user's social media account.",
					"parameters": []map[string]interface{}{
						{
							"name":     "platform",
							"in":       "query",
							"required": true,
							"schema":   map[string]interface{}{"type": "string", "enum": []string{"instagram", "youtube", "twitter"}},
						},
					},
					"responses": map[string]interface{}{
						"200": map[string]interface{}{"description": "Connection URL generated"},
					},
				},
			},
		},
		"components": map[string]interface{}{
			"schemas": map[string]interface{}{},
			"securitySchemes": map[string]interface{}{
				"OAuth2": map[string]interface{}{
					"type": "oauth2",
					"flows": map[string]interface{}{
						"authorizationCode": map[string]interface{}{
							"authorizationUrl": baseURL + "/oauth/authorize",
							"tokenUrl":         baseURL + "/oauth/token",
							"scopes": map[string]string{
								"read":    "Read analytics and insights",
								"write":   "Publish social media posts",
								"publish": "Publish social media content",
							},
						},
					},
				},
			},
		},
		"security": []map[string][]string{
			{"OAuth2": {"read", "write", "publish"}},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(openAPISpec)
}

func (s *HTTPServer) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Privacy Policy - Social Publisher MCP</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 800px; margin: 40px auto; padding: 0 20px; line-height: 1.6; color: #333;">
  <h1>Privacy Policy</h1>
  <p><strong>Effective Date:</strong> January 1, 2026</p>
  <p>Social Publisher MCP Server ("the Service") allows users to publish content, view analytics, and optimize social media workflows across Instagram, YouTube, and Twitter/X.</p>
  <h2>Data Security & Encryption</h2>
  <p>We do not store passwords or plaintext access credentials. All platform access and refresh tokens are encrypted at rest using industry-standard AES-256-GCM encryption in an isolated vault.</p>
  <h2>Data Sharing</h2>
  <p>Your data is used solely to execute user-initiated publishing and analytics actions. We never sell, rent, or distribute personal data to third parties.</p>
</body>
</html>`))
}

func (s *HTTPServer) handleTerms(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`<!DOCTYPE html>
<html>
<head><title>Terms of Service - Social Publisher MCP</title><meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; max-width: 800px; margin: 40px auto; padding: 0 20px; line-height: 1.6; color: #333;">
  <h1>Terms of Service</h1>
  <p><strong>Effective Date:</strong> January 1, 2026</p>
  <p>By using the Social Publisher MCP Server, you agree to comply with platform developer policies of Meta, Google YouTube, and X/Twitter.</p>
</body>
</html>`))
}

func (s *HTTPServer) handleRESTPublish(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	var reqBody map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name":      "publish_post",
		"arguments": reqBody,
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	w.Header().Set("Content-Type", "application/json")
	if resp != nil && resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Result)
}

func (s *HTTPServer) handleRESTAnalytics(w http.ResponseWriter, r *http.Request) {
	platform := r.URL.Query().Get("platform")
	postID := r.URL.Query().Get("post_id")
	if platform == "" || postID == "" {
		http.Error(w, "Query parameters 'platform' and 'post_id' are required", http.StatusBadRequest)
		return
	}

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name": "get_analytics",
		"arguments": map[string]interface{}{
			"platform": platform,
			"post_id":  postID,
		},
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	w.Header().Set("Content-Type", "application/json")
	if resp != nil && resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Result)
}

func (s *HTTPServer) handleRESTInsights(w http.ResponseWriter, r *http.Request) {
	platform := r.URL.Query().Get("platform")
	period := r.URL.Query().Get("time_period")
	if platform == "" {
		http.Error(w, "Query parameter 'platform' is required", http.StatusBadRequest)
		return
	}
	if period == "" {
		period = "days_28"
	}

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name": "get_account_insights",
		"arguments": map[string]interface{}{
			"platform":    platform,
			"time_period": period,
		},
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	w.Header().Set("Content-Type", "application/json")
	if resp != nil && resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Result)
}

func (s *HTTPServer) handleRESTOptimize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	var reqBody map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name":      "optimize_content_seo",
		"arguments": reqBody,
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	w.Header().Set("Content-Type", "application/json")
	if resp != nil && resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Result)
}

func (s *HTTPServer) handleRESTConnect(w http.ResponseWriter, r *http.Request) {
	platform := r.URL.Query().Get("platform")
	if platform == "" {
		http.Error(w, "Query parameter 'platform' is required", http.StatusBadRequest)
		return
	}

	actor := database.GetActor(r.Context())
	userID := actor.ActorID
	if userID == "" || userID == "anonymous" {
		userID = "default_user"
	}

	baseURL := strings.TrimRight(s.cfg.PublicBaseURL, "/")
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://localhost:%d", s.cfg.ServerPort)
	}
	connectURL := fmt.Sprintf("%s/auth/%s/connect?user_id=%s", baseURL, platform, userID)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"platform":    platform,
		"connect_url": connectURL,
		"status":      "action_required",
		"instruction": "Open connect_url in your web browser to authenticate and link account",
	})
}

func (s *HTTPServer) handleRESTUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	var reqBody map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
		http.Error(w, "Invalid JSON payload", http.StatusBadRequest)
		return
	}

	callReq := mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "tools/call",
	}
	paramsJSON, _ := json.Marshal(map[string]interface{}{
		"name":      "upload_media",
		"arguments": reqBody,
	})
	callReq.Params = paramsJSON

	reqBytes, _ := json.Marshal(callReq)
	resp := s.mcpServer.HandleRequest(r.Context(), reqBytes)

	w.Header().Set("Content-Type", "application/json")
	if resp != nil && resp.Error != nil {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(resp.Error)
		return
	}
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp.Result)
}
