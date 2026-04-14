# Upgrade Guide: User-Defined Layers

> **Historical note:** This guide only applies to pre-v0.15 deployments that
> still use layers. v0.15 replaced layers with `EvaluationContext` dimensions,
> and v0.16 removed the remaining layer storage and override model. New
> integrations should use `agent-setup.md` instead.

This guide covers migrating from the hardcoded `Layer` enum to user-defined
layers. This is a breaking change — all steps are required.

**Design doc:** `design-docs/2026-04-09-user-defined-layers.md`

---

## Prerequisites

- [ ] Update your pbflags dependency to the version containing this change
- [ ] Update `buf.build/spotlightgov/pbflags` dependency in `buf.yaml` (if using BSR)

## Step 1: Define your layer enum

Add an enum annotated with `option (pbflags.layers) = true` to your proto
files. This can go in any `.proto` file that's part of your `buf generate`
input — typically alongside your feature definitions or in a shared
`layers.proto`.

```protobuf
import "pbflags/options.proto";

enum Layer {
  option (pbflags.layers) = true;
  LAYER_UNSPECIFIED = 0;  // required: proto3 zero value
  LAYER_GLOBAL = 1;       // convention: explicit global
  LAYER_USER = 2;         // your per-user override dimension
  // Add your own layers here, e.g.:
  // LAYER_ENTITY = 3;
  // LAYER_TENANT = 4;
}
```

**Rules:**
- Exactly one enum across all input files must carry this annotation
- Ordinal 0 is required (proto3 convention)
- Values named `UNSPECIFIED` or `GLOBAL` (after prefix stripping) are treated
  as global (no per-entity overrides)
- All other values define per-entity override dimensions

## Step 2: Update flag annotations

Replace enum-based `layer:` values with string-based values.

**Before:**
```protobuf
bool email_enabled = 1 [(pbflags.flag) = {
  description: "Enable email notifications"
  default: { bool_value: { value: true } }
  layer: LAYER_USER
}];

string digest_frequency = 2 [(pbflags.flag) = {
  description: "Digest email frequency"
  default: { string_value: { value: "daily" } }
  layer: LAYER_GLOBAL
}];
```

**After:**
```protobuf
bool email_enabled = 1 [(pbflags.flag) = {
  description: "Enable email notifications"
  default: { bool_value: { value: true } }
  layer: "user"
}];

string digest_frequency = 2 [(pbflags.flag) = {
  description: "Digest email frequency"
  default: { string_value: { value: "daily" } }
}];
```

**Rules:**
- `layer: "user"` matches `LAYER_USER` (prefix-stripped, case-insensitive)
- Global flags: omit the `layer` field entirely (or use `layer: "global"`)
- The string value must match a value in your layer enum — codegen will fail
  the build if it doesn't

## Step 3: Regenerate code

```bash
buf generate
```

This now produces:
- A `layers/` package with typed ID wrappers (Go: `layers.UserID`,
  Java: `UserID.java`)
- Updated feature client signatures using typed IDs instead of bare strings

**If the build fails** with "no enum annotated with option (pbflags.layers)",
go back to Step 1 — you need the layer enum in your proto input files.

**If the build fails** with "layer ... does not match any value", check that
your `layer: "..."` string matches an enum value name (after prefix stripping).

## Step 4: Update Go call sites

**Before:**
```go
emailEnabled := notifications.EmailEnabled(ctx, userID)         // userID is string
```

**After:**
```go
import "yourpkg/gen/flags/layers"

emailEnabled := notifications.EmailEnabled(ctx, layers.User("user-123"))  // typed
```

The compiler will guide you — every call site passing a bare `string` for a
layer-scoped flag will fail to compile until updated.

**Global evaluation** (no entity context): pass the zero value:
```go
globalDefault := notifications.EmailEnabled(ctx, layers.UserID{})
```

## Step 5: Update Java call sites

**Before:**
```java
boolean enabled = flags.emailEnabled().get(userId);  // userId is String
```

**After:**
```java
import com.yourpkg.flags.layers.UserID;

boolean enabled = flags.emailEnabled().get(UserID.of("user-123"));  // typed
```

**Global evaluation:**
```java
boolean enabled = flags.emailEnabled().get();  // no argument = global
```

Note: `emailEnabled()` now returns `LayerFlag<Boolean, UserID>` instead of
`Flag<Boolean>`. The `LayerFlag` interface has both `get()` (global) and
`get(ID)` (scoped).

## Step 6: Re-sync descriptors and restart services

```bash
# Rebuild descriptors
buf build proto -o descriptors.pb

# Sync to database (no schema migration needed — flags.layer is already VARCHAR)
pbflags-sync \
  --database=postgres://... \
  --descriptors=descriptors.pb

# Restart the evaluator with new definitions from DB
pbflags-evaluator --database=postgres://...
```

The database stores layer values as uppercase strings (`"USER"`, `"ENTITY"`).
Existing rows with `"GLOBAL"` and `"USER"` remain valid — no migration needed.

## Step 7: Verify

- [ ] `buf generate` succeeds
- [ ] `go build ./...` compiles
- [ ] Tests pass
- [ ] Admin UI shows correct layer labels for flags
- [ ] Override management works for layer-scoped flags
- [ ] Existing overrides still evaluate correctly

## Quick reference: what changed

| Before | After |
|---|---|
| `layer: LAYER_USER` | `layer: "user"` |
| `layer: LAYER_GLOBAL` | omit `layer` field |
| `func Flag(ctx, entityID string)` | `func Flag(ctx, user layers.UserID)` |
| `Flag<T>` (Java, all flags) | `Flag<T>` (global) / `LayerFlag<T, ID>` (scoped) |
| Layer enum in `options.proto` | Customer-defined enum with `(pbflags.layers) = true` |

## Troubleshooting

**Build error: "no enum annotated with option (pbflags.layers)"**
You need to add the layer enum (Step 1) to a proto file included in your
`buf generate` inputs.

**Build error: "layer ... does not match any value in the ... enum"**
The `layer: "..."` string doesn't match any value in your layer enum. Check
spelling (case-insensitive, prefix-stripped). For example, `LAYER_ENTITY` is
matched by `layer: "entity"`.

**Build error: "multiple enums annotated with (pbflags.layers)"**
Only one enum across all input files can carry the annotation. Remove the
duplicate.

**Existing overrides not working after upgrade?**
The database `flags.layer` column already stores strings. If the layer name
changed (e.g., from `"USER"` to `"ENTITY"`), update the column value:
```sql
UPDATE feature_flags.flags SET layer = 'ENTITY' WHERE layer = 'USER';
```
