package main

// query_prepare.go is `lw query --prepare` (068): deterministic preparation
// of an ask brief for an external agent to answer read-only.
//
// Why it exists: when lw's configured LLM provider has no access (quota 429,
// billing 402, auth 401/403) or when no API key is configured at all, an agent
// with its own model sitting in the same shell can still answer questions from
// the vault. Like `lw ingest --prepare` for ingest (063), `lw query --prepare`
// does what needs no model — prints an ask brief containing the question,
// how to work, answer rules (the in-process ask system prompt), and the vault
// orientation (under trace.VerbQuery) — and stops. The external agent explores
// the vault read-only through `lw tool --ask` or `lw mcp --ask`.
//
// The ask path is read-only by construction (I3): no changeset is opened,
// no lock is taken, no session record is written, and no vault file is changed.

import (
	"context"
	"fmt"
	"strings"

	"github.com/awepo-pro/lw/internal/agent"
	"github.com/awepo-pro/lw/internal/stage"
	"github.com/awepo-pro/lw/internal/tools"
	"github.com/awepo-pro/lw/internal/trace"
)

// queryPrepareHowToWork is the verbatim instructions section of the ask brief (068 D3).
const queryPrepareHowToWork = "Every call is `lw tool --ask <name> '<json>'`, or `lw tool --ask <name> --from -` with a heredoc. The key names matter. The shape of the tools you will reach for first:\n" +
	"\n" +
	"    lw tool --ask wiki.search '{\"q\":\"kv cache\",\"limit\":5}'\n" +
	"    lw tool --ask wiki.get '{\"page\":\"wiki/concepts/kv-cache.md\"}'\n" +
	"    lw tool --ask raw.get '{\"source\":\"raw/articles/x.md\",\"chunk\":1}'\n" +
	"    lw tool --ask raw.list '{\"query\":\"kv\"}'\n" +
	"\n" +
	"`lw tool --ask --list` prints every tool you may call, with its JSON schema. If you lose this brief, run the same `lw query --prepare` again.\n" +
	"\n" +
	"1. Find the relevant pages with `wiki.search` and read them with `wiki.get`; read the cited raw source with `raw.get` when a page is thin. Several calls may run in parallel.\n" +
	"2. Answer the question in your reply, following the answer rules below. End every claim drawn from the vault with its `^[raw/...]` marker.\n" +
	"3. A result marked as staged comes from the open changeset, which no human has reviewed yet. Cite it as usual. When a call prints a line starting `note:` on stderr, end your answer with that line, verbatim.\n" +
	"4. A refusal exits 1 and its text says how to fix the call; fix it and call again.\n" +
	"5. This is read-only. No changeset is opened and `lw tool --ask` offers no staging tool. You have a shell, but never edit vault files and never run `lw commit`.\n"

// askToolDeps returns the tools.Deps for an ask turn or external ask command (068):
// read-only tools over the engine's vault and index, with no author, no extract,
// and no search. OrientCommittedOnly is false so vault.orient under trace.VerbQuery
// sees an open changeset if one exists.
func askToolDeps(e *stage.Engine) tools.Deps {
	return tools.Deps{
		Vault:  e.Vault(),
		Index:  e.Index(),
		Engine: e,
	}
}

// renderAskBrief renders the ask brief, deterministic for its inputs:
// title and question line, how to work, answer rules (agent.AskSystemPrompt()),
// and vault orientation (under trace.VerbQuery).
func renderAskBrief(question, orientation string) string {
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

// queryPrepare runs `lw query --prepare "<question>"`: finds the vault,
// attaches logging (read-only), fetches vault orientation under trace.VerbQuery,
// and prints the brief to stdout.
func queryPrepare(vaultPath, question string) error {
	root, err := findVaultRoot(vaultPath)
	if err != nil {
		return err
	}
	attachLoggingAt(root)
	e, err := openVaultEngine(root, nil)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer e.Close()

	reg := tools.NewRegistry(askToolDeps(e))
	ctx := trace.WithVerb(context.Background(), trace.VerbQuery)
	orient, err := orientBrief(ctx, reg)
	if err != nil {
		return fmt.Errorf("vault.orient: %w", err)
	}
	if orient.IsError {
		return fmt.Errorf("vault.orient: %s", orient.Content)
	}

	fmt.Print(renderAskBrief(question, orient.Content))
	return nil
}
