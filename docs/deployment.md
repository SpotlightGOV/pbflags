# Deployment

## Services

Three binaries, each with a distinct role and explicit database permission requirements:

| Binary | Role | DB permissions |
|---|---|---|
| `pbflags-sync` | Migrations + definition sync | DDL + R/W |
| `pbflags-admin` | Flag management, UI, local evaluator | R/W (no DDL) |
| `pbflags-evaluator` | Read-only flag resolution | Readonly (or none if upstream) |

For development, `pbflags-admin --standalone` runs all three roles in one process.

## Architecture

```
┌─────────────┐     ┌────────────────────┐     ┌────────────┐
│  Your App   │────▶│  pbflags-evaluator │────▶│ PostgreSQL │
│ (Go/Java)   │     │  (flag resolution) │     │  (readonly) │
└─────────────┘     └────────────────────┘     └────────────┘
  Generated            Three-tier cache:       ┌────────────┐
  type-safe            - Kill set (30s)    ┌──▶│ PostgreSQL │
  client               - Global state (5m)│   │   (R/W)    │
                       - Overrides (5m LRU)│   └────────────┘
                                           │
┌─────────────┐     ┌────────────────────┐─┘
│  Operator   │────▶│  pbflags-admin     │
│  (browser)  │     │  (control plane)   │
└─────────────┘     └────────────────────┘
```

## Standalone

The simplest way to run pbflags — one binary, one command:

```bash
pbflags-admin --standalone \
  --descriptors=descriptors.pb \
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
# 1. CI/CD pipeline (once per deploy — DDL + R/W):
pbflags-sync \
  --descriptors=descriptors.pb \
  --database=postgres://admin:pass@db:5432/flags

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

`pbflags-admin` serves an embedded web dashboard for flag management, built with server-rendered HTML and htmx.

- **Dashboard**: Overview of all features and flags with inline state toggles (ENABLED/DEFAULT/KILLED)
- **Flag Detail**: Per-flag view with state/value editing, override management (layer-scoped flags), and recent audit history
- **Audit Log**: Filterable log of all state changes with actor attribution
- **Override Management**: Add and remove per-entity overrides for layer-scoped flags

The admin UI is available at `http://localhost:9200/` by default (configurable via `--listen` or `PBFLAGS_ADMIN`).

### Security

- **CSRF protection**: All mutating requests (POST/DELETE) require a valid CSRF token via double-submit cookie pattern. htmx sends the token automatically.
- **Input validation**: Flag IDs are validated against the `feature_id/field_number` format before processing.
- **Internal network only**: The admin UI has no authentication. Deploy it behind a VPN, bastion, or internal network. Do not expose it to the public internet.

## Configuration

Environment variables override CLI flags:

| Variable | Description |
|---|---|
| `PBFLAGS_DATABASE` | PostgreSQL connection string |
| `PBFLAGS_DESCRIPTORS` | Path to `descriptors.pb` (standalone and sync only) |
| `PBFLAGS_UPSTREAM` | Upstream evaluator URL (evaluator proxy mode) |
| `PBFLAGS_LISTEN` | Evaluator listen address (default: `localhost:9201`) |
| `PBFLAGS_ADMIN` | Admin listen address (default: `:9200`) |
| `PBFLAGS_ENV_NAME` | Environment label shown in admin UI |
| `PBFLAGS_ENV_COLOR` | Accent color for admin UI environment banner |

## Proto Definitions (BSR)

Proto definitions are published to the [Buf Schema Registry](https://buf.build/spotlightgov/pbflags). Consumers can depend on them directly:

```yaml
# buf.yaml
deps:
  - buf.build/spotlightgov/pbflags
```
