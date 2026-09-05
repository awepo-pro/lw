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
	return Tool{Name: "stage.add_link", Description: "Propose a bidirectional link between two existing pages; broken endpoints are refused.", Schema: json.RawMessage(stageAddLinkSchema), Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
		var a stageAddLinkArgs
		if err := decodeArgs(args, &a); err != nil {
			return badArgs("stage.add_link", err, `{"from":"wiki/concepts/a.md","to":"wiki/concepts/b.md"}`), nil
		}
		if d.Vault == nil {
			return Result{IsError: true, Content: "no vault configured"}, nil
		}
		from, ok := d.Vault.Page(a.From)
		if !ok {
			return Result{IsError: true, Content: fmt.Sprintf("from page %q was not found", a.From)}, nil
		}
		to, ok := d.Vault.Page(a.To)
		if !ok {
			return Result{IsError: true, Content: fmt.Sprintf("to page %q was not found", a.To)}, nil
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
				return Result{IsError: true, Content: fmt.Sprintf("%s has no section to receive a link; add a section-level patch first", pair.page.Path)}, nil
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
		for _, op := range patches {
			if err := stage.ValidateOp(op, d.Vault, d.Vault.Schema()); err != nil {
				return stageFailure("stage.add_link", err)
			}
		}
		if res, err := appendStageOp(d, "stage.add_link", stage.Op{Kind: stage.OpAddLink, From: a.From, To: a.To}); err != nil {
			return res, err
		}
		for _, op := range patches {
			if _, err := d.Engine.Append(op); err != nil {
				return stageFailure("stage.add_link", err)
			}
		}
		return Result{Content: fmt.Sprintf("proposed bidirectional link between %s and %s (%d page patches)", a.From, a.To, len(patches))}, nil
	}}
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
