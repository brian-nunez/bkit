# Driver modules

A driver is a module nested under its parent module, so heavy third-party
dependencies stay out of the parent's `go.mod`:

```text
<parent>/                  github.com/brian-nunez/bkit/<parent>
└── drivers/
    └── <driver>/          github.com/brian-nunez/bkit/<parent>/drivers/<driver>
```

Differences from a top-level module:

- `__MODULE__` is the full path (`<parent>/drivers/<driver>`), everywhere:
  module path, `tagFormat` (`<parent>/drivers/<driver>/v${version}`),
  `COMPONENTS`, and commit scopes.
- `__REPOSITORY_ROOT__` is `../../..`.
- The Go package name is the leaf (`<driver>`).
- `COMPONENTS` lists the driver after its parent.
- The driver requires a released parent version. A driver change that needs
  an unreleased parent change ships in two steps: merge and release the
  parent, then bump the requirement in the driver.
- The driver reaches 100% coverage without its backend. Wrap the client
  library in an unexported interface and run the parent's contract suite
  against an in-memory fake of it; tests against the real backend are extra,
  skipped unless their environment variable is set.

The parent defines the interface; the driver implements it and registers or
constructs it. The parent never imports its drivers.

Go excludes a nested module from its parent's module zip, and
`bkit-commit-filter` excludes it from the parent's release analysis, so
parent and driver release independently.

Completion criterion: `go list -m` in the driver prints its full module
path, `make check-components` passes with the driver after its parent, and
`make -C <parent>/drivers/<driver> check` reports 100.0% coverage with no
backend running.
