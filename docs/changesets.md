# Changesets, the journal, and recovery

The changeset is the reason this project exists: a git-commit-shaped buffer of
proposed operations that a human reviews before any of it reaches `wiki/`.

## Lifecycle

```
stage.open / lw ingest
        │
        ▼
.llmwiki/changesets/open/cs-…/        ← the buffer; exactly one may be open
        │
        ├── review drops hunks ──► op rewritten, checks recomputed
        │
        ├── lw commit ───────────► changesets/committed/cs-…/   + snapshot
        │
        └── review rejects ──────► changesets/rejected/cs-…/    (permanent)
```

One open changeset at a time. Rejection is a move, not a deletion — the log
answers "what did the agent try to do, and what did I reject?", not just "what
got in".

## Layout

```
.llmwiki/
├── config.toml                       vault-local settings (never secrets) *
├── objects/ab/cdef…                  zlib blobs, content-addressed by sha256
├── changesets/
│   ├── open/cs-0193f2a/
│   │   ├── changeset.json            THE BUFFER
│   │   └── session.ndjson            the conversation that produced it
│   ├── committed/cs-0193f2a/
│   └── rejected/cs-0193f2a/
├── journal.ndjson                    append-only; every event, pre- and post-approval
├── snapshots/000042.tree             path → sha manifest after each commit
├── tombstones/000042/wiki/…          renamed/merged sources, moved here — never deleted
├── index.gob                         word-index cache, rebuildable
└── lock                              single-writer guard (exists only while a
                                      commit holds it; `lw doctor --unlock`
                                      removes a stale one)
```

\* in the frozen layout (`01-backbone.md` §14) but no v0.1 code writes it; the
only configuration this build reads is `~/.config/lw/config.toml`.

`objects/` and `journal.ndjson` are the durable state. `snapshots/` and
`index.gob` are derived: delete `index.gob` and it rebuilds from `objects/` and
the journal.

## `changeset.json`

Written with `json.MarshalIndent` and a trailing newline, and validated against
[`spec/changeset.schema.json`](../spec/changeset.schema.json) — **unconditionally,
from the moment the changeset is opened**, not just at commit.

```jsonc
{
  "id": "cs-0193f2a",
  "intent": "Ingest 3 papers on speculative decoding",
  "author": { "kind": "agent", "model": "deepseek-v4-flash", "session": "se-8821" },
  "opened_at": "2026-09-09T14:02:11Z",
  "ops": [ /* … */ ],
  "checks": { "schema": "pass", "lint": "pass", "orphans": 0, "broken_links": 0 }
}
```

Eight op kinds: `ingest_source`, `create_page`, `patch_page`, `rename_page`,
`merge_pages`, `split_page`, `add_link`, `retract`. Each carries its rationale
and its provenance back into `raw/`; a `patch_page` carries `before`/`after`
hashes and per-hunk detail, and a `rename_page` carries the full backlink
cascade so every rewrite is reviewed on its own.

Ops move through states: `proposed` → `accepted` | `dropped` | `rejected`, and
go `stale` when the file they were proposed against has changed on disk since
(`before` hash mismatch) — the review screen shows both versions and offers
rebase or drop.

## `session.ndjson` travels with the changeset

One session per changeset, and the transcript lives **inside the changeset
directory**. Commit or reject moves the directory and the session moves with it,
so "why did it propose this?" is answerable months later with no external
store. `lw log --agent` filters the journal to agent-authored events;
`lw revert` opens a new reviewable changeset holding the inverse ops.

## The journal

`journal.ndjson` is append-only, one compact JSON object per line,
`\n`-terminated, `fsync`ed after each append. Event kinds:

| Event | Meaning |
|---|---|
| `changeset_opened` | a buffer was opened, with intent and author |
| `op_proposed` | a tool call appended an op — **before** anyone approves it |
| `op_accepted` / `op_dropped` | the reviewer took or dropped an op |
| `hunk_dropped` / `hunk_undropped` | hunk-level review decisions |
| `commit_begin` | an apply started |
| `commit_end` | that apply finished |
| `changeset_rejected` | the buffer was moved to `rejected/` |
| `reverted` | a revert changeset was committed |

Pre-approval records are the point: history is written when the proposal is
*made*, not only when it lands.

## Recovery — and the honest boundary

Each target file is written with its own temp-file → `fsync` → rename. POSIX
offers no way to make a dozen renames one transaction, and lw does not pretend
otherwise:

> **Per-file atomic, not cross-file atomic.** A crash between two of those
> renames can leave some paths at their post-commit content and others still at
> their pre-commit content.

What lw does about it is make the window **inspectable and repairable** rather
than pretend it cannot happen:

- `commit_begin` / `commit_end` bracket every apply, so an interrupted one is
  visible in the journal.
- `Engine.Recover` reads that pair and reports, without writing anything: the
  commit id, the **Applied** paths (on-disk sha already equals the post-image)
  vs the **Pending** ones, whether it is **Fixable** — true when the CAS still
  holds every pending path's post-image, which is the normal case — and any
  changeset whose commit completed but which never left `open/`.
- **Roll-forward from `objects/`:** because every post-image is already in the
  content-addressed store, an interrupted commit is finished from the store, not
  undone by hand. `lw doctor` reports the applied/pending split and offers the
  roll-forward.
- `lw doctor --unlock` clears a stale single-writer lock; `--rebuild-index`
  rebuilds the disposable cache.

**`lw doctor` diagnoses. The engine repairs.** Nothing in `internal/stage`
deletes a changeset: rejections are permanent history, and tombstones under
`tombstones/<commit>/` are the durable answer to "this file did not vanish, it
moved".
