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

	// Mock queries for GenerateIntegritySnapshot
	usersCols := []string{"id", "email", "created_at"}
	mock.ExpectQuery("SELECT \\* FROM users").
		WillReturnRows(sqlmock.NewRows(usersCols).
			AddRow("u1", "alice@example.com", time.Now().UTC()).
			AddRow("u2", "bob@example.com", time.Now().UTC()).
			AddRow("u3", "charlie@example.com", time.Now().UTC()))

	connCols := []string{"id", "user_id", "platform", "encrypted_access_token", "encrypted_refresh_token", "is_active"}
	mock.ExpectQuery("SELECT \\* FROM platform_connections").
		WillReturnRows(sqlmock.NewRows(connCols).
			AddRow("c1", "u1", "twitter", tokenTwitterAccess, tokenTwitterRefresh, true).
			AddRow("c2", "u2", "youtube", tokenYouTubeAccess, tokenYouTubeRefresh, true).
			AddRow("c3", "u3", "instagram", tokenInstaAccess, nil, true))

	tables := []string{"users", "platform_connections"}

	snapshot, err := database.GenerateIntegritySnapshot(ctx, db, tables)
	if err != nil {
		t.Fatalf("GenerateIntegritySnapshot failed: %v", err)
	}

	if snapshot.TotalTables != 2 {
		t.Errorf("expected 2 tables in snapshot, got %d", snapshot.TotalTables)
	}
	if snapshot.TotalRows != 6 {
		t.Errorf("expected 6 total rows, got %d", snapshot.TotalRows)
	}
	if snapshot.CombinedChecksum == "" {
		t.Errorf("expected non-empty combined SHA-256 checksum")
	}

	t.Logf("=== GENERATED DATABASE INTEGRITY SNAPSHOT ===")
	t.Logf("Total Tables:      %d", snapshot.TotalTables)
	t.Logf("Total Rows:        %d", snapshot.TotalRows)
	t.Logf("Combined Checksum: %s", snapshot.CombinedChecksum)

	// Mock queries for VerifyDataIntegrityAndDecryptability
	// Re-query users table for verification
	mock.ExpectQuery("SELECT \\* FROM users").
		WillReturnRows(sqlmock.NewRows(usersCols).
			AddRow("u1", "alice@example.com", time.Now().UTC()).
			AddRow("u2", "bob@example.com", time.Now().UTC()).
			AddRow("u3", "charlie@example.com", time.Now().UTC()))

	// Re-query platform_connections table for verification
	mock.ExpectQuery("SELECT \\* FROM platform_connections").
		WillReturnRows(sqlmock.NewRows(connCols).
			AddRow("c1", "u1", "twitter", tokenTwitterAccess, tokenTwitterRefresh, true).
			AddRow("c2", "u2", "youtube", tokenYouTubeAccess, tokenYouTubeRefresh, true).
			AddRow("c3", "u3", "instagram", tokenInstaAccess, nil, true))

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

	t.Logf("=== DATABASE INTEGRITY & DECRYPTABILITY DRILL RESULTS ===")
	t.Logf("Drill Status:               %s", report.Status)
	t.Logf("Verification Duration:      %v", report.VerificationDuration)
	t.Logf("Tables Verified:            %d / %d", report.TablesVerified, snapshot.TotalTables)
	t.Logf("Checksum Match Parity:      %d / %d (%.2f%%)", report.ChecksumMatches, report.TablesVerified, report.IntegrityMatchRate)
	t.Logf("Encrypted Tokens Checked:   %d", report.TokensVerified)
	t.Logf("Tokens Decrypted Success:   %d (%.2f%%)", report.TokensDecryptedSuccess, report.DecryptabilityRate)

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
