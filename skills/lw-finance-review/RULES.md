# Rules for writing expenses

Follow these and the parser, the plan and the review page all stay correct.
Every rule here was tested against lw v2.67.0.

## 1. Where things go

| What | Where | How |
|---|---|---|
| Daily spending | A day note `YYYY-MM-DD.md` (vault root or `ledger/`) | One item per line, or several joined with `+` |
| Family money, rent (fixed monthly costs) | Top of `bookkeeping.md` | `Nov - 家用 4000` |
| Salary, bonus (income) | Top of `bookkeeping.md` | `Nov + salary 16000` |
| Settings and keyword rules | Top of `bookkeeping.md` | `currency = MOP`, `#daily = *` |
| Budgets, buckets, the tag list | `ledger/plan.json` | Edit the numbers; the page follows |

`lw ledger collect` moves day notes into `bookkeeping.md`. Do not type day lines into `bookkeeping.md` yourself, and
do not change lines that collect wrote, except to fix a tag.

## 2. The tags (the only ones allowed)

No tag means `#daily`. Anything that is not a daily need **must** carry its tag.

| Bucket (monthly budget) | Tag | Use it for |
|---|---|---|
| **Needs** (6,000) | *(no tag)* or `#daily` | Meals, drinks, snacks, groceries, small everyday items (1,500 of the 6,000) |
| | `#transport` | Bus, taxi, fuel, parking |
| | `#bill` | Phone, internet, utilities, subscriptions you need |
| | `#health` | Doctor, medicine, dental |
| | `家用` | Family money. A month line, never a day item |
| **Growth & wants** (3,000) | `#course` | Courses, books, certifications, learning tools |
| | `#explore` | Trying something new: workshops, side-project costs |
| | `#gadget` | Devices, hardware, peripherals |
| | `#travel` | Trips, hotels, flights |
| | `#social` | Meals out with others, gifts, gatherings |
| | `#fun` | Games, movies, hobbies, treats for yourself |
| **High-risk investing** (3,000) | `#stock` | Money moved into the brokerage account |
| **Saving & safe investing** (4,000) | `#saving` | Money moved into savings, time deposits, safe funds |

The live list is `ledger/plan.json`; the review page lists each bucket's tags in "Budget against actual" (tags with
no spending are under "Not used"). To add a tag, add it to `plan.json` first, then use it.

## 3. How to write a line in a day note

```
44 jollibee                     -> #daily (no tag needed)
12 bus #transport
1500 (rog falcata) #gadget
138 (lunch with mom) #social
3000 futu #stock
3500 time deposit #saving
-680 (keyboard refund) #gadget  -> a refund: lowers #gadget
25 + 30 + 49.5 (7仔)            -> three items on one line
```

- **One number per item.** Any other number goes inside brackets: `88 (movie 2 tickets) #fun`.
- **One tag per item**, after the item, with a space before `#` and none after it.
- **A tag covers only its own item.** In `45 (kfc) + 1500 (keyboard) #gadget`, only the keyboard is `#gadget`.
- **Say what it was.** `25` alone is counted, but next month nobody knows what it was.

## 4. Mistakes that break things

**Caught by lw** (the note is held as "waiting", or `lw ledger check` reports it):

| You wrote | Problem | Write instead |
|---|---|---|
| `1,500 laptop` | Comma inside a number | `1500 laptop` |
| `45#gadget` | No space before the tag | `45 #gadget` |
| `lunch 23 45` | Two numbers in one item | `23 + 45`, or `23 (lunch 45)` |
| `44 (lunch` | Bracket not closed | `44 (lunch)` |
| `went to the market` | No amount | Add the amount |
| `家用 4000` in `bookkeeping.md` | No month | `Nov - 家用 4000` |
| `budget = ` in `bookkeeping.md` | Empty value | Delete the line (budgets live in `plan.json`) |

**Not caught by lw. These go wrong silently**, so the review page is your only warning:

| You wrote | What happens | Write instead |
|---|---|---|
| `salary 16000` in a day note | Counted as **16,000 spent** | `Nov + salary 16000` in `bookkeeping.md` |
| `12 bus#transport` | Tag lost, counted as `#daily` | `12 bus #transport` |
| `44 lunch # daily` | Tag lost | `44 lunch #daily` |
| `1500 keyboard #gadjet` | Unknown tag: the page shows an error and puts it in no bucket | Use a tag from the list |
| `44 lunch #daily #social` | Only the first tag counts | One tag |
| `1500 keyboard` (tag forgotten) | Counted as `#daily`, inside Needs | `1500 keyboard #gadget` |

## 5. What the review assumes

1. **Positive = money out. A leading minus = refund**, which lowers its tag's total.
2. **`#stock` and `#saving` are transfers, not spending.** Record them on the day you move the money, or the page
   shows 0 invested and 0 saved. lw's own `spent` line counts them as spending; the review page does not.
3. **Income comes from `+` month lines.** If a month has none, the plan salary is assumed and the page says so.
4. **Fixed costs are monthly.** Write the month line once per month (`Dec - 家用 4000`).
5. **A week is Monday to Sunday.** Fixed monthly costs are left out of week views.
6. **Don't use lw's `budget =` line.** It is one number against all day items, including `#stock` and `#saving`.
7. **The page refuses to build** if `lw ledger check` fails or if any category total differs from `lw ledger sum`.
