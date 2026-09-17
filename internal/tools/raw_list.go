// raw_list.go implements raw.list (008 contract §4.2), the one read-only
// discovery tool 008 adds. Nothing before it let the agent find a raw
// source it was not told the exact path of: raw.get demands the path and
// the registry offers no filesystem verb, so a source the user mentioned
// without a path was unreachable. Rows are read through the same seams
// raw.get reads through — Vault.RawSources for the committed vault,
// Engine.Current plus StagedFile for the open changeset — so the tool
// never touches the filesystem itself.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/vault"
)

const rawListSchema = `{
  "type": "object",
  "properties": {
    "query": {"type": "string"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 200}
  },
  "additionalProperties": false
}`

// The default row cap and the hard cap the schema's maximum advertises.
const (
	rawListDefaultLimit = 50
	rawListMaxLimit     = 200
)

// rawRowSep separates a row's fields: an em dash with one space on each
// side (008 §4.2).
const rawRowSep = " — "

type rawListArgs struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

func rawListTool(d Deps) Tool {
	return Tool{
		Name: "raw.list",
		Description: "List every raw source — committed and staged in the open " +
			"changeset — one per line: exact raw/ path, title, source_url, then " +
			"either the ingested date and sha256 prefix or the staging changeset " +
			"id. query keeps only rows where every term appears in the path, " +
			"title or source_url; read a listed source's body with raw.get.",
		Schema:   json.RawMessage(rawListSchema),
		ReadOnly: true,
		Handler: func(ctx context.Context, args json.RawMessage) (Result, error) {
			return rawListHandler(d, args)
		},
	}
}

// rawListRow is one output line plus the lowercased fields its query
// filtering matches against.
type rawListRow struct {
	line      string
	path      string
	title     string
	sourceURL string
}

func rawListHandler(d Deps, args json.RawMessage) (Result, error) {
	var a rawListArgs
	if err := decodeArgs(args, &a); err != nil {
		return badArgs("raw.list", err, `{"query":"kv cache","limit":20}`), nil
	}
	query := strings.TrimSpace(a.Query)
	matched := filterRawRows(rawListRows(d), index.Tokenize(a.Query))

	limit := a.Limit
	if limit <= 0 {
		limit = rawListDefaultLimit
	}
	if limit > rawListMaxLimit {
		limit = rawListMaxLimit
	}

	if len(matched) == 0 {
		if query == "" {
			return Result{Content: "no raw sources"}, nil
		}
		return Result{Content: `no raw sources matching "` + query + `"`}, nil
	}

	var b strings.Builder
	if query == "" {
		fmt.Fprintf(&b, "%d raw source(s)", len(matched))
	} else {
		fmt.Fprintf(&b, "%d raw source(s) matching \"%s\"", len(matched), query)
	}
	shown := matched
	if len(matched) > limit {
		shown = matched[:limit]
	}
	for _, r := range shown {
		b.WriteString("\n")
		b.WriteString(r.line)
	}
	if more := len(matched) - len(shown); more > 0 {
		fmt.Fprintf(&b, "\n(and %d more)", more)
	}
	return Result{Content: b.String()}, nil
}

// rawListRows merges the committed raw sources with the live ingest_source
// ops of the open changeset and sorts the rows by path, committed first on
// a tie (a tie cannot arise through stage.ingest_source's own suffix rule).
// Rows are built from two ordered slices — RawSources is path-sorted, Live
// is op order — and one sort, never a map iteration, so the output is
// deterministic.
func rawListRows(d Deps) []rawListRow {
	var rows []rawListRow
	if d.Vault != nil {
		for _, r := range d.Vault.RawSources() {
			rows = append(rows, rawListRow{
				line: strings.Join([]string{
					r.Path,
					rawListTitle(r.Title),
					r.SourceURL,
					"ingested " + r.Ingested.String(),
					"sha " + shortSHA(r.SHA256),
				}, rawRowSep),
				path:      strings.ToLower(r.Path),
				title:     strings.ToLower(r.Title),
				sourceURL: strings.ToLower(r.SourceURL),
			})
		}
	}
	if d.Engine != nil {
		if cs, err := d.Engine.Current(); err == nil {
			for _, op := range cs.Live() {
				if op.Kind != stage.OpIngestSource {
					continue
				}
				title, sourceURL := stagedRawIdentity(d, op.Path)
				rows = append(rows, rawListRow{
					line: strings.Join([]string{
						op.Path,
						rawListTitle(title),
						sourceURL,
						"staged in " + cs.ID,
					}, rawRowSep),
					path:      strings.ToLower(op.Path),
					title:     strings.ToLower(title),
					sourceURL: strings.ToLower(sourceURL),
				})
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].path < rows[j].path })
	return rows
}

// stagedRawIdentity reads a staged op's title and source_url the way
// raw.get reads a staged body: the bytes behind Engine.StagedFile, parsed
// by vault.ParseRawSource. A staged file that does not parse (unreachable
// for anything stage.ingest_source proposed — rawDocumentDefect proved the
// bytes parse before they were staged) reports no title and no url rather
// than an error, so one bad row cannot hide the rest.
func stagedRawIdentity(d Deps, path string) (title, sourceURL string) {
	if d.Engine == nil {
		return "", ""
	}
	b, staged, err := d.Engine.StagedFile(path)
	if err != nil || !staged {
		return "", ""
	}
	r, err := vault.ParseRawSource(path, b)
	if err != nil {
		return "", ""
	}
	return r.Title, r.SourceURL
}

// rawListTitle is the placeholder a row shows when a source has no title.
func rawListTitle(title string) string {
	if strings.TrimSpace(title) == "" {
		return "(untitled)"
	}
	return title
}

// shortSHA truncates a hex sha256 to the 8 characters a row shows.
func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// filterRawRows keeps the rows where every term appears — in the path, the
// title or the source_url, each matched as a lowercase substring. No terms
// matches everything.
func filterRawRows(rows []rawListRow, terms []string) []rawListRow {
	if len(terms) == 0 {
		return rows
	}
	out := make([]rawListRow, 0, len(rows))
	for _, r := range rows {
		if rawRowMatches(r, terms) {
			out = append(out, r)
		}
	}
	return out
}

func rawRowMatches(r rawListRow, terms []string) bool {
	for _, t := range terms {
		if !strings.Contains(r.path, t) && !strings.Contains(r.title, t) && !strings.Contains(r.sourceURL, t) {
			return false
		}
	}
	return true
}
