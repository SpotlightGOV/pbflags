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
	reg := registryWith(globalFlag("f/1", boolVal(false)))
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: boolVal(true)},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default())
	tracker := NewHealthTracker()
	svc := NewService(eval, reg, tracker, cache, nil)

	resp, err := svc.Evaluate(context.Background(), connect.NewRequest(&pbflagsv1.EvaluateRequest{
		FlagId:   "f/1",
		EntityId: "",
	}))
	require.NoError(t, err, "Evaluate returned error")
	require.Equal(t, "f/1", resp.Msg.FlagId, "flag_id")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL, resp.Msg.Source, "source")
	require.Equal(t, true, resp.Msg.Value.GetBoolValue(), "value")
}

func TestService_BulkEvaluate_SpecificFlags(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(
		globalFlag("f/1", boolVal(false)),
		globalFlag("f/2", strVal("default")),
	)
	fetcher := &stubFetcher{
		flagState: &CachedFlagState{FlagID: "f/1", State: pbflagsv1.State_STATE_ENABLED, Value: boolVal(true)},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default())
	tracker := NewHealthTracker()
	svc := NewService(eval, reg, tracker, cache, nil)

	resp, err := svc.BulkEvaluate(context.Background(), connect.NewRequest(&pbflagsv1.BulkEvaluateRequest{
		FlagIds: []string{"f/1", "f/2"},
	}))
	require.NoError(t, err, "BulkEvaluate error")
	require.Len(t, resp.Msg.Evaluations, 2, "evaluations count")
}

func TestService_BulkEvaluate_AllFlags(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(
		globalFlag("f/1", boolVal(true)),
		globalFlag("f/2", strVal("x")),
		globalFlag("f/3", int64Val(5)),
	)
	fetcher := &stubFetcher{}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default())
	tracker := NewHealthTracker()
	svc := NewService(eval, reg, tracker, cache, nil)

	resp, err := svc.BulkEvaluate(context.Background(), connect.NewRequest(&pbflagsv1.BulkEvaluateRequest{}))
	require.NoError(t, err, "BulkEvaluate error")
	require.Len(t, resp.Msg.Evaluations, 3, "evaluations count (all flags)")
}

func TestService_BulkEvaluate_WithEntityId(t *testing.T) {
	cache := newTestCache(t)
	reg := registryWith(
		userFlag("f/1", strVal("default-1")),
		userFlag("f/2", strVal("default-2")),
	)
	fetcher := &stubFetcher{
		overrides: []*CachedOverride{
			{FlagID: "f/1", EntityID: "user-42", State: pbflagsv1.State_STATE_ENABLED, Value: strVal("override-1")},
		},
	}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default())
	tracker := NewHealthTracker()
	svc := NewService(eval, reg, tracker, cache, nil)

	resp, err := svc.BulkEvaluate(context.Background(), connect.NewRequest(&pbflagsv1.BulkEvaluateRequest{
		FlagIds:  []string{"f/1", "f/2"},
		EntityId: "user-42",
	}))
	require.NoError(t, err, "BulkEvaluate error")
	require.Len(t, resp.Msg.Evaluations, 2, "evaluations count")

	var f1, f2 *pbflagsv1.EvaluateResponse
	for _, e := range resp.Msg.Evaluations {
		switch e.FlagId {
		case "f/1":
			f1 = e
		case "f/2":
			f2 = e
		}
	}
	require.NotNil(t, f1, "expected f/1 in evaluations")
	require.NotNil(t, f2, "expected f/2 in evaluations")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE, f1.Source, "f/1 source")
	require.Equal(t, "override-1", f1.Value.GetStringValue(), "f/1 value")
	require.Equal(t, pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT, f2.Source, "f/2 source (no override for f/2)")
}

func TestService_Health(t *testing.T) {
	cache := newTestCache(t)
	tracker := NewHealthTracker()
	reg := newTestRegistry()
	fetcher := &stubFetcher{}
	eval := NewEvaluator(reg, cache, fetcher, slog.Default())
	svc := NewService(eval, reg, tracker, cache, nil)

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
