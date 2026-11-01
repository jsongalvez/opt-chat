#!/usr/bin/env python3
"""Compare designs for subagent memory on a real opt-chat data dir.

  python3 -I tools/memdesign/sim.py [~/.opt-chat]

(a) separate: each subagent's chat is its own tree; zoom("Name") gives its
    16-32 KB view, then zooms down.
(b) merged: subagent steps are messages of the main chat, in time order.
(c) attached: each subagent's tree hangs under its report line; zoom on the
    report opens its top lines, then zooms down two lines at a time.
(e) search: a search reaches a step's own line in one call, when you know
    a word of it.

Line sizes are modelled, not summarized: a leaf costs its text up to 512
bytes, a merged line the mean size of its level in the main tree on disk.
The view is the main chat's sawtooth (merge most due pairs to lo past hi).
"""
import bisect, glob, json, os, re, statistics as st, sys
from collections import defaultdict

D = os.path.expanduser(sys.argv[1] if len(sys.argv) > 1 else "~/.opt-chat")
STEP = re.compile(r"(?m)^(task|on|opt|user|agent|thinking|tool|echo|loop|compacted|cut)(?:@(\d+)(?:#(\S+?))?)?: ")
CLIP = 30_000


def level_sizes():
    s = defaultdict(list)
    for f in glob.glob(f"{D}/tree/*.jsonl"):
        for l in open(f):
            try:
                m = json.loads(l)
            except ValueError:
                continue
            s[m["l"]].append(m.get("size", len(m["text"])))
    return {k: st.mean(v) + 1 for k, v in s.items()}


LV = level_sizes()


def pcost(L):
    return LV.get(L, 495)


def leaf(n):
    return min(n, 512) + 8


def main_msgs():
    out = []
    for f in sorted(glob.glob(f"{D}/main/*.jsonl")):
        for l in open(f):
            try:
                m = json.loads(l)
            except ValueError:
                continue
            out.append(m)
    return out


def iso_ms(s):
    from datetime import datetime
    return int(datetime.fromisoformat(s).timestamp() * 1000)


def agent_parts():
    """{name: [(ms, bytes)]}, steps split as the feed splits them."""
    out = {}
    for f in glob.glob(f"{D}/agents/*.log"):
        b = open(f, encoding="utf-8", errors="replace").read()
        heads = [m for m in STEP.finditer(b) if m.start() == 0 or b[: m.start()].endswith("\n\n")]
        parts, last = [], 0
        for j, m in enumerate(heads):
            end = heads[j + 1].start() if j + 1 < len(heads) else len(b)
            if m.group(1) == "thinking":
                continue
            at = int(m.group(2)) if m.group(2) else last
            last = at
            t = b[m.end():end]
            for k in range(0, max(len(t), 1), CLIP):
                parts.append((at, len(t[k:k + CLIP].encode())))
        if parts:
            out[os.path.basename(f)[:-4]] = parts
    return out


class View:
    def __init__(self, lo, hi):
        self.lo, self.hi, self.keys, self.bytes = lo, hi, [], 0

    def cost(self, k):
        return self.leafc[k[1]] if k[0] == 0 else pcost(k[0])

    def push(self, i, c):
        self.leafc[i] = c
        self.keys.append((0, i))
        self.bytes += c
        if self.bytes > self.hi:
            self.batches = getattr(self, "batches", 0) + 1
            while self.bytes > self.lo:
                T = self.keys[-1][1] + 1 if self.keys[-1][0] == 0 else (self.keys[-1][1] + 1) << self.keys[-1][0]
                best, bd = -1, -1.0
                for j in range(len(self.keys) - 1):
                    a, b = self.keys[j], self.keys[j + 1]
                    if a[0] == b[0] and a[1] % 2 == 0 and b[1] == a[1] + 1:
                        d = (T - (((b[1] + 1) << b[0]) - 1)) / (1 << b[0])
                        if d > bd:
                            best, bd = j, d
                if best < 0:
                    break
                a, b = self.keys[best], self.keys[best + 1]
                p = (a[0] + 1, a[1] // 2)
                self.bytes += pcost(p[0]) - self.cost(a) - self.cost(b)
                self.keys[best:best + 2] = [p]

    def level_of(self, i):
        starts = [k[1] << k[0] for k in self.keys]
        j = bisect.bisect_right(starts, i) - 1
        return self.keys[j][0]


def build(seq, lo, hi):
    v = View(lo, hi)
    v.leafc = {}
    for i, c in enumerate(seq):
        v.push(i, c)
    return v


def tops(n):
    """The fewest aligned lines tiling n messages: (level, first message)."""
    out, s = [], 0
    for L in range(n.bit_length() - 1, -1, -1):
        if n >> L & 1:
            out.append((L, s))
            s += 1 << L
    return out


def reach_c(n, i):
    t = tops(n)
    for L, s in t:
        if s <= i < s + (1 << L):
            entry = sum(pcost(x) if x else 400 for x, _ in t)
            return 1 + L, entry + L * 2 * pcost(max(L - 1, 0))


def main():
    ms = main_msgs()
    ag = agent_parts()
    mt = [iso_ms(m["date"]) if m.get("date") else 0 for m in ms]
    for j in range(1, len(mt)):
        mt[j] = mt[j] or mt[j - 1]

    seqA = [leaf(len(m["text"].encode())) for m in ms]
    vA = build(seqA, 64_000, 128_000)

    ev = [(t, 0, j) for j, t in enumerate(mt)]
    for name, ps in ag.items():
        for k, (t, n) in enumerate(ps):
            ev.append((t, 1, (name, k, n)))
    ev.sort(key=lambda e: (e[0], e[1]))
    seqB, origin, pos_of_main, pos_of_step = [], [], {}, {}
    for t, kind, x in ev:
        if kind == 0:
            pos_of_main[x] = len(seqB)
            seqB.append(leaf(len(ms[x]["text"].encode())))
            origin.append("main")
        else:
            pos_of_step[(x[0], x[1])] = len(seqB)
            seqB.append(leaf(x[2]))
            origin.append("sub")
    vB = build(seqB, 64_000, 128_000)

    print(f"data: {len(ms)} main messages, {sum(len(p) for p in ag.values())} subagent messages in {len(ag)} logs")
    users = [j for j, m in enumerate(ms) if m.get("kind") == "user"]
    for label, v, pos in (("a/c main tree", vA, lambda j: j), ("b merged", vB, lambda j: pos_of_main[j])):
        ls = [v.level_of(pos(j)) for j in users]
        recent = ls[-50:]
        print(f"{label}: view {v.bytes/1000:.0f} KB in {len(v.keys)} lines; "
              f"user msgs at level 0: {sum(1 for x in ls if x == 0)}/{len(ls)}; "
              f"last 50 user msgs mean level {st.mean(recent):.1f}, max {max(recent)}; "
              f"oldest line level {v.keys[0][0]}; merge batches {getattr(v,'batches',0)} "
              f"(each rewrites the view: the prompt cache misses); a user message shares its line with "
              f"{st.mean([1 << v.level_of(pos(j)) for j in users[-50:]]):.0f} messages (last 50)")
        if v is vB:
            sub_lines = 0
            for L, I in v.keys:
                span = origin[I << L:(I + 1) << L]
                if all(o == "sub" for o in span):
                    sub_lines += 1
            print(f"   b: {sub_lines}/{len(v.keys)} view lines are subagent steps only; "
                  f"{len(seqB)/len(seqA):.1f}x the leaves; a user message shares its line with "
                  f"{st.mean([ (1<<v.level_of(pos_of_main[j])) for j in users[-50:]]) :.0f} messages on average (last 50)")

    C = int(os.environ.get("CTX_TOKENS", 60_000))
    print(f"\nentry width sweep (a and c are its ends): mean calls to a step's own line, and cost in "
          f"token-units = calls*{C}*0.1 (cache read) + entry tokens*1.25 + entry kept in later calls")
    for name in ("AuthTable", "Hotspot", "SecRoles"):
        ps = ag[name]
        row = []
        for E in (1_000, 4_000, 8_000, 16_000, 32_000, 64_000):
            if E == 1_000:
                calls = [r[0] for r in (reach_c(len(ps), i) for i in range(len(ps)))]
                eb = sum(pcost(x) for x, _ in tops(len(ps)))
            else:
                v = build([leaf(x) for _, x in ps], E // 2, E)
                calls = [1 + v.level_of(i) for i in range(len(ps))]
                eb = v.bytes
            k = st.mean(calls)
            et = eb / 4
            cost = k * C * 0.1 + et * 1.25 + et * 0.1 * (k - 1) + (k - 1) * 2 * 120
            row.append(f"{E//1000:>2}KB {k:4.1f}c {cost/1000:5.1f}k")
        print(f"  {name:10} " + " | ".join(row))

    print("\nreach one step of a finished subagent: calls (zooms) and bytes returned, mean over its steps")
    for name in sorted(ag, key=lambda n: -len(ag[n]))[:6] + ["SecRoles", "LaunchReset"]:
        ps = ag[name]
        n = len(ps)
        va = build([leaf(x) for _, x in ps], 16_000, 32_000)
        ra = [(1 + va.level_of(i), va.bytes + va.level_of(i) * 2 * pcost(max(va.level_of(i) - 1, 0))) for i in range(n)]
        rc = [reach_c(n, i) for i in range(n)]
        rb = []
        for i in range(n):
            L = vB.level_of(pos_of_step[(name, i)])
            rb.append((L, L * 2 * pcost(max(L - 1, 0))))
        f = lambda r: f"{st.mean(x for x,_ in r):4.1f} calls {st.mean(y for _,y in r)/1000:5.1f} KB"
        print(f"  {name:14} {n:4} msgs | a {f(ra)} | b {f(rb)} | c {f(rc)} | e 1-2 calls ~1 KB")


if __name__ == "__main__":
    main()
