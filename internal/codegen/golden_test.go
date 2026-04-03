package codegen_test

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

var update = flag.Bool("update", false, "update golden files")

// projectRoot returns the repository root by walking up from the test file.
func projectRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	dir := filepath.Dir(filename) // internal/codegen
	root := filepath.Join(dir, "..", "..")
	abs, err := filepath.Abs(root)
	require.NoError(t, err)
	// Verify it's the project root.
	_, err = os.Stat(filepath.Join(abs, "go.mod"))
	require.NoError(t, err, "could not find go.mod at %s", abs)
	return abs
}

// buildPlugin builds protoc-gen-pbflags and returns the binary path.
func buildPlugin(t *testing.T, root string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "protoc-gen-pbflags")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/protoc-gen-pbflags/")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "build protoc-gen-pbflags: %s", out)
	return bin
}

func generateWithBuf(t *testing.T, root, pluginBin, outDir, lang string, extraOpts ...string) {
	t.Helper()

	opts := []string{"lang=" + lang}
	// Check if extraOpts overrides the default prefix/package.
	hasPrefix := false
	for _, o := range extraOpts {
		if strings.HasPrefix(o, "package_prefix=") || strings.HasPrefix(o, "java_package=") {
			hasPrefix = true
			break
		}
	}
	if !hasPrefix {
		switch lang {
		case "go":
			opts = append(opts, "package_prefix=github.com/SpotlightGOV/pbflags/gen/flags")
		case "java":
			opts = append(opts, "java_package=org.spotlightgov.pbflags.generated")
		}
	}
	opts = append(opts, extraOpts...)

	tmpl := filepath.Join(t.TempDir(), "buf.gen.yaml")
	content := "version: v2\nplugins:\n  - local: " + pluginBin + "\n    out: " + outDir + "\n    opt:\n"
	for _, o := range opts {
		content += "      - " + o + "\n"
	}
	content += "inputs:\n  - directory: proto\n"
	require.NoError(t, os.WriteFile(tmpl, []byte(content), 0o644))

	cmd := exec.Command("buf", "generate", "--template", tmpl, "--path", "proto/example/notifications.proto")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "buf generate (%s): %s", lang, out)
}

func TestGoldenGo(t *testing.T) {
	root := projectRoot(t)
	pluginBin := buildPlugin(t, root)
	goldenDir := filepath.Join(root, "internal", "codegen", "testdata", "golden", "go")

	if *update {
		// Regenerate golden files.
		tmpDir := t.TempDir()
		generateWithBuf(t, root, pluginBin, tmpDir, "go")
		generated := findFile(t, tmpDir, "notifications_flags.go")
		copyFile(t, generated, filepath.Join(goldenDir, "notifications_flags.go"))
		t.Log("updated golden Go file")
		return
	}

	tmpDir := t.TempDir()
	generateWithBuf(t, root, pluginBin, tmpDir, "go")
	generated := findFile(t, tmpDir, "notifications_flags.go")

	compareFiles(t, filepath.Join(goldenDir, "notifications_flags.go"), generated)
}

func TestGoldenJava(t *testing.T) {
	root := projectRoot(t)
	pluginBin := buildPlugin(t, root)
	goldenDir := filepath.Join(root, "internal", "codegen", "testdata", "golden", "java")

	javaFiles := []string{"NotificationsFlags.java", "NotificationsFlagsImpl.java", "PbflagsFlagDescriptorProvider.java"}

	if *update {
		tmpDir := t.TempDir()
		generateWithBuf(t, root, pluginBin, tmpDir, "java")
		for _, f := range javaFiles {
			generated := findFile(t, tmpDir, f)
			copyFile(t, generated, filepath.Join(goldenDir, f))
		}
		t.Log("updated golden Java files")
		return
	}

	tmpDir := t.TempDir()
	generateWithBuf(t, root, pluginBin, tmpDir, "java")
	for _, f := range javaFiles {
		generated := findFile(t, tmpDir, f)
		compareFiles(t, filepath.Join(goldenDir, f), generated)
	}
}

func TestGoldenJavaDagger(t *testing.T) {
	root := projectRoot(t)
	pluginBin := buildPlugin(t, root)
	goldenDir := filepath.Join(root, "internal", "codegen", "testdata", "golden", "java-dagger")

	daggerFiles := []string{"NotificationsFlags.java", "NotificationsFlagsImpl.java", "FlagRegistryModule.java", "PbflagsFlagDescriptorProvider.java"}

	if *update {
		tmpDir := t.TempDir()
		generateWithBuf(t, root, pluginBin, tmpDir, "java", "java_dagger=true")
		for _, f := range daggerFiles {
			generated := findFile(t, tmpDir, f)
			copyFile(t, generated, filepath.Join(goldenDir, f))
		}
		t.Log("updated golden Java Dagger files")
		return
	}

	tmpDir := t.TempDir()
	generateWithBuf(t, root, pluginBin, tmpDir, "java", "java_dagger=true")
	for _, f := range daggerFiles {
		generated := findFile(t, tmpDir, f)
		compareFiles(t, filepath.Join(goldenDir, f), generated)
	}
}

func findFile(t *testing.T, dir, name string) string {
	t.Helper()
	var found string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Name() == name {
			found = path
		}
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, found, "could not find %s in %s", name, dir)
	return found
}

func compareFiles(t *testing.T, golden, actual string) {
	t.Helper()
	goldenData, err := os.ReadFile(golden)
	require.NoError(t, err, "read golden file %s", golden)
	actualData, err := os.ReadFile(actual)
	require.NoError(t, err, "read actual file %s", actual)

	goldenStr := strings.TrimSpace(string(goldenData))
	actualStr := strings.TrimSpace(string(actualData))

	if goldenStr != actualStr {
		// Show a diff for debugging.
		diff, _ := exec.Command("diff", "-u", golden, actual).CombinedOutput()
		t.Fatalf("golden file mismatch for %s.\nRun with -update to regenerate.\n\n%s",
			filepath.Base(golden), string(diff))
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	require.NoError(t, os.WriteFile(dst, data, 0o644))
}
