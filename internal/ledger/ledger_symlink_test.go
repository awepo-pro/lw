package ledger

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestLoadSymlink tests Load behavior when <root>/ledger is a symbolic link (074).
func TestLoadSymlink(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("LW_VAULT", "")

	validTxnLine := "- (id:: 20261031-1230-a) (amt:: -45.00) (cur:: HKD) (cat:: food) (acct:: cash) lunch\n"

	// S1: a vault whose ledger is a symlink to a directory elsewhere under t.TempDir()
	// holding ledger/2026/10/2026-10-31.md with one valid line -> files==1, one txn,
	// Path "ledger/2026/10/2026-10-31.md", no issues.
	var txnsS1 []Txn
	var issuesS1 []Issue
	var filesS1 int

	tmp := t.TempDir()
	symVault := filepath.Join(tmp, "vault_symlink")
	if err := os.MkdirAll(symVault, 0755); err != nil {
		t.Fatal(err)
	}
	targetDir := filepath.Join(tmp, "external_ledger")
	dayDir := filepath.Join(targetDir, "2026", "10")
	if err := os.MkdirAll(dayDir, 0755); err != nil {
		t.Fatal(err)
	}
	dayFile := filepath.Join(dayDir, "2026-10-31.md")
	if err := os.WriteFile(dayFile, []byte(validTxnLine), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, filepath.Join(symVault, "ledger")); err != nil {
		t.Fatal(err)
	}

	t.Run("S1_SymlinkRoot", func(t *testing.T) {
		var errS1 error
		txnsS1, issuesS1, filesS1, errS1 = Load(symVault)
		if errS1 != nil {
			t.Fatalf("unexpected Load error: %v", errS1)
		}
		if filesS1 != 1 {
			t.Errorf("expected files==1, got %d", filesS1)
		}
		if len(txnsS1) != 1 {
			t.Fatalf("expected 1 txn, got %d", len(txnsS1))
		}
		if txnsS1[0].Path != "ledger/2026/10/2026-10-31.md" {
			t.Errorf("expected Path %q, got %q", "ledger/2026/10/2026-10-31.md", txnsS1[0].Path)
		}
		if len(issuesS1) != 0 {
			t.Errorf("expected no issues, got %+v", issuesS1)
		}
	})

	// S2: the same tree as a real directory -> identical (txns, issues, files) to S1 (reflect.DeepEqual).
	t.Run("S2_RealDirectoryIdentical", func(t *testing.T) {
		realVault := filepath.Join(tmp, "vault_real")
		realDayDir := filepath.Join(realVault, "ledger", "2026", "10")
		if err := os.MkdirAll(realDayDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(realDayDir, "2026-10-31.md"), []byte(validTxnLine), 0644); err != nil {
			t.Fatal(err)
		}

		txnsS2, issuesS2, filesS2, errS2 := Load(realVault)
		if errS2 != nil {
			t.Fatalf("unexpected Load error: %v", errS2)
		}
		if !reflect.DeepEqual(txnsS1, txnsS2) {
			t.Errorf("txns mismatch between symlink and real directory:\nS1: %+v\nS2: %+v", txnsS1, txnsS2)
		}
		if !reflect.DeepEqual(issuesS1, issuesS2) {
			t.Errorf("issues mismatch between symlink and real directory:\nS1: %+v\nS2: %+v", issuesS1, issuesS2)
		}
		if filesS1 != filesS2 {
			t.Errorf("files count mismatch: S1=%d, S2=%d", filesS1, filesS2)
		}
	})

	// S3: a dangling ledger symlink -> Load returns a non-nil error (not a silent empty result).
	t.Run("S3_DanglingSymlink", func(t *testing.T) {
		danglingVault := filepath.Join(tmp, "vault_dangling")
		if err := os.MkdirAll(danglingVault, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(tmp, "nonexistent_ledger"), filepath.Join(danglingVault, "ledger")); err != nil {
			t.Fatal(err)
		}

		txns, issues, files, errS3 := Load(danglingVault)
		if errS3 == nil {
			t.Errorf("expected non-nil error for dangling symlink, got nil (txns=%d, issues=%d, files=%d)", len(txns), len(issues), files)
		}
	})

	// S4: receipts under the symlinked tree (ledger/receipts/x.md) are still skipped.
	t.Run("S4_ReceiptsSkipped", func(t *testing.T) {
		receiptsDir := filepath.Join(targetDir, "receipts")
		if err := os.MkdirAll(receiptsDir, 0755); err != nil {
			t.Fatal(err)
		}
		receiptFile := filepath.Join(receiptsDir, "x.md")
		receiptContent := "- (id:: 20261031-1000-a) (amt:: -1.00) (cur:: USD) (cat:: receipts) (acct:: cash) receipt txn\n"
		if err := os.WriteFile(receiptFile, []byte(receiptContent), 0644); err != nil {
			t.Fatal(err)
		}

		txnsS4, issuesS4, filesS4, errS4 := Load(symVault)
		if errS4 != nil {
			t.Fatalf("unexpected Load error: %v", errS4)
		}
		if filesS4 != 1 {
			t.Errorf("expected files==1 with receipts under symlink, got %d", filesS4)
		}
		if len(txnsS4) != 1 {
			t.Errorf("expected 1 txn with receipts under symlink, got %d", len(txnsS4))
		}
		if len(issuesS4) != 0 {
			t.Errorf("expected no issues with receipts under symlink, got %+v", issuesS4)
		}
	})
}
