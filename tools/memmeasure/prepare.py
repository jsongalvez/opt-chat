#!/usr/bin/env python3
"""Make two copies of a chat for measuring subagent memory designs.

  a: subagents' chats are trees of their own (agents/<Name>.mem), as PR #6.
  b: subagents' steps are messages of kind "step" in the main chat.

Both copies hold the chat's messages before --start with their tree, cut
there. replay.jsonl in each holds the rest up to --end, for opt-chat
-replay to feed in, with the same probe points in the chat's time. In b
the named subagents' steps are merged in by date and their logs removed,
so nothing but the main chat holds them.

With --prebuilt OLD (an OUT whose replays ran to the end), each copy gets
OLD's whole tree, and a gets OLD's subagent chats: a replay then builds
nothing, and each probe sees every line before it built, as the views grow
message by message. That splits the summarizer's cost (the first run) from
the probes' (the second), and a usage limit mid-build spoils neither.

Usage: prepare.py SRC OUT --start 13312 --end 13948 --agents A,B --probes 10 [--prebuilt OLD]
"""
import argparse
import glob
import json
import os
import shutil

PROBE = "Probe: reply with the one word ok, and call no tools."


def read_jsonl(paths):
    for p in paths:
        with open(p) as f:
            for line in f:
                if line.strip():
                    yield json.loads(line)


def write_by_day(dirpath, rows, day):
    os.makedirs(dirpath, exist_ok=True)
    out = {}
    for r in rows:
        out.setdefault(day(r), []).append(r)
    for d, rs in out.items():
        with open(os.path.join(dirpath, d + ".jsonl"), "w") as f:
            for r in rs:
                f.write(json.dumps(r, ensure_ascii=False) + "\n")


def copy_chat(src, dst, start):
    ignore = shutil.ignore_patterns("web.json", "lock", "wake.jsonl", "resume.json", "view.json", "usage", "main", "tree", "agents", "opt-chat*")
    shutil.copytree(src, dst, ignore=ignore)
    msgs = [m for m in read_jsonl(sorted(glob.glob(f"{src}/main/*.jsonl"))) if m["i"] < start]
    write_by_day(f"{dst}/main", msgs, lambda m: m["date"][:10])
    days = {m["i"]: m["date"][:10] for m in msgs}
    # A line marked to redo would be built again in the copy, across the
    # whole history: the copy keeps its text as built instead.
    nodes = [n for n in read_jsonl(sorted(glob.glob(f"{src}/tree/*.jsonl"))) if (n["i"] + 1) << n["l"] <= start and not n.get("redo")]
    write_by_day(f"{dst}/tree", nodes, lambda n: days[n["i"] << n["l"]])
    os.makedirs(f"{dst}/usage")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("src")
    ap.add_argument("out")
    ap.add_argument("--start", type=int, required=True)
    ap.add_argument("--end", type=int, required=True)
    ap.add_argument("--agents", required=True)
    ap.add_argument("--probes", type=int, default=10)
    ap.add_argument("--prebuilt")
    o = ap.parse_args()
    if o.start & 1023:
        raise SystemExit("--start must be a multiple of 1024, so the cut tree is whole")
    names = o.agents.split(",")
    src = os.path.expanduser(o.src)
    tail = [m for m in read_jsonl(sorted(glob.glob(f"{src}/main/*.jsonl"))) if o.start <= m["i"] < o.end]
    tail.sort(key=lambda m: m["i"])
    steps = []
    for n in names:
        for m in read_jsonl(sorted(glob.glob(f"{src}/agents/{n}.mem/main/*.jsonl"))):
            text = m["text"] if m.get("part") else f"[{n}] {m['kind']}: {m['text']}"
            steps.append({"kind": "step", "text": text, "date": m["date"], "part": m.get("part", 0), "agent": n})
    # Merge by date; a main message goes first at a tie, and a subagent's
    # steps keep their order.
    timeline = [(m["date"], 0, k, m) for k, m in enumerate(tail)] + [(s["date"], 1, k, s) for k, s in enumerate(steps)]
    timeline.sort(key=lambda t: (t[0], t[1], t[2]))
    first_step = next(k for k, t in enumerate(timeline) if t[1] == 1)
    span = len(timeline) - first_step
    at = {first_step + span * (j + 1) // o.probes for j in range(o.probes)}

    for design in "ab":
        dst = f"{o.out}/{design}"
        copy_chat(src, dst, o.start)
        os.makedirs(f"{dst}/agents")
        for p in glob.glob(f"{src}/agents/*"):
            base = os.path.basename(p)
            name = base.split(".")[0]
            if base.endswith(".mem") and (name in names or design == "a"):
                continue  # a builds the named ones again; b holds no trees
            if name in names and design == "b" and base.endswith(".log"):
                continue
            (shutil.copytree if os.path.isdir(p) else shutil.copy2)(p, f"{dst}/agents/{base}")
        if o.prebuilt:
            shutil.rmtree(f"{dst}/tree")
            shutil.copytree(f"{o.prebuilt}/{design}/tree", f"{dst}/tree")
            for n in names if design == "a" else []:
                shutil.copytree(f"{o.prebuilt}/a/agents/{n}.mem", f"{dst}/agents/{n}.mem", ignore=shutil.ignore_patterns("lock", "usage"))
        with open(f"{o.out}/{design}.replay.jsonl", "w") as f:
            for k, (_, is_step, _, m) in enumerate(timeline):
                if k in at:
                    f.write(json.dumps({"probe": PROBE}) + "\n")
                if is_step and design == "a":
                    continue
                row = {kk: v for kk, v in m.items() if kk not in ("i", "size", "agent")}
                f.write(json.dumps(row, ensure_ascii=False) + "\n")
            if len(timeline) in at:
                f.write(json.dumps({"probe": PROBE}) + "\n")
    print(json.dumps({"main": len(tail), "steps": len(steps), "probes": o.probes, "firstStepAt": first_step}))


if __name__ == "__main__":
    main()
