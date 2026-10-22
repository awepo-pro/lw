package stage

import (
	"fmt"
)

// VaultIdentity returns a string that identifies the in-memory committed vault snapshot.
// It changes whenever the vault is reloaded (e.g. Reload, ReloadVault, Commit, ReloadIfChanged)
// or replaced. It takes no locks.
func (e *Engine) VaultIdentity() string {
	if e == nil || e.vault == nil {
		return ""
	}
	return fmt.Sprintf("%p:%p", e.vault, e.vault.Graph())
}
