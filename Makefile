.PHONY: generate build test clean docker dev dev-db release-notes release

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
