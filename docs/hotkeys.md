# Hotkeys

`lw`'s key bindings live in one keymap (`internal/ui/keys.go`) and can be
rebound without recompiling, from a single TOML file. This document lists
every rebindable action, its default, and the keys that are deliberately not
rebindable.

## The file

`<config dir>/hotkeys.toml`, where `<config dir>` is `$XDG_CONFIG_HOME/lw`,
or `~/.config/lw` when that variable is unset. It is read once, when the TUI
starts (`ui.LoadKeys`); changes take effect on the next `lw tui`.

The format is flat: one action per line, mapped to a list of keystrokes.

```toml
# A binding you do not name keeps its default.
quit = ["Q", "ctrl+c"]

# A binding set to an empty list is unbound: pressing it does nothing.
bottom = []
```

- A **missing** action keeps its default.
- An **empty list** (`action = []`) unbinds that action.
- An **unknown** action name is reported as a warning and ignored; the rest
  of the file still applies. `lw` never refuses to start over a typo in
  `hotkeys.toml`.
- A file that does not parse at all is an error: `lw tui` exits with
  `lw: tui: load keys: ui: parse <path>: …`.

Keystrokes are written the way Bubble Tea names them: a single printable
character (`a`, `A`, `?`), or a named key — `enter`, `esc`, `tab`, `up`,
`down`, `left`, `right`, `backspace`, or a modifier form such as `ctrl+c`,
`ctrl+r`, `shift+tab`. Matching is case-sensitive: `a` and `A` are different
keys.

## Rebindable actions

These are the thirteen actions in the keymap. Defaults are frozen; the
`toml` key is the only way to change one.

| TOML key | Default | Action | Screens |
|---|---|---|---|
| `accept_hunk` | `y` | Accept (undrop) the selected hunk and advance | Review |
| `drop_hunk` | `n` | Drop the selected hunk and advance | Review |
| `split_hunk` | `s` | Split the selected hunk — not implemented in v0.1; Review reports that it lands in v1.0 | Review |
| `accept_all` | `A` | Accept every hunk; refused unless lint is clean | Review |
| `reject_changeset` | `X` | Reject the whole changeset | Review |
| `commit` | `C` | Commit the changeset; refused if lint regresses | Review |
| `move_down` | `j`, `down` | Move the cursor / selection down | Browse, Review, Lint, Log |
| `move_up` | `k`, `up` | Move the cursor / selection up | Browse, Review, Lint, Log |
| `top` | `g` | Jump to the first entry | Browse, Review, Lint, Log |
| `bottom` | `G` | Jump to the last entry | Browse, Review, Lint, Log |
| `next_pane` | `tab` | Cycle to the next screen | Shell |
| `quit` | `q`, `ctrl+c` | Quit `lw` | Shell |
| `help` | `?` | Help — declared and rebindable, but v0.1 renders no help overlay, so no screen consumes it yet | — |

Review's `y`/`n`/`s`/`A`/`X`/`C` letters are the review surface `/PLAN.md`
§9 fixes, and they are also the defaults above. The keymap is the source of
truth: rebinding one of them changes what Review matches, so rebind them
only if you are also prepared to relearn what the documentation says.

### Match order inside a screen

A screen tests its bindings in the order it lists them, and the first match
wins. Navigation is matched first on every screen that has it, so a movement
key rebound onto an action's key **shadows** that action on that screen:

```toml
# The stage file's worked example: vim-key swap onto n/p.
move_down = ["n"]
move_up   = ["p"]
```

On Review this is a real collision: `n` is also `drop_hunk`, and `move_down`
is matched first, so `n` moves the cursor and the only way to drop a hunk is
to rebind `drop_hunk` too. Browse, Review, Lint and Log all behave this way.

```toml
# The collision resolved: move the action off the key you stole.
move_down = ["n"]
move_up   = ["p"]
drop_hunk = ["d"]
```

## Keys that are not rebindable

Each screen also has a small set of screen-local keys that are not in the
keymap and therefore cannot be rebound. They are listed here so a rebound
keymap can be planned around them.

| Keys | Screen | Action |
|---|---|---|
| `enter` | Ask | Send the typed question, or expand the selected tool call |
| `up` / `down` | Ask | Select the previous / next tool call |
| `backspace` | Ask | Delete a character from the input box |
| `ctrl+r` | Ask | Jump to Review |
| `enter` | Browse | Open the selected page |
| `/` | Browse | Find a page by name |
| `esc` | Browse | Close the finder |
| `h` / `left` | Browse | Collapse the tree |
| `l` / `right` | Browse | Expand the tree |
| `enter` | Lint | Expand a finding, or jump to the page in Browse |
| `f` | Lint | Ask the agent to fix — reports that it needs the agent |
| `f` | Log | Cycle the log filter |
| `r` | Log | Revert a commit into a new changeset (opens Review) |

Anything the keymap and this table both leave unbound does nothing. The
footer bar advertises only keys something actually binds — `tab`, `ctrl+r`,
Review's `y`/`n`/`C`, and `q` — and no longer hints at `?`: v0.1 renders no
help overlay, so nothing consumes it (C-124/TD-8; see the `help` row
above).

Screens are cycled with `tab`, in this order: Review → Ask → Lint → Log →
Browse, wrapping back to Review.
