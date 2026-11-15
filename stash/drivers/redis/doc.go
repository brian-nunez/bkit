// Package redis provides a stash store on a Redis server, through go-redis.
//
// Pass [Driver] to stash.Open to open redis:// URLs, and rediss:// URLs over
// TLS, or call [New] with a [Config]:
//
//	s, err := stash.Open(ctx, "redis://cache.internal:6379/0", redis.Driver())
//	s, err := redis.New(ctx, redis.Config{Addr: "cache.internal:6379"})
//
// Both verify the server with PING. URL query parameters are go-redis
// options, such as dial_timeout and pool_size.
//
// Each stash call is one Redis command: SET, SET NX for IfNotExists, INCRBY
// for Incr, PTTL, PEXPIRE, and PERSIST, so expiry is enforced by the server
// with millisecond precision. Keys and Clear use SCAN with glob characters
// escaped, so prefixes match literally.
//
// URL parse errors never include the URL. go-redis types stay inside the
// driver. A store is safe for concurrent use.
package redis
