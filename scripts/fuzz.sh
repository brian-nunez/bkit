#!/usr/bin/env bash
#
# Actively fuzzes every native Go fuzz target in the released modules, one
# target at a time. Targets are discovered from `func Fuzz...` declarations
# in each package's test files, so a new target is fuzzed without editing
# this script. Normal `go test` runs only replay the seed corpus.
#
# Usage:
#   scripts/fuzz.sh [module...]
#
# Without arguments every COMPONENTS entry is fuzzed. FUZZTIME (default 10s)
# is the time per target. A failing input is written to the package's
# testdata/fuzz/<target>/ directory; commit it as a regression seed with the
# fix.
#
# Exit codes:
#   0  every target ran for FUZZTIME without a failure
#   1  at least one target failed

set -euo pipefail

repository_root=$(cd "$(dirname "$0")/.." && pwd)
cd "$repository_root"

fuzztime=${FUZZTIME:-10s}
export GOWORK=off

if [ "$#" -gt 0 ]; then
  modules=("$@")
else
  read -r -a modules <<<"$(make -s --no-print-directory get-all-release-packages | jq -r 'join(" ")')"
fi

failed=()
count=0
for module in "${modules[@]}"; do
  if [ ! -f "$module/go.mod" ]; then
    echo "fuzz.sh: $module is not a module directory" >&2
    exit 2
  fi
  while IFS=' ' read -r pkg dir; do
    for target in $(cat "$dir"/*_test.go 2>/dev/null | sed -n 's/^func \(Fuzz[A-Za-z0-9_]*\)(.*/\1/p' | LC_ALL=C sort -u); do
      count=$((count + 1))
      echo "==> $pkg $target ($fuzztime)"
      if ! (cd "$module" && go test -run='^$' -fuzz="^$target\$" -fuzztime="$fuzztime" -fuzzminimizetime=30s "$pkg"); then
        failed+=("$pkg $target")
      fi
    done
  done < <(cd "$module" && go list -f '{{.ImportPath}} {{.Dir}}' ./...)
done

echo "Fuzzed $count target(s) for $fuzztime each."
if [ "${#failed[@]}" -gt 0 ]; then
  echo "Failing fuzz targets:" >&2
  printf '  %s\n' "${failed[@]}" >&2
  exit 1
fi
