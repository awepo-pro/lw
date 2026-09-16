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
`down`, `left`, `right`, `backspace`, `pgup`, `pgdown`, `home`, `end`, or a
modifier form such as `ctrl+c`, `ctrl+r`, `shift+tab`. Matching is
case-sensitive: `a` and `A` are different keys.

## Rebindable actions

These are the twenty actions in the keymap. Defaults are frozen; the
`toml` key is the only way to change one.

| TOML key | Default | Action | Screens |
|---|---|---|---|
| `accept_hunk` | `y` | Accept (undrop) the selected hunk and advance | Review |
| `drop_hunk` | `n` | Drop the selected hunk and advance | Review |
| `split_hunk` | `s` | Split the selected hunk — not implemented yet; Review says so and suggests dropping the op instead | Review |
| `accept_all` | `A` | Accept every hunk; refused unless lint is clean | Review |
| `reject_changeset` | `X` | Reject the whole changeset | Review |
| `commit` | `C` | Commit the changeset; refused if lint regresses | Review |
| `move_down` | `j`, `down` | Move the cursor / selection down | Browse, Review, Lint, Log |
| `move_up` | `k`, `up` | Move the cursor / selection up | Browse, Review, Lint, Log |
| `top` | `g` | Jump to the first entry | Browse, Review, Lint, Log |
| `bottom` | `G` | Jump to the last entry | Browse, Review, Lint, Log |
| `preview` | `p` | Toggle Review's detail panel between the Diff and a rendered Preview of the staged page | Review |
| `scroll_page_up` | `pgup` | Scroll the content panel up a page | Review, Browse, Ask |
| `scroll_page_down` | `pgdown` | Scroll the content panel down a page | Review, Browse, Ask |
| `scroll_half_up` | `ctrl+u` | Scroll the content panel up half a page | Review, Browse, Ask |
| `scroll_half_down` | `ctrl+d` | Scroll the content panel down half a page | Review, Browse, Ask |
| `scroll_top` | `home` | Scroll the content panel to its top | Review, Browse, Ask |
| `scroll_bottom` | `end` | Scroll the content panel to its bottom | Review, Browse, Ask |
| `next_pane` | `tab` | Cycle to the next screen | Shell |
| `quit` | `q`, `ctrl+c` | Quit `lw` | Shell |
| `help` | `?` | Help — open the centred Keys overlay, which lists the current screen's keys beside the shell's global ones, plus a `Scroll` group on the screens whose content scrolls; `?` or `esc` closes it, and while it is open every other key except quit is ignored | Shell |

Review's `y`/`n`/`s`/`A`/`X`/`C` letters are the review surface `/docs/design.md`
§9 fixes, and they are also the defaults above. The keymap is the source of
truth: rebinding one of them changes what Review matches, so rebind them
only if you are also prepared to relearn what the documentation says.

### Scrolling and the mouse

Three screens have a panel whose content can be longer than the panel:
Review's detail panel, Browse's preview, and Ask's transcript. The
`scroll_*` actions move that content — a page, half a page, or straight to
its top or bottom — without moving the cursor. On Review and Browse the
scroll position jumps back to the top whenever the selection changes,
because the content under the fold belongs to the selection; Ask keeps
its position, as described below.

The mouse wheel scrolls the panel under the pointer, three lines per
notch. Over Review's Ops list or Browse's Pages tree it moves the cursor
instead, exactly like `j`/`k`; Review's Changeset panel and Browse's Links
panel ignore it. lw holds the mouse while the TUI is open, so select
terminal text with shift+drag.

On Ask, scrolling up stops following the conversation: the transcript
leaves the window where it is, marks how many newer lines are below it
(`↓ N newer` on the panel's bottom border), and folds incoming output in
without moving what is on screen. Sending a message — or scrolling back
down to the bottom — follows the conversation again. The scroll keys are
not printable, so they reach Ask even while the message box is taking
text: `home` and `end` work there too.

### Match order inside a screen

A screen tests its bindings in the order it lists them, and the first match
wins. Navigation is matched first on every screen that has it, so a movement
key rebound onto an action's key **shadows** that action on that screen:

```toml
# The stage file's worked example: vim-key swap onto n/p.
move_down = ["n"]
move_up   = ["p"]
```

On Review this is a double collision: `n` is also `drop_hunk` and `p` is
also `preview`, and `move_down`/`move_up` are matched first, so `n` and `p`
both just move the cursor — the only way to drop a hunk or flip the detail
panel to the Preview is to rebind those actions too. Browse, Review, Lint
and Log all behave this way.

```toml
# The collisions resolved: move each action off the key you stole.
move_down = ["n"]
move_up   = ["p"]
drop_hunk = ["d"]
preview   = ["v"]
```

## Keys that are not rebindable

Each screen also has a small set of screen-local keys that are not in the
keymap and therefore cannot be rebound. They are listed here so a rebound
keymap can be planned around them.

| Keys | Screen | Action |
|---|---|---|
| `enter` | Ask | Send the typed question, or expand / collapse the selected tool call |
| `up` / `down` | Ask | Select the previous / next tool call |
| `backspace` | Ask | Delete a character from the input box |
| `ctrl+r` | Ask | Jump to Review |
| `enter` | Browse | Toggle a directory open/closed; a page or raw source is already open — the preview follows the cursor |
| `/` | Browse | Find a page or raw source by name |
| `esc` | Browse | Close the finder |
| `h` / `left` | Browse | Collapse the selected directory, or move up to its parent |
| `l` / `right` | Browse | Expand the selected directory |
| `enter` | Lint | Open the finding's page in Browse |
| `f` | Lint | Ask the agent to fix — reports that it needs the agent |
| `f` | Log | Cycle the log filter |
| `r` | Log | Revert a commit into a new changeset (opens Review) |

Anything the keymap and this table both leave unbound does nothing.

Two screens take text input: Ask's message box types whenever Ask is the
active screen, and Browse's `/` finder types while it is open. While a
screen is taking text like this, printable keys — `q` and `?` included —
type into the input instead of triggering a keymap action, so an action
rebound onto a printable key does not fire while you are typing. The
non-printable globals still work: `ctrl+c` still quits and `tab` still
switches screens.

The footer shows the active screen's own keys, and the shell appends a
suffix after them: `tab screen` always, `q quit` unless that screen is
taking text input, and `? help` last — so every screen's footer ends the
same way. When the terminal is too narrow to hold the row, whole bindings
drop from the end to make it fit; `? help` itself is never dropped. While a
screen is showing a transient message instead (a refused commit, a revert
result), the footer shows that message and `? help` in place of the key
list.

Screens are cycled with `tab`, in this order: Review → Ask → Lint → Log →
Browse, wrapping back to Review.
