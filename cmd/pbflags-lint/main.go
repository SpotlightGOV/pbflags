// pbflags-lint detects breaking changes in pbflags proto definitions by
// comparing the working tree against a base git ref. Designed for use as a
// pre-commit hook.
//
// Usage:
//
//	pbflags-lint [--base <git-ref>] <proto-dir>
//
// Exit codes: 0 = clean, 1 = breaking changes found, 2 = tool error.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/lint"
)

var version = "dev"

func main() {
	base := flag.String("base", "HEAD", "git ref to compare against (default: HEAD)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: pbflags-lint [--base <git-ref>] <proto-dir>\n\n")
		fmt.Fprintf(os.Stderr, "Detects breaking changes in pbflags proto definitions.\n")
		fmt.Fprintf(os.Stderr, "Compares the working tree against a base git ref.\n\n")
		fmt.Fprintf(os.Stderr, "Exit codes:\n")
		fmt.Fprintf(os.Stderr, "  0  no breaking changes\n")
		fmt.Fprintf(os.Stderr, "  1  breaking changes found\n")
		fmt.Fprintf(os.Stderr, "  2  tool error\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println("pbflags-lint", version)
		return
	}

	if flag.NArg() != 1 {
		fmt.Fprintf(os.Stderr, "error: expected exactly one argument: <proto-dir>\n")
		flag.Usage()
		os.Exit(2)
	}
	protoDir := flag.Arg(0)

	if err := run(*base, protoDir); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
}

func run(baseRef, protoDir string) error {
	// Fast path: skip if no proto files changed.
	changed, err := lint.HasProtoChanges(baseRef, protoDir)
	if err != nil {
		return fmt.Errorf("checking for changes: %w", err)
	}
	if !changed {
		return nil
	}

	// Build descriptors from base ref.
	baseData, err := lint.BuildDescriptorsFromRef(protoDir, baseRef)
	if err != nil {
		return fmt.Errorf("building base descriptors: %w", err)
	}

	// Build descriptors from working tree.
	currentData, err := lint.BuildDescriptors(protoDir)
	if err != nil {
		return fmt.Errorf("building current descriptors: %w", err)
	}

	// Parse both.
	baseDefs, err := evaluator.ParseDescriptors(baseData)
	if err != nil {
		return fmt.Errorf("parsing base descriptors: %w", err)
	}
	currentDefs, err := evaluator.ParseDescriptors(currentData)
	if err != nil {
		return fmt.Errorf("parsing current descriptors: %w", err)
	}

	// Check for breaking changes.
	violations := lint.Check(baseDefs, currentDefs)
	if len(violations) == 0 {
		return nil
	}

	// Report violations.
	fmt.Fprintf(os.Stderr, "pbflags-lint: %d issue(s) found:\n\n", len(violations))
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  %s\n\n", v)
	}

	os.Exit(1)
	return nil // unreachable
}
