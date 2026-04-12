package pbflags_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	examplepb "github.com/SpotlightGOV/pbflags/gen/example"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
	"github.com/SpotlightGOV/pbflags/pbflags"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/anypb"
)

// captureHandler implements FlagEvaluatorServiceHandler, captures requests,
// and returns configurable responses.
type captureHandler struct {
	pbflagsv1connect.UnimplementedFlagEvaluatorServiceHandler
	lastContext *anypb.Any
	response    *pbflagsv1.EvaluateResponse
	bulkResp    *pbflagsv1.BulkEvaluateResponse
}

func (h *captureHandler) Evaluate(_ context.Context, req *connect.Request[pbflagsv1.EvaluateRequest]) (*connect.Response[pbflagsv1.EvaluateResponse], error) {
	h.lastContext = req.Msg.GetContext()
	resp := h.response
	if resp == nil {
		resp = &pbflagsv1.EvaluateResponse{
			FlagId: req.Msg.FlagId,
			Source: pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT,
		}
	}
	return connect.NewResponse(resp), nil
}

func (h *captureHandler) BulkEvaluate(_ context.Context, req *connect.Request[pbflagsv1.BulkEvaluateRequest]) (*connect.Response[pbflagsv1.BulkEvaluateResponse], error) {
	h.lastContext = req.Msg.GetContext()
	if h.bulkResp != nil {
		return connect.NewResponse(h.bulkResp), nil
	}
	evals := make([]*pbflagsv1.EvaluateResponse, len(req.Msg.FlagIds))
	for i, id := range req.Msg.FlagIds {
		evals[i] = &pbflagsv1.EvaluateResponse{
			FlagId: id,
			Source: pbflagsv1.EvaluationSource_EVALUATION_SOURCE_DEFAULT,
		}
	}
	return connect.NewResponse(&pbflagsv1.BulkEvaluateResponse{Evaluations: evals}), nil
}

func setupTestServer(t *testing.T, handler *captureHandler) (pbflags.Evaluator, *captureHandler) {
	t.Helper()
	_, h := pbflagsv1connect.NewFlagEvaluatorServiceHandler(handler)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	eval := pbflags.Connect(http.DefaultClient, srv.URL, &examplepb.EvaluationContext{})
	return eval, handler
}

func TestConnect_Evaluate(t *testing.T) {
	t.Parallel()
	handler := &captureHandler{
		response: &pbflagsv1.EvaluateResponse{
			FlagId: "test/1",
			Value:  &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
			Source: pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL,
		},
	}
	eval, _ := setupTestServer(t, handler)

	result, err := eval.Evaluate(context.Background(), "test/1")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Value.GetBoolValue() {
		t.Error("expected true")
	}
	if result.Source != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL {
		t.Errorf("expected GLOBAL, got %v", result.Source)
	}
}

func TestConnect_EvaluateWithDimensions(t *testing.T) {
	t.Parallel()
	handler := &captureHandler{}
	eval, h := setupTestServer(t, handler)

	scoped := eval.With(
		pbflags.StringDimension("user_id", "user-123"),
		pbflags.EnumDimension("plan", protoreflect.EnumNumber(examplepb.PlanLevel_PLAN_LEVEL_PRO)),
	)
	_, err := scoped.Evaluate(context.Background(), "test/1")
	if err != nil {
		t.Fatal(err)
	}

	// Verify the context was sent on the wire.
	if h.lastContext == nil {
		t.Fatal("expected context in request")
	}

	// Deserialize and verify dimension values.
	ctx := &examplepb.EvaluationContext{}
	if err := anypb.UnmarshalTo(h.lastContext, ctx, proto.UnmarshalOptions{}); err != nil {
		t.Fatalf("unmarshal context: %v", err)
	}
	if ctx.UserId != "user-123" {
		t.Errorf("user_id: got %q, want %q", ctx.UserId, "user-123")
	}
	if ctx.Plan != examplepb.PlanLevel_PLAN_LEVEL_PRO {
		t.Errorf("plan: got %v, want PRO", ctx.Plan)
	}
}

func TestConnect_NoDimensionsNoContext(t *testing.T) {
	t.Parallel()
	handler := &captureHandler{}
	eval, h := setupTestServer(t, handler)

	_, err := eval.Evaluate(context.Background(), "test/1")
	if err != nil {
		t.Fatal(err)
	}

	if h.lastContext != nil {
		t.Error("expected nil context when no dimensions are set")
	}
}

func TestConnect_WithIsImmutable(t *testing.T) {
	t.Parallel()
	handler := &captureHandler{}
	eval, h := setupTestServer(t, handler)

	parent := eval.With(pbflags.StringDimension("user_id", "a"))
	child := parent.With(pbflags.StringDimension("user_id", "b"))

	// Evaluate child — should have user_id=b.
	_, err := child.Evaluate(context.Background(), "test/1")
	if err != nil {
		t.Fatal(err)
	}
	ctx := &examplepb.EvaluationContext{}
	if err := anypb.UnmarshalTo(h.lastContext, ctx, proto.UnmarshalOptions{}); err != nil {
		t.Fatal(err)
	}
	if ctx.UserId != "b" {
		t.Errorf("child: got %q, want %q", ctx.UserId, "b")
	}

	// Evaluate parent — should still have user_id=a.
	_, err = parent.Evaluate(context.Background(), "test/1")
	if err != nil {
		t.Fatal(err)
	}
	ctx = &examplepb.EvaluationContext{}
	if err := anypb.UnmarshalTo(h.lastContext, ctx, proto.UnmarshalOptions{}); err != nil {
		t.Fatal(err)
	}
	if ctx.UserId != "a" {
		t.Errorf("parent: got %q, want %q", ctx.UserId, "a")
	}
}

func TestConnect_BulkEvaluate(t *testing.T) {
	t.Parallel()
	handler := &captureHandler{}
	eval, _ := setupTestServer(t, handler)

	results, err := eval.BulkEvaluate(context.Background(), []string{"a/1", "b/2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}
