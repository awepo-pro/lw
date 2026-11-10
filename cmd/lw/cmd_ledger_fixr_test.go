package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/ledger"
)

// TestLedgerFixR_R3_CrashSafetyBetweenSteps tests R3:
// collect moves receipt images BEFORE it moves the note (order: archive check, append the line, images, note).
// A crash after each step is simulated with a hook, followed by running collect again.
// The final state must have:
// - exactly one line in bookkeeping.md (no duplicate lines!)
// - receipt image in ledger/receipts/YYYY/MM/ (not stranded at root!)
// - note in ledger/YYYY/MM/ (not at root!)
func TestLedgerFixR_R3_CrashSafetyBetweenSteps(t *testing.T) {
	steps := []string{"archive-check", "append-line", "move-images"}

	for _, stopAfterStep := range steps {
		t.Run("stop-after-"+stopAfterStep, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".llmwiki"), 0o755); err != nil {
				t.Fatal(err)
			}

			date := "2026-11-09"
			today := "2026-11-10"
			ym := "2026/11"

			// Create day note with embed ![[receipt.jpg]]
			noteContent := "44 (lunch)\n![[receipt.jpg]]\n"
			if err := os.WriteFile(filepath.Join(root, date+".md"), []byte(noteContent), 0o644); err != nil {
				t.Fatal(err)
			}
			// Create receipt.jpg at vault root
			receiptBytes := []byte("FAKE-RECEIPT-IMAGE-BYTES")
			if err := os.WriteFile(filepath.Join(root, "receipt.jpg"), receiptBytes, 0o644); err != nil {
				t.Fatal(err)
			}

			// Hook that stops after stopAfterStep
			hookErr := errors.New("simulated crash after step " + stopAfterStep)
			collectStepHook = func(step string) error {
				if step == stopAfterStep {
					return hookErr
				}
				return nil
			}

			var buf1 bytes.Buffer
			err := collectDayNotes(&buf1, root, today, false, false)
			if err == nil {
				t.Fatalf("expected error from hook at step %s, got nil", stopAfterStep)
			}
			if !errors.Is(err, hookErr) && !strings.Contains(err.Error(), stopAfterStep) {
				t.Fatalf("unexpected error: %v", err)
			}

			// Clear hook to simulate next run
			collectStepHook = nil

			var buf2 bytes.Buffer
			err = collectDayNotes(&buf2, root, today, false, false)
			if err != nil {
				t.Fatalf("second collect failed after crash at %s: %v", stopAfterStep, err)
			}

			// Verify 1: exactly one line in bookkeeping.md
			bkData, err := os.ReadFile(filepath.Join(root, ledger.LooseFile))
			if err != nil {
				t.Fatalf("reading bookkeeping.md: %v", err)
			}
			bkStr := string(bkData)
			expectedLink := fmt.Sprintf("[[%s]]", date)
			count := strings.Count(bkStr, expectedLink)
			if count != 1 {
				t.Errorf("bookkeeping.md has %d occurrences of %s, want exactly 1:\n%s", count, expectedLink, bkStr)
			}

			// Verify 2: note is gone from root
			if _, err := os.Stat(filepath.Join(root, date+".md")); !os.IsNotExist(err) {
				t.Errorf("day note still at root after recovery")
			}

			// Verify 3: note is in archive
			archNotePath := filepath.Join(root, "ledger", ym, date+".md")
			if data, err := os.ReadFile(archNotePath); err != nil || string(data) != noteContent {
				t.Errorf("archived note missing or mismatch: err=%v, content=%q", err, string(data))
			}

			// Verify 4: receipt.jpg is gone from root
			if _, err := os.Stat(filepath.Join(root, "receipt.jpg")); !os.IsNotExist(err) {
				t.Errorf("receipt.jpg stranded at root after crash at %s", stopAfterStep)
			}

			// Verify 5: receipt.jpg is in ledger/receipts/YYYY/MM/
			archReceiptPath := filepath.Join(root, "ledger", "receipts", ym, "receipt.jpg")
			if data, err := os.ReadFile(archReceiptPath); err != nil || string(data) != string(receiptBytes) {
				t.Errorf("archived receipt missing or mismatch: err=%v", err)
			}
		})
	}
}
