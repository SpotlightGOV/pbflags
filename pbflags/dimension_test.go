package pbflags_test

import (
	"testing"

	examplepb "github.com/SpotlightGOV/pbflags/gen/example"
	"github.com/SpotlightGOV/pbflags/pbflags"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestStringDimension(t *testing.T) {
	t.Parallel()
	desc := (&wrapperspb.StringValue{}).ProtoReflect().Descriptor()
	msg := dynamicpb.NewMessage(desc)

	pbflags.StringDimension("value", "hello").Apply(msg.ProtoReflect())

	fd := desc.Fields().ByName("value")
	if got := msg.Get(fd).String(); got != "hello" {
		t.Errorf("StringDimension: got %q, want %q", got, "hello")
	}
}

func TestBoolDimension(t *testing.T) {
	t.Parallel()
	desc := (&wrapperspb.BoolValue{}).ProtoReflect().Descriptor()
	msg := dynamicpb.NewMessage(desc)

	pbflags.BoolDimension("value", true).Apply(msg.ProtoReflect())

	fd := desc.Fields().ByName("value")
	if got := msg.Get(fd).Bool(); got != true {
		t.Errorf("BoolDimension: got %v, want true", got)
	}
}

func TestInt64Dimension(t *testing.T) {
	t.Parallel()
	desc := (&wrapperspb.Int64Value{}).ProtoReflect().Descriptor()
	msg := dynamicpb.NewMessage(desc)

	pbflags.Int64Dimension("value", 42).Apply(msg.ProtoReflect())

	fd := desc.Fields().ByName("value")
	if got := msg.Get(fd).Int(); got != 42 {
		t.Errorf("Int64Dimension: got %d, want 42", got)
	}
}

func TestEnumDimension(t *testing.T) {
	t.Parallel()
	// Use the generated EvaluationContext which has real enum fields
	// (plan: PlanLevel, device_type: DeviceType).
	ctx := &examplepb.EvaluationContext{}
	msg := ctx.ProtoReflect()

	want := examplepb.PlanLevel_PLAN_LEVEL_ENTERPRISE
	pbflags.EnumDimension("plan", protoreflect.EnumNumber(want)).Apply(msg)

	fd := msg.Descriptor().Fields().ByName("plan")
	if got := protoreflect.EnumNumber(msg.Get(fd).Enum()); got != protoreflect.EnumNumber(want) {
		t.Errorf("EnumDimension: got %d, want %d", got, want)
	}
}

func TestDimension_MissingField(t *testing.T) {
	t.Parallel()
	desc := (&wrapperspb.StringValue{}).ProtoReflect().Descriptor()
	msg := dynamicpb.NewMessage(desc)

	// Applying a dimension for a field that doesn't exist should be a no-op.
	dims := []pbflags.Dimension{
		pbflags.StringDimension("nonexistent", "x"),
		pbflags.BoolDimension("nonexistent", true),
		pbflags.Int64Dimension("nonexistent", 1),
		pbflags.EnumDimension("nonexistent", 1),
	}
	for _, d := range dims {
		d.Apply(msg.ProtoReflect()) // must not panic
	}

	// Original field should still be at zero value.
	fd := desc.Fields().ByName("value")
	if got := msg.Get(fd).String(); got != "" {
		t.Errorf("expected empty string after no-op Apply, got %q", got)
	}
}

func TestDimension_KindMismatch(t *testing.T) {
	t.Parallel()
	// Apply dimensions to fields of the wrong kind. Must not panic.
	ctx := &examplepb.EvaluationContext{}
	msg := ctx.ProtoReflect()

	// "user_id" is a string field — applying non-string dimensions must no-op.
	pbflags.BoolDimension("user_id", true).Apply(msg)
	pbflags.Int64Dimension("user_id", 99).Apply(msg)
	pbflags.EnumDimension("user_id", 1).Apply(msg)

	fd := msg.Descriptor().Fields().ByName("user_id")
	if got := msg.Get(fd).String(); got != "" {
		t.Errorf("user_id should be empty after kind-mismatched Apply, got %q", got)
	}

	// "plan" is an enum field — applying non-enum dimensions must no-op.
	pbflags.StringDimension("plan", "enterprise").Apply(msg)
	pbflags.BoolDimension("plan", true).Apply(msg)
	pbflags.Int64Dimension("plan", 3).Apply(msg)

	planFD := msg.Descriptor().Fields().ByName("plan")
	if got := msg.Get(planFD).Enum(); got != 0 {
		t.Errorf("plan should be zero after kind-mismatched Apply, got %d", got)
	}

	// "is_internal" is a bool field — applying non-bool dimensions must no-op.
	pbflags.StringDimension("is_internal", "true").Apply(msg)
	pbflags.Int64Dimension("is_internal", 1).Apply(msg)
	pbflags.EnumDimension("is_internal", 1).Apply(msg)

	boolFD := msg.Descriptor().Fields().ByName("is_internal")
	if got := msg.Get(boolFD).Bool(); got != false {
		t.Errorf("is_internal should be false after kind-mismatched Apply, got %v", got)
	}
}

func TestMultipleDimensions(t *testing.T) {
	t.Parallel()
	desc := (&wrapperspb.BoolValue{}).ProtoReflect().Descriptor()
	msg := dynamicpb.NewMessage(desc)

	pbflags.BoolDimension("value", true).Apply(msg.ProtoReflect())
	fd := desc.Fields().ByName("value")
	if got := msg.Get(fd).Bool(); got != true {
		t.Fatalf("expected true, got %v", got)
	}

	pbflags.BoolDimension("value", false).Apply(msg.ProtoReflect())
	if got := msg.Get(fd).Bool(); got != false {
		t.Errorf("expected false after overwrite, got %v", got)
	}
}

func TestEnumDimension_AllEnumFields(t *testing.T) {
	t.Parallel()
	// Verify Apply works on both enum fields in EvaluationContext.
	ctx := &examplepb.EvaluationContext{}
	msg := ctx.ProtoReflect()

	pbflags.EnumDimension("plan", protoreflect.EnumNumber(examplepb.PlanLevel_PLAN_LEVEL_PRO)).Apply(msg)
	pbflags.EnumDimension("device_type", protoreflect.EnumNumber(examplepb.DeviceType_DEVICE_TYPE_MOBILE)).Apply(msg)

	if ctx.Plan != examplepb.PlanLevel_PLAN_LEVEL_PRO {
		t.Errorf("plan: got %v, want PLAN_LEVEL_PRO", ctx.Plan)
	}
	if ctx.DeviceType != examplepb.DeviceType_DEVICE_TYPE_MOBILE {
		t.Errorf("device_type: got %v, want DEVICE_TYPE_MOBILE", ctx.DeviceType)
	}
}
