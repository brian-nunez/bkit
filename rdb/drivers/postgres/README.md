# rdb/drivers/postgres

Connect [rdb](../..) to PostgreSQL.

The driver uses [pgx](https://github.com/jackc/pgx) through its
`database/sql` adapter. pgx types stay inside the driver, so application
code depends only on rdb.

## Install

```shell
go get github.com/brian-nunez/bkit/rdb/drivers/postgres
```

```go
import "github.com/brian-nunez/bkit/rdb/drivers/postgres"
```

## Usage

Open a URL with `rdb.Open`:

```go
db, err := rdb.Open(ctx, os.Getenv("DATABASE_URL"), postgres.Driver())
```

```text
postgres://app:secret@db.internal:5432/app?sslmode=verify-full
```

The `postgresql:` scheme works too.

Or open a database from a `Config`:

```go
db, err := postgres.Open(ctx, postgres.Config{
	Host:     "db.internal",
	Database: "app",
	User:     user,
	Password: password,
	SSLMode:  "verify-full",
	Params:   map[string]string{"application_name": "api"},
}, rdb.WithMaxOpenConns(20))
```

| Field | Meaning | Empty value |
| --- | --- | --- |
| `Host` | Server host name or IP address | The pgx default |
| `Port` | Server port | 5432 |
| `Database` | Database name | The pgx default |
| `User`, `Password` | Credentials | The pgx default |
| `SSLMode` | `disable`, `require`, `verify-ca`, or `verify-full` | `prefer` |
| `Params` | Other connection parameters | None |

Settings left empty fall back to the pgx defaults, which read the standard
`PGHOST`, `PGUSER`, `PGPASSWORD`, and related environment variables.

Write queries with `?` placeholders. rdb rewrites them as `$1, $2` for
Postgres. Use `db.Raw()` for a query that uses the jsonb `?`, `?|`, or `?&`
operators, and write `$1` placeholders in it.

## Errors

| Sentinel | SQLSTATE |
| --- | --- |
| `rdb.ErrUniqueViolation` | `23505` |
| `rdb.ErrForeignKeyViolation` | `23503` |
| `rdb.ErrNotNullViolation` | `23502` |
| `rdb.ErrCheckViolation` | `23514` |
| `rdb.ErrRetryable` | `40001` serialization failure, `40P01` deadlock |

Connection settings pgx cannot parse, such as an unknown `sslmode`, fail
with `rdb.ErrInvalidURL`. Error messages never include the password.

## Development

From the repository root:

```shell
make -C rdb/drivers/postgres check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage. The unit tests
need no server.

To also run the tests against a real Postgres server:

```shell
docker compose -f rdb/drivers/postgres/docker-compose.yml up -d --wait
RDB_POSTGRES_URL='postgres://rdbtest:rdbtest@localhost:55432/rdbtest?sslmode=disable' make -C rdb/drivers/postgres tests
docker compose -f rdb/drivers/postgres/docker-compose.yml down
```

## Releases

Each release is tagged `rdb/drivers/postgres/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/rdb/drivers/postgres@vX.Y.Z
```
