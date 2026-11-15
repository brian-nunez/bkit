# settings/drivers/consul

Read [settings](../..) from the HashiCorp Consul key/value store.

The driver calls Consul's HTTP API with the Go standard library. It has no
dependency on the Consul client library.

## Install

```shell
go get github.com/brian-nunez/bkit/settings/drivers/consul
```

```go
import "github.com/brian-nunez/bkit/settings/drivers/consul"
```

## Usage

```go
client, err := consul.New("http://localhost:8500",
	consul.WithToken(token),
)
if err != nil {
	return err
}

cfg, err := settings.Load(ctx,
	client.Key("apps/web/config.yaml", yaml.Format),
	client.Prefix("apps/web/overrides"),
	settings.Env(settings.WithPrefix("APP_")),
)
```

### One key with a document

`client.Key(key, format)` reads one key and parses its value with any
settings format, such as `settings.JSON`, `yaml.Format`, or `toml.Format`.

### Keys under a prefix

`client.Prefix(prefix)` reads every key under the prefix. The rest of each
key's path becomes a nested key:

| Consul key | Setting |
| --- | --- |
| `apps/web/overrides/name` | `name` |
| `apps/web/overrides/db/host` | `db.host` |
| `apps/web/overrides/db/` | skipped, this is a folder |

Values are stored as strings, and the settings getters convert them. A
trailing `/` on the prefix is optional.

## Configuration

| Option | Effect |
| --- | --- |
| `consul.WithToken(token)` | Sends the ACL token in `X-Consul-Token` |
| `consul.WithDatacenter(dc)` | Reads from datacenter `dc` |
| `consul.WithNamespace(ns)` | Reads from namespace `ns` |
| `consul.WithHTTPClient(c)` | Uses `c` for requests, for example to trust a private CA |

With a token set, redirects to another host or from https to http are not
followed, and the redirect is reported as `consul.ErrResponse`. A client
passed to `WithHTTPClient` with its own `CheckRedirect` keeps that policy.

## Cancellation

Requests use the context passed to `settings.Load`. Canceling it stops the
request in flight.

## Errors

Match errors with `errors.Is`:

| Sentinel | Meaning |
| --- | --- |
| `consul.ErrNotFound` | The key does not exist, or no key is under the prefix |
| `consul.ErrPermission` | The token cannot read the key |
| `consul.ErrResponse` | Any other failed or unreadable response |
| `consul.ErrInvalidAddress` | `New` got an address that is not an http or https URL |
| `settings.ErrParse` | The format could not parse a key's value |

Error messages include Consul's error text and never include the token.

## Development

From the repository root:

```shell
make -C settings/drivers/consul check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage. The unit tests use
a fake Consul server.

To also run the tests against a real Consul agent with ACLs enabled:

```shell
docker compose -f settings/drivers/consul/docker-compose.yml up -d --wait
SETTINGS_CONSUL_ADDR=http://localhost:8500 make -C settings/drivers/consul tests
docker compose -f settings/drivers/consul/docker-compose.yml down
```

## Releases

Each release is tagged `settings/drivers/consul/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/settings/drivers/consul@vX.Y.Z
```
