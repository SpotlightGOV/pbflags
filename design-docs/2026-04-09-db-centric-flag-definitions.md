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
> definitions and supports two explicit deployment modes: **monolithic**
> (single instance, does everything) and **distributed** (external sync,
> server reads from DB).

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
runtime state. Support two explicit deployment modes:

- **Monolithic** — single instance; handles migrations, sync, and reload
- **Distributed** — external `pbflags-sync` in CI/CD; server(s) read from DB

Both modes require the database at startup and build the in-memory defaults
registry from committed DB state. The DB commit is always the single
promotion point for definition changes — no instance ever serves definitions
that haven't been committed to the database.

**Resilience tradeoff:** The original design allowed the evaluator to
cold-start and serve compiled defaults without any DB connectivity, using
only the descriptor file. This design gives that up — the server cannot
start without reaching the database. This is a conscious trade: the
original resilience was belt-and-suspenders (generated clients already
compile in defaults as the ultimate fallback), and it came at the cost of
requiring descriptor file delivery infrastructure on every evaluator
instance. After startup, the resilience story is unchanged — the in-memory
registry survives DB outages and evaluation degrades gracefully through
cached state to compiled defaults.

## Two axes: definition sync and evaluator topology

This design affects the **definition sync model** — how flag definitions
get into the database and into the server's in-memory registry. It does
NOT change the **evaluator topology** — the root vs proxy hierarchy that
determines how flag *state* is fetched at evaluation time.

| Axis | Options | What it controls |
|------|---------|------------------|
| **Definition sync** | `--monolithic` or `--distributed` | Where definitions come from (descriptor + local sync, or DB via external sync) |
| **Evaluator topology** | `--database` (root) or `--upstream` (proxy) | Where flag state is fetched (DB directly, or forwarded to upstream) |

**Proxy evaluators are orthogonal to this design.** They have no DB access,
no descriptors, and no definitions. They receive typed `FlagValue` responses
from upstream and cache them. The `--monolithic` / `--distributed` flags
only apply to root evaluators — proxy evaluators use `--upstream` and are
completely unaffected by this change.

## Definition sync modes

Root evaluators require an explicit `--monolithic` or `--distributed` flag.
This makes the sync mode a conscious choice with clear validation rules,
not an inference from other flags.

### Monolithic mode

A single server process handles definition sync, evaluation, and the admin
UI. Ideal for small teams, single-server setups, VMs, demos, and local
development.

```
pbflags-server --monolithic --migrate \
  --descriptors=descriptors.pb \
  --database=postgres://... \
  --admin=:8080
```

On startup:
1. If `--migrate`: run pending schema migrations
2. Check schema version (see "Schema version check" below)
3. Parse `descriptors.pb` and sync definitions to DB
4. Load definitions from DB → build in-memory defaults registry
5. Start serving (evaluator + admin UI)

On fsnotify (descriptor file changes):
1. Re-parse `descriptors.pb` and re-sync to DB
2. If sync succeeds: reload definitions from DB → swap registry
3. If sync fails: log error, keep current registry

The registry is always built from committed DB state, not directly from
the parsed descriptor. This ensures the monolithic instance sees exactly
the same definitions it wrote — including any flags that were skipped due
to type/layer conflicts, any normalization applied by the sync, and any
archiving of removed flags.

**Monolithic mode is single-instance only.** Do not run multiple monolithic
instances behind a load balancer. If you need multiple instances, use
distributed mode.

| Flag | Behavior |
|------|----------|
| `--monolithic` | Required. Enables sync + fsnotify. |
| `--descriptors` | Required. Path to `descriptors.pb`. |
| `--database` | Required. PostgreSQL connection string. |
| `--migrate` | Optional. Run pending schema migrations at startup. |
| `--admin` | Optional. Starts admin UI on the given address. |

### Distributed mode

An external `pbflags-sync` job manages schema migrations and definition
sync. The server only needs a database connection string. Ideal for
multi-instance production deployments with CI/CD pipelines.

```
# In CI/CD (runs once per deploy):
pbflags-sync --descriptors=descriptors.pb --database=postgres://...

# Application deploy (any number of instances):
pbflags-server --distributed --database=postgres://...
```

`pbflags-sync` runs schema migrations automatically before syncing
definitions. It embeds all goose migrations and applies pending ones
in a transaction. This makes the deploy workflow a single command — no
separate `goose up` step. Migrations only change on new pbflags releases,
so running them on every sync is a fast no-op in the common case.

The server loads definitions from the database at startup and polls for
changes. No descriptor file is needed at runtime. Only root evaluators
load definitions — proxy evaluators are unaffected (see "Two axes" above).
This limits the worst-case DB load from definition polling to the number of
root evaluators (typically 1-3), regardless of total instance count.

| Flag | Behavior |
|------|----------|
| `--distributed` | Required. DB-only mode, no local sync. |
| `--database` | Required. PostgreSQL connection string. |
| `--admin` | Optional. Starts admin UI on the given address. |
| `--descriptors` | Rejected. Use `pbflags-sync` instead. |
| `--migrate` | Rejected. Use `pbflags-sync` instead. |

### What changes

| Concern | Current | Monolithic | Distributed |
|---------|---------|------------|-------------|
| Mode selection | Implicit | Explicit `--monolithic` | Explicit `--distributed` |
| Server startup | Parse `descriptors.pb` → in-memory defaults | Migrate → sync → load DB → registry | Check schema → load DB → registry |
| Who runs migrations | Manual `goose up` | Server at startup (`--migrate`) | `pbflags-sync` |
| Who syncs definitions | Manual `pbflags-sync` | Server at startup + fsnotify | `pbflags-sync` in CI/CD |
| Definition reload | fsnotify on descriptor file | fsnotify (sync → reload from DB) + DB poll | DB poll + reload endpoint |
| Descriptor file at runtime | Required | Required | Not required |
| Scaling | — | Single root instance only | Any number of root evaluators (+ optional proxy tiers) |
| Best for | — | Single server, VM, demo | Multi-instance, CI/CD, production |

## Design

### Schema version check

On startup, before doing anything else, the server verifies that the
database schema meets its minimum required version. Each server binary
has a minimum migration version compiled in (e.g., server v0.12 requires
at least migration 001).

The check queries goose's version tracking table:

```sql
SELECT MAX(version_id) FROM goose_db_version WHERE is_applied = true
```

| Result | Behavior |
|--------|----------|
| Table doesn't exist | **Fail to start.** Schema not initialized. |
| Version below minimum | **Fail to start.** Schema too old. |
| Version at or above minimum | Proceed with startup. |

The error message is actionable:

```
fatal: database schema version 0 < required 1
  run "pbflags-sync --database=..." to apply migrations, or
  start with "pbflags-server --monolithic --migrate" to auto-migrate
```

In monolithic mode with `--migrate`, migrations run first, then the schema
check verifies success. Without `--migrate`, the check runs immediately
and fails if the schema is behind — the operator must explicitly opt in to
migrations.

### Server startup: building the defaults registry

Both modes produce the same `[]evaluator.FlagDef` slice and build the
registry identically via `NewDefaults(defs)` → `NewRegistry()`. Both
modes load definitions from the database.

**Monolithic mode:**

1. Run migrations (if `--migrate`)
2. Parse `descriptors.pb` → sync to DB (upsert definitions, archive removed flags)
3. `LoadDefinitionsFromDB(pool)` → `[]FlagDef`
4. `NewDefaults(defs)` → `NewRegistry(defaults)`

**Distributed mode:**

1. `LoadDefinitionsFromDB(pool)` → `[]FlagDef`
2. `NewDefaults(defs)` → `NewRegistry(defaults)`

The definition load query:

```sql
SELECT f.feature_id, f.display_name, f.description, f.owner,
       fl.flag_id, fl.field_number, fl.display_name, fl.flag_type,
       fl.layer, fl.description, fl.default_value
FROM feature_flags.features f
JOIN feature_flags.flags fl ON fl.feature_id = f.feature_id
WHERE fl.archived_at IS NULL
ORDER BY f.feature_id, fl.field_number
```

**Batched loading:** For large flag sets (thousands of flags), the query
is executed in batches (e.g., 500 flags per batch, paginated by
`flag_id`) and accumulated before building the registry. **All batches
must be read within a single read transaction** to ensure a consistent
snapshot — if `pbflags-sync` commits between batches, a mixed old/new
`[]FlagDef` would break the equivalence guarantee.

**Equivalence guarantee:** Both the sync-then-load path (monolithic) and
the load-only path (distributed) produce `[]FlagDef` from the same DB
state. Integration tests must verify that for any given set of flag
definitions, the `[]FlagDef` loaded from DB after sync is identical to
what `ParseDescriptorFile()` would produce (same flags, types, defaults,
layers, metadata). This is the critical invariant.

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

**1. DB poll (always active in root mode)**

The server polls for definition changes on an interval (default 60s
± 20% jitter, configurable via `--definition-poll-interval`):

```sql
SELECT GREATEST(
  (SELECT COALESCE(MAX(updated_at), '1970-01-01') FROM feature_flags.flags),
  (SELECT COALESCE(MAX(updated_at), '1970-01-01') FROM feature_flags.features)
)
```

The poll watches both tables — a deploy that only changes feature metadata
(display name, description, owner) triggers a reload, not just flag changes.

If the timestamp is newer than the last load, reload definitions within a
single read transaction and swap the registry. The registry swap is atomic
— the complete `[]FlagDef` is assembled before calling `Registry.Swap()`.

The poller uses the same health-based exponential backoff as the existing
proxy mode health tracker — consecutive failures increase the poll interval
(2x, 4x, 8x capped), and a single successful response resets the backoff.

**2. fsnotify (monolithic mode only)**

Descriptor file changes trigger a re-parse and re-sync to DB. The registry
swap is gated on successful sync — if sync fails, the current registry is
preserved. On successful sync, definitions are reloaded from DB (same path
as the poller) and the registry is swapped.

This ensures the monolithic instance never serves definitions that differ
from what's committed to the database.

**3. Admin reload endpoint**

`POST /admin/reload-definitions` triggers an immediate refresh from DB.
Useful for:
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

**Principle: the database is authoritative.** When a type/layer conflict
is detected during sync, the DB's definition wins. The sync skips the
conflicting flag(s), logs a loud warning, and the server continues. The
server never refuses to start or reload due to a type/layer mismatch.

This is important for consistency: a VM crash or maintenance reboot should
not turn a running server into one that refuses to start. The lint checks
and CI pipeline are the enforcement gates for type/layer changes — the
server is not a second enforcement layer.

**Type/layer conflict handling during sync (monolithic mode):**

1. Sync encounters a flag where the descriptor's type or layer differs
   from the DB's type or layer
2. **Skip the conflicting upsert** — the DB row is unchanged
3. **Log a warning** naming the flag and the mismatch (e.g.,
   `flag "discovery/1": descriptor type STRING conflicts with DB type BOOL,
   keeping DB definition`), including how to fix it (two-deploy process)
4. After sync, **load definitions from DB** — the registry reflects
   committed DB state, including the DB's version of conflicting flags
5. Server starts / reload proceeds normally

Type/layer conflicts cannot occur in distributed mode — `pbflags-sync`
fails hard on conflicts, blocking the CI deploy before any server instance
sees bad definitions.

**Startup failures:**

| Mode | Failure | Behavior |
|------|---------|----------|
| Both | DB unreachable | **Fail to start.** Server cannot build registry. |
| Both | Schema version below minimum | **Fail to start.** Actionable error message. |
| Monolithic | Descriptor parse fails | **Fail to start.** No definitions to sync. |
| Monolithic | Sync type/layer conflict | **Start.** Skip conflicts, log warnings, load from DB. |

**Reload failures (after startup):**

| Mode | Failure | Behavior |
|------|---------|----------|
| Both | DB unreachable (poll) | **Keep current registry.** Back off via health tracker. |
| Monolithic | Descriptor parse fails (fsnotify) | **Keep current registry.** Log error. |
| Monolithic | Sync fails (fsnotify) | **Keep current registry.** Log error. |

**After startup (both modes):** The in-memory defaults registry is
unaffected by DB outages. Evaluation continues using cached state and
compiled defaults. The definition poller backs off using the existing
health tracker (2x, 4x, 8x capped).

**Note on `pbflags-sync`:** `pbflags-sync` continues to **fail hard** on
type/layer conflicts, blocking the CI deploy. This is the right behavior
for the distributed workflow — the conflict is caught before any server
instance sees bad definitions. The "DB is authoritative" principle applies
to the server at runtime, not to the sync tool in CI.

### Comparison of deployment modes

| Property | Monolithic | Distributed |
|----------|------------|-------------|
| Deploy complexity | Minimal (one binary, one file) | Standard CI/CD pipeline |
| Instance count | Single root instance | Any number of root evaluators |
| DB load from definitions | One instance reads/writes | Only root evaluators read (typically 1-3) |
| Definition reload | fsnotify (gated on sync) + DB poll | DB poll (60s default) + reload endpoint |
| Operational overhead | None — server handles everything | Run `pbflags-sync` in deploy pipeline |
| Descriptor file at runtime | Required | Not required (only in CI) |

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
- **Proxy evaluators** — completely unaffected (see "Two axes" above)

## Implementation plan

| Phase | Work |
|-------|------|
| **1. Embed migrations** | Embed goose migrations in both `pbflags-sync` and `pbflags-server`. `pbflags-sync` always runs them. `pbflags-server --monolithic --migrate` runs them at startup. |
| **2. Schema version check** | On startup (both modes), query `goose_db_version` for minimum required migration. Fail with actionable error if schema is missing or behind. |
| **3. DB definition loader** | New function `LoadDefinitionsFromDB(pool) ([]FlagDef, error)` that queries features+flags within a single read transaction, in batches, and returns the same `[]FlagDef` slice that `ParseDescriptorFile` returns. |
| **4. Extract sync package** | Extract sync logic from `pbflags-sync` into a shared package. Both `pbflags-sync` and `pbflags-server` (monolithic mode) call the same code. |
| **5. Explicit mode flags** | Add `--monolithic` and `--distributed` flags with validation: monolithic requires `--descriptors` and `--database`; distributed requires `--database`, rejects `--descriptors` and `--migrate`. |
| **6. Server startup paths** | Monolithic: migrate → sync → load from DB → registry. Distributed: schema check → load from DB → registry. Both call `LoadDefinitionsFromDB`. |
| **7. Definition poller** | Background goroutine that polls `GREATEST(MAX(flags.updated_at), MAX(features.updated_at))` with jitter (± 20%) and reloads within a read transaction. Reuses `Registry.Swap()` and the existing `HealthTracker` backoff logic. |
| **8. fsnotify gated on sync** | In monolithic mode, fsnotify triggers parse → sync → reload from DB → swap. Registry swap only happens if sync succeeds. |
| **9. Admin registry access** | Give the admin service a reference to the `Registry` instead of a static `[]FlagDef` slice. The admin's metadata enrichment reads from `registry.Load()` so it stays current after definition reloads. |
| **10. Reload endpoint** | `POST /admin/reload-definitions` triggers an immediate reload from DB. |
| **11. Equivalence tests** | Integration tests that sync a descriptor to DB via `pbflags-sync`, then compare `LoadDefinitionsFromDB()` output against `ParseDescriptorFile()` output. Must be identical: same flags, types, defaults, layers, metadata. |
| **12. Scale benchmark** | Benchmark with 5K+ flags: batched DB loading within a read transaction, registry construction, atomic swap under concurrent readers. Validates batching and swap correctness under load. |
| **13. Documentation** | Update README, getting-started guide, and deploy examples. Document monolithic and distributed modes with clear guidance on when to use each. |
