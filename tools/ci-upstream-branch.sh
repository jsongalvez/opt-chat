#!/usr/bin/env bash
# Runs the fork's CI on a branch meant for upstream, which has no Go
# workflow of its own: pushes <branch>-ci to origin, the branch plus one
# commit that adds the fork's ci.yml (go test -race) and triggers it and
# formal.yml on that name. The branch itself is left as it is. Delete the
# -ci branch once the PR is sent: git push origin --delete <branch>-ci
set -euo pipefail
b=${1:?usage: tools/ci-upstream-branch.sh <branch>}
root=$(git rev-parse --show-toplevel)
wt=$(mktemp -d)
trap 'git -C "$root" worktree remove --force "$wt"' EXIT
git -C "$root" worktree add -q --detach "$wt" "$b"
mkdir -p "$wt/.github/workflows"
git -C "$root" show origin/master:.github/workflows/ci.yml > "$wt/.github/workflows/ci.yml"
sed -i "s/branches: \[master\]/branches: [master, $b-ci]/" "$wt"/.github/workflows/*.yml
sed -i 's|go test -short ./...|go test -race ./...|' "$wt/.github/workflows/ci.yml"
git -C "$wt" add -A
git -C "$wt" commit -qm "ci: run the fork's checks on $b (throwaway)"
git -C "$wt" push -qf origin "HEAD:refs/heads/$b-ci"
echo "pushed $b-ci at $(git -C "$wt" rev-parse --short HEAD); runs: gh run list -b $b-ci"
