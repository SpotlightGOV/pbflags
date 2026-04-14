# Upgrading

## Standalone

If your proto definitions changed, rebuild the descriptor set first:

```bash
buf build proto -o descriptors.pb
```

Then replace the binary (or image) and restart. Standalone mode runs migrations and syncs definitions on startup, so no separate `pbflags-sync` step is needed.

## Production (multi-instance)

Upgrade `pbflags-sync` first. It runs migrations before syncing, so the database schema is updated before any other component sees it:

1. **Regenerate code and rebuild `descriptors.pb`** from the updated proto definitions.
2. **Deploy the new `pbflags-sync`** in your CI/CD pipeline. This applies any pending migrations and syncs definitions.
3. **Roll out `pbflags-admin`** instances. They check the schema version on startup and will work with the updated schema.
4. **Roll out `pbflags-evaluator`** instances. They only read from the database, so they are safe to update last.

This order matters because `pbflags-admin` and `pbflags-evaluator` do not run migrations — they verify the schema is at the expected version and fail fast if it is not. Always let `pbflags-sync` go first.

## Version-specific upgrade guides

- [Launches and evaluation scopes (v0.17.0)](#launches-and-evaluation-scopes-v0170) — gradual rollouts, per-condition overrides, scope-based codegen
- [Conditions and config-driven flags (v0.16.0)](#conditions-and-config-driven-flags-v0160) — migrating from overrides to YAML conditions
- [Evaluation context dimensions (v0.15.0)](#evaluation-context-dimensions-v0150) — replacing layers with context dimensions
- [User-defined layers](archive/upgrade-guide-user-defined-layers.md) — historical guide for pre-v0.15 layer migrations (archived)

---

## Launches and evaluation scopes (v0.17.0)

This release adds gradual rollouts (launches) with per-condition value overrides,
evaluation scopes with typed codegen, and dimension classification enums. The
launch feature is pre-release — no production launches exist against the old
schema, so the migration is a clean break.

### What changed

**Proto definitions:**

- `DimensionOptions.hashable` (bool, field 2) is replaced by `DimensionDistribution`
  (enum, field 2). Wire-compatible: old `hashable: true` decodes as
  `DIMENSION_DISTRIBUTION_UNIFORM` (enum value 1).
- `DimensionOptions.bounded` (bool, field 3) is removed (reserved). Enum and bool
  dimensions are inherently categorical. Use `distribution: CATEGORICAL` for
  string/int64 dimensions with bounded cardinality.
- `DimensionOptions.presence` (enum, field 4) is new. `REQUIRED` means always
  present; `OPTIONAL` means may be absent.
- `FeatureOptions.scopes` (repeated string, field 4) is new. Lists which
  evaluation scopes a feature is available in.
- `ScopeOptions` message and `(pbflags.scope)` file-level option are new.
  Scopes define named execution contexts with dimension sets.
- `CompiledLaunch` is simplified: `flag_id`, `population_cel`, `value_json` removed.
  New fields: `scope_feature_id`, `affected_features`, `description`.
- `CompiledFeature.launches` (field 6) is reserved. Launches are now at
  `CompiledBundle.launches` (field 3).

**Database (migration 008):**

- `launches` table reshaped: `flag_id`, `population_cel`, `value` columns dropped.
  `feature_id` renamed to `scope_feature_id` (nullable for cross-feature launches).
  New columns: `affected_features` (TEXT[]), `description` (TEXT), `killed_at`
  (TIMESTAMPTZ, nullable). Status values: BAKED → SOAKING.
- **Precondition**: no ACTIVE or BAKED launches may exist. The migration checks
  and raises an exception if any are found.
- Launch-to-flag binding is now inline in the `flags.conditions` JSONB via
  `launch_id` and `launch_value` fields on `StoredCondition`.

**YAML config format:**

The `launches:` section changes from per-flag binding to dimension-only:

```yaml
# Before (v0.16)
launches:
  my_rollout:
    flag: email_enabled
    dimension: user_id
    population: "ctx.plan == PlanLevel.PRO"
    value: true
    ramp_percentage: 25

# After (v0.17)
launches:
  my_rollout:
    dimension: user_id
    ramp_percentage: 25
    description: "Pro email rollout"

flags:
  email_enabled:
    conditions:
      - when: "ctx.plan == PlanLevel.PRO"
        value: false
        launch:
          id: my_rollout
          value: true
      - otherwise: false
```

The `flag`, `population`, and `value` fields are removed from launch definitions.
Population targeting moves to the condition's `when` clause. The launch override
value moves inline to the condition via `launch: {id, value}`.

**Generated code:**

- New per-scope `*Features` types in the `dims` package (e.g., `AnonFeatures`,
  `UserFeatures`). Constructors require scope dimensions as typed parameters.
- Duck-typed `Has<Feature>` interfaces per feature for handler decoupling.
- Context storage helpers: `ContextWith<Scope>Features` / `<Scope>FeaturesFrom`.
- Existing per-feature packages (`notificationsflags`, etc.) are unchanged.

**Lint rules (new):**

- `scope_removed` — removing a scope deletes its generated `*Features` type.
- `scope_dimension_changed` — changing a scope's dimension set changes the
  constructor signature.
- `feature_scope_removed` — removing a feature from a scope deletes the accessor.

### Migration checklist

1. **Update proto dimensions**: replace `hashable: true` with
   `distribution: DIMENSION_DISTRIBUTION_UNIFORM`. Add `presence:` annotations
   (`REQUIRED` or `OPTIONAL`).
2. **Define scopes**: add `option (pbflags.scope) = { name: "...", dimensions: [...] }`
   at file level. Add `scopes: [...]` to each feature's `(pbflags.feature)` option.
3. **Regenerate code**: `buf generate` (or `make generate`).
4. **Update YAML configs**: move launch-to-flag binding from the `launches:` section
   to inline `launch:` keys on individual conditions. Remove `flag`, `population`,
   and `value` from launch definitions.
5. **Deploy `pbflags-sync`**: migration 008 runs automatically. Ensure no ACTIVE
   or BAKED launches exist first.
6. **Update application code** (optional): adopt scope-based access via the new
   `dims.New<Scope>Features(eval, ...)` constructors. Existing per-feature
   `notificationsflags.New(eval)` calls continue to work.
7. **Verify** the admin UI shows launch overrides in the conditions table and
   launch kill/unkill controls on the flag detail page.

---

## Conditions and config-driven flags (v0.16.0)

This release replaces the per-entity override system with YAML-based condition
chains using CEL expressions. The admin UI is now read-only — flag behavior is
managed entirely through config files in git.

### What changed

- The `flag_overrides` table is **dropped**. Per-entity overrides no longer exist.
- The `flags.state` column (ENABLED/DEFAULT/KILLED) is replaced by `killed_at TIMESTAMP NULL`.
  NULL means live, non-NULL means killed. The kill switch is the only runtime control.
- The `flags.value` and `flags.layer` columns are **dropped**.
  Flag values are now determined by the `conditions` JSONB column (set by the sync pipeline).
- The admin UI is **read-only**. The kill switch still works; value editing and override
  management are removed.
- The `layerutil` package and `(pbflags.layers)` codegen extension are removed.

### Before you upgrade

**Export your current database values.** If you have per-entity overrides or
admin-UI-set values, export them before running the migration:

```bash
pbflags-sync export \
  --database=$PBFLAGS_DATABASE \
  --entity-dimension=user_id \
  --output=./features
```

This generates one YAML file per feature with your current values and overrides
converted to condition chains. Review and commit these files — they become your
source of truth.

If you have no overrides or admin-set values (all flags use compiled defaults),
you can skip the export and write config files from scratch.

### YAML config format

Each feature gets a YAML file. Flags can have a static value or a condition chain:

```yaml
feature: notifications
flags:
  email_enabled:
    conditions:
      - when: "ctx.is_internal"
        value: true
      - when: "ctx.plan == PlanLevel.ENTERPRISE"
        value: true
      - otherwise: false

  score_threshold:
    value: 0.75
```

CEL expressions reference fields from your `EvaluationContext` proto message via the
`ctx.` prefix. Enum values use prefix-stripped aliases (e.g., `PlanLevel.ENTERPRISE`).

### Sync pipeline

Pass `--features` to `pbflags-sync` to compile YAML configs into the database:

```bash
pbflags-sync \
  --database=$PBFLAGS_DATABASE \
  --descriptors=descriptors.pb \
  --features=./features \
  --sha=$(git rev-parse HEAD)
```

The `--sha` flag records the git commit in the database; the admin UI displays it
as a badge on flag detail pages. In standalone mode, `pbflags-admin` also accepts
`--features` to sync conditions on startup.

### Validate before deploying

```bash
pbflags-sync validate --descriptors=descriptors.pb --features=./features
```

This checks YAML syntax, CEL expression compilation, and type compatibility without
touching the database. Run it in CI before merge.

### Database migration

Migration 006 runs automatically via `pbflags-sync` or `pbflags-admin --standalone`.
It:

1. Adds `killed_at TIMESTAMP` to flags
2. Migrates `state='KILLED'` rows to `killed_at = updated_at`
3. Drops the `state`, `value`, and `layer` columns
4. Drops the `flag_overrides` table

**This migration is irreversible in practice** — exported data can be restored from
the YAML config files, but the override table data is gone. Export first.

### Generated code (Go)

Layer parameters were removed from generated method signatures in v0.15.0. If you
skipped that migration, see the [v0.15.0 guide](#evaluation-context-dimensions-v0150).

No additional generated Go code changes in v0.16.0.

### Generated code (Java)

The Java client replaces the entity/layer API with typed context dimensions:

- `evaluate()` and `evaluateList()` no longer accept an `entityId` parameter.
- `LayerFlag` and `LayerListFlag` are removed.
- `Flag<T>` and `ListFlag<E>` now have only `get()` (no `get(entityId)` overload).
- `FlagEvaluatorClient` accepts an optional `EvaluationContext` prototype for
  dimension support: `new FlagEvaluatorClient(target, EvaluationContext.getDefaultInstance())`.
- `protoc-gen-pbflags` now generates a `Dimensions.java` class with typed
  constructors (e.g., `Dimensions.userId("...")`, `Dimensions.plan(PlanLevel.PRO)`).
- Bind dimensions at the evaluator level: `evaluator.with(Dimensions.userId("..."))`.
  The returned evaluator carries the context for all subsequent flag evaluations.

### Migration checklist

1. **Export** existing database values and overrides with `pbflags-sync export`.
2. **Write YAML configs** for each feature (or use the exported files as a starting point).
3. **Validate** configs with `pbflags-sync validate`.
4. **Deploy `pbflags-sync`** with `--features` pointing to your config directory.
   Migration 006 runs automatically and is backwards-compatible with the previous schema
   until conditions are synced.
5. **Deploy `pbflags-admin`** with `--features` if using standalone mode.
6. **Verify** the admin UI shows condition chains on flag detail pages and the sync SHA badge.
7. **Remove** any code that calls override APIs (`SetFlagOverride`, `RemoveFlagOverride`).
   These now return `Unimplemented`.
8. **Update Java clients** (if applicable): regenerate codegen, replace `entityId` params
   with `evaluator.with(Dimensions.userId(...))`, remove `LayerFlag`/`LayerListFlag` usage.

---

## Evaluation context dimensions (v0.15.0)

This release replaces the layer system with evaluation context dimensions.
The change touches proto annotations, generated code, and the wire protocol.

### Proto annotations

The `Layer` enum and the `(pbflags.layers)` file-level option have been
removed. Replace them with the `EvaluationContext` message using the new
`(pbflags.context)` and `(pbflags.dimension)` annotations.

The `FlagOptions.layer` field annotation has also been removed from flag
definitions.

### Generated code

The generated `layers` package is replaced by a `dims` package that provides
typed dimension constructors.

Two new packages are introduced:

- **`pbflags`** -- core types including `Evaluator`, `Connect`,
  `ContextWith`, and `FromContext`.
- **`dims`** -- generated dimension constructors derived from your
  `EvaluationContext` message fields.

### Client constructor

Feature client constructors have changed signature:

```go
// Before
client := notificationsflags.NewNotificationsFlagsClient(serviceClient)

// After
client := notificationsflags.New(eval)
```

The old `New<Feature>FlagsClient` name is available as a deprecated alias
during the transition.

### Method signatures

All layer parameters have been removed from generated method signatures.
Evaluation context is now carried implicitly rather than passed per-call.

The `Status()` method has been removed from generated interfaces. If you
need evaluator health checks, use the Connect client directly:

```go
healthClient := pbflagsv1connect.NewFlagEvaluatorServiceClient(httpClient, url)
resp, err := healthClient.Health(ctx, connect.NewRequest(&pbflagsv1.HealthRequest{}))
```

### Wire protocol

The `entity_id` field is now reserved on `EvaluateRequest` and
`BulkEvaluateRequest`. It has been replaced by a `context` field that
carries a `google.protobuf.Any` wrapping your `EvaluationContext` message.

### Migration checklist

1. Define an `EvaluationContext` message with `(pbflags.context)` and
   annotate fields with `(pbflags.dimension)`.
2. Remove the `Layer` enum, `(pbflags.layers)`, and any `layer` field
   annotations from your proto files.
3. Regenerate code (`buf generate` or `protoc`).
4. Replace `layers` package imports with `dims`.
5. Update client construction from `NewXxxFlagsClient(serviceClient)` to
   `New(eval pbflags.Evaluator)`.
6. Remove layer arguments from all flag evaluation call sites.
7. Remove any calls to `Status()` on generated interfaces.
8. Update any code that sets `entity_id` on evaluate requests to use the
   new `context` field instead.
