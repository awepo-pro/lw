BINARY := lw
VERSION := 0.1.0-dev
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test lint check fixtures install release-snapshot clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/lw

test:
	go test ./...

lint:
	gofmt -l . && go vet ./...

check: lint test

fixtures:
	go test ./... -update

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/lw

clean:
	rm -f $(BINARY)
	rm -rf dist/

# Release engineering — the build leg only. `goreleaser release` (which tags,
# uploads and commits the tap) is run by hand by the maintainer; no target
# here publishes anything. Requires goreleaser v2:
#   go install github.com/goreleaser/goreleaser/v2@latest
release-snapshot:
	goreleaser build --snapshot --clean
