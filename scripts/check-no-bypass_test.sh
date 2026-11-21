#!/usr/bin/env bash
#
# Tests for check-no-bypass.sh against throwaway Git repositories. Run with
# `make test-scripts`.

set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/check-no-bypass.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0
count=0

# check commits one Go file with the given line and runs the script.
check() {
  local name=$1 path=$2 line=$3 want_code=$4
  count=$((count + 1))
  local dir="$work/repo-$count"
  mkdir -p "$dir/$(dirname "$path")"
  git -C "$dir" init -q -b main
  printf 'package x\n\n%s\n' "$line" >"$dir/$path"
  git -C "$dir" add -f -A
  local code=0
  (cd "$dir" && bash "$script" >/dev/null 2>&1) || code=$?
  if [ "$code" -eq "$want_code" ]; then
    echo "ok   $name"
  else
    echo "FAIL $name: want exit $want_code, got $code"
    failures=$((failures + 1))
  fi
}

check "plain code passes" x.go 'func f() {}' 0
check "nolint fails" x.go 'func f() {} //nolint:gosec // reason' 1
check "spaced nolint fails" x.go 'func f() {} // nolint' 1
check "lint:ignore fails" x.go '//lint:ignore SA1019 reason' 1
check "nosec fails" x.go 'func f() {} // #nosec G104' 1
check "nolint in a test file fails" x_test.go 'func f() {} //nolint' 1
check "vendored code is ignored" vendor/y/y.go 'func f() {} //nolint' 0
check "nested vendored code is ignored" mod/vendor/y/y.go 'func f() {} //nolint' 0

if [ "$failures" -gt 0 ]; then
  echo "$failures check-no-bypass test(s) failed"
  exit 1
fi
echo "All check-no-bypass tests passed."
