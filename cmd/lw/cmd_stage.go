// cmd_stage.go implements the hidden "lw stage --from <file.json>" verb
// (backbone §13 D-AL): the only way to exercise M2 from the shell before
// internal/tools (S3) exists. It is wired into main.go's verbs table but
// deliberately absent from usage() — that omission is what makes it
// hidden while still reachable through the ordinary dispatch/exitError
// machinery. Treat it as a testing affordance, to be gated or removed
// once the tool layer ships.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/awepo-pro/lw/internal/stage"
)

// cmdStage appends the ops decoded from --from's JSON file to the open
// changeset, opening one with Author{Kind: "human"} and intent "cli stage
// import" when none is open.
func cmdStage(args []string) error {
	fs := flag.NewFlagSet("stage", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	vaultPath := fs.String("vault", "", "vault root (default: nearest ancestor directory containing SCHEMA.md)")
	from := fs.String("from", "", "JSON file of ops to stage (required)")
	if err := fs.Parse(args); err != nil {
		return &exitError{code: 2}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "lw stage: unexpected argument %q\n", fs.Arg(0))
		return &exitError{code: 2}
	}
	if *from == "" {
		fmt.Fprintln(os.Stderr, "usage: lw stage --from <file.json>")
		return &exitError{code: 2}
	}

	ops, err := loadStageOps(*from)
	if err != nil {
		return err
	}

	root, err := findVaultRoot(*vaultPath)
	if err != nil {
		return err
	}

	e, err := stage.OpenEngine(root)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	c, err := e.Current()
	if err != nil {
		if !errors.Is(err, stage.ErrNoChangeset) {
			return err
		}
		c, err = e.OpenChangeset("cli stage import", stage.Author{Kind: "human"})
		if err != nil {
			return err
		}
	}

	for i, op := range ops {
		if _, err := e.Append(op); err != nil {
			return fmt.Errorf("append op %d (%s): %w", i+1, op.Kind, err)
		}
	}

	fmt.Printf("staged %d op(s) into %s\n", len(ops), c.ID)
	return nil
}

// stageOpContentOverlay recovers the one field stage.Op cannot decode
// itself: Content is json:"-" (backbone §5.3 D-AY), deliberately absent
// from changeset.json, so a create_page fixture decoded straight into
// []stage.Op would arrive with no page body and ValidateOp would reject
// it. The --from input format is therefore pinned as a JSON array of op
// objects using stage.Op's own frozen JSON tags verbatim, plus one extra
// key "content" (a string) carrying the post-image text — present only
// in this stage-verb input, never in changeset.json. "id" and "state" may
// be omitted; Engine.Append assigns both.
type stageOpContentOverlay struct {
	Content string                  `json:"content"`
	Cascade []stageOpContentOverlay `json:"cascade"`
}

// loadStageOps reads path, decodes it as a JSON array of stage.Op values
// (every other key already matches stage.Op's tags, so a plain
// json.Unmarshal into []stage.Op handles them), then makes a second decode
// pass purely to recover each entry's "content" (and its cascade
// children's, recursively) into Op.Content.
func loadStageOps(path string) ([]stage.Op, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var ops []stage.Op
	if err := json.Unmarshal(b, &ops); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var overlays []stageOpContentOverlay
	if err := json.Unmarshal(b, &overlays); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	for i := range ops {
		if i < len(overlays) {
			applyStageContentOverlay(&ops[i], overlays[i])
		}
	}
	return ops, nil
}

// applyStageContentOverlay copies ov's Content into op.Content, and
// recurses into op.Cascade/ov.Cascade pairwise — both decoded from the
// same JSON array, so they share order and length.
func applyStageContentOverlay(op *stage.Op, ov stageOpContentOverlay) {
	if ov.Content != "" {
		op.Content = []byte(ov.Content)
	}
	for i := range op.Cascade {
		if i < len(ov.Cascade) {
			applyStageContentOverlay(&op.Cascade[i], ov.Cascade[i])
		}
	}
}
