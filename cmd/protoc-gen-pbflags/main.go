// protoc-gen-pbflags generates type-safe flag client code from feature proto definitions.
//
// Usage via buf.gen.yaml:
//
//	plugins:
//	  - local: protoc-gen-pbflags
//	    out: gen/flags
//	    opt:
//	      - lang=go
//	      - package_prefix=github.com/yourorg/yourrepo/gen/pbflags
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/SpotlightGOV/pbflags/internal/codegen/gogen"
	"github.com/SpotlightGOV/pbflags/internal/codegen/javagen"
	"google.golang.org/protobuf/compiler/protogen"
	pluginpb "google.golang.org/protobuf/types/pluginpb"
)

// version is set via -ldflags at build time (see .goreleaser.yml).
var version = "dev" //lint:ignore U1000 injected by linker

func main() {
	var flags flag.FlagSet
	lang := flags.String("lang", "", "output language: go, java, typescript, rust, node")
	packagePrefix := flags.String("package_prefix", "", "Go import path prefix for generated packages")
	javaPackage := flags.String("java_package", "", "Java package for generated classes")
	javaDagger := flags.Bool("java_dagger", false, "generate Dagger @Module and add @Inject/@Singleton to Java impls")

	opts := protogen.Options{ParamFunc: flags.Set}
	opts.Run(func(plugin *protogen.Plugin) error {
		plugin.SupportedFeatures = uint64(pluginpb.CodeGeneratorResponse_FEATURE_PROTO3_OPTIONAL)
		switch *lang {
		case "go":
			return gogen.Generate(plugin, *packagePrefix)
		case "java":
			if *javaPackage == "" {
				return fmt.Errorf("protoc-gen-pbflags: --java_package is required for lang=java")
			}
			return javagen.Generate(plugin, *javaPackage, *javaDagger)
		case "typescript":
			_, _ = fmt.Fprintf(os.Stderr, "protoc-gen-pbflags: typescript output not yet implemented\n")
			return nil
		case "rust":
			_, _ = fmt.Fprintf(os.Stderr, "protoc-gen-pbflags: rust output not yet implemented\n")
			return nil
		case "node":
			_, _ = fmt.Fprintf(os.Stderr, "protoc-gen-pbflags: node output not yet implemented\n")
			return nil
		case "":
			return fmt.Errorf("protoc-gen-pbflags: --lang is required (go, java, typescript, rust, node)")
		default:
			return fmt.Errorf("protoc-gen-pbflags: unsupported language: %s", *lang)
		}
	})
}
