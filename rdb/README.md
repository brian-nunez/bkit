# rdb

One API over relational databases. Open any supported database from a URL,
run queries with `?` placeholders, scan rows into structs, and match errors
the same way on every database.

Each driver is a separate module under
`github.com/brian-nunez/bkit/rdb/drivers`, so an application installs only
the drivers it uses.

## Install

```shell
go get github.com/brian-nunez/bkit/rdb
```

```go
import "github.com/brian-nunez/bkit/rdb"
```

## Usage

Pass every driver the application supports. The URL's scheme picks one, and
only that driver connects, so the same binary can use one database locally
and another in production by changing `DATABASE_URL`:

```go
db, err := rdb.Open(ctx, os.Getenv("DATABASE_URL"),
	rdb.WithDriver(localDriver),
	rdb.WithDriver(serverDriver),
	rdb.WithMaxOpenConns(25),
)
if err != nil {
	return err
}
defer db.Close()
```

An unknown scheme fails with `rdb.ErrUnknownDriver`. Driver modules also
offer a typed `Open` that takes a `Config` struct.

### Queries

Code that takes an `rdb.Querier` works with a `*rdb.DB`, inside a
transaction, and with `Raw`:

```go
type Users struct{ q rdb.Querier }

type User struct {
	ID        int64  `db:"id"`
	Email     string `db:"email"`
	CreatedAt time.Time
}

func (u Users) ByEmail(ctx context.Context, email string) (User, error) {
	return rdb.One[User](ctx, u.q,
		"SELECT id, email, created_at FROM users WHERE email = ?", email)
}
```

| Call | Returns |
| --- | --- |
| `q.Exec(ctx, query, args...)` | `sql.Result` |
| `q.Query(ctx, query, args...)` | `*sql.Rows`, which the caller closes |
| `rdb.One[T](ctx, q, query, args...)` | The first row as a `T`, or `ErrNotFound` |
| `rdb.All[T](ctx, q, query, args...)` | Every row as a `[]T`, or an empty slice when there are none |
| `rdb.Each[T](ctx, q, query, args...)` | An iterator over the rows, for large results |

```go
count, err := rdb.One[int](ctx, db, "SELECT count(*) FROM users")

for user, err := range rdb.Each[User](ctx, db, "SELECT id, email, created_at FROM users") {
	if err != nil {
		return err
	}
	process(user)
}
```

### Placeholders

Write `?` for every parameter. rdb rewrites placeholders for databases that
use another form, such as `$1, $2` for Postgres, and leaves them unchanged
for databases that use `?`. A `?` inside a string constant, quoted
identifier, or comment is left alone.

Some SQL uses `?` as an operator, such as the Postgres jsonb `?`, `?|`, and
`?&` operators. Send those queries without rewriting:

| Call | Rewriting |
| --- | --- |
| `db.Exec`, `db.Query`, `rdb.One(ctx, db, ...)` | On |
| `db.Raw().Exec(...)`, `rdb.One(ctx, tx.Raw(), ...)` | Off for that query |
| `rdb.Open(ctx, url, rdb.WithoutRebind())` | Off for the whole `DB` |

```go
_, err := db.Raw().Exec(ctx, "UPDATE docs SET tagged = true WHERE data ? 'tag'")
```

A query sent without rewriting must use the database's own placeholders.

### Scanning

`T` is a struct, or a single value such as `int`, `string`, `time.Time`, a
pointer for a nullable column, or a `sql.Scanner`, which receives the only
column.

Struct fields receive columns by name:

- A `db:"name"` tag matches its column, ignoring case.
- An untagged field matches ignoring case and underscores, so `CreatedAt`
  receives `created_at`.
- `db:"-"` skips a field. Unexported fields are skipped.
- Embedded structs are flattened.
- A field with no column keeps its zero value.

Scanning is strict. A column with no matching field, two fields that match
one column, or two columns for one field fail with `ErrScan`, so typos fail
loudly. `rdb.WithoutStrictScan()` makes a `DB` ignore unmatched columns.

### Transactions

```go
err := db.Tx(ctx, func(tx *rdb.Tx) error {
	_, err := tx.Exec(ctx, "UPDATE accounts SET balance = balance - ? WHERE id = ?", amount, from)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE accounts SET balance = balance + ? WHERE id = ?", amount, to)
	return err
}, rdb.Isolation(sql.LevelSerializable))
```

Returning nil commits. Returning an error or panicking rolls back. `Tx`
does not retry. When the error matches `rdb.ErrRetryable`, call `Tx` again.
`rdb.ReadOnly()` starts a read-only transaction.

### Dialects and escape hatches

```go
switch db.Dialect() {
case rdb.Postgres:
	query = "INSERT INTO tags (name) VALUES (?) ON CONFLICT (name) DO NOTHING"
case rdb.MySQL:
	query = "INSERT IGNORE INTO tags (name) VALUES (?)"
}

raw := db.SQL()      // *sql.DB, for libraries that need one
stats := db.Stats()  // sql.DBStats
err := db.Ping(ctx)
```

rdb does not build queries or translate SQL between dialects.

## Configuration

### Pool

| Option | Effect | Default |
| --- | --- | --- |
| `WithMaxOpenConns(n)` | Most connections open at once | Unlimited, or the database's limit |
| `WithMaxIdleConns(n)` | Most idle connections kept | Max open connections when set, otherwise 2 or the warm count |
| `WithConnMaxLifetime(d)` | Closes connections open this long | No limit |
| `WithConnMaxIdleTime(d)` | Closes connections idle this long | No limit |
| `WithWarmConns(n)` | Connections `Open` establishes before it returns | 0 |
| `WithPool(rdb.Pool{...})` | All five settings from one struct | |

`rdb.Pool` has the fields `MaxOpenConns`, `MaxIdleConns`, `ConnMaxLifetime`,
`ConnMaxIdleTime`, and `WarmConns`, so a configuration loader can fill it.
Options apply in order: `WithPool` replaces earlier pool options, and later
options override single fields.

The pool may close warm connections later under the other limits.

### Connections and startup

| Option | Effect |
| --- | --- |
| `WithConnInit(queries...)` | Runs queries on every new connection, such as `SET TIME ZONE 'UTC'`. They are sent as written. A failure discards the connection. |
| `WithConnectTimeout(d)` | Limits opening one connection, including init queries |
| `WithOpenRetry(attempts, delay)` | Pings up to `attempts` times, waiting `delay` between failures |
| `WithOpenTimeout(d)` | Limits all of `Open`: pings, retry delays, and warming |

### Behavior

| Option | Effect |
| --- | --- |
| `WithoutRebind()` | Sends every query as written |
| `WithoutStrictScan()` | Ignores result columns with no matching field |

Negative values, `WithOpenRetry` with fewer than one attempt, and limits the
database cannot honor fail with `ErrInvalidOption`.

## Errors

Errors are `*rdb.Error` values with the operation, a classifying sentinel,
and the driver's original error. Match them with `errors.Is`:

| Sentinel | Meaning |
| --- | --- |
| `ErrNotFound` | `One` found no rows. Also matches `sql.ErrNoRows` |
| `ErrUniqueViolation` | A duplicate value in a unique index or primary key |
| `ErrForeignKeyViolation` | A write that breaks a foreign key |
| `ErrNotNullViolation` | A null in a `NOT NULL` column |
| `ErrCheckViolation` | A write that fails a `CHECK` constraint |
| `ErrRetryable` | A deadlock, serialization failure, or locked database |
| `ErrScan` | Rows that cannot be scanned into the requested type |
| `ErrUnknownDriver` | No driver matches the URL's scheme |
| `ErrInvalidURL` | The URL cannot be parsed or has no scheme |
| `ErrInvalidOption` | An option is out of range or conflicts |

```go
_, err := db.Exec(ctx, "INSERT INTO users (email) VALUES (?)", email)
if errors.Is(err, rdb.ErrUniqueViolation) {
	return ErrEmailTaken
}
```

Error messages never contain the database URL or its credentials. Context
errors match `context.Canceled` and `context.DeadlineExceeded`.

## Writing a driver

A driver implements `rdb.Driver`:

| Method | Returns |
| --- | --- |
| `Schemes()` | The lowercase URL schemes it opens |
| `Dialect()` | Its database's `rdb.Dialect` |
| `Open(u)` | An `rdb.Connection`: a `driver.Connector`, and a connection limit when the database has one. It must not connect, and its errors must not contain the URL. |
| `Classify(err)` | The sentinel matching a database error, or nil |

A driver module exposes `Driver()` returning `rdb.WithDriver(d)`, and a
typed `Open` that builds a `Connection` from a `Config` struct and calls
`rdb.OpenConnection`.

## Development

From the repository root:

```shell
make -C rdb check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage.

## Releases

Each release is tagged `rdb/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/rdb@vX.Y.Z
```
