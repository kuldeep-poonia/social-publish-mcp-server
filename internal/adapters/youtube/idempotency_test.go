package youtube

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
)

func setupYouTubeIdempotencyTestEnv(t *testing.T) (*sql.DB, *database.Repository, string, *httptest.Server, *atomic.Int64, *atomic.Int64) {
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("failed loading config: %v", err)
	}

	db, err := sql.Open("pgx", cfg.PostgresDSN())
	if err != nil {
		t.Fatalf("failed connecting to Postgres: %v", err)
	}

	if err := db.Ping(); err != nil {
		t.Skipf("Postgres not available, skipping test: %v", err)
	}

	rawKey, _ := hex.DecodeString("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	repo := database.NewRepository(db, rawKey, nil)
	ctx := context.Background()

	email := fmt.Sprintf("yt_idemp_user_%d@example.com", time.Now().UnixNano())
	username := fmt.Sprintf("yt_idemp_user_%d", time.Now().UnixNano())
	user, err := repo.CreateUser(ctx, email, username)
	if err != nil {
		t.Fatalf("failed creating user: %v", err)
	}

	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	err = repo.SavePlatformConnection(ctx, user.ID, "youtube", []byte("valid_yt_access_token"), []byte("valid_yt_refresh_token"), expiresAt, RequiredScopes)
	if err != nil {
		t.Fatalf("failed saving platform connection: %v", err)
	}

	var sessionInitCalls atomic.Int64
	var chunkUploadCalls atomic.Int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/upload") {
			sessionInitCalls.Add(1)
			w.Header().Set("Location", fmt.Sprintf("http://%s/session/yt_test_session_id", r.Host))
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodPut {
			chunkUploadCalls.Add(1)
			cr := r.Header.Get("Content-Range")
			if strings.HasPrefix(cr, "bytes */") {
				// Status query
				w.Header().Set("Range", "bytes=0-0")
				w.WriteHeader(308)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id": "yt_video_1787570895940859300", "snippet": {"title": "Test Video"}}`))
			return
		}
		http.NotFound(w, r)
	}))

	return db, repo, user.ID, server, &sessionInitCalls, &chunkUploadCalls
}

func sampleMP4VideoData() []byte {
	return []byte("\x00\x00\x00\x20ftypisom\x00\x00\x02\x00isomiso2mp41\x00\x00\x00\x08free00000000000000000000")
}

func TestYouTubeIdempotency_ComprehensiveSuite(t *testing.T) {
	db, repo, userID, server, sessionInitCalls, chunkUploadCalls := setupYouTubeIdempotencyTestEnv(t)
	defer db.Close()
	defer server.Close()

	client := NewClient("mock_client_id", "mock_client_secret")
	client.uploadBase = server.URL + "/upload"
	quotaMgr := NewQuotaManager(QuotaDailyBudget)

	service := NewPublishService(db, repo, client, quotaMgr)
	ctx := context.Background()

	t.Logf("================================================================================")
	t.Logf("     YOUTUBE APPLICATION-LEVEL IDEMPOTENCY & CRASH-RECOVERY TEST SUITE          ")
	t.Logf("================================================================================")

	// -------------------------------------------------------------------------
	// TEST A: Stale Crashed Worker Recovery (60s Threshold)
	// -------------------------------------------------------------------------
	t.Run("TestA_StaleCrashRecovery", func(t *testing.T) {
		sessionInitCalls.Store(0)
		chunkUploadCalls.Store(0)

		staleKey := fmt.Sprintf("yt_stale_key_%d", time.Now().UnixNano())
		staleCreatedAt := time.Now().UTC().Add(-150 * time.Second)

		query := `
			INSERT INTO posts (user_id, platform, content, media_urls, status, idempotency_key, created_at, updated_at)
			VALUES ($1, 'youtube', 'Stale Video', '{}', 'processing', $2, $3, $3)
		`
		if _, err := db.ExecContext(ctx, query, userID, staleKey, staleCreatedAt); err != nil {
			t.Fatalf("failed inserting synthetic stale row: %v", err)
		}

		videoData := sampleMP4VideoData()
		req := &PublishVideoRequest{
			UserID:         userID,
			Title:          "Stale Video",
			Description:    "Recovered Description",
			PrivacyStatus:  "public",
			VideoReader:    bytes.NewReader(videoData),
			TotalBytes:     int64(len(videoData)),
			IdempotencyKey: staleKey,
		}

		resp, err := service.PublishVideo(ctx, req)
		if err != nil {
			t.Fatalf("stale recovery publish failed: %v", err)
		}

		if resp.IsIdempotentReplay || resp.PlatformPostID != "yt_video_1787570895940859300" {
			t.Fatalf("expected real execution on stale recovery, got replay: %t, id: %s", resp.IsIdempotentReplay, resp.PlatformPostID)
		}

		if sessionInitCalls.Load() != 1 {
			t.Fatalf("expected exactly 1 session init call, got %d", sessionInitCalls.Load())
		}
		t.Logf("[Test A: Stale Worker Recovery] Stale 'processing' row reclaimed cleanly and published. (Success: 100%%)")
	})

	// -------------------------------------------------------------------------
	// TEST B: 50-Goroutine Fresh Insert Race (Exact Mathematical Accounting)
	// -------------------------------------------------------------------------
	t.Run("TestB_50GoroutineConcurrentRace", func(t *testing.T) {
		sessionInitCalls.Store(0)
		chunkUploadCalls.Store(0)

		freshKey := fmt.Sprintf("yt_race_fresh_key_%d", time.Now().UnixNano())
		const numGoroutines = 50

		var winnerCount atomic.Int64
		var conflict409Count atomic.Int64
		var cachedReplayCount atomic.Int64
		var unexpectedErrCount atomic.Int64

		var startBarrier sync.WaitGroup
		startBarrier.Add(1)
		var endBarrier sync.WaitGroup

		for i := 0; i < numGoroutines; i++ {
			endBarrier.Add(1)
			go func() {
				defer endBarrier.Done()
				startBarrier.Wait()

				videoData := sampleMP4VideoData()
				req := &PublishVideoRequest{
					UserID:         userID,
					Title:          "Concurrent Video",
					Description:    "Race Test",
					PrivacyStatus:  "public",
					VideoReader:    bytes.NewReader(videoData),
					TotalBytes:     int64(len(videoData)),
					IdempotencyKey: freshKey,
				}

				resp, err := service.PublishVideo(ctx, req)
				if err != nil {
					if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "processing") {
						conflict409Count.Add(1)
					} else {
						unexpectedErrCount.Add(1)
					}
					return
				}

				if resp.IsIdempotentReplay {
					cachedReplayCount.Add(1)
				} else {
					winnerCount.Add(1)
				}
			}()
		}

		startBarrier.Done()
		endBarrier.Wait()

		totalAccounted := winnerCount.Load() + conflict409Count.Load() + cachedReplayCount.Load() + unexpectedErrCount.Load()

		t.Logf("=== TEST B: 50 GOROUTINE OUTCOME BREAKDOWN ===")
		t.Logf("Total Requests Dispatched:        %d", numGoroutines)
		t.Logf("1. Initial Publisher (Won Lock):  %d (Triggered Upstream Call)", winnerCount.Load())
		t.Logf("2. In-Flight 409 Conflicts:       %d (Caught processing lock / 23505 race)", conflict409Count.Load())
		t.Logf("3. Idempotent Replays (Cached):   %d (Arrived after winner completed)", cachedReplayCount.Load())
		t.Logf("4. Unexpected Errors:             %d", unexpectedErrCount.Load())
		t.Logf("Total Requests Accounted For:     %d / %d (%.2f%%)", totalAccounted, numGoroutines, float64(totalAccounted)/float64(numGoroutines)*100.0)
		t.Logf("Upstream YouTube API Calls Made:  %d (Strict Idempotency Target: 1)", sessionInitCalls.Load())

		if totalAccounted != numGoroutines {
			t.Fatalf("MATHEMATICAL INCONSISTENCY: Expected %d requests accounted for, got %d", numGoroutines, totalAccounted)
		}
		if winnerCount.Load() != 1 {
			t.Fatalf("RACE CONDITION: Expected exactly 1 initial publisher winner, got %d", winnerCount.Load())
		}
		if sessionInitCalls.Load() != 1 {
			t.Fatalf("DUPLICATE UPLOAD HAZARD: Expected exactly 1 session init call, got %d", sessionInitCalls.Load())
		}
		if unexpectedErrCount.Load() != 0 {
			t.Fatalf("CRASH HAZARD: %d unexpected errors occurred", unexpectedErrCount.Load())
		}
	})

	// -------------------------------------------------------------------------
	// TEST C: Replay of Published Video (10 Replays = 0 YouTube Calls)
	// -------------------------------------------------------------------------
	t.Run("TestC_ReplayOfPublishedPost", func(t *testing.T) {
		sessionInitCalls.Store(0)
		chunkUploadCalls.Store(0)

		replayKey := fmt.Sprintf("yt_replay_key_%d", time.Now().UnixNano())
		videoData := sampleMP4VideoData()
		req := &PublishVideoRequest{
			UserID:         userID,
			Title:          "Replay Video",
			Description:    "Replay Description",
			PrivacyStatus:  "public",
			VideoReader:    bytes.NewReader(videoData),
			TotalBytes:     int64(len(videoData)),
			IdempotencyKey: replayKey,
		}

		// Initial publish
		firstResp, err := service.PublishVideo(ctx, req)
		if err != nil {
			t.Fatalf("first publish failed: %v", err)
		}
		if firstResp.IsIdempotentReplay {
			t.Fatalf("first publish should be fresh, got replay")
		}

		sessionCallsAfterFirst := sessionInitCalls.Load()

		// Execute 10 sequential replays
		for i := 1; i <= 10; i++ {
			req.VideoReader = bytes.NewReader(videoData)
			resp, err := service.PublishVideo(ctx, req)
			if err != nil {
				t.Fatalf("replay %d failed: %v", i, err)
			}
			if !resp.IsIdempotentReplay {
				t.Fatalf("replay %d expected IsIdempotentReplay=true", i)
			}
			if resp.PlatformPostID != firstResp.PlatformPostID {
				t.Fatalf("replay %d returned mismatched ID: %s vs %s", i, resp.PlatformPostID, firstResp.PlatformPostID)
			}
		}

		if sessionInitCalls.Load() != sessionCallsAfterFirst {
			t.Fatalf("idempotent replay triggered upstream session init: before=%d, after=%d", sessionCallsAfterFirst, sessionInitCalls.Load())
		}
		t.Logf("[Test C: Published Post Replay] 10 replayed requests returned cached video ID (%s) with 0 additional API calls.", firstResp.PlatformPostID)
	})

	// -------------------------------------------------------------------------
	// TEST D: Concurrent Stale Reclaim Race (20 Workers -> 1 Winner, 19 Losers)
	// -------------------------------------------------------------------------
	t.Run("TestD_ConcurrentStaleReclaimRace", func(t *testing.T) {
		sessionInitCalls.Store(0)
		chunkUploadCalls.Store(0)

		staleRaceKey := fmt.Sprintf("yt_stale_race_key_%d", time.Now().UnixNano())
		staleTime := time.Now().UTC().Add(-150 * time.Second)

		query := `
			INSERT INTO posts (user_id, platform, content, media_urls, status, idempotency_key, created_at, updated_at)
			VALUES ($1, 'youtube', 'Stale Race Video', '{}', 'processing', $2, $3, $3)
		`
		if _, err := db.ExecContext(ctx, query, userID, staleRaceKey, staleTime); err != nil {
			t.Fatalf("failed inserting stale race row: %v", err)
		}

		const numWorkers = 20
		var reclaimWinnerCount atomic.Int64
		var inFlightConflictCount atomic.Int64
		var replayAfterWinnerCount atomic.Int64
		var unexpectedErrors atomic.Int64

		var startBarrier sync.WaitGroup
		startBarrier.Add(1)
		var endBarrier sync.WaitGroup

		for i := 0; i < numWorkers; i++ {
			endBarrier.Add(1)
			go func() {
				defer endBarrier.Done()
				startBarrier.Wait()

				videoData := sampleMP4VideoData()
				req := &PublishVideoRequest{
					UserID:         userID,
					Title:          "Stale Race Video",
					Description:    "Race",
					PrivacyStatus:  "public",
					VideoReader:    bytes.NewReader(videoData),
					TotalBytes:     int64(len(videoData)),
					IdempotencyKey: staleRaceKey,
				}

				resp, err := service.PublishVideo(ctx, req)
				if err != nil {
					if strings.Contains(err.Error(), "409") || strings.Contains(err.Error(), "processing") {
						inFlightConflictCount.Add(1)
					} else {
						unexpectedErrors.Add(1)
					}
					return
				}

				if resp.IsIdempotentReplay {
					replayAfterWinnerCount.Add(1)
				} else {
					reclaimWinnerCount.Add(1)
				}
			}()
		}

		startBarrier.Done()
		endBarrier.Wait()

		totalAccounted := reclaimWinnerCount.Load() + inFlightConflictCount.Load() + replayAfterWinnerCount.Load() + unexpectedErrors.Load()

		t.Logf("=== TEST D: 20 STALE RECLAIM GOROUTINE BREAKDOWN ===")
		t.Logf("Total Stale Workers Dispatched:    %d", numWorkers)
		t.Logf("1. Reclaim Winner (RowsAffected=1): %d", reclaimWinnerCount.Load())
		t.Logf("2. In-Flight 409 Conflicts:        %d (RowsAffected=0 lost conditional UPDATE)", inFlightConflictCount.Load())
		t.Logf("3. Idempotent Replays:             %d (Arrived after winner published)", replayAfterWinnerCount.Load())
		t.Logf("4. Unexpected Errors:              %d", unexpectedErrors.Load())
		t.Logf("Total Requests Accounted For:      %d / %d (%.2f%%)", totalAccounted, numWorkers, float64(totalAccounted)/float64(numWorkers)*100.0)
		t.Logf("Upstream YouTube API Calls Made:   %d (Strict Target: 1)", sessionInitCalls.Load())

		if totalAccounted != numWorkers {
			t.Fatalf("MATHEMATICAL INCONSISTENCY: Expected %d accounted for, got %d", numWorkers, totalAccounted)
		}
		if reclaimWinnerCount.Load() != 1 {
			t.Fatalf("CRITICAL RACE FAILURE: Expected exactly 1 winner, got %d", reclaimWinnerCount.Load())
		}
		if sessionInitCalls.Load() != 1 {
			t.Fatalf("DUPLICATE UPLOAD HAZARD: Expected exactly 1 session init call, got %d", sessionInitCalls.Load())
		}
		if unexpectedErrors.Load() != 0 {
			t.Fatalf("UNEXPECTED ERROR: %d errors occurred", unexpectedErrors.Load())
		}
	})

	// -------------------------------------------------------------------------
	// TEST E: Zero-Quota-Waste Resumable Upload Crash Recovery
	// -------------------------------------------------------------------------
	t.Run("TestE_ZeroQuotaWasteResumableCrashRecovery", func(t *testing.T) {
		sessionInitCalls.Store(0)
		chunkUploadCalls.Store(0)

		crashKey := fmt.Sprintf("yt_resumable_crash_%d", time.Now().UnixNano())
		staleTime := time.Now().UTC().Add(-150 * time.Second)

		// Insert row representing a crashed upload where session URI was already created
		existingSessionURI := fmt.Sprintf("http://%s/session/yt_crashed_session_uri", server.Listener.Addr().String())
		query := `
			INSERT INTO posts (user_id, platform, content, media_urls, status, idempotency_key, upload_session_uri, bytes_uploaded, created_at, updated_at)
			VALUES ($1, 'youtube', 'Crashed Video', '{}', 'processing', $2, $3, 0, $4, $4)
		`
		if _, err := db.ExecContext(ctx, query, userID, crashKey, existingSessionURI, staleTime); err != nil {
			t.Fatalf("failed inserting synthetic crashed session row: %v", err)
		}

		videoData := sampleMP4VideoData()
		req := &PublishVideoRequest{
			UserID:         userID,
			Title:          "Crashed Video",
			Description:    "Resumed Video Description",
			PrivacyStatus:  "public",
			VideoReader:    bytes.NewReader(videoData),
			TotalBytes:     int64(len(videoData)),
			IdempotencyKey: crashKey,
		}

		resp, err := service.PublishVideo(ctx, req)
		if err != nil {
			t.Fatalf("crash recovery publish failed: %v", err)
		}

		if resp.PlatformPostID != "yt_video_1787570895940859300" {
			t.Fatalf("unexpected platform post ID: %s", resp.PlatformPostID)
		}

		// CRITICAL QUOTA CHECK: Zero new session inits must be called!
		if sessionInitCalls.Load() != 0 {
			t.Fatalf("QUOTA LEAK FAILURE: Expected 0 new session inits (reusing stored URI), got %d (wasted 1600 units!)", sessionInitCalls.Load())
		}

		t.Logf("================================================================================")
		t.Logf("     TEST E: ZERO-QUOTA-WASTE RESUMABLE CRASH RECOVERY (100%% PASS)             ")
		t.Logf("================================================================================")
		t.Logf("Resumed From Stored Session URI:  %s", existingSessionURI)
		t.Logf("New Session Inits Triggered:      %d (0 Quota Units Wasted)", sessionInitCalls.Load())
		t.Logf("Chunk Uploads Executed:           %d", chunkUploadCalls.Load())
		t.Logf("Published Video ID:               %s", resp.PlatformPostID)
		t.Logf("================================================================================")
	})
}
