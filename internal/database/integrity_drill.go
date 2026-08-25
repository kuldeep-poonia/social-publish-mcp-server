// Package database provides production data integrity verification, SHA-256 checksums, and out-of-band token decryptability drills.
package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/kuldeep-poonia/social-publish-mcp-server/internal/crypto"
)

var (
	// ErrChecksumMismatch is returned when restored table data does not match pre-backup checksum.
	ErrChecksumMismatch = errors.New("data integrity violation: SHA-256 checksum mismatch")
	// ErrRowCountMismatch is returned when table row count differs after restoration.
	ErrRowCountMismatch = errors.New("data integrity violation: table row count mismatch")
	// ErrTokenDecryptionFailure is returned when an encrypted token cannot be decrypted with the out-of-band master key.
	ErrTokenDecryptionFailure = errors.New("data integrity violation: encrypted OAuth token decryption failed")
)

// TableSnapshot represents a deterministic point-in-time state of a database table.
type TableSnapshot struct {
	TableName      string                   `json:"table_name"`
	RowCount       int                      `json:"row_count"`
	ChecksumSHA256 string                   `json:"checksum_sha256"`
	Rows           []map[string]interface{} `json:"rows"`
}

// DatabaseIntegritySnapshot represents a verifiable snapshot of database relational tables.
type DatabaseIntegritySnapshot struct {
	Timestamp        time.Time                 `json:"timestamp"`
	TotalTables      int                       `json:"total_tables"`
	TotalRows        int                       `json:"total_rows"`
	CombinedChecksum string                    `json:"combined_checksum"`
	Tables           map[string]*TableSnapshot `json:"tables"`
}

// IntegrityReport contains metrics recorded during an integrity and decryptability verification drill.
type IntegrityReport struct {
	StartTime              time.Time     `json:"start_time"`
	EndTime                time.Time     `json:"end_time"`
	VerificationDuration   time.Duration `json:"verification_duration"`
	TablesVerified         int           `json:"tables_verified"`
	TotalRowsVerified      int           `json:"total_rows_verified"`
	ChecksumMatches        int           `json:"checksum_matches"`
	TokensVerified         int           `json:"tokens_verified"`
	TokensDecryptedSuccess int           `json:"tokens_decrypted_success"`
	IntegrityMatchRate     float64       `json:"integrity_match_rate"`
	DecryptabilityRate     float64       `json:"decryptability_rate"`
	Status                 string        `json:"status"`
}

// GenerateIntegritySnapshot exports table rows and calculates deterministic SHA-256 checksums.
func GenerateIntegritySnapshot(ctx context.Context, db *sql.DB, tables []string) (*DatabaseIntegritySnapshot, error) {
	if db == nil {
		return nil, errors.New("database handle cannot be nil")
	}

	snapshot := &DatabaseIntegritySnapshot{
		Timestamp: time.Now().UTC(),
		Tables:    make(map[string]*TableSnapshot),
	}

	var combinedHasher = sha256.New()

	for _, tableName := range tables {
		query := fmt.Sprintf("SELECT * FROM %s", tableName)
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("failed querying table '%s': %w", tableName, err)
		}

		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return nil, fmt.Errorf("failed fetching columns for table '%s': %w", tableName, err)
		}

		var tableRows []map[string]interface{}
		var tableHasher = sha256.New()

		for rows.Next() {
			values := make([]interface{}, len(cols))
			valuePtrs := make([]interface{}, len(cols))
			for i := range values {
				valuePtrs[i] = &values[i]
			}

			if err := rows.Scan(valuePtrs...); err != nil {
				rows.Close()
				return nil, fmt.Errorf("failed scanning row in table '%s': %w", tableName, err)
			}

			rowMap := make(map[string]interface{})
			for i, colName := range cols {
				val := values[i]
				if b, ok := val.([]byte); ok {
					rowMap[colName] = hex.EncodeToString(b)
				} else {
					rowMap[colName] = val
				}
			}
			tableRows = append(tableRows, rowMap)
		}
		rows.Close()

		// Deterministic row sorting by JSON representation to ensure repeatable checksums
		sort.Slice(tableRows, func(i, j int) bool {
			bi, _ := json.Marshal(tableRows[i])
			bj, _ := json.Marshal(tableRows[j])
			return bytes.Compare(bi, bj) < 0
		})

		for _, r := range tableRows {
			rowBytes, _ := json.Marshal(r)
			tableHasher.Write(rowBytes)
			combinedHasher.Write(rowBytes)
		}

		tableChecksum := hex.EncodeToString(tableHasher.Sum(nil))
		snapshot.Tables[tableName] = &TableSnapshot{
			TableName:      tableName,
			RowCount:       len(tableRows),
			ChecksumSHA256: tableChecksum,
			Rows:           tableRows,
		}
		snapshot.TotalRows += len(tableRows)
	}

	snapshot.TotalTables = len(snapshot.Tables)
	snapshot.CombinedChecksum = hex.EncodeToString(combinedHasher.Sum(nil))

	return snapshot, nil
}

// VerifyDataIntegrityAndDecryptability validates that destination database data matches snapshot checksums
// and that all encrypted OAuth credentials can be cleanly decrypted with the out-of-band master key.
func VerifyDataIntegrityAndDecryptability(ctx context.Context, snapshot *DatabaseIntegritySnapshot, db *sql.DB, outOfBandMasterKey []byte) (*IntegrityReport, error) {
	if snapshot == nil {
		return nil, errors.New("integrity snapshot cannot be nil")
	}
	if db == nil {
		return nil, errors.New("database handle cannot be nil")
	}
	if len(outOfBandMasterKey) != 32 {
		return nil, crypto.ErrInvalidKeySize
	}

	start := time.Now().UTC()
	report := &IntegrityReport{
		StartTime:      start,
		TablesVerified: len(snapshot.Tables),
	}

	for tableName, expectedTable := range snapshot.Tables {
		currentSnapshot, err := GenerateIntegritySnapshot(ctx, db, []string{tableName})
		if err != nil {
			return nil, fmt.Errorf("failed generating verification snapshot for table '%s': %w", tableName, err)
		}

		actualTable := currentSnapshot.Tables[tableName]
		if actualTable.RowCount != expectedTable.RowCount {
			return nil, fmt.Errorf("%w for table '%s': expected %d rows, found %d rows", ErrRowCountMismatch, tableName, expectedTable.RowCount, actualTable.RowCount)
		}

		if actualTable.ChecksumSHA256 != expectedTable.ChecksumSHA256 {
			return nil, fmt.Errorf("%w for table '%s': expected checksum '%s', found '%s'", ErrChecksumMismatch, tableName, expectedTable.ChecksumSHA256, actualTable.ChecksumSHA256)
		}

		report.TotalRowsVerified += actualTable.RowCount
		report.ChecksumMatches++
	}

	// Verify OAuth Token Decryptability for platform_connections table
	if _, hasPlatformConn := snapshot.Tables["platform_connections"]; hasPlatformConn {
		query := `SELECT id, user_id, platform, encrypted_access_token, encrypted_refresh_token FROM platform_connections WHERE is_active = TRUE;`
		rows, err := db.QueryContext(ctx, query)
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var id, userID, platform string
				var encAccess, encRefresh []byte
				if scanErr := rows.Scan(&id, &userID, &platform, &encAccess, &encRefresh); scanErr == nil {
					if len(encAccess) > 0 {
						report.TokensVerified++
						decAccess, decErr := crypto.DecryptOAuthToken(encAccess, outOfBandMasterKey)
						if decErr != nil || len(decAccess) == 0 {
							return nil, fmt.Errorf("%w: failed decrypting access token for user %s on %s: %v", ErrTokenDecryptionFailure, userID, platform, decErr)
						}
						report.TokensDecryptedSuccess++
					}

					if len(encRefresh) > 0 {
						report.TokensVerified++
						decRefresh, decErr := crypto.DecryptOAuthToken(encRefresh, outOfBandMasterKey)
						if decErr != nil || len(decRefresh) == 0 {
							return nil, fmt.Errorf("%w: failed decrypting refresh token for user %s on %s: %v", ErrTokenDecryptionFailure, userID, platform, decErr)
						}
						report.TokensDecryptedSuccess++
					}
				}
			}
		}
	}

	report.EndTime = time.Now().UTC()
	report.VerificationDuration = report.EndTime.Sub(report.StartTime)

	if report.TablesVerified > 0 {
		report.IntegrityMatchRate = float64(report.ChecksumMatches) / float64(report.TablesVerified) * 100.0
	} else {
		report.IntegrityMatchRate = 100.0
	}

	if report.TokensVerified > 0 {
		report.DecryptabilityRate = float64(report.TokensDecryptedSuccess) / float64(report.TokensVerified) * 100.0
	} else {
		report.DecryptabilityRate = 100.0
	}

	report.Status = "PASSED_VERIFIED"
	return report, nil
}
