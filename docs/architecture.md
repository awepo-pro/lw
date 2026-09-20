# Architecture

lw is one Go binary. It compiles a directory of **immutable sources** into an
interlinked markdown wiki, and it is built around one rule: the model never
touches your files, and you never accept a change you have not read.

[`/docs/design.md`](../docs/design.md) is the design of record; this page is the map of what
actually shipped in v1.0.0.

---

## The pipeline

```
 raw/ (immutable)                 .llmwiki/                        wiki/
─────────────────    ─────────────────────────────────────    ──────────────
 url or local file   1  extract → deterministic markdown
                     2  agent loop, tools only  ────────────►  proposed pages,
                        18 or 19 tools, 0 filesystem verbs     patches, renames
                     3  every proposal becomes an op  ─────►  OPEN CHANGESET
                                                              + session.ndjson
 you                 4  review hunk by hunk (TUI or lw diff)
                     5  accept / drop / reject
                     6  lw commit ─────────────────────────►  working tree,
                        journaled, snapshotted                 log.md, index
```

1. **Extract.** `lw ingest` turns a URL or a local file into deterministic
   markdown (`internal/extract`): HTML via `golang.org/x/net/html`, markdown
   passed through. Extraction happens *before* the staging engine is opened, so
   a bad source fails the whole command with nothing created.
2. **Propose.** The agent loop (`internal/agent`) runs against a provider and
   may only call tools (`internal/tools`). Reads (`wiki.search`, `wiki.get`,
   `raw.get`, …) answer questions; `stage.*` calls append validated operations
   to a buffer. See [tools.md](tools.md).
3. **Stage.** Each tool call becomes an op in the **open changeset**
   (`.llmwiki/changesets/open/<id>/changeset.json`), each op carrying the
   agent's rationale and its provenance back to `raw/`. Nothing under `wiki/`
   has moved yet.
4. **Review.** `lw diff` prints the projected unified diff; the TUI's review
   screen does the same per hunk, with rationale and provenance beside it.
   Hunks can be dropped individually; the op is rewritten and the checks are
   recomputed.
5. **Decide.** Commit applies the buffer. Rejecting moves the whole directory
   to `changesets/rejected/` — rejections are permanent history, not a
   deletion. See [changesets.md](changesets.md).
6. **Apply.** `internal/stage`'s `Commit` validates, re-hashes the tree to
   catch edits you made meanwhile, writes each file, snapshots, reindexes and
   appends to `log.md` — every step journalled. `lw revert <commit-id>` computes
   the inverse ops and opens them as a *new* changeset, so a rollback is
   reviewed like anything else.

`lw ingest … && lw diff && lw commit -m "…"` is the whole loop, which is what
makes a nightly ingest-and-review job possible.

---

## The verb boundary

This is the part the rest of the design serves. **The agent's tool registry
contains no filesystem verb.** There is no `write`, no `edit`, no `delete`, no
`bash`, no `exec`. They are not denied by a permission profile — *not offered*,
which is stronger, because a profile can be misconfigured and a registry cannot.

Consequences, all enforced in code:

| Rule | Where |
|---|---|
| Tools read through the vault handle and write only through the engine | `internal/tools` handlers |
| `raw/` is write-once, and only `stage.ingest_source` writes it, only for a path that does not exist yet | `internal/tools`, `internal/stage` |
| Nothing mutates the working tree except `stage.Engine.Commit` | `internal/stage/apply.go` |
| Lint results are computed in Go; the model only reads them | `internal/lint` |
| Patches address `## Heading` sections, never line numbers | `internal/stage`, `internal/vault` |
| Renames, merges and splits compute their backlink cascade in the engine, not in the prompt | `internal/stage/derive.go` |

A model that goes off the rails costs you review time. It cannot corrupt the
vault, and everything it wanted to do is sitting in the changeset with its
rationale attached.

---

## Package map

| Package | Owns |
|---|---|
| `internal/vault` | Frontmatter parsing, canonical serialization (byte-stable round-trip), wikilinks, the schema, the graph |
| `internal/index` | In-memory inverted word index — no SQLite, no FTS5 |
| `internal/lint` | The 14 checks ([vault-schema.md](vault-schema.md#the-14-lint-checks)) |
| `internal/stage` | CAS (`objects/`), journal, changesets, lock, apply/revert, snapshots, recovery |
| `internal/tools` | The 18 vault tools plus the conditional web.search ([tools.md](tools.md)) — one definition, two consumers |
| `internal/mcp` | stdio transport for the same registry; name mapping only, no new tools |
| `internal/llm` | OpenAI-compatible streaming client |
| `internal/agent` | The tool-calling loop, sessions, context building |
| `internal/extract` | HTML/markdown sources into deterministic markdown |
| `internal/config` | `~/.config/lw/config.toml`; secrets are referenced, never stored |
| `internal/ui` | Bubble Tea v2 TUI: browse, review, ask, lint, log |
| `cmd/lw` | The CLI verbs; `lw` with no argument is the TUI |

---

## What is deliberately not in v1

Graph view, embeddings and vector search, SQLite/FTS5, PDF and OCR extraction,
multi-vault, parallel changesets, and a web server are all **deferred**, by
design, to the v2 and v2.1 roadmaps (kept outside this repository). Search is a plain word index.
Nothing in this build reads a vector store, and the tool registry never gains a
filesystem verb to make any of it easier.
