.PHONY: generate build test clean docker dev dev-db release-notes

# Generate protobuf Go code from proto definitions.
generate:
	buf generate

# Build all Go binaries.
build:
	go build ./cmd/pbflags-server
	go build ./cmd/protoc-gen-pbflags

# Run Go tests.
test:
	go test ./...

# Remove build artifacts.
clean:
	rm -f pbflags-server protoc-gen-pbflags

# Build the Docker image.
docker:
	docker build -t pbflags-server -f docker/Dockerfile .

# Install the codegen plugin locally.
install-codegen:
	go install ./cmd/protoc-gen-pbflags

# Start only the database for local development.
dev-db:
	docker compose -f docker/docker-compose.yml up -d db

# Pre-generate release notes for review before tagging.
# Usage: make release-notes VERSION=v0.6.0
# Notes are saved to docs/releasenotes/<VERSION>.md. Edit and commit before releasing.
# Delete the file and re-run to regenerate.
release-notes:
ifndef VERSION
	$(error VERSION is required, e.g. make release-notes VERSION=v0.6.0)
endif
	@mkdir -p docs/releasenotes
	RELEASE_TAG=$(VERSION) .github/scripts/generate-release-notes.sh

# Run the server locally with live asset reloading.
# CSS/template changes take effect on browser refresh; Go changes need a restart.
dev: dev-db
	go run ./cmd/pbflags-server \
		--upgrade \
		--database=postgres://admin:admin@localhost:5433/pbflags?sslmode=disable \
		--descriptors=internal/evaluator/testdata/descriptors.pb \
		--listen=localhost:9201 \
		--admin=localhost:9200 \
		--env-name=local \
		--dev-assets=internal/admin/web
