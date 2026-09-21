# llmwiki — a TUI + toolchain for LLM-compiled knowledge vaults

**Status:** shipped: tagged `v1.0.0` (`b4f1154`), 2026-09-14
**Version:** v1 — the build described here (written, and labelled throughout, as "v0.1")
**Date:** 2026-08-29
**Binary:** `lw`

> **Published 2026-09-14** as `docs/design.md` (formerly the root `PLAN.md`). Version labels in the text predate the
> rename: read "v0.1" as v1, "v1.0" as v2 and "v1.1" as v2.1.
>
> The v2 and v2.1 roadmaps are kept outside this repository.
> Anything deliberately *not* in v0.1 is recorded in one of those two, never dropped silently.

---

## 1. Thesis

Karpathy's LLM-wiki pattern replaces RAG with a **compiled** knowledge base: an agent
reads immutable sources and writes an interlinked markdown wiki, then keeps it healthy
with lint passes. Every existing implementation is either a prompt pack
(`nvk/llm-wiki`) or a desktop GUI (`nashsu/llm_wiki`). Both hand the agent a filesystem
and hope the prompt holds.

This project takes the opposite position on the two things that actually break:

**(1) The agent gets no filesystem.** It cannot `Write`, `Edit`, `cp`, `mv` or `rm`
inside the vault. Its only mutation path is a set of graph-aware, validating,
transactional tools that *propose* changes into a buffer. `mv` cannot know that moving a
page orphans twelve backlinks; `stage.rename_page` knows, fixes them, and shows you all
twelve edits before anything touches disk.

**(2) Nothing lands without review.** Ingestion produces a **changeset** — a
git-commit-shaped buffer of proposed operations, each carrying the agent's rationale and
provenance. You review it hunk by hunk in a TUI, accept some, drop others, and commit.
History is written **before** approval, so the log answers "what did the agent try to do,
and what did I reject?" — not just "what got in."

Everything deterministic (parsing, indexing, searching, linting, diffing, applying,
reverting) is Go. Only judgement — what deserves a page, how to phrase it, which sources
agree — goes to the model.

---

## 2. Prior art surveyed

| Project | Lang | What it is | What we take | What we reject |
|---|---|---|---|---|
| [karpathy/llm-wiki gist](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f) | — | The origin pattern (Apr 2026): raw / wiki / schema, ingest–query–lint | The whole conceptual frame | It's an idea file, not software |
| [Hermes `research-llm-wiki` skill](https://hermes-agent.nousresearch.com/docs/user-guide/skills/bundled/research/research-llm-wiki) | md | The most rigorous written spec of the pattern | **Our data model verbatim**: 3 layers, frontmatter fields, 11-point lint suite, orientation ritual, page thresholds | Nothing — it's a spec, we implement it |
| [nvk/llm-wiki](https://github.com/nvk/llm-wiki) (1.1k★, MIT) | Python + md | Claude Code plugin, 28 slash commands, hub-and-spoke multi-topic | The **command taxonomy** (`ingest`, `compile`, `query`, `lint`, `audit`, `retract`) as our CLI verb vocabulary | Hub-and-spoke (→ v1.0); prompt-only enforcement |
| [nashsu/llm_wiki](https://github.com/nashsu/llm_wiki) (17k★) | TS + Tauri | Desktop GUI; two-phase ingest; sigma.js graph; LanceDB | Two-phase ingest (analyse → generate), **SHA256 incremental cache**, crash-recoverable ingest queue | GUI/Tauri; vector DB as the primary retrieval path |
| [yorukot/superfile](https://github.com/yorukot/superfile) (23k★, MIT) | Go, Bubble Tea v2 | Modern TUI file manager, 207 Go files | **Vendored packages** (`pkg/file_preview`, `pkg/string_function`, `pkg/utils`) + its panel-layout, theme and hotkey-config patterns | Forking it — 120 of its files are file-manager UI we'd gut |
| [charmbracelet/crush](https://github.com/charmbracelet/crush) (28k★) | Go | Agentic coding TUI: `ui/diffview`, agent loop, streaming tool calls | Its **diff-view and streaming-tool-call UX** as reference; proof the Go agent loop is a solved shape | Forking — FSL-1.1-MIT restricts competing use |
| [erikjuhani/basalt](https://github.com/erikjuhani/basalt) (1.3k★) | Rust/ratatui | Obsidian vault TUI | Proof terminal markdown reading is pleasant; vim navigation model | Rust; read-only, no agent, no staging |
| [letta-ai/letta-code](https://github.com/letta-ai/letta-code) (3.1k★) | TS/Bun | Stateful agent harness | Its **memory-block idea**, reimplemented as a reviewable vault file | The runtime itself — see D2 |

**Gap this project fills:** nobody has built the *reviewable* version. Every tool above
either trusts the agent with a filesystem or puts a human in a GUI. Nobody has a staging
buffer with hunk-level review and a pre-approval audit trail.

---

## 3. Decisions (settled)

| # | Decision | Rationale |
|---|---|---|
| D1 | **Fresh Go module**; vendor superfile packages under `third_party/` with MIT NOTICE | Hard-forking a 90 MB, 207-file file manager to build a wiki browser means deleting more than we keep |
| D2 | **Own agent loop in Go, behind an `Agent` interface.** letta-code dropped for v0.1 | See §11.1. Short version: we render chat in our own TUI, which discards letta's biggest asset while still paying for Bun + a Letta backend + an embedding provider. The loop is ~950 LOC — cheaper than the integration it replaces |
| D3 | **Own CAS + append-only journal** (`.llmwiki/`), git *semantics*, no git | Git's model is file-level; we need "this commit created 3 pages and 7 backlinks", pre-approval records, and hunk provenance |
| D4 | **Karpathy/Hermes 3-layer single-vault schema** | Fully specified, Obsidian-compatible, lint suite enumerable. Multi-vault → v1.0 |
| D5 | **Go only for v0.1** | Every non-Go component got deferred by D2 (TypeScript) and by the PDF decision (Python → v1.0). One binary, no runtimes. See §5 |
| D6 | **Plain word search**, no SQLite, no FTS5 | An in-memory inverted index is instant at vault scale and removes a cgo-free-but-still-heavy dependency. FTS5 → the v2 roadmap |
| D7 | **One open changeset at a time**, single-threaded ingest | Matches git's index; keeps review comprehensible. Parallel ingest → the v2 roadmap |
| D8 | **The vault is the agent's memory.** No second store | Standing editorial preferences live in a reviewable `curator-memory.md` the agent edits through `stage.*` like any other page. Its memory is diffable, auditable and revertable |
| D9 | **Session per changeset** | Opening a changeset opens a session; commit or reject closes it and archives the transcript beside the changeset. Answers "why did it propose this?" months later — the archive is re-read with `lw session show` |
| D10 | **Embedded chat, rendered in the TUI** | D2 makes this the cheap path: we own the stream, so there is no subprocess to supervise or parse. The tmux fallback is no longer needed |

---

## 4. Architecture

```
                    ┌──────────────────────────────────────────────┐
                    │  lw   (Go, single static binary)             │
   terminal ────────┤                                              │
                    │  TUI (Bubble Tea v2)                         │
                    │    browse · review · ask · lint · log        │
                    │  ──────────────────────────────────────────  │
                    │  agent loop            vault engine          │
                    │    session/ndjson        frontmatter         │
                    │    context builder       wikilinks           │
                    │    tool dispatch  ─────► word index          │
                    │                          lint suite          │
                    │  ──────────────────────────────────────────  │
                    │  staging engine        MCP server   CLI      │
                    │    CAS (sha256)          (stdio)             │
                    │    journal.ndjson                            │
                    │    apply / revert                            │
                    └────────┬─────────────────────────┬───────────┘
                             │ HTTPS                   │ stdio
                             │ /chat/completions       │
                  ┌──────────▼──────────┐    ┌─────────▼──────────┐
                  │ OpenAI-compatible   │    │ external MCP       │
                  │ provider            │    │ clients (optional) │
                  │  base_url + model   │    │  Claude Code,      │
                  │  + api_key          │    │  Codex, letta      │
                  └─────────────────────┘    └────────────────────┘

                              filesystem
              ┌────────────────────────────────────────────┐
              │ vault/  raw/ (immutable)   wiki/           │
              │         SCHEMA.md  index.md  log.md        │
              │         curator-memory.md                  │
              │         .llmwiki/  ← engine + sessions     │
              └────────────────────────────────────────────┘
```

One process. One runtime. The only network call is to your LLM endpoint.

### The trust boundary

This is the load-bearing part of the design.

1. The agent loop's tool registry contains **no** filesystem verbs. There is no `Write`,
   no `Edit`, no `Bash`. Nothing to deny, because nothing is offered — a stronger
   guarantee than a permission profile, since it cannot be misconfigured.
2. The agent's only mutation verbs are `stage.*`. They do not touch the working tree.
   They append validated operations to the open changeset in `.llmwiki/`.
3. Only the review screen (or `lw commit`) applies a changeset. Each file update
   uses temp-file + rename; the journal and recovery path provide crash
   consistency across the multi-file operation, since POSIX cannot make all
   vault-file renames one atomic transaction.
4. Every proposal is journalled when it is *made*, then tagged `accepted`, `rejected` or
   `dropped` at review time. Rejections are permanent history.
5. `raw/` is write-once. `stage.ingest_source` is the only writer, and only for paths
   that do not yet exist.

Practical consequence: an agent that goes off the rails wastes your review time. It
cannot corrupt the vault, and you can read back exactly what it wanted to do.

---

## 5. Why Go only, for now

The original intent was a multi-language build. The decisions above collapsed it to one
language for v0.1, and that is worth stating plainly rather than letting it happen
quietly:

- **TypeScript** existed to host letta-code. D2 removed it.
- **Python** existed to run Docling for PDF extraction. PDFs move to v1.0, so Python
  arrives with them.

What remains is the language the deterministic core always wanted to be in. Bubble Tea v2
(stable Feb 2026) is the strongest TUI stack anywhere; superfile and crush both ship on
it. A single static binary matters for a tool used over SSH. Go has the official
[MCP SDK](https://github.com/modelcontextprotocol/go-sdk), `goldmark` for markdown, and
`glamour` for rendering.

**Language roadmap:** v0.1 Go · v1.0 adds Python (Docling, PDF) · v1.1 adds an OCR
backend. See the roadmap files.

---

## 6. Vault data model

```
vault/
├── SCHEMA.md            domain definition, tag taxonomy (10–20 tags), conventions
├── index.md             sectioned catalog, one line per page
├── log.md               append-only human-readable action log (rotates at 500)
├── curator-memory.md    the agent's standing editorial preferences  ← D8
├── raw/                 IMMUTABLE. Write-once, at ingest.
│   ├── articles/  papers/  transcripts/  assets/
├── wiki/
│   ├── entities/  concepts/  comparisons/  queries/
└── .llmwiki/            engine state
```

**Wiki page frontmatter** (validated by `lw`, not by prompt):

```yaml
---
title: Speculative Decoding
created: 2026-08-29
updated: 2026-08-29
type: concept            # entity | concept | comparison | query | summary
tags: [inference, decoding]   # MUST exist in SCHEMA.md taxonomy
sources: [raw/papers/leviathan-2023.md]
confidence: high         # high | medium | low
contested: false
---
```

**Raw source frontmatter:** `source_url`, `ingested`, `sha256` of body — the hash drives
drift detection and incremental re-ingest.

**`curator-memory.md`** — the reviewable replacement for letta's memory blocks. Plain
markdown, loaded into every session's system context, and edited only through
`stage.patch_page`. A preference the agent learns shows up in your next review as a diff:

```markdown
## Page thresholds
- Do not create pages for individual benchmark numbers. (2026-08-14, after I rejected 4 such pages.)

## Naming
- Prefer the hyphenated vendor form: `gpt-4`, not `gpt4`. (2026-08-29)
```

**Conventions enforced mechanically:** lowercase-hyphen filenames; ≥2 outbound
`[[wikilinks]]` per page; provenance markers `^[raw/papers/x.md]` on synthesized claims;
pages >200 lines flagged as split candidates; tags outside the taxonomy rejected at
`stage.*` time, not discovered later at lint time.

**Obsidian compatible** by construction — it is a normal vault.

---

## 7. The staging engine

### Layout

```
.llmwiki/
├── config.toml                    vault-local settings (never secrets)
├── objects/ab/cdef0123…           zlib blobs, content-addressed (sha256)
├── changesets/
│   ├── open/cs-0193f2a/
│   │   ├── changeset.json         THE BUFFER
│   │   └── session.ndjson         the conversation that produced it  ← D9
│   └── committed/cs-0193f2a/
├── journal.ndjson                 append-only; every event, pre- and post-approval
├── snapshots/000042.tree          path → sha manifest after each commit
├── index.gob                      word index cache (rebuildable)
└── lock                           single-writer guard
```

### Changeset

```jsonc
{
  "id": "cs-0193f2a",
  "intent": "Ingest 3 papers on speculative decoding",
  "author": { "kind": "agent", "model": "deepseek-v4-flash", "session": "se-8821" },
  "opened_at": "2026-08-29T14:02:11Z",
  "ops": [
    { "op": "ingest_source", "path": "raw/papers/leviathan-2023.md",
      "sha256": "…", "extractor": "go/html" },
    { "op": "create_page", "path": "wiki/concepts/speculative-decoding.md",
      "after": "sha256:…",
      "rationale": "Central to 3 of 3 ingested sources; meets page threshold.",
      "provenance": ["raw/papers/leviathan-2023.md", "raw/papers/chen-2023.md"] },
    { "op": "patch_page", "path": "wiki/concepts/kv-cache.md",
      "section": "## Related", "before": "sha256:…", "after": "sha256:…",
      "hunks": [ { "id": "h1", "+": ["- [[speculative-decoding]] — …"] } ] },
    { "op": "rename_page", "from": "wiki/entities/gpt4.md",
      "to": "wiki/entities/gpt-4.md",
      "cascade": [ /* 12 inbound wikilink rewrites, each its own reviewable hunk */ ] }
  ],
  "checks": { "schema": "pass", "lint": "pass", "orphans": 0, "broken_links": 0 }
}
```

### Operations (Go, fully unit-testable, no LLM involved)

| Op | Semantics |
|---|---|
| `stage` | validate → append op → recompute checks |
| `diff` | three-way render: working tree · staged · last snapshot |
| `hunk split/drop` | reviewer drops individual hunks; the op is rewritten, checks rerun |
| `commit` | journaled multi-file apply (temp+rename per file), snapshot, reindex, append to `log.md` |
| `revert <id>` | compute inverse ops from snapshots, open them as a *new* changeset for review |
| `log` | journal query: by author, outcome, page, date |

**Conflict handling:** you edit a page in Obsidian while a changeset is open. `before`
hash mismatch → the review screen marks the op `stale`, shows both, offers rebase or drop.

**Durable vs cache:** `objects/` + `journal.ndjson` are the durable state. `snapshots/`
and `index.gob` regenerate from them.

---

## 8. Tool surface for the LLM

The same Go functions are exposed twice: to the in-process agent loop, and over MCP
(`lw mcp`) so external clients can drive the vault.

**Read — no side effects**

| Tool | Why it exists |
|---|---|
| `vault.orient()` | SCHEMA.md + index.md + `curator-memory.md` + last 30 log entries in one call. Orientation is a mandatory ritual; one tool means it can't be half-done |
| `wiki.search(q, {type, tags, limit})` | Word search over pages with structured filters (§11.4) |
| `wiki.get(page, {section})` | Whole page or one section — keeps context small |
| `wiki.neighbors(page, depth)` / `wiki.backlinks(page)` | Graph slice. This is what stops duplicate pages: before creating `speculative-decoding`, the agent sees `assisted-generation` exists |
| `raw.get(source_id, {chunk})` | Read immutable sources. No write counterpart |
| `wiki.lint({checks})` | All 11 Hermes checks computed in Go. **The model never computes lint results — it only fixes what the engine reports** |

**Propose — staged, never applied**

| Tool | The specialised behaviour a shell can't do |
|---|---|
| `stage.open(intent)` | Opens the buffer and the session |
| `stage.create_page({…})` | Validates frontmatter, rejects out-of-taxonomy tags, enforces ≥2 outbound wikilinks *at proposal time* |
| `stage.patch_page({path, section, op})` | **Section-level**, not line-level: `replace_section`, `append_section`, `insert_after`, `insert_before`, `remove_section`. Survives reformatting; diffs a human can read |
| `stage.rename_page(old, new)` | Rewrites every inbound wikilink atomically, each rewrite its own hunk. The flagship case: `mv` silently breaks the graph |
| `stage.merge_pages([a,b] → c)` / `stage.split_page(p, sections)` | Redirects and backlink rewrites as one reviewable unit |
| `stage.add_link(from, to, {context})` | Bidirectional; refuses to create broken links |
| `stage.ingest_source(uri\|path)` | Extracts, writes to `raw/`, computes sha256, dedupes by hash |
| `stage.retract(page, reason)` | Tombstone + reason, never `rm` |
| `stage.close()` | Returns a human-readable proposal summary |

**Deliberately absent:** any filesystem verb. Not denied — *not offered*.

---

## 9. TUI design

Panel model borrowed from superfile; content entirely ours.

```
┌ ml-systems ─────────────────────────── 412 pages · 89 raw · ⚠ 3 lint ──┐
│ SIDEBAR      │ BROWSER              │ PREVIEW / DIFF                   │
│  ▾ raw       │  concepts/           │                                  │
│    articles  │   ▸ speculative-dec… │   rendered markdown (glamour)    │
│    papers  ● │   ▸ kv-cache         │   or unified diff w/ hunk marks  │
│  ▾ wiki      │   ▸ flash-attention  │                                  │
│    entities  │                      │                                  │
│    concepts● │                      │                                  │
│ ── STAGE ──  │                      │                                  │
│  cs-0193f2a  │                      │                                  │
│   +3 pages   │                      │                                  │
│   ~7 edits   │                      │                                  │
└ [tab] pane · [a]sk · [s]tage · [c]ommit · [l]int · [L]og · [?] ────────┘
```

**Screens**

1. **Browse** — vault tree, glamour preview, backlinks strip, `/` fuzzy find.
2. **Review** ← *the reason this project exists.* Changeset left, per-op diff right, each
   op showing rationale and provenance. `y`/`n` accept or drop a hunk, `s` split,
   `A` accept all, `X` reject the changeset, `C` commit. Stale ops flagged amber.
3. **Ask** — native chat pane (D10). Token deltas stream in; tool calls render as
   collapsible lines; any `stage.*` call badges the STAGE panel live. `Ctrl-R` → review.
4. **Lint** — the 14 checks as a live checklist; `f` asks the agent to fix a failure,
   which produces a changeset — so even repairs go through review.
5. **Log** — journal viewer. Filter `accepted | rejected | agent | human`. `r` reverts a
   commit into a new reviewable changeset.

Graph view is v1.0 (the v2 roadmap). Vim-first keymap, superfile-style `hotkeys.toml` +
`theme.toml` under XDG config.

---

## 10. CLI surface

The TUI is one front-end; every capability is scriptable.

```
lw init [--schema <domain>]        scaffold vault, generate SCHEMA.md interactively
lw config                          set/show provider: base_url, model, api_key ref
lw ingest <url|path>...            extract → agent compiles → leaves a changeset
lw status                          open changeset summary
lw diff [--op <id>]                unified diff of the buffer
lw commit -m "…"                   apply (refuses if lint regresses, --force overrides)
lw log [--rejected] [--agent]      journal query
lw revert <commit-id>              stage the inverse
lw query "…"                       one-shot answer with citations
lw lint [--fix]                    --fix asks the agent; result still stages
lw mcp                             stdio MCP server for external clients
lw doctor                          verify index, hashes, provider reachability
```

`lw ingest … && lw diff && lw commit -m …` makes the whole thing CI-able — a nightly job
that ingests a feed and leaves a changeset for morning review.

---

## 11. The agent

### 11.1 Why we build the loop (D2 in full)

letta-code was the v0 choice. Three of your subsequent decisions removed its value, and
research turned up costs that weren't visible before:

| | |
|---|---|
| **Embedded chat** | Letta's biggest asset is its TUI. Rendering in `lw` discards it, then pays for it anyway in stream parsing and process supervision |
| **Self-hosted** | Letta's docs now state the Docker image is "no longer an actively maintained or supported product surface" |
| **Word search** | Letta's archival memory requires an embedding model. DeepSeek serves none — we'd add a second provider immediately after designing embeddings out |
| **Custom `base_url`** | A run of fixes through Jul 2026, provider setup interactive (`/connect`) rather than declarative, and an **open** regression: "tool calls stored but never dispatched after the first 1–2 rounds" (v0.27.10+) — directly on our hot path |
| **Cost comparison** | The loop is ~950 LOC / ~1.5 weeks. Integrating letta was budgeted at 2 weeks. Building is cheaper than integrating, and removes a runtime |

**The hedge:** the agent sits behind a Go interface. If letta stabilises, or you want its
subagents for multi-angle research, it becomes a backend — not a rewrite.

```go
type Agent interface {
    Send(ctx context.Context, sessionID, msg string, out chan<- Event) error
    Sessions() SessionStore
}
// Event = TextDelta | ToolCall | ToolResult | Done | Error
```

**And letta isn't locked out.** `lw mcp` ships regardless, so letta, Claude Code or Codex
can drive the vault as external clients. Dropping it as our embedded runtime does not
drop it as a driver.

### 11.2 Provider configuration

Provider settings live in `~/.config/lw/config.toml`. Secrets are referenced, never
stored in the vault.

```toml
[llm]
base_url    = "https://api.deepseek.com/v1"
model       = "deepseek-v4-flash"
api_key     = "env:DEEPSEEK_API_KEY"      # env: | keyring: | literal (discouraged)
temperature = 0.2
max_tokens  = 32768

[llm.limits]
max_tool_rounds = 24       # hard stop; a runaway agent costs review time, not money
context_tokens  = 96000
```

**Requirement on the endpoint:** OpenAI-compatible `/chat/completions` **with tool
calling**. DeepSeek, together.ai, Groq, vLLM, llama.cpp and Ollama all qualify.
`lw doctor` probes the endpoint and reports whether tool calling actually works, rather
than failing mysteriously mid-ingest.

### 11.3 Sessions and context (D9)

A session is bound to a changeset: `stage.open` starts one, commit or reject ends it and
archives `session.ndjson` beside the changeset. `lw ingest` and `lw query` use ephemeral
sessions. One open changeset means one live session (D7). The archives stay out of git
through the vault `.gitignore` that `lw init` writes, and `lw session show` re-reads one
in full.

Context assembled per turn:

1. **System prompt** — the curator role and operating procedure. Static per
   session; since 012 its two web-lookup paragraphs are included only when the
   vault's registry actually offers `web.search`.
2. **`curator-memory.md`** — verbatim. Small by design (D8).
3. **Orientation digest** — from `vault.orient()`, injected once per session, refreshed
   if `index.md` changes.
4. **Session history** — compacted when over budget.
5. **User message.**

**Compaction rule that matters:** tool calls that produced staged ops are **never**
compacted away. They are the audit trail behind the changeset. Only prose turns are
summarized. Tool *results* are truncated on the way in — `wiki.search` returns titles and
snippets, not page bodies.

### 11.4 Word search (D6)

No SQLite, no FTS5. An in-memory inverted index, built at startup and updated
incrementally on commit:

- **Tokenize** — lowercase, split on non-alphanumeric, strip frontmatter, keep wikilink
  targets as tokens so `[[kv-cache]]` is findable as `kv` + `cache` + `kv-cache`.
- **Rank** — BM25-lite, title and `title:` frontmatter weighted above body.
- **Filter** — `type`, `tags`, date ranges applied structurally, not lexically.
- **Persist** — `.llmwiki/index.gob`, rebuilt whenever it is stale or missing.

A 400-page vault is roughly 2 MB of text; the index builds in milliseconds and answers in
microseconds. This is comfortably sufficient to several thousand pages. FTS5 and semantic
search are in the v2 roadmap for when it isn't.

---

## 12. Repository layout

```
llmwiki/
├── cmd/lw/                     entrypoint + CLI
├── internal/
│   ├── vault/                  frontmatter, wikilinks, canonical serializer
│   ├── index/                  inverted word index, incremental rebuild
│   ├── lint/                   the 14 checks, each independently testable
│   ├── stage/                  CAS, journal, changeset, apply/revert
│   ├── tools/                  the tool surface (§8) — one definition, two consumers
│   ├── mcp/                    MCP server binding over internal/tools
│   ├── llm/                    OpenAI-compatible client, SSE streaming
│   ├── agent/                  Agent interface, loop, sessions, context builder
│   └── ui/                     browse · review · ask · lint · log
├── third_party/superfile/      vendored MIT packages + NOTICE
├── spec/
│   ├── changeset.schema.json   the contract between agent and engine
│   ├── vault-schema.md
│   └── fixtures/               golden vaults for tests
└── docs/
```

**Key dependencies**

| Dep | Purpose | License |
|---|---|---|
| `charm.land/bubbletea/v2`, `bubbles/v2`, `lipgloss/v2` | TUI | MIT |
| `charmbracelet/glamour` | markdown rendering | MIT |
| `yuin/goldmark` + wikilink extension | parsing | MIT |
| `modelcontextprotocol/go-sdk` | MCP server | MIT |
| `alecthomas/chroma` | code highlighting | MIT |
| vendored superfile pkgs | preview, string utils | MIT (NOTICE required) |

All permissive. No SQLite, no Node, no Python, no database. **Six direct dependencies.**

---

## 13. Milestones

| # | Deliverable | Est. | Done when |
|---|---|---|---|
| **M0** | Spec & scaffold | 1w | `changeset.schema.json` frozen; golden fixture vaults; Go module + CI |
| **M1** | Vault engine | 2w | Parse/serialize round-trips byte-stable on fixtures; word index; all 14 lint checks; `lw lint`, `lw status` |
| **M2** | Staging engine | 2w | CAS + journal + apply/revert; `lw diff/commit/log/revert` work with **no UI and no LLM**; crash-safety and conflict tests pass |
| **M3** | Tool layer + MCP | 1w | Tool surface over `internal/tools`; conformance suite driven by a scripted fake agent; MCP server verified against a real client |
| **M4** | TUI | 3w | Browse + preview, then the review screen with hunk selection. Ship review before anything else |
| **M5** | Agent loop + Ask | 2w | Streaming client, tool dispatch, sessions, compaction, native chat pane; `lw ingest <url>` → changeset → review → commit, end to end |
| **M6** | Polish & release | 2w | Themes, hotkey config, `lw doctor`, goreleaser + brew + nix |

**~13 weeks solo.** M1–M3 must be right, and are also the parts with no UI and no model —
therefore the parts that can be tested exhaustively. Build them first; the rest is
presentation.

**Build order note:** M5 depends on M3 only, not M4. If the TUI slips, `lw ingest` and
`lw diff` still deliver the whole thesis on the command line.

---

## 14. Risks

| Risk | Mitigation |
|---|---|
| We now own an agent loop | It's the smallest part of the system (~950 LOC) and the only part with no correctness invariants — worst case it produces a bad proposal, which the review gate catches. Kept behind an interface (§11.1) |
| Provider variance in tool-calling | `lw doctor` probes tool calling explicitly at config time. Loop tolerates malformed tool JSON with a retry and a clear error rather than a crash |
| Bubble Tea v2 is young (stable Feb 2026) | superfile and crush both ship on it in production |
| Section-level patching of LLM-authored markdown is fiddly | Canonical serializer + round-trip property tests in M1, before anything depends on it |
| Review fatigue — big changesets get rubber-stamped | Cap ops per changeset; sort by risk (new pages and renames first, link additions last); `A` accept-all requires lint-clean |
| Word search proves insufficient | Contained: `wiki.search` is one interface. FTS5 and embeddings are drop-ins (the v2 roadmap) |
| Scope creep into "another Obsidian" | Non-goal, stated: we don't compete on editing. Vault stays Obsidian-compatible so you can use the real thing |

**No open questions remain for v0.1.** Everything deferred is written down in
the v2 roadmap or the v2.1 roadmap.

---

## 15. Sources

- [karpathy/llm-wiki gist](https://gist.github.com/karpathy/442a6bf555914893e9891c11519de94f) · [VentureBeat writeup](https://venturebeat.com/data/karpathy-shares-llm-knowledge-base-architecture-that-bypasses-rag-with-an)
- [Hermes `research-llm-wiki` skill](https://hermes-agent.nousresearch.com/docs/user-guide/skills/bundled/research/research-llm-wiki)
- [nvk/llm-wiki](https://github.com/nvk/llm-wiki) · [nashsu/llm_wiki](https://github.com/nashsu/llm_wiki)
- [yorukot/superfile](https://github.com/yorukot/superfile) · [charmbracelet/crush](https://github.com/charmbracelet/crush) · [erikjuhani/basalt](https://github.com/erikjuhani/basalt)
- [letta-ai/letta-code](https://github.com/letta-ai/letta-code) · [Letta self-hosting](https://docs.letta.com/self-hosting) · [Letta OpenAI-proxy provider](https://docs.letta.com/guides/server/providers/openai-proxy/) · [letta-code issue #2714](https://github.com/letta-ai/letta-code/issues/2714)
- [Charm v2 release](https://charm.land/blog/v2/) · [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk) · [Docling](https://github.com/docling-project/docling)

---

## 16. Addendum — reliability invariants (008, 2026-09-17)

Four invariants were added after a live run measured the failure they close: at
`max_tokens = 2048` a thinking-mode round spent its whole budget on
`reasoning_content`, ended `finish_reason: "length"`, produced zero content —
and lw exited 0 with a changeset that staged the raw source and proposed no
pages.

- **A truncated round is an error, not a clean stop.** When a round's finish
  reason is anything but `stop` or `tool_calls` (in practice `length`) and the
  round completed no tool call, the agent loop ends the turn with
  `ErrTruncated` — exactly one `ErrorEv`, no `DoneEv` — and the round's
  assistant transcript record carries the abnormal `finish` reason. `lw ingest`
  rejects the changeset and its error names the fix:
  `lw config set llm.max_tokens 32768`. The default rose from 8192 to 32768
  because the measured run spent 5,247 reasoning tokens in one round.
- **Commits refuse an empty changeset.** A changeset whose every op was
  dropped — or that never had one — is refused with `ErrNothingToCommit`
  before anything is journalled, in git's "nothing to commit" spirit.
- **A raw-only changeset commits on a second, deliberate keypress.** An ingest
  that staged raw source(s) but proposed no pages first warns in Review —
  `0 pages proposed — this commits raw source(s) only: …` — and commits only
  when the reviewer gives a second `C` — press C again, with no other key in
  between. `lw commit` prints its own warning to stderr — `warning: 0 pages
  proposed — committing raw source(s) only: …` — and commits without asking.
- **The TUI notices commits made elsewhere.** Every two seconds it checks the
  journal's size and mtime stamp; if another process committed, the engine
  reloads the vault, index and open changeset, and the UI refreshes. Appends
  the engine made itself never count as a change.
