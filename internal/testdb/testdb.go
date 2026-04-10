// Package testdb provides a shared PostgreSQL test container for integration tests.
// It starts a single container per test binary (via sync.Once), runs goose migrations,
// and returns a connection pool ready for use.
package testdb

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/SpotlightGOV/pbflags/db"
)

var (
	once  sync.Once
	pgDSN string
	pgErr error
)

func start() {
	ctx := context.Background()

	ctr, err := postgres.Run(ctx,
		"postgres:18-alpine",
		postgres.WithDatabase("pbflags"),
		postgres.WithUsername("admin"),
		postgres.WithPassword("admin"),
		postgres.BasicWaitStrategies(),
		postgres.WithSQLDriver("pgx"),
	)
	if err != nil {
		pgErr = err
		return
	}

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		pgErr = err
		_ = ctr.Terminate(ctx)
		return
	}

	if err := db.Migrate(ctx, dsn); err != nil {
		pgErr = err
		_ = ctr.Terminate(ctx)
		return
	}

	pgDSN = dsn
}

// Require starts a PostgreSQL test container (once per binary), runs goose
// migrations, and returns a fresh connection pool. The pool is closed via
// tb.Cleanup. Tables are truncated for test isolation.
func Require(tb testing.TB) (string, *pgxpool.Pool) {
	tb.Helper()

	once.Do(start)
	if pgErr != nil {
		tb.Fatalf("testdb: start container: %v", pgErr)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, pgDSN)
	if err != nil {
		tb.Fatalf("testdb: create pool: %v", err)
	}
	tb.Cleanup(func() { pool.Close() })

	_, err = pool.Exec(ctx, `
		TRUNCATE feature_flags.flag_audit_log, feature_flags.flag_overrides,
		         feature_flags.flags, feature_flags.features CASCADE`)
	if err != nil {
		tb.Fatalf("testdb: truncate: %v", err)
	}

	return pgDSN, pool
}
