package auth

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestOAuthHandshake_SteppedConcurrencyLoad(t *testing.T) {
	secret := []byte("a-very-secure-jwt-signing-secret-minimum-32-chars-long")
	oauthServer := NewOAuthServer(secret)

	clientID := "client_load_mcp"
	redirectURI := "https://app.socialmcp.io/callback"
	_ = oauthServer.RegisterClient(clientID, "secret", "Load Test Client", []string{redirectURI})

	store := NewInMemorySessionStore()
	ctx := context.Background()

	// Stepped load levels to identify saturation/degradation curve: 100 -> 300 -> 500 -> 800 -> 1000 -> 1500 RPS
	scenarios := []struct {
		name          string
		targetRateRPS int
		totalRequests int
	}{
		{name: "OAuth_Stepped_100_RPS", targetRateRPS: 100, totalRequests: 300},
		{name: "OAuth_Stepped_300_RPS", targetRateRPS: 300, totalRequests: 600},
		{name: "OAuth_Stepped_500_RPS", targetRateRPS: 500, totalRequests: 1000},
		{name: "OAuth_Stepped_800_RPS", targetRateRPS: 800, totalRequests: 1600},
		{name: "OAuth_Stepped_1000_RPS", targetRateRPS: 1000, totalRequests: 2000},
		{name: "OAuth_Stepped_1500_RPS", targetRateRPS: 1500, totalRequests: 3000},
	}

	t.Logf("================================================================================")
	t.Logf("   STEPPED OAUTH 2.1 + PKCE HANDSHAKE CONCURRENCY LOAD & SATURATION TEST        ")
	t.Logf("   (Each Handshake = PKCE S256 Gen + Authorize + Token Exchange + JWT Sign)     ")
	t.Logf("================================================================================")

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			latenciesMs := make([]float64, sc.totalRequests)
			var errorCount int64

			interval := time.Second / time.Duration(sc.targetRateRPS)
			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			var wg sync.WaitGroup
			startOverall := time.Now()

			for i := 0; i < sc.totalRequests; i++ {
				<-ticker.C
				wg.Add(1)

				go func(idx int) {
					defer wg.Done()
					opStart := time.Now()

					// Step 1: Generate PKCE S256 verifier & challenge
					verifier, challenge, err := GeneratePKCEPair()
					if err != nil {
						atomic.AddInt64(&errorCount, 1)
						return
					}

					userID := fmt.Sprintf("usr_oauth_load_%d", idx)

					// Step 2: Authorize (issuance of 60s single-use auth code)
					code, err := oauthServer.Authorize(&AuthorizeRequest{
						ResponseType:        "code",
						ClientID:            clientID,
						RedirectURI:         redirectURI,
						CodeChallenge:       challenge,
						CodeChallengeMethod: "S256",
						UserID:              userID,
					})
					if err != nil {
						atomic.AddInt64(&errorCount, 1)
						return
					}

					// Step 3: Token Exchange (PKCE verification + code consumption + JWT token issuance)
					pair, err := oauthServer.ExchangeCodeForTokens(ctx, &TokenExchangeRequest{
						GrantType:    "authorization_code",
						Code:         code,
						ClientID:     clientID,
						CodeVerifier: verifier,
						RedirectURI:  redirectURI,
					}, store)
					if err != nil || pair == nil || pair.AccessToken == "" {
						atomic.AddInt64(&errorCount, 1)
						return
					}

					duration := time.Since(opStart)
					latenciesMs[idx] = float64(duration.Nanoseconds()) / 1_000_000.0
				}(i)
			}

			wg.Wait()
			totalElapsed := time.Since(startOverall)

			sortedLatencies := make([]float64, len(latenciesMs))
			copy(sortedLatencies, latenciesMs)
			sort.Float64s(sortedLatencies)

			n := len(sortedLatencies)
			p50 := sortedLatencies[int(float64(n)*0.50)]
			p90 := sortedLatencies[int(float64(n)*0.90)]
			p95 := sortedLatencies[int(float64(n)*0.95)]
			p99 := sortedLatencies[int(float64(n)*0.99)]
			maxLat := sortedLatencies[n-1]
			minLat := sortedLatencies[0]

			actualRPS := float64(sc.totalRequests) / totalElapsed.Seconds()
			errorRate := (float64(errorCount) / float64(sc.totalRequests)) * 100.0

			t.Logf("[%s] Target: %d RPS | Total Req: %d | Actual RPS: %.1f | Elapsed: %v",
				sc.name, sc.targetRateRPS, sc.totalRequests, actualRPS, totalElapsed)
			t.Logf("[%s] Errors: %d (Error Rate: %.2f%%)", sc.name, errorCount, errorRate)
			t.Logf("[%s] Handshake Latency -> min: %.3fms | p50: %.3fms | p90: %.3fms | p95: %.3fms | p99: %.3fms | max: %.3fms",
				sc.name, minLat, p50, p90, p95, p99, maxLat)

			if errorCount != 0 {
				t.Fatalf("expected 0 errors under %s, got %d", sc.name, errorCount)
			}
		})
	}
	t.Logf("================================================================================")
}
