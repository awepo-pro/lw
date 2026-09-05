package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"strings"
	"unicode"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

const stageIngestSourceSchema = `{
  "type":"object",
  "properties":{"uri":{"type":"string"},"kind":{"type":"string","enum":["article","paper","transcript"]}},
  "required":["uri"],"additionalProperties":false
}`

type stageIngestSourceArgs struct {
	URI  string `json:"uri"`
	Kind string `json:"kind,omitempty"`
}

func stageIngestSourceTool(d Deps) Tool {
	return Tool{Name: "stage.ingest_source", Description: "Extract and propose a local raw source. Network URLs are rejected in this stage; duplicate body hashes are rejected.", Schema: json.RawMessage(stageIngestSourceSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageIngestSourceArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.ingest_source", err, `{"uri":"/path/to/source.html","kind":"article"}`), nil
		}
		uri := strings.TrimSpace(a.URI)
		if uri == "" {
			return Result{IsError: true, Content: "uri is required: provide a local file path"}, nil
		}
		u, err := url.Parse(uri)
		if err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			return Result{IsError: true, Content: "stage.ingest_source accepts local paths only; HTTP fetching is deferred"}, nil
		}
		if d.Extract == nil {
			return Result{IsError: true, Content: "no extractor configured"}, nil
		}
		if !d.Extract.CanHandle(uri) {
			return Result{IsError: true, Content: fmt.Sprintf("no configured extractor can handle local source %q", uri)}, nil
		}
		doc, err := d.Extract.Extract(ctx, uri)
		if err != nil {
			return Result{}, fmt.Errorf("tools: stage.ingest_source: extract %s: %w", uri, err)
		}
		if doc == nil {
			return Result{}, fmt.Errorf("tools: stage.ingest_source: extractor returned nil document")
		}
		body := normalizeToolBody(doc.Markdown)
		if body == "" {
			return Result{IsError: true, Content: "extracted source is empty"}, nil
		}
		bodySHA := vault.BodySHA256(body)
		kind := a.Kind
		if kind == "" {
			kind = doc.Kind
		}
		if kind != "article" && kind != "paper" && kind != "transcript" {
			kind = "article"
		}
		name := slugSourceName(doc.Title)
		if name == "" {
			name = slugSourceName(path.Base(uri))
		}
		if name == "" {
			return Result{IsError: true, Content: "could not derive a stable source filename from uri or extracted title"}, nil
		}
		sourcePath := "raw/" + kind + "/" + name + ".md"
		if d.Engine != nil {
			if cs, currentErr := d.Engine.Current(); currentErr == nil {
				for _, op := range cs.Live() {
					if op.Kind == stage.OpIngestSource && (op.Path == sourcePath || op.SHA256 == bodySHA) {
						return Result{IsError: true, Content: fmt.Sprintf("source %q is already ingested or proposed in this changeset (duplicate path or sha256)", sourcePath)}, nil
					}
				}
			}
		}
		extractor := doc.Extractor
		if extractor == "" {
			extractor = "local"
		}
		return appendStageOp(d, "stage.ingest_source", stage.Op{Kind: stage.OpIngestSource, Path: sourcePath, Content: []byte(body), SHA256: bodySHA, Extractor: extractor})
	}}
}

func slugSourceName(s string) string {
	s = sanitizeSourceSlug(strings.ToLower(strings.TrimSpace(s)))
	return strings.Trim(s, "-")
}

func sanitizeSourceSlug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
