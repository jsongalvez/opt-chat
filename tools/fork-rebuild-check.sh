#!/usr/bin/env bash
# Checks that a rebuilt fork master lost nothing of the old one.
#   tools/fork-rebuild-check.sh OLD NEW BASE
# OLD: the fork's master before the rebuild; NEW: the rebuilt one;
# BASE: what OLD's own work is measured from (delneg's master, the PR head).
# Fails when a test, a file or a flag of OLD is missing in NEW, or when a
# line that one of OLD's own commits added, and OLD still has, is gone.
set -euo pipefail
old=$1 new=$2 base=$3
cd "$(git rev-parse --show-toplevel)"
fail=0

tests() { git grep -h -o '^func Test[A-Za-z0-9_]*' "$1" -- '*_test.go' | sort -u; }
missing=$(comm -23 <(tests "$old") <(tests "$new"))
[ -z "$missing" ] || { echo "tests in $old, not in $new:"; echo "$missing"; fail=1; }

files() { git ls-tree -r --name-only "$1" | sort; }
gone=$(comm -23 <(files "$old") <(files "$new"))
[ -z "$gone" ] || { echo "files in $old, not in $new:"; echo "$gone"; fail=1; }

tmp=$(mktemp -d)
trap 'git worktree remove --force "$tmp/o" 2>/dev/null; git worktree remove --force "$tmp/n" 2>/dev/null; rm -rf "$tmp"' EXIT
flags() {
	git worktree add -q --detach "$tmp/$2" "$1"
	(cd "$tmp/$2" && go build -o bin . && ./bin -h 2>&1 | grep -o '^  -[a-z-]*' | sort -u)
}
lost=$(comm -23 <(flags "$old" o) <(flags "$new" n))
[ -z "$lost" ] || { echo "flags of $old that $new lacks:"; echo "$lost"; fail=1; }

for c in $(git rev-list --no-merges "$base..$old"); do
	n=0
	while IFS=$'\t' read -r f line; do
		# grep -q on a pipe would SIGPIPE git show, and pipefail call that a miss
		git show "$old:$f" 2>/dev/null | grep -xF -- "$line" >/dev/null || continue
		git show "$new:$f" 2>/dev/null | grep -xF -- "$line" >/dev/null || n=$((n + 1))
	done < <(git show --format= --unified=0 "$c" | awk '
		/^\+\+\+ b\// { f = substr($0, 7); next }
		/^\+/ && !/^\+\+\+/ { l = substr($0, 2); if (l ~ /[^ \t{}()]/) print f "\t" l }')
	[ "$n" = 0 ] || { echo "$(git log -1 --format='%h %s' "$c"): $n added lines gone"; fail=1; }
done

[ "$fail" = 0 ] && echo "nothing of $old lost in $new"
exit "$fail"
