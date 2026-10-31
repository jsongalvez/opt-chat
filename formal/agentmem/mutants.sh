#!/usr/bin/env bash
# Shows each property of AgentMem.tla can fail: takes one fix out of the
# fixed configs, or breaks the spec as a change to the code would, and
# expects check.sh to report that property broken. Exit status 1 if a mutant
# goes uncaught.
set -euo pipefail
cd "$(dirname "$0")"
miss=0
mutant() { # property, config, fix to drop or "", then a find and replace on the spec
  local prop=$1 cfg=$2 drop=$3 find=${4:-} repl=${5:-} d
  d=$(mktemp -d)
  cp AgentMem.tla ./*.cfg check.sh "$d/"
  if [ -n "$drop" ]; then sed -i "s/\"$drop\", \?//; s/, \"$drop\"//" "$d/$cfg"; fi
  if [ -n "$find" ]; then
    python3 - "$d/AgentMem.tla" "$find" "$repl" <<'PY'
import sys
p, a, b = sys.argv[1:]
s = open(p).read()
assert a in s, "mutant text not found: " + a
open(p, "w").write(s.replace(a, b, 1))
PY
  fi
  sed -i "s/^PROPS=.*/PROPS=($prop)/" "$d/check.sh"
  if "$d/check.sh" "$cfg" > "$d/out" 2>&1; then
    echo "MISSED $prop (${drop:-spec mutant})"; miss=1
  elif grep -q "FAIL $prop" "$d/out"; then
    echo "caught $prop (${drop:-spec mutant})"
  else
    echo "ERROR $prop"; cat "$d/out"; miss=1
  fi
  rm -rf "$d"
}
mutant NoWaitOnOthersIO MCFixed.cfg pump
mutant NoWaitOnOthersIO MCFixed.cfg close
mutant NoLostStep MCCrashFixed.cfg seal
mutant FailureSurfaces MCCrashFixed.cfg notice
mutant ChatIsLog MCFixed.cfg "" 'IN IF read[s] = 0 THEN SubSeq(st, Len(chat[s]) + 1, Len(st)) ELSE st' 'IN st'
mutant NoDeadlock MCFixed.cfg "" 'THEN Take(a, {MM, AM(t)}) /\ tgt'"'"' = [tgt EXCEPT ![a] = t] /\ Go(a, "cisc")' 'THEN Take(a, {MM}) /\ tgt'"'"' = [tgt EXCEPT ![a] = t] /\ Go(a, "ciam")'
exit $miss
