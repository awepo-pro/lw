package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"

	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/stage"
)

// testProjectedBuildHook is an unexported hook called whenever the projected search
// index is built from Engine.ProjectedVault(). Used by TestProjectedSearchCache.
var testProjectedBuildHook func()

type searchCache struct {
	mu    sync.Mutex
	key   string
	index *index.Index
}

func newSearchCache() *searchCache {
	return &searchCache{}
}

// openChangesetKey computes the sha256 hash identifying the open changeset and
// the committed vault state, or "" if no changeset is open.
func openChangesetKey(d Deps) string {
	if d.Engine == nil {
		return ""
	}
	cs, err := d.Engine.Current()
	if err != nil || cs == nil {
		return ""
	}
	live := cs.Live()
	liveBytes, err := json.Marshal(live)
	if err != nil {
		return ""
	}
	h := sha256.New()
	h.Write([]byte(cs.ID))
	h.Write([]byte{0})
	h.Write(liveBytes)
	h.Write([]byte{0})
	h.Write([]byte(d.Engine.VaultIdentity()))
	return hex.EncodeToString(h.Sum(nil))
}

// getSearchIndex returns the search index for wiki.search.
// When csKey is non-empty, it uses or rebuilds the projected index cached on d.searchCache.
// When csKey is empty, it returns d.Index (the committed index).
func getSearchIndex(d Deps, csKey string) (*index.Index, error) {
	if csKey == "" || d.Engine == nil {
		return d.Index, nil
	}
	if d.searchCache == nil {
		pv, err := d.Engine.ProjectedVault()
		if err != nil {
			return nil, err
		}
		if testProjectedBuildHook != nil {
			testProjectedBuildHook()
		}
		return index.Build(pv), nil
	}
	d.searchCache.mu.Lock()
	defer d.searchCache.mu.Unlock()
	if d.searchCache.key == csKey && d.searchCache.index != nil {
		return d.searchCache.index, nil
	}
	pv, err := d.Engine.ProjectedVault()
	if err != nil {
		return nil, err
	}
	if testProjectedBuildHook != nil {
		testProjectedBuildHook()
	}
	ix := index.Build(pv)
	d.searchCache.key = csKey
	d.searchCache.index = ix
	return ix, nil
}

// liveStagedTargets returns the set of target paths that have a live op in cs.
func liveStagedTargets(cs *stage.Changeset) map[string]bool {
	if cs == nil {
		return nil
	}
	targets := make(map[string]bool)
	var walk func(op stage.Op)
	walk = func(op stage.Op) {
		switch op.Kind {
		case stage.OpCreatePage, stage.OpPatchPage, stage.OpIngestSource, stage.OpRetract, stage.OpSplitPage:
			if op.Path != "" {
				targets[op.Path] = true
			}
		case stage.OpRenamePage, stage.OpMergePages:
			if op.To != "" {
				targets[op.To] = true
			}
		}
		for _, sub := range op.Cascade {
			walk(sub)
		}
	}
	for _, op := range cs.Live() {
		walk(op)
	}
	return targets
}
