BINARY := lw
VERSION := 0.1.0-dev
LDFLAGS := -X main.version=$(VERSION)
BENCHTIME ?= 1x

.PHONY: build test lint check smoke bench stress-scale fixtures install clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/lw

test:
	go test ./...

lint:
	gofmt -l . && go vet ./...

check: lint test

# Workflow 002: the e2e suite over the real binary, the package benchmarks at
# 250/1000/4000 notes, and the opt-in 1000-note scale run. smoke stays plain
# `go test` — CI is the only place -race runs — and stress-scale is gated
# again inside the test itself, so no other entry point can ever run it.
smoke:
	go test ./internal/e2e/ -count=1 -v

bench:
	go test ./internal/index ./internal/stage ./internal/lint ./internal/vault -run '^$$' -bench . -benchmem -benchtime=$(BENCHTIME)

stress-scale:
	LW_STRESS_SCALE=1 go test ./internal/e2e/ -run TestStressScale -count=1 -v -timeout 20m

fixtures:
	go test ./... -update

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/lw

clean:
	rm -f $(BINARY)
	rm -rf dist/
