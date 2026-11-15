# rdb/drivers/sqlite

Connect [rdb](../..) to SQLite.

The driver uses `modernc.org/sqlite`, a pure Go build of SQLite, so it needs
no cgo and cross-compiles like any Go package.

## Install

```shell
go get github.com/brian-nunez/bkit/rdb/drivers/sqlite
```

```go
import "github.com/brian-nunez/bkit/rdb/drivers/sqlite"
```

## Usage

Open a URL with `rdb.Open`:

```go
db, err := rdb.Open(ctx, os.Getenv("DATABASE_URL"), sqlite.Driver())
```

Or open a database by path:

```go
db, err := sqlite.Open(ctx, sqlite.Config{Path: "app.db"}, rdb.WithMaxOpenConns(4))
```

| URL | Database |
| --- | --- |
| `sqlite:app.db` | `app.db`, relative to the working directory |
| `sqlite:///var/lib/app/app.db` | An absolute path |
| `sqlite::memory:` or `sqlite:` | A private in-memory database |

The `sqlite3:` scheme works too. Missing database files are created.

## Defaults

Every connection runs these pragmas:

| Pragma | Value | Effect |
| --- | --- | --- |
| `foreign_keys` | `1` | Enforces foreign keys |
| `busy_timeout` | `5000` | Waits up to 5 seconds for a lock before failing |
| `journal_mode` | `WAL` | File databases only. Readers run alongside a writer. |

An in-memory database is limited to one connection. Asking for more with
`rdb.WithMaxOpenConns` fails with `rdb.ErrInvalidOption`. The database keeps
its data until the `rdb.DB` is closed, even when the pool replaces the
connection, such as after `rdb.WithConnMaxLifetime` expires.

Query parameters pass to the SQLite driver. A `_pragma` parameter replaces
the default of the same name:

```text
sqlite:app.db?_pragma=busy_timeout(1000)&_pragma=synchronous(NORMAL)
```

`rdb.ReadOnly()` transactions turn on `PRAGMA query_only` for their
duration, so a write inside one fails. The driver turns it off when the
transaction ends.

Writes in SQLite take turns. Under heavy write load, a write can still fail
with `rdb.ErrRetryable` after the busy timeout.

## Errors

| Sentinel | SQLite result code |
| --- | --- |
| `rdb.ErrUniqueViolation` | `SQLITE_CONSTRAINT_UNIQUE`, `SQLITE_CONSTRAINT_PRIMARYKEY` |
| `rdb.ErrForeignKeyViolation` | `SQLITE_CONSTRAINT_FOREIGNKEY` |
| `rdb.ErrNotNullViolation` | `SQLITE_CONSTRAINT_NOTNULL` |
| `rdb.ErrCheckViolation` | `SQLITE_CONSTRAINT_CHECK` |
| `rdb.ErrRetryable` | `SQLITE_BUSY`, `SQLITE_LOCKED` |

A URL path with a bad percent escape fails with `rdb.ErrInvalidURL`.

## Development

From the repository root:

```shell
make -C rdb/drivers/sqlite check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage. SQLite runs inside
the test process, so no server is needed.

## Releases

Each release is tagged `rdb/drivers/sqlite/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/rdb/drivers/sqlite@vX.Y.Z
```
