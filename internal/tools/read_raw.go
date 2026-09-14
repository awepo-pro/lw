package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

// rawChunkRunes is roughly 4000 tokens at ~4 characters per token
// (backbone §6: "raw.get returns one ~4000-token chunk").
const rawChunkRunes = 16000

// stagedSourceMarker prefixes a chunk read from an op still open in the
// current changeset, never a committed raw source — S6-C121. It is one
// line, blank-separated from the chunk header, so a model reading the
// result can tell at a glance the bytes have not landed yet.
const stagedSourceMarker = "(staged in the open changeset, not yet committed)\n\n"

type rawGetArgs struct {
	Source string `json:"source"`
	Chunk  int    `json:"chunk,omitempty"`
}

func rawGetTool(d Deps) Tool {
	return Tool{
		Name: "raw.get",
		Description: "Read one chunk of a raw source's body by its exact " +
			"vault-relative path under raw/ — a committed source, or one just " +
			"staged in the open changeset via stage.ingest_source (read before " +
			"commit too). Long sources are split into ~4000-token chunks; call " +
			"again with an increasing chunk number to read the rest.",
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

	body, marker, ok, err := rawSourceBody(d, source)
	if err != nil {
		return Result{}, fmt.Errorf("tools: raw.get: %w", err)
	}
	if !ok {
		return Result{IsError: true, Content: rawNotFoundMessage(d, source)}, nil
	}

	chunks := chunkText(body, rawChunkRunes)
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

	return Result{Content: fmt.Sprintf("%schunk %d of %d\n\n%s", marker, chunk, n, chunks[chunk-1])}, nil
}

// rawSourceBody resolves source to the raw body raw.get should chunk, and
// the marker to prefix the result with — "" for a committed source, the
// staged-in-changeset notice for one that is not yet committed.
//
// Contract (S6-C121): d.Vault.RawSource is tried first — the committed
// vault. A miss falls back to d.Engine.StagedFile, the read-only seam that
// lets a handler see a path stage.ingest_source (or stage.create_page,
// stage.patch_page) proposed in the currently open changeset without
// touching the filesystem itself (backbone §6). Staged bytes are parsed
// through vault.ParseRawSource — the same parser a committed raw/ file
// goes through — so chunking is identical either way. ok is false, err nil
// when source resolves to neither; err is non-nil only for a genuine
// engine/CAS failure or a staged file that fails to parse as a RawSource
// (an internal invariant violation, since stage.ingest_source only ever
// proposes bytes rawDocumentDefect already proved parse cleanly).
func rawSourceBody(d Deps, source string) (body, marker string, ok bool, err error) {
	if r, found := d.Vault.RawSource(source); found {
		return r.Body, "", true, nil
	}
	if d.Engine == nil {
		return "", "", false, nil
	}
	b, staged, err := d.Engine.StagedFile(source)
	if err != nil {
		return "", "", false, err
	}
	if !staged {
		return "", "", false, nil
	}
	r, err := vault.ParseRawSource(source, b)
	if err != nil {
		return "", "", false, fmt.Errorf("parse staged raw source %s: %w", source, err)
	}
	return r.Body, stagedSourceMarker, true, nil
}

// rawNotFoundMessage builds the IsError message for a source that resolved
// through neither the committed vault nor the open changeset.
//
// Contract (S6-C121): when the engine has one or more live ingest_source
// ops, the message lists their paths — "staged raw sources in the open
// changeset: <path>[, <path>...]" — so a model whose guessed path missed
// can self-correct in one step rather than guessing again blind.
func rawNotFoundMessage(d Deps, source string) string {
	msg := fmt.Sprintf(
		"raw source %q was not found; provide the exact vault-relative path under raw/ — check a citing page's ^[raw/...] provenance marker",
		source,
	)
	if d.Engine == nil {
		return msg
	}
	cs, err := d.Engine.Current()
	if err != nil {
		return msg
	}
	var staged []string
	for _, op := range cs.Live() {
		if op.Kind == stage.OpIngestSource {
			staged = append(staged, op.Path)
		}
	}
	if len(staged) == 0 {
		return msg
	}
	sort.Strings(staged)
	return fmt.Sprintf("%s; staged raw sources in the open changeset: %s", msg, strings.Join(staged, ", "))
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
