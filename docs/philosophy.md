# Philosophy and Design

## Proto as source of truth

Flag definitions live in `.proto` files. This means flag schemas are versioned in source control, reviewed in pull requests, and validated at compile time. The database stores runtime state (values, overrides, kills), but the shape of the flag system — which flags exist, their types, their evaluation context dimensions — is defined in proto.

This gives you a property most feature flag systems don't have: your flag definitions are as reviewable and auditable as your code.

## Key design principles

- **Never-throw guarantee**: All evaluation errors return the compiled default. Application code never needs to handle flag evaluation failures.
- **Type-safe code generation**: Generated interfaces with compile-time type checking. You can't pass a user ID where an entity ID is expected.
- **Graceful degradation**: Stale values served instantly on cache expiry (background refresh in flight); last-resort stale fallback during outages; compiled defaults as the ultimate safety net. Flag evaluation never blocks after initial warmup and keeps working even if the database is unreachable.
- **Fast kill switches**: ~30s polling for emergency shutoffs by default. Kill a flag globally and it takes effect within one poll cycle. In write-through mode (`--cache-flag-ttl=0`), kills are checked inline on every evaluation — no polling delay.
- **Immutable identity**: Flag identity is `feature_id/field_number`, safe to rename proto fields without breaking existing state.
- **Audit trail**: All state changes logged with actor and timestamp.

## Flag evaluation precedence

The evaluator resolves flags using this precedence chain:

1. **Global KILLED** -> compiled default (polled every ~30s, or checked inline in write-through mode)
2. **Per-entity override ENABLED** -> override value
3. **Per-entity override DEFAULT** -> compiled default
4. **Global ENABLED** -> configured value
5. **Stale fallback** -> last known value (if hot cache expired, served while background refresh runs)
6. **Compiled default** -> always safe

The key insight is that the global kill switch always wins, overrides beat global state, and the compiled default is the ultimate safety net. Per-entity kills are not supported — use the global kill switch instead.

## Evaluation context

The evaluation context defines the dimensions along which flags can vary (e.g., per-user, per-plan, per-tenant). You define your context as a proto message annotated with `option (pbflags.context) = true`, where each field is a dimension annotated with `(pbflags.dimension)`:

```protobuf
message EvaluationContext {
  option (pbflags.context) = true;

  string user_id = 1 [(pbflags.dimension) = { description: "Authenticated user" }];
  PlanLevel plan = 2 [(pbflags.dimension) = { description: "Subscription plan" }];
  bool is_internal = 3 [(pbflags.dimension) = { description: "Internal/dogfood user" }];
}
```

The codegen generates a **typed dimension constructor** for each field. These types enforce at compile time that callers supply the correct kind of value for each dimension:

```go
// Can't pass a Plan where a UserID is expected — compiler error.
eval := pbflags.Connect(httpClient, url, &pb.EvaluationContext{})
emailEnabled := nf.EmailEnabled(ctx, eval.With(dims.UserID("user-123")))
lookbackDays := ic.LookbackDays(ctx, eval.With(dims.UserID("user-123"), dims.Plan(pb.PlanLevel_PLAN_LEVEL_PRO)))

// No dimensions evaluates global state (no per-entity override applied).
globalDefault := nf.EmailEnabled(ctx, eval)
```

### How dimensions flow through the system

| Component | What it sees | Dimension-aware? |
|---|---|---|
| Proto definition | `(pbflags.dimension)` on context fields | Source of truth |
| Generated client | Typed constructors (`dims.UserID`, `dims.Plan`) | Yes — enforces correct value types |
| Wire protocol | `EvaluationContext` message fields | Structured — carries typed dimensions |
| Evaluator | Context fields | Resolves overrides against matching dimensions |
| Database | `flags.dimension` VARCHAR, `flag_overrides(flag_id, entity_id)` | Stores dimension name; overrides keyed by opaque entity ID |
| Admin UI | Displays dimension name, shows override section for non-global | Displays only |

Type safety is enforced in both the generated client code and the wire protocol via the structured `EvaluationContext` message.

### Changing a flag's dimension

A flag's dimension is part of its contract with consumers — changing it changes the generated client signature and can invalidate existing override data.

| Transition | Allowed? | Why |
|---|---|---|
| Global → Dimension | **Yes** | No existing overrides. Safe rollout — empty context falls back to global state. |
| Dimension → Global | **No** | Orphaned overrides remain in the database. Cannot be deleted until rollout is complete, but if not deleted, silently reappear if the flag is later given a dimension. |
| Dimension A → Dimension B | **No** | Existing override rows were written with Dimension A's ID semantics (e.g., user IDs). After the change, they're interpreted as Dimension B IDs (e.g., plan levels). If value spaces overlap, overrides evaluate incorrectly. |

The lint tool (`pbflags-lint`) enforces these rules at pre-commit or release time.

### Migrating a flag to a different dimension

When you need to change a flag's dimension, define a new flag instead of modifying the existing one:

1. **Add a new flag** in the same feature message with the desired dimension and a new field number.
2. **Regenerate code.** Both flags are available simultaneously.
3. **Set up overrides** on the new flag for the appropriate entities.
4. **Update application code** to read the new flag. Deploy.
5. **Archive the old flag.** Remove the field from the proto (or mark it `reserved`). Run `pbflags-sync` to archive it.

This avoids any window of incorrect evaluation — both flags coexist during the transition, each with correct override data for its dimension.

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
