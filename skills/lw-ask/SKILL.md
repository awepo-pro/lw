---
name: lw-ask
description: >-
  Answer questions from the user's lw (llmwiki) vault using your own model — lw calls no model and needs no API key.
  Use when the user says "ask my wiki/vault …", "what does my vault/notes say about …", "lw query …", "search my lw notes for …",
  or when lw query fails with a provider error. You explore the vault read-only through `lw tool --ask` and synthesize a cited
  answer. Works in any agent with a shell (Claude Code, Codex, Antigravity).
---

# lw-ask — answer questions from an lw vault yourself

lw compiles immutable sources into a cross-linked markdown wiki where every claim cites its source. When lw's own
model is unavailable (e.g. provider access error or no configured key), or when answering directly from within an agent
session, you answer questions yourself: lw prepares the ask brief, you explore the vault read-only through
`lw tool --ask`, and you synthesize a cited answer. Follow this procedure exactly.

## Rules you must not break

1. **Never write vault files yourself** — no editor, no `cat >`, no `sed -i`, no file creation, editing, or removal
   under the vault. The ask path is strictly read-only.
2. **Never run `lw commit`, `lw sync`, `lw config set`, `lw doctor --discard-changeset`, `lw revert`, or any `lw tool`
   call without `--ask`.** Calling `lw tool` without `--ask` belongs to ingest workflows with an open staging changeset.
   For questions, every tool call must include `--ask`. `lw search` and `lw show` are fine: they only read.
3. **Cite only what you read.** Every factual claim drawn from the vault must end with its citation marker
   (e.g. `^[raw/<path>.md]` or `^[raw/<path>.md p.N]`) copied from the wiki page or raw source. Never cite anything you
   did not read, and never invent citations.
4. **Relay `note:` lines verbatim.** If any tool call prints a line starting with `note:` on stderr (indicating
   unreviewed staged content from an open changeset), you must end your final answer with that exact line verbatim.
5. **Stay inside the brief.** The brief `lw query --prepare` prints is the authority for answering this question — its
   answer rules and vault orientation guide your exploration.

## Step 0 — find the lw binary (once)

Your shell may not have lw on PATH (sudo, VPN namespaces and agent tool shells reset it). Find it once:

```bash
for c in "$(command -v lw)" "$HOME/go/bin/lw" "$HOME/.local/bin/lw" /usr/local/bin/lw; do
  [ -x "$c" ] && echo "$(readlink -f "$c"): $("$c" version)"; done; true
```

Use the newest one that prints `lw v2.53.0` or later, and from now on write its **absolute path** in every command
(shell variables may not survive between your tool calls). Below, `LW` stands for that absolute path. If none is
v2.53.0+, stop and tell the user `lw query --prepare` needs v2.53.0.

## Step 1 — locate the vault

If the user specifies a vault path, pass `--vault <path>` (e.g. `LW query --vault <path> --prepare "..."` and
`LW tool --vault <path> --ask ...`). Otherwise, omit `--vault` to run from the current directory or rely on the
configured `[vault] path`.

## Quick lookups (no brief)

When the user only wants to FIND or READ a wiki page ("where is my page on X", "show me the page on Y", "which
pages mention Z") and not a synthesized answer, skip the brief:

```bash
LW search [--vault <path>] <words>        # ranked committed pages with a snippet; exit 1 = no match
LW show [--vault <path>] [--links] <page> # one page by path, basename, title or alias
```

Report what they print. Both are read-only, need no key, and exist from `lw v2.66.0` (check `LW version`; on an older
lw, use the normal procedure). They see committed wiki pages only, not `notes/` or `raw/`. For any real question,
follow Step 2 onward as before.

## Step 2 — prepare the brief

```bash
LW query [--vault <path>] --prepare "<question>"
```

- Read the entire stdout. It is your brief:
  - `question:` the question being asked.
  - `## How to work:` workflow instructions and example tool calls.
  - `## Answer rules:` persona, tone, formatting, and citation requirements.
  - `## Vault orientation:` schema tags, index of existing pages, and any open changeset.
- If you lose the brief later (e.g. after context compaction), run the same `LW query --prepare` command again.

## Step 3 — read and explore through tools

Query the vault using read-only ask tools:

```bash
LW tool [--vault <path>] --ask wiki.search '{"q":"kv cache","limit":5}'
LW tool [--vault <path>] --ask wiki.get '{"page":"wiki/concepts/kv-cache.md"}'
LW tool [--vault <path>] --ask raw.get '{"source":"raw/articles/x.md","chunk":1}'
LW tool [--vault <path>] --ask raw.list '{"query":"kv"}'
```

For complex or multiline JSON arguments, pass `--from -` with a heredoc:

```bash
LW tool [--vault <path>] --ask wiki.search --from - <<'EOF'
{"q": "kv cache", "limit": 5}
EOF
```

Available read-only ask tools:
- `wiki.search` `{"q":"...","limit":5}`: find pages by query (`q` is required).
- `wiki.get` `{"page":"..."}`: read a wiki page by path or basename (`page` is required; optional `"section"`).
- `raw.get` `{"source":"...","chunk":1}`: read a raw source chunk when a page is thin or citations require verification (`source` is required).
- `raw.list` `{"query":"..."}`: list raw sources matching query (`query` optional).
- `vault.orient` `{}`: view schema tags and vault overview.
- `wiki.neighbors` `{"page":"...","depth":1}`: inspect neighbouring pages in the link graph (`page` is required).
- `wiki.backlinks` `{"page":"..."}`: list pages linking to this page (`page` is required).
- `wiki.lint` `{}`: check vault consistency.

`LW tool --ask --list` prints every offered ask tool with its complete JSON schema.

- Independent tool calls may run in parallel.
- Staged results: results marked `(staged in <id>, unreviewed)` come from an unreviewed open changeset. Cite them as usual.
- Check stderr: if any tool call outputs a line starting with `note:`, save that line to append to your final answer.

## Exit codes of `lw tool --ask`

| Exit | Meaning | Do |
|---|---|---|
| 0 | done; result on stdout | continue reading / answering |
| 1 | tool refused or returned an error; details on stdout/stderr | fix the JSON arguments and retry; do not repeat identical failing calls |
| 2 | usage: tool not offered for ask, bad JSON, or invalid flags | run `LW tool --ask --list` to check tool names and schemas |

Wire names work too (e.g. `wiki_search` = `wiki.search`). Only read-only ask tools are offered (no staging tools, no `web.search`).

## Step 4 — synthesize the answer

1. Answer the user's question directly, clearly, and concisely using the facts retrieved from the vault.
2. Follow the brief's `## Answer rules`.
3. End every factual claim drawn from the vault with its citation marker: `^[raw/<path>.md]` (or `^[raw/<path>.md p.N]`).
4. If any tool call printed a line starting with `note:` on stderr, end your answer with that exact line verbatim on its own line at the end.

## When lw's own model is unavailable

`lw query` normally calls an LLM provider configured in `~/.config/lw/config.toml`. When the provider refuses access (HTTP 401, 402, 403, 429 quota exhaustion) or when no LLM API key is configured, `lw query` prints a hint to stderr:

```
hint: the provider refused access (HTTP 429). An agent with its own model can still answer from this vault: lw query --prepare "<your question>"
```

In those situations, or whenever the user asks an external agent directly, this skill takes over: the agent acts as the model, reading the vault read-only without needing any lw LLM API key.

## MCP alternative

Clients configured for Model Context Protocol (MCP) can run:

```bash
LW mcp [--vault <path>] --ask
```

`lw mcp --ask` runs an MCP server over stdio, offering the same 8 read-only ask tools and instructions matching the vault's answer rules.
