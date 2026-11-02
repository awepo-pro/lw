---
name: lw-ingest
description: >-
  Ingest a source (PDF, HTML, markdown, text, URL, or a folder of them) into the user's lw (llmwiki) vault, with YOU as
  the curator — lw calls no model and needs no API key. Use when the user says "ingest X", "add this paper/article to
  my wiki/vault", "lw ingest X", "compile X into wiki pages", or "recompile raw/…". You stage reviewable wiki pages
  through `lw tool`; the human reviews and commits. Works in any agent with a shell (Claude Code, Codex, Antigravity).
---

# lw-ingest — curate an lw ingest yourself

lw compiles immutable sources into a cross-linked markdown wiki where every claim cites its source. You do the
curation: lw prepares and stages the source, you read it and propose pages through `lw tool`, and a human reviews every
hunk before anything lands. Follow this procedure exactly.

## Rules you must not break

1. **Never write vault files yourself** — no editor, no `cat >`, no `sed -i`, no `mv`/`rm` under the vault. Every
   change is a `lw tool stage.*` call. You have a shell, but the review gate is a promise you keep.
2. **Never run `lw commit`, `lw sync`, `lw doctor --discard-changeset`, `lw revert`, or `lw config set`.** Committing,
   discarding and configuration belong to the human. If something needs them, say so in your report.
3. **Read every chunk of every source before you close.** Never shorten a chunk with `head`, `tail`, `cut` or `grep`;
   a chunk is at most 16 000 characters and fits in one tool result.
4. **Cite everything.** Every claim taken from a source ends with `^[raw/<path>.md]`, or `^[raw/<path>.md p.N]` when
   the chunk header names pages. Do not cite anything you did not read.
5. **Stay inside the brief.** The brief `lw ingest --prepare` prints is the authority for this ingest — its curator
   rules override your own style.

## Step 0 — find the lw binary (once)

Your shell may not have lw on PATH (sudo, VPN namespaces and agent tool shells reset it). Find it once:

```bash
for c in "$(command -v lw)" "$HOME/go/bin/lw" "$HOME/.local/bin/lw" /usr/local/bin/lw; do
  [ -x "$c" ] && echo "$(readlink -f "$c"): $("$c" version)"; done; true
```

Use the newest one that prints `lw v2.45.0` or later, and from now on write its **absolute path** in every command
(shell variables may not survive between your tool calls). Below, `LW` stands for that absolute path. If none is
v2.45.0+, stop and tell the user `lw ingest --prepare` needs v2.45.0.

## Step 1 — prepare

```bash
LW ingest --prepare --agent <you>:<model> <source>...
```

- `<you>:<model>` labels the changeset author the reviewer sees: e.g. `claude-code:claude-opus-5-5`,
  `codex:gpt-5.6`, `agy:gemini-3-pro`. Use the model you actually are, if you know it; else just `<you>`.
- Sources: files, URLs, folders (≤10 files per ingest). `--recompile raw/<path>.md` recompiles a committed raw source.
  Add `--vault <dir>` only if the user names a vault.
- If the user gave extra wording beyond the source ("only chapter 3", "tag it cuda", "in Chinese"), pass it VERBATIM with
  `--instruction "<their words>"` (or `--instruction-file <path>` for a long one; needs lw v2.60.0+). Never paraphrase it away or
  drop it; it steers what to cover, and the Curator rules in the brief still win.
- "Ingest my waiting notes": `LW note list --pending` prints only the notes no committed raw has been ingested from (needs lw v2.61.0+);
  ingest each with `--note <id>` as below (one `--prepare` call may carry several `--note` flags). `no pending notes` means there is nothing to do.
- A quick note from `lw note list` (needs lw v2.58.0+): ingest it by its id, from any directory, with
  `LW ingest --prepare --agent <you>:<model> --note <id-or-unique-prefix>` (repeatable). Do not guess a path to `notes/`;
  an ambiguous or unknown id is an error that lists the candidates — show it to the user.
- Read the whole stdout. It is the brief: **Sources to compile** (raw paths + chunk counts), **How to work** (exact
  argument shapes), **Curator rules**, **Vault orientation** (schema tags, index of existing pages).
- Lines like `sync: pull failed …; working on the local vault` are warnings — continue.
- `skipped …: already in the vault` / `nothing to ingest` → stop and tell the user; the source is already ingested
  (offer `--recompile raw/<path>.md` if they want pages rebuilt from it).
- A PDF failing with docling not found → stop and tell the user to run
  `lw config set extract.command "$HOME/.local/bin/docling"` (or wherever docling is).
- Lost the brief later (context compaction)? `LW tool --brief` prints it again.

## Step 2 — read

```bash
LW tool raw.get '{"source":"raw/papers/x.md","chunk":1}'
LW tool raw.get '{"source":"raw/papers/x.md","chunk":2}'      # independent calls may run in parallel
LW tool wiki.search '{"q":"kv cache","limit":5}'               # the key is "q"
LW tool wiki.get '{"page":"wiki/concepts/kv-cache.md"}'
```

Read all chunks named in the brief. Search the wiki for the source's main topics and read only the pages the search
ties to it — do not survey the whole wiki. `LW tool --list` prints every tool's JSON schema; read the `stage.*`
schemas before your first stage call.

## Step 3 — stage pages

Send page JSON through stdin so shell quoting never touches it:

```bash
LW tool stage.create_page --from - <<'EOF'
{"path": "wiki/concepts/example.md", "title": "Example", "type": "concept",
 "tags": ["methods"], "sources": ["raw/papers/x.md"], "confidence": "medium", "contested": false,
 "body": "## Abstract\n\nTwo to four plain sentences, no citations.\n\n## …\n\nClaim. ^[raw/papers/x.md p.3]\n\n## Related\n\n- [[other-page]] — why\n- [[another-page]] — why\n",
 "rationale": "Why this page should exist, for the human reviewer."}
EOF
```

- Every page body starts with `## Abstract` (pure prose, no `^[…]`), has ≥2 `[[wikilinks]]` to pages that exist or
  that you also stage, uses only tags from SCHEMA.md, and lives under `wiki/{entities,concepts,comparisons,queries}/`.
- When a raw source embeds figures or diagrams under `../../raw/assets/`, preserve the relevant
  `![alt](../../raw/assets/<name>)` markdown image links as-is in the wiki pages you create or patch — the relative
  path resolves identically from both `raw/` and `wiki/`. Never invent an asset path that does not appear in the source.
- Prefer improving an existing page over creating a near-duplicate: `stage.patch_page` with `op` `append_section`,
  `insert_after`, or `replace_text` (`find` must match exactly once in the section). `section` is the exact heading
  line, e.g. `"## Related"`. Never patch `index.md` — lw maintains it.
- Do not create a page for a single fact or a passing claim. Give every call a real `rationale`.
- Independent stage calls may run in parallel (lw serializes them; none are lost).

## Step 4 — close

```bash
LW tool stage.close
```

- Exit 1 naming unread chunks → read them, then close again. (A second close over the same unread set goes through,
  but only do that if a chunk truly cannot be read — and say so in your report.)
- Read the summary it prints. `checks: … lint=fail` / `broken_links=N>0` / `warning: lint regresses` → fix it now:
  stage the missing linked page, or `replace_text` the bad link out, then close again. Hand over a changeset whose
  checks pass.

## Exit codes of `lw tool`

| Exit | Meaning | Do |
|---|---|---|
| 0 | done; result on stdout | continue |
| 1 | the tool refused; stdout says what was wrong and how to fix it | fix the arguments, call again (same call twice in a row = stop and rethink) |
| 2 | usage: tool not offered, bad JSON, or no changeset from `--prepare` | fix the command; `no open changeset` → go back to step 1 |

Only ingest tools are offered (no `web.search`, no `stage.open`). Wire names work too (`raw_get` = `raw.get`).

## Step 5 — report to the user, then stop

Tell the user, briefly:
- the changeset id, and what you staged: each new page and each patch, one line each, with why;
- anything you could not do (an unreadable chunk, a source you skipped, a check you could not make pass);
- how to review: `lw tui` → Review → `y`/`n` per hunk → `C` to commit, or `lw diff` then `lw commit -m "…"`;
  the full trail of your calls: `lw session show --plain`; to throw it away: `lw doctor --discard-changeset`.

Do not commit. Do not start another ingest unless asked.
