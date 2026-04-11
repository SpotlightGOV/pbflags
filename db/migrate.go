// Package db provides embedded database migrations for pbflags.
// Migrations are managed by goose and applied automatically via --upgrade.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // pgx database/sql driver
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate runs all pending goose migrations against the given PostgreSQL DSN.
func Migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Ensure the schema exists before goose tries to create its version table there.
	if _, err := db.ExecContext(ctx, `CREATE SCHEMA IF NOT EXISTS feature_flags`); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	goose.SetBaseFS(migrations)
	goose.SetTableName("feature_flags.pbflags_goose_db_version")

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}

	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	return nil
}
