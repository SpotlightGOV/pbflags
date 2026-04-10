# Java Client

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
    out: src/main/java
    strategy: all  # required — plugin needs all files to find the Layer enum
    opt:
      - lang=java
      - java_package=com.yourorg.flags.generated
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

### Plugin options

| Option | Required | Description |
|---|---|---|
| `lang=java` | Yes | Target language |
| `java_package=...` | Yes | Java package for generated classes |
| `java_dagger=true` | No | Generate Dagger `@Module` with `@Binds` entries |

## Generated API surface

For each feature message (e.g., `Notifications`), the codegen produces:

### Interface

```java
public interface NotificationsFlags {
    // One accessor per flag, returning a typed Flag object.
    Flag<Boolean> emailEnabled();
    Flag<String> digestFrequency();

    // Factory methods
    static NotificationsFlags forEvaluator(FlagEvaluatorClient evaluator);
}
```

Flag accessors return `Flag<T>` objects with `.get()` and `.get(LayerID)` methods.

### Usage

```java
NotificationsFlags flags = NotificationsFlags.forEvaluator(evaluator);

boolean emailEnabled = flags.emailEnabled().get(UserID.of("user-123"));
String frequency = flags.digestFrequency().get();
```

### Evaluator client setup

```java
// Simple: connect by target address
FlagEvaluatorClient client = new FlagEvaluatorClient("localhost:9201");

// Advanced: custom channel (TLS, interceptors, in-process testing)
ManagedChannel channel = ManagedChannelBuilder.forTarget("localhost:9201")
    .useTransportSecurity()
    .build();
FlagEvaluatorClient client = FlagEvaluatorClient.forChannel(channel);
```

### Typed layer IDs

Each non-global layer produces a static inner class:

```java
UserID.of("user-123")
EntityID.of("org-456")
```

### Flag ID constants

Each flag has a constant for its ID, useful for testing and admin operations:

```java
NotificationsFlags.EMAIL_ENABLED_ID  // "notifications/1"
```

## Testing

Add the test dependency:

```groovy
testImplementation("org.spotlightgov.pbflags:pbflags-java-testing:0.3.0")
```

### JUnit 5 extension

```java
class MyTest {
  @RegisterExtension
  static final TestFlagExtension flags = new TestFlagExtension();

  @Test
  void testOverride() {
    flags.set(NotificationsFlags.EMAIL_ENABLED_ID, false);
    var nf = NotificationsFlags.forEvaluator(flags.evaluator());
    assertFalse(nf.emailEnabled().get());
  }
}
```

`TestFlagExtension` provides an in-memory evaluator that starts with compiled defaults. Use `flags.set(id, value)` to override individual flags per test.

## Dagger integration

Add `java_dagger=true` to codegen options:

```yaml
opt:
  - lang=java
  - java_package=com.yourorg.flags.generated
  - java_dagger=true
```

This generates:

- `@Inject` and `@Singleton` annotations on implementation classes
- `FlagRegistryModule.java` — a Dagger `@Module` with `@Binds` entries mapping each `*Flags` interface to its `*FlagsImpl`

Include the module in your Dagger component and inject the interfaces directly.

## Maven coordinates

| Artifact | Description |
|---|---|
| `org.spotlightgov.pbflags:pbflags-java` | Runtime client library |
| `org.spotlightgov.pbflags:pbflags-java-testing` | JUnit 5 test utilities |
