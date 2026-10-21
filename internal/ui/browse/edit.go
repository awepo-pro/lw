package browse

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/awepo-pro/lw/internal/ui"
)

type editFinishedMsg struct {
	path      string
	filePath  string
	beforeSHA string
	err       error
}

// editCurrent handles the "e" keybinding: validates that the selected node is an
// editable wiki page, reads its current content to hash it, resolves the user's
// editor command, and pauses the TUI to spawn the editor via tea.ExecProcess.
func (m *Model) editCurrent() (ui.Pane, tea.Cmd) {
	n := m.selectedNode()
	if n == nil {
		return m, nil
	}
	if n.IsDir() {
		m.setStatus("cannot edit directory", ui.StatusWarn)
		return m, nil
	}
	if strings.HasPrefix(n.Path, "raw/") {
		m.setStatus("cannot edit raw source (raw is immutable)", ui.StatusWarn)
		return m, nil
	}
	if !strings.HasPrefix(n.Path, "wiki/") {
		m.setStatus("can only edit wiki pages", ui.StatusWarn)
		return m, nil
	}
	if m.deps.Engine == nil {
		m.setStatus("no vault to edit", ui.StatusWarn)
		return m, nil
	}

	filePath := filepath.Join(m.deps.Engine.Vault().Root(), filepath.FromSlash(n.Path))
	beforeBytes, err := os.ReadFile(filePath)
	if err != nil {
		m.setStatus(fmt.Sprintf("read page: %v", err), ui.StatusBad)
		return m, nil
	}
	beforeSHA := sha256Hex(beforeBytes)

	argv := editorArgv(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	c := exec.Command(argv[0], append(argv[1:], filePath)...)
	return m, tea.ExecProcess(c, func(err error) tea.Msg {
		return editFinishedMsg{
			path:      n.Path,
			filePath:  filePath,
			beforeSHA: beforeSHA,
			err:       err,
		}
	})
}

// handleEditFinished handles the return from $EDITOR: checks if the file changed,
// reloads the vault and index if modified, rebuilds the tree, and broadcasts reload
// messages so other panes and the shell refresh.
func (m *Model) handleEditFinished(msg editFinishedMsg) (ui.Pane, tea.Cmd) {
	if msg.err != nil {
		m.setStatus(fmt.Sprintf("editor: %v", msg.err), ui.StatusBad)
		return m, nil
	}
	afterBytes, err := os.ReadFile(msg.filePath)
	if err != nil {
		m.setStatus(fmt.Sprintf("read edited file: %v", err), ui.StatusBad)
		return m, nil
	}
	if sha256Hex(afterBytes) == msg.beforeSHA {
		m.setStatus("page unchanged", ui.StatusInfo)
		return m, nil
	}
	if m.deps.Engine != nil {
		if err := m.deps.Engine.ReloadVault(); err != nil {
			m.setStatus(fmt.Sprintf("reload vault: %v", err), ui.StatusBad)
			return m, nil
		}
	}
	m.off = 0
	m.rebuildTree()
	m.selectPath(msg.path)
	m.setStatus("page saved", ui.StatusGood)
	return m, tea.Batch(
		func() tea.Msg { return ui.VaultReloadedMsg{} },
		func() tea.Msg { return ui.StageChangedMsg{} },
	)
}

// editorArgv determines the editor binary and arguments from VISUAL or EDITOR
// environment variables, falling back to "vi".
func editorArgv(visual, editor string) []string {
	if fields := strings.Fields(visual); len(fields) > 0 {
		return fields
	}
	if fields := strings.Fields(editor); len(fields) > 0 {
		return fields
	}
	return []string{"vi"}
}

// sha256Hex computes the hex-encoded SHA-256 hash of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
