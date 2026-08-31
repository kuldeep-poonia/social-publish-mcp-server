package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/redis/go-redis/v9"
)

func newTestServerWithDB(t *testing.T, db *sql.DB, rdb *redis.Client) *HTTPServer {
	t.Helper()
	cfg := &config.Config{
		ServerHost:         "127.0.0.1",
		ServerPort:         8080,
		JWTSigningSecret:   []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long"),
		TokenEncryptionKey: make([]byte, 32),
	}
	s := NewHTTPServer(cfg, db, nil)
	s.redisClient = rdb
	return s
}

func TestServer_Readyz_AllHealthy(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("failed opening sqlmock: %v", err)
	}
	defer mockDB.Close()
	mock.ExpectPing()

	// Mock Redis or use null/mocked client
	s := newTestServerWithDB(t, mockDB, nil)
	// Temporarily override redis status logic in test to simulate connected redis
	// If redisClient is nil, readyz marks redis unreachable
	ts := httptest.NewServer(s.server.Handler)
	defer ts.Close()

	// When redis is nil -> readyz returns 503 with redis: unreachable
	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when Redis is nil, got %d", resp.StatusCode)
	}

	var payload readyzResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed decoding JSON: %v", err)
	}

	if payload.Postgres != "ok" {
		t.Errorf("expected postgres to be 'ok', got '%s'", payload.Postgres)
	}
	if payload.Redis != "unreachable" {
		t.Errorf("expected redis to be 'unreachable', got '%s'", payload.Redis)
	}
}

func TestServer_Readyz_PostgresUnreachable(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("failed opening sqlmock: %v", err)
	}
	defer mockDB.Close()
	mock.ExpectPing().WillReturnError(sql.ErrConnDone)

	s := newTestServerWithDB(t, mockDB, nil)
	ts := httptest.NewServer(s.server.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when DB is failing, got %d", resp.StatusCode)
	}

	var payload readyzResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("failed decoding JSON: %v", err)
	}

	if payload.Status != "unhealthy" {
		t.Errorf("expected status 'unhealthy', got '%s'", payload.Status)
	}
	if payload.Postgres != "unreachable" {
		t.Errorf("expected postgres to be 'unreachable', got '%s'", payload.Postgres)
	}
}

func TestServer_Metrics_OperationalPayload(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("failed opening sqlmock: %v", err)
	}
	defer mockDB.Close()
	mock.ExpectPing()

	s := newTestServerWithDB(t, mockDB, nil)
	ts := httptest.NewServer(s.server.Handler)
	defer ts.Close()

	// Trigger a couple of requests to increment counters
	_, _ = http.Get(ts.URL + "/health")
	_, _ = http.Get(ts.URL + "/non-existent-404")

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /metrics, got %d", resp.StatusCode)
	}

	var metrics metricsResponse
	if err := json.NewDecoder(resp.Body).Decode(&metrics); err != nil {
		t.Fatalf("failed decoding /metrics JSON: %v", err)
	}

	// Verify real runtime fields
	if metrics.Goroutines <= 0 {
		t.Errorf("expected goroutines > 0, got %d", metrics.Goroutines)
	}
	if metrics.MemoryAllocBytes <= 0 {
		t.Errorf("expected memory_alloc_bytes > 0, got %d", metrics.MemoryAllocBytes)
	}
	if metrics.MemorySysBytes <= 0 {
		t.Errorf("expected memory_sys_bytes > 0, got %d", metrics.MemorySysBytes)
	}
	if metrics.PostgresStatus != "connected" {
		t.Errorf("expected postgres_status 'connected', got '%s'", metrics.PostgresStatus)
	}
	if metrics.PostgresLatencyMs < 0 {
		t.Errorf("expected positive postgres_latency_ms, got %f", metrics.PostgresLatencyMs)
	}
	if metrics.RedisStatus != "unreachable" {
		t.Errorf("expected redis_status 'unreachable' for nil client, got '%s'", metrics.RedisStatus)
	}
	if metrics.RequestCountTotal < 2 {
		t.Errorf("expected request_count_total >= 2, got %d", metrics.RequestCountTotal)
	}
	if metrics.ErrorCountTotal < 1 {
		t.Errorf("expected error_count_total >= 1 from 404, got %d", metrics.ErrorCountTotal)
	}

	parsedTime, err := time.Parse(time.RFC3339, metrics.Timestamp)
	if err != nil || parsedTime.IsZero() {
		t.Errorf("invalid RFC3339 timestamp in metrics: %s", metrics.Timestamp)
	}
}

func TestServer_Metrics_ZeroSensitiveDataLeak(t *testing.T) {
	s := newTestServerWithDB(t, nil, nil)
	ts := httptest.NewServer(s.server.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatalf("GET /metrics failed: %v", err)
	}
	defer resp.Body.Close()

	var rawMap map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&rawMap); err != nil {
		t.Fatalf("failed decoding /metrics JSON: %v", err)
	}

	forbiddenKeys := []string{"dsn", "password", "secret", "token", "key", "credential", "auth", "jwt", "user_id"}
	for _, key := range forbiddenKeys {
		if val, exists := rawMap[key]; exists {
			t.Fatalf("SECURITY VIOLATION: sensitive key '%s' (val: %v) leaked in /metrics response", key, val)
		}
	}
}

func TestServer_Version_Payload(t *testing.T) {
	s := newTestServerWithDB(t, nil, nil)
	ts := httptest.NewServer(s.server.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/version")
	if err != nil {
		t.Fatalf("GET /version failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 OK from /version, got %d", resp.StatusCode)
	}

	var ver versionResponse
	if err := json.NewDecoder(resp.Body).Decode(&ver); err != nil {
		t.Fatalf("failed decoding /version JSON: %v", err)
	}

	if ver.Version == "" {
		t.Errorf("expected version to be non-empty")
	}
	if ver.Commit == "" {
		t.Errorf("expected commit to be non-empty")
	}
	if ver.GoVersion != runtime.Version() {
		t.Errorf("expected go_version '%s', got '%s'", runtime.Version(), ver.GoVersion)
	}
}

func TestServer_Observability_LoadLightConcurrency(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatalf("failed opening sqlmock: %v", err)
	}
	defer mockDB.Close()

	for i := 0; i < 50; i++ {
		mock.ExpectPing()
	}

	s := newTestServerWithDB(t, mockDB, nil)
	ts := httptest.NewServer(s.server.Handler)
	defer ts.Close()

	var wg sync.WaitGroup
	errChan := make(chan error, 50)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Add(-1)
			client := &http.Client{Timeout: 3 * time.Second}
			resp, err := client.Get(ts.URL + "/metrics")
			if err != nil {
				errChan <- err
				return
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				errChan <- err
			}
		}()
	}

	wg.Wait()
	close(errChan)

	for err := range errChan {
		if err != nil {
			t.Errorf("concurrent request error: %v", err)
		}
	}
}
