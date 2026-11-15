# bkit

Composable, general-purpose Go modules.

bkit is a monorepo of independently versioned modules. Each module has its
own `go.mod`, depends only on what it needs, and is released with its own
tags, so you import exactly the pieces you use.

## Modules

| Module | Import path | Purpose |
| --- | --- | --- |
| [objex](objex) | `github.com/brian-nunez/bkit/objex` | Backend-independent object storage API |
| [objex/drivers/memory](objex/drivers/memory) | `github.com/brian-nunez/bkit/objex/drivers/memory` | In-memory objex driver |
| [objex/drivers/filesystem](objex/drivers/filesystem) | `github.com/brian-nunez/bkit/objex/drivers/filesystem` | objex driver for a local directory |
| [objex/drivers/s3](objex/drivers/s3) | `github.com/brian-nunez/bkit/objex/drivers/s3` | objex driver for S3 and S3-compatible services |
| [gate](gate) | `github.com/brian-nunez/bkit/gate` | Typed, composable boolean rules and checks |
| [settings](settings) | `github.com/brian-nunez/bkit/settings` | Configuration loaded and merged from any number of sources |
| [settings/drivers/yaml](settings/drivers/yaml) | `github.com/brian-nunez/bkit/settings/drivers/yaml` | YAML format for settings |
| [settings/drivers/toml](settings/drivers/toml) | `github.com/brian-nunez/bkit/settings/drivers/toml` | TOML format for settings |
| [settings/drivers/vault](settings/drivers/vault) | `github.com/brian-nunez/bkit/settings/drivers/vault` | settings sources for HashiCorp Vault key/value secrets |
| [settings/drivers/consul](settings/drivers/consul) | `github.com/brian-nunez/bkit/settings/drivers/consul` | settings sources for the HashiCorp Consul key/value store |
| [rdb](rdb) | `github.com/brian-nunez/bkit/rdb` | One API over relational databases: pooling, placeholders, scanning, errors, transactions |
| [rdb/drivers/sqlite](rdb/drivers/sqlite) | `github.com/brian-nunez/bkit/rdb/drivers/sqlite` | rdb driver for SQLite |
| [rdb/drivers/postgres](rdb/drivers/postgres) | `github.com/brian-nunez/bkit/rdb/drivers/postgres` | rdb driver for PostgreSQL, built on pgx |
| [rdb/drivers/mysql](rdb/drivers/mysql) | `github.com/brian-nunez/bkit/rdb/drivers/mysql` | rdb driver for MySQL and MariaDB |
| [stash](stash) | `github.com/brian-nunez/bkit/stash` | One API over key-value stores, with expiry, atomic operations, and typed values |
| [stash/drivers/memory](stash/drivers/memory) | `github.com/brian-nunez/bkit/stash/drivers/memory` | In-process stash store |
| [stash/drivers/database](stash/drivers/database) | `github.com/brian-nunez/bkit/stash/drivers/database` | stash store in a SQL database opened with rdb |
| [stash/drivers/redis](stash/drivers/redis) | `github.com/brian-nunez/bkit/stash/drivers/redis` | stash store on a Redis server |
| [stash/drivers/valkey](stash/drivers/valkey) | `github.com/brian-nunez/bkit/stash/drivers/valkey` | stash store on a Valkey server |

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
make check    # format check, lint, vet, race-enabled tests, and 100% coverage for every module
```

Every module must reach 100% statement coverage from its own tests, with no
external services. CI and releases enforce it with `make coverage-check`.

Run `make help` for every target, or `make -C <module> help` for one module.
[CONTRIBUTING.md](CONTRIBUTING.md) covers commits and pull requests.

## License

[MIT](LICENSE)
