#!/usr/bin/env python3
"""Where opt-chat's spend goes, from its own usage log.

Usage: python3 -I tools/perf/usage_report.py [CHAT_DIR] [--since YYYY-MM-DD]
CHAT_DIR defaults to ~/.opt-chat. Reads CHAT_DIR/usage/*.jsonl only.

Prices are fitted per model from the logged costUSD by least squares over
input, cache write, cache read and output tokens, so the split needs no price
table. A fit error near 0 means the four rates explain every record exactly.
"""
import glob
import json
import os
import sys
from collections import Counter
from datetime import datetime

K = ["inputTokens", "cacheCreationInputTokens", "cacheReadInputTokens", "outputTokens"]
BIG_WRITE = 40_000


def load(chat, since):
    out = []
    for f in sorted(glob.glob(os.path.join(chat, "usage", "*.jsonl"))):
        if since and os.path.basename(f)[:10] < since:
            continue
        for line in open(f):
            try:
                out.append(json.loads(line))
            except ValueError:
                pass
    return out


def solve(a, b):
    n = len(b)
    m = [a[i][:] + [b[i]] for i in range(n)]
    for i in range(n):
        p = max(range(i, n), key=lambda r: abs(m[r][i]))
        m[i], m[p] = m[p], m[i]
        if m[i][i] == 0:
            return None
        for r in range(n):
            if r != i:
                f = m[r][i] / m[i][i]
                m[r] = [x - f * y for x, y in zip(m[r], m[i])]
    return [m[i][n] / m[i][i] for i in range(n)]


def fit(recs):
    rates = {}
    for model in Counter(m for r in recs for m in r["models"]):
        rows = [([r["models"][model].get(k, 0) / 1e6 for k in K], r["models"][model]["costUSD"])
                for r in recs if model in r["models"] and not r["models"][model].get("webSearchRequests")]
        if len(rows) < 50:  # too few to fit four rates
            continue
        a = [[sum(x[i] * x[j] for x, _ in rows) for j in range(4)] for i in range(4)]
        c = solve(a, [sum(x[i] * y for x, y in rows) for i in range(4)])
        if not c:
            continue
        errs = sorted(abs(sum(p * w for p, w in zip(x, c)) - y) / y for x, y in rows if y > 0)
        rates[model] = c
        print(f"{model}: n={len(rows)} $/Mtok in {c[0]:.3g} write {c[1]:.3g} read {c[2]:.3g} out {c[3]:.3g}"
              f" (write/in {c[1] / c[0]:.2f}: 1.25 = 5-minute cache, 2 = 1-hour) fit err p50 {errs[len(errs) // 2]:.3f}")
    return rates


def tok(r, k):
    return sum(s.get(k, 0) for s in r["models"].values())


def ctx(r):
    return (tok(r, K[0]) + tok(r, K[1]) + tok(r, K[2])) / max(1, r["requests"])


def pct(xs, q):
    return xs[min(len(xs) - 1, int(len(xs) * q))] if xs else 0


def main():
    args = sys.argv[1:]
    since = None
    if "--since" in args:
        i = args.index("--since")
        since = args[i + 1]
        del args[i:i + 2]
    chat = args[0] if args else os.path.expanduser("~/.opt-chat")
    recs = load(chat, since)
    if not recs:
        sys.exit("no usage records")
    print(f"{len(recs)} records, {recs[0]['date'][:16]} to {recs[-1]['date'][:16]}\n")
    rates = fit(recs)
    print()
    for role in ["turn", "agent", "compaction", "btw"]:
        rs = [r for r in recs if r["role"] == role]
        if not rs:
            continue
        parts = [0.0] * 4
        for r in rs:
            for m, s in r["models"].items():
                for i, k in enumerate(K):
                    parts[i] += s.get(k, 0) * rates.get(m, [0] * 4)[i] / 1e6
        total = sum(r["cost"] for r in rs)
        share = " ".join(f"{n} {p / max(sum(parts), 1e-9):.0%}" for n, p in zip(["in", "write", "read", "out"], parts))
        c = sorted(ctx(r) for r in rs)
        print(f"{role}: records {len(rs)} requests {sum(r['requests'] for r in rs)} ${total:.2f}  [{share}]"
              f"  ctx/request p50 {pct(c, .5) / 1e3:.0f}k p90 {pct(c, .9) / 1e3:.0f}k")
        acks = sorted(r["apiMs"] for r in rs if r["requests"] == 1 and tok(r, "outputTokens") <= 8)
        if acks:
            print(f"   ack-like records (one request, <=8 output tokens) {len(acks)}: apiMs p50 {pct(acks, .5)} p90 {pct(acks, .9)}")
        if role == "agent":
            for th in (150e3, 200e3, 250e3):
                big = [r for r in rs if ctx(r) > th]
                print(f"   runs averaging >{th / 1e3:.0f}k context per request: {len(big)}, ${sum(r['cost'] for r in big):.2f}")
        if role == "turn":
            prev, by = None, {"after >60m idle": [0, 0.0], "within 60m": [0, 0.0]}
            for r in rs:
                t = datetime.fromisoformat(r["date"])
                w = tok(r, K[1])
                if w > BIG_WRITE and prev is not None:
                    k = "after >60m idle" if (t - prev).total_seconds() > 3600 else "within 60m"
                    wr = sum(s.get(K[1], 0) * rates.get(m, [0] * 4)[1] / 1e6 for m, s in r["models"].items())
                    by[k][0] += 1
                    by[k][1] += wr
                prev = t
            for k, (n, d) in by.items():
                print(f"   records writing >{BIG_WRITE // 1000}k to the cache {k}: {n}, write ${d:.2f}")


if __name__ == "__main__":
    main()
