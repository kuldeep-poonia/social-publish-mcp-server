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

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/crypto"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestSessionCreation_RealDatabaseLoadConcurrency(t *testing.T) {
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("failed to load configuration: %v", err)
	}

	// 1. Connect to Real PostgreSQL with standard production connection pool limits
	db, err := sql.Open("pgx", cfg.PostgresDSN())
	if err != nil {
		t.Fatalf("failed connecting to real Postgres: %v", err)
	}
	defer db.Close()

	const maxOpenConns = 25
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Skipf("skipping real database load test: Postgres not reachable at %s: %v", cfg.PostgresDSN(), err)
		return
	}

	// 2. Run Migrations to ensure schema is fully populated
	migrationsDir := filepath.Join("..", "..", "migrations")
	migrations, err := LoadMigrations(migrationsDir)
	if err != nil {
		t.Fatalf("failed loading migrations: %v", err)
	}
	migrator := NewMigrator(db)
	if _, err := migrator.Up(ctx, migrations); err != nil {
		t.Fatalf("failed running migrations on real DB: %v", err)
	}

	// 3. Initialize Repositories with real DB and real AES key
	cryptoKey := make([]byte, crypto.KeySizeAES256)
	copy(cryptoKey, cfg.TokenEncryptionKey)
	repo := NewRepository(db, cryptoKey, &MockAuditWriter{})

	// 4. Create base test user for foreign key integrity
	testUserEmail := fmt.Sprintf("loadtest_%d@example.com", time.Now().UnixNano())
	testUsername := fmt.Sprintf("loaduser_%d", time.Now().UnixNano()%100000)
	user, err := repo.CreateUser(ctx, testUserEmail, testUsername)
	if err != nil {
		t.Fatalf("failed creating load test user: %v", err)
	}

	// 5. Test Scenarios: 100 RPS, 500 RPS, 1000 RPS hitting real Postgres connection pool
	scenarios := []struct {
		name          string
		targetRateRPS int
		totalRequests int
	}{
		{name: "DB_Load_100_RPS", targetRateRPS: 100, totalRequests: 300},
		{name: "DB_Load_500_RPS", targetRateRPS: 500, totalRequests: 1000},
		{name: "DB_Load_1000_RPS", targetRateRPS: 1000, totalRequests: 2000},
	}

	t.Logf("================================================================================")
	t.Logf("     REAL POSTGRESQL DATABASE SESSION CREATION & CONNECTION POOL LOAD TEST      ")
	t.Logf("     (Connection Pool: MaxOpenConns=%d, Each Request = 2 Real DB INSERTs)       ", maxOpenConns)
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

					// Real database write: store session + audit log
					sessionHash := fmt.Sprintf("db_load_hash_%d_%d", idx, time.Now().UnixNano())
					expiresAt := time.Now().UTC().Add(30 * 24 * time.Hour)

					err := repo.StoreUserSession(ctx, sessionHash, user.ID, expiresAt)
					if err != nil {
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
			t.Logf("[%s] Real DB Latency -> min: %.3fms | p50: %.3fms | p90: %.3fms | p95: %.3fms | p99: %.3fms | max: %.3fms",
				sc.name, minLat, p50, p90, p95, p99, maxLat)

			if errorCount != 0 {
				t.Fatalf("expected 0 errors under %s, got %d", sc.name, errorCount)
			}
		})
	}
	t.Logf("================================================================================")
}
