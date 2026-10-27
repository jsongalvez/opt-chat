#!/usr/bin/env bash
# Model-checks LimitResume.tla against every property in PROPS, under each
# config given (default MC.cfg, then MC2.cfg). TLC stops at the first broken
# property; this prints its counterexample, drops it, and checks the rest
# again. Exit status 1 if any property breaks. TRACE=1 prints whole states.
set -euo pipefail
cd "$(dirname "$0")"
VER=1.8.0
SUM=78f7e2c6bf9a8b65eda665a23e94431255587e0b43a834a4e2af1723562d9b4a
JAR=${TLA_JAR:-${XDG_CACHE_HOME:-$HOME/.cache}/tla/tla2tools-$VER.jar}
if [ ! -f "$JAR" ]; then
  mkdir -p "$(dirname "$JAR")"
  curl -fsSL -o "$JAR.tmp" "https://github.com/tlaplus/tlaplus/releases/download/v$VER/tla2tools.jar"
  echo "$SUM  $JAR.tmp" | sha256sum -c --quiet
  mv "$JAR.tmp" "$JAR"
fi
PROPS=(NoWaitOnNonLimit NoResumeOfFinished NoEarlyResume NoLateResume NoLostResume TriesBounded TimerOnTime)
CONFIGS=("$@")
[ ${#CONFIGS[@]} -eq 0 ] && CONFIGS=(MC.cfg MC2.cfg)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
fail=0
for cfg in "${CONFIGS[@]}"; do
  left=("${PROPS[@]}")
  while [ ${#left[@]} -gt 0 ]; do
    { cat "$cfg"; printf 'INVARIANT %s\n' "${left[@]}"; } > "$tmp/run.cfg"
    start=$SECONDS
    if java -XX:+UseParallelGC -cp "$JAR" tlc2.TLC -noGenerateSpecTE -workers auto -metadir "$tmp/meta" \
        -config "$tmp/run.cfg" LimitResume.tla > "$tmp/out" 2>&1; then
      echo "$cfg PASS ${left[*]}: $(grep -oE '[0-9]+ distinct states found' "$tmp/out" | tail -1), depth $(grep -oE 'search is [0-9]+' "$tmp/out" | grep -oE '[0-9]+'), $((SECONDS - start))s"
      break
    fi
    fail=1
    p=$(grep -oE 'Invariant [A-Za-z]+ is violated' "$tmp/out" | head -1 | cut -d' ' -f2)
    if [ -z "$p" ]; then
      echo "$cfg ERROR"; tail -20 "$tmp/out"; exit 2
    fi
    echo "$cfg FAIL $p:"
    grep -E '^State [0-9]+:' "$tmp/out" | sed -E 's/ line [0-9].*//; s/^State [0-9]+: <?/    /'
    if [ -n "${TRACE:-}" ]; then sed -n '/is violated/,/distinct states/p' "$tmp/out"; fi
    rm -rf "$tmp/meta"
    keep=()
    for q in "${left[@]}"; do [ "$q" = "$p" ] || keep+=("$q"); done
    left=("${keep[@]}")
  done
done
exit $fail
