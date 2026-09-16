# llmwiki — `lw`

`lw` compiles a directory of **immutable sources** into an interlinked markdown
wiki. An agent reads the sources and proposes pages, patches and renames into a
git-commit-shaped **changeset**; you review the projected diff hunk by hunk and
commit it, or reject it — and the rejection is permanent history too. Two rules
make it different from every agent-with-a-shell tool:

1. **The agent gets no filesystem.** Its tool registry contains no `write`, no
   `edit`, no `delete`, no `bash` — not denied, *not offered*, so there is
   nothing to misconfigure. Its only mutation path is a set of graph-aware,
   validating, transactional tools that propose changes into a buffer. A `mv`
   cannot know that renaming a page orphans twelve backlinks; `stage.rename_page`
   computes all twelve and shows them to you before anything touches disk.
2. **Nothing lands without hunk-level human review.** Every proposal is
   journalled when it is *made*, carrying the agent's rationale and its
   provenance back into `raw/`, so the log answers "what did the agent try, and
   what did I reject?" — not just "what got in".

Everything deterministic — parsing, indexing, search, lint, diff, apply,
revert — is Go. Only judgement goes to the model. [`docs/design.md`](docs/design.md) is the
design of record; [`docs/architecture.md`](docs/architecture.md) is the map.

## Install

A single static binary (`CGO_ENABLED=0`), built for `linux` and `darwin` on
`amd64` and `arm64`. Nothing else to install: no runtime, no database, no
Python.

**Release archive** — grab the tarball for your platform from the
[releases page](https://github.com/awepo-pro/lw/releases), unpack it, and put
`lw` on your `PATH`. `checksums.txt` holds the sha256 of every artifact.

**Homebrew** — the tap is populated by a release:

```bash
brew install awepo-pro/tap/lw
```

**Nix**:

```bash
nix profile install github:awepo-pro/lw        # from GitHub
nix build .#lw                                 # from a checkout of this repo
```

**Go** (Go 1.25.8 or newer):

```bash
go install github.com/awepo-pro/lw/cmd/lw@latest
```

**From source:**

```bash
git clone https://github.com/awepo-pro/lw && cd lw
make build          # → ./lw
```

Check it works: `lw --version` prints `lw <version>`.

## Quickstart

Start in an empty directory. `lw` finds the vault by walking up to the nearest
ancestor containing `SCHEMA.md`, so run the verbs from inside it.

```bash
mkdir ml-wiki && cd ml-wiki

# 1. Scaffold the vault: SCHEMA.md, index.md, log.md, curator-memory.md,
#    raw/{articles,papers,transcripts,assets}/, wiki/, .llmwiki/
lw init --schema "ml-systems"

# 2. Provider settings. lw defaults to DeepSeek; the key is a reference, never
#    a value — see "Secrets" below.
lw config                                    # print the resolved config
lw config set llm.base_url https://api.deepseek.com/v1
lw config set llm.model deepseek-v4-flash
export DEEPSEEK_API_KEY=sk-...               # config points at env:DEEPSEEK_API_KEY

# 3. Ingest a source. This is the only step that needs a provider: it extracts
#    the source to deterministic markdown and runs the agent to propose pages.
lw ingest https://example.com/some-post

# 4. Review. Nothing has touched wiki/ yet.
lw diff                                      # full unified diff
lw diff --stat                               # or just the per-file summary
lw diff --op op3                             # or one operation's files
lw diff --render                             # each changed page as it will read after commit

# 5. Commit — refuses if lint regresses; --force overrides and says so.
lw commit -m "first pages from example.com"

# 6. Health, history, and the same loop without the TUI.
lw doctor                                    # index, objects, journal, provider
lw lint && lw log --limit 20 && lw status
```

`lw` with no command opens the TUI: browse, review, ask, lint, log. Typing a
question on the Ask screen opens a changeset for you if none is open, and
discards it automatically if the turn stages nothing. See
[docs/tutorial.md](docs/tutorial.md) for the full walkthrough, including the
TUI.

`lw ingest`, `lw query` and `lw lint --fix` need a configured provider with a
resolvable API key. **Every other verb works offline with no LLM at all** —
`lw diff`, `lw commit`, `lw log`, `lw revert`, `lw lint`, `lw status`,
`lw doctor` and `lw mcp` are pure Go over the vault.

## Command reference

```
usage: lw <command> [arguments]

commands:
  init [--schema <domain>]     initialize a new vault
  config                       view or edit configuration
  ingest <url|path>...         ingest one or more sources
  status                       show the open changeset, if any
  diff [--op <id>]             show the projected diff of the open changeset
  commit -m "..." [--force]    commit the open changeset
  log [--rejected] [--agent]   show changeset history
  revert <commit-id>           open a reverse changeset for review
  query "..."                  ask the curator agent a question
  lint [--fix]                 run the lint checks
  mcp                          run the MCP server over stdio
  doctor [--unlock] [--rebuild-index] [--discard-changeset] [--json]
                               check vault and lock health
  tui                          launch the terminal UI (default with no command)

flags:
  --version    print the version and exit
  --help, -h   print this message and exit
```

Every verb that operates on an existing vault takes `-vault <dir>` to point at
one explicitly (`init` is the exception — it creates the vault in your working
directory), and every flag spells with one dash or two (`-json` and `--json`
are the same flag). Exit codes: `0` success, `1` failure, `2` usage error.

| Verb | Flags | Notes |
|---|---|---|
| `init` | `--schema <domain>`, `--force` | Scaffolds the full vault layout. Without `--schema` it asks: domain, tags, page-type conventions. Refuses a non-empty directory unless `--force`. The result passes `lw lint` with zero errors |
| `config` | `set <key> <value>`, `--probe` | Prints the resolved config; `set` writes `~/.config/lw/config.toml`; `--probe` checks the provider is reachable and that tool calling actually works. The API key is printed only as `env:NAME (set)` / `(missing)` |
| `ingest` | `-kind article\|paper\|transcript` | One or more URLs or local paths. Extracts every source *before* opening a changeset, so a bad URL fails the whole command with nothing created |
| `status` | — | The open changeset, if any |
| `diff` | `-op <id>`, `-stat`, `-render` | The projected diff of the buffer — what commit would write. `lw diff --render [--op <id>]` renders each changed page as it will read after commit, as plain text when stdout is not a terminal; it cannot be combined with `--stat` |
| `commit` | `-m <msg>` (required), `-force` | Re-hashes the tree first; an op whose `before` hash no longer matches is stale and the commit is refused. Refuses a lint regression unless `--force`, which is journalled |
| `log` | `-rejected`, `-agent`, `-limit <n>`, `-page <path>`, `-since <rfc3339\|YYYY-MM-DD>` | The journal, including what you rejected |
| `revert` | (positional) `<commit-id>` | Opens the inverse ops as a *new* changeset — a rollback is reviewed like anything else |
| `query` | (positional) `"…"` | One-shot answer with citations; uses an ephemeral in-process session and never touches a changeset |
| `lint` | `-checks <id,id,…>`, `-fix`, `-json` | 14 checks ([docs/vault-schema.md](docs/vault-schema.md#the-14-lint-checks)). Exits 1 only on errors. `--fix` asks the agent to propose repairs — as a changeset |
| `mcp` | — | stdio MCP server; see below |
| `doctor` | `--unlock`, `--rebuild-index`, `--discard-changeset`, `--json` | Index freshness, object-store completeness, journal tail, interrupted apply, stale lock, config, provider. Every failure prints the fix. `--discard-changeset` moves the open changeset to `changesets/rejected/`, journalled — the CLI way to discard one without opening the TUI. Exit 1 on any failure |
| `tui` | — | The TUI; also the default with no command |

## The TUI

`lw` with no command — or `lw tui` — opens the terminal UI. Five screens
cycle with `tab`, always in this order:

```
Review → Ask → Lint → Log → Browse   (wrapping back to Review)
```

Every screen draws inside the same frame. The header row names the vault,
the five screens, and — on the right — the vault counts plus the open
changeset (`cs-1c86cb · 1 op · checks ✓`, or `no changeset`). The footer
row always shows what the current screen's keys actually do, ending with
`? help` — `?` toggles a help overlay listing every key on the active
screen. The UI needs a terminal of at least **80×24**; anything smaller
shows a "Terminal too small" notice until the window is enlarged, and `q`
still quits.

| Key | Screen | Action |
|---|---|---|
| `tab` | all | next screen |
| `?` | all | toggle the keys overlay |
| `j`/`k`, `g`/`G` | list screens | move the cursor; jump to top/bottom |
| `pgup`/`pgdn`, `ctrl+u`/`ctrl+d`, `home`/`end` | Review, Browse, Ask | scroll the content panel — Review's detail, Browse's preview, Ask's transcript — a page, half a page, or to its top / bottom |
| mouse wheel | Review, Browse, Ask | scroll the panel under the pointer; over Review's Ops list or Browse's Pages tree it moves the cursor |
| `y` / `n` | Review | accept (undrop) / drop the selected hunk and advance |
| `p` | Review | toggle the detail panel between the diff and a preview of the page as it will read after commit |
| `A` | Review | accept every remaining hunk; refused unless lint is clean |
| `X` | Review | reject the whole changeset |
| `C` | Review | commit, subject to the same lint-regression rule as `lw commit` |
| `enter` | Ask | send the message |
| `↑`/`↓` | Ask | select a tool call in the transcript (`enter` expands it) |
| `ctrl+r` | Ask | jump to Review |
| `enter` | Lint | open the selected finding's page in Browse |
| `enter`, `/` | Browse | open a page; find one by name |
| `h`/`l` | Browse | collapse / expand a subtree |
| `f` | Log | cycle the journal filter (all → accepted → rejected → agent → human) |
| `r` | Log | revert the selected commit into a new changeset |
| `q` | all screens, except while typing in Ask or in Browse's `/` finder | quit (`ctrl+c` also quits) |

Ask's message box takes all typing: `q` and `?` type into the message
instead of quitting or opening the overlay — quit from Ask with `ctrl+c`.
Keys rebind from `~/.config/lw/hotkeys.toml`; see
[docs/hotkeys.md](docs/hotkeys.md) for the table and the file format.

### Themes

lw ships one theme, and it is adaptive: the UI reads the terminal's
light/dark background and picks the matching half of the palette. Setting
`theme = "dark"` or `theme = "light"` in `config.toml` forces a polarity.

The named themes of lw 1 are retired — `nord` is no longer available, and
neither is any other name lw 2 does not find a file for: selecting one falls
back to the default theme with a notice.

To recolour, drop a TOML file in the config directory (`~/.config/lw/`):
`themes/<name>.toml` defines a theme that `theme = "<name>"` selects, and
`theme.toml` overrides single colours on whatever theme is selected — it is
applied last, as the per-machine word. Both files are partial: a key you
omit keeps the value it would otherwise have, and every colour comes in a
light and a dark spelling — `fg_light`/`fg_dark`, `muted_*`, `faint_*`,
`border_*`, `accent_*`, `cursor_*`, `good_*`, `warn_*`, `bad_*`,
`heading_*`, `code_*` — as `#RRGGBB` values. The last two colour the
rendered markdown's headings and code, in Review's and Browse's previews
and in `lw diff --render` (a level-2 heading keeps the accent colour).
The pre-2 `foreground_*` spelling still sets `fg_*`, and
`background_*` is accepted but ignored: lw draws only foregrounds and uses
the terminal's own background.

One colour note: lw draws its exact `#RRGGBB` palette only on a truecolor
terminal. Inside tmux, tmux has to advertise that — e.g.
`set -as terminal-features ',*:RGB'` in `tmux.conf` — or the terminal
reports 256 colours, foregrounds come out approximated, and the cursor
highlight falls back to a neutral grey (the `cursor_*` keys colour the
cursor only at truecolor). On a terminal below 256 colours lw drops the
cursor background entirely and marks the cursor row with the accent bar
alone.

## MCP setup

```bash
lw mcp            # in a vault; serves the same 17 tools over stdio
```

`lw mcp` needs no API key and no provider — the tools are vault operations, and
the connecting client brings its own model. Dots become underscores on the wire
(`wiki.search` → `wiki_search`); the mapping is bijective and nothing is added
or removed in transit, so an MCP client sees exactly the surface described in
[docs/tools.md](docs/tools.md). Point any stdio MCP client at it:

```json
{
  "mcpServers": {
    "lw": { "command": "lw", "args": ["mcp"] }
  }
}
```

One honest note: over MCP, `stage_ingest_source` reports `no extractor
configured` — the extractor is wired by the CLI's `ingest` verb, not by the
transport. Reads, patches, renames, merges, splits, links and retracts all work.

## Secrets

`lw config` never stores or prints an API key. The config file holds a
*reference* — `env:DEEPSEEK_API_KEY` (or `keyring:NAME`) — and `lw` resolves it
at the moment it needs it. `lw config` shows `env:DEEPSEEK_API_KEY (set)` or
`(missing)`; the value itself never appears in a command's output, in the
config file, or anywhere in the vault. A literal key is rejected with a
warning pointing at `env:`.

## Documentation

| | |
|---|---|
| [docs/tutorial.md](docs/tutorial.md) | Start here — a hands-on walkthrough from install to a reviewed wiki |
| [docs/architecture.md](docs/architecture.md) | The pipeline, the agent's verb boundary, the package map |
| [docs/vault-schema.md](docs/vault-schema.md) | The vault layout, frontmatter, and the 14 lint checks |
| [docs/tools.md](docs/tools.md) | The 17 tools and what each one reads |
| [docs/changesets.md](docs/changesets.md) | Changeset layout, the journal, and recovery |
| [spec/vault-schema.md](spec/vault-schema.md) | The normative vault schema |
| [spec/changeset.schema.json](spec/changeset.schema.json) | The JSON Schema every changeset validates against |
| [NOTICE](NOTICE) | Third-party dependencies and licences |
| [LICENSE](LICENSE) | MIT |

## Limitations

Stated plainly, because a tool asking for this much trust should not oversell.

- **A commit is per-file atomic, not cross-file atomic.** Each target file is
  written with its own temp file → `fsync` → rename, which is atomic per file.
  POSIX offers no way to make a dozen renames one transaction, so a crash in
  the middle of an apply can leave some paths written and others not. lw does
  not claim otherwise: `commit_begin`/`commit_end` bracket every apply,
  `lw doctor` detects an interrupted one and prints the applied/pending split,
  and because every post-image is already in the content-addressed store the
  commit is **rolled forward** from `objects/` rather than repaired by hand.
  See [docs/changesets.md](docs/changesets.md#recovery--and-the-honest-boundary).
- **Dependencies.** lw depends on **14 direct Go modules** (39 including
  transitive ones); [NOTICE](NOTICE) names every one with its licence. Two
  of the fourteen became direct with the colour work rather than being
  added: `github.com/alecthomas/chroma/v2`, which syntax-highlights code in
  rendered markdown, and `github.com/charmbracelet/colorprofile`, which
  resolves the terminal's colour profile. Both already arrived through
  glamour and Bubble Tea, so the module set is unchanged.
- **The MCP Go SDK is young.** `github.com/modelcontextprotocol/go-sdk` is
  pinned at `v1.7.0`; its API is still settling upstream, and lw tracks it as
  an ordinary module rather than shipping a fork.
- **Search is a word index.** No SQLite, no FTS5, no embeddings, no vector
  store — an in-memory inverted index, which is instant at vault scale and is
  all v1 promises.
- **Extraction is HTML and markdown.** No PDF, no OCR, no transcripts from
  audio. Sources arrive as files or web pages.
- **One vault, one open changeset.** No multi-vault, no parallel ingest.
- **`stage.ingest_source` takes local paths only**, and only writes a `raw/`
  path that does not already exist — `raw/` is immutable.
- No graph view, no plugin system, no web UI. `lw` is a terminal tool.

## Development

```bash
make check              # gofmt -l . && go vet ./... && go test ./...
make build              # ./lw
make test               # go test ./...
make release-snapshot   # goreleaser build --snapshot --clean → dist/
make clean
```

`make release-snapshot` compiles the release build into `dist/` and nothing
else. It never tags, pushes, uploads or commits the tap — releasing is done by
hand by the maintainer, and no automation in this repository publishes
anything. `docs/` and `spec/` hold the rest of the documentation; `make
fixtures` regenerates test goldens.

## Licence

MIT — see [LICENSE](LICENSE). [NOTICE](NOTICE) credits the design inspiration
and lists every third-party module with its licence.
