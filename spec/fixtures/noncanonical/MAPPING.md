# Mapping — noncanonical to canonical

Each file below is hand-typed, non-canonical YAML frontmatter around the
*exact same body text* as its twin in `minimal/`. Running `vault.Canonical()`
on the file in this column must produce the byte-identical file in the next
column.

| noncanonical file | canonical twin |
|---|---|
| `wiki/concepts/speculative-decoding.md` | `../minimal/wiki/concepts/speculative-decoding.md` |
| `wiki/concepts/kv-cache.md` | `../minimal/wiki/concepts/kv-cache.md` |
| `wiki/concepts/flash-attention.md` | `../minimal/wiki/concepts/flash-attention.md` |
| `wiki/entities/gpt-4.md` | `../minimal/wiki/entities/gpt-4.md` |

## What differs, file by file, and why `Canonical()` erases it

- **speculative-decoding.md** — keys reordered (`type` first, `updated`
  before `created`), `type` needlessly double-quoted, `tags`/`sources` in
  YAML block-sequence style instead of flow style.
- **kv-cache.md** — `confidence` needlessly single-line-quoted with trailing
  whitespace after the value, three blank lines after the closing `---`
  instead of one. Body also carries a fenced code block containing a
  `##`-looking line (backbone §2.4: not a real heading) and survives
  unchanged because `Canonical()` never touches `Body`.
- **flash-attention.md** — `tags` in block-sequence style, and an explicit
  `contested: false`. `false` is the zero value for `Contested`, which
  `Encode()` omits (backbone §2.2), so the line disappears in the twin.
- **gpt-4.md** — keys reordered (`type` before `title`), `type` and `title`
  needlessly double-quoted even though neither needs it, `sources` in
  block-sequence style. Body carries a `[[GPT-4]]` mixed-case wikilink
  (resolved case-insensitively per backbone §2.9) and survives unchanged for
  the same reason as above — `Canonical()` never rewrites link casing in the
  body, only frontmatter.
