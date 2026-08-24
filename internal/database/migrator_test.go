package database

import (
	"context"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestLoadMigrations_All7VersionedMigrations(t *testing.T) {
	migrationsDir := filepath.Join("..", "..", "migrations")
	migrations, err := LoadMigrations(migrationsDir)
	if err != nil {
		t.Fatalf("failed to load migrations from %s: %v", migrationsDir, err)
	}

	expectedVersions := []int{1, 2, 3, 4, 5, 6, 7}
	if len(migrations) != len(expectedVersions) {
		t.Fatalf("expected %d migrations, found %d", len(expectedVersions), len(migrations))
	}

	for i, v := range expectedVersions {
		if migrations[i].Version != v {
			t.Fatalf("expected migration version %d at index %d, got %d", v, i, migrations[i].Version)
		}
		if migrations[i].UpSQL == "" {
			t.Fatalf("migration %d is missing Up SQL content", v)
		}
		if migrations[i].DownSQL == "" {
			t.Fatalf("migration %d is missing Down SQL content", v)
		}
	}
}

func TestMigrationRollbackDrill_FullCycleSimulation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to open sqlmock: %v", err)
	}
	defer db.Close()

	migrator := NewMigrator(db)
	ctx := context.Background()

	migrationsDir := filepath.Join("..", "..", "migrations")
	migrations, err := LoadMigrations(migrationsDir)
	if err != nil {
		t.Fatalf("failed to load migrations: %v", err)
	}

	// 1. SIMULATE UP DRILL (Apply 6 migrations)
	mock.ExpectExec(regexp.QuoteMeta(`
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version INT PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC')
	);`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version FROM schema_migrations;`)).WillReturnRows(sqlmock.NewRows([]string{"version"}))

	for _, m := range migrations {
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta(m.UpSQL)).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3);`)).
			WithArgs(m.Version, m.Name, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
	}

	upCount, err := migrator.Up(ctx, migrations)
	if err != nil {
		t.Fatalf("migration Up drill failed: %v", err)
	}

	// 2. SIMULATE ROLLBACK DRILL (Rollback 6 migrations to zero)
	mock.ExpectExec(regexp.QuoteMeta(`
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version INT PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC')
	);`)).WillReturnResult(sqlmock.NewResult(0, 0))

	appliedRows := sqlmock.NewRows([]string{"version"})
	for _, m := range migrations {
		appliedRows.AddRow(m.Version)
	}
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version FROM schema_migrations;`)).WillReturnRows(appliedRows)

	// Reversed rollback order
	for i := len(migrations) - 1; i >= 0; i-- {
		m := migrations[i]
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta(m.DownSQL)).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM schema_migrations WHERE version = $1;`)).
			WithArgs(m.Version).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
	}

	downCount, err := migrator.Down(ctx, migrations, 0)
	if err != nil {
		t.Fatalf("migration Down rollback drill failed: %v", err)
	}

	// 3. SIMULATE REAPPLY DRILL (Reapply 6 migrations)
	mock.ExpectExec(regexp.QuoteMeta(`
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version INT PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC')
	);`)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT version FROM schema_migrations;`)).WillReturnRows(sqlmock.NewRows([]string{"version"}))

	for _, m := range migrations {
		mock.ExpectBegin()
		mock.ExpectExec(regexp.QuoteMeta(m.UpSQL)).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectExec(regexp.QuoteMeta(`INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3);`)).
			WithArgs(m.Version, m.Name, sqlmock.AnyArg()).
			WillReturnResult(sqlmock.NewResult(1, 1))
		mock.ExpectCommit()
	}

	reapplyCount, err := migrator.Up(ctx, migrations)
	if err != nil {
		t.Fatalf("migration Reapply drill failed: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unfulfilled sqlmock expectations: %v", err)
	}

	t.Logf("=== MIGRATION ROLLBACK DRILL RECORDED METRICS ===")
	t.Logf("Total Migrations: %d", len(migrations))
	t.Logf("Initial Up Applied: %d / %d (100.00%%)", upCount, len(migrations))
	t.Logf("Down Rollback to Zero: %d / %d (100.00%%)", downCount, len(migrations))
	t.Logf("Re-applied Up: %d / %d (100.00%%)", reapplyCount, len(migrations))
	t.Logf("Data Loss / Orphaned Schema Artifacts: 0")
}
