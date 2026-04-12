package contextutil_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/SpotlightGOV/pbflags/internal/codegen/contextutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/compiler/protogen"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/descriptorpb"
	pluginpb "google.golang.org/protobuf/types/pluginpb"
)

func projectRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	root := filepath.Join(filepath.Dir(filename), "..", "..", "..")
	abs, err := filepath.Abs(root)
	require.NoError(t, err)
	return abs
}

// buildDescriptorSet compiles the example proto and returns the FileDescriptorSet bytes.
func buildDescriptorSet(t *testing.T, root string) []byte {
	t.Helper()
	if _, err := exec.LookPath("buf"); err != nil {
		t.Skip("buf not found on PATH")
	}
	out := filepath.Join(t.TempDir(), "descriptors.pb")
	cmd := exec.Command("buf", "build", "-o", out)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "buf build: %s", output)
	data, err := os.ReadFile(out)
	require.NoError(t, err)
	return data
}

func pluginFromDescriptors(t *testing.T, data []byte) *protogen.Plugin {
	t.Helper()
	fds := &descriptorpb.FileDescriptorSet{}
	require.NoError(t, proto.Unmarshal(data, fds))

	// Build a CodeGeneratorRequest with all files.
	req := &pluginpb.CodeGeneratorRequest{}
	for _, fd := range fds.File {
		req.ProtoFile = append(req.ProtoFile, fd)
		req.FileToGenerate = append(req.FileToGenerate, fd.GetName())
	}

	opts := protogen.Options{}
	plugin, err := opts.New(req)
	require.NoError(t, err)
	return plugin
}

func TestDiscoverContext(t *testing.T) {
	root := projectRoot(t)
	data := buildDescriptorSet(t, root)
	plugin := pluginFromDescriptors(t, data)

	ctx, err := contextutil.DiscoverContext(plugin)
	require.NoError(t, err)
	require.NotNil(t, ctx, "expected to find EvaluationContext")

	assert.Equal(t, "example.EvaluationContext", ctx.MessageName)
	assert.Len(t, ctx.Dimensions, 5)

	// Verify dimensions by name.
	byName := make(map[string]contextutil.DimensionDef)
	for _, d := range ctx.Dimensions {
		byName[d.Name] = d
	}

	// user_id: string, hashable
	uid := byName["user_id"]
	assert.Equal(t, contextutil.DimensionString, uid.Kind)
	assert.True(t, uid.Hashable)
	assert.Equal(t, "Authenticated user identifier", uid.Description)

	// session_id: string, hashable
	sid := byName["session_id"]
	assert.Equal(t, contextutil.DimensionString, sid.Kind)
	assert.True(t, sid.Hashable)

	// plan: enum
	plan := byName["plan"]
	assert.Equal(t, contextutil.DimensionEnum, plan.Kind)
	assert.Equal(t, "example.PlanLevel", plan.EnumName)
	assert.Len(t, plan.EnumValues, 4) // UNSPECIFIED, FREE, PRO, ENTERPRISE

	// device_type: enum
	dt := byName["device_type"]
	assert.Equal(t, contextutil.DimensionEnum, dt.Kind)
	assert.Equal(t, "example.DeviceType", dt.EnumName)

	// is_internal: bool
	isInt := byName["is_internal"]
	assert.Equal(t, contextutil.DimensionBool, isInt.Kind)
}

func TestDiscoverContext_NoContext(t *testing.T) {
	// Empty plugin with no context message.
	opts := protogen.Options{}
	plugin, err := opts.New(&pluginpb.CodeGeneratorRequest{})
	require.NoError(t, err)

	ctx, err := contextutil.DiscoverContext(plugin)
	require.NoError(t, err)
	assert.Nil(t, ctx)
}
