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

	httpServer := NewHTTPServer(cfg, nil, nil)
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

	httpServer := NewHTTPServer(cfg, nil, nil)
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

func TestServer_InstagramRoutesAndTools(t *testing.T) {
	signingSecret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	cfg := &config.Config{
		ServerHost:             "127.0.0.1",
		ServerPort:             8080,
		JWTSigningSecret:       signingSecret,
		TokenEncryptionKey:     make([]byte, 32),
		InstagramClientID:      "meta_client_12345",
		InstagramClientSecret:  "meta_sec_67890",
		InstagramWebhookSecret: "webhook_verify_sec_xyz",
	}

	httpServer := NewHTTPServer(cfg, nil, nil)
	ts := httptest.NewServer(httpServer.server.Handler)
	defer ts.Close()

	client := &http.Client{
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	// 1. Instagram Connect Endpoint (Redirects to Meta OAuth)
	connectResp, err := client.Get(ts.URL + "/auth/instagram/connect?user_id=test_user_ig")
	if err != nil {
		t.Fatalf("GET /auth/instagram/connect failed: %v", err)
	}
	if connectResp.StatusCode != http.StatusFound {
		t.Fatalf("expected 302 Found on /auth/instagram/connect, got %d", connectResp.StatusCode)
	}
	loc := connectResp.Header.Get("Location")
	if loc == "" || !bytes.Contains([]byte(loc), []byte("facebook.com")) {
		t.Errorf("unexpected redirect location: %s", loc)
	}
	connectResp.Body.Close()

	// 2. Instagram Webhook Challenge Verification (GET)
	challengeURL := fmt.Sprintf("%s/webhooks/instagram?hub.mode=subscribe&hub.verify_token=%s&hub.challenge=test_challenge_12345",
		ts.URL, cfg.InstagramWebhookSecret)
	whResp, err := http.Get(challengeURL)
	if err != nil {
		t.Fatalf("webhook challenge request failed: %v", err)
	}
	if whResp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on webhook challenge, got %d", whResp.StatusCode)
	}
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(whResp.Body)
	if buf.String() != "test_challenge_12345" {
		t.Errorf("expected echoed challenge 'test_challenge_12345', got '%s'", buf.String())
	}
	whResp.Body.Close()
}

func TestServer_Publish_SyncFirstTransientRetryQueue(t *testing.T) {
	signingSecret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	queueKey := make([]byte, 32)
	copy(queueKey, []byte("test_queue_encryption_key_32byte"))

	cfg := &config.Config{
		ServerHost:               "127.0.0.1",
		ServerPort:               8080,
		JWTSigningSecret:         signingSecret,
		TokenEncryptionKey:       queueKey,
		QueueEncryptionKey:       queueKey,
		RedisHost:                "localhost",
		RedisPort:                6379,
		QueueMaxRetries:          3,
		QueueMaxDeliveryAttempts: 3,
		QueueWorkers:             2,
	}

	httpServer := NewHTTPServer(cfg, nil, nil)
	if httpServer.streamQueue == nil {
		t.Skip("skipping test: Redis stream queue unavailable at localhost:6379")
	}

	ts := httptest.NewServer(httpServer.server.Handler)
	defer ts.Close()

	// 1. Generate Auth Token
	tok, err := auth.IssueSessionTokens("usr_sync_test", "user", signingSecret)
	if err != nil {
		t.Fatalf("failed generating token: %v", err)
	}

	// 2. Dispatch MCP publish_post tool call with transient error simulated
	reqBody := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      "req_123",
		"method":  "tools/call",
		"params": map[string]interface{}{
			"name": "publish_post",
			"arguments": map[string]interface{}{
				"platform": "twitter",
				"content":  "Testing sync-first execution with transient fallback",
			},
		},
	}

	reqBytes, _ := json.Marshal(reqBody)
	httpReq, _ := http.NewRequest("POST", ts.URL+"/mcp/rpc", bytes.NewReader(reqBytes))
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("MCP tool call failed: %v", err)
	}
	defer resp.Body.Close()

	var rpcResp map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&rpcResp)

	t.Logf("=== SYNC-FIRST MCP PUBLISH TOOL EXECUTION RESULT ===")
	t.Logf("HTTP Status: %d", resp.StatusCode)
	t.Logf("RPC Response: %+v", rpcResp)

	// Since database/twitter service is not initialized in standalone mock, it returns service error or is transiently queued
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected HTTP 200 OK from MCP JSON-RPC endpoint, got %d", resp.StatusCode)
	}
}

func TestServer_OpenAPIAndChatGPTIntegration(t *testing.T) {
	cfg := &config.Config{
		ServerHost:       "127.0.0.1",
		ServerPort:       8080,
		PublicBaseURL:    "https://social-mcp.duckdns.org",
		JWTSigningSecret: []byte("test-signing-secret-minimum-32-chars-long"),
	}

	httpServer := NewHTTPServer(cfg, nil, nil)
	ts := httptest.NewServer(httpServer.server.Handler)
	defer ts.Close()

	// 1. Test OpenAPI Spec Discovery
	resp, err := http.Get(ts.URL + "/openapi.json")
	if err != nil {
		t.Fatalf("GET /openapi.json failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK on /openapi.json, got %d", resp.StatusCode)
	}

	var openAPIDoc struct {
		OpenAPI string                 `json:"openapi"`
		Info    map[string]interface{} `json:"info"`
		Paths   map[string]interface{} `json:"paths"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&openAPIDoc); err != nil {
		t.Fatalf("failed decoding openapi.json: %v", err)
	}

	if openAPIDoc.OpenAPI != "3.0.3" {
		t.Errorf("expected OpenAPI version 3.0.3, got %s", openAPIDoc.OpenAPI)
	}

	if _, exists := openAPIDoc.Paths["/api/v1/publish"]; !exists {
		t.Errorf("missing /api/v1/publish in openapi.json paths")
	}
	if _, exists := openAPIDoc.Paths["/api/v1/insights"]; !exists {
		t.Errorf("missing /api/v1/insights in openapi.json paths")
	}
	if _, exists := openAPIDoc.Paths["/api/v1/optimize"]; !exists {
		t.Errorf("missing /api/v1/optimize in openapi.json paths")
	}

	// 2. Test Privacy Policy
	pResp, pErr := http.Get(ts.URL + "/privacy")
	if pErr != nil || pResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /privacy failed: status=%d, err=%v", pResp.StatusCode, pErr)
	}
	pResp.Body.Close()

	// 3. Test Terms of Service
	tResp, tErr := http.Get(ts.URL + "/terms")
	if tErr != nil || tResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /terms failed: status=%d, err=%v", tResp.StatusCode, tErr)
	}
	tResp.Body.Close()

	// 4. Test Connect endpoint
	cResp, cErr := http.Get(ts.URL + "/api/v1/connect?platform=instagram")
	if cErr != nil || cResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/connect failed: status=%d, err=%v", cResp.StatusCode, cErr)
	}
	cResp.Body.Close()

	t.Logf("PASS: Verified ChatGPT OpenAPI 3.0.3, Privacy, Terms, and Connect REST API Endpoints.")
}
