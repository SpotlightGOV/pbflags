# pbflags Roadmap

This document tracks planned improvements and features for pbflags, organized
by theme and approximate priority. Items are sequenced so that foundational
work (observability, security) lands before features that depend on it
(advanced evaluation, ecosystem integrations).

---

## Phase 1 — Observability & Operations (Next)

Low risk, high operational value, no API surface changes required.

### 1. OpenTelemetry Instrumentation

Add distributed tracing and metrics via OpenTelemetry SDK instrumentation.

**Scope:**
- Trace evaluation calls end-to-end (receive request, cache lookup, fetch, respond)
- Record cache hit/miss ratios per tier (kill set, global flags, overrides)
- Measure fetch latency to PostgreSQL and upstream proxy
- Propagate trace context through the Connect RPC boundary

**Why:** The three-tier cache architecture is the core performance story.
Without tracing, operators can't diagnose whether slowness is in the cache
layer, the database, or the upstream proxy. OTel is the industry standard and
integrates with Grafana, Datadog, Honeycomb, etc.

**Key files:** `internal/evaluator/evaluator.go`, `cache.go`, `dbfetcher.go`, `client.go`

### 2. Prometheus Metrics Endpoint

Expose a `/metrics` endpoint with operational counters and histograms.

**Scope:**
- `pbflags_evaluations_total` — counter by feature, source, status
- `pbflags_cache_hits_total` / `pbflags_cache_misses_total` — by tier
- `pbflags_fetch_duration_seconds` — histogram for DB and upstream fetches
- `pbflags_kill_set_size` — gauge of currently killed flags
- `pbflags_override_cache_size` — gauge of LRU entries
- `pbflags_health_consecutive_failures` — gauge per upstream
- `pbflags_poller_last_success_timestamp` — for staleness alerting

**Why:** Metrics are the first thing an on-call engineer reaches for. The
health endpoint (`/healthz`) gives a binary signal; metrics give the gradient.
Cache eviction rates, override cardinality, and poller freshness are all
operationally critical and currently invisible.

**Key files:** `internal/evaluator/cache.go`, `poller.go`, `health.go`, `service.go`

### 3. Structured Logging

Migrate from implicit logging to Go's `log/slog` with consistent fields.

**Scope:**
- Adopt `slog.Logger` throughout the evaluator and admin packages
- Attach structured fields: `feature_id`, `entity_id`, `evaluation_source`,
  `cache_tier`, `latency_ms`
- Support JSON output for log aggregation pipelines
- Configurable log level via flag or environment variable

**Why:** The current logging is ad-hoc. In production, operators need to
correlate evaluation decisions with specific entities and features. Structured
fields enable filtering and aggregation in ELK, Loki, CloudWatch, etc.

**Key files:** All files in `internal/evaluator/`, `internal/admin/`, `cmd/pbflags-server/main.go`

---

## Phase 2 — Reach & Hardening (Soon)

Broadens language support, adds security basics, and improves developer
ergonomics.

### 4. TypeScript Client Code Generation

Add TypeScript as a codegen target in `protoc-gen-pbflags`.

**Scope:**
- Generate typed flag accessors with compiled defaults
- Target both Node.js (server) and browser runtimes
- Use Connect-Web for transport (aligns with existing Connect RPC server)
- Maintain the never-throw guarantee: catch transport errors, return defaults
- Generate `.d.ts` type declarations
- Publish to npm as `@spotlightgov/pbflags`

**Why:** TypeScript has the largest potential user base. Connect-Web is the
natural client for the existing Connect RPC server, making this the
lowest-friction language expansion.

**Key files:** `internal/codegen/` (new `tsgen/` package), `cmd/protoc-gen-pbflags/main.go`

### 5. API Authentication

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

### 6. RBAC for Admin API

Role-based access control separating read and write operations.

**Scope:**
- Define roles: `viewer` (evaluate, list, audit log), `editor` (+ state changes),
  `admin` (+ override management, role management)
- Enforce at the Connect RPC interceptor level
- Store role assignments (start with static config, migrate to DB later)
- Audit log entries include authenticated principal

**Why:** The admin API already has audit logging — adding RBAC completes the
security story. Without it, anyone with network access can kill flags or set
overrides. Depends on authentication (item 5) being in place.

**Depends on:** Item 5 (API Authentication)

**Key files:** `internal/admin/service.go`, new `internal/auth/rbac.go`

### 7. Native TLS Support

Optional in-process TLS termination.

**Scope:**
- Accept `--tls-cert` and `--tls-key` flags
- Upgrade from h2c to h2 (HTTP/2 over TLS) when certs are provided
- Support automatic certificate reload on SIGHUP (align with descriptor reload)
- Document mutual TLS (mTLS) configuration for service-to-service auth

**Why:** Removes hard dependency on external TLS proxy for encrypted transport.
Simplifies single-binary deployments and development setups.

**Key files:** `cmd/pbflags-server/main.go`

### 8. CLI Tool for Flag Management

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
A CLI enables scripting (CI/CD flag gates, incident runbooks) and is the
fastest path to usable flag management before a full UI exists.

**Key files:** New `cmd/pbflags/` directory, consumes admin API client

---

## Phase 3 — Developer Experience (Later)

Higher effort, builds on earlier work.

### 9. Admin Web UI

A lightweight web dashboard for flag management.

**Scope:**
- Feature list with current state (enabled/killed/default) and value
- Flag detail view with override list and audit history
- Toggle flag state, manage overrides via forms
- Search and filter by feature, label, state
- Read-only mode for viewer role, full controls for editor/admin
- Embed in the pbflags-server binary via `//go:embed`
- Tech: lightweight framework (preact/htmx) to minimize bundle size

**Why:** Non-engineering stakeholders (product managers, support) need
visibility into flag state without CLI or API access. The admin API already
exposes all necessary operations — this is a presentation layer.

**Depends on:** Items 5-6 (auth/RBAC) for access control

**Key files:** New `internal/admin/ui/` directory, `cmd/pbflags-server/main.go` (embed + serve)

### 10. Percentage Rollouts

Gradual feature rollout by consistent entity hashing.

**Scope:**
- New `rollout_percentage` field on flag configuration (0-100)
- Deterministic hashing: `hash(feature_id + entity_id) % 100 < percentage`
- Consistent — same entity always gets same result for same percentage
- Monotonic — increasing percentage never removes previously-included entities
- Integrates into evaluation precedence between override and global state checks
- Admin API support for setting and viewing rollout percentage
- Codegen updates for typed rollout configuration

**Why:** Binary on/off is insufficient for safe production rollouts. Percentage
rollouts are the most requested feature flag capability and the `Layer` enum
plus override system provide a natural extension point.

**Key files:** `internal/evaluator/evaluator.go`, `proto/pbflags/v1/types.proto`, `db/migrations/`

### 11. Dry-Run / What-If Evaluation

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

### 12. Cross-Language Integration Tests

End-to-end tests validating Go server + Java client consistency.

**Scope:**
- Test harness: start Go server, evaluate from Java client, assert results match
- Cover all evaluation precedence levels across the language boundary
- Validate proto wire format compatibility
- Run in CI with both PostgreSQL and in-memory modes
- Extend to TypeScript client when available (item 4)

**Why:** The proto contract is the source of truth, but serialization edge cases
(wrapper types, default values, unknown fields) can diverge between language
runtimes. Integration tests catch these before users do.

**Key files:** New `tests/integration/` directory, CI workflow updates

### 13. Chaos / Failure-Mode Tests

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

### 14. Performance Benchmarks

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

## Phase 4 — Advanced Evaluation (Horizon)

Strategic features that depend on adoption signal and earlier foundational work.

### 15. Targeting Rules

Attribute-based evaluation for entity segmentation.

**Scope:**
- Define targeting rules on flags: `attribute op value` (e.g., `region == "EU"`)
- Evaluation receives entity attributes as key-value context
- Rule engine evaluates conditions (equality, set membership, regex, numeric range)
- Precedence: kill > override > targeting rule > global state > default
- Admin API for rule CRUD, audit logging for rule changes
- Proto schema for rule definitions

**Why:** "Enable for users in region=EU" is a fundamental segmentation need.
Without targeting rules, operators must create per-entity overrides for every
entity — which doesn't scale. This is the bridge from feature flags to
feature management.

**Depends on:** Core evaluation stable, admin API mature

**Key files:** `proto/pbflags/v1/types.proto`, `internal/evaluator/evaluator.go`, new rule engine

### 16. Flag Dependencies / Prerequisites

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

### 17. Scheduled Activation / Deactivation

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

### 18. Rust Client Code Generation

Add Rust as a codegen target in `protoc-gen-pbflags`.

**Scope:**
- Generate typed flag accessors with compiled defaults as constants
- Use `tonic` for gRPC transport (standard Rust gRPC library)
- Never-panic guarantee: all evaluation errors return compiled defaults
- Publish to crates.io as `pbflags`
- Include integration test support utilities

**Why:** Rust is a natural fit for proto-first systems and zero-cost
abstractions. The never-throw pattern maps elegantly to Rust's `Result` type
with default fallback. Growing Rust adoption in infrastructure makes this a
strategic language target.

**Key files:** `internal/codegen/` (new `rustgen/` package), `cmd/protoc-gen-pbflags/main.go`

### 19. Node.js Client Code Generation

Add Node.js as a codegen target (if not covered by TypeScript).

**Scope:**
- Evaluate whether the TypeScript client (item 4) fully covers Node.js use cases
- If separate target needed: generate CommonJS/ESM modules with typed accessors
- Support both Connect-Node and gRPC-Node transports
- Never-throw guarantee with compiled defaults
- Publish to npm alongside TypeScript package

**Why:** Some Node.js projects don't use TypeScript. If the TS client requires
a TypeScript build step, a pure JS target removes that friction. Decision
depends on item 4's implementation — may be unnecessary if TS output is
directly consumable.

**Depends on:** Item 4 (TypeScript client) — evaluate coverage gap first

**Key files:** `internal/codegen/` (potentially `nodegen/`), `cmd/protoc-gen-pbflags/main.go`

### 20. OpenFeature Provider

Implement the OpenFeature provider interface for Go and Java.

**Scope:**
- Go: implement `openfeature.FeatureProvider` interface wrapping pbflags evaluator
- Java: implement `dev.openfeature.sdk.FeatureProvider` wrapping `FlagEvaluator`
- Map pbflags evaluation to OpenFeature evaluation context
- Support OpenFeature hooks for logging and metrics
- Publish as separate modules (`pbflags-openfeature-go`, `pbflags-openfeature-java`)

**Why:** OpenFeature is the emerging standard for feature flag client
interfaces. Providing an OpenFeature provider lets teams adopt pbflags without
client-side vendor lock-in — they can swap providers without changing
application code. Also enables interop with OpenFeature's ecosystem of hooks
and tooling.

**Key files:** New `clients/openfeature/` packages for Go and Java

---

## Sequencing Summary

| Phase | Items | Timeline Signal |
|-------|-------|-----------------|
| **Next** | 1-3 (observability) | Low risk, high operational value |
| **Soon** | 4-8 (reach & hardening) | Broadens adoption, hardens for real use |
| **Later** | 9-14 (developer experience) | Higher effort, builds on earlier work |
| **Horizon** | 15-20 (advanced eval & ecosystem) | Strategic bets, depend on adoption |

---

## Out of Scope (for now)

- **Helm chart** — Kubernetes packaging deferred until deployment patterns stabilize
- **Terraform / Pulumi provider** — Infrastructure-as-code integration deferred; proto definitions already serve as the source of truth
