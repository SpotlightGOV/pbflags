// Package pbflagstest provides test helpers for pbflags consumers.
// It implements the FlagEvaluatorServiceHandler interface backed by
// an in-memory override map, removing the need for consumers to
// hand-roll Connect-RPC stubs or construct raw JSON.
package pbflagstest

import (
	"context"
	"sync"

	"connectrpc.com/connect"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
)

// InMemoryEvaluator implements [pbflagsv1connect.FlagEvaluatorServiceHandler]
// backed by an in-memory override map. Flags without overrides return an
// empty response, causing generated clients to fall back to compiled defaults.
//
// It is safe for concurrent use.
type InMemoryEvaluator struct {
	pbflagsv1connect.UnimplementedFlagEvaluatorServiceHandler

	mu       sync.RWMutex
	globals  map[string]*pbflagsv1.FlagValue            // flagID → value
	entities map[string]map[string]*pbflagsv1.FlagValue // flagID → entityID → value
	status   pbflagsv1.EvaluatorStatus
}

// NewInMemoryEvaluator returns a new InMemoryEvaluator with no overrides
// and Health returning EVALUATOR_STATUS_SERVING.
func NewInMemoryEvaluator() *InMemoryEvaluator {
	return &InMemoryEvaluator{
		globals:  make(map[string]*pbflagsv1.FlagValue),
		entities: make(map[string]map[string]*pbflagsv1.FlagValue),
		status:   pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING,
	}
}

// Set registers a global override for a flag.
func (e *InMemoryEvaluator) Set(flagID string, value *pbflagsv1.FlagValue) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.globals[flagID] = value
}

// SetForEntity registers an entity-scoped override.
func (e *InMemoryEvaluator) SetForEntity(flagID, entityID string, value *pbflagsv1.FlagValue) {
	e.mu.Lock()
	defer e.mu.Unlock()
	m, ok := e.entities[flagID]
	if !ok {
		m = make(map[string]*pbflagsv1.FlagValue)
		e.entities[flagID] = m
	}
	m[entityID] = value
}

// SetStatus overrides the status returned by the Health RPC.
func (e *InMemoryEvaluator) SetStatus(status pbflagsv1.EvaluatorStatus) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status = status
}

// Reset clears all overrides and restores status to SERVING.
func (e *InMemoryEvaluator) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.globals = make(map[string]*pbflagsv1.FlagValue)
	e.entities = make(map[string]map[string]*pbflagsv1.FlagValue)
	e.status = pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING
}

// evaluate resolves a single flag. Must be called with at least a read lock held.
func (e *InMemoryEvaluator) evaluate(flagID, entityID string) *pbflagsv1.EvaluateResponse {
	// Entity-scoped override takes precedence.
	if entityID != "" {
		if byEntity, ok := e.entities[flagID]; ok {
			if val, ok := byEntity[entityID]; ok {
				return &pbflagsv1.EvaluateResponse{
					FlagId: flagID,
					Value:  val,
					Source: pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE,
				}
			}
		}
	}
	// Fall back to global override.
	if val, ok := e.globals[flagID]; ok {
		return &pbflagsv1.EvaluateResponse{
			FlagId: flagID,
			Value:  val,
			Source: pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL,
		}
	}
	// No override → empty response (client uses compiled default).
	return &pbflagsv1.EvaluateResponse{FlagId: flagID}
}

// Evaluate implements FlagEvaluatorServiceHandler.
func (e *InMemoryEvaluator) Evaluate(_ context.Context, req *connect.Request[pbflagsv1.EvaluateRequest]) (*connect.Response[pbflagsv1.EvaluateResponse], error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return connect.NewResponse(e.evaluate(req.Msg.GetFlagId(), "")), nil
}

// BulkEvaluate implements FlagEvaluatorServiceHandler.
func (e *InMemoryEvaluator) BulkEvaluate(_ context.Context, req *connect.Request[pbflagsv1.BulkEvaluateRequest]) (*connect.Response[pbflagsv1.BulkEvaluateResponse], error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	flagIDs := req.Msg.GetFlagIds()
	if len(flagIDs) == 0 {
		// Empty = evaluate all known flags.
		seen := make(map[string]struct{})
		for id := range e.globals {
			seen[id] = struct{}{}
			flagIDs = append(flagIDs, id)
		}
		for id := range e.entities {
			if _, ok := seen[id]; !ok {
				flagIDs = append(flagIDs, id)
			}
		}
	}

	evals := make([]*pbflagsv1.EvaluateResponse, len(flagIDs))
	for i, id := range flagIDs {
		evals[i] = e.evaluate(id, "")
	}
	return connect.NewResponse(&pbflagsv1.BulkEvaluateResponse{Evaluations: evals}), nil
}

// Health implements FlagEvaluatorServiceHandler.
func (e *InMemoryEvaluator) Health(_ context.Context, _ *connect.Request[pbflagsv1.HealthRequest]) (*connect.Response[pbflagsv1.HealthResponse], error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	count := int32(len(e.globals))
	return connect.NewResponse(&pbflagsv1.HealthResponse{
		Status:          e.status,
		CachedFlagCount: count,
	}), nil
}

// --- Type-safe value constructors ---

// Bool wraps a bool in a [*pbflagsv1.FlagValue].
func Bool(v bool) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: v}}
}

// String wraps a string in a [*pbflagsv1.FlagValue].
func String(v string) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: v}}
}

// Int64 wraps an int64 in a [*pbflagsv1.FlagValue].
func Int64(v int64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: v}}
}

// Double wraps a float64 in a [*pbflagsv1.FlagValue].
func Double(v float64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: v}}
}

// BoolList wraps a []bool in a [*pbflagsv1.FlagValue].
func BoolList(v ...bool) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolListValue{BoolListValue: &pbflagsv1.BoolList{Values: v}}}
}

// StringList wraps a []string in a [*pbflagsv1.FlagValue].
func StringList(v ...string) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{StringListValue: &pbflagsv1.StringList{Values: v}}}
}

// Int64List wraps a []int64 in a [*pbflagsv1.FlagValue].
func Int64List(v ...int64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{Int64ListValue: &pbflagsv1.Int64List{Values: v}}}
}

// DoubleList wraps a []float64 in a [*pbflagsv1.FlagValue].
func DoubleList(v ...float64) *pbflagsv1.FlagValue {
	return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{DoubleListValue: &pbflagsv1.DoubleList{Values: v}}}
}
