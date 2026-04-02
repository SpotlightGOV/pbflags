# pbflags

Protocol Buffer-based feature flags with type-safe code generation, multi-tier caching, and a never-throw guarantee.

> **Note:** This project is a learning exercise and research exploration into protobuf-driven feature flag design. It was extracted from a production system to study the patterns independently. If you're building a real product and need feature flags, you probably want [Flipt](https://github.com/flipt-io/flipt), [OpenFeature](https://openfeature.dev/), or [Unleash](https://github.com/Unleash/unleash) instead. Those are battle-tested, well-supported, and have ecosystems around them. pbflags exists because we found the proto-as-source-of-truth pattern interesting and wanted to share it.

## Overview

pbflags lets you define feature flags as protobuf messages and generates type-safe client code for Go and Java (with TypeScript, Rust, and Node planned). Flags are evaluated by a standalone server that supports three deployment modes:

- **Root mode**: Direct PostgreSQL access, serves as the source of truth
- **Proxy mode**: Connects to an upstream evaluator, reduces database connection fan-out
- **Combined mode**: Root mode with an embedded admin API

## Architecture

```
┌─────────────┐     ┌─────────────────┐     ┌────────────┐
│  Your App   │────▶│  pbflags-server  │────▶│ PostgreSQL │
│ (Go/Java)   │     │  (evaluator)     │     │            │
└─────────────┘     └─────────────────┘     └────────────┘
  Generated            Three-tier cache:       Flag state,
  type-safe            - Kill set (30s)        overrides,
  client               - Global state (5m)     audit log
                       - Overrides (5m LRU)
```

## Quick Start

### 1. Define flags in proto

```protobuf
syntax = "proto3";
import "pbflags/options.proto";

message Notifications {
  option (pbflags.feature) = {
    id: "notifications"
    description: "Notification delivery controls"
    owner: "platform-team"
  };

  bool email_enabled = 1 [(pbflags.flag) = {
    description: "Enable email notifications"
    default: { bool_value: { value: true } }
    layer: LAYER_USER
  }];

  string digest_frequency = 2 [(pbflags.flag) = {
    description: "Digest email frequency"
    default: { string_value: { value: "daily" } }
    layer: LAYER_GLOBAL
  }];
}
```

### 2. Generate client code

```bash
# Install the codegen plugin
go install github.com/SpotlightGOV/pbflags/cmd/protoc-gen-pbflags@latest

# Generate via buf
buf generate
```

Example `buf.gen.yaml` for Go:

```yaml
version: v2
plugins:
  - local: protoc-gen-pbflags
    out: gen/flags
    opt:
      - lang=go
      - package_prefix=github.com/yourorg/yourrepo/gen/flags
inputs:
  - directory: proto
```

Example for Java:

```yaml
version: v2
plugins:
  - local: protoc-gen-pbflags
    out: src/main/java
    opt:
      - lang=java
      - java_package=com.yourorg.flags.generated
inputs:
  - directory: proto
```

### 3. Use in your application (Go)

```go
// Create a client connected to the evaluator
client := notificationsflags.NewNotificationsFlagsClient(evaluatorClient)

// Type-safe flag access with compiled defaults
emailEnabled := client.EmailEnabled(ctx, userID)  // bool
frequency := client.DigestFrequency(ctx)           // string
```

### 4. Use in your application (Java)

```java
// Create via factory method (framework-agnostic)
NotificationsFlags flags = NotificationsFlags.forEvaluator(evaluator);

// Type-safe flag access
boolean emailEnabled = flags.emailEnabled().get(userId);
String frequency = flags.digestFrequency().get();
```

#### Java client setup

```java
// Simple: connect by target address
FlagEvaluatorClient client = new FlagEvaluatorClient("localhost:9201");

// Advanced: custom channel (TLS, interceptors, in-process testing)
ManagedChannel channel = ManagedChannelBuilder.forTarget("localhost:9201")
    .useTransportSecurity()
    .build();
FlagEvaluatorClient client = FlagEvaluatorClient.forChannel(channel);
```

#### Java testing

```java
// Add test dependency
// testImplementation("org.spotlightgov.pbflags:pbflags-java-testing:0.3.0")

class MyTest {
  @RegisterExtension
  static final TestFlagExtension flags = new TestFlagExtension();

  @Test
  void testOverride() {
    flags.set(NotificationsFlags.EMAIL_ENABLED_ID, false);
    var nf = NotificationsFlags.forEvaluator(flags.evaluator());
    assertFalse(nf.emailEnabled().get());
  }
}
```

#### Dagger integration (opt-in)

Add `java_dagger=true` to codegen options to generate a Dagger `@Module` with `@Binds` entries and `@Inject`/`@Singleton` annotations on implementations:

```yaml
opt:
  - lang=java
  - java_package=com.yourorg.flags.generated
  - java_dagger=true
```

This generates `FlagRegistryModule.java` which binds each `*Flags` interface to its `*FlagsImpl`. Include the module in your Dagger component and inject the interfaces directly.

## Running the Server

### Docker (multi-arch: amd64 + arm64)

```bash
docker pull ghcr.io/spotlightgov/pbflags-server
```

### Docker Compose (local development)

```bash
docker compose -f docker/docker-compose.yml up
```

This starts PostgreSQL + pbflags-server in combined mode (evaluator + admin API).

### Binary

```bash
# Root mode (direct database access)
pbflags-server \
  --database=postgres://user:pass@localhost:5432/mydb?sslmode=disable \
  --descriptors=descriptors.pb \
  --listen=:9201

# Combined mode (root + admin API)
pbflags-server \
  --database=postgres://user:pass@localhost:5432/mydb?sslmode=disable \
  --descriptors=descriptors.pb \
  --listen=:9201 \
  --admin=:9200

# Proxy mode (connects to upstream)
pbflags-server \
  --server=http://root-evaluator:9201 \
  --descriptors=descriptors.pb \
  --listen=:9201
```

### Database schema sync

```bash
# Sync flag definitions from descriptors.pb into PostgreSQL
pbflags-sync \
  --database=postgres://user:pass@localhost:5432/mydb?sslmode=disable \
  --descriptors=descriptors.pb
```

## Proto Definitions (BSR)

Proto definitions are published to the [Buf Schema Registry](https://buf.build/spotlightgov/pbflags). Consumers can depend on them directly:

```yaml
# buf.yaml
deps:
  - buf.build/spotlightgov/pbflags
```

## Configuration

Environment variables override CLI flags:

| Variable | Description |
|---|---|
| `PBFLAGS_DESCRIPTORS` | Path to `descriptors.pb` |
| `PBFLAGS_DATABASE` | PostgreSQL connection string (root mode) |
| `PBFLAGS_SERVER` | Upstream evaluator URL (proxy mode) |
| `PBFLAGS_LISTEN` | Evaluator listen address (default: `localhost:9201`) |
| `PBFLAGS_ADMIN` | Admin API listen address (enables combined mode) |

## Flag Evaluation Precedence

1. **Global KILLED** -> compiled default (polled every ~30s)
2. **Per-entity override KILLED/DEFAULT** -> compiled default
3. **Per-entity override ENABLED** -> override value
4. **Global DEFAULT** -> compiled default
5. **Global ENABLED** -> configured value
6. **Fallback** -> compiled default (always safe)

## Key Design Principles

- **Never-throw guarantee**: All evaluation errors return the compiled default
- **Type-safe code generation**: Generated interfaces with compile-time type checking
- **Graceful degradation**: Stale cache served during outages, compiled defaults as last resort
- **Fast kill switches**: ~30s polling for emergency shutoffs
- **Immutable identity**: Flag identity is `feature_id/field_number`, safe to rename fields
- **Audit trail**: All state changes logged with actor and timestamp

## Repository Structure

```
pbflags/
├── proto/pbflags/          # Core proto definitions (options, types, services)
├── proto/example/          # Example feature flag definitions
├── gen/                    # Generated Go protobuf code
├── cmd/
│   ├── pbflags-server/     # Evaluator server binary
│   ├── pbflags-sync/       # Database schema sync from descriptors
│   └── protoc-gen-pbflags/ # Code generation plugin (Go, Java)
├── internal/
│   ├── evaluator/          # Evaluation engine, caching, health tracking
│   ├── admin/              # Admin API (flag management, audit log)
│   └── codegen/            # Code generators (Go, Java)
├── clients/java/           # Java client library (Gradle)
├── clients/java/testing/   # Java test utilities (InMemoryFlagEvaluator, JUnit 5)
├── db/migrations/          # PostgreSQL schema
└── docker/                 # Dockerfile and docker-compose
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

## License

MIT
