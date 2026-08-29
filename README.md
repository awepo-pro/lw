# llmwiki — `lw`

A TUI + toolchain that compiles immutable sources into a reviewable markdown
wiki. An agent reads sources and proposes an interlinked wiki as a
git-commit-shaped **changeset**; nothing lands until a human reviews it
hunk by hunk in the TUI and commits. The agent has no filesystem — its only
mutation path is a set of graph-aware, validating, transactional tools that
propose changes into a buffer. See [`PLAN.md`](PLAN.md) for the full design.

This repository is a work in progress (v0.1 build). See
`.dev-notes/projects/llmwiki/workflows/001-v0-1-build.md` for current status.

## Build

Requires Go 1.25+ (developed against 1.27.0).

```bash
make build   # builds ./cmd/lw to ./lw
make test    # go test ./...
make check   # gofmt -l . && go vet ./... && go test ./...
```

## Run

```bash
go run ./cmd/lw --help
```
