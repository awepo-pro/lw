#!/usr/bin/env python3
"""Move one file, never losing either copy when the destination name is taken.

  destination free                   -> move it there                             (prints: moved)
  destination has the SAME bytes     -> move over it; nothing is lost             (prints: replaced)
  destination has DIFFERENT bytes    -> move it to <stem>-2<ext>, -3, ... the first
                                        free name (or one holding the same bytes) (prints: renamed)

The source is always gone afterwards (it is a move, not a copy), and an existing file with different
content is never overwritten. The last line is machine-readable: `FINAL<TAB><absolute destination>`.

usage: python3 -I safe_move.py <src> <dest>
       <dest> is a file path, or an existing folder (or a path ending in /) to keep the source's name.
exit:  0 moved / replaced / renamed, 1 error (nothing was moved)
"""
import filecmp
import os
import shutil
import sys


def same_bytes(a, b):
    return os.path.isfile(b) and filecmp.cmp(a, b, shallow=False)


def pick_dest(src, dest):
    """(final path, how) for moving src to dest: 'moved', 'replaced' or 'renamed'. Changes nothing."""
    if dest.endswith(os.sep) or os.path.isdir(dest):
        dest = os.path.join(dest, os.path.basename(src))
    dest = os.path.abspath(dest)
    if not os.path.lexists(dest):
        return dest, "moved"
    if same_bytes(src, dest):
        return dest, "replaced"
    stem, ext = os.path.splitext(dest)
    n = 2
    while True:
        cand = f"{stem}-{n}{ext}"
        if not os.path.lexists(cand):
            return cand, "renamed"
        if same_bytes(src, cand):
            return cand, "replaced"
        n += 1


def main(argv):
    if len(argv) != 3 or argv[1] in ("-h", "--help"):
        print(__doc__)
        return 1
    src, dest = os.path.abspath(argv[1]), argv[2]
    if not os.path.isfile(src):
        print(f"error: {src} is not a file; nothing moved")
        return 1
    final, how = pick_dest(src, dest)
    if os.path.abspath(src) == final:
        print(f"error: {src} is already at its destination; nothing moved")
        return 1
    os.makedirs(os.path.dirname(final), exist_ok=True)
    shutil.move(src, final)  # same filesystem: atomic rename (over an identical file when 'replaced')
    if os.path.exists(src) or not os.path.isfile(final):
        print(f"error: move of {src} -> {final} did not complete")
        return 1
    taken = os.path.join(dest, os.path.basename(src)) if dest.endswith(os.sep) or os.path.isdir(dest) else dest
    note = {"moved": "", "replaced": "  (an identical file was there)",
            "renamed": f"  ({os.path.basename(taken)} there holds different content)"}[how]
    print(f"{how:8} {src} -> {final}{note}")
    print(f"FINAL\t{final}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
