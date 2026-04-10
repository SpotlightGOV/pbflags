# Feature Flags v2: Distributed Flag Service

**Status**: Complete
**Date**: 2026-03-18
**Supersedes**: [2026-03-11-feature-flags.md](2026-03-11-feature-flags.md) (v1,
in-process evaluation)

## Goal

Extract the feature flag system into two standalone components — an **admin
server** and a **flag evaluator** — sharing a Postgres database. Clients in any
language get the same resilience guarantees (never-throw, graceful degradation,
compiled defaults) without reimplementing caching or fallback logic.

The flag service is designed to be run by the team using it (or an infra team).
There are no plans for a SaaS offering.

### Non-Goals

- SaaS operation or multi-tenant hosting
- Open-source repository extraction (stays in Spotlight monorepo for now)
- Admin UI redesign (existing React admin UI continues to work)

## Architecture Overview

### Simplest deployment (one container + Postgres)

```
┌─────────────────────────────────────────────────────────┐
│  Application Process (Java / Go / TypeScript)           │
│                                                         │
│  NotificationsFlags.emailEnabled().get(userId)          │
│       │                                                 │
│  ┌────▼──────────────────────────────────────┐          │
│  │ Generated Client (protoc-gen-flags)       │          │
│  │  • Type-safe getters                      │          │
│  │  • Compiled defaults as constants         │          │
│  │  • catch-all: return default on any error │          │
│  └────┬──────────────────────────────────────┘          │
│       │ gRPC / Connect                                  │
└───────┼─────────────────────────────────────────────────┘
        │
┌───────▼─────────────────────────────────────────────────┐
│  FlagEvaluator (root + admin combined mode)             │
│  • :9099 — FlagEvaluator gRPC service                   │
│  • :8080 — Admin UI + registration API                  │
│  • Direct SQL access to database                        │
│  • Needs descriptors.pb                                 │
│  • Caching, health-based backoff, kill set polling      │
└───────┬─────────────────────────────────────────────────┘
        │ SQL (read-write)
        │
┌───────▼─────────────────────────────────────────────────┐
│  PostgreSQL (feature_flags schema)                      │
│  • Flag state, overrides, audit log                     │
└─────────────────────────────────────────────────────────┘
```

For a small team, this is all you need: one container with `--admin` runs
both the evaluator and admin UI. Add Postgres and you're done.

### Scaled deployment (multiple regions/clusters)

When the deployment grows, add **proxy-mode evaluators** between the
application and the root. Proxy evaluators are generic cache+proxy nodes that
reduce connection fan-out and add a local caching tier. They use the same
binary and the same `FlagEvaluator` interface — the application doesn't know
or care how many tiers are behind its evaluator.

```
  App ──► Local Evaluator (proxy) ──► Regional Evaluator (proxy) ──► Root Evaluator ──► DB
          (per-service evaluator)       (one per region/cluster)       (one, near DB)
```

Each proxy tier is optional. Add tiers only when you need to reduce fan-out
or add regional caching. The architecture is the same at every scale — the
only difference is how many proxy evaluators sit between the application and
the root.

### Why Two Components?

| Component | Responsibility | Failure mode |
|-----------|---------------|--------------|
| Admin server | Flag management UI, registration, audit | Down → no admin changes possible; evaluation unaffected |
| FlagEvaluator | Evaluation, caching, degradation | Upstream unreachable → serve from cache/defaults |
| Generated client | Type safety, compiled defaults, never-throw | Evaluator unreachable → return compiled default (last resort) |

The admin server and evaluation path are fully decoupled. The admin server
being down has zero impact on flag evaluation. The evaluation path scales
independently via the evaluator hierarchy.

### FlagEvaluator Modes

All modes use the **same binary** (same Docker image), selected by arguments:

| Mode | `--upstream` | `--database` | `--descriptors` | `--admin` | Use case |
|------|-------------|-------------|-----------------|----------|----------|
| Root | (none) | PostgreSQL DSN | Required | (off) | Source of truth — reads DB, needs descriptors |
| Root + Admin | (none) | PostgreSQL DSN | Required | port | All-in-one — evaluator + admin UI in one process |
| Proxy | Upstream address | (none) | Not needed | (off) | Cache+proxy — evaluator, regional cache, etc. |

**Root mode** is the default for small deployments. Run it as a evaluator or a
shared service. It reads the database directly and needs `descriptors.pb` to
interpret flag definitions and build the defaults registry.

**Proxy mode** is for scaled deployments. Proxy evaluators receive typed
`FlagValue` responses from upstream, cache them, and serve them to downstream
callers. They don't interpret flag types, parse descriptors, or access the
database. Zero configuration beyond the upstream address.

**Combined mode** (`--admin`): A single process runs both the root evaluator
AND the admin server. This is the easiest way to get started — one container,
one database connection string, one `descriptors.pb`. Ideal for local
development, docker-compose, or small teams that don't need separate scaling
of admin and evaluation. The admin UI and evaluator share the same process
but listen on separate ports.

**Descriptor push order is flexible.** The admin server and root evaluator both
need descriptors, but they consume them independently (admin server for UI
validation, root evaluator for DB interpretation). Either can be updated first.
The only hard constraint: descriptors must be deployed to both before new client
code that references new flags rolls out.

### Connection Fan-Out

In a flat deployment (every evaluator hits the DB), connection count grows
linearly with service instances. The evaluator hierarchy naturally solves this:
only root evaluators open DB connections. Proxy evaluators absorb the
fan-out via RPC, and the number of root evaluators is typically 1-3
regardless of cluster size.

## What Changes from v1

| Aspect | v1 (in-process) | v2 (distributed) |
|--------|-----------------|-------------------|
| Evaluation | In-process Caffeine cache + DB | Evaluator hierarchy with ristretto cache |
| Defaults | Baked into generated Java code | Baked into generated client code (any language) + root evaluator reads from descriptors.pb |
| Flag management | Same process as app | Separate admin server |
| Client coupling | Java only (Dagger, jOOQ) | Any language via gRPC/Connect |
| Codegen | Gradle task (Java-only) | protoc plugin (Go, Java, TypeScript) |
| Schema awareness | Server knows flag types | Admin server + root evaluator know types; proxy evaluators are type-agnostic |
| Cache location | In-process (Caffeine) | Evaluator process (ristretto), layered across hierarchy |
| Failure isolation | Separate thread pool + connection pool | Separate process(es) |
| Scaling | Vertical (per-process cache) | Horizontal (evaluator hierarchy) |

### What Does NOT Change

- **Proto definitions**: Same `options.proto`, same `FeatureOptions` / `FlagOptions` annotations
- **Flag states**: ENABLED / DEFAULT / KILLED (same 3-state model)
- **Precedence rules**: Identical evaluation logic (see v1 3×3 matrix)
- **Layers**: GLOBAL, USER, extensible via enum
- **Identity model**: `<feature_id>/<field_number>` (proto field numbers, rename-safe)
- **Audit logging**: Same actions, same actor tracking
- **Pre-commit linter**: Same invariant checks (type change, layer change, ID stability)

## Proto Definitions

### Flag Options (unchanged from v1)

The existing `proto/flags/options.proto` is unchanged. Feature definitions
(`proto/features/*.proto`) are unchanged. This is the same source-of-truth
for the root evaluator (via `descriptors.pb`), admin server (via
`descriptors.pb`), and client codegen (via `protoc-gen-flags`).

### FlagEvaluator API

This is the evaluation interface exposed by all evaluator modes. It is also the
interface used between evaluator tiers — a proxy evaluator calls the same RPCs
on its upstream that clients call on it.

```proto
// flags/v1/evaluator.proto
syntax = "proto3";
package flags.v1;

option go_package = "github.com/turnbullfamily/spotlightgov/go/gen/flags/v1;flagsv1";

// FlagEvaluator is the evaluation API. Exposed by all evaluator modes.
// Root evaluators expose this to proxy evaluators or directly to clients.
// Proxy evaluators expose this to downstream proxies or clients.
service FlagEvaluator {
  // Evaluate a single flag with optional entity context.
  rpc Evaluate(EvaluateRequest) returns (EvaluateResponse);

  // Bulk evaluate flags. Reduces round trips for page loads.
  rpc BulkEvaluate(BulkEvaluateRequest) returns (BulkEvaluateResponse);

  // Evaluator health and degradation status.
  rpc Health(HealthRequest) returns (HealthResponse);

  // Get killed flags. Used by downstream evaluators to poll the kill set.
  // Root evaluator reads from DB; proxy evaluators forward upstream.
  rpc GetKilledFlags(GetKilledFlagsRequest) returns (GetKilledFlagsResponse);
}

message EvaluateRequest {
  string flag_id = 1;
  string entity_id = 2;  // Empty for global-only evaluation.
}

message EvaluateResponse {
  string flag_id = 1;
  FlagValue value = 2;
  EvaluationSource source = 3;
}

// EvaluationSource indicates where the resolved value came from.
// Used internally by evaluators for metrics. Not exposed to clients
// (clients query module status() instead of per-call source).
enum EvaluationSource {
  EVALUATION_SOURCE_UNSPECIFIED = 0;
  EVALUATION_SOURCE_DEFAULT = 1;       // Compiled default (last resort)
  EVALUATION_SOURCE_GLOBAL = 2;        // Global state (fresh)
  EVALUATION_SOURCE_OVERRIDE = 3;      // Per-entity override (fresh)
  EVALUATION_SOURCE_KILLED = 4;        // Kill switch active
  EVALUATION_SOURCE_CACHED = 5;        // Stale cache (upstream unreachable)
  EVALUATION_SOURCE_ARCHIVED = 6;      // Archived flag's last known value
}

message BulkEvaluateRequest {
  // Empty = all known flags. Populated = only these.
  repeated string flag_ids = 1;
  string entity_id = 2;  // Applied to all flags in the batch.
}

message BulkEvaluateResponse {
  repeated EvaluateResponse evaluations = 1;
}

message HealthRequest {}

message HealthResponse {
  EvaluatorStatus status = 1;
  // Time since last successful upstream contact (DB or parent evaluator).
  int64 seconds_since_upstream_contact = 2;
  // Number of flags with cached state (fresh or stale).
  int32 cached_flag_count = 3;
  // Consecutive upstream failures (drives backoff).
  int32 consecutive_failures = 4;
  // Evaluator mode (root or proxy).
  EvaluatorMode mode = 5;
}

enum EvaluatorStatus {
  EVALUATOR_STATUS_UNSPECIFIED = 0;
  EVALUATOR_STATUS_CONNECTING = 1;  // Startup, no upstream contact yet
  EVALUATOR_STATUS_SERVING = 2;     // Fully connected, fetches succeeding
  EVALUATOR_STATUS_DEGRADED = 3;    // Upstream unreachable, serving from stale cache + defaults
}

enum EvaluatorMode {
  EVALUATOR_MODE_UNSPECIFIED = 0;
  EVALUATOR_MODE_ROOT = 1;          // Direct DB access, needs descriptors
  EVALUATOR_MODE_PROXY = 2;         // Cache + proxy to upstream evaluator
}

message GetKilledFlagsRequest {}

message GetKilledFlagsResponse {
  repeated string flag_ids = 1;
  repeated KilledOverride killed_overrides = 2;
}

message KilledOverride {
  string flag_id = 1;
  string entity_id = 2;
}
```

### Admin Server API

The admin server handles flag management (UI) and registration (release-time).
It talks directly to the database via a library — no separate server in the
evaluation path.

```proto
// flags/v1/admin.proto
syntax = "proto3";
package flags.v1;

option go_package = "github.com/turnbullfamily/spotlightgov/go/gen/flags/v1;flagsv1";

// FlagAdmin is the UI-facing API. Used by admin dashboards to manage
// flag state, overrides, and view audit logs. Flag definitions are read
// directly from descriptors.pb via filesystem mount.
service FlagAdmin {
  rpc ListFeatures(ListFeaturesRequest) returns (ListFeaturesResponse);
  rpc GetFlag(GetFlagRequest) returns (GetFlagResponse);
  rpc UpdateFlagState(UpdateFlagStateRequest) returns (UpdateFlagStateResponse);
  rpc SetFlagOverride(SetFlagOverrideRequest) returns (SetFlagOverrideResponse);
  rpc RemoveFlagOverride(RemoveFlagOverrideRequest) returns (RemoveFlagOverrideResponse);
  rpc GetAuditLog(GetAuditLogRequest) returns (GetAuditLogResponse);
}

// Flag value types. Matches the proto field types used in flag definitions.
enum FlagType {
  FLAG_TYPE_UNSPECIFIED = 0;
  FLAG_TYPE_BOOL = 1;
  FLAG_TYPE_STRING = 2;
  FLAG_TYPE_INT64 = 3;
  FLAG_TYPE_DOUBLE = 4;
}

// Admin messages are identical to v1 — see 2026-03-11-feature-flags.md
// for full definitions. FlagDetail includes metadata (type, default,
// supported_values) enriched from the descriptor index at query time.
```

### Shared Types

```proto
// flags/v1/types.proto — unchanged from original design
// FlagValue, FlagState, State, OverrideState, etc.
```

### Typed Values End-to-End

All values are `FlagValue` oneofs — typed proto values from definition through
evaluator to client. No string encoding anywhere in the pipeline.

The database stores values as **serialized protobuf bytes** in a `BYTEA`
column. The root evaluator interprets these bytes; proxy evaluators pass them
through opaquely as part of `EvaluateResponse` messages.

1. **No parsing overhead**: Values are serialized/deserialized by proto, not
   by custom string parsers.
2. **Schema-agnostic proxies**: Proxy evaluators treat evaluation results as
   opaque cached responses. Only the root evaluator needs type info.
3. **Type safety preserved**: The `FlagValue` oneof enforces type correctness
   at the proto level.

## FlagEvaluator Design

### Evaluator Modes

The FlagEvaluator binary runs in one of two modes, with an optional admin
server embedded in root mode:

#### Root Mode (`--database`)

The root evaluator sits in front of the database and is the source of truth
for flag evaluation. It:

- Reads flag state, overrides, and kill set directly from PostgreSQL
- Needs `descriptors.pb` to build the defaults registry and interpret DB rows
- Hot-reloads descriptors via fsnotify / SIGHUP
- Exposes the `FlagEvaluator` gRPC service
- Uses read-only database credentials

For small deployments, **root mode is all you need.** Run it as a evaluator
alongside your application or as a shared service on the same network.

```bash
# As a evaluator (simplest)
flag-evaluator \
  --database="postgres://readonly@db:5432/flags" \
  --descriptors=/config/descriptors.pb \
  --listen=localhost:9099

# As a shared service
flag-evaluator \
  --database="postgres://readonly@db:5432/flags" \
  --descriptors=/config/descriptors.pb \
  --listen=0.0.0.0:9099
```

With `--admin`, root mode also serves the admin UI and registration API on a
separate port:

```bash
# All-in-one: evaluator + admin (simplest possible deployment)
flag-evaluator \
  --database="postgres://admin@db:5432/flags" \
  --descriptors=/config/descriptors.pb \
  --listen=0.0.0.0:9099 \
  --admin=0.0.0.0:8080
```

Note: combined mode needs read-write DB credentials (for admin writes).
When running root mode without `--admin`, read-only credentials suffice.

#### Proxy Mode (`--upstream`)

Proxy evaluators sit between application clients and the root evaluator. They:

- Forward Evaluate/BulkEvaluate calls upstream with caching
- Poll upstream GetKilledFlags and cache the kill set
- Need **no descriptors** — they are generic cache+proxy nodes
- Reduce connection fan-out to the root

Use proxy mode when scaling beyond what a single root evaluator can serve,
or when you need a local caching tier in a different region/cluster.

```bash
flag-evaluator \
  --upstream=root-evaluator.internal:9099 \
  --listen=localhost:9099    # as evaluator
  # or --listen=0.0.0.0:9099  # as regional cache
```

### Descriptor Parsing and Hot Reload (Root Mode Only)

At startup, the root evaluator reads a `FileDescriptorSet` (produced by
`buf build -o descriptors.pb`). It walks the descriptor tree looking for
messages with the `(flags.v1.feature)` option and fields with the
`(flags.v1.flag)` option.

For each flag field, it extracts:

| Field | Source |
|-------|--------|
| `flag_id` | `<FeatureOptions.id>/<field_number>` |
| `feature_id` | `FeatureOptions.id` |
| `field_number` | Proto field number |
| `display_name` | Proto field name |
| `flag_type` | Proto field type → `FlagType` enum |
| `layer` | `FlagOptions.layer` annotation (`Layer` enum, UNSPECIFIED → GLOBAL) |
| `default_value` | `FlagOptions.default` annotation → `FlagValue` oneof |
| `supported_values` | `FlagOptions.supported_values` annotation |

This is the same extraction logic as v1's `FlagSyncTask` and the `protoc-gen-flags`
codegen plugin.

**Hot reload**: The root evaluator watches the descriptors file for changes (via
`fsnotify` / inotify, or periodic polling of file mtime — configurable). When
the file changes, the root evaluator:

1. Reads and parses the new `FileDescriptorSet`
2. If parsing succeeds:
   - Atomically swaps the defaults registry with the new one
   - New flags get their compiled defaults; removed flags are no longer in the
     registry (but their cached state is preserved for archived flag fallback)
   - Existing caches are **retained** — only the defaults registry changes.
     Cached DB state for flags that still exist remains valid.
   - Logs the delta: flags added, flags removed, defaults changed
3. If parsing fails:
   - The root evaluator **continues serving with its current state** — the old
     defaults registry and all caches remain intact
   - Logs an error with the parse failure details
   - Increments a metric (`descriptor_reload_errors_total`)
   - Retries on the next file change or poll cycle

This means a rolling deploy that updates `descriptors.pb` (e.g., via a
Kubernetes ConfigMap update) takes effect without restarting the evaluator.
Caches survive the reload, so there is no cold-start penalty. A corrupted
or invalid descriptor file cannot take down the evaluator — it simply continues
with what it had.

**Reload triggers:**

- **File watch** (inotify via `fsnotify`): Watches the descriptors path for
  changes. Fires automatically when a deploy updates the file.
- **File poll**: Check mtime periodically. Fallback for filesystems without
  inotify support (e.g., some NFS/ConfigMap mounts).
- **SIGHUP**: Operator or deploy script sends `kill -HUP <pid>` to trigger
  an immediate reload.

### Cache Architecture

All evaluator modes use the same cache structure. The difference is the
upstream data source: root evaluators read from the database; proxy
evaluators read from their upstream evaluator via the FlagEvaluator gRPC
interface.

The evaluator uses **on-demand** fetching. Flag state is fetched from upstream
on first evaluation for each flag (or entity), then cached with a TTL. This
keeps startup fast — the evaluator begins serving immediately.

```
┌──────────────────────────────────────────────────────┐
│                 Evaluator Cache                       │
│                                                      │
│  Kill Set Cache          │  Flag State Cache         │
│  TTL: 30s ± 6s (jitter) │  TTL: 5m ± 60s (jitter)  │
│  Source: GetKilledFlags  │  Source: Evaluate/DB       │
│  (upstream or DB)        │  Load: on-demand (first    │
│  Refresh: background     │  eval per flag)            │
│  On failure: preserve    │  Refresh: background after │
│  last known kills        │  TTL expiry                │
│                          │                            │
│  Override Cache          │  Defaults Registry         │
│  TTL: 5m ± 60s (jitter) │  (ROOT MODE ONLY)          │
│  LRU: 10K entries        │  Source: descriptors.pb    │
│  Source: upstream or DB  │  Always available           │
│  Load: on first eval     │  Proxy mode: no defaults   │
│  for that entity         │  registry needed            │
└──────────────────────────────────────────────────────┘
```

**Key invariant (root mode)**: The defaults registry is populated from
`descriptors.pb` before the root evaluator starts serving. It never depends
on the database. This is the ultimate fallback for root evaluators.

**Proxy evaluators have no defaults registry.** Their fallback chain ends at
stale cache — if no cached data exists and upstream is unreachable, they
return an error to the caller. The generated client's catch-all then returns
the compiled default.

**On-demand fetch flow** (first evaluation of a flag):
1. Client calls `Evaluate(flag_id)`
2. Evaluator checks flag state cache — miss
3. Evaluator calls upstream (parent evaluator or DB) — **non-blocking with
   short timeout** (default 500ms, configurable)
4. If upstream responds: cache result, return value
5. If upstream times out or fails: return **stale cached value** if one exists
   from a previous TTL cycle, otherwise return compiled default (root) or
   error (proxy)

Subsequent evaluations within the TTL hit the cache and never contact
upstream. After TTL expiry, the next evaluation triggers a background refresh
(stale-while-revalidate) — the caller gets the stale cached value immediately
while the refresh happens asynchronously.

**Non-blocking guarantee**: The evaluator MUST NOT block a client evaluation
call waiting for an upstream response beyond the configured short timeout. If
upstream is slow or down, the caller gets the best available value (stale cache
preferred over compiled default) within milliseconds, not seconds.

### Fallback Chain

The evaluator resolves values using this priority order. Each level is tried
only if the previous level has no data:

```
1. Kill set (30s poll)    → compiled default (kills are intentional resets)
2. Cache hit (fresh)      → cached value (normal hot path)
3. Cache miss + upstream  → upstream value (on-demand fetch)
   OK
4. Cache miss + upstream  → stale cached value (last known good from
   fail + stale exists      previous TTL cycle)
5. No cache + no upstream → compiled default (root) or error to caller
                            (proxy: error to caller)
```

**Stale cache is preferred over compiled defaults.** A stale cached value is
more likely to be correct — it was the last known state. A compiled default
is the proto's hardcoded value, which may have been intentionally overridden
by an operator. Serving a stale override is better than reverting to the
default the operator explicitly changed.

**Stale cache served indefinitely**: When upstream is unreachable and the
cache TTL has expired, the evaluator continues to serve the stale cached value
for as long as it has one — there is no hard expiry that discards stale data.
The evaluator exposes its degraded status via `Health()` so applications can
observe and alert on staleness.

**Kill set is sticky**: The 30s TTL controls the *refresh interval*, not the
*expiry*. If upstream becomes unreachable, the evaluator preserves the last
known kill set indefinitely — killed flags stay killed. Kills are only removed
when upstream is reachable and confirms the flag is no longer killed.

### Health Status and Backoff

The evaluator maintains a health status derived from kill poll results and
on-demand fetch outcomes. This status drives exponential backoff to avoid
thundering herds when upstream is unhealthy.

**Health state machine:**

```
                  ┌─────────────┐
      startup ──► │  CONNECTING │ ── first kill poll succeeds ──► SERVING
                  └──────┬──────┘
                         │ first kill poll fails
                         ▼
                  ┌─────────────┐
                  │  DEGRADED   │ ◄── N consecutive failures ── SERVING
                  └──────┬──────┘
                         │ upstream recovers
                         ▼
                  ┌─────────────┐
                  │  SERVING    │
                  └─────────────┘
```

**Exponential backoff for upstream calls:**

| Consecutive failures | Backoff interval | Behavior |
|---|---|---|
| 0 | Normal TTL | Standard polling/fetch |
| 1-2 | 2× normal TTL | Slow down, serve from cache/defaults |
| 3-5 | 4× normal TTL | Further backoff, log warnings |
| 6+ | 8× normal TTL (capped) | Maximum backoff, serve from defaults |

Backoff applies to both the kill poll and on-demand fetches. A single successful
upstream response resets the failure counter to zero. The backoff prevents a
failing upstream from being overwhelmed by retries from multiple evaluators.

### Kill Set Propagation in Hierarchical Deployments

Kill sets propagate through the evaluator hierarchy via the `GetKilledFlags`
RPC. Each tier polls its upstream:

```
Root evaluator:          polls DB every 30s
Proxy evaluator:         polls upstream every 30s
```

**Worst-case propagation latency** for a kill: `depth × 30s`. In a flat
deployment (root evaluator), this is ~30s. With one proxy tier, ~60s.

This is acceptable because:
- Most deployments have 0-1 proxy tiers
- Kill switches are for emergencies — 30-60s is still fast compared to
  deploying a code change
- Each tier's kill set is sticky, so kills only need to propagate once

For deployments that need faster kill propagation, reduce the kill poll TTL
or remove proxy tiers.

### Evaluation Logic

Identical to v1. The root evaluator implements this against the database;
proxy evaluators delegate to upstream via `Evaluate` RPC.

Pseudocode for root evaluator:

```
evaluate(flag_id, entity_id?):
  default = defaults_registry[flag_id]

  // Kill set (30s TTL) — checked first for fast propagation.
  if flag_id in kill_set:
    return default

  // Per-entity override (if applicable).
  if entity_id != "" and layer(flag_id) != GLOBAL:
    override = override_cache.get_or_fetch(flag_id, entity_id)
    if override != nil:
      // Check entity-level killed overrides from kill set.
      if (flag_id, entity_id) in killed_overrides:
        return default
      if override.state == KILLED:
        return default
      if override.state == DEFAULT:
        return default
      if override.state == ENABLED:
        return override.value  // Already typed FlagValue

  // Global state (on-demand, 5m TTL).
  global = flag_state_cache.get_or_fetch(flag_id)
  if global == nil or global.state == DEFAULT:
    return default
  if global.state == KILLED:
    return default

  // ENABLED: return configured value (typed FlagValue).
  if global.value == nil:
    return default
  return global.value
```

Proxy evaluators simply call upstream `Evaluate` and cache the response.
They don't implement evaluation logic — they trust the upstream result.

### Degradation Modes

| Upstream state | Cache state | Evaluator behavior | EvaluationSource |
|---|---|---|---|
| Healthy | Fresh | Normal evaluation | GLOBAL / OVERRIDE / KILLED |
| Unreachable | Warm (within TTL) | Serve from cache | CACHED |
| Unreachable | Stale (past TTL) | Serve stale cache, status=DEGRADED | CACHED |
| Unreachable | Cold (never populated) | Root: compiled default; Proxy: error to caller | DEFAULT (root) or error |

### FlagEvaluator Configuration

```yaml
# flag-evaluator.yaml (or env vars, or CLI flags)

# Mode selection (mutually exclusive):
database: postgres://readonly@db:5432/flags  # Root mode: direct DB access
# OR
upstream: root-evaluator.internal:9099       # Proxy mode

# Root mode only:
descriptors: /config/descriptors.pb          # Required in root mode.

# Common:
listen: localhost:9099                        # Listen address (default).

cache:
  kill_ttl: 30s                              # Kill set poll interval.
  flag_ttl: 5m                               # On-demand flag state cache TTL.
  override_ttl: 5m                           # Override cache entry TTL.
  override_max_entries: 10000                # Override LRU capacity.
  jitter_percent: 20                         # TTL jitter range (±%).
  fetch_timeout: 500ms                       # Max time to wait for upstream.

health:
  port: 9098                                 # Health check port (separate from eval).
```

## Admin Server Design

The admin server is the management plane for the flag system. It exposes the
`FlagAdmin` gRPC service for the admin UI and handles flag registration from
the release pipeline. It talks directly to PostgreSQL via a DB library.

The admin server does NOT participate in the evaluation path. It exists solely
for flag management.

### Capabilities

- **FlagAdmin service**: CRUD for flag states, overrides, audit log queries
- **Descriptor awareness**: Reads `descriptors.pb` directly for flag definitions,
  type/default info, and UI validation (no separate registration step)
- **Embedded HTTP server**: Serves the admin UI static content (React SPA)

### Database Schema

Identical to v1. Runtime values stored as serialized FlagValue protobuf bytes.
Flag type, default, and supported values metadata come from `descriptors.pb`
parsed at server startup — not stored in the database.

```sql
-- Value columns use BYTEA for serialized protobuf:
ALTER TABLE feature_flags.flags
  ALTER COLUMN value TYPE BYTEA USING value::BYTEA;
ALTER TABLE feature_flags.flag_overrides
  ALTER COLUMN value TYPE BYTEA USING value::BYTEA;
```

### Flag Definition Source

Flag definitions (type, default, layer, supported values) are read directly
from `descriptors.pb` at server startup. Both the admin server and evaluator
parse the same file. No separate registration step is needed.

### Archived Flag Behavior

When a flag is removed from proto definitions and archived by the registration
process, the root evaluator handles it:

1. **Root evaluator has the flag in its registry (old descriptors)**: Evaluates
   normally. The flag is in the registry and the DB returns its state.

2. **Root evaluator does NOT have the flag (new descriptors), but DB has
   archived value**: Returns the **archived value** — the last value an
   operator configured before the flag was removed.

3. **No archived value and no stale cache**: Returns an error. The generated
   client's catch-all returns the compiled default baked into the old code
   that still references this flag.

## Change Modes

### Mode 1: Normal State Change (Admin UI → Evaluation)

An operator changes a flag from DEFAULT to ENABLED with a value via the admin UI.

```
Admin UI        Admin Server      Database      Root Evaluator         Client
   │                │                │                │                      │
   │ UpdateFlagState│                │                │                      │
   │───────────────►│                │                │                      │
   │                │ UPDATE flags ──►│                │                      │
   │                │ INSERT audit ──►│                │                      │
   │  OK            │                │                │                      │
   │◄───────────────│                │                │                      │
   │                │                │                │                      │
   │                │                │  (up to 5m:    │                      │
   │                │                │   cache TTL    │                      │
   │                │                │   expires)     │                      │
   │                │                │                │                      │
   │                │                │  SELECT ◄──────│                      │
   │                │                │  ─────────────►│ cache updated        │
   │                │                │                │                      │
   │                │                │                │  Evaluate ◄──────────│
   │                │                │                │  ─────────────────── ►│
```

**Propagation latency**: Up to 5 minutes (flag state cache TTL) for a root
evaluator. If proxy tiers are present, add up to 5 minutes per tier.

### Mode 2: Kill Switch (Emergency Shutoff)

An operator kills a flag due to an incident.

```
Admin UI        Admin Server      Database      Root Evaluator         Client
   │                │                │                │                      │
   │ UpdateFlagState│                │                │                      │
   │ (KILLED)       │                │                │                      │
   │───────────────►│ UPDATE ────────►│                │                      │
   │  OK            │                │                │                      │
   │◄───────────────│                │                │                      │
   │                │                │                │                      │
   │                │                │  (≤30s: kill   │                      │
   │                │                │   poll cycle)  │                      │
   │                │                │                │                      │
   │                │                │  SELECT killed ◄│                      │
   │                │                │  ─────────────►│ kill set updated      │
   │                │                │                │                      │
   │                │                │                │  Evaluate ◄──────────│
   │                │                │                │  default ───────────►│
```

**Propagation latency**: Up to 36 seconds (30s kill poll + jitter) for a root
evaluator. Add ~30s per proxy tier if present.

### Mode 3: Per-Entity Override

Same as kill switch flow but via the override cache (5m TTL). If the entity
hasn't been evaluated recently (not in cache), the override is visible
immediately on next evaluation.

### Mode 4: Deploy-Time New Flags

New flags appear in `descriptors.pb` with each deploy. Both the evaluator
and admin server read the updated file at startup. Until a flag has runtime
state in the DB, evaluation returns the compiled default from the descriptor.

### Mode 5: Server Outage

```
                          Database      Root Evaluator         Client
                              │                │                      │
                              │ TIMEOUT ◄──────│                      │
                              │                │ serve stale          │
                              │                │ cache + backoff      │
                              │                │                      │
                              │                │  Evaluate ◄──────────│
                              │                │  stale ─────────────►│
                              │                │                      │
                              │                │ Health → DEGRADED    │
```

If proxy tiers are present, each proxy independently tracks its own health
relative to its upstream. A root outage causes all downstream proxies to
degrade. A proxy outage only affects its downstream clients — the root and
other proxies are unaffected.

### Change Mode Summary

| Mode | Trigger | Propagation Latency (2-tier) | Safety |
|------|---------|-------------------|--------|
| Normal state change | Admin UI | ≤ 5 min (root); +5 min per proxy tier | Low risk |
| Kill switch (global) | Admin UI / incident | ≤ 36s (root); +30s per proxy tier | Maximum |
| Per-entity override | Admin UI | Immediate (miss) or ≤ 5 min per tier | Scoped |
| New flag (deploy) | Registration | Immediate | Maximum (starts DEFAULT) |
| Removed flag (deploy) | Registration | Immediate | Safe (archived) |
| Upstream outage | Infrastructure | N/A (stale cache > defaults) | Graceful |
| Evaluator crash | Infrastructure | N/A (client returns compiled default) | Graceful |

## Client Codegen: protoc-gen-flags

### Overview

`protoc-gen-flags` is a protoc plugin (Go binary) that reads proto descriptors
and generates type-safe flag client code. It replaces the v1 Gradle task with
a standard protoc plugin that supports multiple output languages.

### Generated Artifacts Per Language

| Language | Interface | Client (with defaults) | Constants |
|----------|-----------|----------------------|-----------|
| Go | `NotificationsFlags` (interface) | `NewNotificationsFlagsClient(evaluator)` | `NotificationsEmailEnabledID` |
| Java | `NotificationsFlags` (interface) | `NotificationsFlagsClient` (class) | `NotificationsFlags.EMAIL_ENABLED_ID` |
| TypeScript | `NotificationsFlags` (type) | `createNotificationsFlagsClient(evaluator)` | `NOTIFICATIONS_EMAIL_ENABLED_ID` |

### Go Output

```go
// GENERATED — do not edit.
package notificationsflags

import (
    "context"
    flagsv1 "github.com/turnbullfamily/spotlightgov/go/gen/flags/v1"
)

const (
    FeatureID = "notifications"

    EmailEnabledID      = "notifications/1"
    DigestFrequencyID   = "notifications/2"
    MaxRetriesID        = "notifications/3"
    ScoreThresholdID    = "notifications/4"
)

// Compiled defaults from proto annotations.
const (
    EmailEnabledDefault    = true
    DigestFrequencyDefault = "daily"
    MaxRetriesDefault      = int64(3)
    ScoreThresholdDefault  = float64(0.75)
)

// NotificationsFlags provides type-safe access to notification feature flags.
type NotificationsFlags interface {
    EmailEnabled(ctx context.Context, entityID string) bool
    DigestFrequency(ctx context.Context) string
    MaxRetries(ctx context.Context) int64
    ScoreThreshold(ctx context.Context) float64

    // Status returns the evaluator's current health state.
    Status(ctx context.Context) flagsv1.EvaluatorStatus
}

// NewNotificationsFlagsClient creates a client backed by a FlagEvaluator connection.
func NewNotificationsFlagsClient(evaluator flagsv1.FlagEvaluatorClient) NotificationsFlags {
    return &notificationsFlagsClient{evaluator: evaluator}
}

type notificationsFlagsClient struct {
    evaluator flagsv1.FlagEvaluatorClient
}

func (c *notificationsFlagsClient) EmailEnabled(ctx context.Context, entityID string) bool {
    resp, err := c.evaluator.Evaluate(ctx, &flagsv1.EvaluateRequest{
        FlagId:   EmailEnabledID,
        EntityId: entityID,
    })
    if err != nil {
        return EmailEnabledDefault
    }
    return resp.Value.GetBoolValue()
}

func (c *notificationsFlagsClient) DigestFrequency(ctx context.Context) string {
    resp, err := c.evaluator.Evaluate(ctx, &flagsv1.EvaluateRequest{
        FlagId: DigestFrequencyID,
    })
    if err != nil {
        return DigestFrequencyDefault
    }
    return resp.Value.GetStringValue()
}

func (c *notificationsFlagsClient) Status(ctx context.Context) flagsv1.EvaluatorStatus {
    resp, err := c.evaluator.Health(ctx, &flagsv1.HealthRequest{})
    if err != nil {
        return flagsv1.EvaluatorStatus_EVALUATOR_STATUS_UNSPECIFIED
    }
    return resp.Status
}

// ... MaxRetries, ScoreThreshold follow same pattern
```

### Java Output

```java
// GENERATED — do not edit.
package com.spotlight.flags.generated;

import com.spotlight.flags.Flag;
import com.spotlight.flags.EvaluatorClient;
import com.spotlight.flags.EvaluatorStatus;

public interface NotificationsFlags {
    String EMAIL_ENABLED_ID = "notifications/1";
    String DIGEST_FREQUENCY_ID = "notifications/2";
    String MAX_RETRIES_ID = "notifications/3";
    String SCORE_THRESHOLD_ID = "notifications/4";

    Flag<Boolean> emailEnabled();
    Flag<String> digestFrequency();
    Flag<Long> maxRetries();
    Flag<Double> scoreThreshold();

    /** Returns the evaluator's current health status. */
    EvaluatorStatus status();
}
```

```java
// GENERATED — do not edit.
package com.spotlight.flags.generated;

public final class NotificationsFlagsClient implements NotificationsFlags {
    // Compiled defaults from proto annotations.
    private static final boolean EMAIL_ENABLED_DEFAULT = true;
    private static final String DIGEST_FREQUENCY_DEFAULT = "daily";
    private static final long MAX_RETRIES_DEFAULT = 3L;
    private static final double SCORE_THRESHOLD_DEFAULT = 0.75;

    private final EvaluatorClient evaluator;

    public NotificationsFlagsClient(EvaluatorClient evaluator) {
        this.evaluator = evaluator;
    }

    @Override
    public EvaluatorStatus status() {
        return evaluator.health().getStatus();
    }

    @Override
    public Flag<Boolean> emailEnabled() {
        return new Flag<>() {
            @Override
            public Boolean get() {
                return evaluator.evaluateBool(EMAIL_ENABLED_ID, "", EMAIL_ENABLED_DEFAULT);
            }

            @Override
            public Boolean get(String entityId) {
                return evaluator.evaluateBool(EMAIL_ENABLED_ID, entityId, EMAIL_ENABLED_DEFAULT);
            }
        };
    }

    // ... digestFrequency, maxRetries, scoreThreshold follow same pattern
}
```

### No Output Wrapper

The v1 `Flag<T>.get()` returned the raw value. v2 keeps this — `get()` returns
`T` directly, not a wrapper. Most callers don't care about evaluation source,
and wrapping every return value adds friction to the most common code path.

**Observability via module status, not per-call metadata**: Instead of wrapping
each evaluation result, the generated flags module exposes a `status()` method
that returns the evaluator's current health state.

```java
// Evaluation — returns raw value, no wrapper
boolean emailOn = notifications.emailEnabled().get(userId);

// Status check — applications that care can query the module
EvaluatorStatus status = notifications.status();
if (status == EvaluatorStatus.DEGRADED) {
    logger.warn("Flag evaluator degraded, serving from cache/defaults");
}
```

### TypeScript Output

```typescript
// GENERATED — do not edit.

export const NOTIFICATIONS_EMAIL_ENABLED_ID = "notifications/1";
export const NOTIFICATIONS_DIGEST_FREQUENCY_ID = "notifications/2";
export const NOTIFICATIONS_MAX_RETRIES_ID = "notifications/3";
export const NOTIFICATIONS_SCORE_THRESHOLD_ID = "notifications/4";

export const NOTIFICATIONS_EMAIL_ENABLED_DEFAULT = true;
export const NOTIFICATIONS_DIGEST_FREQUENCY_DEFAULT = "daily";
export const NOTIFICATIONS_MAX_RETRIES_DEFAULT = 3;
export const NOTIFICATIONS_SCORE_THRESHOLD_DEFAULT = 0.75;

export interface NotificationsFlags {
  emailEnabled(entityId?: string): Promise<boolean>;
  digestFrequency(): Promise<string>;
  maxRetries(): Promise<number>;
  scoreThreshold(): Promise<number>;
  status(): Promise<EvaluatorStatus>;
}

export function createNotificationsFlagsClient(
  evaluator: FlagEvaluatorClient,
): NotificationsFlags {
  return {
    async emailEnabled(entityId?: string): Promise<boolean> {
      try {
        const resp = await evaluator.evaluate({
          flagId: NOTIFICATIONS_EMAIL_ENABLED_ID,
          entityId: entityId ?? "",
        });
        return resp.value!.boolValue!;
      } catch {
        return NOTIFICATIONS_EMAIL_ENABLED_DEFAULT;
      }
    },
    async status(): Promise<EvaluatorStatus> {
      const resp = await evaluator.health({});
      return resp.status;
    },
    // ... other flags
  };
}
```

### protoc-gen-flags Invocation

Integrated into the standard `buf generate` pipeline:

```yaml
# buf.gen.yaml
version: v2
plugins:
  # Standard Go stubs
  - local: protoc-gen-go
    out: gen
    opt: paths=source_relative
  - local: protoc-gen-connect-go
    out: gen
    opt: paths=source_relative

  # Flag client codegen
  - local: protoc-gen-flags
    out: gen/flags
    opt:
      - lang=go
      - package_prefix=github.com/turnbullfamily/spotlightgov/go/gen/flags
    # Only runs on feature definition protos
    inputs:
      - directory: proto/features

  - local: protoc-gen-flags
    out: src/main/java
    opt:
      - lang=java
      - java_package=com.spotlight.flags.generated
    inputs:
      - directory: proto/features
```

## Temporal Workflow Integration

### v1 Pattern (In-Process)

In v1, Temporal workflows evaluate flags via a dedicated activity to maintain
workflow determinism:

```java
// Activity interface
public interface FeatureFlagEvaluatorActivity {
    String evaluateFlag(String flagId, String entityId);
}

// Workflow code
String model = activities.evaluateFlag("summarization/1", "");
```

### v2 Pattern (Evaluator)

In v2, the Temporal worker has a flag evaluator running alongside it (same
Cloud Run multi-container configuration or as a library). The activity calls
the evaluator instead of evaluating in-process:

```java
// Activity implementation (v2)
public class FeatureFlagEvaluatorActivityImpl implements FeatureFlagEvaluatorActivity {
    private final EvaluatorClient evaluator;

    @Override
    public String evaluateFlag(String flagId, String entityId) {
        return evaluator.evaluateString(flagId, entityId, "");
    }
}
```

The activity interface is unchanged. Workflow code is unchanged.

## Deployment

### Single Container Image

All modes — root, proxy, and combined (root + admin) — use the same binary
and Docker image:

```dockerfile
FROM gcr.io/distroless/static-debian12
COPY flag-evaluator /flag-evaluator
ENTRYPOINT ["/flag-evaluator"]
```

Mode is selected via CLI flags. Adding `--admin=:8080` to a root evaluator
embeds the admin server in the same process.

### Example Deployments

#### All-in-one (simplest, getting started)

A single container runs the evaluator and admin UI. Just add Postgres.

```yaml
# docker-compose.yml
services:
  flags:
    image: ghcr.io/spotlightgov/flag-evaluator:1.0.0
    command:
      - --database=postgres://admin@db:5432/flags
      - --descriptors=/config/descriptors.pb
      - --listen=0.0.0.0:9099
      - --admin=0.0.0.0:8080
    ports:
      - "9099:9099"   # Evaluator (gRPC/Connect)
      - "8080:8080"   # Admin UI
```

Applications connect to `flags:9099` for evaluation, operators browse
`flags:8080` for the admin dashboard.

#### Root evaluator (small team, per-service)

Run a root evaluator as a evaluator alongside each application service. Admin
server runs separately (or as an all-in-one instance alongside the evaluators).

```yaml
# Root evaluator as evaluator (per application service)
- name: flag-evaluator
  image: ghcr.io/spotlightgov/flag-evaluator:1.0.0
  args:
    - --database=postgres://readonly@db:5432/flags
    - --descriptors=/config/descriptors.pb
    - --listen=localhost:9099
```

#### Shared root (medium team)

Run a single root evaluator as a shared service. Applications connect to it
directly (no evaluator). Works when the evaluator is on the same network.

```yaml
# Root evaluator (shared service)
- name: flag-evaluator
  image: ghcr.io/spotlightgov/flag-evaluator:1.0.0
  args:
    - --database=postgres://readonly@db:5432/flags
    - --descriptors=/config/descriptors.pb
    - --listen=0.0.0.0:9099
```

#### Hierarchical (multiple regions)

Add proxy evaluators when you need regional caching or connection fan-out
reduction.

```
Region A                          Region B
┌──────────────┐                  ┌──────────────┐
│ App + proxy  │                  │ App + proxy  │
│ App + proxy  │                  │ App + proxy  │
│ App + proxy  │                  │ App + proxy  │
└──────┬───────┘                  └──────┬───────┘
       │                                  │
┌──────▼───────┐                  ┌──────▼───────┐
│ Proxy        │                  │ Proxy        │
│ (region-A)   │                  │ (region-B)   │
└──────┬───────┘                  └──────┬───────┘
       │                                  │
       └──────────────┬───────────────────┘
                      │
               ┌──────▼───────┐
               │ Root         │
               │ (central)    │
               └──────┬───────┘
                      │
               ┌──────▼───────┐
               │  PostgreSQL  │
               └──────────────┘
```

### Standalone Admin Server Deployment

For deployments that separate the admin server from root evaluators (e.g.,
different scaling, different access controls):

```yaml
apiVersion: serving.knative.dev/v1
kind: Service
spec:
  template:
    spec:
      containers:
        - name: flag-admin
          image: ghcr.io/spotlightgov/flag-evaluator:1.0.0
          ports:
            - containerPort: 8080
          args:
            - --database=postgres://admin@db:5432/flags
            - --descriptors=/config/descriptors.pb
            - --admin=0.0.0.0:8080
            - --listen=  # No evaluator port — admin only
```

For simpler deployments, use `--admin` on an existing root evaluator instead
of running a separate admin container.

### Security: DB Credential Separation

| Component | DB access | Credentials |
|-----------|-----------|-------------|
| Root evaluator (without `--admin`) | Read-only | `readonly` DB user |
| Root evaluator (with `--admin`) | Read-write | `admin` DB user |
| Proxy evaluators | None | No DB credentials |

## Monorepo Layout

```
spotlight/
├── proto/
│   ├── flags/v1/
│   │   ├── options.proto          # Flag definition annotations (unchanged)
│   │   ├── evaluator.proto        # FlagEvaluator service (eval + kill poll)
│   │   ├── admin.proto            # FlagAdmin service (UI + registration)
│   │   └── types.proto            # Shared types (FlagValue, State, etc.)
│   └── features/
│       ├── notifications.proto    # Product flag definitions (unchanged)
│       └── summarization.proto    # Product flag definitions (unchanged)
│
├── go/
│   ├── cmd/
│   │   ├── flag-evaluator/main.go # Single binary (root, proxy, combined)
│   │   └── server/main.go         # Govmon server (future phases)
│   ├── internal/
│   │   ├── flageval/              # FlagEvaluator implementation
│   │   │   ├── evaluator.go       # FlagEvaluator service impl (all modes)
│   │   │   ├── root.go            # Root mode: DB queries, descriptor parsing
│   │   │   ├── proxy.go           # Proxy mode: upstream cache+proxy
│   │   │   ├── cache.go           # Ristretto cache management
│   │   │   ├── eval.go            # Evaluation engine (root mode)
│   │   │   ├── health.go          # Health tracking and backoff
│   │   │   └── descriptors.go     # FileDescriptorSet parser
│   │   ├── flagadmin/             # Admin server implementation
│   │   │   ├── server.go          # FlagAdmin service impl
│   │   │   ├── store.go           # Postgres store (read-write)
│   │   │   └── registration.go    # Registration logic
│   │   └── flagdb/                # Shared DB library
│   │       ├── store.go           # Common query functions
│   │       └── migration.go       # Schema migrations
│   └── gen/                       # buf-generated Go stubs
│
├── tools/
│   └── protoc-gen-flags/          # Codegen plugin
│       ├── main.go
│       ├── internal/gogen/        # Go output
│       ├── internal/javagen/      # Java output (future)
│       ├── internal/tsgen/        # TypeScript output (future)
│       └── testdata/              # Golden file tests
│
├── clients/
│   ├── go/                        # Go flag client SDK (thin)
│   ├── java/                      # Java flag client SDK (thin)
│   └── typescript/                # TS flag client SDK (thin)
│
├── src/                           # Existing Java code (v1 → v2 transition)
│   └── ...
└── conformance/                   # Cross-server conformance tests
    └── ...
```

**Extraction boundary**: Everything under `proto/flags/`, `go/cmd/flag*`,
`go/internal/flageval/`, `go/internal/flagadmin/`, `go/internal/flagdb/`,
`tools/protoc-gen-flags/`, and `clients/` has zero imports from spotlight-specific
code. Product-specific feature definitions (`proto/features/`) stay in the
product repo.

## Testing Strategy

### Principle: Same Tests, Different Targets

The v1 testing strategy tested the flag system as an in-process library. v2
tests the flag system as a distributed service with independently testable
components (root evaluator, proxy evaluator, admin server, client).

### Test Tiers

#### Tier 1: Evaluation Engine Unit Tests

The evaluation function is pure (no I/O, no caching). It takes flag state,
override state, and a compiled default, and returns a value. Tested exhaustively.

**3×3 state matrix** (unchanged from v1):

| Global State | Override State | Expected | Source |
|---|---|---|---|
| ENABLED | (none) | Configured value | GLOBAL |
| ENABLED | ENABLED | Override value | OVERRIDE |
| ENABLED | DEFAULT | Compiled default | DEFAULT |
| ENABLED | KILLED | Compiled default | KILLED |
| DEFAULT | (none) | Compiled default | DEFAULT |
| DEFAULT | ENABLED | Override value | OVERRIDE |
| DEFAULT | DEFAULT | Compiled default | DEFAULT |
| DEFAULT | KILLED | Compiled default | KILLED |
| KILLED | (none) | Compiled default | KILLED |
| KILLED | ENABLED | Compiled default | KILLED |
| KILLED | DEFAULT | Compiled default | KILLED |
| KILLED | KILLED | Compiled default | KILLED |

**Additional edge cases** (unchanged from v1):
- Global ENABLED, value is empty string → compiled default
- Entity ID provided, no override exists → fall through to global
- Layer is USER, entity ID is empty → fall through to global, log warning
- Override exists but flag has GLOBAL layer → override ignored
- Malformed value string (non-numeric for INT64) → compiled default

#### Tier 2: Root Evaluator Unit Tests

Test the root evaluator's DB queries, descriptor parsing, cache management,
and degradation behavior. Uses testcontainers-go with PostgreSQL.

**Descriptor parsing**: Same tests as before (parse features, missing options,
all four types, hot reload, corrupt files, SIGHUP, debouncing).

**Cache behavior** (injectable TTLs, seeded Random for deterministic jitter):
- Kill set refresh from DB populates kill cache
- Kill set refresh failure preserves last known kills
- Global state from DB updates cache
- DB failure preserves last known state
- Override cache miss triggers DB query
- Override LRU eviction at capacity
- Concurrent evaluation during cache refresh returns stale (not blocked)

**DB queries:**
- GetFlagState reads correct row
- GetKilledFlags uses partial index for efficiency
- GetOverrides returns overrides for specific entity
- Read-only queries never modify data

#### Tier 3: Proxy Evaluator Unit Tests

Test the proxy evaluator's upstream forwarding, caching, and degradation.
Uses a mock upstream evaluator.

- Evaluate call forwarded to upstream, result cached
- Cached evaluation returns without upstream call
- Upstream failure → stale cache served
- Upstream failure → no cache → error returned to caller
- Kill set polled from upstream GetKilledFlags
- Kill set sticky on upstream failure
- Health transitions: CONNECTING → SERVING → DEGRADED → SERVING

#### Tier 4: Admin Server Unit Tests

Test registration, state management, and admin API. Uses testcontainers-go.

Same test cases as original Tier 3 (registration, state management, admin API,
adversarial tests).

#### Tier 5: Integration Tests (Root + Proxy)

Full integration with root evaluator connected to test Postgres and proxy
evaluator connected to root.

**End-to-end evaluation lifecycle:**
1. Start root evaluator with test Postgres + descriptors
2. Start proxy evaluator with root as upstream
3. Wait for proxy health → SERVING
4. Evaluate flag via proxy → returns compiled default (state=DEFAULT)
5. Set flag to ENABLED via admin server
6. Wait for cache refresh through hierarchy
7. Evaluate flag via proxy → returns configured value
8. Kill flag
9. Wait for kill poll propagation
10. Evaluate flag → returns compiled default (KILLED)

**Hierarchy degradation:**
1. Root healthy, proxy SERVING
2. Stop root evaluator
3. Evaluate via proxy → returns cached value (DEGRADED)
4. Wait past TTL
5. Evaluate via proxy → returns stale cached value (still DEGRADED)
6. Restart root
7. Wait for sync
8. Evaluate via proxy → returns fresh value (SERVING)

#### Tier 6: Conformance Tests (Cross-Implementation)

Go/Java parity has been proven and the cross-implementation conformance test
suite (`tests/conformance/flag_conformance.json`) has been retired. Each
implementation now maintains its own unit tests for evaluation correctness.

#### Tier 7: Codegen Tests

Same as original — golden files, compilation tests, behavioral tests.

#### Tier 8: Property-Based Tests

Same invariants as original, adapted for evaluator hierarchy:

**Never-throw invariant**: For any evaluator status, flag state, override
state, and hierarchy depth — the generated client always returns a value of
the correct type.

**Kill supremacy**: If a flag is in the kill set at ANY tier, evaluation at
that tier returns compiled default.

**Monotonic degradation**: Each evaluator tier independently transitions
through CONNECTING → SERVING → DEGRADED based on its own upstream health.

#### Tier 9: Performance Tests

**Root evaluator latency** (benchmark):
- Cached evaluation (hot path): target < 100μs p99
- DB miss: target < 5ms p99
- Bulk evaluate 100 flags: target < 1ms p99 (cached)

**Proxy-through-root latency** (with proxy tier):
- Cached at proxy: target < 100μs p99
- Proxy miss, root cached: target < 2ms p99 (one RPC hop)
- Proxy miss, root DB miss: target < 10ms p99

**Kill poll efficiency**: Same as original.

**Startup time**: Same as original.

### Test Infrastructure

**Shared test helpers:**

```go
package flagtest

// StartRootEvaluator starts a root evaluator with testcontainers Postgres.
func StartRootEvaluator(t *testing.T, descriptorsPath string) (addr string, cleanup func())

// StartProxyEvaluator starts a proxy evaluator connected to the given upstream.
func StartProxyEvaluator(t *testing.T, upstreamAddr string) (addr string, cleanup func())

// StartAdminServer starts an admin server with testcontainers Postgres.
func StartAdminServer(t *testing.T) (addr string, cleanup func())

// WaitHealthy blocks until the evaluator reports SERVING status.
func WaitHealthy(t *testing.T, evaluatorAddr string, timeout time.Duration)

// SetFlagState sets a flag's state via the admin API.
func SetFlagState(t *testing.T, adminAddr, flagID, state, value string)

// SetOverride sets a per-entity override via the admin API.
func SetOverride(t *testing.T, adminAddr, flagID, entityID, state, value string)
```

**Test-only evaluator endpoints:**

The evaluator exposes additional endpoints when started with `--test-mode`:

- `POST /test/sync` — force immediate state refresh (DB or upstream)
- `POST /test/killpoll` — force immediate kill poll
- `POST /test/clear-cache` — clear all cached data (simulate cold start)
- `GET /test/cache-stats` — return cache hit/miss/eviction counts

These endpoints are excluded from the production build via build tags.

## Decisions

1. **One binary, two logical components (evaluator + admin)**: A single
   `flag-evaluator` binary serves both roles, configured via CLI flags.
   Root mode (`--database`) provides evaluation; adding `--admin` enables
   the admin UI on a separate port. Proxy mode (`--server`) caches and
   forwards to an upstream root evaluator. The admin path and evaluation
   path are fully decoupled — admin being unavailable has zero impact on
   evaluation.

2. **Evaluator hierarchy with recursive interface**: The FlagEvaluator
   interface is the same at every tier. Root and proxy evaluators expose and
   consume the same RPCs. Add/remove tiers without changing client code.

3. **Single container image**: All evaluator modes use the same binary,
   selected by CLI flags. One image to build, test, scan, and deploy.

4. **Root evaluator reads DB directly**: No RPC intermediary between the
   root evaluator and the database. This removes a network hop and a
   point of failure from the evaluation path.

5. **Proxy evaluators need no descriptors**: They are pure cache+proxy
   nodes. Only the root evaluator (which interprets DB rows) and the admin
   server (which validates UI inputs) need descriptors.

6. **Descriptor push order is flexible**: Admin server and root evaluator
   consume descriptors independently. Either can be updated first. The only
   constraint is: both must be updated before new client code rolls out.

7. **Protoc plugin for codegen**: Integrates with standard `buf generate`
   pipeline. No custom build tool needed.

8. **Typed values end-to-end (FlagValue oneof)**: Values are proto `FlagValue`
   oneofs everywhere. Proxy evaluators pass them through opaquely.

9. **On-demand fetch, not bulk sync**: Evaluators fetch flag state per-flag
   on first evaluation, not all flags at startup.

10. **Health-based exponential backoff**: Per-tier. Each evaluator manages its
    own backoff relative to its upstream.

11. **No output wrapper**: `Flag<T>.get()` returns `T` directly. Status
    exposed on the module via `status()`.

12. **Enums for types and layers**: Proto enums for wire stability and
    exhaustive matching.

13. **No hard cache expiry**: Stale caches served indefinitely. Staleness
    exposed via `Health()`.

14. **Kill set propagation via GetKilledFlags RPC**: Each evaluator tier
    polls its upstream for kills. Sticky on failure.

15. **Descriptor-driven definitions**: Both admin server and evaluator read
    `descriptors.pb` directly. No separate registration step or RPC.

16. **DB credential separation**: Root evaluators get read-only credentials.
    Admin server gets read-write. Proxy evaluators have no DB credentials.

## Migration from v1

### Current State Simplification

As of this writing, **no flags have runtime state** — all flags use their
compiled defaults. This dramatically simplifies the migration:

- **No data migration**: The flag server starts fresh from registration.
- **No behavioral divergence possible**: Compiled defaults are identical.
- **Deploy is zero-risk**: If anything fails, compiled defaults serve the
  same values the app was already using.

### Phase 1: Build Evaluator + Admin Server

1. Implement root evaluator with DB queries, descriptor parsing, caching
2. Implement proxy evaluator with upstream forwarding
3. Implement admin server with FlagAdmin + descriptor parsing
4. Implement `protoc-gen-flags` with Go and Java output
5. Build tests (root unit, proxy unit, integration, admin)
6. Deploy root evaluator + admin server — zero-risk since all flags are defaults

### Phase 2: Java Client Transition

1. Generate `NotificationsFlagsClient` / `SummarizationFlagsClient` via protoc-gen-flags
2. Build `EvaluatorClient.java` — thin gRPC wrapper to localhost evaluator
3. In Dagger, swap `NotificationsFlagsImpl` → `NotificationsFlagsClient`
4. Remove `FlagStore`, `FlagEvaluator`, Caffeine caches from Java server
5. Remove `@FlagDataSource`, `@FlagExecutor` — no more flag DB connection pool
6. Java server is now a pure client of the flag evaluator

### Phase 3: Deploy

1. Deploy root evaluator + admin server
2. Add evaluator evaluator to spotlight-server and spotlight-worker
3. Mount `descriptors.pb` on root evaluator and admin server
4. Verify health: all evaluators report SERVING
5. Cut over: swap Java flag bindings to evaluator-backed clients
6. Monitor: evaluation source metrics, degradation alerts

### Rollback Plan

The v1 in-process flag system has been removed — Go/Java parity is proven and
the conformance test suite has been retired. Rolling back requires reintroducing
the in-process flag code:

1. Revert the Dagger binding swap (one-line change)
2. Restore `FlagStore` and in-process evaluation in Java server
3. Evaluators + admin server can be shut down

The rollback is a deploy, not an architecture change.
