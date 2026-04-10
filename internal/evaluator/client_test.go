package evaluator

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
)

// mockEvalClient implements pbflagsv1connect.FlagEvaluatorServiceClient for testing.
type mockEvalClient struct {
	pbflagsv1connect.UnimplementedFlagEvaluatorServiceHandler

	getFlagStateFn func(context.Context, *connect.Request[pbflagsv1.GetFlagStateRequest]) (*connect.Response[pbflagsv1.GetFlagStateResponse], error)
	getKilledFn    func(context.Context, *connect.Request[pbflagsv1.GetKilledFlagsRequest]) (*connect.Response[pbflagsv1.GetKilledFlagsResponse], error)
	getOverridesFn func(context.Context, *connect.Request[pbflagsv1.GetOverridesRequest]) (*connect.Response[pbflagsv1.GetOverridesResponse], error)
}

func (m *mockEvalClient) GetFlagState(ctx context.Context, req *connect.Request[pbflagsv1.GetFlagStateRequest]) (*connect.Response[pbflagsv1.GetFlagStateResponse], error) {
	if m.getFlagStateFn != nil {
		return m.getFlagStateFn(ctx, req)
	}
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not implemented"))
}

func (m *mockEvalClient) GetKilledFlags(ctx context.Context, req *connect.Request[pbflagsv1.GetKilledFlagsRequest]) (*connect.Response[pbflagsv1.GetKilledFlagsResponse], error) {
	if m.getKilledFn != nil {
		return m.getKilledFn(ctx, req)
	}
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not implemented"))
}

func (m *mockEvalClient) GetOverrides(ctx context.Context, req *connect.Request[pbflagsv1.GetOverridesRequest]) (*connect.Response[pbflagsv1.GetOverridesResponse], error) {
	if m.getOverridesFn != nil {
		return m.getOverridesFn(ctx, req)
	}
	return nil, connect.NewError(connect.CodeUnimplemented, errors.New("not implemented"))
}

func newTestClient(mock *mockEvalClient) *FlagServerClient {
	metrics := NewNoopMetrics()
	tracker := NewHealthTracker(metrics)
	return &FlagServerClient{
		eval:    mock,
		tracker: tracker,
		timeout: 5 * time.Second,
		metrics: metrics,
	}
}

// ---------------------------------------------------------------------------
// GetKilledFlags
// ---------------------------------------------------------------------------

func TestGetKilledFlags_Success(t *testing.T) {
	mock := &mockEvalClient{
		getKilledFn: func(_ context.Context, _ *connect.Request[pbflagsv1.GetKilledFlagsRequest]) (*connect.Response[pbflagsv1.GetKilledFlagsResponse], error) {
			return connect.NewResponse(&pbflagsv1.GetKilledFlagsResponse{
				FlagIds: []string{"feat/1", "feat/2"},
			}), nil
		},
	}
	c := newTestClient(mock)

	ks, err := c.GetKilledFlags(context.Background())
	require.NoError(t, err)

	assert.Len(t, ks.FlagIDs, 2)
	assert.Contains(t, ks.FlagIDs, "feat/1")
	assert.Contains(t, ks.FlagIDs, "feat/2")

	// Should record success.
	assert.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING, c.tracker.Status())
}

func TestGetKilledFlags_Error(t *testing.T) {
	mock := &mockEvalClient{
		getKilledFn: func(_ context.Context, _ *connect.Request[pbflagsv1.GetKilledFlagsRequest]) (*connect.Response[pbflagsv1.GetKilledFlagsResponse], error) {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("server down"))
		},
	}
	c := newTestClient(mock)

	_, err := c.GetKilledFlags(context.Background())
	require.Error(t, err)
	assert.Equal(t, int32(1), c.tracker.ConsecutiveFailures())
}

func TestGetKilledFlags_EmptyResponse(t *testing.T) {
	mock := &mockEvalClient{
		getKilledFn: func(_ context.Context, _ *connect.Request[pbflagsv1.GetKilledFlagsRequest]) (*connect.Response[pbflagsv1.GetKilledFlagsResponse], error) {
			return connect.NewResponse(&pbflagsv1.GetKilledFlagsResponse{}), nil
		},
	}
	c := newTestClient(mock)

	ks, err := c.GetKilledFlags(context.Background())
	require.NoError(t, err)
	assert.Empty(t, ks.FlagIDs)
}

// ---------------------------------------------------------------------------
// FetchFlagState
// ---------------------------------------------------------------------------

func TestFetchFlagState_Success(t *testing.T) {
	mock := &mockEvalClient{
		getFlagStateFn: func(_ context.Context, req *connect.Request[pbflagsv1.GetFlagStateRequest]) (*connect.Response[pbflagsv1.GetFlagStateResponse], error) {
			return connect.NewResponse(&pbflagsv1.GetFlagStateResponse{
				Flag: &pbflagsv1.FlagState{
					FlagId: req.Msg.FlagId,
					State:  pbflagsv1.State_STATE_ENABLED,
					Value: &pbflagsv1.FlagValue{
						Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true},
					},
				},
			}), nil
		},
	}
	c := newTestClient(mock)

	fs, err := c.FetchFlagState(context.Background(), "notifications/1")
	require.NoError(t, err)
	require.NotNil(t, fs)
	assert.Equal(t, "notifications/1", fs.FlagID)
	assert.Equal(t, pbflagsv1.State_STATE_ENABLED, fs.State)
	assert.True(t, fs.Value.GetBoolValue())
}

func TestFetchFlagState_NilFlag(t *testing.T) {
	mock := &mockEvalClient{
		getFlagStateFn: func(_ context.Context, _ *connect.Request[pbflagsv1.GetFlagStateRequest]) (*connect.Response[pbflagsv1.GetFlagStateResponse], error) {
			return connect.NewResponse(&pbflagsv1.GetFlagStateResponse{Flag: nil}), nil
		},
	}
	c := newTestClient(mock)

	fs, err := c.FetchFlagState(context.Background(), "notifications/1")
	require.NoError(t, err)
	assert.Nil(t, fs)
}

func TestFetchFlagState_Error(t *testing.T) {
	mock := &mockEvalClient{
		getFlagStateFn: func(_ context.Context, _ *connect.Request[pbflagsv1.GetFlagStateRequest]) (*connect.Response[pbflagsv1.GetFlagStateResponse], error) {
			return nil, connect.NewError(connect.CodeInternal, errors.New("db error"))
		},
	}
	c := newTestClient(mock)

	_, err := c.FetchFlagState(context.Background(), "notifications/1")
	require.Error(t, err)
	assert.Equal(t, int32(1), c.tracker.ConsecutiveFailures())
}

func TestFetchFlagState_Timeout(t *testing.T) {
	mock := &mockEvalClient{
		getFlagStateFn: func(ctx context.Context, _ *connect.Request[pbflagsv1.GetFlagStateRequest]) (*connect.Response[pbflagsv1.GetFlagStateResponse], error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	c := newTestClient(mock)
	c.timeout = 10 * time.Millisecond

	_, err := c.FetchFlagState(context.Background(), "notifications/1")
	require.Error(t, err)
}

// ---------------------------------------------------------------------------
// FetchOverrides
// ---------------------------------------------------------------------------

func TestFetchOverrides_Success(t *testing.T) {
	mock := &mockEvalClient{
		getOverridesFn: func(_ context.Context, req *connect.Request[pbflagsv1.GetOverridesRequest]) (*connect.Response[pbflagsv1.GetOverridesResponse], error) {
			return connect.NewResponse(&pbflagsv1.GetOverridesResponse{
				Overrides: []*pbflagsv1.OverrideState{
					{
						FlagId:   "feat/1",
						EntityId: req.Msg.EntityId,
						State:    pbflagsv1.State_STATE_ENABLED,
					},
				},
			}), nil
		},
	}
	c := newTestClient(mock)

	overrides, err := c.FetchOverrides(context.Background(), "user-42", []string{"feat/1"})
	require.NoError(t, err)
	require.Len(t, overrides, 1)
	assert.Equal(t, "feat/1", overrides[0].FlagID)
	assert.Equal(t, "user-42", overrides[0].EntityID)
	assert.Equal(t, pbflagsv1.State_STATE_ENABLED, overrides[0].State)
}

func TestFetchOverrides_Error(t *testing.T) {
	mock := &mockEvalClient{
		getOverridesFn: func(_ context.Context, _ *connect.Request[pbflagsv1.GetOverridesRequest]) (*connect.Response[pbflagsv1.GetOverridesResponse], error) {
			return nil, connect.NewError(connect.CodeUnavailable, errors.New("network error"))
		},
	}
	c := newTestClient(mock)

	_, err := c.FetchOverrides(context.Background(), "user-42", []string{"feat/1"})
	require.Error(t, err)
	assert.Equal(t, int32(1), c.tracker.ConsecutiveFailures())
}

func TestFetchOverrides_Empty(t *testing.T) {
	mock := &mockEvalClient{
		getOverridesFn: func(_ context.Context, _ *connect.Request[pbflagsv1.GetOverridesRequest]) (*connect.Response[pbflagsv1.GetOverridesResponse], error) {
			return connect.NewResponse(&pbflagsv1.GetOverridesResponse{}), nil
		},
	}
	c := newTestClient(mock)

	overrides, err := c.FetchOverrides(context.Background(), "user-42", nil)
	require.NoError(t, err)
	assert.Empty(t, overrides)
}

// ---------------------------------------------------------------------------
// Health tracking integration
// ---------------------------------------------------------------------------

func TestClient_HealthTracking(t *testing.T) {
	callCount := 0
	mock := &mockEvalClient{
		getKilledFn: func(_ context.Context, _ *connect.Request[pbflagsv1.GetKilledFlagsRequest]) (*connect.Response[pbflagsv1.GetKilledFlagsResponse], error) {
			callCount++
			if callCount <= 2 {
				return nil, connect.NewError(connect.CodeUnavailable, errors.New("down"))
			}
			return connect.NewResponse(&pbflagsv1.GetKilledFlagsResponse{}), nil
		},
	}
	c := newTestClient(mock)

	// Two failures.
	_, _ = c.GetKilledFlags(context.Background())
	_, _ = c.GetKilledFlags(context.Background())
	assert.Equal(t, int32(2), c.tracker.ConsecutiveFailures())

	// Recovery.
	_, err := c.GetKilledFlags(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(0), c.tracker.ConsecutiveFailures())
	assert.Equal(t, pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING, c.tracker.Status())
}

// ---------------------------------------------------------------------------
// StateServer (proxy mode)
// ---------------------------------------------------------------------------

func TestStateServer_GetFlagStateProto(t *testing.T) {
	mock := &mockEvalClient{
		getFlagStateFn: func(_ context.Context, req *connect.Request[pbflagsv1.GetFlagStateRequest]) (*connect.Response[pbflagsv1.GetFlagStateResponse], error) {
			return connect.NewResponse(&pbflagsv1.GetFlagStateResponse{
				Flag: &pbflagsv1.FlagState{
					FlagId: req.Msg.FlagId,
					State:  pbflagsv1.State_STATE_KILLED,
				},
			}), nil
		},
	}
	c := newTestClient(mock)
	ss := c.StateServer()

	resp, err := ss.GetFlagStateProto(context.Background(), "feat/1")
	require.NoError(t, err)
	require.NotNil(t, resp.Flag)
	assert.Equal(t, pbflagsv1.State_STATE_KILLED, resp.Flag.State)
}

func TestStateServer_GetKilledFlagsProto(t *testing.T) {
	mock := &mockEvalClient{
		getKilledFn: func(_ context.Context, _ *connect.Request[pbflagsv1.GetKilledFlagsRequest]) (*connect.Response[pbflagsv1.GetKilledFlagsResponse], error) {
			return connect.NewResponse(&pbflagsv1.GetKilledFlagsResponse{
				FlagIds: []string{"feat/1"},
			}), nil
		},
	}
	c := newTestClient(mock)
	ss := c.StateServer()

	resp, err := ss.GetKilledFlagsProto(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"feat/1"}, resp.FlagIds)
}

func TestStateServer_GetOverridesProto(t *testing.T) {
	mock := &mockEvalClient{
		getOverridesFn: func(_ context.Context, _ *connect.Request[pbflagsv1.GetOverridesRequest]) (*connect.Response[pbflagsv1.GetOverridesResponse], error) {
			return connect.NewResponse(&pbflagsv1.GetOverridesResponse{
				Overrides: []*pbflagsv1.OverrideState{
					{FlagId: "feat/1", EntityId: "e1", State: pbflagsv1.State_STATE_ENABLED},
				},
			}), nil
		},
	}
	c := newTestClient(mock)
	ss := c.StateServer()

	resp, err := ss.GetOverridesProto(context.Background(), "e1", []string{"feat/1"})
	require.NoError(t, err)
	require.Len(t, resp.Overrides, 1)
	assert.Equal(t, "feat/1", resp.Overrides[0].FlagId)
}

// ---------------------------------------------------------------------------
// NewFlagServerClient (integration-style: real HTTP)
// ---------------------------------------------------------------------------

func TestNewFlagServerClient_Integration(t *testing.T) {
	// Spin up a real Connect server with the mock handler.
	mock := &mockEvalClient{
		getKilledFn: func(_ context.Context, _ *connect.Request[pbflagsv1.GetKilledFlagsRequest]) (*connect.Response[pbflagsv1.GetKilledFlagsResponse], error) {
			return connect.NewResponse(&pbflagsv1.GetKilledFlagsResponse{
				FlagIds: []string{"integration/1"},
			}), nil
		},
	}

	mux := http.NewServeMux()
	path, handler := pbflagsv1connect.NewFlagEvaluatorServiceHandler(mock)
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	metrics := NewNoopMetrics()
	tracker := NewHealthTracker(metrics)
	c := NewFlagServerClient(srv.URL, tracker, 5*time.Second, metrics)

	ks, err := c.GetKilledFlags(context.Background())
	require.NoError(t, err)
	assert.Contains(t, ks.FlagIDs, "integration/1")
}
