package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/auth"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/mcp"
)

func TestServer_FullEndToEndIntegration(t *testing.T) {
	signingSecret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	cfg := &config.Config{
		ServerHost:         "127.0.0.1",
		ServerPort:         8080,
		JWTSigningSecret:   signingSecret,
		TokenEncryptionKey: make([]byte, 32),
	}

	httpServer := NewHTTPServer(cfg, nil)
	ts := httptest.NewServer(httpServer.server.Handler)
	defer ts.Close()

	// 1. Healthcheck Endpoint
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on /health, got %d", resp.StatusCode)
	}
	resp.Body.Close()

	// 2. Register OAuth Client
	clientID := "mcp_integration_client"
	redirectURI := "http://localhost:3000/oauth/callback"
	_ = httpServer.oauthServer.RegisterClient(clientID, "secret", "Test Client", []string{redirectURI})

	// 3. Authorize Flow with PKCE
	verifier, challenge, _ := auth.GeneratePKCEPair()
	authURL := fmt.Sprintf("%s/oauth/authorize?response_type=code&client_id=%s&redirect_uri=%s&code_challenge=%s&code_challenge_method=S256&user_id=usr_integration",
		ts.URL, clientID, url.QueryEscape(redirectURI), challenge)

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // Don't follow redirect automatically
		},
	}

	authResp, err := client.Get(authURL)
	if err != nil {
		t.Fatalf("GET /oauth/authorize failed: %v", err)
	}
	if authResp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 Found redirect, got %d", authResp.StatusCode)
	}

	location := authResp.Header.Get("Location")
	authResp.Body.Close()

	locURL, err := url.Parse(location)
	if err != nil {
		t.Fatalf("failed parsing redirect Location: %v", err)
	}
	code := locURL.Query().Get("code")
	if code == "" {
		t.Fatal("expected 'code' in redirect query parameters")
	}

	// 4. Token Exchange Flow
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("client_id", clientID)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)

	tokenResp, err := http.PostForm(ts.URL+"/oauth/token", form)
	if err != nil {
		t.Fatalf("POST /oauth/token failed: %v", err)
	}
	if tokenResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on token exchange, got %d", tokenResp.StatusCode)
	}

	var pair auth.TokenPair
	if err := json.NewDecoder(tokenResp.Body).Decode(&pair); err != nil {
		t.Fatalf("failed decoding token pair: %v", err)
	}
	tokenResp.Body.Close()

	if pair.AccessToken == "" {
		t.Fatal("expected non-empty access_token in response")
	}

	// 5. Protected MCP Endpoint: Call ping without token (expect 401 Unauthorized)
	pingReq := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "ping",
	}
	pingBytes, _ := json.Marshal(pingReq)
	unauthResp, err := http.Post(ts.URL+"/mcp/rpc", "application/json", bytes.NewReader(pingBytes))
	if err != nil {
		t.Fatalf("unauthenticated MCP call failed: %v", err)
	}
	if unauthResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for unauthenticated call, got %d", unauthResp.StatusCode)
	}
	unauthResp.Body.Close()

	// 6. Protected MCP Endpoint: Call ping WITH Bearer token (expect 200 OK + pong)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp/rpc", bytes.NewReader(pingBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+pair.AccessToken)

	authMCPResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authenticated MCP call failed: %v", err)
	}
	if authMCPResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for authenticated call, got %d", authMCPResp.StatusCode)
	}

	var rpcResp mcp.JSONRPCResponse
	if err := json.NewDecoder(authMCPResp.Body).Decode(&rpcResp); err != nil {
		t.Fatalf("failed decoding MCP response: %v", err)
	}
	authMCPResp.Body.Close()

	resultMap, ok := rpcResp.Result.(map[string]interface{})
	if !ok || resultMap["status"] != "pong" {
		t.Fatalf("expected status=pong in result, got: %v", rpcResp.Result)
	}

	// 7. CORS Headers Validation
	corsReq, _ := http.NewRequest(http.MethodOptions, ts.URL+"/mcp/rpc", nil)
	corsReq.Header.Set("Origin", "https://claude.ai")
	corsResp, err := http.DefaultClient.Do(corsReq)
	if err != nil {
		t.Fatalf("CORS preflight failed: %v", err)
	}
	if corsResp.Header.Get("Access-Control-Allow-Origin") != "https://claude.ai" {
		t.Fatalf("expected reflected origin in CORS header, got: %s", corsResp.Header.Get("Access-Control-Allow-Origin"))
	}
	if corsResp.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("expected credentials true in CORS header")
	}
	corsResp.Body.Close()
}

func TestServer_RateLimitingMiddleware_429Enforcement(t *testing.T) {
	signingSecret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	cfg := &config.Config{
		ServerHost:         "127.0.0.1",
		ServerPort:         8080,
		JWTSigningSecret:   signingSecret,
		TokenEncryptionKey: make([]byte, 32),
	}

	httpServer := NewHTTPServer(cfg, nil)
	ts := httptest.NewServer(httpServer.server.Handler)
	defer ts.Close()

	// Burst limit is 200; fire 250 rapid requests from same client
	const burst = 200
	const extra = 50
	var rateLimitedCount int

	for i := 0; i < burst+extra; i++ {
		resp, err := http.Get(ts.URL + "/health")
		if err != nil {
			t.Fatalf("request %d failed: %v", i, err)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			rateLimitedCount++
		}
		resp.Body.Close()
	}

	t.Logf("=== RATE LIMITING MIDDLEWARE TEST ===")
	t.Logf("Total Requests: %d | Throttled (429): %d", burst+extra, rateLimitedCount)
	if rateLimitedCount == 0 {
		t.Fatal("expected requests exceeding burst capacity to receive 429 Too Many Requests")
	}
}
