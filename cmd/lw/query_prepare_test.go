package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/llm"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// buildExpectedAskBrief constructs the frozen D3 ask brief for assertions.
func buildExpectedAskBrief(question, orientation string) string {
	var w strings.Builder
	cleanQuestion := strings.ReplaceAll(question, "\n", " ")
	fmt.Fprintf(&w, "# lw query --prepare — ask brief\nquestion: %s\n\n", cleanQuestion)
	w.WriteString("## How to work\n\n")
	writeBriefPart(&w, queryPrepareHowToWork)
	w.WriteString("\n## Answer rules\n\n")
	writeBriefPart(&w, agent.AskSystemPrompt())
	w.WriteString("\n## Vault orientation\n\n")
	writeBriefPart(&w, orientation)
	return w.String()
}

// TestQueryPrepareBrief pins 068 D3: lw query --prepare "<question>" renders the
// ask brief with no model, no config, and no key. Vault tree is byte-identical before/after.
func TestQueryPrepareBrief(t *testing.T) {
	t.Run("no open changeset", func(t *testing.T) {
		prepHermetic(t)
		prepNoAgent(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		root := testutil.CopyFixture(t, "minimal")
		e, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		reg := tools.NewRegistry(askToolDeps(e))
		ctx := trace.WithVerb(context.Background(), trace.VerbQuery)
		orientRes, err := reg.Call(ctx, "vault.orient", []byte("{}"))
		if err != nil {
			t.Fatalf("vault.orient: %v", err)
		}
		e.Close()

		snapBefore := vaultTreeSnapshot(t, root)
		question := "what is X?\nand Y"
		want := buildExpectedAskBrief(question, orientRes.Content)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"query", "--prepare", "--vault", root, question})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
		}
		if stderr != "" {
			t.Fatalf("stderr = %q, want empty", stderr)
		}
		if stdout != want {
			t.Fatalf("stdout mismatch:\ngot:\n%s\nwant:\n%s", stdout, want)
		}
		if strings.Contains(stdout, "## Open changeset") {
			t.Error("stdout unexpectedly contains '## Open changeset' with none open")
		}
		if !strings.Contains(stdout, "question: what is X? and Y\n") {
			t.Errorf("stdout missing cleaned question line; got:\n%s", stdout)
		}

		snapAfter := vaultTreeSnapshot(t, root)
		assertSnapshotsEqual(t, snapBefore, snapAfter)
	})

	t.Run("with open changeset", func(t *testing.T) {
		prepHermetic(t)
		prepNoAgent(t)
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())

		root := testutil.CopyFixture(t, "minimal")
		e, err := stage.OpenEngine(root)
		if err != nil {
			t.Fatalf("OpenEngine: %v", err)
		}
		_, err = e.OpenChangeset("orient test", stage.Author{Kind: "user"})
		if err != nil {
			t.Fatalf("OpenChangeset: %v", err)
		}
		_, err = e.Append(stage.Op{
			Kind:       stage.OpCreatePage,
			Path:       "wiki/concepts/staged-concept.md",
			Content:    []byte("---\ntitle: Staged Concept\ncreated: 2026-08-30\nupdated: 2026-08-30\ntype: concept\ntags: [inference, memory]\nsources: [raw/articles/kv-cache-explained.md]\nconfidence: medium\n---\n\n# Staged Concept\n\nBody text.^[raw/articles/kv-cache-explained.md]\n\n## Related\n\n- [[kv-cache]]\n- [[flash-attention]]\n"),
			Rationale:  "orient test",
			Provenance: []string{"raw/articles/kv-cache-explained.md"},
		})
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		reg := tools.NewRegistry(askToolDeps(e))
		ctx := trace.WithVerb(context.Background(), trace.VerbQuery)
		orientRes, err := reg.Call(ctx, "vault.orient", []byte("{}"))
		if err != nil {
			t.Fatalf("vault.orient: %v", err)
		}
		e.Close()

		snapBefore := vaultTreeSnapshot(t, root)
		question := "what is X?\nand Y"
		want := buildExpectedAskBrief(question, orientRes.Content)

		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"query", "--prepare", "--vault", root, question})
		})
		if code != 0 {
			t.Fatalf("exit code = %d, want 0; stderr = %q", code, stderr)
		}
		if stderr != "" {
			t.Fatalf("stderr = %q, want empty", stderr)
		}
		if stdout != want {
			t.Fatalf("stdout mismatch:\ngot:\n%s\nwant:\n%s", stdout, want)
		}
		if !strings.Contains(stdout, "## Open changeset") {
			t.Error("stdout missing '## Open changeset' when changeset is open")
		}

		snapAfter := vaultTreeSnapshot(t, root)
		assertSnapshotsEqual(t, snapBefore, snapAfter)
	})
}

// TestQueryPrepareUsage pins 068 D3 usage errors:
// --prepare with no question, and with "" → exit 2, stderr exactly usage: lw query [--prepare] "..."\n.
// Plain lw query with no question → the same line, exit 2.
func TestQueryPrepareUsage(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	const wantUsage = "usage: lw query [--prepare] \"...\"\n"

	cases := []struct {
		name string
		args []string
	}{
		{"prepare with no question", []string{"query", "--prepare", "--vault", root}},
		{"prepare with empty question", []string{"query", "--prepare", "", "--vault", root}},
		{"plain query with no question", []string{"query", "--vault", root}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, code := captureRun(t, func() int {
				return run(tc.args)
			})
			if code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want empty", stdout)
			}
			if stderr != wantUsage {
				t.Fatalf("stderr = %q, want %q", stderr, wantUsage)
			}
		})
	}
}

// TestQueryPrepareArgShapesMatchSchemas pins 068 D3: each example call in
// queryPrepareHowToWork parses, and its keys are in that tool's schema properties.
func TestQueryPrepareArgShapesMatchSchemas(t *testing.T) {
	re := regexp.MustCompile("(?m)^    lw tool --ask ([a-z_.]+) '(\\{.*\\})'$")
	found := map[string]string{}
	for _, m := range re.FindAllStringSubmatch(queryPrepareHowToWork, -1) {
		found[m[1]] = m[2]
	}
	for _, want := range []string{"wiki.search", "wiki.get", "raw.get", "raw.list"} {
		if _, ok := found[want]; !ok {
			t.Errorf("the brief shows no argument shape for %s", want)
		}
	}
	for name, args := range found {
		props, required, closed := schemaOf(t, name)
		var got map[string]any
		if err := json.Unmarshal([]byte(args), &got); err != nil {
			t.Errorf("%s example %s is not a JSON object: %v", name, args, err)
			continue
		}
		for key, val := range got {
			p, ok := props[key]
			if !ok {
				if closed {
					t.Errorf("%s example %s uses %q, which is not in the tool's schema", name, args, key)
				}
				continue
			}
			switch p.Type {
			case "string":
				if _, ok := val.(string); !ok {
					t.Errorf("%s example: %q must be a string, got %v", name, key, val)
				}
			case "integer":
				if f, ok := val.(float64); !ok || f != float64(int(f)) {
					t.Errorf("%s example: %q must be an integer, got %v", name, key, val)
				}
			case "boolean":
				if _, ok := val.(bool); !ok {
					t.Errorf("%s example: %q must be a boolean, got %v", name, key, val)
				}
			case "array":
				if _, ok := val.([]any); !ok {
					t.Errorf("%s example: %q must be an array, got %v", name, key, val)
				}
			}
		}
		for _, key := range required {
			if _, ok := got[key]; !ok {
				t.Errorf("%s example %s lacks the required %q", name, args, key)
			}
		}
	}

	// The read-only examples are accepted as they stand by a real vault.
	root := testutil.CopyFixture(t, "minimal")
	for _, name := range []string{"wiki.search", "wiki.get", "raw.list"} {
		stdout, stderr, code := captureRun(t, func() int {
			return run([]string{"tool", "--ask", "--vault", root, name, found[name]})
		})
		if code != 0 {
			t.Errorf("lw tool --ask %s '%s': exit %d stdout %q stderr %q, want the example to be accepted", name, found[name], code, stdout, stderr)
		}
	}
}

type queryFailingAgent struct {
	err      error
	sessions agent.SessionStore
}

func (f *queryFailingAgent) Send(ctx context.Context, sessionID, msg string, out chan<- agent.Event) error {
	close(out)
	return f.err
}

func (f *queryFailingAgent) Sessions() agent.SessionStore {
	return f.sessions
}

// TestQueryAccessHint pins 068 D4.4:
// fake agent failing with &llm.ProviderError{Status: s} for s in {401, 402, 403, 429}
// -> stderr contains exactly one hint line (HTTP <s>), error text unchanged from base.
// s = 500, llm.ErrStalled, transport ProviderError -> no hint: line.
// Config llm.api_key = "env:LW_TEST_068_UNSET" with that var unset, real newAgent
// -> no-key hint line, error text == base.
func TestQueryAccessHint(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	for _, s := range []int{401, 402, 403, 429} {
		s := s
		t.Run(fmt.Sprintf("status_%d", s), func(t *testing.T) {
			withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
				return &queryFailingAgent{
					err:      &llm.ProviderError{Status: s},
					sessions: sessions,
				}, nil
			})
			_, stderr, code := captureRun(t, func() int {
				return run([]string{"query", "--vault", root, "test question"})
			})
			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			wantHint := fmt.Sprintf("hint: the provider refused access (HTTP %d). An agent with its own model can still answer from this vault: lw query --prepare \"<your question>\"\n", s)
			if !strings.Contains(stderr, wantHint) {
				t.Fatalf("stderr missing expected hint line:\n%s\nwant containing:\n%s", stderr, wantHint)
			}
			// Verify exactly one hint line.
			hintCount := strings.Count(stderr, "hint:")
			if hintCount != 1 {
				t.Fatalf("stderr has %d 'hint:' lines, want 1:\n%s", hintCount, stderr)
			}
			wantErr := fmt.Sprintf("lw: query: agent turn: %s\n", (&llm.ProviderError{Status: s}).Error())
			if !strings.Contains(stderr, wantErr) {
				t.Fatalf("stderr error text changed from base:\n%s\nwant containing:\n%s", stderr, wantErr)
			}
		})
	}

	t.Run("status_500_no_hint", func(t *testing.T) {
		withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
			return &queryFailingAgent{
				err:      &llm.ProviderError{Status: 500},
				sessions: sessions,
			}, nil
		})
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"query", "--vault", root, "test question"})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if strings.Contains(stderr, "hint:") {
			t.Fatalf("stderr unexpectedly contains hint on 500:\n%s", stderr)
		}
	})

	t.Run("stall_no_hint", func(t *testing.T) {
		withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
			return &queryFailingAgent{
				err:      llm.ErrStalled,
				sessions: sessions,
			}, nil
		})
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"query", "--vault", root, "test question"})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if strings.Contains(stderr, "hint:") {
			t.Fatalf("stderr unexpectedly contains hint on stall:\n%s", stderr)
		}
	})

	t.Run("transport_no_hint", func(t *testing.T) {
		withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
			return &queryFailingAgent{
				err:      &llm.ProviderError{Transport: true, Message: "dial tcp: connection refused"},
				sessions: sessions,
			}, nil
		})
		_, stderr, code := captureRun(t, func() int {
			return run([]string{"query", "--vault", root, "test question"})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		if strings.Contains(stderr, "hint:") {
			t.Fatalf("stderr unexpectedly contains hint on transport failure:\n%s", stderr)
		}
	})

	t.Run("no_api_key_configured_real_agent", func(t *testing.T) {
		configDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", configDir)
		t.Setenv("LW_TEST_068_UNSET", "")
		os.Unsetenv("LW_TEST_068_UNSET")

		lwConfigDir := filepath.Join(configDir, "lw")
		if err := os.MkdirAll(lwConfigDir, 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		configContent := "[llm]\napi_key = \"env:LW_TEST_068_UNSET\"\n"
		if err := os.WriteFile(filepath.Join(lwConfigDir, "config.toml"), []byte(configContent), 0o644); err != nil {
			t.Fatalf("WriteFile config.toml: %v", err)
		}

		_, stderr, code := captureRun(t, func() int {
			return run([]string{"query", "--vault", root, "test question"})
		})
		if code != 1 {
			t.Fatalf("exit code = %d, want 1", code)
		}
		const wantNoKeyHint = "hint: no LLM API key is configured. An agent with its own model can still answer from this vault: lw query --prepare \"<your question>\"\n"
		if !strings.Contains(stderr, wantNoKeyHint) {
			t.Fatalf("stderr missing no-key hint:\n%s\nwant containing:\n%s", stderr, wantNoKeyHint)
		}
		const wantBaseErr = "lw: query: construct agent: resolve api key: config: environment variable LW_TEST_068_UNSET is not set\n"
		if !strings.Contains(stderr, wantBaseErr) {
			t.Fatalf("stderr error text mismatch:\n%s\nwant containing:\n%s", stderr, wantBaseErr)
		}
		hintCount := strings.Count(stderr, "hint:")
		if hintCount != 1 {
			t.Fatalf("stderr has %d 'hint:' lines, want 1:\n%s", hintCount, stderr)
		}
	})
}
