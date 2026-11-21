#!/usr/bin/env bash
#
# Installs and runs bkit's pinned developer and CI tools from .tools/ at the
# repository root. Local make targets and GitHub Actions both go through this
# script, so every run uses the same tool versions without adding dev-only
# modules to any go.mod. Versions are pinned in make/development.mk.
#
#   scripts/tools.sh install                  install the pull request check tools
#   scripts/tools.sh golangci-lint [args...]  run pinned golangci-lint
#   scripts/tools.sh govulncheck [args...]    run pinned govulncheck
#   scripts/tools.sh deadcode [args...]       run pinned deadcode
#   scripts/tools.sh gorelease [args...]      run pinned gorelease
#   scripts/tools.sh gremlins [args...]       run pinned gremlins (mutation testing)
#   scripts/tools.sh versions                 print pinned tool versions
#
# Tools run in the caller's working directory.

set -euo pipefail

repository_root=$(cd "$(dirname "$0")/.." && pwd)
tools_dir="$repository_root/.tools"

pinned() {
  local value
  value=$(awk -v name="$1" '$1 == name && ($2 == "?=" || $2 == ":=" || $2 == "=") { print $3; exit }' "$repository_root/make/development.mk")
  if [ -z "$value" ]; then
    echo "tools.sh: $1 is not set in make/development.mk" >&2
    exit 1
  fi
  printf '%s' "$value"
}

GOLANGCI_LINT_VERSION=v$(pinned REQUIRED_GOLANGCI_LINT_VERSION)
GOVULNCHECK_VERSION=$(pinned GOVULNCHECK_VERSION)
DEADCODE_VERSION=$(pinned DEADCODE_VERSION)
GORELEASE_VERSION=$(pinned GORELEASE_VERSION)
GREMLINS_VERSION=$(pinned GREMLINS_VERSION)

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# Release archive checksums for REQUIRED_GOLANGCI_LINT_VERSION, from the
# golangci-lint release's checksums file. Update them with the version.
golangci_lint_checksum() {
  case "$1" in
    v2.15.0-darwin-arm64) echo d1e409481a8ca846c4bc97e781eb5cb1bd5c23353a0b3977a1cbda1b1b6447d5 ;;
    v2.15.0-darwin-amd64) echo 767b899a28285e88ded2823d5dcc4bc5ef681173eb2d80f029a7b17ee4ece3b8 ;;
    v2.15.0-linux-arm64) echo b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998 ;;
    v2.15.0-linux-amd64) echo 9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea ;;
    *) return 1 ;;
  esac
}

ensure_golangci_lint() {
  local bin="$tools_dir/golangci-lint-$GOLANGCI_LINT_VERSION"
  [ -x "$bin" ] && return 0

  local os arch
  case "$(uname -s)" in
    Darwin) os=darwin ;;
    Linux) os=linux ;;
    *) echo "tools.sh: no pinned golangci-lint for $(uname -s)" >&2; exit 1 ;;
  esac
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) echo "tools.sh: no pinned golangci-lint for $(uname -m)" >&2; exit 1 ;;
  esac

  local want
  if ! want=$(golangci_lint_checksum "$GOLANGCI_LINT_VERSION-$os-$arch"); then
    echo "tools.sh: no checksum for golangci-lint $GOLANGCI_LINT_VERSION on $os-$arch; add it to scripts/tools.sh" >&2
    exit 1
  fi

  local name="golangci-lint-${GOLANGCI_LINT_VERSION#v}-$os-$arch"
  local work
  mkdir -p "$tools_dir"
  work=$(mktemp -d "$tools_dir/.golangci-lint.XXXXXX")
  curl -fsSL -o "$work/$name.tar.gz" \
    "https://github.com/golangci/golangci-lint/releases/download/$GOLANGCI_LINT_VERSION/$name.tar.gz"
  local got
  got=$(sha256 "$work/$name.tar.gz")
  if [ "$got" != "$want" ]; then
    rm -rf "$work"
    echo "tools.sh: $name.tar.gz checksum mismatch: got $got, want $want" >&2
    exit 1
  fi
  tar -xzf "$work/$name.tar.gz" -C "$work"
  mv "$work/$name/golangci-lint" "$bin"
  chmod +x "$bin"
  rm -rf "$work"
}

ensure_go_tool() {
  local name=$1 pkg=$2 version=$3
  local bin="$tools_dir/$name-$version"
  [ -x "$bin" ] && return 0

  local work
  mkdir -p "$tools_dir"
  work=$(mktemp -d "$tools_dir/.$name.XXXXXX")
  # Tools install from the module proxy as consumers would, outside any
  # module or workspace in the caller's directory.
  (cd "$work" && GOWORK=off GOFLAGS=-mod=mod GOBIN="$work" go install "$pkg@$version")
  mv "$work/$name" "$bin"
  rm -rf "$work"
}

ensure() {
  case "$1" in
    golangci-lint) ensure_golangci_lint ;;
    govulncheck) ensure_go_tool govulncheck golang.org/x/vuln/cmd/govulncheck "$GOVULNCHECK_VERSION" ;;
    deadcode) ensure_go_tool deadcode golang.org/x/tools/cmd/deadcode "$DEADCODE_VERSION" ;;
    gorelease) ensure_go_tool gorelease golang.org/x/exp/cmd/gorelease "$GORELEASE_VERSION" ;;
    gremlins) ensure_go_tool gremlins github.com/go-gremlins/gremlins/cmd/gremlins "$GREMLINS_VERSION" ;;
    *) echo "tools.sh: unknown tool '$1'" >&2; exit 2 ;;
  esac
}

version_of() {
  case "$1" in
    golangci-lint) echo "$GOLANGCI_LINT_VERSION" ;;
    govulncheck) echo "$GOVULNCHECK_VERSION" ;;
    deadcode) echo "$DEADCODE_VERSION" ;;
    gorelease) echo "$GORELEASE_VERSION" ;;
    gremlins) echo "$GREMLINS_VERSION" ;;
  esac
}

cmd=${1:-}
if [ -z "$cmd" ]; then
  echo "usage: scripts/tools.sh [install|golangci-lint|govulncheck|deadcode|gorelease|gremlins|versions] [args...]" >&2
  exit 2
fi
shift

case "$cmd" in
  install)
    # gremlins runs only in the nightly workflow and installs on first use.
    for tool in golangci-lint govulncheck deadcode gorelease; do
      ensure "$tool"
    done
    ;;
  golangci-lint | govulncheck | deadcode | gorelease | gremlins)
    ensure "$cmd"
    exec "$tools_dir/$cmd-$(version_of "$cmd")" "$@"
    ;;
  versions)
    for tool in golangci-lint govulncheck deadcode gorelease gremlins; do
      echo "$tool=$(version_of "$tool")"
    done
    ;;
  *)
    echo "tools.sh: unknown command '$cmd'" >&2
    exit 2
    ;;
esac
