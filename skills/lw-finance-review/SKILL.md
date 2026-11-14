---
name: lw-finance-review
description: Build the weekly or monthly finance review of an lw (llmwiki) vault as an interactive web page (money-flow diagram, budget against actual, spending pace, biggest items, transactions) from bookkeeping.md and the budget plan in ledger/plan.json. Use when the user says "review my spending", "weekly review", "monthly review", "finance review", "how did I spend this week/month", "build my finance report", "open my spending report", or asks what tags to use or how to write expenses so the report stays correct. Totals are computed by a script and checked against `lw ledger sum`; you never add numbers yourself. Works in any agent with a shell (Claude Code, Codex, Antigravity).
---

# lw-finance-review: the weekly / monthly review page

The user records expenses in day notes; `lw ledger collect` gathers them into `bookkeeping.md` (see the
`lw-bookkeeping` skill). This skill turns that ledger plus the user's budget plan into one interactive HTML page
and a short spoken-style review.

Three files do the work:

- `ledger/plan.json` (in the vault): the plan. Salary, the buckets, each bucket's monthly budget and its tags.
  This file is the single source of truth for tags: the page, the checks and `RULES.md` all follow it.
- `scripts/build_review.py` (in this skill): parses `bookkeeping.md`, checks every category total against
  `lw ledger sum`, and writes `ledger/reviews/<period>.html`.
- `assets/template.html` (in this skill): the page. One self-contained file, no libraries and no network requests.
  `assets/DATA_CONTRACT.md` describes the data it receives and what each section shows.

`RULES.md` (in this skill) is the list of writing rules for the user. Show it, or the part that applies, whenever the
user asks how to write expenses or when the build reports a problem they caused.

## Rules you must not break

1. **Never do sums.** Every number you say comes from the script's output or from `lw ledger report`. Do not add,
   subtract, average or project in your head.
2. **Never edit `bookkeeping.md` or a day note with a file tool.** If a line is wrong (bad tag, empty directive),
   tell the user the exact line and the fix. Edit only if the user says to, and then only that line.
3. **Never build around a failed check.** If `lw ledger check` fails or the script says totals do not match lw,
   stop and report it. Do not pass `--no-verify` for a real review; it exists for template work only.
4. **Never invent a tag.** Tags come from `ledger/plan.json`. If the user wants a new one, add it to `plan.json`
   (with their agreement on the bucket) before it is used.
5. **Never run `lw sync` unless the user asked.**
6. **This is a framework, not licensed financial advice.** Say so once when you comment on investing.

## Step 0: find lw and the vault (once)

```bash
for c in "$(command -v lw)" "$HOME/go/bin/lw" "$HOME/.local/bin/lw" /usr/local/bin/lw; do
  [ -x "$c" ] && echo "$(readlink -f "$c"): $("$c" version)"; done; true
LW config | grep -E '^vault\.path'
```

`LW` is the absolute path of the newest lw (v2.67.0 or later). `V` is `vault.path`. `SKILL` is this skill's folder.

## Step 1: make sure the ledger is current

```bash
LW ledger collect --vault V --dry-run
LW ledger check --vault V
```

- If the dry run lists notes it `would collect` or that are `waiting`, the ledger is behind. Follow the
  `lw-bookkeeping` skill first (or tell the user), then come back.
- Today's note is skipped by default. For a review that should include today, collect with `--include-today`
  only if the user asked for it.
- `check` must print `ok`. If not, stop: report each line and its fix from `RULES.md` section 4.

## Step 2: make sure the plan exists

```bash
[ -f V/ledger/plan.json ] || python3 SKILL/scripts/build_review.py --vault V --init-plan
```

A new plan is the default 16,000 split (Needs 6,000 / Growth & wants 3,000 / High-risk 3,000 / Saving 4,000).
Tell the user it was created and that the numbers are theirs to edit.

## Step 3: build the page

```bash
python3 SKILL/scripts/build_review.py --vault V --lw LW                  # this month
python3 SKILL/scripts/build_review.py --vault V --lw LW --month 2026-11  # a given month
python3 SKILL/scripts/build_review.py --vault V --lw LW --week 2026-W46  # a given ISO week (Mon..Sun)
```

For "this week" get the ISO week with `date +%G-W%V`; for "last week" use `date -d 'last week' +%G-W%V`.

The script prints the period's numbers and `wrote <path>`. The page holds every month in the ledger and can switch
between month and week views itself; the argument only chooses what it opens on and the file name.

If it prints `build_review: ...` and exits 1, nothing was written. Report the message as is.

## Step 4: open it

```bash
xdg-open V/ledger/reviews/<period>.html     # macOS: open
```

The page is one self-contained file. It makes no network requests, so it works offline and the numbers never leave
the machine. It follows the system light or dark theme and prints cleanly.

What the user sees, top to bottom: four key figures, the key findings, a flow diagram (income to buckets to tags), the
budget against actual table, the spending pace chart with the biggest items beside it, the transactions, then month by
month and the checks. Selecting a block, a table row, a day or an item filters the transactions to it.

## Step 5: give the review

Speak like an analyst opening a client meeting. Short, in this order, using only the script's numbers:

1. **Verdict** in one or two sentences: which buckets are inside budget, which are over and by how much.
2. **Bucket by bucket**: actual against budget. For Needs, mention `#daily` against its own budget.
3. **Investing and saving**: what was moved (`#stock`, `#saving`) against the plan. Zero moved is a finding, say it.
4. **The largest items** and whether they explain an overrun.
5. **Data quality**: every `[error]` and `[warn]` line, then the `[info]` lines briefly. An unknown tag is an error
   the user must fix; quote the line.
6. **One to three actions** for the next period. Concrete, tied to a number above.

Be direct. If a bucket is over, say so plainly; do not soften it. Give the path of the page.

## When the user asks about tags or writing rules

Read `SKILL/RULES.md` and `V/ledger/plan.json`, and answer from them. If the two disagree, `plan.json` wins and
`RULES.md` section 2 should be updated to match.

## Changing the plan

Budgets, salary, tag list and bucket membership live in `V/ledger/plan.json`. A tag may sit in one bucket only, and
`default_tag` must be one of the tags; the script refuses to build otherwise. After a change, rebuild the page.
