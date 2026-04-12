# Evaluation Context and Conditional Flags

**Status:** Draft
**Date:** 2026-04-12
**Author:** bmt

## Problem

pbflags today models flag values as static data: a flag has a global value,
optionally overridden per entity. This works for simple on/off flags but
doesn't support common real-world patterns:

- **"Enable for enterprise users"** — there's no way to express a condition
  on user attributes. You must set individual per-entity overrides.
- **"Roll out to 10% of users"** — no percentage rollout mechanism.
  You must manually create per-entity overrides for selected users.
- **"A/B test daily vs. weekly digest"** — no experiment framework.
  There's no way to split users into groups and log assignments.

These are table-stakes capabilities for a feature flag system. The current
workaround — manually creating per-entity overrides — doesn't scale.

Additionally, the current model has a cross-environment usability problem.
Flag values and overrides live in the database and are set per-environment
via the admin UI. Setting up a new environment requires manually configuring
every flag. There's no way to review flag configuration changes before they
take effect across environments.

## Vision

Flag values become **pure functions of evaluation context** rather than
static data lookups. The evaluation context is a typed bag of dimensions
(user ID, plan, region, device type, etc.) defined in proto and set by the
calling application. Conditions are expressions that select a flag value
based on context. Launches ramp new conditions gradually. Experiments split
populations for controlled tests.

The configuration model shifts from UI-driven to **git-ops**: flag behavior
is defined in version-controlled config files (YAML), reviewed in PRs, and
deployed across environments consistently. The admin UI retains operational
controls — kill switches, launch ramp adjustment, experiment lifecycle —
but steady-state configuration lives in code.

This extends pbflags' existing philosophy: proto is the source of truth for
flag **shape**, config files become the source of truth for flag **behavior**,
and the database stores **operational state**.

| Concern | Source of truth | Change velocity | Review process |
|---------|----------------|-----------------|----------------|
| Flag shape (type, name, feature) | Proto | Slow (codegen) | Code review |
| Flag behavior (defaults, conditions) | Config (YAML) | Medium (sync) | Code review |
| Operational state (kills, ramps) | Database | Fast (immediate) | Admin UI audit log |

## Goals (v1)

1. Define **evaluation context** in proto — typed dimensions (string, enum,
   bool, int64) that conditions can inspect, replacing the current layers
   enum. Generated dimension constructors preserve compile-time type safety.
2. Introduce a **YAML config format** for defining flag values and
   conditions alongside proto definitions.
3. Evaluate **conditions** using CEL (Common Expression Language) — safe,
   sandboxed, type-checked at sync time.
4. **Per-flag dimension tracking** with cardinality classification —
   automatic cache key optimization based on which context dimensions each
   flag's conditions reference and how they reference them (bounded enum vs.
   finite literal set vs. unbounded).
5. **Scoped evaluator API** — construct an evaluator with context that
   naturally fits application scopes (process → request → handler).
6. Generated client methods take only `context.Context` (Go) or are
   zero-arg (Java) — no layer parameters.
7. **Clean-cut migration** from layers to context dimensions. One consumer
   (Spotlight), coordinated. No dual-mode period.

## Non-Goals (v1)

- **Launches** (gradual rollout with ramp percentage). Deferred to a
  follow-on design that builds on the condition infrastructure.
- **Experiments** (randomized assignment with logging). Deferred. Depends
  on the launch slicing model.
- **UI-based condition editing**. v1 uses a CLI for config management.
- **Client-side / frontend flag evaluation**. Orthogonal; see
  `research/client-side-flags.md`.

## Concepts

### Evaluation context

The evaluation context is a typed bag of key-value pairs that describes the
current evaluation environment. It replaces the current `Layer` enum with a
richer, multi-dimensional model.

Context dimensions fall into two natural scopes:

| Scope | Set when | Examples | Cardinality |
|-------|----------|----------|-------------|
| Process | Application startup | zone, environment, service | Very low |
| Request | Per-request middleware | user_id, plan, device_type, session_id | Varies |

The context is defined once in proto and shared across all features. The
application constructs a context by binding dimensions at the appropriate
scope — process-level dimensions are set once, request-level dimensions are
added per request. Conditions evaluate against the combined context.

Dimensions support proto scalar types — `string`, `enum`, `bool`, `int64`.
Generated dimension constructors enforce these types at compile time (e.g.,
`dims.Plan(dims.PlanEnterprise)` for an enum dimension, not
`dims.Plan("enterprise")`). On the wire, the context is carried as the
actual proto `EvaluationContext` message (inside a `google.protobuf.Any`),
preserving full type fidelity across the wire. See "Wire protocol" in the
design section.

Missing (unset) dimensions have the proto zero value for their type (empty
string, 0, false, enum ordinal 0). Conditions should be written to handle
this — e.g., `ctx.plan == PlanLevel.ENTERPRISE` naturally evaluates to
false when `plan` is unset (zero value = `PLAN_LEVEL_UNSPECIFIED`).

### Conditions

A condition is a pair: a CEL predicate and a value. A flag's condition chain
is an ordered list of conditions, evaluated top to bottom. The first
condition whose predicate matches the evaluation context determines the flag
value.

```yaml
flags:
  digest_frequency:
    conditions:
      - when: 'ctx.plan == PlanLevel.ENTERPRISE'
        value: "daily"
      - when: 'ctx.plan == PlanLevel.PRO'
        value: "daily"
      - otherwise: "weekly"
```

The `otherwise` clause is the catch-all — it always matches. If no condition
matches and there's no `otherwise`, the compiled default from the proto
definition is used.

Conditions are pure functions: given the same context, they always return
the same value. No side effects, no external state, no randomness. This
makes them safe to cache and easy to reason about.

### Config files

YAML files define flag behavior — default values and conditions. They live
alongside proto definitions and are version-controlled. The sync tool
validates config against the proto schema, compiles CEL expressions, and
writes the compiled result to the database.

Config files reference flags by proto field name within a feature.
`pbflags-sync` maps field names to flag IDs (`feature_id/field_number`)
using the proto descriptor.

### Kill semantics

Today, killing a flag sets it to KILLED and all evaluations return the
compiled default. This remains unchanged.

In the future, when launches and experiments exist, kills operate at
multiple levels:

| Kill target | Effect | Use case |
|-------------|--------|----------|
| Flag | Kills all launches and experiments on the flag. All evaluations return compiled default. | Emergency: something is fundamentally broken with this flag. |
| Launch | Deactivates the launch. Entities in the ramp fall back to the default condition chain. Other launches/experiments on the flag are unaffected. | This rollout is causing errors, roll it back. |
| Experiment | Deactivates the experiment. All entities evaluate the default condition chain (no variant assignment). Other launches/experiments are unaffected. | Experiment is contaminated or causing harm. |

Killing a specific launch or experiment is the more common operational
action. Killing an entire flag is the nuclear option.

In v1, only flag-level kills exist (same as today).

## Design

### Proto: context definition

Customers define an `EvaluationContext` message annotated with a new
`(pbflags.context)` option. Each field defines a context dimension:

```protobuf
import "pbflags/options.proto";

// Enum dimensions get typed constructors and sync-time value validation.
enum PlanLevel {
  PLAN_LEVEL_UNSPECIFIED = 0;
  PLAN_LEVEL_FREE = 1;
  PLAN_LEVEL_PRO = 2;
  PLAN_LEVEL_ENTERPRISE = 3;
}

enum DeviceType {
  DEVICE_TYPE_UNSPECIFIED = 0;
  DEVICE_TYPE_DESKTOP = 1;
  DEVICE_TYPE_MOBILE = 2;
  DEVICE_TYPE_TABLET = 3;
}

enum Region {
  REGION_UNSPECIFIED = 0;
  REGION_US_EAST = 1;
  REGION_US_WEST = 2;
  REGION_EU_WEST = 3;
}

enum Environment {
  ENVIRONMENT_UNSPECIFIED = 0;
  ENVIRONMENT_DEVELOPMENT = 1;
  ENVIRONMENT_STAGING = 2;
  ENVIRONMENT_PRODUCTION = 3;
}

message EvaluationContext {
  option (pbflags.context) = true;

  // String dimensions — identifiers with unbounded cardinality.
  string user_id = 1 [(pbflags.dimension) = {
    description: "Authenticated user identifier"
    hashable: true  // Can be used as a launch/experiment ramp dimension
  }];

  string session_id = 2 [(pbflags.dimension) = {
    description: "Browser session (unauthenticated users)"
    hashable: true
  }];

  // Enum dimensions — bounded cardinality, compile-time safe constructors.
  PlanLevel plan = 3 [(pbflags.dimension) = {
    description: "Subscription tier"
  }];

  DeviceType device_type = 4 [(pbflags.dimension) = {
    description: "Client device class"
  }];

  // Enum dimensions with low cardinality.
  Region region = 5 [(pbflags.dimension) = {
    description: "Deployment region"
  }];

  Environment environment = 6 [(pbflags.dimension) = {
    description: "Deployment environment"
  }];
}
```

New proto extensions in `options.proto`:

```protobuf
message ContextOptions {}

message DimensionOptions {
  string description = 1;
  // Whether this dimension can be used for percentage-based hashing
  // (launches, experiments). Typically true for stable identifiers
  // like user_id, false for attributes like plan or device_type.
  bool hashable = 2;
  // Explicitly declare a string dimension as having bounded cardinality.
  // Enum, bool, and int64 dimensions are inherently bounded. String
  // dimensions are unbounded by default. Setting this to true allows
  // the dimension to be referenced in launch/experiment population
  // conditions without a sync error. Use for string dimensions with
  // a known-finite value space that isn't worth modeling as an enum
  // (e.g., a frequently-changing list of partner IDs).
  bool bounded = 3;
}

extend google.protobuf.MessageOptions {
  optional ContextOptions context = 51003;
}

extend google.protobuf.FieldOptions {
  optional DimensionOptions dimension = 51004;
}
```

**Supported dimension types:**

| Proto type | CEL type | Generated constructor | Wire representation |
|------------|----------|----------------------|---------------------|
| `string` | `string` | `func User(id string) Dimension` | proto string field |
| `enum` | proto enum | `func Plan(p pb.PlanLevel) Dimension` | proto enum field (int32 ordinal) |
| `bool` | `bool` | `func IsInternal(b bool) Dimension` | proto bool field |
| `int64` | `int` | `func OrgSize(n int64) Dimension` | proto int64 field |

Because the wire carries the actual `EvaluationContext` proto message
(via `Any`), all types are represented natively — enum values are ordinals,
not strings. Type safety is end-to-end: compile-time at the call site
(proto enum type), proto-native on the wire, and proto-native in CEL
evaluation.

**Codegen validation:** Exactly one message in the input files must carry
`(pbflags.context)`. Fields must be `string`, `bool`, `int64`, or a proto
`enum` type. Duplicate field names are a proto-level error.

### Config: YAML format

One config file per feature. The file is named after the feature ID:

```yaml
# flags/notifications.yaml
feature: notifications

flags:
  # Static value (no conditions).
  email_enabled:
    value: true

  # Conditional value — evaluated top to bottom, first match wins.
  digest_frequency:
    conditions:
      - when: 'ctx.plan == PlanLevel.ENTERPRISE'
        value: "daily"
      - otherwise: "weekly"

  # Multiple conditions.
  max_retries:
    conditions:
      - when: 'ctx.plan == PlanLevel.ENTERPRISE'
        value: 10
      - when: 'ctx.plan == PlanLevel.PRO'
        value: 5
      - otherwise: 3

  # Entity-specific condition (replaces per-entity overrides).
  notification_emails:
    conditions:
      - when: 'ctx.user_id == "user-99"'
        value: ["special@example.com"]
      - when: 'ctx.user_id in ["user-1", "user-2", "user-3"]'
        value: ["beta@example.com", "ops@example.com"]
      - otherwise: ["ops@example.com"]
```

The top-level key is `flags:`, not `defaults:` — the section defines the
full behavior chain for each flag, including conditions. Launches and
experiments will be added as sibling sections in the future.

**Validation rules** (enforced by `pbflags-sync` at sync time):

- `feature` must match a feature ID in the proto descriptor.
- Each key under `flags` must match a field name in the feature message.
- Each `value` must be type-compatible with the proto field type.
- Each `when` expression must be a valid CEL expression that type-checks
  against the `EvaluationContext` message.
- CEL expressions must reference only declared dimension names (via `ctx.*`).
- Enum dimension comparisons must use registered enum constants (e.g.,
  `ctx.plan == PlanLevel.ENTERPRISE`). Comparing an enum dimension to a
  raw string is a type error.
- A flag may have either a static `value` or `conditions`, not both.
- Condition chains should have an `otherwise` clause. The sync tool warns
  if one is missing (evaluation falls through to the proto compiled default,
  which may be surprising).
- Every flag defined in the proto must have an entry in the config file.
  The sync tool errors if a flag has no config entry (no ambiguous halfway
  state between config-driven and DB-driven flags).

**Config file location:** Passed to `pbflags-sync` via a new `--config`
flag. Required — all flags must have config entries.

```bash
pbflags-sync \
  --descriptors=descriptors.pb \
  --config=flags/ \
  --database=postgres://...
```

### CEL: expression language

[CEL (Common Expression Language)](https://github.com/google/cel-go) is the
condition expression language. CEL is designed for exactly this use case:
safe, sandboxed expression evaluation in configuration and policy.

**Why CEL:**

- Sandboxed — no side effects, no I/O, no system access.
- Guaranteed to terminate — no loops, no recursion, no unbounded computation.
- Type-checkable at parse time — sync-time validation catches type errors
  before deployment.
- Mature Go implementation (`cel-go`), Java implementation (`cel-java`).
- Used in production at scale: Kubernetes admission webhooks, Firebase
  Security Rules, Google Cloud IAM conditions.

**CEL environment:** The sync tool builds the CEL type environment directly
from the `EvaluationContext` proto message descriptor using
`cel.Types(contextDesc)`. This registers `ctx` as a proto message variable
with full type information — field access, enum comparison, and type
checking all work natively.

Enum constants are registered with prefix-stripped names for readability
in config files:

| Proto enum value | CEL constant |
|-----------------|--------------|
| `PLAN_LEVEL_ENTERPRISE` | `PlanLevel.ENTERPRISE` |
| `DEVICE_TYPE_MOBILE` | `DeviceType.MOBILE` |

This is the standard CEL convention for proto enums. The sync tool
registers these constants automatically from the enum descriptors referenced
by the `EvaluationContext` fields.

**Restricted subset (v1):** Start with comparison and containment operators.
Expand to computed values and type coercion only when a concrete need
arises.

| Supported | Example |
|-----------|---------|
| Enum equality | `ctx.plan == PlanLevel.ENTERPRISE` |
| Enum inequality | `ctx.plan != PlanLevel.FREE` |
| String equality | `ctx.user_id == "user-99"` |
| Enum containment | `ctx.region in [Region.US_EAST, Region.US_WEST]` |
| Boolean logic | `ctx.plan == PlanLevel.PRO && ctx.device_type == DeviceType.MOBILE` |
| Negation | `!(ctx.environment == Environment.PRODUCTION)` |
| String presence | `ctx.user_id != ""` (dimension is set) |
| Bool dimension | `ctx.is_internal` |
| Int comparison | `ctx.org_size > 100` |

### Sync: compilation pipeline

`pbflags-sync` gains a config compilation step between descriptor parsing
and database write:

```
 Proto descriptor          Config files (YAML)
       │                          │
       ▼                          ▼
 Parse features/flags      Parse YAML, match to features
       │                          │
       ▼                          ▼
 Build CEL type env ◄── EvaluationContext from proto
       │
       ▼
 For each flag with conditions:
   1. Parse CEL expressions
   2. Type-check against context
   3. Validate value types against proto field type
   4. Walk CEL AST → extract referenced dimensions
   5. Compute per-flag dimension set (union across all conditions)
       │
       ▼
 Write to database:
   - Flag definitions (existing)
   - Compiled conditions + referenced dimensions (new)
```

**Dimension extraction and classification** works by walking the CEL AST
and collecting all `ctx.<name>` identifier references, then classifying
each dimension by how it's used:

| Classification | Pattern | Example | Cache impact |
|---------------|---------|---------|--------------|
| **Bounded** | Enum dimension, or compared against known-finite values | `ctx.plan == PlanLevel.ENTERPRISE` | Include in cache key. Cardinality bounded by enum size. |
| **Finite filter** | Unbounded dimension compared against string literals only (equality or `in`) | `ctx.user_id == "user-99"`, `ctx.user_id in ["u-1", "u-2"]` | Evaluate inline. Cache key uses match result (true/false), not the raw dimension value. Effective cardinality: 2. |
| **Unbounded** | Unbounded dimension referenced in any other pattern | `ctx.user_id != ""` (presence check) | Include in cache key. Sync emits a warning. Evaluator uses LRU cap. |

For a condition chain like:

```yaml
conditions:
  - when: 'ctx.plan == PlanLevel.ENTERPRISE'
    value: true
  - when: 'ctx.user_id in ["user-1", "user-2"]'
    value: true
  - otherwise: false
```

`plan` is classified as **bounded** (enum), `user_id` as **finite filter**
(only appears in an `in` check against literals). The cache key for this
flag is `"notifications/1|plan=3|user_id:match=true"` — not
`"notifications/1|plan=3|user_id=user-123"`. This avoids
unbounded cache growth from allowlist-style conditions that replace
per-entity overrides.

The classification metadata is stored alongside the compiled conditions
in the database.

**CEL version stamp:** The sync tool records the CEL library version used
for compilation alongside the conditions. The evaluator checks this version
at load time. See "CEL compile-failure handling" in the evaluator section.

### DB: schema changes

New columns on the existing `flags` table:

```sql
ALTER TABLE feature_flags.flags
    ADD COLUMN conditions JSONB DEFAULT NULL,
    ADD COLUMN dimension_metadata JSONB DEFAULT NULL,
    ADD COLUMN cel_version VARCHAR(50) DEFAULT NULL;
```

The `conditions` column stores the compiled condition chain as a JSON array:

```json
[
  {"cel": "ctx.plan == \"enterprise\"", "value": {"string_value": "daily"}},
  {"cel": null, "value": {"string_value": "weekly"}}
]
```

A `null` cel field represents the `otherwise` catch-all. The `value` object
uses the same structure as the existing `default_value` column for
consistency.

`dimension_metadata` stores per-dimension classification and cache
key behavior, computed by the sync tool's AST analysis:

```json
{
  "plan": {"classification": "bounded", "cache_key": true},
  "user_id": {"classification": "finite_filter", "cache_key": false,
              "literal_set": ["user-1", "user-2"]}
}
```

`cel_version` records the CEL library version used by the sync tool when
compiling the conditions (e.g., `"cel-go/0.22.1"`). The evaluator checks
this at load time to detect version skew.

**Why columns, not a separate table:** The condition chain is a property of
a flag — it's always loaded alongside the flag definition and never queried
independently. A JSONB column keeps the data model simple and avoids an
additional join in the definition load query. If condition chains grow large
enough to warrant separate storage (e.g., hundreds of conditions per flag),
a separate table can be introduced later.

### Evaluator: condition evaluation and caching

**Evaluation precedence** (replaces the current chain):

1. **KILLED** → compiled default (unchanged)
2. **Conditions** → evaluate condition chain against context, first match wins
3. **Static config value** → the `value` from config (no conditions)
4. **Compiled default** → from proto (ultimate safety net)

Every flag has a config entry (enforced by sync). There is no fallback to
the old DB-value-based evaluation. This eliminates the ambiguous halfway
state where some flags are config-driven and others are DB-driven.

**Condition evaluation flow:**

```
Evaluator receives: (flag_id, Any context)

1. Deserialize Any → dynamicpb.Message (using loaded descriptor)
2. Check kill set → if killed, return compiled default
3. Load flag's condition chain (cached, refreshed with definitions)
4. Build cache key using dimension metadata:
   - Bounded dimensions: include "dim=value" in key
   - Finite-filter dimensions: evaluate literal-set membership,
     include "dim:match=true|false" in key
   - Unbounded dimensions: include "dim=value" in key (LRU-capped)
   e.g., "notifications/2|plan=2|user_id:match=false"
5. Check evaluation cache → return if hit
6. Iterate conditions in order:
   - Evaluate CEL program with deserialized proto message
   - First match → cache result, return value
   - No match and no otherwise → return compiled default
```

**Cache key construction:**

```go
func cacheKey(flagID string, ctx protoreflect.Message, meta DimensionMetadata) string {
    key := flagID
    for _, dim := range meta.Sorted() {
        fd := ctx.Descriptor().Fields().ByName(protoreflect.Name(dim.Name))
        val := ctx.Get(fd)
        switch dim.Classification {
        case Bounded, Unbounded:
            key += "|" + dim.Name + "=" + formatValue(fd, val)
        case FiniteFilter:
            matched := dim.LiteralSet.Contains(formatValue(fd, val))
            key += "|" + dim.Name + ":match=" + strconv.FormatBool(matched)
        }
    }
    return key
}
```

A flag whose conditions only reference `plan` (3 enum values) has at most
3 cache entries. A flag with a `ctx.user_id in [...]` allowlist condition
has at most 2 × (other dimension cardinality) entries — the allowlist
membership is collapsed to a boolean, not expanded per user. A flag with no
conditions (static value) has a cache key of just its flag ID — one entry.

**Cache cardinality guard:** The evaluation cache uses an LRU eviction
policy with a configurable max size (default: 10,000 entries). This bounds
memory usage even if a flag references an unbounded dimension in an
unrecognized pattern. The sync tool warns when this could happen:

```
warning: flag "notifications/5" references unbounded dimension "user_id"
  in pattern "ctx.user_id != \"\"" (not a finite filter).
  Cache key will include user_id values — consider using equality/membership
  checks against literals, or review caching impact.
```

**CEL program compilation:** CEL programs are compiled once when the flag
definition is loaded (at startup and on definition refresh). The compiled
`cel.Program` objects are reused across evaluations.

**CEL compile-failure handling:** If a CEL program fails to compile at load
time (e.g., due to version skew between the sync tool and evaluator), the
evaluator:

1. Logs an error identifying the flag and the compilation error.
2. Falls back to the compiled default for the affected flag.
3. Reports degraded health via the `Health` RPC (`EVALUATOR_STATUS_DEGRADED`).
4. Continues serving all other flags normally.

This ensures a CEL version mismatch degrades gracefully (one flag gets its
compiled default) rather than failing catastrophically (evaluator refuses
to start). The `cel_version` column lets operators diagnose the issue: if
the evaluator's CEL runtime is older than what sync used, the evaluator
needs to be updated before redeploying the affected config.

**Wire protocol changes:**

```protobuf
import "google/protobuf/any.proto";

message EvaluateRequest {
  string flag_id = 1;
  reserved 2;                         // was: entity_id
  google.protobuf.Any context = 3;    // serialized EvaluationContext
}
```

The `context` field carries the customer's `EvaluationContext` proto message
wrapped in `Any`. This preserves full proto typing across the wire — enum
fields carry their ordinal, bool fields are bools, etc.

The pbflags proto cannot import the customer's `EvaluationContext` directly
(it's defined in the customer's proto package). `Any` is the standard proto
pattern for this: it wraps any message as serialized bytes plus a type URL
(e.g., `"type.googleapis.com/myapp.EvaluationContext"`).

**Client side:** The generated client builds an `EvaluationContext` message
from the accumulated dimensions and wraps it in `Any`:

```go
func (e *evaluator) Evaluate(ctx context.Context, flagID string) (*Result, error) {
    anyCtx, _ := anypb.New(e.contextMessage())
    req := &pbflagsv1.EvaluateRequest{FlagId: flagID, Context: anyCtx}
    // ...
}
```

**Evaluator side:** The evaluator already loads the customer's proto
descriptor at startup (for flag definitions). It uses the same descriptor
to find the `EvaluationContext` message type and deserialize the `Any`:

```go
// At startup: find context message descriptor (the one with pbflags.context)
contextDesc := findContextMessage(descriptors)

// Per request:
dynMsg := dynamicpb.NewMessage(contextDesc)
if err := req.Context.UnmarshalTo(dynMsg); err != nil { ... }

// CEL evaluates directly against the proto message
result, _, _ := program.Eval(map[string]interface{}{"ctx": dynMsg})
```

`dynamicpb.Message` implements `protoreflect.ProtoMessage`, which cel-go
supports natively. Field access (`ctx.plan`), enum comparison
(`ctx.plan == PlanLevel.ENTERPRISE`), and type checking all work against
the dynamic message without any string conversion.

`BulkEvaluateRequest` gets the same `Any context` field. Each flag in the
bulk request is evaluated against the same deserialized context message.

### Client API: scoped evaluator pattern

The evaluator is the unit of composition. It carries both a connection to
the flag service and bound context dimensions. The `.With()` method creates
a new evaluator with additional dimensions, forming a natural scope chain.

#### Go

```go
// ── Process startup ──

// Connect to the evaluator service.
eval := pbflags.Connect(httpClient, "http://localhost:9201")

// Bind process-level dimensions (set once, shared across all requests).
global := eval.With(
    dims.Env(pb.Environment_ENVIRONMENT_PRODUCTION),
    dims.Reg(pb.Region_REGION_US_EAST),
)


// ── Request middleware ──

func flagsMiddleware(global pbflags.Evaluator) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            user := auth.UserFrom(r.Context())

            var scoped pbflags.Evaluator
            if user != nil {
                scoped = global.With(
                    dims.User(user.ID),
                    dims.Plan(dims.PlanLevel(user.Plan)),
                    dims.Device(detectDevice(r)),
                )
            } else {
                scoped = global.With(
                    dims.SessionID(sessionIDFromCookie(r)),
                    dims.Device(detectDevice(r)),
                )
            }

            ctx := pbflags.ContextWith(r.Context(), scoped)
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}


// ── Handler ──

func handleSettings(w http.ResponseWriter, r *http.Request) {
    eval := pbflags.FromContext(r.Context())
    flags := notificationsflags.New(eval)

    if flags.EmailEnabled(r.Context()) {
        // ...
    }
    freq := flags.DigestFrequency(r.Context())
}
```

The generated flag interface keeps `context.Context` as its only parameter.
This is Go-idiomatic (cancellation, timeouts, tracing) and removes all
layer parameters:

```go
// Current (layers):
type NotificationsFlags interface {
    EmailEnabled(ctx context.Context, user layers.UserID) bool
    DigestFrequency(ctx context.Context) string
    NotificationEmails(ctx context.Context, entity layers.EntityID) []string
}

// New (context-driven):
type NotificationsFlags interface {
    EmailEnabled(ctx context.Context) bool
    DigestFrequency(ctx context.Context) string
    NotificationEmails(ctx context.Context) []string
}
```

**Evaluator interface:**

```go
package pbflags

// Evaluator provides flag evaluation with bound context dimensions.
// Evaluators are immutable — With() returns a new Evaluator, it does
// not modify the receiver.
type Evaluator interface {
    // With returns a new Evaluator with additional context dimensions bound.
    // Dimensions from the parent are preserved; new dimensions override
    // any existing dimension with the same name.
    With(dims ...Dimension) Evaluator

    // Evaluate resolves a single flag against the bound context.
    // Called by generated client code — not typically called directly.
    Evaluate(ctx context.Context, flagID string) (*Result, error)

    // BulkEvaluate resolves multiple flags against the bound context.
    BulkEvaluate(ctx context.Context, flagIDs []string) ([]*Result, error)
}

// Dimension sets a field on the EvaluationContext proto message.
// Constructed via the generated dims package — callers never build
// these directly.
type Dimension interface {
    Apply(msg protoreflect.Message)
}
```

The `Dimension` interface uses `protoreflect.Message` so that the generated
dims constructors can set typed fields directly on the proto message. The
evaluator accumulates dimensions and applies them to build the
`EvaluationContext` proto before serializing it into `Any` for the RPC.

**Generated dims package** (replaces `layers` package):

```go
// Generated from the EvaluationContext proto message.
package dims

import (
    "github.com/.../pbflags"
    pb "github.com/.../examplepb"  // generated proto types
    "google.golang.org/protobuf/reflect/protoreflect"
)

// String dimensions — identifiers with unbounded cardinality.
func User(id string) pbflags.Dimension       { return stringDim("user_id", id) }
func SessionID(id string) pbflags.Dimension   { return stringDim("session_id", id) }

// Enum dimensions — typed constructors using proto enum types.
func Plan(p pb.PlanLevel) pbflags.Dimension       { return enumDim("plan", int32(p)) }
func Device(d pb.DeviceType) pbflags.Dimension     { return enumDim("device_type", int32(d)) }
func Reg(r pb.Region) pbflags.Dimension            { return enumDim("region", int32(r)) }
func Env(e pb.Environment) pbflags.Dimension       { return enumDim("environment", int32(e)) }
```

String dimensions accept any string. Enum dimensions accept the
proto-generated enum type (`pb.PlanLevel`, `pb.DeviceType`), so callers
write `dims.Plan(pb.PlanLevel_PLAN_LEVEL_ENTERPRISE)` — the proto enum
constants are the source of truth. This is full compile-time safety using
the same types that proto already generates.

The `stringDim` and `enumDim` helpers (in `pbflags` core) implement the
`Dimension` interface by setting the named field on the proto message:

```go
// In pbflags core:
type stringDim struct{ name, value string }
func (d stringDim) Apply(msg protoreflect.Message) {
    fd := msg.Descriptor().Fields().ByName(protoreflect.Name(d.name))
    msg.Set(fd, protoreflect.ValueOfString(d.value))
}
```

For Java, enum dimensions use the proto-generated enum type directly:

```java
public static Dimension plan(PlanLevel p) {
    return new EnumDimension("plan", p.getNumber());
}
```

**Testing helpers** adapt accordingly:

```go
// Defaults() — unchanged semantics, no layer params.
func Defaults() NotificationsFlags

// Testing() — func fields lose layer params.
type TestNotificationsFlags struct {
    EmailEnabledFunc    func(context.Context) bool
    DigestFrequencyFunc func(context.Context) string
    // ...
}
func Testing() *TestNotificationsFlags
```

**Storing evaluator in context.Context:**

```go
// ContextWith stores an Evaluator in a context.Context.
func ContextWith(ctx context.Context, eval Evaluator) context.Context

// FromContext retrieves the Evaluator from a context.Context.
// Returns a no-op evaluator (all compiled defaults) if none is set.
func FromContext(ctx context.Context) Evaluator
```

`FromContext` returns a safe default (compiled defaults, never nil) when
no evaluator is set, preserving the never-throw guarantee.

#### Java

```java
// ── Application scope (Dagger @Singleton / Spring @Bean) ──

@Provides @Singleton
Evaluator globalEvaluator(HttpClient client) {
    return Evaluator.connect(client, "http://localhost:9201")
        .with(Dims.env(Environment.PRODUCTION))
        .with(Dims.reg(Region.US_EAST));
}


// ── Request scope (Dagger @RequestScoped / Spring @RequestScope) ──

@Provides @RequestScoped
Evaluator requestEvaluator(
        @Singleton Evaluator global,
        HttpServletRequest request) {
    User user = Auth.userFrom(request);
    if (user != null) {
        return global.with(
            Dims.user(user.id()),
            Dims.plan(PlanLevel.valueOf(user.plan())),
            Dims.device(DeviceDetector.detect(request))
        );
    } else {
        return global.with(
            Dims.sessionId(Sessions.idFrom(request)),
            Dims.device(DeviceDetector.detect(request))
        );
    }
}


// ── Handler ──

@Inject NotificationsFlags flags;

void handleSettings(HttpServletRequest req, HttpServletResponse resp) {
    if (flags.emailEnabled()) {
        // ...
    }
    String freq = flags.digestFrequency();
}
```

The generated Java interface changes from `Flag<T>` / `LayerFlag<T, ID>`
to direct return types:

```java
// Current (layers):
public interface NotificationsFlags {
    LayerFlag<Boolean, UserID> emailEnabled();  // .get(UserID.of("..."))
    Flag<String> digestFrequency();             // .get()
}

// New (context-driven):
public interface NotificationsFlags {
    boolean emailEnabled();
    String digestFrequency();
    List<String> notificationEmails();
}
```

The `Evaluator` class:

```java
public class Evaluator {
    public static Evaluator connect(HttpClient client, String url) { ... }

    /** Returns a new Evaluator with additional dimensions bound. */
    public Evaluator with(Dimension... dims) { ... }

    // Internal — called by generated code.
    Result evaluate(String flagId) { ... }
}
```

### Admin UI: role change for configured flags

The shift from DB-driven to config-driven flag behavior changes what the
admin UI is for. Today the admin UI is where you set flag values. In the
new model, flag values come from config (git). The admin UI becomes an
**operational control plane** — where you kill things and monitor state.

**What the admin UI shows for configured flags:**

| Panel | Current behavior | New behavior |
|-------|-----------------|--------------|
| Flag value | Editable (set global value) | **Read-only.** Displays the condition chain from config (rendered as a table of conditions → values). Shows which config file and commit defined it. |
| Kill switch | Toggle KILLED state | **Unchanged.** Still the fast operational lever. |
| Overrides | Add/edit per-entity overrides | **Removed.** Overrides are now conditions in config. UI links to the config file for editing. |
| State (ENABLED/DEFAULT) | Toggle between ENABLED and DEFAULT | **Removed.** State is always defined by the config's condition chain. |
| Audit log | Logs DB state changes | **Extended.** Logs kill/unkill actions (DB) and references config deployments (git SHA from sync). |

**Environment-specific behavior:** Differences between environments are
expressed as conditions on the `environment` dimension in config:

```yaml
flags:
  expensive_feature:
    conditions:
      - when: 'ctx.environment == Environment.PRODUCTION && ctx.plan == PlanLevel.ENTERPRISE'
        value: true
      - when: 'ctx.environment != Environment.PRODUCTION'
        value: true   # enabled everywhere except prod-non-enterprise
      - otherwise: false
```

This replaces the current pattern of manually configuring different values
per environment's database. All environments get the same config file; the
conditions make environment-specific behavior explicit and reviewable.

**Audit across git + DB:** Two audit streams, each authoritative for its
domain:

- **Config changes** (conditions, values): git history. The admin UI
  shows the current git SHA that was last synced and links to the commit.
- **Operational changes** (kills, future ramp/experiment actions): DB
  audit log. Same as today.

### Generated code changes

**New packages generated:**

| Package | Replaces | Contents |
|---------|----------|----------|
| `dims` | `layers` | Typed dimension constructors from `EvaluationContext` proto |

**Changed packages:**

| Package | Change |
|---------|--------|
| Feature package (e.g., `notificationsflags`) | Method signatures lose layer params. Constructor takes `Evaluator` instead of `FlagEvaluatorServiceClient`. |
| `flagmeta` | `FlagDescriptor` drops `HasEntityID` / `LayerType` fields. |

**Removed packages:**

| Package | Reason |
|---------|--------|
| `layers` | Replaced by `dims`. |

### Migration: clean cut from layers

pbflags currently has one active consumer (Spotlight). A clean break is
simpler and safer than a dual-mode transition — the risk is not external
compatibility, it's avoiding an ambiguous halfway state where generated
code, YAML config, DB state, and evaluator behavior disagree about which
model owns a flag.

**Migration sequence:**

| Step | What changes | Verifiable state |
|------|-------------|-----------------|
| 1. **Proto** | Remove `Layer` enum. Add `EvaluationContext` message with dimensions (existing layers become hashable string dimensions, plus any new typed dimensions). Remove `layer` field from `FlagOptions`. | Proto compiles, codegen runs. |
| 2. **Codegen** | Codegen produces `dims` package (not `layers`). Generated feature interfaces use `pbflags.Evaluator` constructor, `context.Context`-only getters. | Generated code compiles. |
| 3. **Consumer** | Update Spotlight to scoped evaluator pattern: construct global evaluator, bind request-scoped dimensions in middleware, remove all layer ID parameters from flag callsites. | Consumer compiles and passes unit tests (using `Testing()` / `Defaults()`). |
| 4. **Config export** | Run migration tool: export existing DB state (global values, per-entity overrides) as YAML config files. Global values become static `value:` entries. Per-entity overrides become `ctx.user_id == "..."` or `ctx.entity_id == "..."` conditions. Review generated YAML for correctness. | Config files exist for every flag. `pbflags config validate` passes. |
| 5. **Sync + evaluator** | Deploy updated `pbflags-sync` (reads config, compiles CEL, writes conditions to DB) and updated evaluator (evaluates conditions). DB migration adds `conditions`, `dimension_metadata`, `cel_version` columns. | Sync succeeds. Evaluator starts and serves. |
| 6. **Verify** | Smoke test: for each flag with overrides, verify the condition-based evaluation matches the previous override-based evaluation for the same entities. | Flag values match pre-migration behavior. |
| 7. **Cleanup** | Drop `flag_overrides` table. Drop `flags.layer` column. Remove `flags.state` and `flags.value` columns (behavior now fully in `conditions`). Remove `(pbflags.layers)` extension from `options.proto`. | Schema clean. No dead columns. |

**Export tool:** `pbflags config export` reads the current DB state and
generates YAML config files:

```yaml
# Auto-generated from DB state. Review before committing.
feature: notifications

flags:
  # Was: state=ENABLED, value=true, layer=user
  # Overrides: user-99 → false
  email_enabled:
    conditions:
      - when: 'ctx.user_id == "user-99"'
        value: false
      - otherwise: true

  # Was: state=ENABLED, value="daily", layer=global
  digest_frequency:
    value: "daily"
```

The export tool translates the old model into the new one mechanically.
The generated YAML is a starting point — the team reviews it and may
refactor conditions (e.g., replacing individual user overrides with
attribute-based conditions) before committing.

**What makes this safe:** Steps 1–3 are code-only changes that don't
touch the running system. Step 4 produces config files that can be
reviewed before deployment. Step 5 is the atomic cutover — sync and
evaluator deploy together, and the evaluator's new precedence chain
takes over. Step 6 verifies before cleanup. At no point are two models
active simultaneously.

## Future work

### Launches

A launch gradually rolls out a new condition chain for a flag, ramping from
0% to 100% along a hashable dimension. Defined in config, ramp controlled
via admin UI / CLI.

```yaml
launches:
  daily-digest-for-pro:
    flag: digest_frequency
    description: "Roll out daily digest to pro users"
    dimension: user_id
    population: 'ctx.plan == PlanLevel.PRO'
    treatment:
      conditions:
        - value: "daily"
```

**Lifecycle:** Created → Active (ramping) → Baked (100%, monitoring) →
Completed (treatment replaces default, config updated via PR) or Abandoned
(rolled back, launch removed from config).

The admin UI / CLI controls the ramp percentage and can kill a launch.
Completing a launch is a git operation: the config file is updated to
promote the treatment to the new default and the launch definition is
removed.

Each launch gets its own independent slicing layer — entities are hashed
into "in ramp" or "not in ramp" independently of any other launch or
experiment.

**Dimension restrictions in launches and experiments:**

`population` conditions in launches and experiments are restricted to
**bounded dimensions only** — enum, bool, int64, or string dimensions
explicitly annotated with `bounded: true`. The sync tool rejects
`population` conditions that reference unbounded string dimensions:

```
error: launch "daily-digest-for-pro" population references unbounded
  dimension "user_id" in condition "ctx.user_id != \"\"".
  Only bounded dimensions (enum, bool, int64, or string with
  bounded: true) may appear in population conditions.
```

This prevents unbounded cache growth in the launch/experiment evaluation
path and ensures population membership is cheap to compute. If a string
dimension truly has bounded cardinality (e.g., a small set of partner IDs),
annotate it with `bounded: true` in the proto definition to opt in.

The `dimension` field (hash target for ramp/assignment) is exempt from
this restriction — hashing is inherently bounded (modulo 100).

Note: this restriction applies to launches and experiments only, not to
base flag conditions. Base conditions can reference unbounded dimensions
freely (subject to the sync warning and LRU cache cap described in the
evaluator section).

### Experiments

An experiment randomly assigns entities to variants for controlled testing.
Independent of launches via independent slicing — both a launch and an
experiment can be active on the same flag because they slice the population
independently. The launch affects both control and treatment groups equally,
so the experiment remains valid.

```yaml
experiments:
  notification-cadence:
    flag: digest_frequency
    dimension: user_id
    population: 'ctx.plan == PlanLevel.PRO'
    variants:
      control:
        weight: 50
      daily:
        weight: 50
        value: "daily"
```

**Assignment:** Deterministic hash — `hash(experiment_id + dimension_value)
% 100`. Same input always produces the same bucket. No database write
needed for assignment.

**Logging:** The evaluator remains stateless. The generated client SDK logs
experiment assignments transparently via an optional `ExperimentLogger`
callback. The caller just sees the typed flag value.

**Slicing layers:** By default, one experiment layer (experiments are
mutually exclusive within a layer). Additional experiment layers can be
enabled if the customer can reason about interaction safety — but that's
hard to reason about, so the default is conservative.

### Cardinality lint rules

The per-flag dimension metadata enables future lint rules:

- "Flag X references `user_id` (unbounded) — consider whether this is
  intentional" (warning).
- "Flag X has estimated cardinality > 10,000 — review caching impact"
  (configurable threshold).
- Project-level max cardinality policy.

## Implementation plan

| Phase | Work |
|-------|------|
| **1. Proto extensions** | Add `context` (51003) and `dimension` (51004) extensions to `options.proto`. Reserve `entity_id` field (2) and add `context` field (3) to `EvaluateRequest` and `BulkEvaluateRequest`. Remove `layers` extension and `FlagOptions.layer`. |
| **2. Context discovery** | New `contextutil` package (parallel to `layerutil`) that discovers the `EvaluationContext` message and extracts dimension metadata. Codegen validation: exactly one context message, fields are string/enum/bool/int64. |
| **3. Dims codegen** | Generate `dims` package from context message (replaces `layers`). String dimensions get `func(string)` constructors. Enum dimensions get typed string constants and typed constructors. |
| **4. Evaluator interface** | New `pbflags.Evaluator` interface with `With()` and `Evaluate()`. Implementation wraps `FlagEvaluatorServiceClient`, merges dimensions, populates `EvaluateRequest.context`. `ContextWith` / `FromContext` for Go `context.Context` integration. |
| **5. Feature codegen** | Update generated interfaces: remove layer params, constructor takes `pbflags.Evaluator`. Update `Defaults()`, `Testing()`, `FlagDescriptors`. Remove `layers` package generation. |
| **6. Consumer migration** | Update Spotlight to scoped evaluator pattern. Breaking API change, coordinated. |
| **7. Config parser** | YAML parser + validator. Matches feature/field names to proto descriptor. Validates value types. Validates enum dimension values against declared enums. Enforces every proto flag has a config entry. |
| **8. CEL integration** | Build CEL type environment from `EvaluationContext` proto (typed dimensions, enum value validation). Parse and type-check CEL expressions. AST walker for dimension extraction and classification (bounded/finite-filter/unbounded). |
| **9. DB migration** | Add `conditions` (JSONB), `dimension_metadata` (JSONB), `cel_version` (VARCHAR) columns to `flags` table. |
| **10. Sync: config compilation** | `pbflags-sync --config=flags/` parses YAML, compiles CEL, classifies dimensions, writes conditions + metadata + version to flags table. Emits warnings for unbounded dimension references. |
| **11. Evaluator: conditions** | Load compiled conditions with flag definitions. Compile CEL programs at load time (with graceful failure → compiled default + degraded health). Evaluate condition chain on cache miss. Cache by dimension-classified key. LRU eviction with configurable cap. |
| **12. Config export tool** | `pbflags config export` reads existing DB state (global values, overrides) and generates YAML config files. Translates per-entity overrides to conditions. |
| **13. CLI** | `pbflags config validate` — check YAML + CEL against proto. `pbflags config show <flag>` — render effective condition chain for a flag. |
| **14. Admin UI updates** | Flag value panel becomes read-only (displays condition chain). Remove override management. Show sync git SHA. Link to config file for editing. |
| **15. Cleanup** | Drop `flag_overrides` table. Drop `flags.layer`, `flags.state`, `flags.value` columns. Remove `layerutil` package. Remove `(pbflags.layers)` extension. |
