# pbflags Roadmap

This document tracks planned improvements and features for pbflags, organized
by approximate priority. Items are sequenced so that foundational work lands
before features that depend on it.

---

## Done

### ~~API Authentication~~ ✅

**Implemented in v0.18.0.** Pluggable authentication middleware for the admin API
with three strategies: `none` (default, backward-compatible), `shared-secret`
(Bearer token), and `trusted-header` (for reverse proxies). Configured via
`PBFLAGS_AUTH_STRATEGY` environment variable. See [deployment.md](deployment.md#authentication).

### ~~Evaluation Context~~ ✅

**Implemented in v0.15.0.** Structured `EvaluationContext` message with typed
dimensions defined via `(pbflags.context)` and `(pbflags.dimension)` proto
annotations. Generated `dims` package with dimension constructors.

### ~~Percentage-Based Rollouts~~ ✅

**Implemented in v0.17.0** as "Launches" — per-condition value overrides with
deterministic FNV-32a hashing, evaluation scopes with typed codegen, inline
launch overrides on conditions and static values, admin UI kill/unkill, and
mechanical `pb launch land` for YAML transform.

Phase 2 (future): `launch.in_ramp()` in CEL expressions for structural condition
chain changes under a launch, with CEL simplification for mechanical landing.

### ~~CLI Tool~~ ✅

**Implemented in v0.18.0.** Unified `pb` CLI with admin commands (`pb flag`,
`pb launch`, `pb audit`), auth management (`pb auth`), config commands
(`pb sync`, `pb validate`, `pb show`, `pb compile`, `pb load`, `pb export`),
and developer workflow commands (`pb init`, `pb lint`, `pb migrate`).
All admin commands support `--json` for scripting.

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
  the full flag ruleset or other entities' data
- **Never-throw guarantee**: catch transport errors, return compiled defaults.
  Offline/disconnected clients always have safe values.
- **Developer ergonomics**: framework integrations (React hooks, etc.) that
  make flag consumption feel native

**Key files:** `internal/codegen/` (new `tsgen/` package), `cmd/protoc-gen-pbflags/main.go`

### 2. RBAC for Admin API

Role-based access control separating read and write operations.

**Scope:**
- Define roles: `viewer` (evaluate, list, audit log), `editor` (+ state changes),
  `admin` (+ role management)
- Enforce at the Connect RPC interceptor level
- Store role assignments (start with static config, migrate to DB later)
- Audit log entries include authenticated principal

**Depends on:** API Authentication (done)

**Key files:** `internal/admin/service.go`, `internal/authn/`

### 3. Native TLS Support

Optional in-process TLS termination.

**Scope:**
- Accept `--tls-cert` and `--tls-key` flags
- Upgrade from h2c to h2 (HTTP/2 over TLS) when certs are provided
- Support automatic certificate reload on SIGHUP (align with descriptor reload)
- Document mutual TLS (mTLS) configuration for evaluator service-to-service auth

**Key files:** `cmd/pbflags-admin/main.go`, `cmd/pbflags-evaluator/main.go`

---

## Later

### 4. Dry-Run / What-If Evaluation

Preview evaluation results without changing state.

**Scope:**
- New RPC: `DryRunEvaluate(feature_id, context, hypothetical_state)`
- Returns: what the entity would see under the hypothetical configuration
- Supports hypothetical: state changes, rollout percentages
- No side effects — read-only operation
- Useful for pre-deploy verification and incident debugging

**Key files:** `internal/evaluator/service.go`, `proto/pbflags/v1/evaluator.proto`

### 5. Cross-Language Integration Tests

End-to-end tests validating Go server + Java client consistency.

**Scope:**
- Test harness: start Go server, evaluate from Java client, assert results match
- Cover all evaluation precedence levels across the language boundary
- Validate proto wire format compatibility
- Run in CI with both PostgreSQL and in-memory modes
- Extend to TypeScript client when available

**Key files:** New `tests/integration/` directory, CI workflow updates

### 6. Performance Benchmarks

Document evaluation latency and throughput characteristics.

**Scope:**
- `go test -bench` benchmarks for core evaluation path
- Cache contention under concurrent access (varying goroutine counts)
- Bulk evaluation throughput (varying batch sizes)
- Memory profile under sustained load
- Publish results in documentation with hardware specs

**Key files:** New `internal/evaluator/bench_test.go`, `docs/benchmarks.md`

---

## Future

Items in this section are not committed work — they represent the logical
extension of the system based on where the industry has converged.

### 7. OpenFeature Provider

Implement the OpenFeature provider interface for Go and Java.

**Scope:**
- Go: implement `openfeature.FeatureProvider` interface wrapping pbflags evaluator
- Java: implement `dev.openfeature.sdk.FeatureProvider` wrapping `FlagEvaluator`
- Map OpenFeature `EvaluationContext` to pbflags evaluation context
- Support OpenFeature hooks for logging and metrics
- Publish as separate modules (`pbflags-openfeature-go`, `pbflags-openfeature-java`)

**Key files:** New `clients/openfeature/` packages for Go and Java

### 8. Additional SDK Support (Rust)

Expand SDK coverage beyond Go, Java, and TypeScript.

**Scope:**
- **Rust**: codegen target using `tonic` for gRPC transport, published to
  crates.io as `pbflags`. Never-panic guarantee via `Result` with default fallback.
- **OpenFeature providers** for additional languages (lower bar than full codegen)

### 9. Experimentation Framework (A/B Testing)

Measure the impact of flag variations with statistical analysis.

**Scope:**
- Event collection: SDK-side impression tracking (flag evaluated, variation seen,
  entity context)
- Metrics pipeline: ingest outcome events (conversion, latency, error rate) and
  correlate with flag assignments
- Statistical engine: significance testing, sample size estimation, guardrail metrics
- Experiment lifecycle: create experiment → assign traffic via rollouts →
  collect data → analyze → conclude
- Admin UI for experiment creation, monitoring, and results

**Depends on:** Launches (done) — experiments are rollouts with measurement.

### 10. Flag Dependencies / Prerequisites

Conditional flag activation based on other flag states.

**Scope:**
- New `prerequisites` field: list of `(feature_id/field_number, required_state)` pairs
- Evaluation checks prerequisites before applying flag's own state
- If any prerequisite fails → flag evaluates to compiled default
- Cycle detection at configuration time
- Dependency graph visible in admin UI and CLI

### 11. Scheduled Activation / Deactivation

Time-based flag lifecycle management.

**Scope:**
- New fields: `activate_at` and `deactivate_at` timestamps on flag state
- Evaluator checks wall clock against schedule during evaluation
- Admin API and CLI support for setting schedules
- Audit log records scheduled transitions

---

## Sequencing Summary

| Phase | Items | Signal |
|-------|-------|--------|
| **Done** | Auth, Context, Rollouts, CLI | Foundation complete |
| **Soon** | 1-3 | Client-side story, security hardening |
| **Later** | 4-6 | Testing, benchmarks, operator tooling |
| **Future** | 7-11 | Platform features, not committed |

---

## Out of Scope (for now)

- **Helm chart** — Kubernetes packaging deferred until deployment patterns stabilize
- **Terraform / Pulumi provider** — Infrastructure-as-code integration deferred; proto definitions already serve as the source of truth
