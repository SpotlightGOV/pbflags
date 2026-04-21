package evaluator

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	example "github.com/SpotlightGOV/pbflags/gen/example"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/testdb"
)

// buildDescriptorSet creates a serialized FileDescriptorSet from runtime
// proto messages, including all transitive file imports.
func buildTestDescriptorSet(t *testing.T, msgs ...proto.Message) []byte {
	t.Helper()
	seen := map[string]bool{}
	var files []*descriptorpb.FileDescriptorProto
	for _, msg := range msgs {
		collectTestFiles(msg.ProtoReflect().Descriptor().ParentFile(), seen, &files)
	}
	fds := &descriptorpb.FileDescriptorSet{File: files}
	data, err := proto.Marshal(fds)
	require.NoError(t, err)
	return data
}

func collectTestFiles(fd protoreflect.FileDescriptor, seen map[string]bool, files *[]*descriptorpb.FileDescriptorProto) {
	if seen[fd.Path()] {
		return
	}
	seen[fd.Path()] = true
	for i := 0; i < fd.Imports().Len(); i++ {
		collectTestFiles(fd.Imports().Get(i), seen, files)
	}
	*files = append(*files, protodesc.ToFileDescriptorProto(fd))
}

// -----------------------------------------------------------------------
// discoverContextDescriptor
// -----------------------------------------------------------------------

func TestDiscoverContextDescriptor(t *testing.T) {
	t.Parallel()
	data := buildTestDescriptorSet(t, &example.Notifications{}, &example.EvaluationContext{})

	files, _, err := ParseDescriptorSet(data)
	require.NoError(t, err)

	md, err := discoverContextDescriptor(files)
	require.NoError(t, err)
	assert.Equal(t, "example.EvaluationContext", string(md.FullName()))
}

func TestDiscoverContextDescriptor_NoContext(t *testing.T) {
	t.Parallel()
	// Build a descriptor set from a type that lives in a file without
	// (pbflags.context). Use a well-known type from a different package.
	data := buildTestDescriptorSet(t, &pbflagsv1.FlagValue{})

	files, _, err := ParseDescriptorSet(data)
	require.NoError(t, err)

	_, err = discoverContextDescriptor(files)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no message with (pbflags.context)")
}

// -----------------------------------------------------------------------
// PruneContextDescriptorSet
// -----------------------------------------------------------------------

func TestPruneContextDescriptorSet(t *testing.T) {
	t.Parallel()
	full := buildTestDescriptorSet(t, &example.Notifications{}, &example.EvaluationContext{})

	pruned, err := PruneContextDescriptorSet(full)
	require.NoError(t, err)
	require.NotEmpty(t, pruned)

	// Pruned set should be no larger than the full set (may be equal when
	// all messages share a single proto file, as in the example protos).
	assert.LessOrEqual(t, len(pruned), len(full), "pruned should not be larger than full")

	// Parse the pruned set and verify we can still find EvaluationContext.
	files, _, err := ParseDescriptorSet(pruned)
	require.NoError(t, err)

	md, err := discoverContextDescriptor(files)
	require.NoError(t, err)
	assert.Equal(t, "example.EvaluationContext", string(md.FullName()))

	// Verify Notifications file is NOT in the pruned set.
	notifFile := (&example.Notifications{}).ProtoReflect().Descriptor().ParentFile().Path()
	evalFile := (&example.EvaluationContext{}).ProtoReflect().Descriptor().ParentFile().Path()

	// Only check if they're in different files (they're actually in the same file
	// in the example proto). If same file, pruning can't exclude one without the other.
	if notifFile != evalFile {
		var prunedFDS descriptorpb.FileDescriptorSet
		require.NoError(t, proto.Unmarshal(pruned, &prunedFDS))
		for _, f := range prunedFDS.File {
			assert.NotEqual(t, notifFile, f.GetName(),
				"Notifications file should not be in pruned set")
		}
	}
}

func TestPruneContextDescriptorSet_InvalidData(t *testing.T) {
	t.Parallel()
	_, err := PruneContextDescriptorSet([]byte("not valid proto"))
	assert.Error(t, err)
}

func TestPruneContextDescriptorSet_NoContext(t *testing.T) {
	t.Parallel()
	data := buildTestDescriptorSet(t, &pbflagsv1.FlagValue{})
	_, err := PruneContextDescriptorSet(data)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no message with (pbflags.context)")
}

// -----------------------------------------------------------------------
// LoadConditionEvaluatorFromDescriptorSet
// -----------------------------------------------------------------------

func TestLoadConditionEvaluatorFromDescriptorSet_NilData(t *testing.T) {
	t.Parallel()
	ce, err := LoadConditionEvaluatorFromDescriptorSet(nil, slog.Default())
	require.NoError(t, err)
	assert.Nil(t, ce)
}

func TestLoadConditionEvaluatorFromDescriptorSet_EmptyData(t *testing.T) {
	t.Parallel()
	ce, err := LoadConditionEvaluatorFromDescriptorSet([]byte{}, slog.Default())
	require.NoError(t, err)
	assert.Nil(t, ce)
}

func TestLoadConditionEvaluatorFromDescriptorSet_Valid(t *testing.T) {
	t.Parallel()
	full := buildTestDescriptorSet(t, &example.Notifications{}, &example.EvaluationContext{})
	pruned, err := PruneContextDescriptorSet(full)
	require.NoError(t, err)

	ce, err := LoadConditionEvaluatorFromDescriptorSet(pruned, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, ce)
}

func TestLoadConditionEvaluatorFromDescriptorSet_CanCompileCEL(t *testing.T) {
	t.Parallel()
	full := buildTestDescriptorSet(t, &example.Notifications{}, &example.EvaluationContext{})
	pruned, err := PruneContextDescriptorSet(full)
	require.NoError(t, err)

	ce, err := LoadConditionEvaluatorFromDescriptorSet(pruned, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, ce)

	// Verify it can compile a CEL expression referencing EvaluationContext fields.
	condBytes := mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{Cel: "ctx.is_internal == true", Value: boolFlagValueBytes(t, true)},
		{Cel: "", Value: boolFlagValueBytes(t, false)}, // otherwise
	})
	compiled := ce.CompileConditions("test/1", condBytes)
	require.Len(t, compiled, 2, "should compile both conditions")
	assert.NotNil(t, compiled[0].Program, "CEL condition should have a compiled program")
	assert.Nil(t, compiled[1].Program, "otherwise condition should have nil program")
}

func TestLoadConditionEvaluatorFromDescriptorSet_CanEvaluate(t *testing.T) {
	t.Parallel()
	full := buildTestDescriptorSet(t, &example.Notifications{}, &example.EvaluationContext{})
	pruned, err := PruneContextDescriptorSet(full)
	require.NoError(t, err)

	ce, err := LoadConditionEvaluatorFromDescriptorSet(pruned, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, ce)

	condBytes := mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{Cel: "ctx.is_internal == true", Value: boolFlagValueBytes(t, true)},
		{Cel: "", Value: boolFlagValueBytes(t, false)},
	})
	compiled := ce.CompileConditions("test/1", condBytes)
	require.Len(t, compiled, 2)

	// Evaluate with is_internal=true — should match first condition.
	evalCtx := &example.EvaluationContext{IsInternal: true}
	result := ce.EvaluateConditionsWithOverrides("test/1", compiled, evalCtx, nil)
	require.NotNil(t, result.Value)
	assert.True(t, result.Value.GetBoolValue())

	// Evaluate with is_internal=false — should fall through to otherwise.
	evalCtx2 := &example.EvaluationContext{IsInternal: false}
	result2 := ce.EvaluateConditionsWithOverrides("test/1", compiled, evalCtx2, nil)
	require.NotNil(t, result2.Value)
	assert.False(t, result2.Value.GetBoolValue())
}

func TestPruneAndLoadRoundTrip_WithEnumConditions(t *testing.T) {
	t.Parallel()
	full := buildTestDescriptorSet(t, &example.Notifications{}, &example.EvaluationContext{})
	pruned, err := PruneContextDescriptorSet(full)
	require.NoError(t, err)

	ce, err := LoadConditionEvaluatorFromDescriptorSet(pruned, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, ce)

	// Test with an enum-based CEL expression.
	condBytes := mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{Cel: "ctx.plan == PlanLevel.ENTERPRISE", Value: stringFlagValueBytes(t, "daily")},
		{Cel: "", Value: stringFlagValueBytes(t, "weekly")},
	})
	compiled := ce.CompileConditions("test/2", condBytes)
	require.Len(t, compiled, 2)
	assert.NotNil(t, compiled[0].Program)

	// Enterprise plan should match.
	evalCtx := &example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_ENTERPRISE}
	result := ce.EvaluateConditionsWithOverrides("test/2", compiled, evalCtx, nil)
	require.NotNil(t, result.Value)
	assert.Equal(t, "daily", result.Value.GetStringValue())

	// Free plan should fall through to otherwise.
	evalCtx2 := &example.EvaluationContext{Plan: example.PlanLevel_PLAN_LEVEL_FREE}
	result2 := ce.EvaluateConditionsWithOverrides("test/2", compiled, evalCtx2, nil)
	require.NotNil(t, result2.Value)
	assert.Equal(t, "weekly", result2.Value.GetStringValue())
}

// -----------------------------------------------------------------------
// DB round-trip: UpsertContextDescriptor + LoadContextDescriptorFromDB
// -----------------------------------------------------------------------

func TestContextDescriptorDBRoundTrip(t *testing.T) {
	_, pool := testdb.Require(t)
	ctx := context.Background()

	full := buildTestDescriptorSet(t, &example.Notifications{}, &example.EvaluationContext{})
	pruned, err := PruneContextDescriptorSet(full)
	require.NoError(t, err)

	// Before any upsert, load should return nil.
	data, err := LoadContextDescriptorFromDB(ctx, pool)
	require.NoError(t, err)
	assert.Nil(t, data, "should be nil before any upsert")

	// Upsert the descriptor.
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, UpsertContextDescriptor(ctx, tx, pruned))
	require.NoError(t, tx.Commit(ctx))
	conn.Release()

	// Load should now return the descriptor.
	data, err = LoadContextDescriptorFromDB(ctx, pool)
	require.NoError(t, err)
	require.NotNil(t, data)
	assert.Equal(t, pruned, data)

	// Full round trip: load from DB → create ConditionEvaluator → compile CEL.
	ce, err := LoadConditionEvaluatorFromDescriptorSet(data, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, ce)

	condBytes := mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{Cel: "ctx.is_internal == true", Value: boolFlagValueBytes(t, true)},
		{Cel: "", Value: boolFlagValueBytes(t, false)},
	})
	compiled := ce.CompileConditions("test/1", condBytes)
	require.Len(t, compiled, 2)
	assert.NotNil(t, compiled[0].Program)
}

func TestContextDescriptorDBRoundTrip_Upsert(t *testing.T) {
	_, pool := testdb.Require(t)
	ctx := context.Background()

	full := buildTestDescriptorSet(t, &example.Notifications{}, &example.EvaluationContext{})
	pruned, err := PruneContextDescriptorSet(full)
	require.NoError(t, err)

	// First upsert.
	conn, err := pool.Acquire(ctx)
	require.NoError(t, err)
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, UpsertContextDescriptor(ctx, tx, pruned))
	require.NoError(t, tx.Commit(ctx))
	conn.Release()

	// Second upsert with same data — should succeed (idempotent).
	conn, err = pool.Acquire(ctx)
	require.NoError(t, err)
	tx, err = conn.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, UpsertContextDescriptor(ctx, tx, pruned))
	require.NoError(t, tx.Commit(ctx))
	conn.Release()

	data, err := LoadContextDescriptorFromDB(ctx, pool)
	require.NoError(t, err)
	assert.Equal(t, pruned, data)
}

// -----------------------------------------------------------------------
// Integration: DBFetcher + ConditionEvaluator wired together
// -----------------------------------------------------------------------

func TestDBFetcherWithConditionEvaluator(t *testing.T) {
	_, pool := testdb.Require(t)
	ctx := context.Background()

	// Create a test feature with a bool flag.
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})

	// Build and store conditions for the flag.
	condBytes := mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{Cel: "ctx.is_internal == true", Value: boolFlagValueBytes(t, true)},
		{Cel: "", Value: boolFlagValueBytes(t, false)},
	})
	_, err := pool.Exec(ctx,
		`UPDATE feature_flags.flags SET conditions = $2, cel_version = 'test', condition_count = 2 WHERE flag_id = $1`,
		tf.FlagIDs[0], condBytes)
	require.NoError(t, err)

	// Create ConditionEvaluator from example descriptor.
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	condEval, err := NewConditionEvaluator(md, slog.Default())
	require.NoError(t, err)

	// Create DBFetcher WITH ConditionEvaluator.
	metrics := NewNoopMetrics()
	tracker := NewHealthTracker(metrics)
	tracer := noopTracer()
	fetcher := NewDBFetcher(pool, tracker, slog.Default(), metrics, tracer,
		WithDBConditionEvaluator(condEval))

	state, err := fetcher.FetchFlagState(ctx, tf.FlagIDs[0])
	require.NoError(t, err)
	require.NotNil(t, state)
	require.Len(t, state.Conditions, 2, "conditions should be compiled when ConditionEvaluator is wired")
	assert.NotNil(t, state.Conditions[0].Program, "first condition should have compiled CEL program")
	assert.Nil(t, state.Conditions[1].Program, "otherwise condition should have nil program")
}

func TestDBFetcherWithoutConditionEvaluator(t *testing.T) {
	_, pool := testdb.Require(t)
	ctx := context.Background()

	// Create a test feature with a bool flag with conditions.
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
	})
	condBytes := mustMarshalConditions(t, []*pbflagsv1.CompiledCondition{
		{Cel: "ctx.is_internal == true", Value: boolFlagValueBytes(t, true)},
		{Cel: "", Value: boolFlagValueBytes(t, false)},
	})
	_, err := pool.Exec(ctx,
		`UPDATE feature_flags.flags SET conditions = $2, cel_version = 'test', condition_count = 2 WHERE flag_id = $1`,
		tf.FlagIDs[0], condBytes)
	require.NoError(t, err)

	// Create DBFetcher WITHOUT ConditionEvaluator (the bug).
	metrics := NewNoopMetrics()
	tracker := NewHealthTracker(metrics)
	tracer := noopTracer()
	fetcher := NewDBFetcher(pool, tracker, slog.Default(), metrics, tracer)

	state, err := fetcher.FetchFlagState(ctx, tf.FlagIDs[0])
	require.NoError(t, err)
	require.NotNil(t, state)
	assert.Empty(t, state.Conditions, "without ConditionEvaluator, conditions should be empty (the bug)")
}
