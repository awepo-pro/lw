package config

// baseurl_test.go pins 061 D3: llm.base_url means the API root, because lw
// appends /chat/completions itself. The s47 live failure was a base_url that
// already ended in it, which became ".../chat/completions/chat/completions"
// and came back as an unexplained 404 — so the validator names the fix.

import "testing"

func TestValidateBaseURL(t *testing.T) {
	const wantFmt = "config: llm.base_url ends in /chat/completions, but lw adds that path itself — set it to the API root: lw config set llm.base_url "

	cases := []struct {
		name string
		in   string
		want string // "" = accepted; otherwise the exact error text
	}{
		{"deepseek root", "https://api.deepseek.com/v1", ""},
		{"openrouter root with slash", "https://openrouter.ai/api/v1/", ""},
		{"empty is the key absent", "", ""},
		{"suffix", "https://x/v1/chat/completions", wantFmt + "https://x/v1"},
		{"mixed case suffix with slash", "https://x/v1/Chat/Completions/", wantFmt + "https://x/v1"},
		{"the s47 value", "https://openrouter.ai/api/v1/chat/completions", wantFmt + "https://openrouter.ai/api/v1"},
		{"several trailing slashes", "https://x/v1/chat/completions///", wantFmt + "https://x/v1"},
		{"upper case suffix", "https://x/v1/CHAT/COMPLETIONS", wantFmt + "https://x/v1"},
		{"suffix inside the path is fine", "https://x/chat/completions/v1", ""},
		{"similar but not the suffix", "https://x/v1/chat/completion", ""},
		{"shorter than the suffix", "https://x", ""},
		// FIX2 R9: a pasted value carries whitespace; the suffix is not hidden
		// behind it, and a plain root with some is still a plain root.
		{"suffix then a trailing space", "https://x/v1/chat/completions ", wantFmt + "https://x/v1"},
		{"suffix, slash, then a newline", "https://x/v1/chat/completions/\n", wantFmt + "https://x/v1"},
		{"leading space and the suffix", "  https://x/v1/chat/completions", wantFmt + "https://x/v1"},
		{"root with surrounding space is fine", " https://x/v1 ", ""},
		{"only whitespace is the key absent", "   ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBaseURL(tc.in)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("ValidateBaseURL(%q) = %v, want nil", tc.in, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateBaseURL(%q) = nil, want %q", tc.in, tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("ValidateBaseURL(%q) =\n%q\nwant\n%q", tc.in, err.Error(), tc.want)
			}
		})
	}
}

// TestLoadAcceptsBadBaseURL pins D3's other half: Load does not refuse the
// value. If it did, no lw command could start, and `lw config set llm.base_url`
// — the very repair the validator's text points at — could never run.
func TestLoadAcceptsBadBaseURL(t *testing.T) {
	dir := withConfigDir(t)
	writeConfig(t, dir, `[llm]
model = "m"
base_url = "https://openrouter.ai/api/v1/chat/completions"
`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with a /chat/completions base_url: %v, want nil so config set can repair it", err)
	}
	if got := cfg.LLM.BaseURL; got != "https://openrouter.ai/api/v1/chat/completions" {
		t.Fatalf("Load changed base_url to %q", got)
	}
}
