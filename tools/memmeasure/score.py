#!/usr/bin/env python3
"""Tabulate a memmeasure run: summarizer spend, view shape, probe cache
hits and recall answers, per design.

Usage: score.py DATA   (DATA as prepare.py's OUT, after run.sh)
"""
import glob
import json
import os
import sys


def rows(path):
    if not os.path.exists(path):
        return []
    with open(path) as f:
        return [json.loads(l) for l in f if l.strip()]


def summarizer(d):
    files = glob.glob(f"{d}/usage/*.jsonl") + glob.glob(f"{d}/agents/*.mem/usage/*.jsonl")
    calls = cost = 0
    tok = {"in": 0, "write": 0, "read": 0, "out": 0}
    for f in files:
        for r in rows(f):
            if r.get("role") != "compaction":
                continue
            calls += 1
            cost += r["cost"]
            for m in r.get("models", {}).values():
                tok["in"] += m.get("inputTokens", 0)
                tok["write"] += m.get("cacheCreationInputTokens", 0)
                tok["read"] += m.get("cacheReadInputTokens", 0)
                tok["out"] += m.get("outputTokens", 0)
    return calls, cost, tok


def tokens(u):
    return u.get("input_tokens", 0), u.get("cache_creation_input_tokens", 0), u.get("cache_read_input_tokens", 0), u.get("output_tokens", 0)


def main():
    data = sys.argv[1]
    for design in "ab":
        d = f"{data}/{design}"
        out = rows(f"{data}/{design}.out.jsonl")
        probes = [r for r in out if "probe" in r]
        final = next((r for r in out if r.get("final")), {})
        calls, cost, tok = summarizer(d)
        print(f"== design {design}")
        print(f"summarizer: {calls} compactions, ${cost:.3f}, tokens {tok}; build secs main {final.get('mainBuildSecs', 0):.0f}, agents {final.get('agentBuildSecs', 0):.0f}; failing {final.get('failing')}")
        print(f"final view: {final.get('viewLines')} lines, {final.get('viewBytes')} B (call view {final.get('callViewBytes')} B), {final.get('msgs')} msgs; last 50 user msgs: levels {final.get('last50UserLevels')}, mean msgs per line {final.get('last50UserMeanCover', 0):.1f}")
        pc = 0.0
        rd = wr = inp = 0
        for p in probes:
            ai, aw, ar, _ = tokens(p.get("ack", {}))
            ui, uw, ur, uo = tokens(p.get("usage", {}))
            pc += p.get("cost", 0)
            if p["probe"] > 1:
                rd, wr, inp = rd + ar + ur, wr + aw + uw, inp + ai + ui
            print(f"  probe {p['probe']:2} msg {p['before']:5} view {p['viewBytes']:6} B {p['viewLines']:3} lines | ack in {ai} write {aw} read {ar} | answer write {uw} read {ur} out {uo} | ${p.get('cost', 0):.3f}")
        if probes:
            tot = rd + wr + inp
            print(f"  probes 2..n: cache read {rd}, write {wr}, uncached {inp}: hit rate {rd / max(tot, 1):.1%}; probe spend ${pc:.3f}")
        ev = rows(f"{data}/{design}.eval.jsonl")
        if ev:
            for kind in ("sub", "user"):
                qs = [q for q in ev if q["type"] == kind]
                ok = [q for q in qs if any(g.lower() in (q.get("answer") or "").lower() for g in q["gold"])]
                n = max(len(qs), 1)
                ec = sum(q.get("cost", 0) for q in qs)
                tools = sum(len(q.get("tools") or []) for q in qs)
                secs = sum(q.get("secs", 0) for q in qs)
                agg = [0, 0, 0, 0]
                for q in qs:
                    for src in ("ack", "usage"):
                        for i, v in enumerate(tokens(q.get(src) or {})):
                            agg[i] += v
                print(f"  recall {kind}: {len(ok)}/{len(qs)} right; tools {tools / n:.1f}/q; ${ec / n:.3f}/q; {secs / n:.0f}s/q; tokens in {agg[0] // n} write {agg[1] // n} read {agg[2] // n} out {agg[3] // n} per q")
            for q in sorted(ev, key=lambda q: q["id"]):
                right = any(g.lower() in (q.get("answer") or "").lower() for g in q["gold"])
                print(f"    q{q['id']:2} {q['type']:4} {'RIGHT' if right else 'wrong'} tools {len(q.get('tools') or []):2} ${q.get('cost', 0):.3f} {q.get('secs', 0):.0f}s {q.get('err', '')}")


if __name__ == "__main__":
    main()
