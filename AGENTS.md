# AGENTS.md

## Cursor Cloud specific instructions

### Overview

pbflags is a Go-based Protocol Buffer feature flag system. The main services are:

- **pbflags-server**: Evaluator server (Connect/gRPC API on `:9201`, optional admin web UI on `:9200`)
- **pbflags-sync**: One-shot CLI to seed flag definitions into PostgreSQL
- **protoc-gen-pbflags**: Protoc code generation plugin (Go, Java)

### Prerequisites

- **Go 1.26+** (managed by the Go toolchain directive in `go.mod`)
- **Docker** (needed for PostgreSQL)
- **buf CLI** (needed for codegen tests in `internal/codegen/`)

### Running PostgreSQL

```bash
sudo docker compose -f docker/docker-compose.yml up -d db
```

This starts PostgreSQL 18 on `localhost:5433` (user: `admin`, password: `admin`, database: `pbflags`). The init SQL at `db/migrations/001_schema.sql` is automatically applied.

### Running Tests

Integration tests share a single PostgreSQL database and can deadlock when packages run in parallel. Always use:

```bash
go test -count=1 -p 1 ./...
```

The `-p 1` flag serializes package execution to avoid cross-package DB contention. Without it, `internal/admin`, `internal/evaluator`, and `internal/integration` tests will intermittently fail with deadlock or FK-violation errors.

### Running the Server

Seed flag definitions first:

```bash
go run ./cmd/pbflags-sync \
  --database="postgres://admin:admin@localhost:5433/pbflags?sslmode=disable" \
  --descriptors=internal/evaluator/testdata/descriptors.pb
```

Start the evaluator (root mode, no admin UI):

```bash
go run ./cmd/pbflags-server \
  --database="postgres://admin:admin@localhost:5433/pbflags?sslmode=disable" \
  --descriptors=internal/evaluator/testdata/descriptors.pb \
  --listen=:9201
```

### Known Issue: Admin Web UI Panic

The `--admin=:9200` combined mode panics on startup in Go 1.22+ because `internal/admin/web/handler.go` uses `{flagID...}` wildcard patterns in the middle of URL paths (e.g., `POST /api/flags/{flagID...}/state`), which `net/http.ServeMux` only allows at the end. The evaluator API on `:9201` works correctly without the admin flag.

### Standard Commands

See `Makefile` for `build`, `test`, `generate`, `clean`, `docker`, and `install-codegen` targets. See `README.md` for full configuration and architecture documentation.
