# settings/drivers/vault

Read [settings](../..) from HashiCorp Vault key/value secrets.

The driver calls Vault's HTTP API with the Go standard library. It has no
dependency on the Vault client library.

## Install

```shell
go get github.com/brian-nunez/bkit/settings/drivers/vault
```

```go
import "github.com/brian-nunez/bkit/settings/drivers/vault"
```

## Usage

```go
client, err := vault.New("https://vault.example.com:8200",
	vault.WithToken(token),
)
if err != nil {
	return err
}

cfg, err := settings.Load(ctx,
	settings.File("config.yaml", yaml.Format),
	client.KV2("secret", "apps/web"),
)
```

| Source | Reads |
| --- | --- |
| `client.KV2(mount, path)` | The current version of a secret in a KV version 2 mount |
| `client.KV2(mount, path, vault.WithVersion(n))` | Version `n` of that secret |
| `client.KV1(mount, path)` | A secret in a KV version 1 mount |

The secret's data becomes the tree. A secret with
`{"DB_PASSWORD": "pw", "db": {"host": "db.local"}}` is read as
`db_password` and `db.host`.

To read a file that Vault Agent renders, use `settings.File` with the
file's format.

## Configuration

| Option | Effect |
| --- | --- |
| `vault.WithToken(token)` | Sends the token in `X-Vault-Token` |
| `vault.WithNamespace(ns)` | Sends the namespace in `X-Vault-Namespace` |
| `vault.WithHTTPClient(c)` | Uses `c` for requests, for example to trust a private CA |

Without `WithHTTPClient`, requests use a client that stops each request after
30 seconds, so a server that never answers cannot hang `settings.Load`.

Without `WithToken`, no token is sent. This works with a Vault Agent or
proxy that adds the token. Renewing the token is up to the caller.

With a token set, redirects to another host or from https to http are not
followed, and the redirect is reported as `vault.ErrResponse`. A client
passed to `WithHTTPClient` with its own `CheckRedirect` keeps that policy.

## Cancellation

Requests use the context passed to `settings.Load`. Canceling it stops the
request in flight.

## Errors

Match errors with `errors.Is`:

| Sentinel | Meaning |
| --- | --- |
| `vault.ErrNotFound` | The secret does not exist, or its current version is deleted |
| `vault.ErrPermission` | The token cannot read the secret |
| `vault.ErrResponse` | Any other failed or unreadable response |
| `vault.ErrInvalidAddress` | `New` got an address that is not an http or https URL |

Error messages include Vault's error text and never include the token.

## Development

From the repository root:

```shell
make -C settings/drivers/vault check
```

`check` runs the format check, lint, vet, race-enabled tests, and
`coverage-check`, which requires 100% statement coverage. The unit tests use
a fake Vault server.

To also run the tests against a real Vault dev server:

```shell
docker compose -f settings/drivers/vault/docker-compose.yml up -d --wait
SETTINGS_VAULT_ADDR=http://localhost:8200 make -C settings/drivers/vault tests
docker compose -f settings/drivers/vault/docker-compose.yml down
```

## Releases

Each release is tagged `settings/drivers/vault/vX.Y.Z`:

```shell
go get github.com/brian-nunez/bkit/settings/drivers/vault@vX.Y.Z
```
