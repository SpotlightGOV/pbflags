package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/SpotlightGOV/pbflags/internal/projectconfig"
)

// runFeature dispatches `pb feature <subcommand>`.
func runFeature(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "pb feature: missing subcommand (new)")
		os.Exit(1)
	}
	switch args[0] {
	case "new":
		runFeatureNew(args[1:])
	case "-h", "--help", "help":
		fmt.Println(`pb feature — feature schema (.proto) commands

Usage:
  pb feature new <name>     Scaffold proto/<name>/<name>.proto

After scaffolding, run your usual codegen step (e.g. make generate)
to produce descriptors.pb before using pb config new.`)
	default:
		fmt.Fprintf(os.Stderr, "pb feature: unknown subcommand %q\n", args[0])
		os.Exit(1)
	}
}

// runFeatureNew implements `pb feature new <name>`. Writes a fresh
// proto/<name>/<name>.proto with the standard preamble and an empty
// feature message. Package paths are sourced from .pbflags.yaml when
// present (go_package_prefix, java_package_prefix); otherwise we emit
// TODO placeholders. The proto sits in its own directory because that's
// the convention buf+go expect (one Go package per proto package).
func runFeatureNew(args []string) {
	fs := flag.NewFlagSet("pb feature new", flag.ExitOnError)
	force := fs.Bool("force", false, "Overwrite an existing proto file")
	fs.Parse(args)

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: pb feature new <name>")
		os.Exit(1)
	}
	name := fs.Arg(0)
	if err := validateFeatureName(name); err != nil {
		fmt.Fprintf(os.Stderr, "pb feature new: %v\n", err)
		os.Exit(1)
	}

	projCfg, projRoot, _ := projectconfig.Discover(".")
	protoDir := projCfg.ProtoDir(projRoot)
	if protoDir == "" {
		// No .pbflags.yaml or no proto_path — fall back to ./proto so the
		// command still does something useful in a fresh checkout.
		protoDir = "proto"
	}

	var target string
	switch projCfg.FeatureLayout {
	case "flat":
		// All features share one directory and one Go/Java package.
		target = filepath.Join(protoDir, name+".proto")
	case "", "nested":
		// One subdir per feature so each gets its own Go package
		// (the buf+protoc-gen-go default).
		target = filepath.Join(protoDir, name, name+".proto")
	default:
		fmt.Fprintf(os.Stderr, "pb feature new: invalid feature_layout %q in .pbflags.yaml (want \"flat\" or \"nested\")\n", projCfg.FeatureLayout)
		os.Exit(1)
	}
	if !*force {
		if _, err := os.Stat(target); err == nil {
			fmt.Fprintf(os.Stderr, "pb feature new: %s already exists (use --force to overwrite)\n", target)
			os.Exit(1)
		}
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		fatal(err)
	}

	contents := renderFeatureProto(name, projCfg)
	if err := os.WriteFile(target, []byte(contents), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("Wrote %s\n", target)
	fmt.Println("Next steps:")
	fmt.Println("  - Add flag fields to the message")
	fmt.Println("  - Regenerate descriptors (e.g. make generate)")
	fmt.Printf("  - pb config new %s\n", name)
}

// renderFeatureProto returns the contents of a freshly scaffolded
// feature .proto file. Package prefixes come from the project config
// when set; otherwise TODO comments are emitted so the operator can't
// silently ship a file with someone else's go_package.
func renderFeatureProto(name string, cfg projectconfig.Config) string {
	msgName := pascalCase(name)
	var b strings.Builder
	fmt.Fprintf(&b, "syntax = \"proto3\";\npackage %s;\n\n", name)
	b.WriteString("import \"pbflags/options.proto\";\n\n")

	flat := cfg.FeatureLayout == "flat"
	if cfg.GoPackagePrefix != "" {
		goPrefix := strings.TrimRight(cfg.GoPackagePrefix, "/")
		if flat {
			// All features share one Go package; emit only the prefix.
			fmt.Fprintf(&b, "option go_package = %q;\n", goPrefix)
		} else {
			fmt.Fprintf(&b, "option go_package = \"%s/%s;%spb\";\n", goPrefix, name, name)
		}
	} else {
		if flat {
			b.WriteString("// TODO: set go_package — e.g. \"github.com/myorg/myapp/gen\"\n")
		} else {
			b.WriteString("// TODO: set go_package — e.g. \"github.com/myorg/myapp/gen/" + name + ";" + name + "pb\"\n")
		}
		b.WriteString("// option go_package = \"\";\n")
	}
	if cfg.JavaPackagePrefix != "" {
		b.WriteString("option java_multiple_files = true;\n")
		javaPrefix := strings.TrimRight(cfg.JavaPackagePrefix, ".")
		if flat {
			fmt.Fprintf(&b, "option java_package = %q;\n", javaPrefix)
		} else {
			fmt.Fprintf(&b, "option java_package = \"%s.%s.proto\";\n", javaPrefix, name)
		}
	} else {
		if flat {
			b.WriteString("// TODO: set java_package — e.g. \"org.myorg.myapp\"\n")
		} else {
			b.WriteString("// TODO: set java_package — e.g. \"org.myorg.myapp." + name + ".proto\"\n")
		}
		b.WriteString("// option java_multiple_files = true;\n")
		b.WriteString("// option java_package = \"\";\n")
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "message %s {\n", msgName)
	fmt.Fprintf(&b, "  option (pbflags.feature) = {\n")
	fmt.Fprintf(&b, "    id: %q\n", name)
	fmt.Fprintf(&b, "    description: \"TODO: describe %s\"\n", name)
	fmt.Fprintf(&b, "    owner: \"TODO\"\n")
	fmt.Fprintf(&b, "  };\n")
	fmt.Fprintf(&b, "\n")
	fmt.Fprintf(&b, "  // Add flag fields here. Example:\n")
	fmt.Fprintf(&b, "  // bool example_enabled = 1 [(pbflags.flag) = {\n")
	fmt.Fprintf(&b, "  //   description: \"What this flag controls\"\n")
	fmt.Fprintf(&b, "  //   default: { bool_value: { value: false } }\n")
	fmt.Fprintf(&b, "  // }];\n")
	fmt.Fprintf(&b, "  //\n")
	fmt.Fprintf(&b, "  // Or scaffold one with: pb flag new %s <flag_name> [--type=str|int|double|bool|[]str|...]\n", name)
	b.WriteString("}\n")
	return b.String()
}

// validateFeatureName rejects names that would produce invalid proto
// package identifiers. proto3 packages are lowercase ASCII with
// underscores; we mirror that here.
func validateFeatureName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9' && i > 0:
		case r == '_' && i > 0:
		default:
			return fmt.Errorf("invalid character %q at position %d (lowercase letters, digits, and underscores only; must start with a letter)", r, i)
		}
	}
	return nil
}

// pascalCase converts snake_case feature names ("user_auth") into
// PascalCase message names ("UserAuth").
func pascalCase(name string) string {
	var b strings.Builder
	upNext := true
	for _, r := range name {
		if r == '_' {
			upNext = true
			continue
		}
		if upNext {
			b.WriteRune(unicode.ToUpper(r))
			upNext = false
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
