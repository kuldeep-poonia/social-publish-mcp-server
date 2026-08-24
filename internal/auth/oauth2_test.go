package auth

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestPKCEDowngradeAttack_100PercentRejection(t *testing.T) {
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	oauthServer := NewOAuthServer(secret)

	clientID := "client_mcp_test"
	allowedURI := "http://localhost:3000/callback"
	_ = oauthServer.RegisterClient(clientID, "secret", "MCP Client", []string{allowedURI})

	// 1. Attack Scenario A: Missing PKCE Challenge & Method
	_, err := oauthServer.Authorize(&AuthorizeRequest{
		ResponseType:        "code",
		ClientID:            clientID,
		RedirectURI:         allowedURI,
		CodeChallenge:       "",
		CodeChallengeMethod: "",
		UserID:              "usr_attacker",
	})
	if !errors.Is(err, ErrPKCERequired) {
		t.Fatalf("expected ErrPKCERequired for missing PKCE, got: %v", err)
	}

	// 2. Attack Scenario B: Downgrade to 'plain' Method
	const iterations = 100
	plainRejectionCount := 0

	for i := 0; i < iterations; i++ {
		_, err := oauthServer.Authorize(&AuthorizeRequest{
			ResponseType:        "code",
			ClientID:            clientID,
			RedirectURI:         allowedURI,
			CodeChallenge:       fmt.Sprintf("plain_challenge_%d", i),
			CodeChallengeMethod: "plain", // Downgrade attempt
			UserID:              "usr_attacker",
		})
		if errors.Is(err, ErrInvalidCodeChallengeMethod) {
			plainRejectionCount++
		}
	}

	rejectionRate := (float64(plainRejectionCount) / float64(iterations)) * 100.0
	t.Logf("=== PKCE DOWNGRADE ATTACK TEST RESULTS ===")
	t.Logf("Total 'plain' Downgrade Attempts: %d", iterations)
	t.Logf("Rejections: %d / %d (Rejection Rate: %.2f%%)", plainRejectionCount, iterations, rejectionRate)
	t.Logf("Security Requirement: 100%% rejection of plain method (Met: %t)", rejectionRate == 100.0)

	if rejectionRate != 100.0 {
		t.Fatalf("CRITICAL SECURITY FAILURE: PKCE plain method was not rejected 100%% of the time!")
	}

	// 3. Attack Scenario C: Tampered Code Verifier during exchange
	verifier, challenge, _ := GeneratePKCEPair()
	code, err := oauthServer.Authorize(&AuthorizeRequest{
		ResponseType:        "code",
		ClientID:            clientID,
		RedirectURI:         allowedURI,
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		UserID:              "usr_victim",
	})
	if err != nil {
		t.Fatalf("valid authorize failed: %v", err)
	}

	store := NewInMemorySessionStore()
	_, err = oauthServer.ExchangeCodeForTokens(context.Background(), &TokenExchangeRequest{
		GrantType:    "authorization_code",
		Code:         code,
		ClientID:     clientID,
		CodeVerifier: verifier + "_wrong",
		RedirectURI:  allowedURI,
	}, store)

	if !errors.Is(err, ErrInvalidCodeVerifier) {
		t.Fatalf("expected ErrInvalidCodeVerifier, got: %v", err)
	}
}

func TestAuthorizationCode_ReplayAttack_100PercentRejection(t *testing.T) {
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	oauthServer := NewOAuthServer(secret)

	clientID := "client_mcp_desktop"
	allowedURI := "http://localhost:8080/callback"
	_ = oauthServer.RegisterClient(clientID, "secret", "Desktop Client", []string{allowedURI})

	verifier, challenge, _ := GeneratePKCEPair()
	code, err := oauthServer.Authorize(&AuthorizeRequest{
		ResponseType:        "code",
		ClientID:            clientID,
		RedirectURI:         allowedURI,
		CodeChallenge:       challenge,
		CodeChallengeMethod: "S256",
		UserID:              "usr_legitimate",
	})
	if err != nil {
		t.Fatalf("authorize failed: %v", err)
	}

	store := NewInMemorySessionStore()
	ctx := context.Background()

	// 1. Legitimate first exchange
	pair, err := oauthServer.ExchangeCodeForTokens(ctx, &TokenExchangeRequest{
		GrantType:    "authorization_code",
		Code:         code,
		ClientID:     clientID,
		CodeVerifier: verifier,
		RedirectURI:  allowedURI,
	}, store)
	if err != nil {
		t.Fatalf("legitimate exchange failed: %v", err)
	}
	if pair.AccessToken == "" {
		t.Fatal("expected access token in pair")
	}

	// 2. ADVERSARIAL REPLAY: Attempt to reuse the consumed code 100 times
	const replayAttempts = 100
	rejectionCount := 0
	startReplay := time.Now()

	for i := 0; i < replayAttempts; i++ {
		_, err := oauthServer.ExchangeCodeForTokens(ctx, &TokenExchangeRequest{
			GrantType:    "authorization_code",
			Code:         code,
			ClientID:     clientID,
			CodeVerifier: verifier,
			RedirectURI:  allowedURI,
		}, store)
		if errors.Is(err, ErrInvalidOrConsumedCode) {
			rejectionCount++
		}
	}

	totalDuration := time.Since(startReplay)
	avgTimeToRejection := float64(totalDuration.Microseconds()) / float64(replayAttempts)
	rejectionRate := (float64(rejectionCount) / float64(replayAttempts)) * 100.0

	t.Logf("=== AUTHORIZATION CODE REPLAY TEST RESULTS ===")
	t.Logf("Total Replay Attempts: %d", replayAttempts)
	t.Logf("Rejections: %d / %d (Rejection Rate: %.2f%%)", rejectionCount, replayAttempts, rejectionRate)
	t.Logf("Avg Time to Detect & Reject Replay: %.3f µs", avgTimeToRejection)

	if rejectionRate != 100.0 {
		t.Fatalf("CRITICAL SECURITY FAILURE: consumed authorization code was accepted upon replay!")
	}
}

func TestRedirectURI_InjectionFuzzing_0Bypasses(t *testing.T) {
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	oauthServer := NewOAuthServer(secret)

	clientID := "client_mcp_secure"
	legitURI := "https://app.socialmcp.io/oauth/callback"
	_ = oauthServer.RegisterClient(clientID, "secret", "Web Client", []string{legitURI})

	injectionPayloads := []string{
		"https://evil.com/callback",
		"http://app.socialmcp.io/oauth/callback", // Scheme downgrade
		"https://app.socialmcp.io/oauth/callback/../../evil",
		"https://app.socialmcp.io.evil.com/oauth/callback",
		"https://evil.com?https://app.socialmcp.io/oauth/callback",
		"https://app.socialmcp.io/oauth/callback@evil.com",
		"javascript:alert(1)",
		"data:text/html,<script>alert(1)</script>",
		"https://app.socialmcp.io/oauth/callback#evil",
		"https://app.socialmcp.io:8080/oauth/callback",
	}

	for i := 0; i < 50; i++ {
		injectionPayloads = append(injectionPayloads,
			fmt.Sprintf("https://evil-%d.com/steal-code", i),
			fmt.Sprintf("https://app.socialmcp.io/oauth/callback?attacker=%d", i),
		)
	}

	_, challenge, _ := GeneratePKCEPair()
	bypasses := 0
	totalTested := 0

	for _, payload := range injectionPayloads {
		totalTested++
		_, err := oauthServer.Authorize(&AuthorizeRequest{
			ResponseType:        "code",
			ClientID:            clientID,
			RedirectURI:         payload,
			CodeChallenge:       challenge,
			CodeChallengeMethod: "S256",
			UserID:              "usr_target",
		})
		if err == nil {
			bypasses++
			t.Errorf("CRITICAL SECURITY FLAW: Open redirect accepted unauthorized URI: %s", payload)
		}
	}

	bypassRate := (float64(bypasses) / float64(totalTested)) * 100.0
	t.Logf("=== REDIRECT URI INJECTION FUZZING RESULTS ===")
	t.Logf("Total Malicious Redirect URIs Tested: %d", totalTested)
	t.Logf("Unauthorized Redirect Bypasses: %d (Bypass Rate: %.2f%%)", bypasses, bypassRate)
	t.Logf("Security Target: 0 bypasses (Met: %t)", bypasses == 0)

	if bypasses != 0 {
		t.Fatalf("CRITICAL SECURITY FAILURE: %d open redirect bypasses allowed!", bypasses)
	}
}
