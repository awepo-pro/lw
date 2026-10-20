package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/awepo-pro/lw/internal/extract"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/vault"
)

// runIngestAttach implements `lw ingest --attach <file|url|dir>...` (058 S2).
// It backfills verbatim original files beside committed raw sources whose
// body hashes match, stages the updates deterministically with Author{Kind: "human"},
// and calls no model.
func runIngestAttach(
	ctx context.Context,
	openEng func() error,
	eng func() *stage.Engine,
	canonRoot string,
	ex extract.Extractor,
	sources []string,
	dryRun bool,
) error {
	// 1. Walk directory arguments with extract.Walk.
	expanded := make(map[string]bool)
	explicit := make(map[string]bool)
	expandedSrcs := make([]string, 0, len(sources))
	for _, arg := range sources {
		if info, serr := os.Stat(arg); serr == nil && info.IsDir() {
			files, skipped, werr := extract.Walk(arg, ex)
			if werr != nil {
				return werr
			}
			for _, sk := range skipped {
				fmt.Printf("skipped %s: %s\n", sk.Path, sk.Reason)
			}
			if len(files) == 0 {
				return fmt.Errorf("no ingestible files in %s", arg)
			}
			for _, f := range files {
				expandedSrcs = append(expandedSrcs, f)
				expanded[f] = true
			}
			continue
		}
		expandedSrcs = append(expandedSrcs, arg)
		explicit[arg] = true
	}
	sources = expandedSrcs

	// 2. Classify paths inside the vault's raw/.
	var keptSources []string
	for _, src := range sources {
		if expanded[src] && !explicit[src] {
			if _, under := walkedRawPath(canonRoot, src); under {
				fmt.Printf("skipped %s: inside the vault's raw/\n", src)
				continue
			}
			keptSources = append(keptSources, src)
		} else {
			if !isURLSource(src) {
				if _, under := rawPathUnder(canonRoot, canonicalPath(src)); under {
					return fmt.Errorf("attach %s: path is inside the vault's raw/; pass the original source file or URL", src)
				}
			}
			keptSources = append(keptSources, src)
		}
	}

	// 3 & 4. Extract each remaining source and determine original bytes & extension.
	type extractedDoc struct {
		src       string
		doc       *extract.Doc
		origBytes []byte
		origExt   string
	}
	var docs []extractedDoc
	for _, src := range keptSources {
		doc, err := ex.Extract(ctx, src)
		if err != nil {
			if expanded[src] && !explicit[src] {
				switch {
				case errors.Is(err, extract.ErrNotText):
					fmt.Printf("skipped %s: not text\n", src)
					continue
				case errors.Is(err, extract.ErrTooLittleText):
					fmt.Printf("skipped %s: too little text (scanned or image-only PDF?)\n", src)
					continue
				case errors.Is(err, extract.ErrSidecarMissing):
					fmt.Printf("skipped %s: no PDF sidecar (see lw doctor)\n", src)
					continue
				}
			}
			return fmt.Errorf("extract %s: %w", src, err)
		}

		var origBytes []byte
		var origExt string
		if doc.OriginalBytes != nil {
			origBytes = doc.OriginalBytes
			origExt = doc.OriginalExt
		} else if doc.Original != "" {
			b, rerr := os.ReadFile(doc.Original)
			if rerr != nil {
				return fmt.Errorf("attach %s: read original %s: %w", src, doc.Original, rerr)
			}
			origBytes = b
			origExt = strings.ToLower(filepath.Ext(doc.Original))
		}
		if len(origBytes) == 0 || origExt == "" {
			return fmt.Errorf("attach %s: extractor %s produced no original bytes", src, doc.Extractor)
		}
		docs = append(docs, extractedDoc{
			src:       src,
			doc:       doc,
			origBytes: origBytes,
			origExt:   origExt,
		})
	}

	// 5. Open the engine and read committed raw sources.
	if err := openEng(); err != nil {
		return err
	}
	e := eng()
	v := e.Vault()
	committed := v.RawSources()

	// 6. Match each extracted document by body SHA against committed raws.
	type attachPlan struct {
		src       string
		raw       *vault.RawSource
		extractor string
		origPath  string
		origSHA   string
		origBytes []byte
		beforeSHA string
	}
	var plans []attachPlan
	matchedRaw := make(map[string]string)
	for _, d := range docs {
		sha := tools.SourceBodySHA(d.doc.Markdown)
		var match *vault.RawSource
		for _, r := range committed {
			if r.SHA256 == sha {
				match = r
				break
			}
		}
		if match == nil {
			return fmt.Errorf("attach %s: no committed raw source matches its extracted body (sha256 %s) — run lw ingest %s to ingest it as a new source", d.src, sha[:12], d.src)
		}
		if match.Original != "" {
			fmt.Printf("skipped %s: %s already has original %s\n", d.src, match.Path, match.Original)
			continue
		}
		if first, dup := matchedRaw[match.Path]; dup {
			fmt.Printf("skipped %s: same content as %s\n", d.src, first)
			continue
		}
		matchedRaw[match.Path] = d.src

		var origPath string
		if d.origExt == ".md" {
			origPath = match.Path + ".orig"
		} else {
			origPath = strings.TrimSuffix(match.Path, ".md") + d.origExt
		}
		origSHA := sha256Hex(d.origBytes)
		beforeBytes, err := v.Read(match.Path)
		if err != nil {
			return fmt.Errorf("attach %s: read committed raw %s: %w", d.src, match.Path, err)
		}
		beforeSHA := sha256Hex(beforeBytes)

		plans = append(plans, attachPlan{
			src:       d.src,
			raw:       match,
			extractor: d.doc.Extractor,
			origPath:  origPath,
			origSHA:   origSHA,
			origBytes: d.origBytes,
			beforeSHA: beforeSHA,
		})
	}

	// 7. If all matches already have originals, exit 0 cleanly.
	if len(plans) == 0 {
		fmt.Println("nothing to attach: every matched raw source already has an original")
		return nil
	}

	// 8. Dry run prints the attach plan and exits 0.
	if dryRun {
		for _, p := range plans {
			fmt.Printf("would attach %s -> %s (original: %s)\n", p.src, p.raw.Path, p.origPath)
		}
		return nil
	}

	// 9. Acquire exclusive flock on tool.lock.
	release, err := lockTool(v.Root())
	if err != nil {
		return err
	}
	defer release()

	// 10. Open or join the changeset with Author{Kind: "human"}.
	var keptSrcs []string
	for _, p := range plans {
		keptSrcs = append(keptSrcs, p.src)
	}
	intent := "attach original: " + strings.Join(keptSrcs, ", ")
	cs, joined, err := e.OpenOrJoin(intent, stage.Author{Kind: "human"})
	if err != nil {
		return err
	}
	if joined {
		fmt.Fprintf(os.Stderr, "joined open changeset %s (%d op(s) already staged; they will be reviewed and committed together)\n", cs.ID, len(cs.Live()))
	}
	n0 := len(cs.Ops)

	// 11. Append attach OpIngestSource for each plan.
	for _, p := range plans {
		updated := *p.raw
		updated.Original = p.origPath
		updated.OriginalSHA256 = p.origSHA
		_, err := e.Append(stage.Op{
			Kind:            stage.OpIngestSource,
			Path:            p.raw.Path,
			Before:          p.beforeSHA,
			Content:         updated.Serialize(),
			Extractor:       p.extractor,
			OriginalPath:    p.origPath,
			Original:        p.origSHA,
			OriginalContent: p.origBytes,
		})
		if err != nil {
			return ingestRollback(e, joined, cs.ID, n0, err)
		}
	}

	// 12. Read final changeset and print ingest tail.
	final, err := e.Current()
	if err != nil {
		return ingestRollback(e, joined, cs.ID, n0, fmt.Errorf("read back changeset %s: %w", cs.ID, err))
	}
	printIngestTail(e, final, joined)
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
