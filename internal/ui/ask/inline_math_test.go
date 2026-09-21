// inline_math_test.go pins §F.2.5: the live answer tail renders $…$ math
// through the markdown package's exported inline wrapper — the same tables
// the page seam uses — while the settled prefix converts through the shared
// renderer's own seam.
package ask

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/agent"
)

// TestAskTailConvertsInlineMath: a live buffer whose settled prefix holds
// $a^2$ and whose half-written tail holds $b^2$ shows both converted, and
// no raw math delimiter survives in the tail.
func TestAskTailConvertsInlineMath(t *testing.T) {
	const buf = "Settled sentence with $a^2$ in it.\n\nlive tail with $b^2$ here"

	m := newInlineModel(t)
	m.applyEvent(agent.TextDelta{Text: buf})
	if !m.turnActive {
		t.Fatal("precondition: the streamed entry is not live")
	}

	lines, _ := m.conversationLines(76)
	plain := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(plain, "a²") {
		t.Fatalf("the settled prefix did not convert $a^2$:\n%s", plain)
	}
	if !strings.Contains(plain, "b²") {
		t.Fatalf("the live tail did not convert $b^2$:\n%s", plain)
	}
	if strings.Contains(plain, "$b") {
		t.Fatalf("the live tail kept a raw math delimiter:\n%s", plain)
	}
}
