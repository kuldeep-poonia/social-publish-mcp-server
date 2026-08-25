package instagram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/crypto"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

type testAuditWriter struct{}

func (t *testAuditWriter) WriteAuditLog(_ context.Context, _ *models.AuditLog) error {
	return nil
}

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
	repo := database.NewRepository(db, cryptoKey, &testAuditWriter{})

	return db, repo, cfg
}

func TestInstagramIdempotency_ComprehensiveSuite(t *testing.T) {
	db, repo, _ := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	// Setup test user and mock Instagram connection
	uniqueID := time.Now().UnixNano()
	user, err := repo.CreateUser(ctx, fmt.Sprintf("ig_idemp_user_%d@example.com", uniqueID), fmt.Sprintf("ig_user_%d", uniqueID))
	if err != nil {
		t.Fatalf("failed creating user: %v", err)
	}

	_ = repo.SavePlatformConnection(ctx, user.ID, "instagram", []byte("fake-ig-access-token"), []byte("fake-refresh-token"), time.Now().Add(30*24*time.Hour), RequiredScopes)

	var (
		containerCreateCalls int64
		publishMediaCalls    int64
		pollerCalls          int64
	)

	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		// Discovery
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/me/accounts"):
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"data": []map[string]interface{}{
					{
						"id":           "page_101",
						"name":         "Brand Page",
						"access_token": "page_tok",
						"instagram_business_account": map[string]interface{}{
							"id":       "17841499999",
							"username": "brand_insta",
						},
					},
				},
			})

		// Step 2: Publish Media (must be checked BEFORE /media)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/media_publish"):
			atomic.AddInt64(&publishMediaCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id": fmt.Sprintf("published_ig_%d", time.Now().UnixNano()),
			})

		// Step 1: Create Container
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/media"):
			atomic.AddInt64(&containerCreateCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id": fmt.Sprintf("creation_%d", time.Now().UnixNano()),
			})

		// Poller
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/creation_"):
			atomic.AddInt64(&pollerCalls, 1)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"id":          "container_status_ok",
				"status_code": "FINISHED",
			})

		default:
			http.NotFound(w, r)
		}
	}))
	defer mockServer.Close()

	client := NewClient("test_client_id", "test_secret")
	client.apiBase = mockServer.URL
	client.tokenURL = mockServer.URL + "/oauth/access_token"

	stager, _ := NewMediaStager("", "http://localhost:8080")
	defer stager.Close()

	service := NewService(db, repo, client, stager)

	// ============================================================================
	// TEST A: STALE WORKER CRASH RECOVERY (60s Threshold)
	// ============================================================================
	t.Run("TestA_StaleCrashRecovery", func(t *testing.T) {
		staleKey := fmt.Sprintf("stale_ig_key_%d", time.Now().UnixNano())
		staleTime := time.Now().UTC().Add(-90 * time.Second)

		_, err := db.ExecContext(ctx, `INSERT INTO posts (user_id, platform, content, status, idempotency_key, created_at, updated_at)
		                              VALUES ($1, 'instagram', 'Stale IG post', 'processing', $2, $3, $3)`,
			user.ID, staleKey, staleTime)
		if err != nil {
			t.Fatalf("failed inserting stale test row: %v", err)
		}

		initPublishes := atomic.LoadInt64(&publishMediaCalls)

		resp, err := service.Publish(ctx, &PublishPostRequest{
			UserID:         user.ID,
			Caption:        "Stale IG post recovered",
			MediaURLs:      []string{"https://example.com/images/test.jpg"},
			IdempotencyKey: staleKey,
		})
		if err != nil {
			t.Fatalf("expected successful reclaim, got err: %v", err)
		}

		if resp.Status != "published" {
			t.Errorf("expected status 'published', got: %s", resp.Status)
		}
		if atomic.LoadInt64(&publishMediaCalls) != initPublishes+1 {
			t.Errorf("expected 1 publish API call during stale reclaim")
		}
	})

	// ============================================================================
	// TEST B: 50-GOROUTINE CONCURRENT RACE
	// ============================================================================
	t.Run("TestB_50GoroutineConcurrentRace", func(t *testing.T) {
		raceKey := fmt.Sprintf("race_ig_key_%d", time.Now().UnixNano())
		const concurrency = 50

		var (
			wg           sync.WaitGroup
			successCount int64
			inFlight409  int64
			replays      int64
			failures     int64
		)

		startBarrier := make(chan struct{})
		initPublishes := atomic.LoadInt64(&publishMediaCalls)

		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-startBarrier

				resp, err := service.Publish(ctx, &PublishPostRequest{
					UserID:         user.ID,
					Caption:        "High concurrency IG post",
					MediaURLs:      []string{"https://example.com/images/race.jpg"},
					IdempotencyKey: raceKey,
				})

				if err == nil {
					if resp.IsIdempotentReplay {
						atomic.AddInt64(&replays, 1)
					} else {
						atomic.AddInt64(&successCount, 1)
					}
				} else if errors.Is(err, ErrPostProcessingInProgress) {
					atomic.AddInt64(&inFlight409, 1)
				} else {
					atomic.AddInt64(&failures, 1)
				}
			}()
		}

		close(startBarrier)
		wg.Wait()

		totalAccounted := successCount + inFlight409 + replays
		t.Logf("50-Goroutine Race: Winner=%d, 409s=%d, Replays=%d, Failures=%d (Total Accounted=%d/50)",
			successCount, inFlight409, replays, failures, totalAccounted)

		if successCount != 1 {
			t.Errorf("expected exactly 1 winner, got %d", successCount)
		}
		if totalAccounted != concurrency {
			t.Errorf("concurrency leak: expected %d total accounted, got %d", concurrency, totalAccounted)
		}
		if atomic.LoadInt64(&publishMediaCalls) != initPublishes+1 {
			t.Errorf("upstream API call leak: expected exactly 1 upstream publish call, got %d",
				atomic.LoadInt64(&publishMediaCalls)-initPublishes)
		}
	})

	// ============================================================================
	// TEST C: REPLAY OF PUBLISHED POST
	// ============================================================================
	t.Run("TestC_ReplayOfPublishedPost", func(t *testing.T) {
		replayKey := fmt.Sprintf("replay_ig_key_%d", time.Now().UnixNano())

		// First publish
		resp1, err := service.Publish(ctx, &PublishPostRequest{
			UserID:         user.ID,
			Caption:        "Original IG Post",
			MediaURLs:      []string{"https://example.com/images/pic.jpg"},
			IdempotencyKey: replayKey,
		})
		if err != nil {
			t.Fatalf("first publish failed: %v", err)
		}
		if resp1.IsIdempotentReplay {
			t.Fatal("first call should not be replay")
		}

		callsBefore := atomic.LoadInt64(&publishMediaCalls)

		// Replay call
		resp2, err := service.Publish(ctx, &PublishPostRequest{
			UserID:         user.ID,
			Caption:        "Original IG Post",
			MediaURLs:      []string{"https://example.com/images/pic.jpg"},
			IdempotencyKey: replayKey,
		})
		if err != nil {
			t.Fatalf("replay call failed: %v", err)
		}
		if !resp2.IsIdempotentReplay {
			t.Fatal("expected second call to be marked as replay")
		}
		if resp2.PlatformPostID != resp1.PlatformPostID {
			t.Errorf("expected identical PlatformPostID, got %s vs %s", resp1.PlatformPostID, resp2.PlatformPostID)
		}
		if atomic.LoadInt64(&publishMediaCalls) != callsBefore {
			t.Errorf("replay should burn 0 upstream API calls, burned %d", atomic.LoadInt64(&publishMediaCalls)-callsBefore)
		}
	})

	// ============================================================================
	// TEST D: CONCURRENT STALE RECLAIM RACE
	// ============================================================================
	t.Run("TestD_ConcurrentStaleReclaimRace", func(t *testing.T) {
		staleRaceKey := fmt.Sprintf("stale_race_key_%d", time.Now().UnixNano())
		staleTime := time.Now().UTC().Add(-90 * time.Second)

		_, err := db.ExecContext(ctx, `INSERT INTO posts (user_id, platform, content, status, idempotency_key, created_at, updated_at)
		                              VALUES ($1, 'instagram', 'Stale race post', 'processing', $2, $3, $3)`,
			user.ID, staleRaceKey, staleTime)
		if err != nil {
			t.Fatalf("failed inserting stale row: %v", err)
		}

		const numWorkers = 10
		var (
			wg       sync.WaitGroup
			winners  int64
			inFlight int64
			replays  int64
		)

		start := make(chan struct{})
		initCalls := atomic.LoadInt64(&publishMediaCalls)

		for i := 0; i < numWorkers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start

				resp, err := service.Publish(ctx, &PublishPostRequest{
					UserID:         user.ID,
					Caption:        "Stale race post",
					MediaURLs:      []string{"https://example.com/images/stale.jpg"},
					IdempotencyKey: staleRaceKey,
				})

				if err == nil {
					if resp.IsIdempotentReplay {
						atomic.AddInt64(&replays, 1)
					} else {
						atomic.AddInt64(&winners, 1)
					}
				} else if errors.Is(err, ErrPostProcessingInProgress) {
					atomic.AddInt64(&inFlight, 1)
				}
			}()
		}

		close(start)
		wg.Wait()

		if winners != 1 {
			t.Errorf("expected exactly 1 stale reclaim winner, got %d", winners)
		}
		if atomic.LoadInt64(&publishMediaCalls) != initCalls+1 {
			t.Errorf("expected exactly 1 upstream publish call during stale race, got %d",
				atomic.LoadInt64(&publishMediaCalls)-initCalls)
		}
	})

	// ============================================================================
	// TEST E: TWO-STEP PUBLISH CRASH RECOVERY & EXPIRED CONTAINER FALLBACK
	// ============================================================================
	t.Run("TestE_TwoStepPublishCrashRecovery_And_ExpiredFallback", func(t *testing.T) {
		// Subtest E1: Resume from stored container ID (0 duplicate container creation)
		e1Key := fmt.Sprintf("e1_key_%d", time.Now().UnixNano())
		staleTime := time.Now().UTC().Add(-90 * time.Second)
		existingContainerID := "creation_pre_existing_789"

		_, err := db.ExecContext(ctx, `INSERT INTO posts (user_id, platform, content, status, idempotency_key, upload_session_uri, created_at, updated_at)
		                              VALUES ($1, 'instagram', 'E1 post', 'processing', $2, $3, $4, $4)`,
			user.ID, e1Key, existingContainerID, staleTime)
		if err != nil {
			t.Fatalf("failed inserting E1 test row: %v", err)
		}

		containersBefore := atomic.LoadInt64(&containerCreateCalls)

		respE1, err := service.Publish(ctx, &PublishPostRequest{
			UserID:         user.ID,
			Caption:        "E1 post recovered",
			MediaURLs:      []string{"https://example.com/images/e1.jpg"},
			IdempotencyKey: e1Key,
		})
		if err != nil {
			t.Fatalf("E1 recovery failed: %v", err)
		}
		if respE1.Status != "published" {
			t.Errorf("expected status 'published', got: %s", respE1.Status)
		}
		// Zero new containers created! Resumed existing container
		if atomic.LoadInt64(&containerCreateCalls) != containersBefore {
			t.Errorf("expected 0 new containers created during E1 resume, got %d",
				atomic.LoadInt64(&containerCreateCalls)-containersBefore)
		}

		// Subtest E2: Expired Container Fallback -> Re-creates fresh container and publishes
		e2Key := fmt.Sprintf("e2_key_%d", time.Now().UnixNano())
		expiredContainerID := "creation_expired_404"

		// Mock server to return EXPIRED for creation_expired_404
		customServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(r.URL.Path, "/me/accounts") {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"data": []map[string]interface{}{
						{
							"id": "page_e2",
							"instagram_business_account": map[string]interface{}{
								"id": "17841499999",
							},
						},
					},
				})
				return
			}
			if strings.Contains(r.URL.Path, expiredContainerID) {
				// Expired container!
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"id":          expiredContainerID,
					"status_code": "EXPIRED",
				})
				return
			}
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/media_publish") {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"id": "published_fresh_ig_123",
				})
				return
			}
			if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/media") {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"id": "creation_fresh_999",
				})
				return
			}
			if strings.Contains(r.URL.Path, "creation_fresh_999") {
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"id":          "creation_fresh_999",
					"status_code": "FINISHED",
				})
				return
			}
			http.NotFound(w, r)
		}))
		defer customServer.Close()

		customClient := NewClient("test_client_id", "test_secret")
		customClient.apiBase = customServer.URL
		customClient.tokenURL = customServer.URL + "/oauth/access_token"

		customService := NewService(db, repo, customClient, stager)

		_, err = db.ExecContext(ctx, `INSERT INTO posts (user_id, platform, content, status, idempotency_key, upload_session_uri, created_at, updated_at)
		                              VALUES ($1, 'instagram', 'E2 post', 'processing', $2, $3, $4, $4)`,
			user.ID, e2Key, expiredContainerID, staleTime)
		if err != nil {
			t.Fatalf("failed inserting E2 test row: %v", err)
		}

		respE2, err := customService.Publish(ctx, &PublishPostRequest{
			UserID:         user.ID,
			Caption:        "E2 post recovered with fresh container",
			MediaURLs:      []string{"https://example.com/images/e2.jpg"},
			IdempotencyKey: e2Key,
		})
		if err != nil {
			t.Fatalf("E2 expired container fallback failed: %v", err)
		}
		if respE2.Status != "published" || respE2.PlatformPostID != "published_fresh_ig_123" {
			t.Errorf("unexpected E2 response: %+v", respE2)
		}
	})
}
