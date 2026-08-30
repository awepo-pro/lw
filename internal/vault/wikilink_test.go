package vault

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// TestWikilink covers ParseWikilinks, including the inline-code exclusion
// that only an AST-aware parser (not a line regexp) gets right.
func TestWikilink(t *testing.T) {
	t.Run("simple_target", func(t *testing.T) {
		body := "See [[kv-cache]] for details.\n"
		got := ParseWikilinks(body)
		if len(got) != 1 {
			t.Fatalf("len(links) = %d, want 1 (%#v)", len(got), got)
		}
		w := got[0]
		if w.Target != "kv-cache" {
			t.Errorf("Target = %q, want %q", w.Target, "kv-cache")
		}
		if w.Alias != "" {
			t.Errorf("Alias = %q, want \"\"", w.Alias)
		}
		if body[w.Start:w.End] != "[[kv-cache]]" {
			t.Errorf("body[Start:End] = %q, want %q", body[w.Start:w.End], "[[kv-cache]]")
		}
		if w.Line != 1 {
			t.Errorf("Line = %d, want 1", w.Line)
		}
	})

	t.Run("target_and_alias", func(t *testing.T) {
		body := "See [[kv-cache|the KV cache]] for details.\n"
		got := ParseWikilinks(body)
		if len(got) != 1 {
			t.Fatalf("len(links) = %d, want 1 (%#v)", len(got), got)
		}
		w := got[0]
		if w.Target != "kv-cache" {
			t.Errorf("Target = %q, want %q", w.Target, "kv-cache")
		}
		if w.Alias != "the KV cache" {
			t.Errorf("Alias = %q, want %q", w.Alias, "the KV cache")
		}
		if body[w.Start:w.End] != "[[kv-cache|the KV cache]]" {
			t.Errorf("body[Start:End] = %q, want %q", body[w.Start:w.End], "[[kv-cache|the KV cache]]")
		}
	})

	// MASTER §9 D-Y: fragment support. Real Obsidian syntax order is
	// target, then "#fragment", then "|alias".
	t.Run("target_and_fragment", func(t *testing.T) {
		body := "See [[foo#bar]] for details.\n"
		got := ParseWikilinks(body)
		if len(got) != 1 {
			t.Fatalf("len(links) = %d, want 1 (%#v)", len(got), got)
		}
		w := got[0]
		if w.Target != "foo" {
			t.Errorf("Target = %q, want %q", w.Target, "foo")
		}
		if w.Fragment != "bar" {
			t.Errorf("Fragment = %q, want %q", w.Fragment, "bar")
		}
		if w.Alias != "" {
			t.Errorf("Alias = %q, want \"\"", w.Alias)
		}
		if body[w.Start:w.End] != "[[foo#bar]]" {
			t.Errorf("body[Start:End] = %q, want %q", body[w.Start:w.End], "[[foo#bar]]")
		}
	})

	t.Run("target_fragment_and_alias", func(t *testing.T) {
		body := "See [[a/b#Sec|Text]] for details.\n"
		got := ParseWikilinks(body)
		if len(got) != 1 {
			t.Fatalf("len(links) = %d, want 1 (%#v)", len(got), got)
		}
		w := got[0]
		if w.Target != "a/b" {
			t.Errorf("Target = %q, want %q", w.Target, "a/b")
		}
		if w.Fragment != "Sec" {
			t.Errorf("Fragment = %q, want %q", w.Fragment, "Sec")
		}
		if w.Alias != "Text" {
			t.Errorf("Alias = %q, want %q", w.Alias, "Text")
		}
		if body[w.Start:w.End] != "[[a/b#Sec|Text]]" {
			t.Errorf("body[Start:End] = %q, want %q", body[w.Start:w.End], "[[a/b#Sec|Text]]")
		}
	})

	t.Run("hash_in_alias_is_not_a_fragment", func(t *testing.T) {
		body := "See [[foo|bar#notafragment]] for details.\n"
		got := ParseWikilinks(body)
		if len(got) != 1 {
			t.Fatalf("len(links) = %d, want 1 (%#v)", len(got), got)
		}
		w := got[0]
		if w.Target != "foo" {
			t.Errorf("Target = %q, want %q", w.Target, "foo")
		}
		if w.Fragment != "" {
			t.Errorf("Fragment = %q, want \"\" (a # after | belongs to the alias)", w.Fragment)
		}
		if w.Alias != "bar#notafragment" {
			t.Errorf("Alias = %q, want %q", w.Alias, "bar#notafragment")
		}
	})

	t.Run("link_in_backticks", func(t *testing.T) {
		body := "Not a link: `[[fake]]`. This one is real: [[real-target]].\n"
		got := ParseWikilinks(body)
		if len(got) != 1 {
			t.Fatalf("len(links) = %d, want 1 (%#v)", len(got), got)
		}
		if got[0].Target != "real-target" {
			t.Errorf("Target = %q, want %q", got[0].Target, "real-target")
		}
	})

	t.Run("links_in_fenced_block_ignored", func(t *testing.T) {
		body := "```text\n" +
			"## this is not a heading, just log text\n" +
			"[[fake]]\n" +
			"```\n"
		got := ParseWikilinks(body)
		if len(got) != 0 {
			t.Fatalf("ParseWikilinks(fenced-only body) = %#v, want zero links", got)
		}
	})

	t.Run("ascending_start_order", func(t *testing.T) {
		body := "[[third]] then [[first-in-text-but-not-name]] then [[second]].\n"
		got := ParseWikilinks(body)
		if len(got) != 3 {
			t.Fatalf("len(links) = %d, want 3", len(got))
		}
		for i := 1; i < len(got); i++ {
			if got[i-1].Start >= got[i].Start {
				t.Fatalf("links not in ascending Start order: %#v", got)
			}
		}
	})

	t.Run("wikilink_alias_fixture", func(t *testing.T) {
		root := testutil.FixtureRoot(t)
		path := filepath.Join(root, "pages", "wikilink-alias.want.md")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		_, body, err := ParseFrontmatter(b)
		if err != nil {
			t.Fatalf("ParseFrontmatter: %v", err)
		}

		got := ParseWikilinks(string(body))
		if len(got) != 1 {
			t.Fatalf("len(links) = %d, want 1 (%#v)", len(got), got)
		}
		if got[0].Target != "kv-cache" {
			t.Errorf("Target = %q, want %q", got[0].Target, "kv-cache")
		}
		if got[0].Alias != "the KV cache" {
			t.Errorf("Alias = %q, want %q", got[0].Alias, "the KV cache")
		}
	})

	// This is the load-bearing regression the brief calls out by name:
	// S1-T5's link-broken golden row is pinned to Line: 7 on this exact
	// fixture's body.
	t.Run("broken_target_line_number", func(t *testing.T) {
		root := testutil.FixtureRoot(t)
		path := filepath.Join(root, "dirty", "wiki", "concepts", "broken-target.md")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		_, body, err := ParseFrontmatter(b)
		if err != nil {
			t.Fatalf("ParseFrontmatter: %v", err)
		}

		got := ParseWikilinks(string(body))
		var broken *Wikilink
		for i := range got {
			if got[i].Target == "nonexistent-target" {
				broken = &got[i]
			}
		}
		if broken == nil {
			t.Fatalf("no wikilink to %q found in %#v", "nonexistent-target", got)
		}
		if broken.Line != 7 {
			t.Errorf("Line = %d, want 7", broken.Line)
		}
	})

	t.Run("nested_list_body_survives", func(t *testing.T) {
		root := testutil.FixtureRoot(t)
		path := filepath.Join(root, "pages", "nested-list-body.want.md")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		_, body, err := ParseFrontmatter(b)
		if err != nil {
			t.Fatalf("ParseFrontmatter: %v", err)
		}
		if got := ParseWikilinks(string(body)); len(got) != 0 {
			t.Errorf("ParseWikilinks(nested-list body) = %#v, want zero links", got)
		}
	})

	t.Run("table_body_survives", func(t *testing.T) {
		root := testutil.FixtureRoot(t)
		path := filepath.Join(root, "pages", "table.want.md")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile(%s): %v", path, err)
		}
		_, body, err := ParseFrontmatter(b)
		if err != nil {
			t.Fatalf("ParseFrontmatter: %v", err)
		}
		// A table row like "| Name  | Value |" must not be misread as
		// containing any wikilink.
		if got := ParseWikilinks(string(body)); len(got) != 0 {
			t.Errorf("ParseWikilinks(table body) = %#v, want zero links", got)
		}
	})

	// This is the invariant that makes RewriteWikilinks safe to use as the
	// engine's only rewriter (backbone §2.5, MASTER §9 D-Y): for every link
	// found anywhere, body[Start:End] must reconstruct exactly from the
	// three parts. Asserted here over real vault data, not just unit cases.
	t.Run("reconstruction_property", func(t *testing.T) {
		synthetic := []struct {
			name string
			body string
		}{
			{"plain", "[[foo]]\n"},
			{"alias", "[[foo|Alias]]\n"},
			{"fragment", "[[foo#bar]]\n"},
			{"fragment_and_alias", "[[a/b#Sec|Text]]\n"},
			{"hash_in_alias", "[[foo|bar#notafragment]]\n"},
			{"mixed_case_target", "[[GPT-4]]\n"},
			{"path_target_with_alias", "[[a/b.md|Alias]]\n"},
			{"several_on_one_line", "[[one]] and [[two#frag]] and [[three|Three]] and [[four#f|Four]].\n"},
		}
		for _, tc := range synthetic {
			t.Run(tc.name, func(t *testing.T) {
				for _, w := range ParseWikilinks(tc.body) {
					assertReconstructs(t, tc.body, w)
				}
			})
		}

		root := testutil.FixtureRoot(t)
		for _, vaultName := range []string{"minimal", "dirty"} {
			wikiDir := filepath.Join(root, vaultName, "wiki")
			for _, path := range collectMarkdownFiles(t, wikiDir) {
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("ReadFile(%s): %v", path, err)
				}
				_, body, err := ParseFrontmatter(b)
				if err != nil {
					// dirty/wiki/concepts/malformed.md is deliberately
					// unparsable by fixture design (S1 correction C-1); it
					// contributes no links to reconstruct, so skip it here
					// rather than fail on a fixture that isn't this test's
					// concern.
					continue
				}
				for _, w := range ParseWikilinks(string(body)) {
					assertReconstructs(t, string(body), w)
				}
			}
		}
	})
}

// assertReconstructs asserts that body[w.Start:w.End] equals the literal
// "[[...]]" text rebuilt from w's three parts — the invariant backbone
// §2.5 (MASTER §9 D-Y) requires of every Wikilink.
func assertReconstructs(t *testing.T, body string, w Wikilink) {
	t.Helper()

	want := "[[" + w.Target
	if w.Fragment != "" {
		want += "#" + w.Fragment
	}
	if w.Alias != "" {
		want += "|" + w.Alias
	}
	want += "]]"

	if got := body[w.Start:w.End]; got != want {
		t.Errorf("body[Start:End] = %q, want %q (Target=%q Fragment=%q Alias=%q)",
			got, want, w.Target, w.Fragment, w.Alias)
	}
}

// TestRewrite covers RewriteWikilinks.
func TestRewrite(t *testing.T) {
	t.Run("rewrite_middle_of_three", func(t *testing.T) {
		body := "[[first]] and [[second]] and [[third]].\n"
		links := ParseWikilinks(body)
		if len(links) != 3 {
			t.Fatalf("len(links) = %d, want 3", len(links))
		}

		got := RewriteWikilinks(body, links, func(w Wikilink) (string, bool) {
			if w.Target == "second" {
				return "renamed-second", true
			}
			return "", false
		})

		want := "[[first]] and [[renamed-second]] and [[third]].\n"
		if got != want {
			t.Errorf("RewriteWikilinks = %q, want %q", got, want)
		}

		// The other two links must be byte-identical: re-parse and compare
		// their exact source text, not just their Target strings.
		newLinks := ParseWikilinks(got)
		if len(newLinks) != 3 {
			t.Fatalf("len(newLinks) = %d, want 3", len(newLinks))
		}
		if got[newLinks[0].Start:newLinks[0].End] != "[[first]]" {
			t.Errorf("first link = %q, want %q", got[newLinks[0].Start:newLinks[0].End], "[[first]]")
		}
		if got[newLinks[2].Start:newLinks[2].End] != "[[third]]" {
			t.Errorf("third link = %q, want %q", got[newLinks[2].Start:newLinks[2].End], "[[third]]")
		}
	})

	t.Run("false_leaves_link_byte_identical", func(t *testing.T) {
		body := "Only [[one-link]] here.\n"
		links := ParseWikilinks(body)

		got := RewriteWikilinks(body, links, func(w Wikilink) (string, bool) {
			return "", false
		})

		if got != body {
			t.Errorf("RewriteWikilinks with newTarget always false = %q, want unchanged %q", got, body)
		}
	})

	t.Run("preserves_alias", func(t *testing.T) {
		body := "See [[kv-cache|the KV cache]] here.\n"
		links := ParseWikilinks(body)

		got := RewriteWikilinks(body, links, func(w Wikilink) (string, bool) {
			return "kv-cache-v2", true
		})

		want := "See [[kv-cache-v2|the KV cache]] here.\n"
		if got != want {
			t.Errorf("RewriteWikilinks = %q, want %q", got, want)
		}
	})

	t.Run("rewrites_everything_outside_the_link_untouched", func(t *testing.T) {
		body := "prefix text [[old-target]] suffix text\nsecond line unaffected\n"
		links := ParseWikilinks(body)

		got := RewriteWikilinks(body, links, func(w Wikilink) (string, bool) {
			return "new-target", true
		})

		want := "prefix text [[new-target]] suffix text\nsecond line unaffected\n"
		if got != want {
			t.Errorf("RewriteWikilinks = %q, want %q", got, want)
		}
	})

	t.Run("rewrite_preserves_fragment", func(t *testing.T) {
		body := "See [[foo#bar]] here.\n"
		links := ParseWikilinks(body)

		got := RewriteWikilinks(body, links, func(w Wikilink) (string, bool) {
			return "baz", true
		})

		want := "See [[baz#bar]] here.\n"
		if got != want {
			t.Errorf("RewriteWikilinks = %q, want %q", got, want)
		}
	})

	t.Run("rewrite_preserves_fragment_and_alias", func(t *testing.T) {
		body := "See [[a/b#Sec|Text]] here.\n"
		links := ParseWikilinks(body)

		got := RewriteWikilinks(body, links, func(w Wikilink) (string, bool) {
			return "c/d", true
		})

		want := "See [[c/d#Sec|Text]] here.\n"
		if got != want {
			t.Errorf("RewriteWikilinks = %q, want %q", got, want)
		}
	})
}
