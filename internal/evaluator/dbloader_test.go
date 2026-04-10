package evaluator_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	defsync "github.com/SpotlightGOV/pbflags/internal/sync"
	"github.com/SpotlightGOV/pbflags/internal/testdb"
)

// TestDBLoaderEquivalence syncs a set of definitions to the DB and verifies
// that LoadDefinitionsFromDB returns equivalent FlagDefs.
func TestDBLoaderEquivalence(t *testing.T) {
	dsn, pool := testdb.Require(t)
	ctx := context.Background()

	// Source definitions — covers all four types and both layers.
	srcDefs := []evaluator.FlagDef{
		{
			FlagID: "equiv_test/1", FeatureID: "equiv_test", FieldNum: 1,
			Name: "dark_mode", FlagType: pbflagsv1.FlagType_FLAG_TYPE_BOOL,
			Layer: "", Default: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
			FeatureDisplayName: "EquivTest", FeatureDescription: "Equivalence test feature", FeatureOwner: "platform",
		},
		{
			FlagID: "equiv_test/2", FeatureID: "equiv_test", FieldNum: 2,
			Name: "theme", FlagType: pbflagsv1.FlagType_FLAG_TYPE_STRING,
			Layer: "user", Default: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "light"}},
			FeatureDisplayName: "EquivTest", FeatureDescription: "Equivalence test feature", FeatureOwner: "platform",
		},
		{
			FlagID: "equiv_test/3", FeatureID: "equiv_test", FieldNum: 3,
			Name: "max_items", FlagType: pbflagsv1.FlagType_FLAG_TYPE_INT64,
			Layer: "", Default: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: 100}},
			FeatureDisplayName: "EquivTest", FeatureDescription: "Equivalence test feature", FeatureOwner: "platform",
		},
		{
			FlagID: "equiv_test/4", FeatureID: "equiv_test", FieldNum: 4,
			Name: "threshold", FlagType: pbflagsv1.FlagType_FLAG_TYPE_DOUBLE,
			Layer: "", Default: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: 0.95}},
			FeatureDisplayName: "EquivTest", FeatureDescription: "Equivalence test feature", FeatureOwner: "platform",
		},
	}

	// Sync to DB.
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	_, err = defsync.SyncDefinitions(ctx, conn, srcDefs, slog.Default())
	conn.Close(ctx)
	require.NoError(t, err)

	// Load from DB.
	loaded, err := evaluator.LoadDefinitionsFromDB(ctx, pool)
	require.NoError(t, err)
	require.Len(t, loaded, len(srcDefs))

	// Build map for comparison.
	srcMap := make(map[string]evaluator.FlagDef)
	for _, d := range srcDefs {
		srcMap[d.FlagID] = d
	}

	for _, got := range loaded {
		want, ok := srcMap[got.FlagID]
		require.True(t, ok, "unexpected flag %s", got.FlagID)

		assert.Equal(t, want.FlagID, got.FlagID)
		assert.Equal(t, want.FeatureID, got.FeatureID)
		assert.Equal(t, want.FieldNum, got.FieldNum)
		assert.Equal(t, want.Name, got.Name)
		assert.Equal(t, want.FlagType, got.FlagType, "FlagType mismatch for %s", got.FlagID)
		assert.Equal(t, want.Layer, got.Layer, "Layer mismatch for %s", got.FlagID)
		assert.Equal(t, want.FeatureDisplayName, got.FeatureDisplayName)
		assert.Equal(t, want.FeatureDescription, got.FeatureDescription)
		assert.Equal(t, want.FeatureOwner, got.FeatureOwner)

		// Compare serialized default values (proto equality).
		if want.Default != nil {
			require.NotNil(t, got.Default, "default nil for %s", got.FlagID)
			assert.True(t, proto.Equal(want.Default, got.Default),
				"default mismatch for %s: want %v, got %v", got.FlagID, want.Default, got.Default)
		} else {
			assert.Nil(t, got.Default, "expected nil default for %s", got.FlagID)
		}

		// SupportedValues is not stored in DB — always nil from DB load.
		assert.Nil(t, got.SupportedValues, "SupportedValues should be nil from DB")
	}
}

// BenchmarkLoadDefinitionsFromDB benchmarks batched DB loading with 5K+ flags.
func BenchmarkLoadDefinitionsFromDB(b *testing.B) {
	dsn, pool := testdb.Require(b)
	ctx := context.Background()

	// Seed 5000 flags across 50 features.
	const numFeatures = 50
	const flagsPerFeature = 100
	var defs []evaluator.FlagDef
	for f := 0; f < numFeatures; f++ {
		featureID := fmt.Sprintf("bench_feat_%d", f)
		for i := 1; i <= flagsPerFeature; i++ {
			defs = append(defs, evaluator.FlagDef{
				FlagID:             fmt.Sprintf("%s/%d", featureID, i),
				FeatureID:          featureID,
				FieldNum:           int32(i),
				Name:               fmt.Sprintf("flag_%d", i),
				FlagType:           pbflagsv1.FlagType_FLAG_TYPE_BOOL,
				Default:            &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
				FeatureDisplayName: featureID,
			})
		}
	}

	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(b, err)
	_, err = defsync.SyncDefinitions(ctx, conn, defs, slog.Default())
	conn.Close(ctx)
	require.NoError(b, err)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		loaded, err := evaluator.LoadDefinitionsFromDB(ctx, pool)
		require.NoError(b, err)
		require.GreaterOrEqual(b, len(loaded), numFeatures*flagsPerFeature)
	}
}

// BenchmarkRegistrySwapUnderLoad benchmarks atomic registry swap with concurrent readers.
func BenchmarkRegistrySwapUnderLoad(b *testing.B) {
	defs := make([]evaluator.FlagDef, 5000)
	for i := range defs {
		defs[i] = evaluator.FlagDef{
			FlagID:   fmt.Sprintf("bench/%d", i),
			FlagType: pbflagsv1.FlagType_FLAG_TYPE_BOOL,
			Default:  &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
		}
	}

	reg := evaluator.NewRegistry(evaluator.NewDefaults(defs))

	// Concurrent readers.
	done := make(chan struct{})
	for r := 0; r < 8; r++ {
		go func() {
			for {
				select {
				case <-done:
					return
				default:
					d := reg.Load()
					_ = d.Len()
				}
			}
		}()
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		next := evaluator.NewDefaults(defs)
		reg.Swap(next)
	}
	b.StopTimer()
	close(done)
}
