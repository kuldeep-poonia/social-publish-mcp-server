package twitter

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/crypto"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
)

func setupTestDB(t *testing.T) (*sql.DB, *database.Repository, *config.Config) {
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("failed loading config: %v", err)
	}

	db, err := sql.Open("pgx", cfg.PostgresDSN())
	if err != nil {
		t.Fatalf("failed connecting to Postgres: %v", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		t.Skipf("skipping test: Postgres unreachable: %v", err)
	}

	// Apply migrations
	migrationsDir := filepath.Join("..", "..", "..", "migrations")
	migrations, err := database.LoadMigrations(migrationsDir)
	if err != nil {
		t.Fatalf("failed loading migrations: %v", err)
	}
	migrator := database.NewMigrator(db)
	if _, err := migrator.Up(ctx, migrations); err != nil {
		t.Fatalf("failed running migrations: %v", err)
	}

	cryptoKey := make([]byte, crypto.KeySizeAES256)
	copy(cryptoKey, cfg.TokenEncryptionKey)
	repo := database.NewRepository(db, cryptoKey, nil)

	return db, repo, cfg
}

func TestTwitterIdempotency_ComprehensiveSuite(t *testing.T) {
	db, repo, _ := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// Setup test user and mock Twitter connection
	uniqueID := time.Now().UnixNano()
	user, err := repo.CreateUser(ctx, fmt.Sprintf("idemp_user_%d@example.com", uniqueID), fmt.Sprintf("idemp_user_%d", uniqueID))
	if err != nil {
		t.Fatalf("failed creating user: %v", err)
	}

	_ = repo.SavePlatformConnection(ctx, user.ID, "twitter", []byte("fake-access-token"), []byte("fake-refresh-token"), time.Now().Add(1*time.Hour), RequiredScopes)

	// Mock Twitter API Server
	var tweetAPICallCount int64
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if stringsContains(r.URL.Path, "/tweets") && r.Method == http.MethodPost {
			atomic.AddInt64(&tweetAPICallCount, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]string{
					"id":   fmt.Sprintf("tweet_%d", time.Now().UnixNano()),
					"text": "Idempotent test tweet",
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer mockServer.Close()

	client := NewCustomClient(mockServer.Client(), mockServer.URL, mockServer.URL, "test_client_id", "test_secret")
	service := NewService(db, repo, client)

	t.Logf("================================================================================")
	t.Logf("     TWITTER APPLICATION-LEVEL IDEMPOTENCY & CRASH-RECOVERY TEST SUITE          ")
	t.Logf("================================================================================")

	// ============================================================================
	// TEST A: STALE SINGLE-WORKER CRASH RECOVERY (60s Threshold)
	// ============================================================================
	t.Run("TestA_StaleCrashRecovery", func(t *testing.T) {
		staleKey := fmt.Sprintf("stale_key_%d", time.Now().UnixNano())
		staleTime := time.Now().UTC().Add(-90 * time.Second) // 90 seconds old (crashed worker)

		// Insert stale processing row
		_, err := db.ExecContext(ctx, `INSERT INTO posts (user_id, platform, content, status, idempotency_key, created_at, updated_at)
		                              VALUES ($1, 'twitter', 'Stale tweet', 'processing', $2, $3, $3)`,
			user.ID, staleKey, staleTime)
		if err != nil {
			t.Fatalf("failed inserting stale test row: %v", err)
		}

		initialCalls := atomic.LoadInt64(&tweetAPICallCount)

		// New request arrives for the stale key -> must reclaim and publish successfully
		resp, err := service.PublishTweet(ctx, &PublishTweetRequest{
			UserID:         user.ID,
			Content:        "Stale tweet recovered",
			IdempotencyKey: staleKey,
		})
		if err != nil {
			t.Fatalf("stale recovery publish failed: %v", err)
		}
		if resp.Status != "published" || resp.PlatformPostID == "" {
			t.Fatalf("expected published status, got %s", resp.Status)
		}

		callsMade := atomic.LoadInt64(&tweetAPICallCount) - initialCalls
		if callsMade != 1 {
			t.Fatalf("expected exactly 1 Twitter call during stale recovery, got %d", callsMade)
		}
		t.Logf("[Test A: Stale Worker Recovery] Stale 'processing' row (90s old) reclaimed cleanly and published. (Success: 100%%)")
	})

	// ============================================================================
	// TEST B: 50-GOROUTINE CONCURRENT FRESH INSERT RACE
	// ============================================================================
	t.Run("TestB_50GoroutineConcurrentRace", func(t *testing.T) {
		raceKey := fmt.Sprintf("race_fresh_key_%d", time.Now().UnixNano())
		const concurrency = 50
		var wg sync.WaitGroup
		var successCount int64
		var inFlightConflictCount int64
		initialCalls := atomic.LoadInt64(&tweetAPICallCount)

		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				resp, err := service.PublishTweet(ctx, &PublishTweetRequest{
					UserID:         user.ID,
					Content:        "Concurrent race tweet",
					IdempotencyKey: raceKey,
				})
				if err == nil && resp.Status == "published" {
					atomic.AddInt64(&successCount, 1)
				} else if err != nil && (err == ErrPostProcessingInProgress || stringsContains(err.Error(), "409")) {
					atomic.AddInt64(&inFlightConflictCount, 1)
				}
			}()
		}
		wg.Wait()

		callsMade := atomic.LoadInt64(&tweetAPICallCount) - initialCalls
		t.Logf("[Test B: Concurrent Fresh Race] Total Requests: %d | Published Winner: %d | 409 In-Flight Conflicts: %d | Upstream Calls: %d",
			concurrency, successCount, inFlightConflictCount, callsMade)

		if callsMade != 1 {
			t.Fatalf("CRITICAL IDEMPOTENCY FAILURE: Expected exactly 1 Twitter API call under 50 concurrent requests, got %d", callsMade)
		}
	})

	// ============================================================================
	// TEST C: REPLAY OF PUBLISHED POST (CACHED RESULT WITH 0 API CALLS)
	// ============================================================================
	t.Run("TestC_ReplayOfPublishedPost", func(t *testing.T) {
		replayKey := fmt.Sprintf("replay_key_%d", time.Now().UnixNano())

		// First publish
		resp1, err := service.PublishTweet(ctx, &PublishTweetRequest{
			UserID:         user.ID,
			Content:        "Post to be replayed",
			IdempotencyKey: replayKey,
		})
		if err != nil {
			t.Fatalf("first publish failed: %v", err)
		}

		initialCalls := atomic.LoadInt64(&tweetAPICallCount)

		// Replay 10 times
		for i := 0; i < 10; i++ {
			respN, err := service.PublishTweet(ctx, &PublishTweetRequest{
				UserID:         user.ID,
				Content:        "Post to be replayed",
				IdempotencyKey: replayKey,
			})
			if err != nil {
				t.Fatalf("replay %d failed: %v", i+1, err)
			}
			if !respN.IsIdempotentReplay {
				t.Fatalf("replay %d expected IsIdempotentReplay=true", i+1)
			}
			if respN.PlatformPostID != resp1.PlatformPostID {
				t.Fatalf("replay %d returned mismatched platform_post_id", i+1)
			}
		}

		extraCalls := atomic.LoadInt64(&tweetAPICallCount) - initialCalls
		if extraCalls != 0 {
			t.Fatalf("expected 0 additional Twitter calls during replay, got %d", extraCalls)
		}
		t.Logf("[Test C: Published Post Replay] 10 replayed requests returned cached tweet ID (%s) with 0 additional API calls.", resp1.PlatformPostID)
	})

	// ============================================================================
	// TEST D: CONCURRENT STALE RECLAIM RACE (20 GOROUTINES ON DEAD ROW)
	// ============================================================================
	t.Run("TestD_ConcurrentStaleReclaimRace", func(t *testing.T) {
		staleRaceKey := fmt.Sprintf("stale_race_key_%d", time.Now().UnixNano())
		staleTime := time.Now().UTC().Add(-120 * time.Second) // 2 minutes old

		_, err := db.ExecContext(ctx, `INSERT INTO posts (user_id, platform, content, status, idempotency_key, created_at, updated_at)
		                              VALUES ($1, 'twitter', 'Stale race tweet', 'processing', $2, $3, $3)`,
			user.ID, staleRaceKey, staleTime)
		if err != nil {
			t.Fatalf("failed inserting stale race row: %v", err)
		}

		const concurrency = 20
		var wg sync.WaitGroup
		var winnerCount int64
		var inFlightConflictCount int64
		initialCalls := atomic.LoadInt64(&tweetAPICallCount)

		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				resp, err := service.PublishTweet(ctx, &PublishTweetRequest{
					UserID:         user.ID,
					Content:        "Stale race tweet attempt",
					IdempotencyKey: staleRaceKey,
				})
				if err == nil && resp.Status == "published" {
					atomic.AddInt64(&winnerCount, 1)
				} else if err != nil && (err == ErrPostProcessingInProgress || stringsContains(err.Error(), "409")) {
					atomic.AddInt64(&inFlightConflictCount, 1)
				}
			}()
		}
		wg.Wait()

		callsMade := atomic.LoadInt64(&tweetAPICallCount) - initialCalls
		t.Logf("[Test D: Stale Reclaim Race] Total Concurrent Workers: %d | Winner (RowsAffected=1): %d | 409 Conflicts: %d | Upstream Calls: %d",
			concurrency, winnerCount, inFlightConflictCount, callsMade)

		if callsMade != 1 {
			t.Fatalf("CRITICAL RACE FAILURE: Expected exactly 1 Twitter API call during stale reclaim race, got %d", callsMade)
		}
	})
	t.Logf("================================================================================")
}

func stringsContains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 || (len(s) > 0 && len(substr) > 0 && indexOf(s, substr) >= 0))
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
