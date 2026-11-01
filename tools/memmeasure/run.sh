#!/usr/bin/env bash
# Measure subagent memory designs a (a tree per subagent) and b (steps in
# the main chat) on a copy of a real chat, with the real summarizer and
# real turns. Spends real money: about $1 of Haiku and $10 of Opus for
# 636 messages, 1,303 steps and 18 questions. Never point it at a live chat.
#
#   tools/memmeasure/run.sh SRC DATA QUESTIONS START END AGENTS
#   e.g. run.sh ~/.opt-chat private/mm private/mm-questions.jsonl 13312 13948 A,B,C
#
# Gotchas: the build takes about 2 hours per design at 8 jobs and counts
# against the plan's usage; a limit mid-run is waited out (failed lines are
# tried again), but probes or questions it hits come back with "err": rerun
# them with prepare.py --prebuilt DATA into a fresh dir, which builds nothing.
# A prebuilt replay's views differ from a message-by-message build's (the
# view's merges depend on when lines arrive), so compare designs within one
# mode, never across.
#
# QUESTIONS: JSON lines {"id", "type": "sub"|"user", "q", "gold": [substrings]};
# drawn from a real chat, so keep it out of git (private/).
set -euo pipefail
src=$1 data=$2 questions=$3 start=$4 end=$5 agents=$6
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
bin="$data/opt-chat-mm"
case "$(realpath "$data")" in "$(realpath "$src")"*) echo "DATA must not be inside SRC" >&2; exit 1 ;; esac
go build -C "$repo" -o "$(realpath -m "$bin")" .
python3 -I "$here/prepare.py" "$src" "$data" --start "$start" --end "$end" --agents "$agents"
common=(-model opus -effort medium -compact-model haiku -jobs 8 -agent-jobs 8 -open=false -sounds=false -wake=false -auto-resume=false -resume-at-reset=false)
"$bin" "${common[@]}" -dir "$data/a" -replay "$data/a.replay.jsonl" -replay-out "$data/a.out.jsonl" -replay-agents "$agents" > "$data/a.run.log" 2>&1 &
"$bin" "${common[@]}" -agent-memory=false -agent-steps-in-main -dir "$data/b" -replay "$data/b.replay.jsonl" -replay-out "$data/b.out.jsonl" > "$data/b.run.log" 2>&1 &
wait
"$bin" "${common[@]}" -dir "$data/a" -eval "$questions" -eval-out "$data/a.eval.jsonl" -eval-par 2 > "$data/a.eval.log" 2>&1 &
"$bin" "${common[@]}" -agent-memory=false -agent-steps-in-main -dir "$data/b" -eval "$questions" -eval-out "$data/b.eval.jsonl" -eval-par 2 > "$data/b.eval.log" 2>&1 &
wait
python3 -I "$here/score.py" "$data"
