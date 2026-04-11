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

`docs/` contains as-built documentation and release notes. `research/` contains design research and explorations.

Key references for contributors:

- [docs/contributing.md](docs/contributing.md) — Dev setup, testing, releasing, migration rules
- [docs/deployment.md](docs/deployment.md) — Service topology and configuration
- [docs/philosophy.md](docs/philosophy.md) — Design principles, layers, evaluation precedence
- [docs/agent-setup.md](docs/agent-setup.md) — Consumer integration guide for AI agents; use this when wiring pbflags into another repo rather than changing pbflags itself

## Quick reference

Runtime services: `pbflags-admin`, `pbflags-evaluator`, `pbflags-sync`. Build tools: `protoc-gen-pbflags`, `pbflags-lint`. See [docs/deployment.md](docs/deployment.md) for details.

All Go dev tools (lefthook, staticcheck, goimports, Playwright CLI) are declared as `tool` dependencies in `go.mod` and invoked via `go tool <name>` — no separate install step needed.

## Environment setup

After cloning, install the pre-commit hooks:

```bash
make setup
```

This registers [lefthook](https://github.com/evilmartians/lefthook) git hooks that auto-format and lint on every commit. The hooks run:

1. **Formatters** (auto-fix, re-staged): `goimports`, `buf format`, Spotless/google-java-format
2. **Linters** (check-only): `go vet`, `staticcheck`, `buf lint`, `go mod tidy` drift check

You can also run these manually:

```bash
make fmt    # auto-format all Go, proto, and Java source
make lint   # run all linters
```

Skip hooks for a one-off commit with `git commit --no-verify`.

## Tests

Tests use testcontainers to start PostgreSQL automatically (requires Docker). Run the full suite with:

```bash
go test -count=1 -p 1 ./...
```

Use `-p 1` — several packages share the same database and will deadlock without it.

E2E browser tests (Playwright, gated behind `e2e` build tag):

```bash
go tool playwright install --with-deps   # one-time browser install
make test-e2e                             # run headless
HEADED=1 make test-e2e                    # visible browser with slowdown
```

On failure, Playwright traces are saved to `internal/e2e/testdata/traces/` — open with `npx playwright show-trace <file>.zip`.

## Admin web UI routes

Go 1.22+ `http.ServeMux` panics if a `{name...}` wildcard is not the last segment. The admin UI uses flag IDs containing `/`, so wildcards are placed last:

- `POST /api/flags/state/{flagID...}`
- `POST /api/flags/overrides/{flagID...}`
- `DELETE /api/flags/overrides/entity/{entityID}/{flagID...}`

## Database migrations

Migrations in `db/migrations/` must be backwards-compatible with the previous release. See [docs/contributing.md](docs/contributing.md) for rules.
