#!/usr/bin/env bash
#
# Tests for guardrail-check.sh against throwaway Git repositories. Run with
# `make test-scripts`.

set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/guardrail-check.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
failures=0
count=0

# new_repo creates a repository with one Go file committed as the base.
new_repo() {
  count=$((count + 1))
  dir="$work/repo-$count"
  mkdir -p "$dir"
  git -C "$dir" init -q -b main
  git -C "$dir" config user.name test
  git -C "$dir" config user.email test@example.com
  git -C "$dir" config commit.gpgsign false
  mkdir -p "$dir/gate"
  printf 'package gate\n' >"$dir/gate/gate.go"
  printf 'package gate\n' >"$dir/gate/gate_test.go"
  git -C "$dir" add -A
  git -C "$dir" commit -q -m "chore: base"
  base=$(git -C "$dir" rev-parse HEAD)
}

# change writes content to a file in the repository and commits it.
change() {
  mkdir -p "$(dirname "$dir/$1")"
  printf '%s\n' "$2" >>"$dir/$1"
  git -C "$dir" add -A
  git -C "$dir" commit -q -m "chore: change"
}

expect() {
  local name=$1 want_code=$2 want_text=$3 approved=${4:-}
  local out code=0
  out=$(cd "$dir" && GUARDRAIL_APPROVED="$approved" bash "$script" --base "$base" --head HEAD 2>&1) || code=$?
  if [ "$code" -eq "$want_code" ] && [[ "$out" == *"$want_text"* ]]; then
    echo "ok   $name"
  else
    echo "FAIL $name: want exit $want_code containing '$want_text', got exit $code:"
    printf '%s\n' "$out"
    failures=$((failures + 1))
  fi
}

new_repo
change gate/gate.go "func And() {}"
expect "library code change passes" 0 "ok"

new_repo
change gate/gate_test.go "func TestAnd(t *testing.T) { t.Run(\"x\", nil) }"
expect "test change without a skip passes" 0 "ok"

new_repo
change README.md "Skip(this) //nolint in prose"
expect "non-Go file passes" 0 "ok"

for file in .golangci.yml .commitlintrc.json .gitignore AGENTS.md Makefile gate/Makefile \
  make/development.mk scripts/tools.sh .github/workflows/tests.yml .github/CODEOWNERS \
  .agents/skills/add-bkit-package/SKILL.md objex/objextest/objextest.go \
  rdb/rdbtest/rdbtest.go stash/stashtest/stashtest.go; do
  new_repo
  change "$file" "# edit"
  expect "$file change trips the alarm" 1 "$file"
done

new_repo
change gate/gate.go "func f() {} //nolint:gosec // reason"
expect "nolint directive trips the alarm" 1 "nolint"

new_repo
change gate/gate.go "func f() {} // nolint"
expect "spaced nolint directive trips the alarm" 1 "nolint"

new_repo
change gate/gate.go "//lint:ignore SA1019 reason"
expect "staticcheck ignore trips the alarm" 1 "lint:ignore"

new_repo
change gate/gate.go "func f() {} // #nosec G104"
expect "gosec suppression trips the alarm" 1 "#nosec"

new_repo
change gate/extra_test.go "//go:build ignore"
expect "build constraint trips the alarm" 1 "go:build"

new_repo
change gate/gate_test.go "func TestX(t *testing.T) { t.Skip(\"flaky\") }"
expect "t.Skip trips the alarm" 1 "t.Skip"

new_repo
change gate/gate_test.go "func TestX(t *testing.T) { t.Skipf(\"%s\", x) }"
expect "t.Skipf trips the alarm" 1 "t.Skipf"

new_repo
change gate/gate_test.go "func TestX(tb testing.TB) { tb.SkipNow() }"
expect "tb.SkipNow trips the alarm" 1 "tb.SkipNow"

new_repo
change gate/vendor/x/x.go "func f() {} //nolint"
git -C "$dir" ls-files --error-unmatch gate/vendor/x/x.go >/dev/null
expect "vendored code is ignored" 0 "ok"

new_repo
change .golangci.yml "version: \"2\""
expect "approved guardrail change passes" 0 ".golangci.yml" true

new_repo
change .golangci.yml "version: \"2\""
out=$(cd "$dir" && bash "$script" --base "$base" --head HEAD --markdown 2>&1) || true
if [[ "$out" == *"<!-- guardrail-alarm -->"* && "$out" == *'`.golangci.yml`'* ]]; then
  echo "ok   markdown report carries the comment marker"
else
  echo "FAIL markdown report carries the comment marker:"
  printf '%s\n' "$out"
  failures=$((failures + 1))
fi

new_repo
code=0
(cd "$dir" && bash "$script" --bogus >/dev/null 2>&1) || code=$?
if [ "$code" -eq 2 ]; then
  echo "ok   unknown argument is a usage error"
else
  echo "FAIL unknown argument is a usage error: got exit $code"
  failures=$((failures + 1))
fi

if [ "$failures" -gt 0 ]; then
  echo "$failures guardrail-check test(s) failed"
  exit 1
fi
echo "All guardrail-check tests passed."
