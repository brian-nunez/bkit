#!/usr/bin/env bash
#
# Prints, as a JSON array in COMPONENTS order, the modules with a releasing
# commit since their last tag. The release workflow uses it as its matrix so
# modules with nothing to release get no job.
#
#   scripts/release-packages.sh [branch] [revision]
#
# branch is the release branch (default main). On main, only stable tags
# count as a module's last release; on any other branch, prerelease tags
# count too. revision defaults to HEAD.
#
# A commit counts when it touches the module directory, outside any nested
# module (the same paths bkit-commit-filter gives semantic-release), and its
# message matches a default commit-analyzer release rule: a feat, fix, perf,
# or revert header, or a breaking change. The match is deliberately loose.
# A module listed with nothing to release costs one job that semantic-release
# skips, while a module left out would miss its release.

set -euo pipefail

branch=${1:-main}
revision=${2:-HEAD}

repository_root=$(git rev-parse --show-toplevel)
cd "$repository_root"

components=$(make -s --no-print-directory get-all-release-packages | jq -r '.[]')

# last_tag prints the newest release tag of $1 reachable from $revision.
last_tag() {
  local module=$1 pattern
  pattern="^$module/v[0-9]+\.[0-9]+\.[0-9]+"
  if [ "$branch" = main ]; then
    pattern="$pattern\$"
  else
    pattern="$pattern(-.+)?\$"
  fi
  git tag --merged "$revision" --list "$module/v*" |
    { grep -E "$pattern" || true; } |
    sed "s|^$module/v||" |
    sort -V |
    tail -n 1 |
    sed "s|^|$module/v|"
}

# pathspecs prints the module directory and an exclusion for every nested
# module, one per line.
pathspecs() {
  local module=$1
  printf '%s\n' "$module"
  find "$module" -mindepth 2 -name go.mod \
    -not -path '*/vendor/*' -not -path '*/node_modules/*' -not -path '*/testdata/*' -not -path '*/.*/*' |
    sed -e 's|/go\.mod$||' -e 's|^|:(exclude)|'
}

released=()
for module in $components; do
  tag=$(last_tag "$module")
  range=$revision
  if [ -n "$tag" ]; then
    range="$tag..$revision"
  fi
  mapfile -t specs < <(pathspecs "$module")
  commit=$(git log -1 --format=%H -E -i \
    --grep='^(feat|fix|perf|revert)(\([^)]*\))?!?:' \
    --grep='^BREAKING[ -]CHANGE' \
    "$range" -- "${specs[@]}")
  if [ -n "$commit" ]; then
    released+=("$module")
  fi
done

jq -cn '$ARGS.positional' --args "${released[@]}"
