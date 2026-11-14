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
- **Docs.** `doc.go` carries the package comment (`//` lines): capability,
  ownership boundary, caller responsibilities, zero-value and concurrency
  behavior. Every exported identifier has godoc.
- **Commits** are Conventional Commits scoped by module path:
  `feat(<module>): ...`, `fix(<module>/drivers/<driver>): ...`. Repository-wide
  changes use `ci`, `build`, `docs`, or `chore` with a descriptive scope.
- **Tags come only from the release workflow.** A breaking change that
  crosses v2 must change the module path to `/vN` in the same commit;
  `scripts/check-go-module.sh` blocks the release otherwise.

## Tooling

Versions are pinned once in `make/development.mk` (Go, golangci-lint, the
`go` directive) and read by CI; change them there only. Release tooling is
pinned in `.github/release/package.json`, including the in-repo
`bkit-commit-filter` plugin.

## Validation

```shell
make doctor
make check
```

`make check` runs `check-components`, format check, lint, vet, and
race-enabled tests for the root and every component. For one module:
`make -C <module> check`. Release flow, recovery, and CI details live in
`docs/development-and-release.md`.
