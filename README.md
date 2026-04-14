# pbflags

Protocol Buffer-based feature flags with type-safe code generation, multi-tier caching, and a never-throw guarantee.

> **Note:** This project is a learning exercise and research exploration into protobuf-driven feature flag design. It was extracted from a production system to study the patterns independently. If you're building a real product and need feature flags, you probably want [Flipt](https://github.com/flipt-io/flipt), [OpenFeature](https://openfeature.dev/), or [Unleash](https://github.com/Unleash/unleash) instead. Those are battle-tested, well-supported, and have ecosystems around them. pbflags exists because we found the proto-as-source-of-truth pattern interesting and wanted to share it.

## Overview

pbflags lets you define feature flag schemas as protobuf messages, define flag behavior in YAML config files, and generate type-safe client code for Go and Java. Proto is the source of truth for flag identity, types, defaults, and evaluation context dimensions; YAML config is the source of truth for condition chains; the database stores synced runtime state for evaluators.

## For AI agents

If you are an AI agent integrating pbflags into a consumer project, start with [docs/agent-setup.md](docs/agent-setup.md). It is the shortest end-to-end setup path and avoids maintainer-only details from the rest of the docs.

## Prerequisites

- Go 1.26+
- PostgreSQL (or Docker for `docker compose up`)
- [Buf CLI](https://buf.build/docs/installation)

## Quick Start

### 1. Define flags in proto

```protobuf
syntax = "proto3";
import "pbflags/options.proto";

enum PlanLevel {
  PLAN_LEVEL_UNSPECIFIED = 0;
  PLAN_LEVEL_FREE = 1;
  PLAN_LEVEL_PRO = 2;
  PLAN_LEVEL_ENTERPRISE = 3;
}

// Scope definitions — globally required dims (session_id) are implicit.
option (pbflags.scope) = { name: "anon" };
option (pbflags.scope) = { name: "user", dimensions: ["user_id"] };

// Exactly one message must carry (pbflags.context).
// Each field annotated with (pbflags.dimension) becomes a typed dimension
// constructor in the generated `dims` package.
message EvaluationContext {
  option (pbflags.context) = {};

  string session_id = 1 [(pbflags.dimension) = {
    description: "Stable session identifier"
    distribution: DIMENSION_DISTRIBUTION_UNIFORM
    presence: DIMENSION_PRESENCE_REQUIRED
  }];

  string user_id = 2 [(pbflags.dimension) = {
    description: "Authenticated user identifier"
    distribution: DIMENSION_DISTRIBUTION_UNIFORM
    presence: DIMENSION_PRESENCE_OPTIONAL
  }];

  PlanLevel plan = 3 [(pbflags.dimension) = {
    description: "Subscription tier"
    presence: DIMENSION_PRESENCE_OPTIONAL
  }];
}

message Notifications {
  option (pbflags.feature) = {
    id: "notifications"
    description: "Notification delivery controls"
    owner: "platform-team"
    scopes: ["anon", "user"]
  };

  bool email_enabled = 1 [(pbflags.flag) = {
    description: "Enable email notifications"
    default: { bool_value: { value: true } }
  }];

  string digest_frequency = 2 [(pbflags.flag) = {
    description: "Digest email frequency"
    default: { string_value: { value: "daily" } }
  }];
}
```

### 2. Generate client code

Add pbflags to your `buf.yaml` and generate:

```bash
# buf.yaml
# deps:
#   - buf.build/spotlightgov/pbflags

go install github.com/SpotlightGOV/pbflags/cmd/protoc-gen-pbflags@latest
buf dep update
buf generate --template buf.gen.flags.yaml
buf build proto -o descriptors.pb
```

Example `buf.gen.flags.yaml` for Go:

```yaml
version: v2
plugins:
  - local: protoc-gen-pbflags
    out: gen/flags
    strategy: all
    opt:
      - lang=go
      - package_prefix=github.com/yourorg/yourrepo/gen/flags
inputs:
  - directory: proto
```

### 3. Define flag behavior

Create one YAML config file per feature:

```yaml
# features/notifications.yaml
feature: notifications
flags:
  email_enabled:
    conditions:
      - when: "ctx.plan == PlanLevel.ENTERPRISE"
        value: true
      - otherwise: false
  digest_frequency:
    value: "daily"
```

Validate it before syncing:

```bash
pbflags-sync validate --descriptors=descriptors.pb --features=./features
```

### 4. Run the server

```bash
pbflags-admin --standalone \
  --descriptors=descriptors.pb \
  --features=./features \
  --database=postgres://user:pass@localhost:5432/mydb?sslmode=disable
```

This starts the admin UI on `:9200` and the evaluator on `:9201`. Migrations, definition sync, and evaluation all happen in one process.

Or use Docker Compose:

```bash
docker compose -f docker/docker-compose.yml up
```

### 5. Use in your application

```go
import (
  "net/http"

  pb "github.com/yourorg/yourrepo/gen/proto"
  "github.com/yourorg/yourrepo/gen/flags/dims"
  "github.com/yourorg/yourrepo/gen/flags/notificationsflags"
  "github.com/SpotlightGOV/pbflags/pbflags"
)

// Create an evaluator connected to the pbflags service.
// The zero-value EvaluationContext is used as a prototype.
eval := pbflags.Connect(http.DefaultClient, "http://localhost:9201", &pb.EvaluationContext{})

// Bind dimensions — With() is immutable, returning a new evaluator.
eval = eval.With(dims.UserID("user-123"), dims.Plan(pb.PlanLevel_PLAN_LEVEL_PRO))

// Create the typed feature client.
notifications := notificationsflags.New(eval)

emailEnabled := notifications.EmailEnabled(ctx)    // bool
frequency := notifications.DigestFrequency(ctx)     // string
```

You can also propagate the evaluator through `context.Context`:

```go
// Middleware: store the evaluator in context.
ctx = pbflags.ContextWith(ctx, eval)

// Handler: retrieve it and create feature clients.
eval := pbflags.FromContext(ctx)
notifications := notificationsflags.New(eval)
```

## Language Support

| Language | Status | Package |
|---|---|---|
| Go | Stable | `go get github.com/SpotlightGOV/pbflags` |
| Java | Stable | `org.spotlightgov.pbflags:pbflags-java` ([Maven Central](https://central.sonatype.com/artifact/org.spotlightgov.pbflags/pbflags-java)) |
| Java Testing | Stable | `org.spotlightgov.pbflags:pbflags-java-testing` |
| TypeScript | Planned | - |
| Rust | Planned | - |
| Node | Planned | - |

## Documentation

As-built documentation lives in `docs/`. Design research and explorations live in `research/`.

| Document | Description |
|---|---|
| [Agent Setup](docs/agent-setup.md) | Step-by-step integration guide for AI agents |
| [Deployment](docs/deployment.md) | Service topology, standalone and production setup, admin UI, configuration |
| [Upgrading](docs/upgrading.md) | Upgrade procedures for standalone and multi-instance deployments |
| [Go Client](docs/go.md) | Go codegen setup, buf configuration, generated API surface |
| [Java Client](docs/java.md) | Java codegen setup, Dagger integration, testing utilities |
| [Philosophy](docs/philosophy.md) | Design principles, evaluation context, dimensions, lint tool |
| [Contributing](docs/contributing.md) | Dev setup, testing, releasing, migration rules |
| [Troubleshooting](docs/troubleshooting.md) | Common errors and how to resolve them |
| [Roadmap](docs/roadmap.md) | Planned features and sequencing |

## License

MIT
