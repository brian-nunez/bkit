---
status: "accepted"
date: 2026-11-21
decision-makers: Brian Nunez
---

# Enforce quality with deterministic checks, and alarm on any change that weakens them

## Context and Problem Statement

bkit modules are written and changed at high volume, much of it by coding
agents. Reviewing every line by hand does not scale, and rules written only
in prose (AGENTS.md, READMEs) are followed until they are not. Before this
decision, vulnerability scans, `go mod tidy`, and dead-code checks ran by
hand or only at release time, and nothing flagged a pull request that
loosened a linter, excluded a package from coverage, skipped a test, or
weakened a driver conformance suite to get green. How does bkit keep its
quality bar when nobody reads every line?

## Decision Drivers

* A mistake caught once is caught every time after: enforcement lives in
  code that runs, never in a reviewer's memory.
* Local runs and CI use the same tools at the same versions.
* A check can still change, but never quietly.
* Modules stay free of dev-only dependencies.

## Considered Options

* Deterministic checks plus a guardrail tamper alarm
* Deterministic checks only, relying on review to notice weakened rules
* Prose rules and human review

## Decision Outcome

Chosen option: "Deterministic checks plus a guardrail tamper alarm",
because it is the only option where weakening a check is always a visible,
explicit decision.

* **Pinned tools.** Every tool version lives in `make/development.mk`.
  `scripts/tools.sh` installs each into `.tools/` (golangci-lint from its
  release archive, checked against a pinned SHA-256) and is the only way
  make targets and workflows run them.
* **Checks as targets.** Every check is a make target in `make check` and a
  CI step: format, lint, vet, `tidy-check`, race tests, 100% coverage,
  `deadcode`, `vuln`, the component registry, and the script tests. CI fails
  when a check leaves the working tree dirty. An ad hoc check run once by
  hand does not count.
* **Tamper alarm.** `scripts/guardrail-check.sh` fails a pull request that
  changes a guardrail file (linter config, any Makefile, `make/`,
  `scripts/`, `.github/`, `.commitlintrc.json`, `.gitignore`, `AGENTS.md`,
  `.agents/`, or a driver conformance suite) or adds a bypass (`//nolint`,
  `//lint:ignore`, `#nosec`, `//go:build`, `t.Skip`). `guardrails.yml` runs
  the base branch's copy of the script, posts one alarm comment, and fails
  until the pull request has the `guardrail-approved` label. CODEOWNERS
  lists the same paths.

### Consequences

* Good, because quality no longer depends on a reviewer noticing a
  loosened rule: the alarm names every one.
* Good, because a new advisory, untidy module, or unreachable function
  fails the pull request that introduces it, before a release.
* Bad, because changes to tooling always need the label, even when they
  tighten enforcement.
* Bad, because a new vulnerability advisory can fail an unrelated pull
  request until the affected dependency or Go version is upgraded.

### Confirmation

`scripts/guardrail-check_test.sh` (run by `make test-scripts` in
`make check` and CI) proves each guardrail path and bypass trips the alarm
and that approval clears it. Branch protection on `main` requires
`tests-result`, `lint-result`, and `Guardrail audit`.

## More Information

The approach follows the guardrails in Brian's breadboard repository, adapted
to bkit's many independently released modules.
