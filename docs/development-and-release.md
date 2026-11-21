# Development and release

How the bkit monorepo is built, checked, and released.

## Repository model

```text
bkit/
├── go.mod, doc.go          root module (documentation only, never released)
├── Makefile                COMPONENTS registry and repo-wide targets
├── make/development.mk     pinned versions, GOWORK=off, doctor, tools
├── <module>/               an independently released module
│   ├── go.mod              github.com/brian-nunez/bkit/<module>
│   ├── Makefile            module targets (includes make/development.mk)
│   ├── .releaserc.json     semantic-release config, tagFormat <module>/v${version}
│   ├── drivers/<driver>/   nested driver modules, released separately
│   └── examples/           optional unreleased example module
├── scripts/                tools, component, guardrail, pre-release, and post-release checks
└── .github/
    ├── workflows/          commitlint, lint, tests, release
    └── release/            pinned semantic-release tooling and bkit-commit-filter
```

`COMPONENTS` in the root `Makefile` is the single inventory of released
modules, listed in release order. `scripts/check-components.sh` keeps it
honest: every `go.mod` outside the root and `examples/` must be registered;
every component needs `go.mod`, `Makefile` (with a `coverage-check`
target), `.releaserc.json`, `README.md`, and `doc.go`; module paths, `go`
directives, and tag formats must match; and a driver must follow its parent.

## Coverage

Every module must reach 100% statement coverage from its own tests, without
external services. Each module Makefile's `coverage-check` reads the
`coverage.out` written by `tests`, fails below `100.0%`, and lists every
function under it. It runs in three places:

- `make check` (root and per module);
- the **Check coverage** step of `tests.yml`, after **Run tests**;
- `scripts/check-go-module.sh`, before a release is tagged.

Code that talks to an external service is covered through an internal
interface and an in-memory fake (the s3 driver runs the full objex contract
suite against a fake S3 client); OS failures that cannot be produced on
demand are covered through an unexported seam. Tests against real services,
such as the s3 driver's MinIO suite, are additional and skipped unless their
environment variable is set.

A module Makefile may set `COVERAGE_EXCLUDE`, an extended regular expression
of package paths, only for a shared test-helper package other modules import
from their tests. `deadcode` skips the same packages, since their functions
are called only from other modules. The uses are the driver conformance
suites: `objex/objextest`, `rdb/rdbtest`, and `stash/stashtest`.

## Deterministic checks

Every module Makefile runs these in `check`, and `tests.yml` runs each one as
its own step:

| Target | Fails when |
| --- | --- |
| `tidy-check` | `go mod tidy -diff` reports a change |
| `coverage-check` | statement coverage is below 100% |
| `deadcode` | `deadcode -test` finds a function no test or caller reaches |
| `vuln` | govulncheck finds a known vulnerability in code the module calls |

The root `make check` also runs `make test-scripts` (the tests for the
scripts in `scripts/`), `make no-bypass` (no `//nolint`, `//lint:ignore`, or
`#nosec` in any Go file), and `tidy-check` for the root module and every
example, and `make examples` builds, vets, and race-tests the example
modules. After the checks, CI runs `scripts/check-clean-tree.sh`, which
fails when a check changed a tracked file or left an untracked one.

### Workspaces

`make/development.mk` exports `GOWORK=off`, and CI and releases set it too.
Each module therefore builds against the released versions in its own
`go.mod`, exactly as a consumer does. A personal `go.work` (gitignored) can
help an editor resolve cross-module code, but it never affects a check.

### Cross-module changes

A change that needs an unreleased change in another bkit module ships in two
steps:

1. Merge the dependency change; the release workflow tags it.
2. Bump the requirement in the dependent module (`go get
   github.com/brian-nunez/bkit/<dependency>@vX.Y.Z`) and merge.

## Pinned versions

| What | Where | Value |
| --- | --- | --- |
| Go toolchain for development and CI | `REQUIRED_GO_VERSION`, `make/development.mk` | 1.27.4 |
| `go` directive in every `go.mod` | `GO_MOD_VERSION`, `make/development.mk` | 1.26.0 |
| golangci-lint | `REQUIRED_GOLANGCI_LINT_VERSION`, `make/development.mk`, checksums in `scripts/tools.sh` | 2.15.0 |
| govulncheck | `GOVULNCHECK_VERSION`, `make/development.mk` | v1.10.0 |
| deadcode | `DEADCODE_VERSION`, `make/development.mk` | v0.53.0 |
| gorelease | `GORELEASE_VERSION`, `make/development.mk` | pseudo-version |
| gremlins | `GREMLINS_VERSION`, `make/development.mk` | v0.6.0 |
| semantic-release and plugins | `.github/release/package.json` | exact pins |
| GitHub Actions | workflow files | full commit SHAs |

Workflows read the Go version through `make print-required-go-version`, and
`scripts/tools.sh` reads every tool version from `make/development.mk`, so
changing that file changes CI. Make targets and workflows run tools only
through `scripts/tools.sh`, which installs them into `.tools/`; CI caches
that directory keyed on both files. The `go` directive is the
minimum Go version consumers need; keep it at the oldest supported Go
release.

## Continuous integration

| Workflow | Runs on | Does |
| --- | --- | --- |
| `commitlint.yml` | pull requests | Lints commit messages and the PR title. **API compatibility** runs `scripts/api-check.sh` |
| `guardrails.yml` | pull requests, including label changes | **Guardrail audit**: runs the base branch's `scripts/guardrail-check.sh`, posts one alarm comment, and fails until the `guardrail-approved` label is applied |
| `lint.yml` | push to `main`, pull requests, merge queue | Format check, golangci-lint, and `go vet` in every directory with a `go.mod`, including examples |
| `tests.yml` | push to `main`, pull requests, merge queue | `check-components`; per component `tidy-check`, race tests, `coverage-check`, `deadcode`, and `vuln`; root and example tidiness and example tests; a clean working tree after each job |
| `release.yml` | push to `main` or `beta`, manual dispatch | Releases each component with new commits |

`lint-result` and `tests-result` aggregate their matrices into single checks
that stay stable as modules are added. Make those, **Guardrail audit**, and
**API compatibility** the required status checks.

### API compatibility

The commit type decides the release version, so an incompatible change in a
`feat` or `fix` commit would ship as a minor or patch release and break
consumers. `scripts/api-check.sh` finds every component a pull request
touches and runs gorelease against the module's latest stable tag, from a
clean checkout of the head commit. It fails on an incompatible change unless
a commit marks it breaking, with `!` before the colon or a
`BREAKING CHANGE:` footer. A module with no stable tag is skipped. Run it
locally with `make api-check`, which compares committed work with
`origin/main`.

### Guardrail audit

`scripts/guardrail-check.sh` compares a pull request with its base. It
fails when the change touches a guardrail file (`.golangci.yml`, any
`Makefile`, `make/`, `scripts/`, `.github/`, `.commitlintrc.json`,
`.gitignore`, `AGENTS.md`, `.agents/`, or a conformance suite under
`objex/objextest`, `rdb/rdbtest`, or `stash/stashtest`) or adds a
`//nolint`, `//lint:ignore`, `#nosec`, `//go:build`, or `t.Skip` line. Run
it locally with `make guardrail-check`. `GUARDRAIL_APPROVED=true` clears it
locally, and the `guardrail-approved` label clears it in CI.
`scripts/guardrail-check_test.sh` covers every rule and runs in
`make test-scripts`.

## Releases

Releases are fully automated from Conventional Commits.

1. A push to `main` or `beta` starts `release.yml`.
2. `packages-to-release` validates `COMPONENTS` and runs
   `scripts/release-packages.sh`, which emits, in `COMPONENTS` order, only
   the modules with a releasing commit since their last tag: a `feat`,
   `fix`, `perf`, or `revert` commit, or a breaking change, touching the
   module outside its nested modules. On `main` only stable tags count as
   the last release.
3. `release` processes one component at a time, in `COMPONENTS` order:
   1. builds the module (`make -C <module> build-actions`);
   2. links `.github/release/node_modules` and a temporary `package.json` into
      the module directory and runs semantic-release there;
   3. `bkit-commit-filter` limits commit analysis to that directory,
      excluding nested modules, so only commits touching the module count;
   4. with relevant commits, semantic-release computes the version and runs
      `scripts/check-go-module.sh` (`prepareCmd`): module path matches the
      major version, no `replace`, tidy `go.mod`, no tracked release tooling,
      build, vet, race tests, 100% coverage, `deadcode`, and `vuln`;
   5. semantic-release pushes the tag `<module>/vX.Y.Z` and creates a GitHub
      Release with generated notes;
   6. `scripts/verify-go-module.sh` resolves the new version through
      proxy.golang.org and sum.golang.org with an empty cache, checks the zip
      for release tooling, builds it, and confirms the checksum is stable.

Modules with no releasing commits get no job. A manual run always
evaluates the module it names. No commit is ever pushed by a
release; changelogs live in GitHub Releases.

### Versions

| Commits since the last tag | Next version |
| --- | --- |
| first release with a `feat` or `fix` | `v1.0.0` |
| `fix` | patch |
| `feat` | minor |
| breaking change | major |

Pushes to `beta` produce prereleases such as `<module>/v1.1.0-beta.1`, which Go
resolves only when asked for explicitly. Merge `beta` into `main` to promote.

### Major versions

Go requires a module at v2 or later to end its path in `/vN`. Before merging
a breaking change to a v1 module, change the module path (and every import of
it inside bkit) to `github.com/brian-nunez/bkit/<module>/v2` in the same pull
request. `check-go-module.sh` fails the release, before any tag exists, when
the path and the major version disagree.

## Recovering from a partial release

Published versions are permanent: proxy.golang.org caches every version it
has served. Never delete or move a tag that has been pushed; release a fix
instead, and add a `retract` directive to `go.mod` for a version consumers
must avoid.

| Failure | State | Recovery |
| --- | --- | --- |
| `check-go-module.sh` (prepare) failed | No tag | Fix the cause and merge; the next push releases |
| Tag pushed, GitHub Release missing | Tag exists, no release page | `gh release create <module>/vX.Y.Z --generate-notes --verify-tag` |
| Proxy verification failed | Tag and release exist | Run `release.yml` manually with `module` and `version` to re-verify; an empty run means no new release, only verification |
| One component failed, others released | Mixed | Fix and rerun `release.yml` manually for the failed `module`; dependents already reference published versions |
| A broken version was published | Permanent | Release a fix, then retract the broken version with a `fix(<module>)` commit adding `retract vX.Y.Z` to `go.mod` |

## Repository settings (configured on GitHub)

These live in GitHub settings, not in files:

- Pull requests: allow squash merging only; default the squash commit message
  to the pull request title.
- Branch protection on `main` and `beta`: require pull requests, the merge
  queue, and the `lint-result`, `tests-result`, and `commitlint` checks.
- Actions: workflow permissions read-only by default (`release.yml` requests
  `contents: write` itself).
