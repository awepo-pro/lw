BINARY := lw
VERSION := 1.0.0-dev
LDFLAGS := -X main.version=$(VERSION)
BENCHTIME ?= 1x

.PHONY: build test lint fmt-check vet check smoke bench stress-scale fixtures install release-snapshot clean

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/lw

test:
	go test ./...

# gofmt -l never fails on its own, so lint splits into a check that does
# (fmt-check) and go vet (vet). git ls-files, not a filesystem walk, so
# .dev-notes/ (git-ignored, see .git/info/exclude) never gets probed. Names
# travel NUL-separated (spaces are safe), files deleted but not yet staged are
# skipped, and gofmt's own failure (a syntax error) fails the check too.
fmt-check:
	@bad="$$(git ls-files -z --cached --others --exclude-standard -- '*.go' \
		| xargs -0 -r sh -c 'for f do [ -e "$$f" ] && printf "%s\0" "$$f"; done; true' sh \
		| xargs -0 -r gofmt -l)" || { echo "fmt-check: gofmt failed" >&2; exit 1; }; \
	if [ -n "$$bad" ]; then \
		echo "$$bad"; \
		exit 1; \
	fi

vet:
	go vet ./...

lint: fmt-check vet

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
