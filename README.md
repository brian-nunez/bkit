# bkit

Composable, general-purpose Go modules.

bkit is a monorepo of independently versioned modules. Each module has its
own `go.mod`, depends only on what it needs, and is released with its own
tags, so you import exactly the pieces you use.

## Modules

No modules have been released yet. Each module is listed here as it lands.

| Module | Import path | Purpose |
| --- | --- | --- |

Drivers are nested modules under their parent, imported as
`github.com/brian-nunez/bkit/<module>/drivers/<driver>`.

## Installing a module

```shell
go get github.com/brian-nunez/bkit/<module>@latest
```

Modules require Go 1.26 or newer.

## Versioning

Every module follows [Semantic Versioning](https://semver.org/) with tags of
the form `<module>/vX.Y.Z`, or `<module>/drivers/<driver>/vX.Y.Z` for a
driver. Prereleases use `-beta.N`. Releases are cut
automatically from [Conventional Commits](https://www.conventionalcommits.org/);
see [docs/development-and-release.md](docs/development-and-release.md).

## Development

Requirements: Go 1.27.4, `make`, `jq`, and Git. Node.js is needed only to run
release tooling locally.

```shell
make tools    # install the pinned golangci-lint into .tools/bin
make doctor   # confirm Go and golangci-lint versions
make check    # format check, lint, vet, and race-enabled tests for every module
```

Run `make help` for every target, or `make -C <module> help` for one module.
[CONTRIBUTING.md](CONTRIBUTING.md) covers commits and pull requests.

## License

[MIT](LICENSE)
