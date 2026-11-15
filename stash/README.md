# stash

One API over key-value stores. Keep values in process memory, in the
application's SQL database, or on a key-value server, and switch between
them by changing a URL.

Each driver is a separate module under
`github.com/brian-nunez/bkit/stash/drivers`, so an application installs only
the drivers it uses.

## Install

```shell
go get github.com/brian-nunez/bkit/stash
```

```go
import "github.com/brian-nunez/bkit/stash"
```

Then install a driver for each backend the application uses:

| Driver | Module | Backend | Opens |
| --- | --- | --- | --- |
| [memory](drivers/memory) | `github.com/brian-nunez/bkit/stash/drivers/memory` | Process memory | `memory://`, or `memory.New()` |
| [database](drivers/database) | `github.com/brian-nunez/bkit/stash/drivers/database` | A table in SQLite, Postgres, or MySQL, through rdb | `database.New(ctx, db)` |
| [redis](drivers/redis) | `github.com/brian-nunez/bkit/stash/drivers/redis` | A Redis server | `redis://`, `rediss://`, or `redis.New(ctx, cfg)` |
| [valkey](drivers/valkey) | `github.com/brian-nunez/bkit/stash/drivers/valkey` | A Valkey server | `valkey://`, `valkeys://`, or `valkey.New(ctx, cfg)` |

## Usage

Pass every driver the application supports to `stash.Open`. The URL's scheme
picks one, and only that driver connects:

```go
import (
	"github.com/brian-nunez/bkit/stash"
	"github.com/brian-nunez/bkit/stash/drivers/memory"
	"github.com/brian-nunez/bkit/stash/drivers/redis"
	"github.com/brian-nunez/bkit/stash/drivers/valkey"
)

s, err := stash.Open(ctx, os.Getenv("STASH_URL"),
	memory.Driver(),
	redis.Driver(),
	valkey.Driver(),
)
if err != nil {
	return err
}
defer s.Close()
```

| `STASH_URL` | Result |
| --- | --- |
| `memory://` | An in-process store |
| `redis://cache.internal:6379/0` | A Redis store |
| `valkey://cache.internal:6379/0` | A Valkey store |
| `postgres://...` | `stash.ErrUnknownDriver`, because no driver opens that scheme |

To keep keys in the application's SQL database, build the store from its
`*rdb.DB`. It shares the database's connection pool:

```go
import (
	"github.com/brian-nunez/bkit/rdb"
	"github.com/brian-nunez/bkit/rdb/drivers/postgres"
	"github.com/brian-nunez/bkit/rdb/drivers/sqlite"
	"github.com/brian-nunez/bkit/stash/drivers/database"
)

db, err := rdb.Open(ctx, os.Getenv("DATABASE_URL"), sqlite.Driver(), postgres.Driver())
if err != nil {
	return err
}
s, err := database.New(ctx, db)
```

Every driver runs the same conformance suite, so code written against
`*stash.Store` behaves the same on all of them.

Pass the `*stash.Store` to the rest of the application:

```go
err := s.Set(ctx, "greeting", []byte("hello"), stash.WithTTL(time.Hour))
value, err := s.Get(ctx, "greeting")
if errors.Is(err, stash.ErrNotFound) {
	// Missing or expired.
}
```

| Call | Effect |
| --- | --- |
| `Get(ctx, key)` | The value, or `ErrNotFound` |
| `Set(ctx, key, value, opts...)` | Stores the value. `WithTTL(d)` makes it expire, and `IfNotExists()` stores it only when the key is missing. |
| `Delete(ctx, key)` | Removes the key and reports whether it existed |
| `Exists(ctx, key)` | Whether the key is set |
| `Incr(ctx, key, delta)` | Adds `delta` to a counter and returns the result |
| `TTL(ctx, key)` | Time left before the key expires, or `stash.NoExpiry` |
| `Expire(ctx, key, ttl)` | Sets a new expiry. `0` removes it. |
| `Keys(ctx, opts...)` | An iterator over the keys. `WithPrefix(p)` filters them. |
| `Namespace(name)` | A store whose keys live under `name:` |
| `Clear(ctx)` | Removes every key in a namespace |
| `Ping(ctx)` | Checks that the store is reachable |

### Typed values

`stash.Typed` stores Go values through a codec. `stash.JSON` uses
`encoding/json`:

```go
type Session struct {
	UserID int64
	Roles  []string
}

sessions := stash.Typed[Session](s.Namespace("sessions"), stash.JSON)

err := sessions.Set(ctx, sessionID, Session{UserID: 42}, stash.WithTTL(24*time.Hour))
sess, err := sessions.Get(ctx, sessionID)
```

The codec runs before values reach the driver, so typed values behave the
same on every driver. A value the codec cannot encode or decode fails with
`stash.ErrCodec`. Any type with `Marshal(any) ([]byte, error)` and
`Unmarshal([]byte, any) error` is a codec.

### Atomic operations

`IfNotExists` and `Incr` check and write in one step inside the store, so
racing callers never both win.

```go
// A lock: exactly one caller gets nil.
err := s.Set(ctx, "lock:job-7", []byte(owner), stash.IfNotExists(), stash.WithTTL(30*time.Second))
if errors.Is(err, stash.ErrExists) {
	return ErrBusy
}

// A counter: no increment is lost.
n, err := s.Incr(ctx, "signups:2026-11-14", 1)
```

`Incr` treats a missing key as 0 and creates it without expiry. Counters are
stored as base-10 text, so `Get` returns `"5"`. Incrementing a value that is
not an integer, or past the range of `int64`, fails with
`stash.ErrNotInteger`. An existing key keeps its expiry.

### Expiry

```go
left, err := s.TTL(ctx, key)           // stash.NoExpiry when the key never expires
err = s.Expire(ctx, key, 24*time.Hour) // slide a session forward
err = s.Expire(ctx, key, 0)            // keep it forever
```

`Set` replaces the expiry: a `Set` without `WithTTL` makes the key permanent.
Expired keys are never returned. Each driver removes them in the background
of its normal work.

### Namespaces and keys

```go
cache := s.Namespace("cache")
for key, err := range cache.Keys(ctx, stash.WithPrefix("user:")) {
	if err != nil {
		return err
	}
	fmt.Println(key) // "user:42", without "cache:"
}
err = cache.Clear(ctx) // only keys under "cache:"
```

- A namespace adds `name:` in front of its keys, and namespaces nest.
- Prefixes match literally on every driver, so `%`, `_`, `*`, and `?` in a key
  are ordinary characters.
- `Keys` returns keys in no particular order. A key can repeat when the store
  changes during iteration.
- `Clear` on the root store fails with `stash.ErrInvalidOption`, so no call
  empties a whole backend.

A key is non-empty, valid UTF-8, and at most 512 bytes including its
namespace. Other keys fail with `stash.ErrInvalidKey`.

## Not yet included

Batch calls such as `GetMany` and `SetMany` are planned for a later release.
Until then, call `Get` and `Set` once per key.

## Errors

Errors are `*stash.Error` values with the operation, the key, and the cause.
Match them with `errors.Is`:

| Sentinel | Meaning |
| --- | --- |
| `ErrNotFound` | The key is missing or expired |
| `ErrExists` | `IfNotExists` found the key already set |
| `ErrNotInteger` | `Incr` on a value that is not an integer, or an overflow |
| `ErrInvalidKey` | An empty, non-UTF-8, or too long key or namespace |
| `ErrInvalidOption` | A negative TTL, or `Clear` on the root store |
| `ErrCodec` | A typed value the codec cannot encode or decode |
| `ErrClosed` | A call after `Close` |
| `ErrUnknownDriver` | No driver matches the URL's scheme |
| `ErrInvalidURL` | The URL cannot be parsed or has no scheme |

Error messages never contain the store URL, a key, or a value, because keys
often hold session IDs or tokens. The key is in the `Key` field. Context
errors match `context.Canceled` and `context.DeadlineExceeded`.

## Writing a driver

A driver module implements two interfaces:

| Interface | Role |
| --- | --- |
| `stash.Backend` | Stores full keys, namespace included. `stash.Store` validates keys, applies namespaces, and wraps errors before calling it. |
| `stash.Driver` | Lists URL schemes and opens a `Backend` for a URL |

A driver module exposes `Driver()` returning `stash.WithDriver(d)`, and
constructors that return `stash.New(backend)`. Every driver runs the
conformance suite from its tests:

```go
func TestConformance(t *testing.T) {
	stashtest.Run(t, stashtest.Config{
		NewBackend: func(t *testing.T) stash.Backend { return newBackend(t) },
	})
}
```

`stash/stashtest` is excluded from coverage (`COVERAGE_EXCLUDE` in
`stash/Makefile`) because drivers exercise it from their own tests.

## Development

From the repository root:

```shell
make -C stash check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage.

## Releases

Each release is tagged `stash/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/stash@vX.Y.Z
```
