#!/usr/bin/env python3
"""Replay the main view's sawtooth over a chat's summary tree, message by message.

Reimplements view.go (push, check, fit, mostDue) with every line built as it
arrives, so it shows the size the view settles to, not a backlog's. Reads
<dir>/tree/*.jsonl; writes nothing. Run with python3 -I.

  viewsize.py DIR [--lo 64000 --hi 128000] [--policy sawtooth|oldest]
              [--every 50] [--tsv out.tsv] [--compare-live]

--policy oldest merges the oldest sibling pair first instead of the most
due one (a variant, not what view.go does). --compare-live checks the
replayed final keys against DIR/view.json. Token figures use the fit from
MemMeasure's probes (tokens ~= FIXED + view bytes / BYTES_PER_TOKEN), an
estimate for this chat's text, not a tokenizer count.
"""
import argparse, glob, json, os, statistics, sys

FIXED, BYTES_PER_TOKEN = 36_500, 2.13  # fit over 23 MemMeasure probes, max residual 1.7k tokens
CALL_EVERY = 6  # this chat: 2,491 user messages in 13,988
CHECKPOINT = 64  # claude.go checkpoint: the first message ends on a multiple of it


def load_tree(d):
    tree = {}
    for f in sorted(glob.glob(os.path.join(d, "tree", "*.jsonl"))):
        for raw in open(f, encoding="utf-8"):
            try:
                r = json.loads(raw)
            except ValueError:
                continue
            if "l" in r and "i" in r and "text" in r:
                tree[(r["l"], r["i"])] = r["text"]
    return tree


def name(k):
    l, i = k
    return f"{i << l}+{1 << l}"


def end(k):
    return (k[1] + 1) << k[0]


def flat(s):
    s = s.replace("\n", " ")
    for a, b in (("<chat>", "‹chat›"), ("</chat>", "‹/chat›"), ("<input>", "‹input›"), ("</input>", "‹/input›")):
        s = s.replace(a, b)
    return s


class Replay:
    def __init__(self, tree, lo, hi, policy):
        self.tree, self.lo, self.hi, self.policy = tree, lo, hi, policy
        self.keys, self.bytes, self.shrinking = [], 0, False

    def cost(self, k):
        t = self.tree.get(k)
        return 0 if t is None else len((name(k) + "|" + flat(t)).encode()) + 1

    def pick(self):
        T = end(self.keys[-1])
        best, best_due = -1, 0.0
        for j in range(len(self.keys) - 1):
            a, b = self.keys[j], self.keys[j + 1]
            if not (a[0] == b[0] and a[1] % 2 == 0 and b[1] == a[1] + 1):
                continue
            if self.policy == "oldest":
                best = j
                break
            due = (T - (end(b) - 1)) / (1 << b[0])
            if best < 0 or due > best_due:
                best, best_due = j, due
        if best >= 0 and (self.keys[best][0] + 1, self.keys[best][1] // 2) not in self.tree:
            return -1
        return best

    def push(self, k):
        self.keys.append(k)
        self.bytes += self.cost(k)
        if self.bytes > self.hi:
            self.shrinking = True
        first_changed = None
        while self.shrinking and self.bytes > self.lo:
            j = self.pick()
            if j < 0:
                break
            a, b = self.keys[j], self.keys[j + 1]
            p = (a[0] + 1, a[1] // 2)
            self.bytes += self.cost(p) - self.cost(a) - self.cost(b)
            self.keys[j : j + 2] = [p]
            first_changed = j if first_changed is None else min(first_changed, j)
        else:
            self.shrinking = False
        return first_changed


def tokens(b):
    return FIXED + b / BYTES_PER_TOKEN


def run(tree, lo, hi, policy, every, tsv):
    n = 1 + max(i for (l, i) in tree if l == 0)
    v = Replay(tree, lo, hi, policy)
    rows, batches, rewrite = [], [], []
    prev_cut = 0
    for i in range(n):
        changed = v.push((0, i))
        cut = len(v.keys) // CHECKPOINT * CHECKPOINT
        # bytes a call must write anew: from the first changed line, or from the
        # cut if the first message stayed the same (an estimate: see docstring)
        if changed is not None:
            batches.append(i)
            start = min(changed, prev_cut)
        else:
            start = prev_cut if cut >= prev_cut else cut
        rewrite.append(sum(v.cost(k) for k in v.keys[start:]))
        prev_cut = cut
        l0 = sum(1 for k in v.keys if k[0] == 0)
        rows.append((i, v.bytes, len(v.keys), l0, max(k[0] for k in v.keys), int(changed is not None)))
    if tsv:
        with open(tsv, "w") as f:
            f.write("msg\tbytes\tlines\tverbatim_lines\tmax_level\tbatch\n")
            for r in rows:
                f.write("\t".join(map(str, r)) + "\n")
    tail = rows[len(rows) // 4 :]  # past the first fill
    sizes = [r[1] for r in tail]
    gaps = [b - a for a, b in zip(batches, batches[1:])]
    print(f"policy={policy} lo={lo} hi={hi} messages={n}")
    print(f"  view bytes (last 3/4): min {min(sizes)} mean {statistics.mean(sizes):.0f} max {max(sizes)}")
    print(f"  est. call tokens: min {tokens(min(sizes)):.0f} mean {tokens(statistics.mean(sizes)):.0f} max {tokens(max(sizes)):.0f}")
    print(f"  merge batches: {len(batches)}, messages between them: {gaps[-6:] if gaps else '-'}")
    rw = rewrite[len(rewrite) // 4 :]
    print(f"  est. bytes written anew per call: mean {statistics.mean(rw):.0f}, median {statistics.median(rw):.0f}")
    # a turn every CALL_EVERY messages: cache reads cost 0.1, writes 1.25 of an input token
    calls = range(len(rows) // 4, len(rows), CALL_EVERY)
    unit = []
    for c in calls:
        w = max(rewrite[j] for j in range(max(0, c - CALL_EVERY + 1), c + 1))
        view = rows[c][1]
        unit.append((FIXED * 0.1 + (view - w) / BYTES_PER_TOKEN * 0.1 + w / BYTES_PER_TOKEN * 1.25))
    print(f"  est. input cost per turn (input-token units, a turn per {CALL_EVERY} msgs): mean {statistics.mean(unit):.0f}")
    print(f"  final: {len(v.keys)} lines, {rows[-1][3]} verbatim, top level {rows[-1][4]}")
    if every:
        print("  msg   bytes  lines  batch")
        for r in rows[:: every]:
            print(f"  {r[0]:5d} {r[1]:7d} {r[2]:5d}  {'*' if r[5] else ''}")
    return v, batches


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("dir")
    ap.add_argument("--lo", type=int, default=64_000)
    ap.add_argument("--hi", type=int, default=128_000)
    ap.add_argument("--policy", choices=["sawtooth", "oldest"], default="sawtooth")
    ap.add_argument("--every", type=int, default=0)
    ap.add_argument("--tsv")
    ap.add_argument("--compare-live", action="store_true")
    a = ap.parse_args()
    tree = load_tree(a.dir)
    if not tree:
        sys.exit("no tree/*.jsonl under " + a.dir)
    v, _ = run(tree, a.lo, a.hi, a.policy, a.every, a.tsv)
    if a.compare_live:
        live = [tuple(k) for k in json.load(open(os.path.join(a.dir, "view.json")))]
        same = sum(1 for x, y in zip(live, v.keys) if x == y)
        lb = sum(v.cost(k) for k in live)
        print(f"  live view.json: {len(live)} lines, {lb} bytes; replay {len(v.keys)} lines; {same} leading lines identical"
              f" (replay ends at msg {end(v.keys[-1])}, live at {end(live[-1])})")


if __name__ == "__main__":
    main()
