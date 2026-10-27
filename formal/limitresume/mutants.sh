#!/usr/bin/env bash
# Shows each property of LimitResume.tla can fail: breaks the spec one way per
# property, as a change to the code would, and expects check.sh to report
# that property broken. Exit status 1 if a mutant goes uncaught.
set -euo pipefail
cd "$(dirname "$0")"
here=$PWD
miss=0
mutant() { # property, then a Python find and replace on the spec
  local prop=$1 find=$2 repl=$3 d
  d=$(mktemp -d)
  cp LimitResume.tla MC.cfg check.sh "$d/"
  python3 - "$d/LimitResume.tla" "$find" "$repl" <<'PY'
import sys
p, a, b = sys.argv[1:]
s = open(p).read()
assert a in s, "mutant text not found: " + a
open(p, "w").write(s.replace(a, b, 1))
PY
  sed -i "s/^PROPS=.*/PROPS=($prop)/" "$d/check.sh"
  if "$d/check.sh" MC.cfg > "$d/out" 2>&1; then
    echo "MISSED $prop"; miss=1
  elif grep -q "FAIL $prop" "$d/out"; then
    echo "caught $prop"
  else
    echo "ERROR $prop"; cat "$d/out"; miss=1
  fi
  rm -rf "$d"
}
mutant NoWaitOnNonLimit 'refused.status = "rejected" /\ refused.type = "shared"' 'refused.status = "rejected"'
mutant NoResumeOfFinished '/\ lastEnd'"'"' = [lastEnd EXCEPT ![c] = "ok"]
    /\ Went(c)' '/\ lastEnd'"'"' = [lastEnd EXCEPT ![c] = "ok"]
    /\ UNCHANGED <<st, disk, timer, told, own>>'
mutant NoEarlyResume 'taken == {c \in Who(st) : st.due[c] <= now}' 'taken == {c \in Who(st) : st.due[c] <= now + 1}'
mutant NoLateResume '!.due[c] = IF st.due[c] # None /\ st.due[c] >= at THEN st.due[c] ELSE at]' '!.due = [x \in Callers |-> IF x = c \/ st.due[x] # None THEN at + 1 ELSE None]]'
mutant NoLostResume '/\ st'"'"' = disk
    /\ timer'"'"' = Arm(disk)' '/\ st'"'"' = Empty
    /\ timer'"'"' = None'
mutant TriesBounded 'IN IF tries >= LimitMax' 'IN IF FALSE'
mutant TimerOnTime '/\ st'"'"' = s2 /\ disk'"'"' = s2 /\ timer'"'"' = Arm(s2)
              /\ own' '/\ st'"'"' = s2 /\ disk'"'"' = s2 /\ timer'"'"' = Arm(s2) + 1
              /\ own'
exit $miss
