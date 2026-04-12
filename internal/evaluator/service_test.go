package evaluator

import (
	"context"
	"log/slog"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

func TestService_Evaluate(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: boolVal(true)},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())
	tracker := NewHealthTracker(NewNoopMetrics())
	svc := NewService(eval, tracker, cache, nil)

	resp, err := svc.Evaluate(context.Background(), connect.NewRequest(&pbflagsv1.EvaluateRequest{
		FlagId: "f/1",
	}))
	require.NoError(t, err, "Evaluate returned error")
	require.Equal(t, "f/1", resp.Msg.FlagId, "flag_id")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, resp.Msg.Source, "source")
	require.Equal(t, true, resp.Msg.Value.GetBoolValue(), "value")
}

func TestService_BulkEvaluate_SpecificFlags(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: boolVal(true)},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())
	tracker := NewHealthTracker(NewNoopMetrics())
	svc := NewService(eval, tracker, cache, nil)

	resp, err := svc.BulkEvaluate(context.Background(), connect.NewRequest(&pbflagsv1.BulkEvaluateRequest{
		FlagIds: []string{"f/1", "f/2"},
	}))
	require.NoError(t, err, "BulkEvaluate error")
	require.Len(t, resp.Msg.Evaluations, 2, "evaluations count")
}

func TestService_BulkEvaluate_EmptyFlags(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())
	tracker := NewHealthTracker(NewNoopMetrics())
	svc := NewService(eval, tracker, cache, nil)

	resp, err := svc.BulkEvaluate(context.Background(), connect.NewRequest(&pbflagsv1.BulkEvaluateRequest{}))
	require.NoError(t, err, "BulkEvaluate error")
	require.Empty(t, resp.Msg.Evaluations, "evaluations count (empty when no flag IDs)")
}

// TestService_Evaluate_IgnoresEntityOverrides verifies the transitional behavior:
// the service passes empty entityID to the evaluator, so per-entity overrides
// are not resolved via the wire protocol. This is intentional until pb-cfx.16
// replaces entity_id with context-based condition evaluation.
func TestService_Evaluate_IgnoresEntityOverrides(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: boolVal(true)},
		overrides: []*CachedOverride{
			{FlagID: "f/1", EntityID: "user-42", State: pbflagsv1.State_STATE_ENABLED, Value: strVal("override-val")},
		},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())
	tracker := NewHealthTracker(NewNoopMetrics())
	svc := NewService(eval, tracker, cache, nil)

	// Even though an override exists for user-42, the service passes empty
	// entityID so the evaluator returns the global state.
	resp, err := svc.Evaluate(context.Background(), connect.NewRequest(&pbflagsv1.EvaluateRequest{
		FlagId: "f/1",
	}))
	require.NoError(t, err)
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, resp.Msg.Source,
		"service should return global state, not override (entity_id is discarded in transition)")
}

func TestService_Health(t *testing.T) {
	cache := newTestCache(t)
	tracker := NewHealthTracker(NewNoopMetrics())
	fetcher := &stubFetcher{}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())
	svc := NewService(eval, tracker, cache, nil)

	resp, err := svc.Health(context.Background(), connect.NewRequest(&pbflagsv1.HealthRequest{}))
	require.NoError(t, err, "Health error")
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_CONNECTING, resp.Msg.Status, "status")
	require.Equal(t, int32(0), resp.Msg.ConsecutiveFailures, "failures")

	for i := 0; i < 5; i++ {
		tracker.RecordFailure()
	}
	resp, err = svc.Health(context.Background(), connect.NewRequest(&pbflagsv1.HealthRequest{}))
	require.NoError(t, err, "Health error")
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_DEGRADED, resp.Msg.Status, "status")
	require.Equal(t, int32(5), resp.Msg.ConsecutiveFailures, "failures")
}
