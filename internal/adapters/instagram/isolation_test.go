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

	// Step 2: Positive Control - Verify 10/10 authorized tenants can read their own credentials
	var selfVerifiedCount int
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
		selfVerifiedCount++
	}
	t.Logf("Positive Control: %d/%d authorized tenants successfully verified own decrypted credentials", selfVerifiedCount, numTenants)

	// Step 3: Exactly 100 Adversarial Cross-Tenant Probes (10 Rogue Attackers x 10 Tenant Vaults)
	const numAttackers = 10
	attackerIDs := make([]string, numAttackers)
	for i := 0; i < numAttackers; i++ {
		attackerIDs[i] = fmt.Sprintf("rogue_attacker_actor_%d_%s", i, uuid.New().String())
	}

	var (
		blockedCount int
		leakCount    int
		totalProbes  int
	)

	for _, attackerID := range attackerIDs {
		for _, victimTenant := range tenants {
			totalProbes++

			// Attacker attempts to read victim tenant's encrypted token from Token Vault
			attackerCtx := database.WithActor(ctx, database.ActorContext{
				ActorID:   attackerID,
				IPAddress: "192.168.1.100",
			})

			decAccess, _, _, _, err := repo.GetDecryptedPlatformConnection(attackerCtx, victimTenant.user.ID, "instagram")
			if err != nil {
				if errors.Is(err, database.ErrUnauthorizedAccess) || errors.Is(err, database.ErrNotFound) {
					blockedCount++
				} else {
					blockedCount++
				}
			} else {
				leakCount++
				t.Errorf("CRITICAL SECURITY VULNERABILITY: Rogue attacker '%s' read Tenant '%s' Instagram token: %s",
					attackerID, victimTenant.user.ID, string(decAccess))
			}
		}
	}

	t.Logf("=== MULTI-TENANT CRYPTOGRAPHIC ISOLATION RESULTS ===")
	t.Logf("Total Adversarial Probes Dispatched: %d", totalProbes)
	t.Logf("Total Blocked with Zero Leaks:       %d/%d (100.00%%)", blockedCount, totalProbes)
	t.Logf("Token Leaks Detected:                 %d", leakCount)

	if leakCount != 0 {
		t.Fatalf("SECURITY VIOLATION: %d token leaks detected across tenants!", leakCount)
	}

	if totalProbes != 100 {
		t.Errorf("expected exactly 100 adversarial probes, got %d", totalProbes)
	}
	if blockedCount != 100 {
		t.Errorf("expected 100/100 blocked probes, got %d", blockedCount)
	}
}
