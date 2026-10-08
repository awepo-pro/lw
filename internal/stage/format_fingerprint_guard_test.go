// format_fingerprint_guard_test.go tests the two rules around the golden file
// that make TestFormatFingerprint more than advice (042, review of S2):
//
//   - -update may not launder a shape change. The golden carries the
//     FormatVersion it was generated under; planFingerprintUpdate refuses to
//     rewrite it when the shape moved and the version did not. Without this,
//     "go test -update" was one keystroke away from a passing test and an
//     unbumped format.
//   - the failure text points at the right fix. A new per-PC file under
//     .llmwiki/ changes only the [layout] section and wants an ignore-list
//     entry, not a format bump (fingerprintFailure).
package stage_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// guardBody and guardBody2 stand in for the rendered sections below the
// header: the guard compares them as opaque text, so tiny strings keep the
// cases readable.
const (
	guardBody  = "\n[types]\nstage.Hunk: ID(id,string)\n\n[layout]\n.llmwiki/journal.ndjson\n"
	guardBody2 = "\n[types]\nstage.Hunk: ID(id,string) Note(note,string)\n\n[layout]\n.llmwiki/journal.ndjson\n"
)

// TestFingerprintUpdateGuard drives planFingerprintUpdate through every
// branch of the -update rule.
func TestFingerprintUpdateGuard(t *testing.T) {
	const stillNotBumped = "the vault's on-disk shape changed but stage.FormatVersion is still %d: bump it (and add a migration) before running -update"

	tests := []struct {
		name    string
		old     string // the golden file as it is on disk; "" = none yet
		body    string // the shape rendered now
		version int    // stage.FormatVersion now
		want    string // the golden to write, when wantErr == ""
		wantErr string
	}{
		{
			name:    "unchanged shape, unchanged version rewrites the same bytes",
			old:     fingerprintHeader(1) + guardBody,
			body:    guardBody,
			version: 1,
			want:    fingerprintHeader(1) + guardBody,
		},
		{
			name:    "changed shape, version not bumped is refused",
			old:     fingerprintHeader(1) + guardBody,
			body:    guardBody2,
			version: 1,
			wantErr: strings.Replace(stillNotBumped, "%d", "1", 1),
		},
		{
			name:    "changed shape, version bumped writes the new golden with the new header",
			old:     fingerprintHeader(1) + guardBody,
			body:    guardBody2,
			version: 2,
			want:    fingerprintHeader(2) + guardBody2,
		},
		{
			name:    "the refusal names the golden's version, not a guess",
			old:     fingerprintHeader(4) + guardBody,
			body:    guardBody2,
			version: 4,
			wantErr: strings.Replace(stillNotBumped, "%d", "4", 1),
		},
		{
			name:    "a version lowered below the golden's does not pass a changed shape",
			old:     fingerprintHeader(3) + guardBody,
			body:    guardBody2,
			version: 2,
			wantErr: strings.Replace(stillNotBumped, "%d", "3", 1),
		},
		{
			name:    "unchanged shape with a bumped version just updates the header",
			old:     fingerprintHeader(1) + guardBody,
			body:    guardBody,
			version: 2,
			want:    fingerprintHeader(2) + guardBody,
		},
		{
			name:    "no golden yet is generated at the current version",
			old:     "",
			body:    guardBody,
			version: 1,
			want:    fingerprintHeader(1) + guardBody,
		},
		{
			name:    "a golden with no format-version line is refused rather than guessed",
			old:     "# hand edited\n" + guardBody,
			body:    guardBody,
			version: 1,
			wantErr: "has no format-version line",
		},
		{
			name:    "a golden whose format-version is not a number is refused",
			old:     "format-version: one\n" + guardBody,
			body:    guardBody,
			version: 1,
			wantErr: "format-version",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := planFingerprintUpdate(tt.old, tt.body, tt.version)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("planFingerprintUpdate wrote a golden (%q); want an error containing %q", got, tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
				}
				// The frozen sentence is matched whole, not by substring.
				if strings.HasPrefix(tt.wantErr, "the vault's") && err.Error() != tt.wantErr {
					t.Errorf("error = %q, want exactly %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("planFingerprintUpdate: %v", err)
			}
			if got != tt.want {
				t.Errorf("golden =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

// TestFingerprintHeaderIsParsedBack: whatever header the generator writes,
// the guard reads the same version and body back. A generator and a parser
// that drift apart would make every -update fail, or none.
func TestFingerprintHeaderIsParsedBack(t *testing.T) {
	for _, v := range []int{1, 2, 10} {
		version, body, err := splitFingerprint(fingerprintHeader(v) + guardBody)
		if err != nil || version != v || body != guardBody {
			t.Errorf("splitFingerprint(header(%d)+body) = %d, %q, %v; want %d, the body, nil", v, version, body, err, v)
		}
	}
}

// TestFingerprintFailureHint: the failure always opens with the frozen
// sentence; when only the [layout] section moved it adds the second line that
// sends a per-PC file to vaultsync.Ignore; any other change gets no hint.
func TestFingerprintFailureHint(t *testing.T) {
	const hint = "if the new path is per-PC state, add it to vaultsync.Ignore instead (042 D2)"
	base := fingerprintHeader(1) + guardBody

	tests := []struct {
		name     string
		got      string
		wantHint bool
	}{
		{"only the layout section differs", fingerprintHeader(1) + strings.Replace(guardBody, ".llmwiki/journal.ndjson", ".llmwiki/journal.ndjson\n.llmwiki/review-cache.json", 1), true},
		{"only the types section differs", fingerprintHeader(1) + guardBody2, false},
		{"types and layout both differ", fingerprintHeader(1) + strings.Replace(guardBody2, ".llmwiki/journal.ndjson", ".llmwiki/other", 1), false},
		{"only the format-version header differs", fingerprintHeader(2) + guardBody, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines := strings.Split(fingerprintFailure(base, tt.got), "\n")
			if lines[0] != fingerprintMismatch {
				t.Errorf("first line = %q, want the frozen sentence %q", lines[0], fingerprintMismatch)
			}
			hasHint := len(lines) > 1 && lines[1] == hint
			if hasHint != tt.wantHint {
				t.Errorf("second line = %q; hint present = %v, want %v", lines[1], hasHint, tt.wantHint)
			}
			if !tt.wantHint && strings.Contains(strings.Join(lines, "\n"), hint) {
				t.Errorf("failure carries the per-PC hint although more than the layout changed:\n%s", strings.Join(lines, "\n"))
			}
		})
	}
}

// TestFingerprintUpdateWritesGolden runs the file-level half of -update on a
// scratch path: a first run creates the file, a refused run leaves it
// byte-for-byte alone, an allowed run rewrites it with the new header.
func TestFingerprintUpdateWritesGolden(t *testing.T) {
	path := filepath.Join(t.TempDir(), "testdata", "format-fingerprint.txt")
	read := func() string {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	if err := updateFingerprintGolden(path, guardBody, 1); err != nil {
		t.Fatalf("first -update: %v", err)
	}
	if got, want := read(), fingerprintHeader(1)+guardBody; got != want {
		t.Fatalf("first golden =\n%s\nwant\n%s", got, want)
	}

	err := updateFingerprintGolden(path, guardBody2, 1)
	if err == nil || !strings.Contains(err.Error(), "is still 1: bump it") {
		t.Fatalf("-update of a changed shape at the same version = %v, want the refusal", err)
	}
	if got, want := read(), fingerprintHeader(1)+guardBody; got != want {
		t.Errorf("a refused -update changed the golden:\n%s", got)
	}

	if err := updateFingerprintGolden(path, guardBody2, 2); err != nil {
		t.Fatalf("-update after a bump: %v", err)
	}
	if got, want := read(), fingerprintHeader(2)+guardBody2; got != want {
		t.Errorf("golden after the bump =\n%s\nwant\n%s", got, want)
	}
}
