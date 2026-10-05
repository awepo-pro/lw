# The tool surface

19 tools, of which one is conditional: `web.search` is registered only when a
search provider is configured (a `[web].api_key`; workflow 010), so a
provider-less registry exposes 18. **There is no filesystem verb, and none is
ever added** — no `write`, no `edit`, no `delete`, no `bash`, no `exec`. Not
denied: *not offered*. The registry is compiled into the binary, so there is
nothing for a prompt or a permission profile to flip.

Each tool is defined once in `internal/tools` and consumed twice:

- by the **in-process agent loop** (`lw ingest`, `lw query`, `lw lint --fix`,
  the TUI's ask screen), under its canonical dotted name;
- over **MCP** (`lw mcp`), where dots become underscores: `wiki.search` →
  `wiki_search`. The mapping is total and bijective over every registered
  name, `web.search` included, and the MCP layer adds no tool and changes no
  semantics — it is a transport.

## Read — no side effects, ever

| # | Tool | MCP name | What it reads |
|---|---|---|---|
| 1 | `vault.orient` | `vault_orient` | `SCHEMA.md`, `index.md` (truncated to 200 lines), `curator-memory.md` and the last 30 `log.md` lines **in one call**. On demand: the engine injects the orientation digest itself once per session (§11.3), so the tool is there when the agent wants a fresh look |
| 2 | `wiki.search` | `wiki_search` | The word index: `{q, type?, tags?, limit?}` → at most 20 hits of title plus a short snippet, **never a page body** |
| 3 | `wiki.get` | `wiki_get` | One page in full, or one section (`{page, section?}`, the section being the exact heading line). When the open changeset already stages the page, the staged bytes are what comes back — under a staged notice, so the agent reads its own staged edits before composing the next one. When the `page` argument is not the exact path (a basename, say), the result opens with `resolved "<arg>" → <path> (use this path in stage.* calls)`, so the model never has to guess the folder |
| 4 | `wiki.neighbors` | `wiki_neighbors` | `{page, depth?}` (1–2 hops) in either link direction — the duplicate-page check |
| 5 | `wiki.backlinks` | `wiki_backlinks` | `{page}` → every page linking in, with the linking line's text |
| 6 | `raw.get` | `raw_get` | One ~4000-token chunk of an immutable source's body, by its exact vault-relative path. When the body carries PDF page anchors, the `chunk i of n` header names the physical pages the chunk sits on — `chunk 2 of 3 · pages 2-4`, or `· page 4` when the chunk sits on one — so a claim read here can be cited as `^[src p.N]`. If you do not know a raw source's exact path, call `raw.list` first. A not-found result names up to three closest existing sources (`closest raw sources: …`); `stage.patch_page` and `stage.add_link` do the same for pages, and a missing section lists the page's valid headings |
| 7 | `raw.list` | `raw_list` | Every raw source — committed and staged in the open changeset — one per line: `<path> — <title> — <source_url> — ingested <date> — sha <8 hex>`, or `… — staged in <changeset id>` for a staged one. `query` keeps only rows where every term appears in the path, title or source_url; `limit` defaults to 50 and caps at 200, with `(and N more)` when rows were cut. Read-only, like every tool above |
| 8 | `wiki.lint` | `wiki_lint` | The lint findings, computed by `internal/lint` in Go. **The model never computes a lint result; it only reads these** |
| 9 | `web.search` | `web_search` | The public web — the one outward-facing tool, and only when a search provider is wired: `{query, max_results?}` → at most 10 ranked hits, each a title, URL and short snippet, **never a page body**. Fetch a promising hit into the vault with `stage.ingest_source` |

## Propose — staged, never applied

A proposal appends an op to the open changeset. It does not touch `wiki/`.
Nothing reaches the working tree until a human commits.

| # | Tool | MCP name | What it stages |
|---|---|---|---|
| 10 | `stage.open` | `stage_open` | `{intent}` — opens the buffer and the session that travels with it |
| 11 | `stage.create_page` | `stage_create_page` | A full page: path, title, type, tags, sources, confidence, contested, body, rationale — frontmatter, taxonomy, directory and outbound-link rules all checked *at proposal time* |
| 12 | `stage.patch_page` | `stage_patch_page` | A **section-level** patch: `replace_text`, `replace_section`, `append_section`, `insert_after`, `insert_before` or `remove_section`. Sections survive reformatting; line-number patches do not. `replace_text` swaps a `find` string that must occur exactly once in the section, so a one-link edit never rewrites the section. `replace_section` refuses to drop a nested subsection. Either replace op refuses to cut more than half of a section of 400 bytes or more, and that refusal lists the paragraphs that would go; `"allow_shrink": true` confirms the cut, but only when repeated after that refusal. When the page is already staged in the open changeset, the patch composes against that staged state — the section lookup and `Before` read the staged bytes, not the committed ones |
| 13 | `stage.rename_page` | `stage_rename_page` | `{from, to}` — the engine computes every inbound backlink rewrite from the graph, each one its own reviewable hunk. This is what `mv` cannot do |
| 14 | `stage.merge_pages` | `stage_merge_pages` | `{sources[], into}` — redirect plus backlink rewrites as one reviewable unit |
| 15 | `stage.split_page` | `stage_split_page` | `{path, sections[]}` — the source becomes a reviewable stub |
| 16 | `stage.add_link` | `stage_add_link` | `{from, to, context?}` — bidirectional; refuses a broken endpoint |
| 17 | `stage.ingest_source` | `stage_ingest_source` | `{uri, kind?, name?}` — extracts a **local file or an http(s) URL**, writes `raw/` (write-once, only for a path that does not exist yet), hashes and dedupes by body `sha256`. A fetched page becomes a raw source like any other — same dedupe, same naming, same hunk-level review before anything commits. The MCP transport wires the same HTML and markdown extractor chain as the CLI, so extraction behaves identically over either surface. `name` is used only when the title slugs to nothing (idea 011) |
| 18 | `stage.retract` | `stage_retract` | `{page, reason}` — a tombstone with a reason, **never a deletion** |
| 19 | `stage.close` | `stage_close` | A human-readable summary of the proposal; writes nothing. Once per unread set it refuses when a source this session ingested has chunks it never read with `raw.get`, naming them; a second close goes through and the summary lists `unread chunks: …` for the reviewer |

Nine tools propose changes; `stage.close` is the tenth staging verb and is
itself read-only.

**Raw source naming.** A staged source lands at `raw/<kind dir>/<name>.md`,
named from the first non-empty of: its title, its original basename without
the extension, or `untitled`. When that path already holds a **different**
source (the same body is a dedupe error instead), the first free suffixed
path wins — `notes-2.md`, `notes-3.md`, … — and the tool's result says so in
one sentence: `named raw/articles/notes-2.md because raw/articles/notes.md
already holds a different source.`

## Contracts the handlers are held to

- **Errors are content, not aborts.** A validation failure (out-of-taxonomy
  tag, broken link, path outside `wiki/`) returns `IsError: true` with text
  that says what was wrong and how to fix it, and a **nil Go error** — so the
  model can self-correct in the next round. A Go error is reserved for an
  engine failure and aborts the loop.
- **Context discipline lives here.** `wiki.search` caps at 20 hits of title +
  200-rune snippet; `raw.get` returns one chunk; `vault.orient` truncates
  `index.md` at 200 lines. A handler cannot flood the context window.
- **No handler touches the filesystem.** Reads go through the vault handle,
  writes through the staging engine.

## Related

- [architecture.md](architecture.md) — where this boundary sits in the pipeline
- [changesets.md](changesets.md) — the buffer these tools append to

## Turn-level rules the tools do not show

- **Ask and query turns** (`lw query`, the TUI Ask pane) get their own system prompt and only the read tools —
  plus `web.search`, `stage.open`, `stage.ingest_source` and `stage.close` in the TUI when web lookup is configured.
  A call to a tool not offered this turn is refused with the list of offered tools. Saving an answer with ctrl+s runs
  a separate `file` turn with the full curator tool set.
- **Round budget.** When four or fewer rounds remain in a turn, the last tool result of the round ends with
  `[lw: <k> round(s) left in this turn — …]`: stage what you have and close (curator turns) or answer now (ask/query).
- **Staged page text is not re-sent.** Earlier `stage.create_page` bodies and `stage.patch_page` contents over 512
  bytes are replaced on the wire by `[elided: N bytes of page text sent in this call — wiki.get returns the page's
  current staged or committed text]` — only for calls that actually staged. The stage tools refuse that placeholder
  as content.
