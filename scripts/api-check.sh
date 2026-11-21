#!/usr/bin/env bash
#
# Fails when a change makes an incompatible change to a released module's
# API without marking its commit as breaking. Release versions come from
# Conventional Commits, so an unmarked break would ship as a minor or patch
# release and break consumers.
#
# For every component the change touches (files under the module directory,
# outside nested components), gorelease compares the head commit with the
# module's latest stable tag. An incompatible change passes only when a
# commit in base..head marks it: `!` before the colon in the subject, or a
# `BREAKING CHANGE:` or `BREAKING-CHANGE:` footer. Modules without a stable
# tag have no released API and are skipped.
#
# Usage:
#   scripts/api-check.sh [--base <ref>] [--head <ref>]
#
# Without --base, the base is the merge base of --head and origin/main.
#
# Exit codes:
#   0  no unmarked incompatible change
#   1  unmarked incompatible change, or gorelease failed
#   2  usage error

set -euo pipefail

base=""
head="HEAD"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --base)
      base="${2:-}"
      shift 2
      ;;
    --head)
      head="${2:-}"
      shift 2
      ;;
    -h | --help)
      sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "api-check: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

tools="$(cd "$(dirname "$0")" && pwd)/tools.sh"
repository_root=$(git rev-parse --show-toplevel)
cd "$repository_root"

if [ -z "$base" ]; then
  if git rev-parse --verify -q origin/main >/dev/null; then
    base=$(git merge-base origin/main "$head")
  else
    echo "api-check: specify --base <ref>" >&2
    exit 2
  fi
fi

components=$(make -s --no-print-directory get-all-release-packages | jq -r '.[]')

# owning_component prints the deepest component that contains the file.
owning_component() {
  local file=$1 best="" component
  for component in $components; do
    case "$file" in
      "$component"/*)
        if [ "${#component}" -gt "${#best}" ]; then
          best=$component
        fi
        ;;
    esac
  done
  printf '%s' "$best"
}

changed=""
while IFS= read -r file; do
  [ -z "$file" ] && continue
  component=$(owning_component "$file")
  [ -z "$component" ] && continue
  changed=$(printf '%s\n%s' "$changed" "$component")
done < <(git diff --name-only --no-renames "$base" "$head")
changed=$(printf '%s\n' "$changed" | sed '/^$/d' | LC_ALL=C sort -u)

if [ -z "$changed" ]; then
  echo "api-check: ok (no released module changed)"
  exit 0
fi

breaking=false
while IFS= read -r -d '' message; do
  subject=${message%%$'\n'*}
  if [[ "$subject" =~ ^[a-z]+(\([^\)]*\))?!: ]] ||
    printf '%s\n' "$message" | grep -Eq '^BREAKING[ -]CHANGE: '; then
    breaking=true
  fi
done < <(git log -z --format=%B "$base..$head")

# gorelease reads the module from a clean checkout of the head commit.
work=$(mktemp -d)
cleanup() {
  git worktree remove --force "$work/tree" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
git worktree add --quiet --detach "$work/tree" "$head"

failures=0
for component in $changed; do
  tag=$(git tag --list "$component/v*" --sort=-v:refname |
    grep -E "^$component/v[0-9]+\.[0-9]+\.[0-9]+\$" | head -n 1 || true)
  if [ -z "$tag" ]; then
    echo "api-check: $component has no stable release; skipped"
    continue
  fi
  if [ ! -f "$work/tree/$component/go.mod" ]; then
    echo "api-check: $component was removed; skipped"
    continue
  fi

  echo "api-check: comparing $component with $tag"
  code=0
  output=$(cd "$work/tree/$component" &&
    GOWORK=off GOFLAGS=-mod=mod "$tools" gorelease -base="${tag##*/}" 2>&1) || code=$?
  printf '%s\n' "$output" | sed 's/^/  /'

  if [ "$code" -eq 0 ]; then
    continue
  fi
  if ! printf '%s\n' "$output" | grep -q '^## incompatible changes'; then
    echo "ERROR: gorelease failed for $component without reporting API changes." >&2
    failures=$((failures + 1))
    continue
  fi
  if [ "$breaking" = true ]; then
    echo "api-check: $component has incompatible changes, and the commit is marked breaking"
    continue
  fi
  echo "ERROR: $component has incompatible API changes since $tag, but no commit marks a breaking change." >&2
  echo "       Restore compatibility, or mark the commit with '!' and a 'BREAKING CHANGE:' footer" >&2
  echo "       (a v1+ module also moves to a /vN module path in the same commit)." >&2
  failures=$((failures + 1))
done

if [ "$failures" -gt 0 ]; then
  exit 1
fi
echo "api-check: ok"
