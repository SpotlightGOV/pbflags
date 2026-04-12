package evaluator

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
)

// unmarshalEvalContext deserializes the evaluation context from an Any proto.
// Returns nil (not an error) if there is no context or no condition evaluator.
func (s *Service) unmarshalEvalContext(anyCtx *anypb.Any) (proto.Message, error) {
	ce := s.evaluator.condEval
	if ce == nil || anyCtx == nil {
		return nil, nil
	}
	return ce.UnmarshalContext(anyCtx)
}

// StateServer serves flag state RPCs. In root mode this reads from DB
// (via DBFetcher), in proxy mode it delegates to an upstream evaluator.
type StateServer interface {
	GetFlagStateProto(ctx context.Context, flagID string) (*pbflagsv1.GetFlagStateResponse, error)
	GetKilledFlagsProto(ctx context.Context) (*pbflagsv1.GetKilledFlagsResponse, error)
	GetOverridesProto(ctx context.Context, entityID string, flagIDs []string) (*pbflagsv1.GetOverridesResponse, error)
}

// Service implements the FlagEvaluatorService Connect handler.
type Service struct {
	evaluator *Evaluator
	tracker   *HealthTracker
	cache     *CacheStore
	state     StateServer
}

// NewService creates the evaluator Connect service.
func NewService(eval *Evaluator, tracker *HealthTracker, cache *CacheStore, state StateServer) *Service {
	return &Service{
		evaluator: eval,
		tracker:   tracker,
		cache:     cache,
		state:     state,
	}
}

// Evaluate resolves a single flag value.
func (s *Service) Evaluate(ctx context.Context, req *connect.Request[pbflagsv1.EvaluateRequest]) (*connect.Response[pbflagsv1.EvaluateResponse], error) {
	evalCtx, err := s.unmarshalEvalContext(req.Msg.Context)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	value, source := s.evaluator.EvaluateWithContext(ctx, req.Msg.FlagId, evalCtx)

	return connect.NewResponse(&pbflagsv1.EvaluateResponse{
		FlagId: req.Msg.FlagId,
		Value:  value,
		Source: source,
	}), nil
}

// BulkEvaluate resolves multiple flags at once.
func (s *Service) BulkEvaluate(ctx context.Context, req *connect.Request[pbflagsv1.BulkEvaluateRequest]) (*connect.Response[pbflagsv1.BulkEvaluateResponse], error) {
	flagIDs := req.Msg.FlagIds
	if len(flagIDs) == 0 {
		return connect.NewResponse(&pbflagsv1.BulkEvaluateResponse{}), nil
	}

	evalCtx, err := s.unmarshalEvalContext(req.Msg.Context)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	evaluations := make([]*pbflagsv1.EvaluateResponse, 0, len(flagIDs))
	for _, flagID := range flagIDs {
		value, source := s.evaluator.EvaluateWithContext(ctx, flagID, evalCtx)
		evaluations = append(evaluations, &pbflagsv1.EvaluateResponse{
			FlagId: flagID,
			Value:  value,
			Source: source,
		})
	}

	return connect.NewResponse(&pbflagsv1.BulkEvaluateResponse{
		Evaluations: evaluations,
	}), nil
}

// Health returns the evaluator's current health and degradation status.
func (s *Service) Health(_ context.Context, _ *connect.Request[pbflagsv1.HealthRequest]) (*connect.Response[pbflagsv1.HealthResponse], error) {
	return connect.NewResponse(&pbflagsv1.HealthResponse{
		Status:                    s.tracker.Status(),
		SecondsSinceServerContact: s.tracker.SecondsSinceContact(),
		CachedFlagCount:           s.cache.CachedFlagCount(),
		ConsecutiveFailures:       s.tracker.ConsecutiveFailures(),
	}), nil
}

// GetFlagState fetches state for a single flag via the StateServer.
func (s *Service) GetFlagState(ctx context.Context, req *connect.Request[pbflagsv1.GetFlagStateRequest]) (*connect.Response[pbflagsv1.GetFlagStateResponse], error) {
	flagID := req.Msg.FlagId
	if flagID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	resp, err := s.state.GetFlagStateProto(ctx, flagID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if resp == nil {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	return connect.NewResponse(resp), nil
}

// GetKilledFlags fetches the current kill set via the StateServer.
func (s *Service) GetKilledFlags(ctx context.Context, _ *connect.Request[pbflagsv1.GetKilledFlagsRequest]) (*connect.Response[pbflagsv1.GetKilledFlagsResponse], error) {
	resp, err := s.state.GetKilledFlagsProto(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(resp), nil
}

// GetOverrides fetches overrides for an entity via the StateServer.
func (s *Service) GetOverrides(ctx context.Context, req *connect.Request[pbflagsv1.GetOverridesRequest]) (*connect.Response[pbflagsv1.GetOverridesResponse], error) {
	entityID := req.Msg.EntityId
	if entityID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	resp, err := s.state.GetOverridesProto(ctx, entityID, req.Msg.FlagIds)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(resp), nil
}
