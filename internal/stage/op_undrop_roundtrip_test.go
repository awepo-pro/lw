package stage

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestUndropRoundTripMockupVault asserts the same drop -> undrop
// byte-identity as TestUndropRestoresProposal, against the private mockup
// vault's op3 and op4 — the exact reproduction in issue C-131. Skipped
// unless LW_MOCKUP_VAULT is set. Their OpDiff headers must still be
// @@ -34,6 +34,10 @@ and @@ -21,6 +21,10 @@ after the round trip: the
// same frozen headers TestOpDiffMockupVault asserts for the never-dropped
// case, now proven to survive a drop and an undrop too.
func TestUndropRoundTripMockupVault(t *testing.T) {
	src := os.Getenv("LW_MOCKUP_VAULT")
	if src == "" {
		t.Skip("LW_MOCKUP_VAULT not set: this conformance check runs locally against the private mockup vault")
	}

	dst := filepath.Join(t.TempDir(), "ml-notes")
	if err := copyDirRecursive(src, dst); err != nil {
		t.Fatalf("copy mockup vault: %v", err)
	}

	e, err := OpenEngine(dst)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	t.Cleanup(func() { e.Close() })
	e.now = testutil.FixedClock()

	cases := []struct {
		op         string
		wantHeader string
	}{
		{"op3", "@@ -34,6 +34,10 @@"},
		{"op4", "@@ -21,6 +21,10 @@"},
	}

	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			c, err := e.Current()
			if err != nil {
				t.Fatalf("Current: %v", err)
			}
			op, ok := c.Op(tc.op)
			if !ok {
				t.Fatalf("no such op %s", tc.op)
			}
			if len(op.Hunks) != 1 {
				t.Fatalf("%s has %d hunks, want exactly 1 for this fixture", tc.op, len(op.Hunks))
			}
			hunkID := op.Hunks[0].ID

			want := currentOpAfterBytes(t, e, tc.op)

			if err := e.DropHunk(tc.op, hunkID); err != nil {
				t.Fatalf("DropHunk(%s,%s): %v", tc.op, hunkID, err)
			}
			if err := e.UndropHunk(tc.op, hunkID); err != nil {
				t.Fatalf("UndropHunk(%s,%s): %v", tc.op, hunkID, err)
			}

			got := currentOpAfterBytes(t, e, tc.op)
			if string(got) != string(want) {
				t.Fatalf("%s: drop+undrop did not restore the proposed After byte-for-byte", tc.op)
			}

			fods, err := e.OpDiff(tc.op)
			if err != nil {
				t.Fatalf("OpDiff(%s): %v", tc.op, err)
			}
			if len(fods) == 0 || len(fods[0].Hunks) == 0 {
				t.Fatalf("OpDiff(%s) produced no windows: %+v", tc.op, fods)
			}
			if gotHeader := fods[0].Hunks[0].Header; gotHeader != tc.wantHeader {
				t.Fatalf("OpDiff(%s) header after round trip = %q, want %q", tc.op, gotHeader, tc.wantHeader)
			}
		})
	}
}

// fencedSetupFixtureBefore is a standalone page (written directly into a
// private copy of "minimal", never into spec/fixtures/ itself) whose target
// section, "## Setup", contains a fenced ```bash block with a shell
// comment "# install the tool" inside it — the exact shape repair-1's
// orchestrator probe found misread as a level-1 heading
// (runs/T15-stage-undrop-anchor/orchestrator-probe.log), which made
// insertAtSectionEnd splice new content INSIDE the fence instead of at the
// real end of "## Setup".
const fencedSetupFixtureBefore = "---\n" +
	"title: Fenced Setup Fixture\n" +
	"created: 2026-08-29\n" +
	"updated: 2026-08-29\n" +
	"type: concept\n" +
	"tags: [inference]\n" +
	"confidence: medium\n" +
	"---\n" +
	"\n" +
	"# Fenced Setup Fixture\n" +
	"\n" +
	"Intro paragraph linking to [[kv-cache]] and [[gpt-4]].\n" +
	"\n" +
	"## Setup\n" +
	"\n" +
	"Run this:\n" +
	"\n" +
	"```bash\n" +
	"# install the tool\n" +
	"make install\n" +
	"```\n" +
	"\n" +
	"More setup prose.\n" +
	"\n" +
	"## Next\n" +
	"\n" +
	"Other text.\n"

// fencedSetupFixtureAfter is fencedSetupFixtureBefore with a hunk proposing
// "### Added under Setup" at the end of "## Setup" already applied: after
// "More setup prose." (the section's real last content line), before
// "## Next" — and, critically, NOT inside the ```bash fence. Hand-built
// independently of insertAtSectionEnd, so the test is not circular.
const fencedSetupFixtureAfter = "---\n" +
	"title: Fenced Setup Fixture\n" +
	"created: 2026-08-29\n" +
	"updated: 2026-08-29\n" +
	"type: concept\n" +
	"tags: [inference]\n" +
	"confidence: medium\n" +
	"---\n" +
	"\n" +
	"# Fenced Setup Fixture\n" +
	"\n" +
	"Intro paragraph linking to [[kv-cache]] and [[gpt-4]].\n" +
	"\n" +
	"## Setup\n" +
	"\n" +
	"Run this:\n" +
	"\n" +
	"```bash\n" +
	"# install the tool\n" +
	"make install\n" +
	"```\n" +
	"\n" +
	"More setup prose.\n" +
	"\n" +
	"### Added under Setup\n" +
	"\n" +
	"New content.\n" +
	"\n" +
	"## Next\n" +
	"\n" +
	"Other text.\n"

// assertLines fails t with a readable diff when got != want.
func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lines mismatch:\n--- got ---\n%s\n--- want ---\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
