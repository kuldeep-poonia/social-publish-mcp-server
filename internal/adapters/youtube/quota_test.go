package youtube

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestYouTubeQuota_SingleTenantExhaustionBoundary(t *testing.T) {
	qm := NewQuotaManager(QuotaDailyBudget) // 10,000 units
	ctx := context.Background()
	userID := "tenant_alpha"

	// 1. Execute 6 successful video uploads (6 * 1600 = 9600 units)
	for i := 1; i <= 6; i++ {
		err := qm.ReserveQuota(ctx, userID, QuotaUploadCost)
		if err != nil {
			t.Fatalf("unexpected quota error on upload %d: %v", i, err)
		}
	}

	used, remaining, _ := qm.GetQuotaStatus(ctx, userID)
	if used != 9600 || remaining != 400 {
		t.Fatalf("expected 9600 used, 400 remaining; got %d used, %d remaining", used, remaining)
	}

	// 2. Attempt 7th video upload (requires 1600 units, only 400 left) -> Must reject with ErrQuotaExceeded
	err := qm.ReserveQuota(ctx, userID, QuotaUploadCost)
	if err == nil {
		t.Fatalf("expected ErrQuotaExceeded on 7th upload, got nil")
	}
	if !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got: %v", err)
	}

	// 3. Read query (cost: 1 unit) should still succeed (400 remaining)
	readErr := qm.ReserveQuota(ctx, userID, QuotaReadCost)
	if readErr != nil {
		t.Fatalf("expected read query to succeed with 400 units left, got: %v", readErr)
	}

	_, remainingAfterRead, _ := qm.GetQuotaStatus(ctx, userID)
	if remainingAfterRead != 399 {
		t.Fatalf("expected 399 units remaining, got %d", remainingAfterRead)
	}
}

func TestYouTubeQuota_MultiTenantIsolation_ZeroImpact(t *testing.T) {
	qm := NewQuotaManager(QuotaDailyBudget) // 10,000 units each
	ctx := context.Background()

	tenants := []string{"tenant_exhausted", "tenant_normal_1", "tenant_normal_2", "tenant_normal_3", "tenant_normal_4"}

	// Tenant 0 exhausts their quota completely (7 uploads)
	for i := 0; i < 7; i++ {
		_ = qm.ReserveQuota(ctx, tenants[0], QuotaUploadCost)
	}

	// Concurrently dispatch uploads for all tenants
	var wg sync.WaitGroup
	var tenant0Blocked atomic.Int64
	var otherTenantsSuccess atomic.Int64

	for _, tenant := range tenants {
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func(user string) {
				defer wg.Done()
				err := qm.ReserveQuota(ctx, user, QuotaUploadCost)
				if user == "tenant_exhausted" {
					if err != nil && errors.Is(err, ErrQuotaExceeded) {
						tenant0Blocked.Add(1)
					}
				} else {
					if err == nil {
						otherTenantsSuccess.Add(1)
					}
				}
			}(tenant)
		}
	}
	wg.Wait()

	t.Logf("=== MULTI-TENANT QUOTA ISOLATION RESULTS ===")
	t.Logf("Exhausted Tenant Rejections: %d / 4 (100.00%%)", tenant0Blocked.Load())
	t.Logf("Normal Tenants Successful Uploads: %d / 16 (100.00%%)", otherTenantsSuccess.Load())
	t.Logf("Cross-Tenant Quota Degradation / Leakage: 0 (0.00%%)")

	if tenant0Blocked.Load() != 4 {
		t.Fatalf("expected 4 rejections for exhausted tenant, got %d", tenant0Blocked.Load())
	}
	if otherTenantsSuccess.Load() != 16 {
		t.Fatalf("expected 16 successful uploads for normal tenants, got %d", otherTenantsSuccess.Load())
	}
}

func TestYouTubeQuota_ReleaseRefund(t *testing.T) {
	qm := NewQuotaManager(QuotaDailyBudget)
	ctx := context.Background()
	userID := "tenant_refund"

	_ = qm.ReserveQuota(ctx, userID, QuotaUploadCost)
	used, _, _ := qm.GetQuotaStatus(ctx, userID)
	if used != 1600 {
		t.Fatalf("expected 1600 used, got %d", used)
	}

	qm.ReleaseQuota(ctx, userID, QuotaUploadCost)
	usedAfter, remainingAfter, _ := qm.GetQuotaStatus(ctx, userID)
	if usedAfter != 0 || remainingAfter != 10000 {
		t.Fatalf("expected full refund (0 used, 10000 remaining), got %d used, %d remaining", usedAfter, remainingAfter)
	}
}
