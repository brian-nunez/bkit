// Package consul provides settings sources that read from the HashiCorp
// Consul key/value store over its HTTP API.
//
// Create a [Client] with [New] for one Consul agent, then create sources:
//
//	client, err := consul.New("http://localhost:8500", consul.WithToken(token))
//	cfg, err := settings.Load(ctx,
//		client.Key("apps/web/config.yaml", yaml.Format),
//		client.Prefix("apps/web/overrides"),
//	)
//
// [Client.Key] reads one key that holds a whole document and parses it with
// any settings.Format. [Client.Prefix] reads every key under a prefix and
// turns the rest of each key's path into a nested key, so
// apps/web/overrides/db/host becomes db.host. Prefix values are strings.
// settings lowercases keys, converts values when they are read, and merges
// the result with other sources.
//
// While a token is set, a redirect to another host or from https to http is
// not followed, so the token stays on the configured agent, and the
// redirect response is reported as [ErrResponse].
//
// Every request honors the context passed to Load. Errors match
// [ErrNotFound], [ErrPermission], [ErrResponse], settings.ErrParse, or the
// context's error with errors.Is. Error messages never include the token.
//
// A Client is immutable after New and safe for concurrent use. Its zero
// value is not usable.
package consul
