BINARY := lw
VERSION := 0.1.0-dev
LDFLAGS := -X main.version=$(VERSION)

.PHONY: build test lint check fixtures install clean

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
