// Package integrationtest provides small helpers for PostgreSQL-backed tests that share one database.
package integrationtest

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

var prefixSeq atomic.Uint64

// Prefix returns a unique string for this test run. Combine with Feature() so parallel
// tests do not collide, then pass the same prefix to RegisterCleanup.
func Prefix(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("t%d_%d", time.Now().UnixNano(), prefixSeq.Add(1))
}

// Feature builds a namespaced feature_id: prefix + "_" + logicalName (logicalName
// matches prod style, e.g. "notifications").
func Feature(prefix, logicalName string) string {
	return prefix + "_" + logicalName
}

// Flag returns flag_id in production format: feature_id/field_number (same as
// internal/evaluator/descriptor.go and protoc-gen-pbflags generated *ID constants).
func Flag(featureID string, fieldNumber int32) string {
	return fmt.Sprintf("%s/%d", featureID, fieldNumber)
}

// CleanupFeatureTree deletes rows for every feature_id and flag_id starting with prefix.
func CleanupFeatureTree(t *testing.T, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	ctx := context.Background()
	pat := prefix + "%"
	_, err := pool.Exec(ctx, `DELETE FROM feature_flags.flag_audit_log WHERE flag_id LIKE $1`, pat)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM feature_flags.flag_overrides WHERE flag_id LIKE $1`, pat)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM feature_flags.flags WHERE feature_id LIKE $1`, pat)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `DELETE FROM feature_flags.features WHERE feature_id LIKE $1`, pat)
	require.NoError(t, err)
}

// RegisterCleanup runs CleanupFeatureTree after the test (before the pool is closed).
func RegisterCleanup(t *testing.T, pool *pgxpool.Pool, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		CleanupFeatureTree(t, pool, prefix)
	})
}

// FilterFeatures keeps feature rows whose feature_id starts with prefix (for ListFeatures).
func FilterFeatures(all []*pbflagsv1.FeatureDetail, prefix string) []*pbflagsv1.FeatureDetail {
	var out []*pbflagsv1.FeatureDetail
	for _, f := range all {
		if strings.HasPrefix(f.FeatureId, prefix) {
			out = append(out, f)
		}
	}
	return out
}

// FilterAuditLog keeps entries for flags under this test prefix.
func FilterAuditLog(entries []*pbflagsv1.AuditLogEntry, prefix string) []*pbflagsv1.AuditLogEntry {
	var out []*pbflagsv1.AuditLogEntry
	for _, e := range entries {
		if strings.HasPrefix(e.FlagId, prefix) {
			out = append(out, e)
		}
	}
	return out
}

// FilterKilledFlagIDs keeps global kill entries for this test's flags.
func FilterKilledFlagIDs(ids []string, prefix string) []string {
	var out []string
	for _, id := range ids {
		if strings.HasPrefix(id, prefix) {
			out = append(out, id)
		}
	}
	return out
}

// FilterKilledOverrides keeps override kills for flags under prefix.
func FilterKilledOverrides(overrides []*pbflagsv1.KilledOverride, prefix string) []*pbflagsv1.KilledOverride {
	var out []*pbflagsv1.KilledOverride
	for _, o := range overrides {
		if strings.HasPrefix(o.FlagId, prefix) {
			out = append(out, o)
		}
	}
	return out
}
