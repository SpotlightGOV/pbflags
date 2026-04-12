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

1. Define **evaluation context** in proto — typed dimensions that conditions
   can inspect, replacing the current layers enum.
2. Introduce a **YAML config format** for defining flag default values and
   conditions alongside proto definitions.
3. Evaluate **conditions** using CEL (Common Expression Language) — safe,
   sandboxed, type-checked at sync time.
4. **Per-flag dimension tracking** — automatic cache key optimization based
   on which context dimensions each flag's conditions actually reference.
5. **Scoped evaluator API** — construct an evaluator with context that
   naturally fits application scopes (process → request → handler).
6. Generated client methods take only `context.Context` (Go) or are
   zero-arg (Java) — no layer parameters.
7. Flags without config conditions fall back to existing DB-based evaluation
   for backwards compatibility during migration.
8. Deprecate the current layers system.

## Non-Goals (v1)

- **Launches** (gradual rollout with ramp percentage). Deferred to a
  follow-on design that builds on the condition infrastructure.
- **Experiments** (randomized assignment with logging). Deferred. Depends
  on the launch slicing model.
- **UI-based condition editing**. v1 uses a CLI for config management.
- **Cardinality lint rules**. The per-flag dimension tracking provides the
  data; enforcement rules can be added later.
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

Dimensions are always string-valued on the wire. CEL expressions handle
any necessary type coercion. Missing (unset) dimensions have the zero
value (empty string), and conditions must be written to handle this — e.g.,
`ctx.plan == "enterprise"` naturally evaluates to false when `plan` is unset.

### Conditions

A condition is a pair: a CEL predicate and a value. A flag's condition chain
is an ordered list of conditions, evaluated top to bottom. The first
condition whose predicate matches the evaluation context determines the flag
value.

```yaml
defaults:
  digest_frequency:
    conditions:
      - when: 'ctx.plan == "enterprise"'
        value: "daily"
      - when: 'ctx.plan == "pro"'
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

message EvaluationContext {
  option (pbflags.context) = true;

  string user_id = 1 [(pbflags.dimension) = {
    description: "Authenticated user identifier"
    hashable: true  // Can be used as a launch/experiment ramp dimension
  }];

  string session_id = 2 [(pbflags.dimension) = {
    description: "Browser session (unauthenticated users)"
    hashable: true
  }];

  string plan = 3 [(pbflags.dimension) = {
    description: "Subscription tier"
  }];

  string device_type = 4 [(pbflags.dimension) = {
    description: "Client device class"
  }];

  string region = 5 [(pbflags.dimension) = {
    description: "Deployment region"
  }];

  string environment = 6 [(pbflags.dimension) = {
    description: "Deployment environment (production, staging, etc.)"
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
}

extend google.protobuf.MessageOptions {
  optional ContextOptions context = 51003;
}

extend google.protobuf.FieldOptions {
  optional DimensionOptions dimension = 51004;
}
```

**Codegen validation:** Exactly one message in the input files must carry
`(pbflags.context)`. All fields must be `string` type (context values are
always strings on the wire). Duplicate field names are a proto-level error.

### Config: YAML format

One config file per feature. The file is named after the feature ID:

```yaml
# flags/notifications.yaml
feature: notifications

defaults:
  # Static default (no conditions).
  email_enabled:
    value: true

  # Conditional default.
  digest_frequency:
    conditions:
      - when: 'ctx.plan == "enterprise"'
        value: "daily"
      - otherwise: "weekly"

  # Multiple conditions, evaluated top to bottom.
  max_retries:
    conditions:
      - when: 'ctx.plan == "enterprise"'
        value: 10
      - when: 'ctx.plan == "pro"'
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

**Validation rules** (enforced by `pbflags-sync` at sync time):

- `feature` must match a feature ID in the proto descriptor.
- Each key under `defaults` must match a field name in the feature message.
- Each `value` must be type-compatible with the proto field type.
- Each `when` expression must be a valid CEL expression that type-checks
  against the `EvaluationContext` message.
- CEL expressions must reference only declared dimension names (via `ctx.*`).
- A flag may have either a static `value` or `conditions`, not both.
- Condition chains should have an `otherwise` clause. The sync tool warns
  if one is missing (evaluation falls through to the proto compiled default,
  which may be surprising).

**Config file location:** Passed to `pbflags-sync` via a new `--config`
flag. If omitted, no conditions are loaded and flags evaluate using existing
DB state (backwards compatibility).

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

**CEL environment:** The sync tool builds a CEL type environment from the
proto-defined `EvaluationContext`. Each dimension becomes a string-typed
variable accessible via `ctx.<dimension_name>`. The type checker verifies
that expressions only reference declared dimensions and use valid operators.

**Restricted subset (v1):** Start with comparison and containment operators.
Expand to computed values and type coercion only when a concrete need
arises.

| Supported | Example |
|-----------|---------|
| Equality | `ctx.plan == "enterprise"` |
| Inequality | `ctx.plan != "free"` |
| Containment | `ctx.region in ["us-east", "us-west"]` |
| Boolean logic | `ctx.plan == "pro" && ctx.device_type == "mobile"` |
| Negation | `!(ctx.environment == "production")` |
| String presence | `ctx.user_id != ""` (dimension is set) |

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

**Dimension extraction** works by walking the CEL AST and collecting all
`ctx.<name>` identifier references. For a condition chain like:

```yaml
conditions:
  - when: 'ctx.plan == "enterprise"'
    value: true
  - when: 'ctx.user_id in ["user-1", "user-2"]'
    value: true
  - otherwise: false
```

The referenced dimensions for this flag are `["plan", "user_id"]` — the
union across all conditions.

### DB: schema changes

Two new columns on the existing `flags` table:

```sql
ALTER TABLE feature_flags.flags
    ADD COLUMN conditions JSONB DEFAULT NULL,
    ADD COLUMN referenced_dimensions TEXT[] DEFAULT '{}';
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

`referenced_dimensions` stores the dimension names extracted from the CEL
AST, used by the evaluator for cache key construction.

**Why columns, not a separate table:** The condition chain is a property of
a flag — it's always loaded alongside the flag definition and never queried
independently. A JSONB column keeps the data model simple and avoids an
additional join in the definition load query. If condition chains grow large
enough to warrant separate storage (e.g., hundreds of conditions per flag),
a separate table can be introduced later.

### Evaluator: condition evaluation and caching

**Evaluation precedence** (updated from the current chain):

1. **KILLED** → compiled default (unchanged)
2. **Conditions** → evaluate condition chain against context, first match wins
3. **Static config default** → the `value` from config (no conditions)
4. **DB global value** → existing `state: ENABLED` + `value` from DB
   (backwards compat for flags without config)
5. **Compiled default** → from proto (ultimate safety net)

Steps 2–3 only apply when the flag has a config entry. Flags without config
entries follow the existing path (steps 4–5), preserving full backwards
compatibility during migration.

**Condition evaluation flow:**

```
Evaluator receives: (flag_id, context map<string,string>)

1. Check kill set → if killed, return compiled default
2. Load flag's condition chain (cached, refreshed with definitions)
3. If flag has conditions:
   a. Build cache key: flag_id + referenced dimension values only
      e.g., "notifications/2|plan=enterprise" (only plan is referenced)
   b. Check evaluation cache → return if hit
   c. Iterate conditions:
      - Evaluate CEL program with context
      - First match → cache result, return value
      - No match and no otherwise → return compiled default
4. If flag has no conditions:
   a. Existing evaluation path (DB global value / compiled default)
```

**Cache key construction:**

```go
func cacheKey(flagID string, ctx map[string]string, dims []string) string {
    // dims is pre-sorted and pre-computed at definition load time
    key := flagID
    for _, dim := range dims {
        key += "|" + dim + "=" + ctx[dim]
    }
    return key
}
```

A flag whose conditions only reference `plan` (3 enum values) has at most
3 cache entries — regardless of how many users exist. A flag with no
conditions (static default) has a cache key of just its flag ID — one entry,
same as today.

**CEL program compilation:** CEL programs are compiled once when the flag
definition is loaded (at startup and on definition refresh). The compiled
`cel.Program` objects are reused across evaluations. Compilation is the
expensive step; evaluation is fast.

**Wire protocol changes:**

```protobuf
message EvaluateRequest {
  string flag_id = 1;
  string entity_id = 2;              // deprecated, backwards compat
  map<string, string> context = 3;   // new: evaluation context dimensions
}
```

The evaluator checks `context` first. If empty, it falls back to
`entity_id` for backwards compatibility with old clients. Generated clients
always populate `context`.

`BulkEvaluateRequest` gets the same `context` field. Each flag in the
bulk request is evaluated against the same context.

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
    dims.Environment("production"),
    dims.Region("us-east"),
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
                    dims.Plan(user.Plan),
                    dims.DeviceType(detectDevice(r)),
                )
            } else {
                scoped = global.With(
                    dims.SessionID(sessionIDFromCookie(r)),
                    dims.DeviceType(detectDevice(r)),
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

// Dimension is a key-value pair in the evaluation context.
// Constructed via the generated dims package.
type Dimension struct {
    Name  string
    Value string
}
```

**Generated dims package** (replaces `layers` package):

```go
// Generated from the EvaluationContext proto message.
package dims

import "github.com/.../pbflags"

func User(id string) pbflags.Dimension     { return pbflags.Dimension{Name: "user_id", Value: id} }
func SessionID(id string) pbflags.Dimension { return pbflags.Dimension{Name: "session_id", Value: id} }
func Plan(p string) pbflags.Dimension      { return pbflags.Dimension{Name: "plan", Value: p} }
func DeviceType(d string) pbflags.Dimension { return pbflags.Dimension{Name: "device_type", Value: d} }
func Region(r string) pbflags.Dimension    { return pbflags.Dimension{Name: "region", Value: r} }
func Environment(e string) pbflags.Dimension { return pbflags.Dimension{Name: "environment", Value: e} }
```

Function names are derived from the proto field name using the same
convention as layer ID constructors today: `user_id` → `User`,
`session_id` → `SessionID`, `device_type` → `DeviceType`.

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
        .with(Dims.environment("production"))
        .with(Dims.region("us-east"));
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
            Dims.plan(user.plan()),
            Dims.deviceType(DeviceDetector.detect(request))
        );
    } else {
        return global.with(
            Dims.sessionId(Sessions.idFrom(request)),
            Dims.deviceType(DeviceDetector.detect(request))
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

### Migration from layers

The current layers system is deprecated in this design. Migration path:

**1. Proto changes:**

Replace the `Layer` enum with an `EvaluationContext` message. Each
non-global layer becomes a hashable dimension:

```protobuf
// Before:
enum Layer {
  option (pbflags.layers) = true;
  LAYER_UNSPECIFIED = 0;
  LAYER_GLOBAL = 1;
  LAYER_USER = 2;
  LAYER_ENTITY = 3;
}

// After:
message EvaluationContext {
  option (pbflags.context) = true;
  string user_id = 1 [(pbflags.dimension) = { hashable: true }];
  string entity_id = 2 [(pbflags.dimension) = { hashable: true }];
  // ... additional dimensions
}
```

The `layer` field on `FlagOptions` is deprecated. Flags no longer declare a
layer — the dimensions they vary on are determined by their conditions.

**2. Existing overrides → conditions:**

Per-entity overrides currently in the database are replaced by conditions
in config files:

```yaml
# Before: override set in admin UI
#   flag: notifications/1
#   entity_id: user-99
#   value: false

# After: condition in config
defaults:
  email_enabled:
    conditions:
      - when: 'ctx.user_id == "user-99"'
        value: false
      - otherwise: true
```

A migration tool can export existing overrides from the database as YAML
condition entries to bootstrap the config files.

**3. Client code migration:**

```go
// Before:
flags.EmailEnabled(ctx, layers.User("user-123"))

// After (evaluator already has user_id bound):
flags.EmailEnabled(ctx)
```

**4. Database cleanup:**

Once all flags have config conditions and all consumers are on the new
generated code, the `flag_overrides` table and `flags.layer` column can be
deprecated and eventually removed.

**Migration order:**

1. Add `EvaluationContext` proto and new `options.proto` extensions.
2. Update codegen to generate `dims` package and new client signatures.
3. Update consumer code (Spotlight) to use scoped evaluator pattern.
4. Write config files for existing flags. Export overrides to conditions.
5. Update `pbflags-sync` to compile and load config.
6. Update evaluator to evaluate conditions.
7. Deploy, verify conditions match existing behavior.
8. Remove `Layer` enum, `layers` package, `flag_overrides` table.

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
    population: 'ctx.plan == "pro"'
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
    population: 'ctx.plan == "pro"'
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
| **1. Proto extensions** | Add `context` (51003) and `dimension` (51004) extensions to `options.proto`. Add `context` field (3) to `EvaluateRequest` and `BulkEvaluateRequest`. |
| **2. Context discovery** | New `contextutil` package (parallel to `layerutil`) that discovers the `EvaluationContext` message and extracts dimension metadata. Codegen validation: exactly one context message, all fields are strings. |
| **3. Dims codegen** | Generate `dims` package from context message (replaces `layers`). One constructor function per dimension. |
| **4. Evaluator interface** | New `pbflags.Evaluator` interface with `With()` and `Evaluate()`. Implementation wraps `FlagEvaluatorServiceClient`, merges dimensions, populates `EvaluateRequest.context`. `ContextWith` / `FromContext` for Go `context.Context` integration. |
| **5. Feature codegen** | Update generated interfaces: remove layer params, constructor takes `pbflags.Evaluator`. Update `Defaults()`, `Testing()`, `FlagDescriptors`. |
| **6. Consumer migration** | Update Spotlight to use scoped evaluator pattern. This is a breaking API change, coordinated with the single consumer. |
| **7. Config parser** | YAML parser + validator. Matches feature/field names to proto descriptor. Validates value types. |
| **8. CEL integration** | Build CEL type environment from `EvaluationContext` proto. Parse and type-check CEL expressions. AST walker for dimension extraction. |
| **9. Sync: config compilation** | `pbflags-sync --config=flags/` parses YAML, compiles CEL, writes `conditions` and `referenced_dimensions` to flags table. DB migration for new columns. |
| **10. Evaluator: conditions** | Load compiled conditions with flag definitions. Compile CEL programs at load time. Evaluate condition chain on cache miss. Cache by `(flag_id, referenced_dim_values)`. |
| **11. Backwards compat** | Flags without config conditions use existing DB value path. `entity_id` on the wire falls back if `context` is empty. |
| **12. Deprecation** | Deprecate `(pbflags.layers)`, `FlagOptions.layer`, `layers` package in codegen. Warn at sync time if layers enum is still present. |
| **13. CLI** | `pbflags config validate` — check YAML + CEL against proto. `pbflags config show <flag>` — render effective config for a flag. |
| **14. Override export** | Tool to export existing per-entity overrides from DB as YAML condition entries, bootstrapping config files for migration. |
