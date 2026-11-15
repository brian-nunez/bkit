// Package valkey provides a stash store on a Valkey server, through
// valkey-go, Valkey's official Go client.
//
// Pass [Driver] to stash.Open to open valkey:// URLs, and valkeys:// URLs
// over TLS, or call [New] with a [Config]:
//
//	s, err := stash.Open(ctx, "valkey://cache.internal:6379/0", valkey.Driver())
//	s, err := valkey.New(ctx, valkey.Config{Addr: "cache.internal:6379"})
//
// Both verify the server with PING. URL query parameters are valkey-go
// options, such as dial_timeout. Client-side caching is turned off, because
// stash reads every value from the server.
//
// Each stash call is one Valkey command: SET with NX for IfNotExists and PX
// for a TTL, INCRBY for Incr, PTTL, PEXPIRE, and PERSIST, so expiry is
// enforced by the server with millisecond precision. Keys and Clear use SCAN
// with glob characters escaped, so prefixes match literally.
//
// URL parse errors never include the URL. valkey-go types stay inside the
// driver. A store is safe for concurrent use.
package valkey
