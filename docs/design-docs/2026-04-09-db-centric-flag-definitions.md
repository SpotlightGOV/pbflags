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
> definitions. `pbflags-sync` becomes the deploy-time command (like running
> migrations), and the server loads definitions from the database — no
> descriptor file needed at runtime.

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
is unreachable, every flag returns its compiled default.

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
runtime state. The server loads definitions from the database at startup,
eliminating the requirement for `descriptors.pb` at runtime.

### New deploy workflow

```
1. Run migrations          goose up
2. Sync flag definitions   pbflags-sync --descriptors=descriptors.pb --database=postgres://...
3. Start/restart server    pbflags-server --database=postgres://...
```

Steps 1 and 2 run in CI/CD alongside each other — same place, same
credentials, same descriptor artifact from the build. Step 3 is the
application deploy. The server needs only a database connection string.

### What changes

| Concern | Current | Proposed |
|---------|---------|----------|
| Server startup | Parse `descriptors.pb` → in-memory defaults | Query DB → in-memory defaults |
| `--descriptors` flag | Required | Optional (enables descriptor-mode for backward compat) |
| Definition source of truth | `descriptors.pb` file on disk | `feature_flags.features` + `feature_flags.flags` tables |
| `pbflags-sync` | Separate deploy step, easy to forget | Same step as migrations, single deploy artifact |
| Admin UI flag list | Queries DB (already works) | No change |
| Hot-reload | fsnotify on descriptor file | Poll DB or reload endpoint (see below) |
| Descriptor file at runtime | Required on every evaluator instance | Not required |

## Design

### Server startup: load defaults from DB

On startup in root mode, if `--descriptors` is not provided, the server
queries the database for all non-archived flag definitions:

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
descriptor watcher see no difference.

If `--descriptors` IS provided, the current behavior is preserved: parse
the file, build the registry from it. This is the backward-compatible path
for deployments that already have file delivery infrastructure.

### `pbflags-sync` remains the definition push mechanism

`pbflags-sync` already does exactly what's needed:

1. Parse `descriptors.pb`
2. Upsert features and flags (idempotent `ON CONFLICT DO UPDATE`)
3. Archive flags removed from the descriptor
4. Never touch `state` or `value` columns (runtime state is preserved)

No changes to `pbflags-sync` are required. Its role shifts from "thing you
might forget to run" to "the way you deploy flag definitions" — same as
`goose up` for schema migrations.

### Definition reload

When flag definitions change in the database (via `pbflags-sync`), the
server needs to refresh its in-memory defaults registry. Three mechanisms,
in order of recommendation:

**1. Poll `updated_at` (recommended default)**

The server polls for definition changes on an interval (default 60s,
configurable via `--definition-poll-interval`):

```sql
SELECT MAX(updated_at) FROM feature_flags.flags
```

If the timestamp is newer than the last load, re-run the full definition
query and swap the registry. This is simple, stateless, and works with
any Postgres deployment. The poll is a single indexed query returning one
row — negligible overhead.

**2. Admin reload endpoint**

`POST /admin/reload-definitions` triggers an immediate refresh. Useful for:
- CI/CD pipelines that run `pbflags-sync` then poke the server
- Manual recovery
- Environments where polling is undesirable

**3. LISTEN/NOTIFY (optional, future)**

`pbflags-sync` could issue `NOTIFY flag_definitions_changed` after its
transaction commits. The server subscribes with `LISTEN`. This gives
near-instant propagation without polling. Deferred — the polling approach
is sufficient for most deployments and doesn't require a persistent
listener connection.

### Fallback and resilience

**DB unreachable at startup:** If the server cannot load definitions from
the database, it fails to start. This is the same behavior as today when
`descriptors.pb` is missing or unparseable — the server cannot serve
without knowing what flags exist.

**DB unreachable after startup:** The in-memory defaults registry is
unaffected. Evaluation continues using cached state and compiled defaults,
same as today. The definition poll simply retries on the next interval.

**Descriptor mode (`--descriptors` provided):** Full backward compatibility.
The descriptor file is parsed, the registry is built from it, fsnotify
hot-reload works. The database is still used for runtime state but NOT for
definitions. This mode exists for deployments that want the extra resilience
of a local descriptor file — the evaluator can cold-start without a database
connection.

### Comparison to descriptor-centric model

| Property | Descriptor-centric | DB-centric |
|----------|--------------------|------------|
| Cold-start without DB | Yes (descriptor provides defaults) | No (DB required) |
| Cold-start without descriptor file | No | Yes |
| Deploy steps | Deliver descriptor + run sync + start server | Run sync + start server |
| Hot-reload latency | Instant (fsnotify) | Poll interval (default 60s) |
| Admin UI shows new flags | Only after sync | Immediately after sync |
| Operational complexity | File delivery infrastructure required | Standard DB deploy pipeline |
| Definition source of truth | Descriptor file | Database |

The DB-centric model trades cold-start-without-DB resilience (a rare
scenario — if the DB is down at deploy time, nothing else works either) for
a dramatically simpler operational model.

## What does NOT change

- **Proto definitions** — flags are still defined in proto, compiled to
  `descriptors.pb`, and used by `pbflags-sync` and `protoc-gen-pbflags`
- **`pbflags-sync` interface** — same binary, same flags, same behavior
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
| **1. DB definition loader** | New function `LoadDefinitionsFromDB(pool) ([]FlagDef, error)` that queries features+flags and returns the same `[]FlagDef` slice that `ParseDescriptorFile` returns. |
| **2. Server startup** | When `--descriptors` is empty, call `LoadDefinitionsFromDB` instead of `ParseDescriptorFile`. Build registry identically. |
| **3. Definition poller** | Background goroutine that polls `MAX(updated_at)` and swaps the registry when definitions change. Reuses the existing `Registry.Swap()` mechanism. |
| **4. Reload endpoint** | `POST /admin/reload-definitions` triggers an immediate definition refresh. |
| **5. Documentation** | Update README, getting-started guide, and deploy examples to show the DB-centric workflow as the default. Document `--descriptors` as an optional resilience mode. |

## Open questions

1. **Should `pbflags-sync` auto-run migrations?** If sync is already in the
   deploy pipeline next to `goose up`, should it just handle schema
   migrations too? This would reduce the deploy to a single command:
   `pbflags-sync --descriptors=... --database=... --migrate`. Deferred —
   easy to add later without architectural impact.

2. **Should the definition poll also refresh the admin's descriptor map?**
   Currently the admin receives `[]FlagDef` once at startup and caches it
   in a map. If definitions change at runtime, the admin's metadata
   enrichment (supported values, feature owner, etc.) would be stale until
   restart. The fix is straightforward: give the admin a reference to the
   registry (or a reload callback) instead of a static slice.

3. **Proxy mode.** Proxy evaluators don't have DB access or descriptors.
   They receive typed `FlagValue` responses from upstream and cache them.
   No change is needed — they don't need definitions. Confirming this is
   still true.
