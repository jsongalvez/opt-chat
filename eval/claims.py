"""Score -eval-compact lines claim by claim with a judge model, and run the
claim check (a second call that drops what a line's input doesn't state).

    python eval/claims.py judge MODEL lines.jsonl [base.jsonl]  > graded.jsonl
    python eval/claims.py check MODEL lines.jsonl               > checked.jsonl

judge lists each line's facts its input doesn't hold (not framing, not ids
or names that only identify what the input refers to), and, given the same
nodes' lines from base, the facts the base line took from the input that
this line drops. check rewrites each line without the claims its input
doesn't support. See docs/2026-10-23-faithful-lines.md.
"""
import json, subprocess, sys
from concurrent.futures import ThreadPoolExecutor

JUDGE = """You check one line of an AI assistant's memory against the input it was written from. The writer also saw the earlier chat as context, which you don't see.

1. "added": each fact in the line that the input doesn't state or directly show: a status, outcome, purpose, verdict, list item, or detail about other work. Don't list: framing ("output not shown", "no result here", "unchecked"), a paraphrase of what the input says, or an id, name, path or number that only identifies what the input refers to. A fact the input holds anywhere, including inside a command or its description, is not added.
2. "lost": only if a BASE line is given: each fact the base line states that the input supports and the line leaves out. Ignore wording.

Output only JSON: {"added": ["short quote", ...], "lost": ["short quote", ...]}"""

CHECK = """You correct one line of an AI assistant's memory. The line was written from the input below, with earlier chat as context, and may state facts from that context that the input doesn't hold.

Split the line into claims. Keep each claim that the input states or shows, and each id, name or path that only identifies what the input refers to, or which request a step serves. Drop each other claim: a status, outcome, purpose, verdict, list of owed or open items, or detail about other work that the input doesn't state. Keep the kept claims' words, tags and order. Output only the corrected line, nothing else."""


def call(model, system, prompt):
    for _ in range(3):
        p = subprocess.run(
            ["claude", "-p", "--model", model, "--tools", "", "--system-prompt", system, "--output-format", "json",
             "--settings", '{"disableAllHooks":true}', "--no-session-persistence", prompt],
            capture_output=True, text=True, timeout=300)
        try:
            out = json.loads(p.stdout)
            return out["result"].strip(), out.get("total_cost_usd", 0)
        except Exception:
            continue
    return None, 0


def rows(path):
    return [r for r in (json.loads(l) for l in open(path) if l.strip()) if r.get("line")]


def judge(model, r, base):
    prompt = f"<input>\n{r['input']}\n</input>\n<line>\n{r['line']}\n</line>"
    if base:
        prompt += f"\n<base>\n{base}\n</base>"
    for _ in range(2):
        out, cost = call(model, JUDGE, prompt)
        try:
            s, e = out.find("{"), out.rfind("}")
            j = json.loads(out[s:e + 1])
            return {"i": r["i"], "line": r["line"], "added": j.get("added", []), "lost": j.get("lost", []) if base else [], "judge_cost": cost}
        except Exception:
            continue
    return {"i": r["i"], "line": r["line"], "added": None, "lost": None, "judge_cost": 0}


def check(model, r):
    out, cost = call(model, CHECK, f"<input>\n{r['input']}\n</input>\n<line>\n{r['line']}\n</line>")
    return {**r, "line": out or r["line"], "unchecked": r["line"], "check_cost": cost}


def main():
    mode, model, path = sys.argv[1:4]
    rs = rows(path)
    with ThreadPoolExecutor(8) as ex:
        if mode == "judge":
            base = {r["i"]: r["line"] for r in rows(sys.argv[4])} if len(sys.argv) > 4 else {}
            out = ex.map(lambda r: judge(model, r, base.get(r["i"])), rs)
        else:
            out = ex.map(lambda r: check(model, r), rs)
        for o in out:
            print(json.dumps(o), flush=True)


main()
