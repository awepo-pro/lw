// validate.go implements ValidateOp (backbone §5.5): every malformation
// bullet in that Contract, plus the schema's per-kind required set the
// bullets themselves never name (MASTER §9 D-BH).
package stage

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/awepo-pro/lw/internal/vault"
)

// vaultPathFilenameRE matches a filename in canonical lowercase-hyphen
// form: one or more lowercase-alphanumeric segments joined by single
// hyphens, then ".md" — the same shape internal/lint's path-convention
// check enforces as a warning; ValidateOp enforces it as a hard rejection
// at proposal time (backbone §5.5's first bullet, /PLAN.md §8).
var vaultPathFilenameRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*\.md$`)

// vaultPathSegmentRE matches one lowercase-hyphen directory segment.
var vaultPathSegmentRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// validVaultPath reports whether p is vault-relative, slash-separated, and
// every segment is lowercase-hyphen, with the final segment ending ".md".
func validVaultPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
		return false
	}
	segs := strings.Split(p, "/")
	for i, seg := range segs {
		if seg == "" {
			return false
		}
		if i == len(segs)-1 {
			if !vaultPathFilenameRE.MatchString(seg) {
				return false
			}
		} else if !vaultPathSegmentRE.MatchString(seg) {
			return false
		}
	}
	return true
}

// requireValidPath returns an ErrValidation-wrapping error naming field
// when p is not a valid vault-relative lowercase-hyphen.md path.
func requireValidPath(field, p string) error {
	if !validVaultPath(p) {
		return fmt.Errorf("%w: %s %q is not a vault-relative, slash-separated, lowercase-hyphen.md path", ErrValidation, field, p)
	}
	return nil
}

// validateHunks checks every hunk in hunks carries a non-empty Path and ID
// (backbone §5.3/§5.5 Contract, MASTER §9 D-BH: neither carries omitempty,
// so an unset one emits "" and fails its own subschema even though the key
// itself is optional).
func validateHunks(hunks []Hunk) error {
	for _, h := range hunks {
		if h.Path == "" {
			return fmt.Errorf("%w: hunk %q: path is required", ErrValidation, h.ID)
		}
		if h.ID == "" {
			return fmt.Errorf("%w: hunk with path %q: id is required", ErrValidation, h.Path)
		}
	}
	return nil
}

// ValidateOp checks op against the backbone §5.5 Contract: every
// malformation named there, plus every key the schema's per-kind required
// set demands but no bullet names (MASTER §9 D-BH). Failures wrap
// ErrValidation.
//
// A patch_page whose Path is an EXACT, case-sensitive match for one of
// OQ-9's four named vault-root files is dispatched to validatePatchPage
// here, before the generic path-shape gate below (S4-T0 repair-1,
// secondary fix; rootfile.go's isKnownRootFile). Without this,
// SCHEMA.md never reaches validatePatchPage's non-Page branch through
// this public entry point at all: vaultPathFilenameRE requires lowercase
// and SCHEMA.md is not, so requireValidPath rejects it first with an
// unrelated "not a vault-relative... path" message, and OQ-9's
// SCHEMA.md-specific "it is the rules the validator reads" refusal
// becomes unreachable except by calling validatePatchPage directly (as
// rootfile_test.go's TestPatchRootFileRefusesLogAndSchema already does).
// This is a narrow, enumerable four-name switch, checked by exact string
// equality — it does not loosen validVaultPath or vaultPathFilenameRE for
// any other path any op kind could ever name.
func ValidateOp(op Op, v *vault.Vault, s *vault.Schema) error {
	if op.Kind == OpPatchPage && isKnownRootFile(op.Path) {
		if err := validateHunks(op.Hunks); err != nil {
			return err
		}
		return validatePatchPage(op, v, s)
	}

	if op.Path != "" {
		if err := requireValidPath("path", op.Path); err != nil {
			return err
		}
	}
	if op.From != "" {
		if err := requireValidPath("from", op.From); err != nil {
			return err
		}
	}
	if op.To != "" {
		if err := requireValidPath("to", op.To); err != nil {
			return err
		}
	}
	for _, src := range op.Sources {
		if err := requireValidPath("sources", src); err != nil {
			return err
		}
	}
	if err := validateHunks(op.Hunks); err != nil {
		return err
	}

	switch op.Kind {
	case OpIngestSource:
		return validateIngestSource(op, v)
	case OpCreatePage:
		return validateCreatePage(op, v, s)
	case OpPatchPage:
		return validatePatchPage(op, v, s)
	case OpRenamePage:
		return validateRenamePage(op, v, s)
	case OpMergePages:
		return validateMergePages(op, v, s)
	case OpSplitPage:
		return validateSplitPage(op, v)
	case OpAddLink:
		return validateAddLink(op, v)
	case OpRetract:
		return validateRetract(op, v)
	default:
		return fmt.Errorf("%w: op kind %q is not one of the taxonomy's eight kinds", ErrValidation, op.Kind)
	}
}

// validateIngestSource enforces: path under raw/, does not already exist,
// its sha not already present in any raw source (dedupe by hash), and a
// non-empty Extractor (the schema's required set, D-BH).
func validateIngestSource(op Op, v *vault.Vault) error {
	if !strings.HasPrefix(op.Path, "raw/") {
		return fmt.Errorf("%w: ingest_source: path %q must be under raw/", ErrValidation, op.Path)
	}
	if v.Exists(op.Path) {
		return fmt.Errorf("%w: ingest_source: %s already exists", ErrValidation, op.Path)
	}
	if op.Extractor == "" {
		return fmt.Errorf("%w: ingest_source: extractor is required", ErrValidation)
	}
	sha := vault.BodySHA256(string(op.Content))
	for _, r := range v.RawSources() {
		if r.SHA256 == sha {
			return fmt.Errorf("%w: ingest_source: content already ingested at %s (sha256 %s); dedupe by hash", ErrValidation, r.Path, sha)
		}
	}
	return nil
}

// validateCreatePage enforces: path does not exist; frontmatter valid
// against the schema; at least 2 outbound wikilinks; type matches the
// directory; plus the schema's required Rationale and Provenance (D-BH).
// The three content-dependent bullets read op.Content (D-AY).
func validateCreatePage(op Op, v *vault.Vault, s *vault.Schema) error {
	if v.Exists(op.Path) {
		return fmt.Errorf("%w: create_page: %s already exists", ErrValidation, op.Path)
	}
	if other, ok := basenameCollision(v, op.Path); ok {
		return fmt.Errorf("%w: create_page: %s collides with the existing %s: both answer the bare wikilink [[%s]], which makes it ambiguous and resolve to nothing everywhere in the vault. Give the page a distinct name",
			ErrValidation, op.Path, other, strings.TrimSuffix(path.Base(op.Path), ".md"))
	}
	if op.Rationale == "" {
		return fmt.Errorf("%w: create_page: rationale is required", ErrValidation)
	}
	if len(op.Provenance) < 1 {
		return fmt.Errorf("%w: create_page: at least one provenance entry is required", ErrValidation)
	}

	page, err := vault.ParsePage(op.Path, op.Content)
	if err != nil {
		return fmt.Errorf("%w: create_page: %s: %v", ErrValidation, op.Path, err)
	}
	if err := page.FM.Validate(s); err != nil {
		return fmt.Errorf("%w: create_page: %v", ErrValidation, err)
	}
	if len(page.Links) < 2 {
		return fmt.Errorf("%w: create_page: %s has fewer than 2 outbound wikilinks", ErrValidation, op.Path)
	}
	wantDir := page.FM.Type.Dir()
	if wantDir == "" || path.Dir(op.Path) != wantDir {
		return fmt.Errorf("%w: create_page: %s is under %s but type %s belongs under %s", ErrValidation, op.Path, path.Dir(op.Path), page.FM.Type, wantDir)
	}

	// OQ-9 L2's mirror direction (S4-T0 repair-1): op is well-formed;
	// refuse if its index.md derivation collides with a live root-file
	// patch_page already open in this same changeset. index.md is not
	// allow-listed in phase 1 (TestPatchRootFileRefusesIndexInPhase1), so
	// this never fires end-to-end yet — it is unit-tested directly
	// (TestOneWriterGuardReverseOrderCreateDerivation) so S4-T7 only has
	// to add "index.md" to patchableRootFiles.
	return checkNewWriterOneWriter(v, op)
}

// basenameCollision returns an existing page whose basename equals p's —
// case-insensitively, ".md" stripped — which is exactly the condition that
// makes vault.Resolve's bare-basename step ambiguous (backbone §2.9).
//
// Ambiguity there resolves to NOTHING rather than to a wrong answer, so a
// second wiki/entities/kv-cache.md beside wiki/concepts/kv-cache.md does
// not merely shadow the original: every [[kv-cache]] anywhere in the vault
// stops resolving. Measured on the minimal fixture at 5 lint errors for a
// single such create.
//
// Pre-existing behaviour of Resolve, not of this package (MASTER §11,
// carried out of S2-T7). It is caught here, at proposal time, because the
// alternative is a reviewer working backwards to the cause from five
// broken-link errors in unrelated files — and because S3-T2 hands the
// proposing job to a model, for which a validation message it can act on
// is the whole error convention (backbone §6).
func basenameCollision(v *vault.Vault, p string) (string, bool) {
	want := strings.ToLower(strings.TrimSuffix(path.Base(p), ".md"))
	for _, pg := range v.Pages() {
		if pg.Path == p {
			continue
		}
		if strings.ToLower(strings.TrimSuffix(path.Base(pg.Path), ".md")) == want {
			return pg.Path, true
		}
	}
	return "", false
}

// validatePatchPage enforces: path exists; the named section exists
// (except append_section on a new trailing section — recognized here by
// the section existing in the proposed post-image even though it does not
// exist yet in the current one); Before matches the current sha. A
// top-level patch_page must name a Section (D-BD); a cascade sub-op may
// leave it empty.
func validatePatchPage(op Op, v *vault.Vault, s *vault.Schema) error {
	page, ok := v.Page(op.Path)
	if !ok {
		// op.Path is not a vault.Page: either an OQ-9 allow-listed
		// vault-root bookkeeping file (curator-memory.md in this phase;
		// index.md, log.md and SCHEMA.md are read by the vault — §2.8 —
		// but carry no page/frontmatter structure) or a path that plain
		// does not exist. rootfile.go (S4-T0) routes the former through
		// OQ-9's allow-list instead of a bare "does not exist".
		return validateNonPagePatch(op, v)
	}
	if op.Section != "" {
		if _, ok := page.Section(op.Section); !ok {
			if !sectionInProposedContent(op) {
				return fmt.Errorf("%w: patch_page: %s: section %q does not exist", ErrValidation, op.Path, op.Section)
			}
		}
	}
	if op.Before != page.SHA256() {
		return fmt.Errorf("%w: patch_page: %s: before does not match the current content", ErrValidation, op.Path)
	}
	if op.Content != nil {
		if newPage, err := vault.ParsePage(op.Path, op.Content); err == nil {
			if err := newPage.FM.Validate(s); err != nil {
				return fmt.Errorf("%w: patch_page: %v", ErrValidation, err)
			}
		}
	}
	return nil
}

// sectionInProposedContent reports whether op.Section names a section that
// exists in op.Content once parsed — the append_section-on-a-new-trailing-
// section exception backbone §5.5 carves out of "the named section exists".
func sectionInProposedContent(op Op) bool {
	if op.Content == nil {
		return false
	}
	newPage, err := vault.ParsePage(op.Path, op.Content)
	if err != nil {
		return false
	}
	_, ok := newPage.Section(op.Section)
	return ok
}

// validateRenamePage enforces: source exists, destination does not, and
// Cascade covers every inbound backlink — an incomplete cascade is
// rejected (backbone §5.5).
func validateRenamePage(op Op, v *vault.Vault, s *vault.Schema) error {
	if _, ok := v.Page(op.From); !ok {
		return fmt.Errorf("%w: rename_page: source %s does not exist", ErrValidation, op.From)
	}
	if v.Exists(op.To) {
		return fmt.Errorf("%w: rename_page: destination %s already exists", ErrValidation, op.To)
	}
	return validateCascade(op.Cascade, v, s, []string{op.From}, op)
}

// validateMergePages enforces: Sources lists >= 2 existing pages, To is
// the destination, and every inbound backlink to a merged page is
// cascaded — the same completeness rule as rename_page (backbone §5.5,
// MASTER §9 D-BD/D-AK).
func validateMergePages(op Op, v *vault.Vault, s *vault.Schema) error {
	if len(op.Sources) < 2 {
		return fmt.Errorf("%w: merge_pages: sources must list at least 2 pages", ErrValidation)
	}
	for _, src := range op.Sources {
		if _, ok := v.Page(src); !ok {
			return fmt.Errorf("%w: merge_pages: source %s does not exist", ErrValidation, src)
		}
	}
	if op.To == "" {
		return fmt.Errorf("%w: merge_pages: to is required", ErrValidation)
	}
	return validateCascade(op.Cascade, v, s, op.Sources, op)
}

// validateCascade checks cascade covers every page cascadeLinkingPages
// expects for froms (backbone §5.5's completeness rule, MASTER §9 D-AM,
// D-BE, D-BD — a cascade may legally be empty when the expected set is)
// and that every entry it does carry is a well-formed patch_page rewrite,
// then — OQ-9 L2's mirror direction, S4-T0 repair-1 — refuses writer (the
// full rename_page/merge_pages op cascade belongs to) if its cascade
// collides with a live root-file patch_page already open in this same
// changeset. This is the tighter seam than hooking validateRenamePage and
// validateMergePages separately: both funnel every cascade through here,
// so the mirror check is wired once rather than duplicated at both call
// sites.
func validateCascade(cascade []Op, v *vault.Vault, s *vault.Schema, froms []string, writer Op) error {
	expected := cascadeLinkingPages(v, froms)
	have := map[string]bool{}
	for _, sub := range cascade {
		have[sub.Path] = true
	}
	var missing []string
	for _, p := range expected {
		if !have[p] {
			missing = append(missing, p)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("%w: cascade is incomplete; missing rewrites for %s", ErrValidation, strings.Join(missing, ", "))
	}

	for _, sub := range cascade {
		if sub.Kind != OpPatchPage {
			return fmt.Errorf("%w: cascade entry %s: kind must be patch_page", ErrValidation, sub.Path)
		}
		if err := requireValidPath("cascade path", sub.Path); err != nil {
			return err
		}
		if err := validateHunks(sub.Hunks); err != nil {
			return err
		}
	}

	return checkNewWriterOneWriter(v, writer)
}

// validateSplitPage enforces: Path exists; Sources lists the >= 2
// resulting page paths (backbone §5.5).
func validateSplitPage(op Op, v *vault.Vault) error {
	if _, ok := v.Page(op.Path); !ok {
		return fmt.Errorf("%w: split_page: %s does not exist", ErrValidation, op.Path)
	}
	if len(op.Sources) < 2 {
		return fmt.Errorf("%w: split_page: sources must list at least 2 resulting pages", ErrValidation)
	}
	return nil
}

// validateAddLink enforces: both endpoints exist — never propose a broken
// link (backbone §5.5).
func validateAddLink(op Op, v *vault.Vault) error {
	if _, ok := v.Page(op.From); !ok {
		return fmt.Errorf("%w: add_link: %s does not exist", ErrValidation, op.From)
	}
	if _, ok := v.Page(op.To); !ok {
		return fmt.Errorf("%w: add_link: %s does not exist", ErrValidation, op.To)
	}
	return nil
}

// validateRetract enforces: path exists; a non-empty reason (backbone
// §5.5 — carried on Op.Rationale, the schema's field for it).
func validateRetract(op Op, v *vault.Vault) error {
	if _, ok := v.Page(op.Path); !ok {
		return fmt.Errorf("%w: retract: %s does not exist", ErrValidation, op.Path)
	}
	if op.Rationale == "" {
		return fmt.Errorf("%w: retract: rationale is required", ErrValidation)
	}
	return nil
}
