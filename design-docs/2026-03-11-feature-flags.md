# Feature Flags System Design

**Status**: Complete
**Date**: 2026-03-11
**Last revised**: 2026-03-11
**Superseded by**: [2026-03-18-feature-flags-v2.md](2026-03-18-feature-flags-v2.md)

## Goal

Separate release of code from release of behavior. Flags can be enabled/disabled
without code pushes via an interactive UI. The system is generic, reusable, and
designed to be extracted into a standalone service later.

## Core Concepts

### Features and Flags

A **feature** is an organizational unit grouping one or more **flags**. A flag
has a type (boolean, string, int64, or double), a default value, metadata, and
a state.

Note: the requirements specify three types (boolean, string, numeric). This
design splits numeric into `int64` and `double` for type safety — these are
distinct proto field types and generate distinct Java types (`Long` vs `Double`).

### Terminology

- **Compiled default**: The default value baked into the binary from the proto
  definition. Always available in-memory, even without DB connectivity.
- **Configured value**: The runtime value set via UI/API, stored in the DB.
- **DB default**: The `default_value` column in the flags table, synced from
  proto at deploy time. Exists for the UI's benefit (showing what the default
  is). Kept in sync with the compiled default by construction.

### Flag States

States apply at both the **global** and **per-entity override** levels. The
three states form a clear mental model:

- **`ENABLED`** — use the configured value
- **`DEFAULT`** — use the compiled default
- **`KILLED`** — use the compiled default, with incident semantics
  (cache bypass, fast polling)

**Global states:**

| State | Behavior |
|-------|----------|
| `ENABLED` | Flag is live. Returns configured value (or per-entity override). |
| `DEFAULT` | Flag returns its compiled default globally. Per-entity overrides still apply — this allows "baseline is default, but specific entities are configured." |
| `KILLED` | Flag returns its compiled default. All overrides are bypassed. Polled every 30s, bypasses normal cache. |

**Override states** (per-entity):

| State | Behavior |
|-------|----------|
| `ENABLED` | Override is active. Returns the override's value for this entity. |
| `DEFAULT` | Override forces compiled default for this entity, regardless of the global configured value. Uses normal cache TTL. |
| `KILLED` | Override forces compiled default for this entity. Polled every 30s alongside global kills. |

To remove an override entirely (fall back to global evaluation), delete the
override row — don't set it to `DEFAULT`.

This allows targeted incident response: kill a flag for a specific user or
business without affecting everyone else. It also allows a "soft rollout"
pattern: set the flag globally to `DEFAULT`, then `ENABLED` overrides for
specific entities to test.

**Precedence**: Global `KILLED` trumps everything (all overrides bypassed).
Otherwise, override state is evaluated first, then global state.

### Layers (Per-Entity Overrides)

Each flag is bound to exactly one **layer** (besides global). A layer represents
an entity dimension: users, businesses, etc. A flag varied by `USER` can have
per-user overrides. A flag varied by `BUSINESS` can have per-business overrides.
A flag cannot vary by both.

Built-in layers:
- `GLOBAL` — No entity context needed. Default for all flags.
- `USER` — Overrides keyed by user ID.

Additional layers are defined in configuration (see Layer Config below).

## Configuration: Protobuf Definitions

Features and flags are defined as protobuf messages with custom options. This
gives us:

- **Type safety** from proto field types (bool, string, int64)
- **Stable IDs** from proto field numbers (rename-safe identity)
- **Backwards compatibility** enforced by buf + custom flag linter
- **Multi-language codegen** from protoc plugins
- **Familiar syntax** for engineers who already know proto

### Proto Schema

```proto
// flags/v1/options.proto
syntax = "proto3";
package flags.v1;

import "google/protobuf/descriptor.proto";
import "google/protobuf/wrappers.proto";

option java_multiple_files = true;
option java_package = "com.spotlight.flags.proto.v1";

// Applied to a message to mark it as a feature.
message FeatureOptions {
  // Stable identifier for this feature. Used as the DB primary key.
  // MUST be unique across all features. MUST NOT change once set.
  // Convention: snake_case, e.g., "notifications", "billing_v2".
  string id = 1;

  string description = 2;
  // Owner team for the feature (for UI grouping / contact).
  string owner = 3;
}

// Applied to a field to mark it as a flag.
message FlagOptions {
  string description = 1;

  // Default value (type must match the field type).
  FlagDefault default = 2;

  // Which layer this flag varies by. Treated as GLOBAL if unset.
  // Note: proto3 will yield LAYER_UNSPECIFIED (0) when not set.
  // Schema sync and evaluation MUST treat UNSPECIFIED as GLOBAL.
  Layer layer = 3;

  // Suggested values for the UI dropdown. Not enforced at evaluation time.
  // Can be overridden in the UI when setting values.
  SupportedValues supported_values = 4;
}

// Uses wrapper types to distinguish "default is false/0" from "no default set."
// In proto3, bare bool false and int64 0 are default values and won't serialize
// in a oneof, making them indistinguishable from "not set."
message FlagDefault {
  oneof value {
    google.protobuf.BoolValue bool_value = 1;
    google.protobuf.StringValue string_value = 2;
    google.protobuf.Int64Value int64_value = 3;
    google.protobuf.DoubleValue double_value = 4;
  }
}

message SupportedValues {
  // At most one of these should be populated, matching the flag's field type.
  // Validated at schema sync time (not enforced by proto structure).
  repeated string string_values = 1;
  repeated int64 int64_values = 2;
  repeated double double_values = 3;
  // (bool flags don't need supported_values — it's always true/false)
}

enum Layer {
  LAYER_UNSPECIFIED = 0;  // Treated as GLOBAL by schema sync and evaluation.
  LAYER_GLOBAL = 1;
  LAYER_USER = 2;
  // Additional layers (e.g., LAYER_BUSINESS = 3) are added here.
  // Each new layer requires entity resolution in the client.
}

// Extension field numbers 51000-51001 are reserved for this project.
// Register in a central registry if other teams define custom options.
extend google.protobuf.MessageOptions {
  optional FeatureOptions feature = 51000;
}

extend google.protobuf.FieldOptions {
  optional FlagOptions flag = 51001;
}
```

### Defining a Feature

```proto
// flags/v1/features/notifications.proto
syntax = "proto3";
package flags.v1.features;

import "flags/v1/options.proto";
import "google/protobuf/wrappers.proto";

option java_multiple_files = true;
option java_package = "com.spotlight.flags.proto.v1.features";

// Notifications feature — controls email and push notification behavior.
message Notifications {
  option (flags.v1.feature) = {
    id: "notifications"
    description: "Controls notification delivery behavior"
    owner: "platform-team"
  };

  bool email_enabled = 1 [(flags.v1.flag) = {
    description: "Enable email notifications"
    default: { bool_value: { value: true } }
    layer: LAYER_USER
  }];

  string digest_frequency = 2 [(flags.v1.flag) = {
    description: "How often to send digest emails"
    default: { string_value: { value: "daily" } }
    supported_values: { string_values: ["hourly", "daily", "weekly"] }
    layer: LAYER_GLOBAL
  }];

  int64 max_retries = 3 [(flags.v1.flag) = {
    description: "Max delivery retry attempts"
    default: { int64_value: { value: 3 } }
    supported_values: { int64_values: [1, 3, 5, 10] }
    layer: LAYER_GLOBAL
  }];

  double score_threshold = 4 [(flags.v1.flag) = {
    description: "Minimum relevance score to trigger notification"
    default: { double_value: { value: 0.75 } }
    layer: LAYER_GLOBAL
  }];
}
```

### Why Not Textproto / YAML?

Proto message definitions with custom options give us codegen for free via
standard protoc plugins. Textproto or YAML would require custom parsers and
custom codegen tooling. The tradeoff is that proto custom options have a
slightly more verbose syntax, but the tooling payoff is significant.

## Proto Enum Safety

**Open question resolved**: Proto enum values always have explicit numeric
assignments in the `.proto` file, so the wire format is stable. The danger is
Java's `Enum.ordinal()` which returns declaration position, not the proto number.
These can diverge.

**Rules**:
1. Always use `.getNumber()`, never `.ordinal()` on proto-generated enums.
2. Enable buf lint rules `ENUM_VALUE_PREFIX` and `ENUM_ZERO_VALUE_SUFFIX`.
3. Consider an Error Prone check that flags `.ordinal()` calls on proto enum types.

## Database Schema

Separate schema (`feature_flags`) so it can be extracted to its own service later.
Uses the existing PostgreSQL + Liquibase + jOOQ stack. The schema below will be
expressed as a Liquibase YAML changelog under
`src/main/resources/db/changelog/`, following the existing project conventions.

```sql
CREATE SCHEMA IF NOT EXISTS feature_flags;

-- Registered features (synced from proto definitions at deploy time).
CREATE TABLE feature_flags.features (
    feature_id   TEXT PRIMARY KEY,         -- from FeatureOptions.id, e.g., "notifications"
    display_name TEXT NOT NULL DEFAULT '',  -- current proto message name (updated on sync)
    description  TEXT NOT NULL DEFAULT '',
    owner        TEXT NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Registered flags (synced from proto definitions at deploy time).
-- Flag IDs use proto field numbers for rename-safe identity:
--   <feature_id>/<field_number>, e.g., "notifications/1"
CREATE TABLE feature_flags.flags (
    flag_id      TEXT PRIMARY KEY,         -- e.g., "notifications/1"
    feature_id   TEXT NOT NULL REFERENCES feature_flags.features(feature_id),
    field_number INT NOT NULL,             -- proto field number (immutable)
    display_name TEXT NOT NULL DEFAULT '',  -- current proto field name (updated on sync)
    flag_type    TEXT NOT NULL,            -- 'BOOL', 'STRING', 'INT64', 'DOUBLE'
    layer        TEXT NOT NULL DEFAULT 'GLOBAL',
    description  TEXT NOT NULL DEFAULT '',
    default_value TEXT,                    -- plain string value (for UI display)

    -- Runtime state (managed via UI/API, not proto sync)
    state        TEXT NOT NULL DEFAULT 'DEFAULT',  -- ENABLED, DEFAULT, KILLED
    value        TEXT,                     -- plain string configured value (null when DEFAULT/KILLED)

    -- Soft-delete for flags removed from proto. Schema sync sets this rather
    -- than hard-deleting, preserving override history and audit trail.
    archived_at  TIMESTAMPTZ,

    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT valid_state CHECK (state IN ('ENABLED', 'DEFAULT', 'KILLED')),
    UNIQUE (feature_id, field_number)
);

-- Index for the kill-check query (polled every 30s).
CREATE INDEX idx_flags_killed ON feature_flags.flags(state)
    WHERE state = 'KILLED';

-- Per-entity overrides.
CREATE TABLE feature_flags.flag_overrides (
    flag_id    TEXT NOT NULL REFERENCES feature_flags.flags(flag_id) ON DELETE CASCADE,
    entity_id  TEXT NOT NULL,
    state      TEXT NOT NULL DEFAULT 'ENABLED',  -- ENABLED, DEFAULT, KILLED
    value      TEXT,                      -- plain string value (null when DEFAULT/KILLED)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    PRIMARY KEY (flag_id, entity_id),
    CONSTRAINT valid_override_state CHECK (state IN ('ENABLED', 'DEFAULT', 'KILLED'))
);

-- Index for kill-check polling to include override-level kills.
CREATE INDEX idx_overrides_killed ON feature_flags.flag_overrides(state)
    WHERE state = 'KILLED';

-- Index for UI queries: "show all overrides for entity X."
CREATE INDEX idx_overrides_entity ON feature_flags.flag_overrides(entity_id);

-- Audit log for flag state changes (UI actions).
-- No FK to flags table intentionally — audit entries survive flag archival/deletion.
CREATE TABLE feature_flags.flag_audit_log (
    id          BIGSERIAL PRIMARY KEY,
    flag_id     TEXT NOT NULL,
    action      TEXT NOT NULL,            -- 'STATE_CHANGE', 'VALUE_CHANGE', 'OVERRIDE_SET', 'OVERRIDE_REMOVED'
    old_value   TEXT,
    new_value   TEXT,
    actor       TEXT NOT NULL,            -- who made the change
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_flag ON feature_flags.flag_audit_log(flag_id, created_at DESC);
```

### Identity Model

Flag identity is based on **proto field numbers**, not names. This makes
renames fully backwards-compatible:

| Component | Identity (stable) | Display (renameable) |
|-----------|-------------------|---------------------|
| Feature | `FeatureOptions.id` (explicit) | Proto message name |
| Flag | `<feature_id>/<field_number>` | Proto field name |

Examples:
- Feature `Notifications` with `id: "notifications"` → feature_id = `notifications`
- Field `email_enabled = 1` → flag_id = `notifications/1`
- Rename to `emails_enabled = 1` → flag_id still `notifications/1`, display_name updated

**What's freely renameable** (no state loss, no release gate):
- Proto message name (`Notifications` → `NotificationSettings`)
- Proto field name (`email_enabled` → `emails_enabled`)
- Description, owner, supported_values

**What's immutable** (enforced at commit time, sync fails if violated):
- `FeatureOptions.id`
- Proto field number
- Proto field type (bool, string, int64, double)
- Flag layer (GLOBAL, USER, BUSINESS, etc.)

### Schema Sync

Schema sync is a **deploy-time migration step**, not a per-instance startup
task. It runs once per deploy, like Liquibase migrations, before any application
instances start serving traffic. This avoids race conditions during rolling
deploys where old and new instances have different proto descriptor sets.

Sync reads the proto descriptors (via `DescriptorProtos`) and upserts into
`features` and `flags` tables:

**Adding flags:**
- Inserts new flags with state `DEFAULT`, flag_id = `<feature_id>/<field_number>`.
- Safe during rolling deploys: new rows in DB are ignored by old instances
  (their compiled code doesn't reference the new flag). Old instances' caches
  will include the new flag after refresh but never evaluate it.

**Updating flag metadata:**
- Updates `display_name`, `description`, `default_value`, and `SupportedValues`.
- **Never** changes `state` or runtime `value` — those are owned by the UI/API.
- Validates that `SupportedValues` matches the flag's field type (logs warnings).
- Treats `LAYER_UNSPECIFIED` as `LAYER_GLOBAL`.

**Renaming fields or messages:**
- No-op for identity. The flag_id (`<feature_id>/<field_number>`) and feature_id
  (`FeatureOptions.id`) are unchanged. Sync updates `display_name` to the new
  proto name. All state, overrides, and audit history are preserved.

**Removing flags:**
- Sets `archived_at = now()` for flags present in DB but absent from proto.
  Archived flags are excluded from evaluation and UI listings but retain their
  override and audit history.
- Clears `archived_at` if a previously archived flag reappears in proto.

**Type changes** (e.g., bool → string) — **sync fails, release blocked:**
- Detected when a flag's DB `flag_type` differs from the proto field type.
- Sync aborts with a clear error message identifying the flag and the type mismatch.
- **To change a flag's type**: remove the old field (archived by sync on next
  deploy), add a new field with a new field number. This is a two-deploy
  process — the old flag is archived first, then the new flag is created.

**Layer changes** (e.g., USER → BUSINESS) — **sync fails, release blocked:**
- Detected when a flag's DB `layer` differs from the proto layer annotation.
- Sync aborts with a clear error message.
- **To change a flag's layer**: same two-deploy process as type changes. Remove
  old field, add new field with new field number and desired layer.

**Server-side validation** (enforced by the admin API, not sync):
- `SetFlagOverride` rejects flags with layer `GLOBAL` (overrides don't apply).
- `SetFlagOverride` validates that the value type matches the flag type.
- `UpdateFlagState` with `ENABLED` validates that a value is set (or was
  previously set).

### Pre-Commit and CI Checks

A custom buf plugin or standalone linter (run as a pre-commit hook and in CI)
validates flag definition invariants that buf's built-in rules don't cover:

| Check | When | Severity |
|-------|------|----------|
| `FeatureOptions.id` is set and non-empty | All feature messages | Error |
| `FeatureOptions.id` is unique across all features | All feature messages | Error |
| `FeatureOptions.id` has not changed (compared to base branch) | PR/commit | Error |
| Flag field type has not changed | PR/commit | Error |
| Flag layer annotation has not changed | PR/commit | Error |
| Flag field number has not been reused | PR/commit | Error (buf also catches this) |
| `FlagDefault` type matches field type | All flag fields | Error |
| `SupportedValues` type matches field type | All flag fields | Warning |

These checks run against the proto descriptor set. The "has not changed" checks
compare the current descriptors against the base branch descriptors (similar to
buf breaking, but for custom option semantics).

This gives **three layers of defense**:
1. **Pre-commit hook**: Catches before code is even committed.
2. **CI check**: Catches in PR review if hooks are bypassed.
3. **Schema sync**: Final gate — refuses to deploy if invariants are violated.

## gRPC Service API

```proto
// flags/v1/service.proto
syntax = "proto3";
package flags.v1;

option java_multiple_files = true;
option java_package = "com.spotlight.flags.proto.v1";

// FlagService provides flag evaluation and management.
service FlagService {
  // Evaluate a single flag.
  rpc EvaluateFlag(EvaluateFlagRequest) returns (EvaluateFlagResponse);

  // Bulk evaluate all flags. Global-only (no entity context).
  // Used by the client for global state cache refresh.
  rpc BulkEvaluate(BulkEvaluateRequest) returns (BulkEvaluateResponse);

  // Get killed flags (lightweight, for frequent polling).
  rpc GetKilledFlags(GetKilledFlagsRequest) returns (GetKilledFlagsResponse);

  // --- Admin API (used by UI) ---
  rpc ListFeatures(ListFeaturesRequest) returns (ListFeaturesResponse);
  rpc GetFlag(GetFlagRequest) returns (GetFlagResponse);
  rpc UpdateFlagState(UpdateFlagStateRequest) returns (UpdateFlagStateResponse);
  rpc SetFlagOverride(SetFlagOverrideRequest) returns (SetFlagOverrideResponse);
  rpc RemoveFlagOverride(RemoveFlagOverrideRequest) returns (RemoveFlagOverrideResponse);
}

message EvaluateFlagRequest {
  string flag_id = 1;
  // Entity context for layer-based evaluation. Empty for global.
  string entity_id = 2;
}

message EvaluateFlagResponse {
  FlagValue value = 1;
  // Whether this came from an override or the default/global value.
  EvaluationSource source = 2;
}

message FlagValue {
  oneof value {
    bool bool_value = 1;
    string string_value = 2;
    int64 int64_value = 3;
    double double_value = 4;
  }
}

enum EvaluationSource {
  EVALUATION_SOURCE_UNSPECIFIED = 0;
  EVALUATION_SOURCE_DEFAULT = 1;
  EVALUATION_SOURCE_GLOBAL = 2;
  EVALUATION_SOURCE_OVERRIDE = 3;
  EVALUATION_SOURCE_KILLED = 4;
}

message BulkEvaluateRequest {
  // Empty = return all flags. Populated = return only these.
  // Note: this is global-only evaluation (no entity context).
  repeated string flag_ids = 1;
}

message BulkEvaluateResponse {
  map<string, FlagValue> values = 1;
}

message GetKilledFlagsRequest {}

message GetKilledFlagsResponse {
  // Globally killed flag IDs.
  repeated string flag_ids = 1;
  // Per-entity killed overrides.
  repeated KilledOverride killed_overrides = 2;
}

message KilledOverride {
  string flag_id = 1;
  string entity_id = 2;
}

// --- Admin messages ---

message ListFeaturesRequest {}

message ListFeaturesResponse {
  repeated FeatureDetail features = 1;
}

message FeatureDetail {
  string feature_id = 1;
  string description = 2;
  string owner = 3;
  repeated FlagDetail flags = 4;
}

message FlagDetail {
  string flag_id = 1;
  string description = 2;
  string flag_type = 3;
  string layer = 4;
  string state = 5;
  FlagValue default_value = 6;
  FlagValue current_value = 7;
  repeated FlagOverrideDetail overrides = 8;
}

message FlagOverrideDetail {
  string entity_id = 1;
  string state = 2;    // ENABLED, DEFAULT, KILLED
  FlagValue value = 3;
}

message UpdateFlagStateRequest {
  string flag_id = 1;
  string state = 2;    // ENABLED, DEFAULT, KILLED
  FlagValue value = 3;  // Optional: set value at the same time
  string actor = 4;
}

message UpdateFlagStateResponse {}

// Validates: flag must have a non-GLOBAL layer. Value type must match flag type.
message SetFlagOverrideRequest {
  string flag_id = 1;
  string entity_id = 2;
  string state = 3;    // ENABLED, DEFAULT, KILLED
  FlagValue value = 4;  // Required when state is ENABLED, ignored otherwise
  string actor = 5;
}

message SetFlagOverrideResponse {}

message RemoveFlagOverrideRequest {
  string flag_id = 1;
  string entity_id = 2;
  string actor = 3;
}

message RemoveFlagOverrideResponse {}
```

## Java Client: Dagger Integration

### Evaluation Logic (Pseudocode)

```
evaluate(flag_id, entity_id?):
  flag = lookup(flag_id)
  default = flag.compiled_default  // baked into binary from proto definition

  // Global KILLED: bypass everything, return compiled default.
  if flag.state == KILLED:
    return default

  // Check per-entity override (if entity context provided and flag has a layer).
  if entity_id != null and flag.layer != GLOBAL:
    override = lookupOverride(flag_id, entity_id)
    if override != null:
      if override.state == KILLED:
        return default              // entity-level kill -> compiled default
      if override.state == DEFAULT:
        return default              // entity-level default -> compiled default
      if override.state == ENABLED:
        return override.value       // override active

  // Global evaluation.
  if flag.state == DEFAULT:
    return default

  // ENABLED: return configured value, or compiled default if no value set.
  return flag.value ?? default
```

### Default Value Fallback Chain

Default values from proto definitions are **baked into the generated code** as
compile-time constants. This creates a three-tier fallback:

1. **DB value** — configured via UI/API (override value or global value)
2. **Cached value** — stale but functional if DB is unreachable
3. **Compiled-in default** — zero-dependency fallback, always available in-memory

This means flag evaluation can never fail to produce a value for a known flag,
even on cold start with no database connectivity. The compiled-in default is the
ultimate backstop.

### Caching Strategy

```
┌─────────────────────────────────────────────────┐
│                 In-Process Cache                 │
│                                                  │
│  Global State Cache    │  Override Cache          │
│  (TTL: 5m + jitter)    │  (TTL: 5m + jitter, LRU)│
│  All flags' state +    │  Per (flag_id, entity)   │
│  values. Bulk refresh. │  Loaded on demand.       │
│                        │                          │
│  Kill Cache            │                          │
│  (TTL: 30s + jitter)   │                          │
│  Just killed flag IDs. │                          │
│  Minimal query.        │                          │
└─────────────────────────────────────────────────┘
```

All cache TTLs include jitter (±20%) to prevent thundering herd when multiple
instances refresh simultaneously.

**Implementation**: Use [Caffeine](https://github.com/ben-manes/caffeine) for
all caches. It provides TTL expiry, LRU eviction, async refresh, size bounding,
and built-in metrics (hit/miss ratio). Hand-rolling thread-safe TTL caches is
error-prone and Caffeine is the standard Java caching library.

**Kill polling**: Every 30s ± 6s, runs two queries (both using partial indexes):
1. `SELECT flag_id FROM feature_flags.flags WHERE state = 'KILLED'` — global kills
2. `SELECT flag_id, entity_id FROM feature_flags.flag_overrides WHERE state = 'KILLED'` — per-entity kills

Returns minimal data (just IDs). Global kills take precedence over everything.
Per-entity kills take precedence over the override cache for that entity.

**Kill cache and global state cache relationship**: The global state cache also
contains flag state (including KILLED). The kill cache exists for faster
propagation — it refreshes every 30s vs 5m for the global cache. During
evaluation, the kill cache is checked first. On cold start, both caches are
empty and populated on first refresh; the kill cache refreshes first (30s < 5m),
so there is no window where a killed flag could appear enabled.

**Global state refresh**: Every 5m ± 60s, bulk-loads all flags (state + value + default).
Relatively small dataset (flags are enumerable, not user-scale).

**Override cache**: Caffeine LRU cache keyed by `(flag_id, entity_id)`. Loaded
on first evaluation for that entity. TTL 5m ± 60s. Bounded size configurable
(default 10K entries). For high-cardinality layers (e.g., USER with millions of
users), the LRU size should be tuned based on the active working set — monitor
the `cache_evictions_total` metric and increase if the eviction rate causes
excessive DB load.

### Dagger Module

```java
package com.spotlight.flags;

@Module
public abstract class FlagModule {

  @Provides
  @Singleton
  @FlagExecutor  // Qualifier to distinguish from any shared executor
  static ScheduledExecutorService provideFlagExecutor() {
    return Executors.newScheduledThreadPool(2,
        new ThreadFactoryBuilder().setNameFormat("flag-cache-%d").setDaemon(true).build());
  }

  @Provides
  @Singleton
  static FlagStore provideFlagStore(
      @FlagDataSource DSLContext db,
      @FlagExecutor ScheduledExecutorService executor) {
    return new FlagStore(db, executor);
  }

  @Provides
  @Singleton
  static FlagEvaluator provideFlagEvaluator(FlagStore store) {
    return new FlagEvaluator(store);
  }

  // Bind generated feature interfaces.
  // Each feature definition generates a Provider that reads from FlagEvaluator.
  // (See codegen section below.)
}
```

**Eager initialization**: `FlagStore` must be eagerly initialized so caches are
populated and kill polling starts before the server accepts traffic. The
`ServerComponent` should expose `FlagStore` as a provision method, or
`FlagStore.start()` should be called in the server startup sequence before
binding the gRPC port.

### Type-Safe Flag Access (Generated Code)

For each feature message defined in proto, codegen produces a Java interface and
implementation. Given the `Notifications` feature above:

```java
// GENERATED — do not edit.
package com.spotlight.flags.generated;

public interface NotificationsFlags {
  Flag<Boolean> emailEnabled();
  Flag<String> digestFrequency();
  Flag<Long> maxRetries();
  Flag<Double> scoreThreshold();
}
```

```java
// GENERATED — do not edit.
package com.spotlight.flags.generated;

public final class NotificationsFlagsImpl implements NotificationsFlags {
  private final FlagEvaluator evaluator;

  @Inject
  NotificationsFlagsImpl(FlagEvaluator evaluator) {
    this.evaluator = evaluator;
  }

  @Override
  public Flag<Boolean> emailEnabled() {
    return evaluator.boolFlag("notifications/1");
  }

  @Override
  public Flag<String> digestFrequency() {
    return evaluator.stringFlag("notifications/2");
  }

  @Override
  public Flag<Long> maxRetries() {
    return evaluator.int64Flag("notifications/3");
  }

  @Override
  public Flag<Double> scoreThreshold() {
    return evaluator.doubleFlag("notifications/4");
  }
}
```

`Flag<T>` objects are lightweight and stateless — they capture the flag ID and
delegate to `FlagEvaluator` on each `.get()` call. They can be cached as
singletons per flag without lifecycle concerns.

### The Flag Interface

```java
package com.spotlight.flags;

/**
 * A typed feature flag. Evaluation never throws.
 */
public interface Flag<T> {
  /** Evaluate globally. */
  T get();

  /** Evaluate with entity context (for layer-based flags). */
  T get(String entityId);
}
```

### Injection Patterns

**Simple global access:**
```java
@Inject NotificationsFlags notifications;

boolean emailOn = notifications.emailEnabled().get();
```

**Layer-scoped access:**
```java
// For layer-aware evaluation, pass entity context:
boolean emailOn = notifications.emailEnabled().get(userId);
```

The qualifier-based injection (`@User NotificationsFlags<Integer>`) from the
requirements is elegant but adds significant complexity to codegen and the Dagger
graph. The simpler `.get(entityId)` pattern achieves the same result with less
machinery. We can revisit the qualifier approach later if the simpler API proves
insufficient.

### Error Handling

**Flag evaluation must never throw.** This is achieved at the `FlagEvaluator` level:

```java
public class FlagEvaluator {
  private static final Counter EVAL_ERRORS = Counter.build()
      .name("feature_flag_evaluation_errors_total")
      .help("Flag evaluation errors by flag and error type")
      .labelNames("flag_id", "error_type")
      .register();

  public <T> T evaluate(String flagId, Class<T> type, String entityId) {
    try {
      return doEvaluate(flagId, type, entityId);
    } catch (Exception e) {
      logger.error("Flag evaluation failed for {}, returning default", flagId, e);
      EVAL_ERRORS.labels(flagId, e.getClass().getSimpleName()).inc();
      return getCompiledDefault(flagId, type);
    }
  }
}
```

The compiled default is always available in-memory, so even the error path
cannot fail for known flags. For truly unknown flag IDs (programmer error),
return the type's zero value (`false`, `""`, `0L`, `0.0`). Log at ERROR.
The application must never crash due to a flag evaluation.

### Failure Isolation

The flag system runs on a separate thread pool (`@FlagExecutor`) and connection
pool (`@FlagDataSource`). If the flag database goes down:
1. Cached values continue to serve (stale but functional).
2. Cache refresh failures are logged and counted, not propagated.
3. Kill polling failures are logged; the kill set is conservatively preserved
   (last known killed flags remain killed).
4. Application continues operating on last-known-good flag state.

### Observability

**Metrics** (Prometheus):

| Metric | Type | Labels | Purpose |
|--------|------|--------|---------|
| `feature_flag_evaluation_total` | Counter | `flag_id`, `source` | Evaluation volume and source distribution |
| `feature_flag_evaluation_errors_total` | Counter | `flag_id`, `error_type` | Error rate by flag |
| `feature_flag_evaluation_duration_seconds` | Histogram | `flag_id` | Evaluation latency |
| `feature_flag_cache_hits_total` | Counter | `cache` | Cache hit rate (global, override, kill) |
| `feature_flag_cache_misses_total` | Counter | `cache` | Cache miss rate |
| `feature_flag_cache_refresh_duration_seconds` | Histogram | `cache` | Cache refresh latency |
| `feature_flag_cache_refresh_errors_total` | Counter | `cache` | Cache refresh failures |
| `feature_flag_killed_flags` | Gauge | — | Current count of globally killed flags |
| `feature_flag_active_overrides` | Gauge | — | Current count of active overrides |

**Health check**: Register a gRPC health check for the flag subsystem. Healthy
if at least one successful cache refresh has completed. Degraded if the last
refresh failed but cached data is available. Unhealthy if no cached data and
DB is unreachable.

**Debug logging**: At TRACE level, log each evaluation with flag_id, entity_id,
source, and resolved value. Guard behind level check to avoid allocation overhead
in production.

## Codegen

A Gradle task that reads proto descriptor sets generates:

1. **`<Feature>Flags` interface** — one method per flag field, returning `Flag<T>`.
2. **`<Feature>FlagsImpl` class** — `@Inject`-able implementation that delegates to `FlagEvaluator`.
3. **`FlagRegistryModule`** — Dagger module binding all feature interfaces.

The codegen reads proto descriptors, finds messages with `(flags.v1.feature)`
option, and for each field with `(flags.v1.flag)` option, emits the typed accessor.

### Name Mapping

Proto field names are `snake_case`. Generated Java accessors are `camelCase`:
- `email_enabled` -> `emailEnabled()`
- `max_retries` -> `maxRetries()`
- `score_threshold` -> `scoreThreshold()`

Flag IDs in the database use proto field numbers for stability:
`<feature_id>/<field_number>`, e.g., `notifications/1`. The human-readable
field name is stored in the `display_name` column and used in the admin UI
and log messages. Renaming a proto field updates `display_name` but not the
flag_id — all state and overrides are preserved.

## Admin UI

A React-based admin UI (can live in the existing `admin/` directory) that provides:

- **Feature list view**: All features grouped, with flag states.
- **Flag detail view**: Current state, value, default, description, overrides.
- **State controls**: Enable / Default / Kill toggle.
- **Override management**: Add/remove per-entity overrides with entity ID input.
  Override state selector (Enable / Default / Kill).
- **Value editing**: Type-aware input (toggle for bool, text/dropdown for string,
  number input for numeric). Dropdown pre-populated from `supported_values` but
  with a "custom value" option.
- **Audit log**: Who changed what, when.
- **Kill indicator**: Visual prominence for killed flags (red banner, sorted to top).

The UI talks to the `FlagService` gRPC API (via grpc-web or a thin REST gateway).

## Layer Configuration

Additional layers beyond `GLOBAL` and `USER` are defined by:

1. Adding a value to the `Layer` enum in `options.proto`.
2. No schema changes needed — `flag_overrides` is entity-type-agnostic (keyed by
   string `entity_id`).
3. Implementing entity resolution in the client (how to extract the entity ID
   from the request context for that layer).

Example: adding a `BUSINESS` layer:

```proto
enum Layer {
  LAYER_UNSPECIFIED = 0;
  LAYER_GLOBAL = 1;
  LAYER_USER = 2;
  LAYER_BUSINESS = 3;
}
```

Then on a flag: `layer: LAYER_BUSINESS`, and evaluate with
`flag.get(businessId)`.

## Package Structure

```
src/main/java/com/spotlight/flags/
├── Flag.java                    # Flag<T> interface
├── FlagEvaluator.java           # Core evaluation logic + error handling
├── FlagStore.java               # DB access + caching (Caffeine)
├── FlagModule.java              # Dagger module
├── FlagSyncTask.java            # Proto descriptor -> DB sync at startup
├── FlagServiceImpl.java         # gRPC service implementation
├── generated/                   # Codegen output
│   ├── NotificationsFlags.java
│   ├── NotificationsFlagsImpl.java
│   └── FlagRegistryModule.java
└── proto/                       # (compiled from proto/)

proto/flags/v1/
├── options.proto                # Custom options + Layer enum
├── service.proto                # FlagService gRPC definition
└── features/
    └── notifications.proto      # Example feature definition
```

## Migration Path to Standalone Service

Because the flag system uses:
- Its own schema (`feature_flags`)
- Its own gRPC service (`FlagService`)
- Its own Dagger module (`FlagModule`)
- Its own package (`com.spotlight.flags`)

Extracting it to a separate service later means:
1. Move the package to a new repo/module.
2. `FlagStore` switches from direct DB access to gRPC client calls.
3. `FlagEvaluator` and `Flag<T>` stay identical — they only talk to `FlagStore`.
4. Caching moves from in-process to the client SDK.

## Testing Strategy

All cache TTLs and poll intervals are injectable so tests run in milliseconds,
not real time. Inject a seeded `Random` for jitter to make timing deterministic.

### Unit Tests

**FlagEvaluator** — the core logic. Full 3×3 matrix of global×override states,
plus edge cases. Each scenario tested for all four types (bool, string, int64,
double).

| Global State | Override State | Expected | Notes |
|-------------|---------------|----------|-------|
| ENABLED | (none) | Configured value | |
| ENABLED | ENABLED | Override value | |
| ENABLED | DEFAULT | Compiled default | |
| ENABLED | KILLED | Compiled default | |
| DEFAULT | (none) | Compiled default | |
| DEFAULT | ENABLED | Override value | |
| DEFAULT | DEFAULT | Compiled default | |
| DEFAULT | KILLED | Compiled default | |
| KILLED | (none) | Compiled default | |
| KILLED | ENABLED | Compiled default | Global kill trumps |
| KILLED | DEFAULT | Compiled default | Global kill trumps |
| KILLED | KILLED | Compiled default | Global kill trumps |

Additional edge cases:

| Scenario | Expected |
|----------|----------|
| Global ENABLED, value column is NULL | Compiled default |
| Entity_id provided, no override exists | Configured value (fall through) |
| Layer is USER, entity_id is null/empty | Fall through to global evaluation, log warning |
| Override exists but flag has GLOBAL layer | Override ignored, global evaluation |
| Archived flag evaluated | Compiled default |
| Evaluation throws internally | Compiled default, error logged, Prometheus counter incremented |
| Unknown flag ID | Type zero value (false/""/0L/0.0), error logged |

**FlagStore caching**:
- Cache returns stale data after TTL until async refresh completes
- Kill cache takes precedence over global state cache
- Cold start: killed flag never returns ENABLED between store creation and
  first kill poll completion (ordering invariant)
- Override cache LRU evicts at capacity; next get() for evicted key reloads
  from DB
- Cache refresh failure preserves last-known-good data
- Concurrent evaluation during cache refresh returns stale (not blocked)
- Jitter stays within ±20% bounds (injectable Random, deterministic)

**Schema sync**:
- New flag inserted with state DEFAULT
- Metadata update (description, default, display_name) preserves state/value
- Field rename updates display_name, preserves flag_id
- Feature message rename updates feature display_name, preserves feature_id
- Removed flag gets archived_at set; sibling flags in same feature unaffected
- Re-added flag gets archived_at cleared
- Type change detected → sync fails with clear error message naming the flag
- Layer change detected → sync fails with clear error message naming the flag
- LAYER_UNSPECIFIED treated as LAYER_GLOBAL
- SupportedValues type mismatch with field type → sync logs warning
- Empty descriptor set (zero features) → sync succeeds as no-op
- Sync is idempotent (running twice produces same result)

**Codegen**:
- Generated interface has correct method names (snake_case → camelCase)
- Generated impl uses `<feature_id>/<field_number>` flag IDs
- All four types produce correct `Flag<T>` return types
- Compiled defaults match proto FlagDefault values
- Features without `FeatureOptions.id` → Gradle task fails with clear error
- Generated flag ID constants (e.g., `NotificationsFlags.EMAIL_ENABLED_ID`)

**Pre-commit linter**:
- Flags type change between base and current → error
- Layer change between base and current → error
- FeatureOptions.id change → error
- FeatureOptions.id missing → error
- Duplicate FeatureOptions.id across features → error
- Field number reuse → error
- FlagDefault type mismatch with field type → error
- SupportedValues type mismatch → warning
- Clean rename (name change, same field number) → pass

### Integration Tests

Uses Testcontainers PostgreSQL (not H2 — the schema uses partial indexes,
`TIMESTAMPTZ`, and schema-qualified names that are PostgreSQL-specific).

**End-to-end evaluation**:
- Write flag config to DB, evaluate through FlagEvaluator, verify result
- Update flag state via admin API, trigger cache refresh, verify new value
- Set per-entity override, evaluate with entity context, verify override value
- Kill a flag via admin API, trigger kill poll, verify evaluation returns default
- Kill an override, verify that specific entity gets default while others don't

**gRPC service tests** (using `InProcessServer` + `InProcessChannelBuilder`
from `grpc-testing`):
- EvaluateFlag returns correct value and EvaluationSource
- BulkEvaluate returns all flags with global evaluation
- GetKilledFlags returns both global and per-entity kills
- UpdateFlagState returns `INVALID_ARGUMENT` for wrong value type
- UpdateFlagState on archived flag → rejected
- SetFlagOverride on GLOBAL-layer flag → `INVALID_ARGUMENT`
- SetFlagOverride with wrong value type → `INVALID_ARGUMENT`
- SetFlagOverride with empty entity_id → `INVALID_ARGUMENT`
- UpdateFlagState to ENABLED without value → `INVALID_ARGUMENT`
- RemoveFlagOverride → entity falls back to global evaluation
- ListFeatures excludes archived flags
- All mutations produce audit log entries with correct action, old/new values,
  and actor

**Failure isolation**:
- Shut down DB after initial cache load → evaluations continue with cached values
- Start with DB down → evaluations return compiled defaults, no exceptions
- Inject exception in FlagStore → FlagEvaluator returns default, app stays healthy
- Verify flag system failure doesn't propagate to gRPC server health

**Lifecycle smoke test**: Single test that creates a flag, enables it, evaluates
it, sets an override, evaluates with entity context, kills it, verifies kill
propagation, un-kills it, and verifies recovery. Catches end-to-end wiring
issues that isolated tests miss.

### Property-Based Tests

Use [jqwik](https://jqwik.net/) (JUnit 5 compatible) to generate random
combinations of evaluation inputs and verify invariants:

- **Never-throw invariant**: For any combination of global state, override
  state, flag type, entity_id presence, and value presence, evaluation always
  returns a value of the correct type and never throws.
- **Kill supremacy**: For any override state, if global state is KILLED, the
  result is always the compiled default.
- **Type consistency**: The returned value's type always matches the flag's
  declared type, regardless of what's stored in the DB value columns.

### Adversarial Tests

- Malformed string in `value` column (e.g., non-numeric for INT64) → evaluation
  returns compiled default, error logged
- Extremely long entity_id strings (10K chars) → handled without error
- Unicode/special characters in string flag values → round-trips correctly
- SQL injection via entity_id → parameterized queries prevent injection
  (verify with intentionally malicious strings)
- Concurrent admin mutations (two actors killing same flag simultaneously)
  → both succeed, audit log captures both, final state is consistent

### Performance Tests

- **Kill poll latency**: Testcontainers PostgreSQL, EXPLAIN ANALYZE verifies
  partial index usage. Completes in < 5ms with 1000 flags and 10K overrides.
- **Evaluation throughput**: JMH microbenchmark for cached `Flag.get()` path.
  Verify no allocation per evaluation (Caffeine lookup only).
- **Cache refresh under load**: JMH benchmark with concurrent reader threads
  during cache refresh. Verify readers are never blocked (Caffeine async
  refresh returns stale data during reload).

### Test Utilities

Codegen produces flag ID constants for test use:
```java
// GENERATED
public final class NotificationsFlags {
  public static final String EMAIL_ENABLED_ID = "notifications/1";
  public static final String DIGEST_FREQUENCY_ID = "notifications/2";
  // ...
}
```

`TestFlagModule` replaces `FlagModule` in test Dagger components:

```java
/**
 * Test module that replaces FlagModule. All flags return compiled defaults.
 * Individual flags can be overridden per-test via flag ID.
 */
@Module
public class TestFlagModule {
  private final Map<String, Object> overrides = new ConcurrentHashMap<>();

  /** Override a flag value for this test. */
  public <T> void set(String flagId, T value) {
    overrides.put(flagId, value);
  }

  /** Reset all overrides to compiled defaults. */
  public void reset() { overrides.clear(); }
}
```

`TestFlagExtension` provides automatic cleanup:

```java
/**
 * JUnit 5 extension that resets flag overrides after each test.
 * Usage: @ExtendWith(TestFlagExtension.class)
 */
public class TestFlagExtension implements AfterEachCallback {
  @Override
  public void afterEach(ExtensionContext context) {
    // Lookup TestFlagModule from the test's Dagger component and reset.
  }
}
```

This ensures application tests aren't coupled to flag system infrastructure.
Tests that need specific flag values set them explicitly; tests that don't care
get compiled defaults automatically.

## Decisions

1. **Codegen approach**: Gradle task reading descriptor sets. Simpler for a
   single-repo setup — no plugin binary to build/distribute. Can migrate to a
   protoc plugin later if needed for multi-repo.

2. **REST gateway for UI**: Envoy grpc-web proxy (already in the stack).

3. **Cache invalidation**: Poll-based. Sufficient at this scale. LISTEN/NOTIFY
   can be added later if lower-latency flag changes are needed.

4. **Qualifier injection**: Deferred. Start with `.get()` / `.get(entityId)`.
   Revisit if usage patterns show `@Global`/`@User` qualifiers would
   significantly reduce boilerplate.

5. **Override cache sizing**: Default 10K entries. Monitor eviction rate for
   high-cardinality layers and tune accordingly.
