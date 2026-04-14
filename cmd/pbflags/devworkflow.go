package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/lint"
)

// --- lint ---

func runLint(args []string) {
	fs := flag.NewFlagSet("pb lint", flag.ExitOnError)
	base := fs.String("base", "HEAD", "Git ref to compare against")
	jsonOut := fs.Bool("json", false, "Output as JSON")
	fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: pb lint [--base <ref>] <proto-dir>")
		os.Exit(2)
	}
	protoDir := fs.Arg(0)

	changed, err := lint.HasProtoChanges(*base, protoDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	if !changed {
		if *jsonOut {
			printJSON(map[string]any{"violations": []string{}, "changed": false})
		}
		return
	}

	baseData, err := lint.BuildDescriptorsFromRef(protoDir, *base)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	currentData, err := lint.BuildDescriptors(protoDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	baseDefs, err := evaluator.ParseDescriptors(baseData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}
	currentDefs, err := evaluator.ParseDescriptors(currentData)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(2)
	}

	violations := lint.Check(baseDefs, currentDefs)

	baseScopes, baseFeatures, err := lint.ExtractScopesFromDescriptors(baseData)
	if err == nil {
		currentScopes, currentFeatures, err := lint.ExtractScopesFromDescriptors(currentData)
		if err == nil {
			violations = append(violations, lint.CheckScopes(baseScopes, currentScopes, baseFeatures, currentFeatures)...)
		}
	}

	if *jsonOut {
		printJSON(map[string]any{"violations": violations, "changed": true})
		if len(violations) > 0 {
			os.Exit(1)
		}
		return
	}

	if len(violations) == 0 {
		return
	}

	fmt.Fprintf(os.Stderr, "%d issue(s) found:\n\n", len(violations))
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  %s\n\n", v)
	}
	os.Exit(1)
}

// --- init ---

func runInit(args []string) {
	fs := flag.NewFlagSet("pb init", flag.ExitOnError)
	featuresPath := fs.String("features", "features", "Relative path for feature config directory")
	fs.Parse(args)

	if _, err := os.Stat(".pbflags.yaml"); err == nil {
		fmt.Fprintln(os.Stderr, "error: .pbflags.yaml already exists in this directory")
		os.Exit(1)
	}

	configContent := fmt.Sprintf("features_path: %s\n", *featuresPath)
	if err := os.WriteFile(".pbflags.yaml", []byte(configContent), 0o644); err != nil {
		fatal(err)
	}

	if err := os.MkdirAll(*featuresPath, 0o755); err != nil {
		fatal(err)
	}

	exampleConfig := `# Example feature flag configuration.
# See docs/philosophy.md for design principles.
feature: example

flags:
  enabled:
    value: false
  # Use conditions for targeted rollout:
  # enabled:
  #   conditions:
  #     - when: 'ctx.environment == "staging"'
  #       value: true
  #     - value: false
`
	examplePath := filepath.Join(*featuresPath, "example.yaml")
	if err := os.WriteFile(examplePath, []byte(exampleConfig), 0o644); err != nil {
		fatal(err)
	}

	fmt.Println("Initialized pbflags project:")
	fmt.Printf("  .pbflags.yaml\n")
	fmt.Printf("  %s/example.yaml\n", *featuresPath)
}
