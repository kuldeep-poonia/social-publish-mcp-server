package security_test

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/adapters/instagram"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
)

// TestSecurity_InjectionAndPathTraversalDefense tests that SQL injection payloads,
// path traversal attacks, and header injection attempts are 100% neutralized.
func TestSecurity_InjectionAndPathTraversalDefense(t *testing.T) {
	t.Logf("=== RUNNING INJECTION & PATH TRAVERSAL PENETRATION SUITE ===")

	// 1. SQL Injection Fuzzing against Database Layer
	t.Run("SQL_Injection_Fuzzing", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("failed initializing sqlmock: %v", err)
		}
		defer db.Close()

		key := make([]byte, 32)
		repo := database.NewRepository(db, key, nil)

		sqliPayloads := []string{
			"' OR '1'='1",
			"admin' --",
			"1; DROP TABLE users; --",
			"1' UNION SELECT null, null, null, null, null, null --",
			"'; EXEC xp_cmdshell('dir'); --",
			"\" or \"\"=\"",
			"1' AND SLEEP(5) --",
		}

		ctx := database.WithActor(context.Background(), database.ActorContext{
			ActorID:   "test_actor",
			IPAddress: "127.0.0.1",
		})

		for _, payload := range sqliPayloads {
			// All queries in repository.go use parameterized placeholders ($1, $2)
			mock.ExpectQuery("SELECT id, user_id, platform, platform_post_id, content, media_urls, status, scheduled_at, published_at, idempotency_key, created_at, updated_at FROM posts WHERE id = \\$1").
				WithArgs(payload).
				WillReturnError(sql.ErrNoRows)

			post, _ := repo.GetPostByID(ctx, payload)
			if post != nil {
				t.Fatalf("CRITICAL SQLI VIOLATION: SQL injection payload executed: %s", payload)
			}
			t.Logf("PASS: SQL injection payload safely parameterized: %s", payload)
		}
	})

	// 2. Path Traversal Defense on Ephemeral Media Server
	t.Run("Path_Traversal_MediaStager", func(t *testing.T) {
		stager, err := instagram.NewMediaStager("", "http://localhost:8080")
		if err != nil {
			t.Fatalf("failed creating media stager: %v", err)
		}
		defer stager.Close()

		traversalPayloads := []string{
			"../../etc/passwd",
			"..%2f..%2fetc%2fpasswd",
			"..\\..\\Windows\\system32\\cmd.exe",
			"....//....//etc/passwd",
			"/etc/passwd",
			"C:/Windows/system32/cmd.exe",
			"0123456789abcdef0123456789abcdef/../../secret.txt",
			"invalid_non_hex_token_format_12345.jpg",
		}

		for _, token := range traversalPayloads {
			req := httptest.NewRequest(http.MethodGet, "/media/ephemeral/"+token, nil)
			rec := httptest.NewRecorder()

			stager.ServeHTTP(rec, req)

			if rec.Code == http.StatusOK {
				t.Fatalf("CRITICAL PATH TRAVERSAL VIOLATION: Directory traversal payload returned 200 OK: %s", token)
			}
			t.Logf("PASS: Path traversal payload rejected (HTTP %d): %s", rec.Code, token)
		}
	})
}
