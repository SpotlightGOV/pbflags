package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SpotlightGOV/pbflags/internal/projectconfig"
)

// runFlagNew implements `pb flag new <feature> <flag> --type=<t>`.
// It edits a .proto on disk: finds the message carrying
// `(pbflags.feature) { id: "<feature>" }`, picks the next field tag,
// and inserts a `(pbflags.flag)`-annotated field with a type-appropriate
// default. We deliberately operate on the .proto text (not on
// descriptors) so the command works without a regenerated descriptors.pb
// — the operator can rerun their codegen step afterwards.
func runFlagNew(args []string) {
	fs := flag.NewFlagSet("pb flag new", flag.ExitOnError)
	typ := fs.String("type", "bool", "Flag type: bool|str|int|double, or list form []bool|[]str|[]int|[]double")
	desc := fs.String("description", "TODO: describe this flag", "Flag description")
	protoPathFlag := fs.String("proto", "", "Override the proto path (defaults to .pbflags.yaml proto_path)")
	positional := parsePositionalFlags(fs, args)

	if len(positional) != 2 {
		fmt.Fprintln(os.Stderr, "usage: pb flag new <feature> <flag> [--type=<t>] [--description=...]")
		os.Exit(1)
	}
	feature := positional[0]
	flagName := positional[1]

	if err := validateFeatureName(flagName); err != nil {
		fatal(fmt.Errorf("flag name: %w", err))
	}

	spec, err := parseTypeShorthand(*typ)
	if err != nil {
		fatal(err)
	}

	projCfg, projRoot, _ := projectconfig.Discover(".")
	protoDir := *protoPathFlag
	if protoDir == "" {
		protoDir = projCfg.ProtoDir(projRoot)
	}
	if protoDir == "" {
		protoDir = "proto"
	}

	target, err := findFeatureProto(protoDir, feature)
	if err != nil {
		fatal(err)
	}

	updated, nextTag, err := insertFlagField(target, feature, flagName, *desc, spec)
	if err != nil {
		fatal(err)
	}
	if err := os.WriteFile(target, updated, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("Added %s = %d to %s\n", flagName, nextTag, target)
	fmt.Println("Next steps:")
	fmt.Println("  - Edit the field's description / default if the placeholders aren't right")
	fmt.Println("  - Regenerate descriptors (e.g. make generate)")
}

// flagTypeSpec captures the proto-level type shape we need to emit.
type flagTypeSpec struct {
	protoType   string // e.g. "bool", "repeated string"
	defaultExpr string // the inner content of `default: { ... }`
}

func parseTypeShorthand(s string) (flagTypeSpec, error) {
	repeated := false
	if strings.HasPrefix(s, "[]") {
		repeated = true
		s = s[2:]
	}
	switch s {
	case "bool":
		if repeated {
			return flagTypeSpec{"repeated bool", "bool_list_value: { values: [] }"}, nil
		}
		return flagTypeSpec{"bool", "bool_value: { value: false }"}, nil
	case "str", "string":
		if repeated {
			return flagTypeSpec{"repeated string", "string_list_value: { values: [] }"}, nil
		}
		return flagTypeSpec{"string", "string_value: { value: \"\" }"}, nil
	case "int", "int64":
		if repeated {
			return flagTypeSpec{"repeated int64", "int64_list_value: { values: [] }"}, nil
		}
		return flagTypeSpec{"int64", "int64_value: { value: 0 }"}, nil
	case "double", "float":
		if repeated {
			return flagTypeSpec{"repeated double", "double_list_value: { values: [] }"}, nil
		}
		return flagTypeSpec{"double", "double_value: { value: 0.0 }"}, nil
	}
	return flagTypeSpec{}, fmt.Errorf("unknown --type %q (want bool|str|int|double or []bool|[]str|[]int|[]double)", s)
}

// findFeatureProto walks protoDir for a .proto file containing a message
// whose (pbflags.feature) annotation carries `id: "<feature>"`. We avoid
// descriptors here so the command works in fresh checkouts without a
// codegen run.
func findFeatureProto(protoDir, feature string) (string, error) {
	idMarker := regexp.MustCompile(`(?m)^\s*id:\s*"` + regexp.QuoteMeta(feature) + `"\s*$`)
	featureMarker := regexp.MustCompile(`\(pbflags\.feature\)\s*=\s*\{`)

	var matches []string
	err := filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// Both markers must be present; we lean on the message-locator
		// step (insertFlagField) to confirm they belong to the same
		// message. Two different features in one file would still match,
		// but that's a deliberate choice we can disambiguate later.
		if featureMarker.Match(data) && idMarker.Match(data) {
			matches = append(matches, path)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk %s: %w", protoDir, err)
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("feature %q not found under %s — did you create the .proto with `pb feature new`?", feature, protoDir)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("feature %q matched multiple files: %s", feature, strings.Join(matches, ", "))
	}
}

// insertFlagField rewrites a .proto file's bytes to add a new flag
// field to the message that owns `(pbflags.feature) { id: "<feature>" }`.
// Returns the new bytes plus the field tag we picked.
func insertFlagField(path, feature, flagName, description string, spec flagTypeSpec) ([]byte, int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	src := string(data)
	lines := strings.Split(src, "\n")

	msgStart, msgEnd, err := locateFeatureMessage(lines, feature)
	if err != nil {
		return nil, 0, fmt.Errorf("%s: %w", path, err)
	}

	// Refuse duplicate names — proto would reject this anyway, but a
	// pre-flight check gives a friendlier error than a codegen failure.
	dupRe := regexp.MustCompile(`(?m)^\s*(?:repeated\s+)?\w+\s+` + regexp.QuoteMeta(flagName) + `\s*=\s*\d+\b`)
	for i := msgStart + 1; i < msgEnd; i++ {
		line := stripComment(lines[i])
		if dupRe.MatchString(line) {
			return nil, 0, fmt.Errorf("%s: flag %q already exists", path, flagName)
		}
	}

	nextTag := nextFieldTag(lines, msgStart+1, msgEnd)

	field := renderFlagField(flagName, nextTag, description, spec)

	// Trim a single trailing blank line inside the message body so we
	// don't accumulate gaps when scaffolding repeatedly.
	insertAt := msgEnd
	if insertAt-1 > msgStart && strings.TrimSpace(lines[insertAt-1]) == "" {
		insertAt--
	}

	out := make([]string, 0, len(lines)+len(field)+2)
	out = append(out, lines[:insertAt]...)
	if insertAt > msgStart+1 {
		out = append(out, "")
	}
	out = append(out, field...)
	out = append(out, lines[insertAt:]...)

	return []byte(strings.Join(out, "\n")), nextTag, nil
}

// locateFeatureMessage finds the [start, end) line range of the message
// whose body contains `id: "<feature>"` directly under a
// `(pbflags.feature)` annotation. Returns the line index of the opening
// `message ... {` and the line index of the matching closing `}`.
func locateFeatureMessage(lines []string, feature string) (int, int, error) {
	msgRe := regexp.MustCompile(`^\s*message\s+\w+\s*\{`)
	idRe := regexp.MustCompile(`^\s*id:\s*"` + regexp.QuoteMeta(feature) + `"\s*$`)

	for i, line := range lines {
		if !msgRe.MatchString(line) {
			continue
		}
		// Scan body with brace counting; comments can contain braces, so
		// strip those before counting.
		depth := 1
		hasMatch := false
		for j := i + 1; j < len(lines); j++ {
			body := stripComment(lines[j])
			depth += strings.Count(body, "{") - strings.Count(body, "}")
			if !hasMatch && idRe.MatchString(lines[j]) {
				hasMatch = true
			}
			if depth == 0 {
				if hasMatch {
					return i, j, nil
				}
				break
			}
		}
	}
	return 0, 0, fmt.Errorf("no message with (pbflags.feature) id %q", feature)
}

var fieldTagRe = regexp.MustCompile(`=\s*(\d+)\s*\[`)

// nextFieldTag returns max(observed field tags in [start,end)) + 1, or 1
// if none are present. Comment lines are ignored so the example block
// from `pb feature new` doesn't poison the tag space.
func nextFieldTag(lines []string, start, end int) int {
	max := 0
	for i := start; i < end; i++ {
		body := stripComment(lines[i])
		for _, m := range fieldTagRe.FindAllStringSubmatch(body, -1) {
			var n int
			fmt.Sscanf(m[1], "%d", &n)
			if n > max {
				max = n
			}
		}
	}
	return max + 1
}

// stripComment removes a `// ...` line comment if any, returning the
// pre-comment portion. Block comments aren't handled — proto files
// rarely use them and the proto3 style guide discourages them.
func stripComment(line string) string {
	if idx := strings.Index(line, "//"); idx >= 0 {
		return line[:idx]
	}
	return line
}

// renderFlagField returns the indented lines of a new flag field. We
// match the formatting of `pb feature new`'s example block (2-space
// indent, single-line default) so the file stays grep-friendly.
func renderFlagField(name string, tag int, description string, spec flagTypeSpec) []string {
	return []string{
		fmt.Sprintf("  %s %s = %d [(pbflags.flag) = {", spec.protoType, name, tag),
		fmt.Sprintf("    description: %q", description),
		fmt.Sprintf("    default: { %s }", spec.defaultExpr),
		"  }];",
	}
}
