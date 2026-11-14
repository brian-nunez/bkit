#!/usr/bin/env bash
#
# Post-release verification for one module. Run from the module directory
# after its tag is pushed:
#
#   scripts/verify-go-module.sh v1.2.3
#
# Resolves the version through proxy.golang.org and sum.golang.org with an
# empty module cache, as a new consumer would, then confirms the published
# zip holds no release tooling, builds, and keeps a stable checksum.

set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "Usage: $0 <module-version>" >&2
  exit 2
fi

version=v${1#v}

export GOWORK=off
export GOFLAGS=-mod=mod
export GOPROXY=https://proxy.golang.org
export GOSUMDB=sum.golang.org
export GOPRIVATE=
export GONOPROXY=
export GONOSUMDB=

module_path=$(go list -m)
cache_dir=$(mktemp -d)
trap 'chmod -R u+w "$cache_dir" 2>/dev/null || true; rm -rf "$cache_dir"' EXIT
export GOMODCACHE="$cache_dir/modcache"

# The proxy fetches a new tag from GitHub on first request, which can lag the
# push by a short time.
download=""
for attempt in 1 2 3 4 5 6; do
  if download=$(go mod download -json "$module_path@$version" 2>"$cache_dir/download.err"); then
    break
  fi
  if [ "$attempt" -eq 6 ]; then
    cat "$cache_dir/download.err" >&2
    echo "proxy.golang.org did not serve $module_path@$version" >&2
    exit 1
  fi
  sleep $((attempt * 10))
done

mkdir "$cache_dir/consumer"
cd "$cache_dir/consumer"
go mod init bkit-consumer >/dev/null 2>&1
go mod edit "-require=$module_path@$version"

zip_file=$(jq -er '.Zip' <<<"$download")
unzip -Z1 "$zip_file" >"$cache_dir/zip-entries"
if grep -Eq '/(node_modules|\.tools)/|/package(-lock)?\.json$' "$cache_dir/zip-entries"; then
  echo "Published module contains release tooling files:" >&2
  grep -E '/(node_modules|\.tools)/|/package(-lock)?\.json$' "$cache_dir/zip-entries" >&2
  exit 1
fi

go mod verify
go build "$module_path/..."

first_sum=$(jq -er '.Sum' <<<"$download")
retained_sum=$(go mod download -json "$module_path@$version" | jq -er '.Sum')
test "$first_sum" = "$retained_sum"

echo "Verified $module_path@$version from proxy.golang.org with fresh and retained caches"
