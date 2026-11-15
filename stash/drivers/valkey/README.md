# stash/drivers/valkey

Keep [stash](../..) keys on a [Valkey](https://valkey.io) server.

The driver uses [valkey-go](https://github.com/valkey-io/valkey-go),
Valkey's official Go client. Its types stay inside the driver, so
application code depends only on stash.

## Install

```shell
go get github.com/brian-nunez/bkit/stash/drivers/valkey
```

```go
import "github.com/brian-nunez/bkit/stash/drivers/valkey"
```

## Usage

Open a URL with `stash.Open`:

```go
s, err := stash.Open(ctx, os.Getenv("STASH_URL"), valkey.Driver())
```

```text
valkey://user:secret@cache.internal:6379/0
valkeys://user:secret@cache.internal:6380/0
```

`valkeys://` connects over TLS. Query parameters are valkey-go options, such
as `dial_timeout`.

Or connect with a `Config`:

```go
s, err := valkey.New(ctx, valkey.Config{
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

Each stash call is one Valkey command, so the server enforces expiry and
atomicity:

| Call | Command |
| --- | --- |
| `Set` | `SET`, with `NX` for `IfNotExists` and `PX` for a TTL |
| `Incr` | `INCRBY` |
| `TTL` | `PTTL` |
| `Expire` | `PEXPIRE`, or `PERSIST` for `0` |
| `Keys`, `Clear` | `SCAN` with glob characters escaped, then `DEL` for `Clear` |

Expiry has millisecond precision. `Keys` can return a key twice when keys
change during the scan, as `SCAN` does. Client-side caching is turned off,
because stash reads every value from the server.

URL parse errors never include the URL.

## Development

From the repository root:

```shell
make -C stash/drivers/valkey check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage. The unit tests run
the `stashtest` conformance suite through the real valkey-go client against
a small in-process server, so no Valkey server is needed.

To also run the suite against a real Valkey server:

```shell
docker compose -f stash/drivers/valkey/docker-compose.yml up -d --wait
STASH_VALKEY_URL=valkey://localhost:56390/0 make -C stash/drivers/valkey tests
docker compose -f stash/drivers/valkey/docker-compose.yml down
```

Each test works under its own key prefix and deletes it afterwards, so the
suite never flushes the database.

## Releases

Each release is tagged `stash/drivers/valkey/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/stash/drivers/valkey@vX.Y.Z
```
