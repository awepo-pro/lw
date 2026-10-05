package main

// query_prefix_test.go is 039's pin on the one line `lw query` puts in front
// of the question. The prefix used to say "citing the wiki pages you draw from
// by path" while the system prompt said "Never narrate your sources" — a
// contradiction the model resolved by narrating. Citation rules now live in
// the ask prompt (internal/agent), where they cite raw sources by provenance
// marker, so the prefix carries only the read-only framing. Permanent
// regression test (D-10C).

import "testing"

func TestQueryPrefixBytes(t *testing.T) {
	const want = "Answer the following question about the vault. This is a read-only query: do not open a changeset or propose any change.\n\nQuestion: "
	if queryPromptPrefix != want {
		t.Fatalf("queryPromptPrefix drifted:\n got  %q\n want %q", queryPromptPrefix, want)
	}
}
