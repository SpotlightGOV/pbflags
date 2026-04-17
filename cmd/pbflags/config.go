package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	pbflagsv1 "github.com/SpotlightGOV/pbflags/gen/pbflags/v1"
	"github.com/SpotlightGOV/pbflags/internal/configfile"
	"github.com/SpotlightGOV/pbflags/internal/evaluator"
	"github.com/SpotlightGOV/pbflags/internal/projectconfig"
)

// runConfig dispatches `pb config <subcommand>`. Subcommands include
// the newly-added scaffolder (`new`) and the rehomed flat commands
// (`show`, `compile`, `load`, `format`, `validate`, `export`).
func runConfig(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, configHelp)
		os.Exit(1)
	}
	switch args[0] {
	case "new":
		runConfigNew(args[1:])
	case "show":
		runShow(args[1:])
	case "compile":
		runCompile(args[1:])
	case "load":
		runLoad(args[1:])
	case "format":
		runFormat(args[1:])
	case "validate":
		runValidate(args[1:])
	case "export":
		runExport(args[1:])
	case "-h", "--help", "help":
		fmt.Println(configHelp)
	default:
		fmt.Fprintf(os.Stderr, "pb config: unknown subcommand %q\n\n%s\n", args[0], configHelp)
		os.Exit(1)
	}
}

const configHelp = `pb config — YAML feature config commands

Usage:
  pb config new <feature>     Scaffold features/<feature>.yaml from proto defaults
  pb config show <flag>       Show resolved config for a specific flag
  pb config validate          Validate YAML configs against proto descriptors
  pb config format            Format YAML configs into canonical form
  pb config compile           Compile YAML configs into a binary bundle
  pb config load              Load a compiled bundle into the database
  pb config export            Export DB state as YAML config files

Most subcommands accept --descriptors / --features (or PBFLAGS_DESCRIPTORS /
PBFLAGS_FEATURES env vars), and fall back to .pbflags.yaml when present.`

// runConfigNew scaffolds a YAML config file for <feature> from its proto
// definition. Each flag in the proto becomes a top-level entry whose
// `value:` is the proto-declared default (falling back to the type's
// zero value when no default is set). The file is written to
// <features-dir>/<feature>.yaml.
func runConfigNew(args []string) {
	fs := flag.NewFlagSet("pb config new", flag.ExitOnError)
	descriptors := fs.String("descriptors", "", "path to descriptors.pb (or PBFLAGS_DESCRIPTORS)")
	configDir := fs.String("features", "", "directory to write the YAML config (or PBFLAGS_FEATURES)")
	force := fs.Bool("force", false, "Overwrite an existing config file")
	stdout := fs.Bool("stdout", false, "Write to stdout instead of the features directory")
	fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: pb config new <feature>")
		os.Exit(1)
	}
	feature := fs.Arg(0)

	resolveEnv(nil, descriptors, configDir, nil)
	resolveProjectConfig(descriptors, configDir)

	if *descriptors == "" {
		fmt.Fprintln(os.Stderr, "pb config new: --descriptors is required (set the flag, PBFLAGS_DESCRIPTORS, or descriptors_path in .pbflags.yaml)")
		os.Exit(1)
	}

	descData, err := os.ReadFile(*descriptors)
	if err != nil {
		fatal(fmt.Errorf("read descriptors: %w", err))
	}
	defs, err := evaluator.ParseDescriptors(descData)
	if err != nil {
		fatal(fmt.Errorf("parse descriptors: %w", err))
	}

	var featureDefs []evaluator.FlagDef
	for _, d := range defs {
		if d.FeatureID == feature {
			featureDefs = append(featureDefs, d)
		}
	}
	if len(featureDefs) == 0 {
		fmt.Fprintf(os.Stderr, "pb config new: feature %q not found in descriptors %s\n", feature, *descriptors)
		os.Exit(1)
	}

	cfg := &configfile.Config{
		Feature: feature,
		Flags:   make(map[string]configfile.FlagEntry, len(featureDefs)),
	}
	for _, d := range featureDefs {
		val := d.Default
		if val == nil {
			val = zeroFlagValue(d.FlagType)
		}
		cfg.Flags[d.Name] = configfile.FlagEntry{Value: val}
	}

	yamlBytes, err := configfile.Marshal(cfg)
	if err != nil {
		fatal(fmt.Errorf("marshal: %w", err))
	}

	if *stdout {
		os.Stdout.Write(yamlBytes)
		return
	}

	dir := *configDir
	if dir == "" {
		// No features_path configured — fall back to ./features so the
		// command still does something useful in a fresh checkout.
		dir = "features"
	}
	target := filepath.Join(dir, feature+".yaml")
	if !*force {
		if _, err := os.Stat(target); err == nil {
			fmt.Fprintf(os.Stderr, "pb config new: %s already exists (use --force to overwrite)\n", target)
			os.Exit(1)
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		fatal(err)
	}
	if err := os.WriteFile(target, yamlBytes, 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("Wrote %s (%d flag%s)\n", target, len(featureDefs), plural(len(featureDefs)))

	// Hint the next move so the operator doesn't have to remember,
	// but only when we're inside a configured project (so a fresh-checkout
	// scaffold doesn't lie about a sync target that doesn't exist).
	if _, projRoot, _ := projectconfig.Discover("."); projRoot != "" {
		fmt.Println("Next: pb sync")
	}
}

// zeroFlagValue returns a typed zero FlagValue for use when a proto
// flag has no `default` set. We emit *something* so the YAML scaffold
// is immediately valid; the operator can edit afterwards.
func zeroFlagValue(t pbflagsv1.FlagType) *pbflagsv1.FlagValue {
	switch t {
	case pbflagsv1.FlagType_FLAG_TYPE_BOOL:
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolValue{BoolValue: false}}
	case pbflagsv1.FlagType_FLAG_TYPE_STRING:
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringValue{StringValue: ""}}
	case pbflagsv1.FlagType_FLAG_TYPE_INT64:
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64Value{Int64Value: 0}}
	case pbflagsv1.FlagType_FLAG_TYPE_DOUBLE:
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleValue{DoubleValue: 0}}
	case pbflagsv1.FlagType_FLAG_TYPE_BOOL_LIST:
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_BoolListValue{BoolListValue: &pbflagsv1.BoolList{}}}
	case pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST:
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_StringListValue{StringListValue: &pbflagsv1.StringList{}}}
	case pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST:
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_Int64ListValue{Int64ListValue: &pbflagsv1.Int64List{}}}
	case pbflagsv1.FlagType_FLAG_TYPE_DOUBLE_LIST:
		return &pbflagsv1.FlagValue{Value: &pbflagsv1.FlagValue_DoubleListValue{DoubleListValue: &pbflagsv1.DoubleList{}}}
	}
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
