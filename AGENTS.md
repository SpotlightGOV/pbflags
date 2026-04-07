# Agent / contributor notes (pbflags)

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

Integration tests in `internal/admin`, `internal/evaluator`, and `internal/integration` share one PostgreSQL database. Each test generates a unique prefix via `internal/integrationtest.Prefix`, builds `feature_id` / `flag_id` from that prefix (`integrationtest.Feature` / `integrationtest.Flag`), and teardown deletes only rows under that prefix (`integrationtest.CleanupFeatureTree` / `RegisterCleanup`). The full suite is safe with default package parallelism:

```bash
go test -count=1 ./...
```

When debugging a single package, you can still use `-p 1` to reduce noise from concurrent packages.

## Admin web UI routes and `http.ServeMux`

Go 1.22+ `http.ServeMux` **panics at registration** if a `{name...}` wildcard segment is not the **last** segment of the pattern (for example, `/api/flags/{flagID...}/state` is invalid).

The admin UI uses URLs where the flag id is `feature/field` (contains `/`). Wildcards are therefore placed **last**, for example:

- `POST /api/flags/state/{flagID...}`
- `POST /api/flags/overrides/{flagID...}`
- `DELETE /api/flags/overrides/entity/{entityID}/{flagID...}`

When adding new routes, keep multi-segment ids in a trailing `{...}` segment only.

## Common commands

See the root **`Makefile`** and **`README.md`** for `make build`, `make generate`, and server startup examples.
