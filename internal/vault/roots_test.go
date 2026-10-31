package vault_test

import (
	"reflect"
	"testing"

	"github.com/awepo-pro/lw/internal/vault"
)

// TestNonWikiRoots pins the set of root-level directories lw excludes from
// wiki content. Each member prevents a leak into history or snapshots:
// notes/ (047), storage/ (066), and ledger/ (074).
func TestNonWikiRoots(t *testing.T) {
	want := []string{"ledger", "notes", "storage"}
	if !reflect.DeepEqual(vault.NonWikiRoots, want) {
		t.Fatalf("vault.NonWikiRoots = %v, want %v", vault.NonWikiRoots, want)
	}
	if vault.LedgerDir != "ledger" {
		t.Errorf("vault.LedgerDir = %q, want %q", vault.LedgerDir, "ledger")
	}
	if vault.NotesDir != "notes" {
		t.Errorf("vault.NotesDir = %q, want %q", vault.NotesDir, "notes")
	}
	if vault.StorageDir != "storage" {
		t.Errorf("vault.StorageDir = %q, want %q", vault.StorageDir, "storage")
	}
}

// TestIsNonWikiRoot pins membership in the non-wiki root directory set.
// It matches single root directory names exactly; nested paths and casing variations
// are not root-level matches.
func TestIsNonWikiRoot(t *testing.T) {
	for _, name := range []string{"ledger", "notes", "storage"} {
		if !vault.IsNonWikiRoot(name) {
			t.Errorf("vault.IsNonWikiRoot(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"wiki", "raw", "Ledger", "ledger/", "ledgers", "", "deep/ledger"} {
		if vault.IsNonWikiRoot(name) {
			t.Errorf("vault.IsNonWikiRoot(%q) = true, want false", name)
		}
	}
}
