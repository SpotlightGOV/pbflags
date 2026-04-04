package evaluator

import (
	"sync"
	"time"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// HealthTracker maintains health state and computes backoff intervals.
type HealthTracker struct {
	mu                  sync.Mutex
	status              pbflagsv1.EvaluatorStatus
	consecutiveFailures int32
	lastSuccessTime     time.Time
	metrics             *Metrics
}

// NewHealthTracker creates a tracker starting in CONNECTING state.
func NewHealthTracker(m *Metrics) *HealthTracker {
	return &HealthTracker{
		status:  pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_CONNECTING,
		metrics: m,
	}
}

// RecordSuccess resets failure count and transitions to SERVING.
func (t *HealthTracker) RecordSuccess() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.consecutiveFailures = 0
	t.lastSuccessTime = time.Now()
	t.status = pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING
	t.metrics.ConsecutiveFails.Set(0)
}

// RecordFailure increments the failure counter and may transition to DEGRADED.
func (t *HealthTracker) RecordFailure() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.consecutiveFailures++
	t.metrics.ConsecutiveFails.Set(float64(t.consecutiveFailures))
	if t.consecutiveFailures >= 3 {
		t.status = pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_DEGRADED
	}
}

// Status returns the current evaluator status.
func (t *HealthTracker) Status() pbflagsv1.EvaluatorStatus {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status
}

// SecondsSinceContact returns seconds since last successful server contact.
func (t *HealthTracker) SecondsSinceContact() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastSuccessTime.IsZero() {
		return 0
	}
	return int64(time.Since(t.lastSuccessTime).Seconds())
}

// ConsecutiveFailures returns the current failure count.
func (t *HealthTracker) ConsecutiveFailures() int32 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.consecutiveFailures
}

// BackoffMultiplier returns the current backoff multiplier.
//
//	0 failures → 1x, 1-2 → 2x, 3-5 → 4x, 6+ → 8x (capped)
func (t *HealthTracker) BackoffMultiplier() int {
	t.mu.Lock()
	f := t.consecutiveFailures
	t.mu.Unlock()

	switch {
	case f == 0:
		return 1
	case f <= 2:
		return 2
	case f <= 5:
		return 4
	default:
		return 8
	}
}
