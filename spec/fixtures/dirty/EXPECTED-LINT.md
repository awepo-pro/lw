# Expected lint findings — `dirty/`

Golden for **S1-T5** (`internal/lint`). `lint.Run` over this vault must
produce exactly these 13 findings (sorted by `Path`, then `Line`, then
`Check`, matching `lint.Report.Findings`'s ordering contract). Every one of
the 11 checks in backbone §4 fires at least once. Severities match backbone
§4's table exactly.

| check | path | line | severity | why |
|---|---|---|---|---|
| index-sync | index.md | 0 | error | wiki/concepts/thin-links.md has no line in index.md; add one |
| index-sync | index.md | 14 | error | entry [[nonexistent-catalog-entry]] points to a page that does not exist; remove it or create the page |
| src-integrity | raw/papers/drifted-source.md | 0 | error | body sha256 does not match frontmatter sha256; re-ingest to refresh the hash |
| path-convention | wiki/concepts/KV_Cache.md | 0 | warn | filename is not lowercase-hyphen.md; rename to kv-cache.md |
| fm-dates | wiki/concepts/backwards-dates.md | 0 | warn | created 2026-09-05 is after updated 2026-08-01; fix one of the dates |
| link-broken | wiki/concepts/broken-target.md | 7 | error | [[nonexistent-target]] resolves to nothing; fix the target or create the page |
| size-split | wiki/concepts/long-page.md | 0 | info | body exceeds 200 lines; consider splitting into smaller pages |
| fm-required | wiki/concepts/malformed.md | 0 | error | frontmatter block never closes; add the closing --- delimiter or fix the YAML |
| src-integrity | wiki/concepts/missing-raw.md | 0 | error | sources entry raw/papers/nonexistent-source.md not found under raw/; ingest it or drop the citation |
| src-provenance | wiki/concepts/no-provenance.md | 0 | warn | cites raw/papers/valid-source.md but has no ^[raw/papers/valid-source.md] marker; add one or drop the source |
| link-orphan | wiki/concepts/orphan-page.md | 0 | warn | no inbound links; link to it from a related page or retract it |
| fm-taxonomy | wiki/concepts/rogue-tag.md | 0 | error | tag `nonexistent-tag` is not in SCHEMA.md; add it to the taxonomy or retag |
| link-min-out | wiki/concepts/thin-links.md | 0 | warn | only 1 outbound wikilink; add at least one more |

## Notes on `Path`/`Line` attribution (judgment calls — see report.md)

- Frontmatter-derived checks (`fm-required`, `fm-taxonomy`, `fm-dates`,
  `path-convention`, `src-integrity` on the citing side, `src-provenance`,
  `link-min-out`, `link-orphan`, `size-split`, `index-sync`) have no
  byte-offset to point at, so `Line` is `0` per backbone §4's `Finding.Line`
  contract ("0 when not line-specific").
- `link-broken` is the one check with a real line number: `Wikilink.Line` is
  1-based within `Page.Body` (backbone §2.5). `broken-target.md`'s body
  starts at `# Broken Target Page`; the broken link is on body line 7.
- `src-integrity`'s "drift" half is attributed to the raw source file itself
  (`raw/papers/drifted-source.md`), since the defect — a body that no longer
  matches its own frontmatter hash — is a property of that file, independent
  of which page cites it. The "missing" half is attributed to the citing
  page (`missing-raw.md`), since the defect is that page's dangling
  `sources:` entry.
- `index-sync`'s two defects get two rows on the same `index.md` path: the
  missing-entry case has no real location (`Line: 0`); the
  points-nowhere case does (`Line: 14`, the actual line of the phantom
  wikilink in `index.md`).
