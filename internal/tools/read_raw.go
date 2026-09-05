package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// rawChunkRunes is roughly 4000 tokens at ~4 characters per token
// (backbone §6: "raw.get returns one ~4000-token chunk").
const rawChunkRunes = 16000

type rawGetArgs struct {
	Source string `json:"source"`
	Chunk  int    `json:"chunk,omitempty"`
}

func rawGetTool(d Deps) Tool {
	return Tool{
		Name: "raw.get",
		Description: "Read one chunk of a raw source's body by its exact " +
			"vault-relative path under raw/. Long sources are split into " +
			"~4000-token chunks; call again with an increasing chunk number " +
			"to read the rest.",
		Schema:   json.RawMessage(rawGetSchema),
		ReadOnly: true,
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			return rawGetHandler(ctx, d, args)
		},
	}
}

func rawGetHandler(ctx context.Context, d Deps, args json.RawMessage) (Result, error) {
	var a rawGetArgs
	if err := decodeArgs(args, &a); err != nil {
		return badArgs("raw.get", err, `{"source": "raw/papers/leviathan-2023.md"}`), nil
	}

	source := strings.TrimSpace(a.Source)
	if source == "" {
		return Result{IsError: true, Content: `source is required: provide the exact vault-relative path under raw/, e.g. {"source": "raw/papers/leviathan-2023.md"}`}, nil
	}

	r, ok := d.Vault.RawSource(source)
	if !ok {
		return Result{IsError: true, Content: fmt.Sprintf(
			"raw source %q was not found; provide the exact vault-relative path under raw/ — check a citing page's ^[raw/...] provenance marker",
			source,
		)}, nil
	}

	chunks := chunkText(r.Body, rawChunkRunes)
	n := len(chunks)
	chunk := a.Chunk
	if chunk == 0 {
		chunk = 1
	}
	if chunk < 1 || chunk > n {
		return Result{IsError: true, Content: fmt.Sprintf(
			"chunk %d is out of range for %s: it has %d chunk(s), so chunk must be between 1 and %d",
			chunk, source, n, n,
		)}, nil
	}

	return Result{Content: fmt.Sprintf("chunk %d of %d\n\n%s", chunk, n, chunks[chunk-1])}, nil
}

// chunkText splits s into chunks of at most maxRunes runes each, never
// splitting a UTF-8 rune. An empty s still yields one empty chunk, so
// raw.get always has a "chunk 1 of 1" to report rather than a division by
// zero or an out-of-range chunk 1.
func chunkText(s string, maxRunes int) []string {
	runes := []rune(s)
	if len(runes) == 0 {
		return []string{""}
	}

	out := make([]string, 0, len(runes)/maxRunes+1)
	for start := 0; start < len(runes); start += maxRunes {
		end := start + maxRunes
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, string(runes[start:end]))
	}
	return out
}
