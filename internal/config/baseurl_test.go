package config

// baseurl_test.go pins 061 D3: llm.base_url means the API root, because lw
// appends /chat/completions itself. The s47 live failure was a base_url that
// already ended in it, which became ".../chat/completions/chat/completions"
// and came back as an unexplained 404 — so the validator names the fix.

import "testing"

func TestValidateBaseURL(t *testing.T) {
	const wantFmt = "config: llm.base_url ends in /chat/completions, but lw adds that path itself — set it to the API root: lw config set llm.base_url "
	const wantShape = "config: llm.base_url must be an http(s) URL with a host, e.g. https://api.deepseek.com/v1"

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
		// FIX3 F1: the value must be a URL the HTTP client can use. A scheme-less
		// "api.example.com/v1" went to the client, came back "unsupported
		// protocol scheme" — a permanent failure retried for 14 s.
		{"no scheme", "api.example.com/v1", wantShape},
		{"host and port with no scheme parses as a scheme", "localhost:8080/v1", wantShape},
		{"ftp scheme", "ftp://api.example.com/v1", wantShape},
		{"scheme with no host", "https:///v1", wantShape},
		{"http with no host at all", "http://", wantShape},
		{"port only host", "http://:8080/v1", wantShape},
		{"a path is not a URL", "/v1", wantShape},
		{"spaces inside", "https://api example.com/v1", wantShape},
		{"http with a port is fine", "http://localhost:8080/v1", ""},
		{"https any case is fine", "HTTPS://API.EXAMPLE.COM/v1", ""},
		{"ipv6 host is fine", "http://[::1]:8080/v1", ""},
		// The suffix refusal is checked first: it is the older, more specific text.
		{"suffix wins over a missing scheme", "api.example.com/v1/chat/completions", wantFmt + "api.example.com/v1"},
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

// TestLoadTrimsBaseURL (FIX3 F7): whatever the file holds, the loaded base_url
// carries no surrounding white space — the endpoint is built from it, and a
// trailing space or newline in a hand-edited value made a URL the client
// refused.
func TestLoadTrimsBaseURL(t *testing.T) {
	dir := withConfigDir(t)
	writeConfig(t, dir, "[llm]\nmodel = \"m\"\nbase_url = \" https://api.deepseek.com/v1 \\n\"\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.LLM.BaseURL; got != "https://api.deepseek.com/v1" {
		t.Fatalf("Load base_url = %q, want it trimmed", got)
	}
}
