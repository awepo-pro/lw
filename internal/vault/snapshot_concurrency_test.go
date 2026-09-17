// snapshot_concurrency_test.go is 008 A-802's vault half (MASTER §5
// R-803): the vault's schema, pages, raw sources, parse errors and graph
// move as one immutable snapshot, so a Reload concurrent with any reader is
// race-free, a reload swaps every field together, and the slices and page
// pointers handed out before a reload stay valid after it.
package vault

import (
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/awepo-pro/lw/internal/testutil"
)

// reloadReadingVault drives Reload count times over dir's vault, writing
// one new page and one new raw source to the tree before each reload — the
// on-disk churn a foreign commit's reload sees.
func reloadReadingVault(t *testing.T, dir string, v *Vault, count int) {
	t.Helper()
	for i := 0; i < count; i++ {
		page := filepath.Join(dir, "wiki", "concepts", "churn-page.md")
		if err := writeTestFile(t, page, "---\ntitle: Churn\ncreated: 2026-01-01\nupdated: 2026-01-01\ntype: concept\n---\n\n# Churn\n\n- [[kv-cache]]\n"); err != nil {
			t.Fatalf("write churn page: %v", err)
		}
		raw := filepath.Join(dir, "raw", "articles", "churn-raw.md")
		if err := writeTestFile(t, raw, "---\nsource_url: https://example.org/churn\ningested: 2026-01-01\nsha256: 0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n---\n\nChurn body.\n"); err != nil {
			t.Fatalf("write churn raw: %v", err)
		}
		if err := v.Reload(); err != nil {
			t.Fatalf("Reload %d: %v", i, err)
		}
	}
}

func TestVaultSnapshotConcurrency(t *testing.T) {
	t.Run("reload_while_reading_is_race_free", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		v, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				// Every accessor, exactly as the agent's turn, lint's report
				// command and the TUI's render read them.
				if v.Schema() == nil {
					t.Errorf("Schema() = nil")
					return
				}
				for _, p := range v.Pages() {
					if _, ok := v.Page(p.Path); !ok {
						t.Errorf("Page(%q) = !ok right after Pages() returned it", p.Path)
						return
					}
				}
				for _, r := range v.RawSources() {
					if _, ok := v.RawSource(r.Path); !ok {
						t.Errorf("RawSource(%q) = !ok right after RawSources() returned it", r.Path)
						return
					}
				}
				g := v.Graph()
				g.Broken()
				g.Orphans()
				g.Backlinks("wiki/concepts/kv-cache.md")
				g.Outbound("wiki/concepts/kv-cache.md")
				g.Neighbors("wiki/concepts/kv-cache.md", 2)
			}
		}()

		reloadReadingVault(t, dir, v, 25)

		close(stop)
		wg.Wait()
	})

	t.Run("reload_swaps_all_fields_together", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		v, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		// A page whose wikilink cannot resolve yet: the graph of the first
		// reload must show it broken, with no backlink for the target.
		if err := writeTestFile(t, filepath.Join(dir, "wiki", "concepts", "links-out.md"),
			"---\ntitle: Links Out\ncreated: 2026-01-01\nupdated: 2026-01-01\ntype: concept\n---\n\n# Links Out\n\nSee [[swap-target]] for the answer.\n"); err != nil {
			t.Fatalf("write links-out.md: %v", err)
		}
		if err := v.Reload(); err != nil {
			t.Fatalf("Reload (first): %v", err)
		}
		if _, ok := v.Page("wiki/concepts/swap-target.md"); ok {
			t.Fatal("setup: swap-target.md already present")
		}
		if got := len(v.Graph().Backlinks("wiki/concepts/swap-target.md")); got != 0 {
			t.Fatalf("backlinks before the target exists = %d, want 0", got)
		}

		// Add the target page, a raw source, and a raw file that cannot
		// parse — one reload must swap pages, raw sources, parse errors and
		// the graph (the new backlink included) together.
		if err := writeTestFile(t, filepath.Join(dir, "wiki", "concepts", "swap-target.md"),
			"---\ntitle: Swap Target\ncreated: 2026-01-01\nupdated: 2026-01-01\ntype: concept\n---\n\n# Swap Target\n\nThe answer lives here.\n"); err != nil {
			t.Fatalf("write swap-target.md: %v", err)
		}
		if err := writeTestFile(t, filepath.Join(dir, "raw", "articles", "swap-raw.md"),
			"---\nsource_url: https://example.org/swap\ningested: 2026-01-01\nsha256: fdecba9876543210fdecba9876543210fdecba9876543210fdecba9876543210\n---\n\nSwap raw body.\n"); err != nil {
			t.Fatalf("write swap-raw.md: %v", err)
		}
		if err := writeTestFile(t, filepath.Join(dir, "raw", "articles", "broken-raw.md"),
			"no frontmatter delimiter here at all\n"); err != nil {
			t.Fatalf("write broken-raw.md: %v", err)
		}
		if err := v.Reload(); err != nil {
			t.Fatalf("Reload (second): %v", err)
		}

		if _, ok := v.Page("wiki/concepts/swap-target.md"); !ok {
			t.Error("Pages does not see swap-target.md after the reload")
		}
		if _, ok := v.RawSource("raw/articles/swap-raw.md"); !ok {
			t.Error("RawSources does not see raw/articles/swap-raw.md after the reload")
		}
		var namedBroken bool
		for _, pe := range v.ParseErrors() {
			if pe.Path == "raw/articles/broken-raw.md" {
				namedBroken = true
			}
		}
		if !namedBroken {
			t.Errorf("ParseErrors does not name raw/articles/broken-raw.md: %v", v.ParseErrors())
		}
		backlinks := v.Graph().Backlinks("wiki/concepts/swap-target.md")
		if len(backlinks) != 1 || backlinks[0].From != "wiki/concepts/links-out.md" {
			t.Errorf("backlinks of the new page = %+v, want one ref from wiki/concepts/links-out.md", backlinks)
		}
	})

	t.Run("old_slices_stay_valid", func(t *testing.T) {
		dir := testutil.CopyFixture(t, "minimal")
		v, err := Open(dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}

		pages := v.Pages()
		raws := v.RawSources()
		errs := v.ParseErrors()
		g := v.Graph()
		page, ok := v.Page("wiki/concepts/kv-cache.md")
		if !ok {
			t.Fatal("setup: fixture missing wiki/concepts/kv-cache.md")
		}
		oldBody := page.Body
		oldPaths := make([]string, 0, len(pages))
		for _, p := range pages {
			oldPaths = append(oldPaths, p.Path)
		}

		// Rewrite the page's body, add a page, add a raw source — then
		// reload. Nothing the reload builds may reach back into the old
		// snapshot.
		rewritten := strings.Replace(oldBody,
			"The key/value cache stores per-layer attention projections",
			"The rewritten cache stores per-layer attention projections", 1)
		if rewritten == oldBody {
			t.Fatal("setup: the rewrite did not change the body")
		}
		if err := writeTestFile(t, filepath.Join(dir, "wiki", "concepts", "kv-cache.md"),
			"---\ntitle: KV Cache\ncreated: 2026-01-01\nupdated: 2026-01-02\ntype: concept\n---\n\n"+rewritten); err != nil {
			t.Fatalf("rewrite kv-cache.md: %v", err)
		}
		if err := writeTestFile(t, filepath.Join(dir, "wiki", "concepts", "added-page.md"),
			"---\ntitle: Added\ncreated: 2026-01-01\nupdated: 2026-01-01\ntype: concept\n---\n\n# Added\n"); err != nil {
			t.Fatalf("write added-page.md: %v", err)
		}
		if err := writeTestFile(t, filepath.Join(dir, "raw", "articles", "added-raw.md"),
			"---\nsource_url: https://example.org/added\ningested: 2026-01-01\nsha256: 0000000000000000000000000000000000000000000000000000000000000000\n---\n\nAdded raw body.\n"); err != nil {
			t.Fatalf("write added-raw.md: %v", err)
		}
		if err := v.Reload(); err != nil {
			t.Fatalf("Reload: %v", err)
		}

		// The pre-reload slice is unchanged: same length, same paths, and
		// the page pointers still carry their old bodies.
		if got := len(pages); got != len(oldPaths) {
			t.Fatalf("the pre-reload Pages slice changed length: %d, want %d", got, len(oldPaths))
		}
		for i, p := range pages {
			if p.Path != oldPaths[i] {
				t.Errorf("pre-reload Pages slice moved at %d: %q, want %q", i, p.Path, oldPaths[i])
			}
		}
		if page.Body != oldBody {
			t.Error("the pre-reload *Page was mutated by the reload")
		}
		if got := len(raws); got != 2 {
			t.Errorf("the pre-reload RawSources slice now holds %d entries, want the original 2", got)
		}
		if got := len(errs); got != 0 {
			t.Errorf("the pre-reload ParseErrors slice now holds %d entries, want the original 0", got)
		}
		if got := len(g.Broken()); got != 0 {
			t.Errorf("the pre-reload Graph changed: %d broken refs, want 0", got)
		}
		// And the new snapshot answers on its own.
		if got := len(v.Pages()); got != 5 {
			t.Errorf("len(Pages()) after the reload = %d, want 5", got)
		}
	})
}
