#!/bin/sh
# run.sh BEFORE_REV AFTER_REV: extract both revisions of the repository with
# git archive and compare them with movecheck. AFTER_REV may be WORK: the worktree's files as they
# are now (tracked and untracked, but not ignored).
set -e
here=$(cd "$(dirname "$0")" && pwd)
repo=${REPO:-$(git rev-parse --show-toplevel)} # the repository to compare; run from inside it
trees=$here/trees
rm -rf "${trees:?}/before" "${trees:?}/after" && mkdir -p "$trees/before" "$trees/after"
git -C "$repo" archive "$1" | tar -x -C "$trees/before"
if [ "$2" = WORK ]; then
	(cd "$repo" && git ls-files -co --exclude-standard -z | grep -zv '^review/' | xargs -0 tar -c) | tar -x -C "$trees/after"
else
	git -C "$repo" archive "$2" | tar -x -C "$trees/after"
fi
cd "$here" && GOTOOLCHAIN=auto go build -o "$here/movecheck" . && "$here/movecheck" -allow-doc ledger.AccountAmount -allow-doc ledger.Payment "$trees/before" "$trees/after"
