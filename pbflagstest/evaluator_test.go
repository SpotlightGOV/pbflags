package pbflagstest_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
	"github.com/SpotlightGOV/pbflags/pbflagstest"
)

func TestEvaluate_NoOverride(t *testing.T) {
	eval := pbflagstest.NewInMemoryEvaluator()
	resp, err := eval.Evaluate(context.Background(), connect.NewRequest(&pbflagsv1.EvaluateRequest{
		FlagId: "feature/flag1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetValue() != nil {
		t.Fatalf("expected nil value, got %v", resp.Msg.GetValue())
	}
	if resp.Msg.GetSource() != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_UNSPECIFIED {
		t.Fatalf("expected UNSPECIFIED source, got %v", resp.Msg.GetSource())
	}
}

func TestEvaluate_GlobalOverride(t *testing.T) {
	eval := pbflagstest.NewInMemoryEvaluator()
	eval.Set("feature/flag1", pbflagstest.Bool(true))

	resp, err := eval.Evaluate(context.Background(), connect.NewRequest(&pbflagsv1.EvaluateRequest{
		FlagId: "feature/flag1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Msg.GetValue().GetBoolValue() {
		t.Fatal("expected true")
	}
	if resp.Msg.GetSource() != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL {
		t.Fatalf("expected GLOBAL source, got %v", resp.Msg.GetSource())
	}
}

func TestEvaluate_EntityOverride(t *testing.T) {
	eval := pbflagstest.NewInMemoryEvaluator()
	eval.Set("feature/flag1", pbflagstest.Bool(false))
	eval.SetForEntity("feature/flag1", "user-42", pbflagstest.Bool(true))

	// Entity override wins.
	resp, err := eval.Evaluate(context.Background(), connect.NewRequest(&pbflagsv1.EvaluateRequest{
		FlagId:   "feature/flag1",
		EntityId: "user-42",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Msg.GetValue().GetBoolValue() {
		t.Fatal("expected entity override true")
	}
	if resp.Msg.GetSource() != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_OVERRIDE {
		t.Fatalf("expected OVERRIDE source, got %v", resp.Msg.GetSource())
	}

	// Different entity falls back to global.
	resp, err = eval.Evaluate(context.Background(), connect.NewRequest(&pbflagsv1.EvaluateRequest{
		FlagId:   "feature/flag1",
		EntityId: "user-99",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetValue().GetBoolValue() {
		t.Fatal("expected global override false")
	}
	if resp.Msg.GetSource() != pbflagsv1.EvaluationSource_EVALUATION_SOURCE_GLOBAL {
		t.Fatalf("expected GLOBAL source, got %v", resp.Msg.GetSource())
	}
}

func TestEvaluate_AllValueTypes(t *testing.T) {
	eval := pbflagstest.NewInMemoryEvaluator()
	eval.Set("f/bool", pbflagstest.Bool(true))
	eval.Set("f/string", pbflagstest.String("hello"))
	eval.Set("f/int64", pbflagstest.Int64(42))
	eval.Set("f/double", pbflagstest.Double(3.14))
	eval.Set("f/boollist", pbflagstest.BoolList(true, false))
	eval.Set("f/stringlist", pbflagstest.StringList("a", "b"))
	eval.Set("f/int64list", pbflagstest.Int64List(1, 2, 3))
	eval.Set("f/doublelist", pbflagstest.DoubleList(1.1, 2.2))

	cases := []struct {
		flagID string
		check  func(*pbflagsv1.FlagValue)
	}{
		{"f/bool", func(v *pbflagsv1.FlagValue) {
			if !v.GetBoolValue() {
				t.Error("bool: expected true")
			}
		}},
		{"f/string", func(v *pbflagsv1.FlagValue) {
			if v.GetStringValue() != "hello" {
				t.Errorf("string: got %q", v.GetStringValue())
			}
		}},
		{"f/int64", func(v *pbflagsv1.FlagValue) {
			if v.GetInt64Value() != 42 {
				t.Errorf("int64: got %d", v.GetInt64Value())
			}
		}},
		{"f/double", func(v *pbflagsv1.FlagValue) {
			if v.GetDoubleValue() != 3.14 {
				t.Errorf("double: got %f", v.GetDoubleValue())
			}
		}},
		{"f/boollist", func(v *pbflagsv1.FlagValue) {
			vals := v.GetBoolListValue().GetValues()
			if len(vals) != 2 || vals[0] != true || vals[1] != false {
				t.Errorf("boollist: got %v", vals)
			}
		}},
		{"f/stringlist", func(v *pbflagsv1.FlagValue) {
			vals := v.GetStringListValue().GetValues()
			if len(vals) != 2 || vals[0] != "a" || vals[1] != "b" {
				t.Errorf("stringlist: got %v", vals)
			}
		}},
		{"f/int64list", func(v *pbflagsv1.FlagValue) {
			vals := v.GetInt64ListValue().GetValues()
			if len(vals) != 3 || vals[0] != 1 || vals[1] != 2 || vals[2] != 3 {
				t.Errorf("int64list: got %v", vals)
			}
		}},
		{"f/doublelist", func(v *pbflagsv1.FlagValue) {
			vals := v.GetDoubleListValue().GetValues()
			if len(vals) != 2 || vals[0] != 1.1 || vals[1] != 2.2 {
				t.Errorf("doublelist: got %v", vals)
			}
		}},
	}

	for _, tc := range cases {
		resp, err := eval.Evaluate(context.Background(), connect.NewRequest(&pbflagsv1.EvaluateRequest{
			FlagId: tc.flagID,
		}))
		if err != nil {
			t.Fatalf("%s: %v", tc.flagID, err)
		}
		tc.check(resp.Msg.GetValue())
	}
}

func TestBulkEvaluate(t *testing.T) {
	eval := pbflagstest.NewInMemoryEvaluator()
	eval.Set("f/a", pbflagstest.Bool(true))
	eval.Set("f/b", pbflagstest.String("yes"))

	resp, err := eval.BulkEvaluate(context.Background(), connect.NewRequest(&pbflagsv1.BulkEvaluateRequest{
		FlagIds: []string{"f/a", "f/b", "f/unknown"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	evals := resp.Msg.GetEvaluations()
	if len(evals) != 3 {
		t.Fatalf("expected 3 evaluations, got %d", len(evals))
	}
	if !evals[0].GetValue().GetBoolValue() {
		t.Error("f/a: expected true")
	}
	if evals[1].GetValue().GetStringValue() != "yes" {
		t.Errorf("f/b: got %q", evals[1].GetValue().GetStringValue())
	}
	if evals[2].GetValue() != nil {
		t.Error("f/unknown: expected nil value")
	}
}

func TestBulkEvaluate_EmptyIDs(t *testing.T) {
	eval := pbflagstest.NewInMemoryEvaluator()
	eval.Set("f/a", pbflagstest.Bool(true))
	eval.Set("f/b", pbflagstest.Int64(7))

	resp, err := eval.BulkEvaluate(context.Background(), connect.NewRequest(&pbflagsv1.BulkEvaluateRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(resp.Msg.GetEvaluations()); got != 2 {
		t.Fatalf("expected 2 evaluations for all known flags, got %d", got)
	}
}

func TestHealth(t *testing.T) {
	eval := pbflagstest.NewInMemoryEvaluator()
	eval.Set("f/a", pbflagstest.Bool(true))

	resp, err := eval.Health(context.Background(), connect.NewRequest(&pbflagsv1.HealthRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetStatus() != pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING {
		t.Fatalf("expected SERVING, got %v", resp.Msg.GetStatus())
	}
	if resp.Msg.GetCachedFlagCount() != 1 {
		t.Fatalf("expected 1 cached flag, got %d", resp.Msg.GetCachedFlagCount())
	}
}

func TestSetStatus(t *testing.T) {
	eval := pbflagstest.NewInMemoryEvaluator()
	eval.SetStatus(pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_DEGRADED)

	resp, err := eval.Health(context.Background(), connect.NewRequest(&pbflagsv1.HealthRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetStatus() != pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_DEGRADED {
		t.Fatalf("expected DEGRADED, got %v", resp.Msg.GetStatus())
	}
}

func TestReset(t *testing.T) {
	eval := pbflagstest.NewInMemoryEvaluator()
	eval.Set("f/a", pbflagstest.Bool(true))
	eval.SetForEntity("f/a", "u1", pbflagstest.Bool(false))
	eval.SetStatus(pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_DEGRADED)

	eval.Reset()

	resp, err := eval.Evaluate(context.Background(), connect.NewRequest(&pbflagsv1.EvaluateRequest{
		FlagId: "f/a",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.GetValue() != nil {
		t.Fatal("expected nil after reset")
	}

	health, err := eval.Health(context.Background(), connect.NewRequest(&pbflagsv1.HealthRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if health.Msg.GetStatus() != pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING {
		t.Fatal("expected SERVING after reset")
	}
}

// TestConnectHandler verifies the evaluator works end-to-end through
// a real Connect HTTP handler, which is the primary consumer use case.
func TestConnectHandler(t *testing.T) {
	eval := pbflagstest.NewInMemoryEvaluator()
	eval.Set("notifications/6", pbflagstest.Bool(true))

	_, handler := pbflagsv1connect.NewFlagEvaluatorServiceHandler(eval)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := pbflagsv1connect.NewFlagEvaluatorServiceClient(http.DefaultClient, srv.URL)

	resp, err := client.Evaluate(context.Background(), connect.NewRequest(&pbflagsv1.EvaluateRequest{
		FlagId: "notifications/6",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Msg.GetValue().GetBoolValue() {
		t.Fatal("expected true from Connect handler")
	}
}
