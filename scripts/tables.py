"""Print the brief's results tables from results.csv, and nothing else.

Usage: python3 scripts/tables.py [results/results.csv]

Every number here is computed from that one file, so a reviewer can
recompute it. Percentiles are nearest-rank. Times in ms, percentages to
one decimal. "not measured" where a cell has no rows behind it.
"""

import csv
import math
import sys
from collections import defaultdict

NM = "not measured"

# List price for openai/gpt-oss-20b, console.groq.com/docs/model/openai/gpt-oss-20b
# (read 2026-11-12). Reasoning tokens are in completion_tokens; we assume
# they bill as output (the page does not say).
USD_PER_M_IN, USD_PER_M_OUT = 0.075, 0.30


def cost_per_1000(answers):
    """Free tier: $0. At list price: what 1,000 such answers would cost."""
    if not answers:
        return NM
    tin = sum(num(r, "tokens_in") for r in answers) / len(answers)
    tout = sum(num(r, "tokens_out") for r in answers) / len(answers)
    return f"$0 free; ${1000 * (tin * USD_PER_M_IN + tout * USD_PER_M_OUT) / 1e6:.3f} at list"


def pct(values, p):
    """Nearest-rank percentile: the smallest value with at least p% of
    values at or below it. None for an empty list."""
    if not values:
        return None
    s = sorted(values)
    rank = max(1, math.ceil(p / 100 * len(s)))
    return s[rank - 1]


def ms(v):
    return NM if v is None else str(int(v))


def triple(values, ps=(50, 95, 99)):
    if not values:
        return NM
    return " / ".join(ms(pct(values, p)) for p in ps)


def share(n, total):
    return NM if total == 0 else f"{100 * n / total:.1f}"


def num(row, col):
    v = row.get(col, "")
    return None if v == "" else float(v)


def load(path):
    with open(path, newline="") as f:
        return list(csv.DictReader(f))


def by(rows, key):
    groups = defaultdict(list)
    for r in rows:
        groups[key(r)].append(r)
    return dict(sorted(groups.items()))


def duration_s(rows):
    """Span of scheduled starts: how long the load was offered."""
    from datetime import datetime
    ts = [datetime.fromisoformat(r["start_time"].replace("Z", "+00:00")) for r in rows]
    return (max(ts) - min(ts)).total_seconds()


def table_2a(rows):
    out = ["| Scenario | Real or stand-in | Requests sent | Duration (s) | Offered rate (per min) | First token p50 / p95 / p99 (ms) | Total time p50 / p95 / p99 (ms) |",
           "|---|---|---|---|---|---|---|"]
    for (scen, src), rs in by(rows, lambda r: (r["scenario"], r["source"])).items():
        d = duration_s(rs)
        rate = NM if d == 0 else f"{60 * len(rs) / d:.1f}"
        first = [num(r, "first_token_ms") for r in rs if num(r, "first_token_ms") is not None]
        total = [num(r, "total_ms") for r in rs]
        out.append(f"| {scen} | {src} | {len(rs)} | {d:.0f} | {rate} | {triple(first)} | {triple(total)} |")
    return out


def answered(r):
    """A model answer was produced and shown (not a refusal, L2 or L3)."""
    return r["status"] == "ok" and int(num(r, "tokens_out") or 0) > 0


def table_2b(rows):
    out = ["| Scenario | Succeeded % | Rate-limited % | Timed out or given up % | Retries per request | Peak queue length | Tokens per answer | Cost per 1,000 answers |",
           "|---|---|---|---|---|---|---|---|"]
    for (scen, src), rs in by(rows, lambda r: (r["scenario"], r["source"])).items():
        n = len(rs)
        status = defaultdict(int)
        for r in rs:
            status[r["status"]] += 1
        retries = sum(num(r, "retries") or 0 for r in rs) / n
        peak = max(int(num(r, "queue_len_at_arrival") or 0) for r in rs)
        ans = [r for r in rs if answered(r)]
        tokens = NM if not ans else f"{sum(num(r, 'tokens_in') + num(r, 'tokens_out') for r in ans) / len(ans):.0f}"
        out.append(f"| {scen} ({src}) | {share(status['ok'], n)} | {share(status['rate_limited'], n)} | "
                   f"{share(status['timeout'], n)} | {retries:.2f} | {peak} | {tokens} | {cost_per_1000(ans)} |")
    return out


def table_3(rows):
    out = ["| Run, setting | Questions asked | Correct and cited % | Wrong % | Correctly refused % | Tokens per answer | First token p50 / p95 (ms) | Total time p50 / p95 (ms) |",
           "|---|---|---|---|---|---|---|---|"]
    evals = [r for r in rows if r["answer_grade"]]
    # Grouped by run as well as setting: tuning rows (eval-dev) and
    # reported rows (eval-heldout) must never be averaged together.
    for (scen, setting), rs in by(evals, lambda r: (r["scenario"], r["retrieval_setting"])).items():
        n = len(rs)
        g = defaultdict(int)
        for r in rs:
            g[r["answer_grade"]] += 1
        ans = [r for r in rs if answered(r)]
        tokens = NM if not ans else f"{sum(num(r, 'tokens_in') + num(r, 'tokens_out') for r in ans) / len(ans):.0f}"
        first = [num(r, "first_token_ms") for r in rs if num(r, "first_token_ms") is not None]
        total = [num(r, "total_ms") for r in rs]
        out.append(f"| {scen}, {setting} | {n} | {share(g['correct'], n)} | {share(g['wrong'], n)} | {share(g['refused_correctly'], n)} | "
                   f"{tokens} | {triple(first, (50, 95))} | {triple(total, (50, 95))} |")
    return out


def slowest(rows):
    """Where the time goes: the slowest request per scenario, split into
    waiting (queue), working (model) and the rest (retries' backoff and
    our own overhead)."""
    out = ["| Scenario | Request | Total (ms) | Queue wait (ms) | Model (ms) | Other: backoff + overhead (ms) | Retries | Status |",
           "|---|---|---|---|---|---|---|---|"]
    for scen, rs in by(rows, lambda r: r["scenario"]).items():
        r = max(rs, key=lambda r: num(r, "total_ms"))
        total, queue, model = (num(r, c) for c in ("total_ms", "queue_wait_ms", "model_ms"))
        out.append(f"| {scen} | {r['request_id']} | {total:.0f} | {queue:.0f} | {model:.0f} | {total - queue - model:.0f} | {r['retries']} | {r['status']} |")
    return out


def main(path="results/results.csv"):
    rows = load(path)
    load_rows = [r for r in rows if not r["answer_grade"]]
    for title, lines in [("Table 2a: Latency per load scenario", table_2a(load_rows)),
                         ("Table 2b: Outcomes per load scenario", table_2b(load_rows)),
                         ("Table 3: Retrieval settings (graded eval rows)", table_3(rows)),
                         ("Where the time goes: slowest request per scenario", slowest(rows))]:
        print(f"\n### {title}\n")
        print("\n".join(lines))


if __name__ == "__main__":
    main(*sys.argv[1:2])
