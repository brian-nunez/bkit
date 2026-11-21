#!/usr/bin/env bash
#
# Fails when the working tree has changes to tracked files or new untracked
# files that .gitignore does not cover. CI runs it after the checks so a
# formatter, go mod tidy, or test run that rewrites committed files cannot
# pass unnoticed.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

status=$(git status --porcelain)
if [ -n "$status" ]; then
  printf '%s\n' "$status"
  git --no-pager diff
  echo "ERROR: the checks changed the working tree. Run them locally and commit the result." >&2
  exit 1
fi

echo "Working tree is clean."
