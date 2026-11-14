#!/usr/bin/env python3
"""Build the interactive finance review page for an lw vault.

Reads bookkeeping.md and ledger/plan.json, checks every category total against
`lw ledger sum`, and writes one self-contained HTML page to ledger/reviews/.

    build_review.py --vault V --lw /abs/path/to/lw                 # this month
    build_review.py --vault V --lw LW --month 2026-11
    build_review.py --vault V --lw LW --week 2026-W46
    build_review.py --vault V --init-plan                          # write ledger/plan.json
    build_review.py --vault V --lw LW --json-only out.json         # data only (for template work)

Standard library only. All money is handled as integer cents.
"""
import argparse
import calendar
import datetime as dt
import json
import re
import subprocess
import sys
from pathlib import Path

SKILL_DIR = Path(__file__).resolve().parent.parent
TEMPLATE = SKILL_DIR / "assets" / "template.html"
DEFAULT_PLAN = SKILL_DIR / "assets" / "plan.default.json"
PLACEHOLDER = "/*__REVIEW_DATA__*/"

MONTHS = {
    "january": 1, "jan": 1, "february": 2, "feb": 2, "march": 3, "mar": 3, "april": 4, "apr": 4,
    "may": 5, "june": 6, "jun": 6, "july": 7, "jul": 7, "august": 8, "aug": 8, "september": 9,
    "sep": 9, "sept": 9, "october": 10, "oct": 10, "november": 11, "nov": 11, "december": 12, "dec": 12,
}
MONTH_ORDER = sorted(MONTHS, key=len, reverse=True)
DAY_RE = re.compile(r"^(\d{4}-\d{2}-\d{2})(?!\d)")
ITEM_RE = re.compile(r"^(-)?\$?(\d{1,12}(?:\.\d{1,2})?)(.*)$", re.S)
AMOUNT_RE = re.compile(r"^\$?(\d{1,12}(?:\.\d{1,2})?)$")
SOURCE_RE = re.compile(r"\s+\[\[([^\[\]]+)\]\]$")
TAG_RE = re.compile(r"^#[\w-]+$", re.U)


class BuildError(Exception):
    pass


def cents(s):
    whole, _, frac = s.partition(".")
    return int(whole) * 100 + int((frac + "00")[:2])


def money(c):
    return round(c / 100, 2)


def fmt(c):
    return f"{c / 100:,.2f}"


def split_plus(s):
    pieces, cur, depth = [], [], 0
    for ch in s:
        if ch == "(":
            depth += 1
        elif ch == ")":
            depth = max(0, depth - 1)
        if ch == "+" and depth == 0:
            pieces.append("".join(cur).strip())
            cur = []
        else:
            cur.append(ch)
    pieces.append("".join(cur).strip())
    return pieces


def parse_tail(tail):
    """Same as lw: the first #tag wins, the rest is the note (outer brackets dropped)."""
    tag, words = "", []
    for tok in tail.split():
        if TAG_RE.match(tok):
            if not tag:
                tag = tok[1:].lower()
        else:
            words.append(tok)
    note = " ".join(words).strip()
    if note.startswith("(") and note.endswith(")"):
        note = note[1:-1].strip()
    return tag, note


def is_hrule(line):
    body = line.replace(" ", "").replace("\t", "")
    return len(body) >= 3 and all(ch in "-*_=" for ch in body)


def parse_month_line(content):
    idx = max(content.rfind(" "), content.rfind("\t"))
    if idx == -1:
        return None
    m = AMOUNT_RE.match(content[idx:].strip())
    head = content[:idx].rstrip()
    if not m or not head:
        return None
    year, month, rem = "", 0, ""
    fa = re.match(r"^(\d{4})-(\d{2})(?=$|[ \t+-])", head)
    if fa and 1 <= int(fa.group(2)) <= 12:
        year, month, rem = fa.group(1), int(fa.group(2)), head[7:]
    if not month:
        fb = re.match(r"^(\d{1,2})月(?=$|[ \t+-])", head)
        if fb and 1 <= int(fb.group(1)) <= 12:
            month, rem = int(fb.group(1)), head[fb.end():]
    if not month:
        low = head.lower()
        for cand in MONTH_ORDER:
            if low.startswith(cand) and len(head) > len(cand) and head[len(cand)] in " \t-+":
                month, rem = MONTHS[cand], head[len(cand):]
                break
    if not month:
        return None
    rem = rem.lstrip()
    if not year:
        fy = re.match(r"^(\d{4})(?=$|[ \t+-])", rem)
        if fy:
            year, rem = fy.group(1), rem[4:].lstrip()
    if not rem or rem[0] not in "+-":
        return None
    label = rem[1:].strip()
    if not label or label[0] in "+-":
        return None
    return {"year": year, "month": month, "sign": rem[0], "label": label, "cents": cents(m.group(1))}


def parse_book(text):
    """Parse bookkeeping.md the way lw does. Returns (entries, rules, currency).

    Each entry: date ('' for a month line), month, cents (>0 = money out), cat, note, kind, line, tag_source.
    Lines lw would reject are skipped here: `lw ledger check` must pass first.
    """
    lines = text.split("\n")
    rules, currency = [], ""
    days, months = [], []
    in_fence, in_front = False, False
    for i, raw in enumerate(lines, 1):
        line = raw.rstrip("\r")
        t = line.lstrip(" \t")
        if i == 1 and t.strip() == "---":
            in_front = True
            continue
        if in_front:
            if t.strip() == "---":
                in_front = False
            continue
        if t.startswith("```") or t.startswith("~~~"):
            in_fence = not in_fence
            continue
        if in_fence or not t.strip():
            continue
        if t.startswith((">", "//", "<!--")) or t.strip() == "#" or t.startswith("# ") or is_hrule(line):
            continue
        content = t
        if content.startswith(("- ", "* ")):
            content = content[2:].lstrip(" \t")
        content = content.rstrip()
        eq = content.find("=")
        if eq != -1 and not content.startswith("#"):
            key = content[:eq].strip().lower()
            if key == "currency":
                currency = content[eq + 1:].strip().upper()
                continue
            if key == "budget":
                continue
        if content.startswith("#") and eq != -1:
            name = content[1:eq].strip()
            if re.fullmatch(r"[\w-]+", name, re.U):
                kws = [k.strip().lower() for k in content[eq + 1:].split(",") if k.strip()]
                if kws:
                    rules.append((name.lower(), kws))
                continue
        dm = DAY_RE.match(content)
        if dm:
            date = dm.group(1)
            rest = content[10:].strip()
            if rest[:1] in (",", ":"):
                rest = rest[1:].strip()
            sm = SOURCE_RE.search(rest)
            if sm:
                rest = rest[:sm.start()].strip()
            items = []
            for piece in split_plus(rest):
                im = ITEM_RE.match(piece)
                if not im:
                    items = []
                    break
                c = cents(im.group(2))
                tag, note = parse_tail(im.group(3))
                items.append({"cents": -c if im.group(1) else c, "tag": tag, "note": note})
            if items:
                days.append({"line": i, "date": date, "items": items})
            continue
        ml = parse_month_line(content)
        if ml:
            ml["line"] = i
            months.append(ml)

    def rule_cat(note, catch_all):
        """Returns (category, source): a keyword rule, else the `#tag = *` catch-all, else nothing."""
        low = note.lower()
        if low:
            for name, kws in rules:
                if any(k != "*" and k in low for k in kws):
                    return name, "rule"
        if catch_all:
            for name, kws in rules:
                if "*" in kws:
                    return name, "default"
        return "", "default"

    entries = []
    for d in days:
        for it in d["items"]:
            if it["tag"]:
                cat, src = it["tag"], "explicit"
            else:
                cat, src = rule_cat(it["note"], True)
            entries.append({"date": d["date"], "month": d["date"][:7], "cents": it["cents"],
                            "cat": cat or "uncategorized", "note": it["note"], "kind": "day",
                            "line": d["line"], "tag_source": src})
    for ml in months:
        year = ml["year"]
        if not year:
            below = [d for d in days if d["line"] > ml["line"]]
            above = [d for d in days if d["line"] < ml["line"]]
            if below:
                year = below[0]["date"][:4]
            elif above:
                year = above[-1]["date"][:4]
        if not year:
            continue
        tag, note = parse_tail(ml["label"])
        cat = tag or rule_cat(note, False)[0] or re.sub(r"\s+", "-", note.lower())
        c = ml["cents"] if ml["sign"] == "-" else -ml["cents"]
        entries.append({"date": "", "month": f"{year}-{ml['month']:02d}", "cents": c, "cat": cat,
                        "note": note, "kind": "fixed" if ml["sign"] == "-" else "income",
                        "line": ml["line"], "tag_source": "fixed"})
    entries.sort(key=lambda e: e["line"])
    return entries, rules, currency


def run_lw(lw, vault, *args):
    p = subprocess.run([lw, "ledger", *args, "--vault", str(vault)], capture_output=True, text=True)
    return p.returncode, (p.stdout + p.stderr).strip()


def verify_with_lw(lw, vault, entries):
    """Every month's category totals must equal what `lw ledger sum` says."""
    code, out = run_lw(lw, vault, "check")
    if code != 0 or not out.startswith("ok"):
        raise BuildError("lw ledger check found problems. Fix them first:\n" + out)
    for month in sorted({e["month"] for e in entries}):
        code, out = run_lw(lw, vault, "sum", month)
        if code != 0:
            raise BuildError(f"lw ledger sum {month} failed:\n{out}")
        theirs = {}
        for row in out.splitlines():
            parts = row.split()
            if len(parts) < 4 or parts[1] == "total":
                continue
            theirs[parts[1]] = theirs.get(parts[1], 0) + round(float(parts[2]) * 100)
        mine = {}
        for e in entries:
            if e["month"] == month:
                mine[e["cat"]] = mine.get(e["cat"], 0) - e["cents"]
        if mine != theirs:
            diff = sorted(set(mine) | set(theirs))
            rows = [f"  {c}: review {fmt(mine.get(c, 0))} vs lw {fmt(theirs.get(c, 0))}"
                    for c in diff if mine.get(c, 0) != theirs.get(c, 0)]
            raise BuildError(f"Totals for {month} do not match lw. Nothing was written.\n" + "\n".join(rows))


def load_plan(vault):
    path = vault / "ledger" / "plan.json"
    if not path.exists():
        raise BuildError(f"No plan found at {path}. Run with --init-plan to create it, then edit the numbers.")
    plan = json.loads(path.read_text(encoding="utf-8"))
    seen = {}
    for b in plan["buckets"]:
        for t in b["tags"]:
            key = t["tag"].lower()
            if key in seen:
                raise BuildError(f"plan.json: tag #{t['tag']} is in two buckets ({seen[key]} and {b['id']}).")
            seen[key] = b["id"]
    if plan["default_tag"].lower() not in seen:
        raise BuildError(f"plan.json: default_tag #{plan['default_tag']} is not one of the plan's tags.")
    return plan, seen


def week_range(week):
    m = re.fullmatch(r"(\d{4})-W(\d{2})", week)
    if not m:
        raise BuildError(f"Bad week {week!r}. Write it like 2026-W46.")
    start = dt.date.fromisocalendar(int(m.group(1)), int(m.group(2)), 1)
    return start, start + dt.timedelta(days=6)


def build_data(vault, lw, focus_type, focus_value, today):
    book = vault / "bookkeeping.md"
    if not book.exists():
        raise BuildError(f"{book} not found.")
    entries, _rules, currency = parse_book(book.read_text(encoding="utf-8"))
    plan, tag_bucket = load_plan(vault)
    lw_version = ""
    if lw:
        verify_with_lw(lw, vault, entries)
        lw_version = subprocess.run([lw, "version"], capture_output=True, text=True).stdout.strip()
    currency = currency or plan.get("currency", "")
    default_tag = plan["default_tag"].lower()
    issues = []

    out_entries, income = [], []
    for n, e in enumerate(entries):
        if e["kind"] == "income":
            income.append({"month": e["month"], "amount": money(-e["cents"]), "label": e["note"], "assumed": False})
            continue
        tag = e["cat"]
        if tag == "uncategorized":
            tag = default_tag
        bucket = tag_bucket.get(tag, "unknown")
        if bucket == "unknown":
            issues.append({"level": "error", "code": "unknown-tag", "line": e["line"],
                           "msg": f"Line {e['line']}: #{tag} is not a tag in the plan ({fmt(e['cents'])}, {e['note'] or 'no note'})."})
        out_entries.append({"id": n, "date": e["date"] or None, "month": e["month"], "amount": money(e["cents"]),
                            "tag": tag, "bucket": bucket, "note": e["note"], "kind": e["kind"],
                            "tag_source": e["tag_source"], "line": e["line"]})

    month_keys = sorted({e["month"] for e in out_entries} | {i["month"] for i in income})
    if focus_type == "month" and focus_value not in month_keys:
        month_keys = sorted(month_keys + [focus_value])
    months = []
    for mk in month_keys:
        y, mo = int(mk[:4]), int(mk[5:7])
        dim = calendar.monthrange(y, mo)[1]
        first, last = dt.date(y, mo, 1), dt.date(y, mo, dim)
        elapsed = dim if today > last else (0 if today < first else today.day)
        m_inc = [i for i in income if i["month"] == mk]
        inc_c = sum(round(i["amount"] * 100) for i in m_inc)
        assumed = not m_inc
        if assumed:
            inc_c = round(plan["salary"] * 100)
            income.append({"month": mk, "amount": plan["salary"], "label": "salary (assumed from plan)", "assumed": True})
            issues.append({"level": "warn", "code": "income-assumed",
                           "msg": f"{mk}: no income line in bookkeeping.md, so the plan salary {fmt(inc_c)} is assumed."})
        m_ent = [e for e in out_entries if e["month"] == mk]
        buckets = {}
        for b in plan["buckets"] + [{"id": "unknown", "budget": 0}]:
            actual = sum(round(e["amount"] * 100) for e in m_ent if e["bucket"] == b["id"])
            if b["id"] == "unknown" and not actual:
                continue
            buckets[b["id"]] = {"budget": b["budget"], "actual": money(actual)}
        tags = {}
        for e in m_ent:
            t = tags.setdefault(e["tag"], {"actual": 0, "count": 0})
            t["actual"] += round(e["amount"] * 100)
            t["count"] += 1
        for t in tags.values():
            t["actual"] = money(t["actual"])
        total_out = sum(round(e["amount"] * 100) for e in m_ent)
        recorded = sorted({e["date"] for e in m_ent if e["date"]})
        missing = []
        for d in range(1, elapsed + 1):
            ds = f"{mk}-{d:02d}"
            if ds not in recorded:
                missing.append(ds)
        if missing:
            issues.append({"level": "info", "code": "missing-days",
                           "msg": f"{mk}: no entry on {len(missing)} day(s): " + ", ".join(x[8:] for x in missing)})
        untagged = [e for e in m_ent if e["tag_source"] == "default"]
        if untagged:
            issues.append({"level": "info", "code": "untagged",
                           "msg": f"{mk}: {len(untagged)} item(s) had no tag and were counted as #{default_tag}."})
        nonote = [e for e in m_ent if e["kind"] == "day" and not e["note"]]
        if nonote:
            issues.append({"level": "info", "code": "no-note",
                           "msg": f"{mk}: {len(nonote)} item(s) have no note, so nobody can tell what they were."})
        months.append({"month": mk, "days_in_month": dim, "days_elapsed": elapsed, "complete": today > last,
                       "income": money(inc_c), "income_assumed": assumed, "total_out": money(total_out),
                       "residual": money(inc_c - total_out), "buckets": buckets, "tags": tags,
                       "recorded_days": len(recorded), "missing_days": missing})

    if focus_type == "week":
        start, end = week_range(focus_value)
    else:
        y, mo = int(focus_value[:4]), int(focus_value[5:7])
        start, end = dt.date(y, mo, 1), dt.date(y, mo, calendar.monthrange(y, mo)[1])
    return {
        "meta": {"generated": dt.datetime.now().isoformat(timespec="seconds"), "today": today.isoformat(),
                 "currency": currency, "lw_version": lw_version, "verified": bool(lw),
                 "focus": {"type": focus_type, "value": focus_value, "start": start.isoformat(), "end": end.isoformat()},
                 "large_item_threshold": plan.get("large_item_threshold", 500)},
        "plan": plan,
        "entries": out_entries,
        "income": income,
        "months": months,
        "issues": issues,
    }


def summary(data):
    """Plain numbers for the agent to quote. The agent never adds these up itself."""
    f, cur = data["meta"]["focus"], data["meta"]["currency"]
    names = {b["id"]: b["name"] for b in data["plan"]["buckets"]}
    names["unknown"] = "Unknown tags"
    out = []
    if f["type"] == "month":
        m = next(x for x in data["months"] if x["month"] == f["value"])
        out.append(f"{f['value']} {cur}  (day {m['days_elapsed']} of {m['days_in_month']}, {m['recorded_days']} day(s) with entries)")
        out.append(f"income {m['income']:,.2f}" + (" (assumed from plan)" if m["income_assumed"] else ""))
        for bid, b in m["buckets"].items():
            gap = b["budget"] - b["actual"]
            state = f"{gap:,.2f} left" if gap >= 0 else f"{-gap:,.2f} OVER"
            out.append(f"  {names[bid]}: {b['actual']:,.2f} of {b['budget']:,.2f} ({state})")
        out.append(f"total out {m['total_out']:,.2f}; left from income {m['residual']:,.2f}")
        for t, v in sorted(m["tags"].items(), key=lambda kv: -kv[1]["actual"]):
            out.append(f"  #{t} {v['actual']:,.2f} ({v['count']})")
    else:
        ents = [e for e in data["entries"] if e["date"] and f["start"] <= e["date"] <= f["end"]]
        out.append(f"{f['value']} {cur}  ({f['start']} to {f['end']}, fixed monthly costs not included)")
        by_b, by_t = {}, {}
        for e in ents:
            by_b[e["bucket"]] = by_b.get(e["bucket"], 0) + round(e["amount"] * 100)
            by_t[e["tag"]] = by_t.get(e["tag"], 0) + round(e["amount"] * 100)
        for bid, c in by_b.items():
            out.append(f"  {names[bid]}: {fmt(c)}")
        out.append(f"total out {fmt(sum(by_b.values()))} in {len(ents)} item(s)")
        for t, c in sorted(by_t.items(), key=lambda kv: -kv[1]):
            out.append(f"  #{t} {fmt(c)}")
    for i in data["issues"]:
        out.append(f"[{i['level']}] {i['msg']}")
    return "\n".join(out)


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--vault", required=True)
    ap.add_argument("--lw", help="absolute path to lw; totals are checked against it")
    ap.add_argument("--month", help="YYYY-MM (default: this month)")
    ap.add_argument("--week", help="YYYY-Www, ISO week, Monday to Sunday")
    ap.add_argument("--today", help="YYYY-MM-DD (default: today)")
    ap.add_argument("--init-plan", action="store_true", help="write ledger/plan.json from the default plan")
    ap.add_argument("--json-only", metavar="FILE", help="write the data as JSON and stop")
    ap.add_argument("--no-verify", action="store_true", help="skip the lw cross-check (template work only)")
    a = ap.parse_args()
    vault = Path(a.vault).resolve()
    try:
        if a.init_plan:
            dest = vault / "ledger" / "plan.json"
            if dest.exists():
                raise BuildError(f"{dest} already exists. Edit it; it was not overwritten.")
            dest.parent.mkdir(parents=True, exist_ok=True)
            dest.write_text(DEFAULT_PLAN.read_text(encoding="utf-8"), encoding="utf-8")
            print(f"wrote {dest}")
            return 0
        if a.month and a.week:
            raise BuildError("Give --month or --week, not both.")
        if not a.lw and not a.no_verify:
            raise BuildError("Pass --lw /abs/path/to/lw so totals can be checked (or --no-verify for template work).")
        today = dt.date.fromisoformat(a.today) if a.today else dt.date.today()
        if a.week:
            ftype, fval = "week", a.week
        else:
            ftype, fval = "month", a.month or today.strftime("%Y-%m")
            if not re.fullmatch(r"\d{4}-(0[1-9]|1[0-2])", fval):
                raise BuildError(f"Bad month {fval!r}. Write it like 2026-11.")
        data = build_data(vault, None if a.no_verify else a.lw, ftype, fval, today)
        blob = json.dumps(data, ensure_ascii=False)
        if a.json_only:
            Path(a.json_only).write_text(json.dumps(data, ensure_ascii=False, indent=1), encoding="utf-8")
            print(f"wrote {a.json_only}")
            return 0
        tpl = TEMPLATE.read_text(encoding="utf-8")
        if tpl.count(PLACEHOLDER) != 1:
            raise BuildError(f"{TEMPLATE} must contain {PLACEHOLDER} exactly once.")
        html = tpl.replace(PLACEHOLDER, blob.replace("</", "<\\/"))
        out = vault / "ledger" / "reviews" / f"{fval}.html"
        out.parent.mkdir(parents=True, exist_ok=True)
        out.write_text(html, encoding="utf-8")
        print(summary(data))
        print(f"\nwrote {out}")
        return 0
    except BuildError as e:
        print(f"build_review: {e}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
