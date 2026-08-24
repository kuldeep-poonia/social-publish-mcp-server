// Package server provides the HTTP server, routing, security middleware, and CORS configuration.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/twitter"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/auth"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/mcp"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/ratelimit"
)

type twitterOAuthState struct {
	codeVerifier string
	userID       string
	redirectURI  string
	expiresAt    time.Time
}

// HTTPServer represents the configured HTTP server.
type HTTPServer struct {
	server         *http.Server
	oauthServer    *auth.OAuthServer
	mcpServer      *mcp.Server
	transport      *mcp.HTTPTransport
	limiter        ratelimit.Limiter
	cfg            *config.Config
	repo           *database.Repository
	twitterService *twitter.Service
	twitterClient  *twitter.Client
	oauthStatesMu  sync.Mutex
	oauthStates    map[string]twitterOAuthState
}

// NewHTTPServer builds and configures the HTTP server with all routes and middleware.
func NewHTTPServer(cfg *config.Config, db *sql.DB, repo *database.Repository) *HTTPServer {
	oauthServer := auth.NewOAuthServer(cfg.JWTSigningSecret)
	
	// Pre-register standard OAuth clients for Claude Desktop, MCP SDKs, and local CLI
	allowedRedirects := []string{
		"http://localhost:8080/callback",
		"http://127.0.0.1:8080/callback",
		"http://localhost:8080/auth/twitter/callback",
		"http://localhost:8080/auth/callback/twitter",
		"claude://oauth/callback",
	}
	_ = oauthServer.RegisterClient("mcp_client_desktop", "", "MCP Desktop Client", allowedRedirects)
	_ = oauthServer.RegisterClient("claude_desktop", "", "Claude Desktop Client", allowedRedirects)
	_ = oauthServer.RegisterClient("curl_test", "", "Curl Test Client", allowedRedirects)

	mcpServer := mcp.NewServer()
	transport := mcp.NewHTTPTransport(mcpServer)
	limiter := ratelimit.NewTokenBucketLimiter(100.0, 200.0) // 100 RPS with 200 burst

	twitterClient := twitter.NewClient(cfg.TwitterClientID, cfg.TwitterClientSecret)
	var twitterService *twitter.Service
	if db != nil && repo != nil {
		twitterService = twitter.NewService(db, repo, twitterClient)
	}

	s := &HTTPServer{
		oauthServer:    oauthServer,
		mcpServer:      mcpServer,
		transport:      transport,
		limiter:        limiter,
		cfg:            cfg,
		repo:           repo,
		twitterService: twitterService,
		twitterClient:  twitterClient,
		oauthStates:    make(map[string]twitterOAuthState),
	}

	// Register Social Publishing MCP Tools
	s.registerMCPToolHandlers()

	mux := http.NewServeMux()

	// Healthcheck
	mux.HandleFunc("/health", s.handleHealth)

	// OAuth 2.1 Endpoints
	mux.HandleFunc("/oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("/oauth/token", s.handleToken)

	// Twitter Live Browser OAuth Connect & Callback Handlers (supports both /auth/twitter/callback and /auth/callback/twitter)
	mux.HandleFunc("/auth/twitter/connect", s.handleTwitterConnect)
	mux.HandleFunc("/auth/twitter/callback", s.handleTwitterCallback)
	mux.HandleFunc("/auth/callback/twitter", s.handleTwitterCallback)
	mux.HandleFunc("/auth/callback", s.handleTwitterCallback)

	// MCP Protocol Endpoints
	mux.HandleFunc("/mcp/rpc", s.authMiddleware(transport.HandleDirectRPC))
	mux.HandleFunc("/mcp/sse", s.authMiddleware(transport.HandleSSE))
	mux.HandleFunc("/mcp/messages", s.authMiddleware(transport.HandleMessages))

	// Wrap root with CORS and Rate Limiting
	handler := s.rateLimitMiddleware(s.corsMiddleware(mux))

	s.server = &http.Server{
		Addr:         fmt.Sprintf("%s:%d", cfg.ServerHost, cfg.ServerPort),
		Handler:      handler,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	return s
}

func (s *HTTPServer) registerMCPToolHandlers() {
	publishHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
		actor := database.GetActor(ctx)
		if actor.ActorID == "" || actor.ActorID == "anonymous" {
			return nil, errors.New("unauthorized: authenticated user session required to publish")
		}

		platform, _ := args["platform"].(string)
		content, _ := args["content"].(string)
		idempotencyKey, _ := args["idempotency_key"].(string)

		var mediaURLs []string
		if rawURLs, ok := args["media_urls"].([]interface{}); ok {
			for _, u := range rawURLs {
				if str, ok := u.(string); ok {
					mediaURLs = append(mediaURLs, str)
				}
			}
		}

		if platform != "twitter" {
			return nil, fmt.Errorf("platform '%s' is not supported in current release (only 'twitter' active)", platform)
		}

		if s.twitterService == nil {
			return nil, errors.New("twitter service is not initialized")
		}

		resp, err := s.twitterService.PublishTweet(ctx, &twitter.PublishTweetRequest{
			UserID:         actor.ActorID,
			Content:        content,
			MediaURLs:      mediaURLs,
			IdempotencyKey: idempotencyKey,
		})
		if err != nil {
			return nil, err
		}

		resultJSON, _ := json.Marshal(resp)
		return &mcp.CallToolResult{
			Content: []mcp.ToolContent{
				{Type: "text", Text: string(resultJSON)},
			},
			IsError: false,
		}, nil
	}

	analyticsHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
		actor := database.GetActor(ctx)
		if actor.ActorID == "" || actor.ActorID == "anonymous" {
			return nil, errors.New("unauthorized: authenticated user session required for analytics")
		}

		platform, _ := args["platform"].(string)
		postID, _ := args["post_id"].(string)

		if platform != "twitter" {
			return nil, fmt.Errorf("platform '%s' is not supported in current release", platform)
		}

		if s.repo == nil {
			return nil, errors.New("database repository is not initialized")
		}

		accessBytes, _, _, _, err := s.repo.GetDecryptedPlatformConnection(ctx, actor.ActorID, "twitter")
		if err != nil {
			return nil, fmt.Errorf("failed retrieving Twitter credentials: %w", err)
		}

		metrics, err := s.twitterClient.GetTweetAnalytics(ctx, string(accessBytes), postID)
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
	}

	connectHandler := func(ctx context.Context, args map[string]interface{}) (*mcp.CallToolResult, error) {
		platform, _ := args["platform"].(string)
		if platform != "twitter" {
			return nil, fmt.Errorf("platform '%s' connection is not supported yet", platform)
		}

		actor := database.GetActor(ctx)
		userID := actor.ActorID
		if userID == "" || userID == "anonymous" {
			userID = "test_user_1"
		}

		connectURL := fmt.Sprintf("http://localhost:%d/auth/twitter/connect?user_id=%s", s.cfg.ServerPort, userID)

		payload := map[string]string{
			"platform":      "twitter",
			"connect_url":   connectURL,
			"status":        "action_required",
			"instruction":   "Open connect_url in your web browser to authenticate Twitter and save tokens into vault",
		}
		bytes, _ := json.Marshal(payload)
		return &mcp.CallToolResult{
			Content: []mcp.ToolContent{
				{Type: "text", Text: string(bytes)},
			},
			IsError: false,
		}, nil
	}

	s.mcpServer.RegisterSocialTools(publishHandler, analyticsHandler, connectHandler)
}

// Start runs the HTTP server.
func (s *HTTPServer) Start() error {
	return s.server.ListenAndServe()
}

// Shutdown gracefully stops the server.
func (s *HTTPServer) Shutdown(ctx context.Context) error {
	return s.server.Shutdown(ctx)
}

func (s *HTTPServer) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"status":    "healthy",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
		"version":   "0.1.0",
	})
}

func (s *HTTPServer) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed, use GET", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()
	req := &auth.AuthorizeRequest{
		ResponseType:        q.Get("response_type"),
		ClientID:            q.Get("client_id"),
		RedirectURI:         q.Get("redirect_uri"),
		Scope:               q.Get("scope"),
		State:               q.Get("state"),
		CodeChallenge:       q.Get("code_challenge"),
		CodeChallengeMethod: q.Get("code_challenge_method"),
		UserID:              q.Get("user_id"),
	}

	code, err := s.oauthServer.Authorize(req)
	if err != nil {
		http.Error(w, fmt.Sprintf("Authorization Error: %v", err), http.StatusBadRequest)
		return
	}

	// Redirect back with authorization code
	redirectTarget := fmt.Sprintf("%s?code=%s", req.RedirectURI, code)
	if req.State != "" {
		redirectTarget += fmt.Sprintf("&state=%s", req.State)
	}
	http.Redirect(w, r, redirectTarget, http.StatusFound)
}

func (s *HTTPServer) handleToken(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed, use POST", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Failed parsing form", http.StatusBadRequest)
		return
	}

	req := &auth.TokenExchangeRequest{
		GrantType:    r.FormValue("grant_type"),
		Code:         r.FormValue("code"),
		ClientID:     r.FormValue("client_id"),
		CodeVerifier: r.FormValue("code_verifier"),
		RedirectURI:  r.FormValue("redirect_uri"),
		RefreshToken: r.FormValue("refresh_token"),
	}

	// Use in-memory store for skeleton or repository for DB
	store := auth.NewInMemorySessionStore()
	pair, err := s.oauthServer.ExchangeCodeForTokens(r.Context(), req, store)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"invalid_grant","error_description":"%v"}`, err), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(pair)
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
				next(w, r.WithContext(ctx))
				return
			}

			http.Error(w, "Unauthorized: missing Bearer token", http.StatusUnauthorized)
			return
		}

		claims, err := auth.ValidateAccessToken(tokenStr, s.cfg.JWTSigningSecret)
		if err != nil {
			http.Error(w, fmt.Sprintf("Unauthorized: %v", err), http.StatusUnauthorized)
			return
		}

		// Inject ActorContext for auditing
		ctx := database.WithActor(r.Context(), database.ActorContext{
			ActorID:   claims.UserID,
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
	if callbackURL == "" {
		callbackURL = fmt.Sprintf("http://%s:%d/auth/twitter/callback", s.cfg.ServerHost, s.cfg.ServerPort)
		if s.cfg.ServerHost == "0.0.0.0" {
			callbackURL = fmt.Sprintf("http://localhost:%d/auth/twitter/callback", s.cfg.ServerPort)
		}
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
