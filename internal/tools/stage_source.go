package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

const stageIngestSourceSchema = `{
  "type":"object",
  "properties":{"uri":{"type":"string"},"kind":{"type":"string","enum":["article","paper","transcript"]},"name":{"type":"string"}},
  "required":["uri"],"additionalProperties":false
}`

type stageIngestSourceArgs struct {
	URI  string `json:"uri"`
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
}

// rawKindDir maps stage.ingest_source's kind argument to the raw/
// subdirectory lw init actually scaffolds — raw/articles, raw/papers,
// raw/transcripts (cmd/lw/cmd_init.go), which internal/extract's own
// kindDir/SuggestPath already use. Fixed S6-C122: this handler used to
// build "raw/" + kind directly, staging every source one directory below
// where lw init, extract and the backbone's own docs put it, so a page's
// declared sources: could never resolve against a real raw/ path once the
// tool proposed it. kind is normalized to one of the three schema enum
// values before this is called, so the default is unreachable in
// practice; it is still the safe article-shaped fallback, never a made-up
// fourth directory.
func rawKindDir(kind string) string {
	switch kind {
	case "paper":
		return "papers"
	case "transcript":
		return "transcripts"
	default:
		return "articles"
	}
}

// SourceBodySHA is the one body-hash rule for an extracted source's
// markdown (008 contract §14, A-807): leading blank lines come off, the
// body is normalized the way a raw file stores it, and the result runs
// through vault.BodySHA256 — the sha a staged raw file records in its
// frontmatter and a committed one carries in RawSources(). stage.ingest_source
// records it and cmdIngest's duplicate pre-check compares it, so the two
// can never disagree about whether the vault already holds a source.
func SourceBodySHA(markdown string) string {
	return vault.BodySHA256(normalizeToolBody(strings.TrimLeft(markdown, "\n")))
}

func stageIngestSourceTool(d Deps) Tool {
	return Tool{Name: "stage.ingest_source", Description: "Extract and propose a raw source from a local file path or an http(s) URL; duplicate body hashes are rejected. On success the result names the exact staged path and chunk count — read the staged source with raw.get before proposing pages from it. Pass name, a short ASCII slug such as \"quaternion-introduction\", when the source's title has no Latin letters; it is used only when the title gives no usable file name.", Schema: json.RawMessage(stageIngestSourceSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageIngestSourceArgs
		if err := decodeArgsStrict(args, &a); err != nil {
			return badArgs("stage.ingest_source", err, `{"uri":"/path/to/source.html","kind":"article"}`), nil
		}
		uri := strings.TrimSpace(a.URI)
		if uri == "" {
			return Result{IsError: true, Content: "uri is required: provide a local file path or an http(s) URL"}, nil
		}
		// 010's un-defer (contract §3): a URL flows to d.Extract exactly
		// like a local file — same dedupe, naming, validator and
		// Engine.Append below. The fetched page becomes a raw source like
		// any other, under the normal hunk-level human review.
		if u, perr := url.Parse(uri); perr == nil && (u.Scheme == "http" || u.Scheme == "https") {
			slog.Info("ingest url", "uri", uri)
		}
		if d.Extract == nil {
			return Result{IsError: true, Content: "no extractor configured"}, nil
		}
		if !d.Extract.CanHandle(uri) {
			return Result{IsError: true, Content: fmt.Sprintf("no configured extractor can handle %q", uri)}, nil
		}
		doc, err := d.Extract.Extract(ctx, uri)
		if err != nil {
			// A fetch (or parse) failure is recoverable — the model can
			// re-ingest from a path or another URL — so it is an IsError
			// result with a nil Go error, never a turn abort.
			return Result{IsError: true, Content: "stage.ingest_source: " + err.Error()}, nil
		}
		if doc == nil {
			return Result{}, fmt.Errorf("tools: stage.ingest_source: extractor returned nil document")
		}
		// Leading blank lines come off before the body is hashed: §2.7
		// defines a raw Body as everything after the closing delimiter with
		// leading blank lines stripped, so a body hashed before that strip
		// would disagree with the sha a re-parse recomputes (src-integrity
		// drift, on a file raw/ never lets anyone rewrite). body still
		// carries the normalized text — the empty check, chunk count and
		// staged bytes all need it — while the hash itself comes from
		// SourceBodySHA, the exported rule cmdIngest's pre-check shares.
		body := normalizeToolBody(strings.TrimLeft(doc.Markdown, "\n"))
		if body == "" {
			return Result{IsError: true, Content: "extracted source is empty"}, nil
		}
		bodySHA := SourceBodySHA(doc.Markdown)
		kind := a.Kind
		if kind == "" {
			kind = doc.Kind
		}
		if kind != "article" && kind != "paper" && kind != "transcript" {
			kind = "article"
		}
		// 008 §4.1, extended by 009 (idea 011): title, else the optional
		// name hint, else basename minus one trailing extension, else
		// "untitled" — never empty, so the old "could not derive a stable
		// source filename" refusal is gone.
		name := sourceNameForDoc(doc.Title, a.Name, uri)
		kindDir := rawKindDir(kind)
		candidate := "raw/" + kindDir + "/" + name + ".md"
		sourceURL := strings.TrimSpace(doc.SourceURL)
		if sourceURL == "" {
			sourceURL = uri
		}
		content := rawSourceDocument(sourceURL, ingestDate(), body)
		if defect := rawDocumentDefect(sourceURL, body, content); defect != "" {
			return Result{IsError: true, Content: defect}, nil
		}
		// The engine's own dedupe (stage.validateIngestSource) compares
		// sha256(op.Content) against the body sha each committed raw file
		// records in its frontmatter. op.Content is now the whole file —
		// frontmatter included (TD-5) — so that comparison can no longer
		// fire, and this check carries the guarantee instead: refuse a body
		// that is already in the vault, whatever path it landed at.
		if d.Vault != nil {
			for _, r := range d.Vault.RawSources() {
				if r.SHA256 == bodySHA {
					return Result{IsError: true, Content: fmt.Sprintf("source %q is already ingested at %s (sha256 %s); dedupe by hash", uri, r.Path, bodySHA)}, nil
				}
			}
		}
		// 008 §4.1 splits the old "same path OR same sha" refusal: a body
		// already proposed in this changeset is still refused with the
		// existing text — compared by BODY sha (C-806), the way the
		// committed check above compares frontmatter shas, because the
		// whole-file sha Append records in op.SHA256 changes whenever
		// source_url does and would buy the same body a -2 path — while a
		// taken candidate holding a DIFFERENT source falls through to the
		// -n suffix below instead of cornering the agent.
		if d.Engine != nil {
			fileSHA := vault.BodySHA256(string(content))
			if cs, currentErr := d.Engine.Current(); currentErr == nil {
				for _, op := range cs.Live() {
					if op.Kind != stage.OpIngestSource {
						continue
					}
					dup := false
					if sha := stagedBodySHA(d, op.Path); sha != "" {
						dup = sha == bodySHA
					} else {
						// The staged bytes could not be read back (a CAS
						// failure); keep the pre-008 whole-file comparison.
						dup = op.SHA256 == fileSHA
					}
					if dup {
						return Result{IsError: true, Content: fmt.Sprintf("source %q is already ingested or proposed in this changeset (duplicate path or sha256)", candidate)}, nil
					}
				}
			}
		}
		_, sourcePath, suffixed := resolveSourcePath(d, kindDir, name)
		extractor := doc.Extractor
		if extractor == "" {
			extractor = "local"
		}
		res, err := appendStageOp(d, "stage.ingest_source", stage.Op{Kind: stage.OpIngestSource, Path: sourcePath, Content: content, SHA256: bodySHA, Extractor: extractor})
		if err != nil || res.IsError {
			return res, err
		}
		// S6-C121: name the exact staged path and how to read it back, in
		// the same changeset, before it is committed. Without this an
		// agent that just staged a source has no way to learn the path
		// stage.ingest_source normalized it to (kind, slug and directory
		// are all derived here, not echoed anywhere else) and falls back
		// to guessing at raw.get, which fails on every guess. n is the
		// chunk count raw.get itself will report for the SAME body — see
		// rawSourceBody, which parses the identical staged bytes back
		// through vault.ParseRawSource before chunking, so the two counts
		// cannot drift.
		n := len(chunkText(body, rawChunkRunes))
		res.Content = fmt.Sprintf("%s at %s — %d chunk(s); read it with raw.get {\"source\":%q,\"chunk\":1}",
			res.Content, sourcePath, n, sourcePath)
		// 008 §4.1: when the -n suffix decided the path, say so — otherwise
		// the agent cannot tell why the source did not land at its title.
		if suffixed {
			res.Content += fmt.Sprintf(" named %s because %s already holds a different source.", sourcePath, candidate)
		}
		return res, nil
	}}
}

// stagedBodySHA returns the sha to compare the incoming body against for
// one live ingest_source op: the sha256 of the body parsed back out of the
// op's stored bytes — the same read raw.get and raw.list make through
// Engine.StagedFile, because Append clears op.Content and keeps only the
// whole-file sha in op.SHA256 (C-806) — or the sha of the whole stored
// bytes when they do not parse, mirroring stage's own ingestBodySHA. ""
// means the bytes could not be read at all and the caller falls back to
// the pre-008 whole-file comparison on op.SHA256; a hex sha256 is never
// "".
func stagedBodySHA(d Deps, path string) string {
	if d.Engine == nil {
		return ""
	}
	b, staged, err := d.Engine.StagedFile(path)
	if err != nil || !staged {
		return ""
	}
	if src, perr := vault.ParseRawSource("raw/proposed/proposal.md", b); perr == nil {
		return vault.BodySHA256(src.Body)
	}
	return vault.BodySHA256(string(b))
}

// rawSourceDocument renders the canonical bytes of a raw/ file for one
// extracted source: the three-key provenance block ParseRawSource requires
// (backbone §2.7 — source_url, ingested, sha256, nothing else, in that
// order), a blank line, then the extractor's markdown body byte for byte.
//
// TD-5: before this the tool proposed the bare markdown, so the committed
// raw file failed to parse as a RawSource at all — one fm-required error
// through Vault.ParseErrors, and a source Vault.RawSources never counted,
// which is what made `lw status` report `0 raw` after a successful ingest.
// Built through RawSource.Serialize rather than hand-formatted, so the
// proposed bytes are exactly what the same RawSource re-serializes to and
// the frontmatter sha256 is BodySHA256 of the body src-integrity compares.
func rawSourceDocument(sourceURL string, ingested vault.Date, body string) []byte {
	return (&vault.RawSource{
		SourceURL: sourceURL,
		Ingested:  ingested,
		SHA256:    vault.BodySHA256(body),
		Body:      body,
	}).Serialize()
}

// ingestDate returns the date a raw file records as `ingested`. Deps
// (backbone §6) carries no clock and neither Doc nor Op carries a
// timestamp, so — exactly like stage.create_page's created and updated —
// the handler takes today's UTC date. Only the calendar date is emitted,
// so every proposal built on the same UTC day is byte-identical; the
// layout is a literal, which is why ParseDate cannot fail here.
func ingestDate() vault.Date {
	d, _ := vault.ParseDate(time.Now().UTC().Format("2006-01-02"))
	return d
}

// rawDocumentDefect verifies the file this tool is about to propose parses
// back to exactly the source it was built from, returning "" when it does.
// raw/ is write-once (00-conventions §5.3): a malformed proposal is not
// reviewable junk the agent can re-edit but a permanent vault resident, so
// the one thing that can break it — a source_url that is not representable
// as a plain YAML scalar (a newline, or a " #" the parser reads as a
// comment) — is caught here, before it is staged, and named for the model
// to fix.
func rawDocumentDefect(sourceURL, body string, b []byte) string {
	// The path argument only feeds ParseRawSource's error text, which this
	// check discards in favour of its own message; the placeholder is never
	// a real file.
	parsed, err := vault.ParseRawSource("raw/proposed/proposal.md", b)
	if err == nil && parsed.SourceURL == sourceURL && parsed.SHA256 == vault.BodySHA256(body) && parsed.Body == body {
		return ""
	}
	return fmt.Sprintf(
		"source url %q cannot be recorded in raw frontmatter as a plain YAML scalar; ingest from a path or URL without newlines or \" #\" sequences", sourceURL)
}
