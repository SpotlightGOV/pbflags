.PHONY: help generate build test test-e2e lint fmt setup clean docker dev dev-db release-notes release

.DEFAULT_GOAL := help

## help: Show this help message.
help:
	@echo "Usage: make <target>"
	@echo ""
	@echo "Development:"
	@echo "  dev             Start admin server locally with live asset reloading (standalone mode)"
	@echo "  dev-db          Start only the PostgreSQL container for local development"
	@echo ""
	@echo "Build:"
	@echo "  build           Build all Go binaries (admin, evaluator, sync, codegen plugin)"
	@echo "  generate        Regenerate protobuf Go code (builds codegen plugin first)"
	@echo "  docker          Build the Docker image"
	@echo "  install-codegen Install protoc-gen-pbflags to GOPATH/bin"
	@echo ""
	@echo "Quality:"
	@echo "  fmt             Auto-format Go, Java, and proto files"
	@echo "  lint            Run all linters (go vet, staticcheck, buf lint)"
	@echo "  setup           Install pre-commit hooks and dev tooling"
	@echo ""
	@echo "Test:"
	@echo "  test            Run Go unit and integration tests"
	@echo "  test-e2e        Run browser E2E tests via Playwright (HEADED=1 for visible browser)"
	@echo ""
	@echo "Release:"
	@echo "  release         Lint, test, test-e2e, then tag and push (VERSION=, MAJOR=1, or auto)"
	@echo "                  Opens \$$EDITOR for release notes if none exist for the version"
	@echo "  release-notes   Generate release notes, open in \$$EDITOR, and git-add (VERSION= or auto)"
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
	go build ./cmd/pbflags-admin
	go build ./cmd/pbflags-evaluator
	go build ./cmd/pbflags-sync
	go build ./cmd/protoc-gen-pbflags

# Run Go tests.
test:
	go test ./...

# Run browser E2E tests (requires Playwright: go tool playwright install --with-deps).
# Set HEADED=1 for visible browser with slowdown.
test-e2e:
	go test -tags e2e -count=1 -p 1 -v ./internal/e2e/

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

# Remove build artifacts.
clean:
	rm -f pbflags-admin pbflags-evaluator pbflags-sync protoc-gen-pbflags

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
	.github/scripts/release.sh $(RELEASE_TAG)

# Generate release notes, open in $EDITOR, and stage for commit.
# Auto-detects version from branch (same as make release). Override with VERSION=.
#   make release-notes                — auto-detect version
#   make release-notes VERSION=v0.6.0 — explicit version
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
	echo "Opening release notes for review..."; \
	$${EDITOR:-vi} "$$NOTES"; \
	git add "$$NOTES"; \
	echo "Release notes staged: $$NOTES"; \
	echo "Commit when ready, or run 'make release' to finish the release."

# Run the admin server locally with live asset reloading (standalone mode).
# CSS/template changes take effect on browser refresh; Go changes need a restart.
dev: dev-db
	go run ./cmd/pbflags-admin \
		--standalone \
		--database=postgres://admin:admin@localhost:5433/pbflags?sslmode=disable \
		--descriptors=internal/evaluator/testdata/descriptors.pb \
		--evaluator-listen=localhost:9201 \
		--listen=localhost:9200 \
		--env-name=local \
		--dev-assets=internal/admin/web
