# Data contract: build_review.py → template.html

`scripts/build_review.py` replaces the single placeholder `/*__REVIEW_DATA__*/` in `assets/template.html`
with one JSON object (with `</` escaped as `<\/`). The template must hold it like this:

```html
<script id="review-data" type="application/json">/*__REVIEW_DATA__*/</script>
```

and read it with `JSON.parse(document.getElementById('review-data').textContent)`.
When the placeholder is still there (template opened directly), the page must show a short
"no data: build this page with build_review.py" message instead of crashing.

All amounts are numbers in the plan currency, two decimals. **Positive = money out.**
A negative amount on an entry is a refund and lowers that tag's and bucket's total.

## Shape

```jsonc
{
  "meta": {
    "generated": "2026-11-14T20:23:25",   // local time the page was built
    "today": "2026-11-14",                // the review date; "now" for pace and elapsed days
    "currency": "MOP",
    "lw_version": "lw v2.67.0",
    "verified": true,                     // true = every month's category totals equal `lw ledger sum`
    "focus": {                            // what the page opens on
      "type": "month",                    // "month" | "week"
      "value": "2026-11",                 // "YYYY-MM" | "YYYY-Www" (ISO week, Mon..Sun)
      "start": "2026-11-01", "end": "2026-11-30"
    },
    "large_item_threshold": 500           // default for the "hide large items" control
  },

  "plan": {                               // ledger/plan.json, verbatim
    "salary": 16000,
    "default_tag": "daily",               // items with no tag are counted under this tag
    "buckets": [                          // ordered; this order is the display order
      {
        "id": "needs",                    // "needs" | "growth" | "risk" | "safe" today; treat as open-ended
        "name": "Needs",
        "desc": "…",
        "kind": "spend",                  // "spend" | "invest" | "save"
        "budget": 6000,                   // per month
        "residual": true,                 // optional; only on the bucket that takes "what remains"
        "tags": [
          { "tag": "daily", "label": "Daily food & necessities", "desc": "…",
            "budget": 1500,               // optional per-tag monthly budget
            "fixed": true }               // optional; true = written as a month line (e.g. 家用)
        ]
      }
    ],
    "theory": {
      "barbell_risk_share_of_invested": [0.10, 0.15],
      "annual_rates": { "safe": 0.03, "index": 0.06, "risk": 0.0 },
      "notes": ["…"]
    }
  },

  "entries": [                            // every money-out line in the ledger, all months, in file order
    {
      "id": 0,
      "date": "2026-11-02",               // null for a fixed monthly cost
      "month": "2026-11",
      "amount": 44.0,                     // > 0 out, < 0 refund
      "tag": "daily",                     // lower-case; one of the plan tags, or something unknown
      "bucket": "needs",                  // a plan bucket id, or "unknown" when the tag is not in the plan
      "note": "jollibee",                 // may be ""
      "kind": "day",                      // "day" | "fixed"
      "tag_source": "explicit",           // "explicit" (#tag typed) | "rule" (keyword rule) | "default" (no tag) | "fixed"
      "line": 8                           // line in bookkeeping.md
    }
  ],

  "income": [                             // one or more per month
    { "month": "2026-11", "amount": 16000, "label": "salary", "assumed": false }
    // assumed: true = no income line in the ledger, plan salary used
  ],

  "months": [                             // ascending; totals already checked against lw
    {
      "month": "2026-11",
      "days_in_month": 30,
      "days_elapsed": 14,                 // up to meta.today; = days_in_month for past months
      "complete": false,
      "income": 16000.0,
      "income_assumed": false,
      "total_out": 7598.4,                // all buckets, incl. invest and save transfers
      "residual": 8401.6,                 // income - total_out
      "buckets": { "needs": { "budget": 6000, "actual": 4574.4 }, "…": {} },
                                          // an "unknown" key appears only when unknown tags were used
      "tags": { "daily": { "actual": 574.4, "count": 18 } },
      "recorded_days": 13,
      "missing_days": ["2026-11-01"]      // elapsed days with no entry
    }
  ],

  "issues": [                             // data-quality notes for the reader
    { "level": "info", "code": "missing-days", "msg": "…" }
    // level: "error" | "warn" | "info"
    // codes: unknown-tag, income-assumed, missing-days, untagged, no-note
  ]
}
```

## Rules the page must follow

- **Month figures**: use `months[]` as given. Do not re-derive them.
- **Week figures**: the page adds up `entries` whose `date` is inside the week (Mon..Sun, ISO).
  Fixed monthly costs (`kind: "fixed"`, `date: null`) are not part of a week. A week's budget for a **spend** bucket
  or tag is `(monthly budget - that month's fixed costs in the bucket) x 7 / days_in_month`, using the month the week's
  Monday is in. **Invest and save buckets have no week budget**: money is moved once a month, so the week view shows
  what was moved and nothing to compare it with.
- **Spending is not everything out.** "Spent" is the `kind: "spend"` buckets (plus unknown tags). Money moved into
  `invest` and `save` buckets is shown as "Saved and invested" and is never drawn in the spending pace chart.
- **Going over is bad only for spending.** A spend bucket above its budget is an error (red). An invest or save
  bucket at or above its budget is "Funded".
- **Safe bucket** (`residual: true`): show money actually moved (`buckets.safe.actual`, from `#saving`) against its
  budget, and say how much income is still left over (`residual`).
- **Barbell check** (month view only): risk share of invested money = `risk.actual / (risk.actual + safe.actual)`;
  planned share = `risk.budget / (risk.budget + safe.budget)`; compare both with `theory.barbell_risk_share_of_invested`.
- **Projection**: for a period still running, month end = spent so far + (daily rate of dated spend items under
  `large_item_threshold`) x days left. Large items are counted once, not repeated.
- **Unknown tags**: entries with `bucket: "unknown"` are shown as errors and never hidden.
- **No sums in prose**: every number shown comes from this JSON or from adding `entries` in the page.
- **No network, no libraries**: the page is one file. Do not add a CDN script; the page holds private figures.
- **Notes and tags are untrusted text**: insert them as text nodes, never as HTML.

## What the page shows (top to bottom)

| Section | Question it answers | Built from |
|---|---|---|
| Key figures | How much came in, went out, was put away, is left? | `months[]` (month) or `entries` (week) |
| Key findings | What needs attention? Over budget, ahead of pace, nothing saved, the largest item | computed in the page |
| Where the money went | Income to buckets to tags, as a flow diagram | bucket and tag totals |
| Budget against actual | Each bucket and tag: actual, budget, remaining, used, status | plan budgets + totals |
| Spending pace | Running total against budget, and each day's spending | dated spend `entries` |
| Biggest items | Which notes took the most money | spend `entries` grouped by `note` |
| Transactions | Every line, searchable and sortable | `entries` |
| Month by month | Out by bucket against income (two months or more) | `months[]` |
| Checks | Barbell share, and ledger notes to fix | `plan.theory`, `issues` |

Selecting a block in the flow diagram, a row in the budget table, a day in the pace chart or a biggest item filters
the transactions to it. The page opens on `meta.focus` and can switch to any month or week in the ledger
(`#month=YYYY-MM` or `#week=YYYY-Www` in the address does the same).
