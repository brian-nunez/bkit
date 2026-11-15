package valkey

import (
	"context"
	"crypto/tls"
	"fmt"
	"iter"
	"net/url"
	"strconv"
	"strings"
	"time"

	vk "github.com/valkey-io/valkey-go"

	"github.com/brian-nunez/bkit/stash"
)

const (
	// scanCount is the COUNT hint Keys and DeletePrefix pass to SCAN.
	scanCount = 500

	// defaultAddr is the server New connects to without Config.Addr.
	defaultAddr = "localhost:6379"
)

// Config describes a Valkey server for New.
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

// Driver returns a stash.Option that lets stash.Open open valkey:// URLs, and
// valkeys:// URLs over TLS.
func Driver() stash.Option {
	return stash.WithDriver(valkeyDriver{})
}

// New connects to the server cfg describes and verifies it with PING.
func New(ctx context.Context, cfg Config) (*stash.Store, error) {
	addr := cfg.Addr
	if addr == "" {
		addr = defaultAddr
	}
	b, err := connect(ctx, vk.ClientOption{
		InitAddress: []string{addr},
		Username:    cfg.Username,
		Password:    cfg.Password,
		SelectDB:    cfg.DB,
		TLSConfig:   cfg.TLS,
	})
	if err != nil {
		return nil, &stash.Error{Op: "open", Err: err}
	}
	return stash.New(b), nil
}

// valkeyDriver implements stash.Driver.
type valkeyDriver struct{}

// Schemes returns the URL schemes the driver opens.
func (valkeyDriver) Schemes() []string {
	return []string{"valkey", "valkeys"}
}

// Open connects to the server u names, as valkey-go parses it, and verifies
// it with PING.
func (valkeyDriver) Open(ctx context.Context, u *url.URL) (stash.Backend, error) {
	opt, err := vk.ParseURL(u.String())
	if err != nil {
		// valkey-go parse errors can quote parts of the URL.
		return nil, fmt.Errorf("%w: valkey-go cannot parse the URL", stash.ErrInvalidURL)
	}
	return connect(ctx, opt)
}

// connect creates a client without client-side caching, which stash does not
// use, and pings the server.
func connect(ctx context.Context, opt vk.ClientOption) (*backend, error) {
	opt.DisableCache = true
	c, err := vk.NewClient(opt)
	if err != nil {
		return nil, err
	}
	b := &backend{c: c}
	if err := b.Ping(ctx); err != nil {
		c.Close()
		return nil, err
	}
	return b, nil
}

// backend implements stash.Backend over a Valkey client.
type backend struct {
	c vk.Client
}

// do runs one command.
func (b *backend) do(ctx context.Context, cmd vk.Completed) vk.ValkeyResult {
	return b.c.Do(ctx, cmd)
}

// Get returns the value of key.
func (b *backend) Get(ctx context.Context, key string) ([]byte, error) {
	v, err := b.do(ctx, b.c.B().Get().Key(key).Build()).AsBytes()
	if vk.IsValkeyNil(err) {
		return nil, stash.ErrNotFound
	}
	return v, err
}

// Set stores value under key with SET, adding NX for IfNotExists and PX for
// a TTL.
func (b *backend) Set(ctx context.Context, key string, value []byte, opts stash.SetOptions) error {
	args := []string{string(value)}
	if opts.IfNotExists {
		args = append(args, "NX")
	}
	if opts.TTL > 0 {
		args = append(args, "PX", strconv.FormatInt(max(opts.TTL.Milliseconds(), 1), 10))
	}
	err := b.do(ctx, b.c.B().Arbitrary("SET").Keys(key).Args(args...).Build()).Error()
	if vk.IsValkeyNil(err) {
		return stash.ErrExists // SET NX replies nil when the key exists
	}
	return err
}

// Delete removes key.
func (b *backend) Delete(ctx context.Context, key string) (bool, error) {
	n, err := b.do(ctx, b.c.B().Del().Key(key).Build()).AsInt64()
	return n > 0, err
}

// Exists reports whether key is set.
func (b *backend) Exists(ctx context.Context, key string) (bool, error) {
	n, err := b.do(ctx, b.c.B().Exists().Key(key).Build()).AsInt64()
	return n > 0, err
}

// Incr adds delta with INCRBY, which Valkey runs atomically and which keeps
// the key's expiry.
func (b *backend) Incr(ctx context.Context, key string, delta int64) (int64, error) {
	n, err := b.do(ctx, b.c.B().Incrby().Key(key).Increment(delta).Build()).AsInt64()
	if verr, ok := vk.IsValkeyErr(err); ok {
		if msg := verr.Error(); strings.Contains(msg, "not an integer") || strings.Contains(msg, "overflow") {
			return 0, fmt.Errorf("%w: %w", stash.ErrNotInteger, err)
		}
	}
	return n, err
}

// TTL returns the time left before key expires, from PTTL.
func (b *backend) TTL(ctx context.Context, key string) (time.Duration, error) {
	ms, err := b.do(ctx, b.c.B().Pttl().Key(key).Build()).AsInt64()
	switch {
	case err != nil:
		return 0, err
	case ms == -2:
		return 0, stash.ErrNotFound
	case ms == -1:
		return stash.NoExpiry, nil
	}
	return time.Duration(ms) * time.Millisecond, nil
}

// Expire sets the expiry of key with PEXPIRE, or removes it with PERSIST.
func (b *backend) Expire(ctx context.Context, key string, ttl time.Duration) error {
	if ttl > 0 {
		ms := max(ttl.Milliseconds(), 1)
		n, err := b.do(ctx, b.c.B().Pexpire().Key(key).Milliseconds(ms).Build()).AsInt64()
		if err == nil && n == 0 {
			err = stash.ErrNotFound
		}
		return err
	}
	// PERSIST returns 0 both for a missing key and for a key with no
	// expiry, so a 0 needs an existence check.
	n, err := b.do(ctx, b.c.B().Persist().Key(key).Build()).AsInt64()
	if err != nil || n == 1 {
		return err
	}
	found, err := b.Exists(ctx, key)
	if err == nil && !found {
		err = stash.ErrNotFound
	}
	return err
}

// scan returns one SCAN page of keys that start with prefix.
func (b *backend) scan(ctx context.Context, cursor uint64, prefix string) (vk.ScanEntry, error) {
	cmd := b.c.B().Scan().Cursor(cursor).Match(globPrefix(prefix)).Count(scanCount).Build()
	return b.do(ctx, cmd).AsScanEntry()
}

// Keys yields the keys that start with prefix, using SCAN.
func (b *backend) Keys(ctx context.Context, prefix string) iter.Seq2[string, error] {
	return func(yield func(string, error) bool) {
		var cursor uint64
		for {
			page, err := b.scan(ctx, cursor, prefix)
			if err != nil {
				yield("", err)
				return
			}
			for _, k := range page.Elements {
				if !yield(k, nil) {
					return
				}
			}
			if page.Cursor == 0 {
				return
			}
			cursor = page.Cursor
		}
	}
}

// DeletePrefix removes every key that starts with prefix, one SCAN page at
// a time.
func (b *backend) DeletePrefix(ctx context.Context, prefix string) error {
	var cursor uint64
	for {
		page, err := b.scan(ctx, cursor, prefix)
		if err != nil {
			return err
		}
		if len(page.Elements) > 0 {
			if err := b.do(ctx, b.c.B().Del().Key(page.Elements...).Build()).Error(); err != nil {
				return err
			}
		}
		if page.Cursor == 0 {
			return nil
		}
		cursor = page.Cursor
	}
}

// Ping sends PING.
func (b *backend) Ping(ctx context.Context) error {
	return b.do(ctx, b.c.B().Ping().Build()).Error()
}

// Close closes the client.
func (b *backend) Close() error {
	b.c.Close()
	return nil
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
