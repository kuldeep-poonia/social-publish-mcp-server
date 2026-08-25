package security_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/auth"
)

// TestSecurity_AuthBypassAndTokenTampering verifies that malicious JWT modifications,
// algorithm 'none' stripping, forged signatures, expired tokens, and PKCE tampering are 100% rejected.
func TestSecurity_AuthBypassAndTokenTampering(t *testing.T) {
	jwtSecret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	oauthServer := auth.NewOAuthServer(jwtSecret)
	store := auth.NewInMemorySessionStore()

	_ = oauthServer.RegisterClient("test_client", "", "Test Client", []string{"http://localhost:8080/callback"})

	t.Logf("=== RUNNING AUTH BYPASS & TOKEN TAMPERING PENETRATION SUITE ===")

	// 1. JWT "alg: none" Attack
	t.Run("JWT_Alg_None_Attack", func(t *testing.T) {
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"victim_user_123","exp":9999999999,"scope":"read write"}`))
		noneToken := fmt.Sprintf("%s.%s.", header, payload) // No signature

		claims, err := auth.ValidateAccessToken(noneToken, jwtSecret)
		if err == nil || claims != nil {
			t.Fatalf("CRITICAL AUTH BYPASS: JWT with 'alg: none' was accepted!")
		}
		t.Logf("PASS: 'alg: none' token rejected: %v", err)
	})

	// 2. JWT Forged Signature with Different Key
	t.Run("JWT_Forged_Secret_Key", func(t *testing.T) {
		wrongSecret := []byte("an-incorrect-and-attacker-controlled-signing-secret-key")
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"victim_user_123","exp":9999999999,"scope":"read write"}`))

		mac := hmac.New(sha256.New, wrongSecret)
		mac.Write([]byte(header + "." + payload))
		forgedSig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

		forgedToken := fmt.Sprintf("%s.%s.%s", header, payload, forgedSig)
		claims, err := auth.ValidateAccessToken(forgedToken, jwtSecret)
		if err == nil || claims != nil {
			t.Fatalf("CRITICAL AUTH BYPASS: Forged signature token was accepted!")
		}
		t.Logf("PASS: Forged signature token rejected: %v", err)
	})

	// 3. Expired Token Replay Attack
	t.Run("JWT_Expired_Token_Replay", func(t *testing.T) {
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
		payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":"user_123","exp":%d,"scope":"read"}`, time.Now().Add(-1*time.Hour).Unix())))

		mac := hmac.New(sha256.New, jwtSecret)
		mac.Write([]byte(header + "." + payload))
		sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

		expiredToken := fmt.Sprintf("%s.%s.%s", header, payload, sig)
		claims, err := auth.ValidateAccessToken(expiredToken, jwtSecret)
		if err == nil || claims != nil {
			t.Fatalf("CRITICAL AUTH BYPASS: Expired JWT was accepted!")
		}
		t.Logf("PASS: Expired JWT rejected: %v", err)
	})

	// 4. PKCE Code Challenge Verification Bypass
	t.Run("PKCE_Code_Verifier_Tampering", func(t *testing.T) {
		validVerifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		h := sha256.Sum256([]byte(validVerifier))
		validChallenge := base64.RawURLEncoding.EncodeToString(h[:])

		authReq := &auth.AuthorizeRequest{
			ResponseType:        "code",
			ClientID:            "test_client",
			RedirectURI:         "http://localhost:8080/callback",
			Scope:               "read",
			CodeChallenge:       validChallenge,
			CodeChallengeMethod: "S256",
			UserID:              "test_user_pkce",
		}

		code, err := oauthServer.Authorize(authReq)
		if err != nil {
			t.Fatalf("failed generating auth code: %v", err)
		}

		// Attacker attempts to exchange with incorrect verifier
		tamperedVerifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk_TAMPERED"
		_, tokenErr := oauthServer.ExchangeCodeForTokens(context.Background(), &auth.TokenExchangeRequest{
			GrantType:    "authorization_code",
			Code:         code,
			ClientID:     "test_client",
			RedirectURI:  "http://localhost:8080/callback",
			CodeVerifier: tamperedVerifier,
		}, store)

		if tokenErr == nil {
			t.Fatalf("CRITICAL SECURITY VIOLATION: PKCE exchange succeeded with tampered code verifier!")
		}
		t.Logf("PASS: PKCE code verifier tampering rejected: %v", tokenErr)
	})

	// 5. Instagram Webhook HMAC Signature Tampering
	t.Run("Instagram_Webhook_HMAC_Tampering", func(t *testing.T) {
		webhookSecret := "instagram_production_webhook_secret_key_123"
		bodyPayload := []byte(`{"object":"instagram","entry":[{"id":"123","time":1600000000}]}`)

		mac := hmac.New(sha256.New, []byte(webhookSecret))
		mac.Write(bodyPayload)
		validSig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

		// Case A: Tampered Body
		tamperedBody := []byte(`{"object":"instagram","entry":[{"id":"123","time":1600000000}],"injected":"admin"}`)
		if verifyWebhookHMAC(tamperedBody, validSig, webhookSecret) {
			t.Fatalf("CRITICAL WEBHOOK VIOLATION: Tampered webhook body accepted!")
		}
		t.Logf("PASS: Tampered webhook payload rejected by HMAC")

		// Case B: Tampered Signature
		tamperedSig := "sha256=0000000000000000000000000000000000000000000000000000000000000000"
		if verifyWebhookHMAC(bodyPayload, tamperedSig, webhookSecret) {
			t.Fatalf("CRITICAL WEBHOOK VIOLATION: Forged webhook signature accepted!")
		}
		t.Logf("PASS: Forged webhook signature rejected by HMAC")
	})
}

func verifyWebhookHMAC(payload []byte, signatureHeader, secret string) bool {
	if !strings.HasPrefix(signatureHeader, "sha256=") {
		return false
	}
	expectedHash := strings.TrimPrefix(signatureHeader, "sha256=")

	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	actualHash := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(actualHash), []byte(expectedHash))
}
