# Agent Setup Guide

Instructions for AI agents integrating pbflags into a consumer project. Follow these steps in order. This is the shortest reliable path from a clean repo to generated clients plus a working local evaluator.

## 1. Prerequisites

Verify these are available in the environment:

```bash
go version    # requires 1.26+
buf --version # requires buf CLI
```

If buf is missing: `go install github.com/bufbuild/buf/cmd/buf@latest`

You also need a PostgreSQL instance reachable by the `--database` DSN you will pass to `pbflags-admin`. The database user needs permission to create schemas, tables, and indexes (standalone mode runs migrations on startup).

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

// Required: exactly one message with this annotation.
// Each field is an evaluation context dimension (user, plan, etc).
message EvaluationContext {
  option (pbflags.context) = {};

  string user_id = 1 [(pbflags.dimension) = { description: "Authenticated user" hashable: true }];
  PlanLevel plan = 2 [(pbflags.dimension) = { description: "Subscription plan" }];
  bool is_internal = 3 [(pbflags.dimension) = { description: "Internal/dogfood user" }];
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
  }];
}
```

Key rules:
- Exactly one `EvaluationContext` message with `option (pbflags.context) = {}` across all proto files
- Each dimension is a field on that message annotated with `(pbflags.dimension)`
- Each feature is a `message` with `option (pbflags.feature)`
- Each flag is a field with `option (pbflags.flag)`
- Supported types: `bool`, `string`, `int64`, `double` (and `repeated` variants)
- Dimension-based targeting is configured via CEL conditions in YAML config, not in proto annotations
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

**Important:** `strategy: all` is required — the plugin needs all files in a single invocation to discover the `EvaluationContext` message.

## 6. Generate

```bash
buf generate --template buf.gen.flags.yaml
```

This produces one package per feature message. For Go, expect:
- `gen/flags/<feature>flags/` — interface, client, defaults, testing stub
- `gen/flags/dims/` — typed dimension constructors

## 7. Build descriptors for the server

```bash
buf build proto -o descriptors.pb
```

`pbflags-sync` and `pbflags-admin --standalone` read this descriptor set to discover feature and flag definitions at runtime.

## 8. Define flag behavior in YAML config

Flag behavior (who sees what value) is defined in YAML config files, not in the admin UI. The admin UI is read-only — operators use YAML configs checked into git for flag behavior, and the kill switch for emergencies.

Create a `features/` directory with one YAML file per feature. Cross-feature launches go in a `launches/` subdirectory:

```
features/
  notifications.proto          # feature proto definition
  notifications.yaml           # feature flag config
  billing.proto
  billing.yaml
  launches/
    pro-v2.yaml                # cross-feature launch (ID = filename)
```

```yaml
# features/notifications.yaml
feature: notifications
flags:
  email_enabled:
    conditions:
      - when: "ctx.is_internal"
        value: true
      - otherwise: false
  score_threshold:
    value: 0.75
```

Each flag can have:
- A `conditions` list — evaluated in order; the first matching `when` CEL expression wins
- An `otherwise` clause — used when no `when` matches (without one, the flag falls through to the compiled default)
- A static `value` — shorthand for a single-entry condition chain with no CEL expression
- A `launch` key on conditions or static values — per-condition override under a gradual rollout

CEL expressions reference evaluation context dimensions via `ctx.<field_name>` (e.g., `ctx.is_internal`, `ctx.plan == PlanLevel.ENTERPRISE`).

### Gradual rollouts (launches)

Define launches in the `launches:` section of a feature config. Each launch specifies a hash dimension and an initial ramp percentage. Bind launches to flags via `launch:` keys on individual conditions or static values:

```yaml
feature: notifications

launches:
  digest_rollout:
    dimension: user_id
    ramp_percentage: 25
    description: "Roll out hourly digest for Pro users"

flags:
  digest_frequency:
    conditions:
      - when: "ctx.plan == PlanLevel.PRO"
        value: "daily"
        launch:
          id: digest_rollout
          value: "hourly"    # Pro users in the 25% ramp get "hourly"
      - otherwise: "weekly"

  email_enabled:
    value: false
    launch:
      id: digest_rollout
      value: true            # entities in the ramp get true
```

Rules:
- Each condition may have at most **one** launch override.
- Launch dimensions must have `distribution: UNIFORM` in the proto definition.
- Feature-scoped launches (in a feature config) can only be referenced from that feature. Cross-feature launches go in a top-level `launches/` directory.
- `ramp_percentage` is only applied on first sync. Subsequent ramp changes are made via the admin UI or CLI.

### Validate configs in CI

Use `pbflags-sync validate` to catch syntax and CEL compilation errors before deploy:

```bash
pbflags-sync validate --descriptors=descriptors.pb --features=./features
```

This checks YAML structure, CEL expression compilation, and value type compatibility against the proto descriptors — all without a database connection.

### Inspect a flag's condition chain

```bash
pbflags-sync show --descriptors=descriptors.pb --features=./features notifications/email_enabled
```

### Project config file

To avoid repeating `--features` and `--descriptors` on every command, create a `.pbflags.yaml` at the project root:

```yaml
# .pbflags.yaml
features_path: features
```

When `features_path` is set, `pbflags-sync`, `validate`, and `show` automatically use it as the default `--features` directory.

## 9. Use in application code

### Go

```go
import (
    "context"
    "net/http"

    "github.com/SpotlightGOV/pbflags/pbflags"
    "<MODULE>/gen/flags/<feature>flags"
    "<MODULE>/gen/flags/dims"
    pb "<MODULE>/gen/flags/<proto_package>"
)

// Create a base evaluator.
eval := pbflags.Connect(http.DefaultClient, "http://localhost:9201", &pb.EvaluationContext{})

// Bind dimensions — With() returns a new evaluator (immutable).
scoped := eval.With(dims.UserID("user-123"))
<feature> := <feature>flags.New(scoped)
val := <feature>.<FlagName>(context.Background())

// Multiple dimensions:
scoped := eval.With(
    dims.UserID("user-123"),
    dims.Plan(pb.PlanLevel_PLAN_LEVEL_PRO),
    dims.IsInternal(true),
)
<feature> := <feature>flags.New(scoped)
val := <feature>.<FlagName>(ctx)

// Context propagation:
ctx = pbflags.ContextWith(ctx, eval.With(dims.UserID("user-123")))
// ... later, in a handler or service:
<feature> := <feature>flags.New(pbflags.FromContext(ctx))
val := <feature>.<FlagName>(ctx)

// Without an evaluator (compiled defaults only):
<feature> := <feature>flags.Defaults()
val := <feature>.<FlagName>(context.Background())
```

Evaluation errors (network failures, type mismatches) are logged via `slog.Default()` and the compiled default is returned. To use a custom logger:

```go
import "<MODULE>/gen/flags/flagmeta"

<feature> := <feature>flags.New(eval, flagmeta.WithLogger(myLogger))
```

Note: flag methods take only `context.Context` — dimensions are bound on the evaluator via `With()`, not passed per-call.

### Java

```java
import com.yourorg.flags.generated.<Feature>Flags;
import com.yourorg.flags.generated.Dimensions;
import com.yourorg.flags.proto.EvaluationContext;
import com.yourorg.flags.proto.PlanLevel;
import org.spotlightgov.pbflags.FlagEvaluator;
import org.spotlightgov.pbflags.FlagEvaluatorClient;

// All formats accepted: "localhost:9201", "http://localhost:9201", "https://host:9201"
FlagEvaluatorClient eval =
    new FlagEvaluatorClient("localhost:9201", EvaluationContext.getDefaultInstance());

FlagEvaluator scoped = eval.with(
    Dimensions.userId("user-123"),
    Dimensions.plan(PlanLevel.PLAN_LEVEL_PRO)
);
<Feature>Flags <feature> = <Feature>Flags.forEvaluator(scoped);
boolean val = <feature>.<flagName>().get();
```

For Java consumers, add the runtime dependency using the same release version as the pbflags binaries/plugin you are integrating:

```groovy
implementation("org.spotlightgov.pbflags:pbflags-java:<pbflags-version>")
```

## 10. Run the evaluator

For local development, the easiest path is standalone mode:

```bash
go install github.com/SpotlightGOV/pbflags/cmd/pbflags-admin@latest

pbflags-admin --standalone \
  --descriptors=descriptors.pb \
  --features=./features \
  --database=postgres://user:pass@localhost:5432/dbname
```

This starts the admin UI and evaluator in one process. The embedded evaluator listens on `:9201` by default.

If the consumer project needs the production topology instead of standalone mode, use [deployment.md](deployment.md) after the local integration path is working.

## 11. Verify

- `buf generate --template buf.gen.flags.yaml` succeeds
- `buf build proto -o descriptors.pb` succeeds
- For Go, the generated `dims/` package exists
- `pbflags-sync validate --descriptors=descriptors.pb --features=./features` passes (if using YAML configs)
- `pbflags-admin --standalone --descriptors=descriptors.pb --features=./features ...` starts cleanly
- A sample flag read returns the compiled default before any config changes

## Common mistakes

- **Missing `strategy: all`** in buf.gen.yaml — the plugin silently generates nothing if it can't find the `EvaluationContext` message.
- **Wrong `package_prefix`** — must match the Go module path exactly, including the output directory.
- **Forgetting `buf dep update`** after upgrading pbflags — the context/dimension annotations won't be found with stale BSR deps.
- **Forgetting `buf build ... -o descriptors.pb`** — code generation alone is not enough; the server reads the descriptor set.
- **`protoc-gen-pbflags` not on `PATH`** — `buf generate` cannot invoke a `local:` plugin unless the binary is discoverable.
