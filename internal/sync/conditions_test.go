package sync

import (
	"context"
	"encoding/json"
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
	"github.com/SpotlightGOV/pbflags/internal/flagfmt"
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
		condJSON, dimJSON, valueBytes, _, err := compileFlag("email_enabled", entry, compiler, boundedDims)
		if err != nil {
			t.Fatalf("compileFlag: %v", err)
		}
		if condJSON != nil {
			t.Error("expected nil condJSON for static value")
		}
		if dimJSON != nil {
			t.Error("expected nil dimJSON for static value")
		}
		if valueBytes == nil {
			t.Error("expected non-nil valueBytes for static value")
		}
	})

	t.Run("condition chain", func(t *testing.T) {
		entry := configfile.FlagEntry{
			Conditions: []configfile.Condition{
				{When: `ctx.plan == PlanLevel.ENTERPRISE`, Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "daily"}}},
				{When: "", Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "weekly"}}},
			},
		}
		condJSON, dimJSON, _, warnings, err := compileFlag("digest_frequency", entry, compiler, boundedDims)
		if err != nil {
			t.Fatalf("compileFlag: %v", err)
		}
		if condJSON == nil {
			t.Fatal("expected non-nil condJSON")
		}

		var conds []flagfmt.StoredCondition
		if err := json.Unmarshal(condJSON, &conds); err != nil {
			t.Fatalf("unmarshal conditions: %v", err)
		}
		if len(conds) != 2 {
			t.Fatalf("got %d conditions, want 2", len(conds))
		}
		if conds[0].CEL == nil || *conds[0].CEL != "ctx.plan == PlanLevel.ENTERPRISE" {
			t.Errorf("condition 0 CEL = %v", conds[0].CEL)
		}
		if conds[1].CEL != nil {
			t.Errorf("condition 1 (otherwise) CEL should be nil, got %v", conds[1].CEL)
		}

		if dimJSON == nil {
			t.Fatal("expected non-nil dimJSON for condition with enum dimension")
		}

		var dims map[string]*celenv.DimensionMeta
		if err := json.Unmarshal(dimJSON, &dims); err != nil {
			t.Fatalf("unmarshal dims: %v", err)
		}
		if dims["plan"] == nil || dims["plan"].Classification != celenv.Bounded {
			t.Errorf("plan classification = %v, want bounded", dims["plan"])
		}

		// No unbounded warnings expected.
		for _, w := range warnings {
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
		_, _, _, warnings, err := compileFlag("test_flag", entry, compiler, boundedDims)
		if err != nil {
			t.Fatalf("compileFlag: %v", err)
		}
		if len(warnings) == 0 {
			t.Error("expected unbounded dimension warnings")
		}
	})
}

func TestSyncConditionsIntegration(t *testing.T) {
	_, pool := testdb.Require(t)
	ctx := context.Background()

	// Create a test feature with two flags.
	tf := testdb.CreateTestFeature(t, pool, []testdb.FlagSpec{
		{FlagType: "BOOL", Layer: "GLOBAL"},
		{FlagType: "STRING", Layer: "GLOBAL"},
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
	var condJSON, dimJSON []byte
	var celVersion *string
	err = pool.QueryRow(ctx,
		`SELECT conditions, dimension_metadata, cel_version FROM feature_flags.flags WHERE flag_id = $1`,
		tf.FlagIDs[1],
	).Scan(&condJSON, &dimJSON, &celVersion)
	if err != nil {
		t.Fatalf("query flag: %v", err)
	}

	if condJSON == nil {
		t.Fatal("conditions should be non-nil for conditional flag")
	}
	var conds []flagfmt.StoredCondition
	if err := json.Unmarshal(condJSON, &conds); err != nil {
		t.Fatalf("unmarshal conditions: %v", err)
	}
	if len(conds) != 2 {
		t.Fatalf("got %d conditions, want 2", len(conds))
	}

	if celVersion == nil || *celVersion == "" {
		t.Error("cel_version should be set")
	}

	// Static flag should have NULL conditions.
	var staticCond []byte
	err = pool.QueryRow(ctx,
		`SELECT conditions FROM feature_flags.flags WHERE flag_id = $1`,
		tf.FlagIDs[0],
	).Scan(&staticCond)
	if err != nil {
		t.Fatalf("query static flag: %v", err)
	}
	if staticCond != nil {
		t.Error("static flag should have NULL conditions")
	}
}
