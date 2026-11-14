#!/usr/bin/env bash
#
# Pre-release gate for one module. semantic-release runs it from the module
# directory as prepareCmd, before the release tag exists:
#
#   scripts/check-go-module.sh v1.2.3
#
# Fails when the module path does not match the major version, go.mod has a
# replace directive or is not tidy, release tooling files are tracked, or the
# module does not build, pass its tests, and reach 100% coverage as a consumer
# would see it.

set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "Usage: $0 <module-version>" >&2
  exit 2
fi

version=${1#v}
if ! printf '%s' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  echo "Invalid module version: $1" >&2
  exit 2
fi
major=${version%%.*}

export GOWORK=off
export GOFLAGS=-mod=mod

module_path=$(go list -m)
echo "Checking $module_path v$version"

if [ "$major" -ge 2 ]; then
  case "$module_path" in
    */v"$major") ;;
    *)
      echo "Releasing v$version requires the module path to end in /v$major; go.mod declares $module_path." >&2
      echo "Change the module path (and every import of it) in the same change as the breaking commit." >&2
      exit 1
      ;;
  esac
elif printf '%s' "$module_path" | grep -Eq '/v[0-9]+$'; then
  echo "Module path $module_path has a major-version suffix, but the release is v$version." >&2
  exit 1
fi

replace_count=$(go mod edit -json | jq '.Replace // [] | length')
if [ "$replace_count" -ne 0 ]; then
  echo "go.mod contains $replace_count replace directive(s); released modules must depend on published versions." >&2
  exit 1
fi

if ! go mod tidy -diff; then
  echo "go.mod or go.sum is not tidy; run 'make tidy' in the module and commit the result." >&2
  exit 1
fi

tracked_release_files=$(git ls-files -- . | grep -E '(^|/)(node_modules/|package(-lock)?\.json$)' || true)
if [ -n "$tracked_release_files" ]; then
  echo "Release tooling files are tracked inside the module:" >&2
  printf '%s\n' "$tracked_release_files" >&2
  exit 1
fi

go build ./...
go vet ./...
# Race-enabled tests plus the module's 100% coverage requirement, exactly as
# CI runs them.
make --no-print-directory tests-nobuild coverage-check

echo "Pre-release checks passed for $module_path v$version"
