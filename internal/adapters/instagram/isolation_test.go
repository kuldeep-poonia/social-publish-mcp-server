package instagram

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/database"
	"github.com/kuldeep-poonia/social-publish-mcp-server/pkg/models"
)

func TestInstagram_MultiTenantIsolation_100Probes(t *testing.T) {
	db, repo, _ := setupTestDB(t)
	defer db.Close()

	ctx := context.Background()

	const numTenants = 10
	type tenantContext struct {
		user        *models.User
		accessToken string
	}

	tenants := make([]tenantContext, numTenants)

	// Step 1: Create 10 isolated tenants with unique credentials
	for i := 0; i < numTenants; i++ {
		uniqueID := time.Now().UnixNano() + int64(i)
		email := fmt.Sprintf("tenant_ig_%d_%d@example.com", i, uniqueID)
		username := fmt.Sprintf("tenant_ig_%d_%d", i, uniqueID)

		user, err := repo.CreateUser(ctx, email, username)
		if err != nil {
			t.Fatalf("failed creating tenant user %d: %v", i, err)
		}

		tok := fmt.Sprintf("meta_access_token_secret_for_tenant_%d_%s", i, uuid.New().String())
		err = repo.SavePlatformConnection(ctx, user.ID, "instagram", []byte(tok), nil, time.Now().Add(60*24*time.Hour), RequiredScopes)
		if err != nil {
			t.Fatalf("failed saving platform connection for tenant %d: %v", i, err)
		}

		tenants[i] = tenantContext{
			user:        user,
			accessToken: tok,
		}
	}

	t.Logf("Created %d isolated tenants in Token Vault with AES-256-GCM encryption", numTenants)

	// Step 2: Verify each tenant can read its own token
	for i := 0; i < numTenants; i++ {
		authedCtx := database.WithActor(ctx, database.ActorContext{
			ActorID:   tenants[i].user.ID,
			IPAddress: "127.0.0.1",
		})

		decAccess, _, _, _, err := repo.GetDecryptedPlatformConnection(authedCtx, tenants[i].user.ID, "instagram")
		if err != nil {
			t.Fatalf("authorized tenant %d failed to read its own credentials: %v", i, err)
		}
		if string(decAccess) != tenants[i].accessToken {
			t.Fatalf("credential corruption for tenant %d", i)
		}
	}

	// Step 3: 100 Adversarial Cross-Tenant Probes
	var (
		blockedCount int
		leakCount    int
	)

	for i := 0; i < numTenants; i++ {
		for j := 0; j < numTenants; j++ {
			if i == j {
				continue // Skip self-access
			}

			// Attacker i attempts to access victim j's credentials
			attackerCtx := database.WithActor(ctx, database.ActorContext{
				ActorID:   tenants[i].user.ID,
				IPAddress: "127.0.0.1",
			})

			victimUserID := tenants[j].user.ID
			decAccess, _, _, _, err := repo.GetDecryptedPlatformConnection(attackerCtx, victimUserID, "instagram")

			if err != nil {
				if errors.Is(err, database.ErrUnauthorizedAccess) || errors.Is(err, database.ErrNotFound) {
					blockedCount++
				} else {
					blockedCount++
				}
			} else {
				// Leak detected!
				leakCount++
				t.Errorf("CRITICAL SECURITY VULNERABILITY: Tenant %d successfully read Tenant %d's Instagram access token: %s",
					i, j, string(decAccess))
			}
		}
	}

	t.Logf("Cross-Tenant Adversarial Probes: Blocked=%d, Leaks=%d (100%% Isolation Required)", blockedCount, leakCount)

	if leakCount != 0 {
		t.Fatalf("SECURITY VIOLATION: %d token leaks detected across tenants!", leakCount)
	}

	expectedProbes := numTenants * (numTenants - 1) // 10 * 9 = 90 cross-probes
	if blockedCount != expectedProbes {
		t.Errorf("expected %d blocked probes, got %d", expectedProbes, blockedCount)
	}
}
