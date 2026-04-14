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

// Scope definitions — globally required dims are implicit in every scope.
option (pbflags.scope) = { name: "anon" };
option (pbflags.scope) = { name: "user", dimensions: ["user_id"] };

// Required: exactly one message with this annotation.
// Each field is an evaluation context dimension (user, plan, etc).
message EvaluationContext {
  option (pbflags.context) = {};

  string session_id = 1 [(pbflags.dimension) = {
    description: "Stable session identifier"
    distribution: DIMENSION_DISTRIBUTION_UNIFORM
    presence: DIMENSION_PRESENCE_REQUIRED
  }];
  string user_id = 2 [(pbflags.dimension) = {
    description: "Authenticated user"
    distribution: DIMENSION_DISTRIBUTION_UNIFORM
    presence: DIMENSION_PRESENCE_OPTIONAL
  }];
  PlanLevel plan = 3 [(pbflags.dimension) = {
    description: "Subscription plan"
    presence: DIMENSION_PRESENCE_OPTIONAL
  }];
  bool is_internal = 4 [(pbflags.dimension) = {
    description: "Internal/dogfood user"
    presence: DIMENSION_PRESENCE_OPTIONAL
  }];
}

message MyFeature {
  option (pbflags.feature) = {
    id: "my_feature"
    description: "Feature description"
    owner: "team-name"
    scopes: ["anon", "user"]
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
- Launch dimensions must have `distribution: UNIFORM` in the proto definition (high cardinality, suitable for hash-based traffic splitting).
- The launch dimension must be present in **every scope** of every affected feature. For example, a launch on `user_id` cannot be used by features available in the `anon` scope (which lacks `user_id`). Use a globally required dimension like `session_id` if the launch must span all scopes.
- Feature-scoped launches (in a feature config) can only be referenced from that feature. Cross-feature launches go in a top-level `launches/` directory.
- When `ramp_percentage` is set in config, it is **authoritative** — sync always writes it to the database. CLI/UI ramp changes are ephemeral and will be overwritten on next sync. When `ramp_percentage` is omitted from config, CLI/UI ramp changes persist across syncs.
- Use `pb launch ramp` or the admin UI for incident response — the ramp change takes effect immediately, though it will be overwritten on next sync if the launch has `ramp_percentage` in config.

Launch lifecycle commands (require admin API):

```bash
pb launch ramp <id> <pct>   # Set ramp percentage (0-100)
pb launch soak <id>          # Set ramp to 100% and status to SOAKING
pb launch land <id>          # Promote launch values to defaults, remove launch from config
pb launch abandon <id>       # Set status to ABANDONED (launch will not be landed)
pb launch kill <id>          # Emergency disable (reversible)
pb launch unkill <id>        # Restore a killed launch
```

`pb launch land` transforms the YAML config files: it replaces each condition's `value` with the launch override value, removes `launch:` keys, deletes the launch definition, sets status to COMPLETED, and opens a PR. The launch must be SOAKING to land. Use `--dry-run` to preview changes without writing, or `--no-pr` to skip PR creation.

### Validate and format configs in CI

Use `pb validate` to catch syntax and CEL compilation errors before deploy:

```bash
pb validate --descriptors=descriptors.pb --features=./features
```

This checks YAML structure, CEL expression compilation, and value type compatibility against the proto descriptors — all without a database connection.

Use `pb format` to enforce canonical YAML formatting. It round-trips each config file through the parser, catching lossy parsing as a side effect:

```bash
pb format --descriptors=descriptors.pb --features=./features          # rewrite files
pb format --descriptors=descriptors.pb --features=./features --check  # CI mode: exit 1 if unformatted
```

### Inspect a flag's condition chain

```bash
pb show --descriptors=descriptors.pb --features=./features notifications/email_enabled
```

### Project config file

To avoid repeating flags on every command, create a `.pbflags.yaml` at the project root:

```yaml
# .pbflags.yaml
features_path: features
descriptors_path: descriptors.pb
proto_path: proto
```

All paths are resolved relative to the `.pbflags.yaml` location. When set, `pb sync`, `pb validate`, `pb format`, `pb show`, `pb compile`, and `pb lint` automatically use the configured defaults.

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

#### Scope-based access (recommended)

If your proto defines evaluation scopes, prefer the generated `*Features` types over manual `With()` calls. Scope constructors require their dimensions as typed parameters — missing a dimension is a compile error:

```go
import "<MODULE>/gen/flags/dims"

// Scope constructors enforce dimension contracts at compile time.
userFeatures := dims.NewUserFeatures(eval, sessionID, userID)
val := userFeatures.<Feature>().<FlagName>(ctx)

// Anonymous scope — only session_id required.
anonFeatures := dims.NewAnonFeatures(eval, sessionID)
val := anonFeatures.<Feature>().<FlagName>(ctx)

// Context storage — store once in middleware, retrieve in handlers.
ctx = dims.ContextWithUserFeatures(ctx, userFeatures)
// ... later:
userFeatures := dims.UserFeaturesFrom(ctx)

// Duck-typed interfaces let handlers declare what they need.
func handleNotification(features dims.HasNotifications) {
    freq := features.Notifications().DigestFrequency(ctx)
}
```

See [Go client docs](go.md#scope-based-access-recommended) for full details.

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
