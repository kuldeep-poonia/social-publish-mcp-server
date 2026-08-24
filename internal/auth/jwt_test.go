package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestIssueAndValidateAccessToken(t *testing.T) {
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	userID := "usr_test_987654321"

	pair, err := IssueSessionTokens(userID, "user", secret)
	if err != nil {
		t.Fatalf("failed to issue session tokens: %v", err)
	}

	if pair.AccessToken == "" || pair.RefreshToken == "" {
		t.Fatal("expected non-empty access and refresh tokens")
	}

	claims, err := ValidateAccessToken(pair.AccessToken, secret)
	if err != nil {
		t.Fatalf("failed to validate valid access token: %v", err)
	}

	if claims.UserID != userID {
		t.Fatalf("expected userID %s, got %s", userID, claims.UserID)
	}
}

func TestTamperedJWTSignature_100PercentRejection(t *testing.T) {
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	pair, err := IssueSessionTokens("usr_alice", "user", secret)
	if err != nil {
		t.Fatalf("failed to issue token: %v", err)
	}

	parts := strings.Split(pair.AccessToken, ".")
	if len(parts) != 3 {
		t.Fatalf("expected 3 parts in JWT, got %d", len(parts))
	}

	// Tamper with signature
	tamperedSig := parts[2] + "corrupt"
	tamperedToken := parts[0] + "." + parts[1] + "." + tamperedSig

	_, err = ValidateAccessToken(tamperedToken, secret)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for tampered signature, got %v", err)
	}
}

func TestTamperedJWTPayload_100PercentRejection(t *testing.T) {
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	pair, err := IssueSessionTokens("usr_alice", "user", secret)
	if err != nil {
		t.Fatalf("failed to issue token: %v", err)
	}

	parts := strings.Split(pair.AccessToken, ".")
	// Tamper with payload byte
	tamperedPayload := parts[1] + "X"
	tamperedToken := parts[0] + "." + tamperedPayload + "." + parts[2]

	_, err = ValidateAccessToken(tamperedToken, secret)
	if err == nil {
		t.Fatal("expected error on tampered payload, got nil")
	}
}

func TestExpiredAccessToken_Rejection(t *testing.T) {
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	now := time.Now().UTC().Add(-20 * time.Minute) // 20 mins ago

	claims := SessionClaims{
		UserID:    "usr_expired",
		Role:      "user",
		IssuedAt:  now.Unix(),
		ExpiresAt: now.Add(10 * time.Minute).Unix(), // Expired 10 mins ago
	}

	expiredToken, err := signJWT(claims, secret)
	if err != nil {
		t.Fatalf("failed to sign token: %v", err)
	}

	_, err = ValidateAccessToken(expiredToken, secret)
	if !errors.Is(err, ErrTokenExpired) {
		t.Fatalf("expected ErrTokenExpired, got %v", err)
	}
}

func TestRotatedRefreshToken_ReplayAttack_100PercentRejection(t *testing.T) {
	ctx := context.Background()
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	store := NewInMemorySessionStore()

	// 1. Initial login: Issue session token pair
	initialPair, err := IssueSessionTokens("usr_bob_777", "user", secret)
	if err != nil {
		t.Fatalf("failed to issue initial tokens: %v", err)
	}

	// Persist initial session
	err = store.StoreSession(ctx, &UserSession{
		RefreshTokenHash: initialPair.RefreshTokenHash,
		UserID:           "usr_bob_777",
		ExpiresAt:        initialPair.RefreshTokenExpiresAt,
		CreatedAt:        time.Now().UTC(),
		IsRevoked:        false,
	})
	if err != nil {
		t.Fatalf("failed to store initial session: %v", err)
	}

	// 2. Legitimate rotation: Rotate using initial refresh token
	rotatedPair1, err := RotateRefreshToken(ctx, initialPair.RefreshToken, store, secret)
	if err != nil {
		t.Fatalf("first rotation failed: %v", err)
	}
	if rotatedPair1.RefreshToken == initialPair.RefreshToken {
		t.Fatal("rotated refresh token must be different from initial token")
	}

	// 3. ADVERSARIAL REPLAY: Attempt to use the OLD (initial) refresh token again
	const replayAttempts = 100
	rejectionCount := 0

	for i := 0; i < replayAttempts; i++ {
		_, err := RotateRefreshToken(ctx, initialPair.RefreshToken, store, secret)
		if errors.Is(err, ErrInvalidOrRevokedRefreshToken) {
			rejectionCount++
		}
	}

	rejectionRate := (float64(rejectionCount) / float64(replayAttempts)) * 100.0
	t.Logf("=== Rotated Refresh Token Replay Rejection Results ===")
	t.Logf("Total Replay Attempts: %d", replayAttempts)
	t.Logf("Rejections: %d / %d (Rejection Rate: %.2f%%)", rejectionCount, replayAttempts, rejectionRate)

	if rejectionRate != 100.0 {
		t.Fatalf("CRITICAL SECURITY FAILURE: expected 100%% rejection of rotated refresh token replay, got %.2f%%", rejectionRate)
	}

	// 4. Verify that the newly rotated token is still valid for one subsequent rotation
	rotatedPair2, err := RotateRefreshToken(ctx, rotatedPair1.RefreshToken, store, secret)
	if err != nil {
		t.Fatalf("valid second rotation failed: %v", err)
	}
	if rotatedPair2.RefreshToken == rotatedPair1.RefreshToken {
		t.Fatal("second rotated token must be unique")
	}
}
