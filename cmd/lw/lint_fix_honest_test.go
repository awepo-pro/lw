package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/config"
	"github.com/awepo-pro/lw/internal/index"
	"github.com/awepo-pro/lw/internal/lint"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/testutil"
	"github.com/awepo-pro/lw/internal/vault"
)

// TestLintFixHonest_F3_1_InfoOnlyVault pins workflow 081, F3.1:
// A vault whose report has ONLY info findings: `lw lint --fix --vault V` with
// NO provider config -> exit 0, stdout contains
// "nothing to fix: N info finding(s) are advisory; lw lint lists them" (N = the count),
// no open changeset, no agent constructed.
func TestLintFixHonest_F3_1_InfoOnlyVault(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")

	// Mutate kv-cache.md so confidence: low fires check fm-quality (Severity: info).
	kvPath := filepath.Join(root, "wiki", "concepts", "kv-cache.md")
	content, err := os.ReadFile(kvPath)
	if err != nil {
		t.Fatalf("read kv-cache.md: %v", err)
	}
	newContent := strings.Replace(string(content), "confidence: high", "confidence: low", 1)
	if err := os.WriteFile(kvPath, []byte(newContent), 0o644); err != nil {
		t.Fatalf("write kv-cache.md: %v", err)
	}

	// Precondition: verify with lint.Run that only info findings exist.
	v, err := vault.Open(root)
	if err != nil {
		t.Fatalf("vault.Open: %v", err)
	}
	lintCtx := &lint.Context{Vault: v, Index: index.Build(v), Graph: v.Graph()}
	rep := lint.Run(lintCtx, nil)
	if len(rep.Findings) != 1 {
		t.Fatalf("precondition: expected exactly 1 finding, got %d: %+v", len(rep.Findings), rep.Findings)
	}
	if rep.Findings[0].Severity != lint.SevInfo {
		t.Fatalf("precondition: finding severity = %v, want SevInfo", rep.Findings[0].Severity)
	}
	if rep.Errors != 0 || rep.Warns != 0 {
		t.Fatalf("precondition: expected 0 errors and 0 warns, got %d errors, %d warns", rep.Errors, rep.Warns)
	}

	// Scratch XDG_CONFIG_HOME so no provider config is reachable.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// Fail the test if agent construction is attempted.
	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		t.Fatal("newAgent should never be called when only info findings exist")
		return nil, nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--fix", "--vault", root})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q stdout=%q", code, stderr, stdout)
	}
	const wantOut = "nothing to fix: 1 info finding(s) are advisory; lw lint lists them\n"
	if stdout != wantOut {
		t.Fatalf("stdout = %q, want %q; stderr=%q", stdout, wantOut, stderr)
	}

	// No open changeset should exist.
	eng, err := stage.OpenEngine(root)
	if err != nil {
		t.Fatalf("OpenEngine: %v", err)
	}
	defer eng.Close()
	if cs, err := eng.Current(); err == nil {
		t.Fatalf("changeset %s was opened, want no open changeset", cs.ID)
	}
}

// TestLintFixHonest_F3_2_Planning pins workflow 081, F3.2:
// Planning: a report with an info and a warn finding on page P and only an info
// finding on page Q -> exactly one round (P), its message lists the warn line
// and not the info line; intent = `lint --fix: repair 1 finding(s)`.
func TestLintFixHonest_F3_2_Planning(t *testing.T) {
	report := lint.Report{
		Findings: []lint.Finding{
			{Check: "fm-dates", Path: "wiki/concepts/p.md", Line: 4, Severity: lint.SevWarn, Message: "created is after updated"},
			{Check: "fm-quality", Path: "wiki/concepts/p.md", Line: 0, Severity: lint.SevInfo, Message: "confidence is low"},
			{Check: "fm-quality", Path: "wiki/concepts/q.md", Line: 0, Severity: lint.SevInfo, Message: "confidence is low"},
		},
		Warns: 1,
	}

	rounds, intent := planLintFix(report)
	if intent != "lint --fix: repair 1 finding(s)" {
		t.Errorf("intent = %q, want %q", intent, "lint --fix: repair 1 finding(s)")
	}
	if len(rounds) != 1 {
		t.Fatalf("got %d rounds, want 1", len(rounds))
	}
	if rounds[0].page != "wiki/concepts/p.md" {
		t.Errorf("rounds[0].page = %q, want %q", rounds[0].page, "wiki/concepts/p.md")
	}
	if len(rounds[0].findings) != 1 {
		t.Fatalf("got %d findings in round 0, want 1", len(rounds[0].findings))
	}
	if rounds[0].findings[0].Check != "fm-dates" {
		t.Errorf("rounds[0].findings[0].Check = %q, want %q", rounds[0].findings[0].Check, "fm-dates")
	}

	msg := buildLintFixRoundMessage(rounds[0], 0, len(rounds))
	if !strings.Contains(msg, "created is after updated") {
		t.Errorf("round message missing warn line:\n%s", msg)
	}
	if strings.Contains(msg, "confidence is low") {
		t.Errorf("round message unexpectedly contains info line:\n%s", msg)
	}
	if strings.Contains(msg, "wiki/concepts/q.md") {
		t.Errorf("round message unexpectedly mentions page Q:\n%s", msg)
	}
}

// TestLintFixHonest_F3_3_PageRoundFrozenMessage pins workflow 081, F3.3:
// The full message for a page round (i=0, n=2) equals the frozen text byte for byte.
func TestLintFixHonest_F3_3_PageRoundFrozenMessage(t *testing.T) {
	r := lintFixRound{
		page: "wiki/concepts/foo.md",
		findings: []lint.Finding{
			{Path: "wiki/concepts/foo.md", Line: 10, Severity: lint.SevWarn, Message: "created is after updated", Check: "fm-dates"},
		},
	}
	got := buildLintFixRoundMessage(r, 0, 2)
	const want = "The vault's lint check reported findings for page wiki/concepts/foo.md; this round covers that page only (round 1 of 2). For each finding below, stage a repair with the stage.* tools (a patch, a rename, a link, whatever fits) only when an honest one exists, stating your rationale. Touch this page only \u2014 no other page may be modified this round.\n\n" +
		"An honest repair makes the page better for its reader. Never add a link, citation, source or sentence only to satisfy a check: a link must lead to a page this page's reader would want next, and a citation must name a source that states the claim. When no honest repair exists (for example, no existing page is genuinely related), stage nothing for that finding and say why in one line; leaving a finding is an acceptable outcome. Text that lands on a page, a link's context included, describes the content and never mentions the lint check.\n\n" +
		"Do not close the changeset: more pages follow in later rounds.\n\n" +
		"- wiki/concepts/foo.md:10: warn: created is after updated (fm-dates)\n"

	if got != want {
		t.Fatalf("page round message mismatch.\nGOT:\n%s\nWANT:\n%s", got, want)
	}
}

// TestLintFixHonest_F3_3_VaultRoundFrozenMessage pins workflow 081, F3.3:
// The full message for a vault round (i=1, n=2) equals the frozen text byte for byte.
func TestLintFixHonest_F3_3_VaultRoundFrozenMessage(t *testing.T) {
	r := lintFixRound{
		page: "",
		findings: []lint.Finding{
			{Path: "", Line: 0, Severity: lint.SevWarn, Message: "orphaned edge detected", Check: "link-orphan"},
		},
	}
	got := buildLintFixRoundMessage(r, 1, 2)
	const want = "The vault's lint check reported 1 vault-level finding(s) (round 2 of 2). They are not tied to a single page. For each one, stage a repair with the stage.* tools (a patch, a rename, a link, whatever fits) only when an honest one exists, stating your rationale, and touch only what these findings name \u2014 no other page may be modified this round.\n\n" +
		"An honest repair makes the page better for its reader. Never add a link, citation, source or sentence only to satisfy a check: a link must lead to a page this page's reader would want next, and a citation must name a source that states the claim. When no honest repair exists (for example, no existing page is genuinely related), stage nothing for that finding and say why in one line; leaving a finding is an acceptable outcome. Text that lands on a page, a link's context included, describes the content and never mentions the lint check.\n\n" +
		"This is the last round. When you are done, call stage.close to summarize the proposed changeset.\n\n" +
		"- :0: warn: orphaned edge detected (link-orphan)\n"

	if got != want {
		t.Fatalf("vault round message mismatch.\nGOT:\n%s\nWANT:\n%s", got, want)
	}
}

// TestLintFixHonest_CleanVault pins workflow 081:
// An all-clean vault still prints clean (unchanged).
func TestLintFixHonest_CleanVault(t *testing.T) {
	root := testutil.CopyFixture(t, "minimal")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	withFakeAgent(t, func(e *stage.Engine, cfg *config.Config, sessions agent.SessionStore) (agent.Agent, error) {
		t.Fatal("newAgent should never be called for an all-clean vault")
		return nil, nil
	})

	stdout, stderr, code := captureRun(t, func() int {
		return run([]string{"lint", "--fix", "--vault", root})
	})

	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%q", code, stderr)
	}
	if stdout != "clean\n" {
		t.Fatalf("stdout = %q, want %q", stdout, "clean\n")
	}
}
