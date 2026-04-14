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
    strategy: all  # required — plugin needs all files to find the EvaluationContext message
    opt:
      - lang=go
      - package_prefix=github.com/yourorg/yourrepo/gen/flags
inputs:
  - directory: proto
```

`strategy: all` is required because the plugin needs to see all files in a single invocation to discover the `EvaluationContext` message.

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
    // One method per flag — just context in, typed value out.
    EmailEnabled(ctx context.Context) bool
    DigestFrequency(ctx context.Context) string
    MaxRetries(ctx context.Context) int64
    ScoreThreshold(ctx context.Context) float64
    NotificationEmails(ctx context.Context) []string
}
```

- All methods take only `context.Context` — evaluation dimensions are bound on the evaluator, not passed per-call.
- Return types are native Go types: `bool`, `string`, `int64`, `float64`, or their slice variants for list flags.

### Evaluator and dimensions

Create an evaluator with `pbflags.Connect`, then scope it with dimensions from the generated `dims` package:

```go
import (
    "net/http"

    pb "github.com/yourorg/yourrepo/gen/flags/proto"
    "github.com/yourorg/yourrepo/gen/flags/dims"
    "github.com/yourorg/yourrepo/gen/flags/notificationsflags"
    "github.com/SpotlightGOV/pbflags/pbflags"
)

// Create a base evaluator.
eval := pbflags.Connect(http.DefaultClient, "http://localhost:9201", &pb.EvaluationContext{})

// Add dimensions — With() returns a new evaluator (immutable).
scoped := eval.With(dims.UserID("user-123"), dims.Plan(pb.PlanLevel_PLAN_LEVEL_PRO))

// Create a typed feature client from the scoped evaluator.
notifications := notificationsflags.New(scoped)
```

Returns compiled defaults on any evaluation error (never panics).

### Scope-based access (recommended)

If your proto defines evaluation scopes, the codegen produces per-scope `*Features` types with constructors that require the scope's dimensions. This makes missing dimensions a compile error:

```go
import "github.com/yourorg/yourrepo/gen/flags/dims"

// Scope constructors require their dimensions as typed parameters.
userFeatures := dims.NewUserFeatures(eval, sessionID, userID)
notifications := userFeatures.Notifications()  // cached — no allocation after first call
emailEnabled := notifications.EmailEnabled(ctx)

// Duck-typed interfaces let handlers declare what they need.
func handleNotification(features dims.HasNotifications) {
    freq := features.Notifications().DigestFrequency(ctx)
}
// Accepts *UserFeatures, *TenantFeatures, or any scope with Notifications().
```

### Context integration

Store and retrieve evaluators via `context.Context` for use in middleware / request handlers:

```go
// In middleware — attach the evaluator to the request context.
ctx = pbflags.ContextWith(ctx, eval.With(dims.UserID(currentUser(ctx))))

// In a handler — retrieve it.
notifications := notificationsflags.New(pbflags.FromContext(ctx))
enabled := notifications.EmailEnabled(ctx)
```

`FromContext` returns a no-op evaluator (compiled defaults) if none is set, so it is always safe to call.

### `Defaults()` — offline defaults

```go
flags := notificationsflags.Defaults()
```

Returns an implementation backed entirely by compiled defaults. Use this when the evaluator is optional — eliminates nil checks:

```go
type Server struct {
    flags notificationsflags.NotificationsFlags  // never nil
}

func NewServer(eval pbflags.Evaluator) *Server {
    flags := notificationsflags.Defaults()
    if eval != nil {
        flags = notificationsflags.New(eval)
    }
    return &Server{flags: flags}
}
```

`Defaults()` returns a zero-allocation empty struct.

### `Testing()` — test stubs

```go
flags := notificationsflags.Testing()
flags.EmailEnabledFunc = func(_ context.Context) bool {
    return false  // override just this flag
}
// Other flags still return compiled defaults

svc := NewService(flags)
```

Returns a mutable struct with func fields pre-populated with compiled defaults. Override individual fields to stub specific flags without implementing the full interface.

### `FlagDescriptors` — flag metadata

```go
for _, d := range notificationsflags.FlagDescriptors {
    fmt.Printf("Flag %s (%s): type=%v list=%v\n",
        d.ID, d.FieldName, d.Type, d.IsList)
}
```

A `[]flagmeta.FlagDescriptor` slice providing structured metadata about every flag. Each descriptor includes:

- `ID`, `FieldName` — flag identification
- `Type` (`FlagTypeBool`, `FlagTypeString`, `FlagTypeInt64`, `FlagTypeDouble`) and `IsList`
- Typed default fields (`DefaultBool`, `DefaultString`, `DefaultStrings`, etc.)

### Evaluation context dimensions

The codegen produces a `dims` package with typed dimension constructors derived from your `EvaluationContext` proto message:

```go
import "github.com/yourorg/yourrepo/gen/flags/dims"

dims.UserID("user-123")                           // string dimension
dims.Plan(pb.PlanLevel_PLAN_LEVEL_PRO)             // enum dimension
dims.IsInternal(true)                              // bool dimension
```

Use these with `eval.With(...)` to bind dimensions before creating a feature client. Dimensions are immutable — `With()` always returns a new evaluator without modifying the parent.
