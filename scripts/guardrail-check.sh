#!/usr/bin/env bash
#
# Audits a git diff for changes to bkit's deterministic guardrails, or for
# newly added lint and test bypasses, so that weakening a check is always a
# visible, explicitly approved decision.
#
# Guardrails: linter and formatter config, every Makefile (including
# COVERAGE_EXCLUDE), make/, scripts/, .github/ (workflows, CODEOWNERS, release
# tooling), commitlint config, .gitignore, AGENTS.md, the agent skills, and
# the driver conformance suites every driver runs (objex/objextest,
# rdb/rdbtest, stash/stashtest).
#
# Bypasses, on added lines of non-vendored Go files: //nolint, //lint:ignore,
# #nosec, and //go:build constraints anywhere, and t.Skip, t.Skipf, or
# t.SkipNow in test files and conformance suites.
#
# Usage:
#   scripts/guardrail-check.sh [--base <ref>] [--head <ref>] [--markdown]
#
# Without --base, the base is the merge base of --head and origin/main.
#
# Exit codes:
#   0  no guardrail change or bypass, or GUARDRAIL_APPROVED=true
#   1  guardrail change or bypass without approval
#   2  usage error

set -euo pipefail

base=""
head="HEAD"
markdown=false

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
    --markdown)
      markdown=true
      shift
      ;;
    -h | --help)
      sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      echo "guardrail-check: unknown argument: $1" >&2
      exit 2
      ;;
  esac
done

if [ -z "$base" ]; then
  if git rev-parse --verify -q origin/main >/dev/null; then
    base=$(git merge-base origin/main "$head")
  elif git rev-parse --verify -q "$head~1" >/dev/null; then
    base="$head~1"
  else
    echo "guardrail-check: specify --base <ref>" >&2
    exit 2
  fi
fi

is_conformance_suite() {
  case "$1" in
    objex/objextest/* | rdb/rdbtest/* | stash/stashtest/*) return 0 ;;
    *) return 1 ;;
  esac
}

is_guardrail_file() {
  case "$1" in
    .golangci.yml | .commitlintrc.json | .gitignore | AGENTS.md | CLAUDE.md) return 0 ;;
    Makefile | */Makefile | make/* | scripts/* | .github/* | .agents/*) return 0 ;;
  esac
  is_conformance_suite "$1"
}

changed_guardrails=()
while IFS= read -r file; do
  [ -z "$file" ] && continue
  if is_guardrail_file "$file"; then
    changed_guardrails+=("$file")
  fi
done < <(git diff --name-only --no-renames "$base" "$head")

bypass_hits=()
skip_hits=()
current_file=""

while IFS= read -r line; do
  case "$line" in
    "+++ b/"*)
      current_file="${line#+++ b/}"
      continue
      ;;
    "+++ "* | "--- "*)
      current_file=""
      continue
      ;;
    "+"*) ;;
    *) continue ;;
  esac
  [ -z "$current_file" ] && continue
  case "$current_file" in
    vendor/* | */vendor/*) continue ;;
  esac

  added="${line#+}"
  if [[ "$added" =~ //[[:space:]]*nolint || "$added" =~ //[[:space:]]*lint:ignore || "$added" =~ \#nosec || "$added" =~ ^[[:space:]]*//go:build ]]; then
    bypass_hits+=("$current_file: $added")
  fi
  if [[ "$current_file" == *_test.go ]] || is_conformance_suite "$current_file"; then
    if [[ "$added" =~ (^|[^[:alnum:]_])[[:alnum:]_]+\.Skip(f|Now)?\( ]]; then
      skip_hits+=("$current_file: $added")
    fi
  fi
done < <(git diff --unified=0 --no-renames "$base" "$head" -- '*.go')

if [ "${#changed_guardrails[@]}" -eq 0 ] && [ "${#bypass_hits[@]}" -eq 0 ] && [ "${#skip_hits[@]}" -eq 0 ]; then
  if [ "$markdown" = true ]; then
    echo "No guardrail changes or lint/test bypasses detected."
  else
    echo "guardrail-check: ok (no guardrail changes or bypasses)"
  fi
  exit 0
fi

trim() {
  local s="$1"
  s="${s#"${s%%[![:space:]]*}"}"
  printf '%s' "$s"
}

if [ "$markdown" = true ]; then
  cat <<'EOF'
<!-- guardrail-alarm -->
## Guardrail change alarm

This pull request changes bkit's deterministic guardrails or adds a lint or test bypass.
**Do not merge until the reason for each change below is understood and accepted.**

EOF
  if [ "${#changed_guardrails[@]}" -gt 0 ]; then
    echo "### Guardrail files changed"
    for f in "${changed_guardrails[@]}"; do
      echo "- \`$f\`"
    done
    echo ""
  fi
  if [ "${#bypass_hits[@]}" -gt 0 ]; then
    echo "### Lint bypasses or build constraints added"
    for h in "${bypass_hits[@]}"; do
      echo "- \`$(trim "$h")\`"
    done
    echo ""
  fi
  if [ "${#skip_hits[@]}" -gt 0 ]; then
    echo "### Test skips added"
    for h in "${skip_hits[@]}"; do
      echo "- \`$(trim "$h")\`"
    done
    echo ""
  fi
  cat <<'EOF'
### Reviewer checklist
1. **Why** is this rule, check, workflow, conformance test, or bypass changing?
2. Does the change **tighten** or **loosen** enforcement?
3. Once reviewed and accepted, add the `guardrail-approved` label to clear this alarm.
EOF
else
  echo "ALARM: guardrail change or bypass detected"
  if [ "${#changed_guardrails[@]}" -gt 0 ]; then
    echo "  Guardrail files changed:"
    for f in "${changed_guardrails[@]}"; do
      echo "    - $f"
    done
  fi
  if [ "${#bypass_hits[@]}" -gt 0 ]; then
    echo "  Lint bypasses or build constraints added:"
    for h in "${bypass_hits[@]}"; do
      echo "    - $(trim "$h")"
    done
  fi
  if [ "${#skip_hits[@]}" -gt 0 ]; then
    echo "  Test skips added:"
    for h in "${skip_hits[@]}"; do
      echo "    - $(trim "$h")"
    done
  fi
  echo "  After review, set GUARDRAIL_APPROVED=true locally or add the 'guardrail-approved' label to the pull request."
fi

if [ "${GUARDRAIL_APPROVED:-}" = "true" ]; then
  exit 0
fi
exit 1
