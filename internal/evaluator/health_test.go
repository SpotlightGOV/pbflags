package evaluator

import (
	"testing"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthTracker_InitialState(t *testing.T) {
	ht := NewHealthTracker(NewNoopMetrics())
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_CONNECTING, ht.Status(), "initial status")
	require.Equal(t, int32(0), ht.ConsecutiveFailures(), "initial failures")
	require.Equal(t, int64(0), ht.SecondsSinceContact(), "initial seconds since contact")
	require.Equal(t, 1, ht.BackoffMultiplier(), "initial backoff")
}

func TestHealthTracker_ConnectingToServing(t *testing.T) {
	ht := NewHealthTracker(NewNoopMetrics())
	ht.RecordSuccess()
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING, ht.Status(), "status after success")
	require.Equal(t, int32(0), ht.ConsecutiveFailures(), "failures after success")
}

func TestHealthTracker_ServingToDegraded(t *testing.T) {
	ht := NewHealthTracker(NewNoopMetrics())
	ht.RecordSuccess()

	ht.RecordFailure()
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING, ht.Status(), "status after 1 failure")
	ht.RecordFailure()
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING, ht.Status(), "status after 2 failures")

	ht.RecordFailure()
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_DEGRADED, ht.Status(), "status after 3 failures")
	require.Equal(t, int32(3), ht.ConsecutiveFailures(), "failures")
}

func TestHealthTracker_DegradedToServing(t *testing.T) {
	ht := NewHealthTracker(NewNoopMetrics())
	ht.RecordSuccess()
	for i := 0; i < 5; i++ {
		ht.RecordFailure()
	}
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_DEGRADED, ht.Status(), "expected DEGRADED")

	ht.RecordSuccess()
	require.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING, ht.Status(), "status after recovery")
	require.Equal(t, int32(0), ht.ConsecutiveFailures(), "failures after recovery")
}

func TestHealthTracker_BackoffMultiplier(t *testing.T) {
	tests := []struct {
		failures int
		want     int
	}{
		{0, 1}, {1, 2}, {2, 2}, {3, 4}, {4, 4}, {5, 4}, {6, 8}, {10, 8}, {100, 8},
	}

	for _, tt := range tests {
		ht := NewHealthTracker(NewNoopMetrics())
		for i := 0; i < tt.failures; i++ {
			ht.RecordFailure()
		}
		assert.Equal(t, tt.want, ht.BackoffMultiplier(), "BackoffMultiplier() with %d failures", tt.failures)
	}
}

func TestHealthTracker_BackoffResetsOnSuccess(t *testing.T) {
	ht := NewHealthTracker(NewNoopMetrics())
	for i := 0; i < 10; i++ {
		ht.RecordFailure()
	}
	require.Equal(t, 8, ht.BackoffMultiplier(), "backoff at 10 failures")

	ht.RecordSuccess()
	require.Equal(t, 1, ht.BackoffMultiplier(), "backoff after recovery")
}

func TestHealthTracker_SecondsSinceContact(t *testing.T) {
	ht := NewHealthTracker(NewNoopMetrics())
	require.Equal(t, int64(0), ht.SecondsSinceContact(), "seconds before contact")

	ht.RecordSuccess()
	require.LessOrEqual(t, ht.SecondsSinceContact(), int64(1), "seconds immediately after success")
}
