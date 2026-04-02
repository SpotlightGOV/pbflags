package codegen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// compilePkgPrefix is the package prefix used when generating Go code for
// compile tests. It must match the actual module layout so that the generated
// imports (gen/pbflags/v1, gen/pbflags/v1/pbflagsv1connect) resolve.
const compilePkgPrefix = "github.com/SpotlightGOV/pbflags/gen/pbflags"

// TestGoldenFilesCompile verifies the generated Go code compiles as valid Go
// within a temporary module. Uses the real module's package prefix so imports resolve.
func TestGoldenFilesCompile(t *testing.T) {
	root := projectRoot(t)
	pluginBin := buildPlugin(t, root)

	// Generate with the real prefix so imports resolve against the real module.
	genDir := t.TempDir()
	generateWithBuf(t, root, pluginBin, genDir, "go",
		"package_prefix="+compilePkgPrefix)
	generated := findFile(t, genDir, "notifications_flags.go")
	genData, err := os.ReadFile(generated)
	require.NoError(t, err)

	// Create a temporary Go module containing the generated code.
	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "notificationsflags")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(pkgDir, "notifications_flags.go"), genData, 0o644))

	goMod := `module testcompile

go 1.26

require (
	connectrpc.com/connect v1.19.1
	github.com/SpotlightGOV/pbflags v0.0.0
	google.golang.org/protobuf v1.36.11
)

replace github.com/SpotlightGOV/pbflags => ` + root + `
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0o644))

	mainGo := `package main

import (
	"fmt"
	_ "testcompile/notificationsflags"
)

func main() {
	fmt.Println("compile ok")
}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(mainGo), 0o644))

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = tmpDir
	out, err := tidy.CombinedOutput()
	require.NoError(t, err, "go mod tidy: %s", out)

	build := exec.Command("go", "build", "./...")
	build.Dir = tmpDir
	out, err = build.CombinedOutput()
	require.NoError(t, err, "go build: %s", out)
}

// TestSuccessfulEvaluation verifies the generated client code works correctly
// with a mock evaluator for all four flag types.
func TestSuccessfulEvaluation(t *testing.T) {
	root := projectRoot(t)
	pluginBin := buildPlugin(t, root)
	tmpDir := t.TempDir()

	// Generate code with real prefix so imports resolve.
	generateWithBuf(t, root, pluginBin, tmpDir, "go",
		"package_prefix="+compilePkgPrefix)
	generatedDir := filepath.Dir(findFile(t, tmpDir, "notifications_flags.go"))

	// Build a test Go module that exercises the generated client.
	modDir := t.TempDir()
	pkgDir := filepath.Join(modDir, "notificationsflags")
	require.NoError(t, os.MkdirAll(pkgDir, 0o755))

	// Copy generated files.
	entries, err := os.ReadDir(generatedDir)
	require.NoError(t, err)
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(generatedDir, e.Name()))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(pkgDir, e.Name()), data, 0o644))
	}

	goMod := `module testeval

go 1.26

require (
	connectrpc.com/connect v1.19.1
	github.com/SpotlightGOV/pbflags v0.0.0
	google.golang.org/protobuf v1.36.11
)

replace github.com/SpotlightGOV/pbflags => ` + root + `
`
	require.NoError(t, os.WriteFile(filepath.Join(modDir, "go.mod"), []byte(goMod), 0o644))

	// Write the behavioral test.
	testCode := `package notificationsflags_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/gen/pbflags/v1/pbflagsv1connect"
	nf "testeval/notificationsflags"
)

// mockEvaluator implements FlagEvaluatorServiceClient for testing.
type mockEvaluator struct {
	pbflagsv1connect.UnimplementedFlagEvaluatorServiceHandler
	responses map[string]*pbflagsv1.EvaluateResponse
}

func (m *mockEvaluator) Evaluate(_ context.Context, req *connect.Request[pbflagsv1.EvaluateRequest]) (*connect.Response[pbflagsv1.EvaluateResponse], error) {
	resp, ok := m.responses[req.Msg.FlagId]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	return connect.NewResponse(resp), nil
}

func (m *mockEvaluator) Health(_ context.Context, _ *connect.Request[pbflagsv1.HealthRequest]) (*connect.Response[pbflagsv1.HealthResponse], error) {
	return connect.NewResponse(&pbflagsv1.HealthResponse{
		Status: pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING,
	}), nil
}

func TestBoolFlag(t *testing.T) {
	mock := &mockEvaluator{responses: map[string]*pbflagsv1.EvaluateResponse{
		nf.EmailEnabledID: {Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: false}}},
	}}
	client := nf.NewNotificationsFlagsClient(mock)
	if got := client.EmailEnabled(context.Background(), "user-1"); got != false {
		t.Errorf("EmailEnabled = %v, want false", got)
	}
}

func TestStringFlag(t *testing.T) {
	mock := &mockEvaluator{responses: map[string]*pbflagsv1.EvaluateResponse{
		nf.DigestFrequencyID: {Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: "weekly"}}},
	}}
	client := nf.NewNotificationsFlagsClient(mock)
	if got := client.DigestFrequency(context.Background()); got != "weekly" {
		t.Errorf("DigestFrequency = %q, want %q", got, "weekly")
	}
}

func TestInt64Flag(t *testing.T) {
	mock := &mockEvaluator{responses: map[string]*pbflagsv1.EvaluateResponse{
		nf.MaxRetriesID: {Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: 10}}},
	}}
	client := nf.NewNotificationsFlagsClient(mock)
	if got := client.MaxRetries(context.Background()); got != 10 {
		t.Errorf("MaxRetries = %d, want 10", got)
	}
}

func TestDoubleFlag(t *testing.T) {
	mock := &mockEvaluator{responses: map[string]*pbflagsv1.EvaluateResponse{
		nf.ScoreThresholdID: {Value: &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: 0.95}}},
	}}
	client := nf.NewNotificationsFlagsClient(mock)
	if got := client.ScoreThreshold(context.Background()); got != 0.95 {
		t.Errorf("ScoreThreshold = %f, want 0.95", got)
	}
}

func TestErrorReturnsDefaults(t *testing.T) {
	// Empty mock — all Evaluate calls return NotFound error.
	mock := &mockEvaluator{responses: map[string]*pbflagsv1.EvaluateResponse{}}
	client := nf.NewNotificationsFlagsClient(mock)
	ctx := context.Background()

	if got := client.EmailEnabled(ctx, "user-1"); got != nf.EmailEnabledDefault {
		t.Errorf("EmailEnabled on error = %v, want %v", got, nf.EmailEnabledDefault)
	}
	if got := client.DigestFrequency(ctx); got != nf.DigestFrequencyDefault {
		t.Errorf("DigestFrequency on error = %q, want %q", got, nf.DigestFrequencyDefault)
	}
	if got := client.MaxRetries(ctx); got != nf.MaxRetriesDefault {
		t.Errorf("MaxRetries on error = %d, want %d", got, nf.MaxRetriesDefault)
	}
	if got := client.ScoreThreshold(ctx); got != nf.ScoreThresholdDefault {
		t.Errorf("ScoreThreshold on error = %f, want %f", got, nf.ScoreThresholdDefault)
	}
}

func TestStatus(t *testing.T) {
	mock := &mockEvaluator{responses: map[string]*pbflagsv1.EvaluateResponse{}}
	client := nf.NewNotificationsFlagsClient(mock)
	if got := client.Status(context.Background()); got != pbflagsv1.EvaluatorStatus_EVALUATOR_STATUS_SERVING {
		t.Errorf("Status = %v, want SERVING", got)
	}
}
`
	testDir := filepath.Join(modDir, "notificationsflags")
	require.NoError(t, os.WriteFile(filepath.Join(testDir, "notifications_flags_test.go"), []byte(testCode), 0o644))

	// go mod tidy
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = modDir
	out, err := tidy.CombinedOutput()
	require.NoError(t, err, "go mod tidy: %s", out)

	// Run the behavioral tests.
	test := exec.Command("go", "test", "-v", "-count=1", "./notificationsflags/")
	test.Dir = modDir
	out, err = test.CombinedOutput()
	t.Log(string(out))
	require.NoError(t, err, "behavioral tests failed")
}
