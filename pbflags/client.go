package pbflags

import (
	"context"
	"net/http"

	"connectrpc.com/connect"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

// Connect creates an Evaluator backed by a FlagEvaluatorService at the given URL.
// contextMsg is a zero-value instance of the customer's EvaluationContext proto
// (e.g., &examplepb.EvaluationContext{}). It is used as a prototype for creating
// new context messages during evaluation.
func Connect(httpClient *http.Client, url string, contextMsg proto.Message) Evaluator {
	client := pbflagsv1connect.NewFlagEvaluatorServiceClient(httpClient, url)
	return &clientEvaluator{
		client:     client,
		contextMsg: contextMsg,
	}
}

// clientEvaluator implements Evaluator by wrapping a FlagEvaluatorServiceClient.
type clientEvaluator struct {
	client     pbflagsv1connect.FlagEvaluatorServiceClient
	contextMsg proto.Message // prototype for creating new context messages
	dims       []Dimension   // accumulated dimensions
}

func (e *clientEvaluator) With(dims ...Dimension) Evaluator {
	combined := make([]Dimension, len(e.dims)+len(dims))
	copy(combined, e.dims)
	copy(combined[len(e.dims):], dims)
	return &clientEvaluator{
		client:     e.client,
		contextMsg: e.contextMsg,
		dims:       combined,
	}
}

func (e *clientEvaluator) Evaluate(ctx context.Context, flagID string) (*Result, error) {
	anyCtx, err := e.buildContext()
	if err != nil {
		return nil, err
	}
	resp, err := e.client.Evaluate(ctx, connect.NewRequest(&pbflagsv1.EvaluateRequest{
		FlagId:  flagID,
		Context: anyCtx,
	}))
	if err != nil {
		return nil, err
	}
	return &Result{
		Value:  resp.Msg.GetValue(),
		Source: resp.Msg.GetSource(),
	}, nil
}

func (e *clientEvaluator) BulkEvaluate(ctx context.Context, flagIDs []string) ([]*Result, error) {
	anyCtx, err := e.buildContext()
	if err != nil {
		return nil, err
	}
	resp, err := e.client.BulkEvaluate(ctx, connect.NewRequest(&pbflagsv1.BulkEvaluateRequest{
		FlagIds: flagIDs,
		Context: anyCtx,
	}))
	if err != nil {
		return nil, err
	}
	results := make([]*Result, len(resp.Msg.GetEvaluations()))
	for i, eval := range resp.Msg.GetEvaluations() {
		results[i] = &Result{
			Value:  eval.GetValue(),
			Source: eval.GetSource(),
		}
	}
	return results, nil
}

// buildContext creates a new EvaluationContext proto from accumulated dimensions,
// applies all dimensions, and wraps it in Any.
func (e *clientEvaluator) buildContext() (*anypb.Any, error) {
	if len(e.dims) == 0 {
		return nil, nil
	}
	msg := e.contextMsg.ProtoReflect().New()
	for _, d := range e.dims {
		d.Apply(msg)
	}
	return anypb.New(msg.Interface())
}
