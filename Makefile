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
	@echo "  release         Tag and push a release (VERSION=, MAJOR=1, or auto)"
	@echo "  release-notes   Generate release notes (VERSION= required)"
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

# Tag and push a release.
#   make release              — next minor from main, next patch from release branch
#   make release MAJOR=1      — next major from main
#   make release VERSION=v1.2.3 — explicit version
release:
ifdef VERSION
	git tag $(VERSION)
	git push origin $(VERSION)
else ifdef MAJOR
	.github/scripts/next-tag.sh --major --push
else
	.github/scripts/next-tag.sh --push
endif

# Pre-generate release notes for review before tagging.
# Usage: make release-notes VERSION=v0.6.0
# Notes are saved to docs/releasenotes/<VERSION>.md. Edit and commit before releasing.
# Delete the file and re-run to regenerate.
release-notes:
ifndef VERSION
	$(error VERSION is required, e.g. make release-notes VERSION=v0.6.0)
endif
	@mkdir -p docs/releasenotes
	RELEASE_TAG=$(VERSION) OUTPUT_FILE=docs/releasenotes/$(VERSION).md \
		.github/scripts/generate-release-notes.sh

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
