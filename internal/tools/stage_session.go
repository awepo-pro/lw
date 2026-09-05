package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/stage"
)

const stageOpenSchema = `{
  "type": "object",
  "properties": {"intent": {"type": "string"}},
  "required": ["intent"],
  "additionalProperties": false
}`

const stageCloseSchema = `{
  "type": "object",
  "properties": {},
  "required": [],
  "additionalProperties": false
}`

type stageOpenArgs struct {
	Intent string `json:"intent"`
}

func stageTools(d Deps) []Tool {
	return []Tool{
		stageOpenTool(d), stageCreatePageTool(d), stagePatchPageTool(d),
		stageRenamePageTool(d), stageMergePagesTool(d), stageSplitPageTool(d),
		stageAddLinkTool(d), stageIngestSourceTool(d), stageRetractTool(d),
		stageCloseTool(d),
	}
}

func stageOpenTool(d Deps) Tool {
	return Tool{
		Name: "stage.open", Description: "Open one reviewable changeset for the supplied intent.",
		Schema: json.RawMessage(stageOpenSchema), ReadOnly: false,
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			var a stageOpenArgs
			if err := decodeArgs(args, &a); err != nil {
				return badArgs("stage.open", err, `{"intent":"curate the vault"}`), nil
			}
			if strings.TrimSpace(a.Intent) == "" {
				return Result{IsError: true, Content: "intent is required: explain what this changeset should accomplish"}, nil
			}
			if d.Engine == nil {
				return Result{IsError: true, Content: "no staging engine configured"}, nil
			}
			cs, err := d.Engine.OpenChangeset(strings.TrimSpace(a.Intent), d.Author)
			if err != nil {
				return stageFailure("stage.open", err)
			}
			return Result{Content: fmt.Sprintf("opened changeset %s for %q", cs.ID, cs.Intent), Data: cs}, nil
		},
	}
}

func stageCloseTool(d Deps) Tool {
	return Tool{
		Name: "stage.close", Description: "Summarize the currently proposed changeset for human review.",
		Schema: json.RawMessage(stageCloseSchema), ReadOnly: true,
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			if len(args) != 0 && string(args) != "null" {
				var ignored struct{}
				if err := decodeArgs(args, &ignored); err != nil {
					return badArgs("stage.close", err, `{}`), nil
				}
			}
			if d.Engine == nil {
				return Result{IsError: true, Content: "no staging engine configured"}, nil
			}
			cs, err := d.Engine.Current()
			if err != nil {
				return stageFailure("stage.close", err)
			}
			return stageSummary(cs), nil
		},
	}
}

func stageSummary(cs *stage.Changeset) Result {
	counts := make(map[stage.OpKind]int)
	for _, op := range cs.Live() {
		counts[op.Kind]++
	}
	kinds := make([]string, 0, len(counts))
	for kind := range counts {
		kinds = append(kinds, string(kind))
	}
	sort.Strings(kinds)
	var b strings.Builder
	fmt.Fprintf(&b, "changeset %s\nintent: %s\noperations: %d", cs.ID, cs.Intent, len(cs.Live()))
	if len(kinds) > 0 {
		b.WriteString("\nby kind:")
		for _, kind := range kinds {
			fmt.Fprintf(&b, "\n- %s: %d", kind, counts[stage.OpKind(kind)])
		}
	}
	touches := cs.Touches()
	fmt.Fprintf(&b, "\nfiles touched: %d", len(touches))
	for _, p := range touches {
		fmt.Fprintf(&b, "\n- %s", p)
	}
	fmt.Fprintf(&b, "\nchecks: schema=%s lint=%s orphans=%d broken_links=%d",
		cs.Checks.Schema, cs.Checks.Lint, cs.Checks.Orphans, cs.Checks.BrokenLinks)
	return Result{Content: b.String(), Data: cs}
}

func stageFailure(tool string, err error) (Result, error) {
	if errors.Is(err, stage.ErrValidation) || errors.Is(err, stage.ErrNoChangeset) || errors.Is(err, stage.ErrOpenChangeset) || errors.Is(err, stage.ErrStale) {
		return Result{IsError: true, Content: fmt.Sprintf("%s: %v", tool, err)}, nil
	}
	return Result{}, fmt.Errorf("tools: %s: %w", tool, err)
}

func appendStageOp(d Deps, tool string, op stage.Op) (Result, error) {
	if d.Engine == nil {
		return Result{IsError: true, Content: "no staging engine configured"}, nil
	}
	id, err := d.Engine.Append(op)
	if err != nil {
		return stageFailure(tool, err)
	}
	return Result{Content: fmt.Sprintf("proposed %s (%s)", id, op.Kind), Data: id}, nil
}
