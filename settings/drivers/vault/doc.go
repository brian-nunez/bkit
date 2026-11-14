// Package vault provides settings sources that read secrets from HashiCorp
// Vault's key/value engines over its HTTP API.
//
// Create a [Client] with [New] for one Vault server, then create one source
// per secret:
//
//	client, err := vault.New("https://vault.example.com:8200", vault.WithToken(token))
//	cfg, err := settings.Load(ctx, client.KV2("secret", "apps/web"))
//
// [Client.KV2] reads from a KV version 2 mount and [Client.KV1] from a KV
// version 1 mount. The secret's data becomes the source's tree, and settings
// lowercases its keys and merges it with other sources.
//
// Authentication is a token passed with [WithToken]. The caller obtains and
// renews it. A file rendered by Vault Agent needs no driver: read it with
// settings.File.
//
// Every request honors the context passed to Load. Errors match
// [ErrNotFound], [ErrPermission], [ErrResponse], or the context's error with
// errors.Is. Error messages never include the token.
//
// A Client is immutable after New and safe for concurrent use. Its zero
// value is not usable.
package vault
