---
name: add-bkit-package
description: >-
  Add a new independently released Go module to bkit. Use when asked to add a
  package, library, or driver that needs its own go.mod and release tag.
  Covers module files, root registration, documentation, CI discovery, and
  release configuration. Not for an ordinary Go package inside an existing
  module.
---

# Add a bkit module

Create a complete, independently versioned Go module that follows the
repository's build, test, lint, documentation, and release contracts. The
repository files are the source of truth: when an existing module differs
from a template here, follow the repository and update this skill in the
same change.

## Establish scope

Confirm the work needs its own module and release tag: consumers must be
able to depend on it and version it separately from other bkit modules. For
an ordinary package inside an existing module, work within that module; it
gets no `go.mod`, `.releaserc.json`, `COMPONENTS` entry, or tag of its own.

Identify:

- module directory (`<module>`), or `<parent>/drivers/<driver>` for a driver;
- Go package name: short, lowercase, a valid identifier;
- one-sentence purpose;
- public API;
- ownership boundary: what the module owns and what callers keep.

For a **driver** (a module nested under another module), also read
[`drivers.md`](drivers.md).

Ask Brian when a missing value would change the public contract.

Completion criterion: module directory, package name, purpose, API, and
release independence are explicit.

## Inspect repository contracts

Read before creating files:

1. `AGENTS.md`
2. `README.md`
3. `CONTRIBUTING.md`
4. `docs/development-and-release.md`
5. Root `go.mod`, `Makefile`, `make/development.mk`, `.golangci.yml`, `.gitignore`
6. `.github/workflows/tests.yml`, `lint.yml`, `release.yml`
7. One existing module's `go.mod`, `Makefile`, `README.md`, `doc.go`, and
   `.releaserc.json`, when one exists

Take the Go version (`GO_MOD_VERSION`) and tool versions from
`make/development.mk`, never from memory.

Completion criterion: every required module file and root integration point
is accounted for against the current repository.

## Create the module

Minimum structure:

```text
<module>/
├── .releaserc.json
├── Makefile
├── README.md
├── doc.go
├── go.mod
├── <implementation>.go
└── <implementation>_test.go
```

Start from [`templates/`](templates/) and replace every placeholder:

| Placeholder | Value |
| --- | --- |
| `__MODULE__` | module directory: `<module>`, or `<parent>/drivers/<driver>` |
| `__PACKAGE__` | Go package name |
| `__REPOSITORY_ROOT__` | relative path to the repo root: `..`, or `../../..` for a driver |
| `__GO_MOD_VERSION__` | `GO_MOD_VERSION` from `make/development.mk` |
| `__SUMMARY__` | one-sentence purpose |

Once a module exists, copy from it instead of the template when they differ.

Every module ships with an implementation and tests at 100% statement
coverage; there is no doc-only placeholder module.

### go.mod

From `templates/go.mod.tmpl`:

```go
module github.com/brian-nunez/bkit/<module>

go <GO_MOD_VERSION>
```

Standard library first. Add a dependency only when the standard library or
the current dependency set cannot satisfy the feature, and only with Brian's
approval. Depend on other bkit modules through released versions only; a
`replace` directive blocks the release.

Completion criterion: `go list -m` in `<module>` prints
`github.com/brian-nunez/bkit/<module>`.

### Package documentation

`doc.go` holds the package comment, in `//` line comments:

```go
// Package <package> provides <capability>.
//
// The package owns <module responsibilities>. Callers supply <caller
// responsibilities>.
//
// Create a <Type> with New<Type>; its zero value is <usable | not
// initialized>. <Concurrency contract.>
package <package>
```

bkit modules are general-purpose: describe capabilities, never a specific
application. Document every exported identifier in its implementation file.

Completion criterion: the package builds, every exported identifier has
godoc, and the zero-value and concurrency behavior of public types is
explicit.

### Module README

`<module>/README.md` covers:

- purpose
- import path `github.com/brian-nunez/bkit/<module>`
- initialization or primary usage example
- configuration and error behavior relevant to consumers
- validation commands
- release tag format `<module>/vX.Y.Z`

Write it in the README prose style from `AGENTS.md`.

Document only implemented behavior. Keep credentials, personal filesystem
paths, and development-history language out of examples.

Completion criterion: a new consumer can install, initialize, and validate
the module using only repository documentation.

## Add the module Makefile

Copy `templates/Makefile` (or an existing module's `Makefile`) to
`<module>/Makefile`, set `MODULE_NAME` and `REPOSITORY_ROOT`, and keep the
recipe tabs. `make/development.mk` supplies the pinned Go and linter
versions, `GOWORK=off`, `doctor`, and `tools`; the module Makefile supplies
build, test, coverage, format, lint, vet, and clean.

Keep every target the root `Makefile` and workflows invoke (`build-actions`,
`tests-actions`, `coverage-check`, `check`, `format-check`, `lint`, `vet`,
`clean`). `scripts/check-components.sh` fails a component whose Makefile has
no `coverage-check`.

`coverage-check` reads the `coverage.out` written by the last test run and
fails unless total statement coverage is exactly 100.0%, listing every
function below it. Leave `COVERAGE_EXCLUDE` empty. Set it only to exclude a
shared test-helper package that other modules import from their tests (such
as `objex/objextest`, a driver contract suite), with a comment saying why;
library code is never excluded.

Completion criterion: `make -C <module> check` passes, including
`coverage-check` at 100.0%.

## Add release configuration

Copy `templates/.releaserc.json` to `<module>/.releaserc.json` and set
`tagFormat` to `<module>/v${version}`. Semantic-release runs from the module
directory; the in-repo `bkit-commit-filter` plugin limits commit analysis to
that directory and excludes nested modules, so a driver change never
releases its parent.

`prepareCmd` runs `scripts/check-go-module.sh` before the tag exists: it
rejects a module path that does not match the major version, `replace`
directives, untidy `go.mod`, failing builds or tests, and coverage below
100% (it runs the module's `tests-nobuild coverage-check`). After the tag is
pushed, the workflow runs `scripts/verify-go-module.sh` against
proxy.golang.org.

Release tooling lives in `.github/release/`; a module never gets its own
`package.json`. Tags are created only by the release workflow; for partial
release recovery follow `docs/development-and-release.md`.

Use Conventional Commit scopes that name the module:

```text
feat(<module>): add <capability>
fix(<parent>/drivers/<driver>): <correct behavior>
```

A module's first release needs a `feat` or `fix` commit touching its
directory; it releases as `v1.0.0` from `main`, or `v1.0.0-beta.N` from
`beta`.

Completion criterion: `jq empty <module>/.releaserc.json` passes and
`tagFormat` is `<module>/v${version}`.

## Register the module at the root

Add the directory to `COMPONENTS` in the root `Makefile`. Order is release
order: place a module after every bkit module it depends on, and a driver
after its parent.

```make
COMPONENTS = <dependency> <module> <module>/drivers/<driver>
```

`scripts/check-components.sh` (run by `make check` and CI) fails when a
module is missing from `COMPONENTS`, a component lacks its files or a
`coverage-check` target, a driver precedes its parent, a `tagFormat` does
not match its directory, or a `go.mod` declares a different Go version.

Update the root `README.md`: add a row to the Modules table with import path
and purpose.

Update `docs/development-and-release.md` only when the module changes the
shared workflow. Add `<module>/AGENTS.md` only for instructions that do not
apply repository-wide.

Leave the workflows, root `go.mod`, `.golangci.yml`, and `.gitignore`
unchanged unless the module introduces a genuine repository-wide
requirement; explain that requirement in the change.

Completion criterion: `make check-components` passes and the README catalog
lists the module.

## Implement, then test

Implement the requested public behavior before writing its tests, following
the repository's standard-library-first, sentinel-error (`errors.Is` /
`errors.As`), godoc, and secret-safety rules. Then:

1. Unit tests for public behavior and failure paths.
2. Integration tests only where a real boundary requires them, skipped
   cleanly when the dependency is unavailable.
3. No credentials or production data in fixtures.
4. Deterministic and safe under `-race`.

Coverage is 100% of statements from the tests CI runs, which never include
external services. Reach code that talks to a service through an internal
interface and an in-memory fake (the s3 driver runs its full contract suite
against a fake S3 client), and reach failures the OS cannot produce on demand
(fsync, close, rename races) through an unexported seam over the few calls
involved. Delete a branch no input can reach rather than leaving it
uncovered. Never change the public API to make code testable.

Completion criterion: tests cover the public contract and error handling
without production services, and `make -C <module> tests coverage-check`
reports `100.0%`.

## Validate the complete integration

From the repository root:

```shell
make -C <module> format
make -C <module> check
make check
make get-all-release-packages
jq empty <module>/.releaserc.json
```

`make get-all-release-packages` must list `<module>`. Then confirm:

- every placeholder is replaced;
- no `replace` directive;
- no secret, API key, personal path, or temporary task context in files;
- Markdown links resolve inside the repository unless they cite an external
  authoritative source;
- `make -C <module> check` reported `<module> coverage: 100.0%`;
- `vendor/`, `coverage.out`, `coverage.check.out`, `node_modules/`, and
  `package.json` are absent from the change;
- root and module documentation agree on import path and tag format.

Completion criterion: every command passes, or the report names the exact
failing command and blocker.

## Completion report

Report:

1. Module directory and package name.
2. Import path.
3. Release tag format.
4. Created files.
5. Root files updated.
6. Validation commands and results, including the coverage total.
7. Skipped checks and why.

The module is complete only when implementation, tests at 100% coverage,
release configuration, root registration, documentation, CI discovery, and
validation are all accounted for.
