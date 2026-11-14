# Contributing to bkit

## Setup

1. Install Go 1.27.4 (the exact version `make doctor` checks), `make`, `jq`,
   and Git.
2. Install the pinned linter and confirm your environment:

   ```shell
   make tools
   make doctor
   ```

## Workflow

bkit uses GitHub Flow: branch from `main`, open a pull request, and merge
through the merge queue. Pull requests are squash-merged, so the pull request
title becomes the commit on `main` that the release tooling reads.

Before opening a pull request:

```shell
make format
make check
```

For one module, `make -C <module> check`.

## Commit messages and pull request titles

Both follow [Conventional Commits](https://www.conventionalcommits.org/) and
are checked in CI. Scope a change by the module path it touches:

```text
feat(<module>): add <capability>
fix(<module>/drivers/<driver>): <correct behavior>
docs(<module>): document <topic>
ci(release): pin setup-node to v7.1.0
```

The type decides the release:

| Commit | Release |
| --- | --- |
| `fix:` | patch |
| `feat:` | minor |
| `BREAKING CHANGE:` footer or `!` after the type | major |
| `docs:`, `test:`, `refactor:`, `ci:`, `build:`, `chore:` | none |

A pull request should change one module where possible; a squash merge that
spans modules releases each module it touches with the same commit type.

A major release past v1 requires changing the module path to end in `/vN`
in the same pull request.

## Adding a module

Follow the `add-bkit-package` skill in
`.agents/skills/add-bkit-package/SKILL.md`; it applies to people as well as
agents. New direct dependencies need maintainer approval.

## Releases

Releases are automatic on merge to `main` (stable) and `beta`
(prereleases). Never create or push tags by hand. See
[docs/development-and-release.md](docs/development-and-release.md).
