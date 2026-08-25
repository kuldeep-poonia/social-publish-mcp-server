package database_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/crypto"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
)

func TestDatabase_IntegrityVerificationDrill(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed opening sqlmock: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	testMasterKey := make([]byte, 32)
	if _, err := rand.Read(testMasterKey); err != nil {
		t.Fatalf("failed generating test master key: %v", err)
	}

	// 1. Prepare sample encrypted token payloads
	tokenTwitterAccess, _ := crypto.EncryptOAuthToken([]byte("twitter-access-token-12345"), testMasterKey)
	tokenTwitterRefresh, _ := crypto.EncryptOAuthToken([]byte("twitter-refresh-token-67890"), testMasterKey)
	tokenYouTubeAccess, _ := crypto.EncryptOAuthToken([]byte("youtube-access-token-abcde"), testMasterKey)
	tokenYouTubeRefresh, _ := crypto.EncryptOAuthToken([]byte("youtube-refresh-token-fghij"), testMasterKey)
	tokenInstaAccess, _ := crypto.EncryptOAuthToken([]byte("insta-longlived-token-klmno"), testMasterKey)

	// Define Column Schemas for all 7 Tables
	usersCols := []string{"id", "email", "created_at"}
	sessionsCols := []string{"id", "user_id", "token_hash", "is_revoked", "created_at"}
	oauthCols := []string{"id", "user_id", "client_id", "scope", "created_at"}
	connCols := []string{"id", "user_id", "platform", "encrypted_access_token", "encrypted_refresh_token", "is_active"}
	postsCols := []string{"id", "user_id", "platform", "content", "status", "created_at"}
	auditCols := []string{"id", "user_id", "action", "resource_type", "ip_address", "created_at"}
	quotaCols := []string{"id", "user_id", "units_used", "last_reset_at"}

	now := time.Now().UTC()

	// Setup Helper to Add Mock Expectations for All 7 Tables (Alphabetical Order)
	mockAll7Tables := func() {
		// Table 1: audit_logs (3 rows)
		mock.ExpectQuery("SELECT \\* FROM audit_logs").
			WillReturnRows(sqlmock.NewRows(auditCols).
				AddRow("a1", "u1", "publish_post", "post", "127.0.0.1", now).
				AddRow("a2", "u2", "upload_video", "post", "127.0.0.1", now).
				AddRow("a3", "u3", "token_refresh", "connection", "127.0.0.1", now))

		// Table 2: oauth_connections (2 rows)
		mock.ExpectQuery("SELECT \\* FROM oauth_connections").
			WillReturnRows(sqlmock.NewRows(oauthCols).
				AddRow("o1", "u1", "mcp_desktop_client", "publish:all", now).
				AddRow("o2", "u2", "mcp_web_client", "publish:twitter", now))

		// Table 3: platform_connections (3 rows)
		mock.ExpectQuery("SELECT \\* FROM platform_connections").
			WillReturnRows(sqlmock.NewRows(connCols).
				AddRow("c1", "u1", "twitter", tokenTwitterAccess, tokenTwitterRefresh, true).
				AddRow("c2", "u2", "youtube", tokenYouTubeAccess, tokenYouTubeRefresh, true).
				AddRow("c3", "u3", "instagram", tokenInstaAccess, nil, true))

		// Table 4: posts (3 rows)
		mock.ExpectQuery("SELECT \\* FROM posts").
			WillReturnRows(sqlmock.NewRows(postsCols).
				AddRow("p1", "u1", "twitter", "Hello Twitter from MCP", "PUBLISHED", now).
				AddRow("p2", "u2", "youtube", "Building Go Systems Video", "PUBLISHED", now).
				AddRow("p3", "u3", "instagram", "Behind the Scenes Photo", "PUBLISHED", now))

		// Table 5: user_sessions (3 rows)
		mock.ExpectQuery("SELECT \\* FROM user_sessions").
			WillReturnRows(sqlmock.NewRows(sessionsCols).
				AddRow("s1", "u1", "hash_alice_sess", false, now).
				AddRow("s2", "u2", "hash_bob_sess", false, now).
				AddRow("s3", "u3", "hash_charlie_sess", true, now))

		// Table 6: users (3 rows)
		mock.ExpectQuery("SELECT \\* FROM users").
			WillReturnRows(sqlmock.NewRows(usersCols).
				AddRow("u1", "alice@example.com", now).
				AddRow("u2", "bob@example.com", now).
				AddRow("u3", "charlie@example.com", now))

		// Table 7: youtube_quota (2 rows)
		mock.ExpectQuery("SELECT \\* FROM youtube_quota").
			WillReturnRows(sqlmock.NewRows(quotaCols).
				AddRow("q1", "u1", 1600, now).
				AddRow("q2", "u2", 3200, now))
	}

	// 1. Mock queries for GenerateIntegritySnapshot across all 7 tables
	mockAll7Tables()

	all7Tables := []string{
		"audit_logs",
		"oauth_connections",
		"platform_connections",
		"posts",
		"user_sessions",
		"users",
		"youtube_quota",
	}

	snapshot, err := database.GenerateIntegritySnapshot(ctx, db, all7Tables)
	if err != nil {
		t.Fatalf("GenerateIntegritySnapshot failed: %v", err)
	}

	if snapshot.TotalTables != 7 {
		t.Errorf("expected 7 tables in snapshot, got %d", snapshot.TotalTables)
	}
	if snapshot.TotalRows != 19 {
		t.Errorf("expected 19 total rows across 7 tables, got %d", snapshot.TotalRows)
	}
	if snapshot.CombinedChecksum == "" {
		t.Errorf("expected non-empty combined SHA-256 checksum")
	}

	t.Logf("=== GENERATED ALL-7-TABLE DATABASE INTEGRITY SNAPSHOT ===")
	t.Logf("Total Tables:      %d", snapshot.TotalTables)
	t.Logf("Total Rows:        %d", snapshot.TotalRows)
	t.Logf("Combined Checksum: %s", snapshot.CombinedChecksum)
	for tName, tSnap := range snapshot.Tables {
		t.Logf("  - Table: %-22s | Rows: %2d | SHA-256: %s", tName, tSnap.RowCount, tSnap.ChecksumSHA256)
	}

	// 2. Mock queries for VerifyDataIntegrityAndDecryptability across all 7 tables
	mockAll7Tables()

	// Mock token decryptability query
	tokenDecryptCols := []string{"id", "user_id", "platform", "encrypted_access_token", "encrypted_refresh_token"}
	mock.ExpectQuery("SELECT id, user_id, platform, encrypted_access_token, encrypted_refresh_token FROM platform_connections WHERE is_active = TRUE").
		WillReturnRows(sqlmock.NewRows(tokenDecryptCols).
			AddRow("c1", "u1", "twitter", tokenTwitterAccess, tokenTwitterRefresh).
			AddRow("c2", "u2", "youtube", tokenYouTubeAccess, tokenYouTubeRefresh).
			AddRow("c3", "u3", "instagram", tokenInstaAccess, nil))

	report, err := database.VerifyDataIntegrityAndDecryptability(ctx, snapshot, db, testMasterKey)
	if err != nil {
		t.Fatalf("VerifyDataIntegrityAndDecryptability failed: %v", err)
	}

	t.Logf("=== DATABASE ALL-7-TABLE INTEGRITY & DECRYPTABILITY DRILL RESULTS ===")
	t.Logf("Drill Status:               %s", report.Status)
	t.Logf("Verification Duration:      %v", report.VerificationDuration)
	t.Logf("Tables Verified:            %d / %d (100.00%%)", report.TablesVerified, snapshot.TotalTables)
	t.Logf("Total Rows Verified:        %d / %d (100.00%%)", report.TotalRowsVerified, snapshot.TotalRows)
	t.Logf("Checksum Match Parity:      %d / %d (%.2f%%)", report.ChecksumMatches, report.TablesVerified, report.IntegrityMatchRate)
	t.Logf("Encrypted Tokens Checked:   %d", report.TokensVerified)
	t.Logf("Tokens Decrypted Success:   %d (%.2f%%)", report.TokensDecryptedSuccess, report.DecryptabilityRate)

	if report.TablesVerified != 7 {
		t.Errorf("expected 7 verified tables, got %d", report.TablesVerified)
	}
	if report.TotalRowsVerified != 19 {
		t.Errorf("expected 19 verified rows, got %d", report.TotalRowsVerified)
	}
	if report.IntegrityMatchRate != 100.0 {
		t.Errorf("expected 100%% integrity match rate, got %.2f%%", report.IntegrityMatchRate)
	}
	if report.DecryptabilityRate != 100.0 {
		t.Errorf("expected 100%% decryptability rate, got %.2f%%", report.DecryptabilityRate)
	}
	if report.TokensDecryptedSuccess != 5 {
		t.Errorf("expected 5 decrypted tokens, got %d", report.TokensDecryptedSuccess)
	}
}

func TestDatabase_IntegrityVerificationDrill_ChecksumMismatchDetection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed opening sqlmock: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	testMasterKey := make([]byte, 32)
	rand.Read(testMasterKey)

	usersCols := []string{"id", "email", "created_at"}
	mock.ExpectQuery("SELECT \\* FROM users").
		WillReturnRows(sqlmock.NewRows(usersCols).
			AddRow("u1", "alice@example.com", time.Now().UTC()))

	snapshot, err := database.GenerateIntegritySnapshot(ctx, db, []string{"users"})
	if err != nil {
		t.Fatalf("snapshot generation failed: %v", err)
	}

	// Simulate corrupted/tampered row in restored DB
	mock.ExpectQuery("SELECT \\* FROM users").
		WillReturnRows(sqlmock.NewRows(usersCols).
			AddRow("u1", "tampered_attacker_email@example.com", time.Now().UTC()))

	_, err = database.VerifyDataIntegrityAndDecryptability(ctx, snapshot, db, testMasterKey)
	if err == nil {
		t.Fatalf("expected error on tampered checksum, got nil")
	}

	t.Logf("PASS: Tampered database data accurately detected by SHA-256 checksum mismatch: %v", err)
}

func TestDatabase_IntegrityVerificationDrill_WrongMasterKeyRejection(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed opening sqlmock: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	correctMasterKey := make([]byte, 32)
	wrongMasterKey := make([]byte, 32)
	rand.Read(correctMasterKey)
	rand.Read(wrongMasterKey)

	tokenTwitterAccess, _ := crypto.EncryptOAuthToken([]byte("twitter-access-token-12345"), correctMasterKey)

	connCols := []string{"id", "user_id", "platform", "encrypted_access_token", "encrypted_refresh_token", "is_active"}
	mock.ExpectQuery("SELECT \\* FROM platform_connections").
		WillReturnRows(sqlmock.NewRows(connCols).
			AddRow("c1", "u1", "twitter", tokenTwitterAccess, nil, true))

	snapshot, err := database.GenerateIntegritySnapshot(ctx, db, []string{"platform_connections"})
	if err != nil {
		t.Fatalf("snapshot generation failed: %v", err)
	}

	// Re-query for verification
	mock.ExpectQuery("SELECT \\* FROM platform_connections").
		WillReturnRows(sqlmock.NewRows(connCols).
			AddRow("c1", "u1", "twitter", tokenTwitterAccess, nil, true))

	tokenDecryptCols := []string{"id", "user_id", "platform", "encrypted_access_token", "encrypted_refresh_token"}
	mock.ExpectQuery("SELECT id, user_id, platform, encrypted_access_token, encrypted_refresh_token FROM platform_connections WHERE is_active = TRUE").
		WillReturnRows(sqlmock.NewRows(tokenDecryptCols).
			AddRow("c1", "u1", "twitter", tokenTwitterAccess, nil))

	// Verify using WRONG master key
	_, err = database.VerifyDataIntegrityAndDecryptability(ctx, snapshot, db, wrongMasterKey)
	if err == nil {
		t.Fatalf("expected decryption error with wrong master key, got nil")
	}

	t.Logf("PASS: Restored database with wrong out-of-band master key rejected: %v", err)
}
