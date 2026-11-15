package redis

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"iter"
	"net/url"
	"strings"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/brian-nunez/bkit/stash"
)

// scanCount is the COUNT hint Keys and DeletePrefix pass to SCAN.
const scanCount = 500

// Config describes a Redis server for New.
type Config struct {
	// Addr is the server's host:port. Empty means localhost:6379.
	Addr string

	// Username and Password authenticate with the server.
	Username, Password string

	// DB is the database number.
	DB int

	// TLS enables TLS with this configuration. Nil means a plain
	// connection.
	TLS *tls.Config
}

// Driver returns a stash.Option that lets stash.Open open redis:// URLs, and
// rediss:// URLs over TLS.
func Driver() stash.Option {
	return stash.WithDriver(redisDriver{})
}

// New connects to the server cfg describes and verifies it with PING.
func New(ctx context.Context, cfg Config) (*stash.Store, error) {
	b, err := connect(ctx, &goredis.Options{
		Addr:      cfg.Addr,
		Username:  cfg.Username,
		Password:  cfg.Password,
		DB:        cfg.DB,
		TLSConfig: cfg.TLS,
	})
	if err != nil {
		return nil, &stash.Error{Op: "open", Err: err}
	}
	return stash.New(b), nil
}

// redisDriver implements stash.Driver.
type redisDriver struct{}

// Schemes returns the URL schemes the driver opens.
func (redisDriver) Schemes() []string {
	return []string{"redis", "rediss"}
}

// Open connects to the server u names, as go-redis parses it, and verifies
// it with PING.
func (redisDriver) Open(ctx context.Context, u *url.URL) (stash.Backend, error) {
	opts, err := goredis.ParseURL(u.String())
	if err != nil {
		// go-redis parse errors can quote parts of the URL.
		return nil, fmt.Errorf("%w: go-redis cannot parse the URL", stash.ErrInvalidURL)
	}
	return connect(ctx, opts)
}

// client is the part of the go-redis client the driver uses. Tests replace
// it with an in-memory fake.
type client interface {
	Get(ctx context.Context, key string) *goredis.StringCmd
	Set(ctx context.Context, key string, value any, expiration time.Duration) *goredis.StatusCmd
	SetNX(ctx context.Context, key string, value any, expiration time.Duration) *goredis.BoolCmd
	Del(ctx context.Context, keys ...string) *goredis.IntCmd
	Exists(ctx context.Context, keys ...string) *goredis.IntCmd
	IncrBy(ctx context.Context, key string, value int64) *goredis.IntCmd
	PTTL(ctx context.Context, key string) *goredis.DurationCmd
	PExpire(ctx context.Context, key string, expiration time.Duration) *goredis.BoolCmd
	Persist(ctx context.Context, key string) *goredis.BoolCmd
	Scan(ctx context.Context, cursor uint64, match string, count int64) *goredis.ScanCmd
	Ping(ctx context.Context) *goredis.StatusCmd
	Close() error
}

// newClient creates the client for opts. Tests replace it.
var newClient = func(opts *goredis.Options) client {
	return goredis.NewClient(opts)
}

// connect creates a client and pings the server.
func connect(ctx context.Context, opts *goredis.Options) (*backend, error) {
	c := newClient(opts)
	if err := c.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return &backend{c: c}, nil
}

// backend implements stash.Backend over a Redis client.
type backend struct {
	c client
}

// Get returns the value of key.
func (b *backend) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := b.c.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, stash.ErrNotFound
	}
	return v, err
}

// Set stores value under key with SET, or SET NX for IfNotExists.
func (b *backend) Set(ctx context.Context, key string, value []byte, opts stash.SetOptions) error {
	if !opts.IfNotExists {
		return b.c.Set(ctx, key, value, opts.TTL).Err()
	}
	ok, err := b.c.SetNX(ctx, key, value, opts.TTL).Result()
	if err == nil && !ok {
		err = stash.ErrExists
	}
	return err
}

// Delete removes key.
func (b *backend) Delete(ctx context.Context, key string) (bool, error) {
	n, err := b.c.Del(ctx, key).Result()
	return n > 0, err
}

// Exists reports whether key is set.
func (b *backend) Exists(ctx context.Context, key string) (bool, error) {
	n, err := b.c.Exists(ctx, key).Result()
	return n > 0, err
}

// Incr adds delta with INCRBY, which Redis runs atomically and which keeps
// the key's expiry.
func (b *backend) Incr(ctx context.Context, key string, delta int64) (int64, error) {
	n, err := b.c.IncrBy(ctx, key, delta).Result()
	var rerr goredis.Error
	if errors.As(err, &rerr) && (strings.Contains(rerr.Error(), "not an integer") || strings.Contains(rerr.Error(), "overflow")) {
		return 0, fmt.Errorf("%w: %w", stash.ErrNotInteger, err)
	}
	return n, err
}

// TTL returns the time left before key expires, from PTTL.
func (b *backend) TTL(ctx context.Context, key string) (time.Duration, error) {
	d, err := b.c.PTTL(ctx, key).Result()
	switch {
	case err != nil:
		return 0, err
	case d == -2:
		return 0, stash.ErrNotFound
	case d == -1:
		return stash.NoExpiry, nil
	}
	return d, nil
}

// Expire sets the expiry of key with PEXPIRE, or removes it with PERSIST.
func (b *backend) Expire(ctx context.Context, key string, ttl time.Duration) error {
	if ttl > 0 {
		ok, err := b.c.PExpire(ctx, key, ttl).Result()
		if err == nil && !ok {
			err = stash.ErrNotFound
		}
		return err
	}
	// PERSIST returns false both for a missing key and for a key with no
	// expiry, so a false result needs an existence check.
	ok, err := b.c.Persist(ctx, key).Result()
	if err != nil || ok {
		return err
	}
	found, err := b.Exists(ctx, key)
	if err == nil && !found {
		err = stash.ErrNotFound
	}
	return err
}

// Keys yields the keys that start with prefix, using SCAN.
func (b *backend) Keys(ctx context.Context, prefix string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		var cursor uint64
		for {
			keys, next, err := b.c.Scan(ctx, cursor, globPrefix(prefix), scanCount).Result()
			if err != nil {
				yield("", err)
				return
			}
			for _, k := range keys {
				if !yield(k, nil) {
					return
				}
			}
			if next == 0 {
				return
			}
			cursor = next
		}
	}
}

// DeletePrefix removes every key that starts with prefix, one SCAN page at
// a time.
func (b *backend) DeletePrefix(ctx context.Context, prefix string) error {
	var cursor uint64
	for {
		keys, next, err := b.c.Scan(ctx, cursor, globPrefix(prefix), scanCount).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := b.c.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		if next == 0 {
			return nil
		}
		cursor = next
	}
}

// Ping sends PING.
func (b *backend) Ping(ctx context.Context) error {
	return b.c.Ping(ctx).Err()
}

// Close closes the client.
func (b *backend) Close() error {
	return b.c.Close()
}

// globPrefix returns a SCAN MATCH pattern for keys that start with prefix.
// Glob characters in prefix are escaped so they match literally.
func globPrefix(prefix string) string {
	var sb strings.Builder
	for _, r := range prefix {
		switch r {
		case '\\', '*', '?', '[', ']':
			sb.WriteByte('\\')
		}
		sb.WriteRune(r)
	}
	sb.WriteByte('*')
	return sb.String()
}
