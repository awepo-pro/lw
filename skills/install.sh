#!/bin/sh
# Install the lw agent skills from this repo by symlinking each skills/<name>/ into every agent's skill folder that
# exists on this PC (Claude Code, Codex, Antigravity/Gemini). Symlinks, not copies, so a `git pull` of this repo is
# the whole update. An existing copy that is not already our symlink is moved to
# ~/.local/share/lw-skills-backup/<timestamp>/<agent>/<name> -- outside the skill folder, or the agent would load it twice.
#
#   sh skills/install.sh          install / refresh
#   sh skills/install.sh -n       show what would change, change nothing
set -eu

dry=0
[ "${1:-}" = "-n" ] && dry=1

src_root=$(cd "$(dirname "$0")" && pwd -P)
stamp=$(date +%Y%m%d-%H%M%S)
backup_root="$HOME/.local/share/lw-skills-backup/$stamp"

run() { if [ "$dry" = 1 ]; then echo "would: $*"; else "$@"; fi; }

for agent_dir in "$HOME/.claude/skills" "$HOME/.codex/skills" "$HOME/.gemini/config/skills"; do
  [ -d "$(dirname "$agent_dir")" ] || continue          # that agent is not installed here
  run mkdir -p "$agent_dir"
  for skill in "$src_root"/*/; do
    [ -f "$skill/SKILL.md" ] || continue
    name=$(basename "$skill")
    want="$src_root/$name"
    dest="$agent_dir/$name"
    if [ -L "$dest" ] && [ "$(readlink -f "$dest")" = "$want" ]; then
      echo "ok      $dest"
      continue
    fi
    if [ -e "$dest" ] || [ -L "$dest" ]; then
      bak="$backup_root/$(echo "$agent_dir" | sed "s|^$HOME/||; s|/|_|g")"
      run mkdir -p "$bak"
      run mv "$dest" "$bak/$name"
      echo "backup  $dest -> $bak/$name"
    fi
    run ln -s "$want" "$dest"
    echo "linked  $dest -> $want"
  done
done
