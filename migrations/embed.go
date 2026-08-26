// Package migrations provides embedded SQL migration scripts for database schema provisioning.
package migrations

import "embed"

// FS contains the embedded versioned .up.sql and .down.sql migration files.
//
//go:embed *.sql
var FS embed.FS
