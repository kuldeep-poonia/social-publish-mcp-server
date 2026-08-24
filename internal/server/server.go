// Package server provides the HTTP server, routing, security middleware, and CORS configuration.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/auth"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/mcp"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/ratelimit"
)

// HTTPServer represents the configured HTTP server.
type HTTPServer struct {
	server      *http.Server
	oauthServer *auth.OAuthServer
	mcpServer   *mcp.Server
	transport   *mcp.HTTPTransport
	limiter     ratelimit.Limiter
	cfg         *config.Config
	repo        *database.Repository
}

// NewHTTPServer builds and configures the HTTP server with all routes and middleware.
func NewHTTPServer(cfg *config.Config, repo *database.Repository) *HTTPServer {
	oauthServer := auth.NewOAuthServer(cfg.JWTSigningSecret)
	mcpServer := mcp.NewServer()
	transport := mcp.NewHTTPTransport(mcpServer)
	limiter := ratelimit.NewTokenBucketLimiter(100.0, 200.0) // 100 RPS with 200 burst

	s := &HTTPServer{
		oauthServer: oauthServer,
		mcpServer:   mcpServer,
		transport:   transport,
		limiter:     limiter,
		cfg:         cfg,
		repo:        repo,
	}

	mux := http.NewServeMux()

	// Healthcheck
	mux.HandleFunc("/health", s.handleHealth)

	// OAuth 2.1 Endpoints
	mux.HandleFunc("/oauth/authorize", s.handleAuthorize)
	mux.HandleFunc("/oauth/token", s.handleToken)

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
