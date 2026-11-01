package vault

import "testing"

func TestLaterCopy_E6(t *testing.T) {
	if got := LaterCopy("raw/articles/x-2.md", "raw/articles/x.md"); got != true {
		t.Errorf(`LaterCopy("raw/articles/x-2.md", "raw/articles/x.md") = %v, want true`, got)
	}
	if got := LaterCopy("raw/articles/x.md", "raw/articles/x-2.md"); got != false {
		t.Errorf(`LaterCopy("raw/articles/x.md", "raw/articles/x-2.md") = %v, want false`, got)
	}
	if got := LaterCopy("raw/articles/x-10.md", "raw/articles/x-2.md"); got != true {
		t.Errorf(`LaterCopy("raw/articles/x-10.md", "raw/articles/x-2.md") = %v, want true`, got)
	}
}
