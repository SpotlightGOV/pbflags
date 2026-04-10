# Agent Setup Guide

Instructions for AI agents integrating pbflags into a consumer project. Follow these steps in order. This is the shortest reliable path from a clean repo to generated clients plus a working local evaluator.

## 1. Prerequisites

Verify these are available in the environment:

```bash
go version    # requires 1.26+
buf --version # requires buf CLI
```

If buf is missing: `go install github.com/bufbuild/buf/cmd/buf@latest`

You also need a PostgreSQL instance reachable by the `--database` DSN you will pass to `pbflags-admin` or `pbflags-sync`.

## 2. Install the codegen plugin

```bash
go install github.com/SpotlightGOV/pbflags/cmd/protoc-gen-pbflags@latest
```

Verify: `which protoc-gen-pbflags`

If `which` cannot find it, add `$(go env GOPATH)/bin` to `PATH` before running `buf generate`.

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

Create a dedicated `buf.gen.flags.yaml`. This is separate from any existing protobuf codegen template the consumer repo already uses for normal protobuf stubs.

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

## 7. Build descriptors for the server

```bash
buf build proto -o descriptors.pb
```

`pbflags-sync` and `pbflags-admin --standalone` read this descriptor set to discover feature and flag definitions at runtime.

## 8. Use in application code

### Go

```go
import (
    "context"
    "net/http"

    "<MODULE>/gen/flags/<feature>flags"
    "<MODULE>/gen/flags/layers"
    "<MODULE>/gen/flags/v1/pbflagsv1connect"
)

evaluatorClient := pbflagsv1connect.NewFlagEvaluatorServiceClient(
    http.DefaultClient,
    "http://localhost:9201",
)
client := <feature>flags.New<Feature>FlagsClient(evaluatorClient)
val := client.<FlagName>(context.Background(), layers.User("user-123"))

// Without an evaluator (compiled defaults only):
client := <feature>flags.Defaults()
val := client.<FlagName>(context.Background(), layers.UserID{})
```

### Java

```java
import com.yourorg.flags.generated.<Feature>Flags;
import com.yourorg.flags.generated.layers.UserID;
import org.spotlightgov.pbflags.FlagEvaluatorClient;

FlagEvaluatorClient evaluatorClient = new FlagEvaluatorClient("localhost:9201");
<Feature>Flags flags = <Feature>Flags.forEvaluator(evaluatorClient);
boolean val = flags.<flagName>().get(UserID.of("user-123"));
```

For Java consumers, add the runtime dependency using the same release version as the pbflags binaries/plugin you are integrating:

```groovy
implementation("org.spotlightgov.pbflags:pbflags-java:<pbflags-version>")
```

## 9. Run the evaluator

For local development, the easiest path is standalone mode:

```bash
go install github.com/SpotlightGOV/pbflags/cmd/pbflags-admin@latest

pbflags-admin --standalone \
  --descriptors=descriptors.pb \
  --database=postgres://user:pass@localhost:5432/dbname
```

This starts the admin UI and evaluator in one process. The embedded evaluator listens on `:9201` by default.

If the consumer project needs the production topology instead of standalone mode, use [deployment.md](deployment.md) after the local integration path is working.

## 10. Verify

- `buf generate --template buf.gen.flags.yaml` succeeds
- `buf build proto -o descriptors.pb` succeeds
- The generated `layers/` package or classes exist
- `pbflags-admin --standalone --descriptors=descriptors.pb ...` starts cleanly
- A sample flag read returns the compiled default before any admin changes

## Common mistakes

- **Missing `strategy: all`** in buf.gen.yaml — the plugin silently generates nothing if it can't find the Layer enum.
- **Wrong `package_prefix`** — must match the Go module path exactly, including the output directory.
- **Forgetting `buf dep update`** after upgrading pbflags — the Layer annotation won't be found with stale BSR deps.
- **Forgetting `buf build ... -o descriptors.pb`** — code generation alone is not enough; the server reads the descriptor set.
- **`protoc-gen-pbflags` not on `PATH`** — `buf generate` cannot invoke a `local:` plugin unless the binary is discoverable.
- **Using field names as flag IDs** — flag identity is `feature_id/field_number`, not `feature_id/field_name`. Field names can be renamed safely.
