# User-Defined Layers

**Issue:** pb-7br
**Status:** Approved
**Author:** bmt
**Date:** 2026-04-09

## Problem

The `Layer` enum in `options.proto` is hardcoded:

```protobuf
enum Layer {
  LAYER_UNSPECIFIED = 0;
  LAYER_GLOBAL = 1;
  LAYER_USER = 2;
}
```

Different products need different override dimensions. Spotlight needs
per-entity (government body) overrides for flags like `discovery/lookback_days`.
Adding `LAYER_ENTITY = 3` to the pbflags proto solves the immediate need but
repeats the problem for every new product or dimension. Layers should be
configurable without modifying the pbflags proto.

At the same time, layers must remain a **global concept** — two flags in the
same layer must mean the same thing. The set of layers for a given product is
expected to be fairly fixed, not ad hoc. We need compile-time safety so that
callers cannot pass the wrong kind of ID to a flag evaluation.

## Goals

1. Products define their own layers without modifying `options.proto`.
2. Layers remain a typed, global concept — shared across all features.
3. Generated client code enforces correct layer ID types at compile time.
4. Invalid layer references are caught at build time (codegen validation).
5. Layer definitions are lintable for breaking changes (future flag-lint port).

## Non-Goals

- Multi-layer flags (a flag varying along multiple dimensions simultaneously).
  If a product needs per-tenant AND per-user variation, those are separate flags.
- Hierarchical layer resolution (e.g., tenant overrides user overrides global).
  The evaluator distinguishes "global" vs. "has overrides" — nothing more.
- Renaming the `entityID` parameter per layer in the wire protocol. The
  `EvaluateRequest.entity_id` field stays a string; type safety is enforced in
  generated client code, not on the wire.

## Design

### Customer-defined layer enum

Customers define a proto enum in their own codebase, annotated with a new
pbflags option:

```protobuf
import "pbflags/options.proto";

enum Layer {
  option (pbflags.layers) = true;
  LAYER_UNSPECIFIED = 0;
  LAYER_GLOBAL = 1;
  LAYER_USER = 2;
  LAYER_ENTITY = 3;
}

message Discovery {
  option (pbflags.feature) = { id: "discovery" };
  int64 lookback_days = 1 [(pbflags.flag) = {
    layer: "entity"
    default: { int64_value: { value: 30 } }
  }];
  string region = 2 [(pbflags.flag) = {
    default: { string_value: { value: "us-east" } }
    // layer omitted → global
  }];
}
```

The `layer` field on `FlagOptions` becomes a `string` (field number 5,
replacing the deprecated enum at field 3). The string value is matched against
the annotated enum using a prefix-stripping, case-insensitive convention:
`"entity"` matches `LAYER_ENTITY`.

### Generated typed layer IDs

The codegen generates a shared `layers` package from the annotated enum.
Each non-global enum value produces a distinct Go type:

```go
// {out}/layers/layers.go
package layers

// EntityID identifies an entity for layer-scoped flag evaluation.
// The zero value evaluates global state.
type EntityID struct{ v string }
func Entity(id string) EntityID { return EntityID{v: id} }
func (id EntityID) String() string { return id.v }

type UserID struct{ v string }
func User(id string) UserID { return UserID{v: id} }
func (id UserID) String() string { return id.v }
```

Feature codegen imports and uses these types:

```go
package discoveryflags
import ".../layers"

type DiscoveryFlags interface {
    LookbackDays(ctx context.Context, entity layers.EntityID) int64
    Region(ctx context.Context) string  // global — no layer param
}
```

Callers use typed constructors. The zero value evaluates global state:

```go
client.LookbackDays(ctx, layers.Entity("govt-body-123"))  // scoped
client.LookbackDays(ctx, layers.EntityID{})                // global fallback
```

For Java, a new `LayerFlag<T, ID>` interface extends the existing `Flag<T>`
pattern:

```java
public interface LayerFlag<T, ID> {
    T get();         // global evaluation
    T get(ID id);    // scoped evaluation
}
```

### Codegen validation (build-time lint)

The `protoc-gen-pbflags` plugin validates at generation time. If any check
fails, protoc/buf surfaces it as a build error — you cannot forget to run
a separate lint step.

**Checks baked into codegen (no history needed):**

- No `(pbflags.layers)` enum found in input files
- `layer` string on a flag doesn't match any value in the layers enum
- Layers enum missing ordinal 0 (proto3 requires a zero value)
- Multiple enums annotated with `(pbflags.layers)`
- Empty feature ID
- Unsupported flag field type

**Checks left for flag-lint (need change history):**

- Layer enum value removed (breaking — removes a generated type)
- Flag's layer changed (breaking — changes generated function signature)
- Field number reused within a feature message

### Layer name matching convention

Given `layer: "entity"` and enum value `LAYER_ENTITY`:

1. Compute the common prefix of all enum value names (e.g., `LAYER_`).
2. Strip the prefix from each value name (e.g., `ENTITY`).
3. Match the `layer` string case-insensitively against the stripped names.
4. Special cases: `""` (empty/unset) and `"global"` mean global (no overrides).

Type name derivation: stripped name `ENTITY` becomes Go type `EntityID` and
constructor `Entity()`.

### Single layer per flag

A flag has exactly one layer. The `entity_id` field in the `flag_overrides`
database table represents the identifier for that layer's dimension. If a flag
has `layer: "tenant"`, then `entity_id` holds the tenant ID. If `layer: "user"`,
it holds the user ID.

This keeps the data model simple: no multi-dimensional precedence, no new
database columns, no new override table structures. The evaluator only
distinguishes "global vs. non-global" when deciding whether to look up
overrides.

## Alternatives Considered

### A. Free-form string layers (no enum)

`FlagOptions` gets a `string layer` field. Products write any string
(`"entity"`, `"tenant"`, etc.) without declaring valid values anywhere.

**Rejected because:** No single source of truth for what layers exist. Typos
silently create new "layers." Codegen can't generate typed wrappers without
knowing the full set. Can't lint for breaking changes without a schema.

### B. Extend the pbflags Layer enum

Add `LAYER_ENTITY = 3`, `LAYER_TENANT = 4`, etc. directly to `options.proto`.

**Rejected because:** Every product needing a new dimension must modify the
pbflags proto, cut a release, and update BSR. This is the current design's
problem restated.

### C. Keep the enum in options.proto but treat it as a shared vocabulary

Accept that layers are rare and fairly fixed. Products choose from a curated
set. New layers require a pbflags release but that's infrequent.

**Rejected because:** Still couples layer definitions to the pbflags release
cycle. Even if layers are rare, the coupling is architecturally wrong — layer
semantics are a product concern, not a framework concern. Also prevents
products from using layer names that are meaningful to their domain.

### D. FlagOptions.layer stays as enum, customer writes ordinals

Keep the field typed as `pbflags.Layer` (enum). Customer defines their own
enum for documentation and codegen, but writes raw ordinals in flag annotations
(e.g., `layer: 3`) because proto only resolves names against the declared
field type.

**Rejected because:** `layer: 3` is not self-documenting. The readability cost
outweighs the benefit of keeping the original field type.

### E. Multi-layer flags

Allow a flag to vary along multiple dimensions simultaneously (e.g., both
per-tenant AND per-user). Override resolution uses a precedence chain across
layers.

**Rejected because:** Dramatically increases complexity — the override table
needs additional columns, the evaluator needs multi-dimensional lookup and
precedence rules, the admin UI needs per-layer override sections. The practical
need hasn't been demonstrated. If a product needs this, they can use separate
flags for each dimension.

## Breaking Changes

This is a breaking change to the `options.proto` API and generated code.

- The `Layer` enum is removed from `options.proto`.
- `FlagOptions.layer` changes from enum (field 3) to string (field 5).
- `FlagDetail.layer` in `admin.proto` changes from `pbflags.Layer` to `string`.
- Generated Go client signatures change from `func(ctx, entityID string)` to
  `func(ctx, entity layers.EntityID)` (typed wrappers).
- Generated Java code changes from `Flag<T>` to `LayerFlag<T, ID>` for
  layer-scoped flags.
- All consumers must define a `(pbflags.layers)` enum in their proto.

We currently have one consumer (Spotlight), and this change is coordinated.

## Impact on Existing Components

| Component | Impact |
|---|---|
| `options.proto` | Remove `Layer` enum, add `string layer` field, add `layers` enum option extension |
| `admin.proto` | `FlagDetail.layer` becomes `string` |
| Descriptor parsing (`evaluator/descriptor.go`) | `FlagDef.Layer` becomes `string`, parse field 5 instead of 3 |
| Schema sync (`pbflags-sync`) | Use string layer directly, no ordinal mapping |
| Evaluator | No changes — already only checks global vs. non-global |
| Admin store | Replace `parseLayer()` enum mapping with `isGlobalLayer()` string check |
| Admin UI | Replace `isUserLayer` with `hasOverrides`, display arbitrary layer names |
| Go codegen | Layer enum discovery, layers package generation, typed ID signatures |
| Java codegen | Same as Go, plus `LayerFlag<T, ID>` interface |
| Database schema | No changes — `flags.layer` is already `VARCHAR(50)` |
