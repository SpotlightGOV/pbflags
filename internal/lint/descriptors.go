package lint

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// HasProtoChanges returns true if any .proto files in protoDir differ
// between the given git ref and the working tree.
func HasProtoChanges(baseRef, protoDir string) (bool, error) {
	cmd := exec.Command("git", "diff", "--name-only", baseRef, "--", protoDir)
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("git diff: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasSuffix(line, ".proto") {
			return true, nil
		}
	}
	return false, nil
}

// BuildDescriptors builds a FileDescriptorSet from the proto files in
// protoDir (working tree). Returns the serialized bytes.
func BuildDescriptors(protoDir string) ([]byte, error) {
	tmpFile, err := os.CreateTemp("", "pbflags-lint-*.pb")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	cmd := exec.Command("buf", "build", protoDir, "--exclude-source-info", "-o", tmpPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("buf build (working tree): %w\n%s", err, out)
	}

	return os.ReadFile(tmpPath)
}

// BuildDescriptorsFromRef builds a FileDescriptorSet from the proto files
// at the given git ref. Extracts the proto directory from the ref into a
// temp directory, then runs buf build.
func BuildDescriptorsFromRef(protoDir, gitRef string) ([]byte, error) {
	tmpDir, err := os.MkdirTemp("", "pbflags-lint-base-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Extract proto files from the git ref.
	// git archive outputs a tar; we pipe it to tar to extract.
	archiveCmd := exec.Command("git", "archive", gitRef, "--", protoDir)
	tarCmd := exec.Command("tar", "xf", "-", "-C", tmpDir)
	tarCmd.Stdin, err = archiveCmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("pipe setup: %w", err)
	}

	if err := tarCmd.Start(); err != nil {
		return nil, fmt.Errorf("tar start: %w", err)
	}
	if err := archiveCmd.Run(); err != nil {
		return nil, fmt.Errorf("git archive %s: %w", gitRef, err)
	}
	if err := tarCmd.Wait(); err != nil {
		return nil, fmt.Errorf("tar extract: %w", err)
	}

	// Copy buf.yaml and buf.lock if they exist (buf needs them for deps/config).
	for _, name := range []string{"buf.yaml", "buf.lock"} {
		data, err := os.ReadFile(name)
		if err != nil {
			continue // file doesn't exist, skip
		}
		if err := os.WriteFile(filepath.Join(tmpDir, name), data, 0o644); err != nil {
			return nil, fmt.Errorf("copy %s: %w", name, err)
		}
	}

	// Build descriptors from the extracted files.
	extractedProtoDir := filepath.Join(tmpDir, protoDir)
	tmpFile, err := os.CreateTemp("", "pbflags-lint-base-*.pb")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	cmd := exec.Command("buf", "build", extractedProtoDir, "--exclude-source-info", "-o", tmpPath)
	cmd.Dir = tmpDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("buf build (ref %s): %w\n%s", gitRef, err, out)
	}

	return os.ReadFile(tmpPath)
}
