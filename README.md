<p align="center">
  <img src="docs/brand/icon.svg" width="128" height="128" alt="bkit icon: white interlocking blocks on a Go-blue rounded square">
</p>

<h1 align="center">bkit</h1>

<p align="center">
  <b>General-purpose Go modules, each versioned, released, and installed on its own.</b>
</p>

<p align="center">
  Small, focused APIs with swappable drivers, so an application changes backends in one place.<br>
  Every module uses the standard library first and reaches 100% test coverage.<br>
  <i>By Brian Nunez.</i>
</p>

<p align="center">
  <a href="https://github.com/brian-nunez/bkit/actions/workflows/tests.yml"><img alt="CI" src="https://img.shields.io/github/actions/workflow/status/brian-nunez/bkit/tests.yml?branch=main&style=flat-square&label=CI&logo=githubactions&logoColor=white"></a>
  <a href="https://www.linkedin.com/in/brianjnunez/"><img alt="LinkedIn: Brian Nunez" src="https://img.shields.io/badge/LinkedIn-Brian%20Nunez-0A66C2?style=flat-square&logo=linkedin&logoColor=white"></a>
  <img alt="Discord: .derthon" src="https://img.shields.io/badge/Discord-.derthon-5865F2?style=flat-square&logo=discord&logoColor=white">
</p>

<p align="center">
  <a href="#modules"><img alt="Browse modules" src="https://img.shields.io/badge/Browse%20modules-00ADD8?style=for-the-badge&logo=go&logoColor=white" height="40"></a>
</p>

<p align="center">
  <a href="#modules"><b>Modules</b></a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="docs/development-and-release.md">Development and release</a> ·
  <a href="CONTRIBUTING.md">Contributing</a>
</p>

<br>

<p align="center">
  <a href="#why-bkit">Why bkit</a> ·
  <a href="#modules">Modules</a> ·
  <a href="#quick-start">Quick start</a> ·
  <a href="#usage">Usage</a> ·
  <a href="#versioning">Versioning</a> ·
  <a href="#development">Development</a> ·
  <a href="#license">License</a>
</p>

## Why bkit

- **Independent.** Every module has its own `go.mod` and release tags, so an
  application imports exactly the pieces it uses and upgrades each one
  separately.
- **Swappable.** Modules with backends define one API, and each backend is a
  driver module. Changing a database, store, or bucket backend means changing
  the one place the driver is constructed.
- **Light.** Core modules use only the standard library. Heavy third-party
  dependencies live in driver modules, so an application installs only the
  clients it needs.
- **Tested.** Every module reaches 100% statement coverage from its own tests,
  with no external services. objex and stash drivers also run a shared
  conformance suite, so they behave the same for the same calls.
- **Released.** Versions follow Semantic Versioning and are cut automatically
  from Conventional Commits, one module at a time.

## Modules

Every module imports from `github.com/brian-nunez/bkit/<module>`. Drivers are
nested modules, imported from `github.com/brian-nunez/bkit/<module>/drivers/<driver>`.

### objex

| Module | Purpose |
| --- | --- |
| **[objex](objex)** | Backend-independent object storage API |
| [objex/drivers/memory](objex/drivers/memory) | In-memory objex driver |
| [objex/drivers/filesystem](objex/drivers/filesystem) | objex driver for a local directory |
| [objex/drivers/s3](objex/drivers/s3) | objex driver for S3 and S3-compatible services |

### gate

| Module | Purpose |
| --- | --- |
| **[gate](gate)** | Typed, composable boolean rules and checks |

### loom

| Module | Purpose |
| --- | --- |
| **[loom](loom)** | Graphs of typed Go functions, run in parallel on an elastic worker pool |

### settings

| Module | Purpose |
| --- | --- |
| **[settings](settings)** | Configuration loaded and merged from any number of sources |
| [settings/drivers/yaml](settings/drivers/yaml) | YAML format for settings |
| [settings/drivers/toml](settings/drivers/toml) | TOML format for settings |
| [settings/drivers/vault](settings/drivers/vault) | settings sources for HashiCorp Vault key/value secrets |
| [settings/drivers/consul](settings/drivers/consul) | settings sources for the HashiCorp Consul key/value store |

### rdb

| Module | Purpose |
| --- | --- |
| **[rdb](rdb)** | One API over relational databases: pooling, placeholders, scanning, errors, and transactions |
| [rdb/drivers/sqlite](rdb/drivers/sqlite) | rdb driver for SQLite, in pure Go |
| [rdb/drivers/postgres](rdb/drivers/postgres) | rdb driver for PostgreSQL, built on pgx |
| [rdb/drivers/mysql](rdb/drivers/mysql) | rdb driver for MySQL and MariaDB |

### stash

| Module | Purpose |
| --- | --- |
| **[stash](stash)** | One API over key-value stores, with expiry, atomic operations, and typed values |
| [stash/drivers/memory](stash/drivers/memory) | In-process stash store |
| [stash/drivers/database](stash/drivers/database) | stash store in a SQL database opened with rdb |
| [stash/drivers/redis](stash/drivers/redis) | stash store on a Redis server |
| [stash/drivers/valkey](stash/drivers/valkey) | stash store on a Valkey server |

## Quick start

Install the modules and drivers you need:

```shell
go get github.com/brian-nunez/bkit/settings@latest               # configuration
go get github.com/brian-nunez/bkit/rdb@latest                    # SQL databases
go get github.com/brian-nunez/bkit/rdb/drivers/sqlite@latest     # one rdb driver
go get github.com/brian-nunez/bkit/stash/drivers/database@latest # stash on top of rdb
```

Modules require Go 1.26 or newer. They compose: here configuration comes from
the environment, the database URL picks the driver, and the key-value store
shares the database's connection pool.

```go
cfg, err := settings.Load(ctx, settings.Env(settings.WithPrefix("APP_")))
if err != nil {
	return err
}

db, err := rdb.Open(ctx, cfg.GetString("database.url", "sqlite:app.db"),
	sqlite.Driver(),
	rdb.WithMaxOpenConns(10),
)
if err != nil {
	return err
}
defer db.Close()

store, err := database.New(ctx, db)
if err != nil {
	return err
}
```

Passing several drivers does not open several databases. The URL's scheme
picks one driver, and only that driver connects, so
`APP_DATABASE__URL=postgres://...` switches the same binary to Postgres once
the postgres driver is passed to `rdb.Open`.

## Usage

Each module's README covers its full API. These examples show the shape of
each one.

### objex

Construct a driver in one place and pass `objex.Bucket` everywhere else:

```go
client, err := s3.New(ctx, s3.WithRegion("us-east-1"))
if err != nil {
	return err
}
bucket := client.Bucket("assets")

_, err = bucket.Put(ctx, "avatars/42.png", r, objex.WithContentType("image/png"))

obj, err := bucket.Get(ctx, "avatars/42.png")
if errors.Is(err, objex.ErrNotFound) {
	// No avatar yet.
}
defer obj.Close()
```

### gate

Any `func(T) bool` is a rule, and rules combine into larger ones:

```go
type rule = gate.Rule[Access]

func hasRole(role string) rule {
	return func(a Access) bool { return slices.Contains(a.User.Roles, role) }
}

var (
	isOwner  rule = func(a Access) bool { return a.User.ID == a.Doc.OwnerID }
	isBanned rule = func(a Access) bool { return a.User.Banned }

	canDelete = hasRole("admin").Or(hasRole("user").And(isOwner))
	canEdit   = canDelete.And(gate.None(isBanned))
)

if !canDelete(Access{User: user, Doc: doc}) {
	return ErrForbidden
}
```

### loom

Tasks are plain functions with typed values. Each starts as soon as its
parents are done:

```go
g := loom.New(loom.WithRetry(loom.Backoff(3, time.Second, time.Minute)))
users := loom.Add(g, "users", fetchUsers)
orders := loom.Add(g, "orders", fetchOrders)
report := loom.Add(g, "report", func(ctx context.Context, in *loom.Inputs) (Report, error) {
	return build(loom.Get(in, users), loom.Get(in, orders)), nil
}, loom.After(users, orders))

res, err := g.Run(ctx)
if err != nil {
	return err
}
fmt.Println(loom.Get(res, report))
```

### settings

Later sources override earlier ones, and maps merge key by key:

```go
cfg, err := settings.Load(ctx,
	settings.Map(map[string]any{"server": map[string]any{"addr": ":80"}}),
	settings.File("config.yaml", yaml.Format),
	settings.Env(settings.WithPrefix("APP_")),
)
if err != nil {
	return err
}

addr := cfg.GetString("server.addr", ":8080")
port, err := settings.Get[int](cfg, "server.port")
```

### rdb

The URL's scheme picks one of the drivers passed in, and only that driver
connects. Write `?` placeholders on every database, and scan rows into
structs:

```go
db, err := rdb.Open(ctx, os.Getenv("DATABASE_URL"), sqlite.Driver(), postgres.Driver())
if err != nil {
	return err
}

user, err := rdb.One[User](ctx, db, "SELECT id, email, created_at FROM users WHERE email = ?", email)
if errors.Is(err, rdb.ErrNotFound) {
	return ErrNoSuchUser
}

_, err = db.Exec(ctx, "INSERT INTO users (email) VALUES (?)", email)
if errors.Is(err, rdb.ErrUniqueViolation) {
	return ErrEmailTaken
}

err = db.Tx(ctx, func(tx *rdb.Tx) error {
	_, err := tx.Exec(ctx, "UPDATE accounts SET balance = balance - ? WHERE id = ?", amount, from)
	return err
})
```

### stash

Store typed values with expiry, and take locks that exactly one caller wins:

```go
s, err := stash.Open(ctx, os.Getenv("STASH_URL"), memory.Driver(), valkey.Driver())
if err != nil {
	return err
}

sessions := stash.Typed[Session](s.Namespace("sessions"), stash.JSON)
err = sessions.Set(ctx, sessionID, Session{UserID: 42}, stash.WithTTL(24*time.Hour))

err = s.Set(ctx, "lock:job-7", []byte(owner), stash.IfNotExists(), stash.WithTTL(30*time.Second))
if errors.Is(err, stash.ErrExists) {
	return ErrBusy
}
```

## Versioning

Every module follows [Semantic Versioning](https://semver.org/) with tags of
the form `<module>/vX.Y.Z`, or `<module>/drivers/<driver>/vX.Y.Z` for a
driver. Prereleases use `-beta.N`. Releases are cut automatically from
[Conventional Commits](https://www.conventionalcommits.org/), and only modules
with releasing commits get a new version.

```shell
go get github.com/brian-nunez/bkit/rdb@v1.0.0
```

## Development

Requirements: Go 1.27.4, `make`, `jq`, and Git. Node.js is needed only to run
release tooling locally.

```shell
make tools                     # install the pinned golangci-lint into .tools/bin
make doctor                    # confirm the Go and golangci-lint versions
make check                     # format check, lint, vet, race tests, and 100% coverage for every module
make -C <module> check         # the same checks for one module
make help                      # list every target
```

Every module must reach 100% statement coverage from its own tests, with no
external services. CI and releases enforce it with `make coverage-check`.
Drivers that talk to a server also ship a `docker-compose.yml` for optional
integration tests.

## Documentation

- [Development and release](docs/development-and-release.md): CI, the release
  flow, and recovering from a partial release
- [Contributing](CONTRIBUTING.md): setup, commit messages, and pull requests
- [Agent guide](AGENTS.md): repository rules for coding agents
- Each module's README, linked from the [Modules](#modules) tables

## License

bkit is licensed under the [MIT License](LICENSE). Copyright 2026 Brian Nunez.
