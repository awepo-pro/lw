package score

import (
	"fmt"
	"strings"
	"testing"
)

// TestGetNotFoundIgnoresReadRefusalSize pins 060 review LOW-1. A wiki.get that
// ended its turn has no recovered text, so GetNotFound compares result_bytes
// with the length of the not-found answer for the page the call asked for. 048's
// read refusal for wiki.get is a fixed text, so a page argument whose not-found
// answer is exactly as long (about 62 characters) makes a refused crawl-ending
// call look like a not-found — the commonest way a crawl ends. A result_bytes
// equal to the refusal's length is therefore never counted: the scorer prefers
// to under-count a not-found of that exact size to over-counting a refusal.
func TestGetNotFoundIgnoresReadRefusalSize(t *testing.T) {
	refusal := len(fmt.Sprintf(readRefusalFmt, "wiki.get"))
	pad := refusal - len(pageNotFoundText(""))
	if pad <= 0 {
		t.Fatalf("the refusal (%d bytes) is not longer than an empty not-found (%d); the collision this test builds cannot exist", refusal, len(pageNotFoundText("")))
	}
	collide := strings.Repeat("a", pad)
	if got := len(pageNotFoundText(collide)); got != refusal {
		t.Fatalf("the colliding page %q gives a %d-byte not-found, want the refusal's %d", collide, got, refusal)
	}

	tests := []struct {
		name  string
		calls []gnfCall
		want  int
	}{
		{"a last-round refusal whose size equals the not-found for the page asked", []gnfCall{
			{name: "wiki.get", args: gnfPage(collide), fail: true, bytes: refusal},
		}, 0},
		{"the same through the wire spelling", []gnfCall{
			{name: "wiki_get", args: gnfPage(collide), fail: true, bytes: refusal},
		}, 0},
		{"a not-found of another size is still counted", []gnfCall{
			{name: "wiki.get", args: gnfPage(collide + "b"), fail: true, bytes: len(pageNotFoundText(collide + "b"))},
		}, 1},
		{"a refusal-sized call beside a counted one", []gnfCall{
			{name: "wiki.get", args: gnfPage(collide), fail: true, bytes: refusal},
			{name: "wiki.get", args: gnfPage("paged-attention"), fail: true, bytes: len(pageNotFoundText("paged-attention"))},
		}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			turn := writeGetTurn(t, dir, false, tc.calls...)
			got, err := GetNotFound(dir, turnID(1), turn)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("GetNotFound = %d, want %d", got, tc.want)
			}
		})
	}

	// A call whose text WAS recovered is judged by the text alone, whatever
	// its size: a real not-found of exactly the refusal's length still counts.
	t.Run("recovered text outranks the size", func(t *testing.T) {
		dir := t.TempDir()
		turn := writeGetTurn(t, dir, true, gnfCall{name: "wiki.get", args: gnfPage(collide), fail: true, result: pageNotFoundText(collide)})
		got, err := GetNotFound(dir, turnID(1), turn)
		if err != nil {
			t.Fatal(err)
		}
		if got != 1 {
			t.Errorf("GetNotFound = %d, want 1: a recovered not-found text counts whatever its size", got)
		}
	})
}
