# The vault schema

**The contract of record is [`../spec/vault-schema.md`](../spec/vault-schema.md).**
It is the human-readable half of a two-file contract, and this page deliberately
does not restate it — a schema documented twice is a schema that drifts.

| File | What it pins |
|---|---|
| [`spec/vault-schema.md`](../spec/vault-schema.md) | Directory layout, frontmatter field order and enums, mechanical conventions, the lint checks |
| [`spec/changeset.schema.json`](../spec/changeset.schema.json) | The JSON Schema every `changeset.json` validates against, unconditionally, from the moment it is written |

Both are enforced by code, not by prompt. This page is the short tour.

## Layout

```
vault/
├── SCHEMA.md            domain definition, tag taxonomy (10–20 tags), conventions
├── index.md             sectioned catalog, one line per page
├── log.md               append-only human-readable log (rotates at 500 entries)
├── curator-memory.md    the agent's standing editorial preferences — a normal page
├── raw/                 IMMUTABLE: articles/  papers/  transcripts/  assets/
├── wiki/                entities/  concepts/  comparisons/  queries/  summaries/
└── .llmwiki/            engine state — not part of the reviewable wiki
```

It is a plain Obsidian-compatible markdown vault. `lw` is a compiler over it,
not a container for it: delete `.llmwiki/` and you have lost the engine's
journal and cache, not your notes.

## A page

```yaml
---
title: Speculative Decoding
created: 2026-09-09
updated: 2026-09-09
type: concept                 # entity | concept | comparison | query | summary
tags: [inference, decoding]   # every tag must exist in SCHEMA.md's taxonomy
sources: [raw/papers/leviathan-2023.md]
confidence: high              # high | medium | low
contested: false
---
```

Frontmatter is **parsed with `yaml.v3` and written by lw's own serializer**,
which is byte-stable: `Serialize(Parse(b)) == b` is a property test over every
fixture. Nothing in the build re-emits your frontmatter in a house style, so a
round-trip through the engine shows up as an empty diff.

Raw sources carry `source_url`, `ingested` and the `sha256` of the body — the
hash that drives drift detection and dedupe at ingest.

## Conventions the engine enforces

Enforced at `stage.*` time, where the agent can self-correct — not discovered
later at lint time:

- lowercase-hyphen, slash-separated, vault-relative paths
- tags outside `SCHEMA.md`'s taxonomy are rejected
- ≥ 2 outbound `[[wikilinks]]` per page
- provenance markers back to `raw/` on synthesized claims
- a `rename`/`merge` computes and stages every inbound backlink rewrite itself

## The 14 lint checks

`lw lint [--checks <ids>]` runs all of them; the model can only read what the
engine reports. Severity is three-level — **error**, **warn**, **info** — and
`lw lint` exits non-zero only on errors: warnings and infos are printed and the
command still exits 0. A vault with no findings prints `clean`.

| ID | Sev | Fails when |
|---|---|---|
| `fm-required` | error | frontmatter missing, unparsable, or a required field absent / wrong type |
| `fm-taxonomy` | error | a tag is not in `SCHEMA.md`'s taxonomy |
| `index-sync` | error | `index.md` and the `wiki/` page set are not 1:1 |
| `link-broken` | error | a `[[wikilink]]` resolves to nothing |
| `src-integrity` | error | a `sources:` entry is missing from `raw/`, or its body `sha256` differs from the frontmatter hash (drift) |
| `fm-dates` | warn | `created`/`updated` malformed, or `created > updated` |
| `link-min-out` | warn | fewer than 2 outbound wikilinks |
| `link-orphan` | warn | no inbound links |
| `path-convention` | warn | filename is not `lowercase-hyphen.md`, or the directory does not match `type` |
| `src-provenance` | warn | a page with `sources:` carries no `^[raw/...]` provenance marker |
| `src-stale` | warn | a page's `updated` is more than 90 days earlier than a cited source's ingested date |
| `fm-quality` | info | `confidence: low`, `contested: true`, or a single source with no confidence set |
| `log-rotate` | info | `log.md` exceeds 500 entries |
| `size-split` | info | body exceeds 200 lines — split candidate |

`lw lint --json` emits the report as indented JSON; `lw lint --fix` hands the
findings to the agent — and its repairs arrive as a **changeset**, so even a
lint fix goes through review.

## Deeper reading

- [changesets.md](changesets.md) — the buffer that mutates this tree, its
  journal, and what happens when a commit is interrupted
- [architecture.md](architecture.md) — the pipeline and the agent's verb boundary
