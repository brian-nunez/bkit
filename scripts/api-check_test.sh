#!/usr/bin/env bash
#
# Tests for api-check.sh against a throwaway Git repository and a local
# file:// module proxy that serves the module's released version, so the
# tests run offline. Run with `make test-scripts`.

set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/api-check.sh"
work=$(mktemp -d)
trap 'chmod -R u+w "$work" 2>/dev/null || true; rm -rf "$work"' EXIT
failures=0
count=0

module=example.test/mod
v1_source='package mod

// A is part of the released API.
func A() {}

// B is part of the released API.
func B() {}'

# publish writes module example.test/mod v1.0.0 with v1_source into a
# file:// proxy at $work/proxy.
publish() {
  local dir="$work/proxy/$module/@v"
  mkdir -p "$dir" "$work/zip/$module@v1.0.0"
  printf 'module %s\n\ngo 1.26.0\n' "$module" >"$dir/v1.0.0.mod"
  printf '{"Version":"v1.0.0","Time":"2026-01-01T00:00:00Z"}\n' >"$dir/v1.0.0.info"
  printf 'v1.0.0\n' >"$dir/list"
  cp "$dir/v1.0.0.mod" "$work/zip/$module@v1.0.0/go.mod"
  printf '%s\n' "$v1_source" >"$work/zip/$module@v1.0.0/mod.go"
  (cd "$work/zip" && python3 -c '
import sys, zipfile, os
with zipfile.ZipFile(sys.argv[1], "w") as z:
    for root, _, files in os.walk(sys.argv[2]):
        for f in files:
            z.write(os.path.join(root, f))
' "$dir/v1.0.0.zip" "$module@v1.0.0")
}

# new_repo creates a repository whose only component, mod, is released as
# mod/v1.0.0 with v1_source.
new_repo() {
  count=$((count + 1))
  dir="$work/repo-$count"
  mkdir -p "$dir/mod" "$dir/scripts"
  git -C "$dir" init -q -b main
  git -C "$dir" config user.name test
  git -C "$dir" config user.email test@example.com
  git -C "$dir" config commit.gpgsign false
  git -C "$dir" config tag.gpgsign false
  printf 'get-all-release-packages:\n\t@echo %s\n' "'[\"mod\"]'" >"$dir/Makefile"
  printf 'module %s\n\ngo 1.26.0\n' "$module" >"$dir/mod/go.mod"
  printf '%s\n' "$v1_source" >"$dir/mod/mod.go"
  printf 'docs\n' >"$dir/README.md"
  git -C "$dir" add -A
  git -C "$dir" commit -q -m "feat(mod): first release"
  git -C "$dir" tag mod/v1.0.0
  base=$(git -C "$dir" rev-parse HEAD)
}

commit() {
  git -C "$dir" add -A
  git -C "$dir" commit -q -m "$1"
}

expect() {
  local name=$1 want_code=$2 want_text=$3
  local out code=0
  out=$(cd "$dir" &&
    GOPROXY="file://$work/proxy" GOSUMDB=off GONOSUMDB="$module" GOMODCACHE="$work/modcache" GOTOOLCHAIN=local \
      bash "$script" --base "$base" --head HEAD 2>&1) || code=$?
  if [ "$code" -eq "$want_code" ] && [[ "$out" == *"$want_text"* ]]; then
    echo "ok   $name"
  else
    echo "FAIL $name: want exit $want_code containing '$want_text', got exit $code:"
    printf '%s\n' "$out"
    failures=$((failures + 1))
  fi
}

# The cases point GOPROXY at the local proxy, which cannot serve gorelease,
# so install the pinned gorelease through the configured proxy first.
tools="$(dirname "$script")/tools.sh"
"$tools" gorelease -h >/dev/null 2>&1 || true
if ! compgen -G "$(dirname "$script")/../.tools/gorelease-*" >/dev/null; then
  echo "FAIL could not install the pinned gorelease"
  exit 1
fi

publish

new_repo
printf 'more docs\n' >>"$dir/README.md"
commit "docs(readme): explain"
expect "change outside every module passes" 0 "no released module changed"

new_repo
printf '\n// C is new.\nfunc C() {}\n' >>"$dir/mod/mod.go"
commit "feat(mod): add C"
expect "compatible addition passes" 0 "api-check: ok"

new_repo
sed -i.bak '/^\/\/ B is/,$d' "$dir/mod/mod.go" && rm "$dir/mod/mod.go.bak"
commit "feat(mod): drop B"
expect "unmarked removal fails" 1 "no commit marks a breaking change"

new_repo
sed -i.bak '/^\/\/ B is/,$d' "$dir/mod/mod.go" && rm "$dir/mod/mod.go.bak"
commit "feat(mod)!: drop B"
expect "removal marked with ! passes" 0 "marked breaking"

new_repo
sed -i.bak '/^\/\/ B is/,$d' "$dir/mod/mod.go" && rm "$dir/mod/mod.go.bak"
commit "feat(mod): drop B

BREAKING CHANGE: B is gone; call A instead."
expect "removal with a BREAKING CHANGE footer passes" 0 "marked breaking"

new_repo
sed -i.bak '/^\/\/ B is/,$d' "$dir/mod/mod.go" && rm "$dir/mod/mod.go.bak"
commit "fix(mod): mention BREAKING CHANGE: only in prose"
expect "breaking text in the subject does not count" 1 "no commit marks a breaking change"

new_repo
git -C "$dir" tag -d mod/v1.0.0 >/dev/null
sed -i.bak '/^\/\/ B is/,$d' "$dir/mod/mod.go" && rm "$dir/mod/mod.go.bak"
commit "feat(mod): drop B"
expect "module without a stable tag is skipped" 0 "no stable release"

new_repo
printf 'package mod\n\nfunc broken( {\n' >"$dir/mod/broken.go"
commit "fix(mod): break the build"
expect "gorelease failure without API report fails" 1 "gorelease failed"

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
  echo "$failures api-check test(s) failed"
  exit 1
fi
echo "All api-check tests passed."
