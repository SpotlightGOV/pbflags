package sync

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	example "github.com/SpotlightGOV/pbflags/gen/example"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/celenv"
	"github.com/SpotlightGOV/pbflags/internal/codegen/contextutil"
	"github.com/SpotlightGOV/pbflags/internal/configfile"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/testdb"
)

// buildDescriptorSet creates a serialized FileDescriptorSet from runtime
// proto messages, including all transitive file imports.
func buildDescriptorSet(msgs ...proto.Message) ([]byte, error) {
	seen := map[string]bool{}
	var files []*descriptorpb.FileDescriptorProto
	for _, msg := range msgs {
		collectFiles(msg.ProtoReflect().Descriptor().ParentFile(), seen, &files)
	}
	fds := &descriptorpb.FileDescriptorSet{File: files}
	return proto.Marshal(fds)
}

func collectFiles(fd protoreflect.FileDescriptor, seen map[string]bool, files *[]*descriptorpb.FileDescriptorProto) {
	if seen[fd.Path()] {
		return
	}
	seen[fd.Path()] = true
	for i := 0; i < fd.Imports().Len(); i++ {
		collectFiles(fd.Imports().Get(i), seen, files)
	}
	*files = append(*files, protodesc.ToFileDescriptorProto(fd))
}

func TestDiscoverContextMessage(t *testing.T) {
	descData, err := buildDescriptorSet(&example.Notifications{}, &example.EvaluationContext{})
	if err != nil {
		t.Fatalf("build descriptor set: %v", err)
	}

	files, _, err := evaluator.ParseDescriptorSet(descData)
	if err != nil {
		t.Fatalf("parse descriptor set: %v", err)
	}

	msg, err := contextutil.DiscoverContextFromFiles(files)
	if err != nil {
		t.Fatalf("discoverContextMessage: %v", err)
	}

	if string(msg.FullName()) != "example.EvaluationContext" {
		t.Errorf("found %q, want example.EvaluationContext", msg.FullName())
	}
}

func TestCompileFlag(t *testing.T) {
	md := (&example.EvaluationContext{}).ProtoReflect().Descriptor()
	compiler, err := celenv.NewCompiler(md)
	if err != nil {
		t.Fatalf("NewCompiler: %v", err)
	}
	boundedDims := celenv.BoundedDimsFromDescriptor(md)

	t.Run("static value", func(t *testing.T) {
		entry := configfile.FlagEntry{
			Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}},
		}
		result, err := compileFlag("email_enabled", entry, compiler, boundedDims)
		if err != nil {
			t.Fatalf("compileFlag: %v", err)
		}
		if len(result.Conditions) != 1 {
			t.Fatalf("got %d conditions, want 1", len(result.Conditions))
		}
		if result.Conditions[0].Cel != "" {
			t.Errorf("static value condition CEL should be empty, got %q", result.Conditions[0].Cel)
		}
		if len(result.DimMeta) != 0 {
			t.Error("expected empty DimMeta for static value")
		}
	})

	t.Run("condition chain", func(t *testing.T) {
		entry := configfile.FlagEntry{
			Conditions: []configfile.Condition{
				{When: `ctx.plan == PlanLevel.ENTERPRISE`, Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "daily"}}},
				{When: "", Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "weekly"}}},
			},
		}
		result, err := compileFlag("digest_frequency", entry, compiler, boundedDims)
		if err != nil {
			t.Fatalf("compileFlag: %v", err)
		}
		if len(result.Conditions) != 2 {
			t.Fatalf("got %d conditions, want 2", len(result.Conditions))
		}
		if result.Conditions[0].Cel != "ctx.plan == PlanLevel.ENTERPRISE" {
			t.Errorf("condition 0 CEL = %q", result.Conditions[0].Cel)
		}
		if result.Conditions[1].Cel != "" {
			t.Errorf("condition 1 (otherwise) CEL should be empty, got %q", result.Conditions[1].Cel)
		}

		if result.DimMeta["plan"] == nil || result.DimMeta["plan"].Classification != string(celenv.Bounded) {
			t.Errorf("plan classification = %v, want bounded", result.DimMeta["plan"])
		}

		// No unbounded warnings expected.
		for _, w := range result.Warnings {
			if w != "" {
				t.Errorf("unexpected warning: %s", w)
			}
		}
	})

	t.Run("unbounded warning", func(t *testing.T) {
		entry := configfile.FlagEntry{
			Conditions: []configfile.Condition{
				{When: `ctx.user_id == ctx.session_id`, Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: true}}},
				{When: "", Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: false}}},
			},
		}
		result, err := compileFlag("test_flag", entry, compiler, boundedDims)
		if err != nil {
			t.Fatalf("compileFlag: %v", err)
		}
		if len(result.Warnings) == 0 {
			t.Error("expected unbounded dimension warnings")
		}
	})
}

func TestSyncConditionsIntegration(t *testing.T) {
	_, pool := testdb.Require(t)
	ctx := context.Background()

	// Create a test feature with two flags.
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL"},
		{FlagType: "STRING"},
	})

	// Build descriptor set from example protos.
	descData, err := buildDescriptorSet(&example.Notifications{}, &example.EvaluationContext{})
	if err != nil {
		t.Fatalf("build descriptor set: %v", err)
	}

	// Build flag defs that match our test feature.
	defs := []evaluator.FlagDef{
		{FlagID: tf.FlagIDs[0], FeatureID: tf.FeatureID, FieldNum: 1, Name: "email_enabled", FlagType: pbflagsv1.FlagType_FLAG_TYPE_BOOL},
		{FlagID: tf.FlagIDs[1], FeatureID: tf.FeatureID, FieldNum: 2, Name: "digest_frequency", FlagType: pbflagsv1.FlagType_FLAG_TYPE_STRING},
	}

	// Write YAML config to temp dir.
	configDir := t.TempDir()
	yamlData := []byte(`feature: ` + tf.FeatureID + `
flags:
  email_enabled:
    value: true
  digest_frequency:
    conditions:
      - when: 'ctx.plan == PlanLevel.ENTERPRISE'
        value: "daily"
      - otherwise: "weekly"
`)
	if err := os.WriteFile(filepath.Join(configDir, "test.yaml"), yamlData, 0644); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	// Acquire a raw pgx.Conn from the pool.
	conn, err := pgx.Connect(ctx, pool.Config().ConnString())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	result, err := SyncConditions(ctx, conn, configDir, descData, defs, logger, "")
	if err != nil {
		t.Fatalf("SyncConditions: %v", err)
	}

	if result.FlagsUpdated != 2 {
		t.Errorf("flags updated = %d, want 2", result.FlagsUpdated)
	}

	// Verify DB state for the conditional flag.
	var condBytes, dimBytes []byte
	var celVersion *string
	err = pool.QueryRow(ctx,
		`SELECT conditions, dimension_metadata, cel_version FROM feature_flags.flags WHERE flag_id = $1`,
		tf.FlagIDs[1],
	).Scan(&condBytes, &dimBytes, &celVersion)
	if err != nil {
		t.Fatalf("query flag: %v", err)
	}

	if condBytes == nil {
		t.Fatal("conditions should be non-nil for conditional flag")
	}
	var stored pbflagsv1.StoredConditions
	if err := proto.Unmarshal(condBytes, &stored); err != nil {
		t.Fatalf("unmarshal conditions: %v", err)
	}
	if len(stored.Conditions) != 2 {
		t.Fatalf("got %d conditions, want 2", len(stored.Conditions))
	}

	if celVersion == nil || *celVersion == "" {
		t.Error("cel_version should be set")
	}

	// Static flag should now have a single-entry condition chain (no CEL).
	var staticCond []byte
	var staticCelVersion *string
	err = pool.QueryRow(ctx,
		`SELECT conditions, cel_version FROM feature_flags.flags WHERE flag_id = $1`,
		tf.FlagIDs[0],
	).Scan(&staticCond, &staticCelVersion)
	if err != nil {
		t.Fatalf("query static flag: %v", err)
	}
	if staticCond == nil {
		t.Fatal("static flag should have non-nil conditions")
	}
	var staticStored pbflagsv1.StoredConditions
	if err := proto.Unmarshal(staticCond, &staticStored); err != nil {
		t.Fatalf("unmarshal static conditions: %v", err)
	}
	if len(staticStored.Conditions) != 1 {
		t.Fatalf("got %d static conditions, want 1", len(staticStored.Conditions))
	}
	if staticStored.Conditions[0].Cel != "" {
		t.Errorf("static condition CEL should be empty, got %q", staticStored.Conditions[0].Cel)
	}
	if staticCelVersion == nil || *staticCelVersion == "" {
		t.Error("static flag cel_version should be set")
	}
}
