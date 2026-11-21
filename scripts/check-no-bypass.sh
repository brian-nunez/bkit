#!/usr/bin/env bash
#
# Fails when any tracked Go file outside vendor/ suppresses a linter with
# //nolint, //lint:ignore, or #nosec. Findings are fixed in the code, or,
# when a rule truly does not apply, excluded in .golangci.yml with a comment
# and a path and text scope, where the guardrail audit sees the change.

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

hits=$(git ls-files -z -- '*.go' ':!:vendor/**' ':!:**/vendor/**' |
  xargs -0 -r grep -nE '//[[:space:]]*nolint|//[[:space:]]*lint:ignore|#nosec' || true)

if [ -n "$hits" ]; then
  printf '%s\n' "$hits"
  echo "ERROR: lint suppression directives found. Fix the code, or add a scoped, commented exclusion to .golangci.yml." >&2
  exit 1
fi

echo "No lint suppression directives."
