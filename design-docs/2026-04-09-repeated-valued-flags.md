# Repeated-Valued (List) Flags

**Status:** Complete
**Date:** 2026-04-09
**Last revised:** 2026-04-09
**Issue:** pb-i32
**Author:** bmt

## Problem

pbflags supports four scalar flag types: `bool`, `string`, `int64`, and `double`.
Some use cases need list-valued flags. Spotlight wants to migrate
`incident_notification_emails` (a list of email addresses) into pbflags, but
encoding it as a comma-separated string is fragile — values containing commas
or whitespace break silently, there's no type safety, and the admin UI shows
it as an opaque string with no way to add or remove individual entries.

Proto already has first-class support for repeated fields. pbflags should
leverage this to support list-valued flags end-to-end: from proto definition
through descriptor parsing, database storage, evaluation, admin API/UI, to
generated client code returning typed slices/lists.

## Goals

1. `repeated` proto fields annotated with `(pbflags.flag)` produce list-valued flags.
2. End-to-end type safety: list element types are enforced at the proto, codegen,
   and evaluation layers.
3. List flags participate in the same evaluation precedence chain as scalar flags
   (kill set, per-entity overrides, global state, stale cache, compiled default).
4. Admin UI supports viewing, editing, and overriding list values.
5. No database schema migration required — the existing BYTEA storage handles
   serialized list values transparently.

## Non-Goals

- **Set semantics.** Lists maintain insertion order and allow duplicates. Uniqueness
  constraints are an application concern, not a framework concern.
- **Partial list operations.** Overrides replace the entire list — no "add item" or
  "remove item" operations. This keeps override semantics consistent with scalars
  (an override is a complete replacement of the value).
- **Nested/complex types.** Repeated message fields, maps, or repeated-of-repeated
  are out of scope. Only repeated scalars (bool, string, int64, double) are
  supported.
- **Max-length enforcement in proto.** A configurable max list size may be enforced
  at the admin service layer, but is not part of the proto schema.
- **Repeated bool flags.** While technically supported by the proto schema and
  plumbing, `repeated bool` has no practical use case. Codegen will accept it
  for completeness but we won't optimize the admin UI for it.

## Design

### Proto changes

#### `types.proto`: FlagValue and FlagType

Add four list wrapper messages and corresponding oneof variants to `FlagValue`.
Add four list type enum values to `FlagType`:

```protobuf
// List wrapper messages. Each wraps a repeated scalar to allow inclusion
// in the FlagValue oneof (proto3 does not allow repeated fields in oneofs).
message BoolList {
  repeated bool values = 1;
}
message StringList {
  repeated string values = 1;
}
message Int64List {
  repeated int64 values = 1;
}
message DoubleList {
  repeated double values = 1;
}

message FlagValue {
  oneof value {
    bool bool_value = 1;
    string string_value = 2;
    int64 int64_value = 3;
    double double_value = 4;
    // List variants — field numbers 5-8 mirror scalar numbers 1-4.
    BoolList bool_list_value = 5;
    StringList string_list_value = 6;
    Int64List int64_list_value = 7;
    DoubleList double_list_value = 8;
  }
}

enum FlagType {
  FLAG_TYPE_UNSPECIFIED = 0;
  FLAG_TYPE_BOOL = 1;
  FLAG_TYPE_STRING = 2;
  FLAG_TYPE_INT64 = 3;
  FLAG_TYPE_DOUBLE = 4;
  FLAG_TYPE_BOOL_LIST = 5;
  FLAG_TYPE_STRING_LIST = 6;
  FLAG_TYPE_INT64_LIST = 7;
  FLAG_TYPE_DOUBLE_LIST = 8;
}
```

The parallel numbering (scalar N maps to list N+4) is intentional — it makes
the relationship explicit and simplifies element-type derivation.

#### `options.proto`: FlagDefault

Add list fields to the `FlagDefault` oneof. Unlike scalar defaults, list
defaults do not need wrapper types — the presence of a message-typed oneof
field is distinguishable from "not set" in proto3 (the oneof tracks which
field is populated):

```protobuf
message FlagDefault {
  oneof value {
    google.protobuf.BoolValue bool_value = 1;
    google.protobuf.StringValue string_value = 2;
    google.protobuf.Int64Value int64_value = 3;
    google.protobuf.DoubleValue double_value = 4;
    // List defaults — no wrapper needed (message in oneof tracks presence).
    BoolList bool_list_value = 5;
    StringList string_list_value = 6;
    Int64List int64_list_value = 7;
    DoubleList double_list_value = 8;
  }
}
```

This requires importing `types.proto` from `options.proto` (or defining the
list messages in `options.proto` and re-exporting). Since `BoolList` etc. are
used in both `FlagValue` and `FlagDefault`, defining them in `types.proto` and
importing is cleaner.

#### `SupportedValues` — no changes

The existing `SupportedValues` message constrains individual items, not entire
lists. For a `repeated string` flag with `supported_values: { string_values:
["hourly", "daily", "weekly"] }`, each item in the list must be one of those
values. This interpretation requires no proto changes.

### Example usage

```protobuf
import "pbflags/options.proto";
import "pbflags/v1/types.proto";

message IncidentConfig {
  option (pbflags.feature) = {
    id: "incident_config"
    owner: "ops-team"
  };

  repeated string notification_emails = 1 [(pbflags.flag) = {
    description: "Email addresses for incident notifications"
    default: { string_list_value: { values: ["ops@spotlight.gov"] } }
  }];

  int64 severity_threshold = 2 [(pbflags.flag) = {
    default: { int64_value: { value: 3 } }
  }];
}
```

### Database impact

**No schema migration required.** The `flags.value`, `flags.default_value`,
`flag_overrides.value`, and `flag_audit_log.{old,new}_value` columns are all
`BYTEA` containing proto-marshaled `FlagValue`. Extending the `FlagValue`
oneof with list variants is a compatible proto change — the serialized bytes
are opaque to the database.

The `flags.flag_type VARCHAR(20)` column will store new string values:
`BOOL_LIST`, `STRING_LIST`, `INT64_LIST`, `DOUBLE_LIST`. The column is wide
enough (20 chars); no ALTER TABLE needed.

The `flags.state` CHECK constraint (`ENABLED`, `DEFAULT`, `KILLED`) applies
unchanged to list flags — kill/enable/default semantics are orthogonal to
value cardinality.

### Descriptor parsing (`evaluator/descriptor.go`)

**`kindToFlagType`** currently takes `protoreflect.Kind`. Change it to accept
`protoreflect.FieldDescriptor` so it can check `fd.IsList()`:

```go
func fieldToFlagType(fd protoreflect.FieldDescriptor) pbflagsv1.FlagType {
    if fd.IsList() {
        switch fd.Kind() {
        case protoreflect.BoolKind:
            return pbflagsv1.FlagType_FLAG_TYPE_BOOL_LIST
        case protoreflect.StringKind:
            return pbflagsv1.FlagType_FLAG_TYPE_STRING_LIST
        case protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
            return pbflagsv1.FlagType_FLAG_TYPE_INT64_LIST
        case protoreflect.DoubleKind:
            return pbflagsv1.FlagType_FLAG_TYPE_DOUBLE_LIST
        default:
            return pbflagsv1.FlagType_FLAG_TYPE_UNSPECIFIED
        }
    }
    // existing scalar logic unchanged
    switch fd.Kind() { ... }
}
```

**`parseFlagDefault`** gains new cases for field numbers 5-8 (list defaults).
Unlike scalar defaults that unwrap `google.protobuf.*Value`, list defaults
read a repeated field directly:

```go
case 5: // BoolList
    fv = parseBoolListDefault(v.Message())
case 6: // StringList
    fv = parseStringListDefault(v.Message())
case 7: // Int64List
    fv = parseInt64ListDefault(v.Message())
case 8: // DoubleList
    fv = parseDoubleListDefault(v.Message())
```

Each parser reads field 1 (`repeated` values) from the list message and
constructs the corresponding `FlagValue` variant.

### Sync (`pbflags-sync`)

**`flagTypeString`** gains four new cases mapping the new `FlagType` enum
values to their DB strings. No other sync logic changes — the upsert query
already handles `flag_type` and `default_value` generically.

### Evaluation

**No changes to the evaluator.** The `Evaluate()` method returns
`*pbflagsv1.FlagValue` opaquely — it does not inspect the oneof variant.
The cache, stale fallback, kill set, and override resolution all operate on
`FlagValue` as an opaque proto message. List values flow through unchanged.

### Admin service and store

**`marshalFlagValue` / `unmarshalFlagValue`**: No changes. They call
`proto.Marshal` / `proto.Unmarshal` on `FlagValue`, which handles list
variants automatically.

**`parseFlagType`** (store.go): Add cases for the four list type strings.

**Type validation (new)**: The admin service currently does not validate that
a submitted `FlagValue`'s oneof variant matches the flag's declared `FlagType`.
As part of this feature, add validation in `UpdateFlagState` and
`SetFlagOverride` to reject mismatched types (e.g., submitting a scalar string
for a STRING_LIST flag). This validation benefits scalar flags too, as a
defense-in-depth measure.

### Admin web UI

#### Value display (`formatFlagValue`, `typeLabel`)

`formatFlagValue` gains cases for list oneof variants. Display format:
comma-separated items in brackets, e.g. `[ops@spotlight.gov, alerts@example.com]`.
Individual items containing commas are quoted. Empty lists display as `[]`.

`typeLabel` maps the new FlagType values to badge text: `string[]`, `int64[]`,
`double[]`, `bool[]`.

#### Value editing

For list flags, the single-value `<input>` or `<select>` is replaced with a
**textarea** (one value per line). This is the simplest approach that works
with the existing htmx form submission pattern:

```html
{{if isList .Flag.FlagType}}
  <form class="detail-value-form"
        hx-post="/api/flags/state/{{.Flag.FlagId}}"
        hx-target="#content" hx-swap="innerHTML"
        hx-vals='{"state":"ENABLED","flag_type":"{{.Flag.FlagType}}"}'>
    <div class="detail-value-input-group">
      <textarea name="value" class="detail-value-textarea" rows="4"
                placeholder="one value per line">{{listLines .Flag.CurrentValue}}</textarea>
      <button type="submit" class="btn btn-primary">Set</button>
    </div>
  </form>
{{end}}
```

`supported_values` remains a UI hint only — arbitrary values are accepted.
The UI could optionally show a multi-select `<select multiple>` for list flags
with `supported_values` in a future enhancement, but the textarea covers all
cases without additional JavaScript.

#### Value parsing (`parseFlagValue`)

The handler's `parseFlagValue` function gains list-type cases. For list types,
the raw form value (textarea content) is split by newlines, trimmed, and empty
lines discarded. For typed lists (int64, double, bool), entries that fail to
parse are silently dropped rather than rejecting the entire submission — this
is more forgiving for manual editing and consistent with the general
"never-throw" philosophy:

```go
case "STRING_LIST":
    items := splitListValue(raw)
    return &pbflagsv1.FlagValue{
        Value: &pbflagsv1.FlagValue_StringListValue{
            StringListValue: &pbflagsv1.StringList{Values: items},
        },
    }, nil
case "INT64_LIST":
    items := splitListValue(raw)
    var parsed []int64
    for _, s := range items {
        v, err := strconv.ParseInt(s, 10, 64)
        if err != nil {
            continue // drop invalid entries
        }
        parsed = append(parsed, v)
    }
    return &pbflagsv1.FlagValue{
        Value: &pbflagsv1.FlagValue_Int64ListValue{
            Int64ListValue: &pbflagsv1.Int64List{Values: parsed},
        },
    }, nil
```

#### Override form

The override form follows the same pattern: textarea for list values, parsed
the same way. The override table displays list values with the same bracketed
format.

### Go codegen

#### Type mapping

`goTypeInfo` (which currently takes `protoreflect.Kind`) is extended to handle
list fields. When `field.Desc.IsList()` is true, the return types change:

| Element Kind | `goType`    | `getterName`         | `oneofType`                 |
|-------------|-------------|----------------------|-----------------------------|
| BoolKind    | `[]bool`    | `GetBoolListValue`   | `FlagValue_BoolListValue`   |
| StringKind  | `[]string`  | `GetStringListValue` | `FlagValue_StringListValue` |
| Int64Kind   | `[]int64`   | `GetInt64ListValue`  | `FlagValue_Int64ListValue`  |
| DoubleKind  | `[]float64` | `GetDoubleListValue` | `FlagValue_DoubleListValue` |

The getter extracts the list message then calls `.GetValues()` to get the
underlying slice.

#### Default values

Go `const` cannot hold slices. List defaults are generated as **functions**
that return a fresh copy on each call, preventing accidental mutation of a
shared slice:

```go
// Compiled defaults from proto annotations.
const (
    SeverityThresholdDefault = int64(3)
)

// NotificationEmailsDefault returns the compiled default for NotificationEmails.
func NotificationEmailsDefault() []string {
    return []string{"ops@spotlight.gov"}
}
```

Usage in generated client and Defaults():

```go
func (c *incidentConfigFlagsClient) NotificationEmails(ctx context.Context) []string {
    resp, err := c.evaluator.Evaluate(ctx, connect.NewRequest(&pbflagsv1.EvaluateRequest{
        FlagId: NotificationEmailsID,
    }))
    if err != nil {
        return NotificationEmailsDefault()
    }
    v, ok := resp.Msg.GetValue().GetValue().(*pbflagsv1.FlagValue_StringListValue)
    if !ok {
        return NotificationEmailsDefault()
    }
    return v.StringListValue.GetValues()
}
```

For list flags without a proto default, the zero value is `nil`:
```go
func (defaultIncidentConfigFlags) NotificationEmails(_ context.Context) []string {
    return nil
}
```

#### Unknown-bytes parsing

The Go codegen has two extraction paths: a reflection-based path (when protoc
resolves the `pbflags.flag` extension into typed fields) and an unknown-bytes
fallback (when extensions land in the unknown wire bytes, which happens with
some protoc/buf versions that don't have the pbflags extension descriptors
linked into the plugin's descriptor pool). The `parseFlagDefault` function in
the unknown-bytes path gains cases for field numbers 5-8. Each parses the list
message's repeated field and produces the Go literal string (e.g.,
`[]string{"a", "b"}`).

### Java codegen

#### New SDK interfaces

Java's `Class<T>` type token cannot represent parameterized types like
`List<String>` due to type erasure. Rather than adding a `TypeToken` dependency
or using raw types, introduce a parallel set of list-aware interfaces:

**`ListFlag.java`** (new):
```java
package org.spotlightgov.pbflags;
import java.util.List;

public interface ListFlag<E> {
    List<E> get();
    List<E> get(String entityId);
}
```

**`LayerListFlag.java`** (new):
```java
package org.spotlightgov.pbflags;
import java.util.List;

public interface LayerListFlag<E, ID> {
    List<E> get();
    List<E> get(ID id);
}
```

**`FlagEvaluator`** gains factory methods:
```java
default <E> ListFlag<E> listFlag(
    String flagId, Class<E> elementType, List<E> compiledDefault) { ... }

default <E, ID> LayerListFlag<E, ID> layerListFlag(
    String flagId, Class<E> elementType, List<E> compiledDefault,
    java.util.function.Function<ID, String> idToString) { ... }
```

**`FlagEvaluatorClient`** gains `evaluateList`:
```java
public <E> List<E> evaluateList(
    String flagId, Class<E> elementType, List<E> compiledDefault,
    @Nullable String entityId) { ... }
```

The `extractListValue` helper mirrors `extractValue`, switching on the
`ValueCase` to extract the appropriate list:

```java
private static <E> List<E> extractListValue(
    FlagValue value, Class<E> elementType, List<E> fallback) {
    if (elementType == String.class
        && value.getValueCase() == FlagValue.ValueCase.STRING_LIST_VALUE) {
        return (List<E>) List.copyOf(value.getStringListValue().getValuesList());
    }
    // ... bool, int64, double cases
    return fallback;
}
```

`List.copyOf()` ensures the returned list is unmodifiable and detached from
the proto's internal storage.

#### Generated code

For a `repeated string` flag, the generated interface uses `ListFlag`:

```java
public interface IncidentConfigFlags {
    String NOTIFICATION_EMAILS_ID = "incident_config/1";
    List<String> NOTIFICATION_EMAILS_DEFAULT = List.of("ops@spotlight.gov");

    ListFlag<String> notificationEmails();

    // scalar flags use Flag<T> as before
    Flag<Long> severityThreshold();
}
```

The `forEvaluator` factory:
```java
static IncidentConfigFlags forEvaluator(FlagEvaluator evaluator) {
    return new IncidentConfigFlags() {
        @Override
        public ListFlag<String> notificationEmails() {
            return evaluator.listFlag(
                NOTIFICATION_EMAILS_ID, String.class, NOTIFICATION_EMAILS_DEFAULT);
        }
        // ...
    };
}
```

For layer-scoped list flags, use `LayerListFlag<E, ID>` with the same pattern.

#### Type mapping

| Element Kind | `javaType`      | `javaBoxedType` | `javaClassLiteral` |
|-------------|-----------------|------------------|--------------------|
| BoolKind    | `List<Boolean>` | `Boolean`        | `Boolean.class`    |
| StringKind  | `List<String>`  | `String`         | `String.class`     |
| Int64Kind   | `List<Long>`    | `Long`           | `Long.class`       |
| DoubleKind  | `List<Double>`  | `Double`         | `Double.class`     |

The `javaBoxedType` and `javaClassLiteral` represent the *element* type —
the list wrapping is handled by the `ListFlag<E>` / `LayerListFlag<E, ID>`
interfaces.

## Wire Compatibility

Adding new oneof variants to `FlagValue` and `FlagDefault` is a backwards-
compatible proto change. Clients running older code that does not understand
list variants will see the new oneof fields as unknown — the `extractValue` /
type-check logic returns the compiled default (safe degradation). This matches
the existing never-throw guarantee.

Old evaluator servers will store and return list FlagValues as opaque BYTEA
without interpreting them, since the evaluator does not inspect the oneof
variant. The only hard requirement is that `pbflags-sync` and the admin UI
are upgraded before list flags are defined in proto — otherwise sync would
skip them (UNSPECIFIED type) and the UI would not render them.

**Upgrade order:** sync + admin server first, then evaluator, then clients.
This is the same order used for any new flag type or proto change.

## Alternatives Considered

### A. Comma-separated strings

Encode lists as a single `string` flag with comma-separated values.

**Rejected because:** This is the current workaround and the reason this
issue exists. Values containing commas break silently. No type safety for
non-string lists. The admin UI shows an opaque string. Generated client code
returns `string`, not `[]string` — every call site must split and parse.

### B. JSON-encoded strings

Encode lists as a JSON array in a `string` flag (e.g., `["a","b"]`).

**Rejected because:** Same problems as comma-separated, except parsing is
slightly more reliable. Still no type safety — a JSON string is opaque to the
proto schema, the evaluator, and codegen. The admin UI would need a JSON
editor. The generated client still returns `string`.

### C. Normalized database table

Store list items in a separate `flag_list_values` table with one row per item,
keyed by `(flag_id, ordinal)`.

**Rejected because:** Adds complexity to every layer — evaluation needs a
JOIN or second query, caching needs to aggregate rows, overrides need a
parallel `override_list_values` table, audit log entries become multi-row.
The protobuf-serialized BYTEA approach is simpler and consistent with the
existing pattern. PostgreSQL BYTEA columns handle variable-length values
efficiently.

### D. Single generic `ListValue` message

Instead of typed `BoolList` / `StringList` / etc., use a single
`ListValue { repeated FlagValue values = 1; }` that holds a list of scalar
FlagValues.

**Rejected because:** This allows mixed-type lists (a list containing both
strings and ints), which is never valid. Type checking would need to happen
at runtime rather than being enforced by the proto schema. The typed list
messages make invalid states unrepresentable.

### E. `ListFlag<E> extends Flag<List<E>>` in Java

Make `ListFlag` a subtype of `Flag` to allow interop with existing code that
accepts `Flag<?>`.

**Rejected because:** `FlagEvaluator.flag()` creates `Flag<T>` using
`evaluate(flagId, Class<T>, ...)`, and `Class<List<String>>` is not
expressible in Java due to type erasure. Separate `listFlag()` /
`evaluateList()` methods are required regardless. Adding an inheritance
relationship between `ListFlag` and `Flag` would create a confusing API
surface where `ListFlag<String>.get()` returns `List<String>` but the
inherited `Flag<List<String>>.get()` signature looks identical yet is backed
by different extraction logic. Keeping them separate is simpler and
unambiguous.

## Impact on Existing Components

| Component | Impact |
|---|---|
| `types.proto` | Add `BoolList`, `StringList`, `Int64List`, `DoubleList` messages; extend `FlagValue` oneof with list variants; extend `FlagType` enum |
| `options.proto` | Extend `FlagDefault` oneof with list variants; import `types.proto` list messages |
| `evaluator.proto` | No changes — `FlagValue` extension flows through |
| `admin.proto` | No changes — uses `FlagValue` and `FlagType` from `types.proto` |
| Descriptor parsing | `kindToFlagType` → `fieldToFlagType` (check `IsList()`); `parseFlagDefault` gains list cases |
| Schema sync | `flagTypeString` gains list cases; no other changes |
| Database schema | No migration — BYTEA handles list values; `flag_type` column stores new string values |
| Evaluator | No changes — FlagValue is opaque |
| Admin service | Add type validation (FlagValue variant vs. flag_type); `parseFlagType` gains list cases |
| Admin store | No changes — marshal/unmarshal are generic |
| Admin web UI | `formatFlagValue` and `typeLabel` gain list cases; textarea input for list editing; `parseFlagValue` gains list parsing (split by newline) |
| Go codegen | `goTypeInfo` gains list cases (returns `[]T`); defaults become functions; accessor extracts list from oneof |
| Java SDK | New `ListFlag<E>`, `LayerListFlag<E, ID>` interfaces; `evaluateList` on `FlagEvaluator` / `FlagEvaluatorClient` |
| Java codegen | `javaTypeInfo` gains list cases; generated interfaces use `ListFlag<E>` / `LayerListFlag<E, ID>` |
| Tests | New test cases for list flag parsing, evaluation, admin CRUD, codegen golden files |

## Resolved Questions

1. **Max list size.** The admin service enforces a configurable maximum number
   of items per list. The default is 100 items. Deployments can override this
   via server configuration. The proto schema does not enforce this — it is a
   server-side validation rule only.

2. **Empty list semantics.** `[]` (empty list) is a valid flag value, distinct
   from "no value set." The proto schema supports this: an empty `StringList`
   message in the oneof is distinguishable from "no oneof field set" in both
   `FlagDefault` and `FlagValue`. A flag can have a default of `[]`, and an
   admin can explicitly set the value to `[]`. The evaluator and generated code
   treat both as `[]T{}` / `List.of()`.

3. **`supported_values` for list items.** `supported_values` remains a UI hint
   only, consistent with scalar flag behavior. No server-side enforcement.
   Improved UX for repeated fields with `supported_values` (e.g., multi-select
   dropdown) can be done as a follow-up.
