// quaternion_render_test.go pins fix-wave 2 (A15-3) against the real
// corpus: the math-dense region of raw/articles/quaternion.md — the page
// from the user's failure screenshot — renders with every balanced plain
// span converted (frozen goldens), and no raw dollar pair or script
// fallback shape survives the pipeline.
package markdown

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/testutil"
)

// readQuaternionRaw loads the frozen fix-2 fixture — quaternion.md lines
// 22-77, verbatim bytes.
func readQuaternionRaw(t *testing.T) []byte {
	t.Helper()
	src, err := os.ReadFile("testdata/quaternion-raw.md")
	if err != nil {
		t.Fatalf("read testdata/quaternion-raw.md: %v", err)
	}
	return src
}

// TestRenderQuaternionGolden freezes the real quaternion region at width 80
// in both polarities plus a plain rendering, exactly the way mathpage's
// goldens are taken. Generated once with -update after the fix-2 converter
// landed, then immutable.
func TestRenderQuaternionGolden(t *testing.T) {
	src := readQuaternionRaw(t)
	for _, tc := range []struct {
		kind  string
		style Style
	}{
		{"dark", darkStyle},
		{"light", lightStyle},
	} {
		out, err := NewRenderer().Render(src, Options{Width: 80, Style: tc.style})
		if err != nil {
			t.Fatalf("render %s: %v", tc.kind, err)
		}
		testutil.GoldenString(t,
			filepath.Join("testdata", "golden", fmt.Sprintf("quaternion-raw-80-%s.ansi.golden", tc.kind)),
			joinLines(out))
	}

	plain, err := NewRenderer().Render(src, Options{Width: 80, Style: darkStyle, Plain: true})
	if err != nil {
		t.Fatalf("render plain: %v", err)
	}
	testutil.GoldenString(t, filepath.Join("testdata", "golden", "quaternion-raw-80.txt.golden"), joinLines(plain))
}

// TestQuaternionNoRawDollarPairs: after Render, none of the screenshot's
// failure shapes survive — no dollar-wrapped plain span, no unconverted
// prime span, no construct-less $$…$$ display pair (A15-3), and no ^(') /
// ^(’ ) script fallback.
func TestQuaternionNoRawDollarPairs(t *testing.T) {
	out, err := NewRenderer().Render(readQuaternionRaw(t), Options{Width: 80, Style: darkStyle})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	plain := ansi.Strip(strings.Join(out, "\n"))

	for _, bad := range []string{"$p=[x,y,z]$", "$u$", "$p' = [x'", "^('", "^(’",
		"$$ v = [0", "$$ θ"} {
		if strings.Contains(plain, bad) {
			t.Errorf("rendered quaternion region still contains %q:\n%s", bad, plain)
		}
	}
}
