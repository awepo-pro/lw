// prompts.go is the D10 suggested-prompt set: the questions the empty
// transcript offers, read off index.md at New and refreshed on
// ui.VaultReloadedMsg. Split out of ask.go by concern when the Model grew
// the markdown renderer and the session-id title slot (005 T-B); it holds
// no Model state of its own beyond what it reads off m.deps.
package ask

import "regexp"

// promptEntryRe matches one index.md bullet D10 turns into a suggested
// prompt: `- [[target]] — description`, with the `|label` form of a
// wikilink allowed (s2-screens.md T08).
var promptEntryRe = regexp.MustCompile(`(?m)^- \[\[([^\]|]+)(?:\|[^\]]*)?\]\] — (.+)$`)

// fallbackPrompts is the D10 prompt set for a vault whose index has fewer
// than two entries (or none at all).
func fallbackPrompts() []string {
	return []string{
		"What does the wiki cover so far?",
		"Which pages were updated most recently?",
		"Which pages rest on a single source?",
	}
}

// promptsFromIndex computes the D10 prompt set from index.md's content.
// The first two entries' descriptions become the personalized prompts (T1,
// T2); anything from the third on is not needed. The frozen grids pin the
// descriptions — not the wikilink targets — as T1/T2: on the mockup vault
// the first two entries are `[[vertex-ai]] — Vertex AI` and
// `[[claude]] — Claude`, and ask-80x24 reads "What does the wiki say about
// Vertex AI?" / "How is Claude related to Vertex AI?".
func promptsFromIndex(src []byte) []string {
	var descs []string
	for _, match := range promptEntryRe.FindAllStringSubmatch(string(src), -1) {
		descs = append(descs, match[2])
		if len(descs) == 2 {
			break
		}
	}
	if len(descs) < 2 {
		return fallbackPrompts()
	}
	return []string{
		"What does the wiki say about " + descs[0] + "?",
		"How is " + descs[1] + " related to " + descs[0] + "?",
		"Which pages rest on a single source?",
	}
}

// loadPrompts reads index.md through the engine's vault (D10) and returns
// the prompt set for it. Every failure — no engine, unreadable vault,
// missing index — degrades to fallbackPrompts; suggested questions are
// never worth an error surface.
func (m *Model) loadPrompts() []string {
	if m.deps.Engine == nil {
		return fallbackPrompts()
	}
	src, err := m.deps.Engine.Vault().Read("index.md")
	if err != nil {
		return fallbackPrompts()
	}
	return promptsFromIndex(src)
}
