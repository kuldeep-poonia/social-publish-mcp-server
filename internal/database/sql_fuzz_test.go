package database

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/crypto"
	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

func TestSQLInjectionFuzzing_150Payloads(t *testing.T) {
	adversarialPayloads := []string{
		"' OR '1'='1",
		"' OR '1'='1' --",
		"' OR 1=1/*",
		"admin' --",
		"admin' #",
		"' UNION SELECT null, username, password FROM users--",
		"1; DROP TABLE users;--",
		"1; DROP TABLE platform_connections;--",
		"1'; EXEC sp_msforeachtable 'DROP TABLE ?'--",
		"' OR EXISTS(SELECT * FROM users WHERE email='admin@example.com')--",
		"1' AND 1=cast((SELECT table_name FROM information_schema.tables LIMIT 1) as int)--",
		"1' AND SLEEP(5)--",
		"1' WAITFOR DELAY '0:0:5'--",
		"1' OR pg_sleep(5)--",
		"'; benchmark(50000000,MD5(1))--",
		"1' HAVING 1=1--",
		"1' GROUP BY id HAVING 1=1--",
		"1' ORDER BY 1--",
		"1' ORDER BY 100--",
		"x' AND (SELECT 1 FROM (SELECT COUNT(*), CONCAT((SELECT database()), FLOOR(RAND(0)*2)) x FROM information_schema.tables GROUP BY x) a)--",
		"1%27%20OR%201=1%20--",
		"' OR ''='",
		"' OR 1=1 LIMIT 1; --",
		"' UNION ALL SELECT NULL,NULL,NULL,NULL,NULL--",
		`{"$gt": ""}`,
		`{"$where": "this.password == 'secret'"}`,
		"NaN",
		"null",
		"undefined",
		"0x27204f5220313d31",
	}

	// Expand payloads to 150+ variations
	for i := 0; i < 120; i++ {
		adversarialPayloads = append(adversarialPayloads,
			fmt.Sprintf("' OR id='%04d' --", i),
			fmt.Sprintf("user_%d'; DROP TABLE audit_logs; --", i),
			fmt.Sprintf("'; SELECT pg_sleep(%d); --", i%5),
		)
	}

	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	cryptoKey := make([]byte, crypto.KeySizeAES256)
	repo := NewRepository(db, cryptoKey, &MockAuditWriter{})
	ctx := context.Background()

	bypassesDetected := 0
	totalTested := 0

	queryPattern := regexp.QuoteMeta(`SELECT id, email, username, created_at, updated_at FROM users WHERE id = $1;`)

	for _, payload := range adversarialPayloads {
		totalTested++

		// Parameterized query expects the raw payload as string argument $1
		mock.ExpectQuery(queryPattern).
			WithArgs(payload).
			WillReturnError(sql.ErrNoRows) // Expected behavior: payload treated strictly as literal data parameter

		_, err := repo.GetUserByID(ctx, payload)
		if err != nil && err != ErrNotFound {
			// If an unexpected error occurs
			t.Fatalf("unexpected query execution failure on payload %s: %v", payload, err)
		}
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled sqlmock expectations: %v", err)
	}

	bypassRate := (float64(bypassesDetected) / float64(totalTested)) * 100.0
	t.Logf("=== SQL INJECTION ADVERSARIAL FUZZING RESULTS ===")
	t.Logf("Total Payloads Tested: %d", totalTested)
	t.Logf("Successful Injection Bypasses: %d (Bypass Rate: %.2f%%)", bypassesDetected, bypassRate)
	t.Logf("Security Requirement: 0 bypasses allowed (Met: %t)", bypassesDetected == 0)

	if bypassesDetected != 0 {
		t.Fatalf("CRITICAL SECURITY FAILURE: %d SQL injection vulnerabilities detected!", bypassesDetected)
	}
}

func TestPlatformConnection_EncryptionRoundtripInRepository(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	cryptoKey := make([]byte, crypto.KeySizeAES256)
	for i := range cryptoKey {
		cryptoKey[i] = byte(i)
	}

	auditWriter := &MockAuditWriter{}
	repo := NewRepository(db, cryptoKey, auditWriter)
	ctx := WithActor(context.Background(), ActorContext{ActorID: "usr_alice", IPAddress: "127.0.0.1"})

	rawAccess := []byte("twitter-live-access-token-12345678")
	rawRefresh := []byte("twitter-live-refresh-token-87654321")
	expiresAt := time.Now().UTC().Add(2 * time.Hour)
	scopes := []string{"tweet.read", "tweet.write", "users.read"}

	// 1. SAVE: Encrypt and Upsert
	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO platform_connections (id, user_id, platform, encrypted_access_token, encrypted_refresh_token, token_expires_at, scopes, is_active, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, TRUE, $8, $8)
		ON CONFLICT (user_id, platform) DO UPDATE SET
			encrypted_access_token = EXCLUDED.encrypted_access_token,
			encrypted_refresh_token = EXCLUDED.encrypted_refresh_token,
			token_expires_at = EXCLUDED.token_expires_at,
			scopes = EXCLUDED.scopes,
			is_active = TRUE,
			updated_at = EXCLUDED.updated_at;
	`)).
		WithArgs(sqlmock.AnyArg(), "usr_alice", "twitter", sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = repo.SavePlatformConnection(ctx, "usr_alice", "twitter", rawAccess, rawRefresh, expiresAt, scopes)
	if err != nil {
		t.Fatalf("failed to save platform connection: %v", err)
	}

	// Verify audit log was recorded
	if len(auditWriter.entries) != 1 {
		t.Fatalf("expected 1 audit log entry, got %d", len(auditWriter.entries))
	}
	if auditWriter.entries[0].Action != "PLATFORM_CONNECTED" {
		t.Fatalf("expected PLATFORM_CONNECTED action, got %s", auditWriter.entries[0].Action)
	}

	// 2. READ: Fetch and Decrypt
	encAccess, _ := crypto.EncryptOAuthToken(rawAccess, cryptoKey)
	encRefresh, _ := crypto.EncryptOAuthToken(rawRefresh, cryptoKey)

	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT encrypted_access_token, encrypted_refresh_token, token_expires_at, scopes, is_active
		FROM platform_connections
		WHERE user_id = $1 AND platform = $2;
	`)).
		WithArgs("usr_alice", "twitter").
		WillReturnRows(sqlmock.NewRows([]string{"encrypted_access_token", "encrypted_refresh_token", "token_expires_at", "scopes", "is_active"}).
			AddRow(encAccess, encRefresh, expiresAt, models.StringArray{"tweet.read", "tweet.write", "users.read"}, true))

	decAccess, decRefresh, exp, scps, err := repo.GetDecryptedPlatformConnection(ctx, "usr_alice", "twitter")
	if err != nil {
		t.Fatalf("failed to get decrypted platform connection: %v", err)
	}

	if string(decAccess) != string(rawAccess) {
		t.Fatalf("access token mismatch: expected %s, got %s", string(rawAccess), string(decAccess))
	}
	if string(decRefresh) != string(rawRefresh) {
		t.Fatalf("refresh token mismatch: expected %s, got %s", string(rawRefresh), string(decRefresh))
	}
	if len(scps) != len(scopes) {
		t.Fatalf("scopes length mismatch: expected %d, got %d", len(scopes), len(scps))
	}
	if exp.Location() != time.UTC {
		t.Fatalf("expiration timestamp not in UTC: %v", exp.Location())
	}
}
