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
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT},
	}
	eval := NewEvaluator(cache, fetcher, slog.Default(), NewNoopMetrics(), noopTracer())
	tracker := NewHealthTracker(NewNoopMetrics())
	svc := NewService(eval, tracker, cache, nil)

	resp, err := svc.Evaluate(context.Background(), connect.NewRequest(&pbflagsv1.EvaluateRequest{
		FlagId: "f/1",
	}))
	require.NoError(t, err, "Evaluate returned error")
	require.Equal(t, "f/1", resp.Msg.FlagId, "flag_id")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, resp.Msg.Source, "source")
	require.Nil(t, resp.Msg.Value, "value = nil (conditions handle values)")
}

func TestService_BulkEvaluate_SpecificFlags(t *testing.T) {
	cache := newTestCache(t)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_DEFAULT},
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
