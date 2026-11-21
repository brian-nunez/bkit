# bkit agent guide

bkit is a Go multi-module monorepo: every directory listed in `COMPONENTS` in
the root `Makefile` is an independently released module at
`github.com/brian-nunez/bkit/<module>`. The root module holds only `doc.go`.

## Adding a module

Use the `add-bkit-package` skill
(`.agents/skills/add-bkit-package/SKILL.md`) for any new module or driver.
It owns the module file set,
templates, root registration, and validation steps.

## Rules

- **General-purpose.** Modules serve any application: name capabilities,
  never a specific consumer. Use type parameters where they make an API
  safer or simpler, not by default.
- **Standard library first.** A new direct dependency needs Brian's approval;
  heavy third-party dependencies belong in driver modules so the parent
  stays dependency-light.
- **Released dependencies only.** Modules depend on other bkit modules
  through published versions. `replace` directives block a release; only
  unreleased `examples/` modules may use one.
- **GOWORK=off.** `make/development.mk` exports it, so make, CI, and releases
  build each module from its own `go.mod`. A personal `go.work` is fine for
  editor support and stays gitignored; never rely on it for a passing build.
- **Release order.** `COMPONENTS` order is release order: a module follows
  every bkit module it depends on, and a driver follows its parent.
- **Errors** are sentinel values matched with `errors.Is` / `errors.As`;
  no panic-driven control flow.
- **100% coverage.** Every module's tests reach 100% statement coverage
  without external services; `coverage-check` enforces it in `make check`,
  CI, and the pre-release gate. Reach external services through an internal
  interface and an in-memory fake, delete unreachable branches, and never
  change the public API for testability. `COVERAGE_EXCLUDE` is only for a
  shared driver conformance suite (`objex/objextest`, `rdb/rdbtest`,
  `stash/stashtest`), and it also exempts that suite from `deadcode`.
- **Docs.** `doc.go` carries the package comment (`//` lines): capability,
  ownership boundary, caller responsibilities, zero-value and concurrency
  behavior. Every exported identifier has godoc.
- **README prose** is plain and direct: short declarative sentences joined
  by periods and commas. Say what the module does and how to use it, each
  fact stated as what is true. Hard limits: no em dashes, no semicolons, no
  ownership framing ("rdb owns ...", "callers supply ..."), and no
  "not X, it's Y" contrasts.
- **Tags come only from the release workflow.** A breaking change that
  crosses v2 must change the module path to `/vN` in the same commit;
  `scripts/check-go-module.sh` blocks the release otherwise.

## Tooling

Versions are pinned once in `make/development.mk` (Go, the `go` directive,
golangci-lint, govulncheck, deadcode, gorelease, gremlins) and read by CI;
change them there only. `scripts/tools.sh` installs each tool into `.tools/`
(golangci-lint from its release archive, checked against a pinned SHA-256),
and both make and CI run tools only through it. Release tooling is
pinned in `.github/release/package.json`, including the in-repo
`bkit-commit-filter` plugin.

## Validation

```shell
make doctor
make check
```

`make check` runs `check-components`, format check, lint, vet,
`tidy-check`, race-enabled tests, `coverage-check` (100% statement
coverage), `deadcode`, and `vuln` (govulncheck) for the root and every
component, then builds, vets, and tests the example modules. CI runs the
same targets and fails when a check leaves the working tree dirty. For one module: `make -C <module> check`; after `make -C <module>
tests`, `make -C <module> coverage-check` lists any function below 100%. Release flow, recovery, and CI details live in
`docs/development-and-release.md`.

## Commits

Every commit follows [Conventional Commits 1.0.0](https://www.conventionalcommits.org/en/v1.0.0/),
and commitlint checks it in CI. Pull requests are squash-merged, so the pull
request title becomes the commit the release tooling reads.

```text
<type>(<scope>): <summary>

<body>

<footers>
```

*   **Type**: `feat` (new behavior), `fix` (corrected behavior), `docs`,
    `refactor` (no behavior change), `perf`, `test`, `build`, `ci`, `style`
    (formatting only), `chore`, `revert`. In a module, `feat` releases a minor
    version, `fix`, `perf`, and `revert` a patch, and the rest nothing.
*   **Scope**: the module path, `<module>` or `<module>/drivers/<driver>`.
    Repository-wide changes use `ci`, `build`, `docs`, or `chore` with a
    descriptive scope such as `release`, `readme`, or `coverage`.
*   **Summary**: imperative mood, lowercase, no trailing period, 72
    characters or fewer. It names the change in behavior:
    `fix(<module>): reject empty keys`, not `fix(<module>): update keys.go`.
*   **Body**: required unless the summary alone tells a reviewer everything.
    Wrap at 72 columns (commitlint rejects body lines over 100). State:
    *   **Why**: the problem, bug, or need that prompted the change.
    *   **What**: the approach, and what now behaves differently.
    *   **Impact**: anything a reader must know: follow-up releases,
        configuration changes, trade-offs taken.

    Every sentence carries information a reviewer cannot get from the diff.
*   **Footers**: `BREAKING CHANGE: <what breaks and how to migrate>` for
    incompatible changes (and `!` after the type or scope); a breaking change
    past v1 also changes the module path to `/vN`. `Refs: #<issue>` or
    `Closes: #<issue>` for tracked work.
*   One logical change per commit, and one module per pull request where
    possible: a squash merge that touches several modules releases each of
    them with the same type.

## Decisions

Decisions that are costly to reverse are recorded in [`docs/adr/`](docs/adr/README.md)
as MADR records, and get a record before implementation. Start from
[`docs/adr/adr-template.md`](docs/adr/adr-template.md) and add the record to
the index.

## Skills

*   `add-bkit-package` (`.agents/skills/add-bkit-package/SKILL.md`): adding
    any new module or driver.

## Boundaries

*   `vendor/`, `coverage.out`, `coverage.check.out`, and `.tools/` are build
    output: never edit or commit them.
*   `go.sum` changes only through `go mod tidy` or `go get`.
*   Tags and GitHub Releases come only from the release workflow. Recovering
    a partial release follows `docs/development-and-release.md` and needs
    Brian's approval.
*   A new direct dependency in any module needs Brian's approval.
*   Pull requests are merged by Brian. Driver pull requests that need an
    unreleased parent wait for the parent's release before leaving draft.
