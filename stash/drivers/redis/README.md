# stash/drivers/redis

Keep [stash](../..) keys on a Redis server.

The driver uses [go-redis](https://github.com/redis/go-redis). Its types
stay inside the driver, so application code depends only on stash.

## Install

```shell
go get github.com/brian-nunez/bkit/stash/drivers/redis
```

```go
import "github.com/brian-nunez/bkit/stash/drivers/redis"
```

## Usage

Open a URL with `stash.Open`:

```go
s, err := stash.Open(ctx, os.Getenv("STASH_URL"), redis.Driver())
```

```text
redis://user:secret@cache.internal:6379/0
rediss://user:secret@cache.internal:6380/0
```

`rediss://` connects over TLS. Query parameters are go-redis options, such
as `dial_timeout` and `pool_size`.

Or connect with a `Config`:

```go
s, err := redis.New(ctx, redis.Config{
	Addr:     "cache.internal:6379",
	Password: password,
	TLS:      &tls.Config{MinVersion: tls.VersionTLS12},
})
```

| Field | Meaning | Empty value |
| --- | --- | --- |
| `Addr` | Server `host:port` | `localhost:6379` |
| `Username`, `Password` | Credentials | None |
| `DB` | Database number | 0 |
| `TLS` | TLS configuration | Plain connection |

Both forms send `PING` and fail when the server does not answer.

## Behavior

Each stash call is one Redis command, so the server enforces expiry and
atomicity:

| Call | Command |
| --- | --- |
| `Set` | `SET`, or `SET NX` with `IfNotExists` |
| `Incr` | `INCRBY` |
| `TTL` | `PTTL` |
| `Expire` | `PEXPIRE`, or `PERSIST` for `0` |
| `Keys`, `Clear` | `SCAN` with glob characters escaped, then `DEL` for `Clear` |

Expiry has millisecond precision. `Keys` can return a key twice when keys
change during the scan, as `SCAN` does.

URL parse errors never include the URL.

## Development

From the repository root:

```shell
make -C stash/drivers/redis check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage. The unit tests run
the `stashtest` conformance suite against an in-memory fake of the Redis
client, so no server is needed.

To also run the suite against a real Redis server:

```shell
docker compose -f stash/drivers/redis/docker-compose.yml up -d --wait
STASH_REDIS_URL=redis://localhost:56380/0 make -C stash/drivers/redis tests
docker compose -f stash/drivers/redis/docker-compose.yml down
```

Each test works under its own key prefix and deletes it afterwards, so the
suite never flushes the database.

## Releases

Each release is tagged `stash/drivers/redis/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/stash/drivers/redis@vX.Y.Z
```
