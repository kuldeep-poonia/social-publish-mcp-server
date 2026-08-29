// Package server provides the HTTP server, routing, security middleware, and CORS configuration.
package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
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
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/optimizer"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/persona"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/queue"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/ratelimit"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/scheduler"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/scout"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/telemetry"
	"github.com/kuldeep-poonia/social-publish-mcp-server/web"
	"github.com/redis/go-redis/v9"
)

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
	schedulerService    *scheduler.Service
	scoutService        *scout.Service
	optimizerService    *optimizer.Service
	personaService      *persona.Service
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
		"https://chat.openai.com/aip/*",
		"https://chatgpt.com/aip/*",
		"https://chatgpt.com/*",
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

	var limiter ratelimit.Limiter = ratelimit.NewTokenBucketLimiter(100.0, 200.0)
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

	var schedulerService *scheduler.Service
	if db != nil && repo != nil {
		schedulerService = scheduler.NewService(db, instagramService, twitterService, youtubeService, mediaStager, repo)
		schedulerService.StartWorker(context.Background(), 30*time.Second)
	}

	personaService := persona.NewService(db)
	geminiGen := scout.NewGeminiClient(cfg.GeminiAPIKey, nil)
	scoutService := scout.NewService(db, repo, nil, nil, geminiGen, personaService)
	optimizerService := optimizer.NewService(db, repo, youtubeClient, cfg.GeminiAPIKey, nil, personaService)

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
		schedulerService:    schedulerService,
		scoutService:        scoutService,
		optimizerService:    optimizerService,
		personaService:      personaService,
		redisClient:         rdb,
		streamQueue:         streamQueue,
		dlqManager:          dlqManager,
		telemetry:           telemetry.DefaultTelemetry(),
		logger:              telemetry.DefaultLogger(),
		oauthStates:         make(map[string]twitterOAuthState),
	}

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

	if rdb != nil {
		transport.SetSessionStore(&redisSessionStore{client: rdb})
	}

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

	// Twitter OAuth Connect & Callback Handlers
	mux.HandleFunc("/auth/twitter/connect", s.handleTwitterConnect)
	mux.HandleFunc("/auth/twitter/callback", s.handleTwitterCallback)
	mux.HandleFunc("/auth/callback/twitter", s.handleTwitterCallback)
	mux.HandleFunc("/auth/callback", s.handleTwitterCallback)

	// YouTube OAuth Connect & Callback Handlers
	mux.HandleFunc("/auth/youtube/connect", s.handleYouTubeConnect)
	mux.HandleFunc("/auth/youtube/callback", s.handleYouTubeCallback)
	mux.HandleFunc("/auth/callback/youtube", s.handleYouTubeCallback)

	// Instagram OAuth Connect & Callback Handlers
	mux.HandleFunc("/auth/instagram/connect", s.handleInstagramConnect)
	mux.HandleFunc("/auth/instagram/callback", s.handleInstagramCallback)
	mux.HandleFunc("/auth/callback/instagram", s.handleInstagramCallback)

	// Ephemeral Media Server for Meta Crawler
	if mediaStager != nil {
		mux.HandleFunc("/media/ephemeral/", mediaStager.ServeHTTP)
	}

	// Meta Webhook Endpoint (HMAC-SHA256 signature verified)
	mux.HandleFunc("/webhooks/instagram", s.handleInstagramWebhook)

	// MCP Protocol Endpoints (Streamable HTTP + Legacy Parallel SSE with 12-Month Migration Window)
	mux.HandleFunc("/mcp", s.authMiddleware(transport.HandleStreamableHTTP))
	mux.HandleFunc("/mcp/rpc", s.authMiddleware(transport.HandleDirectRPC))
	mux.HandleFunc("/mcp/sse", s.authMiddleware(transport.HandleSSE))
	mux.HandleFunc("/mcp/messages", s.authMiddleware(transport.HandleMessages))

	// ChatGPT Actions OpenAPI 3.1.0 Discovery & Legal Endpoints
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

	// Scheduling & Cron Triggers
	mux.HandleFunc("/api/v1/schedule", s.authMiddleware(s.handleRESTSchedule))
	mux.HandleFunc("/api/v1/schedule/", s.authMiddleware(s.handleRESTScheduleByID))
	mux.HandleFunc("/api/v1/cron/execute-scheduled", s.handleCronExecuteScheduled)

	// Trending Topics Scout
	mux.HandleFunc("/api/v1/scout", s.authMiddleware(s.handleRESTScout))

	// Post Metadata & CTR Optimization
	mux.HandleFunc("/api/v1/posts/metadata", s.authMiddleware(s.handleRESTUpdateMetadata))
	mux.HandleFunc("/api/v1/posts/", s.authMiddleware(s.handleRESTPostRouting))

	// Brand Persona & Voice Lock
	mux.HandleFunc("/api/v1/persona", s.authMiddleware(s.handleRESTPersona))

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

// Handler returns the underlying configured http.Handler for testing or routing.
func (s *HTTPServer) Handler() http.Handler {
	return s.server.Handler
}

// Start runs the HTTP server.
func (s *HTTPServer) Start() error {
	return s.server.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *HTTPServer) Shutdown(ctx context.Context) error {
	if s.schedulerService != nil {
		s.schedulerService.StopWorker()
	}
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

func (s *HTTPServer) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Mcp-Session-Id, Accept, X-Requested-With, X-Client-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

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

func (s *HTTPServer) authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[Auth Middleware] Incoming request: Path=%s, Method=%s, HasAuthHeader=%v, HasTokenQuery=%v, IP=%s",
			r.URL.Path, r.Method, r.Header.Get("Authorization") != "", r.URL.Query().Get("token") != "", extractClientIP(r))

		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			authHeader = r.URL.Query().Get("token")
		}

		tokenStr := strings.TrimPrefix(authHeader, "Bearer ")
		if strings.TrimSpace(tokenStr) == "" {
			if s.cfg.Environment == "development" {
				devUserID := r.Header.Get("X-User-ID")
				if devUserID == "" {
					devUserID = r.URL.Query().Get("user_id")
				}
				if devUserID == "" {
					devUserID = "test_user_1"
				}

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

		ctx := database.WithActor(r.Context(), database.ActorContext{
			ActorID:   actualUserID,
			IPAddress: extractClientIP(r),
		})

		next(w, r.WithContext(ctx))
	}
}

type redisSessionStore struct {
	client *redis.Client
}

func (r *redisSessionStore) SetSession(ctx context.Context, sessionID, userID, clientID string, ttl time.Duration) error {
	if r.client == nil {
		return nil
	}
	key := fmt.Sprintf("mcp:session:%s", sessionID)
	data, _ := json.Marshal(map[string]interface{}{
		"session_id":     sessionID,
		"user_id":        userID,
		"client_id":      clientID,
		"last_active_at": time.Now().UTC().Format(time.RFC3339),
	})
	return r.client.Set(ctx, key, string(data), ttl).Err()
}

func (r *redisSessionStore) GetSession(ctx context.Context, sessionID string) (string, bool, error) {
	if r.client == nil {
		return "", false, nil
	}
	key := fmt.Sprintf("mcp:session:%s", sessionID)
	val, err := r.client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return "", false, nil
		}
		return "", false, err
	}
	var sess map[string]interface{}
	_ = json.Unmarshal([]byte(val), &sess)
	uid, _ := sess["user_id"].(string)
	return uid, true, nil
}

func (r *redisSessionStore) DeleteSession(ctx context.Context, sessionID string) error {
	if r.client == nil {
		return nil
	}
	key := fmt.Sprintf("mcp:session:%s", sessionID)
	return r.client.Del(ctx, key).Err()
}

func extractClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return "127.0.0.1"
}
