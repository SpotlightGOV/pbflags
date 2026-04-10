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

Both models build the in-memory defaults registry from the database. The
difference is who writes definitions to the database.

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
2. Parse `descriptors.pb` and sync definitions to DB
3. Load definitions from DB into in-memory defaults registry
4. Start serving (evaluator + admin UI)

On fsnotify (descriptor file changes):
1. Re-parse `descriptors.pb` and re-sync to DB
2. Reload definitions from DB, swap registry

One binary, one flag file, one database. No CI pipeline, no separate sync
job, no cron.

**Multi-instance with monolithic mode:** When running multiple server
instances with `--descriptors` (e.g., behind a load balancer), use
`--no-migrate` and `--no-sync` on all but one instance to avoid redundant
work at startup. The designated "leader" instance runs migrations and sync;
the others just load definitions from DB.

```
# Leader instance — runs migrations and sync
pbflags-server --database=postgres://... --descriptors=descriptors.pb --admin=:8080

# Additional instances — read-only, skip migrate and sync
pbflags-server --database=postgres://... --descriptors=descriptors.pb --no-migrate --no-sync
```

Note: `--no-migrate` and `--no-sync` only apply when `--descriptors` is
provided. Without `--descriptors`, the server never writes to the database
for definitions — it only reads.

| Flag | Effect |
|------|--------|
| `--no-migrate` | Skip schema migrations at startup. Server fails if schema is missing. |
| `--no-sync` | Skip definition sync at startup and on fsnotify. Server loads definitions from DB only. |
| Both | Server behaves like distributed mode but still watches the descriptor file for... nothing. Equivalent to omitting `--descriptors`. |

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
| Server startup | Parse `descriptors.pb` → in-memory defaults | Migrate → sync → load DB → in-memory defaults | Load DB → in-memory defaults |
| `--descriptors` flag | Required | Provided (enables sync + fsnotify) | Omitted (DB-only) |
| Who runs migrations | Manual `goose up` | Server at startup | `pbflags-sync` |
| Who syncs definitions | Manual `pbflags-sync` | Server at startup + fsnotify | `pbflags-sync` in CI/CD |
| Definition reload | fsnotify on descriptor file | fsnotify + DB poll | DB poll + reload endpoint |
| Descriptor file at runtime | Required | Required | Not required |
| Best for | — | Single server, VM, demo | Multi-instance, CI/CD, production |

## Design

### Server startup: load defaults from DB

In both deployment models, the server ultimately loads definitions from the
database to build its in-memory defaults registry:

```sql
SELECT f.feature_id, f.display_name, f.description, f.owner,
       fl.flag_id, fl.field_number, fl.display_name, fl.flag_type,
       fl.layer, fl.description, fl.default_value
FROM feature_flags.features f
JOIN feature_flags.flags fl ON fl.feature_id = f.feature_id
WHERE fl.archived_at IS NULL
ORDER BY f.feature_id, fl.field_number
```

The result is mapped to `[]evaluator.FlagDef` — the same struct that
`ParseDescriptorFile()` returns today. From there, `NewDefaults(defs)` and
`NewRegistry(defaults)` work identically. The evaluator, admin API, and
reload mechanism see no difference.

In monolithic mode, the server first runs migrations and syncs definitions
from the descriptor to the database (unless `--no-migrate` / `--no-sync`),
then loads from DB. In distributed mode, the server just loads from DB
directly — `pbflags-sync` has already populated it.

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

If the timestamp is newer than the last load, re-run the full definition
query and swap the registry. This is simple, stateless, and works with any
Postgres deployment. The poll is a single indexed query returning one row —
negligible overhead.

The poller uses the same health-based exponential backoff as the existing
proxy mode health tracker — consecutive failures increase the poll interval
(2x, 4x, 8x capped), and a single successful response resets the backoff.

**2. fsnotify (monolithic mode, when `--descriptors` provided)**

Same mechanism as today. Descriptor file changes trigger a re-parse, re-sync
to DB, and registry swap. The DB poll also runs, so other instances (if any)
pick up the change within the poll interval.

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

### Fallback and resilience

**DB unreachable at startup:** The server fails to start. This is the same
behavior as today when `descriptors.pb` is missing or unparseable — the
server cannot serve without knowing what flags exist.

**DB unreachable after startup:** The in-memory defaults registry is
unaffected. Evaluation continues using cached state and compiled defaults,
same as today. The definition poller backs off using the health tracker.

**Monolithic mode, descriptor parse failure:** Same as today — the server
logs an error and continues serving with the current registry. The failed
parse does not sync to the database, so other instances are unaffected.

### Comparison of deployment models

| Property | Monolithic | Distributed |
|----------|------------|-------------|
| Deploy complexity | Minimal (one binary, one file) | Standard CI/CD pipeline |
| Scaling | Single instance (or leader + followers) | Any number of instances |
| Definition reload | Instant (fsnotify) + DB poll | DB poll (60s default) + reload endpoint |
| Operational overhead | None — server handles everything | Run `pbflags-sync` in deploy pipeline |
| Descriptor file at runtime | Required on server | Not required (only in CI) |

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

## Implementation plan

| Phase | Work |
|-------|------|
| **1. Embed migrations** | Embed goose migrations in both `pbflags-sync` and `pbflags-server`. Run pending migrations automatically (skipped with `--no-migrate`). |
| **2. DB definition loader** | New function `LoadDefinitionsFromDB(pool) ([]FlagDef, error)` that queries features+flags and returns the same `[]FlagDef` slice that `ParseDescriptorFile` returns. |
| **3. Embed sync in server** | Extract sync logic from `pbflags-sync` into a shared package. Call it from the server on startup and fsnotify when `--descriptors` is provided (skipped with `--no-sync`). |
| **4. Server startup modes** | When `--descriptors` is provided: migrate → sync → load from DB. When omitted: load from DB only. Both paths build the registry identically. |
| **5. Definition poller** | Background goroutine that polls `MAX(updated_at)` with jitter (± 20%) and swaps the registry when definitions change. Reuses `Registry.Swap()` and the existing `HealthTracker` backoff logic. |
| **6. Admin registry access** | Give the admin service a reference to the `Registry` (or a reload callback) instead of a static `[]FlagDef` slice. The admin's metadata enrichment reads from `registry.Load()` so it stays current after definition reloads. |
| **7. Reload endpoint** | `POST /admin/reload-definitions` triggers an immediate definition refresh. |
| **8. Documentation** | Update README, getting-started guide, and deploy examples. Document both deployment models with clear guidance on when to use each. |

## Open questions

1. **Proxy mode.** Proxy evaluators don't have DB access or descriptors.
   They receive typed `FlagValue` responses from upstream and cache them.
   No change is needed — they don't need definitions. Confirming this is
   still true.
