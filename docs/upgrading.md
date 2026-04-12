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

- [User-defined layers](upgrade-guide-user-defined-layers.md) — migrating from hardcoded to user-defined layer enums (v0.6.0)

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
