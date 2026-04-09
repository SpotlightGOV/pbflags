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

- **`pbflags-server`** — Feature flag evaluator (Connect RPC); optional combined mode serves the admin API and embedded web UI.
- **`pbflags-sync`** — Syncs flag definitions from `descriptors.pb` into PostgreSQL.
- **`protoc-gen-pbflags`** — `protoc` / `buf` plugin for Go and Java client codegen.

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

## Common commands

See the root **`Makefile`** and **`README.md`** for `make build`, `make generate`, and server startup examples.

