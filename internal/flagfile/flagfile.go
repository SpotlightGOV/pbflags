// Package flagfile reads picocli-style @file references and expands them
// into a flat argument slice. Shared by all pbflags CLI binaries.
package flagfile

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExpandArgs reads picocli-style @file references in args and expands them.
// The file format is one --flag=value per line, with # comments and optional quoting.
//
// After expansion, host-local overrides from ~/.config/pbflags/<binary>.flags
// are merged with last-wins semantics (override matching flags, append new ones).
// This allows per-host config without modifying checked-in files.
func ExpandArgs(args []string) ([]string, error) {
	var expanded []string
	for _, arg := range args {
		if !strings.HasPrefix(arg, "@") {
			expanded = append(expanded, arg)
			continue
		}
		path := strings.TrimPrefix(arg, "@")
		fileArgs, err := readFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading flag file %s: %w", path, err)
		}
		expanded = append(expanded, fileArgs...)
	}
	return applyHomeOverrides(expanded)
}

func readFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var args []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) >= 2 && line[0] == '"' && line[len(line)-1] == '"' {
			line = line[1 : len(line)-1]
		}
		args = append(args, line)
	}
	return args, scanner.Err()
}

// applyHomeOverrides loads ~/.config/pbflags/<binary>.flags (if it exists) and
// merges its entries into args with last-wins semantics. The override directory
// can be changed via PBFLAGS_OVERRIDES_DIR.
func applyHomeOverrides(args []string) ([]string, error) {
	dir := os.Getenv("PBFLAGS_OVERRIDES_DIR")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return args, nil
		}
		dir = filepath.Join(home, ".config", "pbflags")
	}

	bin := filepath.Base(os.Args[0])
	path := filepath.Join(dir, bin+".flags")

	overrides, err := readFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return args, nil
		}
		return nil, fmt.Errorf("reading home overrides %s: %w", path, err)
	}
	if len(overrides) == 0 {
		return args, nil
	}
	return mergeFlags(args, overrides), nil
}

// mergeFlags appends overrides to base with last-wins deduplication by flag name.
func mergeFlags(base, overrides []string) []string {
	seen := make(map[string]int) // flag name → index in result
	result := make([]string, 0, len(base)+len(overrides))

	for _, arg := range base {
		name := flagName(arg)
		if name == "" {
			result = append(result, arg)
			continue
		}
		seen[name] = len(result)
		result = append(result, arg)
	}
	for _, arg := range overrides {
		name := flagName(arg)
		if name == "" {
			result = append(result, arg)
			continue
		}
		if idx, ok := seen[name]; ok {
			result[idx] = arg // replace in place
		} else {
			seen[name] = len(result)
			result = append(result, arg)
		}
	}
	return result
}

// flagName extracts the flag name from a --flag=value argument.
func flagName(arg string) string {
	s := strings.TrimPrefix(arg, "\"")
	if !strings.HasPrefix(s, "--") {
		return ""
	}
	s = s[2:]
	if i := strings.Index(s, "="); i >= 0 {
		return s[:i]
	}
	return s
}
