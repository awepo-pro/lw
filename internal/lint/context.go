package lint

import (
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/vault"
)

// Context is everything a Check needs. A check reads from it and never
// mutates it.
type Context struct {
	Vault *vault.Vault
	Index *index.Index
	Graph *vault.Graph
}
