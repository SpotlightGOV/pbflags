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

// Define your override layers. Exactly one enum must carry this annotation.
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

### 2. Set up buf dependency

Add pbflags to your `buf.yaml`:

```yaml
version: v2
modules:
  - path: proto
deps:
  - buf.build/spotlightgov/pbflags
```

Then pull the latest version:

```bash
buf dep update
```

> **Important:** After upgrading pbflags, always run `buf dep update` to pull the
> latest proto definitions from BSR. The `Layer` enum annotation
> (`option (pbflags.layers) = true`) and string-valued `layer` fields were
> introduced in v0.6.0 — older BSR commits do not include them.

### 3. Generate client code

```bash
# Install the codegen plugin
go install github.com/SpotlightGOV/pbflags/cmd/protoc-gen-pbflags@latest

# Generate via buf
buf generate --template buf.gen.flags.yaml
```

Example `buf.gen.flags.yaml` for Go:

```yaml
version: v2
plugins:
  - local: protoc-gen-pbflags
    out: gen/flags
    strategy: all  # required — plugin needs all files to find the Layer enum
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
    strategy: all  # required — plugin needs all files to find the Layer enum
    opt:
      - lang=java
      - java_package=com.yourorg.flags.generated
inputs:
  - directory: proto
```

Complete example configs are in [`proto/example/`](proto/example/).

### 4. Use in your application (Go)

```go
import "github.com/yourorg/yourrepo/gen/flags/layers"

// Create a client connected to the evaluator
client := notificationsflags.NewNotificationsFlagsClient(evaluatorClient)

// Type-safe flag access with compiled defaults and typed layer IDs
emailEnabled := client.EmailEnabled(ctx, layers.User("user-123"))  // bool
frequency := client.DigestFrequency(ctx)                            // string

// Pass zero value for global evaluation (no entity context)
globalDefault := client.EmailEnabled(ctx, layers.UserID{})          // bool
```

#### Using `Defaults()` when no evaluator is available

Each generated package includes a `Defaults()` constructor that returns an
implementation backed entirely by compiled defaults. This eliminates nil
checks when the evaluator is optional:

```go
// Without Defaults() — nil check at every call site:
showPowered := uiflags.ShowPoweredByDefault
if s.flags != nil {
    showPowered = s.flags.ShowPoweredBy(ctx)
}

// With Defaults() — initialize once, call freely:
type Server struct {
    flags uiflags.UIFlags  // never nil
}

func NewServer(evaluator pbflagsv1connect.FlagEvaluatorServiceClient) *Server {
    flags := uiflags.Defaults()
    if evaluator != nil {
        flags = uiflags.NewUIFlagsClient(evaluator)
    }
    return &Server{flags: flags}
}

func (s *Server) handler(ctx context.Context) {
    showPowered := s.flags.ShowPoweredBy(ctx)  // no nil check needed
}
```

`Defaults()` returns a zero-allocation empty struct. `Status()` returns
`EVALUATOR_STATUS_UNSPECIFIED` since no evaluator is connected.

### 5. Use in your application (Java)

```java
// Create via factory method (framework-agnostic)
NotificationsFlags flags = NotificationsFlags.forEvaluator(evaluator);

// Type-safe flag access with typed layer IDs
boolean emailEnabled = flags.emailEnabled().get(UserID.of("user-123"));
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

### Database Migrations

Schema is managed by [goose](https://github.com/pressly/goose). Run migrations before first startup or after upgrading:

```bash
pbflags-server \
  --database=postgres://user:pass@localhost:5432/mydb?sslmode=disable \
  --upgrade
```

This applies all pending migrations and exits. Migration state is tracked in the `goose_db_version` table.

### Database schema sync

```bash
# Sync flag definitions from descriptors.pb into PostgreSQL
pbflags-sync \
  --database=postgres://user:pass@localhost:5432/mydb?sslmode=disable \
  --descriptors=descriptors.pb
```

## Admin Web UI

When running in combined mode (`--admin`), pbflags serves an embedded web dashboard for flag management. The UI is built with server-rendered HTML and htmx.

### Features

- **Dashboard**: Overview of all features and flags with inline state toggles (ENABLED/DEFAULT/KILLED)
- **Flag Detail**: Per-flag view with state/value editing, override management (layer-scoped flags), and recent audit history
- **Audit Log**: Filterable log of all state changes with actor attribution
- **Override Management**: Add and remove per-entity overrides for layer-scoped flags

### Enabling

Pass the `--admin` flag (or set `PBFLAGS_ADMIN`) to start the admin UI:

```bash
pbflags-server \
  --database=postgres://... \
  --descriptors=descriptors.pb \
  --admin=:9200
```

The admin UI is then available at `http://localhost:9200/`.

### Security

- **CSRF protection**: All mutating requests (POST/DELETE) require a valid CSRF token via double-submit cookie pattern. htmx sends the token automatically.
- **Input validation**: Flag IDs are validated against the `feature_id/field_number` format before processing.
- **Internal network only**: The admin UI has no authentication. Deploy it behind a VPN, bastion, or internal network. Do not expose it to the public internet.

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
2. **Per-entity override ENABLED** -> override value
3. **Per-entity override DEFAULT** -> compiled default
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

## Layers

Layers define the override dimensions for your flag system. Each non-global
layer represents a dimension along which flags can vary (e.g., per-user,
per-entity, per-tenant). You define your layers as a proto enum annotated
with `option (pbflags.layers) = true`:

```protobuf
enum Layer {
  option (pbflags.layers) = true;
  LAYER_UNSPECIFIED = 0;
  LAYER_GLOBAL = 1;
  LAYER_USER = 2;
  LAYER_ENTITY = 3;
}
```

The codegen generates a **typed ID wrapper** for each non-global layer.
These types enforce at compile time that callers pass the correct kind of
identifier for each flag:

```go
// Can't pass an EntityID where a UserID is expected — compiler error.
emailEnabled := client.EmailEnabled(ctx, layers.User("user-123"))
lookbackDays := client.LookbackDays(ctx, layers.Entity("govt-body-456"))

// Zero value evaluates global state (no per-entity override applied).
globalDefault := client.EmailEnabled(ctx, layers.UserID{})
```

### How layers flow through the system

| Component | What it sees | Layer-aware? |
|---|---|---|
| Proto definition | `layer: "user"` | Source of truth |
| Generated client | Typed parameter (`layers.UserID`) | Yes — enforces correct ID type |
| Wire protocol | `entity_id` (opaque string) | No — layer-agnostic |
| Evaluator | `IsGlobalLayer()` | Minimal — only global vs. non-global |
| Database | `flags.layer` VARCHAR, `flag_overrides(flag_id, entity_id)` | Stores layer name; overrides keyed by opaque entity ID |
| Admin UI | Displays layer name, shows override section for non-global | Displays only |

The wire protocol and evaluator are intentionally layer-agnostic. Type
safety is enforced in the generated client code, not on the wire.

### Changing a flag's layer

A flag's layer is part of its contract with consumers — changing it changes
the generated client signature and can invalidate existing override data.

| Transition | Allowed? | Why |
|---|---|---|
| Global → Layer | **Yes** | No existing overrides. Safe rollout — empty `entity_id` falls back to global state. |
| Layer → Global | **No** | Orphaned overrides remain in the database. Cannot be deleted until rollout is complete, but if not deleted, silently reappear if the flag is later re-layered. |
| Layer A → Layer B | **No** | Existing override rows were written with Layer A's ID semantics (e.g., user IDs). After the change, they're interpreted as Layer B IDs (e.g., entity IDs). If ID spaces overlap, overrides evaluate incorrectly. |

The lint tool (`pbflags-lint`) enforces these rules at pre-commit time.

### Migrating a flag to a different layer

When you need to change a flag's layer, define a new flag instead of
modifying the existing one:

1. **Add a new flag** in the same feature message with the desired layer and
   a new field number.
2. **Regenerate code.** Both flags are available simultaneously.
3. **Set up overrides** on the new flag for the appropriate entities.
4. **Update application code** to read the new flag. Deploy.
5. **Archive the old flag.** Remove the field from the proto (or mark it
   `reserved`). Run `pbflags-sync` to archive it.

This avoids any window of incorrect evaluation — both flags coexist during
the transition, each with correct override data for its layer.

```protobuf
message Notifications {
  // Old: per-user (will be archived after migration)
  bool email_enabled = 1 [(pbflags.flag) = {
    layer: "user"
    default: { bool_value: { value: true } }
  }];

  // New: per-entity
  bool email_enabled_v2 = 5 [(pbflags.flag) = {
    layer: "entity"
    default: { bool_value: { value: true } }
  }];
}
```

## Lint Tool

`pbflags-lint` detects breaking changes in your proto definitions before they
reach production. It compares the working tree against a base git ref and
reports violations.

### Installation

```bash
go install github.com/SpotlightGOV/pbflags/cmd/pbflags-lint@latest
```

### Usage

```bash
# Pre-commit: compare working tree vs HEAD
pbflags-lint proto/

# CI: compare against main branch
pbflags-lint --base origin/main proto/

# Compare against a tag
pbflags-lint --base v1.2.0 proto/
```

**Important:** Run `pbflags-lint` from the repository root. The tool uses
`git archive` internally, which does not support paths outside the current
directory. If your proto directory is in a subdirectory, use `go -C` to
set the working directory:

```bash
# From a Go submodule
go -C go tool pbflags-lint --base=origin/main proto
```

Exit codes: `0` = clean, `1` = breaking changes found, `2` = tool error.

### What it checks

| Rule | Description |
|---|---|
| `type_changed` | A flag's type changed (e.g., bool to string) |
| `layer_changed` | A flag's layer changed in a forbidden direction |

Layer transition rules: global to layer is allowed; layer to global and
layer A to layer B are forbidden (see [Changing a flag's layer](#changing-a-flags-layer)).

Flag removal is normal lifecycle and is **not** flagged — the evaluator
gracefully handles archived flags.

Stateless checks (invalid layer names, missing layers enum, etc.) are
enforced by codegen at build time — the lint tool only covers
history-dependent rules that require comparing two versions.

### Pre-commit integration

All examples assume the hook runs from the repository root (where `.git/` lives).

```yaml
# lefthook.yml
pre-commit:
  commands:
    pbflags:
      glob: "proto/**/*.proto"
      run: pbflags-lint proto/
```

```yaml
# .pre-commit-config.yaml
- repo: https://github.com/SpotlightGOV/pbflags
  hooks:
    - id: pbflags-lint
      args: [proto/]
```

```json
// package.json (lint-staged)
{ "proto/**/*.proto": "pbflags-lint proto/" }
```

If `pbflags-lint` is installed as a Go tool dependency (`go tool`), use
`go -C <module> tool pbflags-lint` so the working directory is the repo root:

```yaml
# lefthook.yml (Go tool in a submodule)
pre-commit:
  commands:
    pbflags:
      glob: "proto/**/*.proto"
      run: go -C go tool pbflags-lint --base=origin/main proto
```

The tool skips quickly (exit 0) when no `.proto` files have changed,
so it's safe to run on every commit.

## Repository Structure

```
pbflags/
├── proto/pbflags/          # Core proto definitions (options, types, services)
├── proto/example/          # Example feature flag definitions
├── gen/                    # Generated Go protobuf code
├── cmd/
│   ├── pbflags-server/     # Evaluator server binary
│   ├── pbflags-sync/       # Database schema sync from descriptors
│   ├── pbflags-lint/       # Pre-commit breaking change detector
│   └── protoc-gen-pbflags/ # Code generation plugin (Go, Java)
├── internal/
│   ├── evaluator/          # Evaluation engine, caching, health tracking
│   ├── admin/              # Admin API (flag management, audit log)
│   │   └── web/            # Embedded web UI (htmx dashboard)
│   ├── codegen/            # Code generators (Go, Java)
│   └── lint/               # Breaking change detection logic
├── clients/java/           # Java client library (Gradle)
├── clients/java/testing/   # Java test utilities (InMemoryFlagEvaluator, JUnit 5)
├── db/migrations/          # PostgreSQL schema
└── docker/                 # Dockerfile and docker-compose
```

## Releasing

Releases are triggered by pushing a git tag matching `v*`. The GitHub Actions
release workflow builds multi-platform binaries via GoReleaser, pushes a Docker
image to GHCR, and creates a GitHub release with AI-generated release notes.

### Pre-generating release notes

You can generate and review release notes **before** tagging a release:

```bash
make release-notes VERSION=v0.6.0
```

This calls the Claude API to synthesize user-facing notes from the git log
between the previous tag and `v0.6.0`, saving them to
`docs/releasenotes/v0.6.0.md`. Review and edit the file, then commit it:

```bash
git add docs/releasenotes/v0.6.0.md
git commit -m "docs: add release notes for v0.6.0"
```

When the release workflow runs, it detects the pre-committed notes and uses
them as-is instead of generating on the fly. If no pre-committed notes exist,
the workflow generates them automatically (the previous behavior).

To regenerate notes, delete the file and re-run `make release-notes`.

### Tagging and releasing

```bash
git tag v0.6.0
git push origin v0.6.0
```

The release workflow will:

1. Use pre-committed release notes (or generate them via Claude API)
2. Build binaries for linux/macOS on amd64/arm64
3. Build and push a Docker image to `ghcr.io/spotlightgov/pbflags-server`
4. Push proto definitions to the Buf Schema Registry
5. Trigger Java client publishing to Maven Central

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
