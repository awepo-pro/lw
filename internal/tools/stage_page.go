package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

const stageCreatePageSchema = `{
  "type":"object",
  "properties":{
    "path":{"type":"string"},"title":{"type":"string"},"type":{"type":"string"},
    "tags":{"type":"array","items":{"type":"string"}},"sources":{"type":"array","items":{"type":"string"}},
    "confidence":{"type":"string"},"contested":{"type":"boolean"},"body":{"type":"string"},"rationale":{"type":"string"}
  },
  "required":["path","title","type","tags","sources","confidence","contested","body","rationale"],
  "additionalProperties":false
}`

const stagePatchPageSchema = `{
  "type":"object",
  "properties":{
    "path":{"type":"string"},"section":{"type":"string"},
    "op":{"type":"string","enum":["replace_section","append_section","insert_after","insert_before","remove_section"]},
    "content":{"type":"string"},"rationale":{"type":"string"}
  },
  "required":["path","section","op","content","rationale"],
  "additionalProperties":false
}`

const stageRenamePageSchema = `{
  "type":"object",
  "properties":{"from":{"type":"string"},"to":{"type":"string"},"rationale":{"type":"string"}},
  "required":["from","to","rationale"],"additionalProperties":false
}`

const stageMergePagesSchema = `{
  "type":"object",
  "properties":{"sources":{"type":"array","items":{"type":"string"},"minItems":2},"into":{"type":"string"},"rationale":{"type":"string"}},
  "required":["sources","into","rationale"],"additionalProperties":false
}`

const stageSplitPageSchema = `{
  "type":"object",
  "properties":{"path":{"type":"string"},"sections":{"type":"array","items":{"type":"string"},"minItems":2},"rationale":{"type":"string"}},
  "required":["path","sections","rationale"],"additionalProperties":false
}`

type stageCreatePageArgs struct {
	Path       string   `json:"path"`
	Title      string   `json:"title"`
	Type       string   `json:"type"`
	Tags       []string `json:"tags"`
	Sources    []string `json:"sources"`
	Confidence string   `json:"confidence"`
	Contested  bool     `json:"contested"`
	Body       string   `json:"body"`
	Rationale  string   `json:"rationale"`
}

func stageCreatePageTool(d Deps) Tool {
	return Tool{Name: "stage.create_page", Description: "Propose a validated wiki page; frontmatter, taxonomy, directory and outbound-link rules are checked before staging.", Schema: json.RawMessage(stageCreatePageSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageCreatePageArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.create_page", err, `{"path":"wiki/concepts/new-page.md","title":"New Page"}`), nil
		}
		now := time.Now().UTC()
		created, _ := vault.ParseDate(now.Format("2006-01-02"))
		fm := vault.Frontmatter{Title: a.Title, Created: created, Updated: created, Type: vault.PageType(a.Type), Tags: a.Tags, Sources: a.Sources, Confidence: vault.Confidence(a.Confidence), Contested: a.Contested}
		page := vault.Page{Path: a.Path, FM: fm, Body: normalizeToolBody(a.Body)}
		return appendStageOp(d, "stage.create_page", stage.Op{Kind: stage.OpCreatePage, Path: a.Path, Content: page.Serialize(), Rationale: a.Rationale, Provenance: append([]string(nil), a.Sources...)})
	}}
}

type stagePatchPageArgs struct {
	Path      string `json:"path"`
	Section   string `json:"section"`
	Op        string `json:"op"`
	Content   string `json:"content"`
	Rationale string `json:"rationale"`
}

func stagePatchPageTool(d Deps) Tool {
	return Tool{Name: "stage.patch_page", Description: "Propose a section-level page patch using replace_section, append_section, insert_after, insert_before or remove_section. When the open changeset already stages the page, the patch composes against that staged state, not the committed bytes.", Schema: json.RawMessage(stagePatchPageSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stagePatchPageArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.patch_page", err, `{"path":"wiki/concepts/kv-cache.md","section":"## Related","op":"append_section","content":"- [[new-page]]","rationale":"add a related page"}`), nil
		}
		if d.Vault == nil {
			return Result{IsError: true, Content: "no vault configured"}, nil
		}
		page, onFail, ok, err := stagedPatchBase(d, a.Path)
		if err != nil {
			return Result{}, fmt.Errorf("tools: stage.patch_page: %w", err)
		}
		if !ok {
			return onFail, nil
		}
		sec, ok := page.Section(a.Section)
		if !ok {
			return Result{IsError: true, Content: fmt.Sprintf("section %q was not found on %s", a.Section, a.Path)}, nil
		}
		var body string
		switch a.Op {
		case "replace_section":
			body = vault.ReplaceSection(page.Body, sec, normalizeToolBody(a.Content))
		case "append_section":
			body = vault.AppendToSection(page.Body, sec, sectionAppendText(a.Content))
		case "insert_after":
			body = vault.InsertAfterSection(page.Body, sec, a.Content)
		case "insert_before":
			body = vault.InsertBeforeSection(page.Body, sec, a.Content)
		case "remove_section":
			body = vault.RemoveSection(page.Body, sec) // Content ignored
		default:
			return Result{IsError: true, Content: fmt.Sprintf("op %q is invalid; use replace_section, append_section, insert_after, insert_before or remove_section", a.Op)}, nil
		}
		updated := vault.Page{Path: page.Path, FM: page.FM, Body: body}
		hunks := stage.ComputeHunks(string(page.Serialize()), string(updated.Serialize()))
		for i := range hunks {
			hunks[i].Path = a.Path
			hunks[i].Section = a.Section
		}
		return appendStageOp(d, "stage.patch_page", stage.Op{Kind: stage.OpPatchPage, Path: a.Path, Section: a.Section, Before: page.SHA256(), Content: updated.Serialize(), Hunks: hunks, Rationale: a.Rationale})
	}}
}

// stagedPatchBase resolves the base page stage.patch_page computes against
// (020 T-B): when the currently open changeset already holds a live
// content op for path, its staged bytes are the base — the section lookup,
// the old side of the hunks and Before all read from them, so a second
// patch on a staged page composes with the first (its Before is the staged
// sha) instead of proposing against content the engine has already moved
// past. Only when nothing staged targets path does the committed vault
// answer, which keeps every changeset without a prior op on the path on
// byte-for-byte its pre-T-B path. Staged bytes parse through
// vault.ParsePage — the same parser a committed page goes through — so the
// sha the tool proposes is the sha the engine's own projection recomputes.
//
// A staged parse failure for a path the committed vault carries as a Page
// is an internal-invariant violation returned as err — the turn aborts,
// mirroring rawSourceBody (020 FIX-1): Append only stages bytes it
// validated, so unparseable staged content for a committed page means the
// changeset or the CAS is damaged, and editing on top of it would hide the
// damage. A cascade sub-op may also target a VAULT-ROOT file
// (curator-memory.md, index.md), whose staged bytes legitimately are not
// page-structured (op.go's buildCascadeOp raw branch): for such a path —
// or any path the committed vault does not carry as a Page — the staged
// bytes are not consumed, and the committed lookup below answers, whose
// miss is the plain not-found IsError. ok is false with onFail set when
// path resolves through neither the changeset nor the vault.
func stagedPatchBase(d Deps, path string) (page *vault.Page, onFail Result, ok bool, err error) {
	if d.Engine != nil {
		b, staged, err := d.Engine.StagedFile(path)
		if err != nil {
			return nil, Result{}, false, err
		}
		if staged {
			p, perr := vault.ParsePage(path, b)
			if perr == nil {
				return p, Result{}, true, nil
			}
			if _, isPage := d.Vault.Page(path); isPage {
				// A committed page's staged bytes should parse: failing
				// that is damage, not input the model gets to argue with.
				return nil, Result{}, false, fmt.Errorf("parse staged page %s: %w", path, perr)
			}
			// Root-file cascade bytes (or an unknown path): keep the
			// committed lookup below.
		}
	}
	p, found := d.Vault.Page(path)
	if !found {
		return nil, Result{IsError: true, Content: fmt.Sprintf("page %q was not found", path)}, false, nil
	}
	return p, Result{}, true, nil
}

type stageRenamePageArgs struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Rationale string `json:"rationale"`
}

func stageRenamePageTool(d Deps) Tool {
	return Tool{Name: "stage.rename_page", Description: "Propose a page rename. The engine computes every inbound backlink cascade from the graph.", Schema: json.RawMessage(stageRenamePageSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageRenamePageArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.rename_page", err, `{"from":"wiki/concepts/old-page.md","to":"wiki/concepts/new-page.md","rationale":"canonical name"}`), nil
		}
		return appendStageOp(d, "stage.rename_page", stage.Op{Kind: stage.OpRenamePage, From: a.From, To: a.To, Rationale: a.Rationale})
	}}
}

type stageMergePagesArgs struct {
	Sources   []string `json:"sources"`
	Into      string   `json:"into"`
	Rationale string   `json:"rationale"`
}

func stageMergePagesTool(d Deps) Tool {
	return Tool{Name: "stage.merge_pages", Description: "Propose merging source pages into one destination; backlink rewrites are computed by the engine.", Schema: json.RawMessage(stageMergePagesSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageMergePagesArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.merge_pages", err, `{"sources":["wiki/concepts/a.md","wiki/concepts/b.md"],"into":"wiki/concepts/merged.md","rationale":"combine duplicates"}`), nil
		}
		return appendStageOp(d, "stage.merge_pages", stage.Op{Kind: stage.OpMergePages, Sources: a.Sources, To: a.Into, Rationale: a.Rationale})
	}}
}

type stageSplitPageArgs struct {
	Path      string   `json:"path"`
	Sections  []string `json:"sections"`
	Rationale string   `json:"rationale"`
}

func stageSplitPageTool(d Deps) Tool {
	return Tool{Name: "stage.split_page", Description: "Propose splitting a page into the listed resulting page paths; the source becomes a reviewable stub.", Schema: json.RawMessage(stageSplitPageSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageSplitPageArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.split_page", err, `{"path":"wiki/concepts/old-page.md","sections":["wiki/concepts/part-a.md","wiki/concepts/part-b.md"],"rationale":"separate concerns"}`), nil
		}
		return appendStageOp(d, "stage.split_page", stage.Op{Kind: stage.OpSplitPage, Path: a.Path, Sources: a.Sections, Rationale: a.Rationale})
	}}
}

func normalizeToolBody(body string) string {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return ""
	}
	return body + "\n"
}

func sectionAppendText(content string) string {
	content = strings.TrimRight(content, "\n")
	if content == "" {
		return ""
	}
	// AppendToSection is a byte-offset primitive. Its caller must supply the
	// blank-line separator that keeps a middle section distinct from the next
	// heading (backbone §2.4 / 00-conventions §5.6). The section's existing
	// content already ends at a line boundary, so only the trailing blank line
	// belongs in the inserted text.
	return content + "\n\n"
}

func pageBasename(p string) string {
	return strings.TrimSuffix(path.Base(p), ".md")
}
