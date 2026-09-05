package stage

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNewChangesetIDDeterministic proves the clock and the entropy source
// are truly injected: fixing both must produce the same id twice
// (00-conventions.md §3).
func TestNewChangesetIDDeterministic(t *testing.T) {
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)
	entropy := []byte{1, 2, 3, 4, 5, 6, 7, 8}

	id1, err := newChangesetID(fixed, bytes.NewReader(entropy))
	if err != nil {
		t.Fatalf("newChangesetID (1st): %v", err)
	}
	id2, err := newChangesetID(fixed, bytes.NewReader(entropy))
	if err != nil {
		t.Fatalf("newChangesetID (2nd): %v", err)
	}
	if id1 != id2 {
		t.Fatalf("newChangesetID with a fixed clock and reader produced %q then %q, want equal", id1, id2)
	}
	if !changesetIDPattern.MatchString(id1) {
		t.Fatalf("id %q does not match ^cs-([0-9a-f]{7}|[0-9a-f]{16})$", id1)
	}

	// A different entropy stream at the same instant must change the id.
	id3, err := newChangesetID(fixed, bytes.NewReader([]byte{8, 7, 6, 5, 4, 3, 2, 1}))
	if err != nil {
		t.Fatalf("newChangesetID (3rd): %v", err)
	}
	if id3 == id1 {
		t.Fatalf("newChangesetID ignored the entropy source: got %q for two different readers", id3)
	}
}

// TestNewChangesetIDIs16Hex pins MASTER §9 D-CI's Part A: newChangesetID
// now truncates to 16 hex characters (64 bits), not 7 (28 bits), and the
// result matches changesetIDPattern.
//
// (TestNewChangesetIDUnique, which used to live here, is deleted — not
// loosened — per .dev-notes/issues/OQ-11-changeset-id-collision/02-solution.md
// §6: it drew 1000 ids from real entropy at a 28-bit width and asserted no
// two collided, a property the code never promised. That birthday-bound
// assertion failed about 1 run in 538 even on correct code (measured: 3
// failures in 2000 runs on pre-fix main). This test and
// TestChangesetIDPatternAcceptsBothWidths below replace it deterministically.)
func TestNewChangesetIDIs16Hex(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	id, err := newChangesetID(now, rand.Reader)
	if err != nil {
		t.Fatalf("newChangesetID: %v", err)
	}
	const wantLen = len("cs-") + 16
	if len(id) != wantLen {
		t.Fatalf("newChangesetID = %q (len %d), want length %d (16 hex chars)", id, len(id), wantLen)
	}
	if !changesetIDPattern.MatchString(id) {
		t.Fatalf("id %q does not match changesetIDPattern", id)
	}
}

// TestChangesetIDPatternAcceptsBothWidths pins MASTER §9 D-CI's Part C:
// changesetIDPattern must accept both the current 16-hex width and the
// legacy 7-hex width forever, and reject anything else — in particular
// widths one character short or long of either legal form, which a naive
// "at least N hex" pattern would wrongly admit.
func TestChangesetIDPatternAcceptsBothWidths(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"cs-0193f2a", true},            // legacy 7-hex (D-H)
		{"cs-3f0a91c2b7d45e68", true},   // current 16-hex (D-CI)
		{"cs-0193f2", false},            // 6 hex — one short of legacy
		{"cs-0193f2a1", false},          // 8 hex — one long of legacy
		{"cs-3f0a91c2b7d45e6", false},   // 15 hex — one short of current
		{"cs-3f0a91c2b7d45e681", false}, // 17 hex — one long of current
		{"cs-3F0a91c2b7d45e68", false},  // uppercase hex is not legal
		{"cs-", false},                  // empty
		{"3f0a91c2b7d45e68", false},     // missing "cs-" prefix
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			if got := changesetIDPattern.MatchString(tt.id); got != tt.want {
				t.Fatalf("changesetIDPattern.MatchString(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestNewChangesetIDShortRead(t *testing.T) {
	if _, err := newChangesetID(time.Now(), bytes.NewReader([]byte{1, 2, 3})); err == nil {
		t.Fatal("newChangesetID with too little entropy returned no error")
	}
}

func TestNextCommitIDAbsentDir(t *testing.T) {
	dir := t.TempDir()
	id, err := nextCommitID(filepath.Join(dir, "does-not-exist"))
	if err != nil {
		t.Fatalf("nextCommitID on an absent directory: %v", err)
	}
	if id != "000001" {
		t.Fatalf("nextCommitID(absent) = %q, want %q", id, "000001")
	}
}

func TestNextCommitIDEmptyDir(t *testing.T) {
	dir := t.TempDir()
	id, err := nextCommitID(dir)
	if err != nil {
		t.Fatalf("nextCommitID on an empty directory: %v", err)
	}
	if id != "000001" {
		t.Fatalf("nextCommitID(empty) = %q, want %q", id, "000001")
	}
}

func TestNextCommitIDHighestPlusOne(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"000001.tree", "000002.tree", "000042.tree", "not-a-tree.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	id, err := nextCommitID(dir)
	if err != nil {
		t.Fatalf("nextCommitID: %v", err)
	}
	if id != "000043" {
		t.Fatalf("nextCommitID = %q, want %q", id, "000043")
	}
}
