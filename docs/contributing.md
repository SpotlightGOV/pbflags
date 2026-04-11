# Contributing

## Prerequisites

- Go 1.26+ (see `go.mod`)
- [Docker](https://docs.docker.com/get-docker/) and Docker Compose (PostgreSQL for tests)
- [Buf CLI](https://buf.build/docs/installation) (for `make generate`)

### Playwright (E2E tests only)

Browser-based E2E tests require Playwright browsers. Install them once:

```bash
go tool playwright install --with-deps
```

## Getting started

```bash
# Install hooks and repo-local dev tooling
make setup

# Start the dev database
make dev-db

# Run the admin server with live asset reloading
make dev
```

This starts `pbflags-admin --standalone` against a local PostgreSQL on port 5433. The admin UI is at `http://localhost:9200`, the evaluator at `http://localhost:9201`. CSS and template changes take effect on browser refresh; Go changes require a restart.

## Running tests

### Unit and integration tests

```bash
go test -count=1 -p 1 ./...
```

The `-p 1` flag is important: several packages share the same database and running them in parallel can deadlock or flake. The `Makefile` `test` target runs `go test ./...` without `-p 1` — prefer the command above for a reliable full run.

Integration tests use testcontainers to start PostgreSQL automatically (requires Docker).

### E2E browser tests

```bash
make test-e2e

# Headed mode (visible browser with slowdown):
HEADED=1 make test-e2e
```

E2E tests are gated behind the `e2e` build tag so they don't run during `go test ./...`. On failure, Playwright traces are saved to `internal/e2e/testdata/traces/` — view them with `npx playwright show-trace <file>.zip`.

## Common commands

```bash
make help           # Show all targets
make build          # Build all binaries
make generate       # Regenerate protobuf code (builds codegen plugin first)
make docker         # Build Docker image
make install-codegen  # Install protoc-gen-pbflags to GOPATH/bin
```

## Database migrations

Migrations live in `db/migrations/` and use [goose](https://github.com/pressly/goose). `pbflags-sync` and `pbflags-admin --standalone` run them automatically on startup; `pbflags-admin` (normal) and `pbflags-evaluator` only check the schema version.

**Goose version table location:** the migration tracker table (`pbflags_goose_db_version`) lives in the `feature_flags` schema. Releases before this change created it in `public` — after upgrading you can safely `DROP TABLE IF EXISTS public.pbflags_goose_db_version;` once all services are on the new version. Existing migrations are idempotent, so goose will re-record them in the new table automatically.

**Backwards compatibility rule:** every migration must be compatible with the previous release's queries. During a production upgrade, `pbflags-sync` applies the new schema first, then admin and evaluator instances are rolled out — so the old code runs against the new schema during the rollout window.

- Add columns as nullable or with defaults.
- Add tables freely.
- Rename or drop columns across two releases (add new, then remove old).
- Never change column types in place.

## Admin web UI routes

Go 1.22+ `http.ServeMux` panics at registration if a `{name...}` wildcard segment is not the last segment of the pattern. The admin UI uses flag IDs containing `/` (`feature/field`), so wildcards are placed last:

- `POST /api/flags/state/{flagID...}`
- `POST /api/flags/overrides/{flagID...}`
- `DELETE /api/flags/overrides/entity/{entityID}/{flagID...}`

When adding new routes, keep multi-segment IDs in a trailing `{...}` segment only.

## Repository structure

```
pbflags/
├── proto/pbflags/          # Core proto definitions (options, types, services)
├── proto/example/          # Example feature flag definitions
├── gen/                    # Generated Go protobuf code
├── cmd/
│   ├── pbflags-admin/      # Control plane (admin API + UI + local evaluator)
│   ├── pbflags-evaluator/  # Read-only flag resolution service
│   ├── pbflags-sync/       # Schema migration + definition sync
│   ├── pbflags-lint/       # Pre-commit breaking change detector
│   └── protoc-gen-pbflags/ # Code generation plugin (Go, Java)
├── internal/
│   ├── evaluator/          # Evaluation engine, caching, health tracking
│   ├── admin/              # Admin API (flag management, audit log)
│   │   └── web/            # Embedded web UI (htmx dashboard)
│   ├── codegen/            # Code generators (Go, Java)
│   ├── e2e/                # Browser E2E tests (Playwright)
│   └── lint/               # Breaking change detection logic
├── clients/java/           # Java client library (Gradle)
├── clients/java/testing/   # Java test utilities (InMemoryFlagEvaluator, JUnit 5)
├── db/migrations/          # PostgreSQL schema (goose)
├── design-docs/            # Historical design documents
├── docs/                   # Operator and contributor documentation
└── docker/                 # Dockerfile and docker-compose
```

## Releasing

Releases are triggered by pushing a git tag matching `v*`. The GitHub Actions release workflow builds multi-platform binaries via GoReleaser, pushes a Docker image to GHCR, and creates a GitHub release.

### Branch strategy

All development happens on `main`. Releases follow a branching convention:

- **Minor/major releases** (`vX.Y.0`) are tagged on `main`. The release workflow automatically creates a `release/X.Y.0` branch from the tag.
- **Patch releases** (`vX.Y.Z`, Z>0) are tagged on the corresponding `release/X.Y.0` branch after cherry-picking fixes.

### Cutting a release

```bash
make release              # next minor from main, next patch from release branch
make release MAJOR=1      # next major from main
make release VERSION=v1.0.0  # explicit version
```

This runs lint, tests, and E2E tests, then generates release notes (if none exist), opens `$EDITOR` for review, and prompts for confirmation before tagging and pushing.

### Release notes

```bash
make release-notes                # auto-detect version from branch
make release-notes VERSION=v0.7.0 # explicit version
```

Generates notes to `docs/releasenotes/<VERSION>.md` via the Claude API, opens `$EDITOR` for review, and stages the file. You can prepare release notes ahead of time this way — `make release` will skip generation if the file already exists.

### What the CI release workflow does

After the tag is pushed, GitHub Actions:

1. Verifies the tag is on the correct branch (main for `.0`, release branch for patches)
2. Uses pre-committed release notes (or generates them)
3. Builds binaries for linux/macOS on amd64/arm64
4. Builds and pushes Docker image to `ghcr.io/spotlightgov/pbflags`
5. Pushes proto definitions to the Buf Schema Registry
6. Creates `release/X.Y.0` branch (for `.0` releases only)
7. Triggers Java client publishing to Maven Central
