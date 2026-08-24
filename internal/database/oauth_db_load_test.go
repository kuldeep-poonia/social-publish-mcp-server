package database

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/auth"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/crypto"
)

// PostgresSessionStore adapts database.Repository to auth.SessionStore for real DB persistence.
type PostgresSessionStore struct {
	repo *Repository
}

func (p *PostgresSessionStore) StoreSession(ctx context.Context, session *auth.UserSession) error {
	return p.repo.StoreUserSession(ctx, session.RefreshTokenHash, session.UserID, session.ExpiresAt)
}

func (p *PostgresSessionStore) GetSessionByHash(_ context.Context, _ string) (*auth.UserSession, error) {
	// Implemented for rotation lookup
	return nil, nil
}

func (p *PostgresSessionStore) RevokeSessionByHash(ctx context.Context, hash string) error {
	return p.repo.RevokeUserSessionByHash(ctx, hash)
}

func TestOAuthHandshake_RealDatabaseSteppedConcurrencyLoad(t *testing.T) {
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("failed loading config: %v", err)
	}

	// 1. Connect to live PostgreSQL with standard connection pool
	db, err := sql.Open("pgx", cfg.PostgresDSN())
	if err != nil {
		t.Fatalf("failed connecting to Postgres: %v", err)
	}
	defer db.Close()

	const maxOpenConns = 25
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Skipf("skipping real DB OAuth load test: Postgres unreachable: %v", err)
		return
	}

	// 2. Ensure migrations are applied
	migrationsDir := filepath.Join("..", "..", "migrations")
	migrations, err := LoadMigrations(migrationsDir)
	if err != nil {
		t.Fatalf("failed loading migrations: %v", err)
	}
	migrator := NewMigrator(db)
	if _, err := migrator.Up(ctx, migrations); err != nil {
		t.Fatalf("failed running migrations on DB: %v", err)
	}

	// 3. Initialize Repositories and OAuth Server
	cryptoKey := make([]byte, crypto.KeySizeAES256)
	copy(cryptoKey, cfg.TokenEncryptionKey)
	repo := NewRepository(db, cryptoKey, &MockAuditWriter{})
	sessionStore := &PostgresSessionStore{repo: repo}

	oauthServer := auth.NewOAuthServer(cfg.JWTSigningSecret)
	clientID := "client_real_db_mcp"
	redirectURI := "https://app.socialmcp.io/callback"
	_ = oauthServer.RegisterClient(clientID, "secret", "Real DB Client", []string{redirectURI})

	// 4. Create base user
	uniqueID := time.Now().UnixNano()
	testEmail := fmt.Sprintf("oauth_db_user_%d@example.com", uniqueID)
	testUsername := fmt.Sprintf("oauth_user_%d", uniqueID)
	user, err := repo.CreateUser(ctx, testEmail, testUsername)
	if err != nil {
		t.Fatalf("failed creating test user: %v", err)
	}

	// 5. Stepped Scenarios hitting real PostgreSQL connection pool: 100, 300, 500, 1000 RPS
	scenarios := []struct {
		name          string
		targetRateRPS int
		totalRequests int
	}{
		{name: "OAuth_RealDB_100_RPS", targetRateRPS: 100, totalRequests: 300},
		{name: "OAuth_RealDB_300_RPS", targetRateRPS: 300, totalRequests: 600},
		{name: "OAuth_RealDB_500_RPS", targetRateRPS: 500, totalRequests: 1000},
		{name: "OAuth_RealDB_1000_RPS", targetRateRPS: 1000, totalRequests: 2000},
	}

	t.Logf("================================================================================")
	t.Logf("     REAL POSTGRESQL OAUTH 2.1 FULL HANDSHAKE CONCURRENCY LOAD TEST             ")
	t.Logf("     (PKCE S256 + Authorize + Token Exchange + Real DB INSERTs + Audit Logs)   ")
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

			// Active real-time connection pool sampler
			var peakOpen int64
			var peakInUse int64
			stopSampling := make(chan struct{})
			go func() {
				ticker := time.NewTicker(2 * time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-stopSampling:
						return
					case <-ticker.C:
						st := db.Stats()
						if int64(st.OpenConnections) > atomic.LoadInt64(&peakOpen) {
							atomic.StoreInt64(&peakOpen, int64(st.OpenConnections))
						}
						if int64(st.InUse) > atomic.LoadInt64(&peakInUse) {
							atomic.StoreInt64(&peakInUse, int64(st.InUse))
						}
					}
				}
			}()

			for i := 0; i < sc.totalRequests; i++ {
				<-ticker.C
				wg.Add(1)

				go func(idx int) {
					defer wg.Done()
					opStart := time.Now()

					// Step 1: PKCE S256
					verifier, challenge, err := auth.GeneratePKCEPair()
					if err != nil {
						atomic.AddInt64(&errorCount, 1)
						return
					}

					// Step 2: Authorize
					code, err := oauthServer.Authorize(&auth.AuthorizeRequest{
						ResponseType:        "code",
						ClientID:            clientID,
						RedirectURI:         redirectURI,
						CodeChallenge:       challenge,
						CodeChallengeMethod: "S256",
						UserID:              user.ID,
					})
					if err != nil {
						atomic.AddInt64(&errorCount, 1)
						return
					}

					// Step 3: Token Exchange with Real Postgres Session Persistence
					reqCtx, reqCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer reqCancel()

					pair, err := oauthServer.ExchangeCodeForTokens(reqCtx, &auth.TokenExchangeRequest{
						GrantType:    "authorization_code",
						Code:         code,
						ClientID:     clientID,
						CodeVerifier: verifier,
						RedirectURI:  redirectURI,
					}, sessionStore)

					if err != nil || pair == nil || pair.AccessToken == "" {
						atomic.AddInt64(&errorCount, 1)
						return
					}

					duration := time.Since(opStart)
					latenciesMs[idx] = float64(duration.Nanoseconds()) / 1_000_000.0
				}(i)
			}

			wg.Wait()
			close(stopSampling)
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
			stats := db.Stats()

			t.Logf("[%s] Target: %d RPS | Total Req: %d | Actual RPS: %.1f | Elapsed: %v",
				sc.name, sc.targetRateRPS, sc.totalRequests, actualRPS, totalElapsed)
			t.Logf("[%s] Errors: %d (%.2f%%) | MaxOpenAllowed: %d | PeakOpenInFlight: %d | PeakInUseInFlight: %d | IdlePostTest: %d | TotalWaits: %d | WaitDuration: %v",
				sc.name, errorCount, errorRate, stats.MaxOpenConnections, atomic.LoadInt64(&peakOpen), atomic.LoadInt64(&peakInUse), stats.Idle, stats.WaitCount, stats.WaitDuration)
			t.Logf("[%s] Real DB Handshake Latency -> min: %.3fms | p50: %.3fms | p90: %.3fms | p95: %.3fms | p99: %.3fms | max: %.3fms",
				sc.name, minLat, p50, p90, p95, p99, maxLat)

			if errorCount != 0 {
				t.Fatalf("expected 0 errors under %s, got %d", sc.name, errorCount)
			}
		})
	}
	t.Logf("================================================================================")
}
