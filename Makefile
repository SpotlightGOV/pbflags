.PHONY: help generate build install test test-e2e lint fmt setup setup-beads clean docker dev dev-db dev-seed release-notes release

.DEFAULT_GOAL := help

## help: Show this help message.
help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "Development:"
	@echo "  dev             Start admin server locally with live asset reloading (standalone mode)"
	@echo "  dev-seed        Sync demo conditions into the running dev database (second terminal)"
	@echo "  dev-db          Start only the PostgreSQL container for local development"
	@echo ""
	@echo "Build:"
	@echo "  build           Build all Go binaries (admin, evaluator, sync, codegen plugin)"
	@echo "  generate        Regenerate protobuf Go code (builds codegen plugin first)"
	@echo "  install         Install CLI to GOPATH/bin (pbflags + pb alias)"
	@echo "  docker          Build the Docker image"
	@echo "  install-codegen Install protoc-gen-pbflags to GOPATH/bin"
	@echo ""
	@echo "Quality:"
	@echo "  fmt             Auto-format Go, Java, and proto files"
	@echo "  lint            Run all linters (go vet, staticcheck, buf lint)"
	@echo "  setup           Install pre-commit hooks and dev tooling"
	@echo "  setup-beads     Bootstrap a new local beads db"
	@echo ""
	@echo "Test:"
	@echo "  test            Run Go unit and integration tests"
	@echo "  test-e2e        Run browser E2E tests via Playwright (HEADED=1 for visible browser)"
	@echo "  test-harness    Run distribution and hardening tests (on-demand)"
	@echo "  bench           Run benchmarks and save results to benchdata/"
	@echo "  bench-compare   Compare latest benchmark against previous run (needs benchstat)"
	@echo ""
	@echo "Release:"
	@echo "  release         Lint, test, test-e2e, then tag and push (VERSION=, MAJOR=1, or auto)"
	@echo "                  Opens \$$EDITOR for release notes if none exist for the version"
	@echo "                  Set INTERACTIVE=false to skip editor and confirmation prompts"
	@echo "  release-notes   Generate release notes, open in \$$EDITOR, and git-add (VERSION= or auto)"
	@echo "                  Set INTERACTIVE=false to skip opening \$$EDITOR"
	@echo ""
	@echo "Cleanup:"
	@echo "  clean           Remove build artifacts"

# Generate protobuf Go code from proto definitions.
# Builds protoc-gen-pbflags first so buf can invoke it.
generate:
	go build -o $(shell go env GOPATH)/bin/protoc-gen-pbflags ./cmd/protoc-gen-pbflags
	buf generate

# Build all Go binaries.
build:
	go build ./cmd/pbflags
	go build ./cmd/pbflags-admin
	go build ./cmd/pbflags-evaluator
	go build ./cmd/protoc-gen-pbflags

# Install CLI binaries to GOPATH/bin and create the 'pb' alias.
install:
	go install ./cmd/pbflags
	ln -sf "$$(go env GOPATH)/bin/pbflags" "$$(go env GOPATH)/bin/pb"
	@echo "Installed: pbflags, pb (alias)"

# Run Go tests.
test:
	go test ./...

# Run browser E2E tests (requires Playwright: go tool playwright install --with-deps).
# Set HEADED=1 for visible browser with slowdown.
test-e2e:
	go test -tags e2e -count=1 -p 1 -v ./internal/e2e/

# Run distribution and hardening tests (gated behind "harness" build tag).
test-harness:
	go test -tags harness -count=1 -v ./internal/evaluator/

# Run benchmarks and save results to benchdata/ with a timestamped filename.
# Use COUNT=N to override iteration count (default 5 for benchstat stability).
bench:
	$(eval BENCH_FILE := benchdata/$(shell date +%Y%m%d-%H%M%S).txt)
	go test -tags harness -run='^$$' -bench=. -benchmem -count=$(or $(COUNT),5) ./internal/evaluator/ | tee $(BENCH_FILE)
	@echo ""
	@echo "Benchmark results saved to $(BENCH_FILE)"

# Compare the two most recent benchmark runs using benchstat.
bench-compare:
	$(eval LATEST := $(shell ls -t benchdata/*.txt 2>/dev/null | head -1))
	$(eval PREVIOUS := $(shell ls -t benchdata/*.txt 2>/dev/null | head -2 | tail -1))
	@if [ -z "$(LATEST)" ] || [ -z "$(PREVIOUS)" ] || [ "$(LATEST)" = "$(PREVIOUS)" ]; then \
		echo "Need at least two benchmark runs in benchdata/. Run 'make bench' twice."; \
		exit 1; \
	fi
	@echo "Comparing $(PREVIOUS) → $(LATEST)"
	go tool benchstat $(PREVIOUS) $(LATEST)

# Auto-format Go, proto, and Java source files.
fmt:
	go tool goimports -w $(shell find . -name '*.go' -not -path './gen/*' -not -path './clients/*' -not -path './vendor/*')
	buf format -w
	cd clients/java && ./gradlew spotlessApply

# Run all linters.
lint:
	go vet ./...
	go tool staticcheck ./...
	buf lint

# Install pre-commit hooks and required dev tools.
setup:
	go tool lefthook install
	@echo "Pre-commit hooks installed."
	@echo ""
	@echo "For E2E tests: go tool playwright install --with-deps"

# Set up beads in a new clone
setup-beads:
	@if [ -d .beads ]; then \
		echo ".beads already exists — skipping setup."; \
		exit 0; \
	fi; \
	cp -R .beads.template .beads; \
  chmod 700 .beads; \
	if bd dolt status 2>&1 | grep -q "not running"; then \
		echo "Bootstrapping beads."; \
		bd bootstrap --yes \
	else \
		echo "Dolt server already running."; \
	fi;

# Remove build artifacts.
clean:
	rm -f pbflags pbflags-admin pbflags-evaluator protoc-gen-pbflags

# Build the Docker image.
docker:
	docker build -t pbflags -f docker/Dockerfile .

# Install the codegen plugin locally.
install-codegen:
	go install ./cmd/protoc-gen-pbflags

# Start only the database for local development.
dev-db:
	docker compose -f docker/docker-compose.yml up -d db

# Full release pipeline: lint, test, test-e2e, then generate/review release
# notes and tag.
#   make release              — next minor from main, next patch from release branch
#   make release MAJOR=1      — next major from main
#   make release VERSION=v1.2.3 — explicit version
release:
ifndef VERSION
	$(eval RELEASE_TAG := $(shell .github/scripts/next-tag.sh $(if $(MAJOR),--major)))
else
	$(eval RELEASE_TAG := $(VERSION))
endif
	$(MAKE) lint
	$(MAKE) test
	$(MAKE) test-e2e
	INTERACTIVE=$(or $(INTERACTIVE),true) .github/scripts/release.sh $(RELEASE_TAG)

# Generate release notes, open in $EDITOR, and stage for commit.
# Auto-detects version from branch (same as make release). Override with VERSION=.
# Set INTERACTIVE=false to skip opening $EDITOR.
#   make release-notes                        — auto-detect version
#   make release-notes VERSION=v0.6.0         — explicit version
#   make release-notes INTERACTIVE=false       — generate without opening editor
release-notes:
ifndef VERSION
	$(eval RELEASE_TAG := $(shell .github/scripts/next-tag.sh $(if $(MAJOR),--major)))
else
	$(eval RELEASE_TAG := $(VERSION))
endif
	@NOTES="docs/releasenotes/$(RELEASE_TAG).md"; \
	if [ -f "$$NOTES" ]; then \
		echo "Release notes already exist: $$NOTES"; \
		echo "Delete the file and re-run to regenerate."; \
		exit 1; \
	fi; \
	mkdir -p docs/releasenotes; \
	RELEASE_TAG=$(RELEASE_TAG) OUTPUT_FILE=$$NOTES \
		.github/scripts/generate-release-notes.sh; \
	echo ""; \
	if [ "$(or $(INTERACTIVE),true)" = "false" ]; then \
		echo "Release notes generated: $$NOTES"; \
		echo "Edit the file, then run 'make release INTERACTIVE=false' to finish the release."; \
	else \
		echo "Opening release notes for review..."; \
		$${EDITOR:-vi} "$$NOTES"; \
		echo "Commit when ready, or run 'make release' to finish the release."; \
	fi; \
	git add "$$NOTES"; \
	echo "Release notes staged: $$NOTES"

# Build descriptors.pb from proto sources for local development.
dev/descriptors.pb: $(shell find proto -name '*.proto' -type f)
	buf build proto -o dev/descriptors.pb

# Seed the running dev database with demo flag conditions via the sync binary.
# Call from a second terminal after `make dev` is running.
dev-seed: dev/descriptors.pb
	go run ./cmd/pbflags sync \
		--database=postgres://admin:admin@localhost:9202/pbflags?sslmode=disable \
		--descriptors=dev/descriptors.pb \
		--features=dev/features
	psql postgres://admin:admin@localhost:9202/pbflags?sslmode=disable < dev/seed-launches.sql
	PBFLAGS_DATABASE=postgres://admin:admin@localhost:9202/pbflags?sslmode=disable \
		go run dev/seed-overrides.go
	@echo "Demo data synced (flags + launch states + overrides). Refresh the admin UI."

# Run the admin server locally with live asset reloading (standalone mode).
# CSS/template changes take effect on browser refresh; Go changes need a restart.
# Uses dev/descriptors.pb (built from proto/) so conditions can be synced.
#
# Clears any stale sync lock left over from prior dev sessions: the admin
# server triggers an initial sync on startup, which fails if the lock is
# held — and `pb unlock` can't help here because it goes through the admin
# API that won't come up. Delete the row directly so `make dev` is always
# self-recoverable.
dev: dev-db dev/descriptors.pb
	@psql postgres://admin:admin@localhost:9202/pbflags?sslmode=disable \
		-c "DELETE FROM feature_flags.sync_lock WHERE id = 1" >/dev/null 2>&1 || true
	go run ./cmd/pbflags-admin \
		--standalone \
		--database=postgres://admin:admin@localhost:9202/pbflags?sslmode=disable \
		--descriptors=dev/descriptors.pb \
		--features=dev/features \
		--evaluator-listen=localhost:9201 \
		--listen=localhost:9200 \
		--env-name=local \
		--allow-runtime-overrides \
		--dev-assets=internal/admin/web
