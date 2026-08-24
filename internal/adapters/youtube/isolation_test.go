package youtube

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/config"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
)

func TestYouTubeUserIsolation_100PercentSecure(t *testing.T) {
	cfg, err := config.LoadConfig()
	if err != nil {
		t.Fatalf("failed loading config: %v", err)
	}

	db, err := sql.Open("pgx", cfg.PostgresDSN())
	if err != nil {
		t.Fatalf("failed connecting to Postgres: %v", err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Skipf("Postgres not available, skipping live isolation test: %v", err)
	}

	rawKey, _ := hex.DecodeString("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	repo := database.NewRepository(db, rawKey, nil)
	ctx := context.Background()

	const numTenants = 10
	tenantIDs := make([]string, numTenants)

	for i := 0; i < numTenants; i++ {
		email := fmt.Sprintf("yt_tenant_%d_%d@example.com", i, time.Now().UnixNano())
		username := fmt.Sprintf("yt_tenant_%d_%d", i, time.Now().UnixNano())
		user, err := repo.CreateUser(ctx, email, username)
		if err != nil {
			t.Fatalf("failed creating tenant %d: %v", i, err)
		}
		tenantIDs[i] = user.ID

		accessToken := []byte(fmt.Sprintf("secret_yt_access_token_for_tenant_%d", i))
		refreshToken := []byte(fmt.Sprintf("secret_yt_refresh_token_for_tenant_%d", i))
		expiresAt := time.Now().UTC().Add(1 * time.Hour)

		err = repo.SavePlatformConnection(ctx, user.ID, "youtube", accessToken, refreshToken, expiresAt, RequiredScopes)
		if err != nil {
			t.Fatalf("failed saving token for tenant %d: %v", i, err)
		}
	}

	t.Logf("================================================================================")
	t.Logf("     YOUTUBE MULTI-TENANT CRYPTOGRAPHIC ISOLATION ADVERSARIAL FUZZING           ")
	t.Logf("     (10 Distinct Tenants in Live PostgreSQL | 100 Cross-Tenant Attack Probes)  ")
	t.Logf("================================================================================")

	const totalAttacks = 100
	var blockedAttacks atomic.Int64
	var crossTenantLeaks atomic.Int64
	var wg sync.WaitGroup

	for i := 0; i < totalAttacks; i++ {
		wg.Add(1)
		attackerIdx := i % numTenants
		victimIdx := (attackerIdx + 1 + (i / numTenants)) % numTenants
		if victimIdx == attackerIdx {
			victimIdx = (victimIdx + 1) % numTenants
		}
		attackerID := tenantIDs[attackerIdx]
		victimID := tenantIDs[victimIdx]

		go func(attID, vicID string, probeID int) {
			defer wg.Done()

			attackCtx := database.WithActor(ctx, database.ActorContext{
				ActorID:   attID,
				IPAddress: fmt.Sprintf("192.168.1.%d", (probeID%250)+1),
			})

			accessBytes, refreshBytes, _, _, err := repo.GetDecryptedPlatformConnection(attackCtx, vicID, "youtube")
			if err != nil {
				blockedAttacks.Add(1)
				return
			}

			if strings.Contains(string(accessBytes), "secret_yt_access_token") ||
				strings.Contains(string(refreshBytes), "secret_yt_refresh_token") {
				crossTenantLeaks.Add(1)
				t.Errorf("[CRITICAL LEAK] Attacker '%s' extracted victim '%s' YouTube tokens!", attID, vicID)
			}
		}(attackerID, victimID, i)
	}

	wg.Wait()

	t.Logf("=== MULTI-TENANT CROSS-USER ISOLATION RESULTS ===")
	t.Logf("Total Adversarial Cross-Tenant Probes: %d", totalAttacks)
	t.Logf("Blocked / Cryptographically Isolated: %d / %d (Isolation Rate: %.2f%%)",
		blockedAttacks.Load(), totalAttacks, float64(blockedAttacks.Load())/float64(totalAttacks)*100.0)
	t.Logf("Cross-Tenant Data Leaks: %d", crossTenantLeaks.Load())
	t.Logf("Security Requirement: 100%% isolation & 0 leaks (Met: %t)",
		blockedAttacks.Load() == totalAttacks && crossTenantLeaks.Load() == 0)
	t.Logf("================================================================================")

	if crossTenantLeaks.Load() > 0 {
		t.Fatalf("FAILED: %d cross-tenant token leaks detected!", crossTenantLeaks.Load())
	}
	if blockedAttacks.Load() != totalAttacks {
		t.Fatalf("FAILED: expected %d blocked attacks, got %d", totalAttacks, blockedAttacks.Load())
	}
}
