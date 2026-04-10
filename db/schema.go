package db

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// MinSchemaVersion is the minimum migration version required by this binary.
const MinSchemaVersion = 1

// CheckSchemaVersion verifies the database schema meets the minimum required
// migration version. Returns an error with an actionable message if the schema
// is missing or behind.
func CheckSchemaVersion(ctx context.Context, dsn string) error {
	conn, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer conn.Close()

	return CheckSchemaVersionConn(ctx, conn)
}

// CheckSchemaVersionConn is like CheckSchemaVersion but accepts an existing
// *sql.DB connection.
func CheckSchemaVersionConn(ctx context.Context, conn *sql.DB) error {
	var version int64
	err := conn.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied = true`,
	).Scan(&version)
	if err != nil {
		return fmt.Errorf("database schema version 0 < required %d\n"+
			"  run \"pbflags-sync --database=...\" to apply migrations, or\n"+
			"  start with \"pbflags-server --monolithic --migrate\" to auto-migrate",
			MinSchemaVersion)
	}

	if version < int64(MinSchemaVersion) {
		return fmt.Errorf("database schema version %d < required %d\n"+
			"  run \"pbflags-sync --database=...\" to apply migrations, or\n"+
			"  start with \"pbflags-server --monolithic --migrate\" to auto-migrate",
			version, MinSchemaVersion)
	}

	return nil
}
