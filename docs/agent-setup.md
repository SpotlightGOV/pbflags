# Agent Setup Guide

Instructions for AI agents integrating pbflags into a consumer project. Follow these steps in order.

## 1. Prerequisites

Verify these are available in the environment:

```bash
go version    # requires 1.26+
buf --version # requires buf CLI
```

If buf is missing: `go install github.com/bufbuild/buf/cmd/buf@latest`

## 2. Install the codegen plugin

```bash
go install github.com/SpotlightGOV/pbflags/cmd/protoc-gen-pbflags@latest
```

Verify: `which protoc-gen-pbflags`

## 3. Add proto dependency

Add to `buf.yaml` (create if it doesn't exist):

```yaml
version: v2
modules:
  - path: proto
deps:
  - buf.build/spotlightgov/pbflags
```

Then pull:

```bash
buf dep update
```

## 4. Define flags

Create a `.proto` file (e.g., `proto/flags/myflags.proto`):

```protobuf
syntax = "proto3";
package myproject.flags;
import "pbflags/options.proto";

// Required: exactly one enum with this annotation.
// Add one entry per override dimension (user, tenant, etc).
enum Layer {
  option (pbflags.layers) = true;
  LAYER_UNSPECIFIED = 0;
  LAYER_GLOBAL = 1;
  LAYER_USER = 2;
}

message MyFeature {
  option (pbflags.feature) = {
    id: "my_feature"
    description: "Feature description"
    owner: "team-name"
  };

  bool enabled = 1 [(pbflags.flag) = {
    description: "Enable the feature"
    default: { bool_value: { value: false } }
    layer: "user"  // per-user override; omit for global-only
  }];
}
```

Key rules:
- Exactly one `Layer` enum with `option (pbflags.layers) = true` across all proto files
- Each feature is a `message` with `option (pbflags.feature)`
- Each flag is a field with `option (pbflags.flag)`
- Supported types: `bool`, `string`, `int64`, `double` (and `repeated` variants)
- `layer` is optional; omit for global-only flags
- Flag identity is `feature_id/field_number` — field numbers are immutable

## 5. Configure codegen

### Go

Create `buf.gen.flags.yaml`:

```yaml
version: v2
plugins:
  - local: protoc-gen-pbflags
    out: gen/flags
    strategy: all
    opt:
      - lang=go
      - package_prefix=<MODULE>/gen/flags
inputs:
  - directory: proto
```

Replace `<MODULE>` with the project's Go module path (from `go.mod`).

### Java

```yaml
version: v2
plugins:
  - local: protoc-gen-pbflags
    out: src/main/java
    strategy: all
    opt:
      - lang=java
      - java_package=com.yourorg.flags.generated
inputs:
  - directory: proto
```

Add `java_dagger=true` to `opt` if the project uses Dagger.

**Important:** `strategy: all` is required — the plugin needs all files in a single invocation to discover the Layer enum.

## 6. Generate

```bash
buf generate --template buf.gen.flags.yaml
```

This produces one package per feature message. For Go, expect:
- `gen/flags/<feature>flags/` — interface, client, defaults, testing stub
- `gen/flags/layers/` — typed layer ID wrappers

## 7. Use in application code

### Go

```go
import (
    "<MODULE>/gen/flags/<feature>flags"
    "<MODULE>/gen/flags/layers"
)

// With an evaluator connection:
client := <feature>flags.New<Feature>FlagsClient(evaluatorClient)
val := client.<FlagName>(ctx, layers.User("user-123"))

// Without an evaluator (compiled defaults only):
client := <feature>flags.Defaults()
val := client.<FlagName>(ctx, layers.UserID{})
```

### Java

```java
<Feature>Flags flags = <Feature>Flags.forEvaluator(evaluatorClient);
boolean val = flags.<flagName>().get(UserID.of("user-123"));
```

## 8. Run the evaluator

For local development, the easiest path is Docker:

```bash
# If the consumer repo doesn't have a docker-compose for pbflags,
# run standalone directly:
go install github.com/SpotlightGOV/pbflags/cmd/pbflags-admin@latest

pbflags-admin --standalone \
  --descriptors=<path-to-descriptors.pb> \
  --database=postgres://user:pass@localhost:5432/dbname
```

To generate `descriptors.pb` from the proto files:

```bash
buf build proto -o descriptors.pb
```

The evaluator listens on `localhost:9201` by default.

## Common mistakes

- **Missing `strategy: all`** in buf.gen.yaml — the plugin silently generates nothing if it can't find the Layer enum.
- **Wrong `package_prefix`** — must match the Go module path exactly, including the output directory.
- **Forgetting `buf dep update`** after upgrading pbflags — the Layer annotation won't be found with stale BSR deps.
- **Using field names as flag IDs** — flag identity is `feature_id/field_number`, not `feature_id/field_name`. Field names can be renamed safely.
