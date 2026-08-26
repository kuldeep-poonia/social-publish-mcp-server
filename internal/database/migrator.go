// Package database provides PostgreSQL connection pooling, migration execution, and audited repositories.
package database

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/kuldeep-poonia/social-publish-mcp-server/migrations"
)

// Migration represents a single versioned migration step.
type Migration struct {
	Version int
	Name    string
	UpSQL   string
	DownSQL string
}

// Migrator handles versioned database schema migrations.
type Migrator struct {
	db *sql.DB
}

// NewMigrator initializes a Migrator instance.
func NewMigrator(db *sql.DB) *Migrator {
	return &Migrator{db: db}
}

// EnsureMigrationTable creates the schema_migrations tracking table if it does not exist.
func (m *Migrator) EnsureMigrationTable(ctx context.Context) error {
	query := `
	CREATE TABLE IF NOT EXISTS schema_migrations (
		version INT PRIMARY KEY,
		name VARCHAR(255) NOT NULL,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() AT TIME ZONE 'UTC')
	);`
	if _, err := m.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("failed to create schema_migrations table: %w", err)
	}
	return nil
}

// LoadFS reads and parses all .up.sql and .down.sql files from an abstract filesystem (e.g. embed.FS or os.DirFS).
func LoadFS(fsys fs.FS, dirPath string) ([]Migration, error) {
	if dirPath == "" {
		dirPath = "."
	}

	entries, err := fs.ReadDir(fsys, dirPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations from filesystem at %s: %w", dirPath, err)
	}

	migrationsMap := make(map[int]*Migration)

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		if !strings.HasSuffix(filename, ".sql") {
			continue
		}

		parts := strings.Split(filename, "_")
		if len(parts) < 2 {
			continue
		}

		version, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}

		filePath := filename
		if dirPath != "." && dirPath != "" {
			filePath = dirPath + "/" + filename
		}

		content, err := fs.ReadFile(fsys, filePath)
		if err != nil {
			return nil, fmt.Errorf("failed to read migration file %s: %w", filePath, err)
		}

		if _, exists := migrationsMap[version]; !exists {
			migrationsMap[version] = &Migration{
				Version: version,
				Name:    filename,
			}
		}

		if strings.HasSuffix(filename, ".up.sql") {
			migrationsMap[version].UpSQL = string(content)
		} else if strings.HasSuffix(filename, ".down.sql") {
			migrationsMap[version].DownSQL = string(content)
		}
	}

	migrations := make([]Migration, 0, len(migrationsMap))
	for _, m := range migrationsMap {
		migrations = append(migrations, *m)
	}

	// Order Guarantee: Strictly sort migrations ascending by version
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	return migrations, nil
}

// LoadMigrations reads and parses all .up.sql and .down.sql files from the specified disk directory.
func LoadMigrations(dirPath string) ([]Migration, error) {
	return LoadFS(os.DirFS(dirPath), ".")
}

// RunMigrations executes all pending embedded migrations on startup.
// Returns the count of newly applied migrations, or a descriptive error if execution fails.
func RunMigrations(ctx context.Context, db *sql.DB) (int, error) {
	migList, err := LoadFS(migrations.FS, ".")
	if err != nil {
		return 0, fmt.Errorf("failed loading embedded migrations: %w", err)
	}

	migrator := NewMigrator(db)
	return migrator.Up(ctx, migList)
}

// Up applies all pending migrations in ascending order.
// Enforces strict transaction isolation per migration and fail-fast semantics.
func (m *Migrator) Up(ctx context.Context, migrations []Migration) (int, error) {
	if err := m.EnsureMigrationTable(ctx); err != nil {
		return 0, err
	}

	appliedMap, err := m.getAppliedVersions(ctx)
	if err != nil {
		return 0, err
	}

	appliedCount := 0
	for _, mig := range migrations {
		// Idempotency: skip already applied migrations
		if appliedMap[mig.Version] {
			continue
		}

		if strings.TrimSpace(mig.UpSQL) == "" {
			continue
		}

		tx, err := m.db.BeginTx(ctx, nil)
		if err != nil {
			return appliedCount, fmt.Errorf("failed to begin transaction for migration %d: %w", mig.Version, err)
		}

		if _, err := tx.ExecContext(ctx, mig.UpSQL); err != nil {
			_ = tx.Rollback()
			return appliedCount, fmt.Errorf("failed executing Up migration %d (%s): %w", mig.Version, mig.Name, err)
		}

		recordQuery := `INSERT INTO schema_migrations (version, name, applied_at) VALUES ($1, $2, $3);`
		if _, err := tx.ExecContext(ctx, recordQuery, mig.Version, mig.Name, time.Now().UTC()); err != nil {
			_ = tx.Rollback()
			return appliedCount, fmt.Errorf("failed recording migration %d in schema_migrations: %w", mig.Version, err)
		}

		if err := tx.Commit(); err != nil {
			return appliedCount, fmt.Errorf("failed committing migration %d: %w", mig.Version, err)
		}
		appliedCount++
	}

	return appliedCount, nil
}

// Down rolls back the specified number of migrations in descending order. If steps <= 0, rolls back all applied migrations.
func (m *Migrator) Down(ctx context.Context, migrations []Migration, steps int) (int, error) {
	if err := m.EnsureMigrationTable(ctx); err != nil {
		return 0, err
	}

	appliedMap, err := m.getAppliedVersions(ctx)
	if err != nil {
		return 0, err
	}

	// Reverse sort migrations for rollback
	reversed := make([]Migration, len(migrations))
	copy(reversed, migrations)
	sort.Slice(reversed, func(i, j int) bool {
		return reversed[i].Version > reversed[j].Version
	})

	rolledBackCount := 0
	for _, mig := range reversed {
		if !appliedMap[mig.Version] {
			continue
		}

		if steps > 0 && rolledBackCount >= steps {
			break
		}

		if strings.TrimSpace(mig.DownSQL) == "" {
			continue
		}

		tx, err := m.db.BeginTx(ctx, nil)
		if err != nil {
			return rolledBackCount, fmt.Errorf("failed to begin rollback tx for migration %d: %w", mig.Version, err)
		}

		if _, err := tx.ExecContext(ctx, mig.DownSQL); err != nil {
			_ = tx.Rollback()
			return rolledBackCount, fmt.Errorf("failed executing Down migration %d (%s): %w", mig.Version, mig.Name, err)
		}

		deleteQuery := `DELETE FROM schema_migrations WHERE version = $1;`
		if _, err := tx.ExecContext(ctx, deleteQuery, mig.Version); err != nil {
			_ = tx.Rollback()
			return rolledBackCount, fmt.Errorf("failed removing migration %d from schema_migrations: %w", mig.Version, err)
		}

		if err := tx.Commit(); err != nil {
			return rolledBackCount, fmt.Errorf("failed committing rollback for migration %d: %w", mig.Version, err)
		}
		rolledBackCount++
	}

	return rolledBackCount, nil
}

func (m *Migrator) getAppliedVersions(ctx context.Context) (map[int]bool, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT version FROM schema_migrations;`)
	if err != nil {
		return nil, fmt.Errorf("failed to query schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int]bool)
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("failed scanning migration row: %w", err)
		}
		applied[v] = true
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error reading migration rows: %w", err)
	}

	return applied, nil
}
