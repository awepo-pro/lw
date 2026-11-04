package hub_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/awepo-pro/lw/internal/hub"
)

type scopeCase struct {
	Path  string `json:"path"`
	Allow bool   `json:"allow"`
	Code  string `json:"code,omitempty"`
}

type scopeFixture struct {
	Comment string      `json:"comment"`
	Cases   []scopeCase `json:"cases"`
}

// TestHScope runs every row in testdata/scope-cases.json against hub.CheckPath.
func TestHScope(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "scope-cases.json"))
	if err != nil {
		t.Fatalf("failed to read testdata/scope-cases.json: %v", err)
	}

	var fixture scopeFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatalf("failed to parse testdata/scope-cases.json: %v", err)
	}

	if len(fixture.Cases) == 0 {
		t.Fatal("no test cases found in scope-cases.json")
	}

	for _, tc := range fixture.Cases {
		tc := tc
		t.Run(tc.Path, func(t *testing.T) {
			err := hub.CheckPath(tc.Path)
			if tc.Allow {
				if err != nil {
					t.Fatalf("expected path %q to be allowed, got error: %v", tc.Path, err)
				}
				return
			}

			if err == nil {
				t.Fatalf("expected path %q to be denied, but CheckPath returned nil", tc.Path)
			}

			hubErr, ok := err.(*hub.Error)
			if !ok {
				t.Fatalf("expected *hub.Error for %q, got %T: %v", tc.Path, err, err)
			}

			if hubErr.Code != tc.Code {
				t.Fatalf("expected error code %q for %q, got %q (message: %s)", tc.Code, tc.Path, hubErr.Code, hubErr.Message)
			}
		})
	}
}
