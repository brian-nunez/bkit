# rdb/drivers/mysql

Connect [rdb](../..) to MySQL and MariaDB.

The driver uses
[go-sql-driver/mysql](https://github.com/go-sql-driver/mysql). Its types
stay inside the driver, so application code depends only on rdb.

## Install

```shell
go get github.com/brian-nunez/bkit/rdb/drivers/mysql
```

```go
import "github.com/brian-nunez/bkit/rdb/drivers/mysql"
```

## Usage

Open a URL with `rdb.Open`:

```go
db, err := rdb.Open(ctx, os.Getenv("DATABASE_URL"), mysql.Driver())
```

```text
mysql://app:secret@db.internal:3306/app?tls=true
```

The `mariadb:` scheme works the same way. Encode special characters in the
user name and password, such as `@` as `%40`.

Or open a database from a `Config`:

```go
db, err := mysql.Open(ctx, mysql.Config{
	Host:     "db.internal",
	Database: "app",
	User:     user,
	Password: password,
	Params:   map[string]string{"tls": "true"},
}, rdb.WithMaxOpenConns(20))
```

| Field | Meaning | Empty value |
| --- | --- | --- |
| `Host` | Server host name or IP address | `127.0.0.1` |
| `Port` | Server port | 3306 |
| `Database` | Database name | None |
| `User`, `Password` | Credentials | None |
| `Params` | Driver parameters and session variables | None |

Query parameters and `Params` are
[go-sql-driver/mysql parameters](https://github.com/go-sql-driver/mysql#parameters),
such as `tls`, `timeout`, and `loc`. Unknown parameters become session
variables, so `time_zone=%27%2B00%3A00%27` runs `SET time_zone='+00:00'` on
every connection.

`parseTime` defaults to `true`, so `DATE` and `DATETIME` columns scan into
`time.Time`. Set `parseTime=false` to read them as text.

## Errors

| Sentinel | Error number |
| --- | --- |
| `rdb.ErrUniqueViolation` | 1062 |
| `rdb.ErrForeignKeyViolation` | 1451, 1452 |
| `rdb.ErrNotNullViolation` | 1048, 1364 |
| `rdb.ErrCheckViolation` | 3819 (MySQL), 4025 (MariaDB) |
| `rdb.ErrRetryable` | 1213 deadlock, 1205 lock wait timeout |

Connection settings the driver cannot parse, such as an unknown `tls`
value, fail with `rdb.ErrInvalidURL`. Error messages never include the
password.

## Development

From the repository root:

```shell
make -C rdb/drivers/mysql check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage. The unit tests
need no server.

To also run the tests against real MySQL and MariaDB servers:

```shell
docker compose -f rdb/drivers/mysql/docker-compose.yml up -d --wait
RDB_MYSQL_URL=mysql://rdbtest:rdbtest@localhost:53306/rdbtest make -C rdb/drivers/mysql tests
RDB_MYSQL_URL=mariadb://rdbtest:rdbtest@localhost:53307/rdbtest make -C rdb/drivers/mysql tests
docker compose -f rdb/drivers/mysql/docker-compose.yml down
```

## Releases

Each release is tagged `rdb/drivers/mysql/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/rdb/drivers/mysql@vX.Y.Z
```
