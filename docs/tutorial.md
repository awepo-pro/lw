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
$ lw --version
lw 1.0.0-dev
```

`lw` finds its vault by walking up from your current directory to the
nearest ancestor containing `SCHEMA.md` — so every verb below except `init`
expects you to be inside a vault, or to pass `-vault <dir>`.

## 3. Connect a provider

`lw` speaks the plain OpenAI chat-completions wire format to whatever
endpoint you point it at. It defaults to DeepSeek:

```
$ lw config
config file: /home/you/.config/lw/config.toml (not present — showing lw's defaults)

llm.base_url               = https://api.deepseek.com/v1   (default)
llm.model                  = deepseek-v4-flash   (default)
llm.api_key                = env:DEEPSEEK_API_KEY (missing)   (default)
llm.temperature            = 0.2   (default)
llm.max_tokens             = 8192   (default)
llm.limits.max_tool_rounds = 24   (default)
llm.limits.context_tokens  = 96000   (default)
theme                      = (not set)   (default)

(file) = not lw's built-in value · (default) = lw's built-in value; change one with lw config set <key> <value>
```

(That path is your own `$XDG_CONFIG_HOME/lw/config.toml`, or `~/.config/lw/config.toml`
when that variable is unset — nothing above is vault-specific.)

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
$ mkdir ml-notes && cd ml-notes
$ lw init --schema ml-systems
initialized vault in /home/you/ml-notes
  domain           ml-systems
  tags             10 (10-20 recommended)
  files            SCHEMA.md, index.md, log.md, curator-memory.md
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
- **`raw/{articles,papers,transcripts,assets}/`** — where ingested sources
  land, write-once and immutable.
- **`wiki/{entities,concepts,comparisons,queries}/`** — where reviewed pages
  land, one directory per page type.
- **`.llmwiki/`** — engine state: the object store, the changeset buffer,
  the journal, the search index cache. Not part of the reviewable wiki —
  deleting it loses history, not your notes.

See [vault-schema.md](vault-schema.md) for the full layout, the frontmatter
fields, and the 14 lint checks.

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
command with nothing created.

```bash
lw ingest /home/you/Downloads/gemini-note.md     # a local file
lw ingest https://example.com/some-post          # or a URL
```

A real run, ingesting a 51 KB saved chat transcript about a vLLM prefix-cache
debugging session. It took about a minute and a half. The agent's streamed
summary comes first (its wording varies run to run, so it is abridged here);
the changeset summary at the end is `lw`'s own, fixed output:

```
$ lw ingest /home/anton/Downloads/gemini-note.md
Ingested raw/articles/gemini.md (a 4-chunk Gemini chat transcript, exported from a vLLM deployment
debugging session), then built four pages around it:
| wiki/entities/vllm.md | entity | …
| wiki/concepts/prefix-caching.md | concept | …
| wiki/concepts/multimodal-cache.md | concept | …
| wiki/queries/why-is-prefix-cache-hit-rate-zero.md | query | …
…the three mechanism pages carry confidence: low with contested: true.
opened changeset cs-5cf3e3d030aad754: ingest /home/anton/Downloads/gemini-note.md (5 op(s))
  op1 ingest_source raw/articles/gemini.md
  op2 create_page wiki/entities/vllm.md
  op3 create_page wiki/concepts/prefix-caching.md
  op4 create_page wiki/concepts/multimodal-cache.md
  op5 create_page wiki/queries/why-is-prefix-cache-hit-rate-zero.md
```

`op1` is always the immutable copy of your source, staged (not yet written)
under `raw/articles/`, `raw/papers/` or `raw/transcripts/` depending on kind.
It carries the original path or URL forward as `source_url`, and a `sha256`
of its body that later drives drift detection:

```
$ head -5 raw/articles/gemini.md
---
source_url: /home/anton/Downloads/gemini-note.md
ingested: 2026-09-13
sha256: 8ff0a73248a8b2d1fdf7d56f43256435af3a027431adc769eacb678f2aeeb98e
---
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
open changeset cs-5cf3e3d030aad754: ingest /home/anton/Downloads/gemini-note.md (5 op(s), 0 stale; schema=pass lint=pass orphans=0 broken_links=0)
```

The `open changeset` line is where the real state lives: how many ops, how
many are stale (proposed against a file that has since changed on disk), and
whether the buffer currently passes schema, lint, orphan and broken-link
checks.

`lw diff --stat` shows what would land, one line per file:

```
$ lw diff --stat
wiki/concepts/multimodal-cache.md | +33 -0
wiki/concepts/prefix-caching.md | +56 -0
wiki/entities/vllm.md | +50 -0
wiki/queries/why-is-prefix-cache-hit-rate-zero.md | +47 -0
index.md | +7 -0
raw/articles/gemini.md | +670 -0
6 file(s) changed, 863 insertion(s)(+), 0 deletion(s)(-)
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
$ lw commit -m "first pages from gemini-note"
committed 000001
```

A commit re-hashes the tree first — if a file an op was proposed against has
changed on disk since (someone edited it by hand, or another op touched it),
that op is stale and the commit is refused rather than silently overwriting
your edit. A commit is also refused if it would make lint worse than it
already is; `--force` overrides that, and the override itself is journalled,
so "we shipped a lint regression on purpose" is part of the permanent record,
not a quiet exception.

After the commit, `lw status` counts what actually landed:

```
$ lw lint
wiki/concepts/multimodal-cache.md:0: info: confidence is low; corroborate with another source or raise the confidence (fm-quality)
wiki/concepts/prefix-caching.md:0: info: confidence is low; corroborate with another source or raise the confidence (fm-quality)
wiki/queries/why-is-prefix-cache-hit-rate-zero.md:0: info: confidence is low; corroborate with another source or raise the confidence (fm-quality)
0 errors, 0 warnings, 3 info
$ lw status
4 pages · 1 raw · 10 tags
lint: 0 errors, 0 warnings, 3 info
no open changeset
```

Three `info`-level findings, zero errors: a page synthesized from a single
source is expected to read `confidence: low` until something else
corroborates it — that is `fm-quality`, and `lw lint` only exits non-zero on
an **error**-level finding. See [vault-schema.md](vault-schema.md#the-14-lint-checks)
for all 14 checks and their severities.

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
$ lw query "What is prefix caching in vLLM, and why can the hit rate read 0%? Cite the wiki pages."
## Why the hit rate can read 0%
Two distinct stories, and the vault keeps them separate.
**(a) A real hit that the metric hides** — wiki/concepts/prefix-caching.md, "Observing it", and wiki/queries/why-is-prefix-cache-hit-rate-zero.md: …
**(b) A hit that genuinely never happens** — the six candidate causes tabled in wiki/queries/why-is-prefix-cache-hit-rate-zero.md: …
**Overall status:** all four pages carry low/medium confidence … every claim above traces to a single source, raw/articles/gemini.md.
$ lw status
4 pages · 1 raw · 10 tags
lint: 0 errors, 0 warnings, 3 info
no open changeset
```

(The answer, abridged here, took about 12 seconds; `lw status` afterwards
confirms the query left no changeset behind.)

Note the vault this ran against has exactly one source ingested so far, so
the answer is honest about that: every claim traces back to the same
transcript. Ask a question about something you have not ingested yet, and
the agent has nothing to cite — see §13 for what that refusal looks like.

## 9. The TUI tour

`lw` with no command, or `lw tui`, opens the terminal UI. Screens cycle with
`tab`, always in this order:

```
Review → Ask → Lint → Log → Browse   (wrapping back to Review)
```

The footer always shows what the current screen's keys actually do — for
example, on Review:

```
[tab] next screen · [ctrl+r] ask→review · [y/n] accept/drop · [C] commit · [q] quit
```

### Ask, then Review

Type a question and press `enter`. If no changeset is open, an Ask turn
opens one for you — the intent is literally `ask: <your question>` — and if
the turn ends without staging anything, that changeset is discarded
automatically; you never have to clean it up by hand. A real capture, asking
the vault above to stage a new page:

```
 vault — 4 pages · 1 raw · ⚠ 0 lint
STAGE               │you: From raw/articles/gemini.md, stage one new glossary-style concept page wiki/concepts/kv-cache-block.md …
no changeset        │▸ stage.create_page {"path": "wiki/concepts/kv-cache-block.md", "ti…  → proposed op1 (create_page)
                    │▸ stage.close {}  → changeset cs-94394f82d4f6264a intent: ask: From raw/article…
                    │— done: stop (7 round(s)) —
 [tab] next screen · [ctrl+r] ask→review · [y/n] accept/drop · [C] commit · [q] quit
```

The moment `stage.create_page` succeeds, the left-hand badge changes from
`no changeset` to `cs-94394f82d4f6264a / 1 op(s)`. Pressing `ctrl+r` jumps
straight to Review:

```
STAGE               │OPS                             |op1  create_page  wiki/concepts/kv-cache-block.md
                    │op1 create_page wiki/concepts/kv|rationale: New glossary-style concept page defining vLLM's 16-token KV cache block, requested …
cs-94394f82d4f6264a │                                |provenance: raw/articles/gemini.md
1 op(s)             │                                |(no hunks — whole-file change)
```

A brand-new page has no hunks to accept or drop individually — it is shown
as one whole-file change, with its rationale and provenance right there.
Pressing `C` commits it: the badge returns to `no changeset`, and the
journal gets a `commit_end … commit=000002` line.

Not every Ask turn stages something. On an empty vault, `lw log` after an
Ask turn that only answered a question (nothing to cite, nothing to stage)
reads:

```
changeset_opened   … message="ask: Which pages exist…"
changeset_rejected … message="ask turn staged nothing"
```

### Review

`j`/`k` move between hunks; `y` accepts (undrops) the selected hunk and
advances, `n` drops it and advances. `A` accepts every remaining hunk, but is
refused outright if the changeset does not currently pass lint — you cannot
blanket-accept your way past a warning you have not looked at. `X` rejects
the whole changeset. `C` commits, subject to the same lint-regression rule
as `lw commit` on the command line.

### Lint, Log, Browse

Lint lists findings by severity; `enter` expands one or jumps to the page in
Browse. Log lists the journal's events; `f` cycles its filter through
all → accepted → rejected → agent → human, and `r` reverts the selected
**commit** into a new changeset, which drops you into Review to decide on it. Browse is the page tree: `enter`
opens a page, `/` finds one by name, `h`/`l` collapse or expand a subtree.

Every key above is the shipped default. All of them except a handful of
screen-local ones (`enter`, arrows, `ctrl+r` on Ask; `enter`, `/`, `h`/`l` on
Browse) are rebindable from `~/.config/lw/hotkeys.toml` — see
[hotkeys.md](hotkeys.md) for the full table, the file format, and the
match-order rule that lets a rebound navigation key shadow an action key on
the same screen.

## 10. History and undo

`lw log` prints the journal, oldest problems included:

```
$ lw log --limit 20
2026-09-13T18:03:49Z changeset_opened changeset=cs-5cf3e3d030aad754 actor=agent message="ingest /home/anton/Downloads/gemini-note.md"
2026-09-13T18:03:54Z op_proposed changeset=cs-5cf3e3d030aad754 op=op1 actor=agent paths=raw/articles/gemini.md
2026-09-13T18:04:37Z op_proposed changeset=cs-5cf3e3d030aad754 op=op2 actor=agent paths=wiki/entities/vllm.md
…
2026-09-13T18:05:15Z commit_end changeset=cs-5cf3e3d030aad754 commit=000001 actor=agent message="first pages from gemini-note" data={"lint_errors":0,"lint_warns":0}
```

Useful filters: `--agent` (only agent-authored events), `--rejected` (only
discarded changesets), `--limit <n>`, `--page <path>` (one page's history),
`--since <rfc3339|YYYY-MM-DD>`.

`lw revert <commit-id>` does not touch anything directly — it computes the
inverse of that commit's ops and opens **a new changeset**, so a rollback
gets exactly the same review it would get on the way in:

```
$ lw revert 000001
opened cs-62c74288e3c4cde2: revert of 000001 (skipped: index.md, raw/articles/gemini.md)
$ lw diff --stat
wiki/concepts/multimodal-cache.md | +3 -18
wiki/concepts/prefix-caching.md | +3 -41
wiki/entities/vllm.md | +3 -36
wiki/queries/why-is-prefix-cache-hit-rate-zero.md | +3 -32
4 file(s) changed, 12 insertion(s)(+), 127 deletion(s)(-)
```

Two paths are skipped, and the revert says so rather than dropping them
silently. `raw/articles/gemini.md` is skipped because `raw/` is write-once and
immutable: a revert can undo the pages a source produced, but it never
touches the source itself. `index.md` is skipped because v1 does not
reconstruct `index.md` on revert; the reverted pages become retraction
stubs rather than disappearing, so the index entries that point at them keep
resolving. From here you either `lw commit -m "revert 000001"` like any
other changeset, or decide you did not want the rollback after all and
discard it:

```
$ lw doctor --discard-changeset
discarded open changeset cs-62c74288e3c4cde2 (.llmwiki/changesets/open -> .llmwiki/changesets/rejected)
$ lw log --rejected --limit 5
2026-09-13T18:05:49Z changeset_rejected changeset=cs-62c74288e3c4cde2 actor=human message="discarded by lw doctor --discard-changeset"
```

Only one changeset can be open at a time, whether it came from `ingest`, an
Ask turn, or `revert` — commit, reject, or discard the current one before
starting another.

## 11. Keeping the vault healthy

`lw doctor` runs seven checks — **index, objects, journal, recovery, lock,
config, provider** — and every failing one prints a `fix:` line. On a fresh
vault with no key exported yet, only `config` fails, and `provider` is
skipped rather than attempted, because there is nothing to probe with:

```
$ lw doctor
lw doctor — /home/you/ml-notes
✓ index    0 document(s) indexed in .llmwiki/index.gob
✓ objects  no objects referenced yet (no open changeset, no snapshot)
✓ journal  0 event(s) parse cleanly
✓ recovery no interrupted apply
✓ lock     no lock held
✗ config   api_key env:DEEPSEEK_API_KEY (missing) · model deepseek-v4-flash @ https://api.deepseek.com/v1
  fix: export DEEPSEEK_API_KEY, or point llm.api_key at another variable with lw config set llm.api_key env:NAME
- provider skipped: api_key did not resolve: config: environment variable DEEPSEEK_API_KEY is not set
1 of 7 check(s) failed
```

`--unlock` clears a stale single-writer lock (only ever held while a commit
is applying); `--rebuild-index` rebuilds the disposable search-index cache
(`.llmwiki/index.gob`) from the vault's pages before checking. It is safe to
run any time, since the index is a cache, not the source of truth. `--json` emits the same seven checks as one JSON object, for
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
source. See [vault-schema.md](vault-schema.md#the-14-lint-checks) for the
full table of 14 checks and their severities.

## 12. Using lw from other agents

`lw mcp` runs the same 17 tools over an MCP stdio server, for any client
that brings its own model:

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
translation. One gap worth knowing about: over MCP, `stage_ingest_source`
reports `no extractor configured`, because the extractor is wired by the
CLI's `ingest` verb, not by the MCP transport. Every read, patch, rename,
merge, split, link and retract tool works the same either way. See
[tools.md](tools.md) for what each of the 17 tools reads or stages.

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
produced it, forever. See [changesets.md](changesets.md) for the full
layout and lifecycle.
