package browse

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/ui"
)

// collectCmds executes cmd, expanding tea.BatchMsg recursively, and returns
// every message produced.
func collectCmds(t *testing.T, cmd tea.Cmd, budget int) []tea.Msg {
	t.Helper()
	if cmd == nil || budget <= 0 {
		return nil
	}
	msg := cmd()
	if msg == nil {
		return nil
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		var got []tea.Msg
		for _, c := range batch {
			got = append(got, collectCmds(t, c, budget-1)...)
		}
		return got
	}
	return []tea.Msg{msg}
}

// TestBrowseEditHelp verifies FooterHelp has ("e", "edit") directly after "enter",
// and OverlayHelp has ("e", "edit in $EDITOR") directly after "enter".
func TestBrowseEditHelp(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()
	m := New(d).(*Model)

	// Check FooterHelp()
	footer := m.FooterHelp()
	var enterIdx, eIdx = -1, -1
	for i, b := range footer {
		h := b.Help()
		if h.Key == "enter" {
			enterIdx = i
		}
		if h.Key == "e" {
			eIdx = i
			if h.Desc != "edit" {
				t.Errorf("FooterHelp 'e' desc = %q, want %q", h.Desc, "edit")
			}
		}
	}
	if eIdx == -1 {
		t.Fatal("FooterHelp missing 'e' binding")
	}
	if enterIdx == -1 {
		t.Fatal("FooterHelp missing 'enter' binding")
	}
	if eIdx != enterIdx+1 {
		t.Errorf("FooterHelp 'e' index = %d, want directly after 'enter' (%d)", eIdx, enterIdx+1)
	}

	// Check Help() fallback also carries it
	helpBindings := m.Help()
	var helpEFound bool
	for _, b := range helpBindings {
		if b.Help().Key == "e" && b.Help().Desc == "edit" {
			helpEFound = true
			break
		}
	}
	if !helpEFound {
		t.Fatal("Help() missing 'e' binding")
	}

	// Check OverlayHelp()
	title, entries := m.OverlayHelp()
	if title != "Browse" {
		t.Errorf("OverlayHelp title = %q, want 'Browse'", title)
	}
	enterIdx, eIdx = -1, -1
	for i, entry := range entries {
		if entry.Key == "enter" {
			enterIdx = i
		}
		if entry.Key == "e" {
			eIdx = i
			if entry.Desc != "edit in $EDITOR" {
				t.Errorf("OverlayHelp 'e' desc = %q, want %q", entry.Desc, "edit in $EDITOR")
			}
		}
	}
	if eIdx == -1 {
		t.Fatal("OverlayHelp missing 'e' entry")
	}
	if enterIdx == -1 {
		t.Fatal("OverlayHelp missing 'enter' entry")
	}
	if eIdx != enterIdx+1 {
		t.Errorf("OverlayHelp 'e' index = %d, want directly after 'enter' (%d)", eIdx, enterIdx+1)
	}
}

// TestBrowseEditGuards verifies the refusal guards for pressing "e":
// - on directory: "cannot edit directory" (StatusWarn)
// - on raw source: "cannot edit raw source (raw is immutable)" (StatusWarn)
// - with nil engine: "no vault to edit" (StatusWarn)
func TestBrowseEditGuards(t *testing.T) {
	t.Run("directory", func(t *testing.T) {
		d, engine := newTestDeps(t, "minimal")
		defer engine.Close()
		m := New(d).(*Model)

		// Expand raw root and move to a directory node
		if !m.selectPath("wiki/concepts") {
			t.Fatal("selectPath('wiki/concepts') failed")
		}
		n := m.selectedNode()
		if n == nil || !n.IsDir() {
			t.Fatalf("selected node is not a directory: %+v", n)
		}

		_, cmd := m.Update(keyMsg("e"))
		if cmd != nil {
			t.Errorf("Update(e) on directory produced cmd = %v, want nil", cmd)
		}
		status, level := m.Status()
		if status != "cannot edit directory" {
			t.Errorf("status = %q, want %q", status, "cannot edit directory")
		}
		if level != ui.StatusWarn {
			t.Errorf("level = %v, want StatusWarn", level)
		}
	})

	t.Run("raw_source", func(t *testing.T) {
		d, engine := newTestDeps(t, "minimal")
		defer engine.Close()
		m := New(d).(*Model)

		const rawPath = "raw/articles/kv-cache-explained.md"
		if !m.selectPath(rawPath) {
			t.Fatalf("selectPath(%q) failed", rawPath)
		}
		n := m.selectedNode()
		if n == nil || !strings.HasPrefix(n.Path, "raw/") {
			t.Fatalf("selected node is not a raw source: %+v", n)
		}

		_, cmd := m.Update(keyMsg("e"))
		if cmd != nil {
			t.Errorf("Update(e) on raw source produced cmd = %v, want nil", cmd)
		}
		status, level := m.Status()
		if status != "cannot edit raw source (raw is immutable)" {
			t.Errorf("status = %q, want %q", status, "cannot edit raw source (raw is immutable)")
		}
		if level != ui.StatusWarn {
			t.Errorf("level = %v, want StatusWarn", level)
		}
	})

	t.Run("nil_engine", func(t *testing.T) {
		d, engine := newTestDeps(t, "minimal")
		defer engine.Close()
		m := New(d).(*Model)

		const pagePath = "wiki/concepts/kv-cache.md"
		if !m.selectPath(pagePath) {
			t.Fatalf("selectPath(%q) failed", pagePath)
		}
		m.deps.Engine = nil

		_, cmd := m.Update(keyMsg("e"))
		if cmd != nil {
			t.Errorf("Update(e) with nil engine produced cmd = %v, want nil", cmd)
		}
		status, level := m.Status()
		if status != "no vault to edit" {
			t.Errorf("status = %q, want %q", status, "no vault to edit")
		}
		if level != ui.StatusWarn {
			t.Errorf("level = %v, want StatusWarn", level)
		}
	})

	t.Run("nil_node", func(t *testing.T) {
		d, engine := newTestDeps(t, "minimal")
		defer engine.Close()
		m := New(d).(*Model)
		m.visible = nil
		m.cursor = 0

		_, cmd := m.Update(keyMsg("e"))
		if cmd != nil {
			t.Errorf("Update(e) with nil node produced cmd = %v, want nil", cmd)
		}
		status, _ := m.Status()
		if status != "" {
			t.Errorf("status = %q, want empty", status)
		}
	})
}

// TestBrowseEditUnchanged verifies that delivering an editFinishedMsg where
// the file's SHA matches beforeSHA sets status "page unchanged" (StatusInfo)
// and returns no command.
func TestBrowseEditUnchanged(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()
	m := New(d).(*Model)

	const targetPath = "wiki/concepts/kv-cache.md"
	if !m.selectPath(targetPath) {
		t.Fatalf("selectPath(%q) failed", targetPath)
	}

	filePath := filepath.Join(engine.Vault().Root(), filepath.FromSlash(targetPath))
	b, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	sha := sha256Hex(b)

	msg := editFinishedMsg{
		path:      targetPath,
		filePath:  filePath,
		beforeSHA: sha,
		err:       nil,
	}

	_, cmd := m.Update(msg)
	if cmd != nil {
		t.Errorf("Update(editFinishedMsg) produced cmd = %v, want nil", cmd)
	}
	status, level := m.Status()
	if status != "page unchanged" {
		t.Errorf("status = %q, want %q", status, "page unchanged")
	}
	if level != ui.StatusInfo {
		t.Errorf("statusLevel = %v, want StatusInfo", level)
	}
}

// TestBrowseEditError verifies that delivering an editFinishedMsg with an error
// sets status "editor: ..." (StatusBad).
func TestBrowseEditError(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()
	m := New(d).(*Model)

	const targetPath = "wiki/concepts/kv-cache.md"
	if !m.selectPath(targetPath) {
		t.Fatalf("selectPath(%q) failed", targetPath)
	}

	filePath := filepath.Join(engine.Vault().Root(), filepath.FromSlash(targetPath))

	msg := editFinishedMsg{
		path:      targetPath,
		filePath:  filePath,
		beforeSHA: "dummy-sha",
		err:       errors.New("exit status 1"),
	}

	_, cmd := m.Update(msg)
	if cmd != nil {
		t.Errorf("Update(editFinishedMsg) produced cmd = %v, want nil", cmd)
	}
	status, level := m.Status()
	if status != "editor: exit status 1" {
		t.Errorf("status = %q, want %q", status, "editor: exit status 1")
	}
	if level != ui.StatusBad {
		t.Errorf("statusLevel = %v, want StatusBad", level)
	}

	// Also test read error on after-bytes
	msgMissing := editFinishedMsg{
		path:      targetPath,
		filePath:  filepath.Join(engine.Vault().Root(), "wiki/concepts/missing-file.md"),
		beforeSHA: "dummy-sha",
		err:       nil,
	}
	_, _ = m.Update(msgMissing)
	status, level = m.Status()
	if !strings.HasPrefix(status, "read edited file:") {
		t.Errorf("status = %q, want prefix 'read edited file:'", status)
	}
	if level != ui.StatusBad {
		t.Errorf("statusLevel = %v, want StatusBad", level)
	}
}

// TestBrowseEditSavedAndReloaded verifies the full save-and-reload lifecycle:
// modifying a page file on disk, delivering editFinishedMsg:
// 1. Vault is reloaded and contains the modified content.
// 2. Search index is rebuilt and finds the newly added keyword.
// 3. Open changeset op targeting that page flips to StateStale.
// 4. Preview panel re-renders with the edited text.
// 5. Broadcasts VaultReloadedMsg and StageChangedMsg in a tea.Batch.
func TestBrowseEditSavedAndReloaded(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	// 1. Open a changeset and append an op targeting "wiki/concepts/kv-cache.md".
	if _, err := engine.OpenChangeset("watch edits", stage.Author{Kind: "agent", Model: "test"}); err != nil {
		t.Fatalf("OpenChangeset: %v", err)
	}
	const targetPath = "wiki/concepts/kv-cache.md"
	page, ok := engine.Vault().Page(targetPath)
	if !ok {
		t.Fatalf("vault missing %s", targetPath)
	}

	const oldLine = "- [[flash-attention]] — a kernel design that reduces the memory-bandwidth cost"
	newLine := "- [[flash-attention]] — a kernel design that reduces the memory cost"
	rewritten := *page
	rewritten.Body = strings.Replace(page.Body, oldLine, newLine, 1)
	opID, err := engine.Append(stage.Op{
		Kind:      stage.OpPatchPage,
		Path:      page.Path,
		Section:   "## Related",
		Before:    page.SHA256(),
		Content:   rewritten.Serialize(),
		Rationale: "stale test op",
		Hunks: []stage.Hunk{{
			ID:   "h1",
			Path: page.Path,
			Del:  []string{oldLine},
			Add:  []string{newLine},
		}},
	})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}

	// 2. Create the browse model and select the target page.
	m := New(d).(*Model)
	if !m.selectPath(targetPath) {
		t.Fatalf("selectPath(%q) failed", targetPath)
	}

	// 3. Read initial file content and SHA.
	filePath := filepath.Join(engine.Vault().Root(), filepath.FromSlash(targetPath))
	beforeBytes, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	beforeSHA := sha256Hex(beforeBytes)

	// 4. Simulate editor modifying the file with a unique keyword near the top.
	const uniqueKeyword = "Zygoheterozygote"
	const heading = "# KV Cache\n"
	if !strings.Contains(string(beforeBytes), heading) {
		t.Fatalf("beforeBytes does not contain %q", heading)
	}
	modifiedBytes := []byte(strings.Replace(string(beforeBytes), heading, heading+"\n"+uniqueKeyword+" verification term.\n\n", 1))
	if err := os.WriteFile(filePath, modifiedBytes, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// 5. Deliver editFinishedMsg to m.Update.
	msg := editFinishedMsg{
		path:      targetPath,
		filePath:  filePath,
		beforeSHA: beforeSHA,
		err:       nil,
	}
	pane, cmd := m.Update(msg)
	if cmd == nil {
		t.Fatal("Update(editFinishedMsg) returned nil cmd, want tea.Batch")
	}

	// 6. Verify status reported.
	status, level := pane.(ui.StatusReporter).Status()
	if status != "page saved" {
		t.Errorf("status = %q, want %q", status, "page saved")
	}
	if level != ui.StatusGood {
		t.Errorf("level = %v, want StatusGood", level)
	}

	// 7. Verify vault reloaded with updated content.
	reloadedPage, ok := engine.Vault().Page(targetPath)
	if !ok {
		t.Fatalf("reloaded vault missing %s", targetPath)
	}
	if !strings.Contains(reloadedPage.Body, uniqueKeyword) {
		t.Fatalf("reloaded page does not contain %s", uniqueKeyword)
	}

	// 8. Verify search index rebuilt.
	hits := engine.Index().Search(uniqueKeyword, index.Options{})
	var foundInIndex bool
	for _, h := range hits {
		if h.Path == targetPath {
			foundInIndex = true
			break
		}
	}
	if !foundInIndex {
		t.Fatalf("Index().Search(%q) did not find %s; hits = %+v", uniqueKeyword, targetPath, hits)
	}

	// 9. Verify open changeset op targeting the modified page flips to StateStale.
	csAfter, err := engine.Current()
	if err != nil {
		t.Fatalf("Current after reload: %v", err)
	}
	opAfter, ok := csAfter.Op(opID)
	if !ok {
		t.Fatalf("op %s not found in open changeset", opID)
	}
	if opAfter.State != stage.StateStale {
		t.Fatalf("op %s State = %v, want StateStale", opID, opAfter.State)
	}

	// 10. Verify preview panel re-renders with the edited text.
	viewText := ansi.Strip(m.View(100, 30))
	if !strings.Contains(viewText, uniqueKeyword) {
		t.Fatalf("m.View(100, 30) does not contain %s:\n%s", uniqueKeyword, viewText)
	}

	// 11. Verify broadcast messages: VaultReloadedMsg and StageChangedMsg.
	msgs := collectCmds(t, cmd, 10)
	var gotVaultReloaded, gotStageChanged bool
	for _, raw := range msgs {
		switch raw.(type) {
		case ui.VaultReloadedMsg:
			gotVaultReloaded = true
		case ui.StageChangedMsg:
			gotStageChanged = true
		}
	}
	if !gotVaultReloaded {
		t.Errorf("broadcast messages missing ui.VaultReloadedMsg: %#v", msgs)
	}
	if !gotStageChanged {
		t.Errorf("broadcast messages missing ui.StageChangedMsg: %#v", msgs)
	}
}

// TestBrowseEditLaunch verifies that pressing "e" on a valid wiki page produces
// a tea.ExecProcess command (returning tea.execMsg).
func TestBrowseEditLaunch(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()
	m := New(d).(*Model)

	t.Setenv("VISUAL", "fake-editor --flag")
	t.Setenv("EDITOR", "")

	const targetPath = "wiki/concepts/kv-cache.md"
	if !m.selectPath(targetPath) {
		t.Fatalf("selectPath(%q) failed", targetPath)
	}

	_, cmd := m.Update(keyMsg("e"))
	if cmd == nil {
		t.Fatal("Update(e) produced nil cmd")
	}
	rawMsg := cmd()
	if rawMsg == nil {
		t.Fatal("cmd() returned nil msg")
	}
	if got := reflect.TypeOf(rawMsg).String(); got != "tea.execMsg" {
		t.Fatalf("cmd() returned type %q, want tea.execMsg", got)
	}
}

// TestBrowseEditParseErrorPageInTreeAndPreview verifies that a page with broken
// frontmatter appears in the tree (via rebuildTree including ParseErrors) and
// can be previewed (via sourceOf fallback to v.Read).
func TestBrowseEditParseErrorPageInTreeAndPreview(t *testing.T) {
	d, engine := newTestDeps(t, "minimal")
	defer engine.Close()

	// Write a file with unparseable frontmatter under wiki/concepts/
	brokenRel := "wiki/concepts/broken.md"
	brokenDisk := filepath.Join(engine.Vault().Root(), filepath.FromSlash(brokenRel))
	const brokenContent = "---\ntitle: Broken Page\ninvalid-yaml: [unclosed\n---\n# Broken Page\n\nBody of broken page.\n"
	if err := os.WriteFile(brokenDisk, []byte(brokenContent), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Reload the vault to pick up the parse error
	if err := engine.ReloadVault(); err != nil {
		t.Fatalf("ReloadVault: %v", err)
	}

	m := New(d).(*Model)

	// Verify the broken page is selectable in the tree
	if !m.selectPath(brokenRel) {
		t.Fatalf("selectPath(%q) failed; unparseable page should be in tree", brokenRel)
	}
	node := m.selectedNode()
	if node == nil {
		t.Fatal("selectedNode is nil")
	}
	if node.Path != brokenRel {
		t.Fatalf("selected node path = %q, want %q", node.Path, brokenRel)
	}

	// Verify sourceOf succeeds via v.Read fallback
	src, ok := m.sourceOf(node)
	if !ok {
		t.Fatalf("sourceOf(%q) = ok=false, want true", brokenRel)
	}
	if string(src) != brokenContent {
		t.Fatalf("sourceOf(%q) = %q, want %q", brokenRel, string(src), brokenContent)
	}

	// Verify View renders the preview without error
	view := ansi.Strip(m.View(100, 30))
	if !strings.Contains(view, "Broken Page") {
		t.Fatalf("m.View(100, 30) does not contain 'Broken Page':\n%s", view)
	}
	if !strings.Contains(view, "Body of broken page") {
		t.Fatalf("m.View(100, 30) does not contain 'Body of broken page':\n%s", view)
	}
}

// TestEditorArgv tests resolution of editor binary and args from VISUAL and EDITOR.
func TestEditorArgv(t *testing.T) {
	tests := []struct {
		visual string
		editor string
		want   []string
	}{
		{"", "", []string{"vi"}},
		{"   ", "  ", []string{"vi"}},
		{"nano -w", "vim", []string{"nano", "-w"}},
		{"", "vim", []string{"vim"}},
		{"code --wait", "", []string{"code", "--wait"}},
		{"", "emacs -nw", []string{"emacs", "-nw"}},
	}
	for _, tc := range tests {
		got := editorArgv(tc.visual, tc.editor)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("editorArgv(%q, %q) = %v, want %v", tc.visual, tc.editor, got, tc.want)
		}
	}
}

// TestSha256Hex tests hex-encoded SHA-256 computation.
func TestSha256Hex(t *testing.T) {
	h := sha256Hex([]byte("hello"))
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if h != want {
		t.Errorf("sha256Hex('hello') = %q, want %q", h, want)
	}
}
