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

## Documentation

Detailed docs are in the `docs/` directory. Key references for contributors:

- [docs/contributing.md](docs/contributing.md) — Dev setup, testing, releasing, migration rules
- [docs/deployment.md](docs/deployment.md) — Service topology and configuration
- [docs/philosophy.md](docs/philosophy.md) — Design principles, layers, evaluation precedence

## Quick reference

Runtime services: `pbflags-admin`, `pbflags-evaluator`, `pbflags-sync`. Build tools: `protoc-gen-pbflags`, `pbflags-lint`. See [docs/deployment.md](docs/deployment.md) for details.

Tests require PostgreSQL on port 5433 (`make dev-db`). Run the full suite with:

```bash
go test -count=1 -p 1 ./...
```

Use `-p 1` — several packages share the same database and will deadlock without it.

E2E browser tests (Playwright, gated behind `e2e` build tag):

```bash
make test-e2e
```

## Admin web UI routes

Go 1.22+ `http.ServeMux` panics if a `{name...}` wildcard is not the last segment. The admin UI uses flag IDs containing `/`, so wildcards are placed last:

- `POST /api/flags/state/{flagID...}`
- `POST /api/flags/overrides/{flagID...}`
- `DELETE /api/flags/overrides/entity/{entityID}/{flagID...}`

## Database migrations

Migrations in `db/migrations/` must be backwards-compatible with the previous release. See [docs/contributing.md](docs/contributing.md) for rules.

