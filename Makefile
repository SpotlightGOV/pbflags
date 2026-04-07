.PHONY: generate build test clean docker

# Generate protobuf Go code from proto definitions.
generate:
	buf generate

# Build all Go binaries.
build:
	go build ./cmd/pbflags-server
	go build ./cmd/protoc-gen-pbflags

# Run Go tests (integration tests use namespaced DB rows; parallel packages are OK).
test:
	go test -count=1 ./...

# Remove build artifacts.
clean:
	rm -f pbflags-server protoc-gen-pbflags

# Build the Docker image.
docker:
	docker build -t pbflags-server -f docker/Dockerfile .

# Install the codegen plugin locally.
install-codegen:
	go install ./cmd/protoc-gen-pbflags
