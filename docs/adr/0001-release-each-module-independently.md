---
status: "accepted"
date: 2026-11-14
decision-makers: Brian Nunez
---

# Release each module independently from one repository

## Context and Problem Statement

bkit holds general-purpose Go modules that applications adopt one at a time.
An application that needs one module should not download, build, or upgrade
the others, and a change to one module should not force a new version of the
rest. Go ties versions to module paths and tags, so the repository layout
decides how modules are versioned and consumed. How should bkit structure
and release its modules?

## Decision Drivers

* An application depends only on the modules it imports, with no
  transitive dependencies from modules it does not use.
* Each module gets its own semantic version and changelog.
* One set of tooling covers every module: build, lint, the coverage gate,
  and releases.
* Releases are automatic and safe, with no hand-made tags.

## Considered Options

* One repository, one Go module per directory, each released with its own
  tags
* One repository, a single Go module for everything
* One repository per module

## Decision Outcome

Chosen option: "One repository, one Go module per directory, each released
with its own tags", because it is the only option that gives every module its
own dependencies and version while keeping one place for tooling, CI, and
review.

Every directory listed in `COMPONENTS` in the root `Makefile` is a module
with its own `go.mod`, path `github.com/brian-nunez/bkit/<module>`, and tags
of the form `<module>/vX.Y.Z`. semantic-release runs once per module from its
directory, in `COMPONENTS` order, and the in-repo `bkit-commit-filter` limits
each run to commits that touch that module, excluding nested modules.

### Consequences

* Good, because an application pulls in only the modules it imports and
  their own dependencies.
* Good, because each module is versioned, released, and upgraded on its own
  schedule.
* Good, because lint rules, the coverage gate, CI, and release tooling are
  defined once in the root and shared by every module.
* Bad, because modules depend on each other through released versions only,
  so a change that spans a module and its dependent ships in two steps.
* Bad, because release tooling is more complex: `COMPONENTS` order, the
  commit filter, nested module exclusion, and `GOWORK=off` for every build.
* Bad, because a squash merge that touches several modules releases each of
  them with the same commit type, so pull requests should change one module
  where possible.

### Confirmation

`scripts/check-components.sh`, run by `make check` and CI, fails when a
module is missing from `COMPONENTS`, lacks its files, has the wrong
`tagFormat`, or precedes a module it depends on. During a release,
`scripts/check-go-module.sh` rejects `replace` directives, untidy `go.mod`
files, failing tests, and module paths that do not match the major version.
`scripts/release-packages.sh` builds the release matrix from modules with
releasing commits.

## Pros and Cons of the Options

### One repository, one Go module per directory, each released with its own tags

Each module directory has a `go.mod`, a `Makefile`, and a `.releaserc.json`,
and is tagged `<module>/vX.Y.Z`.

* Good, because dependencies and versions are scoped to each module.
* Good, because shared tooling and review live in one place.
* Good, because Go's module proxy resolves subdirectory modules from tags
  directly.
* Neutral, because developers can still use a personal `go.work` for editor
  support, as long as builds never depend on it.
* Bad, because the release pipeline needs per-module configuration and
  ordering.
* Bad, because changes across modules need released intermediate versions.

### One repository, a single Go module for everything

One `go.mod` at the root, one version for the whole repository.

* Good, because releases and cross-package changes are simple.
* Bad, because every application inherits the dependencies of every package,
  including heavy client libraries it never uses.
* Bad, because a change to any package bumps the version of all of them.

### One repository per module

Each module lives in its own repository with its own tooling.

* Good, because each module is fully isolated, with simple tags.
* Bad, because tooling, lint rules, the coverage gate, and CI are duplicated
  in every repository and drift apart.
* Bad, because changes that span modules need coordinated pull requests in
  several repositories.

## More Information

The release flow, recovery steps, and CI details are in
[docs/development-and-release.md](../development-and-release.md). The rules
for adding a module are in the `add-bkit-package` skill under
`.agents/skills/`.
