# Go Client

## Codegen setup

### Buf configuration

Add pbflags to your `buf.yaml`:

```yaml
version: v2
modules:
  - path: proto
deps:
  - buf.build/spotlightgov/pbflags
```

Create `buf.gen.flags.yaml`:

```yaml
version: v2
plugins:
  - local: protoc-gen-pbflags
    out: gen/flags
    strategy: all  # required — plugin needs all files to find the Layer enum
    opt:
      - lang=go
      - package_prefix=github.com/yourorg/yourrepo/gen/flags
inputs:
  - directory: proto
```

`strategy: all` is required because the plugin needs to see all files in a single invocation to discover the `Layer` enum.

### Install and generate

```bash
go install github.com/SpotlightGOV/pbflags/cmd/protoc-gen-pbflags@latest
buf dep update
buf generate --template buf.gen.flags.yaml
```

> After upgrading pbflags, always run `buf dep update` to pull the latest proto definitions from BSR.

### Plugin options

| Option | Required | Description |
|---|---|---|
| `lang=go` | Yes | Target language |
| `package_prefix=...` | Yes | Go import path prefix for generated packages |

## Generated API surface

For each feature message (e.g., `Notifications`), the codegen produces a package (e.g., `notificationsflags/`) containing:

### Interface

```go
type NotificationsFlags interface {
    // One method per flag, with typed return value and optional layer parameter.
    EmailEnabled(ctx context.Context, id layers.UserID) bool
    DigestFrequency(ctx context.Context) string

    // Status returns the evaluator connection health.
    Status(ctx context.Context) pbflagsv1.EvaluatorStatus
}
```

- Flags with a non-global layer take a typed layer ID parameter (e.g., `layers.UserID`).
- Global flags take no entity parameter.
- Return types are native Go types: `bool`, `string`, `int64`, `float64`, or their slice variants for list flags.

### Setup

```go
evaluator := pbflagsv1connect.NewFlagEvaluatorServiceClient(
    http.DefaultClient,
    "http://localhost:9201",
)
notifications := notificationsflags.NewNotificationsFlagsClient(evaluator)
```

Returns compiled defaults on any evaluation error (never panics).

Minimal imports:

```go
import (
    "net/http"

    "github.com/yourorg/yourrepo/gen/flags/v1/pbflagsv1connect"
)
```

### `Defaults()` — offline defaults

```go
flags := notificationsflags.Defaults()
```

Returns an implementation backed entirely by compiled defaults. Use this when the evaluator is optional — eliminates nil checks:

```go
type Server struct {
    flags notificationsflags.NotificationsFlags  // never nil
}

func NewServer(evaluator pbflagsv1connect.FlagEvaluatorServiceClient) *Server {
    flags := notificationsflags.Defaults()
    if evaluator != nil {
        flags = notificationsflags.NewNotificationsFlagsClient(evaluator)
    }
    return &Server{flags: flags}
}
```

`Defaults()` returns a zero-allocation empty struct.

### `Testing()` — test stubs

```go
flags := notificationsflags.Testing()
flags.EmailEnabledFunc = func(_ context.Context, _ layers.UserID) bool {
    return false  // override just this flag
}
// Other flags still return compiled defaults

svc := NewService(flags)
```

Returns a mutable struct with func fields pre-populated with compiled defaults. Override individual fields to stub specific flags without implementing the full interface.

### `FlagDescriptors` — flag metadata

```go
for _, d := range notificationsflags.FlagDescriptors {
    fmt.Printf("Flag %s (%s): type=%v list=%v layer=%q\n",
        d.ID, d.FieldName, d.Type, d.IsList, d.LayerType)
}
```

A `[]flagmeta.FlagDescriptor` slice providing structured metadata about every flag. Each descriptor includes:

- `ID`, `FieldName` — flag identification
- `Type` (`FlagTypeBool`, `FlagTypeString`, `FlagTypeInt64`, `FlagTypeDouble`) and `IsList`
- Typed default fields (`DefaultBool`, `DefaultString`, `DefaultStrings`, etc.)
- `HasEntityID` and `LayerType` — layer/entity scoping info

### Typed layer IDs

The codegen also produces a `layers/` package with typed ID wrappers for each non-global layer:

```go
import "github.com/yourorg/yourrepo/gen/flags/layers"

layers.User("user-123")    // layers.UserID
layers.Entity("org-456")   // layers.EntityID

// Zero value = global evaluation (no per-entity override applied)
layers.UserID{}
```

Compile-time safety: you cannot pass a `UserID` where an `EntityID` is expected.
