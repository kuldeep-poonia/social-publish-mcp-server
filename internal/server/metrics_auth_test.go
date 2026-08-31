package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
)

func TestServer_MetricsEndpointAuthentication(t *testing.T) {
	metricsSecret := "test_metrics_scrape_token_secret_12345"
	cfg := &config.Config{
		ServerHost:         "127.0.0.1",
		ServerPort:         8080,
		MetricsBearerToken: metricsSecret,
		JWTSigningSecret:   []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long"),
		TokenEncryptionKey: make([]byte, 32),
	}

	httpServer := NewHTTPServer(cfg, nil, nil)
	ts := httptest.NewServer(httpServer.server.Handler)
	defer ts.Close()

	// 1. Unauthenticated request to /metrics/prometheus -> 401 Unauthorized
	respUnauth, err := http.Get(ts.URL + "/metrics/prometheus")
	if err != nil {
		t.Fatalf("GET /metrics/prometheus failed: %v", err)
	}
	defer respUnauth.Body.Close()

	t.Logf("Unauthenticated /metrics/prometheus HTTP Status: %d", respUnauth.StatusCode)
	if respUnauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("SECURITY VIOLATION: expected 401 Unauthorized for unauthenticated /metrics/prometheus, got %d", respUnauth.StatusCode)
	}

	// 2. Request with invalid Bearer token -> 401 Unauthorized
	reqInvalid, _ := http.NewRequest("GET", ts.URL+"/metrics/prometheus", nil)
	reqInvalid.Header.Set("Authorization", "Bearer invalid_bearer_token")
	respInvalid, err := http.DefaultClient.Do(reqInvalid)
	if err != nil {
		t.Fatalf("GET /metrics/prometheus with invalid token failed: %v", err)
	}
	defer respInvalid.Body.Close()

	if respInvalid.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 Unauthorized for invalid token, got %d", respInvalid.StatusCode)
	}

	// 3. Authenticated request with valid Bearer token -> 200 OK + Prometheus text
	reqAuth, _ := http.NewRequest("GET", ts.URL+"/metrics/prometheus", nil)
	reqAuth.Header.Set("Authorization", "Bearer "+metricsSecret)
	respAuth, err := http.DefaultClient.Do(reqAuth)
	if err != nil {
		t.Fatalf("authenticated GET /metrics/prometheus failed: %v", err)
	}
	defer respAuth.Body.Close()

	if respAuth.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK for authenticated /metrics/prometheus, got %d", respAuth.StatusCode)
	}

	bodyBytes, _ := io.ReadAll(respAuth.Body)
	bodyStr := string(bodyBytes)

	// Verify core metric names are present in output
	if !strings.Contains(bodyStr, "social_mcp_requests_total") {
		t.Errorf("expected metric 'social_mcp_requests_total' in /metrics output")
	}
	if !strings.Contains(bodyStr, "social_mcp_request_duration_seconds") {
		t.Errorf("expected metric 'social_mcp_request_duration_seconds' in /metrics output")
	}

	t.Logf("=== AUTHENTICATED /metrics SCRAPE OUTPUT VERIFIED ===")
	t.Logf("Content Length: %d bytes", len(bodyStr))
}

func TestServer_MinimalSafeHealthEndpoint(t *testing.T) {
	cfg := &config.Config{
		ServerHost:         "127.0.0.1",
		ServerPort:         8080,
		JWTSigningSecret:   []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long"),
		TokenEncryptionKey: make([]byte, 32),
	}

	httpServer := NewHTTPServer(cfg, nil, nil)
	ts := httptest.NewServer(httpServer.server.Handler)
	defer ts.Close()

	// Unauthenticated request to /health -> must succeed with 200 OK
	resp, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /health, got %d", resp.StatusCode)
	}

	var healthPayload map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&healthPayload); err != nil {
		t.Fatalf("failed decoding health JSON: %v", err)
	}

	t.Logf("=== /health RESPONSE PAYLOAD ===")
	t.Logf("Payload: %+v", healthPayload)

	// Invariant: only minimal status and timestamp exposed — zero database credentials, connection strings, or diagnostics
	if healthPayload["status"] != "ok" {
		t.Errorf("expected status 'ok', got '%v'", healthPayload["status"])
	}
	if _, ok := healthPayload["timestamp"]; !ok {
		t.Errorf("expected timestamp in health payload")
	}

	// Verify no internal state leaks
	forbiddenKeys := []string{"db", "database", "postgres", "redis", "config", "token", "password", "env"}
	for _, key := range forbiddenKeys {
		if _, leaked := healthPayload[key]; leaked {
			t.Fatalf("SECURITY VIOLATION: /health leaked internal system details: key '%s' found in response", key)
		}
	}
}
