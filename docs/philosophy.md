# Philosophy and Design

## Proto as source of truth

Flag definitions live in `.proto` files. This means flag schemas are versioned in source control, reviewed in pull requests, and validated at compile time. The database stores runtime state (values, overrides, kills), but the shape of the flag system — which flags exist, their types, their layers — is defined in proto.

This gives you a property most feature flag systems don't have: your flag definitions are as reviewable and auditable as your code.

## Key design principles

- **Never-throw guarantee**: All evaluation errors return the compiled default. Application code never needs to handle flag evaluation failures.
- **Type-safe code generation**: Generated interfaces with compile-time type checking. You can't pass a user ID where an entity ID is expected.
- **Graceful degradation**: Stale cache served during outages, compiled defaults as last resort. Flag evaluation keeps working even if the database is unreachable.
- **Fast kill switches**: ~30s polling for emergency shutoffs. Kill a flag globally and it takes effect within one poll cycle.
- **Immutable identity**: Flag identity is `feature_id/field_number`, safe to rename proto fields without breaking existing state.
- **Audit trail**: All state changes logged with actor and timestamp.

## Flag evaluation precedence

The evaluator resolves flags using this precedence chain:

1. **Global KILLED** -> compiled default (polled every ~30s)
2. **Per-entity override ENABLED** -> override value
3. **Per-entity override DEFAULT** -> compiled default
4. **Global DEFAULT** -> compiled default
5. **Global ENABLED** -> configured value
6. **Fallback** -> compiled default (always safe)

The key insight is that kills always win, overrides beat global state, and the compiled default is the ultimate safety net.

## Layers

Layers define the override dimensions for your flag system. Each non-global layer represents a dimension along which flags can vary (e.g., per-user, per-entity, per-tenant). You define your layers as a proto enum annotated with `option (pbflags.layers) = true`:

```protobuf
enum Layer {
  option (pbflags.layers) = true;
  LAYER_UNSPECIFIED = 0;
  LAYER_GLOBAL = 1;
  LAYER_USER = 2;
  LAYER_ENTITY = 3;
}
```

The codegen generates a **typed ID wrapper** for each non-global layer. These types enforce at compile time that callers pass the correct kind of identifier for each flag:

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

The wire protocol and evaluator are intentionally layer-agnostic. Type safety is enforced in the generated client code, not on the wire.

### Changing a flag's layer

A flag's layer is part of its contract with consumers — changing it changes the generated client signature and can invalidate existing override data.

| Transition | Allowed? | Why |
|---|---|---|
| Global → Layer | **Yes** | No existing overrides. Safe rollout — empty `entity_id` falls back to global state. |
| Layer → Global | **No** | Orphaned overrides remain in the database. Cannot be deleted until rollout is complete, but if not deleted, silently reappear if the flag is later re-layered. |
| Layer A → Layer B | **No** | Existing override rows were written with Layer A's ID semantics (e.g., user IDs). After the change, they're interpreted as Layer B IDs (e.g., entity IDs). If ID spaces overlap, overrides evaluate incorrectly. |

The lint tool (`pbflags-lint`) enforces these rules at pre-commit time.

### Migrating a flag to a different layer

When you need to change a flag's layer, define a new flag instead of modifying the existing one:

1. **Add a new flag** in the same feature message with the desired layer and a new field number.
2. **Regenerate code.** Both flags are available simultaneously.
3. **Set up overrides** on the new flag for the appropriate entities.
4. **Update application code** to read the new flag. Deploy.
5. **Archive the old flag.** Remove the field from the proto (or mark it `reserved`). Run `pbflags-sync` to archive it.

This avoids any window of incorrect evaluation — both flags coexist during the transition, each with correct override data for its layer.

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
| `layer_changed` | A flag's layer changed in a forbidden direction |

Flag removal is normal lifecycle and is **not** flagged — the evaluator gracefully handles archived flags. Stateless checks (invalid layer names, missing layers enum, etc.) are enforced by codegen at build time.

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
