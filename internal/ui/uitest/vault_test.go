// vault_test.go pins the public fixture's shape and the changeset
// PublicVault stages on it: the committed vault the header counts (6 pages,
// 1 raw source), the index's entry shape, the four live op kinds in stage
// order, op4's single dropped hunk, clean checks over the full projected
// tree, and OpDiff's window totals — 4 non-dropped, 1 dropped. CopyVault is
// pinned on the same fixture as its source vault.
package uitest

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
)

// TestPublicVaultShape opens the public fixture and asserts everything the
// screens and the conformance gate lean on, all through the engine's public
// surface: Vault().Pages()/RawSources() as the header counts them (the
// COMMITTED vault — the staged page and raw exist only in the projection
// until a commit), Vault().Read for index.md, Current for the staged
// changeset, and OpDiff for the proposed windows.
func TestPublicVaultShape(t *testing.T) {
	const name = "uitest-public"
	v := PublicVault(t, name)

	if got := filepath.Base(v.Root); got != name {
		t.Errorf("PublicVault root = %q, want its base name to be %q (the header shows it)", v.Root, name)
	}

	if got := len(v.Engine.Vault().Pages()); got != 6 {
		t.Errorf("committed vault has %d pages, want 6", got)
	}
	if got := len(v.Engine.Vault().RawSources()); got != 1 {
		t.Errorf("committed vault has %d raw sources, want 1", got)
	}

	// index.md carries at least two entries in the `- [[slug]] — Title`
	// shape, under its section headings.
	idx, err := v.Engine.Vault().Read("index.md")
	if err != nil {
		t.Fatalf("Vault().Read(index.md): %v", err)
	}
	entries := 0
	for _, line := range strings.Split(string(idx), "\n") {
		if strings.HasPrefix(line, "- [[") && strings.Contains(line, "]] — ") {
			entries++
		}
	}
	if entries < 2 {
		t.Errorf("index.md carries %d `- [[slug]] — Title` entries, want at least 2:\n%s", entries, idx)
	}

	// The staged changeset: four live ops, in append order, ids assigned
	// op1..op4 by the engine.
	c, err := v.Engine.Current()
	if err != nil {
		t.Fatalf("Current(): %v", err)
	}
	live := c.Live()
	if len(live) != 4 {
		t.Fatalf("open changeset has %d live ops, want 4", len(live))
	}
	wantKinds := []stage.OpKind{
		stage.OpIngestSource, stage.OpCreatePage, stage.OpPatchPage, stage.OpPatchPage,
	}
	for i, op := range live {
		if wantID := fmt.Sprintf("op%d", i+1); op.ID != wantID {
			t.Errorf("live op %d has id %q, want %q", i, op.ID, wantID)
		}
		if op.Kind != wantKinds[i] {
			t.Errorf("live op %s is %q, want %q", op.ID, op.Kind, wantKinds[i])
		}
	}

	// op4 — the last patch_page — carries exactly one hunk, and that hunk is
	// the one PublicVault dropped. Dropping a hunk never drops its op.
	op4, ok := c.Op("op4")
	if !ok {
		t.Fatalf("changeset has no op4")
	}
	if len(op4.Hunks) != 1 {
		t.Fatalf("op4 carries %d hunks, want 1", len(op4.Hunks))
	}
	if op4.Hunks[0].ID != "h1" || !op4.Hunks[0].Dropped {
		t.Errorf("op4's hunk = id %q dropped %v, want h1 dropped", op4.Hunks[0].ID, op4.Hunks[0].Dropped)
	}
	if op4.State != stage.StateProposed {
		t.Errorf("op4 state = %q, want proposed (DropHunk drops a hunk, not its op)", op4.State)
	}

	// Checks are computed over the FULL projected tree (index.md included),
	// so a fixture that linted dirty would fail here, not downstream.
	wantChecks := stage.Checks{Schema: "pass", Lint: "pass", Orphans: 0, BrokenLinks: 0}
	if c.Checks != wantChecks {
		t.Errorf("checks = %+v, want %+v", c.Checks, wantChecks)
	}

	// OpDiff across the live ops: op1's raw source, op2's created page plus
	// the index.md the engine derives for it, op3's patch — four
	// non-dropped windows in all — and op4's single window, shown as
	// proposed (every hunk applied) but carrying its hunk's dropped flag.
	nonDropped, dropped := 0, 0
	perOp := make(map[string]int, len(live))
	for _, op := range live {
		fds, err := v.Engine.OpDiff(op.ID)
		if err != nil {
			t.Fatalf("OpDiff(%s): %v", op.ID, err)
		}
		for _, fd := range fds {
			for _, dh := range fd.Hunks {
				perOp[op.ID]++
				if dh.Dropped {
					dropped++
					if dh.HunkID != "h1" || op.ID != "op4" || fd.Path != "wiki/entities/banneton.md" {
						t.Errorf("dropped window attributed to op %s hunk %q on %s, want op4 h1 on wiki/entities/banneton.md", op.ID, dh.HunkID, fd.Path)
					}
				} else {
					nonDropped++
				}
			}
		}
	}
	if nonDropped != 4 || dropped != 1 {
		t.Errorf("OpDiff totals: %d non-dropped and %d dropped windows, want 4 and 1 (per op: %v)", nonDropped, dropped, perOp)
	}
	for opID, want := range map[string]int{"op1": 1, "op2": 2, "op3": 1, "op4": 1} {
		if perOp[opID] != want {
			t.Errorf("OpDiff(%s) has %d windows, want %d (op2's second is the derived index.md)", opID, perOp[opID], want)
		}
	}
}

// TestCopyVault copies the public fixture as a stand-in for an existing
// vault directory and pins that the copy — not the original — is opened:
// same page and raw counts, engine state created under the copy and never
// under the source, and no changeset staged on the bare vault.
func TestCopyVault(t *testing.T) {
	src, err := filepath.Abs(fixtureVaultDir)
	if err != nil {
		t.Fatalf("Abs(%s): %v", fixtureVaultDir, err)
	}
	v := CopyVault(t, fixtureVaultDir)

	if got := filepath.Base(v.Root); got != "vault" {
		t.Errorf("CopyVault root = %q, want its base name to be the source's: %q", v.Root, got)
	}
	if !filepath.IsAbs(v.Root) || v.Root == src {
		t.Errorf("CopyVault root %q, want an absolute path distinct from the source %q", v.Root, src)
	}
	if got := len(v.Engine.Vault().Pages()); got != 6 {
		t.Errorf("copied vault has %d pages, want 6", got)
	}
	if got := len(v.Engine.Vault().RawSources()); got != 1 {
		t.Errorf("copied vault has %d raw sources, want 1", got)
	}
	if _, err := v.Engine.Current(); !errors.Is(err, stage.ErrNoChangeset) {
		t.Errorf("Current() on a bare copy = %v, want ErrNoChangeset", err)
	}

	// The engine's state lands in the copy; the source vault is never
	// written to — CopyVault is what keeps $LW_MOCKUP_VAULT pristine.
	if _, err := os.Stat(filepath.Join(v.Root, ".llmwiki")); err != nil {
		t.Errorf("no engine state under the copy %s: %v", v.Root, err)
	}
	if _, err := os.Stat(filepath.Join(src, ".llmwiki")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("source vault %s grew engine state (err %v), want it untouched", src, err)
	}
}

// TestPublicVaultDeterministic pins C24's whole point (contract §6 note 2
// as amended): two PublicVault calls — different temp dirs, different
// names — stage the SAME changeset id at the SAME OpenedAt, and every
// journal event either wrote carries the fixed clock's TS. The id shows
// in the shell header (cs9) and the timestamps in the Log screen, so
// without this the screens' goldens would differ on every run. The journal
// is read through Engine.Journal().Query, the public path the Log screen
// itself uses.
func TestPublicVaultDeterministic(t *testing.T) {
	fixed := testutil.FixedClock()

	var firstID string
	for i, name := range []string{"deterministic-a", "deterministic-b"} {
		v := PublicVault(t, name)

		c, err := v.Engine.Current()
		if err != nil {
			t.Fatalf("PublicVault(%q): Current: %v", name, err)
		}
		if i == 0 {
			firstID = c.ID
		} else if c.ID != firstID {
			t.Errorf("second PublicVault changeset id = %q, want the first call's %q", c.ID, firstID)
		}
		if !c.OpenedAt.Equal(fixed()) {
			t.Errorf("PublicVault(%q): OpenedAt = %s, want the fixed clock %s", name, c.OpenedAt, fixed())
		}

		events, err := v.Engine.Journal().Query(stage.Filter{})
		if err != nil {
			t.Fatalf("PublicVault(%q): Journal().Query: %v", name, err)
		}
		if len(events) == 0 {
			t.Fatalf("PublicVault(%q): journal has no events, want the changeset_opened one at least", name)
		}
		for _, ev := range events {
			if !ev.TS.Equal(fixed()) {
				t.Errorf("PublicVault(%q): journal event %s TS = %s, want the fixed clock %s", name, ev.Kind, ev.TS, fixed())
			}
		}
	}
}
