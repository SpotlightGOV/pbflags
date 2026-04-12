// Package testdb provides a shared PostgreSQL test container for integration tests.
// It starts a single container per test binary (via sync.Once), runs goose migrations,
// and returns a connection pool ready for use.
//
// Tests should use [CreateTestFeature] to create uniquely-named features and
// flags that are automatically cleaned up, enabling parallel test execution.
package testdb

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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
// tb.Cleanup.
//
// Tables are NOT truncated; use [CreateTestFeature] to create isolated test
// fixtures that are cleaned up automatically per test.
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

	return pgDSN, pool
}

// featureSeq provides unique numeric suffixes to keep feature_id values short
// enough for the VARCHAR(255) column while still being unique across parallel tests.
var featureSeq atomic.Int64

// sanitizeName converts a test name (which may contain slashes and other
// characters) into a short, safe identifier suitable for a feature_id.
func sanitizeName(name string) string {
	// Replace path separators and spaces with underscores.
	r := strings.NewReplacer("/", "_", " ", "_", "(", "", ")", "")
	s := r.Replace(name)
	// Truncate to keep within VARCHAR(255) with room for the sequence suffix.
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// TestFeature holds the feature_id and flag IDs created for a test.
type TestFeature struct {
	FeatureID string
	FlagIDs   []string
}

// FlagID returns the flag_id for the given field number (1-based).
// Panics if fieldNum is out of range.
func (f *TestFeature) FlagID(fieldNum int) string {
	if fieldNum < 1 || fieldNum > len(f.FlagIDs) {
		panic(fmt.Sprintf("testdb: FlagID(%d) out of range [1, %d]", fieldNum, len(f.FlagIDs)))
	}
	return f.FlagIDs[fieldNum-1]
}

// FlagSpec describes one flag to create within a test feature.
type FlagSpec struct {
	FlagType string // e.g. "BOOL", "STRING", "INT64", "DOUBLE"
	Layer    string // e.g. "USER", "GLOBAL"
}

// CreateTestFeature inserts a uniquely-named feature and its flags into the
// database. The feature_id is derived from t.Name() plus a sequence number,
// guaranteeing no collisions even when tests run in parallel.
//
// Cleanup is registered via t.Cleanup to delete the feature, its flags,
// overrides, and audit log entries when the test finishes.
func CreateTestFeature(t *testing.T, pool *pgxpool.Pool, specs []FlagSpec) *TestFeature {
	t.Helper()

	seq := featureSeq.Add(1)
	featureID := fmt.Sprintf("%s_%d", sanitizeName(t.Name()), seq)

	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO feature_flags.features (feature_id)
		VALUES ($1)`, featureID)
	if err != nil {
		t.Fatalf("testdb: create feature %s: %v", featureID, err)
	}

	tf := &TestFeature{FeatureID: featureID}
	for i, spec := range specs {
		flagID := fmt.Sprintf("%s/%d", featureID, i+1)
		_, err := pool.Exec(ctx, `
			INSERT INTO feature_flags.flags (flag_id, feature_id, field_number, flag_type, layer, state)
			VALUES ($1, $2, $3, $4, $5, 'DEFAULT')`,
			flagID, featureID, i+1, spec.FlagType, spec.Layer)
		if err != nil {
			t.Fatalf("testdb: create flag %s: %v", flagID, err)
		}
		tf.FlagIDs = append(tf.FlagIDs, flagID)
	}

	t.Cleanup(func() {
		ctx := context.Background()
		// Delete in FK order: audit log (no FK but references flag_id), overrides, flags, feature.
		for _, fid := range tf.FlagIDs {
			pool.Exec(ctx, `DELETE FROM feature_flags.flag_audit_log WHERE flag_id = $1`, fid)
			pool.Exec(ctx, `DELETE FROM feature_flags.flag_overrides WHERE flag_id = $1`, fid)
		}
		pool.Exec(ctx, `DELETE FROM feature_flags.flags WHERE feature_id = $1`, featureID)
		pool.Exec(ctx, `DELETE FROM feature_flags.features WHERE feature_id = $1`, featureID)
	})

	return tf
}
