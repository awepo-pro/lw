BINARY := lw
VERSION := 1.0.0-dev
LDFLAGS := -X main.version=$(VERSION)
BENCHTIME ?= 1x

.PHONY: build test lint check smoke bench stress-scale fixtures install release-snapshot clean

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

# -p 1 runs the four packages one at a time: each generates its own scale
# fixture vaults, and a sibling's generation landing inside another's timed
# window would show up as noise in the ns/op columns.
bench:
	go test ./internal/index ./internal/stage ./internal/lint ./internal/vault -run '^$$' -bench . -benchmem -benchtime=$(BENCHTIME) -p 1

stress-scale:
	LW_STRESS_SCALE=1 go test ./internal/e2e/ -run TestStressScale -count=1 -v -timeout 20m

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
