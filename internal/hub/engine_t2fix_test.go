package hub_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/hub"
)

// TestT2Fix2cPutIdenticalBaseX verifies that when the body SHA matches the current
// file on disk, Put returns ResultApplied with no commit and no write, whatever base is.
func TestT2Fix2cPutIdenticalBaseX(t *testing.T) {
	ctx := context.Background()
	eng, dir, commits := setupEngine(t, nil)

	body := []byte("identical content\n")
	filePath := filepath.Join(dir, "notes/a.md")
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, body, 0o644); err != nil {
		t.Fatal(err)
	}

	baseX := "1111111111111111111111111111111111111111111111111111111111111111"
	res, err := eng.Put(ctx, testDevice, "notes/a.md", baseX, body)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if res.Result != hub.ResultApplied {
		t.Fatalf("Result want applied, got %s", res.Result)
	}
	if len(*commits) != 0 {
		t.Fatalf("expected no commit for identical put with base=X, got %v", *commits)
	}
}
