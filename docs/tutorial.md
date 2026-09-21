# Tutorial: from an empty directory to a reviewed wiki

This is a hands-on walkthrough of `lw`, second person, one command at a time.
Every piece of terminal output on this page is real: it was either captured
from a live run against a configured provider, or run by hand, offline,
against a throwaway vault while writing this page. Nothing here is invented,
and nothing here is a feature from a later release — if it is not in `lw
--help` today, it is not on this page.

If you already know the shape of the tool and just want the reference
material, see the [README](../README.md)'s command table, or the deeper docs
this page links to as it goes: [vault-schema.md](vault-schema.md),
[tools.md](tools.md), [changesets.md](changesets.md), [hotkeys.md](hotkeys.md).

## 1. What you will build

You start with sources — a saved chat transcript, an article, a research
note, a URL — and you end with a small, cross-linked markdown wiki
that cites every claim back to where it came from. `lw` is the compiler in
the middle: an agent reads what you ingest and *proposes* pages, patches and
renames as a single reviewable unit called a changeset. It never writes to
`wiki/` directly, because it has no filesystem tool to do that with — its
only mutation path is a set of validating "stage" calls that append to a
buffer.

Nothing lands until you say so. You read the projected diff — hunk by hunk,
with the agent's rationale and the source it drew from sitting right next to
each change — and you accept, drop, or reject. A rejection is not silence: it
is written to the journal the moment the agent proposed the change, so the
history answers "what did it try, and what did I turn down?", not just "what
got in". By the end of this tutorial you will have ingested one real source,
reviewed and committed the pages it produced, asked the vault a question, and
walked the same loop again through the TUI.

## 2. Install and check

From a checkout of this repository:

```bash
git clone https://github.com/awepo-pro/lw && cd lw
make build          # → ./lw
```

or, with Go 1.25.8 or newer on your `PATH`:

```bash
go install github.com/awepo-pro/lw/cmd/lw@latest
```

Either way, check it landed:

```
$ lw version
lw v2.1.0-2-g91f0c32
```

The version is the release tag the build was made from — a bare `vX.Y.Z`
exactly at a tag, tag-plus-commit between tags — so a bug report can name the
exact build. `lw --version` prints the same thing.

`lw` finds its vault by walking up from your current directory to the
nearest ancestor containing `SCHEMA.md` — so every verb below except `init`
expects you to be inside a vault, or to pass `-vault <dir>`.

## 3. Connect a provider

`lw` speaks the plain OpenAI chat-completions wire format to whatever
endpoint you point it at. It defaults to DeepSeek:

```
$ lw config
config file: /tmp/lw-008-T-H/xdg/lw/config.toml (not present — showing lw's defaults)

llm.base_url               = https://api.deepseek.com/v1   (default)
llm.model                  = deepseek-v4-flash   (default)
llm.api_key                = env:DEEPSEEK_API_KEY (missing)   (default)
llm.temperature            = 0.2   (default)
llm.max_tokens             = 32768   (default)
llm.limits.max_tool_rounds = 24   (default)
llm.limits.context_tokens  = 96000   (default)
theme                      = (not set)   (default)

(file) = not lw's built-in value · (default) = lw's built-in value; change one with lw config set <key> <value>
```

(That path is your own `$XDG_CONFIG_HOME/lw/config.toml`, or `~/.config/lw/config.toml`
when that variable is unset — the capture above pointed `XDG_CONFIG_HOME` at a
scratch directory so it would not touch a real config. Nothing above is
vault-specific.)

The API key is never a value in the config file — it is a *reference*, and
`lw` resolves it at the moment it needs it:

```bash
export DEEPSEEK_API_KEY=sk-...
```

Anything OpenAI-compatible works, not just DeepSeek. If you point `lw` at a
different provider, set both keys together, since one without the other is
usually wrong:

```bash
lw config set llm.base_url https://api.deepseek.com/v1
lw config set llm.model    deepseek-v4-flash
```

```
$ lw config set llm.base_url https://api.deepseek.com/v1
llm.base_url set to https://api.deepseek.com/v1
saved /home/you/.config/lw/config.toml
warning: DEEPSEEK_API_KEY is not set in this shell; lw cannot resolve the key until it is exported
```

**The base URL must be an OpenAI-compatible `/v1` endpoint** — chat
completions with tool calling, not an Anthropic-shaped Messages API. A URL
like `https://api.deepseek.com/anthropic` looks plausible and is wrong: it
answers with a 404, because the request `lw` sends does not match what that
path expects. If a provider offers both an OpenAI-compatible path and a
native one, use the OpenAI-compatible one.

Once the key is exported, confirm it end-to-end:

```bash
lw config --probe    # resolves the key, calls the model, confirms tool calling works
```

Without a key exported, `--probe` fails fast and tells you why, with no
network call:

```
$ lw config --probe
probe failed: config: environment variable DEEPSEEK_API_KEY is not set
  fix: export the variable llm.api_key names, or re-point it: lw config set llm.api_key env:NAME
```

With the key exported and a reachable endpoint, the same underlying check —
`lw doctor`'s `config` and `provider` lines — looks like this (captured from a
live session):

```
✓ config   api_key env:DEEPSEEK_API_KEY (set) · model deepseek-v4-flash @ https://api.deepseek.com/v1
✓ provider deepseek-v4-flash reachable, tool calling ok (1568ms)
```

## 4. Create a vault

Start in an empty directory:

```
$ mkdir ml-wiki && cd ml-wiki
$ lw init --schema ml-systems
initialized vault in /home/you/ml-wiki
  domain           ml-systems
  tags             10 (10-20 recommended)
  files            SCHEMA.md, index.md, log.md, curator-memory.md, .gitignore
  directories      raw/{articles,papers,transcripts,assets}, wiki/{entities,concepts,comparisons,queries} (8 of 8 created)
  engine           .llmwiki/ (staging engine state)
next: lw config — point lw at a provider, then lw ingest <url> to add the first source
```

`--schema ml-systems` skips the interactive prompt and scaffolds a vault
around that domain name; without it, `init` asks for the domain, the tag
taxonomy, and page-type conventions instead. What it lays down:

- **`SCHEMA.md`** — the domain definition and the 10–20 tag taxonomy every
  page's `tags:` must draw from.
- **`index.md`** — a sectioned catalog, one line per page, kept in sync by
  the engine.
- **`log.md`** — an append-only human-readable log, rotating at 500 entries.
- **`curator-memory.md`** — the agent's own standing editorial preferences,
  a normal page you can read and edit like any other.
- **`.gitignore`** — one line, `.llmwiki/`, so the engine's state and the
  agent conversation transcripts it keeps never commit by accident, even
  from a vault that is a public git repository.
- **`raw/{articles,papers,transcripts,assets}/`** — where ingested sources
  land, write-once and immutable.
- **`wiki/{entities,concepts,comparisons,queries}/`** — where reviewed pages
  land, one directory per page type.
- **`.llmwiki/`** — engine state: the object store, the changeset buffer,
  the journal, the search index cache, and the session transcripts —
  git-ignored by the `.gitignore` above. Not part of the reviewable wiki —
  deleting it loses history, not your notes.

See [vault-schema.md](vault-schema.md) for the full layout, the frontmatter
fields, and the 15 lint checks.

A freshly scaffolded vault is empty but valid:

```
$ lw status
0 pages · 0 raw · 10 tags
lint: 0 errors, 0 warnings, 0 info
no open changeset

$ lw lint
clean
```

## 5. Ingest your first source

`lw ingest` takes one or more local paths or URLs. This is the step that
actually needs your provider: it extracts the source to deterministic
markdown, then runs the agent — reading the extracted text, `SCHEMA.md`,
`index.md` and recent `log.md` — to propose pages. Extraction happens
*before* any changeset is opened, so a bad path or a bad URL fails the whole
command with nothing created. A source whose body the vault already holds —
committed earlier, or repeated within the same command — is skipped before
the agent runs, with a `skipped <source>: …` line; when every source is
skipped, `lw ingest` prints `nothing to ingest: every source is already in
the vault` and exits 0 without opening a changeset or calling the provider.

```bash
lw ingest /home/you/Downloads/kv-cache-explained.md  # a local file
lw ingest https://example.com/some-post          # or a URL
```

A real run, ingesting a small saved article about the KV cache. It took about
three minutes. The agent's streamed summary comes first (its wording varies
run to run, so it is abridged here); the changeset summary at the end is
`lw`'s own, fixed output:

```
$ lw ingest ~/Downloads/kv-cache-explained.md
Fresh vault — this ingest will be the first content. Ingesting the source now.
…
Empty vault confirmed. The article supports one full concept page ([[kv-cache]]) plus the vocabulary
it presumes ([[autoregressive-decoding]]), and its "why it matters" techniques naturally form a
standing [[query]] page. …
Changeset **cs-92c7d850b461792a** is ready for review — 4 operations, all checks passing (schema ✓,
lint ✓, 0 orphans, 0 broken links).
…
1. **`raw/articles/kv-cache-explained.md`** (ingest) — the article, staged and read in full (1 chunk).
…
opened changeset cs-92c7d850b461792a: ingest ~/Downloads/kv-cache-explained.md (4 op(s))
  op1 ingest_source raw/articles/kv-cache-explained.md
  op2 create_page wiki/concepts/kv-cache.md
  op3 create_page wiki/concepts/autoregressive-decoding.md
  op4 create_page wiki/queries/reducing-kv-cache-memory.md
```

If the proposed changeset would leave the vault with more lint errors than
the last commit, the run ends with
`warning: lint regresses — <P> error(s) projected vs <B> in the last commit; lw commit will refuse this (review with lw diff)`
— still exit 0, with the changeset open, so you can `lw diff` it before
`lw commit` refuses it.

`op1` is always the immutable copy of your source, staged (not yet written)
under `raw/articles/`, `raw/papers/` or `raw/transcripts/` depending on kind.
It carries the original path or URL forward as `source_url`, and a `sha256`
of its body that later drives drift detection — visible in the projected
diff:

```
$ lw diff --op op1
--- /dev/null
+++ b/raw/articles/kv-cache-explained.md
@@ -0,0 +1,27 @@
+---
+source_url: ~/Downloads/kv-cache-explained.md
+ingested: 2026-09-15
+sha256: 4f5463061b616c978d23836edd0ceb613d153165b7d9f20c6f0535e76b50c56e
+---
…
```

Every `create_page` op after it is grounded in that same source: it is the
only thing on disk the agent has read, so it is the only thing a new page can
cite. Nothing in `wiki/` or `index.md` has actually changed yet — that all
lives in the open changeset until you review and commit it.

## 6. Review before anything lands

While a changeset is open, `lw status` reports **committed** counts, not
staged ones — so `0 pages · 0 raw` here does not mean nothing happened, it
means nothing has landed:

```
$ lw status
0 pages · 0 raw · 10 tags
lint: 0 errors, 0 warnings, 0 info
open changeset cs-92c7d850b461792a: ingest ~/Downloads/kv-cache-explained.md (4 op(s), 0 stale; schema=pass lint=pass orphans=0 broken_links=0)
```

The `open changeset` line is where the real state lives: how many ops, how
many are stale (proposed against a file that has since changed on disk), and
whether the buffer currently passes schema, lint, orphan and broken-link
checks.

`lw diff --stat` shows what would land, one line per file:

```
$ lw diff --stat
wiki/concepts/autoregressive-decoding.md | +30 -0
wiki/concepts/kv-cache.md | +38 -0
wiki/queries/reducing-kv-cache-memory.md | +34 -0
index.md | +5 -0
raw/articles/kv-cache-explained.md | +27 -0
5 file(s) changed, 134 insertion(s)(+), 0 deletion(s)(-)
```

`lw diff` (no flags) expands that into the full unified diff, one hunk per
changed section; `lw diff --op op3` narrows it to a single operation's files
— useful once a changeset has more ops than you want to read at once. Every
hunk in the TUI's Review screen carries the same two things a raw `diff`
does not: the agent's **rationale** for the change, and its **provenance**
back to `raw/` — see §9 below for what that looks like on screen.

Lint runs against the *projected* tree, not just the committed one, so you
can catch a problem before you commit it:

```
$ lw lint
clean
```

## 7. Commit — or throw it away

```
$ lw commit -m "first pages from kv-cache-explained"
committed 000001
```

A commit re-hashes the tree first — if a file an op was proposed against has
changed on disk since (someone edited it by hand, or another op touched it),
that op is stale and the commit is refused rather than silently overwriting
your edit. A commit is also refused if it would make lint worse than it
already is; `--force` overrides that, and the override itself is journalled,
so "we shipped a lint regression on purpose" is part of the permanent record,
not a quiet exception.

After the commit, `lw lint` and `lw status` count what actually landed:

```
$ lw lint
clean
$ lw status
3 pages · 1 raw · 10 tags
lint: 0 errors, 0 warnings, 0 info
no open changeset
```

Zero findings here: the curator judged this short article strong enough to
stage every page `confidence: high`. When it is less sure — a page left at
`confidence: low`, marked `contested: true`, or a single-source page with no
confidence set at all — the `fm-quality` check reports one **info**-level
finding per page, suggesting you corroborate with another source or raise the
confidence. **Info** never fails `lw lint`'s exit code; only an **error**-level
finding does. See [vault-schema.md](vault-schema.md#the-15-lint-checks)
for all 15 checks and their severities.

**If you do not want the changeset at all**, there is no `lw reject` verb —
discard it instead, either with `X` on the TUI's Review screen, or from the
command line:

```bash
lw doctor --discard-changeset
```

This moves the buffer from `.llmwiki/changesets/open/` to
`changesets/rejected/` and journals the discard; nothing is deleted, and
`lw log --rejected` shows it afterward. §10 below walks through exactly this,
discarding a changeset opened by `lw revert`.

## 8. Ask questions

`lw query` is read-only: it answers with citations to your own pages and
never opens, touches, or leaves behind a changeset.

```
$ lw query "What is the KV cache, and why does per-token cost drop from O(n) to O(1) during decoding? Cite the wiki pages."
## What the KV cache is
The **KV cache** stores the key and value projections a transformer computes during decoding, so
each new token only requires computing attention *against* the cached keys and values, rather than
recomputing those projections from scratch (`wiki/concepts/kv-cache.md`). …
## Why per-token cost drops from O(n) to O(1)
…
2. **Caching makes it O(1) incremental work.** By storing the key and value projections from
earlier steps, the cache removes the recomputation (`wiki/concepts/autoregressive-decoding.md`):
each new token only pays the constant attention work against the cached keys and values, "turning
the per-token cost from O(n) attention work back to O(1) incremental work" (`wiki/concepts/kv-cache.md`). …
**Sources cited:** `wiki/concepts/kv-cache.md`, `wiki/concepts/autoregressive-decoding.md` — both drawn
from `raw/articles/kv-cache-explained.md`.
$ lw status
3 pages · 1 raw · 10 tags
lint: 0 errors, 0 warnings, 0 info
no open changeset
```

(The answer, abridged here, took about 36 seconds; `lw status` afterwards
confirms the query left no changeset behind.)

Note the vault this ran against has exactly one source ingested so far, so
the answer is honest about that: every claim traces back to the same
article. Ask a question about something you have not ingested yet, and
the agent has nothing to cite — see §13 for what that refusal looks like.

### Keep an answer: file it as a query page

A follow-up question in the same Ask pane sees the earlier conversation
of that pane, so "expand on the second point" needs no re-explaining.

After an answer that cites the vault — one carrying a `^[raw/…]` or
`^[wiki/…]` marker — the pane shows
`ctrl+s save this answer into the wiki`. Pressing `ctrl+s` runs one
more agent turn that stages a `wiki/queries/` page, or updates an
existing query page that already answers the same question. Like
everything else, it reaches the wiki only through normal hunk review:
`ctrl+r` opens Review, and nothing lands until you commit.

An answer with no vault marker — for example one under
`Not from your vault:` — cannot be filed: pressing `ctrl+s` answers
`this answer cites no vault source; nothing to save`. The wiki only
holds sourced claims.

In `lw session show`, the history an Ask pane copied forward is marked
`· carried`, so a transcript tells the carried turns from the turn's own
records.

### Web lookup

An answer under `Not from your vault:` is the honest response to a question
the vault cannot answer — and the cue to go find a source. The curator may
search the web with `web.search` and ingest the best result with
`stage.ingest_source`, at most two pages per question.

`web.search` is offered only when configured. The tool is never denied — it
is simply *not offered* until a provider is wired, the same way the registry
treats anything a vault should not do: with no key, the agent's tool list
does not contain it, and a question falls back to the §8 answer. Tavily is
the built-in provider. As with every key in `lw` (§3), the config file holds
a reference, not a value, and the variable is resolved when a lookup runs:

```toml
[web]
api_key = "env:TAVILY_API_KEY"   # in your config.toml — see §3
```

```bash
export TAVILY_API_KEY=tvly-...
```

With `api_key` set, `lw doctor`'s config check reports it on its own
line — `web: provider tavily, env:TAVILY_API_KEY (set)`, or `(missing)`
while the variable is not exported; the key value itself is never printed.

A search result is not a source until it is ingested. The agent stages the
best hit with `stage.ingest_source` — the same op `lw ingest <url>` runs —
so a fetched page reaches `raw/` exactly the way a local file does, and the
pages proposed from it sit in a changeset you review hunk by hunk before
anything lands (§6). Once committed, an ingested page is a raw source like
any other: claims drawn from it carry `^[raw/…]` provenance markers, and an
answer built on them can be filed with `ctrl+s` exactly like one that cites
a source you ingested by hand (§8 above).

## 9. The TUI tour

`lw` with no command, or `lw tui`, opens the terminal UI. Screens cycle with
`tab`, always in this order:

```
Review → Ask → Lint → Log → Browse   (wrapping back to Review)
```

Every screen draws inside the same frame. The header row names the vault,
the five screens, and — on the right — the vault counts plus the open
changeset, or `no changeset`. The footer row always shows what the current
screen's keys actually do — for example, on Review:

```
 y accept hunk  n drop hunk  j/k move  p preview  A accept all  C commit  ? help
```

On a narrow terminal the footer drops keys from its right end until it fits.
`?` toggles a help overlay listing every key on the active screen, and the
UI needs a terminal of at least **80×24** — anything smaller shows a
"Terminal too small" notice until the window is enlarged (`q` still quits).

(The Review screens below were captured against the same vault as §5–§8,
moments after the query above; the Ask capture and the session captures in
*Reading a conversation back* come from a separate run against a fresh copy
of that vault, so their ids differ. Rows cut from a capture for length are
marked `…`.)

### Ask, then Review

Type a question and press `enter`. If no changeset is open, an Ask turn
opens one for you — the intent is literally `ask: <your question>` — and if
the turn ends without staging anything, that changeset is discarded
automatically; you never have to clean it up by hand. The message box takes
all typing, so on this screen `q` and `?` type into the message instead of
quitting or opening the overlay — quit from Ask with `ctrl+c`. A real
capture, asking the vault to stage a patch to an existing page (the turn is
abridged; the transcript tail-follows, so the earliest rows have scrolled up
under the `↑ 28 earlier` note):

```
 ml-wiki   Review  Ask  Lint  Log  Browse    3 pages · 1 raw · 0 lint   cs-fd447f · 1 op · checks ✓
╭ Transcript — cs-fd447f ──────────────────────────────────────────────────────────── ↑ 28 earlier ╮
│                                                                                                  │
│ ▸ stage.close {}  → changeset cs-fd447f752de3e221                                                │
│                                                                                                  │
│ assistant                                                                                        │
│ Done — one staged patch, nothing else in the changeset.                                          │
│                                                                                                  │
│ cs-fd447f752de3e221 — 1 operation, 1 file touched (wiki/concepts/kv-cache.md), schema/lint pass, │
│ no broken links or orphans.                                                                      │
│                                                                                                  │
│ The patch inserts a new ## Trade-offs section between "Why it matters" and "Open threads",       │
│ saying in short:                                                                                 │
│                                                                                                  │
│ • The cache trades compute for memory — it eliminates per-token recomputation of key/value       │
│   projections (linking autoregressive-decoding) but must store every past token's keys and       │
│   values, growing linearly with sequence length and batch size. ^[raw/articles/kv-cache-         │
│   explained.md]                                                                                  │
│ • Reading the cache back each step costs memory bandwidth, which flash-attention-style kernels   │
│   reduce. ^[raw/articles/kv-cache-explained.md]                                                  │
│                                                                                                  │
│ Both claims trace to the single already-ingested source, marked inline as written; no new pages, │
│ tags, or sources were introduced. Ready for your review.                                         │
│                                                                                                  │
│ done · 5 rounds                                                                                  │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
╭ Message ─────────────────────────────────────────────────────────────────────────────────────────╮
│ › █ Ask about the wiki…                                                                          │
╰──────────────────────────────────────────────────────────────────────────────────────────────────╯
 enter send  ↑/↓ select tool call  ctrl+r review  tab screen  ? help
```

Each turn renders in the same shape: your question under a bold `you`, each
tool call as one collapsed `▸ name args → result` row, the answer under
`assistant`, and a `done · N rounds` line at the end. The answer is rendered
as markdown — headings, lists, code, tables, links — through the same
renderer as Review's and Browse's previews, and the transcript panel's title
carries the session id (`Transcript — cs-fd447f`), which is how you find the
conversation again with `lw session show`; once review commits or rejects
that changeset the title keeps the id with its state, and a turn that staged
nothing adds a `nothing staged · conversation kept · lw session show <id>`
line saying so. `↑`/`↓` select a tool
call in the transcript and `enter` expands or collapses the selected one.
The header's right side tracks the buffer the whole time: the moment the
agent stages something, `no changeset` becomes `cs-fd447f · 1 op · checks
✓`. Pressing `ctrl+r` jumps straight to Review:

```
 ml-wiki   Review  Ask  Lint  Log  Browse    3 pages · 1 raw · 0 lint   cs-1c86cb · 1 op · checks ✓
╭ Ops ──────────────────────────╮╭ Diff ──────────────────────────────────────────────── p preview ╮
│▌● patch kv-cache.md           ││ patch  wiki/concepts/kv-cache.md                   op1 · 1 hunk │
│                               ││ User-requested short Trade-offs section, grounded in the page's │
│                               ││ only source (raw/articles/kv-cache-explained.md, "Why it        │
│                               ││ matters" list): the cache trades compute savings for linear     │
│                               ││ memory growth and per-step bandwidth cost. Placed after…        │
│                               ││                                                                 │
│                               ││▌@@ -32,6 +32,16 @@  h1                                          │
│                               ││▌  - Flash-attention style kernels reduce the memory-bandwidth   │
│                               ││▌  cost of reading                                               │
│                               ││▌  the cache back during each decoding step.                     │
│                               ││▌  ^[raw/articles/kv-cache-explained.md]                         │
│                               ││▌                                                                │
│                               ││▌+ ## Trade-offs                                                 │
│                               ││▌+                                                               │
│                               ││▌+ The cache trades memory for compute: it avoids recomputing    │
│                               ││▌  attention over all                                            │
│                               ││▌+ previous tokens at every step, but its footprint grows        │
│                               ││▌  linearly with sequence                                        │
│                               ││▌+ length and batch size, and each decoding step must read the   │
│                               ││▌  cache back,                                                   │
│                               ││▌+ paying a memory-bandwidth cost.                               │
│                               ││▌  ^[raw/articles/kv-cache-explained.md]                         │
│                               ││▌+                                                               │
│                               ││▌+ Techniques for shrinking that footprint are collected under   │
│                               ││▌+ [[reducing-kv-cache-memory]].                                 │
│                               ││▌+                                                               │
│                               ││▌  ## Related                                                    │
╰─────────────────────── 1 of 1 ╯│▌                                                                │
╭ Changeset ────────────────────╮│▌  - [[autoregressive-decoding]] — the decoding loop the cache   │
│ source  ask: Stage one patch… ││▌  serves                                                        │
│ id      cs-1c86cb57bba9cb8a   ││                                                                 │
│ ops     1 · 0 dropped · 0 st… ││                                                                 │
│ hunks   1 kept · 0 dropped    ││                                                                 │
│                               ││                                                                 │
│ checks  ✓ schema   ✓ lint     ││                                                                 │
│         ✓ orphans  ✓ links    ││                                                                 │
╰───────────────────────────────╯╰─────────────────────────────────────────────────────────────────╯
 y accept hunk  n drop hunk  j/k move  p preview  A accept all  C commit  ? help
```

The left-hand **Ops** panel lists the changeset's operations, each with a
glyph for its state: `●` untouched, `◐` with some of its hunks dropped, `✗`
with every hunk dropped, and `!` stale — an op proposed against a file that
has since changed on disk, which `y`, `n` and `A` refuse to act on. The
`▌` gutter marks the cursor: the row it names in Ops, and the hunk it names
in the **Diff** panel on the right, which carries the op's rationale and
provenance above the hunks themselves. On a terminal tall enough, a third
panel under Ops summarizes the changeset — where it came from, its id, the
ops and hunks kept or dropped, and the engine checks.

A brand-new page has no hunks to accept or drop individually — it is shown
as one whole-file change, with its rationale and provenance right there.

Pressing `p` flips the Detail panel between the diff and a **Preview** of
the page under the cursor as it will read after commit:

```
╭ Ops ──────────────────────────╮╭ Preview ──────────────────────────────────────────────── p diff ╮
│▌● patch kv-cache.md           ││ patch  wiki/concepts/kv-cache.md              op1 · staged page │
│                               ││                                                                 │
│                               ││   KV cache                                                      │
│                               ││   concept · basics, reference · confidence high · updated       │
│                               ││   2026-09-15                                                    │
│                               ││                                                                 │
│                               ││   KV cache                                                      │
│                               ││                                                                 │
│                               ││   The KV cache stores the key and value projections a           │
│                               ││   transformer computes during autoregressive-decoding, so each  │
│                               ││   new token only requires computing attention against the       │
│                               ││   cached keys and values, not recomputing them from scratch.    │
│                               ││   [kv-cache-explained.md]                                       │
│                               ││                                                                 │
…
│                               ││ ▎ Trade-offs                                                    │
│ checks  ✓ schema   ✓ lint     ││ ▎                                                               │
│         ✓ orphans  ✓ links    ││ ▎ The cache trades memory for compute: it avoids recomputing    │
╰───────────────────────────────╯╰────────────────────────────────────────────────────── ↓ 14 more ╯
 y accept hunk  n drop hunk  j/k move  p diff  A accept all  C commit  X reject changeset  ? help
```

Pressing `p` again flips back to the diff. Pressing `C` commits: the footer
confirms with the commit id — ` committed 000002` — the header's right side
returns to `no changeset`, and the journal gets its `commit_end` line.

Not every Ask turn stages something. On an empty vault, `lw log` after an
Ask turn that only answered a question (nothing to cite, nothing to stage)
reads:

```
changeset_opened   … message="ask: Which pages exist so far?"
changeset_rejected … message="ask turn staged nothing"
```

### Reading a conversation back

Nothing above is lost when the turn ends. Every agent conversation is
recorded as it happens, in the vault, under the changeset it opened — a
session *is* a changeset, and the session id in the Transcript panel's title
is the changeset id — and the recording stays after the changeset is
committed or rejected. `lw session list` shows what is on disk, newest first:

```
$ lw session list
ID         STATE      STARTED               RECORDS  BYTES
cs-fd447f  open       2026-09-16T12:00:06Z  10       15379
cs-bfa09e  rejected   2026-09-16T11:55:44Z  10       16158
cs-614c83  committed  2026-09-16T11:51:12Z  17       44643
```

`lw session show [<id>]` prints one back: the question, every tool call with
its full arguments and full result, and each answer rendered exactly as the
TUI drew it. It defaults to the open session, and a short prefix of the id
works (`cs-fd447f` or plain `fd447f`). The model's thinking is folded to one
line unless you pass `--thinking`; `--plain` strips colour for piping, and
`--json` prints the transcript file byte for byte. The capture below is
trimmed in the middle — a full transcript runs long, because nothing in it is
abbreviated — and its ids come from a separate run from the rest of this
tutorial, so they will not match the ones above:

```
$ lw session show --plain
session cs-fd447f752de3e221 · open · 2026-09-16T12:00:06Z · 10 records

── you ─────────────────────────────────────────────────────────────────────────

Stage one patch only, no new pages: add a short Trade-offs section to wiki/concepts/kv-cache.md — what the cache trades compute for, per the ingested source.

── assistant ───────────────────────────────────────────────────────────────────

(thinking: 20 lines hidden; --thinking shows them)

The orientation is already in context, so I'll go straight to reading the target
page and its source. First, the current state of kv-cache:

── tool wiki.get ───────────────────────────────────────────────────────────────

args:
  page: wiki/concepts/kv-cache.md
result:
  ---
  title: KV cache
  created: 2026-09-16
  updated: 2026-09-16
  type: concept
  tags: [basics, reference]
  sources: [raw/articles/kv-cache-explained.md]
  confidence: high
  ---

  # KV cache

…
```

### Review

`j`/`k` move between hunks; `y` accepts (undrops) the selected hunk and
advances, `n` drops it and advances; `g`/`G` jump to the start and end of
the walk. `A` accepts every remaining hunk, but is refused outright if the
changeset does not currently pass lint — you cannot
blanket-accept your way past a warning you have not looked at. `X` rejects
the whole changeset. `C` commits, subject to the same lint-regression rule
as `lw commit` on the command line.

### Lint, Log, Browse

Lint lists findings by severity; `enter` opens the finding's page in
Browse. Log lists the journal's events; `f` cycles its filter through
all → accepted → rejected → agent → human, and `r` reverts the selected
**commit** into a new changeset, which drops you into Review to decide on it. Browse is the page tree: `enter`
opens a page, `/` finds one by name, `h`/`l` collapse or expand a subtree.

Every key above is the shipped default. All of them except a handful of
screen-local ones (`enter`, the arrow keys and `ctrl+r` on Ask; `enter` on
Lint; `enter`, `/`, `h`/`l` on Browse) are rebindable from
`~/.config/lw/hotkeys.toml` — see
[hotkeys.md](hotkeys.md) for the full table, the file format, and the
match-order rule that lets a rebound navigation key shadow an action key on
the same screen.

## 10. History and undo

`lw log` prints the journal, oldest problems included:

```
$ lw log --limit 20
2026-09-15T20:20:04Z changeset_opened changeset=cs-92c7d850b461792a actor=agent message="ingest ~/Downloads/kv-cache-explained.md"
2026-09-15T20:20:23Z op_proposed changeset=cs-92c7d850b461792a op=op1 actor=agent paths=raw/articles/kv-cache-explained.md
2026-09-15T20:22:47Z op_proposed changeset=cs-92c7d850b461792a op=op2 actor=agent paths=wiki/concepts/kv-cache.md
…
2026-09-15T20:25:55Z commit_end changeset=cs-92c7d850b461792a commit=000001 actor=agent message="first pages from kv-cache-explained" data={"lint_errors":0,"lint_warns":0}
```

Useful filters: `--agent` (only agent-authored events), `--rejected` (only
discarded changesets), `--limit <n>`, `--page <path>` (one page's history),
`--since <rfc3339|YYYY-MM-DD>`.

`lw revert <commit-id>` does not touch anything directly — it computes the
inverse of that commit's ops and opens **a new changeset**, so a rollback
gets exactly the same review it would get on the way in:

```
$ lw revert 000001
opened cs-c5b709e6ddbd1832: revert of 000001 (skipped: index.md, raw/articles/kv-cache-explained.md)
skipped: index.md
skipped: raw/articles/kv-cache-explained.md
$ lw diff --stat
wiki/concepts/autoregressive-decoding.md | +2 -14
wiki/concepts/kv-cache.md | +2 -32
wiki/queries/reducing-kv-cache-memory.md | +3 -19
3 file(s) changed, 7 insertion(s)(+), 65 deletion(s)(-)
```

Two paths are skipped, and the revert says so rather than dropping them
silently. `raw/articles/kv-cache-explained.md` is skipped because `raw/` is
write-once and immutable: a revert can undo the pages a source produced, but
it never touches the source itself. `index.md` is skipped because v1 does not
reconstruct `index.md` on revert; the reverted pages become retraction
stubs rather than disappearing, so the index entries that point at them keep
resolving. From here you either `lw commit -m "revert 000001"` like any
other changeset, or decide you did not want the rollback after all and
discard it:

```
$ lw doctor --discard-changeset
lw doctor — /home/you/ml-wiki
discarded open changeset cs-c5b709e6ddbd1832 (.llmwiki/changesets/open -> .llmwiki/changesets/rejected)
…
$ lw log --rejected --limit 5
2026-09-15T20:32:34Z changeset_rejected changeset=cs-2ed04c0ee9cc69bf actor=agent message="rejected in review"
2026-09-15T20:36:08Z changeset_rejected changeset=cs-c5b709e6ddbd1832 actor=human message="discarded by lw doctor --discard-changeset"
```

The first line is an earlier changeset turned down with `X` on the Review
screen — rejections are journalled too — and the second is the discard
above.

Only one changeset can be open at a time, whether it came from `ingest`, an
Ask turn, or `revert` — commit, reject, or discard the current one before
starting another.

## 11. Keeping the vault healthy

`lw doctor` runs eight checks — **index, objects, journal, recovery, lock,
git, config, provider** — and every failing one prints a `fix:` line. On a fresh
vault with no key exported yet, only `config` fails, and `provider` is
skipped rather than attempted, because there is nothing to probe with:

```
$ lw doctor
lw doctor — /home/you/ml-wiki
✓ index    0 document(s) indexed in .llmwiki/index.gob
✓ objects  no objects referenced yet (no open changeset, no snapshot)
✓ journal  0 event(s) parse cleanly
✓ recovery no interrupted apply
✓ lock     no lock held
- git      skipped: not a git repository
✗ config   api_key env:DEEPSEEK_API_KEY (missing) · model deepseek-v4-flash @ https://api.deepseek.com/v1
  fix: export DEEPSEEK_API_KEY, or point llm.api_key at another variable with lw config set llm.api_key env:NAME
- provider skipped: api_key did not resolve: config: environment variable DEEPSEEK_API_KEY is not set
1 of 8 check(s) failed
```

The `git` check looks at the vault's own git repository, if it is one: a
vault that tracks nothing under `.llmwiki/` passes, and one that *does* track
it draws a warning — transcripts hold whole file dumps, raw provider output
and the model's thinking, and a vault may be a public repository. The warning
names the fix (`git rm -r --cached .llmwiki/ && git commit`); lw never runs
that command itself, and no `.gitignore` removes what git history already
holds.

`--unlock` clears a stale single-writer lock (only ever held while a commit
is applying); `--rebuild-index` rebuilds the disposable search-index cache
(`.llmwiki/index.gob`) from the vault's pages before checking. It is safe to
run any time, since the index is a cache, not the source of truth. `--json` emits the same eight checks as one JSON object, for
scripting. `--discard-changeset` is covered in §7 and §10.

`recovery` is the one worth understanding before you need it: a commit
writes each file with its own temp-file → `fsync` → rename, which is atomic
per file but not across the whole commit — POSIX has no way to make a dozen
renames one transaction. `commit_begin`/`commit_end` bracket every apply in
the journal, so an interrupted one is visible, and because every post-image
is already saved in the content-addressed object store, `lw doctor` reports
the applied/pending split and the commit is rolled **forward** from
`objects/`, never guessed at or reverted by hand. See
[changesets.md](changesets.md#recovery--and-the-honest-boundary) for the full
account.

Lint is the other health signal, and it is worth re-reading now that you
have committed pages: **error** fails `lw lint`'s exit code, **warn** and
**info** do not. A page with one source and `confidence: low` (§7 above) is
an `info` — normal, not a problem to chase down before you have a second
source. See [vault-schema.md](vault-schema.md#the-15-lint-checks) for the
full table of 15 checks and their severities.

## 12. Using lw from other agents

`lw mcp` runs the same tools the CLI's agent verbs offer — the 18 vault
tools, plus a conditional 19th — over an MCP stdio server, for any client
that brings its own model. (The CLI's agent verbs offer a 19th,
`web.search`, whenever `[web].api_key` is configured and resolves — §8's
Web lookup. Over MCP the same rule holds: a configured server offers
`web.search` as the 19th tool alongside the 18 vault tools, and with no
provider wired it is simply not offered on either surface.)

```bash
lw mcp            # from inside a vault
```

```json
{
  "mcpServers": {
    "lw": { "command": "lw", "args": ["mcp"] }
  }
}
```

No API key or provider is needed to run `lw mcp` itself — the tools are
vault operations, not model calls. Dots become underscores on the wire
(`wiki.search` → `wiki_search`); nothing is added or removed in the
translation. The MCP server wires the same HTML and markdown extractor
chain the CLI's `ingest` verb does, so `stage_ingest_source` extracts
sources identically over either surface. Every read, patch, rename,
merge, split, link and retract tool works the same either way. See
[tools.md](tools.md) for what each of the 18 vault tools reads or stages,
plus the conditional `web.search`.

## 13. Troubleshooting

**The provider returns a 400 or 404, or "not found".** Your `llm.base_url`
is almost certainly pointed at the wrong path. It must be an
OpenAI-compatible chat-completions endpoint (typically ending in `/v1`), not
a provider's native or Anthropic-shaped Messages path — `.../anthropic`
instead of `.../v1` is exactly this mistake, and it looks like a valid host
right up until the first request comes back 404. Fix with
`lw config set llm.base_url <the /v1 endpoint>` and re-probe.

**`api_key env:DEEPSEEK_API_KEY (missing)`, or `resolve api key: … environment
variable … is not set`.** `lw` never stores a key value, only a reference —
export the variable the reference names in the shell you are running `lw`
from:

```bash
export DEEPSEEK_API_KEY=sk-...
```

If you are using a different provider, point the reference at your own
variable first: `lw config set llm.api_key env:MY_PROVIDER_KEY`.

**"a changeset is already open" / you cannot ingest, ask, or revert.** Only
one changeset exists at a time. Finish the one you have — `lw status` shows
its id and op count, `lw diff` shows what it would write — then either
`lw commit`, discard it with `X` on Review, or `lw doctor
--discard-changeset` from the command line (§7).

**The agent refuses to write a page, on an empty vault or otherwise.** The
curator will not synthesize a claim it cannot cite: every page needs
provenance back to something in `raw/`, and needs at least two outbound
`[[wikilinks]]`. Asking the Ask screen to "create a page about X" before you
have ingested anything gets a reasoned refusal, not a fabricated page (and
the empty changeset that turn opened is discarded for you) — ingest a source
first. `lw query` never writes pages at all; it only answers.

**Where the state actually lives.** `.llmwiki/journal.ndjson` is the
append-only event log; `.llmwiki/changesets/{open,committed,rejected}/`
holds every changeset, each with the `session.ndjson` transcript that
produced it, forever — git-ignored by the vault `.gitignore`, and readable
again with `lw session show`. See [changesets.md](changesets.md) for the full
layout and lifecycle.
