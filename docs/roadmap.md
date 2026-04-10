# pbflags Roadmap

This document tracks planned improvements and features for pbflags, organized
by approximate priority. Items are sequenced so that foundational work lands
before features that depend on it.

---

## Soon

### 1. Client-Side Flags

Define the first-class pattern for how client-side applications (browser,
mobile, edge) consume flags from pbflags.

**Scope:**
- **Evaluation model**: server-evaluated flags delivered to the client (not
  client-side rule evaluation — flag rules and targeting logic stay server-side)
- **Transport**: Connect-Web for browser clients, aligning with the existing
  Connect RPC server. Evaluate SSE streaming vs. polling for flag updates.
- **TypeScript SDK**: codegen target generating typed flag accessors with
  compiled defaults. Target both Node.js (server) and browser runtimes.
  Publish to npm as `@spotlightgov/pbflags`.
- **Security boundary**: clients receive only their evaluated results, never
  the full flag ruleset or other entities' overrides
- **Never-throw guarantee**: catch transport errors, return compiled defaults.
  Offline/disconnected clients always have safe values.
- **Developer ergonomics**: framework integrations (React hooks, etc.) that
  make flag consumption feel native

**Why:** Server-side Go and Java clients exist, but there's no story for how
browser or mobile applications consume flags. This is a different problem
than server-side evaluation — it involves transport choices, security
boundaries, and update propagation that don't arise in backend services.
Without a defined client-side pattern, teams will build ad-hoc solutions
that bypass the type safety and never-throw guarantees.

**Key files:** `internal/codegen/` (new `tsgen/` package), `cmd/protoc-gen-pbflags/main.go`

### 2. API Authentication

Add token-based authentication for evaluator and admin services.

**Scope:**
- Support API key authentication via `Authorization: Bearer <token>` header
- Separate evaluator (read) and admin (write) token scopes
- Token validation middleware for Connect RPC handlers
- Configuration via environment variable or config file
- Optional — disabled by default for backwards compatibility

**Why:** The system currently assumes a trusted network. Any deployment beyond
localhost or a private VPC needs authentication. Even a simple shared-secret
scheme prevents accidental exposure and enables audit trail attribution.

**Key files:** `cmd/pbflags-server/main.go`, new `internal/auth/` package

### 3. RBAC for Admin API

Role-based access control separating read and write operations.

**Scope:**
- Define roles: `viewer` (evaluate, list, audit log), `editor` (+ state changes),
  `admin` (+ override management, role management)
- Enforce at the Connect RPC interceptor level
- Store role assignments (start with static config, migrate to DB later)
- Audit log entries include authenticated principal

**Why:** The admin API already has audit logging — adding RBAC completes the
security story. Without it, anyone with network access can kill flags or set
overrides.

**Depends on:** Item 2 (API Authentication)

**Key files:** `internal/admin/service.go`, new `internal/auth/rbac.go`

### 4. Native TLS Support

Optional in-process TLS termination.

**Scope:**
- Accept `--tls-cert` and `--tls-key` flags
- Upgrade from h2c to h2 (HTTP/2 over TLS) when certs are provided
- Support automatic certificate reload on SIGHUP (align with descriptor reload)
- Document mutual TLS (mTLS) configuration for service-to-service auth

**Why:** Removes hard dependency on external TLS proxy for encrypted transport.
Simplifies single-binary deployments and development setups.

**Key files:** `cmd/pbflags-server/main.go`

---

## Later

### 5. Dry-Run / What-If Evaluation

Preview evaluation results without changing state.

**Scope:**
- New RPC: `DryRunEvaluate(feature_id, entity_id, hypothetical_state)`
- Returns: what the entity would see under the hypothetical configuration
- Supports hypothetical: state changes, override additions, rollout percentages
- No side effects — read-only operation
- Useful for pre-deploy verification and incident debugging

**Why:** "What would user X see if I enabled this flag?" is a question operators
ask during every incident and rollout. Currently requires reading code or
making the change and checking. A dry-run endpoint makes this safe and fast.

**Key files:** `internal/evaluator/service.go`, `proto/pbflags/v1/evaluator.proto`

### 6. Cross-Language Integration Tests

End-to-end tests validating Go server + Java client consistency.

**Scope:**
- Test harness: start Go server, evaluate from Java client, assert results match
- Cover all evaluation precedence levels across the language boundary
- Validate proto wire format compatibility
- Run in CI with both PostgreSQL and in-memory modes
- Extend to TypeScript client when available

**Why:** The proto contract is the source of truth, but serialization edge cases
(wrapper types, default values, unknown fields) can diverge between language
runtimes. Integration tests catch these before users do.

**Key files:** New `tests/integration/` directory, CI workflow updates

### 7. Chaos / Failure-Mode Tests

Validate graceful degradation under simulated failures.

**Scope:**
- PostgreSQL connection drops mid-evaluation → stale cache served
- Upstream proxy timeout → health tracker backoff behavior
- Kill set poll failure → last-known kill set preserved
- Cache at capacity → LRU eviction doesn't drop hot entries
- Descriptor file deleted mid-operation → last-known config preserved
- Network partition between proxy and root → proxy serves stale

**Why:** The stale-cache-during-outages guarantee and exponential backoff are
key reliability features. They're tested in unit tests but not under realistic
failure conditions. Chaos tests validate the system-level behavior.

**Key files:** `internal/evaluator/cache_test.go`, `health_test.go`, new `tests/chaos/`

### 8. Performance Benchmarks

Document evaluation latency and throughput characteristics.

**Scope:**
- `go test -bench` benchmarks for core evaluation path
- Cache contention under concurrent access (varying goroutine counts)
- Override LRU behavior at capacity (10k entries)
- Bulk evaluation throughput (varying batch sizes)
- Memory profile under sustained load
- Publish results in documentation with hardware specs

**Why:** Ristretto is fast and the architecture is sound, but without published
numbers, users can't capacity-plan. Benchmarks also serve as regression
detection — if a change doubles evaluation latency, the benchmark catches it.

**Key files:** New `internal/evaluator/bench_test.go`, `docs/benchmarks.md`

---

## Future

Items in this section are not committed work — they represent the logical
extension of the system based on where the industry has converged. Informed
by competitive analysis against LaunchDarkly, Flipt, Unleash, and Flagsmith.
Sequencing is meaningful: each item builds on the one before it.

### 9. Attribute-Based Targeting and Segmentation

Replace per-entity overrides as the only targeting mechanism with a rule
engine that evaluates structured attributes.

**Scope:**
- Introduce an **evaluation context** to replace the opaque `entity_id` string:
  structured key-value attributes (plan, region, role, email, custom properties)
- Define **segments** as reusable audience slices with attribute constraints
  (equals, contains, in, regex, numeric range) and match-all/match-any logic
- Define **targeting rules** on flags that reference segments, evaluated
  top-to-bottom with first-match-wins semantics
- Precedence: kill > per-entity override > targeting rule > global state > default
- Proto schema for rules, segments, and evaluation context
- Admin API + UI for rule and segment CRUD
- Audit logging for all rule/segment changes
- Backward compatible: existing entity_id overrides continue to work

**Why:** "Enable for users where `plan = enterprise`" is the fundamental
capability that every competitor provides and pbflags currently lacks. Without
targeting rules, operators must create per-entity overrides for every entity —
which doesn't scale. This is the bridge from feature flags to feature
management. Every subsequent item in this section depends on it.

**Key files:** `proto/pbflags/v1/types.proto`, `internal/evaluator/evaluator.go`, new rule engine

### 10. Percentage-Based Rollouts

Gradual feature rollout by consistent entity hashing, integrated with the
targeting rule engine.

**Scope:**
- Deterministic hashing: `hash(flag_key + entity_id) % 10000` for 0.01% granularity
- Consistent — same entity always gets same result for same percentage
- Monotonic — increasing percentage never removes previously-included entities
- Rollouts operate within targeting rules: "for segment X, roll out to 25%"
- Support percentage distributions across multiple values/variants
- Admin API + UI for configuring rollout percentages
- Codegen updates for typed rollout configuration

**Why:** Binary on/off is insufficient for safe production rollouts. Percentage
rollouts are the most commonly expected feature flag capability and the
primary mechanism for risk mitigation during releases. Combined with
targeting, this enables "roll out to 10% of enterprise users in US-EAST" —
the bread and butter of progressive delivery.

**Depends on:** Item 9 (targeting) — rollouts are a distribution within a
targeting rule, and require the evaluation context for entity identity.

**Key files:** `internal/evaluator/evaluator.go`, `proto/pbflags/v1/types.proto`, `db/migrations/`

### 11. OpenFeature Provider

Implement the OpenFeature provider interface for Go and Java.

**Scope:**
- Go: implement `openfeature.FeatureProvider` interface wrapping pbflags evaluator
- Java: implement `dev.openfeature.sdk.FeatureProvider` wrapping `FlagEvaluator`
- Map OpenFeature `EvaluationContext` to pbflags evaluation context (from item 9)
- Support OpenFeature hooks for logging and metrics
- Publish as separate modules (`pbflags-openfeature-go`, `pbflags-openfeature-java`)

**Why:** OpenFeature is the CNCF-incubating standard for feature flag client
interfaces. Every major competitor (LaunchDarkly, Flipt, Unleash, Flagsmith)
ships OpenFeature providers. Providing one lets teams adopt pbflags without
client-side vendor lock-in and enables interop with the OpenFeature hook
ecosystem. The evaluation context model from item 9 maps naturally to
OpenFeature's `EvaluationContext`.

**Depends on:** Item 10 (percentage rollouts) — OpenFeature's evaluation model
assumes the backend supports contextual evaluation; without targeting and
rollouts, the provider would be a thin wrapper with limited value.

**Key files:** New `clients/openfeature/` packages for Go and Java

### 12. Additional SDK Support (Rust)

Expand SDK coverage beyond Go, Java, and TypeScript.

**Scope:**
- **Rust**: codegen target using `tonic` for gRPC transport, published to
  crates.io as `pbflags`. Never-panic guarantee via `Result` with default fallback.
- **OpenFeature providers** for additional languages (lower bar than full codegen)

**Why:** With OpenFeature in place, teams using unsupported languages can use
the OpenFeature SDK with a pbflags provider as the breadth path, while codegen
clients remain the premium type-safe path. Rust is a natural fit for
proto-first systems. LaunchDarkly ships 29+ SDKs — pbflags doesn't need
parity, but breadth matters for platform adoption.

**Depends on:** Item 11 (OpenFeature) — OpenFeature providers for new languages
are faster to ship than full codegen targets and provide immediate coverage.

**Key files:** `internal/codegen/` (new `rustgen/` package), `cmd/protoc-gen-pbflags/main.go`

### 13. Experimentation Framework (A/B Testing)

Measure the impact of flag variations with statistical analysis.

**Scope:**
- Event collection: SDK-side impression tracking (flag evaluated, variation seen,
  entity context)
- Metrics pipeline: ingest outcome events (conversion, latency, error rate) and
  correlate with flag assignments
- Statistical engine: significance testing (frequentist or Bayesian), sample size
  estimation, guardrail metrics
- Experiment lifecycle: create experiment → assign traffic via rollouts →
  collect data → analyze → conclude
- Admin UI for experiment creation, monitoring, and results
- Integration with external analytics (export to warehouse, webhook on conclusion)

**Why:** Experimentation is the end state of feature management — not just
"is the flag on?" but "is the flag *working*?" LaunchDarkly has built-in
A/B testing; most open-source tools do not. This is a significant
differentiator but also the highest-effort item on the roadmap. It requires
targeting + rollouts as prerequisites, plus an event/metrics pipeline that
doesn't exist yet.

**Depends on:** Items 9-10 (targeting + rollouts) — experiments are rollouts
with measurement.

**Key files:** New `internal/experiment/` package, `proto/pbflags/v1/experiment.proto`

### 14. Flag Dependencies / Prerequisites

Conditional flag activation based on other flag states.

**Scope:**
- New `prerequisites` field: list of `(feature_id/field_number, required_state)` pairs
- Evaluation checks prerequisites before applying flag's own state
- If any prerequisite fails → flag evaluates to compiled default
- Cycle detection at configuration time (admin API rejects cycles)
- Dependency graph visible in admin UI and CLI

**Why:** Complex feature rollouts often have ordering constraints ("Flag B
requires Flag A"). Without prerequisites, operators must manually coordinate
flag states — error-prone during incidents. Dependency tracking makes the
implicit explicit.

**Key files:** `internal/evaluator/evaluator.go`, `proto/pbflags/v1/types.proto`, `internal/admin/store.go`

### 15. Scheduled Activation / Deactivation

Time-based flag lifecycle management.

**Scope:**
- New fields: `activate_at` and `deactivate_at` timestamps on flag state
- Evaluator checks wall clock against schedule during evaluation
- Scheduler goroutine for proactive state transitions (vs. lazy evaluation)
- Admin API and CLI support for setting schedules
- Audit log records scheduled transitions
- Timezone-aware with UTC storage

**Why:** "Enable the holiday banner at 9am EST on Black Friday, disable at
midnight" is a common use case. Currently requires manual intervention or
external cron jobs. Built-in scheduling reduces operational burden and
eliminates the risk of forgetting to disable a flag.

**Key files:** `internal/evaluator/evaluator.go`, `proto/pbflags/v1/types.proto`, `internal/admin/store.go`

### 16. CLI Tool for Flag Management

A `pbflags` CLI for scriptable flag operations.

**Scope:**
- `pbflags flags list` — list features and flag states
- `pbflags flags get <feature_id>/<field_number>` — show flag details
- `pbflags flags set <feature_id>/<field_number> --state=ENABLED --value=...`
- `pbflags flags kill <feature_id>/<field_number>` — emergency kill switch
- `pbflags overrides set <feature_id>/<field_number> --entity=<id> --value=...`
- `pbflags overrides remove <feature_id>/<field_number> --entity=<id>`
- `pbflags audit <feature_id>` — view audit log
- Connect to admin API via `--server` flag, authenticate via `--token` or env var
- Output formats: table (default), JSON, YAML

**Why:** Flag operations shouldn't require curl/grpcurl incantations or a UI.
A CLI enables scripting (CI/CD flag gates, incident runbooks).

**Key files:** New `cmd/pbflags/` directory, consumes admin API client

---

## Sequencing Summary

| Phase | Items | Signal |
|-------|-------|--------|
| **Soon** | 1-4 | Client-side story, security hardening |
| **Later** | 5-8 | Testing, benchmarks, operator tooling |
| **Future** | 9-16 | Feature management platform, not committed |

---

## Out of Scope (for now)

- **Helm chart** — Kubernetes packaging deferred until deployment patterns stabilize
- **Terraform / Pulumi provider** — Infrastructure-as-code integration deferred; proto definitions already serve as the source of truth
