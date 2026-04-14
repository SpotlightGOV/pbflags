# Philosophy and Design

## Proto as source of truth

Flag definitions live in `.proto` files. This means flag schemas are versioned in source control, reviewed in pull requests, and validated at compile time. The database stores runtime state (kill switches and synced condition metadata), but the shape of the flag system — which flags exist, their types, their compiled defaults, and their evaluation context dimensions — is defined in proto.

This gives you a property most feature flag systems don't have: your flag definitions are as reviewable and auditable as your code.

## Key design principles

- **Never-throw guarantee**: All evaluation errors return the compiled default. Application code never needs to handle flag evaluation failures.
- **Type-safe code generation**: Generated interfaces with compile-time type checking. You can't pass a user ID where an entity ID is expected.
- **Graceful degradation**: Stale values served instantly on cache expiry (background refresh in flight); last-resort stale fallback during outages; compiled defaults as the ultimate safety net. Flag evaluation never blocks after initial warmup and keeps working even if the database is unreachable.
- **Fast kill switches**: ~30s polling for emergency shutoffs by default. Kill a flag globally and it takes effect within one poll cycle. In write-through mode (`--cache-flag-ttl=0`), kills are checked inline on every evaluation — no polling delay.
- **Immutable identity**: Flag identity is `feature_id/field_number`, safe to rename proto fields without breaking existing state.
- **Audit trail**: All state changes logged with actor and timestamp.

## Flag evaluation precedence

The evaluator resolves flags using a three-step precedence chain:

1. **Killed** (`killed_at IS NOT NULL`) → compiled default (polled every ~30s, or checked inline in write-through mode)
2. **Condition chain with launch overrides** (CEL expressions evaluated top-to-bottom, first match wins; if matched condition has a launch override and entity is in ramp, return launch value) → condition or launch value
3. **Compiled default** (from proto definition) → always safe

The kill switch is the only runtime control — it overrides everything and forces the compiled default. There is no "enabled" or "disabled" toggle. A flag is either killed or it is live and evaluating its condition chain.

Conditions are CEL expressions defined in YAML config files and synced to the database by `pbflags-sync`. Each condition has an expression and a value; the evaluator walks the chain in order and returns the value of the first condition whose expression evaluates to true against the supplied evaluation context. If a matched condition has a **launch override**, the evaluator checks whether the entity is in the launch's ramp (via deterministic hashing on the launch's dimension). If so, the launch override value is returned instead of the base condition value. If no condition matches, the compiled default is returned.

**Launches** are gradual rollouts that modify what individual conditions return, not the structure of the condition chain. A launch defines a hash dimension and a ramp percentage (0–100); the launch-to-flag binding is expressed inline on conditions via `launch: {id, value}` in the YAML config. Launches can also override static flag values. Launches have their own kill switch (`killed_at` on the launch record) that is orthogonal to their lifecycle status (CREATED → ACTIVE → SOAKING → COMPLETED / ABANDONED).

This model keeps the evaluation path simple and auditable: proto files define the typed schema, YAML config files define behavior, both are reviewed in pull requests, and evaluation is deterministic for any given context. The kill switch (both flag-level and launch-level) provides the safety escape hatch.

## Evaluation context

The evaluation context defines the dimensions along which flags can vary (e.g., per-user, per-plan, per-tenant). You define your context as a proto message annotated with `option (pbflags.context) = {}`, where each field is a dimension annotated with `(pbflags.dimension)`. Dimensions declare their `distribution` (UNIFORM for hashable identifiers, CATEGORICAL for enum-like values) and `presence` (REQUIRED or OPTIONAL):

```protobuf
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
}
```

### Evaluation scopes

Scopes define which dimensions are available in a given execution context. Globally required dimensions are implicit in every scope. Features declare which scopes they're available in:

```protobuf
// Scope definitions — globally required dims (session_id) are implicit.
option (pbflags.scope) = { name: "anon" };
option (pbflags.scope) = { name: "user", dimensions: ["user_id"] };

message Notifications {
  option (pbflags.feature) = {
    id: "notifications"
    scopes: ["anon", "user"]
  };
  // ...flags...
}
```

The codegen generates **per-scope feature accessor types** with constructors that require their dimensions. Feature accessor methods only exist on scopes the feature lists:

```go
eval := pbflags.Connect(httpClient, url, &pb.EvaluationContext{})

// Scope-based access — dimensions are constructor parameters (compile-time safe).
userFeatures := dims.NewUserFeatures(eval, sessionID, userID)
emailEnabled := userFeatures.Notifications().EmailEnabled(ctx)

// Duck-typed interfaces let handlers declare what they need.
func handleNotification(features dims.HasNotifications) {
    freq := features.Notifications().DigestFrequency(ctx)
}
```

### How dimensions flow through the system

| Component | What it sees | Dimension-aware? |
|---|---|---|
| Proto definition | `(pbflags.dimension)` on context fields | Source of truth |
| Generated client | Typed constructors (`dims.UserID`, `dims.Plan`) | Yes — enforces correct value types |
| Wire protocol | `EvaluationContext` message fields | Structured — carries typed dimensions |
| Evaluator | Context fields | Evaluates CEL conditions against supplied dimensions |
| Database | `flags.conditions` JSONB, `flags.killed_at` | Stores condition chain and kill state |
| YAML config | `ctx.<field>` references in CEL expressions | Defines targeting behavior |
| Admin UI | Displays condition chain and sync SHA | Displays only |

Type safety is enforced in both the generated client code and the wire protocol via the structured `EvaluationContext` message.

### Changing a flag's dimension

A flag's dimension is part of its contract with consumers — changing it changes the generated client signature and can invalidate existing condition expressions that reference the old dimension.

| Transition | Allowed? | Why |
|---|---|---|
| Global → Dimension | **Yes** | No existing conditions reference dimension fields. Safe rollout — empty context falls back to compiled default. |
| Dimension → Global | **No** | Existing conditions reference the old dimension's context fields and would fail CEL evaluation or silently mismatch. |
| Dimension A → Dimension B | **No** | Existing conditions were written against Dimension A's context fields (e.g., `user_id`). After the change, they reference fields that no longer apply. |

The lint tool (`pbflags-lint`) enforces these rules at pre-commit or release time.

### Migrating a flag to a different dimension

When you need to change a flag's dimension, define a new flag instead of modifying the existing one:

1. **Add a new flag** in the same feature message with the desired dimension and a new field number.
2. **Regenerate code.** Both flags are available simultaneously.
3. **Define conditions** on the new flag with CEL expressions targeting the new dimension.
4. **Update application code** to read the new flag. Deploy.
5. **Archive the old flag.** Remove the field from the proto (or mark it `reserved`). Run `pbflags-sync` to archive it.

This avoids any window of incorrect evaluation — both flags coexist during the transition, each with conditions written for the correct dimension.

## Lint tool

`pbflags-lint` detects breaking changes in your proto definitions before they reach production. It compares the working tree against a base git ref and reports violations.

```bash
# Pre-commit: compare working tree vs HEAD
pbflags-lint proto/

# CI: compare against main branch
pbflags-lint --base origin/main proto/
```

Exit codes: `0` = clean, `1` = breaking changes found, `2` = tool error.

| Rule | Description |
|---|---|
| `type_changed` | A flag's type changed (e.g., bool to string) |
| `dimension_changed` | A flag's dimension changed in a forbidden direction |
| `scope_removed` | A scope was removed (deletes generated `*Features` type) |
| `scope_dimension_changed` | A scope's dimension set changed (changes constructor signature) |
| `feature_scope_removed` | A feature was removed from a scope (deletes generated accessor) |

Flag removal is normal lifecycle and is **not** flagged — the evaluator gracefully handles archived flags. Stateless checks (invalid dimension names, missing context message, etc.) are enforced by codegen at build time.

### Pre-commit integration

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

The tool skips quickly (exit 0) when no `.proto` files have changed, so it's safe to run on every commit.
