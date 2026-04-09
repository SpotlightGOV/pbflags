package evaluator

import (
	"context"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
)

var clientTracer = otel.Tracer("pbflags/client")

// FlagServerClient talks to the upstream evaluator's FlagEvaluatorService via Connect.
type FlagServerClient struct {
	eval    pbflagsv1connect.FlagEvaluatorServiceClient
	tracker *HealthTracker
	timeout time.Duration
	metrics *Metrics
}

// NewFlagServerClient creates a Connect client for the upstream evaluator.
func NewFlagServerClient(serverURL string, tracker *HealthTracker, fetchTimeout time.Duration, m *Metrics, opts ...connect.ClientOption) *FlagServerClient {
	return &FlagServerClient{
		eval:    pbflagsv1connect.NewFlagEvaluatorServiceClient(http.DefaultClient, serverURL, opts...),
		tracker: tracker,
		timeout: fetchTimeout,
		metrics: m,
	}
}

// GetKilledFlags fetches the current kill set from the server.
func (c *FlagServerClient) GetKilledFlags(ctx context.Context) (*KillSet, error) {
	ctx, span := clientTracer.Start(ctx, "FlagServerClient.GetKilledFlags")
	defer span.End()

	timer := prometheus.NewTimer(c.metrics.FetchDuration.WithLabelValues("upstream", "killed_flags"))
	defer timer.ObserveDuration()

	resp, err := c.eval.GetKilledFlags(ctx, connect.NewRequest(&pbflagsv1.GetKilledFlagsRequest{}))
	if err != nil {
		c.tracker.RecordFailure()
		return nil, err
	}
	c.tracker.RecordSuccess()

	ks := &KillSet{
		FlagIDs: make(map[string]struct{}, len(resp.Msg.FlagIds)),
	}
	for _, id := range resp.Msg.FlagIds {
		ks.FlagIDs[id] = struct{}{}
	}
	return ks, nil
}

// FetchFlagState implements Fetcher.
func (c *FlagServerClient) FetchFlagState(ctx context.Context, flagID string) (*CachedFlagState, error) {
	ctx, span := clientTracer.Start(ctx, "FlagServerClient.FetchFlagState",
		trace.WithAttributes(attribute.String("flag_id", flagID)))
	defer span.End()

	timer := prometheus.NewTimer(c.metrics.FetchDuration.WithLabelValues("upstream", "flag_state"))
	defer timer.ObserveDuration()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.eval.GetFlagState(ctx, connect.NewRequest(&pbflagsv1.GetFlagStateRequest{FlagId: flagID}))
	if err != nil {
		c.tracker.RecordFailure()
		return nil, err
	}
	c.tracker.RecordSuccess()

	fs := resp.Msg.Flag
	if fs == nil {
		return nil, nil
	}

	return &CachedFlagState{
		FlagID:   fs.FlagId,
		State:    fs.State,
		Value:    fs.Value,
		Archived: resp.Msg.Archived,
	}, nil
}

// FetchOverrides implements Fetcher.
func (c *FlagServerClient) FetchOverrides(ctx context.Context, entityID string, flagIDs []string) ([]*CachedOverride, error) {
	ctx, span := clientTracer.Start(ctx, "FlagServerClient.FetchOverrides",
		trace.WithAttributes(attribute.String("entity_id", entityID)))
	defer span.End()

	timer := prometheus.NewTimer(c.metrics.FetchDuration.WithLabelValues("upstream", "overrides"))
	defer timer.ObserveDuration()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	resp, err := c.eval.GetOverrides(ctx, connect.NewRequest(&pbflagsv1.GetOverridesRequest{
		EntityId: entityID,
		FlagIds:  flagIDs,
	}))
	if err != nil {
		c.tracker.RecordFailure()
		return nil, err
	}
	c.tracker.RecordSuccess()

	result := make([]*CachedOverride, 0, len(resp.Msg.Overrides))
	for _, o := range resp.Msg.Overrides {
		result = append(result, &CachedOverride{
			FlagID:   o.FlagId,
			EntityID: o.EntityId,
			State:    o.State,
			Value:    o.Value,
		})
	}
	return result, nil
}

// StateServer returns a StateServer that delegates to the upstream evaluator.
func (c *FlagServerClient) StateServer() StateServer {
	return &proxyStateServer{client: c.eval}
}

type proxyStateServer struct {
	client pbflagsv1connect.FlagEvaluatorServiceClient
}

func (p *proxyStateServer) GetFlagStateProto(ctx context.Context, flagID string) (*pbflagsv1.GetFlagStateResponse, error) {
	resp, err := p.client.GetFlagState(ctx, connect.NewRequest(&pbflagsv1.GetFlagStateRequest{FlagId: flagID}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (p *proxyStateServer) GetKilledFlagsProto(ctx context.Context) (*pbflagsv1.GetKilledFlagsResponse, error) {
	resp, err := p.client.GetKilledFlags(ctx, connect.NewRequest(&pbflagsv1.GetKilledFlagsRequest{}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (p *proxyStateServer) GetOverridesProto(ctx context.Context, entityID string, flagIDs []string) (*pbflagsv1.GetOverridesResponse, error) {
	resp, err := p.client.GetOverrides(ctx, connect.NewRequest(&pbflagsv1.GetOverridesRequest{
		EntityId: entityID,
		FlagIds:  flagIDs,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}
