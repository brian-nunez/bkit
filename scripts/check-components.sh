#!/usr/bin/env bash
#
# Validates that the root Makefile's COMPONENTS list, the Go modules on disk,
# and each module's release configuration agree. Run from anywhere inside the
# repository; `make check` and CI run it.

set -euo pipefail

repository_root=$(git rev-parse --show-toplevel)
cd "$repository_root"

go_mod_version=$(make -s --no-print-directory print-go-mod-version)
components=$(make -s --no-print-directory get-all-release-packages | jq -r '.[]')

failures=0
fail() {
  echo "ERROR: $*" >&2
  failures=$((failures + 1))
}

is_component() {
  printf '%s\n' "$components" | grep -Fxq -- "$1"
}

go_directive() {
  awk '$1 == "go" { print $2; exit }' "$1"
}

# Modules on disk. Example modules are unreleased and stay out of COMPONENTS.
modules=$(
  find . -type f -name go.mod \
    -not -path './go.mod' \
    -not -path '*/vendor/*' \
    -not -path '*/node_modules/*' \
    -not -path '*/examples/*' \
    -not -path './.tools/*' \
    -not -path './.agents/*' |
    sed -e 's|^\./||' -e 's|/go\.mod$||' |
    LC_ALL=C sort
)

root_go_version=$(go_directive go.mod)
if [ "$root_go_version" != "$go_mod_version" ]; then
  fail "go.mod declares go $root_go_version; GO_MOD_VERSION is $go_mod_version"
fi

for module in $modules; do
  if ! is_component "$module"; then
    fail "$module has a go.mod but is not listed in COMPONENTS in the root Makefile"
  fi
done

seen=""
for component in $components; do
  case "$component" in
    /* | *..* | *[!a-z0-9_/-]* | [!a-z]*)
      fail "COMPONENTS entry '$component' is not a valid module directory"
      continue
      ;;
  esac

  for required in go.mod Makefile .releaserc.json README.md doc.go; do
    if [ ! -f "$component/$required" ]; then
      fail "$component is missing $required"
    fi
  done

  if [ -f "$component/Makefile" ] && ! grep -qE '^coverage-check:' "$component/Makefile"; then
    fail "$component/Makefile has no coverage-check target; copy it from the add-bkit-package template"
  fi

  if [ -f "$component/go.mod" ]; then
    expected_path="github.com/brian-nunez/bkit/$component"
    actual_path=$(awk '$1 == "module" { print $2; exit }' "$component/go.mod")
    case "$actual_path" in
      "$expected_path" | "$expected_path"/v[2-9] | "$expected_path"/v[1-9][0-9]) ;;
      *) fail "$component/go.mod declares module $actual_path; expected $expected_path" ;;
    esac

    actual_go_version=$(go_directive "$component/go.mod")
    if [ "$actual_go_version" != "$go_mod_version" ]; then
      fail "$component/go.mod declares go $actual_go_version; GO_MOD_VERSION is $go_mod_version"
    fi
  fi

  if [ -f "$component/.releaserc.json" ]; then
    expected_tag_format="$component/v\${version}"
    if ! actual_tag_format=$(jq -er '.tagFormat' "$component/.releaserc.json" 2>/dev/null); then
      fail "$component/.releaserc.json is not valid JSON or has no tagFormat"
    elif [ "$actual_tag_format" != "$expected_tag_format" ]; then
      fail "$component/.releaserc.json tagFormat is '$actual_tag_format'; expected '$expected_tag_format'"
    fi
  fi

  # A nested module releases after every enclosing component.
  parent=$component
  while [ "${parent%/*}" != "$parent" ]; do
    parent=${parent%/*}
    if is_component "$parent" && ! printf '%s\n' "$seen" | grep -Fxq -- "$parent"; then
      fail "$component is listed before its parent $parent in COMPONENTS"
    fi
  done

  seen=$(printf '%s\n%s' "$seen" "$component")
done

if [ "$failures" -gt 0 ]; then
  echo "Component check failed with $failures error(s)." >&2
  exit 1
fi

echo "Components are consistent ($(printf '%s\n' "$components" | grep -c . || true) registered)."
