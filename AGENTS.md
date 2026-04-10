# Agent / contributor notes (pbflags)

<!-- BEGIN BEADS INTEGRATION v:1 profile:minimal hash:ca08a54f -->
## Beads Issue Tracker

This project uses **bd (beads)** for issue tracking. Run `bd prime` to see full workflow context and commands.

### Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --claim  # Claim work
bd close <id>         # Complete work
```

### Rules

- Use `bd` for ALL task tracking — do NOT use TodoWrite, TaskCreate, or markdown TODO lists
- Run `bd prime` for detailed command reference and session close protocol
- Use `bd remember` for persistent knowledge — do NOT use MEMORY.md files

## Session Completion

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd dolt push
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds
<!-- END BEADS INTEGRATION -->

## Services and binaries

Three runtime services, each with a distinct role and explicit DB permission requirements:

- **`pbflags-admin`** — Flag management control plane: admin API, web UI, and a local evaluator interface. Requires R/W database access (no DDL). With `--standalone`, runs all three roles in one process (including migrations and sync). Requires DDL+R/W in standalone mode.
- **`pbflags-evaluator`** — Read-only flag resolution service. Requires either `--database` (readonly) or `--upstream` (proxy to another evaluator). Does not need descriptors.
- **`pbflags-sync`** — Runs schema migrations and syncs flag definitions from `descriptors.pb` into PostgreSQL. Requires DDL+R/W. Runs once per deploy in CI/CD pipelines.

Build-time tools:

- **`protoc-gen-pbflags`** — `protoc` / `buf` plugin for Go and Java client codegen.
- **`pbflags-lint`** — Pre-commit breaking change detector.

## Deployment topologies

### Standalone (development / single instance)

```bash
pbflags-admin --standalone \
  --descriptors=descriptors.pb \
  --database=postgres://... \
  --env-name=local
```

Runs all three roles (admin + evaluator + sync) in one process. Auto-migrates, syncs definitions, watches descriptor file. A lease row warns if another standalone instance is active.

### Production (multi-instance)

```bash
# CI/CD pipeline (once per deploy):
pbflags-sync --descriptors=descriptors.pb --database=postgres://...

# Control plane (one or more instances):
pbflags-admin --database=postgres://...

# Evaluators (any number, as sidecars or in a hierarchy):
pbflags-evaluator --database=postgres://...

# Optional: upstream proxy evaluators for fan-out reduction:
pbflags-evaluator --upstream=http://evaluator:9201
```

`pbflags-sync` is the single writer for definitions. Admin instances serve the UI and manage flag state (R/W). Evaluator instances resolve flags (readonly). All poll the DB for changes.

## Prerequisites

- Go 1.26+ (see `go.mod`)
- [Docker](https://docs.docker.com/get-docker/) and Docker Compose (for PostgreSQL in local integration tests)
- [Buf CLI](https://buf.build/docs/installation) (for `make generate`)

## PostgreSQL for integration tests

Integration tests expect PostgreSQL on port **5433** (see `internal/integration/service_test.go` and `internal/evaluator/integration_test.go`). Start the stack from the repo root:

```bash
docker compose -f docker/docker-compose.yml up -d
```

## Running tests

**Use parallel package isolation for the full suite:** several packages share the same database. Running them in parallel can deadlock or flake.

```bash
go test -count=1 -p 1 ./...
```

The `Makefile` `test` target runs `go test ./...` without `-p 1`; prefer the command above for a reliable full run locally and in agents.

## Admin web UI routes and `http.ServeMux`

Go 1.22+ `http.ServeMux` **panics at registration** if a `{name...}` wildcard segment is not the **last** segment of the pattern (for example, `/api/flags/{flagID...}/state` is invalid).

The admin UI uses URLs where the flag id is `feature/field` (contains `/`). Wildcards are therefore placed **last**, for example:

- `POST /api/flags/state/{flagID...}`
- `POST /api/flags/overrides/{flagID...}`
- `DELETE /api/flags/overrides/entity/{entityID}/{flagID...}`

When adding new routes, keep multi-segment ids in a trailing `{...}` segment only.

## Database migrations

Migrations live in `db/migrations/` and are applied by goose. `pbflags-sync` and `pbflags-admin --standalone` run them automatically on startup; `pbflags-admin` (normal) and `pbflags-evaluator` only check the schema version.

**Backwards compatibility rule:** every migration must be compatible with the previous release's queries. During a production rollout, `pbflags-sync` applies the new schema first, then admin and evaluator instances are updated — so the old code runs against the new schema during the rollout window. Concretely:

- Add columns as nullable or with defaults.
- Add tables freely.
- Rename or drop columns across two releases (add new, then remove old).
- Never change column types in place.

## Common commands

See the root **`Makefile`** and **`README.md`** for `make build`, `make generate`, and server startup examples.

