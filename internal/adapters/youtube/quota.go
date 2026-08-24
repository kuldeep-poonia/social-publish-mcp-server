// Package youtube provides per-user daily quota tracking and budget management
// to prevent noisy neighbor quota exhaustion.
package youtube

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrQuotaExceeded is returned when a user does not have sufficient daily quota units.
	ErrQuotaExceeded = errors.New("youtube quota: daily quota limit exceeded (10,000 units/day); try again after daily reset (08:00 UTC)")
)

// UserQuotaRecord tracks quota usage for a single user for the current daily window.
type UserQuotaRecord struct {
	UserID        string
	DailyBudget   int64
	UsedUnits     int64
	CurrentWindow time.Time
}

// QuotaManager orchestrates multi-tenant quota accounting with daily resets.
type QuotaManager struct {
	mu          sync.Mutex
	dailyBudget int64
	usage       map[string]*UserQuotaRecord
}

// NewQuotaManager initializes a QuotaManager with the specified daily budget per user.
func NewQuotaManager(dailyBudget int64) *QuotaManager {
	if dailyBudget <= 0 {
		dailyBudget = QuotaDailyBudget // 10,000 units
	}
	return &QuotaManager{
		dailyBudget: dailyBudget,
		usage:       make(map[string]*UserQuotaRecord),
	}
}

// currentWindowStart returns the start of the current Google YouTube quota day (08:00:00 UTC / 00:00:00 PST).
func currentWindowStart(t time.Time) time.Time {
	utc := t.UTC()
	todayReset := time.Date(utc.Year(), utc.Month(), utc.Day(), 8, 0, 0, 0, time.UTC)
	if utc.Before(todayReset) {
		// Before 08:00 UTC, window started yesterday at 08:00 UTC
		return todayReset.Add(-24 * time.Hour)
	}
	return todayReset
}

// ReserveQuota atomically checks and reserves quota units for a tenant.
// Returns an error if the tenant exceeds their daily budget.
func (qm *QuotaManager) ReserveQuota(_ context.Context, userID string, units int64) error {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	now := time.Now().UTC()
	window := currentWindowStart(now)

	rec, exists := qm.usage[userID]
	if !exists || rec.CurrentWindow.Before(window) {
		// New window or fresh user
		rec = &UserQuotaRecord{
			UserID:        userID,
			DailyBudget:   qm.dailyBudget,
			UsedUnits:     0,
			CurrentWindow: window,
		}
		qm.usage[userID] = rec
	}

	if rec.UsedUnits+units > rec.DailyBudget {
		return fmt.Errorf("%w: user '%s' has %d units remaining, attempted %d",
			ErrQuotaExceeded, userID, rec.DailyBudget-rec.UsedUnits, units)
	}

	rec.UsedUnits += units
	return nil
}

// ReleaseQuota refunds reserved quota units if an operation fails before making upstream calls.
func (qm *QuotaManager) ReleaseQuota(_ context.Context, userID string, units int64) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	now := time.Now().UTC()
	window := currentWindowStart(now)

	rec, exists := qm.usage[userID]
	if exists && !rec.CurrentWindow.Before(window) {
		rec.UsedUnits -= units
		if rec.UsedUnits < 0 {
			rec.UsedUnits = 0
		}
	}
}

// GetQuotaStatus returns current quota utilization and time until next reset.
func (qm *QuotaManager) GetQuotaStatus(_ context.Context, userID string) (used, remaining int64, nextReset time.Time) {
	qm.mu.Lock()
	defer qm.mu.Unlock()

	now := time.Now().UTC()
	window := currentWindowStart(now)
	nextReset = window.Add(24 * time.Hour)

	rec, exists := qm.usage[userID]
	if !exists || rec.CurrentWindow.Before(window) {
		return 0, qm.dailyBudget, nextReset
	}

	return rec.UsedUnits, rec.DailyBudget - rec.UsedUnits, nextReset
}
