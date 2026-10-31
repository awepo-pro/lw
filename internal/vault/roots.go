package vault

// Vault-root directories lw never treats as wiki content. Each was added after a leak: notes/ (047), storage/ (066),
// ledger/ (074). A name is matched at the vault root only; "deep/ledger" is ordinary content.
const (
	LedgerDir  = "ledger"
	NotesDir   = "notes"
	StorageDir = "storage"
)

// NonWikiRoots is sorted. Adding a member here is the whole change; TestWalkSkipSetIsNonWikiRoots proves the walk follows.
var NonWikiRoots = []string{LedgerDir, NotesDir, StorageDir}

// IsNonWikiRoot reports whether name is exactly one of NonWikiRoots. name is a single root-level entry, not a path.
func IsNonWikiRoot(name string) bool {
	for _, r := range NonWikiRoots {
		if name == r {
			return true
		}
	}
	return false
}
