# DB-Centric Flag Definitions

**Date:** 2026-04-09
**Status:** Draft
**Issue:** [#28](https://github.com/SpotlightGOV/pbflags/issues/28)

> The server currently requires `descriptors.pb` at runtime to build its
> in-memory defaults registry. Flag definitions are pushed to the database
> by a separate `pbflags-sync` binary that must be run explicitly at deploy
> time. When sync is forgotten, the admin UI shows stale flags even though
> the evaluator is serving the correct defaults.
>
> This proposal makes the database the single source of truth for flag
> definitions and supports two deployment models: a **monolithic deployment**
> where the server handles everything (migrations, sync, reload), and a
> **distributed deployment** where an external `pbflags-sync` job manages
> definitions and the server only reads from the database.

---

## Background

The original architecture (v1, v2 design docs) separated concerns into two
layers:

- **Definition layer** — `descriptors.pb` defines what flags exist, their
  types, defaults, and layers. Parsed at startup into an in-memory defaults
  registry.
- **Runtime state layer** — PostgreSQL stores overrides, kills, and
  configured values. Consulted on-demand per flag evaluation.

The in-memory defaults registry gave the evaluator an important property:
**evaluation never depends on the database for defaults.** Even if Postgres
is unreachable, every flag returns its compiled default. In practice, this
was belt-and-suspenders — generated clients also compile in defaults, so the
client-side fallback already provides this guarantee regardless of the
server's definition source.

This design assumed infrastructure for delivering the descriptor file to
running instances (GCS config-sync, Kubernetes ConfigMaps, mounted volumes).
For the original deployment at Spotlight, that infrastructure existed. For an
open-source tool, it is a significant operational burden.

### What broke

When Spotlight reorganized flags (25 new flags across per-backend features),
the server correctly hot-reloaded from the updated descriptor and reported
`total_flags: 34`. But the admin UI only showed the original 9 flags
because `pbflags-sync` was never wired into the deploy pipeline to replace
the old Java `FlagSyncTask`.

The root cause is a deploy-time coordination problem: the server and the
database must both be updated, in the right order, by different mechanisms.
The server gets its descriptor via file delivery; the database gets its
definitions via an explicit sync command. Forgetting either step leaves the
system in an inconsistent state that is not immediately obvious.

## Proposal

Make the database the single source of truth for flag definitions and
runtime state. Support two deployment models that share the same DB-centric
foundation:

- **Monolithic deployment** — one server binary does everything
- **Distributed deployment** — external sync job manages definitions,
  server(s) read from DB

In distributed mode, the server builds its in-memory defaults registry from
the database. In monolithic mode, the server builds its registry directly
from the descriptor (no DB round-trip) and syncs definitions to the database
as a side effect for the admin UI and override FK integrity.

## Deployment models

### Monolithic deployment

A single server process handles migrations, definition sync, evaluation, and
the admin UI. Ideal for small teams, single-server setups, VMs, demos, and
local development.

```
pbflags-server --database=postgres://... --descriptors=descriptors.pb --admin=:8080
```

On startup:
1. Run pending schema migrations
2. Parse `descriptors.pb` → build in-memory defaults registry directly
3. Sync definitions to DB (side effect for admin UI / override FKs)
4. Start serving (evaluator + admin UI)

On fsnotify (descriptor file changes):
1. Re-parse `descriptors.pb` → swap registry directly from parsed definitions
2. Re-sync to DB (side effect)

The registry is built directly from the parsed descriptor, **not** from a
DB round-trip. This means the evaluator can start and serve defaults even if
the database is temporarily unreachable — only the sync step fails, and it
retries on the next poll interval. The DB sync is a side effect that keeps
the admin UI and override FK constraints in sync with the evaluator.

One binary, one flag file, one database. No CI pipeline, no separate sync
job, no cron.

**Scaling up from monolithic:** When you need multiple server instances
(e.g., behind a load balancer), run one instance in monolithic mode and the
rest in distributed mode. The monolithic instance handles migrations, sync,
and fsnotify; the distributed instances just read from the database. No
special flags needed — the two modes compose naturally.

```
# One monolithic instance — handles migrations, sync, and fsnotify
pbflags-server --database=postgres://... --descriptors=descriptors.pb --admin=:8080

# Additional instances — distributed mode, read from DB only
pbflags-server --database=postgres://...
```

### Distributed deployment

An external `pbflags-sync` job manages schema migrations and definition
sync. The server only needs a database connection string. Ideal for
multi-instance production deployments with CI/CD pipelines.

```
# In CI/CD (runs once per deploy):
pbflags-sync --descriptors=descriptors.pb --database=postgres://...

# Application deploy (any number of instances):
pbflags-server --database=postgres://...
```

`pbflags-sync` runs schema migrations automatically before syncing
definitions. It embeds all goose migrations and applies pending ones
in a transaction. This makes the deploy workflow a single command — no
separate `goose up` step. Migrations only change on new pbflags releases,
so running them on every sync is a fast no-op in the common case.

The server loads definitions from the database at startup and polls for
changes. No descriptor file is needed at runtime.

### What changes

| Concern | Current | Monolithic | Distributed |
|---------|---------|------------|-------------|
| Server startup | Parse `descriptors.pb` → in-memory defaults | Migrate → parse descriptor → registry + sync to DB | Load DB → in-memory defaults |
| `--descriptors` flag | Required | Provided (enables sync + fsnotify) | Omitted (DB-only) |
| Who runs migrations | Manual `goose up` | Server at startup | `pbflags-sync` |
| Who syncs definitions | Manual `pbflags-sync` | Server at startup + fsnotify | `pbflags-sync` in CI/CD |
| Definition reload | fsnotify on descriptor file | fsnotify + DB poll | DB poll + reload endpoint |
| Descriptor file at runtime | Required | Required (on monolithic instance) | Not required (only in CI) |
| Best for | — | Single server, VM, demo | Multi-instance, CI/CD, production |

In distributed mode, servers can run as **root** evaluators (direct DB
access) or **proxy** evaluators (cache + forward to upstream). Only root
evaluators load definitions from the database. Proxy evaluators receive
typed `FlagValue` responses from upstream and cache them — they don't need
definitions, DB access, or descriptors. This limits the worst-case DB load
to the number of root evaluators (typically 1-3), regardless of total
instance count.

## Design

### Server startup: building the defaults registry

Both deployment models produce the same `[]evaluator.FlagDef` slice and
build the registry identically via `NewDefaults(defs)` → `NewRegistry()`.
The difference is where the `[]FlagDef` comes from.

**Monolithic mode** (`--descriptors` provided):

1. `ParseDescriptorFile()` → `[]FlagDef` (same as today)
2. `NewDefaults(defs)` → `NewRegistry(defaults)` (registry built directly)
3. Sync `[]FlagDef` to DB in background (side effect for admin UI / FKs)

The registry is built from the descriptor parse result with no DB
dependency. The evaluator can start and serve defaults even if the database
is temporarily unreachable — only the sync step fails.

**Distributed mode** (`--descriptors` omitted):

1. `LoadDefinitionsFromDB(pool)` → `[]FlagDef`
2. `NewDefaults(defs)` → `NewRegistry(defaults)`

```sql
SELECT f.feature_id, f.display_name, f.description, f.owner,
       fl.flag_id, fl.field_number, fl.display_name, fl.flag_type,
       fl.layer, fl.description, fl.default_value
FROM feature_flags.features f
JOIN feature_flags.flags fl ON fl.feature_id = f.feature_id
WHERE fl.archived_at IS NULL
ORDER BY f.feature_id, fl.field_number
```

For large flag sets (thousands of flags), the query is executed in batches
(e.g., 500 flags per batch, paginated by `flag_id`) and accumulated before
building the registry.

**Equivalence guarantee:** Both paths produce `[]FlagDef`. Integration
tests must verify that for any given set of flag definitions,
`ParseDescriptorFile()` and `LoadDefinitionsFromDB()` produce identical
`[]FlagDef` slices (same flags, same types, same defaults, same layers).
This is the critical invariant that allows the two modes to be
interchangeable.

### `pbflags-sync` as the CI/CD deploy command

`pbflags-sync` already does the core sync work:

1. Parse `descriptors.pb`
2. Upsert features and flags (idempotent `ON CONFLICT DO UPDATE`)
3. Archive flags removed from the descriptor
4. Never touch `state` or `value` columns (runtime state is preserved)

With this change, `pbflags-sync` also runs schema migrations automatically
before syncing definitions. It embeds all goose migrations and applies
pending ones in a transaction:

```
pbflags-sync --descriptors=descriptors.pb --database=postgres://...
```

### Definition reload

When flag definitions change in the database, the server needs to refresh
its in-memory defaults registry.

**1. DB poll (distributed mode, always active in root mode)**

The server polls for definition changes on an interval (default 60s
± 20% jitter, configurable via `--definition-poll-interval`):

```sql
SELECT MAX(updated_at) FROM feature_flags.flags
```

If the timestamp is newer than the last load, reload definitions and swap
the registry. The reload query fetches definitions in batches (e.g., 500
flags per batch) to scale to thousands of flags without large single-query
result sets. The registry swap is still atomic — batches are accumulated
into a complete `[]FlagDef` before calling `Registry.Swap()`.

The timestamp check itself is a single indexed query returning one row —
negligible overhead. This is simple, stateless, and works with any Postgres
deployment.

The poller uses the same health-based exponential backoff as the existing
proxy mode health tracker — consecutive failures increase the poll interval
(2x, 4x, 8x capped), and a single successful response resets the backoff.

**2. fsnotify (monolithic mode, when `--descriptors` provided)**

Same mechanism as today. Descriptor file changes trigger a re-parse and
registry swap directly from the parsed definitions. The DB sync runs as a
side effect. The DB poll also runs, so distributed instances (if any) pick
up the change within the poll interval.

**3. Admin reload endpoint**

`POST /admin/reload-definitions` triggers an immediate refresh. Useful for:
- CI/CD pipelines that run `pbflags-sync` then poke the server
- Manual recovery
- Environments where polling is undesirable

**4. LISTEN/NOTIFY (future)**

`pbflags-sync` could issue `NOTIFY flag_definitions_changed` after its
transaction commits. The server subscribes with `LISTEN`. This gives
near-instant propagation without polling. Deferred — the polling approach
is sufficient for most deployments and doesn't require a persistent
listener connection.

### Failure modes

Sync can fail for three reasons: the descriptor can't be parsed, the
database is unreachable, or the sync detects a type/layer conflict (an
operator error that requires a two-deploy fix per the v1 design). Each
failure type has different behavior depending on when it occurs.

**Monolithic mode — startup failures:**

| Failure | Behavior | Rationale |
|---------|----------|-----------|
| Descriptor parse fails | **Fail to start.** | Server can't build registry without definitions. Same as today. |
| DB unreachable | **Start and serve.** Registry built from descriptor. Sync retries on poll interval. Admin UI shows stale definitions until DB recovers. | Evaluator has everything it needs from the descriptor. DB is only needed for overrides/kills, which degrade gracefully via cache. |
| Type/layer conflict | **Fail to start.** | Operator error. The descriptor contains a breaking change (type or layer mutation) that conflicts with existing DB state. Must be fixed via two-deploy process: remove old field first, then add new field with new field number. |

**Monolithic mode — fsnotify reload failures:**

| Failure | Behavior | Rationale |
|---------|----------|-----------|
| Descriptor parse fails | Log error, **keep current registry.** | Same as today. Corrupted descriptor can't take down a running evaluator. |
| DB unreachable | **Swap registry from new descriptor.** Log warning about sync failure, retry sync on next poll. | Evaluator should serve updated defaults. DB sync is a side effect — its failure shouldn't block the evaluator from using better data. |
| Type/layer conflict | Log error, **keep current registry.** Do not swap. DB unchanged. | The new descriptor has a breaking change. Swapping the registry would put the evaluator and DB out of sync (evaluator sees new type, DB has old type, admin UI would set values of wrong type). |

**Distributed mode — startup failures:**

| Failure | Behavior | Rationale |
|---------|----------|-----------|
| DB unreachable | **Fail to start.** | No descriptor to fall back on. Server can't build registry. |

Type/layer conflicts can't occur in distributed mode — `pbflags-sync`
catches them and fails before the server ever starts. Parse failures can't
occur because there's no descriptor to parse.

**Distributed mode — poll reload failures:**

| Failure | Behavior | Rationale |
|---------|----------|-----------|
| DB unreachable | **Keep current registry.** Back off using health tracker. | Same graceful degradation as all other DB-dependent operations. |

**After startup (both modes):** The in-memory defaults registry is
unaffected by DB outages. Evaluation continues using cached state and
compiled defaults. The definition poller backs off using the existing
health tracker (2x, 4x, 8x capped).

### Comparison of deployment models

| Property | Monolithic | Distributed |
|----------|------------|-------------|
| Deploy complexity | Minimal (one binary, one file) | Standard CI/CD pipeline |
| Scaling | Single instance (or monolithic + distributed) | Root evaluators + optional proxy tiers |
| DB load from definitions | One instance reads/writes | Only root evaluators read (typically 1-3) |
| Definition reload | Instant (fsnotify) + DB poll | DB poll (60s default) + reload endpoint |
| Operational overhead | None — server handles everything | Run `pbflags-sync` in deploy pipeline |
| Descriptor file at runtime | Required on monolithic instance | Not required (only in CI) |

## What does NOT change

- **Proto definitions** — flags are still defined in proto, compiled to
  `descriptors.pb`, and used by `pbflags-sync` and `protoc-gen-pbflags`
- **`pbflags-sync` interface** — same binary, same flags, same behavior
  (plus embedded migrations)
- **Evaluation logic** — same precedence chain (kill set → override →
  global state → compiled default)
- **Admin API** — already reads from DB; no changes needed for flag listing
- **Generated clients** — compiled defaults baked into client code, unchanged
- **Schema** — no migration required; the existing tables already store
  everything needed
- **`protoc-gen-pbflags`** — codegen is unrelated to server runtime
- **Proxy evaluators** — no DB access, no descriptors, no definitions
  needed. They receive typed `FlagValue` responses from upstream and cache
  them. Completely unaffected by this change.

## Implementation plan

| Phase | Work |
|-------|------|
| **1. Embed migrations** | Embed goose migrations in both `pbflags-sync` and `pbflags-server`. Run pending migrations automatically in monolithic mode. |
| **2. DB definition loader** | New function `LoadDefinitionsFromDB(pool) ([]FlagDef, error)` that queries features+flags in batches and returns the same `[]FlagDef` slice that `ParseDescriptorFile` returns. |
| **3. Extract sync package** | Extract sync logic from `pbflags-sync` into a shared package. Both `pbflags-sync` and `pbflags-server` (monolithic mode) call the same code. |
| **4. Server startup modes** | When `--descriptors` is provided (monolithic): migrate → parse descriptor → build registry → sync to DB. When omitted (distributed): load from DB → build registry. Both paths produce `[]FlagDef` and build the registry identically. |
| **5. Definition poller** | Background goroutine that polls `MAX(updated_at)` with jitter (± 20%) and reloads definitions in batches when they change. Reuses `Registry.Swap()` and the existing `HealthTracker` backoff logic. |
| **6. Admin registry access** | Give the admin service a reference to the `Registry` instead of a static `[]FlagDef` slice. The admin's metadata enrichment reads from `registry.Load()` so it stays current after definition reloads. |
| **7. Reload endpoint** | `POST /admin/reload-definitions` triggers an immediate definition refresh. |
| **8. Equivalence tests** | Integration tests that sync a descriptor to DB via `pbflags-sync`, then compare `ParseDescriptorFile()` output against `LoadDefinitionsFromDB()` output for the same flag set. Must be identical: same flags, types, defaults, layers, metadata. |
| **9. Scale benchmark** | Benchmark with 5K+ flags: definition loading (batched DB query), registry construction, atomic swap under concurrent readers. Validates that batching works correctly and swap doesn't block evaluations. |
| **10. Documentation** | Update README, getting-started guide, and deploy examples. Document monolithic and distributed deployment models with clear guidance on when to use each. |
