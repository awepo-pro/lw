# Fixture vaults

Golden data for `internal/vault` and `internal/lint` (Stage 1). Nothing here
is Go code; this is the ground truth those packages are built and tested
against (backbone `01-backbone.md` §2, §4).

## `minimal/`

A small, canonical, lint-clean vault on the domain "ml-systems"
(`/.dev-notes/PLAN-v1.md` §6's running example). Every wiki page's frontmatter is already
in the exact byte form `vault.Frontmatter.Encode()` must produce (backbone
§2.2): known keys in struct order, zero-value optional fields omitted, lists
in flow style, minimal quoting. This is the byte-stability golden behind
`Serialize(Parse(b)) == b`.

Consumed by: **S1-T1** (`internal/vault` parse + serialize property test),
and downstream by **S1-T5** (`internal/lint`) as the "a clean vault reports
zero findings" case.

## `noncanonical/`

The same four wiki pages as `minimal/`, hand-typed the way a human actually
writes YAML frontmatter: block-style tag/source lists, needlessly quoted
scalars, keys in a different order, extra blank lines after the closing
`---`, trailing whitespace on a frontmatter line, a mixed-case `[[Link]]`,
and a `##`-looking line inside a fenced code block. `MAPPING.md` pairs each
file with its canonical twin in `minimal/` and explains, file by file, why
each difference is one `Canonical()` actually erases.

Consumed by: **S1-T1** — `Canonical()` on every file here must equal its twin
in `minimal/`, byte for byte.

## `dirty/`

Deliberately broken on every one of the 14 `internal/lint` checks (backbone
§4, MASTER §9 D-V). `EXPECTED-LINT.md` is the exact, ordered golden the checks
must reproduce — 16 findings. Exactly one page,
`wiki/concepts/malformed.md`, is parse-broken on purpose (it never closes its frontmatter block, tripping `fm-required`); the
vault still loads (backbone §2.8: a page that fails to parse is kept in a
parse-error list, not fatal) so the other thirteen checks have something to run
against.

Consumed by: **S1-T5** (`internal/lint`), and indirectly by **S1-T1**–**T4**
as a "realistic, mostly-valid vault" fixture wherever one is needed.

## `pages/`

Ten single-page round-trip cases, each an `<name>.in.md` / `<name>.want.md`
pair: `Canonical("wiki/concepts/<name>.md", in.md)` must equal `want.md`,
byte for byte. Covers edge cases `minimal/`'s real pages never hit: an empty
body, a body with no trailing newline, a unicode title, a title needing
quoting, an explicitly empty `tags: []`, unknown extra frontmatter keys
(sorted on output), a nested list, a table, a fenced code block containing a
`---` line, and a wikilink with an alias.

Consumed by: **S1-T1**.
