# Deployment

## Services

Three binaries, each with a distinct role and explicit database permission requirements:

| Binary | Role | DB permissions |
|---|---|---|
| `pbflags-sync` | Migrations + definition sync | DDL + R/W |
| `pbflags-admin` | Kill switches, dashboard UI, local evaluator | R/W (no DDL) |
| `pbflags-evaluator` | Read-only flag resolution | Readonly, or none if using an upstream evaluator |

For development, `pbflags-admin --standalone` runs all three roles in one process.

## Architecture

```
┌─────────────┐     ┌────────────────────┐     ┌────────────┐
│  Your App   │────▶│  pbflags-evaluator │────▶│ PostgreSQL │
│ (Go/Java)   │     │  (flag resolution) │     │  (readonly) │
└─────────────┘     └────────────────────┘     └────────────┘
  Generated            Three-tier cache:       ┌────────────┐
  type-safe            - Kill set (30s)     ┌──▶│ PostgreSQL │
  client               - Flag state (10m)  │   │   (R/W)    │
                       - Conditions (LRU)  │   └────────────┘
                                           │
┌─────────────┐     ┌────────────────────┐─┘
│  Operator   │────▶│  pbflags-admin     │
│  (browser)  │     │  (control plane)   │
└─────────────┘     └────────────────────┘
```

## Standalone

The simplest way to run pbflags — one binary, one command:

```bash
buf build proto -o descriptors.pb
```

```bash
pbflags-admin --standalone \
  --descriptors=descriptors.pb \
  --features=./features \
  --database=postgres://user:pass@localhost:5432/mydb?sslmode=disable \
  --env-name=local
```

On startup, standalone mode:

1. Runs pending schema migrations automatically
2. Checks for other standalone instances (warns but does not fail)
3. Parses `descriptors.pb` and syncs definitions to the database
4. Loads definitions into an in-memory registry
5. Watches the descriptor file for changes (re-syncs on change)
6. Polls the database for definition updates (60s default)
7. Starts the admin UI on `:9200` and the evaluator on `:9201`

**Single-instance only.** Running multiple standalone instances risks split-brain definition conflicts. A lease row in the database detects conflicts and logs a warning. For multiple instances, use the production topology below.

### Docker Compose

```bash
docker compose -f docker/docker-compose.yml up
```

This starts PostgreSQL + `pbflags-admin --standalone`.

## Production (multi-instance)

In production, the three roles run as separate processes with explicit DB permissions:

```bash
# 1. CI/CD pipeline (once per change to flag definitions — DDL + R/W):
pbflags-sync \
  --descriptors=descriptors.pb \
  --features=./features \
  --database=postgres://admin:pass@db:5432/flags \
  --sha=$(git rev-parse HEAD)

# 2. Control plane (one or more instances — R/W, no DDL):
pbflags-admin \
  --database=postgres://app:pass@db:5432/flags

# 3. Evaluators (any number — readonly):
pbflags-evaluator \
  --database=postgres://readonly:pass@db:5432/flags

# 4. Optional: upstream proxy evaluators for fan-out reduction (no DB):
pbflags-evaluator \
  --upstream=http://evaluator:9201
```

`pbflags-sync` runs migrations automatically before syncing definitions. Admin and evaluator instances poll the database for changes (default 60s, configurable via `--definition-poll-interval`).

## Admin Web UI

`pbflags-admin` serves an embedded read-only web dashboard, built with server-rendered HTML and htmx.

- **Dashboard**: Overview of all features and flags with condition counts and sync SHA badge showing config provenance
- **Flag Detail**: Per-flag view with a condition chain table (CEL expression → value), kill state, and recent audit history
- **Kill Switch**: The only runtime control — kill a flag to force the compiled default, or revive it to resume condition evaluation
- **Audit Log**: Filterable log of all state changes with actor attribution

The admin UI does not support editing flag values or conditions — those are defined in proto source and synced via `pbflags-sync`. The only mutation available is the kill switch.

The admin UI is available at `http://localhost:9200/` by default (configurable via `--listen` or `PBFLAGS_ADMIN`). The embedded evaluator listener is configured separately via `--evaluator-listen` or `PBFLAGS_LISTEN`.

### Security

- **CSRF protection**: All mutating requests (POST/DELETE) require a valid CSRF token via double-submit cookie pattern. htmx sends the token automatically.
- **Input validation**: Flag IDs are validated against the `feature_id/field_number` format before processing.
- **Internal network only**: The admin UI has no authentication. Deploy it behind a VPN, bastion, or internal network. Do not expose it to the public internet.

## Configuration

### @-file syntax

All pbflags CLI tools accept picocli-style `@file` references. This lets you
store flags in a file (one `--flag=value` per line) and reference it on the
command line:

```bash
# config.flags
--database=postgres://app:pass@db:5432/flags
--env-name=staging
--env-color=#FFA500
```

```bash
pbflags-admin @config.flags --standalone --descriptors=descriptors.pb
```

Flags from `@file` expand in place — later flags on the command line win.

Host-local overrides can be placed in `~/.config/pbflags/<binary>.flags`
(e.g. `~/.config/pbflags/pbflags-admin.flags`). These merge with last-wins
semantics, so you can override individual flags per host without editing
checked-in config files. The override directory can be changed via
`PBFLAGS_OVERRIDES_DIR`.

### Cache tuning

The evaluator uses a three-tier cache with configurable TTLs:

| Flag | Default | Description |
|---|---|---|
| `--cache-kill-ttl` | 30s | Kill-set poll interval |
| `--cache-flag-ttl` | 10m | Flag state (conditions + killed_at) hot-cache TTL |
| `--cache-condition-ttl` | 10m | Condition evaluation cache TTL (dimension-keyed LRU) |

The three tiers are:

1. **Kill-set cache** — polled on a short interval (`--cache-kill-ttl`, default 30s) so emergency shutoffs propagate quickly.
2. **Flag state cache** — caches conditions and `killed_at` per flag with a longer TTL (`--cache-flag-ttl`, default 10m).
3. **Condition evaluation cache** — dimension-keyed LRU that caches the result of CEL condition evaluation for a given flag + evaluation context combination (`--cache-condition-ttl`, default 10m).

#### Standard mode (default)

With the default TTLs, the evaluator caches flag state in a hot cache
(Ristretto) with jitter to prevent thundering herd. When an entry
expires, the evaluator returns the **stale value immediately** and
triggers a background refresh — no request ever blocks on a fetch after
initial warmup. The evaluation source will be `STALE` until the
background refresh completes, then `CONDITION` or `DEFAULT` on the next
call.

A background kill set poller runs every `--cache-kill-ttl` (default 30s)
to ensure killed flags take effect quickly.

#### Write-through mode (`--cache-flag-ttl=0`)

Setting `--cache-flag-ttl=0` (and/or `--cache-condition-ttl=0`) disables
the hot cache entirely. Every evaluation fetches from the database,
giving instant flag propagation. A stale fallback map is still populated
on each fetch as a safety net if the database becomes unavailable.

This mode is designed for small or standalone deployments where the
database is local and sub-millisecond fetch latency is acceptable.

When `--cache-flag-ttl` is less than or equal to `--cache-kill-ttl`, the
kill set poller is automatically disabled. Instead, the evaluator fetches
each flag's state and detects kills inline.

#### Evaluation sources

The `EvaluationSource` in the response tells consumers where the
resolved value came from:

| Source | Meaning |
|---|---|
| `CONDITION` | Fresh value from a matched condition in the chain |
| `STALE` | Stale value returned while background refresh is in flight (normal operation after TTL expiry) |
| `CACHED` | Last-resort stale value (database unreachable, no background refresh possible) |
| `KILLED` | Flag is killed — compiled default returned |
| `ARCHIVED` | Archived flag's last known value |
| `DEFAULT` | Compiled default (flag not found, no condition matched, or no conditions defined) |

In dashboards, a steady rate of `STALE` evaluations is normal -- it
means TTL-expired entries are being served while refreshes complete in
the background. A spike in `CACHED` evaluations indicates the database
may be unreachable.

### Environment variables

Environment variables override CLI flags:

| Variable | Used by | Equivalent flag | Notes |
|---|---|---|---|
| `PBFLAGS_DATABASE` | admin, evaluator, sync | `--database` | PostgreSQL connection string |
| `PBFLAGS_DESCRIPTORS` | admin standalone, sync | `--descriptors` | Path to `descriptors.pb` |
| `PBFLAGS_UPSTREAM` | evaluator | `--upstream` | Upstream evaluator URL in proxy mode |
| `PBFLAGS_ADMIN` | admin | `--listen` | Admin UI/API listen address, default `:9200` |
| `PBFLAGS_LISTEN` | admin, evaluator | `--evaluator-listen` on admin, `--listen` on evaluator | Evaluator listen address; default is `:9201` in `pbflags-admin`, `localhost:9201` in `pbflags-evaluator` |
| `PBFLAGS_ENV_NAME` | admin | `--env-name` | Environment label shown in admin UI |
| `PBFLAGS_ENV_COLOR` | admin | `--env-color` | Accent color for admin UI environment banner |
| `PBFLAGS_CACHE_KILL_TTL` | admin, evaluator | `--cache-kill-ttl` | Kill set poll interval (default `30s`) |
| `PBFLAGS_CACHE_FLAG_TTL` | admin, evaluator | `--cache-flag-ttl` | Flag state cache TTL (default `10m`, `0` for write-through) |
| `PBFLAGS_CACHE_CONDITION_TTL` | admin, evaluator | `--cache-condition-ttl` | Condition evaluation cache TTL (default `10m`, `0` for write-through) |

## Proto Definitions (BSR)

Proto definitions are published to the [Buf Schema Registry](https://buf.build/spotlightgov/pbflags). Consumers can depend on them directly:

```yaml
# buf.yaml
deps:
  - buf.build/spotlightgov/pbflags
```
