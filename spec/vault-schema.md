# Vault schema — the human-readable contract

This is the human-readable counterpart to [`changeset.schema.json`](changeset.schema.json).
Together they are one contract in two languages: this file documents the vault
itself (directory layout, frontmatter, mechanical conventions, lint checks);
the JSON Schema documents the changeset buffer that mutates it. Section
numbers below (`/docs/design.md §N`) cite the design of record; nothing here invents
a rule that isn't traceable to `/docs/design.md` or `01-backbone.md`.

## 1. Directory layout (`/docs/design.md §6`, backbone §1)

```
vault/
├── SCHEMA.md            domain definition, tag taxonomy (10-20 tags), conventions
├── index.md             sectioned catalog, one line per page
├── log.md               append-only human-readable action log (rotates at 500 lines)
├── curator-memory.md    the agent's standing editorial preferences
├── raw/                 IMMUTABLE. Write-once, at ingest (00-conventions.md §5.3).
│   ├── articles/
│   ├── papers/
│   ├── transcripts/
│   └── assets/
├── wiki/
│   ├── entities/
│   ├── concepts/
│   ├── comparisons/
│   ├── queries/
│   └── summaries/        (backbone §2.1 PageType.Dir — SCHEMA.md's fifth type)
└── .llmwiki/             engine state (backbone §14, not part of the reviewable wiki)
```

`wiki/summaries` is not enumerated in `/docs/design.md §6`'s tree but is one of the
five `PageType` values in backbone §2.1 (`TypeSummary`), whose `Dir()` is
`"wiki/summaries"`; it is listed here for completeness.

## 2. Wiki page frontmatter (`/docs/design.md §6`; field order and enums from
backbone §2.1-§2.2)

Every page under `wiki/` opens with a YAML frontmatter block, delimited by
`---` lines, in this canonical field order (`backbone §2.2 Frontmatter`,
`Encode` contract):

| Field | Type | Required | Allowed values / notes |
|---|---|---|---|
| `title` | string | always emitted | non-empty (`Validate` contract) |
| `created` | date (`YYYY-MM-DD`) | always emitted | non-zero; `created <= updated` |
| `updated` | date (`YYYY-MM-DD`) | always emitted | non-zero |
| `type` | string | always emitted | one of `entity`, `concept`, `comparison`, `query`, `summary` (backbone §2.1 `PageType`) |
| `tags` | list of strings | emitted unless empty (then `[]`) | every tag must be present in `SCHEMA.md`'s taxonomy (`/docs/design.md §6`, backbone §2.2 `Validate`) |
| `sources` | list of strings | emitted unless empty | paths into `raw/`, e.g. `raw/papers/leviathan-2023.md` |
| `confidence` | string | emitted unless empty | one of `high`, `medium`, `low` (backbone §2.1 `Confidence`) |
| `contested` | bool | emitted unless false | `true` / `false` |
| *(unknown keys)* | scalar | preserved verbatim in `Extra`, emitted sorted by key | never invented by the tool, only round-tripped |

Lists are flow style (`tags: [inference, decoding]`, empty is `[]`); scalars
are unquoted unless they contain `:#[]{},&*!|>'"%@` or leading/trailing
space, in which case they are double-quoted (backbone §2.2 `Encode`
contract). This is the byte-stability golden that `spec/fixtures/minimal/`
and `spec/fixtures/noncanonical/` (S0-T3) exercise.

## 3. Raw source frontmatter (`/docs/design.md §6`; fields from backbone §2.7 `RawSource`)

Every page under `raw/` opens with a frontmatter block carrying:

| Field | Type | Notes |
|---|---|---|
| `source_url` | string | where the material came from |
| `ingested` | date (`YYYY-MM-DD`) | when `stage.ingest_source` wrote it |
| `sha256` | string | hex sha256 of the body; drives dedupe and drift detection (`/docs/design.md §6`) |

`raw/` is write-once: the only writer is `stage.ingest_source`, and only for a
path that does not yet exist (00-conventions.md §5.3). A raw file's body
sha256 disagreeing with its recorded `sha256` is drift, caught by the
`src-integrity` check (§5 below).

## 4. Mechanical conventions (`/docs/design.md §6`)

Enforced by code, not by prompt:

- **Filenames** are `lowercase-hyphen.md` — no underscores, no mixed case.
- Every wiki page carries **at least 2 outbound `[[wikilinks]]`**.
- Claims synthesized from a source carry a **provenance marker**,
  `^[raw/papers/x.md]`, pointing at the source that backs them.
- A page body **exceeding 200 lines** is flagged as a split candidate
  (`size-split`, §5 below) — see also backbone §4 "Page thresholds".
- Tags outside `SCHEMA.md`'s taxonomy are **rejected at `stage.*` proposal
  time**, not merely discovered later at lint time (`/docs/design.md §6`, backbone
  §5.5 `ValidateOp`).
- The vault is a normal Obsidian vault by construction — no proprietary
  extension to Markdown or YAML.

## 5. The 15 lint checks (backbone §4 `internal/lint`)

Each check is independently testable (`check_<id>.go`); the set is fixed at
15 (`page-abstract` added by 014). `Severity` is `error`, `warn`, or `info`; only `error` blocks `lw commit`
(`/docs/design.md §7`, `/docs/design.md §10`).

Checks 1–14 come from MASTER §9 **D-V**; check 15, `page-abstract`, was
added by 014 (wiki-page structure). Checks 1–11 enforce `/docs/design.md`
§6's mechanical conventions; 12–14 come from the Hermes suite `/docs/design.md` §2
cites. Hermes's *contradictions* check is deliberately absent — it requires
judgement, and `internal/lint` is pure Go (`/docs/design.md` §8); it is deferred to
the v2 roadmap, §8.

| # | ID | Severity | Fires when |
|---|---|---|---|
| 1 | `fm-required` | error | frontmatter is missing, unparsable, or a required field is absent or the wrong type |
| 2 | `fm-taxonomy` | error | a tag is not in `SCHEMA.md`'s taxonomy |
| 3 | `fm-dates` | warn | `created`/`updated` are malformed, or `created > updated` |
| 4 | `path-convention` | warn | the filename is not `lowercase-hyphen.md`, or the directory does not match `type` |
| 5 | `link-broken` | error | a `[[wikilink]]` resolves to nothing |
| 6 | `link-min-out` | warn | the page has fewer than 2 outbound wikilinks |
| 7 | `link-orphan` | warn | the page has no inbound links (exempt: `index.md`, `SCHEMA.md`, `log.md`, `curator-memory.md`, `wiki/queries/**`) |
| 8 | `src-integrity` | error | a `sources:` entry is missing from `raw/`, or its body sha256 differs from the frontmatter `sha256` (drift) |
| 9 | `src-provenance` | warn | a page carrying `sources:` has no `^[raw/…]` provenance marker |
| 10 | `index-sync` | error | `index.md` and the `wiki/` page set are not 1:1 (a missing line, or a line pointing nowhere) |
| 11 | `size-split` | info | the body exceeds 200 lines — split candidate |
| 12 | `fm-quality` | info | `confidence: low`, or `contested: true`, or the page cites exactly one source and sets no `confidence` |
| 13 | `src-stale` | warn | the page's `updated` is more than 90 days earlier than the `ingested` date of a source it cites |
| 14 | `log-rotate` | info | `log.md` exceeds 500 entries and should be rotated to `log-YYYY.md` |
| 15 | `page-abstract` | warn | a `wiki/` page has no `## Abstract` section (014) |

## 6. The changeset buffer (`/docs/design.md §7`; schema in `changeset.schema.json`)

`.llmwiki/changesets/open/<id>/changeset.json` is the only in-flight
representation of a proposed edit; nothing in the codebase writes into the
vault except `stage.Engine.Commit` (00-conventions.md §5.4). Its shape —
`id`, `intent`, `author`, `opened_at`, `ops`, `checks`, and the eight `op`
kinds (`ingest_source`, `create_page`, `patch_page`, `rename_page`,
`merge_pages`, `split_page`, `add_link`, `retract`) — is defined once, in
backbone §5.3, and frozen as JSON Schema in `changeset.schema.json`. See that
file's `$defs.op.oneOf` for the per-kind required fields, derived from
backbone §5.5 (`ValidateOp`).

Two structural notes worth calling out because they are easy to miss reading
the struct alone:

- `Hunk.Add` serializes as `"+"` and `Hunk.Del` as `"-"`; `Hunk.Before` is
  `json:"-"` and is **never serialized** — it exists only for in-memory
  display and has no property in the schema.
- `Op.Kind` serializes as `"op"`, not `"kind"` — the schema pins this per
  variant with a JSON Schema `const`.
