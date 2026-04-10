# pbflags

Protocol Buffer-based feature flags with type-safe code generation, multi-tier caching, and a never-throw guarantee.

> **Note:** This project is a learning exercise and research exploration into protobuf-driven feature flag design. It was extracted from a production system to study the patterns independently. If you're building a real product and need feature flags, you probably want [Flipt](https://github.com/flipt-io/flipt), [OpenFeature](https://openfeature.dev/), or [Unleash](https://github.com/Unleash/unleash) instead. Those are battle-tested, well-supported, and have ecosystems around them. pbflags exists because we found the proto-as-source-of-truth pattern interesting and wanted to share it.

## Overview

pbflags lets you define feature flags as protobuf messages and generates type-safe client code for Go and Java. Flags are the proto source of truth, the database is the runtime source of truth, and generated clients give you compile-time type safety at every call site.

## For AI agents

If you are an AI agent integrating pbflags into a project, see [docs/agent-setup.md](docs/agent-setup.md) — a step-by-step guide designed for automated setup without parsing this README.

## Prerequisites

- Go 1.26+
- PostgreSQL (or Docker for `docker compose up`)
- [Buf CLI](https://buf.build/docs/installation)

## Quick Start

### 1. Define flags in proto

```protobuf
syntax = "proto3";
import "pbflags/options.proto";

enum Layer {
  option (pbflags.layers) = true;
  LAYER_UNSPECIFIED = 0;
  LAYER_GLOBAL = 1;
  LAYER_USER = 2;
}

message Notifications {
  option (pbflags.feature) = {
    id: "notifications"
    description: "Notification delivery controls"
    owner: "platform-team"
  };

  bool email_enabled = 1 [(pbflags.flag) = {
    description: "Enable email notifications"
    default: { bool_value: { value: true } }
    layer: "user"
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

### 3. Run the server

```bash
pbflags-admin --standalone \
  --descriptors=descriptors.pb \
  --database=postgres://user:pass@localhost:5432/mydb?sslmode=disable
```

This starts the admin UI on `:9200` and the evaluator on `:9201`. Migrations, definition sync, and evaluation all happen in one process.

Or use Docker Compose:

```bash
docker compose -f docker/docker-compose.yml up
```

### 4. Use in your application

```go
import "github.com/yourorg/yourrepo/gen/flags/layers"

client := notificationsflags.NewNotificationsFlagsClient(evaluatorClient)

emailEnabled := client.EmailEnabled(ctx, layers.User("user-123"))  // bool
frequency := client.DigestFrequency(ctx)                            // string
```

## Clients

| Language | Status | Package |
|---|---|---|
| Go | Stable | `go get github.com/SpotlightGOV/pbflags` |
| Java | Stable | `org.spotlightgov.pbflags:pbflags-java` ([Maven Central](https://central.sonatype.com/artifact/org.spotlightgov.pbflags/pbflags-java)) |
| Java Testing | Stable | `org.spotlightgov.pbflags:pbflags-java-testing` |
| TypeScript | Planned | - |
| Rust | Planned | - |
| Node | Planned | - |

## Documentation

| Document | Description |
|---|---|
| [Agent Setup](docs/agent-setup.md) | Step-by-step integration guide for AI agents |
| [Deployment](docs/deployment.md) | Service topology, standalone and production setup, admin UI, configuration |
| [Upgrading](docs/upgrading.md) | Upgrade procedures for standalone and multi-instance deployments |
| [Go Client](docs/go.md) | Go codegen setup, buf configuration, generated API surface |
| [Java Client](docs/java.md) | Java codegen setup, Dagger integration, testing utilities |
| [Philosophy](docs/philosophy.md) | Design principles, evaluation precedence, layers, lint tool |
| [Contributing](docs/contributing.md) | Dev setup, testing, releasing, migration rules |
| [Troubleshooting](docs/troubleshooting.md) | Common errors and how to resolve them |
| [Roadmap](docs/roadmap.md) | Planned features and sequencing |

## License

MIT
