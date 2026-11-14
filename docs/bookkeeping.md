# Bookkeeping

## The idea

Write a day note, lw summarises it into `bookkeeping.md` and archives it. No model inside lw: parsing and reporting are deterministic Go.

## Day notes

A day note is named `YYYY-MM-DD.md` and sits directly at the vault root or in `ledger/`.

Frontmatter (`---`), code fences, blank lines, comments (`# `, `>`, `//`, `<!--`), and horizontal rules (`---`) are ignored. Leading bullets (`- ` or `* `) are stripped, and a line with only the note's date is skipped. Full-width numbers and punctuation are normalized automatically.

What a line may look like:
- **Amount first**: `44 (jollibee)`, `$49.50 (lunch)`, or `12.50 bus #transit`
- **Note first**: `kfc 45`, `bus 12.50`, or `lunch 25 #food`
- **Multiple items with `+` or a comma**: `lunch 25 + dinner 30`, `kfc 45, mcd 55`, `44, 55`
- **One number per item**: outside parentheses an item holds exactly one amount. Put any other number in parentheses: `6 (bus 26)`, `40 (2 coffees)`. `bus 26 6`, `2 x 15` and `45 dinner 50` are held as waiting rather than guessed. `1,200` is not read as an amount: write `1200`.
- **Money in with `-`**: `-500 refund` or `refund -500`
- **Pasted receipt image**: `![[receipt-lunch.jpg]]` or `![](receipt-lunch.jpg)`

What makes a note "waiting":
- **Prose without an amount**: `went to the supermarket and bought milk` (`waiting 2026-11-07.md: line 1: no amount in "went to the supermarket and bought milk"`)
- **Two numbers in one item**: `lunch 23 45` (`waiting 2026-11-06.md: line 1: two numbers in "lunch 23 45" (write 23 + 45, or put the note in parentheses)`)
- **Only an image**: `![[receipt-store.jpg]]` (`waiting 2026-11-05.md: only a receipt image`)
- **Date mismatch**: `2026-11-09` inside `2026-11-08.md` (`waiting 2026-11-08.md: line 1: date 2026-11-09 is not this note's date`)
- **Unbalanced parentheses**: `44 (lunch` (`waiting 2026-11-08.md: line 1: unbalanced parentheses`)
- **Archive collision**: destination file already exists with different contents (`waiting 2026-11-08.md: the archive already holds a different 2026-11-08.md`)

A receipt photo with no typed amount needs a person or an agent to read it.

How to supply its line:
```bash
lw ledger collect --day 2026-11-05 --line "88 (supermarket) #groceries"
```

## The central file

`bookkeeping.md` sits at the vault root. lw only ever appends to it; it never edits or rewrites existing entries.

Line shapes:
- **Day line**: `YYYY-MM-DD, <items> [[YYYY-MM-DD]]`
  ```
  2026-11-08, 44 (jollibee) + 45 (kfc) + 12.50 (bus) + -500 (refund) [[2026-11-08]]
  ```
  The trailing `[[date]]` is the source link to the archived day note in `ledger/YYYY/MM/YYYY-MM-DD.md`.
- **Month line**: `<MONTH>[ <YYYY>] <sign> <label> <amount>`
  ```
  Nov - rent 1200
  2026-11 + salary 30000
  11月 - utilities 350
  ```
  A `-` indicates fixed expenses and `+` indicates recurring income. The year is explicit or inferred from nearby day lines.
- **Directives**:
  ```
  currency = HKD
  budget = 5000
  ```
- **Category rules**:
  ```
  #food = jollibee, kfc, lunch, dinner, coffee
  #transit = bus, subway, taxi
  #misc = *
  ```
  Keywords are case-insensitive substring matches against item notes. `*` acts as a catch-all category for items not matched by earlier rules.

## Commands

Commands run against a vault created with `lw init --schema test`:

### `lw ledger collect --dry-run`
```
$ lw ledger collect --dry-run
would collect 2026-11-08: 44 (jollibee) + 45 (kfc) + 12.50 (bus) + -500 (refund) -> ledger/2026/11/2026-11-08.md (1 receipt(s))
would collect 2026-11-09: 25 (lunch) + 30 (dinner) -> ledger/2026/11/2026-11-09.md
skipped 2026-11-10.md: today (use --include-today)
would collect 2 day note(s), 0 waiting
```

### `lw ledger collect`
```
$ lw ledger collect
collected 2026-11-08: 44 (jollibee) + 45 (kfc) + 12.50 (bus) + -500 (refund) -> ledger/2026/11/2026-11-08.md (1 receipt(s))
collected 2026-11-09: 25 (lunch) + 30 (dinner) -> ledger/2026/11/2026-11-09.md
skipped 2026-11-10.md: today (use --include-today)
collected 2 day note(s), 0 waiting
run lw ledger import to store the receipts so lw sync carries them
```

### `lw ledger collect --include-today`
```
$ lw ledger collect --include-today
collected 2026-11-10: 20 (coffee) -> ledger/2026/11/2026-11-10.md
collected 1 day note(s), 0 waiting
```

### `lw ledger import --dry-run`
```
$ lw ledger import --dry-run
would store receipts/2026/11/receipt-lunch.jpg
1 stored, 0 unchanged, 0 conflict(s), 0 error(s)
```

### `lw ledger import`
```
$ lw ledger import
stored receipts/2026/11/receipt-lunch.jpg
1 stored, 0 unchanged, 0 conflict(s), 0 error(s)
run lw sync to carry them to your other PCs
```

### `lw ledger check`
```
$ lw ledger check
ok: 8 transaction(s) in 4 file(s)
```

### `lw ledger sum`
```
$ lw ledger sum
HKD  food           -164.00   (5)
HKD  rent           -1200.00  (1)
HKD  transit        -12.50    (1)
HKD  uncategorized  500.00    (1)
HKD  total          -876.50   (8)
```

### `lw ledger report`
```
$ lw ledger report 2026-11
2026-11 HKD
spent 176.50 (6 item(s) on 3 day(s))
fixed 1200.00 (rent 1200.00)
money in 500.00
net out 876.50
budget 5000.00: 4823.50 left
by category:
  rent 1200.00 (1)
  food 164.00 (5)
  transit 12.50 (1)
average per recorded day 58.83
```

## The review page

`lw ledger report` prints one month as text. For a page that shows where the money went, use the
`lw-finance-review` agent skill (in `skills/`, installed by `sh skills/install.sh`). Ask your agent
"review my spending" or "weekly review". lw does not build this page itself and no model adds the numbers: the
skill runs a script.

The page needs two files in the vault:

- `bookkeeping.md`, with the day lines that `lw ledger collect` wrote.
- `ledger/plan.json`: your plan. It holds the salary, the buckets (for example Needs, Growth & wants, High-risk
  investing, Saving), each bucket's monthly budget and the tags that belong to it. The skill writes a default plan
  the first time; the numbers are yours to edit.

The script runs `lw ledger check`, then compares every category total with `lw ledger sum`. If a total differs,
it writes nothing and says which one. Otherwise it writes `ledger/reviews/<period>.html`, for example
`2026-11.html` or `2026-W46.html`. The page is one file with no network requests, so it opens offline.

From top to bottom the page shows: four key figures, the key findings, a flow diagram from income to buckets to
tags, the budget against actual for each bucket and tag, the spending pace with the biggest items, every
transaction, and a month by month chart. Selecting a block, a row, a day or an item filters the transactions to it.
The page can switch between any month and any week in the ledger.

The page differs from `lw ledger report` in two ways:

- Money moved to saving or investing (`#saving`, `#stock`) is not counted as spending on the page. lw's `spent`
  line counts it.
- Budgets come from `ledger/plan.json`, one for each bucket. The `budget =` line in `bookkeeping.md` is not used.

The rules for writing expenses so that the page stays correct are in `skills/lw-finance-review/RULES.md`.

## Where things end up

- Day notes are moved to `ledger/YYYY/MM/YYYY-MM-DD.md`.
- Embedded receipt images are moved to `ledger/receipts/YYYY/MM/<image>`.

`ledger/receipts/` is kept out of git by the managed `.gitignore`. Receipts are carried into content-addressed storage by `lw ledger import`, and `lw sync` carries them across PCs.

## Every day, automatically

`lw sync watch --collect`: at start, whenever the date changes, and after every successful sync, the watcher runs `lw ledger collect` (never today's note); when a collect moved a receipt it also runs `lw ledger import`. A day note that arrives from another device is therefore collected seconds after it syncs. Run it on one PC only. `lw sync install --print --collect` puts the flag in the unit.

## Grammar v1

The older `- (id:: ...)` day-file lines still work and feed the same totals. They can coexist with loose bookkeeping lines in the same vault.
