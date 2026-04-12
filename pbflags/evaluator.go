package pbflags

import (
	"context"
	"reflect"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// Result holds the outcome of a single flag evaluation.
type Result struct {
	Value  *pbflagsv1.FlagValue
	Source pbflagsv1.EvaluationSource
}

// Evaluator provides flag evaluation with bound context dimensions.
// Evaluators are immutable — With() returns a new Evaluator, it does
// not modify the receiver.
type Evaluator interface {
	// With returns a new Evaluator with additional context dimensions bound.
	// Dimensions from the parent are preserved; new dimensions override
	// any existing dimension with the same name.
	With(dims ...Dimension) Evaluator

	// Evaluate resolves a single flag against the bound context.
	// Called by generated client code — not typically called directly.
	Evaluate(ctx context.Context, flagID string) (*Result, error)

	// BulkEvaluate resolves multiple flags against the bound context.
	BulkEvaluate(ctx context.Context, flagIDs []string) ([]*Result, error)
}

type contextKey struct{}

// ContextWith stores an Evaluator in a context.Context.
// Panics if eval is nil.
func ContextWith(ctx context.Context, eval Evaluator) context.Context {
	if isNilEvaluator(eval) {
		panic("pbflags.ContextWith: eval must not be nil")
	}
	return context.WithValue(ctx, contextKey{}, eval)
}

// FromContext retrieves the Evaluator from a context.Context.
// Returns a no-op evaluator (all compiled defaults) if none is set
// or if the stored value is a typed-nil.
func FromContext(ctx context.Context) Evaluator {
	eval, ok := ctx.Value(contextKey{}).(Evaluator)
	if ok && !isNilEvaluator(eval) {
		return eval
	}
	return noopEvaluator{}
}

// isNilEvaluator returns true for interface-nil and typed-nil Evaluator values.
func isNilEvaluator(eval Evaluator) bool {
	if eval == nil {
		return true
	}
	v := reflect.ValueOf(eval)
	return v.Kind() == reflect.Ptr && v.IsNil()
}

// noopEvaluator returns zero-value results for all evaluations.
// Used as the safe default when no evaluator is set in context.
type noopEvaluator struct{}

func (noopEvaluator) With(...Dimension) Evaluator { return noopEvaluator{} }

func (noopEvaluator) Evaluate(_ context.Context, _ string) (*Result, error) {
	return &Result{Source: pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT}, nil
}

func (noopEvaluator) BulkEvaluate(_ context.Context, flagIDs []string) ([]*Result, error) {
	results := make([]*Result, len(flagIDs))
	for i := range results {
		results[i] = &Result{Source: pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT}
	}
	return results, nil
}
