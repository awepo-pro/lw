package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

const stageAddLinkSchema = `{
  "type":"object",
  "properties":{"from":{"type":"string"},"to":{"type":"string"},"context":{"type":"string"}},
  "required":["from","to"],"additionalProperties":false
}`

type stageAddLinkArgs struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Context string `json:"context,omitempty"`
}

func stageAddLinkTool(d Deps) Tool {
	return Tool{Name: "stage.add_link", Description: "Propose a bidirectional link between two existing pages; broken endpoints are refused. When the open changeset already stages an endpoint, the link composes against that staged state.", Schema: json.RawMessage(stageAddLinkSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageAddLinkArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.add_link", err, `{"from":"wiki/concepts/a.md","to":"wiki/concepts/b.md"}`), nil
		}
		if d.Vault == nil {
			return Result{IsError: true, Content: "no vault configured"}, nil
		}
		// Both endpoints resolve through stagedPatchBase (020 FIX-3b, G3
		// review finding 5): when the open changeset already holds a live
		// content op for an endpoint, that staged state is the base — the
		// existing-link check reads the staged body and Before is the
		// staged sha — so add_link composes with staged edits exactly the
		// way stage.patch_page does, instead of proposing against content
		// the engine has already moved past. Precisely the shape lint
		// --fix's per-page rounds create: the round that patches a page
		// may also link it. Only when nothing staged targets the path
		// does the committed vault answer, keeping every changeset
		// without a prior op on the endpoint byte-for-byte its pre-fix
		// path.
		from, _, ok, err := stagedPatchBase(d, a.From)
		if err != nil {
			return Result{}, fmt.Errorf("tools: stage.add_link: %w", err)
		}
		if !ok {
			return Result{IsError: true, Content: fmt.Sprintf("from page %q was not found%s", a.From, stagedStateClause(d, a.From))}, nil
		}
		to, _, ok, err := stagedPatchBase(d, a.To)
		if err != nil {
			return Result{}, fmt.Errorf("tools: stage.add_link: %w", err)
		}
		if !ok {
			return Result{IsError: true, Content: fmt.Sprintf("to page %q was not found%s", a.To, stagedStateClause(d, a.To))}, nil
		}
		if a.From == a.To {
			return Result{IsError: true, Content: "from and to must be different pages"}, nil
		}
		patches := make([]stage.Op, 0, 2)
		for _, pair := range []struct {
			page   *vault.Page
			target string
		}{{from, a.To}, {to, a.From}} {
			if hasResolvedLink(d.Vault, pair.page, pair.target) {
				continue
			}
			sec, ok := pair.page.Section("## Related")
			if !ok && len(pair.page.Sections) > 0 {
				sec = pair.page.Sections[len(pair.page.Sections)-1]
				ok = true
			}
			if !ok {
				return Result{IsError: true, Content: fmt.Sprintf("%s has no section to receive a link; add a section-level patch first%s", pair.page.Path, stagedStateClause(d, pair.page.Path))}, nil
			}
			line := "- [[" + pageBasename(pair.target) + "]]"
			if strings.TrimSpace(a.Context) != "" {
				line += " — " + strings.TrimSpace(a.Context)
			}
			updatedBody := vault.AppendToSection(pair.page.Body, sec, sectionAppendText(line))
			updated := vault.Page{Path: pair.page.Path, FM: pair.page.FM, Body: updatedBody}
			hunks := stage.ComputeHunks(string(pair.page.Serialize()), string(updated.Serialize()))
			for i := range hunks {
				hunks[i].Path = pair.page.Path
				hunks[i].Section = sec.Heading
			}
			patches = append(patches, stage.Op{Kind: stage.OpPatchPage, Path: pair.page.Path, Section: sec.Heading, Before: pair.page.SHA256(), Content: updated.Serialize(), Hunks: hunks, Rationale: "bidirectional link"})
		}
		if len(patches) == 0 {
			return Result{Content: "link already exists in both directions"}, nil
		}
		// No committed-side pre-flight: the old per-patch
		// ValidateOp(op, d.Vault, …) loop re-validated against the
		// committed page, so for a staged endpoint it rejected the very
		// staged-sha Before the handler now correctly proposes — the
		// pre-flight and the engine disagreed, and the refusal carried
		// no recovery hint (G3 finding 5). stage.patch_page has no
		// separate pre-flight either: Engine.Append validates every op
		// against the same cascadeBase projection the patches were
		// computed from, and the refusals below name the staged state.
		res, err := appendStageOp(d, "stage.add_link", stage.Op{Kind: stage.OpAddLink, From: a.From, To: a.To})
		if err != nil || res.IsError {
			return addLinkFailure(d, res, err, a.From, a.To)
		}
		for _, op := range patches {
			if _, err := d.Engine.Append(op); err != nil {
				fres, ferr := stageFailure("stage.add_link", err)
				return addLinkFailure(d, fres, ferr, op.Path)
			}
		}
		return Result{Content: fmt.Sprintf("proposed bidirectional link between %s and %s (%d page patches)", a.From, a.To, len(patches))}, nil
	}}
}

// stagedStateClause is the parenthetical appended to an add_link refusal
// whose target page the open changeset holds staged state for (020
// FIX-3b, G3 review finding 5): a refusal that says nothing about the
// staged state sends a proposing agent back to a committed page it
// already moved past. A page the committed vault also carries has staged
// edits to compose against; a path absent from the committed vault exists
// only because a live op created it, and no composition can make it an
// add_link endpoint — the clause says that instead. Empty when the engine
// holds nothing staged for the path, keeping every pre-existing refusal
// byte-for-byte.
func stagedStateClause(d Deps, p string) string {
	if d.Engine == nil {
		return ""
	}
	_, staged, err := d.Engine.StagedFile(p)
	if err != nil || !staged {
		return ""
	}
	if _, committed := d.Vault.Page(p); !committed {
		return fmt.Sprintf(" (%s exists only in the open changeset's staged state; an add_link endpoint must be a committed page, so link after this changeset commits)", p)
	}
	return " (this page has staged edits in the open changeset; re-read it with wiki.get and compose against that staged state)"
}

// addLinkFailure is stageFailure's result path with one addition: a
// validation-class refusal (IsError, nil Go error) gains
// stagedStateClause for the first endpoint the clause has something to
// say about. A turn-aborting Go error passes through untouched.
func addLinkFailure(d Deps, res Result, goErr error, paths ...string) (Result, error) {
	if goErr != nil || !res.IsError {
		return res, goErr
	}
	for _, p := range paths {
		if clause := stagedStateClause(d, p); clause != "" {
			res.Content += clause
			break
		}
	}
	return res, nil
}

func hasResolvedLink(v *vault.Vault, page *vault.Page, target string) bool {
	resolvedTarget, ok := vault.Resolve(v, target)
	if !ok {
		return false
	}
	for _, link := range page.Links {
		if resolved, ok := vault.Resolve(v, link.Target); ok && resolved == resolvedTarget {
			return true
		}
	}
	return false
}

const stageRetractSchema = `{
  "type":"object",
  "properties":{"page":{"type":"string"},"reason":{"type":"string"}},
  "required":["page","reason"],"additionalProperties":false
}`

type stageRetractArgs struct {
	Page   string `json:"page"`
	Reason string `json:"reason"`
}

func stageRetractTool(d Deps) Tool {
	return Tool{Name: "stage.retract", Description: "Propose a tombstone with a reason; the source page is never deleted.", Schema: json.RawMessage(stageRetractSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageRetractArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.retract", err, `{"page":"wiki/concepts/old-page.md","reason":"superseded"}`), nil
		}
		return appendStageOp(d, "stage.retract", stage.Op{Kind: stage.OpRetract, Path: a.Page, Rationale: a.Reason})
	}}
}
