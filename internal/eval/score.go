package eval

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/awepo-pro/lw/internal/eval/score"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/trace"
	"github.com/awepo-pro/lw/internal/vault"
)

// The metric names (C5). A result's Metrics map holds a name only when the
// metric applies to that run: absent is "not applicable" or "unmeasured",
// never 0 and never null, because a 0 would be averaged into the scorecard
// as if the model had earned it.
const (
	MetricFactRecall      = "fact_recall"
	MetricAbstainOK       = "abstain_ok"
	MetricCiteValid       = "cite_valid"
	MetricCiteExpected    = "cite_expected"
	MetricChunkCoverage   = "chunk_coverage"
	MetricToolErrorRate   = "tool_error_rate"
	MetricRounds          = "rounds"
	MetricMaxRoundsHit    = "max_rounds_hit"
	MetricInputTokens     = "input_tokens"
	MetricCachedTokens    = "cached_tokens"
	MetricOutputTokens    = "output_tokens"
	MetricReasoningTokens = "reasoning_tokens"
	MetricWallS           = "wall_s"
	MetricOps             = "ops"
	MetricPagesStaged     = "pages_staged"
	MetricLintWarns       = "lint_warns"

	// The ingest-process and page-quality metrics of 049. The first four read
	// the trace (absent when the run recorded no turn); the others read what
	// was staged against the snapshot.
	MetricReadsBeforeFirstStage = "reads_before_first_stage"
	MetricReadRefusals          = "read_refusals"
	MetricClosed                = "closed"
	MetricSearchCalls           = "search_calls"
	MetricPagesNew              = "pages_new"
	MetricPatchedLossless       = "patched_lossless"
	MetricDupPages              = "dup_pages"
	MetricOrphansNew            = "orphans_new"
)

// metricOrder is the order every table prints metrics in: accuracy first,
// then how the run got there, then what an ingest left behind. A metric a
// newer scorer adds sorts after these, by name.
var metricOrder = []string{
	MetricFactRecall, MetricAbstainOK, MetricCiteValid, MetricCiteExpected, MetricChunkCoverage,
	MetricToolErrorRate, MetricRounds, MetricMaxRoundsHit,
	MetricInputTokens, MetricCachedTokens, MetricOutputTokens, MetricReasoningTokens, MetricWallS,
	MetricOps, MetricPagesStaged, MetricLintWarns,
	MetricReadsBeforeFirstStage, MetricReadRefusals, MetricClosed, MetricSearchCalls,
	MetricPagesNew, MetricPatchedLossless, MetricDupPages, MetricOrphansNew,
}

// resultsFile is the scored form of a run, kept beside run.json.
const resultsFile = "results.json"

// CaseResult is one (case, index) run, scored. Failed runs are kept — as
// failed, with no metrics — so a report can count them. (037 T3.)
type CaseResult struct {
	Case    string             `json:"case"`
	Index   int                `json:"index"`
	Verb    string             `json:"verb"`
	Kind    string             `json:"kind"`
	Failed  bool               `json:"failed"`
	Metrics map[string]float64 `json:"metrics"`
}

// Results is <run>/results.json: the run's own record plus every run's
// metrics, sorted by case then index. It is the one file `lweval show` and
// `lweval compare` read, so neither needs the set, the snapshot or the
// traces — a run can be reported on from its results alone.
//
// SetSHA256 is the sha256 of the cases.toml the run was SCORED against, which
// is not Run.SetSHA256 (the one it was run against): a fact fixed after the
// run changes the score without changing the run, and `compare` must be able
// to tell two scorings of different sets apart. (037 T3, A-037-8.)
type Results struct {
	Run       RunInfo      `json:"run"`
	SetSHA256 string       `json:"set_sha256"`
	Results   []CaseResult `json:"results"`
}

// LoadResults reads <runDir>/results.json, the file Score wrote (037 T3).
func LoadResults(runDir string) (*Results, error) {
	p := filepath.Join(runDir, resultsFile)
	b, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("eval: %s has no %s; run `lweval score %s` first", runDir, resultsFile, runDir)
		}
		return nil, fmt.Errorf("eval: %w", err)
	}
	var r Results
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("eval: %s: %w", p, err)
	}
	return &r, nil
}

// Score loads the set a run directory sits in — <set>/runs/<id>, the layout
// `lweval run` writes — and scores the run (037 T3). A run directory kept
// anywhere else goes through ScoreSet with its set named.
func Score(runDir string) (*Results, error) {
	abs, err := filepath.Abs(runDir)
	if err != nil {
		return nil, fmt.Errorf("eval: score: %w", err)
	}
	runs := filepath.Dir(abs)
	if filepath.Base(runs) != "runs" {
		return nil, fmt.Errorf("eval: score: %s is not <set>/runs/<run-id>, so its set cannot be found; name the set explicitly", runDir)
	}
	set, err := LoadSet(filepath.Dir(runs))
	if err != nil {
		return nil, fmt.Errorf("eval: score: %w", err)
	}
	return ScoreSet(abs, set)
}

// ScoreSet scores every (case, index) under runDir against set and writes
// <runDir>/results.json (037 T3).
//
// Scoring reads the artifacts and the set AS THEY ARE NOW, so a fact fixed in
// cases.toml after a run is re-scored without another provider call — that is
// the reason the runner keeps raw artifacts and scores nothing itself. Two
// things are still held fixed: the run must have been made against this set's
// snapshot (a ref resolves against that vault, and another vault would score
// the same answer differently), and every directory must be a finished job.
// Anything else is an error, because a scorecard that quietly skipped a run
// would shrink N and tighten the noise estimate it is meant to be honest about.
func ScoreSet(runDir string, set *Set) (*Results, error) {
	abs, err := filepath.Abs(runDir)
	if err != nil {
		return nil, fmt.Errorf("eval: score: %w", err)
	}
	var info RunInfo
	if err := readJSONFile(filepath.Join(abs, "run.json"), &info); err != nil {
		return nil, fmt.Errorf("eval: score: %w", err)
	}
	if info.SnapshotSHA256 != set.SnapshotSHA256 {
		return nil, fmt.Errorf("eval: score: run used snapshot sha256 %s, but the set's snapshot %s is %s",
			info.SnapshotSHA256, set.Snapshot, set.SnapshotSHA256)
	}
	setSHA, err := fileSHA256(filepath.Join(set.Dir, "cases.toml"))
	if err != nil {
		return nil, fmt.Errorf("eval: score: read cases.toml: %w", err)
	}
	refs, err := listRunCases(abs)
	if err != nil {
		return nil, err
	}

	snap, err := os.MkdirTemp("", "lweval-score-")
	if err != nil {
		return nil, fmt.Errorf("eval: score: %w", err)
	}
	defer removeAll(snap)
	if err := Extract(filepath.Join(set.Dir, set.Snapshot), snap); err != nil {
		return nil, fmt.Errorf("eval: score: %w", err)
	}

	s := &scorer{set: set, snap: snap, bodies: map[string]string{}, ask: map[string]AskCase{}, ingest: map[string]IngestCase{}}
	for _, c := range set.Ask {
		s.ask[c.ID] = c
	}
	for _, c := range set.Ingest {
		s.ingest[c.ID] = c
	}
	res := &Results{Run: info, SetSHA256: setSHA, Results: []CaseResult{}}
	for _, ref := range refs {
		r, err := s.scoreOne(ref)
		if err != nil {
			return nil, fmt.Errorf("eval: score: %s/%d: %w", ref.id, ref.index, err)
		}
		res.Results = append(res.Results, r)
	}
	if err := writeJSON(filepath.Join(abs, resultsFile), res); err != nil {
		return nil, err
	}
	return res, nil
}

// caseRef names one (case, index) directory of a run.
type caseRef struct {
	id    string
	index int
	dir   string
}

// listRunCases returns the <case>/<i> directories under a run, sorted by
// case id and then numerically by index. Dot-directories (.work, a crashed
// run's scratch) and plain files (run.json, results.json) are not cases.
func listRunCases(runDir string) ([]caseRef, error) {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return nil, fmt.Errorf("eval: score: %w", err)
	}
	var out []caseRef
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		kids, err := os.ReadDir(filepath.Join(runDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("eval: score: %w", err)
		}
		for _, k := range kids {
			i, err := strconv.Atoi(k.Name())
			if !k.IsDir() || err != nil || i < 1 {
				continue
			}
			out = append(out, caseRef{id: e.Name(), index: i, dir: filepath.Join(runDir, e.Name(), k.Name())})
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].id != out[b].id {
			return out[a].id < out[b].id
		}
		return out[a].index < out[b].index
	})
	return out, nil
}

// scorer is one scoring pass: the set, the extracted snapshot vault refs
// resolve against, and a cache of source bodies — an answer cites the same
// paper a dozen times, and re-parsing a 300 KB extracted PDF per marker is
// the cost lint already learned to avoid.
type scorer struct {
	set    *Set
	snap   string
	bodies map[string]string // root + "\x00" + path -> raw body
	ask    map[string]AskCase
	ingest map[string]IngestCase

	// snapSlugs is the slug of every wiki page of the snapshot vault, read
	// once on first use: dup_pages compares each new page against all of them
	// and a run has dozens of ingests (049).
	snapSlugs     []string
	snapSlugsRead bool
}

// scoreOne scores one run directory. A run that failed (its last attempt
// exited non-zero) gets a record and no metrics: its output is whatever a
// dying lw left, and scoring it would charge the model for the harness.
func (s *scorer) scoreOne(ref caseRef) (CaseResult, error) {
	var meta RunMeta
	if err := readJSONFile(filepath.Join(ref.dir, "meta.json"), &meta); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return CaseResult{}, errors.New("no meta.json (the job did not finish); delete the directory or re-run it")
		}
		return CaseResult{}, err
	}
	if meta.Case != ref.id || meta.Index != ref.index {
		return CaseResult{}, fmt.Errorf("meta.json says %s/%d", meta.Case, meta.Index)
	}
	res := CaseResult{Case: ref.id, Index: ref.index, Verb: meta.Verb, Kind: meta.Kind, Metrics: map[string]float64{}}
	switch meta.Verb {
	case "query":
		c, ok := s.ask[ref.id]
		if !ok {
			return CaseResult{}, fmt.Errorf("ask case %q is not in cases.toml", ref.id)
		}
		res.Kind = c.Kind
	case "ingest":
		if _, ok := s.ingest[ref.id]; !ok {
			return CaseResult{}, fmt.Errorf("ingest case %q is not in cases.toml", ref.id)
		}
	default:
		return CaseResult{}, fmt.Errorf("meta.json has verb %q (want query or ingest)", meta.Verb)
	}
	if meta.Failed {
		res.Failed = true
		return res, nil
	}

	turns, err := loadTurns(ref.dir, meta.Traces)
	if err != nil {
		return CaseResult{}, err
	}
	if meta.Verb == "query" {
		err = s.scoreAsk(res.Metrics, s.ask[ref.id], ref.dir)
	} else {
		err = s.scoreIngest(res.Metrics, s.ingest[ref.id], ref.dir, turns)
	}
	if err != nil {
		return CaseResult{}, err
	}
	processMetrics(res.Metrics, turns)
	return res, nil
}

// scoreAsk adds the metrics of an `lw query` run: the answer is stdout.txt.
func (s *scorer) scoreAsk(m map[string]float64, c AskCase, dir string) error {
	b, err := os.ReadFile(filepath.Join(dir, "stdout.txt"))
	if err != nil {
		return err
	}
	text := string(b)
	if len(c.Facts) > 0 {
		hit, _, err := score.FactRecall(text, c.Facts)
		if err != nil {
			return err
		}
		m[MetricFactRecall] = float64(hit) / float64(len(c.Facts))
	}
	refs := score.Refs(text)
	if c.Kind == "absent" {
		// lw's designed answer to an out-of-vault question is the "Not from
		// your vault:" label and then a knowledge answer, which may name the
		// wiki pages the vault does hold. What contradicts the label is
		// appealing to RAW evidence — a marker or a raw/ path — for the part
		// the vault does not cover; a wiki ref or wikilink does not. (A-037-3.)
		m[MetricAbstainOK] = boolMetric(score.Abstained(text) && !score.RawEvidence(refs))
	}
	if len(refs) > 0 {
		m[MetricCiteValid] = validShare(score.CheckRefs(refs, s.resolver("")))
	}
	if len(c.CiteAny) > 0 {
		m[MetricCiteExpected] = boolMetric(score.CitesAny(refs, c.CiteAny))
	}
	return nil
}

// scoreIngest adds the metrics of an `lw ingest` run, from what it staged.
//
// The text scored is the BODY of every staged wiki page, concatenated in
// path order — not the whole file. The frontmatter holds the title, tags and
// a sources list, none of it content the model wrote about the source: a
// fact that is only a tag would score a hit, and the sources paths would add
// a valid ref to every page and pull cite_valid toward 1. A page that does
// not parse is scored as written, so a malformed page is judged by what is on
// it rather than dropped from the count.
func (s *scorer) scoreIngest(m map[string]float64, c IngestCase, dir string, turns []*trace.Turn) error {
	stagedDir := filepath.Join(dir, "staged")
	pages, err := stagedWikiPages(stagedDir)
	if err != nil {
		return err
	}
	text := joinPageBodies(pages)
	// pages_staged makes "the ingest staged no wiki page" a number on the
	// scorecard: with 0 pages every fact misses and every ref check passes
	// vacuously, and neither of those would say why. (A-037-10.)
	m[MetricPagesStaged] = float64(len(pages))
	newPages, err := s.stagedPageMetrics(m, pages)
	if err != nil {
		return err
	}
	if len(c.Facts) > 0 {
		hit, _, err := score.FactRecall(text, c.Facts)
		if err != nil {
			return err
		}
		m[MetricFactRecall] = float64(hit) / float64(len(c.Facts))
	}
	resolve := s.resolver(stagedDir)
	if refs := score.Refs(text); len(refs) > 0 {
		m[MetricCiteValid] = validShare(score.CheckRefs(refs, resolve))
	}

	var cs changesetFile
	switch err := readJSONFile(filepath.Join(dir, "changeset.json"), &cs); {
	case errors.Is(err, fs.ErrNotExist):
		// Nothing staged: an ingest that exited 0 and proposed nothing is a
		// result (no ops), and there is no lint report to take warns from.
	case err != nil:
		return err
	}
	live, readTotal, total := 0, 0, 0
	seen := map[string]bool{}
	for _, op := range cs.Ops {
		if !isLive(op.State) {
			continue
		}
		live++
		if op.Op != string(stage.OpIngestSource) || seen[op.Path] {
			continue
		}
		seen[op.Path] = true
		body, ok := resolveStagedSource(stagedDir, op.Path)
		if !ok {
			continue
		}
		read, n := score.ChunkCoverage(turns, op.Path, body)
		readTotal += read
		total += n
	}
	m[MetricOps] = float64(live)
	if total > 0 {
		m[MetricChunkCoverage] = float64(readTotal) / float64(total)
	}

	var lf lintFile
	switch err := readJSONFile(filepath.Join(dir, "lint.json"), &lf); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return err
	default:
		m[MetricLintWarns] = float64(lf.Warns)
		// lint.json holds only findings on pages the ingest staged, so an
		// orphan here is either a page it changed or one it created; only the
		// created one is the ingest's own failure to link.
		orphans := 0
		for _, f := range lf.Findings {
			if f.Check == "link-orphan" && newPages[f.Path] {
				orphans++
			}
		}
		m[MetricOrphansNew] = float64(orphans)
	}
	return ingestTraceMetrics(m, filepath.Join(dir, "traces"), turns)
}

// ingestTraceMetrics adds the four ingest metrics that come from the trace:
// how many wiki reads came before the first page change (refused or not),
// how many reads 048's budget refused, whether stage.close was reached, and
// how many wiki.search calls were made. A run with no recorded turn has none
// of them — "nobody looked" must not average in as 0 reads. (049.)
func ingestTraceMetrics(m map[string]float64, tracesDir string, turns []*trace.Turn) error {
	if len(turns) == 0 {
		return nil
	}
	refusals := 0
	for _, t := range turns {
		if t == nil {
			continue
		}
		errs, err := score.ToolErrors(tracesDir, t.ID, t)
		if err != nil {
			return err
		}
		for _, e := range errs {
			if score.IsReadTool(e.Name) && score.IsReadRefusal(e.Text) {
				refusals++
			}
		}
	}
	m[MetricReadsBeforeFirstStage] = float64(score.ReadsBeforeFirstStage(turns))
	m[MetricReadRefusals] = float64(refusals)
	m[MetricClosed] = boolMetric(score.Closed(turns))
	m[MetricSearchCalls] = float64(score.CallCount(turns, "wiki.search"))
	return nil
}

// stagedPageMetrics adds pages_new, dup_pages and patched_lossless, and
// returns the set of new pages' paths (the ones the snapshot does not have).
//
// A staged wiki page either is not in the snapshot — new — or is, and then
// the ingest edited a page that existed. For the new ones, dup_pages counts
// those whose slug is a near-duplicate of any snapshot page's (the model made
// a second page for a topic the wiki already covered). For the edited ones,
// patched_lossless is the share whose staged body still has every non-blank
// line of the snapshot's: a patch that rewrites a page and drops its facts
// fails here though it scores well on fact_recall. It is absent when no
// existing page was staged — there was nothing to lose. (049.)
func (s *scorer) stagedPageMetrics(m map[string]float64, pages []stagedPage) (map[string]bool, error) {
	newPages := map[string]bool{}
	existing, kept := 0, 0
	for _, p := range pages {
		full := filepath.Join(s.snap, filepath.FromSlash(p.path))
		info, err := os.Stat(full)
		switch {
		case err == nil && info.Mode().IsRegular():
			b, err := os.ReadFile(full)
			if err != nil {
				return nil, err
			}
			existing++
			if score.LinesKept(pageBody(p.path, b), p.body) {
				kept++
			}
		case err == nil || errors.Is(err, fs.ErrNotExist):
			newPages[p.path] = true
		default:
			return nil, err
		}
	}
	m[MetricPagesNew] = float64(len(newPages))
	if existing > 0 {
		m[MetricPatchedLossless] = float64(kept) / float64(existing)
	}

	slugs, err := s.snapshotSlugs()
	if err != nil {
		return nil, err
	}
	dups := 0
	for _, p := range pages { // page order, so the count never depends on map order
		if !newPages[p.path] {
			continue
		}
		slug := slugOf(p.path)
		for _, other := range slugs {
			if score.NearDuplicate(slug, other) {
				dups++
				break // a page is counted once, however many it matches
			}
		}
	}
	m[MetricDupPages] = float64(dups)
	return newPages, nil
}

// snapshotSlugs returns the slug of every wiki page in the snapshot vault,
// in path order, reading the tree on the first call only.
func (s *scorer) snapshotSlugs() ([]string, error) {
	if s.snapSlugsRead {
		return s.snapSlugs, nil
	}
	root := filepath.Join(s.snap, "wiki")
	var slugs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == root {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() && strings.HasSuffix(d.Name(), ".md") {
			slugs = append(slugs, slugOf(d.Name()))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.snapSlugs, s.snapSlugsRead = slugs, true
	return slugs, nil
}

// slugOf is a page's slug: its file name without the ".md", from a
// slash-separated path or a bare name.
func slugOf(p string) string {
	return strings.TrimSuffix(p[strings.LastIndex(p, "/")+1:], ".md")
}

// isLive reports whether an op's state leaves it in the changeset the way
// the runner's own staging does: dropped and rejected ops are out.
func isLive(state string) bool {
	return state != string(stage.StateDropped) && state != string(stage.StateRejected)
}

// stagedPage is one staged wiki page: its vault path (slash-separated,
// "wiki/…") and its body as scoring reads it.
type stagedPage struct {
	path string
	body string
}

// stagedWikiPages reads every file under stagedDir/wiki, in slash-path order.
// A page's body is what follows its frontmatter (pageBody).
func stagedWikiPages(stagedDir string) ([]stagedPage, error) {
	root := filepath.Join(stagedDir, "wiki")
	var paths []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == root {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	pages := make([]stagedPage, 0, len(paths))
	for _, rel := range paths {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		pages = append(pages, stagedPage{path: "wiki/" + rel, body: pageBody("wiki/"+rel, b)})
	}
	return pages, nil
}

// pageBody is the text of a page file that scoring reads: the body after the
// frontmatter, or the whole file when it does not parse — a malformed page is
// judged by what is on it rather than dropped.
func pageBody(path string, b []byte) string {
	if page, err := vault.ParsePage(path, b); err == nil {
		return page.Body
	}
	return string(b)
}

// joinPageBodies joins the bodies of pages, separated by a blank line so a
// word at the end of one page never fuses with the start of the next.
func joinPageBodies(pages []stagedPage) string {
	parts := make([]string, len(pages))
	for i, p := range pages {
		parts[i] = p.body
	}
	return strings.Join(parts, "\n\n")
}

// resolveStagedSource returns the body of a source the ingest staged:
// frontmatter stripped, the form raw.get serves and chunking is counted over.
func resolveStagedSource(stagedDir, p string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(stagedDir, filepath.FromSlash(p)))
	if err != nil {
		return "", false
	}
	rs, err := vault.ParseRawSource(p, b)
	if err != nil {
		return "", false
	}
	return rs.Body, true
}

// resolver returns the ref resolver for score.CheckRefs: a raw/ path
// resolves to its source's body (frontmatter stripped, as lint resolves it),
// a wiki/ path to "exists". An ingest's own staged files shadow the snapshot —
// a page that cites the source it just staged must find it — and an ask has
// no staged dir. A path that is not a local relative path resolves to nothing,
// so a ref can never read outside the two roots.
func (s *scorer) resolver(stagedDir string) func(string) (string, bool) {
	roots := []string{s.snap}
	if stagedDir != "" {
		roots = []string{stagedDir, s.snap}
	}
	return func(p string) (string, bool) {
		if !filepath.IsLocal(filepath.FromSlash(p)) || strings.Contains(p, `\`) {
			return "", false
		}
		for _, root := range roots {
			full := filepath.Join(root, filepath.FromSlash(p))
			info, err := os.Stat(full)
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if !strings.HasPrefix(p, "raw/") {
				return "", true
			}
			key := root + "\x00" + p
			if body, ok := s.bodies[key]; ok {
				return body, true
			}
			b, err := os.ReadFile(full)
			if err != nil {
				continue
			}
			rs, err := vault.ParseRawSource(p, b)
			if err != nil {
				return "", false // a file that is not a raw source is not one
			}
			s.bodies[key] = rs.Body
			return rs.Body, true
		}
		return "", false
	}
}

// loadTurns reads the turns a run recorded, by the ids meta.json lists. A
// turn that cannot be read is an error naming it: scoring around a damaged
// trace would report process numbers for a different run than the one made.
func loadTurns(caseDir string, ids []string) ([]*trace.Turn, error) {
	var turns []*trace.Turn
	for _, id := range ids {
		t, err := trace.Load(filepath.Join(caseDir, "traces"), id)
		if err != nil {
			return nil, fmt.Errorf("turn %s: %w", id, err)
		}
		turns = append(turns, t)
	}
	return turns, nil
}

// processMetrics adds the numbers that say how a run went rather than how
// well. A run with no recorded turn has none of them (tracing off, or lw
// died before the first request) and tokens are absent unless the provider
// reported usage — "unmeasured" must not average in as zero.
func processMetrics(m map[string]float64, turns []*trace.Turn) {
	if len(turns) == 0 {
		return
	}
	p := score.ProcessOf(turns)
	m[MetricRounds] = float64(p.Rounds)
	m[MetricMaxRoundsHit] = float64(p.MaxRoundsHit)
	m[MetricWallS] = float64(p.WallMS) / 1000
	if p.ToolCalls > 0 {
		m[MetricToolErrorRate] = float64(p.ToolErrs) / float64(p.ToolCalls)
	}
	if p.HasUsage {
		m[MetricInputTokens] = float64(p.InputTokens)
		m[MetricCachedTokens] = float64(p.CachedTokens)
		m[MetricOutputTokens] = float64(p.OutputTokens)
		m[MetricReasoningTokens] = float64(p.ReasoningTokens)
	}
}

func boolMetric(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// validShare is valid ÷ total over checked refs; refs must not be empty.
func validShare(refs []score.Ref) float64 {
	valid := 0
	for _, r := range refs {
		if r.Valid {
			valid++
		}
	}
	return float64(valid) / float64(len(refs))
}

// readJSONFile decodes the JSON file at path into v. A missing file keeps
// its fs.ErrNotExist so callers can tell "absent" from "damaged".
func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return nil
}
