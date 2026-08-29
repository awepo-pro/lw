# llmwiki v1.0 — the next version

**Status:** backlog. Nothing here is built in v0.1.
**Reads with:** [`PLAN.md`](PLAN.md) (v0.1 — current build) · [`PLAN-v1.1.md`](PLAN-v1.1.md) (v1.1)

Every item records *why it was deferred* and *what in v0.1 it plugs into*, so picking one
up later doesn't mean rediscovering the design. Items are ordered by expected value.

---

## 1. Knowledge graph — visualization and traversal

**The headline feature of v1.0.** A compiled wiki *is* a graph; v0.1 can query it
(`wiki.neighbors`, `wiki.backlinks`) but never shows it. Seeing the graph is how you spot
a cluster that should be one page, an orphan island, or the concept everything points at.

**Deferred because** the graph is only interesting once there's a vault worth looking at,
and the review gate had to come first.

**Plugs into:** `internal/index` already builds the full link table on every reindex —
nodes and edges are in memory today. This is a rendering problem, not a data problem.

**Scope**

- **TUI graph screen** (`g`) — braille/half-block canvas, force-directed layout computed
  in Go. Pan, zoom, `Enter` to open a node, `/` to locate. Orphans highlighted, clusters
  tinted by tag, node size by inbound-link count.
- **Focus mode** — ego graph around the current page at depth 1–3, which is the view that
  actually gets used while writing.
- **Overlays** — colour by `confidence`, by staleness, by `contested: true`. The graph
  becomes a lint surface: low-confidence hubs are exactly where to spend attention.
- **Staged-diff overlay** — show what an open changeset would do *to the graph*: new
  nodes ghosted in, rewritten edges highlighted, orphans it would create flagged in red.
  This is the feature that makes graph work earn its place next to the review screen.
- **Export** — `lw graph --format dot|json|svg` for Graphviz, Gephi, or a web viewer.

**Design notes**
- Force-directed layout on 400+ nodes needs to be incremental and cancellable, or it
  blocks the UI. Compute in a goroutine, stream position updates as Bubble Tea messages.
- Terminal graphs get illegible past ~150 visible nodes. Default to focus mode; make the
  full-vault view an explicit action with automatic clustering above a threshold.
- `nashsu/llm_wiki` uses sigma.js + graphology + ForceAtlas2 for exactly this; worth
  reading their layout parameters even though we're not in a browser.

---

## 2. PDF ingestion

**Deferred because** it's the one feature that reintroduces a second language, and v0.1
was better served staying a single binary.

**Scope: text-based PDFs only.** Image-based/scanned PDFs and OCR are v1.1
(`PLAN-v1.1.md`).

**Plugs into:** `internal/extract` — v0.1 ships the interface and the pure-Go HTML/text/
markdown backends. This adds a backend, not a subsystem.

```
lw extract <file>
  .html .txt .md      → pure Go            always available
  .pdf .docx .epub    → Docling sidecar    if Python present
                        else: a clear error, never mangled text
```

- **Docling** (MIT, CPU-only, IBM Research) over Marker (GPL-3 + RAIL-M commercial
  restriction) and MinerU (heavier, GPU-leaning — keep as an opt-in backend for CJK and
  formula-dense papers).
- **Boundary:** JSON over stdout, subprocess, `uv`-managed. `lw` runs fine without it.
- **`lw doctor`** reports extractor availability and version.
- **Quality gate:** the extractor returns a confidence signal; below threshold, ingest
  refuses rather than poisoning the vault with garbled text. A wiki compiled from mangled
  extraction is worse than no wiki.

---

## 3. Search: FTS5, then semantic

**Deferred because** the whole premise of the compiled-wiki pattern is that a
well-organised vault needs less retrieval machinery than RAG does. Ship word search,
measure, then add only what's missing.

**Plugs into:** `wiki.search` is a single interface (§11.4 of `PLAN.md`). Both of these
are drop-in implementations behind it.

**Stage 1 — SQLite + FTS5.** When the in-memory index stops being comfortable (roughly
several thousand pages, or when startup rebuild becomes noticeable). Brings phrase
queries, prefix matching, proper stemming and ranked snippets. `modernc.org/sqlite`
keeps the build cgo-free.

**Stage 2 — semantic search, optional.** A small local embedding model (bge-small,
all-MiniLM, or whatever's served locally) behind an optional config block:

```toml
[embeddings]              # entirely optional; absent = word search only
base_url = "http://localhost:11434/v1"
model    = "bge-small-en-v1.5"
```

- **Hybrid, not replacement:** BM25 for recall, embeddings to rerank the top 50. Pure
  vector search is what the pattern exists to avoid.
- **Where it actually pays:** duplicate detection at `stage.create_page` time — "you're
  about to create `speculative-decoding`, but `assisted-generation` is 0.89 similar." A
  word index misses that; it's the single best use of embeddings here.
- Vectors live in `.llmwiki/`, rebuildable, never required.

---

## 4. Parallel ingest and multiple changesets

**Deferred because** single-threaded matches git's index and keeps review comprehensible.
Concurrency here is a real design problem, not a flag.

**Scope**

- Multiple open changesets, each with its own session (v0.1's D9 binding still holds).
- **Conflict detection between open changesets** — the hard part. Two ingests both
  proposing `wiki/concepts/kv-cache.md` must be detected at stage time, not at commit.
  Requires a path-level claim registry over the open set.
- Review screen gains a changeset switcher; STAGE panel lists all open buffers.
- `lw ingest --parallel <n>` for bulk backlog import.
- **Ordering guarantee:** commits still serialise. Only proposal is concurrent.

**Watch out:** this multiplies review load, which `PLAN.md` §14 already names as the
top product risk. Ship the graph diff overlay (item 1) first so large changesets are
reviewable at a glance.

---

## 5. Multi-vault hub

**Deferred because** the engine is already vault-agnostic; this is a picker plus a
registry, and it adds nothing until you have a second vault.

`nvk/llm-wiki`'s hub-and-spoke model:

```
~/wiki/
├── wikis.json          registry
├── topics/<name>/      each a complete v0.1 vault
└── .archive/
```

- Vault picker in the TUI (`V`), `lw --vault <name>` on the CLI.
- Cross-vault search, explicitly opt-in — vault isolation is a feature, not a limitation.
- Per-vault provider config (a cheap model for one domain, a strong one for another).

---

## 6. Multi-angle research

`nvk/llm-wiki` launches 5–10 parallel agents across angles (academic, technical, applied,
news, contrarian). v0.1's single loop can't, and adding subagents was one of letta's
genuine advantages.

- `lw research "<topic>"` — fan out N agent loops with distinct system prompts, gather
  into one changeset with per-angle provenance.
- Thesis mode: `lw thesis "<claim>"` — evidence for and against, with anti-confirmation
  bias framing, filed as a `comparisons/` page.
- **Alternatively:** this is where a letta backend behind the `Agent` interface would
  earn its keep, since subagent orchestration is exactly what it already does. Evaluate
  build-vs-adopt at the time.

---

## 7. Second agent backend

The `Agent` interface exists in v0.1 precisely so this stays cheap.

- **letta backend** — if its self-hosted story stabilises and the tool-dispatch
  regression closes. Buys subagents and memory blocks.
- **Anthropic-native backend** — direct Messages API with prompt caching, which would cut
  cost noticeably given our large static system prompt + orientation digest.
- Selected per vault: `[llm] backend = "openai-compatible" | "letta" | "anthropic"`.

---

## 8. Smaller items

| Item | Note |
|---|---|
| `lw export --git` | Materialise the journal into a real git repo — history in a format other tools read. Deliberately one-way |
| Web viewer | `lw serve` — read-only browsable vault with the graph, for sharing. Not an editor |
| Watch mode | `lw watch` — detect external edits (Obsidian) and reindex live; today you get a stale-op flag at review time |
| Attachments | Images and assets in `raw/assets/` with preview in the TUI (superfile's `file_preview` is already vendored) |
| Templates | Per-`type` page skeletons in `SCHEMA.md` so `stage.create_page` starts from house style |
| `lw audit` | Periodic deep review: sample N pages, re-verify claims against `raw/`, flag drift the lint suite can't see |
| Log rotation UI | Browse archived `log-YYYY.md` from the Log screen |
| Themes | Ship 3–4 curated themes beyond the default; superfile's theme format is already vendored |
