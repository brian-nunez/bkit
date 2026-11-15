# stash/drivers/database

Keep [stash](../..) keys in a table of the application's SQL database. It
works with every database [rdb](../../../rdb) supports: SQLite, Postgres,
and MySQL or MariaDB.

## Install

```shell
go get github.com/brian-nunez/bkit/stash/drivers/database
```

```go
import "github.com/brian-nunez/bkit/stash/drivers/database"
```

## Usage

Open the database with rdb, then create the store from it:

```go
db, err := rdb.Open(ctx, os.Getenv("DATABASE_URL"),
	sqlite.Driver(),
	postgres.Driver(),
)
if err != nil {
	return err
}
defer db.Close()

s, err := database.New(ctx, db)
```

The store shares the database's connection pool. Closing the store leaves
the database open.

| Option | Effect |
| --- | --- |
| `database.WithTable(name)` | Stores keys in `name` instead of `stash`. Letters, digits, and underscores, at most 63 characters. |

## Table

`New` creates the table and an index on `expires_at` when they are missing,
so it is safe to call on every start.

| Column | SQLite | Postgres | MySQL and MariaDB |
| --- | --- | --- | --- |
| `k` | `TEXT PRIMARY KEY` | `TEXT COLLATE "C" PRIMARY KEY` | `VARBINARY(512) PRIMARY KEY` |
| `v` | `BLOB` | `BYTEA` | `LONGBLOB` |
| `expires_at` | `INTEGER` | `BIGINT` | `BIGINT` |

`expires_at` holds Unix milliseconds, or null for keys that never expire.
Keys compare byte for byte on every database, so `User` and `user` are two
keys.

## Behavior

- Expiry times come from the application's clock. Keep the clocks of every
  instance that shares a table in sync.
- Expired rows are never returned. Every hundredth write deletes them.
- `Set` with `IfNotExists` is one `INSERT` statement, so exactly one of
  several racing callers stores its value.
- `Incr` runs in a transaction that locks the row. When two callers create
  the same counter at once, the loser runs again, so no increment is lost.
- `Keys` reads 500 keys per query, in key order.

## Development

From the repository root:

```shell
make -C stash/drivers/database check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage. The tests run the
`stashtest` conformance suite on SQLite inside the test process, so no
server is needed.

To also run the suite against real Postgres, MySQL, and MariaDB servers:

```shell
docker compose -f stash/drivers/database/docker-compose.yml up -d --wait
STASH_POSTGRES_URL='postgres://stashtest:stashtest@localhost:55436/stashtest?sslmode=disable' \
STASH_MYSQL_URL=mysql://stashtest:stashtest@localhost:53316/stashtest \
  make -C stash/drivers/database tests
STASH_MYSQL_URL=mariadb://stashtest:stashtest@localhost:53317/stashtest make -C stash/drivers/database tests
docker compose -f stash/drivers/database/docker-compose.yml down
```

## Releases

Each release is tagged `stash/drivers/database/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/stash/drivers/database@vX.Y.Z
```
